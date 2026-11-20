package service

import (
	"context"
	"errors"
	"testing"
)

func TestOperatorApprovalIsScopedToConfirmedApplicationAndEnvironment(t *testing.T) {
	_, store, _, _, _, cd := cdFixture(t)
	allowed, err := store.CanApproveDeliveryTarget("bob", 2)
	if err != nil || !allowed {
		t.Fatalf("explicitly granted operator not authorized: allowed=%t err=%v", allowed, err)
	}

	if _, err := store.ReplaceDeliveryApprovalGrants(2, nil, "test-admin"); err != nil {
		t.Fatal(err)
	}
	allowed, err = store.CanApproveDeliveryTarget("bob", 2)
	if err != nil || allowed {
		t.Fatalf("revoked operator scope still authorizes: allowed=%t err=%v", allowed, err)
	}

	release := seedRelease(store, "success", nil)
	store.releases = append(store.releases, release)
	request, err := cd.RequestPromotion(release.ID, 2, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cd.ApprovePromotion(context.Background(), request.ID, "bob"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unscoped operator approval error=%v, want forbidden", err)
	}
	stored, err := store.FindPromotionRequest(request.ID)
	if err != nil || stored.Status != "pending" {
		t.Fatalf("denied approval changed request: request=%+v err=%v", stored, err)
	}
	if calls := store.Logs(2, "", "", 0); len(calls) != 0 {
		t.Fatalf("denied approval queued a production deployment: %+v", calls)
	}
	if _, err := cd.RejectPromotion(request.ID, "bob", "deny"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unscoped operator rejection error=%v, want forbidden", err)
	}
}

func TestApprovalGrantRequiresReviewedProductionBinding(t *testing.T) {
	_, store, _, _, _, _ := cdFixture(t)
	if _, err := store.ReplaceDeliveryApprovalGrants(2, []DeliveryApprovalScope{{ApplicationID: 2, EnvironmentID: 1}}, "test-admin"); !errors.Is(err, ErrValidation) {
		t.Fatalf("non-production environment grant error=%v, want validation", err)
	}

	model := store.DeliveryModel()
	for i := range model.Bindings {
		if model.Bindings[i].AppID == 2 {
			model.Bindings[i].RequiresReview = true
		}
	}
	store.LoadDeliveryModel(model)
	if _, err := store.ReplaceDeliveryApprovalGrants(2, []DeliveryApprovalScope{{ApplicationID: 2, EnvironmentID: 2}}, "test-admin"); !errors.Is(err, ErrValidation) {
		t.Fatalf("unreviewed binding grant error=%v, want validation", err)
	}
}

func TestApprovalAdministratorDoesNotNeedOperatorGrant(t *testing.T) {
	_, store, _, _, _, _ := cdFixture(t)
	store.LoadUsers(append(store.Users(), User{ID: 6, Username: "root", Role: RoleAdmin, Enabled: true}))
	allowed, err := store.CanApproveDeliveryTarget("root", 2)
	if err != nil || !allowed {
		t.Fatalf("admin should retain global approval authority: allowed=%t err=%v", allowed, err)
	}
}
