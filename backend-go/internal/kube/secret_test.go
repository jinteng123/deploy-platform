package kube

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestMaskSecretData freezes the masking contract: every data and
// stringData value becomes "***" while keys stay visible; non-secret
// fields and missing sections are untouched.
func TestMaskSecretData(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata":   map[string]any{"name": "s1", "resourceVersion": "9"},
		"type":       "Opaque",
		"data":       map[string]any{"password": "cGFzcw==", "token": "dG9r"},
		"stringData": map[string]any{"extra": "plain"},
	}}
	MaskSecretData(obj)
	data, _, _ := unstructured.NestedMap(obj.Object, "data")
	if len(data) != 2 || data["password"] != MaskedValue || data["token"] != MaskedValue {
		t.Fatalf("data=%v", data)
	}
	sd, _, _ := unstructured.NestedMap(obj.Object, "stringData")
	if sd["extra"] != MaskedValue {
		t.Fatalf("stringData=%v", sd)
	}
	if obj.GetResourceVersion() != "9" || obj.GetName() != "s1" {
		t.Fatal("metadata must be preserved")
	}

	// No data sections → no-op, no crash.
	empty := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "s2"}}}
	MaskSecretData(empty)
	MaskSecretData(nil)
}

// TestSecretDefAndEditableRegistry covers the registry deltas for
// secrets: allowlisted with masked reads, creatable (stringData in,
// masked response out) and deletable — but never editable, so masked
// content can never be written back. The editable set remains exactly
// the deletable namespaced kinds.
func TestSecretDefAndEditableRegistry(t *testing.T) {
	sec, err := LookupResource("", "v1", "secrets")
	if err != nil {
		t.Fatalf("secrets must be allowlisted now: %v", err)
	}
	if !IsSecretDef(sec) || !sec.Namespaced {
		t.Fatalf("secret def=%+v", sec)
	}
	if sec.Scalable || sec.Imageable || sec.Restartable || sec.Editable {
		t.Fatalf("secrets must stay non-editable: %+v", sec)
	}
	if !sec.Deletable || !sec.Creatable {
		t.Fatalf("secrets must be creatable and deletable: %+v", sec)
	}
	other, _ := LookupResource("", "v1", "configmaps")
	if IsSecretDef(other) {
		t.Fatal("configmaps are not secrets")
	}

	// Editable ⊆ Deletable: everyday namespaced kinds minus pods, whose
	// manifest is deliberately read-only (controller-owned objects).
	for _, d := range AllowedResources() {
		if d.Editable && !d.Deletable {
			t.Fatalf("%s editable without deletable", d.Resource)
		}
	}
	pod, _ := LookupResource("", "v1", "pods")
	if pod.Editable || !pod.Deletable {
		t.Fatalf("pods must be deletable-but-not-editable: %+v", pod)
	}
}
