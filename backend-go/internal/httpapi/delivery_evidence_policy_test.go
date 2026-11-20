package httpapi

import (
	"encoding/json"
	"testing"

	"deploy-platform/backend-go/internal/config"
	"deploy-platform/backend-go/internal/service"
)

func TestProductionEvidencePolicyAPIIsAdminOnlyAndVersioned(t *testing.T) {
	store := deliveryPolicyStore(t)
	e := newCDEngine(t, config.Config{Env: "dev", MockRuntime: true, TransferDir: t.TempDir()}, store)
	viewer := seedUser(t, store, "evidence-viewer", service.RoleViewer)
	admin := seedUser(t, store, "evidence-admin", service.RoleAdmin)

	response := do(t, e, "GET", "/api/v1/environments/9/evidence-policy", nil, viewer)
	var history service.DeliveryEnvironmentEvidencePolicyHistory
	if err := json.Unmarshal(response.Body.Bytes(), &history); err != nil || response.Code != 200 || history.EnvironmentID != 9 || history.CurrentVersion != 0 {
		t.Fatalf("empty evidence policy history=%+v response=%d err=%v", history, response.Code, err)
	}
	body := map[string]any{
		"expected_version":            0,
		"required_verification_kinds": []string{"ci", "smoke"},
		"max_evidence_age_minutes":    120,
		"trusted_submitters": map[string][]string{
			"ci": {"apikey:yunxiao-prod"}, "smoke": {"apikey:smoke-prod"},
		},
	}
	if response = do(t, e, "PUT", "/api/v1/environments/9/evidence-policy", body, viewer); response.Code != 403 {
		t.Fatalf("viewer policy write=%d %s", response.Code, response.Body.String())
	}
	if response = do(t, e, "PUT", "/api/v1/environments/9/evidence-policy", body, admin); response.Code != 200 {
		t.Fatalf("admin policy write=%d %s", response.Code, response.Body.String())
	}
	var created service.DeliveryEnvironmentEvidencePolicyVersion
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.Version != 1 || created.MaxEvidenceAgeMinutes != 120 || len(created.RequiredVerificationKinds) != 2 {
		t.Fatalf("created policy=%+v err=%v", created, err)
	}
	if response = do(t, e, "PUT", "/api/v1/environments/9/evidence-policy", body, admin); response.Code != 409 {
		t.Fatalf("stale policy write=%d %s", response.Code, response.Body.String())
	}
	invalid := map[string]any{
		"expected_version":            1,
		"required_verification_kinds": []string{"ci"},
		"max_evidence_age_minutes":    120,
		"trusted_submitters":          map[string][]string{},
	}
	if response = do(t, e, "PUT", "/api/v1/environments/9/evidence-policy", invalid, admin); response.Code != 400 {
		t.Fatalf("untrusted source accepted=%d %s", response.Code, response.Body.String())
	}
	response = do(t, e, "GET", "/api/v1/delivery/model", nil)
	var model service.DeliveryModel
	if err := json.Unmarshal(response.Body.Bytes(), &model); err != nil || response.Code != 200 || len(model.Environments) != 1 || model.Environments[0].EvidencePolicyVersion != 1 {
		t.Fatalf("environment summary=%+v response=%d err=%v", model.Environments, response.Code, err)
	}
}
