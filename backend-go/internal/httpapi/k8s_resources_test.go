package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"deploy-platform/backend-go/internal/config"
	"deploy-platform/backend-go/internal/kube"
	"deploy-platform/backend-go/internal/service"

	"github.com/gin-gonic/gin"
)

// scriptRunner is a CommandRunner fake that records SSH argv and replies
// from a script, letting tests drive sshKubernetes without a real master.
type scriptRunner struct {
	mu    sync.Mutex
	calls []string
	stdin []byte
	fn    func(args []string) (string, string, error)
}

func (s *scriptRunner) Run(context.Context, string, []string, io.Reader) (string, string, error) {
	return "", "", nil
}

func (s *scriptRunner) RunSSH(_ context.Context, _ service.Cluster, args []string) (string, string, error) {
	s.mu.Lock()
	s.calls = append(s.calls, strings.Join(args, " "))
	s.mu.Unlock()
	return s.fn(args)
}

func (s *scriptRunner) RunSSHStdin(_ context.Context, _ service.Cluster, args []string, stdin io.Reader) (string, string, error) {
	data, _ := io.ReadAll(stdin)
	s.mu.Lock()
	s.calls = append(s.calls, strings.Join(args, " "))
	s.stdin = data
	s.mu.Unlock()
	return s.fn(args)
}

func (s *scriptRunner) RunSSHStream(_ context.Context, _ service.Cluster, args []string) (io.ReadCloser, error) {
	s.mu.Lock()
	s.calls = append(s.calls, strings.Join(args, " "))
	s.mu.Unlock()
	out, _, err := s.fn(args)
	return io.NopCloser(strings.NewReader(out)), err
}

func (s *scriptRunner) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// newSSHEngine wires an engine whose only cluster uses legacy_ssh, so K8s
// resource requests flow through sshKubernetes and the scripted runner.
func newSSHEngine(t *testing.T, runner service.CommandRunner) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.Config{Env: "dev", MockRuntime: false, TransferDir: t.TempDir()}
	store := service.NewStoreWithData([]service.Cluster{{
		ID: 1, Name: "legacy", EnvCode: "legacy", Environment: "dev",
		EnvironmentTier: "non_production", DeployPolicy: "manual",
		ConnectionType: "legacy_ssh", Enabled: true,
		K8sMasterHost: "h", K8sMasterPort: 22, K8sMasterUser: "u",
		DefaultNamespace: "ns0",
	}}, nil, nil, nil, nil)
	k8s := service.NewK8sService(cfg, runner, nil)
	worker := service.NewWorker(service.NewPipeline(cfg, store, runner, k8s), 1)
	t.Cleanup(worker.Close)
	seedTestAuth(t, store)
	return newEngine(cfg, store, worker, k8s, service.NewCDService(cfg, store, worker), nil, nil)
}

func TestResourceTypesEndpoint(t *testing.T) {
	_, e, _ := newTestApp(t)
	w := do(t, e, "GET", "/api/v1/k8s/resource-types", nil)
	if w.Code != 200 {
		t.Fatalf("resource-types → %d %s", w.Code, w.Body.String())
	}
	var defs []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &defs); err != nil {
		t.Fatal(err)
	}
	if len(defs) != len(kube.AllowedResources()) {
		t.Fatalf("allowlist size = %d, want %d", len(defs), len(kube.AllowedResources()))
	}
	byResource := map[string]map[string]any{}
	for _, d := range defs {
		byResource[d["resource"].(string)] = d
	}
	if byResource["nodes"]["namespaced"] != false {
		t.Fatalf("nodes must be cluster-scoped: %v", byResource["nodes"])
	}
	if byResource["pods"]["namespaced"] != true {
		t.Fatalf("pods must be namespaced: %v", byResource["pods"])
	}
	// Secrets are allowlisted since step 7 with masked reads; create and
	// delete are allowed (create submits stringData, delete touches no
	// data), but editable stays off so masked data can never round-trip.
	sec, ok := byResource["secrets"]
	if !ok {
		t.Fatal("secrets must be allowlisted (masked read)")
	}
	for _, flag := range []string{"scalable", "imageable", "restartable", "editable"} {
		if sec[flag] == true {
			t.Fatalf("secrets %s must be false: %v", flag, sec)
		}
	}
	if sec["deletable"] != true || sec["creatable"] != true {
		t.Fatalf("secrets deletable/creatable must be true: %v", sec)
	}
	if byResource["deployments"]["editable"] != true || byResource["nodes"]["editable"] != false {
		t.Fatalf("editable flags wrong: %v", byResource)
	}
	// PVC/ConfigMap/Secret are the creatable kinds — the create-dialog
	// gate. Everything else (e.g. deployments) stays non-creatable.
	for _, r := range []string{"persistentvolumeclaims", "configmaps", "secrets"} {
		if byResource[r]["creatable"] != true {
			t.Fatalf("%s must be creatable: %v", r, byResource[r])
		}
	}
	if byResource["deployments"]["creatable"] != false {
		t.Fatalf("creatable flags wrong: %v", byResource)
	}
}

