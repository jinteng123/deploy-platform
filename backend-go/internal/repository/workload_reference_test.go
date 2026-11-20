package repository

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/service"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestPinDeliveryBindingWorkloadRefsUpdatesOnlyAfterExpectedIdentityMatches(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}

	expectedTemplate := service.DeliveryWorkloadReference{ClusterID: 2, Namespace: "prod", Kind: "Deployment", Name: "api"}
	expectedActive := expectedTemplate
	template := service.DeliveryWorkloadReference{ClusterID: 2, Namespace: "prod", Kind: "Deployment", Name: "api", UID: "uid-first", ResourceVersion: "11"}
	active := template
	created := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectQuery("(?s)SELECT template_workload_cluster_id.*FROM delivery_bindings WHERE id=\\? FOR UPDATE").
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{
			"template_workload_cluster_id", "template_workload_namespace", "template_workload_kind", "template_workload_name", "template_workload_uid", "template_workload_resource_version",
			"active_workload_cluster_id", "active_workload_namespace", "active_workload_kind", "active_workload_name", "active_workload_uid", "active_workload_resource_version",
		}).AddRow(2, "prod", "Deployment", "api", "", "", 2, "prod", "Deployment", "api", "", ""))
	mock.ExpectExec("UPDATE delivery_bindings SET\\s+template_workload_cluster_id=").
		WithArgs(2, "prod", "Deployment", "api", "uid-first", "11", 2, "prod", "Deployment", "api", "uid-first", "11", int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	row := sqlmock.NewRows(strings.Split(deliveryBindingColumns, ",")).AddRow(
		int64(9), 4, int64(7), int64(8), int64(6), nil, "", nil, "", false, created,
		2, "prod", "Deployment", "api", "uid-first", "11",
		2, "prod", "Deployment", "api", "uid-first", "11",
	)
	mock.ExpectQuery("SELECT .* FROM delivery_bindings WHERE id=\\?").WithArgs(int64(9)).WillReturnRows(row)
	mock.ExpectCommit()

	binding, err := repo.PinDeliveryBindingWorkloadRefs(9, expectedTemplate, expectedActive, template, active)
	if err != nil {
		t.Fatalf("pin workload identity: %v", err)
	}
	if binding.ActiveWorkloadRef != active || binding.TemplateWorkloadRef != template {
		t.Fatalf("persisted references=%+v", binding)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPinDeliveryBindingWorkloadRefsRollsBackOnConcurrentRebind(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	ref := service.DeliveryWorkloadReference{ClusterID: 2, Namespace: "prod", Kind: "Deployment", Name: "api"}
	mock.ExpectBegin()
	mock.ExpectQuery("(?s)SELECT template_workload_cluster_id.*FROM delivery_bindings WHERE id=\\? FOR UPDATE").
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{
			"template_workload_cluster_id", "template_workload_namespace", "template_workload_kind", "template_workload_name", "template_workload_uid", "template_workload_resource_version",
			"active_workload_cluster_id", "active_workload_namespace", "active_workload_kind", "active_workload_name", "active_workload_uid", "active_workload_resource_version",
		}).AddRow(2, "prod", "Deployment", "api", "", "", 2, "prod", "Deployment", "api", "replacement", "12"))
	mock.ExpectRollback()
	newRef := ref
	newRef.UID = "first-observation"
	if _, err := repo.PinDeliveryBindingWorkloadRefs(9, ref, ref, newRef, newRef); !errors.Is(err, service.ErrConflict) {
		t.Fatalf("concurrent reference update error=%v, want conflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryRunPromotionUpdatesAppAndActiveReferenceInSameTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now().UTC()
	oldRef := service.DeliveryWorkloadReference{ClusterID: 2, Namespace: "prod", Kind: "Deployment", Name: "api", UID: "uid-stable", ResourceVersion: "12"}
	newRef := service.DeliveryWorkloadReference{ClusterID: 2, Namespace: "prod", Kind: "Deployment", Name: "api-plan-7", UID: "uid-candidate", ResourceVersion: "19"}
	promotion := service.DeliveryBindingWorkloadPromotion{AppID: 4, ExpectedDeployment: oldRef.Name, ExpectedActiveWorkloadRef: oldRef, ActiveWorkloadRef: newRef}
	step := "success"
	log := service.DeployLog{ID: 20, ClusterID: 2, Namespace: "prod", DeploymentName: "api", Status: "success", CurrentStep: &step, StepDetail: "[]", StartedAt: &now, FinishedAt: &now}
	event := service.DeliveryRunEvent{EventKey: "control:live-verified", EventType: "control", Stage: "success", Status: "success", Detail: "confirmed"}
	initialRun := sqlmock.NewRows(strings.Split(deliveryRunColumns, ",")).AddRow(7, 41, 1, 20, "blue_green", "running", "awaiting_live_verification", 3, nil, now, now)
	updatedRun := sqlmock.NewRows(strings.Split(deliveryRunColumns, ",")).AddRow(7, 41, 1, 20, "blue_green", "success", "success", 4, nil, now, now)
	updatedEvent := sqlmock.NewRows(strings.Split(deliveryRunEventColumns, ",")).AddRow(11, 7, 4, event.EventKey, event.EventType, event.Stage, event.Status, event.Detail, nil, now)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE deploy_log_id=\\? FOR UPDATE").WithArgs(int64(20)).WillReturnRows(initialRun)
	mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), event.EventKey).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT id,status FROM deploy_logs WHERE id=\\? FOR UPDATE").WithArgs(int64(20)).WillReturnRows(sqlmock.NewRows([]string{"id", "status"}).AddRow(20, "running"))
	mock.ExpectQuery("(?s)SELECT a.deployment_name,a.cluster_id,a.namespace,.*FROM apps a JOIN delivery_bindings").WithArgs(4).
		WillReturnRows(sqlmock.NewRows([]string{"deployment_name", "cluster_id", "namespace", "active_cluster", "active_namespace", "active_kind", "active_name", "active_uid", "active_rv"}).
			AddRow("api", 2, "prod", oldRef.ClusterID, oldRef.Namespace, oldRef.Kind, oldRef.Name, oldRef.UID, oldRef.ResourceVersion))
	mock.ExpectExec("UPDATE apps SET deployment_name=\\?").WithArgs(newRef.Name, 4, oldRef.Name).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE delivery_bindings SET active_workload_cluster_id=").WithArgs(newRef.ClusterID, newRef.Namespace, newRef.Kind, newRef.Name, newRef.UID, newRef.ResourceVersion, 4).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE deploy_logs SET status=").WithArgs("success", "success", nil, "[]", nil, &now, &now, int64(20)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM delivery_resource_locks WHERE deploy_log_id=\\?").WithArgs(int64(20)).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("UPDATE delivery_runs SET status=\\?.*WHERE id=\\? AND version=\\?").WithArgs("success", "success", int64(4), nil, sqlmock.AnyArg(), int64(7), int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO delivery_run_events").WithArgs(int64(7), int64(4), event.EventKey, event.EventType, event.Stage, event.Status, event.Detail, nil, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(11, 1))
	mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE id=\\?").WithArgs(int64(7)).WillReturnRows(updatedRun)
	mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), event.EventKey).WillReturnRows(updatedEvent)
	mock.ExpectCommit()

	run, savedEvent, duplicate, err := repo.TransitionDeliveryRunExecutionWithWorkloadPromotion(log, 3, event, promotion)
	if err != nil || duplicate || run.Status != "success" || savedEvent.ID != 11 {
		t.Fatalf("workload promotion run=%+v event=%+v duplicate=%v error=%v", run, savedEvent, duplicate, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExplicitWorkloadRebindChecksResourceLockAndPreservesTemplate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now().UTC()
	template := service.DeliveryWorkloadReference{ClusterID: 2, Namespace: "prod", Kind: "Deployment", Name: "api", UID: "template-uid", ResourceVersion: "3"}
	oldActive := template
	oldActive.UID = "old-active-uid"
	active := oldActive
	active.UID = "replacement-uid"
	active.ResourceVersion = "19"
	mock.ExpectBegin()
	mock.ExpectQuery("(?s)SELECT template_workload_cluster_id.*FROM delivery_bindings WHERE id=\\? FOR UPDATE").
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{
			"template_workload_cluster_id", "template_workload_namespace", "template_workload_kind", "template_workload_name", "template_workload_uid", "template_workload_resource_version",
			"active_workload_cluster_id", "active_workload_namespace", "active_workload_kind", "active_workload_name", "active_workload_uid", "active_workload_resource_version",
		}).AddRow(2, "prod", "Deployment", "api", template.UID, template.ResourceVersion, 2, "prod", "Deployment", "api", oldActive.UID, oldActive.ResourceVersion))
	mock.ExpectQuery("SELECT a.cluster_id,a.namespace,a.deployment_name FROM apps a JOIN delivery_bindings").WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "namespace", "deployment_name"}).AddRow(2, "prod", "api"))
	lock := service.ResourceLock{ClusterID: 2, Namespace: "prod", Kind: "deployment", Name: "api"}
	mock.ExpectQuery("SELECT deploy_log_id FROM delivery_resource_locks WHERE resource_key=\\? FOR UPDATE").WithArgs(resourceLockKey(lock)).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("UPDATE delivery_bindings SET\\s+template_workload_cluster_id=").
		WithArgs(2, "prod", "Deployment", "api", template.UID, template.ResourceVersion, 2, "prod", "Deployment", "api", active.UID, active.ResourceVersion, int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	row := sqlmock.NewRows(strings.Split(deliveryBindingColumns, ",")).AddRow(
		int64(9), 4, int64(7), int64(8), int64(6), nil, "", nil, "", false, now,
		2, "prod", "Deployment", "api", template.UID, template.ResourceVersion,
		2, "prod", "Deployment", "api", active.UID, active.ResourceVersion,
	)
	mock.ExpectQuery("SELECT .* FROM delivery_bindings WHERE id=\\?").WithArgs(int64(9)).WillReturnRows(row)
	mock.ExpectCommit()

	binding, err := repo.RebindDeliveryBindingActiveWorkloadRef(9, template, oldActive, template, active)
	if err != nil {
		t.Fatalf("rebind active workload: %v", err)
	}
	if binding.TemplateWorkloadRef != template || binding.ActiveWorkloadRef != active {
		t.Fatalf("template or active identity incorrect: %+v", binding)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
