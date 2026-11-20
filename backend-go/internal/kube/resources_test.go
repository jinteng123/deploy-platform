package kube

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery/cached/memory"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

func TestResourceRegistry(t *testing.T) {
	defs := AllowedResources()
	if len(defs) != 20 {
		t.Fatalf("registry size = %d, want 20 (read-only release observations included)", len(defs))
	}
	for _, d := range defs {
		got, err := LookupResource(d.Group, d.Version, d.Resource)
		if err != nil || got != d {
			t.Fatalf("lookup %v → %v, %v", d.GVR(), got, err)
		}
	}
	// 返回副本，外部修改不影响注册表
	defs[0].Kind = "Broken"
	if AllowedResources()[0].Kind != "Node" {
		t.Fatal("AllowedResources leaks registry internals")
	}
	for _, bad := range [][3]string{
		{"apps", "v1", "secrets"},
		{"", "v1", "serviceaccounts"}, // delivery uses a named internal read, not the generic browser API
		{"apps", "v2", "deployments"},
		{"", "", "pods"},
		{"", "v1", "pods/exec"},
	} {
		if _, err := LookupResource(bad[0], bad[1], bad[2]); !errors.Is(err, ErrResourceNotAllowed) {
			t.Fatalf("%v → %v, want ErrResourceNotAllowed", bad, err)
		}
	}
}

// resourceAPIs mirrors a cluster that serves the whole registry.
var resourceAPIs = []*metav1.APIResourceList{
	{GroupVersion: "v1", APIResources: []metav1.APIResource{
		{Name: "nodes", Kind: "Node"},
		{Name: "namespaces", Kind: "Namespace"},
		{Name: "pods", Kind: "Pod", Namespaced: true},
		{Name: "services", Kind: "Service", Namespaced: true},
		{Name: "resourcequotas", Kind: "ResourceQuota", Namespaced: true},
		{Name: "configmaps", Kind: "ConfigMap", Namespaced: true},
		{Name: "persistentvolumes", Kind: "PersistentVolume"},
		{Name: "persistentvolumeclaims", Kind: "PersistentVolumeClaim", Namespaced: true},
		{Name: "events", Kind: "Event", Namespaced: true},
	}},
	{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{
		{Name: "deployments", Kind: "Deployment", Namespaced: true},
		{Name: "replicasets", Kind: "ReplicaSet", Namespaced: true},
		{Name: "statefulsets", Kind: "StatefulSet", Namespaced: true},
		{Name: "daemonsets", Kind: "DaemonSet", Namespaced: true},
	}},
	{GroupVersion: "autoscaling/v1", APIResources: []metav1.APIResource{
		{Name: "horizontalpodautoscalers", Kind: "HorizontalPodAutoscaler", Namespaced: true},
	}},
	{GroupVersion: "discovery.k8s.io/v1", APIResources: []metav1.APIResource{
		{Name: "endpointslices", Kind: "EndpointSlice", Namespaced: true},
	}},
	{GroupVersion: "batch/v1", APIResources: []metav1.APIResource{
		{Name: "jobs", Kind: "Job", Namespaced: true},
		{Name: "cronjobs", Kind: "CronJob", Namespaced: true},
	}},
	{GroupVersion: "networking.k8s.io/v1", APIResources: []metav1.APIResource{
		{Name: "ingresses", Kind: "Ingress", Namespaced: true},
	}},
	{GroupVersion: "storage.k8s.io/v1", APIResources: []metav1.APIResource{
		{Name: "storageclasses", Kind: "StorageClass"},
	}},
}

func uobj(apiVersion, kind, ns, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       map[string]any{"k": "v"},
		"status":     map[string]any{"phase": "ok"},
	}}
}

// fakeResourceClient builds a Client backed by a dynamic fake for data and
// a real RESTMapper over a fake discovery carrying resourceAPIs.
func fakeResourceClient(t *testing.T, apis []*metav1.APIResourceList, objs ...runtime.Object) *Client {
	t.Helper()
	listKinds := map[schema.GroupVersionResource]string{}
	for _, d := range AllowedResources() {
		listKinds[d.GVR()] = d.Kind + "List"
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objs...)
	cs := fake.NewSimpleClientset()
	fd, ok := cs.Discovery().(*fakediscovery.FakeDiscovery)
	if !ok {
		t.Fatal("fake clientset discovery has unexpected type")
	}
	fd.Resources = apis
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(fd))
	return NewWithClientset(cs, dyn, mapper)
}