func TestResourceListMockValidation(t *testing.T) {
	_, e, _ := newTestApp(t)

	cases := []struct {
		name string
		path string
		want int
	}{
		{"no cluster_id", "/api/v1/k8s/resources?version=v1&resource=pods", 400},
		{"bad cluster_id", "/api/v1/k8s/resources?cluster_id=x&version=v1&resource=pods", 400},
		{"unknown cluster", "/api/v1/k8s/resources?cluster_id=999&version=v1&resource=pods", 404},
		{"no version", "/api/v1/k8s/resources?cluster_id=1&resource=pods", 400},
		{"no resource", "/api/v1/k8s/resources?cluster_id=1&version=v1", 400},
		{"apps/v1 secrets still rejected", "/api/v1/k8s/resources?cluster_id=1&group=apps&version=v1&resource=secrets", 400},
		{"unknown resource", "/api/v1/k8s/resources?cluster_id=1&version=v1&resource=foos", 400},
		{"wrong group", "/api/v1/k8s/resources?cluster_id=1&group=apps&version=v1&resource=pods", 400},
		{"bad namespace", "/api/v1/k8s/resources?cluster_id=1&version=v1&resource=pods&namespace=Bad_NS", 400},
		{"bad all_namespaces", "/api/v1/k8s/resources?cluster_id=1&version=v1&resource=pods&all_namespaces=yes", 400},
	}
	for _, tc := range cases {
		if w := do(t, e, "GET", tc.path, nil); w.Code != tc.want {
			t.Fatalf("%s → %d (want %d) %s", tc.name, w.Code, tc.want, w.Body.String())
		}
	}
}

func TestResourceListMockHappyPath(t *testing.T) {
	_, e, _ := newTestApp(t)

	w := do(t, e, "GET", "/api/v1/k8s/resources?cluster_id=1&version=v1&resource=pods", nil)
	if w.Code != 200 {
		t.Fatalf("list → %d %s", w.Code, w.Body.String())
	}
	var list map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list["kind"] != "PodList" || list["apiVersion"] != "v1" {
		t.Fatalf("list envelope: %v %v", list["apiVersion"], list["kind"])
	}
	items, _ := list["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items=%v", items)
	}
	first := items[0].(map[string]any)
	meta := first["metadata"].(map[string]any)
	if first["kind"] != "Pod" || meta["namespace"] != "default" {
		t.Fatalf("item: %v", first)
	}

	// all_namespaces is accepted on list; cluster-scoped resources ignore ns.
	if w := do(t, e, "GET", "/api/v1/k8s/resources?cluster_id=1&version=v1&resource=pods&all_namespaces=true", nil); w.Code != 200 {
		t.Fatalf("all_namespaces list → %d %s", w.Code, w.Body.String())
	}
	w = do(t, e, "GET", "/api/v1/k8s/resources?cluster_id=1&version=v1&resource=nodes", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "NodeList") {
		t.Fatalf("nodes list → %d %s", w.Code, w.Body.String())
	}
	// namespace on a cluster-scoped resource is ignored, not an error.
	if w := do(t, e, "GET", "/api/v1/k8s/resources?cluster_id=1&version=v1&resource=nodes&namespace=whatever", nil); w.Code != 200 {
		t.Fatalf("nodes+ns → %d %s", w.Code, w.Body.String())
	}
}

