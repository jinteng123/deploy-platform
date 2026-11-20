package httpapi

import (
	"deploy-platform/backend-go/internal/kube"
	"deploy-platform/backend-go/internal/service"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestWriteScaleContract freezes scale semantics through the mock
// runtime: happy path merges the patch into the returned object,
// capability and body violations → 400, stale resourceVersion → 409.
func TestWriteScaleContract(t *testing.T) {
	_, e, _ := newTestApp(t)
	scale := func(res, name, ns string) string {
		q := "cluster_id=1&group=apps&version=v1&resource=" + res + "&namespace=" + ns
		return "/api/v1/k8s/resources/" + name + "/scale?" + q
	}

	w := do(t, e, "POST", scale("deployments", "d", "default"), map[string]any{"replicas": 3})
	if w.Code != 200 {
		t.Fatalf("scale → %d %s", w.Code, w.Body.String())
	}
	var obj map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
		t.Fatal(err)
	}
	if obj["spec"].(map[string]any)["replicas"].(float64) != 3 {
		t.Fatalf("patched object: %v", obj)
	}

	for _, tc := range []struct {
		name string
		path string
		body any
		want int
	}{
		{"pods not scalable", "/api/v1/k8s/resources/p/scale?cluster_id=1&version=v1&resource=pods&namespace=default", map[string]any{"replicas": 1}, 400},
		{"missing replicas", scale("deployments", "d", "default"), map[string]any{}, 400},
		{"negative replicas", scale("deployments", "d", "default"), map[string]any{"replicas": -1}, 400},
		{"over max replicas", scale("deployments", "d", "default"), map[string]any{"replicas": 51}, 400},
		{"stale resourceVersion → 409", scale("deployments", "d", "default"), map[string]any{"replicas": 2, "resource_version": "stale"}, 409},
		{"missing cluster", "/api/v1/k8s/resources/d/scale?group=apps&version=v1&resource=deployments&namespace=default", map[string]any{"replicas": 1}, 400},
		{"unknown cluster", "/api/v1/k8s/resources/d/scale?cluster_id=999&group=apps&version=v1&resource=deployments&namespace=default", map[string]any{"replicas": 1}, 404},
		{"non-allowlisted GVR", "/api/v1/k8s/resources/d/scale?cluster_id=1&group=apps&version=v1&resource=secrets&namespace=default", map[string]any{"replicas": 1}, 400},
	} {
		if w := do(t, e, "POST", tc.path, tc.body); w.Code != tc.want {
			t.Fatalf("%s → %d (want %d) %s", tc.name, w.Code, tc.want, w.Body.String())
		}
	}
}

// TestWriteRestartAndDeleteContract covers rollout-restart capability
// gating and the delete allowlist + precondition semantics.
func TestWriteRestartAndDeleteContract(t *testing.T) {
	_, e, _ := newTestApp(t)

	w := do(t, e, "POST", "/api/v1/k8s/resources/d/restart?cluster_id=1&group=apps&version=v1&resource=deployments&namespace=default", nil)
	if w.Code != 200 {
		t.Fatalf("restart → %d %s", w.Code, w.Body.String())
	}
	// The restartedAt annotation must land in the returned object.
	var obj map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
		t.Fatal(err)
	}
	anns := obj["spec"].(map[string]any)["template"].(map[string]any)["metadata"].(map[string]any)["annotations"]
	if anns == nil {
		t.Fatalf("no restart annotation merged: %v", obj)
	}

	if w := do(t, e, "POST", "/api/v1/k8s/resources/p/restart?cluster_id=1&version=v1&resource=pods&namespace=default", nil); w.Code != 400 {
		t.Fatalf("restart pods → %d, want 400", w.Code)
	}

	// delete: pods deletable, nodes not (cluster-scoped, high-risk).
	if w := do(t, e, "DELETE", "/api/v1/k8s/resources/p1?cluster_id=1&version=v1&resource=pods&namespace=default", nil); w.Code != 200 {
		t.Fatalf("delete pod → %d %s", w.Code, w.Body.String())
	}
	if w := do(t, e, "DELETE", "/api/v1/k8s/resources/n1?cluster_id=1&version=v1&resource=nodes", nil); w.Code != 400 {
		t.Fatalf("delete node → %d, want 400 (not deletable)", w.Code)
	}
	if w := do(t, e, "DELETE", "/api/v1/k8s/resources/ns1?cluster_id=1&version=v1&resource=namespaces", nil); w.Code != 400 {
		t.Fatalf("delete namespace → %d, want 400 (not deletable)", w.Code)
	}
	if w := do(t, e, "DELETE", "/api/v1/k8s/resources/missing?cluster_id=1&version=v1&resource=pods&namespace=default", nil); w.Code != 404 {
		t.Fatalf("delete missing → %d, want 404", w.Code)
	}
	if w := do(t, e, "DELETE", "/api/v1/k8s/resources/p1?cluster_id=1&version=v1&resource=pods&namespace=default&resource_version=stale", nil); w.Code != 409 {
		t.Fatalf("delete stale rv → %d, want 409", w.Code)
	}
}

