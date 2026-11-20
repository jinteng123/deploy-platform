package repository

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"deploy-platform/backend-go/internal/service"
)

const executionActionColumns = `id,deploy_log_id,action_key,action_kind,status,intent_json,observed_json,outcome_code,created_at,finished_at`

func scanExecutionAction(row rowScanner) (service.ExecutionAction, error) {
	var action service.ExecutionAction
	var intentJSON, observedJSON []byte
	var outcome sql.NullString
	var finished sql.NullTime
	if err := row.Scan(&action.ID, &action.DeployLogID, &action.ActionKey, &action.ActionKind, &action.Status, &intentJSON, &observedJSON, &outcome, &action.CreatedAt, &finished); err != nil {
		return action, err
	}
	if !json.Valid(intentJSON) {
		return action, fmt.Errorf("decode execution action intent: invalid JSON")
	}
	action.Intent = append(json.RawMessage(nil), intentJSON...)
	if len(observedJSON) > 0 {
		if !json.Valid(observedJSON) {
			return action, fmt.Errorf("decode execution action observation: invalid JSON")
		}
		action.Observed = append(json.RawMessage(nil), observedJSON...)
	}
	if outcome.Valid {
		action.OutcomeCode = outcome.String
	}
	if finished.Valid {
		action.FinishedAt = &finished.Time
	}
	return action, nil
}

func (m *MySQL) BeginExecutionAction(action service.ExecutionAction) (service.ExecutionAction, bool, error) {
	result, err := m.db.Exec(`INSERT INTO delivery_execution_actions(deploy_log_id,action_key,action_kind,status,intent_json,created_at,updated_at)
		VALUES(?,?,?,'intent',?,?,?) ON DUPLICATE KEY UPDATE id=LAST_INSERT_ID(id)`,
		action.DeployLogID, action.ActionKey, action.ActionKind, string(action.Intent), action.CreatedAt, action.CreatedAt)
	if err != nil {
		return service.ExecutionAction{}, false, wrapConflict(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return service.ExecutionAction{}, false, err
	}
	saved, err := scanExecutionAction(m.db.QueryRow(`SELECT `+executionActionColumns+` FROM delivery_execution_actions WHERE deploy_log_id=? AND action_key=?`, action.DeployLogID, action.ActionKey))
	if err != nil {
		return service.ExecutionAction{}, false, err
	}
	if saved.ActionKind != action.ActionKind || !sameJSON(saved.Intent, action.Intent) {
		return service.ExecutionAction{}, false, fmt.Errorf("%w: 动作 %s 的意图与已持久化记录不一致", service.ErrConflict, action.ActionKey)
	}
	return saved, affected > 0, nil
}

func (m *MySQL) BeginExecutionActionWithClaim(action service.ExecutionAction, claimID string) (service.ExecutionAction, bool, error) {
	if claimID == "" {
		return service.ExecutionAction{}, false, fmt.Errorf("worker claim is required")
	}
	tx, err := m.db.Begin()
	if err != nil {
		return service.ExecutionAction{}, false, err
	}
	defer tx.Rollback()
	if err := lockWorkerClaim(tx, action.DeployLogID, claimID); err != nil {
		return service.ExecutionAction{}, false, err
	}
	result, err := tx.Exec(`INSERT INTO delivery_execution_actions(deploy_log_id,action_key,action_kind,status,intent_json,created_at,updated_at)
		VALUES(?,?,?,'intent',?,?,?) ON DUPLICATE KEY UPDATE id=LAST_INSERT_ID(id)`,
		action.DeployLogID, action.ActionKey, action.ActionKind, string(action.Intent), action.CreatedAt, action.CreatedAt)
	if err != nil {
		return service.ExecutionAction{}, false, wrapConflict(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return service.ExecutionAction{}, false, err
	}
	saved, err := scanExecutionAction(tx.QueryRow(`SELECT `+executionActionColumns+` FROM delivery_execution_actions WHERE deploy_log_id=? AND action_key=?`, action.DeployLogID, action.ActionKey))
	if err != nil {
		return service.ExecutionAction{}, false, err
	}
	if saved.ActionKind != action.ActionKind || !sameJSON(saved.Intent, action.Intent) {
		return service.ExecutionAction{}, false, fmt.Errorf("%w: 动作 %s 的意图与已持久化记录不一致", service.ErrConflict, action.ActionKey)
	}
	if err := tx.Commit(); err != nil {
		return service.ExecutionAction{}, false, err
	}
	return saved, affected > 0, nil
}

func (m *MySQL) CompleteExecutionAction(logID int64, key, status string, observed json.RawMessage, outcome string, finishedAt time.Time) (service.ExecutionAction, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return service.ExecutionAction{}, err
	}
	defer tx.Rollback()
	action, err := scanExecutionAction(tx.QueryRow(`SELECT `+executionActionColumns+` FROM delivery_execution_actions WHERE deploy_log_id=? AND action_key=? FOR UPDATE`, logID, key))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.ExecutionAction{}, service.ErrNotFound
		}
		return service.ExecutionAction{}, err
	}
	if action.Status == service.ExecutionActionApplied {
		if status == action.Status && action.OutcomeCode == outcome && sameJSON(action.Observed, observed) {
			if err := tx.Commit(); err != nil {
				return service.ExecutionAction{}, err
			}
			return action, nil
		}
		return service.ExecutionAction{}, fmt.Errorf("%w: 已确认完成的动作不能改写", service.ErrConflict)
	}
	if _, err := tx.Exec(`UPDATE delivery_execution_actions SET status=?,observed_json=?,outcome_code=?,finished_at=?,updated_at=? WHERE id=?`,
		status, string(observed), outcome, finishedAt, finishedAt, action.ID); err != nil {
		return service.ExecutionAction{}, wrapConflict(err)
	}
	action, err = scanExecutionAction(tx.QueryRow(`SELECT `+executionActionColumns+` FROM delivery_execution_actions WHERE id=?`, action.ID))
	if err != nil {
		return service.ExecutionAction{}, err
	}
	if err := tx.Commit(); err != nil {
		return service.ExecutionAction{}, err
	}
	return action, nil
}

