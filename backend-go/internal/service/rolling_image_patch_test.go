package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestApprovedDeploymentImagePatchChecksIdentityAndCurrentImage(t *testing.T) {
	deployment := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "prod", "name": "orders", "uid": "approved-uid", "resourceVersion": "42"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{
			map[string]any{"name": "sidecar", "image": "sidecar:v1"},
			map[string]any{"name": "app", "image": "orders:v1"},
		}}}},
	}}
	patch, err := approvedDeploymentImagePatch(deployment, "prod", "orders", "app", "orders:v1", "orders@sha256:abc", "approved-uid")
	if err != nil {
		t.Fatal(err)
	}
	var ops []struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(patch, &ops); err != nil {
		t.Fatal(err)
	}
	if len(ops) != 5 || ops[0].Path != "/metadata/uid" || ops[0].Value != "approved-uid" || ops[1].Path != "/metadata/resourceVersion" || ops[1].Value != "42" || ops[2].Path != "/spec/template/spec/containers/1/name" || ops[3].Value != "orders:v1" || ops[4].Op != "replace" || ops[4].Path != "/spec/template/spec/containers/1/image" {
		t.Fatalf("patch lost atomic identity/image tests: %+v", ops)
	}
	if _, err := approvedDeploymentImagePatch(deployment, "prod", "orders", "app", "orders:v1", "orders:v2", "other-uid"); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed UID error=%v, want conflict", err)
	}
	if _, err := approvedDeploymentImagePatch(deployment, "prod", "orders", "app", "orders:changed", "orders:v2", "approved-uid"); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed image error=%v, want conflict", err)
	}
}

func TestPatchApprovedDeploymentImageRejectsReplacementAtWrite(t *testing.T) {
	runtime := &recordingK8s{deploymentUID: "approved-uid", replacementUIDOnPatch: "replacement-uid"}
	err := patchApprovedDeploymentImage(context.Background(), runtime, "prod", "orders", "app", "reg.io/app:current", "orders:v2", "approved-uid")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("replacement at write error=%v, want conflict", err)
	}
	if len(runtime.setCalls) != 0 {
		t.Fatalf("replacement Deployment was mutated: %v", runtime.setCalls)
	}
	if _, err := approvedDeploymentCurrentImage(context.Background(), runtime, "prod", "orders", "app", "approved-uid"); !errors.Is(err, ErrConflict) {
		t.Fatalf("replacement readback error=%v, want conflict", err)
	}
}
