package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"deploy-platform/backend-go/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// templateHash binds the existing workload identity and its non-candidate
// configuration. Only a hash is stored: inline env values are never copied to
// release records. Mutable ConfigMap/Secret contents need versioned references.
func templateHash(ctx context.Context, rt KubernetesRuntime, app App, allowMock bool) (string, error) {
	def, err := kube.LookupResource("apps", "v1", "deployments")
	if err != nil {
		return "", err
	}
	obj, err := rt.GetResource(ctx, def, app.Namespace, app.DeploymentName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if obj == nil {
		return "", NewValidationError("目标 Deployment 不存在")
	}
	return workloadHash(obj, app.ContainerName, allowMock)
}
func workloadHash(obj *unstructured.Unstructured, container string, allowMock bool) (string, error) {
	spec, found, err := unstructured.NestedMap(obj.Object, "spec")
	if err != nil || !found {
		return "", NewValidationError("目标 Deployment 缺少 spec")
	}
	template, found, err := unstructured.NestedMap(spec, "template")
	if (err != nil || !found) && !allowMock {
		return "", NewValidationError("目标 Deployment 缺少 Pod 模板")
	}
	if found {
		containers, _, err := unstructured.NestedSlice(template, "spec", "containers")
		if err != nil {
			return "", err
		}
		matched := false
		for _, c := range containers {
			m, ok := c.(map[string]any)
			if ok && m["name"] == container {
				delete(m, "image")
				matched = true
			}
		}
		if !matched && !allowMock {
			return "", NewValidationError("目标容器不存在")
		}
		if err := unstructured.SetNestedSlice(template, containers, "spec", "containers"); err != nil {
			return "", err
		}
		spec["template"] = template
	}
	// HPA changes replicas independently of a release; bind Pod configuration,
	// selectors and rollout policy without rejecting legitimate autoscaling.
	delete(spec, "replicas")
	data, err := json.Marshal(struct {
		UID  string
		Spec map[string]any
	}{string(obj.GetUID()), spec})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
func (s *CDService) currentTemplate(ctx context.Context, c Cluster, a App) (string, error) {
	if s.worker == nil {
		return "", NewValidationError("发布运行时不可用")
	}
	rt, err := s.worker.pipeline.k8s.For(c)
	if err != nil {
		return "", err
	}
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return templateHash(readCtx, rt, a, s.cfg.MockRuntime)
}