// TestWriteImageContract covers the image endpoint's validation and the
// container-existence guard (mock Deployments include the app container).
func TestWriteImageContract(t *testing.T) {
	_, e, _ := newTestApp(t)
	base := "/api/v1/k8s/resources/d/image?cluster_id=1&group=apps&version=v1&resource=deployments&namespace=default"

	if w := do(t, e, "POST", base, map[string]any{"container": "unknown-container", "image": "nginx:1.27"}); w.Code != 400 {
		t.Fatalf("image unknown container → %d, want 400", w.Code)
	}
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"missing container", map[string]any{"image": "x"}},
		{"missing image", map[string]any{"container": "app"}},
		{"blank image", map[string]any{"container": "app", "image": "  "}},
	} {
		if w := do(t, e, "POST", base, tc.body); w.Code != 400 {
			t.Fatalf("%s → %d, want 400", tc.name, w.Code)
		}
	}
	// pods are not imageable.
	if w := do(t, e, "POST", "/api/v1/k8s/resources/p/image?cluster_id=1&version=v1&resource=pods&namespace=default", map[string]any{"container": "c", "image": "i"}); w.Code != 400 {
		t.Fatalf("image pods → %d, want 400", w.Code)
	}
}

// ---- Step 7: YAML edit (PUT whole manifest) ----

// TestParseManifest freezes the manifest validation contract: GVK must
// equal the allowlisted def, name/namespace must match the request,
// resourceVersion is mandatory, server-owned fields are stripped.
func TestParseManifest(t *testing.T) {
	def, _ := kube.LookupResource("apps", "v1", "deployments")

	good := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: prod
  resourceVersion: "77"
  managedFields: [{x: 1}]
  uid: abc
spec:
  replicas: 3
`
	obj, err := parseManifest(good, def, "prod", "web")
	if err != nil {
		t.Fatal(err)
	}
	if obj.GetName() != "web" || obj.GetNamespace() != "prod" || obj.GetResourceVersion() != "77" {
		t.Fatalf("obj=%v", obj.Object["metadata"])
	}
	if _, found, _ := unstructured.NestedSlice(obj.Object, "metadata", "managedFields"); found {
		t.Fatal("managedFields must be stripped")
	}
	if obj.GetUID() != "" {
		t.Fatal("uid must be stripped")
	}

	for _, tc := range []struct {
		name, manifest, defNS, pathName string
	}{
		{"empty", "", "prod", "web"},
		{"bad yaml", "apiVersion: [unclosed", "prod", "web"},
		{"wrong kind", "apiVersion: apps/v1\nkind: StatefulSet\nmetadata: {name: web, resourceVersion: '1'}", "prod", "web"},
		{"wrong apiVersion", "apiVersion: v1\nkind: Deployment\nmetadata: {name: web, resourceVersion: '1'}", "prod", "web"},
		{"missing name", "apiVersion: apps/v1\nkind: Deployment\nmetadata: {resourceVersion: '1'}", "prod", "web"},
		{"name mismatch", "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: other, resourceVersion: '1'}", "prod", "web"},
		{"ns mismatch", "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web, namespace: other, resourceVersion: '1'}", "prod", "web"},
		{"missing rv", "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web}", "prod", "web"},
	} {
		if _, err := parseManifest(tc.manifest, def, tc.defNS, tc.pathName); err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
	}
	// namespace omitted in manifest → filled with the resolved one.
	obj, err = parseManifest("apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web, resourceVersion: '9'}", def, "prod", "web")
	if err != nil || obj.GetNamespace() != "prod" {
		t.Fatalf("ns fill obj=%v err=%v", obj, err)
	}
}

// TestPutManifestContract covers the PUT endpoint through the mock
// runtime: happy path returns the updated object, capability gating,
// validation failures, and the stale-rv → 409 mapping.
func TestPutManifestContract(t *testing.T) {
	_, e, _ := newTestApp(t)
	put := func(res, name, ns, manifest string) *httptest.ResponseRecorder {
		q := fmt.Sprintf("cluster_id=1&group=apps&version=v1&resource=%s&namespace=%s", res, ns)
		return do(t, e, "PUT", "/api/v1/k8s/resources/"+name+"?"+q, map[string]any{"manifest": manifest})
	}
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  resourceVersion: "77"
spec:
  replicas: 4
`
	w := put("deployments", "web", "prod", manifest)
	if w.Code != 200 {
		t.Fatalf("put → %d %s", w.Code, w.Body.String())
	}
	var obj map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
		t.Fatal(err)
	}
	if obj["spec"].(map[string]any)["replicas"].(float64) != 4 {
		t.Fatalf("spec not applied: %v", obj)
	}
	if rv := obj["metadata"].(map[string]any)["resourceVersion"]; rv != "2" {
		t.Fatalf("mock update should bump rv, got %v", rv)
	}

	stale := strings.Replace(manifest, `"77"`, `"stale"`, 1)
	if w := put("deployments", "web", "prod", stale); w.Code != 409 {
		t.Fatalf("stale rv → %d, want 409", w.Code)
	}
	if w := put("deployments", "web", "prod", "not: [yaml"); w.Code != 400 {
		t.Fatalf("bad yaml → %d, want 400", w.Code)
	}
	// events/secrets are not editable → 400 regardless of manifest.
	evQ := "cluster_id=1&version=v1&resource=events&namespace=default"
	if w := do(t, e, "PUT", "/api/v1/k8s/resources/ev1?"+evQ, map[string]any{"manifest": manifest}); w.Code != 400 {
		t.Fatalf("put events → %d, want 400", w.Code)
	}
	secQ := "cluster_id=1&version=v1&resource=secrets&namespace=default"
	if w := do(t, e, "PUT", "/api/v1/k8s/resources/s1?"+secQ, map[string]any{"manifest": manifest}); w.Code != 400 {
		t.Fatalf("put secrets → %d, want 400", w.Code)
	}
	// secrets ARE browsable now — masking applies on the read paths.
	if w := do(t, e, "GET", "/api/v1/k8s/resources?"+secQ, nil); w.Code != 200 {
		t.Fatalf("list secrets → %d", w.Code)
	}
	// secrets are deletable (delete touches no data) but still NOT
	// editable — the masked-manifest write-back boundary stays.
	if w := do(t, e, "DELETE", "/api/v1/k8s/resources/s1?"+secQ, nil); w.Code != 200 {
		t.Fatalf("delete secrets → %d, want 200", w.Code)
	}
	// pods are deletable but NOT editable — the Kuboard-aligned
	// read-only pod boundary (controller-owned objects).
	podQ := "cluster_id=1&version=v1&resource=pods&namespace=default"
	podManifest := "apiVersion: v1\nkind: Pod\nmetadata: {name: p1, resourceVersion: '7'}\n"
	if w := do(t, e, "PUT", "/api/v1/k8s/resources/p1?"+podQ, map[string]any{"manifest": podManifest}); w.Code != 400 {
		t.Fatalf("put pod → %d, want 400 (pods read-only)", w.Code)
	}
	// ?dry_run=true runs validation without persisting — the mock
	// returns the object with the submitted rv unchanged (no "2" bump).
	dryQ := "cluster_id=1&group=apps&version=v1&resource=deployments&namespace=prod&dry_run=true"
	w = do(t, e, "PUT", "/api/v1/k8s/resources/web?"+dryQ, map[string]any{"manifest": manifest})
	if w.Code != 200 {
		t.Fatalf("dry-run put → %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
		t.Fatal(err)
	}
	if rv := obj["metadata"].(map[string]any)["resourceVersion"]; rv != "77" {
		t.Fatalf("dry-run must not bump rv, got %v", rv)
	}
	// dry-run still hits validation + conflict paths.
	if w := do(t, e, "PUT", "/api/v1/k8s/resources/web?"+dryQ, map[string]any{"manifest": stale}); w.Code != 409 {
		t.Fatalf("dry-run stale rv → %d, want 409", w.Code)
	}
	if w := do(t, e, "PUT", "/api/v1/k8s/resources/web?"+dryQ, map[string]any{"manifest": "not: [yaml"}); w.Code != 400 {
		t.Fatalf("dry-run bad yaml → %d, want 400", w.Code)
	}
}

