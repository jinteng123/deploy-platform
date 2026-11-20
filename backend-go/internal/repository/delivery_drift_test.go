package repository

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/service"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestObserveDeliveryDriftPersistsStateAndTransitionAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Date(2026, 10, 10, 1, 2, 3, 0, time.UTC)
	expected := "registry.example/app:expected"
	actual := "registry.example/app:actual"
	baseline := int64(5)
	observation := service.DeliveryDriftObservation{
		AppID: 9, AppName: "payments", ClusterID: 3, Namespace: "prod", Workload: "api", Container: "api",
		Status: "drifted", ExpectedImage: &expected, ActualImage: &actual, BaselineLogID: &baseline,
		ObservedBy: "operator", ObservedAt: now, Source: service.DriftObservationSource,
	}
	key := service.DeliveryDriftTargetKey(observation)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM delivery_drift_states WHERE target_key=\\? FOR UPDATE").WithArgs(key).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO delivery_drift_states").WithArgs(
		key, 9, "payments", 3, "prod", "api", "api", "drifted", &expected, &actual, &baseline, nil, now, now, int64(1), "operator", service.DriftObservationSource,
	).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO delivery_drift_events").WithArgs(
		key, 9, "payments", 3, "prod", "api", "api", nil, "drifted", "initial", &expected, &actual, &baseline, nil, now, nil, now, "operator", service.DriftObservationSource,
	).WillReturnResult(sqlmock.NewResult(11, 1))
	mock.ExpectCommit()

	states, err := repo.ObserveDeliveryDrift([]service.DeliveryDriftObservation{observation})
	if err != nil || len(states) != 1 || states[0].TargetKey != key || states[0].DriftStartedAt == nil || !states[0].DriftStartedAt.Equal(now) {
		t.Fatalf("states=%+v err=%v", states, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestObserveDeliveryDriftUpdatesCurrentStateWithoutDuplicateEvent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	started := time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC)
	now := started.Add(time.Minute)
	expected := "registry.example/app:expected"
	actual := "registry.example/app:actual"
	baseline := int64(5)
	observation := service.DeliveryDriftObservation{
		AppID: 9, AppName: "payments", ClusterID: 3, Namespace: "prod", Workload: "api", Container: "api",
		Status: "drifted", ExpectedImage: &expected, ActualImage: &actual, BaselineLogID: &baseline,
		ObservedBy: "operator", ObservedAt: now, Source: service.DriftObservationSource,
	}
	key := service.DeliveryDriftTargetKey(observation)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM delivery_drift_states WHERE target_key=\\? FOR UPDATE").WithArgs(key).
		WillReturnRows(sqlmock.NewRows(strings.Split(deliveryDriftStateColumns, ",")).AddRow(key, 9, "payments", 3, "prod", "api", "api", "drifted", expected, actual, baseline, nil, started, started, 1, "operator", service.DriftObservationSource))
	mock.ExpectExec("UPDATE delivery_drift_states SET").WithArgs("payments", "drifted", &expected, &actual, &baseline, nil, &started, now, int64(2), "operator", service.DriftObservationSource, sqlmock.AnyArg(), key).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	states, err := repo.ObserveDeliveryDrift([]service.DeliveryDriftObservation{observation})
	if err != nil || len(states) != 1 || states[0].ObservationCount != 2 || states[0].DriftStartedAt == nil || !states[0].DriftStartedAt.Equal(started) {
		t.Fatalf("states=%+v err=%v", states, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryDriftHistoryUsesBoundedClusterAndAppFilters(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now().UTC()
	mock.ExpectQuery("FROM delivery_drift_events WHERE cluster_id=\\? AND app_id IN \\(\\?,\\?\\) ORDER BY observed_at DESC,id DESC LIMIT \\?").
		WithArgs(3, 7, 9, 25).
		WillReturnRows(sqlmock.NewRows([]string{"id", "target_key", "app_id", "app_name", "cluster_id", "namespace", "deployment_name", "container_name", "previous_status", "status", "change_kind", "expected_image", "actual_image", "baseline_log_id", "reason", "drift_started_at", "drift_duration_seconds", "observed_at", "observed_by", "source"}).
			AddRow(10, strings.Repeat("a", 64), 7, "payments", 3, "prod", "api", "api", "in_sync", "drifted", "status", "expected", "actual", 5, nil, now, nil, now, "operator", service.DriftObservationSource))
	events, err := repo.DeliveryDriftHistory(service.DeliveryDriftHistoryFilter{ClusterID: 3, AppIDs: []int{7, 9}, Limit: 25})
	if err != nil || len(events) != 1 || events[0].Status != "drifted" || events[0].PreviousStatus != "in_sync" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
