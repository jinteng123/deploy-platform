package e2e

import (
	"testing"
	"time"

	"deploy-platform/backend-go/internal/repository"
	"deploy-platform/backend-go/internal/service"
)

func TestDeliveryDriftMySQLPersistence(t *testing.T) {
	skipUnlessE2E(t)
	mysql := startMigrationE2EMySQL(t)
	applyE2ESchemaThroughMigration(t, mysql.container, mysql.rootPassword, mysql.database, "036")

	repo, err := repository.Open(mysql.dsn)
	if err != nil {
		t.Fatalf("open isolated drift database: %v", err)
	}
	defer repo.Close()

	started := time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC)
	expected := "registry.example/shop@sha256:expected"
	actual := "registry.example/shop:unexpected"
	baseline := int64(44)
	observation := service.DeliveryDriftObservation{
		AppID: 7, AppName: "shop", ClusterID: 3, Namespace: "prod", Workload: "shop-api", Container: "api",
		Status: "drifted", ExpectedImage: &expected, ActualImage: &actual, BaselineLogID: &baseline,
		ObservedBy: "operator", ObservedAt: started,
	}
	states, err := repo.ObserveDeliveryDrift([]service.DeliveryDriftObservation{observation})
	if err != nil || len(states) != 1 || states[0].DriftStartedAt == nil || !states[0].DriftStartedAt.Equal(started) {
		t.Fatalf("initial drift observation states=%+v err=%v", states, err)
	}

	observation.ObservedAt = started.Add(time.Minute)
	states, err = repo.ObserveDeliveryDrift([]service.DeliveryDriftObservation{observation})
	if err != nil || len(states) != 1 || states[0].ObservationCount != 2 {
		t.Fatalf("repeat drift observation states=%+v err=%v", states, err)
	}

	observation.Status = "in_sync"
	observation.ActualImage = &expected
	observation.ObservedAt = started.Add(5 * time.Minute)
	states, err = repo.ObserveDeliveryDrift([]service.DeliveryDriftObservation{observation})
	if err != nil || len(states) != 1 || states[0].ObservationCount != 3 || states[0].DriftStartedAt != nil {
		t.Fatalf("recovered drift observation states=%+v err=%v", states, err)
	}

	loaded, err := repo.DeliveryDriftStates()
	if err != nil || len(loaded) != 1 || loaded[0].Status != "in_sync" || loaded[0].ObservationCount != 3 || !loaded[0].LastObservedAt.Equal(observation.ObservedAt) {
		t.Fatalf("reloaded current drift state=%+v err=%v", loaded, err)
	}
	history, err := repo.DeliveryDriftHistory(service.DeliveryDriftHistoryFilter{ClusterID: 3, AppIDs: []int{7}, Limit: 10})
	if err != nil || len(history) != 2 || history[0].Status != "in_sync" || history[1].Status != "drifted" {
		t.Fatalf("drift transition history=%+v err=%v", history, err)
	}
	if history[0].DriftDurationSecs == nil || *history[0].DriftDurationSecs != 300 {
		t.Fatalf("recovery duration=%v, want 300 seconds", history[0].DriftDurationSecs)
	}
}