// TestPutManifestSSHRuntime exercises replace over SSH: the manifest
// JSON is piped into `kubectl replace -f -` via RunSSHStdin.
func TestPutManifestSSHRuntime(t *testing.T) {
	runner := &scriptRunner{fn: func(args []string) (string, string, error) {
		return `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","namespace":"prod","resourceVersion":"88"}}`, "", nil
	}}
	e := newSSHEngine(t, runner)
	manifest := "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web, resourceVersion: '77'}\nspec: {replicas: 2}\n"
	w := do(t, e, "PUT", "/api/v1/k8s/resources/web?cluster_id=1&group=apps&version=v1&resource=deployments&namespace=prod",
		map[string]any{"manifest": manifest})
	if w.Code != 200 {
		t.Fatalf("put → %d %s", w.Code, w.Body.String())
	}
	calls := runner.recorded()
	if len(calls) != 1 || !strings.Contains(calls[0], "replace -f -") || !strings.Contains(calls[0], "-n prod") {
		t.Fatalf("calls=%v", calls)
	}
	// The piped manifest is JSON (valid kubectl input) with the rv kept
	// as the update precondition and managedFields stripped.
	var sent map[string]any
	if err := json.Unmarshal(runner.stdin, &sent); err != nil {
		t.Fatalf("stdin is not JSON: %v", err)
	}
	if sent["kind"] != "Deployment" {
		t.Fatalf("stdin manifest=%s", runner.stdin)
	}
	meta := sent["metadata"].(map[string]any)
	if meta["resourceVersion"] != "77" || meta["namespace"] != "prod" {
		t.Fatalf("stdin metadata=%v", meta)
	}
	if _, ok := meta["managedFields"]; ok {
		t.Fatal("managedFields must be stripped before piping")
	}
}

const sshDeploymentJSON = `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","namespace":"prod","resourceVersion":"77"},
"spec":{"replicas":2,"template":{"metadata":{},"spec":{"containers":[{"name":"app","image":"nginx:1.25"},{"name":"side","image":"busy:1"}]}}}}`

