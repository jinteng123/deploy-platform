package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"deploy-platform/backend-go/internal/config"
	"deploy-platform/backend-go/internal/kube"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

const (
	blueGreenStableLabelAction = "blue-green:stable-label"
	blueGreenStableEntryAction = "blue-green:stable-entry"
	blueGreenCandidateDeploy   = "blue-green:candidate-deployment"
	blueGreenCandidateService  = "blue-green:candidate-service"
	blueGreenPromotedPreview   = "blue-green:promotion:preview-service"
	blueGreenCutoverAction     = "blue-green:entry:candidate"
	blueGreenRestoreAction     = "blue-green:entry:stable"
	blueGreenPromoteAction     = "blue-green:entry:active-stable"
)

type blueGreenCandidateIdentity struct {
	PlanID                 int64             `json:"plan_id"`
	ExecutionID            int64             `json:"execution_id"`
	StableDeployment       string            `json:"stable_deployment"`
	StableDeploymentUID    string            `json:"stable_deployment_uid"`
	StableService          string            `json:"stable_service"`
	StableServiceUID       string            `json:"stable_service_uid"`
	StableServiceSelector  map[string]string `json:"stable_service_selector"`
	CandidateDeployment    string            `json:"candidate_deployment"`
	CandidateDeploymentUID string            `json:"candidate_deployment_uid"`
	CandidateService       string            `json:"candidate_service"`
	CandidateServiceUID    string            `json:"candidate_service_uid"`
	CandidateImage         string            `json:"candidate_image"`
	StableEndpointCount    int               `json:"stable_ready_endpoint_count"`
	CandidateEndpointCount int               `json:"candidate_ready_endpoint_count"`
	CandidateImageEvidence StepResult        `json:"-"`
}

type blueGreenActionIntent struct {
	PlanID           int64             `json:"plan_id"`
	ExecutionID      int64             `json:"execution_id"`
	Namespace        string            `json:"namespace"`
	Kind             string            `json:"kind"`
	Name             string            `json:"name"`
	UID              string            `json:"uid,omitempty"`
	Selector         map[string]string `json:"selector,omitempty"`
	PreviousSelector map[string]string `json:"previous_selector,omitempty"`
	ConfigHash       string            `json:"config_hash,omitempty"`
	Image            string            `json:"image,omitempty"`
	SpecHash         string            `json:"spec_hash,omitempty"`
}

type blueGreenActionObservation struct {
	Name            string            `json:"name"`
	UID             string            `json:"uid,omitempty"`
	ResourceVersion string            `json:"resource_version,omitempty"`
	Selector        map[string]string `json:"selector,omitempty"`
	Image           string            `json:"image,omitempty"`
	EndpointCount   int               `json:"ready_endpoint_count,omitempty"`
}

// prepareBlueGreenCandidate establishes a stable-only Service selector,
// verifies the resulting EndpointSlices, then creates one isolated candidate
// Deployment and private preview Service. Every Kubernetes mutation has a
// durable intent, deterministic identity, and readback before it is accepted.
// Approved execution calls this helper before stopping at the operator's
// candidate-review gate; reconcile reuses its journal instead of replaying
// already confirmed writes.
func prepareBlueGreenCandidate(ctx context.Context, store *Store, runtime KubernetesRuntime, log DeployLog, plan DeliveryPlan, executionID int64) (blueGreenCandidateIdentity, error) {
	return prepareBlueGreenCandidateWithConfig(ctx, store, runtime, log, plan, executionID, config.Config{})
}

func prepareBlueGreenCandidateWithConfig(ctx context.Context, store *Store, runtime KubernetesRuntime, log DeployLog, plan DeliveryPlan, executionID int64, cfg config.Config) (blueGreenCandidateIdentity, error) {
	if store == nil || runtime == nil || log.ID <= 0 || executionID <= 0 || plan.ID <= 0 || plan.Strategy != "blue_green" {
		return blueGreenCandidateIdentity{}, NewValidationError("蓝绿执行上下文不完整")
	}
	target := plan.Snapshot.Target
	if target.ClusterID != log.ClusterID || target.AppID <= 0 || target.Namespace != log.Namespace || target.Deployment != log.DeploymentName || target.Container != log.ContainerName || plan.ReleaseID != log.ReleaseID || log.ImageFull == nil || *log.ImageFull != plan.Snapshot.Artifact.Image {
		return blueGreenCandidateIdentity{}, NewValidationError("发布任务与蓝绿计划目标不一致")
	}
	if target.WorkloadUID == "" || target.ServiceUID == "" || target.ServiceName == "" || target.ServiceConfigHash == "" || target.StableLabelKey == "" || target.CandidateLabelKey == "" || target.CandidateLabelValue == "" {
		return blueGreenCandidateIdentity{}, NewValidationError("蓝绿计划缺少资源身份或隔离基线")
	}

	deploymentDef, err := kube.LookupResource("apps", "v1", "deployments")
	if err != nil {
		return blueGreenCandidateIdentity{}, err
	}
	serviceDef, err := kube.LookupResource("", "v1", "services")
	if err != nil {
		return blueGreenCandidateIdentity{}, err
	}
	deployment, err := runtime.GetResource(ctx, deploymentDef, target.Namespace, target.Deployment, metav1.GetOptions{})
	if err != nil {
		return blueGreenCandidateIdentity{}, fmt.Errorf("读取稳定 Deployment 失败: %w", err)
	}
	service, err := runtime.GetResource(ctx, serviceDef, target.Namespace, target.ServiceName, metav1.GetOptions{})
	if err != nil {
		return blueGreenCandidateIdentity{}, fmt.Errorf("读取稳定 Service 失败: %w", err)
	}
	if err := validateBlueGreenLiveBaseline(plan, deployment, service); err != nil {
		return blueGreenCandidateIdentity{}, err
	}
	if plan.Snapshot.SchemaVersion >= 7 {
		if err := validateDeliveryConfigurationReferencesAgainstSnapshot(ctx, runtime, plan, deployment); err != nil {
			return blueGreenCandidateIdentity{}, err
		}
	}

	if err := ensureBlueGreenStableLabel(ctx, store, runtime, log.ID, executionID, plan, deploymentDef, deployment); err != nil {
		return blueGreenCandidateIdentity{}, err
	}
	rolloutCtx, cancelRollout := context.WithTimeout(ctx, RolloutWaitTimeout)
	_, rolloutErr := runtime.RolloutStatus(rolloutCtx, target.Namespace, target.Deployment)
	cancelRollout()
	if rolloutErr != nil {
		return blueGreenCandidateIdentity{}, fmt.Errorf("等待稳定版本标签 rollout 失败: %w", rolloutErr)
	}
	deployment, err = runtime.GetResource(ctx, deploymentDef, target.Namespace, target.Deployment, metav1.GetOptions{})
	if err != nil {
		return blueGreenCandidateIdentity{}, fmt.Errorf("复核稳定 Deployment 失败: %w", err)
	}
	if err := validateStableDeploymentReady(deployment, target); err != nil {
		return blueGreenCandidateIdentity{}, err
	}

	service, err = runtime.GetResource(ctx, serviceDef, target.Namespace, target.ServiceName, metav1.GetOptions{})
	if err != nil {
		return blueGreenCandidateIdentity{}, fmt.Errorf("复核稳定 Service 失败: %w", err)
	}
	stableSelector, err := BlueGreenStableServiceSelector(target.ServiceSelector, target.StableLabelKey)
	if err != nil {
		return blueGreenCandidateIdentity{}, err
	}
	if err := ensureBlueGreenStableServiceEntry(ctx, store, runtime, log.ID, executionID, plan, serviceDef, service, stableSelector); err != nil {
		return blueGreenCandidateIdentity{}, err
	}
	endpointCount, err := waitForBlueGreenStableEndpoints(ctx, runtime, target.Namespace, target.ServiceName, target.StableLabelKey)
	if err != nil {
		return blueGreenCandidateIdentity{}, err
	}

	service, err = runtime.GetResource(ctx, serviceDef, target.Namespace, target.ServiceName, metav1.GetOptions{})
	if err != nil {
		return blueGreenCandidateIdentity{}, fmt.Errorf("创建候选前复核 Service 失败: %w", err)
	}
	resources, err := BuildBlueGreenCandidateResources(plan, executionID, deployment, service)
	if err != nil {
		return blueGreenCandidateIdentity{}, err
	}
	candidateLabels, found, err := unstructured.NestedStringMap(resources.Deployment.Object, "spec", "template", "metadata", "labels")
	if err != nil || !found {
		return blueGreenCandidateIdentity{}, NewValidationError("候选 Deployment 缺少 Pod 标签")
	}
	if err := validateBlueGreenCandidateIsolation(ctx, runtime, target.Namespace, candidateLabels, store, log.ID, &resources); err != nil {
		return blueGreenCandidateIdentity{}, err
	}
	createdDeployment, err := ensureBlueGreenCandidateObject(ctx, store, runtime, log.ID, plan, executionID, deploymentDef, resources.Deployment, blueGreenCandidateDeploy, "Deployment")
	if err != nil {
		return blueGreenCandidateIdentity{}, err
	}
	createdService, err := ensureBlueGreenCandidateObject(ctx, store, runtime, log.ID, plan, executionID, serviceDef, resources.Service, blueGreenCandidateService, "Service")
	if err != nil {
		return blueGreenCandidateIdentity{}, err
	}
	candidateEndpointCount, err := waitForBlueGreenCandidateReady(ctx, runtime, plan, executionID, deploymentDef, serviceDef, resources.Deployment, resources.Service, createdDeployment, createdService)
	if err != nil {
		return blueGreenCandidateIdentity{}, err
	}
	candidateImageEvidence := observeBlueGreenCandidateImage(ctx, runtime, plan, createdDeployment.GetName(), cfg)
	return blueGreenCandidateIdentity{
		PlanID: plan.ID, ExecutionID: executionID,
		StableDeployment: target.Deployment, StableDeploymentUID: target.WorkloadUID,
		StableService: target.ServiceName, StableServiceUID: target.ServiceUID,
		StableServiceSelector: stableSelector,
		CandidateDeployment:   createdDeployment.GetName(), CandidateDeploymentUID: string(createdDeployment.GetUID()),
		CandidateService: createdService.GetName(), CandidateServiceUID: string(createdService.GetUID()),
		CandidateImage:      plan.Snapshot.Artifact.Image,
		StableEndpointCount: endpointCount, CandidateEndpointCount: candidateEndpointCount,
		CandidateImageEvidence: candidateImageEvidence,
	}, nil
}