func TestResourceDetailMock(t *testing.T) {
	_, e, _ := newTestApp(t)

	w := do(t, e, "GET", "/api/v1/k8s/resources/mock-pods-1?cluster_id=1&version=v1&resource=pods&namespace=dev", nil)
	if w.Code != 200 {
		t.Fatalf("detail → %d %s", w.Code, w.Body.String())
	}
	var obj map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
		t.Fatal(err)
	}
	meta := obj["metadata"].(map[string]any)
	if obj["kind"] != "Pod" || meta["name"] != "mock-pods-1" || meta["namespace"] != "dev" {
		t.Fatalf("obj: %v", obj)
	}

	if w := do(t, e, "GET", "/api/v1/k8s/resources/missing?cluster_id=1&version=v1&resource=pods", nil); w.Code != 404 {
		t.Fatalf("missing → %d, want 404", w.Code)
	}
	// all_namespaces makes no sense on a detail fetch.
	if w := do(t, e, "GET", "/api/v1/k8s/resources/x?cluster_id=1&version=v1&resource=pods&all_namespaces=true", nil); w.Code != 400 {
		t.Fatalf("all_namespaces detail → %d, want 400", w.Code)
	}
	// Cluster-scoped detail needs no namespace.
	if w := do(t, e, "GET", "/api/v1/k8s/resources/kbdev-control-plane?cluster_id=1&version=v1&resource=nodes", nil); w.Code != 200 {
		t.Fatalf("node detail → %d %s", w.Code, w.Body.String())
	}
}

const sshPodListJSON = `{"apiVersion":"v1","kind":"PodList","items":[{"apiVersion":"v1","kind":"Pod","metadata":{"name":"p1","namespace":"app"}}]}`

// TestResourceSSHRuntime exercises the generic resource API through the
// real sshKubernetes implementation with a scripted kubectl.
func TestResourceSSHRuntime(t *testing.T) {
	runner := &scriptRunner{fn: func(args []string) (string, string, error) {
		return sshPodListJSON, "", nil
	}}
	e := newSSHEngine(t, runner)

	w := do(t, e, "GET", "/api/v1/k8s/resources?cluster_id=1&version=v1&resource=pods&namespace=app&label_selector=app%3Dx&field_selector=spec.nodeName%3Dn1", nil)
	if w.Code != 200 {
		t.Fatalf("ssh list → %d %s", w.Code, w.Body.String())
	}
	calls := runner.recorded()
	if len(calls) != 1 {
		t.Fatalf("calls=%v", calls)
	}
	for _, frag := range []string{"kubectl get pods", "-n app", "-l app=x", "--field-selector spec.nodeName=n1", "-o json"} {
		if !strings.Contains(calls[0], frag) {
			t.Fatalf("call %q missing %q", calls[0], frag)
		}
	}
	var list map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list["items"].([]any)) != 1 {
		t.Fatalf("items: %v", list["items"])
	}
}

func TestResourceSSHArgShapes(t *testing.T) {
	runner := &scriptRunner{fn: func(args []string) (string, string, error) {
		return sshPodListJSON, "", nil
	}}
	e := newSSHEngine(t, runner)

	// all_namespaces=true → --all-namespaces instead of -n.
	if w := do(t, e, "GET", "/api/v1/k8s/resources?cluster_id=1&version=v1&resource=pods&all_namespaces=true", nil); w.Code != 200 {
		t.Fatalf("all-ns → %d", w.Code)
	}
	// cluster-scoped resource → no namespace flags at all.
	if w := do(t, e, "GET", "/api/v1/k8s/resources?cluster_id=1&version=v1&resource=nodes&namespace=app", nil); w.Code != 200 {
		t.Fatalf("nodes → %d", w.Code)
	}
	// detail → positional name.
	if w := do(t, e, "GET", "/api/v1/k8s/resources/p1?cluster_id=1&version=v1&resource=pods&namespace=app", nil); w.Code != 200 {
		t.Fatalf("detail → %d", w.Code)
	}

	calls := runner.recorded()
	if len(calls) != 3 {
		t.Fatalf("calls=%v", calls)
	}
	if !strings.Contains(calls[0], "--all-namespaces") || strings.Contains(calls[0], "-n ") {
		t.Fatalf("all-ns call: %q", calls[0])
	}
	if strings.Contains(calls[1], "-n") || strings.Contains(calls[1], "all-namespaces") || !strings.Contains(calls[1], "get nodes") {
		t.Fatalf("nodes call: %q", calls[1])
	}
	if !strings.Contains(calls[2], "get pods p1") || !strings.Contains(calls[2], "-n app") {
		t.Fatalf("detail call: %q", calls[2])
	}
}