// TestWriteImageSSHRuntime exercises the image endpoint end-to-end on
// sshKubernetes: GET for the container check then a strategic merge
// patch — the argv and body are frozen.
func TestWriteImageSSHRuntime(t *testing.T) {
	runner := &scriptRunner{fn: func(args []string) (string, string, error) {
		if strings.Contains(strings.Join(args, " "), "patch") {
			return sshDeploymentJSON, "", nil
		}
		return sshDeploymentJSON, "", nil
	}}
	e := newSSHEngine(t, runner)

	w := do(t, e, "POST", "/api/v1/k8s/resources/web/image?cluster_id=1&group=apps&version=v1&resource=deployments&namespace=prod",
		map[string]any{"container": "app", "image": "nginx:1.27", "resource_version": "77"})
	if w.Code != 200 {
		t.Fatalf("image → %d %s", w.Code, w.Body.String())
	}
	calls := runner.recorded()
	if len(calls) != 2 {
		t.Fatalf("calls=%v", calls)
	}
	if !strings.Contains(calls[0], "get deployments.v1.apps web") || !strings.Contains(calls[0], "-o json") {
		t.Fatalf("get call=%q", calls[0])
	}
	for _, frag := range []string{"patch deployments.v1.apps web", "--type=strategic", `"name":"app"`, `"image":"nginx:1.27"`, `"resourceVersion":"77"`, "-n prod"} {
		if !strings.Contains(calls[1], frag) {
			t.Fatalf("patch call %q missing %q", calls[1], frag)
		}
	}
}

// TestWriteImageContainerGuard proves a nonexistent container name is
// rejected client-side (400) and never reaches kubectl patch.
func TestWriteImageContainerGuard(t *testing.T) {
	runner := &scriptRunner{fn: func([]string) (string, string, error) {
		return sshDeploymentJSON, "", nil
	}}
	e := newSSHEngine(t, runner)

	w := do(t, e, "POST", "/api/v1/k8s/resources/web/image?cluster_id=1&group=apps&version=v1&resource=deployments&namespace=prod",
		map[string]any{"container": "nope", "image": "nginx:1.27"})
	if w.Code != 400 {
		t.Fatalf("unknown container → %d %s", w.Code, w.Body.String())
	}
	for _, call := range runner.recorded() {
		if strings.Contains(call, "patch") {
			t.Fatalf("patch must not be issued: %q", call)
		}
	}
}

