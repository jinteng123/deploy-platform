package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	DefaultDriftHistoryLimit = 25
	MaxDriftHistoryLimit     = 100
	DriftObservationSource   = "on_demand_read"
	DriftBackgroundSource    = "background_poll"
)

var ErrDeliveryDriftEventPagingUnavailable = errors.New("漂移事件分页不可用")

type DeliveryDriftObservation struct {
	AppID         int       `json:"app_id"`
	AppName       string    `json:"app_name"`
	ClusterID     int       `json:"cluster_id"`
	Namespace     string    `json:"namespace"`
	Workload      string    `json:"workload"`
	Container     string    `json:"container"`
	Status        string    `json:"status"`
	ExpectedImage *string   `json:"expected_image"`
	ActualImage   *string   `json:"actual_image"`
	BaselineLogID *int64    `json:"baseline_log_id,omitempty"`
	Reason        string    `json:"reason,omitempty"`
	ObservedBy    string    `json:"observed_by"`
	ObservedAt    time.Time `json:"observed_at"`
	Source        string    `json:"source"`
}

type DeliveryDriftState struct {
	TargetKey        string     `json:"-"`
	AppID            int        `json:"app_id"`
	AppName          string     `json:"app_name"`
	ClusterID        int        `json:"cluster_id"`
	Namespace        string     `json:"namespace"`
	Workload         string     `json:"workload"`
	Container        string     `json:"container"`
	Status           string     `json:"status"`
	ExpectedImage    *string    `json:"expected_image"`
	ActualImage      *string    `json:"actual_image"`
	BaselineLogID    *int64     `json:"baseline_log_id,omitempty"`
	Reason           string     `json:"reason,omitempty"`
	DriftStartedAt   *time.Time `json:"drift_started_at,omitempty"`
	ObservedAt       time.Time  `json:"observed_at"`
	LastObservedAt   time.Time  `json:"last_observed_at"`
	ObservationCount int64      `json:"observation_count"`
	ObservedBy       string     `json:"observed_by"`
	Source           string     `json:"source"`
}

type DeliveryDriftEvent struct {
	ID                int64      `json:"id"`
	AppID             int        `json:"app_id"`
	AppName           string     `json:"app_name"`
	ClusterID         int        `json:"cluster_id"`
	Namespace         string     `json:"namespace"`
	Workload          string     `json:"workload"`
	Container         string     `json:"container"`
	PreviousStatus    string     `json:"previous_status,omitempty"`
	Status            string     `json:"status"`
	ChangeKind        string     `json:"change_kind"`
	ExpectedImage     *string    `json:"expected_image"`
	ActualImage       *string    `json:"actual_image"`
	BaselineLogID     *int64     `json:"baseline_log_id,omitempty"`
	Reason            string     `json:"reason,omitempty"`
	DriftStartedAt    *time.Time `json:"drift_started_at,omitempty"`
	DriftDurationSecs *int64     `json:"drift_duration_seconds,omitempty"`
	ObservedAt        time.Time  `json:"observed_at"`
	ObservedBy        string     `json:"observed_by"`
	Source            string     `json:"source"`
	TargetKey         string     `json:"-"`
}

type DeliveryDriftHistoryFilter struct {
	ClusterID int
	AppIDs    []int
	Limit     int
}

type DeliveryDriftPersistence interface {
	ObserveDeliveryDrift([]DeliveryDriftObservation) ([]DeliveryDriftState, error)
	DeliveryDriftHistory(DeliveryDriftHistoryFilter) ([]DeliveryDriftEvent, error)
}

// DeliveryDriftNotificationPersistence atomically persists drift transitions
// and their outbox notifications when outbound notification capture is active.
type DeliveryDriftNotificationPersistence interface {
	ObserveDeliveryDriftWithNotifications([]DeliveryDriftObservation, time.Time) ([]DeliveryDriftState, error)
}

type DeliveryDriftEventPagePersistence interface {
	DeliveryDriftEventsAfter(context.Context, int64, int) ([]DeliveryDriftEvent, error)
}

func (s *Store) LoadDeliveryDriftStates(rows []DeliveryDriftState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.driftStates = make(map[string]DeliveryDriftState, len(rows))
	for _, row := range rows {
		s.driftStates[row.TargetKey] = row
	}
}

