package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

const (
	ExecutionActionIntent     = "intent"
	ExecutionActionApplied    = "applied"
	ExecutionActionNotApplied = "not_applied"
	ExecutionActionUnknown    = "unknown"
)

// ExecutionAction records the durable intent and readback of an external
// write. Intent and Observed are JSON because future strategies can journal
// different resource types without storing credentials or full manifests.
type ExecutionAction struct {
	ID          int64           `json:"id"`
	DeployLogID int64           `json:"deploy_log_id"`
	ActionKey   string          `json:"action_key"`
	ActionKind  string          `json:"action_kind"`
	Status      string          `json:"status"`
	Intent      json.RawMessage `json:"intent"`
	Observed    json.RawMessage `json:"observed,omitempty"`
	OutcomeCode string          `json:"outcome_code,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	FinishedAt  *time.Time      `json:"finished_at,omitempty"`
}

// ExecutionActionPersistence is separate from the legacy persistence
// contract so test doubles and older in-memory flows can remain small.
type ExecutionActionPersistence interface {
	BeginExecutionAction(ExecutionAction) (ExecutionAction, bool, error)
	CompleteExecutionAction(int64, string, string, json.RawMessage, string, time.Time) (ExecutionAction, error)
	ExecutionActions(int64) ([]ExecutionAction, error)
}

func cloneExecutionAction(action ExecutionAction) ExecutionAction {
	action.Intent = append(json.RawMessage(nil), action.Intent...)
	action.Observed = append(json.RawMessage(nil), action.Observed...)
	if action.FinishedAt != nil {
		finished := *action.FinishedAt
		action.FinishedAt = &finished
	}
	return action
}

func (s *Store) BeginExecutionAction(action ExecutionAction) (ExecutionAction, bool, error) {
	if action.DeployLogID <= 0 || action.ActionKey == "" || action.ActionKind == "" || !json.Valid(action.Intent) {
		return ExecutionAction{}, false, NewValidationError("外部动作意图无效")
	}
	if action.Status == "" {
		action.Status = ExecutionActionIntent
	}
	if action.Status != ExecutionActionIntent {
		return ExecutionAction{}, false, NewValidationError("外部动作必须从 intent 状态开始")
	}
	if action.CreatedAt.IsZero() {
		action.CreatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if claimID := s.workerClaimIDLocked(action.DeployLogID); claimID != "" && !s.workerClaimIsCurrentLocked(action.DeployLogID, claimID) {
		return ExecutionAction{}, false, newConflict("Worker 任务认领已撤销，拒绝记录外部动作")
	}
	if repo, ok := s.persistence.(ExecutionActionPersistence); ok {
		var saved ExecutionAction
		var created bool
		var err error
		if claimID := s.workerClaimIDLocked(action.DeployLogID); claimID != "" {
			if !s.workerClaimIsCurrentLocked(action.DeployLogID, claimID) {
				return ExecutionAction{}, false, newConflict("Worker 任务认领已撤销，拒绝记录外部动作")
			}
			fenced, supported := s.persistence.(FencedExecutionActionPersistence)
			if !supported {
				return ExecutionAction{}, false, fmt.Errorf("外部动作存储不支持 Worker 认领 fencing")
			}
			saved, created, err = fenced.BeginExecutionActionWithClaim(action, claimID)
		} else {
			saved, created, err = repo.BeginExecutionAction(action)
		}
		if err != nil {
			return ExecutionAction{}, false, err
		}
		s.cacheExecutionActionLocked(saved)
		return cloneExecutionAction(saved), created, nil
	}
	if s.executionActions == nil {
		s.executionActions = map[int64][]ExecutionAction{}
	}
	for _, existing := range s.executionActions[action.DeployLogID] {
		if existing.ActionKey != action.ActionKey {
			continue
		}
		if existing.ActionKind != action.ActionKind || !bytes.Equal(existing.Intent, action.Intent) {
			return ExecutionAction{}, false, newConflict("动作 %s 的持久化意图与重试请求不一致", action.ActionKey)
		}
		return cloneExecutionAction(existing), false, nil
	}
	action.ID = int64(len(s.executionActions[action.DeployLogID]) + 1)
	s.executionActions[action.DeployLogID] = append(s.executionActions[action.DeployLogID], cloneExecutionAction(action))
	return cloneExecutionAction(action), true, nil
}

func (s *Store) CompleteExecutionAction(logID int64, actionKey, status string, observed json.RawMessage, outcome string) (ExecutionAction, error) {
	if logID <= 0 || actionKey == "" || !json.Valid(observed) || outcome == "" {
		return ExecutionAction{}, NewValidationError("外部动作回读无效")
	}
	if status != ExecutionActionApplied && status != ExecutionActionNotApplied && status != ExecutionActionUnknown {
		return ExecutionAction{}, NewValidationError("外部动作终态无效")
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if claimID := s.workerClaimIDLocked(logID); claimID != "" && !s.workerClaimIsCurrentLocked(logID, claimID) {
		return ExecutionAction{}, newConflict("Worker 任务认领已撤销，拒绝更新外部动作结果")
	}
	if repo, ok := s.persistence.(ExecutionActionPersistence); ok {
		var saved ExecutionAction
		var err error
		if claimID := s.workerClaimIDLocked(logID); claimID != "" {
			if !s.workerClaimIsCurrentLocked(logID, claimID) {
				return ExecutionAction{}, newConflict("Worker 任务认领已撤销，拒绝更新外部动作结果")
			}
			fenced, supported := s.persistence.(FencedExecutionActionPersistence)
			if !supported {
				return ExecutionAction{}, fmt.Errorf("外部动作存储不支持 Worker 认领 fencing")
			}
			saved, err = fenced.CompleteExecutionActionWithClaim(logID, actionKey, status, observed, outcome, claimID)
		} else {
			saved, err = repo.CompleteExecutionAction(logID, actionKey, status, observed, outcome, now)
		}
		if err != nil {
			return ExecutionAction{}, err
		}
		s.cacheExecutionActionLocked(saved)
		return cloneExecutionAction(saved), nil
	}
	for i, existing := range s.executionActions[logID] {
		if existing.ActionKey != actionKey {
			continue
		}
		if existing.Status == ExecutionActionApplied && status != ExecutionActionApplied {
			return ExecutionAction{}, newConflict("已确认完成的动作不能改写为其他结果")
		}
		if existing.Status == status && bytes.Equal(existing.Observed, observed) && existing.OutcomeCode == outcome {
			return cloneExecutionAction(existing), nil
		}
		existing.Status = status
		existing.Observed = append(json.RawMessage(nil), observed...)
		existing.OutcomeCode = outcome
		existing.FinishedAt = &now
		s.executionActions[logID][i] = existing
		return cloneExecutionAction(existing), nil
	}
	return ExecutionAction{}, fmt.Errorf("%w: 外部动作 %s 不存在", ErrNotFound, actionKey)
}

func (s *Store) ExecutionActions(logID int64) ([]ExecutionAction, error) {
	if repo, ok := s.persistence.(ExecutionActionPersistence); ok {
		return repo.ExecutionActions(logID)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := s.executionActions[logID]
	result := make([]ExecutionAction, len(rows))
	for i, action := range rows {
		result[i] = cloneExecutionAction(action)
	}
	return result, nil
}

func (s *Store) cacheExecutionActionLocked(action ExecutionAction) {
	if s.executionActions == nil {
		s.executionActions = map[int64][]ExecutionAction{}
	}
	rows := s.executionActions[action.DeployLogID]
	for i := range rows {
		if rows[i].ActionKey == action.ActionKey {
			rows[i] = cloneExecutionAction(action)
			s.executionActions[action.DeployLogID] = rows
			return
		}
	}
	s.executionActions[action.DeployLogID] = append(rows, cloneExecutionAction(action))
}