// TestWriteSSHErrorMapping freezes kubectl stderr → HTTP semantics on
// the write paths so the SSH runtime keeps parity with the direct
// client: Forbidden → 403, NotFound → 404, Conflict (stale
// resourceVersion on replace/patch) → 409, AlreadyExists on create →
// 409, Invalid (schema/admission rejection) → 400.
func TestWriteSSHErrorMapping(t *testing.T) {
	// Forbidden on patch → 403 with the Kubernetes message surfaced.
	runner := &scriptRunner{fn: func(args []string) (string, string, error) {
		if strings.Contains(strings.Join(args, " "), "patch") {
			return "", `Error from server (Forbidden): deployments.apps "web" is forbidden: User cannot patch`, errors.New("exit status 1")
		}
		return sshDeploymentJSON, "", nil
	}}
	e := newSSHEngine(t, runner)
	w := do(t, e, "POST", "/api/v1/k8s/resources/web/scale?cluster_id=1&group=apps&version=v1&resource=deployments&namespace=prod",
		map[string]any{"replicas": 3})
	if w.Code != 403 {
		t.Fatalf("forbidden patch → %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "forbidden") && !strings.Contains(w.Body.String(), "Forbidden") {
		t.Fatalf("forbidden detail should surface: %s", w.Body.String())
	}

	// NotFound on delete → 404.
	runner = &scriptRunner{fn: func([]string) (string, string, error) {
		return "", `Error from server (NotFound): pods "p9" not found`, errors.New("exit status 1")
	}}
	e = newSSHEngine(t, runner)
	w = do(t, e, "DELETE", "/api/v1/k8s/resources/p9?cluster_id=1&version=v1&resource=pods&namespace=prod", nil)
	if w.Code != 404 {
		t.Fatalf("notfound delete → %d %s", w.Code, w.Body.String())
	}

	// Conflict on replace (stale resourceVersion) → 409, same as the
	// direct client — the frontend's isConflict() refresh hint depends
	// on this status, and the audit must classify it as "conflict".
	runner = &scriptRunner{fn: func([]string) (string, string, error) {
		return "", `Error from server (Conflict): Operation cannot be fulfilled on deployments.apps "web": the object has been modified; please apply your changes to the latest version and try again`, errors.New("exit status 1")
	}}
	e = newSSHEngine(t, runner)
	w = do(t, e, "PUT", "/api/v1/k8s/resources/web?cluster_id=1&group=apps&version=v1&resource=deployments&namespace=prod",
		map[string]any{"manifest": "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web, resourceVersion: '77'}\nspec: {replicas: 2}\n"})
	if w.Code != 409 {
		t.Fatalf("conflict replace → %d %s", w.Code, w.Body.String())
	}

	// Conflict on patch (scale with a stale precondition) → 409 too.
	runner = &scriptRunner{fn: func([]string) (string, string, error) {
		return "", `Error from server (Conflict): Operation cannot be fulfilled on deployments.apps "web": the object has been modified`, errors.New("exit status 1")
	}}
	e = newSSHEngine(t, runner)
	w = do(t, e, "POST", "/api/v1/k8s/resources/web/scale?cluster_id=1&group=apps&version=v1&resource=deployments&namespace=prod",
		map[string]any{"replicas": 3})
	if w.Code != 409 {
		t.Fatalf("conflict patch → %d %s", w.Code, w.Body.String())
	}

	// AlreadyExists on create → 409 (duplicate name on an SSH cluster).
	runner = &scriptRunner{fn: func([]string) (string, string, error) {
		return "", `Error from server (AlreadyExists): persistentvolumeclaims "data" already exists`, errors.New("exit status 1")
	}}
	e = newSSHEngine(t, runner)
	w = do(t, e, "POST", "/api/v1/k8s/resources?cluster_id=1&version=v1&resource=persistentvolumeclaims&namespace=prod",
		map[string]any{"manifest": "apiVersion: v1\nkind: PersistentVolumeClaim\nmetadata: {name: data}\nspec: {accessModes: [ReadWriteOnce]}\n"})
	if w.Code != 409 {
		t.Fatalf("alreadyexists create → %d %s", w.Code, w.Body.String())
	}

	// Invalid on replace (apiserver schema/admission rejection) → 400,
	// with kubectl's message preserved so the user sees which field.
	runner = &scriptRunner{fn: func([]string) (string, string, error) {
		return "", `Error from server (Invalid): Deployment.apps "web" is invalid: spec.replicas: Invalid value: "x"`, errors.New("exit status 1")
	}}
	e = newSSHEngine(t, runner)
	w = do(t, e, "PUT", "/api/v1/k8s/resources/web?cluster_id=1&group=apps&version=v1&resource=deployments&namespace=prod",
		map[string]any{"manifest": "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web, resourceVersion: '77'}\nspec: {replicas: 2}\n"})
	if w.Code != 400 {
		t.Fatalf("invalid replace → %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "spec.replicas") {
		t.Fatalf("invalid detail should preserve kubectl's field info: %s", w.Body.String())
	}
}

// TestPostManifestContract freezes the create endpoint's contract
// through the mock runtime: happy path → 201 with server-stamped fields,
// dry_run → 200 with no stamps, non-creatable kinds and malformed
// manifests → 400 denied, viewer → 403, name "exists" → 409.
func TestPostManifestContract(t *testing.T) {
	app, e, _ := newTestApp(t)
	post := func(q, manifest string, token ...string) *httptest.ResponseRecorder {
		return do(t, e, "POST", "/api/v1/k8s/resources?"+q, map[string]any{"manifest": manifest}, token...)
	}
	pvcQ := "cluster_id=1&version=v1&resource=persistentvolumeclaims&namespace=prod"
	manifest := `apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: data
spec:
  accessModes: [ReadWriteOnce]
  resources: {requests: {storage: 2Gi}}
`
	w := post(pvcQ, manifest)
	if w.Code != 201 {
		t.Fatalf("create → %d %s", w.Code, w.Body.String())
	}
	var obj map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
		t.Fatal(err)
	}
	if obj["kind"] != "PersistentVolumeClaim" {
		t.Fatalf("echoed kind: %v", obj["kind"])
	}
	meta := obj["metadata"].(map[string]any)
	if meta["name"] != "data" || meta["namespace"] != "prod" {
		t.Fatalf("echoed metadata: %v", meta)
	}
	if meta["resourceVersion"] != "1" {
		t.Fatalf("mock create should stamp rv=1, got %v", meta["resourceVersion"])
	}
	if !hasAudit(app.store, "create", "data", service.AuditSuccess) {
		t.Fatalf("create audit missing: %+v", app.store.Audits(5))
	}

	// dry_run=true → 200 and no server-stamped fields (the mock only
	// stamps on real creates), audited under its own action.
	dryQ := pvcQ + "&dry_run=true"
	w = post(dryQ, manifest)
	if w.Code != 200 {
		t.Fatalf("dry-run create → %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
		t.Fatal(err)
	}
	if _, ok := obj["metadata"].(map[string]any)["resourceVersion"]; ok {
		t.Fatalf("dry-run must not stamp rv: %v", obj["metadata"])
	}
	if !hasAudit(app.store, "create_dryrun", "data", service.AuditSuccess) {
		t.Fatalf("create_dryrun audit missing: %+v", app.store.Audits(5))
	}

	// deployments are not creatable → deny before touching the runtime.
	depQ := "cluster_id=1&group=apps&version=v1&resource=deployments&namespace=prod"
	depManifest := "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web}\n"
	w = post(depQ, depManifest)
	if w.Code != 400 {
		t.Fatalf("create deployments → %d, want 400", w.Code)
	}
	if !hasAudit(app.store, "create", "", service.AuditDenied) {
		t.Fatalf("create-denied audit missing: %+v", app.store.Audits(5))
	}

	for _, tc := range []struct {
		name     string
		manifest string
	}{
		{"kind mismatch", "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: data2}\n"},
		{"resourceVersion present", strings.Replace(manifest, "name: data", "name: data2\n  resourceVersion: \"9\"", 1)},
		{"namespace mismatch", strings.Replace(manifest, "name: data", "name: data3\n  namespace: other", 1)},
		{"invalid name", strings.Replace(manifest, "name: data", "name: Bad_Name", 1)},
		{"empty name", "apiVersion: v1\nkind: PersistentVolumeClaim\nmetadata: {}\n"},
		{"bad yaml", "not: [yaml"},
	} {
		if w := post(pvcQ, tc.manifest); w.Code != 400 {
			t.Fatalf("%s → %d, want 400 (%s)", tc.name, w.Code, w.Body.String())
		}
	}

	// A duplicate name surfaces the apiserver's AlreadyExists → 409.
	w = post(pvcQ, strings.Replace(manifest, "name: data", "name: exists", 1))
	if w.Code != 409 {
		t.Fatalf("duplicate create → %d, want 409 (%s)", w.Code, w.Body.String())
	}

	// viewer role cannot reach the endpoint at all.
	viewer := seedUser(t, app.store, "pvcviewer", service.RoleViewer)
	if w := post(pvcQ, manifest, viewer); w.Code != 403 {
		t.Fatalf("viewer create → %d, want 403", w.Code)
	}
}

// TestPostManifestConfigMapSecret freezes the create contract for the
// other two creatable kinds: ConfigMap → 201 with its data echoed back
// verbatim, Secret → 201 with every data/stringData value masked to
// "***" — the create response obeys the same no-leak rule as reads.
// Secret delete is now allowed and audited.
func TestPostManifestConfigMapSecret(t *testing.T) {
	app, e, _ := newTestApp(t)
	post := func(q, manifest string, token ...string) *httptest.ResponseRecorder {
		return do(t, e, "POST", "/api/v1/k8s/resources?"+q, map[string]any{"manifest": manifest}, token...)
	}

	// ConfigMap create → 201, data echoed back verbatim (no masking).
	cmQ := "cluster_id=1&version=v1&resource=configmaps&namespace=prod"
	cmManifest := `apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
data:
  app.yaml: |
    listen: 8080
  debug: "false"
`
	w := post(cmQ, cmManifest)
	if w.Code != 201 {
		t.Fatalf("configmap create → %d %s", w.Code, w.Body.String())
	}
	var obj map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
		t.Fatal(err)
	}
	data := obj["data"].(map[string]any)
	if data["debug"] != "false" || !strings.Contains(data["app.yaml"].(string), "listen: 8080") {
		t.Fatalf("configmap data mangled: %v", data)
	}
	if !hasAudit(app.store, "create", "app-config", service.AuditSuccess) {
		t.Fatalf("configmap create audit missing: %+v", app.store.Audits(5))
	}

	// Secret create via stringData → 201, but the response can never
	// carry the plaintext (or any) values: MaskSecretData masks both
	// data and stringData fields on the way out.
	secQ := "cluster_id=1&version=v1&resource=secrets&namespace=prod"
	secManifest := `apiVersion: v1
kind: Secret
metadata:
  name: db-credentials
type: Opaque
stringData:
  username: admin
  password: s3cret
`
	w = post(secQ, secManifest)
	if w.Code != 201 {
		t.Fatalf("secret create → %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "s3cret") || strings.Contains(w.Body.String(), "admin") {
		t.Fatalf("secret create response leaks plaintext: %s", w.Body.String())
	}
	var secObj map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &secObj); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"data", "stringData"} {
		vals, ok := secObj[field].(map[string]any)
		if !ok {
			continue
		}
		for k, v := range vals {
			if v != "***" {
				t.Fatalf("secret %s[%s] = %v, want masked", field, k, v)
			}
		}
	}
	if secObj["stringData"] == nil && secObj["data"] == nil {
		t.Fatal("secret response should still carry the (masked) data keys")
	}
	if !hasAudit(app.store, "create", "db-credentials", service.AuditSuccess) {
		t.Fatalf("secret create audit missing: %+v", app.store.Audits(5))
	}

	// Secret delete is allowed now — and audited like every write.
	w = do(t, e, "DELETE", "/api/v1/k8s/resources/db-credentials?"+secQ, nil)
	if w.Code != 200 {
		t.Fatalf("secret delete → %d %s", w.Code, w.Body.String())
	}
	if !hasAudit(app.store, "delete", "db-credentials", service.AuditSuccess) {
		t.Fatalf("secret delete audit missing: %+v", app.store.Audits(5))
	}
}