func observeBlueGreenCandidateImage(ctx context.Context, runtime KubernetesRuntime, plan DeliveryPlan, deploymentName string, cfg config.Config) StepResult {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	status, detail := observePodImageDigestWithRegistry(ctx, runtime, plan.Snapshot.Target.Namespace, deploymentName, plan.Snapshot.Target.Container, plan.Snapshot.Artifact.Image, cfg)
	return StepResult{
		Step: "verify_candidate_pod_image", Success: true, ObservationStatus: status,
		Stdout: tailBytes(detail, 2000),
	}
}

func appendCandidateImageEvidence(log *DeployLog, evidence StepResult) {
	if log == nil || evidence.Step == "" {
		return
	}
	var steps []StepResult
	if err := json.Unmarshal([]byte(log.StepDetail), &steps); err != nil {
		steps = nil
	}
	filtered := steps[:0]
	for _, step := range steps {
		if step.Step != evidence.Step {
			filtered = append(filtered, step)
		}
	}
	log.StepDetail = marshalSteps(append(filtered, evidence))
}

func waitForBlueGreenCandidateReady(ctx context.Context, runtime KubernetesRuntime, plan DeliveryPlan, executionID int64, deploymentDef, serviceDef kube.ResourceDef, desiredDeployment, desiredService, createdDeployment, createdService *unstructured.Unstructured) (int, error) {
	target := plan.Snapshot.Target
	rolloutCtx, cancelRollout := context.WithTimeout(ctx, RolloutWaitTimeout)
	_, rolloutErr := runtime.RolloutStatus(rolloutCtx, target.Namespace, desiredDeployment.GetName())
	cancelRollout()
	currentDeployment, readErr := runtime.GetResource(ctx, deploymentDef, target.Namespace, desiredDeployment.GetName(), metav1.GetOptions{})
	if readErr != nil {
		return 0, fmt.Errorf("候选 Deployment rollout 后回读失败: %w", readErr)
	}
	if rolloutErr != nil {
		return 0, fmt.Errorf("等待候选 Deployment rollout 失败: %w", rolloutErr)
	}
	if err := validateBlueGreenCandidateDeploymentReady(currentDeployment, desiredDeployment, createdDeployment, plan.ID, executionID, target.Container, plan.Snapshot.Artifact.Image); err != nil {
		return 0, err
	}
	currentService, err := runtime.GetResource(ctx, serviceDef, target.Namespace, desiredService.GetName(), metav1.GetOptions{})
	if err != nil {
		return 0, fmt.Errorf("候选预览 Service 回读失败: %w", err)
	}
	if err := validateBlueGreenCandidateObject(currentService, desiredService, plan.ID, executionID, "Service", target.Container, plan.Snapshot.Artifact.Image); err != nil {
		return 0, err
	}
	if currentService.GetUID() != createdService.GetUID() {
		return 0, newConflict("候选预览 Service UID 已变化")
	}
	return waitForBlueGreenCandidateEndpoints(ctx, runtime, target.Namespace, desiredService.GetName(), target.CandidateLabelKey, target.CandidateLabelValue)
}

func validateBlueGreenCandidateDeploymentReady(current, desired, created *unstructured.Unstructured, planID, executionID int64, container, image string) error {
	if current == nil || desired == nil || created == nil || current.GetUID() == "" || current.GetUID() != created.GetUID() {
		return newConflict("候选 Deployment UID 已变化，停止发布")
	}
	if err := validateBlueGreenCandidateObject(current, desired, planID, executionID, "Deployment", container, image); err != nil {
		return err
	}
	desiredReplicas, found, err := unstructured.NestedInt64(desired.Object, "spec", "replicas")
	if err != nil || !found || desiredReplicas < 1 {
		return NewValidationError("候选 Deployment 副本数无效")
	}
	generation := current.GetGeneration()
	observedGeneration, observedFound, observedErr := unstructured.NestedInt64(current.Object, "status", "observedGeneration")
	ready, readyFound, readyErr := unstructured.NestedInt64(current.Object, "status", "readyReplicas")
	updated, updatedFound, updatedErr := unstructured.NestedInt64(current.Object, "status", "updatedReplicas")
	available, availableFound, availableErr := unstructured.NestedInt64(current.Object, "status", "availableReplicas")
	if observedErr != nil || readyErr != nil || updatedErr != nil || availableErr != nil || !observedFound || !readyFound || !updatedFound || !availableFound || observedGeneration < generation || ready < desiredReplicas || updated < desiredReplicas || available < desiredReplicas {
		return newConflict("候选 Deployment 尚未达到全部副本就绪")
	}
	return nil
}