func (m *MySQL) CompleteExecutionActionWithClaim(logID int64, key, status string, observed json.RawMessage, outcome, claimID string) (service.ExecutionAction, error) {
	if claimID == "" {
		return service.ExecutionAction{}, fmt.Errorf("worker claim is required")
	}
	tx, err := m.db.Begin()
	if err != nil {
		return service.ExecutionAction{}, err
	}
	defer tx.Rollback()
	if err := lockWorkerClaim(tx, logID, claimID); err != nil {
		return service.ExecutionAction{}, err
	}
	action, err := scanExecutionAction(tx.QueryRow(`SELECT `+executionActionColumns+` FROM delivery_execution_actions WHERE deploy_log_id=? AND action_key=? FOR UPDATE`, logID, key))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.ExecutionAction{}, service.ErrNotFound
		}
		return service.ExecutionAction{}, err
	}
	if action.Status == service.ExecutionActionApplied {
		if status == action.Status && action.OutcomeCode == outcome && sameJSON(action.Observed, observed) {
			if err := tx.Commit(); err != nil {
				return service.ExecutionAction{}, err
			}
			return action, nil
		}
		return service.ExecutionAction{}, fmt.Errorf("%w: 已确认完成的动作不能改写", service.ErrConflict)
	}
	finishedAt := time.Now().UTC()
	if _, err := tx.Exec(`UPDATE delivery_execution_actions SET status=?,observed_json=?,outcome_code=?,finished_at=?,updated_at=? WHERE id=?`,
		status, string(observed), outcome, finishedAt, finishedAt, action.ID); err != nil {
		return service.ExecutionAction{}, wrapConflict(err)
	}
	action, err = scanExecutionAction(tx.QueryRow(`SELECT `+executionActionColumns+` FROM delivery_execution_actions WHERE id=?`, action.ID))
	if err != nil {
		return service.ExecutionAction{}, err
	}
	if err := tx.Commit(); err != nil {
		return service.ExecutionAction{}, err
	}
	return action, nil
}

func lockWorkerClaim(tx *sql.Tx, logID int64, claimID string) error {
	var current sql.NullString
	err := tx.QueryRow(`SELECT worker_claim_id FROM deploy_logs WHERE id=? FOR UPDATE`, logID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrNotFound
	}
	if err != nil {
		return err
	}
	if !current.Valid || current.String != claimID {
		return fmt.Errorf("%w: Worker 认领已失效，拒绝写入外部动作", service.ErrConflict)
	}
	return nil
}

func (m *MySQL) ExecutionActions(logID int64) ([]service.ExecutionAction, error) {
	rows, err := m.db.Query(`SELECT `+executionActionColumns+` FROM delivery_execution_actions WHERE deploy_log_id=? ORDER BY id`, logID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []service.ExecutionAction{}
	for rows.Next() {
		action, err := scanExecutionAction(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, action)
	}
	return result, rows.Err()
}

func sameJSON(left, right []byte) bool {
	var l, r any
	if json.Unmarshal(left, &l) != nil || json.Unmarshal(right, &r) != nil {
		return false
	}
	return reflect.DeepEqual(l, r)
}