// TestPostManifestSSHRuntime exercises create over SSH: the manifest
// JSON is piped into `kubectl create -f -` via RunSSHStdin.
func TestPostManifestSSHRuntime(t *testing.T) {
	runner := &scriptRunner{fn: func(args []string) (string, string, error) {
		return `{"apiVersion":"v1","kind":"PersistentVolumeClaim","metadata":{"name":"data","namespace":"prod","uid":"u1"}}`, "", nil
	}}
	e := newSSHEngine(t, runner)
	manifest := "apiVersion: v1\nkind: PersistentVolumeClaim\nmetadata: {name: data, managedFields: [{x: 1}]}\nspec: {accessModes: [ReadWriteOnce]}\nstatus: {phase: Pending}\n"
	w := do(t, e, "POST", "/api/v1/k8s/resources?cluster_id=1&version=v1&resource=persistentvolumeclaims&namespace=prod",
		map[string]any{"manifest": manifest})
	if w.Code != 201 {
		t.Fatalf("create → %d %s", w.Code, w.Body.String())
	}
	calls := runner.recorded()
	if len(calls) != 1 || !strings.Contains(calls[0], "create -f -") || !strings.Contains(calls[0], "-o json") || !strings.Contains(calls[0], "-n prod") {
		t.Fatalf("calls=%v", calls)
	}
	var sent map[string]any
	if err := json.Unmarshal(runner.stdin, &sent); err != nil {
		t.Fatalf("stdin is not JSON: %v", err)
	}
	if sent["kind"] != "PersistentVolumeClaim" {
		t.Fatalf("stdin manifest=%s", runner.stdin)
	}
	meta := sent["metadata"].(map[string]any)
	if meta["namespace"] != "prod" {
		t.Fatalf("stdin metadata=%v", meta)
	}
	if _, ok := meta["managedFields"]; ok {
		t.Fatal("managedFields must be stripped before piping")
	}
	if _, ok := sent["status"]; ok {
		t.Fatal("status must be dropped before piping")
	}

	// dry_run=true → kubectl --dry-run=server and a 200.
	runner2 := &scriptRunner{fn: func(args []string) (string, string, error) {
		return `{"apiVersion":"v1","kind":"PersistentVolumeClaim","metadata":{"name":"data","namespace":"prod"}}`, "", nil
	}}
	e2 := newSSHEngine(t, runner2)
	w = do(t, e2, "POST", "/api/v1/k8s/resources?cluster_id=1&version=v1&resource=persistentvolumeclaims&namespace=prod&dry_run=true",
		map[string]any{"manifest": manifest})
	if w.Code != 200 {
		t.Fatalf("dry-run create → %d %s", w.Code, w.Body.String())
	}
	calls = runner2.recorded()
	if len(calls) != 1 || !strings.Contains(calls[0], "--dry-run=server") {
		t.Fatalf("dry-run calls=%v", calls)
	}
}

