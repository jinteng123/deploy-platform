package httpapi

import (
	"encoding/json"
	"testing"

	"deploy-platform/backend-go/internal/config"
	"deploy-platform/backend-go/internal/service"
)

func deliveryPolicyStore(t *testing.T) *service.Store {
	t.Helper()
	store := cdStore()
	store.LoadYunxiaoConnections([]service.YunxiaoConnection{{ID: 7, Name: "production"}})
	app, err := store.FindApp(2)
	if err != nil {
		t.Fatal(err)
	}
	app.DeploymentConfigRepoURL = "https://git.example.com/orders/deploy-config.git"
	app.DeploymentConfigPath = "charts/orders"
	if _, err := store.UpdateApp(app); err != nil {
		t.Fatal(err)
	}
	store.LoadDeliveryModel(service.DeliveryModel{
		Environments: []service.DeliveryEnvironment{{ID: 9, Code: "prod", Name: "生产", Tier: "production"}},
		Applications: []service.DeliveryApplication{{ID: 7, Name: "订单"}},
		Components:   []service.DeliveryComponent{{ID: 8, ApplicationID: 7, Name: "orders-api"}},
		Bindings: []service.DeliveryBinding{{
			ID: 41, AppID: 2, ApplicationID: 7, ComponentID: 8, EnvironmentID: 9,
		}},
	})
	return store
}

func TestDeliveryPolicyAPIAppendsAndReadsVersions(t *testing.T) {
	store := deliveryPolicyStore(t)
	e := newCDEngine(t, config.Config{Env: "dev", MockRuntime: true, TransferDir: t.TempDir()}, store)
	viewer := seedUser(t, store, "policy-viewer", service.RoleViewer)

	response := do(t, e, "GET", "/api/v1/apps/2/delivery-policy", nil, viewer)
	if response.Code != 200 {
		t.Fatalf("read policy: %d %s", response.Code, response.Body.String())
	}
	var history service.DeliveryPolicyHistory
	if err := json.Unmarshal(response.Body.Bytes(), &history); err != nil || history.BindingID != 41 || history.CurrentVersion != 0 || len(history.Versions) != 0 {
		t.Fatalf("empty history=%+v err=%v", history, err)
	}

	response = do(t, e, "PUT", "/api/v1/apps/2/delivery-policy", map[string]any{
		"expected_version": 0, "strategy": "blue_green", "service_name": "orders-entry", "candidate_retention_hours": 48,
	})
	if response.Code != 200 {
		t.Fatalf("create policy: %d %s", response.Code, response.Body.String())
	}
	var created service.DeliveryPolicyVersion
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.Version != 1 || created.CandidateRetentionHours != 48 {
		t.Fatalf("created=%+v err=%v", created, err)
	}

	response = do(t, e, "PUT", "/api/v1/apps/2/delivery-policy", map[string]any{
		"expected_version": 0, "strategy": "rolling",
	})
	if response.Code != 409 {
		t.Fatalf("stale update: %d %s", response.Code, response.Body.String())
	}
	response = do(t, e, "PUT", "/api/v1/apps/2/delivery-policy", map[string]any{
		"expected_version": 1, "strategy": "gateway_canary",
	})
	if response.Code != 400 {
		t.Fatalf("canary draft without thresholds: %d %s", response.Code, response.Body.String())
	}
	response = do(t, e, "PUT", "/api/v1/apps/2/delivery-policy", map[string]any{
		"expected_version": 1, "strategy": "gateway_canary",
		"canary_analysis": map[string]any{
			"min_requests": 1000, "max_observation_age_seconds": 120,
			"max_candidate_error_rate": 0.02, "max_error_rate_increase": 0.01,
			"max_candidate_p95_latency_ms": 250, "max_p95_latency_increase_ratio": 0.2,
			"stages": []map[string]any{
				{"candidate_weight_percent": 10, "observation_duration_seconds": 60},
				{"candidate_weight_percent": 50, "observation_duration_seconds": 120},
				{"candidate_weight_percent": 100, "observation_duration_seconds": 300},
			},
		},
	})
	if response.Code != 200 {
		t.Fatalf("canary draft: %d %s", response.Code, response.Body.String())
	}
	var canary service.DeliveryPolicyVersion
	if err := json.Unmarshal(response.Body.Bytes(), &canary); err != nil || canary.Version != 2 || canary.Readiness != "integration_pending" || canary.CanaryAnalysis == nil || canary.CanaryAnalysis.MinRequests != 1000 || *canary.CanaryAnalysis.MaxCandidateErrorRate != 0.02 || len(canary.CanaryAnalysis.Stages) != 3 || canary.CanaryAnalysis.Stages[2].CandidateWeightPercent != 100 {
		t.Fatalf("canary=%+v err=%v", canary, err)
	}
	response = do(t, e, "GET", "/api/v1/apps/2/delivery-policy", nil, viewer)
	if err := json.Unmarshal(response.Body.Bytes(), &history); err != nil || response.Code != 200 || history.CurrentVersion != 2 || len(history.Versions) != 2 {
		t.Fatalf("history=%+v response=%d err=%v", history, response.Code, err)
	}
}

