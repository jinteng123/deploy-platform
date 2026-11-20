package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"deploy-platform/backend-go/internal/kube"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func patchJSON(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestScalePatch(t *testing.T) {
	p, err := ScalePatch(3, "123")
	if err != nil {
		t.Fatal(err)
	}
	m := patchJSON(t, p)
	if m["spec"].(map[string]any)["replicas"].(float64) != 3 {
		t.Fatalf("patch=%v", m)
	}
	if m["metadata"].(map[string]any)["resourceVersion"] != "123" {
		t.Fatalf("rv not embedded: %v", m)
	}
	// No resourceVersion → no metadata key at all.
	p, _ = ScalePatch(1, "")
	if _, ok := patchJSON(t, p)["metadata"]; ok {
		t.Fatal("empty rv must not produce metadata")
	}
}

func TestImagePatch(t *testing.T) {
	p, err := ImagePatch("app", "nginx:1.27", "42")
	if err != nil {
		t.Fatal(err)
	}
	m := patchJSON(t, p)
	containers, _, _ := unstructured.NestedSlice(m, "spec", "template", "spec", "containers")
	if len(containers) != 1 {
		t.Fatalf("containers=%v", containers)
	}
	c := containers[0].(map[string]any)
	if c["name"] != "app" || c["image"] != "nginx:1.27" {
		t.Fatalf("container=%v", c)
	}
	if m["metadata"].(map[string]any)["resourceVersion"] != "42" {
		t.Fatalf("rv=%v", m["metadata"])
	}
}

func TestRestartPatch(t *testing.T) {
	p, err := RestartPatch("77")
	if err != nil {
		t.Fatal(err)
	}
	m := patchJSON(t, p)
	ts, ok, _ := unstructured.NestedString(m, "spec", "template", "metadata", "annotations", "kubectl.kubernetes.io/restartedAt")
	if !ok || ts == "" {
		t.Fatalf("restart annotation missing: %v", m)
	}
	if rv, _, _ := unstructured.NestedString(m, "metadata", "resourceVersion"); rv != "77" {
		t.Fatalf("resourceVersion=%q", rv)
	}
}

func workloadObj(containers ...string) *unstructured.Unstructured {
	cs := make([]any, 0, len(containers))
	for _, n := range containers {
		cs = append(cs, map[string]any{"name": n, "image": "img:1"})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{"containers": cs},
			},
		},
	}}
}

func TestContainerInPodTemplate(t *testing.T) {
	obj := workloadObj("app", "sidecar")
	if !ContainerInPodTemplate(obj, "app") || !ContainerInPodTemplate(obj, "sidecar") {
		t.Fatal("existing containers must be found")
	}
	if ContainerInPodTemplate(obj, "missing") {
		t.Fatal("absent container must not be found")
	}
	if ContainerInPodTemplate(&unstructured.Unstructured{Object: map[string]any{}}, "x") {
		t.Fatal("empty object must not find anything")
	}
	// initContainers count too.
	init := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"initContainers": []any{map[string]any{"name": "init"}},
		}}},
	}}
	if !ContainerInPodTemplate(init, "init") {
		t.Fatal("initContainer should be found")
	}
}

func TestSSHPatchResource(t *testing.T) {
	runner := &sshStubRunner{fn: func([]string) (string, string, error) {
		return `{"kind":"Deployment","metadata":{"name":"d"}}`, "", nil
	}}
	rt := &sshKubernetes{runner: runner, cluster: Cluster{}}
	def, _ := kube.LookupResource("apps", "v1", "deployments")
	obj, err := rt.PatchResource(context.Background(), def, "prod", "d", types.MergePatchType, []byte(`{"spec":{"replicas":3}}`), metav1.PatchOptions{})
	if err != nil || obj.GetName() != "d" {
		t.Fatalf("obj=%v err=%v", obj, err)
	}
	call := runner.calls[0]
	for _, frag := range []string{"patch deployments.v1.apps d", "--type=merge", "-n prod", "-o json"} {
		if !strings.Contains(call, frag) {
			t.Fatalf("call %q missing %q", call, frag)
		}
	}
	runner.calls = nil
	_, err = rt.PatchResource(context.Background(), def, "prod", "d", types.JSONPatchType, []byte(`[{"op":"test","path":"/metadata/uid","value":"u"}]`), metav1.PatchOptions{})
	if err != nil || len(runner.calls) != 1 || !strings.Contains(runner.calls[0], "--type=json") {
		t.Fatalf("JSON patch kubectl command=%v error=%v", runner.calls, err)
	}
	runner.calls = nil
	if _, err := rt.PatchResource(context.Background(), def, "prod", "d", types.ApplyPatchType, []byte(`{}`), metav1.PatchOptions{}); !errors.Is(err, ErrValidation) || len(runner.calls) != 0 {
		t.Fatalf("unsupported patch reached kubectl: calls=%v error=%v", runner.calls, err)
	}
}