// TestPostResourceDataEdit freezes the structured data-edit contract:
// server-side merge on the live object (set overwrites, delete_keys
// remove, untouched keys keep their REAL values — never the "***" mask),
// rv is mandatory and stale → 409, immutable ConfigMaps reject data
// changes, non-DataEditable kinds → 400, viewer → 403, and the audit
// detail carries key names only.
func TestPostResourceDataEdit(t *testing.T) {
	app, e, _ := newTestApp(t)
	cmQ := "cluster_id=1&version=v1&resource=configmaps&namespace=prod"
	post := func(q string, body map[string]any, token ...string) *httptest.ResponseRecorder {
		return do(t, e, "POST", "/api/v1/k8s/resources/cm1/data?"+q, body, token...)
	}

	// ConfigMap merge: keep stays, drop is deleted, set writes through —
	// the response echoes the merged plaintext (configmaps are not
	// masked).
	w := post(cmQ, map[string]any{
		"resource_version": "1",
		"set":              map[string]string{"keep": "v2", "added": "x"},
		"delete_keys":      []string{"drop"},
	})
	if w.Code != 200 {
		t.Fatalf("configmap data edit → %d %s", w.Code, w.Body.String())
	}
	var cm map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &cm); err != nil {
		t.Fatal(err)
	}
	data := cm["data"].(map[string]any)
	if data["keep"] != "v2" || data["added"] != "x" {
		t.Fatalf("merged data wrong: %v", data)
	}
	if _, ok := data["drop"]; ok {
		t.Fatalf("deleted key survived: %v", data)
	}
	if !hasAudit(app.store, "update", "cm1", service.AuditSuccess) {
		t.Fatalf("data-edit audit missing: %+v", app.store.Audits(5))
	}

	// Secret merge: the plaintext set value is base64'd into data, kept
	// keys keep their real base64 — and the RESPONSE masks everything.
	secQ := "cluster_id=1&version=v1&resource=secrets&namespace=prod"
	w = do(t, e, "POST", "/api/v1/k8s/resources/s1/data?"+secQ,
		map[string]any{
			"resource_version": "1",
			"set":              map[string]string{"keep": "n3w", "fresh": "v"},
			"delete_keys":      []string{"drop"},
			"type":             "Opaque",
		})
	if w.Code != 200 {
		t.Fatalf("secret data edit → %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "n3w") {
		t.Fatalf("secret edit response leaks plaintext: %s", w.Body.String())
	}
	var sec map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &sec); err != nil {
		t.Fatal(err)
	}
	for k, v := range sec["data"].(map[string]any) {
		if v != "***" {
			t.Fatalf("secret data[%s]=%v, want masked", k, v)
		}
	}
	if !hasAudit(app.store, "update", "s1", service.AuditSuccess) {
		t.Fatalf("secret data-edit audit missing: %+v", app.store.Audits(5))
	}
	// Audit detail carries key names, never values.
	for _, a := range app.store.Audits(10) {
		if strings.Contains(a.Detail, "n3w") {
			t.Fatalf("audit detail leaks secret value: %+v", a)
		}
	}

	// Stale resource_version → 409.
	w = post(cmQ, map[string]any{
		"resource_version": "999",
		"set":              map[string]string{"keep": "v3"},
	})
	if w.Code != 409 {
		t.Fatalf("stale rv edit → %d, want 409 (%s)", w.Code, w.Body.String())
	}

	// Validation failures: missing rv → 400; empty diff → 400;
	// non-DataEditable kind → 400; viewer → 403.
	w = post(cmQ, map[string]any{"set": map[string]string{"a": "b"}})
	if w.Code != 400 {
		t.Fatalf("missing rv → %d, want 400", w.Code)
	}
	w = post(cmQ, map[string]any{"resource_version": "1"})
	if w.Code != 400 {
		t.Fatalf("empty diff → %d, want 400", w.Code)
	}
	w = do(t, e, "POST", "/api/v1/k8s/resources/p1/data?cluster_id=1&version=v1&resource=pods&namespace=prod",
		map[string]any{"resource_version": "1", "set": map[string]string{"a": "b"}})
	if w.Code != 400 {
		t.Fatalf("pods data edit → %d, want 400", w.Code)
	}
	viewer := seedUser(t, app.store, "dataviewer", service.RoleViewer)
	w = do(t, e, "POST", "/api/v1/k8s/resources/cm1/data?"+cmQ,
		map[string]any{"resource_version": "1", "set": map[string]string{"a": "b"}}, viewer)
	if w.Code != 403 {
		t.Fatalf("viewer data edit → %d, want 403", w.Code)
	}
}

