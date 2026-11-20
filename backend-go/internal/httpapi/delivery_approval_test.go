package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"deploy-platform/backend-go/internal/service"
)

func confirmApprovalFixture(store *service.Store) {
	model := store.DeliveryModel()
	for i := range model.Environments {
		if model.Environments[i].ID == 2 {
			model.Environments[i].Name = "生产环境"
			model.Environments[i].RequiresReview = false
		}
	}
	for i := range model.Applications {
		if model.Applications[i].ID == 2 {
			model.Applications[i].RequiresReview = false
		}
	}
	for i := range model.Components {
		if model.Components[i].ID == 2 {
			model.Components[i].RequiresReview = false
		}
	}
	for i := range model.Bindings {
		if model.Bindings[i].AppID == 2 {
			model.Bindings[i].RequiresReview = false
		}
	}
	store.LoadDeliveryModel(model)
}

func TestDeliveryApprovalGrantAdminAPI(t *testing.T) {
	store := cdStore()
	confirmApprovalFixture(store)
	e := newCDEngine(t, cdConfig(t), store)
	operatorToken := seedUser(t, store, "scoped-operator", service.RoleOperator)
	operator, err := store.FindUserByUsername("scoped-operator")
	if err != nil {
		t.Fatal(err)
	}

	path := "/api/v1/delivery/approval-grants/users/" + fmt.Sprint(operator.ID)
	if w := do(t, e, http.MethodPut, path, map[string]any{"scopes": []any{
		map[string]any{"application_id": 2, "environment_id": 2},
	}}); w.Code != http.StatusOK {
		t.Fatalf("admin grant → %d %s", w.Code, w.Body.String())
	}
	if allowed, err := store.CanApproveDeliveryTarget(operator.Username, 2); err != nil || !allowed {
		t.Fatalf("saved grant not effective: allowed=%t err=%v", allowed, err)
	}

	if w := do(t, e, http.MethodPut, path, map[string]any{"scopes": []any{
		map[string]any{"application_id": 2, "environment_id": 1},
	}}); w.Code != http.StatusBadRequest {
		t.Fatalf("non-production scope → %d %s", w.Code, w.Body.String())
	}
	if w := do(t, e, http.MethodPut, path, map[string]any{"scopes": []any{}}, operatorToken); w.Code != http.StatusForbidden {
		t.Fatalf("operator scope management → %d %s", w.Code, w.Body.String())
	}
	if w := do(t, e, http.MethodPut, path, map[string]any{"scopes": []any{}}); w.Code != http.StatusOK {
		t.Fatalf("revoke all scopes → %d %s", w.Code, w.Body.String())
	}
	if allowed, err := store.CanApproveDeliveryTarget(operator.Username, 2); err != nil || allowed {
		t.Fatalf("revoked grant still effective: allowed=%t err=%v", allowed, err)
	}

	w := do(t, e, http.MethodGet, "/api/v1/delivery/approval-grants", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list scopes → %d %s", w.Code, w.Body.String())
	}
	var grants []service.DeliveryApprovalGrant
	if err := json.Unmarshal(w.Body.Bytes(), &grants); err != nil {
		t.Fatal(err)
	}
	if len(grants) != 0 {
		t.Fatalf("revoked scope remains in list: %+v", grants)
	}
}
