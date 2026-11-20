package httpapi

import (
	"strings"
	"testing"

	"deploy-platform/backend-go/internal/service"
)

// hasAudit scans recent audit entries for a match.
func hasAudit(store *service.Store, action, name, result string) bool {
	for _, a := range store.Audits(50) {
		if a.Action == action && a.Name == name && a.Result == result {
			return true
		}
	}
	return false
}

// TestSecretViewAudited — explicit Secret reads leave an audit trail while
// list reads (which never expose values) do not spam the log.
func TestSecretViewAudited(t *testing.T) {
	app, e, _ := newTestApp(t)
	w := do(t, e, "GET", "/api/v1/k8s/resources/s1?cluster_id=1&version=v1&resource=secrets&namespace=default", nil)
	if w.Code != 200 {
		t.Fatalf("secret get → %d %s", w.Code, w.Body.String())
	}
	if !hasAudit(app.store, "secret_view", "s1", service.AuditSuccess) {
		t.Fatalf("secret_view audit missing: %+v", app.store.Audits(5))
	}
	// Failed secret reads audit too (apiserver denial → denied/failed).
	w = do(t, e, "GET", "/api/v1/k8s/resources/missing?cluster_id=1&version=v1&resource=secrets&namespace=default", nil)
	if w.Code != 404 {
		t.Fatalf("missing secret → %d", w.Code)
	}
	if !hasAudit(app.store, "secret_view", "missing", service.AuditFailed) {
		t.Fatalf("missing-secret audit missing: %+v", app.store.Audits(5))
	}
}

// TestDeniedWritesAudited — platform-level write rejections (unsupported
// verb, out-of-range input) produce denied audit entries, not silence.
func TestDeniedWritesAudited(t *testing.T) {
	app, e, _ := newTestApp(t)
	// nodes are read-only — delete is refused at the allowlist.
	w := do(t, e, "DELETE", "/api/v1/k8s/resources/n1?cluster_id=1&version=v1&resource=nodes", nil)
	if w.Code != 400 {
		t.Fatalf("delete node → %d %s", w.Code, w.Body.String())
	}
	if !hasAudit(app.store, "delete", "n1", service.AuditDenied) {
		t.Fatalf("delete-denied audit missing: %+v", app.store.Audits(5))
	}
	// Out-of-range replicas is refused before touching the apiserver.
	w = do(t, e, "POST", "/api/v1/k8s/resources/d1/scale?cluster_id=1&group=apps&version=v1&resource=deployments&namespace=default",
		map[string]any{"replicas": 99})
	if w.Code != 400 {
		t.Fatalf("scale 99 → %d", w.Code)
	}
	if !hasAudit(app.store, "scale", "d1", service.AuditDenied) {
		t.Fatalf("scale-denied audit missing: %+v", app.store.Audits(5))
	}
	// Phantom container name → denied.
	w = do(t, e, "POST", "/api/v1/k8s/resources/d1/image?cluster_id=1&group=apps&version=v1&resource=deployments&namespace=default",
		map[string]any{"container": "ghost", "image": "x:1"})
	if w.Code != 400 {
		t.Fatalf("phantom container → %d", w.Code)
	}
	if !hasAudit(app.store, "image", "d1", service.AuditDenied) {
		t.Fatalf("image-denied audit missing: %+v", app.store.Audits(5))
	}
}

// TestDryRunAudited — server-side dry runs audit under their own
// action so validation traffic never masquerades as a real write.
func TestDryRunAudited(t *testing.T) {
	app, e, _ := newTestApp(t)
	manifest := "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: d1, resourceVersion: '7'}\nspec: {replicas: 2}\n"
	q := "cluster_id=1&group=apps&version=v1&resource=deployments&namespace=default&dry_run=true"
	w := do(t, e, "PUT", "/api/v1/k8s/resources/d1?"+q, map[string]any{"manifest": manifest})
	if w.Code != 200 {
		t.Fatalf("dry-run put → %d %s", w.Code, w.Body.String())
	}
	if !hasAudit(app.store, "update_dryrun", "d1", service.AuditSuccess) {
		t.Fatalf("update_dryrun audit missing: %+v", app.store.Audits(5))
	}
	// A dry run against a read-only kind audits denied under the same
	// action — the attempt itself is what matters.
	pq := "cluster_id=1&version=v1&resource=pods&namespace=default&dry_run=true"
	w = do(t, e, "PUT", "/api/v1/k8s/resources/p1?"+pq, map[string]any{"manifest": manifest})
	if w.Code != 400 {
		t.Fatalf("dry-run pod put → %d", w.Code)
	}
	if !hasAudit(app.store, "update_dryrun", "p1", service.AuditDenied) {
		t.Fatalf("update_dryrun denied audit missing: %+v", app.store.Audits(5))
	}
}

// TestConflictAudited — a lost optimistic-concurrency race records
// result=conflict.
func TestConflictAudited(t *testing.T) {
	app, e, _ := newTestApp(t)
	w := do(t, e, "POST", "/api/v1/k8s/resources/d1/scale?cluster_id=1&group=apps&version=v1&resource=deployments&namespace=default",
		map[string]any{"replicas": 2, "resource_version": "stale"})
	if w.Code != 409 {
		t.Fatalf("stale scale → %d %s", w.Code, w.Body.String())
	}
	if !hasAudit(app.store, "scale", "d1", service.AuditConflict) {
		t.Fatalf("conflict audit missing: %+v", app.store.Audits(5))
	}
}

// TestPromoteAudited — a failed promotion records action+result; the
// success path shares the same auditErr wiring.
func TestPromoteAudited(t *testing.T) {
	app, e, _ := newTestApp(t)
	w := do(t, e, "POST", "/api/v1/releases/999/promote", map[string]any{"cluster_id": 1})
	if w.Code != 404 {
		t.Fatalf("promote 999 → %d", w.Code)
	}
	if !hasAudit(app.store, "promote", "999", service.AuditFailed) {
		t.Fatalf("promote audit missing: %+v", app.store.Audits(5))
	}
}

// TestClusterCreateAudited — credential-bearing cluster writes are
// audited, but the entry must never carry credential material.
func TestClusterCreateAudited(t *testing.T) {
	store := service.NewStore()
	e := newCipherEngine(t, testCipherKey(), store)
	w := do(t, e, "POST", "/api/v1/clusters", map[string]any{
		"name": "审计集群", "env_code": "audit-test", "environment": "staging",
		"connection_type": "kubeconfig", "credential": testKubeconfig,
	})
	if w.Code != 200 {
		t.Fatalf("create cluster → %d %s", w.Code, w.Body.String())
	}
	var audited string
	for _, a := range store.Audits(50) {
		if a.Action == "cluster_create" && a.Name == "审计集群" {
			audited = a.Detail + "|" + a.GVR + "|" + a.Name
		}
	}
	if audited == "" {
		t.Fatalf("cluster_create audit missing: %+v", store.Audits(5))
	}
	if strings.Contains(audited, "secret-token") || strings.Contains(audited, "certificate") {
		t.Fatalf("audit leaks credential material: %s", audited)
	}
}
