package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"deploy-platform/backend-go/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const observationDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type podImageObservationRuntime struct {
	KubernetesRuntime
	deployment  *unstructured.Unstructured
	replicaSets *unstructured.UnstructuredList
	pods        *unstructured.UnstructuredList
	listError   map[string]error
}

func (r podImageObservationRuntime) GetResource(_ context.Context, def kube.ResourceDef, _, _ string, _ metav1.GetOptions) (*unstructured.Unstructured, error) {
	if def.Resource != "deployments" || r.deployment == nil {
		return nil, errors.New("unexpected get")
	}
	return r.deployment.DeepCopy(), nil
}

func (r podImageObservationRuntime) ListResources(_ context.Context, def kube.ResourceDef, _ string, _ metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	if err := r.listError[def.Resource]; err != nil {
		return nil, err
	}
	switch def.Resource {
	case "replicasets":
		return r.replicaSets.DeepCopy(), nil
	case "pods":
		return r.pods.DeepCopy(), nil
	default:
		return nil, errors.New("unexpected list")
	}
}

func TestObservePodImageDigestVerifiesReadyPodsOwnedByCurrentReplicaSet(t *testing.T) {
	runtime := newPodImageObservationRuntime(observationDigest)
	status, detail := observePodImageDigest(context.Background(), runtime, "prod", "orders", "api", "registry.example/orders/api@"+observationDigest)
	if status != "verified" {
		t.Fatalf("observation status=%q detail=%q", status, detail)
	}
	if !strings.Contains(detail, "2 个当前 ReplicaSet Pod") || !strings.Contains(detail, observationDigest) {
		t.Fatalf("observation detail=%q", detail)
	}
}

func TestObservePodImageDigestKeepsMultiArchAndRuntimeSpecificIDsUnknown(t *testing.T) {
	t.Run("platform manifest can differ from requested index", func(t *testing.T) {
		runtime := newPodImageObservationRuntime("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		status, detail := observePodImageDigest(context.Background(), runtime, "prod", "orders", "api", "registry.example/orders/api@"+observationDigest)
		if status != "unknown" || !strings.Contains(detail, "多架构镜像") {
			t.Fatalf("observation status=%q detail=%q", status, detail)
		}
	})
	t.Run("runtime config ID does not expose a registry digest", func(t *testing.T) {
		runtime := newPodImageObservationRuntime("sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
		for i := range runtime.pods.Items {
			statuses, _, _ := unstructured.NestedSlice(runtime.pods.Items[i].Object, "status", "containerStatuses")
			statuses[0].(map[string]any)["imageID"] = "containerd://sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
			_ = unstructured.SetNestedSlice(runtime.pods.Items[i].Object, statuses, "status", "containerStatuses")
		}
		status, detail := observePodImageDigest(context.Background(), runtime, "prod", "orders", "api", "registry.example/orders/api@"+observationDigest)
		if status != "unknown" || !strings.Contains(detail, "未提供可识别的 registry digest") {
			t.Fatalf("observation status=%q detail=%q", status, detail)
		}
	})
}

func TestObservePodImageDigestDegradesToUnknownWhenReadPermissionsAreMissing(t *testing.T) {
	runtime := newPodImageObservationRuntime(observationDigest)
	runtime.listError = map[string]error{"pods": errors.New("forbidden")}
	status, detail := observePodImageDigest(context.Background(), runtime, "prod", "orders", "api", "registry.example/orders/api@"+observationDigest)
	if status != "unknown" || !strings.Contains(detail, "没有读取 Pod 状态的权限") {
		t.Fatalf("observation status=%q detail=%q", status, detail)
	}
}

func newPodImageObservationRuntime(imageDigest string) podImageObservationRuntime {
	deployment := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "orders", "namespace": "prod", "uid": "deployment-uid"},
		"spec": map[string]any{
			"selector": map[string]any{"matchLabels": map[string]any{"app": "orders"}},
			"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "api", "image": "registry.example/orders/api@" + observationDigest}}}},
		},
	}}
	latestReplicaSet := observationReplicaSet("rs-new", "2", "deployment-uid", "rs-new-uid", observationDigest)
	oldReplicaSet := observationReplicaSet("rs-old", "1", "deployment-uid", "rs-old-uid", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	otherReplicaSet := observationReplicaSet("rs-other", "9", "other-deployment-uid", "rs-other-uid", imageDigest)
	return podImageObservationRuntime{
		deployment: deployment,
		replicaSets: &unstructured.UnstructuredList{Items: []unstructured.Unstructured{
			*oldReplicaSet, *latestReplicaSet, *otherReplicaSet,
		}},
		pods: &unstructured.UnstructuredList{Items: []unstructured.Unstructured{
			*observationPod("pod-a", "rs-new-uid", "containerd://registry.example/orders/api@"+imageDigest),
			*observationPod("pod-b", "rs-new-uid", "containerd://registry.example/orders/api@"+imageDigest),
			*observationPod("old-pod", "rs-old-uid", "containerd://registry.example/orders/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			*observationPod("other-pod", "rs-other-uid", "containerd://registry.example/orders/api@"+imageDigest),
		}},
	}
}

func observationReplicaSet(name, revision, deploymentUID, replicaSetUID, imageDigest string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "ReplicaSet",
		"metadata": map[string]any{
			"name": name, "namespace": "prod", "uid": replicaSetUID,
			"annotations":     map[string]any{"deployment.kubernetes.io/revision": revision},
			"ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "orders", "uid": deploymentUID, "controller": true}},
		},
		"spec": map[string]any{
			"replicas": int64(2),
			"selector": map[string]any{"matchLabels": map[string]any{"app": "orders", "pod-template-hash": name}},
			"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "api", "image": "registry.example/orders/api@" + imageDigest}}}},
		},
	}}
}

func observationPod(name, replicaSetUID, imageID string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{
			"name": name, "namespace": "prod", "labels": map[string]any{"app": "orders", "pod-template-hash": "rs-new"},
			"ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": "rs-new", "uid": replicaSetUID, "controller": true}},
		},
		"status": map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"name": "api", "imageID": imageID, "ready": true}}},
	}}
}
