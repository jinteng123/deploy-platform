package service

import (
	"testing"
	"time"
)

func TestDeliveryDriftObservationsTrackTransitionsAndDuration(t *testing.T) {
	store := NewStore()
	start := time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC)
	expected := "registry.example/shop/api@sha256:" + repeatHex("a", 64)
	actual := "registry.example/shop/api@sha256:" + repeatHex("b", 64)
	recovered := expected
	baselineID := int64(42)
	observe := func(at time.Time, status string, image *string, reason string) DeliveryDriftState {
		t.Helper()
		rows, err := store.ObserveDeliveryDrift([]DeliveryDriftObservation{{
			AppID: 7, AppName: "shop-api", ClusterID: 2, Namespace: "prod", Workload: "api", Container: "api",
			Status: status, ExpectedImage: &expected, ActualImage: image, BaselineLogID: &baselineID,
			Reason: reason, ObservedBy: "operator", ObservedAt: at,
		}})
		if err != nil || len(rows) != 1 {
			t.Fatalf("observe status=%s: states=%+v err=%v", status, rows, err)
		}
		return rows[0]
	}

	unknown := observe(start, "unknown", nil, "读取当前镜像失败")
	if unknown.DriftStartedAt != nil || unknown.ObservationCount != 1 || unknown.ObservedAt != start {
		t.Fatalf("initial unknown state=%+v", unknown)
	}

	driftAt := start.Add(time.Minute)
	drifted := observe(driftAt, "drifted", &actual, "")
	if drifted.DriftStartedAt == nil || !drifted.DriftStartedAt.Equal(driftAt) {
		t.Fatalf("drift start not recorded: %+v", drifted)
	}
	repeated := observe(driftAt.Add(time.Minute), "drifted", &actual, "")
	if repeated.ObservationCount != 3 || !repeated.DriftStartedAt.Equal(driftAt) {
		t.Fatalf("repeated observation reset drift state: %+v", repeated)
	}

	changedImage := "registry.example/shop/api@sha256:" + repeatHex("c", 64)
	imageChangeAt := driftAt.Add(2 * time.Minute)
	changed := observe(imageChangeAt, "drifted", &changedImage, "")
	if !changed.DriftStartedAt.Equal(driftAt) {
		t.Fatalf("image change reset drift duration: %+v", changed)
	}

	unknownAgain := observe(driftAt.Add(3*time.Minute), "unknown", nil, "读取当前镜像失败")
	if unknownAgain.DriftStartedAt == nil || !unknownAgain.DriftStartedAt.Equal(driftAt) {
		t.Fatalf("unknown read incorrectly cleared active drift: %+v", unknownAgain)
	}

	recoveredAt := driftAt.Add(5 * time.Minute)
	inSync := observe(recoveredAt, "in_sync", &recovered, "")
	if inSync.DriftStartedAt != nil || inSync.ObservationCount != 6 {
		t.Fatalf("recovery did not close active drift: %+v", inSync)
	}

	events, err := store.DeliveryDriftHistory(DeliveryDriftHistoryFilter{ClusterID: 2, AppIDs: []int{7}, Limit: 25})
	if err != nil || len(events) != 5 {
		t.Fatalf("transition history=%+v err=%v", events, err)
	}
	if events[0].Status != "in_sync" || events[0].DriftDurationSecs == nil || *events[0].DriftDurationSecs != 5*60 {
		t.Fatalf("recovery event duration=%+v", events[0])
	}
	if events[1].Status != "unknown" || events[2].ChangeKind != "image" || events[3].Status != "drifted" || events[4].ChangeKind != "initial" {
		t.Fatalf("unexpected transition ordering: %+v", events)
	}
	if filtered, err := store.DeliveryDriftHistory(DeliveryDriftHistoryFilter{ClusterID: 2, AppIDs: []int{99}, Limit: 25}); err != nil || len(filtered) != 0 {
		t.Fatalf("app filter returned unrelated history: rows=%+v err=%v", filtered, err)
	}
}

func TestDeliveryDriftObservationAndHistoryValidation(t *testing.T) {
	store := NewStore()
	base := DeliveryDriftObservation{AppID: 1, AppName: "api", ClusterID: 1, Namespace: "prod", Workload: "api", Container: "api", Status: "unknown", ObservedBy: "operator"}
	if _, err := store.ObserveDeliveryDrift([]DeliveryDriftObservation{{}}); err == nil {
		t.Fatal("empty target accepted")
	}
	base.Status = "healthy"
	if _, err := store.ObserveDeliveryDrift([]DeliveryDriftObservation{base}); err == nil {
		t.Fatal("unknown status accepted")
	}
	if _, err := store.DeliveryDriftHistory(DeliveryDriftHistoryFilter{ClusterID: 0}); err == nil {
		t.Fatal("missing cluster accepted")
	}
	if _, err := store.DeliveryDriftHistory(DeliveryDriftHistoryFilter{ClusterID: 1, Limit: MaxDriftHistoryLimit + 1}); err == nil {
		t.Fatal("unbounded history accepted")
	}
}

func repeatHex(char string, count int) string {
	result := ""
	for range count {
		result += char
	}
	return result
}
