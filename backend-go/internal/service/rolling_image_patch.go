package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"deploy-platform/backend-go/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func (p *Pipeline) approvedWorkloadUID(log DeployLog) (string, error) {
	run, err := p.store.DeliveryRunForDeployLog(log.ID)
	if errors.Is(err, ErrNotFound) {
		return "", nil // Legacy deploys and explicit rollback have no plan snapshot.
	}
	if err != nil {
		return "", err
	}
	if run.Strategy != "rolling" || run.DeliveryPlanID <= 0 {
		return "", NewValidationError("镜像任务与滚动发布计划不一致")
	}
	plan, err := p.store.FindDeliveryPlan(run.DeliveryPlanID)
	if err != nil {
		return "", err
	}
	target := plan.Snapshot.Target
	if target.WorkloadUID == "" || target.ClusterID != log.ClusterID || target.Namespace != log.Namespace || target.Deployment != log.DeploymentName || target.Container != log.ContainerName {
		return "", newConflict("镜像任务与审批的 Deployment 目标不一致")
	}
	return target.WorkloadUID, nil
}

func actionCurrentImage(ctx context.Context, runtime KubernetesRuntime, log DeployLog, approvedUID string) (string, error) {
	if approvedUID != "" {
		return approvedDeploymentCurrentImage(ctx, runtime, log.Namespace, log.DeploymentName, log.ContainerName, approvedUID)
	}
	return runtime.CurrentImage(ctx, log.Namespace, log.DeploymentName, log.ContainerName)
}

// patchApprovedDeploymentImage keeps the approved workload identity and the
// image read immediately before journaling in the same apiserver operation.
// A re-created workload or concurrent image edit fails the JSON Patch tests.
func patchApprovedDeploymentImage(ctx context.Context, runtime KubernetesRuntime, namespace, deployment, container, previousImage, targetImage, approvedUID string) error {
	def, err := kube.LookupResource("apps", "v1", "deployments")
	if err != nil {
		return err
	}
	obj, err := runtime.GetResource(ctx, def, namespace, deployment, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("读取审批目标 Deployment 失败: %w", err)
	}
	patch, err := approvedDeploymentImagePatch(obj, namespace, deployment, container, previousImage, targetImage, approvedUID)
	if err != nil {
		return err
	}
	_, err = runtime.PatchResource(ctx, def, namespace, deployment, types.JSONPatchType, patch, metav1.PatchOptions{FieldManager: "deploy-platform-rolling"})
	return err
}

func approvedDeploymentCurrentImage(ctx context.Context, runtime KubernetesRuntime, namespace, deployment, container, approvedUID string) (string, error) {
	def, err := kube.LookupResource("apps", "v1", "deployments")
	if err != nil {
		return "", err
	}
	obj, err := runtime.GetResource(ctx, def, namespace, deployment, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if obj == nil || obj.GetNamespace() != namespace || obj.GetName() != deployment || string(obj.GetUID()) != approvedUID {
		return "", newConflict("审批后的 Deployment 身份已变化，不能确认镜像结果")
	}
	containers, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	if err != nil || !found {
		return "", NewValidationError("目标 Deployment 容器清单无法读取")
	}
	for _, raw := range containers {
		row, ok := raw.(map[string]any)
		if !ok {
			return "", NewValidationError("目标 Deployment 容器清单格式无效")
		}
		if row["name"] == container {
			image, ok := row["image"].(string)
			if !ok || image == "" {
				return "", NewValidationError("目标 Deployment 容器镜像无法读取")
			}
			return image, nil
		}
	}
	return "", NewValidationError("目标 Deployment 中不存在审批的容器")
}

func approvedDeploymentImagePatch(obj *unstructured.Unstructured, namespace, deployment, container, previousImage, targetImage, approvedUID string) ([]byte, error) {
	if obj == nil || namespace == "" || deployment == "" || container == "" || previousImage == "" || targetImage == "" || approvedUID == "" {
		return nil, NewValidationError("审批目标镜像 patch 输入不完整")
	}
	if obj.GetNamespace() != namespace || obj.GetName() != deployment || string(obj.GetUID()) != approvedUID || obj.GetResourceVersion() == "" {
		return nil, newConflict("审批后的 Deployment 身份或版本已变化，停止写入镜像")
	}
	containers, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	if err != nil || !found {
		return nil, NewValidationError("目标 Deployment 容器清单无法读取")
	}
	for i, raw := range containers {
		row, ok := raw.(map[string]any)
		if !ok {
			return nil, NewValidationError("目标 Deployment 容器清单格式无效")
		}
		if row["name"] != container {
			continue
		}
		if row["image"] != previousImage {
			return nil, newConflict("审批后目标容器镜像已被其他操作修改，停止覆盖")
		}
		path := fmt.Sprintf("/spec/template/spec/containers/%d", i)
		return json.Marshal([]map[string]any{
			{"op": "test", "path": "/metadata/uid", "value": approvedUID},
			{"op": "test", "path": "/metadata/resourceVersion", "value": obj.GetResourceVersion()},
			{"op": "test", "path": path + "/name", "value": container},
			{"op": "test", "path": path + "/image", "value": previousImage},
			{"op": "replace", "path": path + "/image", "value": targetImage},
		})
	}
	return nil, NewValidationError("目标 Deployment 中不存在审批的容器")
}
