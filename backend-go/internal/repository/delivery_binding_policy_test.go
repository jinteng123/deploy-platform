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

func expectBindingUpsert(mock sqlmock.Sqlmock, pipeline *service.YunxiaoDeploymentPipelineBinding) {
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT c.environment_tier,e.environment_tier,e.requires_review,a.source_kind").WithArgs(int64(9), 2).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_tier", "environment_tier", "environment_review", "source_kind", "repo", "path", "image", "config_repo", "config_path"}).
			AddRow("production", "production", false, "repository", "https://git.example.com/orders.git", "services/api", "registry.example.com/orders/api", "", ""))
	mock.ExpectQuery("SELECT application_id FROM delivery_components WHERE id=\\? FOR UPDATE").WithArgs(int64(8)).
		WillReturnRows(sqlmock.NewRows([]string{"application_id"}).AddRow(int64(7)))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM delivery_bindings b JOIN apps a ON a.id=b.app_id").
		WithArgs(int64(8), 2, "repository", "https://git.example.com/orders.git", "services/api", "registry.example.com/orders/api", "", "").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	var connectionID any
	pipelineID := ""
	if pipeline != nil {
		connectionID = pipeline.ConnectionID
		pipelineID = pipeline.PipelineID
	}
	mock.ExpectExec("INSERT INTO delivery_bindings").WithArgs(
		2, int64(7), int64(8), int64(9), "blue_green", "orders-entry", connectionID, pipelineID,
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(41, 1))
	mock.ExpectExec("UPDATE delivery_applications SET requires_review=0 WHERE id=\\?").WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE delivery_components SET requires_review=0 WHERE id=\\?").WithArgs(int64(8)).WillReturnResult(sqlmock.NewResult(0, 1))
	bindingRows := sqlmock.NewRows(strings.Split(deliveryBindingColumns, ",")).AddRow(
		int64(41), 2, int64(7), int64(8), int64(9), "blue_green", "orders-entry", nullablePipelineConnection(pipeline), pipelineID, false, time.Now(),
		int64(2), "orders-prod", "Deployment", "orders-api", "deployment-uid", "42",
		int64(2), "orders-prod", "Deployment", "orders-api", "deployment-uid", "42",
	)
	mock.ExpectQuery("SELECT .* FROM delivery_bindings WHERE app_id=\\?").WithArgs(2).WillReturnRows(bindingRows)
}

func nullablePipelineConnection(pipeline *service.YunxiaoDeploymentPipelineBinding) any {
	if pipeline == nil {
		return nil
	}
	return pipeline.ConnectionID
}

func TestAssignDeliveryBindingAndPolicyCommitTogether(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	expectBindingUpsert(mock, nil)
	mock.ExpectQuery("SELECT binding_id,version,strategy,service_name,candidate_retention_hours,canary_analysis_json,created_by,created_at").
		WithArgs(int64(41)).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO delivery_policy_versions").WithArgs(int64(41), int64(1), "blue_green", "orders-entry", 48, nil, "operator", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	binding, policy, created, err := repo.AssignDeliveryBindingWithPolicy(
		service.DeliveryBinding{AppID: 2, ApplicationID: 7, ComponentID: 8, EnvironmentID: 9, StrategyDraft: "blue_green", ServiceName: "orders-entry",
			TemplateWorkloadRef: service.DeliveryWorkloadReference{ClusterID: 2, Namespace: "orders-prod", Kind: "Deployment", Name: "orders-api", UID: "deployment-uid", ResourceVersion: "42"},
			ActiveWorkloadRef:   service.DeliveryWorkloadReference{ClusterID: 2, Namespace: "orders-prod", Kind: "Deployment", Name: "orders-api", UID: "deployment-uid", ResourceVersion: "42"}},
		service.DeliveryPolicyVersion{Strategy: "blue_green", ServiceName: "orders-entry", CandidateRetentionHours: 48, Readiness: "configurable", CreatedBy: "operator"}, 0)
	if err != nil || binding.ID != 41 || policy.Version != 1 || policy.BindingID != binding.ID || !created {
		t.Fatalf("binding=%+v policy=%+v created=%v err=%v", binding, policy, created, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAssignDeliveryBindingPersistsSeparateDeploymentPipeline(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	pipeline := &service.YunxiaoDeploymentPipelineBinding{ConnectionID: 9, PipelineID: "123456"}
	expectBindingUpsert(mock, pipeline)
	mock.ExpectCommit()

	binding, err := repo.AssignDeliveryBinding(service.DeliveryBinding{
		AppID: 2, ApplicationID: 7, ComponentID: 8, EnvironmentID: 9,
		StrategyDraft: "blue_green", ServiceName: "orders-entry", DeploymentPipeline: pipeline,
		TemplateWorkloadRef: service.DeliveryWorkloadReference{ClusterID: 2, Namespace: "orders-prod", Kind: "Deployment", Name: "orders-api", UID: "deployment-uid", ResourceVersion: "42"},
		ActiveWorkloadRef:   service.DeliveryWorkloadReference{ClusterID: 2, Namespace: "orders-prod", Kind: "Deployment", Name: "orders-api", UID: "deployment-uid", ResourceVersion: "42"},
	})
	if err != nil || binding.DeploymentPipeline == nil || *binding.DeploymentPipeline != *pipeline {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAssignDeliveryBindingRollsBackWhenPolicyInsertFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	expectBindingUpsert(mock, nil)
	mock.ExpectQuery("SELECT binding_id,version,strategy,service_name,candidate_retention_hours,canary_analysis_json,created_by,created_at").
		WithArgs(int64(41)).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO delivery_policy_versions").WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()

	_, _, _, err = repo.AssignDeliveryBindingWithPolicy(
		service.DeliveryBinding{AppID: 2, ApplicationID: 7, ComponentID: 8, EnvironmentID: 9, StrategyDraft: "blue_green", ServiceName: "orders-entry"},
		service.DeliveryPolicyVersion{Strategy: "blue_green", ServiceName: "orders-entry", CandidateRetentionHours: 48, CreatedBy: "operator"}, 0)
	if err == nil {
		t.Fatal("expected policy insertion failure")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAssignDeliveryBindingRollsBackWhenPolicyVersionIsStale(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	expectBindingUpsert(mock, nil)
	mock.ExpectQuery("SELECT binding_id,version,strategy,service_name,candidate_retention_hours,canary_analysis_json,created_by,created_at").
		WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"binding_id", "version", "strategy", "service_name", "candidate_retention_hours", "canary_analysis_json", "created_by", "created_at"}).
		AddRow(int64(41), int64(1), "rolling", "", 0, nil, "operator", time.Now()))
	mock.ExpectRollback()

	_, _, _, err = repo.AssignDeliveryBindingWithPolicy(
		service.DeliveryBinding{AppID: 2, ApplicationID: 7, ComponentID: 8, EnvironmentID: 9, StrategyDraft: "blue_green", ServiceName: "orders-entry"},
		service.DeliveryPolicyVersion{Strategy: "blue_green", ServiceName: "orders-entry", CandidateRetentionHours: 48, CreatedBy: "operator"}, 0)
	if !errors.Is(err, service.ErrConflict) {
		t.Fatalf("stale policy error=%v, want conflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
