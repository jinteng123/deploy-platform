package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/service"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestNotificationAlertPageUsesBoundedStatusKeysetQueries(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	createdAt := time.Date(2026, 10, 14, 8, 30, 0, 0, time.UTC)
	columns := []string{"id", "event", "payload", "status", "attempts", "next_attempt_at", "created_at", "last_error"}
	row := func(id, status string) *sqlmock.Rows {
		return sqlmock.NewRows(columns).AddRow(id, "deploy_failed", `{"id":"`+id+`"}`, status, 2, createdAt, createdAt, "")
	}

	mock.ExpectQuery(`SELECT id,event,payload,status,attempts,next_attempt_at,created_at,last_error FROM delivery_notifications WHERE status = \? ORDER BY created_at DESC,id DESC LIMIT \?`).
		WithArgs("pending", 3).WillReturnRows(row("z-new", "pending").AddRow("x-old", "deploy_failed", `{"id":"x-old"}`, "pending", 1, createdAt, createdAt, ""))
	mock.ExpectQuery(`SELECT id,event,payload,status,attempts,next_attempt_at,created_at,last_error FROM delivery_notifications WHERE status = \? ORDER BY created_at DESC,id DESC LIMIT \?`).
		WithArgs("dead", 3).WillReturnRows(row("y-dead", "dead"))

	first, err := repo.NotificationAlertsPage(context.Background(), nil, 3)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first) != 3 || first[0].ID != "z-new" || first[1].ID != "y-dead" || first[2].ID != "x-old" {
		t.Fatalf("merged status pages not globally ordered: %+v", first)
	}

	cursor := &service.DeliveryNotificationCursor{CreatedAt: createdAt, ID: "y-dead"}
	cursorQuery := `SELECT id,event,payload,status,attempts,next_attempt_at,created_at,last_error FROM delivery_notifications WHERE status = \? AND \(created_at < \? OR \(created_at = \? AND id < \?\)\) ORDER BY created_at DESC,id DESC LIMIT \?`
	mock.ExpectQuery(cursorQuery).WithArgs("pending", createdAt, createdAt, "y-dead", 2).WillReturnRows(row("x-old", "pending"))
	mock.ExpectQuery(cursorQuery).WithArgs("dead", createdAt, createdAt, "y-dead", 2).WillReturnRows(sqlmock.NewRows(columns))

	second, err := repo.NotificationAlertsPage(context.Background(), cursor, 2)
	if err != nil || len(second) != 1 || second[0].ID != "x-old" {
		t.Fatalf("keyset continuation=%+v err=%v", second, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationQueueUsesBoundedDueReadsAndIdempotentCapture(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	columns := []string{"id", "event", "payload", "status", "attempts", "next_attempt_at", "created_at", "last_error"}
	mock.ExpectQuery(`SELECT id,event,payload,status,attempts,next_attempt_at,created_at,last_error FROM delivery_notifications WHERE status='pending' AND next_attempt_at<=\? ORDER BY next_attempt_at,created_at,id LIMIT \?`).
		WithArgs(now, 25).WillReturnRows(sqlmock.NewRows(columns).AddRow("n1", "deploy_finished", `{"id":"n1"}`, "pending", 1, now, now, ""))
	due, err := repo.DueNotifications(context.Background(), now, 25)
	if err != nil || len(due) != 1 || due[0].ID != "n1" {
		t.Fatalf("due notification page=%+v err=%v", due, err)
	}

	row := service.DeliveryNotification{ID: "n2", Event: "deploy_failed", Payload: `{"id":"n2"}`, Status: "pending", NextAttemptAt: now, CreatedAt: now}
	insertQuery := `INSERT IGNORE INTO delivery_notifications\(id,event,payload,status,attempts,next_attempt_at,created_at,last_error\) VALUES\(\?,\?,\?,\?,\?,\?,\?,\?\)`
	mock.ExpectExec(insertQuery).WithArgs(row.ID, row.Event, row.Payload, row.Status, row.Attempts, row.NextAttemptAt, row.CreatedAt, row.LastError).WillReturnResult(sqlmock.NewResult(0, 1))
	created, err := repo.InsertNotificationIfAbsent(context.Background(), row)
	if err != nil || !created {
		t.Fatalf("insert notification created=%t err=%v", created, err)
	}
	mock.ExpectExec(insertQuery).WithArgs(row.ID, row.Event, row.Payload, row.Status, row.Attempts, row.NextAttemptAt, row.CreatedAt, row.LastError).WillReturnResult(sqlmock.NewResult(0, 0))
	created, err = repo.InsertNotificationIfAbsent(context.Background(), row)
	if err != nil || created {
		t.Fatalf("duplicate notification capture created=%t err=%v", created, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRetryNotificationIsConditionalAndDistinguishesMissingRows(t *testing.T) {
	now := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name       string
		status     string
		wantErr    error
		updated    int64
		missingRow bool
	}{
		{name: "dead row retried", status: "dead", updated: 1},
		{name: "missing row", missingRow: true, wantErr: service.ErrNotFound},
		{name: "non dead row", status: "pending", wantErr: service.ErrConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			repo := &MySQL{db: db}
			mock.ExpectExec(`UPDATE delivery_notifications SET status='pending',attempts=0,next_attempt_at=\?,last_error='' WHERE id=\? AND status='dead'`).
				WithArgs(now, "n1").WillReturnResult(sqlmock.NewResult(0, test.updated))
			if test.updated == 0 {
				if test.missingRow {
					mock.ExpectQuery(`SELECT status FROM delivery_notifications WHERE id=\?`).WithArgs("n1").WillReturnRows(sqlmock.NewRows([]string{"status"}))
				} else {
					mock.ExpectQuery(`SELECT status FROM delivery_notifications WHERE id=\?`).WithArgs("n1").WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(test.status))
				}
			}
			err = repo.RetryNotification("n1", now)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("retry error=%v, want %v", err, test.wantErr)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