func validateBlueGreenLiveBaseline(plan DeliveryPlan, deployment, service *unstructured.Unstructured) error {
	target := plan.Snapshot.Target
	if deployment == nil || deployment.GetNamespace() != target.Namespace || deployment.GetName() != target.Deployment || string(deployment.GetUID()) != target.WorkloadUID {
		return newConflict("稳定 Deployment UID 已变化，停止蓝绿执行")
	}
	if service == nil || service.GetNamespace() != target.Namespace || service.GetName() != target.ServiceName || string(service.GetUID()) != target.ServiceUID {
		return newConflict("稳定 Service UID 已变化，停止蓝绿执行")
	}
	serviceHash, err := serviceConfigurationHash(service)
	if err != nil || serviceHash != target.ServiceConfigHash {
		return newConflict("稳定 Service 端口或配置摘要已变化，停止蓝绿执行")
	}
	if targetContainerImage(deployment, target.Container) != target.CurrentImage {
		return newConflict("稳定 Deployment 镜像已变化，停止蓝绿执行")
	}
	labelsMap, found, err := unstructured.NestedStringMap(deployment.Object, "spec", "template", "metadata", "labels")
	if err != nil || !found {
		return NewValidationError("稳定 Deployment Pod 标签无法读取")
	}
	if current, exists := labelsMap[target.StableLabelKey]; exists && current != "stable" {
		return newConflict("稳定标签键已被外部占用")
	}
	if target.StableLabelAlreadyPresent || labelsMap[target.StableLabelKey] == "stable" {
		workloadHash, err := blueGreenWorkloadHash(deployment, target)
		if err != nil || workloadHash != target.WorkloadHash {
			return newConflict("稳定 Deployment 配置摘要已变化，停止蓝绿执行")
		}
	} else {
		workloadHash, err := workloadHash(deployment, target.Container, false)
		if err != nil || workloadHash != target.WorkloadHash {
			return newConflict("稳定 Deployment 配置摘要已变化，停止蓝绿执行")
		}
	}
	selector, found, err := unstructured.NestedStringMap(service.Object, "spec", "selector")
	if err != nil || !found {
		return NewValidationError("稳定 Service selector 无法读取")
	}
	stableSelector, err := BlueGreenStableServiceSelector(target.ServiceSelector, target.StableLabelKey)
	if err != nil {
		return err
	}
	candidateSelector, err := BlueGreenCandidateSelector(target.CandidateLabelKey, target.CandidateLabelValue)
	if err != nil {
		return err
	}
	if !sameStringMap(selector, target.ServiceSelector) && !sameStringMap(selector, stableSelector) && !sameStringMap(selector, candidateSelector) {
		return newConflict("稳定 Service selector 已偏离计划基线，停止蓝绿执行")
	}
	return nil
}

func validateStableDeploymentReady(deployment *unstructured.Unstructured, target DeliveryPlanTarget) error {
	if deployment == nil || string(deployment.GetUID()) != target.WorkloadUID {
		return newConflict("稳定 Deployment UID 已变化，不能确认稳定版本")
	}
	labelsMap, found, err := unstructured.NestedStringMap(deployment.Object, "spec", "template", "metadata", "labels")
	if err != nil || !found || labelsMap[target.StableLabelKey] != "stable" {
		return newConflict("稳定版本标签尚未回读确认")
	}
	image := targetContainerImage(deployment, target.Container)
	if image != target.CurrentImage {
		return newConflict("稳定 Deployment 镜像在标签 rollout 期间发生变化")
	}
	workloadHash, err := blueGreenWorkloadHash(deployment, target)
	if err != nil || workloadHash != target.WorkloadHash {
		return newConflict("稳定 Deployment 配置在标签 rollout 期间发生变化")
	}
	ready, found, err := unstructured.NestedInt64(deployment.Object, "status", "readyReplicas")
	if err != nil || !found || ready < target.Replicas {
		return newConflict("稳定 Deployment 就绪副本不足，不能调整 Service selector")
	}
	return nil
}

func ensureBlueGreenStableLabel(ctx context.Context, store *Store, runtime KubernetesRuntime, logID, executionID int64, plan DeliveryPlan, def kube.ResourceDef, deployment *unstructured.Unstructured) error {
	target := plan.Snapshot.Target
	intent := blueGreenActionIntent{PlanID: plan.ID, ExecutionID: executionID, Namespace: target.Namespace, Kind: "Deployment", Name: target.Deployment, UID: target.WorkloadUID, Selector: map[string]string{target.StableLabelKey: "stable"}}
	if err := beginBlueGreenAction(store, logID, blueGreenStableLabelAction, "patch_stable_pod_label", intent); err != nil {
		return err
	}
	prior, err := findExecutionAction(store, logID, blueGreenStableLabelAction)
	if err != nil {
		return err
	}
	labelsMap, found, err := unstructured.NestedStringMap(deployment.Object, "spec", "template", "metadata", "labels")
	if err != nil || !found {
		return NewValidationError("稳定 Deployment Pod 标签无法读取")
	}
	if labelsMap[target.StableLabelKey] == "stable" {
		if prior != nil && prior.Status == ExecutionActionApplied {
			return validateCandidateActionObservedIdentity(prior, deployment)
		}
		return completeBlueGreenAction(store, logID, blueGreenStableLabelAction, ExecutionActionApplied, blueGreenActionObservation{
			Name: deployment.GetName(), UID: string(deployment.GetUID()), ResourceVersion: deployment.GetResourceVersion(),
		}, "stable_label_readback")
	}
	if prior != nil && prior.Status == ExecutionActionApplied {
		return newConflict("稳定标签动作曾确认完成，但当前 Deployment 已回退，停止自动覆盖")
	}
	patch, err := BlueGreenStableLabelPatch(deployment, target.StableLabelKey)
	if err != nil {
		return err
	}
	if string(patch) == "[]" {
		return NewValidationError("稳定标签状态不一致")
	}
	_, patchErr := runtime.PatchResource(ctx, def, target.Namespace, target.Deployment, types.JSONPatchType, patch, metav1.PatchOptions{FieldManager: "deploy-platform-blue-green"})
	readback, readErr := runtime.GetResource(ctx, def, target.Namespace, target.Deployment, metav1.GetOptions{})
	if readErr != nil {
		_ = completeBlueGreenAction(store, logID, blueGreenStableLabelAction, ExecutionActionUnknown, blueGreenActionObservation{Name: target.Deployment}, "stable_label_readback_failed")
		return fmt.Errorf("稳定标签写入结果未知，Deployment 回读失败: %w", readErr)
	}
	observedLabels, found, observedErr := unstructured.NestedStringMap(readback.Object, "spec", "template", "metadata", "labels")
	if string(readback.GetUID()) != target.WorkloadUID || observedErr != nil || !found {
		_ = completeBlueGreenAction(store, logID, blueGreenStableLabelAction, ExecutionActionUnknown, blueGreenActionObservation{Name: readback.GetName(), UID: string(readback.GetUID()), ResourceVersion: readback.GetResourceVersion()}, "stable_label_identity_changed")
		return newConflict("稳定标签写入结果未知，Deployment 身份或标签格式异常")
	}
	if observedLabels[target.StableLabelKey] != "stable" {
		status := ExecutionActionUnknown
		outcome := "stable_label_write_unknown"
		if patchErr != nil {
			status, outcome = ExecutionActionNotApplied, "stable_label_write_not_applied"
		}
		_ = completeBlueGreenAction(store, logID, blueGreenStableLabelAction, status, blueGreenActionObservation{Name: readback.GetName(), UID: string(readback.GetUID()), ResourceVersion: readback.GetResourceVersion()}, outcome)
		if patchErr != nil {
			return fmt.Errorf("稳定标签写入未生效: %w", patchErr)
		}
		return newConflict("稳定标签写入已响应，但回读值不符合计划")
	}
	if err := completeBlueGreenAction(store, logID, blueGreenStableLabelAction, ExecutionActionApplied, blueGreenActionObservation{
		Name: readback.GetName(), UID: string(readback.GetUID()), ResourceVersion: readback.GetResourceVersion(),
	}, "stable_label_confirmed"); err != nil {
		return fmt.Errorf("稳定标签已生效，但动作回读无法持久化: %w", err)
	}
	return nil
}

