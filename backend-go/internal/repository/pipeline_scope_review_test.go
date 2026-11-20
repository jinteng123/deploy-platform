package repository

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/service"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestSaveDeliveryPipelineScopeReviewVerifiesCurrentBinding(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	review := service.DeliveryPipelineScopeReview{
		BindingID: 41, ConnectionID: 9, PipelineID: "123456",
		DefinitionHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		WriteScope:     service.PipelineScopeConfigurationOnly, Reviewer: "operator-a",
		ReviewedAt: time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC),
	}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT deployment_pipeline_connection_id,deployment_pipeline_id FROM delivery_bindings WHERE id=\\? FOR UPDATE").
		WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"connection_id", "pipeline_id"}).AddRow(int64(9), "123456"))
	mock.ExpectExec("INSERT INTO delivery_pipeline_scope_reviews").WithArgs(
		int64(41), int64(9), "123456", review.DefinitionHash, service.PipelineScopeConfigurationOnly,
		"operator-a", review.ReviewedAt,
	).WillReturnResult(sqlmock.NewResult(72, 1))
	mock.ExpectCommit()
	saved, err := repo.SaveDeliveryPipelineScopeReview(review)
	if err != nil || saved.ID != 72 || saved.BindingID != 41 || saved.WriteScope != service.PipelineScopeConfigurationOnly {
		t.Fatalf("saved review=%+v err=%v", saved, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSaveDeliveryPipelineScopeReviewRejectsChangedBinding(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT deployment_pipeline_connection_id,deployment_pipeline_id FROM delivery_bindings WHERE id=\\? FOR UPDATE").
		WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"connection_id", "pipeline_id"}).AddRow(int64(9), "654321"))
	mock.ExpectRollback()
	_, err = repo.SaveDeliveryPipelineScopeReview(service.DeliveryPipelineScopeReview{
		BindingID: 41, ConnectionID: 9, PipelineID: "123456",
	})
	if !errors.Is(err, service.ErrConflict) {
		t.Fatalf("changed binding should conflict, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLatestDeliveryPipelineScopeReviewsReturnsNewestPerBinding(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	rows := sqlmock.NewRows([]string{"id", "delivery_binding_id", "connection_id", "pipeline_id", "definition_hash", "write_scope", "reviewer", "reviewed_at"}).
		AddRow(int64(81), int64(41), int64(9), "123456", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", service.PipelineScopeUnknown, "operator-b", time.Now().UTC())
	mock.ExpectQuery("SELECT r.id,r.delivery_binding_id,r.connection_id,r.pipeline_id,r.definition_hash,r.write_scope,r.reviewer,r.reviewed_at").WillReturnRows(rows)
	reviews, err := repo.LatestDeliveryPipelineScopeReviews()
	if err != nil || len(reviews) != 1 || reviews[0].ID != 81 || reviews[0].BindingID != 41 {
		t.Fatalf("latest reviews=%+v err=%v", reviews, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLatestDeliveryPipelineScopeReviewsSurfacesSchemaErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	mock.ExpectQuery("SELECT r.id,r.delivery_binding_id,r.connection_id,r.pipeline_id,r.definition_hash,r.write_scope,r.reviewer,r.reviewed_at").WillReturnError(sql.ErrNoRows)
	if _, err := repo.LatestDeliveryPipelineScopeReviews(); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("schema/read error swallowed: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
