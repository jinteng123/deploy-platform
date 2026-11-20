package repository

import (
	"context"
	"database/sql"
	"deploy-platform/backend-go/internal/service"
	"fmt"
	"sort"
	"time"
)

func (m *MySQL) Notifications() ([]service.DeliveryNotification, error) {
	rows, err := m.db.Query(`SELECT id,event,payload,status,attempts,next_attempt_at,created_at,last_error FROM delivery_notifications ORDER BY created_at DESC,id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []service.DeliveryNotification{}
	for rows.Next() {
		var n service.DeliveryNotification
		if err := rows.Scan(&n.ID, &n.Event, &n.Payload, &n.Status, &n.Attempts, &n.NextAttemptAt, &n.CreatedAt, &n.LastError); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (m *MySQL) DueNotifications(ctx context.Context, now time.Time, limit int) ([]service.DeliveryNotification, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT id,event,payload,status,attempts,next_attempt_at,created_at,last_error FROM delivery_notifications WHERE status='pending' AND next_attempt_at<=? ORDER BY next_attempt_at,created_at,id LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]service.DeliveryNotification, 0, limit)
	for rows.Next() {
		var n service.DeliveryNotification
		if err := rows.Scan(&n.ID, &n.Event, &n.Payload, &n.Status, &n.Attempts, &n.NextAttemptAt, &n.CreatedAt, &n.LastError); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (m *MySQL) InsertNotificationIfAbsent(ctx context.Context, n service.DeliveryNotification) (bool, error) {
	result, err := m.db.ExecContext(ctx, `INSERT IGNORE INTO delivery_notifications(id,event,payload,status,attempts,next_attempt_at,created_at,last_error) VALUES(?,?,?,?,?,?,?,?)`, n.ID, n.Event, n.Payload, n.Status, n.Attempts, n.NextAttemptAt, n.CreatedAt, n.LastError)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

func (m *MySQL) RetryNotification(id string, now time.Time) error {
	result, err := m.db.Exec(`UPDATE delivery_notifications SET status='pending',attempts=0,next_attempt_at=?,last_error='' WHERE id=? AND status='dead'`, now, id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed > 0 {
		return nil
	}
	var status string
	if err := m.db.QueryRow(`SELECT status FROM delivery_notifications WHERE id=?`, id).Scan(&status); err != nil {
		if err == sql.ErrNoRows {
			return service.ErrNotFound
		}
		return err
	}
	if status != "dead" {
		return fmt.Errorf("%w: 仅失败通知可重新投递", service.ErrConflict)
	}
	return fmt.Errorf("%w: 失败通知未更新", service.ErrConflict)
}

func (m *MySQL) NotificationAlertsPage(ctx context.Context, cursor *service.DeliveryNotificationCursor, limit int) ([]service.DeliveryNotification, error) {
	out := make([]service.DeliveryNotification, 0, limit*2)
	for _, status := range []string{"pending", "dead"} {
		query := `SELECT id,event,payload,status,attempts,next_attempt_at,created_at,last_error FROM delivery_notifications WHERE status = ?`
		args := []any{status}
		if cursor != nil {
			query += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
			args = append(args, cursor.CreatedAt, cursor.CreatedAt, cursor.ID)
		}
		query += ` ORDER BY created_at DESC,id DESC LIMIT ?`
		args = append(args, limit)
		rows, err := m.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var n service.DeliveryNotification
			if err := rows.Scan(&n.ID, &n.Event, &n.Payload, &n.Status, &n.Attempts, &n.NextAttemptAt, &n.CreatedAt, &n.LastError); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, n)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MySQL) SaveNotification(n service.DeliveryNotification) error {
	_, err := m.db.Exec(`INSERT INTO delivery_notifications(id,event,payload,status,attempts,next_attempt_at,created_at,last_error) VALUES(?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE status=VALUES(status),attempts=VALUES(attempts),next_attempt_at=VALUES(next_attempt_at),last_error=VALUES(last_error)`, n.ID, n.Event, n.Payload, n.Status, n.Attempts, n.NextAttemptAt, n.CreatedAt, n.LastError)
	return err
}