func TestDeliveryBindingAndPolicySaveAtomically(t *testing.T) {
	store := deliveryPolicyStore(t)
	e := newCDEngine(t, config.Config{Env: "dev", MockRuntime: true, TransferDir: t.TempDir()}, store)
	response := do(t, e, "PUT", "/api/v1/apps/2/delivery-binding", map[string]any{
		"application_id": 7, "component_id": 8, "environment_id": 9,
		"strategy_draft": "blue_green", "service_name": "orders-entry",
		"expected_policy_version": 0, "strategy": "blue_green", "candidate_retention_hours": 48,
		"deployment_pipeline": map[string]any{"connection_id": 7, "pipeline_id": "123456"},
	})
	if response.Code != 200 {
		t.Fatalf("atomic binding and policy save: %d %s", response.Code, response.Body.String())
	}
	var binding service.DeliveryBinding
	if err := json.Unmarshal(response.Body.Bytes(), &binding); err != nil || binding.ID != 41 || binding.ServiceName != "orders-entry" || binding.DeploymentPipeline == nil || binding.DeploymentPipeline.ConnectionID != 7 || binding.DeploymentPipeline.PipelineID != "123456" {
		t.Fatalf("saved binding=%+v err=%v", binding, err)
	}
	history, err := store.DeliveryPolicyHistoryForApp(2)
	if err != nil || history.CurrentVersion != 1 || len(history.Versions) != 1 || history.Versions[0].CandidateRetentionHours != 48 {
		t.Fatalf("saved history=%+v err=%v", history, err)
	}

	response = do(t, e, "PUT", "/api/v1/apps/2/delivery-binding", map[string]any{
		"application_id": 7, "component_id": 8, "environment_id": 9,
		"expected_policy_version": 0, "strategy": "rolling",
	})
	if response.Code != 409 {
		t.Fatalf("stale atomic save: %d %s", response.Code, response.Body.String())
	}
	model := store.DeliveryModel()
	if model.Bindings[0].StrategyDraft != "blue_green" || model.Bindings[0].ServiceName != "orders-entry" || model.Bindings[0].DeploymentPipeline == nil || model.Bindings[0].DeploymentPipeline.PipelineID != "123456" {
		t.Fatalf("stale policy update partially changed binding: %+v", model.Bindings[0])
	}
}

func TestDeliveryPolicyAPIRoleAndValidationGates(t *testing.T) {
	store := deliveryPolicyStore(t)
	e := newCDEngine(t, config.Config{Env: "dev", MockRuntime: true, TransferDir: t.TempDir()}, store)
	viewer := seedUser(t, store, "policy-read-only", service.RoleViewer)
	response := do(t, e, "PUT", "/api/v1/apps/2/delivery-policy", map[string]any{
		"expected_version": 0, "strategy": "rolling",
	}, viewer)
	if response.Code != 403 {
		t.Fatalf("viewer policy write: %d %s", response.Code, response.Body.String())
	}
	response = do(t, e, "PUT", "/api/v1/apps/2/delivery-policy", map[string]any{
		"expected_version": 0, "strategy": "blue_green",
	})
	if response.Code != 400 {
		t.Fatalf("blue-green without Service: %d %s", response.Code, response.Body.String())
	}
	response = do(t, e, "PUT", "/api/v1/apps/999/delivery-policy", map[string]any{
		"expected_version": 0, "strategy": "rolling",
	})
	if response.Code != 404 {
		t.Fatalf("unknown binding: %d %s", response.Code, response.Body.String())
	}
}
