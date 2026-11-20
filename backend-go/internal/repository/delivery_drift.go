package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"deploy-platform/backend-go/internal/service"
)

const deliveryDriftStateColumns = `target_key,app_id,app_name,cluster_id,namespace,deployment_name,container_name,status,expected_image,actual_image,baseline_log_id,reason,drift_started_at,last_observed_at,observation_count,observed_by,source`

func (m *MySQL) DeliveryDriftStates() ([]service.DeliveryDriftState, error) {
	rows, err := m.db.Query(`SELECT ` + deliveryDriftStateColumns + ` FROM delivery_drift_states ORDER BY target_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []service.DeliveryDriftState{}
	for rows.Next() {
		state, err := scanDeliveryDriftState(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, state)
	}
	return out, rows.Err()
}

type driftRowScanner interface {
	Scan(dest ...any) error
}

func scanDeliveryDriftState(row driftRowScanner) (service.DeliveryDriftState, error) {
	var state service.DeliveryDriftState
	var expected, actual, reason sql.NullString
	var baseline sql.NullInt64
	var started sql.NullTime
	if err := row.Scan(&state.TargetKey, &state.AppID, &state.AppName, &state.ClusterID, &state.Namespace, &state.Workload, &state.Container, &state.Status, &expected, &actual, &baseline, &reason, &started, &state.LastObservedAt, &state.ObservationCount, &state.ObservedBy, &state.Source); err != nil {
		return service.DeliveryDriftState{}, err
	}
	state.ExpectedImage = nullStr(expected)
	state.ActualImage = nullStr(actual)
	state.BaselineLogID = nullInt64(baseline)
	state.Reason = reason.String
	state.ObservedAt = state.LastObservedAt
	if started.Valid {
		value := started.Time
		state.DriftStartedAt = &value
	}
	return state, nil
}

func (m *MySQL) ObserveDeliveryDrift(observations []service.DeliveryDriftObservation) ([]service.DeliveryDriftState, error) {
	return m.observeDeliveryDrift(observations, nil)
}

func (m *MySQL) ObserveDeliveryDriftWithNotifications(observations []service.DeliveryDriftObservation, notificationTime time.Time) ([]service.DeliveryDriftState, error) {
	now := notificationTime.UTC()
	return m.observeDeliveryDrift(observations, &now)
}

func (m *MySQL) observeDeliveryDrift(observations []service.DeliveryDriftObservation, notificationTime *time.Time) ([]service.DeliveryDriftState, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	states := make([]service.DeliveryDriftState, 0, len(observations))
	for _, observation := range observations {
		key := service.DeliveryDriftTargetKey(observation)
		previous, err := scanDeliveryDriftState(tx.QueryRow(`SELECT `+deliveryDriftStateColumns+` FROM delivery_drift_states WHERE target_key=? FOR UPDATE`, key))
		hasPrevious := err == nil
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		state, event, changed := service.NextDeliveryDriftState(key, observation, previous, hasPrevious)
		if hasPrevious {
			_, err = tx.Exec(`UPDATE delivery_drift_states SET app_name=?,status=?,expected_image=?,actual_image=?,baseline_log_id=?,reason=?,drift_started_at=?,last_observed_at=?,observation_count=?,observed_by=?,source=?,updated_at=? WHERE target_key=?`,
				state.AppName, state.Status, state.ExpectedImage, state.ActualImage, state.BaselineLogID, dbOptionalText(state.Reason), state.DriftStartedAt,
				state.LastObservedAt, state.ObservationCount, state.ObservedBy, state.Source, time.Now().UTC(), key)
		} else {
			_, err = tx.Exec(`INSERT INTO delivery_drift_states (`+deliveryDriftStateColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				state.TargetKey, state.AppID, state.AppName, state.ClusterID, state.Namespace, state.Workload, state.Container, state.Status,
				state.ExpectedImage, state.ActualImage, state.BaselineLogID, dbOptionalText(state.Reason), state.DriftStartedAt, state.LastObservedAt,
				state.ObservationCount, state.ObservedBy, state.Source)
		}
		if err != nil {
			return nil, err
		}
		if changed {
			event.ID, err = insertDeliveryDriftEvent(tx, key, event)
			if err != nil {
				return nil, err
			}
			if notificationTime != nil {
				notification, shouldNotify, err := service.DeliveryDriftNotification(event, *notificationTime)
				if err != nil {
					return nil, err
				}
				if shouldNotify {
					if err := insertDriftNotification(tx, notification); err != nil {
						return nil, err
					}
				}
			}
		}
		states = append(states, state)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return states, nil
}

func insertDeliveryDriftEvent(tx *sql.Tx, targetKey string, event service.DeliveryDriftEvent) (int64, error) {
	var previous any
	if event.PreviousStatus != "" {
		previous = event.PreviousStatus
	}
	result, err := tx.Exec(`INSERT INTO delivery_drift_events (target_key,app_id,app_name,cluster_id,namespace,deployment_name,container_name,previous_status,status,change_kind,expected_image,actual_image,baseline_log_id,reason,drift_started_at,drift_duration_seconds,observed_at,observed_by,source) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		targetKey, event.AppID, event.AppName, event.ClusterID, event.Namespace, event.Workload, event.Container, previous, event.Status, event.ChangeKind,
		event.ExpectedImage, event.ActualImage, event.BaselineLogID, dbOptionalText(event.Reason), event.DriftStartedAt, event.DriftDurationSecs,
		event.ObservedAt, event.ObservedBy, event.Source)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func insertDriftNotification(tx *sql.Tx, notification service.DeliveryNotification) error {
	_, err := tx.Exec(`INSERT INTO delivery_notifications(id,event,payload,status,attempts,next_attempt_at,created_at,last_error) VALUES(?,?,?,?,?,?,?,?)`,
		notification.ID, notification.Event, notification.Payload, notification.Status, notification.Attempts,
		notification.NextAttemptAt, notification.CreatedAt, notification.LastError)
	return err
}

func dbOptionalText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func (m *MySQL) DeliveryDriftHistory(filter service.DeliveryDriftHistoryFilter) ([]service.DeliveryDriftEvent, error) {
	query := `SELECT id,target_key,app_id,app_name,cluster_id,namespace,deployment_name,container_name,previous_status,status,change_kind,expected_image,actual_image,baseline_log_id,reason,drift_started_at,drift_duration_seconds,observed_at,observed_by,source FROM delivery_drift_events WHERE cluster_id=?`
	args := []any{filter.ClusterID}
	if len(filter.AppIDs) > 0 {
		placeholders := make([]string, len(filter.AppIDs))
		for i, appID := range filter.AppIDs {
			placeholders[i] = "?"
			args = append(args, appID)
		}
		query += ` AND app_id IN (` + strings.Join(placeholders, ",") + `)`
	}
	query += ` ORDER BY observed_at DESC,id DESC LIMIT ?`
	args = append(args, filter.Limit)
	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []service.DeliveryDriftEvent{}
	for rows.Next() {
		var event service.DeliveryDriftEvent
		var previous, expected, actual, reason sql.NullString
		var baseline, duration sql.NullInt64
		var started sql.NullTime
		if err := rows.Scan(&event.ID, &event.TargetKey, &event.AppID, &event.AppName, &event.ClusterID, &event.Namespace, &event.Workload, &event.Container, &previous, &event.Status, &event.ChangeKind, &expected, &actual, &baseline, &reason, &started, &duration, &event.ObservedAt, &event.ObservedBy, &event.Source); err != nil {
			return nil, err
		}
		event.PreviousStatus = previous.String
		event.ExpectedImage = nullStr(expected)
		event.ActualImage = nullStr(actual)
		event.BaselineLogID = nullInt64(baseline)
		event.Reason = reason.String
		if started.Valid {
			value := started.Time
			event.DriftStartedAt = &value
		}
		if duration.Valid {
			value := duration.Int64
			event.DriftDurationSecs = &value
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (m *MySQL) DeliveryDriftEventsAfter(ctx context.Context, afterID int64, limit int) ([]service.DeliveryDriftEvent, error) {
	if afterID < 0 || limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("invalid drift event cursor or limit")
	}
	query := `SELECT id,target_key,app_id,app_name,cluster_id,namespace,deployment_name,container_name,previous_status,status,change_kind,expected_image,actual_image,baseline_log_id,reason,drift_started_at,drift_duration_seconds,observed_at,observed_by,source FROM delivery_drift_events WHERE id>? ORDER BY id LIMIT ?`
	rows, err := m.db.QueryContext(ctx, query, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]service.DeliveryDriftEvent, 0, limit)
	for rows.Next() {
		event, err := scanDeliveryDriftEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func scanDeliveryDriftEvent(row driftRowScanner) (service.DeliveryDriftEvent, error) {
	var event service.DeliveryDriftEvent
	var previous, expected, actual, reason sql.NullString
	var baseline, duration sql.NullInt64
	var started sql.NullTime
	if err := row.Scan(&event.ID, &event.TargetKey, &event.AppID, &event.AppName, &event.ClusterID, &event.Namespace, &event.Workload, &event.Container, &previous, &event.Status, &event.ChangeKind, &expected, &actual, &baseline, &reason, &started, &duration, &event.ObservedAt, &event.ObservedBy, &event.Source); err != nil {
		return service.DeliveryDriftEvent{}, err
	}
	event.PreviousStatus = previous.String
	event.ExpectedImage = nullStr(expected)
	event.ActualImage = nullStr(actual)
	event.BaselineLogID = nullInt64(baseline)
	event.Reason = reason.String
	if started.Valid {
		value := started.Time
		event.DriftStartedAt = &value
	}
	if duration.Valid {
		value := duration.Int64
		event.DriftDurationSecs = &value
	}
	return event, nil
}
