package service

import (
	"encoding/json"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Patch payloads for the controlled write endpoints. resourceVersion is
// embedded in metadata when provided — the API server compares it with
// the stored object and answers 409 Conflict on a stale read, giving
// optimistic concurrency without JSON-patch test ops (which the SSH
// path can't express portably either).

// ScalePatch sets spec.replicas (deployments, statefulsets).
func ScalePatch(replicas int32, resourceVersion string) ([]byte, error) {
	return marshalPatch(map[string]any{
		"spec": map[string]any{"replicas": replicas},
	}, resourceVersion)
}

// ImagePatch sets one container's image via a strategic merge keyed on
// the container name — safe for built-in workload types and never
// touches sibling containers (a plain merge would replace the array).
func ImagePatch(container, image, resourceVersion string) ([]byte, error) {
	return marshalPatch(map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{
						map[string]any{"name": container, "image": image},
					},
				},
			},
		},
	}, resourceVersion)
}

// RestartPatch bumps kubectl.kubernetes.io/restartedAt on the pod
// template — the standard rollout-restart trigger.
func RestartPatch(resourceVersion string) ([]byte, error) {
	return marshalPatch(map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]any{
						"kubectl.kubernetes.io/restartedAt": time.Now().UTC().Format(time.RFC3339),
					},
				},
			},
		},
	}, resourceVersion)
}

func marshalPatch(spec map[string]any, resourceVersion string) ([]byte, error) {
	patch := map[string]any{}
	if resourceVersion != "" {
		patch["metadata"] = map[string]any{"resourceVersion": resourceVersion}
	}
	for k, v := range spec {
		patch[k] = v
	}
	return json.Marshal(patch)
}

// ContainerInPodTemplate reports whether container name exists in
// spec.template.spec.containers or initContainers. Checked before an
// image patch so a typo can't inject a phantom container.
func ContainerInPodTemplate(obj *unstructured.Unstructured, name string) bool {
	for _, field := range []string{"containers", "initContainers"} {
		list, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", field)
		for _, c := range list {
			if m, ok := c.(map[string]any); ok {
				if n, _, _ := unstructured.NestedString(m, "name"); n == name {
					return true
				}
			}
		}
	}
	return false
}

// ErrContainerNotFound marks an image update against a container name
// absent from the pod template — rejected client-side as 400.
var ErrContainerNotFound = fmt.Errorf("容器不存在")