func DeliveryDriftTargetKey(row DeliveryDriftObservation) string {
	key := fmt.Sprintf("%d\x00%d\x00%s\x00%s\x00%s", row.AppID, row.ClusterID, row.Namespace, row.Workload, row.Container)
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func (s *Store) ObserveDeliveryDrift(rows []DeliveryDriftObservation) ([]DeliveryDriftState, error) {
	if len(rows) == 0 {
		return []DeliveryDriftState{}, nil
	}
	now := time.Now().UTC()
	seen := make(map[string]struct{}, len(rows))
	for i := range rows {
		row := &rows[i]
		row.AppName = strings.TrimSpace(row.AppName)
		row.Namespace = strings.TrimSpace(row.Namespace)
		row.Workload = strings.TrimSpace(row.Workload)
		row.Container = strings.TrimSpace(row.Container)
		row.Status = strings.TrimSpace(row.Status)
		row.ObservedBy = strings.TrimSpace(row.ObservedBy)
		row.Reason = strings.TrimSpace(row.Reason)
		if row.AppID <= 0 || row.ClusterID <= 0 || row.AppName == "" || row.Namespace == "" || row.Workload == "" || row.Container == "" {
			return nil, NewValidationError("漂移观测必须包含应用、集群、namespace、Deployment 和容器")
		}
		if row.Status != "in_sync" && row.Status != "drifted" && row.Status != "unknown" {
			return nil, NewValidationError("漂移状态必须是 in_sync、drifted 或 unknown")
		}
		if len(row.AppName) > 255 || len(row.Namespace) > 253 || len(row.Workload) > 253 || len(row.Container) > 253 || len(row.ObservedBy) > 64 || len(row.Reason) > 1000 {
			return nil, NewValidationError("漂移观测字段超出长度限制")
		}
		for _, image := range []*string{row.ExpectedImage, row.ActualImage} {
			if image != nil && len(*image) > 4096 {
				return nil, NewValidationError("镜像地址过长，无法记录漂移观测")
			}
		}
		if row.ObservedAt.IsZero() {
			row.ObservedAt = now
		} else {
			row.ObservedAt = row.ObservedAt.UTC()
		}
		if row.Source == "" {
			row.Source = DriftObservationSource
		} else if row.Source != DriftObservationSource && row.Source != DriftBackgroundSource {
			return nil, NewValidationError("漂移观测来源无效")
		}
		key := DeliveryDriftTargetKey(*row)
		if _, ok := seen[key]; ok {
			return nil, NewValidationError("漂移观测批次包含重复目标")
		}
		seen[key] = struct{}{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	persistence := s.persistence
	captureNotifications := s.notificationCaptureEnabled
	if persistence != nil {
		repo, ok := persistence.(DeliveryDriftPersistence)
		if !ok {
			return nil, fmt.Errorf("漂移历史存储不可用")
		}
		var states []DeliveryDriftState
		var err error
		if captureNotifications {
			notifyingRepo, ok := persistence.(DeliveryDriftNotificationPersistence)
			if !ok {
				return nil, fmt.Errorf("漂移通知存储不可用")
			}
			states, err = notifyingRepo.ObserveDeliveryDriftWithNotifications(rows, now)
		} else {
			states, err = repo.ObserveDeliveryDrift(rows)
		}
		if err != nil {
			return nil, err
		}
		if s.driftStates == nil {
			s.driftStates = map[string]DeliveryDriftState{}
		}
		for _, state := range states {
			s.driftStates[state.TargetKey] = state
		}
		return states, nil
	}

	if s.driftStates == nil {
		s.driftStates = map[string]DeliveryDriftState{}
	}
	if s.nextDriftEventID < 1 {
		s.nextDriftEventID = 1
	}
	states := make([]DeliveryDriftState, 0, len(rows))
	for _, row := range rows {
		key := DeliveryDriftTargetKey(row)
		previous, exists := s.driftStates[key]
		state, event, changed := NextDeliveryDriftState(key, row, previous, exists)
		s.driftStates[key] = state
		if changed {
			event.ID = s.nextDriftEventID
			s.nextDriftEventID++
			s.driftEvents = append(s.driftEvents, event)
			if s.notificationCaptureEnabled {
				notification, shouldNotify, err := DeliveryDriftNotification(event, now)
				if err != nil {
					s.notificationCaptureFailures++
					return nil, err
				}
				if shouldNotify {
					if err := s.insertNotificationIfAbsentLocked(context.Background(), notification); err != nil {
						return nil, err
					}
				}
			}
		}
		states = append(states, state)
	}
	return states, nil
}

func (s *Store) DeliveryDriftEventsAfter(ctx context.Context, afterID int64, limit int) ([]DeliveryDriftEvent, error) {
	if afterID < 0 || limit < 1 || limit > 1000 {
		return nil, NewValidationError("漂移事件游标或分页数量无效")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	persistence := s.persistence
	pageRepo, ok := s.persistence.(DeliveryDriftEventPagePersistence)
	s.mu.RUnlock()
	if persistence != nil {
		if !ok {
			return nil, ErrDeliveryDriftEventPagingUnavailable
		}
		return pageRepo.DeliveryDriftEventsAfter(ctx, afterID, limit)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]DeliveryDriftEvent, 0, limit)
	for _, event := range s.driftEvents {
		if event.ID <= afterID {
			continue
		}
		out = append(out, event)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (s *Store) DeliveryDriftHistory(filter DeliveryDriftHistoryFilter) ([]DeliveryDriftEvent, error) {
	if filter.ClusterID <= 0 {
		return nil, NewValidationError("cluster_id 必须是正整数")
	}
	if filter.Limit == 0 {
		filter.Limit = DefaultDriftHistoryLimit
	}
	if filter.Limit < 1 || filter.Limit > MaxDriftHistoryLimit {
		return nil, NewValidationError("limit 必须在 1 到 100 之间")
	}
	ids := make(map[int]struct{}, len(filter.AppIDs))
	for _, id := range filter.AppIDs {
		if id <= 0 {
			return nil, NewValidationError("app_ids 必须是正整数")
		}
		ids[id] = struct{}{}
	}
	filter.AppIDs = filter.AppIDs[:0]
	for id := range ids {
		filter.AppIDs = append(filter.AppIDs, id)
	}
	sort.Ints(filter.AppIDs)
	if s.persistence != nil {
		repo, ok := s.persistence.(DeliveryDriftPersistence)
		if !ok {
			return nil, fmt.Errorf("漂移历史读取不可用")
		}
		return repo.DeliveryDriftHistory(filter)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]DeliveryDriftEvent, 0, filter.Limit)
	for i := len(s.driftEvents) - 1; i >= 0 && len(out) < filter.Limit; i-- {
		row := s.driftEvents[i]
		if row.ClusterID != filter.ClusterID {
			continue
		}
		if len(filter.AppIDs) > 0 {
			if _, ok := ids[row.AppID]; !ok {
				continue
			}
		}
		out = append(out, row)
	}
	return out, nil
}

func NextDeliveryDriftState(key string, observation DeliveryDriftObservation, previous DeliveryDriftState, hasPrevious bool) (DeliveryDriftState, DeliveryDriftEvent, bool) {
	state := DeliveryDriftState{
		TargetKey: key, AppID: observation.AppID, AppName: observation.AppName, ClusterID: observation.ClusterID,
		Namespace: observation.Namespace, Workload: observation.Workload, Container: observation.Container,
		Status: observation.Status, ExpectedImage: copyString(observation.ExpectedImage), ActualImage: copyString(observation.ActualImage),
		BaselineLogID: copyInt64(observation.BaselineLogID), Reason: observation.Reason, ObservedAt: observation.ObservedAt, LastObservedAt: observation.ObservedAt,
		ObservationCount: 1, ObservedBy: observation.ObservedBy, Source: observation.Source,
	}
	if hasPrevious {
		state.ObservationCount = previous.ObservationCount + 1
		state.DriftStartedAt = copyTime(previous.DriftStartedAt)
	}
	if observation.Status == "drifted" && state.DriftStartedAt == nil {
		started := observation.ObservedAt
		state.DriftStartedAt = &started
	}
	if observation.Status == "in_sync" {
		state.DriftStartedAt = nil
	}
	changeKind := deliveryDriftChangeKind(previous, hasPrevious, observation)
	if changeKind == "" {
		return state, DeliveryDriftEvent{}, false
	}
	event := DeliveryDriftEvent{
		AppID: state.AppID, AppName: state.AppName, ClusterID: state.ClusterID, Namespace: state.Namespace,
		Workload: state.Workload, Container: state.Container, Status: state.Status, ChangeKind: changeKind,
		ExpectedImage: copyString(state.ExpectedImage), ActualImage: copyString(state.ActualImage), BaselineLogID: copyInt64(state.BaselineLogID),
		Reason: state.Reason, ObservedAt: state.LastObservedAt, ObservedBy: state.ObservedBy, Source: state.Source, TargetKey: key,
	}
	if hasPrevious {
		event.PreviousStatus = previous.Status
	}
	if state.DriftStartedAt != nil {
		event.DriftStartedAt = copyTime(state.DriftStartedAt)
	}
	if observation.Status == "in_sync" && previous.DriftStartedAt != nil {
		seconds := int64(observation.ObservedAt.Sub(*previous.DriftStartedAt).Seconds())
		if seconds < 0 {
			seconds = 0
		}
		event.DriftStartedAt = copyTime(previous.DriftStartedAt)
		event.DriftDurationSecs = &seconds
	}
	return state, event, true
}

func deliveryDriftChangeKind(previous DeliveryDriftState, hasPrevious bool, observation DeliveryDriftObservation) string {
	if !hasPrevious {
		return "initial"
	}
	if previous.Status != observation.Status {
		return "status"
	}
	if !equalString(previous.ExpectedImage, observation.ExpectedImage) || !equalInt64(previous.BaselineLogID, observation.BaselineLogID) {
		return "baseline"
	}
	if !equalString(previous.ActualImage, observation.ActualImage) {
		return "image"
	}
	if previous.Reason != observation.Reason {
		return "reason"
	}
	return ""
}

func equalString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func equalInt64(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func copyString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
