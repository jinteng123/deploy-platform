package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"deploy-platform/backend-go/internal/kube"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func mustDef(t *testing.T, group, version, resource string) kube.ResourceDef {
	t.Helper()
	def, err := kube.LookupResource(group, version, resource)
	if err != nil {
		t.Fatalf("LookupResource(%s,%s,%s): %v", group, version, resource, err)
	}
	return def
}

func TestKubectlResourceArgs(t *testing.T) {
	pods := mustDef(t, "", "v1", "pods")
	nodes := mustDef(t, "", "v1", "nodes")
	deps := mustDef(t, "apps", "v1", "deployments")

	cases := []struct {
		name string
		def  kube.ResourceDef
		ns   string
		opts metav1.ListOptions
		want []string
	}{
		{"namespaced", pods, "app", metav1.ListOptions{}, []string{"get", "pods", "-n", "app"}},
		{"all namespaces", pods, "", metav1.ListOptions{}, []string{"get", "pods", "--all-namespaces"}},
		{"cluster scoped ignores ns", nodes, "app", metav1.ListOptions{}, []string{"get", "nodes"}},
		{"grouped resource", deps, "app", metav1.ListOptions{}, []string{"get", "deployments.v1.apps", "-n", "app"}},
		{"selectors", pods, "app", metav1.ListOptions{LabelSelector: "a=b", FieldSelector: "x=y"},
			[]string{"get", "pods", "-n", "app", "-l", "a=b", "--field-selector", "x=y"}},
	}
	for _, tc := range cases {
		got := kubectlResourceArgs(tc.def, tc.ns, tc.opts)
		if strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestKubectlErrMapping(t *testing.T) {
	pods := mustDef(t, "", "v1", "pods")
	exit := errors.New("exit status 1")

	err := kubectlErr("获取资源详情失败", `Error from server (NotFound): pods "p" not found`, exit, pods, "p")
	if !apierrors.IsNotFound(err) {
		t.Fatalf("NotFound stderr → %v", err)
	}
	err = kubectlErr("获取资源列表失败", "Error from server (Forbidden): pods is forbidden", exit, pods, "")
	if !apierrors.IsForbidden(err) {
		t.Fatalf("Forbidden stderr → %v", err)
	}
	err = kubectlErr("获取资源列表失败", "connection refused", exit, pods, "")
	if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) ||
		!strings.Contains(err.Error(), "获取资源列表失败") || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("generic stderr → %v", err)
	}
}

func TestMockKubernetesResources(t *testing.T) {
	pods := mustDef(t, "", "v1", "pods")
	nodes := mustDef(t, "", "v1", "nodes")
	ctx := context.Background()
	mock := MockKubernetes{}

	list, err := mock.ListResources(ctx, pods, "dev", metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if list.GetKind() != "PodList" || len(list.Items) != 2 {
		t.Fatalf("list: %s %d", list.GetKind(), len(list.Items))
	}
	if list.Items[0].GetKind() != "Pod" || list.Items[0].GetNamespace() != "dev" {
		t.Fatalf("item: %s %s", list.Items[0].GetKind(), list.Items[0].GetNamespace())
	}

	// Cluster-scoped objects carry no namespace.
	list, err = mock.ListResources(ctx, nodes, "ignored", metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if list.Items[0].GetNamespace() != "" {
		t.Fatalf("node item leaked namespace: %v", list.Items[0])
	}

	obj, err := mock.GetResource(ctx, pods, "dev", "p1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if obj.GetName() != "p1" || obj.GetNamespace() != "dev" || obj.GetKind() != "Pod" {
		t.Fatalf("obj: %v", obj)
	}
	if _, err := mock.GetResource(ctx, pods, "dev", "missing", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("missing → %v, want NotFound", err)
	}
	if _, err := mock.GetResource(ctx, pods, "dev", "", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("empty name → %v, want NotFound", err)
	}
}