// TestSSHDeleteResourceVersionGuard proves the SSH delete path enforces
// a resourceVersion precondition itself: stale → Conflict, matching →
// get then delete, both through kubectl.
func TestSSHDeleteResourceVersionGuard(t *testing.T) {
	rv := "9"
	opts := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{ResourceVersion: &rv}}
	def, _ := kube.LookupResource("", "v1", "pods")

	// Stored rv differs → Conflict (409), and no delete is issued.
	runner := &sshStubRunner{fn: func([]string) (string, string, error) {
		return `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"p","resourceVersion":"12"}}`, "", nil
	}}
	rt := &sshKubernetes{runner: runner, cluster: Cluster{}}
	err := rt.DeleteResource(context.Background(), def, "prod", "p", opts)
	if !apierrors.IsConflict(err) {
		t.Fatalf("stale rv err=%v, want Conflict", err)
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "delete") {
			t.Fatalf("delete must not run on stale rv: %q", call)
		}
	}

	// Matching rv → get then delete.
	runner = &sshStubRunner{fn: func(args []string) (string, string, error) {
		if strings.Contains(strings.Join(args, " "), "delete") {
			return "", "", nil
		}
		return `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"p","resourceVersion":"9"}}`, "", nil
	}}
	rt = &sshKubernetes{runner: runner, cluster: Cluster{}}
	if err := rt.DeleteResource(context.Background(), def, "prod", "p", opts); err != nil {
		t.Fatalf("matching rv delete: %v", err)
	}
	if len(runner.calls) != 2 || !strings.Contains(runner.calls[0], "get") || !strings.Contains(runner.calls[1], "delete pods p") {
		t.Fatalf("calls=%v", runner.calls)
	}
}

// TestSSHUpdateResource proves the SSH whole-object path: JSON on
// stdin into `kubectl replace -f -`, namespace flag, stdout → object.
func TestSSHUpdateResource(t *testing.T) {
	runner := &sshStubRunner{fn: func([]string) (string, string, error) {
		return `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","resourceVersion":"88"}}`, "", nil
	}}
	rt := &sshKubernetes{runner: runner, cluster: Cluster{}}
	def, _ := kube.LookupResource("apps", "v1", "deployments")
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "web", "resourceVersion": "77"},
		"spec":     map[string]any{"replicas": float64(5)},
	}}
	out, err := rt.UpdateResource(context.Background(), def, "prod", obj, metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out.GetResourceVersion() != "88" {
		t.Fatalf("rv=%q", out.GetResourceVersion())
	}
	if len(runner.calls) != 1 || !strings.Contains(runner.calls[0], "kubectl replace -f -") || !strings.Contains(runner.calls[0], "-n prod") {
		t.Fatalf("calls=%v", runner.calls)
	}
	var sent map[string]any
	if err := json.Unmarshal(runner.stdin, &sent); err != nil {
		t.Fatalf("stdin not JSON: %v", err)
	}
	if sent["kind"] != "Deployment" || sent["metadata"].(map[string]any)["resourceVersion"] != "77" {
		t.Fatalf("stdin=%s", runner.stdin)
	}

	// kubectl failure → kubectlErr mapping (NotFound shown for missing).
	runner.fn = func([]string) (string, string, error) {
		return "", `Error from server (NotFound): deployments.apps "web" not found`, errors.New("exit status 1")
	}
	if _, err := rt.UpdateResource(context.Background(), def, "prod", obj, metav1.UpdateOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("notfound err=%v", err)
	}

	// Server-side dry run → kubectl replace --dry-run=server.
	runner.fn = func([]string) (string, string, error) {
		return `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","resourceVersion":"77"}}`, "", nil
	}
	runner.calls = nil
	if _, err := rt.UpdateResource(context.Background(), def, "prod", obj, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}}); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 || !strings.Contains(runner.calls[0], "--dry-run=server") {
		t.Fatalf("dry-run calls=%v", runner.calls)
	}
}

// TestMockUpdateResource freezes mock semantics for the YAML save path:
// rv "stale" → Conflict, name "missing" → NotFound, else rv bumps to 2.
func TestMockUpdateResource(t *testing.T) {
	rt := MockKubernetes{}
	def, _ := kube.LookupResource("", "v1", "configmaps")
	mk := func(name, rv string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]any{"name": name, "resourceVersion": rv},
			"data":     map[string]any{"k": "v"},
		}}
	}
	out, err := rt.UpdateResource(context.Background(), def, "prod", mk("cm1", "7"), metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out.GetResourceVersion() != "2" || out.GetNamespace() != "prod" {
		t.Fatalf("out=%v", out.Object["metadata"])
	}
	if _, err := rt.UpdateResource(context.Background(), def, "prod", mk("cm1", "stale"), metav1.UpdateOptions{}); !apierrors.IsConflict(err) {
		t.Fatalf("stale err=%v", err)
	}
	if _, err := rt.UpdateResource(context.Background(), def, "prod", mk("missing", "1"), metav1.UpdateOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("missing err=%v", err)
	}
	// Dry run validates but never persists — no rv bump.
	out, err = rt.UpdateResource(context.Background(), def, "prod", mk("cm1", "7"), metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
	if err != nil {
		t.Fatal(err)
	}
	if out.GetResourceVersion() != "7" {
		t.Fatalf("dry-run rv=%q, want submitted rv unchanged", out.GetResourceVersion())
	}
}

func TestSSHDeleteResource(t *testing.T) {
	runner := &sshStubRunner{fn: func([]string) (string, string, error) {
		return "", "", nil
	}}
	rt := &sshKubernetes{runner: runner, cluster: Cluster{}}
	def, _ := kube.LookupResource("", "v1", "pods")
	if err := rt.DeleteResource(context.Background(), def, "dev", "p1", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runner.calls[0], "delete pods p1") || !strings.Contains(runner.calls[0], "-n dev") {
		t.Fatalf("call=%q", runner.calls[0])
	}
	// kubectl stderr (NotFound) → apierrors NotFound.
	runner.fn = func([]string) (string, string, error) {
		return "", `Error from server (NotFound): pods "p9" not found`, errors.New("exit status 1")
	}
	err := rt.DeleteResource(context.Background(), def, "dev", "p9", metav1.DeleteOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("want NotFound, got %v", err)
	}
}
