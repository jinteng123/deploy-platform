package httpapi

import (
	"encoding/json"
	"testing"

	"deploy-platform/backend-go/internal/config"
	"deploy-platform/backend-go/internal/service"
)

func TestDeliveryPreviewEndpointReturnsExplicitPartialState(t *testing.T) {
	cfg := config.Config{Env: "dev", MockRuntime: true, TransferDir: t.TempDir()}
	e := newCDEngine(t, cfg, cdStore())

	response := do(t, e, "POST", "/api/v1/delivery-preview", map[string]any{
		"cluster_id": 2, "namespace": "ns-prod", "deployment_name": "app-prod",
	})
	if response.Code != 200 {
		t.Fatalf("delivery preview status=%d body=%s", response.Code, response.Body.String())
	}
	var preview service.DeliveryCapabilityPreview
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if len(preview.Checks) != 6 {
		t.Fatalf("checks=%+v", preview.Checks)
	}
	if preview.Checks[0].Status != "unknown" || preview.Checks[1].Status != "unknown" {
		t.Fatalf("mock deployment without pod labels must stay unknown: %+v", preview.Checks)
	}
}

func TestDeliveryPreviewEndpointRequiresOperator(t *testing.T) {
	cfg := config.Config{Env: "dev", MockRuntime: true, TransferDir: t.TempDir()}
	store := cdStore()
	e := newCDEngine(t, cfg, store)
	viewer := seedUser(t, store, "preview-viewer", service.RoleViewer)

	response := do(t, e, "POST", "/api/v1/delivery-preview", map[string]any{
		"cluster_id": 2, "namespace": "ns-prod", "deployment_name": "app-prod",
	}, viewer)
	if response.Code != 403 {
		t.Fatalf("viewer delivery preview status=%d body=%s", response.Code, response.Body.String())
	}
}
