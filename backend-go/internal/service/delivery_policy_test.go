package service

import (
	"errors"
	"testing"
)

func policyTestStore(review bool) *Store {
	store := NewStore()
	store.LoadDeliveryModel(DeliveryModel{Bindings: []DeliveryBinding{{
		ID: 41, AppID: 12, ApplicationID: 7, ComponentID: 8, EnvironmentID: 9, RequiresReview: review,
	}}})
	return store
}

func TestDeliveryPolicyVersionsAreImmutableAndRequireExpectedVersion(t *testing.T) {
	store := policyTestStore(false)
	first, created, err := store.SaveDeliveryPolicyVersion(12, 0, "blue_green", "orders-entry", 48, "alice")
	if err != nil || !created || first.Version != 1 || first.CandidateRetentionHours != 48 || first.Readiness != "configurable" {
		t.Fatalf("first policy=%+v created=%t err=%v", first, created, err)
	}
	unchanged, created, err := store.SaveDeliveryPolicyVersion(12, 1, "blue_green", "orders-entry", 48, "bob")
	if err != nil || created || unchanged.Version != 1 || unchanged.CreatedBy != "alice" {
		t.Fatalf("no-op edit=%+v created=%t err=%v", unchanged, created, err)
	}
	if _, _, err := store.SaveDeliveryPolicyVersion(12, 0, "rolling", "", 0, "bob"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale expected version error=%v", err)
	}
	second, created, err := store.SaveDeliveryPolicyVersion(12, 1, "rolling", "", 0, "bob")
	if err != nil || !created || second.Version != 2 || second.ServiceName != "" || second.CandidateRetentionHours != 0 {
		t.Fatalf("second policy=%+v created=%t err=%v", second, created, err)
	}
	history, err := store.DeliveryPolicyHistoryForApp(12)
	if err != nil || history.CurrentVersion != 2 || len(history.Versions) != 2 || history.Versions[0].Version != 1 || history.Versions[1].Version != 2 {
		t.Fatalf("policy history=%+v err=%v", history, err)
	}
}

func TestDeliveryPolicyRejectsInvalidOrUnconfirmedConfiguration(t *testing.T) {
	store := policyTestStore(false)
	for _, input := range []struct {
		strategy string
		service  string
		hours    int
	}{
		{strategy: "bogus"},
		{strategy: "blue_green"},
		{strategy: "blue_green", service: "bad/name"},
		{strategy: "blue_green", service: "orders-entry", hours: 721},
		{strategy: "rolling", hours: 24},
	} {
		if _, _, err := store.SaveDeliveryPolicyVersion(12, 0, input.strategy, input.service, input.hours, "alice"); !errors.Is(err, ErrValidation) {
			t.Fatalf("input=%+v error=%v", input, err)
		}
	}
	if _, _, err := policyTestStore(true).SaveDeliveryPolicyVersion(12, 0, "rolling", "", 0, "alice"); !errors.Is(err, ErrValidation) {
		t.Fatalf("unconfirmed binding error=%v", err)
	}
}

func TestGatewayCanaryPolicyRemainsDraftOnly(t *testing.T) {
	store := policyTestStore(false)
	if _, _, err := store.SaveDeliveryPolicyVersion(12, 0, "gateway_canary", "", 0, "alice"); !errors.Is(err, ErrValidation) {
		t.Fatalf("missing canary analysis policy error=%v", err)
	}
	analysis := testCanaryAnalysisPolicy()
	policy, created, err := store.SaveDeliveryPolicyVersion(12, 0, "gateway_canary", "", 0, "alice", &analysis)
	if err != nil || !created || policy.Version != 1 || policy.Readiness != "integration_pending" || !sameCanaryAnalysisPolicy(policy.CanaryAnalysis, &analysis) {
		t.Fatalf("gateway policy=%+v created=%t err=%v", policy, created, err)
	}
	*analysis.MaxCandidateErrorRate = 0.5
	current, err := store.CurrentDeliveryPolicy(41)
	if err != nil || current.CanaryAnalysis == nil || *current.CanaryAnalysis.MaxCandidateErrorRate != 0.05 {
		t.Fatalf("saved policy must not retain caller-owned pointers: policy=%+v err=%v", current, err)
	}
}

func TestGatewayCanaryPolicyRejectsPartialRouteIdentityAndCopiesCompleteIdentity(t *testing.T) {
	for name, invalidRoute := range map[string]CanaryGatewayRouteRef{
		"partial":       {ConnectionID: 7, RouteID: "orders-entry"},
		"same backends": {ConnectionID: 7, RouteID: "orders-entry", StableBackendID: "orders", CandidateBackendID: "orders"},
		"padded ID":     {ConnectionID: 7, RouteID: " orders-entry ", StableBackendID: "orders-stable", CandidateBackendID: "orders-candidate"},
	} {
		store := policyTestStore(false)
		analysis := testCanaryAnalysisPolicy()
		analysis.GatewayRoute = &invalidRoute
		if _, _, err := store.SaveDeliveryPolicyVersion(12, 0, "gateway_canary", "", 0, "alice", &analysis); !errors.Is(err, ErrValidation) {
			t.Fatalf("%s route identity should be rejected: %v", name, err)
		}
	}

	store := policyTestStore(false)
	analysis := testCanaryAnalysisPolicy()
	route := CanaryGatewayRouteRef{
		ConnectionID: 7, RouteID: "orders-entry", StableBackendID: "orders-stable", CandidateBackendID: "orders-candidate",
	}
	analysis.GatewayRoute = &route
	saved, created, err := store.SaveDeliveryPolicyVersion(12, 0, "gateway_canary", "", 0, "alice", &analysis)
	if err != nil || !created || saved.CanaryAnalysis == nil || saved.CanaryAnalysis.GatewayRoute == nil || *saved.CanaryAnalysis.GatewayRoute != route {
		t.Fatalf("complete route identity should be stored in the immutable policy: policy=%+v created=%t err=%v", saved, created, err)
	}
	route.RouteID = "mutated-after-save"
	current, err := store.CurrentDeliveryPolicy(41)
	if err != nil || current.CanaryAnalysis == nil || current.CanaryAnalysis.GatewayRoute == nil || current.CanaryAnalysis.GatewayRoute.RouteID != "orders-entry" {
		t.Fatalf("saved route identity must not retain caller-owned pointers: policy=%+v err=%v", current, err)
	}
}