func ensureBlueGreenStableServiceEntry(ctx context.Context, store *Store, runtime KubernetesRuntime, logID, executionID int64, plan DeliveryPlan, def kube.ResourceDef, service *unstructured.Unstructured, desired map[string]string) error {
	target := plan.Snapshot.Target
	intent := blueGreenActionIntent{PlanID: plan.ID, ExecutionID: executionID, Namespace: target.Namespace, Kind: "Service", Name: target.ServiceName, UID: target.ServiceUID, Selector: desired}
	if err := beginBlueGreenAction(store, logID, blueGreenStableEntryAction, "set_stable_service_selector", intent); err != nil {
		return err
	}
	if service == nil || string(service.GetUID()) != target.ServiceUID {
		return newConflict("稳定 Service UID 已变化，停止 selector 更新")
	}
	configHash, err := serviceConfigurationHash(service)
	if err != nil || configHash != target.ServiceConfigHash {
		return newConflict("稳定 Service 配置已变化，停止 selector 更新")
	}
	prior, err := findExecutionAction(store, logID, blueGreenStableEntryAction)
	if err != nil {
		return err
	}
	selector, found, err := unstructured.NestedStringMap(service.Object, "spec", "selector")
	if err != nil || !found {
		return NewValidationError("稳定 Service selector 无法读取")
	}
	if sameStringMap(selector, desired) {
		if prior != nil && prior.Status == ExecutionActionApplied {
			var observed blueGreenActionObservation
			if len(prior.Observed) > 0 {
				if err := json.Unmarshal(prior.Observed, &observed); err != nil || (observed.UID != "" && observed.UID != string(service.GetUID())) || (len(observed.Selector) > 0 && !sameStringMap(observed.Selector, desired)) {
					return newConflict("稳定 Service selector 与已确认动作观察不一致")
				}
			}
			return nil
		}
		return completeBlueGreenAction(store, logID, blueGreenStableEntryAction, ExecutionActionApplied, blueGreenActionObservation{
			Name: service.GetName(), UID: string(service.GetUID()), ResourceVersion: service.GetResourceVersion(), Selector: selector,
		}, "stable_service_selector_confirmed")
	}
	if prior != nil && prior.Status == ExecutionActionApplied {
		return newConflict("稳定 Service selector 曾确认完成，但当前值已改变，停止自动覆盖")
	}
	patch, err := BlueGreenServiceSelectorPatch(service, target.ServiceSelector, desired)
	if err != nil {
		return err
	}
	_, patchErr := runtime.PatchResource(ctx, def, target.Namespace, target.ServiceName, types.JSONPatchType, patch, metav1.PatchOptions{FieldManager: "deploy-platform-blue-green"})
	readback, readErr := runtime.GetResource(ctx, def, target.Namespace, target.ServiceName, metav1.GetOptions{})
	if readErr != nil {
		_ = completeBlueGreenAction(store, logID, blueGreenStableEntryAction, ExecutionActionUnknown, blueGreenActionObservation{Name: target.ServiceName}, "stable_service_selector_readback_failed")
		return fmt.Errorf("Service selector 写入结果未知，回读失败: %w", readErr)
	}
	observed, found, observedErr := unstructured.NestedStringMap(readback.Object, "spec", "selector")
	if string(readback.GetUID()) != target.ServiceUID || observedErr != nil || !found || serviceConfigurationHashMustMatch(readback, target.ServiceConfigHash) != nil {
		_ = completeBlueGreenAction(store, logID, blueGreenStableEntryAction, ExecutionActionUnknown, blueGreenActionObservation{Name: readback.GetName(), UID: string(readback.GetUID()), ResourceVersion: readback.GetResourceVersion(), Selector: observed}, "stable_service_selector_identity_changed")
		return newConflict("Service selector 写入结果未知，Service 身份或配置已变化")
	}
	if !sameStringMap(observed, desired) {
		status := ExecutionActionUnknown
		outcome := "stable_service_selector_write_unknown"
		if patchErr != nil {
			status, outcome = ExecutionActionNotApplied, "stable_service_selector_write_not_applied"
		}
		_ = completeBlueGreenAction(store, logID, blueGreenStableEntryAction, status, blueGreenActionObservation{Name: readback.GetName(), UID: string(readback.GetUID()), ResourceVersion: readback.GetResourceVersion(), Selector: observed}, outcome)
		if patchErr != nil {
			return fmt.Errorf("稳定 Service selector 写入未生效: %w", patchErr)
		}
		return newConflict("Service selector 写入已响应，但回读值不符合计划")
	}
	if err := completeBlueGreenAction(store, logID, blueGreenStableEntryAction, ExecutionActionApplied, blueGreenActionObservation{
		Name: readback.GetName(), UID: string(readback.GetUID()), ResourceVersion: readback.GetResourceVersion(), Selector: observed,
	}, "stable_service_selector_confirmed"); err != nil {
		return fmt.Errorf("稳定 Service 已切到稳定标签，但动作回读无法持久化: %w", err)
	}
	return nil
}

func serviceConfigurationHashMustMatch(service *unstructured.Unstructured, expected string) error {
	hash, err := serviceConfigurationHash(service)
	if err != nil || hash != expected {
		return newConflict("Service 配置摘要已变化")
	}
	return nil
}

func waitForBlueGreenStableEndpoints(ctx context.Context, runtime KubernetesRuntime, namespace, serviceName, stableLabelKey string) (int, error) {
	return waitForBlueGreenTrackEndpoints(ctx, runtime, namespace, serviceName, stableLabelKey, "stable", "稳定")
}

func waitForBlueGreenCandidateEndpoints(ctx context.Context, runtime KubernetesRuntime, namespace, serviceName, candidateLabelKey, candidateLabelValue string) (int, error) {
	return waitForBlueGreenTrackEndpoints(ctx, runtime, namespace, serviceName, candidateLabelKey, candidateLabelValue, "候选")
}