// TestClusterOverviewEndpoint freezes the overview contract through the
// mock runtime: populated sections plus a metrics block that reports
// availability instead of failing the request.
func TestClusterOverviewEndpoint(t *testing.T) {
	_, e, _ := newTestApp(t)

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/api/v1/k8s/overview", 400},
		{"/api/v1/k8s/overview?cluster_id=x", 400},
		{"/api/v1/k8s/overview?cluster_id=999", 404},
		{"/api/v1/k8s/overview?cluster_id=1", 200},
	} {
		if w := do(t, e, "GET", tc.path, nil); w.Code != tc.want {
			t.Fatalf("%s → %d (want %d) %s", tc.path, w.Code, tc.want, w.Body.String())
		}
	}

	w := do(t, e, "GET", "/api/v1/k8s/overview?cluster_id=1", nil)
	var ov map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &ov); err != nil {
		t.Fatal(err)
	}
	if ov["server_version"] != "v1.29.0-mock" {
		t.Fatalf("server_version=%v", ov["server_version"])
	}
	nodes, _ := ov["nodes"].(map[string]any)
	if nodes["total"].(float64) != 2 {
		t.Fatalf("nodes=%v", nodes)
	}
	if ov["namespaces"].(float64) != 2 {
		t.Fatalf("namespaces=%v", ov["namespaces"])
	}
	workloads, _ := ov["workloads"].(map[string]any)
	if workloads["pods"].(float64) != 2 {
		t.Fatalf("workloads=%v", workloads)
	}
	metrics, _ := ov["metrics"].(map[string]any)
	if metrics["available"] != true {
		t.Fatalf("mock metrics should be available: %v", metrics)
	}
	topPods, _ := ov["top_pods"].([]any)
	if len(topPods) == 0 {
		t.Fatal("mock metrics should yield top pods")
	}
}

// TestResourceSSHErrorMapping freezes kubectl stderr → HTTP semantics:
// (NotFound) → 404, (Forbidden) → 403, anything else → 500.
func TestResourceSSHErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   int
	}{
		{"not found", `Error from server (NotFound): pods "p9" not found`, 404},
		{"forbidden", "Error from server (Forbidden): pods is forbidden: User cannot list", 403},
		{"generic", "connection refused", 500},
	}
	for _, tc := range cases {
		runner := &scriptRunner{fn: func(args []string) (string, string, error) {
			return "", tc.stderr, errors.New("exit status 1")
		}}
		e := newSSHEngine(t, runner)
		if w := do(t, e, "GET", "/api/v1/k8s/resources?cluster_id=1&version=v1&resource=pods", nil); w.Code != tc.want {
			t.Fatalf("%s → %d (want %d) %s", tc.name, w.Code, tc.want, w.Body.String())
		}
	}

	// Unparseable kubectl output → 500, not a panic.
	runner := &scriptRunner{fn: func(args []string) (string, string, error) {
		return "not json", "", nil
	}}
	e := newSSHEngine(t, runner)
	if w := do(t, e, "GET", "/api/v1/k8s/resources?cluster_id=1&version=v1&resource=pods", nil); w.Code != 500 {
		t.Fatalf("bad json → %d", w.Code)
	}
}
