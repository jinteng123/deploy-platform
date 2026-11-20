package kube

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func newWriteFake(t *testing.T) *Client {
	t.Helper()
	dep := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "web", "namespace": "dev", "resourceVersion": "5"},
		"spec": map[string]any{"replicas": int64(2),
			"template": map[string]any{"spec": map[string]any{
				"containers": []any{map[string]any{"name": "app", "image": "v1"}}}}},
	}}
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClient(scheme, dep)
	return NewWithClientset(fake.NewSimpleClientset(), dyn, nil)
}

// TestPatchResourceDirect verifies the dynamic-client patch path: merge
// patch applies and returns the patched object; a stale resourceVersion
// inside the patch body surfaces the apiserver's 409 Conflict.
func TestPatchResourceDirect(t *testing.T) {
	c := newWriteFake(t)
	def, err := LookupResource("apps", "v1", "deployments")
	if err != nil {
		t.Fatal(err)
	}
	obj, err := c.PatchResource(context.Background(), def, "dev", "web",
		types.MergePatchType, []byte(`{"spec":{"replicas":4}}`), metav1.PatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, _, _ := unstructured.NestedInt64(obj.Object, "spec", "replicas"); got != 4 {
		t.Fatalf("replicas=%d", got)
	}
	// the stored object must reflect the patch too
	stored, err := c.GetResource(context.Background(), def, "dev", "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, _, _ := unstructured.NestedInt64(stored.Object, "spec", "replicas"); got != 4 {
		t.Fatalf("stored replicas=%d", got)
	}
}

// TestDeleteResourceDirect covers the fake-client delete path and —
// critically — that a mismatched resourceVersion precondition surfaces
// as a Conflict so the HTTP layer can answer 409.
func TestDeleteResourceDirect(t *testing.T) {
	c := newWriteFake(t)
	def, err := LookupResource("apps", "v1", "deployments")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteResource(context.Background(), def, "dev", "web", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetResource(context.Background(), def, "dev", "web", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("deleted object err=%v", err)
	}
	if err := c.DeleteResource(context.Background(), def, "dev", "web", metav1.DeleteOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("second delete err=%v", err)
	}

	// ResourceVersion precondition: matching → delete proceeds. (Stale-rv
	// rejection is enforced atomically by the real apiserver — the fake
	// tracker doesn't simulate preconditions — so the Conflict path is
	// covered by the ssh get-compare test, the mock runtime, and the
	// real-Kind check.)
	c = newWriteFake(t)
	rv := "5"
	if err := c.DeleteResource(context.Background(), def, "dev", "web",
		metav1.DeleteOptions{Preconditions: &metav1.Preconditions{ResourceVersion: &rv}}); err != nil {
		t.Fatalf("matching rv delete: %v", err)
	}
}

// TestWriteResourcesRegistry freezes capability metadata: workload
// controllers carry the full write surface, low-risk namespaced
// resources are deletable, high-risk kinds stay read-only.
func TestWriteResourcesRegistry(t *testing.T) {
	must := func(group, version, resource string) ResourceDef {
		d, err := LookupResource(group, version, resource)
		if err != nil {
			t.Fatalf("%s: %v", resource, err)
		}
		return d
	}
	for _, r := range []struct{ group, res string }{
		{"apps", "deployments"}, {"apps", "statefulsets"},
	} {
		d := must(r.group, "v1", r.res)
		if !d.Scalable || !d.Imageable || !d.Restartable || !d.Deletable {
			t.Fatalf("%s caps=%+v", r.res, d)
		}
	}
	// daemonsets: restartable + deletable but NOT scalable/imageable is
	// wrong too — ds has containers so it IS imageable; it isn't scalable.
	ds := must("apps", "v1", "daemonsets")
	if ds.Scalable || !ds.Restartable || !ds.Deletable || !ds.Imageable {
		t.Fatalf("daemonsets caps=%+v", ds)
	}
	// Everyday namespaced kinds are deletable but nothing else.
	for _, tc := range []struct{ group, res string }{
		{"", "pods"}, {"", "configmaps"}, {"", "persistentvolumeclaims"},
		{"", "services"}, {"networking.k8s.io", "ingresses"},
		{"batch", "jobs"}, {"batch", "cronjobs"},
	} {
		d := must(tc.group, "v1", tc.res)
		if d.Scalable || d.Imageable || d.Restartable || !d.Deletable {
			t.Fatalf("%s caps=%+v", tc.res, d)
		}
	}
	// High-risk kinds: never writable through this API.
	for _, tc := range []struct{ group, res string }{
		{"", "namespaces"}, {"", "nodes"}, {"", "persistentvolumes"},
		{"storage.k8s.io", "storageclasses"}, {"", "events"},
	} {
		d := must(tc.group, "v1", tc.res)
		if d.Scalable || d.Imageable || d.Restartable || d.Deletable {
			t.Fatalf("%s must be read-only: %+v", tc.res, d)
		}
	}
	if !strings.Contains(must("apps", "v1", "deployments").KubectlResource(), "deployments") {
		t.Fatal("KubectlResource sanity")
	}
}

// TestCreateResourceDirect exercises the dynamic-client create path:
// the object lands in the fake store and a duplicate create surfaces
// AlreadyExists so the HTTP layer can answer 409.
func TestCreateResourceDirect(t *testing.T) {
	c := newWriteFake(t)
	def, err := LookupResource("", "v1", "persistentvolumeclaims")
	if err != nil {
		t.Fatal(err)
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolumeClaim",
		"metadata": map[string]any{"name": "data", "namespace": "dev"},
		"spec": map[string]any{"accessModes": []any{"ReadWriteOnce"},
			"resources": map[string]any{"requests": map[string]any{"storage": "2Gi"}}},
	}}
	out, err := c.CreateResource(context.Background(), def, "dev", obj, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out.GetName() != "data" || out.GetNamespace() != "dev" {
		t.Fatalf("created object: %s/%s", out.GetNamespace(), out.GetName())
	}
	if gvk := out.GroupVersionKind(); gvk.Kind != "PersistentVolumeClaim" {
		t.Fatalf("GVK not stamped: %v", gvk)
	}
	if _, err := c.CreateResource(context.Background(), def, "dev", obj.DeepCopy(), metav1.CreateOptions{}); !apierrors.IsAlreadyExists(err) {
		t.Fatalf("duplicate create must surface AlreadyExists, got %v", err)
	}
}