// waitForBlueGreenCandidateEntryEndpoints allows old EndpointSlice entries to
// drain after a Service selector update. A wrong-track ready Pod keeps the
// observation pending; a lasting mismatch times out and retains the lock.
func waitForBlueGreenCandidateEntryEndpoints(ctx context.Context, runtime KubernetesRuntime, namespace, serviceName, candidateLabelKey, candidateLabelValue string) (int, error) {
	endpointDef, err := kube.LookupResource("discovery.k8s.io", "v1", "endpointslices")
	if err != nil {
		return 0, err
	}
	podDef, err := kube.LookupResource("", "v1", "pods")
	if err != nil {
		return 0, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, RolloutWaitTimeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		ready, count, err := blueGreenTrackEndpointsReadyInternal(waitCtx, runtime, endpointDef, podDef, namespace, serviceName, candidateLabelKey, candidateLabelValue, "候选", true)
		if err != nil {
			return 0, err
		}
		if ready {
			return count, nil
		}
		select {
		case <-waitCtx.Done():
			return 0, fmt.Errorf("等待生产入口 EndpointSlice 全部指向候选 Pod 超时: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func waitForBlueGreenTrackEndpoints(ctx context.Context, runtime KubernetesRuntime, namespace, serviceName, labelKey, labelValue, trackName string) (int, error) {
	endpointDef, err := kube.LookupResource("discovery.k8s.io", "v1", "endpointslices")
	if err != nil {
		return 0, err
	}
	podDef, err := kube.LookupResource("", "v1", "pods")
	if err != nil {
		return 0, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, RolloutWaitTimeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		ready, count, err := blueGreenTrackEndpointsReady(waitCtx, runtime, endpointDef, podDef, namespace, serviceName, labelKey, labelValue, trackName)
		if err != nil {
			return 0, err
		}
		if ready {
			return count, nil
		}
		select {
		case <-waitCtx.Done():
			return 0, fmt.Errorf("等待 Service EndpointSlice 全部指向%s Pod 超时: %w", trackName, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func blueGreenTrackEndpointsReady(ctx context.Context, runtime KubernetesRuntime, endpointDef, podDef kube.ResourceDef, namespace, serviceName, labelKey, labelValue, trackName string) (bool, int, error) {
	return blueGreenTrackEndpointsReadyInternal(ctx, runtime, endpointDef, podDef, namespace, serviceName, labelKey, labelValue, trackName, false)
}

func blueGreenTrackEndpointsReadyInternal(ctx context.Context, runtime KubernetesRuntime, endpointDef, podDef kube.ResourceDef, namespace, serviceName, labelKey, labelValue, trackName string, tolerateOldTrack bool) (bool, int, error) {
	selector := labels.Set(map[string]string{"kubernetes.io/service-name": serviceName}).AsSelector().String()
	slices, err := runtime.ListResources(ctx, endpointDef, namespace, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return false, 0, fmt.Errorf("读取%s Service EndpointSlice 失败: %w", trackName, err)
	}
	if slices == nil || len(slices.Items) == 0 {
		return false, 0, nil
	}
	pods, err := runtime.ListResources(ctx, podDef, namespace, metav1.ListOptions{})
	if err != nil {
		return false, 0, fmt.Errorf("读取%s Service 后端 Pod 失败: %w", trackName, err)
	}
	if pods == nil {
		return false, 0, fmt.Errorf("读取%s Service 后端 Pod 失败: 空响应", trackName)
	}
	byName := make(map[string]*unstructured.Unstructured, len(pods.Items))
	for i := range pods.Items {
		pod := &pods.Items[i]
		byName[pod.GetName()] = pod
	}
	readyCount := 0
	unknown := false
	oldTrack := false
	for i := range slices.Items {
		endpoints, found, err := unstructured.NestedSlice(slices.Items[i].Object, "endpoints")
		if err != nil {
			return false, 0, fmt.Errorf("EndpointSlice %s 格式无效", slices.Items[i].GetName())
		}
		if !found {
			unknown = true
			continue
		}
		for _, rawEndpoint := range endpoints {
			endpoint, ok := rawEndpoint.(map[string]any)
			if !ok {
				return false, 0, NewValidationError("EndpointSlice endpoint 格式无效")
			}
			isReady, readyFound, readyErr := unstructured.NestedBool(endpoint, "conditions", "ready")
			if readyErr != nil {
				return false, 0, NewValidationError("EndpointSlice ready 状态格式无效")
			}
			if !readyFound {
				unknown = true
				continue
			}
			if !isReady {
				continue
			}
			target, found, targetErr := unstructured.NestedMap(endpoint, "targetRef")
			if targetErr != nil || !found || target["kind"] != "Pod" {
				unknown = true
				continue
			}
			podName, _ := target["name"].(string)
			pod := byName[podName]
			if pod == nil {
				unknown = true
				continue
			}
			podLabels := pod.GetLabels()
			if podLabels[labelKey] != labelValue {
				if tolerateOldTrack {
					oldTrack = true
					continue
				}
				return false, 0, newConflict("%s Service 当前指向不符合轨道标签的 Pod %s，停止蓝绿执行", trackName, podName)
			}
			readyCount++
		}
	}
	return readyCount > 0 && !unknown && !oldTrack, readyCount, nil
}

func ensureBlueGreenCandidateObject(ctx context.Context, store *Store, runtime KubernetesRuntime, logID int64, plan DeliveryPlan, executionID int64, def kube.ResourceDef, desired *unstructured.Unstructured, actionKey, kind string) (*unstructured.Unstructured, error) {
	if desired == nil {
		return nil, NewValidationError("蓝绿候选资源为空")
	}
	target := plan.Snapshot.Target
	specHash, err := objectSpecHash(desired)
	if err != nil {
		return nil, err
	}
	intent := blueGreenActionIntent{PlanID: plan.ID, ExecutionID: executionID, Namespace: target.Namespace, Kind: kind, Name: desired.GetName(), Image: plan.Snapshot.Artifact.Image, SpecHash: specHash}
	prior, err := findExecutionAction(store, logID, actionKey)
	if err != nil {
		return nil, err
	}
	if prior != nil {
		var recorded blueGreenActionIntent
		if err := json.Unmarshal(prior.Intent, &recorded); err != nil || !sameBlueGreenActionIntent(recorded, intent) {
			return nil, newConflict("候选 %s 的持久化创建意图与当前计划不一致", kind)
		}
	}
	if err := beginBlueGreenAction(store, logID, actionKey, "create_candidate_"+kind, intent); err != nil {
		return nil, err
	}
	current, getErr := runtime.GetResource(ctx, def, target.Namespace, desired.GetName(), metav1.GetOptions{})
	if getErr == nil {
		if prior == nil {
			return nil, newConflict("候选 %s 名称已占用且无先前创建动作记录", kind)
		}
		if err := validateBlueGreenCandidateObject(current, desired, plan.ID, executionID, kind, plan.Snapshot.Target.Container, plan.Snapshot.Artifact.Image); err != nil {
			_ = completeBlueGreenAction(store, logID, actionKey, ExecutionActionUnknown, blueGreenActionObservation{Name: current.GetName(), UID: string(current.GetUID()), ResourceVersion: current.GetResourceVersion()}, "candidate_name_owned_by_other_resource")
			return nil, err
		}
		if err := validateCandidateActionObservedIdentity(prior, current); err != nil {
			return nil, err
		}
		if prior != nil && prior.Status == ExecutionActionApplied {
			return current, nil
		}
		if err := completeBlueGreenAction(store, logID, actionKey, ExecutionActionApplied, blueGreenObservationForObject(current, kind, plan.Snapshot.Target.Container), "candidate_create_readback"); err != nil {
			return nil, fmt.Errorf("候选 %s 已存在，但创建结果无法持久化: %w", kind, err)
		}
		return current, nil
	}
	if !apierrors.IsNotFound(getErr) {
		return nil, fmt.Errorf("读取候选 %s 失败: %w", kind, getErr)
	}
	if prior != nil && prior.Status == ExecutionActionApplied {
		return nil, newConflict("已确认创建的候选 %s 已不存在，停止自动重建", kind)
	}
	_, createErr := runtime.CreateResource(ctx, def, target.Namespace, desired.DeepCopy(), metav1.CreateOptions{FieldManager: "deploy-platform-blue-green"})
	current, getErr = runtime.GetResource(ctx, def, target.Namespace, desired.GetName(), metav1.GetOptions{})
	if getErr == nil {
		if apierrors.IsAlreadyExists(createErr) {
			_ = completeBlueGreenAction(store, logID, actionKey, ExecutionActionUnknown, blueGreenActionObservation{Name: current.GetName(), UID: string(current.GetUID()), ResourceVersion: current.GetResourceVersion()}, "candidate_name_already_occupied")
			return nil, newConflict("候选 %s 名称在写入前已被其他资源占用", kind)
		}
		if definitiveKubernetesWriteRejection(createErr) {
			_ = completeBlueGreenAction(store, logID, actionKey, ExecutionActionNotApplied, blueGreenActionObservation{Name: current.GetName(), UID: string(current.GetUID()), ResourceVersion: current.GetResourceVersion()}, "candidate_create_rejected_existing_object")
			return nil, fmt.Errorf("候选 %s 创建请求被 Kubernetes 拒绝，但发现同名对象需要人工核对: %w", kind, createErr)
		}
		if err := validateBlueGreenCandidateObject(current, desired, plan.ID, executionID, kind, plan.Snapshot.Target.Container, plan.Snapshot.Artifact.Image); err != nil {
			_ = completeBlueGreenAction(store, logID, actionKey, ExecutionActionUnknown, blueGreenActionObservation{Name: current.GetName(), UID: string(current.GetUID()), ResourceVersion: current.GetResourceVersion()}, "candidate_create_readback_mismatch")
			return nil, err
		}
		if err := validateCandidateActionObservedIdentity(prior, current); err != nil {
			return nil, err
		}
		if err := completeBlueGreenAction(store, logID, actionKey, ExecutionActionApplied, blueGreenObservationForObject(current, kind, plan.Snapshot.Target.Container), "candidate_create_confirmed"); err != nil {
			return nil, fmt.Errorf("候选 %s 已创建，但动作回读无法持久化: %w", kind, err)
		}
		return current, nil
	}
	if createErr != nil && apierrors.IsAlreadyExists(createErr) {
		return nil, newConflict("候选 %s 名称已存在，但无法证明其属于当前执行", kind)
	}
	status := ExecutionActionUnknown
	outcome := "candidate_create_unknown"
	if definitiveKubernetesWriteRejection(createErr) {
		status, outcome = ExecutionActionNotApplied, "candidate_create_rejected"
	}
	_ = completeBlueGreenAction(store, logID, actionKey, status, blueGreenActionObservation{Name: desired.GetName()}, outcome)
	if createErr != nil {
		return nil, fmt.Errorf("创建候选 %s 失败且未能回读确认: %w", kind, createErr)
	}
	return nil, fmt.Errorf("候选 %s 创建已响应但资源回读失败: %w", kind, getErr)
}

func definitiveKubernetesWriteRejection(err error) bool {
	return err != nil && (apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) || apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) || apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err))
}

func validateBlueGreenCandidateObject(current, desired *unstructured.Unstructured, planID, executionID int64, kind, container, image string) error {
	if current == nil || desired == nil || current.GetName() != desired.GetName() || current.GetNamespace() != desired.GetNamespace() || current.GetUID() == "" {
		return newConflict("候选 %s 身份不完整，拒绝接管", kind)
	}
	managed := current.GetLabels()
	if managed[blueGreenManagedLabel] != "deploy-platform" || managed[blueGreenPlanLabel] != fmt.Sprint(planID) || managed[blueGreenRunLabel] != fmt.Sprint(executionID) {
		return newConflict("候选 %s 同名资源不属于当前计划与执行", kind)
	}
	expectedHash, err := objectSpecHash(desired)
	if err != nil {
		return err
	}
	actualHash, err := objectSpecHash(current)
	if err != nil || expectedHash != actualHash {
		return newConflict("候选 %s spec 与审批计划不一致", kind)
	}
	if kind == "Deployment" && container != "" && image != "" && targetContainerImage(current, container) != image {
		return newConflict("候选 Deployment 镜像与审批制品不一致")
	}
	return nil
}

func sameBlueGreenActionIntent(a, b blueGreenActionIntent) bool {
	return a.PlanID == b.PlanID && a.ExecutionID == b.ExecutionID && a.Namespace == b.Namespace && a.Kind == b.Kind && a.Name == b.Name && a.UID == b.UID && a.Image == b.Image && a.SpecHash == b.SpecHash && a.ConfigHash == b.ConfigHash && sameStringMap(a.Selector, b.Selector) && sameStringMap(a.PreviousSelector, b.PreviousSelector)
}

// setBlueGreenEntryTrack performs the conditional Service selector write used
// for cutover and recovery. The caller must enforce approval, readiness,
// resource locks and human confirmation before selecting the candidate track.
func setBlueGreenEntryTrack(ctx context.Context, store *Store, runtime KubernetesRuntime, logID int64, plan DeliveryPlan, executionID int64, expected, desired map[string]string, actionKey string) error {
	target := plan.Snapshot.Target
	if store == nil || runtime == nil || logID <= 0 || plan.ID <= 0 || executionID <= 0 || target.ServiceName == "" || target.ServiceUID == "" || target.ServiceConfigHash == "" || len(expected) == 0 || len(desired) == 0 || actionKey == "" || sameStringMap(expected, desired) {
		return NewValidationError("蓝绿入口轨道更新参数不完整")
	}
	def, err := kube.LookupResource("", "v1", "services")
	if err != nil {
		return err
	}
	intent := blueGreenActionIntent{
		PlanID: plan.ID, ExecutionID: executionID, Namespace: target.Namespace, Kind: "Service",
		Name: target.ServiceName, UID: target.ServiceUID, Selector: desired,
		PreviousSelector: expected, ConfigHash: target.ServiceConfigHash,
	}
	prior, err := findExecutionAction(store, logID, actionKey)
	if err != nil {
		return err
	}
	if prior != nil {
		var recorded blueGreenActionIntent
		if err := json.Unmarshal(prior.Intent, &recorded); err != nil || prior.ActionKind != "set_service_track_selector" || !sameBlueGreenActionIntent(recorded, intent) {
			return newConflict("Service 轨道动作与当前计划不一致")
		}
	}
	service, err := runtime.GetResource(ctx, def, target.Namespace, target.ServiceName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("读取 Service 轨道切换前状态失败: %w", err)
	}
	if err := validateBlueGreenEntryService(service, target.ServiceName, target.ServiceUID, target.ServiceConfigHash); err != nil {
		return err
	}
	selector, found, err := unstructured.NestedStringMap(service.Object, "spec", "selector")
	if err != nil || !found {
		return NewValidationError("Service selector 无法读取")
	}
	if sameStringMap(selector, desired) {
		if prior == nil || prior.Status == ExecutionActionNotApplied {
			return newConflict("Service 已指向目标轨道但没有本次动作记录，停止接管")
		}
		if prior.Status == ExecutionActionApplied {
			var observed blueGreenActionObservation
			if len(prior.Observed) > 0 {
				if err := json.Unmarshal(prior.Observed, &observed); err != nil || (observed.UID != "" && observed.UID != string(service.GetUID())) || (len(observed.Selector) > 0 && !sameStringMap(observed.Selector, desired)) {
					return newConflict("Service 轨道切换回读与持久化观察不一致")
				}
			}
			return nil
		}
		return completeBlueGreenAction(store, logID, actionKey, ExecutionActionApplied, blueGreenActionObservation{
			Name: service.GetName(), UID: string(service.GetUID()), ResourceVersion: service.GetResourceVersion(), Selector: selector,
		}, "service_track_readback")
	}
	if !sameStringMap(selector, expected) || (prior != nil && prior.Status == ExecutionActionApplied) {
		return newConflict("Service selector 已偏离轨道动作基线，停止覆盖")
	}
	if err := beginBlueGreenAction(store, logID, actionKey, "set_service_track_selector", intent); err != nil {
		return err
	}
	patch, err := BlueGreenServiceSelectorPatch(service, expected, desired)
	if err != nil {
		return err
	}
	_, patchErr := runtime.PatchResource(ctx, def, target.Namespace, target.ServiceName, types.JSONPatchType, patch, metav1.PatchOptions{FieldManager: "deploy-platform-blue-green"})
	readback, readErr := runtime.GetResource(ctx, def, target.Namespace, target.ServiceName, metav1.GetOptions{})
	if readErr != nil {
		_ = completeBlueGreenAction(store, logID, actionKey, ExecutionActionUnknown, blueGreenActionObservation{Name: target.ServiceName}, "service_track_readback_failed")
		return fmt.Errorf("Service 轨道写入结果未知，回读失败: %w", readErr)
	}
	if err := validateBlueGreenEntryService(readback, target.ServiceName, target.ServiceUID, target.ServiceConfigHash); err != nil {
		_ = completeBlueGreenAction(store, logID, actionKey, ExecutionActionUnknown, blueGreenActionObservation{Name: readback.GetName(), UID: string(readback.GetUID()), ResourceVersion: readback.GetResourceVersion()}, "service_track_identity_changed")
		return err
	}
	observed, found, err := unstructured.NestedStringMap(readback.Object, "spec", "selector")
	if err != nil || !found {
		_ = completeBlueGreenAction(store, logID, actionKey, ExecutionActionUnknown, blueGreenActionObservation{Name: readback.GetName(), UID: string(readback.GetUID()), ResourceVersion: readback.GetResourceVersion()}, "service_track_selector_unknown")
		return NewValidationError("Service selector 写入后无法读取")
	}
	if !sameStringMap(observed, desired) {
		status, outcome := ExecutionActionUnknown, "service_track_write_unknown"
		if patchErr != nil && sameStringMap(observed, expected) {
			status, outcome = ExecutionActionNotApplied, "service_track_write_not_applied"
		}
		_ = completeBlueGreenAction(store, logID, actionKey, status, blueGreenActionObservation{
			Name: readback.GetName(), UID: string(readback.GetUID()), ResourceVersion: readback.GetResourceVersion(), Selector: observed,
		}, outcome)
		if patchErr != nil {
			return fmt.Errorf("Service 轨道切换未生效: %w", patchErr)
		}
		return newConflict("Service 轨道写入已响应，但回读 selector 不符合目标")
	}
	if definitiveKubernetesWriteRejection(patchErr) {
		_ = completeBlueGreenAction(store, logID, actionKey, ExecutionActionNotApplied, blueGreenActionObservation{
			Name: readback.GetName(), UID: string(readback.GetUID()), ResourceVersion: readback.GetResourceVersion(), Selector: observed,
		}, "service_track_write_rejected")
		return fmt.Errorf("Service 轨道写入被 Kubernetes 拒绝，虽然当前读回值相同仍需人工核对: %w", patchErr)
	}
	if err := completeBlueGreenAction(store, logID, actionKey, ExecutionActionApplied, blueGreenActionObservation{
		Name: readback.GetName(), UID: string(readback.GetUID()), ResourceVersion: readback.GetResourceVersion(), Selector: observed,
	}, "service_track_confirmed"); err != nil {
		return fmt.Errorf("Service 已切换，但动作回读无法持久化: %w", err)
	}
	return nil
}

func switchBlueGreenServiceToCandidate(ctx context.Context, store *Store, runtime KubernetesRuntime, logID int64, plan DeliveryPlan, executionID int64) error {
	target := plan.Snapshot.Target
	stableSelector, err := BlueGreenStableServiceSelector(target.ServiceSelector, target.StableLabelKey)
	if err != nil {
		return err
	}
	candidateSelector, err := BlueGreenCandidateSelector(target.CandidateLabelKey, target.CandidateLabelValue)
	if err != nil {
		return err
	}
	return setBlueGreenEntryTrack(ctx, store, runtime, logID, plan, executionID, stableSelector, candidateSelector, blueGreenCutoverAction)
}

func restoreBlueGreenServiceToStable(ctx context.Context, store *Store, runtime KubernetesRuntime, logID int64, plan DeliveryPlan, executionID int64) error {
	target := plan.Snapshot.Target
	stableSelector, err := BlueGreenStableServiceSelector(target.ServiceSelector, target.StableLabelKey)
	if err != nil {
		return err
	}
	candidateSelector, err := BlueGreenCandidateSelector(target.CandidateLabelKey, target.CandidateLabelValue)
	if err != nil {
		return err
	}
	return setBlueGreenEntryTrack(ctx, store, runtime, logID, plan, executionID, candidateSelector, stableSelector, blueGreenRestoreAction)
}

func inspectBlueGreenCandidate(ctx context.Context, store *Store, runtime KubernetesRuntime, logID int64, plan DeliveryPlan, executionID int64) (BlueGreenCandidateResources, *unstructured.Unstructured, *unstructured.Unstructured, *unstructured.Unstructured, error) {
	target := plan.Snapshot.Target
	deploymentDef, err := kube.LookupResource("apps", "v1", "deployments")
	if err != nil {
		return BlueGreenCandidateResources{}, nil, nil, nil, err
	}
	serviceDef, err := kube.LookupResource("", "v1", "services")
	if err != nil {
		return BlueGreenCandidateResources{}, nil, nil, nil, err
	}
	stableDeployment, err := runtime.GetResource(ctx, deploymentDef, target.Namespace, target.Deployment, metav1.GetOptions{})
	if err != nil {
		return BlueGreenCandidateResources{}, nil, nil, nil, fmt.Errorf("读取稳定 Deployment 失败: %w", err)
	}
	entryService, err := runtime.GetResource(ctx, serviceDef, target.Namespace, target.ServiceName, metav1.GetOptions{})
	if err != nil {
		return BlueGreenCandidateResources{}, nil, nil, nil, fmt.Errorf("读取生产 Service 失败: %w", err)
	}
	if err := validateBlueGreenLiveBaseline(plan, stableDeployment, entryService); err != nil {
		return BlueGreenCandidateResources{}, nil, nil, nil, err
	}
	stableSelector, err := BlueGreenStableServiceSelector(target.ServiceSelector, target.StableLabelKey)
	if err != nil {
		return BlueGreenCandidateResources{}, nil, nil, nil, err
	}
	selector, found, err := unstructured.NestedStringMap(entryService.Object, "spec", "selector")
	if err != nil || !found {
		return BlueGreenCandidateResources{}, nil, nil, nil, NewValidationError("生产 Service selector 无法读取")
	}
	if !sameStringMap(selector, stableSelector) {
		candidateSelector, selectorErr := BlueGreenCandidateSelector(target.CandidateLabelKey, target.CandidateLabelValue)
		if selectorErr != nil || !sameStringMap(selector, candidateSelector) {
			return BlueGreenCandidateResources{}, nil, nil, nil, newConflict("生产 Service 当前没有指向稳定或本次候选轨道")
		}
	}
	buildService := entryService.DeepCopy()
	if err := unstructured.SetNestedStringMap(buildService.Object, stableSelector, "spec", "selector"); err != nil {
		return BlueGreenCandidateResources{}, nil, nil, nil, err
	}
	resources, err := BuildBlueGreenCandidateResources(plan, executionID, stableDeployment, buildService)
	if err != nil {
		return BlueGreenCandidateResources{}, nil, nil, nil, err
	}
	candidateDeployment, err := runtime.GetResource(ctx, deploymentDef, target.Namespace, resources.Deployment.GetName(), metav1.GetOptions{})
	if err != nil {
		return BlueGreenCandidateResources{}, nil, nil, nil, fmt.Errorf("读取候选 Deployment 失败: %w", err)
	}
	if candidateLabels, found, labelErr := unstructured.NestedStringMap(candidateDeployment.Object, "spec", "template", "metadata", "labels"); labelErr == nil && found && candidateLabels[target.StableLabelKey] == "stable" {
		candidateDesiredLabels, desiredFound, desiredErr := unstructured.NestedStringMap(resources.Deployment.Object, "spec", "template", "metadata", "labels")
		if desiredErr != nil || !desiredFound {
			return BlueGreenCandidateResources{}, nil, nil, nil, NewValidationError("候选期望 Pod 标签无法读取")
		}
		candidateDesiredLabels[target.StableLabelKey] = "stable"
		if err := unstructured.SetNestedStringMap(resources.Deployment.Object, candidateDesiredLabels, "spec", "template", "metadata", "labels"); err != nil {
			return BlueGreenCandidateResources{}, nil, nil, nil, err
		}
	}
	if err := validateBlueGreenCandidateDeploymentReady(candidateDeployment, resources.Deployment, candidateDeployment, plan.ID, executionID, target.Container, plan.Snapshot.Artifact.Image); err != nil {
		return BlueGreenCandidateResources{}, nil, nil, nil, err
	}
	candidateService, serviceErr := runtime.GetResource(ctx, serviceDef, target.Namespace, resources.Service.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(serviceErr) {
		retirement, actionErr := findExecutionAction(store, logID, blueGreenPromotedPreview)
		if actionErr != nil {
			return BlueGreenCandidateResources{}, nil, nil, nil, actionErr
		}
		if retirement == nil {
			return BlueGreenCandidateResources{}, nil, nil, nil, newConflict("候选预览 Service 不存在且没有已记录的成功收尾动作")
		}
		var intent blueGreenActionIntent
		if err := json.Unmarshal(retirement.Intent, &intent); err != nil || intent.PlanID != plan.ID || intent.ExecutionID != executionID || intent.Namespace != target.Namespace || intent.Kind != "Service" || intent.Name != resources.Service.GetName() || intent.UID == "" {
			return BlueGreenCandidateResources{}, nil, nil, nil, newConflict("候选预览 Service 收尾动作身份与当前计划不匹配")
		}
		createdUID, uidErr := recordedBlueGreenCandidateUID(store, logID, blueGreenCandidateService, resources.Service.GetName())
		if uidErr != nil || string(createdUID) != intent.UID {
			return BlueGreenCandidateResources{}, nil, nil, nil, newConflict("候选预览 Service 收尾动作 UID 与创建记录不匹配")
		}
		if retirement.Status != ExecutionActionApplied {
			if err := completeBlueGreenAction(store, logID, blueGreenPromotedPreview, ExecutionActionApplied, blueGreenActionObservation{Name: intent.Name, UID: intent.UID}, "candidate_preview_retirement_confirmed"); err != nil {
				return BlueGreenCandidateResources{}, nil, nil, nil, fmt.Errorf("候选预览 Service 已不存在，但收尾结果无法持久化: %w", err)
			}
		}
	} else if serviceErr != nil {
		return BlueGreenCandidateResources{}, nil, nil, nil, fmt.Errorf("读取候选预览 Service 失败: %w", serviceErr)
	} else {
		retirement, actionErr := findExecutionAction(store, logID, blueGreenPromotedPreview)
		if actionErr != nil {
			return BlueGreenCandidateResources{}, nil, nil, nil, actionErr
		}
		if retirement != nil && retirement.Status == ExecutionActionApplied {
			return BlueGreenCandidateResources{}, nil, nil, nil, newConflict("已确认退役的候选预览 Service 被重新创建，停止自动接管")
		}
		creation, actionErr := findExecutionAction(store, logID, blueGreenCandidateService)
		if actionErr != nil {
			return BlueGreenCandidateResources{}, nil, nil, nil, actionErr
		}
		if err := validateCandidateActionObservedIdentity(creation, candidateService); err != nil {
			return BlueGreenCandidateResources{}, nil, nil, nil, err
		}
		if err := validateBlueGreenCandidateObject(candidateService, resources.Service, plan.ID, executionID, "Service", target.Container, plan.Snapshot.Artifact.Image); err != nil {
			return BlueGreenCandidateResources{}, nil, nil, nil, err
		}
		if _, err := waitForBlueGreenCandidateEndpoints(ctx, runtime, target.Namespace, resources.Service.GetName(), target.CandidateLabelKey, target.CandidateLabelValue); err != nil {
			return BlueGreenCandidateResources{}, nil, nil, nil, err
		}
	}
	return resources, stableDeployment, entryService, candidateDeployment, nil
}

func promoteBlueGreenCandidateToStable(ctx context.Context, store *Store, runtime KubernetesRuntime, logID int64, plan DeliveryPlan, executionID int64) (string, DeliveryWorkloadReference, error) {
	target := plan.Snapshot.Target
	_, _, entryService, candidateDeployment, err := inspectBlueGreenCandidate(ctx, store, runtime, logID, plan, executionID)
	if err != nil {
		return "", DeliveryWorkloadReference{}, err
	}
	candidateSelector, err := BlueGreenCandidateSelector(target.CandidateLabelKey, target.CandidateLabelValue)
	if err != nil {
		return "", DeliveryWorkloadReference{}, err
	}
	currentSelector, found, err := unstructured.NestedStringMap(entryService.Object, "spec", "selector")
	if err != nil || !found {
		return "", DeliveryWorkloadReference{}, NewValidationError("生产 Service selector 无法读取")
	}
	if !sameStringMap(currentSelector, candidateSelector) {
		return "", DeliveryWorkloadReference{}, newConflict("业务验证期间生产 Service 未保持在本次候选轨道")
	}
	if _, err := waitForBlueGreenCandidateEntryEndpoints(ctx, runtime, target.Namespace, target.ServiceName, target.CandidateLabelKey, target.CandidateLabelValue); err != nil {
		return "", DeliveryWorkloadReference{}, fmt.Errorf("确认业务验证时生产候选端点不健康: %w", err)
	}
	serviceDef, err := kube.LookupResource("", "v1", "services")
	if err != nil {
		return "", DeliveryWorkloadReference{}, err
	}
	previewServiceName := blueGreenResourceName(target.ServiceName, plan.ID)
	previewServiceUID, err := recordedBlueGreenCandidateUID(store, logID, blueGreenCandidateService, previewServiceName)
	if err != nil {
		return "", DeliveryWorkloadReference{}, err
	}
	if err := deleteOwnedBlueGreenCandidate(ctx, store, runtime, logID, executionID, plan.ID, target.Namespace, serviceDef, previewServiceName, previewServiceUID, "Service", blueGreenPromotedPreview); err != nil {
		return "", DeliveryWorkloadReference{}, fmt.Errorf("活动版本已通过验证，但候选预览 Service 收尾未确认: %w", err)
	}
	if candidateDeployment.GetUID() == "" {
		return "", DeliveryWorkloadReference{}, newConflict("候选 Deployment UID 为空，不能更新活动资源绑定")
	}
	active := DeliveryWorkloadReference{
		ClusterID: target.ClusterID, Namespace: target.Namespace, Kind: "Deployment", Name: candidateDeployment.GetName(),
		UID: string(candidateDeployment.GetUID()), ResourceVersion: candidateDeployment.GetResourceVersion(),
	}
	return candidateDeployment.GetName(), active, nil
}

func validateBlueGreenEntryService(service *unstructured.Unstructured, name, uid, configHash string) error {
	if service == nil || service.GetName() != name || service.GetUID() == "" || string(service.GetUID()) != uid {
		return newConflict("稳定 Service UID 已变化，停止轨道更新")
	}
	if err := serviceConfigurationHashMustMatch(service, configHash); err != nil {
		return err
	}
	return nil
}

func objectSpecHash(object *unstructured.Unstructured) (string, error) {
	if object == nil {
		return "", NewValidationError("Kubernetes 资源 spec 为空")
	}
	spec, found, err := unstructured.NestedMap(object.Object, "spec")
	if err != nil || !found {
		return "", NewValidationError("Kubernetes 资源 spec 无法读取")
	}
	if object.GetKind() == "Service" {
		if err := stripBlueGreenAllocatedServiceFields(spec); err != nil {
			return "", err
		}
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("序列化 Kubernetes 资源 spec 失败: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func blueGreenObservationForObject(object *unstructured.Unstructured, kind, container string) blueGreenActionObservation {
	observation := blueGreenActionObservation{Name: object.GetName(), UID: string(object.GetUID()), ResourceVersion: object.GetResourceVersion()}
	if kind == "Service" {
		observation.Selector, _, _ = unstructured.NestedStringMap(object.Object, "spec", "selector")
	} else {
		observation.Image = targetContainerImage(object, container)
	}
	return observation
}

func beginBlueGreenAction(store *Store, logID int64, actionKey, actionKind string, intent blueGreenActionIntent) error {
	encoded, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	_, _, err = store.BeginExecutionAction(ExecutionAction{DeployLogID: logID, ActionKey: actionKey, ActionKind: actionKind, Status: ExecutionActionIntent, Intent: encoded})
	return err
}

func findExecutionAction(store *Store, logID int64, actionKey string) (*ExecutionAction, error) {
	actions, err := store.ExecutionActions(logID)
	if err != nil {
		return nil, err
	}
	for i := range actions {
		if actions[i].ActionKey == actionKey {
			return &actions[i], nil
		}
	}
	return nil, nil
}

func completeBlueGreenAction(store *Store, logID int64, actionKey, status string, observation blueGreenActionObservation, outcome string) error {
	encoded, err := json.Marshal(observation)
	if err != nil {
		return err
	}
	_, err = store.CompleteExecutionAction(logID, actionKey, status, encoded, outcome)
	return err
}