// TestPostResourceDataEditSSHRuntime exercises the same merge over SSH:
// kubectl get feeds the merge, kubectl replace -f - writes back — the
// piped JSON must carry the merged data with base64-encoded set values
// and never the "***" mask.
func TestPostResourceDataEditSSHRuntime(t *testing.T) {
	secretJSON := `{"apiVersion":"v1","kind":"Secret","type":"Opaque","metadata":{"name":"s1","namespace":"prod","resourceVersion":"7"},"data":{"keep":"b2xk","drop":"ZHJvcA=="}}`
	runner := &scriptRunner{fn: func(args []string) (string, string, error) {
		if strings.Contains(strings.Join(args, " "), "get") {
			return secretJSON, "", nil
		}
		return `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"s1","namespace":"prod","resourceVersion":"8"}}`, "", nil
	}}
	e := newSSHEngine(t, runner)
	w := do(t, e, "POST", "/api/v1/k8s/resources/s1/data?cluster_id=1&version=v1&resource=secrets&namespace=prod",
		map[string]any{
			"resource_version": "7",
			"set":              map[string]string{"keep": "n3w"},
			"delete_keys":      []string{"drop"},
		})
	if w.Code != 200 {
		t.Fatalf("secret data edit → %d %s", w.Code, w.Body.String())
	}
	calls := runner.recorded()
	if len(calls) != 2 || !strings.Contains(calls[0], "get") || !strings.Contains(calls[1], "replace -f -") {
		t.Fatalf("calls=%v", calls)
	}
	var sent map[string]any
	if err := json.Unmarshal(runner.stdin, &sent); err != nil {
		t.Fatalf("stdin is not JSON: %v", err)
	}
	data := sent["data"].(map[string]any)
	if data["keep"] != "bjN3" {
		t.Fatalf("set value must be base64 of n3w: %v", data)
	}
	if _, ok := data["drop"]; ok {
		t.Fatalf("deleted key survived: %v", data)
	}
	if strings.Contains(string(runner.stdin), "***") {
		t.Fatal("masked placeholder must never be piped back")
	}
	if _, ok := sent["metadata"].(map[string]any)["managedFields"]; ok {
		t.Fatal("managedFields must be stripped before piping")
	}
}

// TestMergeResourceData unit-tests the merge helper directly: secret
// values are base64-encoded, configmap values stay verbatim, deletes
// remove keys, untouched keys survive, type is applied on secrets only.
func TestMergeResourceData(t *testing.T) {
	secDef, _ := kube.LookupResource("", "v1", "secrets")
	cmDef, _ := kube.LookupResource("", "v1", "configmaps")

	obj := &unstructured.Unstructured{Object: map[string]any{
		"data": map[string]any{"keep": "b2xk", "drop": "Z28="},
	}}
	mergeResourceData(obj, secDef, map[string]string{"keep": "new", "add": "v"}, []string{"drop"}, "kubernetes.io/basic-auth")
	data, _, _ := unstructured.NestedStringMap(obj.Object, "data")
	if data["keep"] != "bmV3" || data["add"] != "dg==" {
		t.Fatalf("secret merge wrong: %v", data)
	}
	if _, ok := data["drop"]; ok {
		t.Fatal("drop survived")
	}
	if obj.Object["type"] != "kubernetes.io/basic-auth" {
		t.Fatalf("type not applied: %v", obj.Object["type"])
	}

	cm := &unstructured.Unstructured{Object: map[string]any{
		"data": map[string]any{"keep": "old"},
	}}
	mergeResourceData(cm, cmDef, map[string]string{"keep": "v2"}, nil, "ignored")
	cmData, _, _ := unstructured.NestedStringMap(cm.Object, "data")
	if cmData["keep"] != "v2" {
		t.Fatalf("configmap merge wrong: %v", cmData)
	}
}
