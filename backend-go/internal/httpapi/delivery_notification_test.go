package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/service"
)

func TestActiveDeliveryNotificationsUseBoundedStableCursorPages(t *testing.T) {
	app, engine, _ := newTestApp(t)
	base := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	app.store.LoadNotifications([]service.DeliveryNotification{
		{ID: "a-older", Event: "deploy_failed", Payload: "secret payload", Status: "dead", CreatedAt: base},
		{ID: "b-delivered", Event: "deploy_finished", Status: "delivered", CreatedAt: base},
		{ID: "d-pending", Event: "deploy_finished", Status: "pending", CreatedAt: base},
		{ID: "c-dead", Event: "deploy_failed", Status: "dead", CreatedAt: base},
	})

	firstResponse := do(t, engine, http.MethodGet, "/api/v1/delivery/notifications/active?limit=2", nil)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first page: %d %s", firstResponse.Code, firstResponse.Body.String())
	}
	var first service.DeliveryNotificationPage
	if err := json.Unmarshal(firstResponse.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != "d-pending" || first.Items[1].ID != "c-dead" {
		t.Fatalf("first page was not newest-first or included delivered rows: %+v", first.Items)
	}
	if !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first page cursor: %+v", first)
	}
	if strings.Contains(firstResponse.Body.String(), "secret payload") {
		t.Fatal("notification page exposed payload")
	}
	updatedRows := app.store.Notifications()
	for i := range updatedRows {
		if updatedRows[i].ID == "c-dead" {
			updatedRows[i].Status = "delivered"
		}
	}
	app.store.LoadNotifications(updatedRows)

	secondPath := "/api/v1/delivery/notifications/active?limit=2&cursor=" + url.QueryEscape(first.NextCursor)
	secondResponse := do(t, engine, http.MethodGet, secondPath, nil)
	if secondResponse.Code != http.StatusOK {
		t.Fatalf("second page: %d %s", secondResponse.Code, secondResponse.Body.String())
	}
	var second service.DeliveryNotificationPage
	if err := json.Unmarshal(secondResponse.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].ID != "a-older" || second.HasMore || second.NextCursor != "" {
		t.Fatalf("second page: %+v", second)
	}

	for _, path := range []string{
		"/api/v1/delivery/notifications/active?limit=0",
		"/api/v1/delivery/notifications/active?limit=101",
		"/api/v1/delivery/notifications/active?limit=bad",
		"/api/v1/delivery/notifications/active?cursor=missing",
		"/api/v1/delivery/notifications/active?cursor=" + strings.Repeat("x", 513),
	} {
		if response := do(t, engine, http.MethodGet, path, nil); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid page request %s: %d %s", path, response.Code, response.Body.String())
		}
	}

	viewer := seedUser(t, app.store, "notification-viewer", service.RoleViewer)
	if response := do(t, engine, http.MethodGet, "/api/v1/delivery/notifications/active", nil, viewer); response.Code != http.StatusForbidden {
		t.Fatalf("viewer read admin notification alerts: %d", response.Code)
	}
	if response := do(t, engine, http.MethodGet, "/api/v1/delivery/notifications/active", nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous read notification alerts: %d", response.Code)
	}
}
