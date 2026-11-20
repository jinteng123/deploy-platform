package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/service"
)

func TestDeliveryRunAPIShowsPlanTaskAndEvents(t *testing.T) {
	app, engine, _ := newTestApp(t)
	plan := service.DeliveryPlan{
		ID: 41, ReleaseID: 17, TargetClusterID: 2, TargetAppID: 9,
		Strategy: "rolling", Status: "preview_only", SnapshotHash: "snapshot",
		Snapshot: service.DeliveryPlanSnapshot{SchemaVersion: 1, Strategy: "rolling"},
	}
	app.store.LoadDeliveryPlans([]service.DeliveryPlan{plan})
	deploy, err := app.store.AddLog(service.DeployLog{
		ClusterID: 2, AppName: "orders", DeploymentName: "orders-api", ContainerName: "api",
		Namespace: "prod", DeployType: "image_deploy", Status: "running", CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	app.store.LoadDeliveryRuns([]service.DeliveryRun{{
		ID: 7, DeliveryPlanID: plan.ID, PromotionRequestID: 5, DeployLogID: deploy.ID,
		Strategy: "rolling", Status: "running", CurrentStage: "rollout", Version: 2,
	}}, []service.DeliveryRunEvent{{
		ID: 1, RunID: 7, Version: 1, EventKey: "run-created", EventType: "queued",
		Stage: "queued", Status: "queued", Detail: "已排队", CreatedAt: time.Now(),
	}, {
		ID: 2, RunID: 7, Version: 2, EventKey: "step:rollout", EventType: "stage",
		Stage: "rollout", Status: "running", Detail: "等待工作负载就绪", CreatedAt: time.Now(),
	}})

	w := do(t, engine, "GET", "/api/v1/delivery-runs/7", nil)
	if w.Code != 200 {
		t.Fatalf("delivery run detail → %d: %s", w.Code, w.Body.String())
	}
	var detail service.DeliveryRunView
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Run.ID != 7 || detail.Plan.ID != plan.ID || detail.Deploy.ID != deploy.ID || len(detail.Events) != 2 {
		t.Fatalf("incomplete run view: %+v", detail)
	}
	w = do(t, engine, "GET", fmt.Sprintf("/api/v1/delivery-runs/by-deploy-log/%d", deploy.ID), nil)
	if w.Code != 200 {
		t.Fatalf("delivery run by log → %d: %s", w.Code, w.Body.String())
	}
	if w := do(t, engine, "GET", "/api/v1/delivery-runs/7", nil, ""); w.Code != 401 {
		t.Fatalf("unauthenticated delivery run read → %d, want 401", w.Code)
	}
	viewer := seedUser(t, app.store, "observer", service.RoleViewer)
	control := map[string]any{"action": "confirm_cutover", "expected_version": 2, "idempotency_key": "control-test-123"}
	if w := do(t, engine, "POST", "/api/v1/delivery-runs/7/actions", control, viewer); w.Code != http.StatusForbidden {
		t.Fatalf("viewer delivery run control → %d, want 403: %s", w.Code, w.Body.String())
	}
	if w := do(t, engine, "POST", "/api/v1/delivery-runs/7/actions", map[string]any{}, testSessionToken); w.Code != http.StatusBadRequest {
		t.Fatalf("incomplete delivery run control → %d, want 400: %s", w.Code, w.Body.String())
	}
}

func TestBlueGreenRestoreAPIRequiresCompatibilityAcknowledgement(t *testing.T) {
	app, engine, _ := newTestApp(t)
	plan := service.DeliveryPlan{
		ID: 91, ReleaseID: 17, TargetClusterID: 2, TargetAppID: 9,
		Strategy: "blue_green", Status: "approved", SnapshotHash: "restore-compatibility",
		Snapshot: service.DeliveryPlanSnapshot{SchemaVersion: 1, Strategy: "blue_green", Target: service.DeliveryPlanTarget{Namespace: "prod", ServiceName: "orders-entry"}},
	}
	app.store.LoadDeliveryPlans([]service.DeliveryPlan{plan})
	stage := "awaiting_live_verification"
	deploy, err := app.store.AddLog(service.DeployLog{
		ID: 91, ClusterID: 2, AppName: "orders", DeploymentName: "orders-api", Namespace: "prod",
		DeployType: "image_deploy", Status: "running", CurrentStep: &stage, StepDetail: "[]",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := service.DeliveryRun{
		ID: 91, DeliveryPlanID: plan.ID, DeployLogID: deploy.ID, Strategy: "blue_green",
		Status: "paused", CurrentStage: stage, Version: 3,
	}
	app.store.LoadDeliveryRuns([]service.DeliveryRun{run}, []service.DeliveryRunEvent{{
		ID: 1, RunID: run.ID, Version: 1, EventKey: "restore-api-created", EventType: "queued", Stage: "queued", Status: "queued",
	}})
	operator := seedUser(t, app.store, "restore-operator", service.RoleOperator)
	body := map[string]any{
		"action": service.DeliveryActionRestoreStable, "expected_version": run.Version, "idempotency_key": "restore-api-compat-001",
	}
	response := do(t, engine, "POST", "/api/v1/delivery-runs/91/actions", body, operator)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "不回退数据库迁移") {
		t.Fatalf("restore without compatibility acknowledgment → %d: %s", response.Code, response.Body.String())
	}
	unchanged, err := app.store.DeliveryRun(run.ID)
	if err != nil || unchanged.Version != run.Version || unchanged.CurrentStage != stage {
		t.Fatalf("rejected API restore changed the durable run: run=%+v err=%v", unchanged, err)
	}
}

func TestDeliveryRunAPIConfirmsCanaryRestoreBusinessHealth(t *testing.T) {
	app, engine, _ := newTestApp(t)
	stableDigest := "sha256:" + strings.Repeat("a", 64)
	candidateDigest := "sha256:" + strings.Repeat("b", 64)
	analysis := service.CanaryAnalysisPolicy{Stages: []service.CanaryStage{{CandidateWeightPercent: 10, ObservationDurationSeconds: 60}}}
	plan := service.DeliveryPlan{
		ID: 61, ReleaseID: 17, TargetClusterID: 2, TargetAppID: 9,
		Strategy: "gateway_canary", Status: "approved", SnapshotHash: "canary-snapshot",
		Snapshot: service.DeliveryPlanSnapshot{
			SchemaVersion: 4, Strategy: "gateway_canary",
			Artifact: service.DeliveryPlanArtifact{Digest: &candidateDigest},
			Target:   service.DeliveryPlanTarget{StableImageDigest: stableDigest},
			Policy:   service.DeliveryPlanPolicySnapshot{Strategy: "gateway_canary", CanaryAnalysis: &analysis},
		},
	}
	app.store.LoadDeliveryPlans([]service.DeliveryPlan{plan})
	step := "awaiting_restore_verification"
	deploy, err := app.store.AddLog(service.DeployLog{
		ClusterID: 2, AppName: "orders", DeploymentName: "orders-api", Namespace: "prod",
		DeployType: "image_deploy", Status: "running", CurrentStep: &step, StepDetail: "[]",
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(service.CanaryRestoreCheckpoint{
		SchemaVersion: 1, StageIndex: 0, StableImageDigest: stableDigest, CandidateImageDigest: candidateDigest,
		Route: service.CanaryGatewayRouteRef{
			ConnectionID: 3, RouteID: "orders-public", StableBackendID: "orders-stable", CandidateBackendID: "orders-candidate",
		},
		EffectiveWeights: service.CanaryTrafficWeights{StableWeightPercent: 100}, RouteRevision: "route-v10", RestoredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	run := service.DeliveryRun{
		ID: 71, DeliveryPlanID: plan.ID, DeployLogID: deploy.ID, Strategy: "gateway_canary",
		Status: "running", CurrentStage: step, Version: 2,
	}
	app.store.LoadDeliveryRuns([]service.DeliveryRun{run}, []service.DeliveryRunEvent{
		{ID: 1, RunID: run.ID, Version: 1, EventKey: "run-created", EventType: "queued", Stage: "queued", Status: "queued"},
		{ID: 2, RunID: run.ID, Version: 2, EventKey: "canary:restore:restored-001", EventType: "canary_restore", Stage: step, Status: "running", Detail: "权重回读成功", Payload: payload},
	})

	operator := seedUser(t, app.store, "release-operator", service.RoleOperator)
	body := map[string]any{
		"action":           service.DeliveryActionConfirmCanaryRestore,
		"expected_version": run.Version,
		"idempotency_key":  "confirm-restore-api-001",
	}
	w := do(t, engine, "POST", "/api/v1/delivery-runs/71/actions", body, operator)
	if w.Code != http.StatusOK {
		t.Fatalf("confirm canary restore → %d: %s", w.Code, w.Body.String())
	}
	var view service.DeliveryRunView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Run.Status != "failed" || view.Run.CurrentStage != "restore_verified" || view.Deploy.Status != "failed" ||
		len(view.Events) != 3 || view.Events[len(view.Events)-1].EventType != "canary_restore_verification" {
		t.Fatalf("API did not persist the terminal recovery verification: %+v", view)
	}
	viewer := seedUser(t, app.store, "release-viewer", service.RoleViewer)
	body["idempotency_key"] = "confirm-restore-api-002"
	if w := do(t, engine, "POST", "/api/v1/delivery-runs/71/actions", body, viewer); w.Code != http.StatusForbidden {
		t.Fatalf("viewer confirmed canary recovery → %d, want 403: %s", w.Code, w.Body.String())
	}
}

func TestDeliveryRunAPICancelsQueuedCanaryBeforeAnyExternalWrite(t *testing.T) {
	app, engine, _ := newTestApp(t)
	// This test exercises only the operator API transition. Stop the recovered
	// worker's periodic scanner so it cannot claim the seeded queue row while
	// the request is checking its expected version, especially under -race.
	app.worker.Close()
	policy := service.CanaryAnalysisPolicy{Stages: []service.CanaryStage{{CandidateWeightPercent: 10, ObservationDurationSeconds: 60}, {CandidateWeightPercent: 100, ObservationDurationSeconds: 60}}}
	plan := service.DeliveryPlan{
		ID: 81, ReleaseID: 17, TargetClusterID: 2, TargetAppID: 9,
		Strategy: "gateway_canary", Status: "approved", SnapshotHash: "queued-canary",
		Snapshot: service.DeliveryPlanSnapshot{
			SchemaVersion: 4, Strategy: "gateway_canary",
			Policy: service.DeliveryPlanPolicySnapshot{Strategy: "gateway_canary", CanaryAnalysis: &policy},
		},
	}
	app.store.LoadDeliveryPlans([]service.DeliveryPlan{plan})
	step := "queued"
	deploy, err := app.store.AddLog(service.DeployLog{
		ID: 81, ClusterID: 2, AppName: "checkout", DeploymentName: "checkout-api", Namespace: "prod",
		DeployType: "image_deploy", Status: "pending", CurrentStep: &step, StepDetail: "[]",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := service.DeliveryRun{
		ID: 81, DeliveryPlanID: plan.ID, PromotionRequestID: 7, DeployLogID: deploy.ID,
		Strategy: "gateway_canary", Status: "queued", CurrentStage: "queued", Version: 1,
	}
	app.store.LoadDeliveryRuns([]service.DeliveryRun{run}, []service.DeliveryRunEvent{{
		ID: 1, RunID: run.ID, Version: 1, EventKey: "run-created", EventType: "queued", Stage: "queued", Status: "queued",
	}})
	body := map[string]any{
		"action": service.DeliveryActionCancel, "expected_version": 1, "idempotency_key": "cancel-canary-api-001",
	}
	viewer := seedUser(t, app.store, "canary-cancel-viewer", service.RoleViewer)
	if response := do(t, engine, "POST", "/api/v1/delivery-runs/81/actions", body, viewer); response.Code != http.StatusForbidden {
		t.Fatalf("viewer cancelled queued canary → %d, want 403: %s", response.Code, response.Body.String())
	}
	operator := seedUser(t, app.store, "canary-cancel-operator", service.RoleOperator)
	response := do(t, engine, "POST", "/api/v1/delivery-runs/81/actions", body, operator)
	if response.Code != http.StatusOK {
		t.Fatalf("operator cancelled queued canary → %d: %s", response.Code, response.Body.String())
	}
	var view service.DeliveryRunView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Run.Status != "cancelled" || view.Run.Version != 2 || view.Deploy.Status != "failed" || len(view.Actions) != 0 || len(view.Events) != 2 {
		t.Fatalf("queued canary cancellation response=%+v", view)
	}
	replay := do(t, engine, "POST", "/api/v1/delivery-runs/81/actions", body, operator)
	if replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), `"version":2`) {
		t.Fatalf("idempotent queued canary cancellation → %d: %s", replay.Code, replay.Body.String())
	}
}
