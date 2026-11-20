package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

// ErrDeliveryRunStopped is returned when a worker reaches a checkpoint after
// an operator paused or cancelled its durable run. It is a normal stop signal,
// not an execution failure.
var ErrDeliveryRunStopped = errors.New("发布运行已暂停或取消")

// DeliveryRun is the orchestration identity for an approved immutable plan.
// DeployLog remains the compatibility record used by existing release APIs.
type DeliveryRun struct {
	ID                 int64     `json:"id"`
	DeliveryPlanID     int64     `json:"delivery_plan_id"`
	PromotionRequestID int64     `json:"promotion_request_id"`
	DeployLogID        int64     `json:"deploy_log_id"`
	Strategy           string    `json:"strategy"`
	Status             string    `json:"status"`
	CurrentStage       string    `json:"current_stage"`
	Version            int64     `json:"version"`
	ErrorMessage       *string   `json:"error_message,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// DeliveryRunEvent is an append-only, human-readable orchestration transition.
// Detail is deliberately plain text; Kubernetes manifests and credentials are
// never placed in this timeline.
type DeliveryRunEvent struct {
	ID        int64           `json:"id"`
	RunID     int64           `json:"run_id"`
	Version   int64           `json:"version"`
	EventKey  string          `json:"event_key"`
	EventType string          `json:"event_type"`
	Stage     string          `json:"stage"`
	Status    string          `json:"status"`
	Detail    string          `json:"detail,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

func sameDeliveryRunEventContent(a, b DeliveryRunEvent) bool {
	return a.EventType == b.EventType && a.Stage == b.Stage && a.Status == b.Status &&
		a.Detail == b.Detail && bytes.Equal(a.Payload, b.Payload)
}

func validateDeliveryRunEventPayload(payload json.RawMessage) error {
	if len(payload) == 0 {
		return nil
	}
	if len(payload) > 64*1024 || !json.Valid(payload) {
		return NewValidationError("发布运行事件结构化内容无效或超过 64 KiB")
	}
	return nil
}

func isTerminalDeliveryRunStatus(status string) bool {
	switch status {
	case "success", "failed", "interrupted", "cancelled":
		return true
	default:
		return false
	}
}

type DeliveryRunView struct {
	Run     DeliveryRun        `json:"run"`
	Plan    DeliveryPlan       `json:"plan"`
	Deploy  DeployLog          `json:"deploy_log"`
	Events  []DeliveryRunEvent `json:"events"`
	Actions []ExecutionAction  `json:"actions"`
}

func interruptionStage(log DeployLog) string {
	if log.CurrentStep != nil && *log.CurrentStep != "" {
		return *log.CurrentStep
	}
	return "interrupted"
}

func deliveryRunAttemptEventKey(log DeployLog, key string) string {
	at := time.Now().UTC()
	if log.StartedAt != nil && !log.StartedAt.IsZero() {
		at = *log.StartedAt
	}
	return fmt.Sprintf("attempt:%d:%s", at.UnixNano(), key)
}

// DeliveryRunRepository handles reads and optimistic, append-only stage events.
type DeliveryRunRepository interface {
	DeliveryRuns() ([]DeliveryRun, []DeliveryRunEvent, error)
	AppendDeliveryRunEvent(int64, int64, DeliveryRunEvent) (DeliveryRun, DeliveryRunEvent, error)
}

// DeliveryRunExecutionPersistence commits the legacy task projection and its
// orchestration event together. This keeps existing progress APIs and the
// durable run timeline consistent across database failures.
type DeliveryRunExecutionPersistence interface {
	UpdateDeployLogAndAppendDeliveryRunEvent(DeployLog, DeliveryRunEvent) (DeliveryRun, DeliveryRunEvent, error)
}

// VersionedDeliveryRunExecutionPersistence applies a user-initiated state
// transition only when the caller's observed run version is still current.
type VersionedDeliveryRunExecutionPersistence interface {
	TransitionDeliveryRunExecution(DeployLog, int64, DeliveryRunEvent) (DeliveryRun, DeliveryRunEvent, bool, error)
}

// LockedPromotionRunPersistence commits an approval, its deployment task,
// resource locks, and the initial run row in one database transaction.
type LockedPromotionRunPersistence interface {
	ApprovePromotionWithRun(PromotionRequest, DeployLog, []ResourceLock, DeliveryRun) (PromotionRequest, ImageRelease, DeployLog, DeliveryRun, error)
}

func cloneDeliveryRun(run DeliveryRun) DeliveryRun {
	if run.ErrorMessage != nil {
		message := *run.ErrorMessage
		run.ErrorMessage = &message
	}
	return run
}

func (s *Store) LoadDeliveryRuns(runs []DeliveryRun, events []DeliveryRunEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deliveryRuns = make([]DeliveryRun, len(runs))
	s.deliveryRunEvents = make(map[int64][]DeliveryRunEvent, len(runs))
	s.nextDeliveryRun = 1
	for i, run := range runs {
		s.deliveryRuns[i] = cloneDeliveryRun(run)
		if run.ID >= s.nextDeliveryRun {
			s.nextDeliveryRun = run.ID + 1
		}
	}
	for _, event := range events {
		event.Payload = append(json.RawMessage(nil), event.Payload...)
		s.deliveryRunEvents[event.RunID] = append(s.deliveryRunEvents[event.RunID], event)
	}
}

func (s *Store) DeliveryRun(id int64) (DeliveryRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, run := range s.deliveryRuns {
		if run.ID == id {
			return cloneDeliveryRun(run), nil
		}
	}
	return DeliveryRun{}, ErrNotFound
}

func (s *Store) DeliveryRunForDeployLog(logID int64) (DeliveryRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, run := range s.deliveryRuns {
		if run.DeployLogID == logID {
			return cloneDeliveryRun(run), nil
		}
	}
	return DeliveryRun{}, ErrNotFound
}

func (s *Store) DeliveryRunEvents(runID int64) ([]DeliveryRunEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	found := false
	for _, run := range s.deliveryRuns {
		if run.ID == runID {
			found = true
			break
		}
	}
	if !found {
		return nil, ErrNotFound
	}
	events := s.deliveryRunEvents[runID]
	result := make([]DeliveryRunEvent, len(events))
	for i, event := range events {
		event.Payload = append(json.RawMessage(nil), event.Payload...)
		result[i] = event
	}
	return result, nil
}

func (s *Store) DeliveryRunView(runID int64) (DeliveryRunView, error) {
	run, err := s.DeliveryRun(runID)
	if err != nil {
		return DeliveryRunView{}, err
	}
	plan, err := s.FindDeliveryPlan(run.DeliveryPlanID)
	if err != nil {
		return DeliveryRunView{}, err
	}
	deploy, err := s.FindLog(run.DeployLogID)
	if err != nil {
		return DeliveryRunView{}, err
	}
	events, err := s.DeliveryRunEvents(run.ID)
	if err != nil {
		return DeliveryRunView{}, err
	}
	actions, err := s.ExecutionActions(run.DeployLogID)
	if err != nil {
		return DeliveryRunView{}, err
	}
	return DeliveryRunView{Run: run, Plan: plan, Deploy: deploy, Events: events, Actions: actions}, nil
}

// RecordDeliveryRunEvent persists a single idempotent transition for the run
// associated with logID. A duplicate event key is resolved before the expected
// version check so a retry after a lost response remains idempotent; a new key
// with a stale version is rejected. Non-plan deployment logs intentionally
// remain on the legacy progress surface and return found=false.
func (s *Store) RecordDeliveryRunEvent(logID int64, expectedVersion int64, event DeliveryRunEvent) (DeliveryRun, bool, error) {
	if logID <= 0 || expectedVersion <= 0 || event.EventKey == "" || event.EventType == "" || event.Stage == "" || event.Status == "" {
		return DeliveryRun{}, false, NewValidationError("发布运行事件字段不完整")
	}
	if len(event.Detail) > 2000 || !utf8.ValidString(event.Detail) {
		return DeliveryRun{}, false, NewValidationError("发布运行事件内容无效")
	}
	if err := validateDeliveryRunEventPayload(event.Payload); err != nil {
		return DeliveryRun{}, false, err
	}
	event.Payload = append(json.RawMessage(nil), event.Payload...)
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i := range s.deliveryRuns {
		if s.deliveryRuns[i].DeployLogID == logID {
			index = i
			break
		}
	}
	if index < 0 {
		return DeliveryRun{}, false, nil
	}
	current := s.deliveryRuns[index]
	event.RunID = current.ID
	event.CreatedAt = time.Now().UTC()
	if repo, ok := s.persistence.(DeliveryRunRepository); ok {
		savedRun, savedEvent, err := repo.AppendDeliveryRunEvent(current.ID, expectedVersion, event)
		if err != nil {
			return current, true, err
		}
		s.deliveryRuns[index] = cloneDeliveryRun(savedRun)
		s.upsertDeliveryRunEventLocked(savedEvent)
		return cloneDeliveryRun(savedRun), true, nil
	}
	for _, existing := range s.deliveryRunEvents[current.ID] {
		if existing.EventKey != event.EventKey {
			continue
		}
		if !sameDeliveryRunEventContent(existing, event) {
			return current, true, fmt.Errorf("%w: 发布运行事件 key 已用于不同状态", ErrConflict)
		}
		return cloneDeliveryRun(current), true, nil
	}
	if current.Version != expectedVersion {
		return current, true, newConflict("发布运行版本已从 %d 更新到 %d", expectedVersion, current.Version)
	}
	if isTerminalDeliveryRunStatus(current.Status) && event.Status != current.Status {
		return current, true, newConflict("已结束的发布运行不能从 %s 改回 %s", current.Status, event.Status)
	}
	current.Version++
	statusChanged := current.Status != event.Status
	current.Status = event.Status
	current.CurrentStage = event.Stage
	current.UpdatedAt = event.CreatedAt
	if statusChanged {
		if event.Status == "failed" || event.Status == "interrupted" {
			current.ErrorMessage = strPtr(event.Detail)
		} else {
			current.ErrorMessage = nil
		}
	}
	event.Version = current.Version
	event.ID = int64(len(s.deliveryRunEvents[current.ID]) + 1)
	s.deliveryRuns[index] = cloneDeliveryRun(current)
	s.deliveryRunEvents[current.ID] = append(s.deliveryRunEvents[current.ID], event)
	return cloneDeliveryRun(current), true, nil
}

// UpdateLogExecutionAndRecordDeliveryRunEvent updates a deploy log and, when
// it belongs to a delivery run, appends its corresponding stage event as one
// persistence operation. Legacy logs keep using their existing persistence.
func (s *Store) UpdateLogExecutionAndRecordDeliveryRunEvent(log DeployLog, event DeliveryRunEvent) (bool, error) {
	if log.ID <= 0 || event.EventKey == "" || event.EventType == "" || event.Stage == "" || event.Status == "" || event.Status != log.Status {
		return false, NewValidationError("发布运行事件字段不完整")
	}
	if len(event.Detail) > 2000 || !utf8.ValidString(event.Detail) {
		return false, NewValidationError("发布运行事件内容无效")
	}
	if err := validateDeliveryRunEventPayload(event.Payload); err != nil {
		return false, err
	}
	event.Payload = append(json.RawMessage(nil), event.Payload...)
	s.mu.Lock()
	defer s.mu.Unlock()

	logIndex := -1
	for i := range s.logs {
		if s.logs[i].ID == log.ID {
			logIndex = i
			break
		}
	}
	if logIndex < 0 {
		return false, ErrNotFound
	}
	runIndex := -1
	for i := range s.deliveryRuns {
		if s.deliveryRuns[i].DeployLogID == log.ID {
			runIndex = i
			break
		}
	}

	var savedRun DeliveryRun
	var savedEvent DeliveryRunEvent
	claimID := s.workerClaimIDLocked(log.ID)
	if claimID != "" {
		if !s.workerClaimIsCurrentLocked(log.ID, claimID) {
			return false, newConflict("Worker 任务认领已撤销，拒绝写入运行检查点")
		}
		log.WorkerClaimID = claimID
	}
	if s.persistence != nil {
		if runIndex >= 0 {
			var err error
			if claimID != "" {
				persistence, ok := s.persistence.(FencedDeliveryRunExecutionPersistence)
				if !ok {
					return false, fmt.Errorf("发布执行存储不支持 Worker 认领 fencing")
				}
				savedRun, savedEvent, err = persistence.UpdateDeployLogAndAppendDeliveryRunEventWithClaim(log, event, claimID)
			} else {
				persistence, ok := s.persistence.(DeliveryRunExecutionPersistence)
				if !ok {
					return false, fmt.Errorf("发布执行存储不支持任务与阶段事件的原子提交")
				}
				savedRun, savedEvent, err = persistence.UpdateDeployLogAndAppendDeliveryRunEvent(log, event)
			}
			if err != nil {
				return false, err
			}
		} else if claimID != "" {
			persistence, ok := s.persistence.(FencedLogExecutionPersistence)
			if !ok {
				return false, fmt.Errorf("任务存储不支持 Worker 认领 fencing")
			}
			if err := persistence.UpdateLogExecutionWithClaim(log, claimID); err != nil {
				return false, err
			}
		} else if err := s.persistence.UpdateLogExecution(log); err != nil {
			return false, err
		}
	} else if runIndex >= 0 {
		current := s.deliveryRuns[runIndex]
		event.RunID = current.ID
		duplicate := false
		for _, existing := range s.deliveryRunEvents[current.ID] {
			if existing.EventKey != event.EventKey {
				continue
			}
			if !sameDeliveryRunEventContent(existing, event) {
				return false, fmt.Errorf("%w: 发布运行事件 key 已用于不同状态", ErrConflict)
			}
			savedRun, savedEvent = cloneDeliveryRun(current), existing
			duplicate = true
			break
		}
		if !duplicate {
			if DeliveryRunPipelineStopped(current.Status) {
				return false, ErrDeliveryRunStopped
			}
			event.CreatedAt = time.Now().UTC()
			current.Version++
			current.Status = event.Status
			current.CurrentStage = event.Stage
			current.UpdatedAt = event.CreatedAt
			if event.Status == "failed" || event.Status == "interrupted" {
				current.ErrorMessage = strPtr(event.Detail)
			} else if event.Status == "success" {
				current.ErrorMessage = nil
			}
			event.Version = current.Version
			event.ID = int64(len(s.deliveryRunEvents[current.ID]) + 1)
			savedRun, savedEvent = cloneDeliveryRun(current), event
		}
	}

	cur := s.logs[logIndex]
	cur.Status = log.Status
	cur.CurrentStep = log.CurrentStep
	cur.ImageFull = log.ImageFull
	cur.StepDetail = log.StepDetail
	cur.ErrorMessage = log.ErrorMessage
	cur.StartedAt = log.StartedAt
	cur.FinishedAt = log.FinishedAt
	if isTerminalDeployStatus(log.Status) {
		cur.WorkerClaimID = ""
	} else if claimID != "" {
		cur.WorkerClaimID = claimID
	}
	s.logs[logIndex] = cur
	if runIndex >= 0 {
		s.deliveryRuns[runIndex] = cloneDeliveryRun(savedRun)
		s.upsertDeliveryRunEventLocked(savedEvent)
	}
	if isTerminalDeployStatus(log.Status) {
		s.releaseResourceLocksLocked(log.ID)
		s.captureTerminalLogLocked(cur)
	}
	return runIndex >= 0, nil
}

// DeliveryRunPipelineStopped identifies durable run states where the worker
// must not append another execution checkpoint.
func DeliveryRunPipelineStopped(status string) bool {
	switch status {
	case "paused", "success", "failed", "interrupted", "cancelled":
		return true
	default:
		return false
	}
}

func (s *Store) upsertDeliveryRunEventLocked(event DeliveryRunEvent) {
	event.Payload = append(json.RawMessage(nil), event.Payload...)
	for i, existing := range s.deliveryRunEvents[event.RunID] {
		if existing.EventKey == event.EventKey {
			s.deliveryRunEvents[event.RunID][i] = event
			return
		}
	}
	s.deliveryRunEvents[event.RunID] = append(s.deliveryRunEvents[event.RunID], event)
}