func mustLookup(t *testing.T, group, version, resource string) ResourceDef {
	t.Helper()
	d, err := LookupResource(group, version, resource)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestListResourcesNamespaced(t *testing.T) {
	c := fakeResourceClient(t, resourceAPIs,
		uobj("v1", "Pod", "dev", "p1"), uobj("v1", "Pod", "dev", "p2"), uobj("v1", "Pod", "prod", "p3"))
	pods := mustLookup(t, "", "v1", "pods")

	list, err := c.ListResources(context.Background(), pods, "dev", metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("dev pods = %d", len(list.Items))
	}
	for i := range list.Items {
		if list.Items[i].GetAPIVersion() != "v1" || list.Items[i].GetKind() != "Pod" {
			t.Fatalf("item gvk = %s", list.Items[i].GroupVersionKind())
		}
	}
	if list.GetKind() != "PodList" {
		t.Fatalf("list kind = %s", list.GetKind())
	}

	// 空 namespace → 全部命名空间
	list, err = c.ListResources(context.Background(), pods, "", metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 3 {
		t.Fatalf("all-ns pods = %d", len(list.Items))
	}
}

func TestListResourcesClusterScoped(t *testing.T) {
	c := fakeResourceClient(t, resourceAPIs, uobj("v1", "Node", "", "n1"))
	nodes := mustLookup(t, "", "v1", "nodes")
	// 集群级资源忽略 namespace 参数
	list, err := c.ListResources(context.Background(), nodes, "ignored", metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].GetName() != "n1" {
		t.Fatalf("nodes = %+v", list.Items)
	}
}

func TestGetResourceAndNotFound(t *testing.T) {
	c := fakeResourceClient(t, resourceAPIs,
		uobj("apps/v1", "Deployment", "dev", "web"))
	deploy := mustLookup(t, "apps", "v1", "deployments")

	obj, err := c.GetResource(context.Background(), deploy, "dev", "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if obj.GetAPIVersion() != "apps/v1" || obj.GetKind() != "Deployment" || obj.GetName() != "web" {
		t.Fatalf("obj = %s %s %s", obj.GetAPIVersion(), obj.GetKind(), obj.GetName())
	}
	if _, err := c.GetResource(context.Background(), deploy, "dev", "ghost", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("missing get → %v, want NotFound", err)
	}
	if _, err := c.GetResource(context.Background(), deploy, "dev", "", metav1.GetOptions{}); err == nil {
		t.Fatal("empty name must fail")
	}
}

func TestResourceUnsupportedAndUnconfigured(t *testing.T) {
	// discovery 只注册 pods：deployments 在 allowlist 但集群不支持
	onlyPods := []*metav1.APIResourceList{{GroupVersion: "v1", APIResources: []metav1.APIResource{
		{Name: "pods", Kind: "Pod", Namespaced: true},
	}}}
	c := fakeResourceClient(t, onlyPods)
	deploy := mustLookup(t, "apps", "v1", "deployments")
	if _, err := c.ListResources(context.Background(), deploy, "dev", metav1.ListOptions{}); !errors.Is(err, ErrResourceUnsupported) {
		t.Fatalf("unsupported → %v, want ErrResourceUnsupported", err)
	}

	// 无 dynamic client → 明确报错
	noDyn := NewWithClientset(fake.NewSimpleClientset(), nil, nil)
	if _, err := noDyn.ListResources(context.Background(), mustLookup(t, "", "v1", "pods"), "", metav1.ListOptions{}); err == nil {
		t.Fatal("nil dynamic client must fail")
	}

	// 无 mapper（跳过 discovery 的测试）→ 信任注册表
	c2 := fakeResourceClient(t, resourceAPIs, uobj("v1", "Pod", "dev", "p1"))
	c2.mapper = nil
	if _, err := c2.ListResources(context.Background(), mustLookup(t, "", "v1", "pods"), "dev", metav1.ListOptions{}); err != nil {
		t.Fatalf("nil mapper fallback → %v", err)
	}
}

// normalizeJSON round-trips a value through JSON so apiVersion/kind/
// metadata/spec/status compare shape-for-shape with kubectl output.
func normalizeJSON(t *testing.T, v any) map[string]any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

var parityFields = []string{"apiVersion", "kind", "metadata", "spec", "status"}

// TestKindResourceParity verifies the generic resource layer against a real
// API server: every registry entry's items must match `kubectl get -o json`
// on apiVersion, kind, metadata, spec and status. Gated to the explicitly
// named local Kind context; read-only, skipped otherwise.
func TestKindResourceParity(t *testing.T) {
	kctx := os.Getenv("KB_KIND_CONTEXT")
	if kctx == "" {
		t.Skip("set KB_KIND_CONTEXT=kind-kbdev to run against the local kind cluster")
	}
	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skip("kubectl not on PATH")
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: kctx},
	).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	c, err := newClientForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	for _, def := range AllowedResources() {
		t.Run(def.KubectlResource(), func(t *testing.T) {
			// kubectl strips metadata.managedFields by default; ask for the
			// full object so both sides carry identical fields.
			args := []string{"--context", kctx, "get", def.KubectlResource(), "--show-managed-fields", "-o", "json"}
			if def.Namespaced {
				args = append(args, "--all-namespaces")
			}
			out, err := exec.Command("kubectl", args...).Output()
			if err != nil {
				t.Fatalf("kubectl get %s: %v", def.KubectlResource(), err)
			}
			var want struct {
				Items []map[string]any `json:"items"`
			}
			if err := json.Unmarshal(out, &want); err != nil {
				t.Fatal(err)
			}

			list, err := c.ListResources(ctx, def, "", metav1.ListOptions{})
			if err != nil {
				t.Fatalf("ListResources %s: %v", def.KubectlResource(), err)
			}
			if len(list.Items) != len(want.Items) {
				t.Fatalf("items = %d, kubectl = %d", len(list.Items), len(want.Items))
			}
			byUID := map[string]map[string]any{}
			for _, it := range want.Items {
				uid, _ := it["metadata"].(map[string]any)["uid"].(string)
				byUID[uid] = it
			}
			var firstNS, firstName string
			for i := range list.Items {
				got := normalizeJSON(t, list.Items[i].Object)
				uid := list.Items[i].GetUID()
				w, ok := byUID[string(uid)]
				if !ok {
					t.Fatalf("api object %s not in kubectl output", uid)
				}
				for _, f := range parityFields {
					if !reflect.DeepEqual(got[f], w[f]) {
						t.Fatalf("%s/%s field %s differs from kubectl", def.Resource, uid, f)
					}
				}
				if firstName == "" {
					firstNS = list.Items[i].GetNamespace()
					firstName = list.Items[i].GetName()
				}
			}
			if firstName == "" {
				t.Skip("no objects to Get")
			}
			obj, err := c.GetResource(ctx, def, firstNS, firstName, metav1.GetOptions{})
			if err != nil {
				t.Fatalf("GetResource %s/%s: %v", firstNS, firstName, err)
			}
			getArgs := []string{"--context", kctx, "get", def.KubectlResource(),
				firstName, "--show-managed-fields", "-o", "json"}
			if def.Namespaced {
				getArgs = append(getArgs, "-n", firstNS)
			}
			wantObj, err := exec.Command("kubectl", getArgs...).Output()
			if err != nil {
				t.Fatalf("kubectl get one: %v", err)
			}
			var w map[string]any
			if err := json.Unmarshal(wantObj, &w); err != nil {
				t.Fatal(err)
			}
			got := normalizeJSON(t, obj.Object)
			for _, f := range parityFields {
				if !reflect.DeepEqual(got[f], w[f]) {
					t.Fatalf("get %s field %s differs from kubectl", firstName, f)
				}
			}
		})
	}
}
