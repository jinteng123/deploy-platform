package httpapi

import (
	"deploy-platform/backend-go/internal/service"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestProductionCompatibilityAndConsoleEntrypointsCannotBypassGate(t *testing.T) {
	cfg := cdConfig(t)
	cfg.PromotionApproval = true
	store := cdStore()
	e := newCDEngine(t, cfg, store)
	w := do(t, e, "POST", "/api/v1/deploy", map[string]any{"cluster_id": 2, "deployment_name": "app", "container_name": "app", "file_name": "app.tar", "version": "v1", "namespace": "ns"})
	if w.Code != 403 {
		t.Fatalf("legacy production: %d %s", w.Code, w.Body.String())
	}
	base := "?cluster_id=2&group=apps&version=v1&resource=deployments&namespace=ns"
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/v1/k8s/resources/app/image" + base, map[string]any{"container": "app", "image": "reg.io/app:v2"}},
		{"POST", "/api/v1/k8s/resources/app/image" + base + "&dry_run=true", map[string]any{"container": "app", "image": "reg.io/app:v2"}},
		{"POST", "/api/v1/k8s/resources/app/restart" + base, nil},
		{"DELETE", "/api/v1/k8s/resources/app" + base + "&dry_run=true", nil},
		{"PUT", "/api/v1/k8s/resources/app" + base, map[string]any{"manifest": "arbitrary"}},
	} {
		w := do(t, e, tc.method, tc.path, tc.body)
		if w.Code != 403 {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	if len(store.Logs(0, "", "", 0)) != 0 {
		t.Fatal("rejected writes created tasks")
	}
}

func TestEvidenceContractAndPromotionBlockers(t *testing.T) {
	cfg := cdConfig(t)
	store := cdStore()
	e := newCDEngine(t, cfg, store)
	w := do(t, e, "POST", "/api/v1/webhooks/yunxiao", map[string]any{"event_id": "evidence-flow", "target_cluster": "test", "app_name": "app", "image_full": "reg.io/app:v1", "image_digest": "sha256:" + strings.Repeat("ab", 32)})
	if w.Code != 202 {
		t.Fatalf("receive: %d %s", w.Code, w.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	id := int64(created["release_id"].(float64))
	path := fmt.Sprintf("/api/v1/releases/%d", id)
	w = do(t, e, "GET", path, nil)
	var view map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &view)
	if view["can_promote"] != false || len(view["promotion_blockers"].([]any)) == 0 {
		t.Fatalf("rollout incorrectly certifies business: %s", w.Body.String())
	}
	evidence := map[string]any{"digest": "sha256:" + strings.Repeat("ab", 32), "status": "passed", "kind": "manual", "summary": "登录和订单 smoke 通过", "valid_until": time.Now().Add(time.Hour).Format(time.RFC3339), "recorded_by": "forged"}
	w = do(t, e, "POST", path+"/verification", evidence)
	if w.Code != 200 {
		t.Fatalf("evidence: %d %s", w.Code, w.Body.String())
	}
	r, _ := store.FindImageRelease(id)
	if r.Metadata.Verification.RecordedBy != "admin" {
		t.Fatal("caller forged attestation identity")
	}
	evidence["summary"] = strings.Repeat("验收", 600)
	if w := do(t, e, "POST", path+"/verification", evidence); w.Code != 200 {
		t.Fatalf("declared character limit rejected Chinese summary: %d %s", w.Code, w.Body.String())
	}
	evidence["report_url"] = "javascript:alert(1)"
	if w := do(t, e, "POST", path+"/verification", evidence); w.Code != 400 {
		t.Fatalf("unsafe report URL: %d", w.Code)
	}
	evidence["report_url"] = ""
	evidence["digest"] = "sha256:" + strings.Repeat("cd", 32)
	if w := do(t, e, "POST", path+"/verification", evidence); w.Code != 400 {
		t.Fatalf("wrong digest report: %d", w.Code)
	}
}

func TestRollbackContractRejectsArbitraryImageAndDerivesTarget(t *testing.T) {
	app, e, _ := newTestApp(t)
	store := app.store
	if w := do(t, e, "POST", "/api/v1/rollback", map[string]any{"cluster_id": 1, "image_full": "reg.io/app:v1"}); w.Code != 400 {
		t.Fatalf("arbitrary rollback %d", w.Code)
	}
	image := "reg.io/app@sha256:" + strings.Repeat("ab", 32)
	target, _ := store.AddLog(service.DeployLog{ClusterID: 1, Namespace: "ns", DeploymentName: "app", AppName: "app", ContainerName: "app", ImageFull: &image, Status: "success"})
	w := do(t, e, "POST", "/api/v1/rollback", map[string]any{"cluster_id": 1, "target_log_id": target.ID, "image_full": "evil.io/app:v2"})
	if w.Code != 202 {
		t.Fatalf("historical rollback %d %s", w.Code, w.Body.String())
	}
	logs := store.Logs(1, "app", "", 0)
	if logs[0].ImageFull == nil || *logs[0].ImageFull != image {
		t.Fatalf("caller controlled rollback image: %+v", logs[0])
	}
}

func TestProductionDeploymentGuardSurvivesDeregistration(t *testing.T) {
	cfg := cdConfig(t)
	store := cdStore()
	e := newCDEngine(t, cfg, store)
	for _, a := range store.Apps(2) {
		if err := store.DeleteApp(a.ID); err != nil {
			t.Fatal(err)
		}
	}
	w := do(t, e, "POST", "/api/v1/k8s/resources/app/image?cluster_id=2&group=apps&version=v1&resource=deployments&namespace=ns", map[string]any{"container": "app", "image": "reg.io/app:v2"})
	if w.Code != 403 {
		t.Fatalf("deregistration bypass: %d %s", w.Code, w.Body.String())
	}
}

func TestDriftReportsExpectedActualAndUnknownWithoutBaseline(t *testing.T) {
	app, e, _ := newTestApp(t)
	driftApp, err := app.store.SaveApp(service.App{ClusterID: 1, AppName: "drift-app", MatchKeyword: "drift-app", Namespace: "ns", DeploymentName: "app", ContainerName: "app"})
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/delivery/drift?cluster_id=1&app_ids=%d", driftApp.ID)
	w := do(t, e, "GET", path, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "缺少已确认") {
		t.Fatalf("unknown baseline: %d %s", w.Code, w.Body.String())
	}
	var observations []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &observations); err != nil || len(observations) != 1 || observations[0]["app_name"] != "drift-app" {
		t.Fatalf("app_ids filter returned unrelated workloads: %d %s", len(observations), w.Body.String())
	}
	if observations[0]["status"] != "unknown" || observations[0]["observation_count"].(float64) != 1 {
		t.Fatalf("unknown drift state missing metadata: %s", w.Body.String())
	}
	if w := do(t, e, "GET", path, nil); w.Code != 200 {
		t.Fatalf("repeat drift observation: %d %s", w.Code, w.Body.String())
	}
	image := "reg.io/app@sha256:" + strings.Repeat("ab", 32)
	_, _ = app.store.AddLog(service.DeployLog{ClusterID: 1, Namespace: "ns", DeploymentName: "app", ContainerName: "app", Status: "success", ImageFull: &image})
	w = do(t, e, "GET", path, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"drifted"`) || !strings.Contains(w.Body.String(), image) || !strings.Contains(w.Body.String(), `"observed_at":"20`) || !strings.Contains(w.Body.String(), `"drift_started_at"`) {
		t.Fatalf("drift not observed: %d %s", w.Code, w.Body.String())
	}
	historyPath := fmt.Sprintf("/api/v1/delivery/drift/history?cluster_id=1&app_ids=%d&limit=10", driftApp.ID)
	w = do(t, e, "GET", historyPath, nil)
	var events []service.DeliveryDriftEvent
	if err := json.Unmarshal(w.Body.Bytes(), &events); w.Code != 200 || err != nil || len(events) != 2 || events[0].Status != "drifted" || events[1].Status != "unknown" {
		t.Fatalf("drift transition history: %d %s err=%v", w.Code, w.Body.String(), err)
	}
	for _, invalid := range []string{
		"/api/v1/delivery/drift/history",
		"/api/v1/delivery/drift/history?cluster_id=1&limit=abc",
		"/api/v1/delivery/drift/history?cluster_id=1&app_ids=0",
		"/api/v1/delivery/drift/history?cluster_id=1&limit=101",
	} {
		if response := do(t, e, "GET", invalid, nil); response.Code != 400 {
			t.Fatalf("invalid drift history query accepted %s: %d %s", invalid, response.Code, response.Body.String())
		}
	}
	if w := do(t, e, "GET", "/api/v1/delivery/drift?cluster_id=1&app_ids=1,invalid", nil); w.Code != 400 {
		t.Fatalf("malformed app_ids accepted: %d %s", w.Code, w.Body.String())
	}
}
