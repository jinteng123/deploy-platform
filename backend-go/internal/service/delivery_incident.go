package service

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultDeliveryIncidentLimit             = 50
	MaxDeliveryIncidentLimit                 = 100
	DeliveryIncidentCausalityUnknown         = "unknown"
	DeliveryIncidentCausedByDeployment       = "caused_by_deployment"
	DeliveryIncidentNotCausedByDeployment    = "not_caused_by_deployment"
	DeliveryIncidentRemediationUnknown       = "unknown"
	DeliveryIncidentRemediationRollback      = "rollback"
	DeliveryIncidentRemediationHotfix        = "hotfix_deployment"
	DeliveryIncidentRemediationConfig        = "configuration_deployment"
	DeliveryIncidentRemediationRestart       = "restart_or_scale"
	DeliveryIncidentRemediationNoAction      = "no_action"
	DeliveryIncidentRemediationPlanned       = "planned"
	DeliveryIncidentRemediationUnplanned     = "unplanned"
	DeliveryIncidentRemediationNotApplicable = "not_applicable"
)

// DeliveryIncident records a production issue against the deployment that
// operators believe is related. It is an evidence ledger, not an automated
// causal analysis or a DORA metric by itself.
type DeliveryIncident struct {
	ID                     int64                   `json:"id"`
	DeployLogID            int64                   `json:"deploy_log_id"`
	ReleaseID              *int64                  `json:"release_id,omitempty"`
	ClusterID              int                     `json:"cluster_id"`
	AppName                string                  `json:"app_name"`
	Summary                string                  `json:"summary"`
	Status                 string                  `json:"status"`
	OccurredAt             time.Time               `json:"occurred_at"`
	RecordedAt             time.Time               `json:"recorded_at"`
	RecordedBy             string                  `json:"recorded_by"`
	Version                int64                   `json:"version"`
	RecoveryOccurredAt     *time.Time              `json:"recovery_occurred_at,omitempty"`
	RecoveryRecordedAt     *time.Time              `json:"recovery_recorded_at,omitempty"`
	RecoveryRecordedBy     string                  `json:"recovery_recorded_by,omitempty"`
	RecoveryConfirmation   string                  `json:"recovery_confirmation,omitempty"`
	InterventionStartedAt  *time.Time              `json:"intervention_started_at,omitempty"`
	InterventionRecordedAt *time.Time              `json:"intervention_recorded_at,omitempty"`
	InterventionRecordedBy string                  `json:"intervention_recorded_by,omitempty"`
	CausalityStatus        string                  `json:"causality_status"`
	CausalityRecordedAt    *time.Time              `json:"causality_recorded_at,omitempty"`
	CausalityRecordedBy    string                  `json:"causality_recorded_by,omitempty"`
	CausalityReason        string                  `json:"causality_reason,omitempty"`
	RemediationType        string                  `json:"remediation_type"`
	RemediationDeployLogID *int64                  `json:"remediation_deploy_log_id,omitempty"`
	RemediationPlanStatus  string                  `json:"remediation_plan_status"`
	RemediationRecordedAt  *time.Time              `json:"remediation_recorded_at,omitempty"`
	RemediationRecordedBy  string                  `json:"remediation_recorded_by,omitempty"`
	RemediationReason      string                  `json:"remediation_reason,omitempty"`
	Events                 []DeliveryIncidentEvent `json:"events"`
}

type DeliveryIncidentEvent struct {
	ID                     int64     `json:"id"`
	IncidentID             int64     `json:"incident_id"`
	EventType              string    `json:"event_type"`
	DeployLogID            int64     `json:"deploy_log_id"`
	OccurredAt             time.Time `json:"occurred_at"`
	RecordedAt             time.Time `json:"recorded_at"`
	RecordedBy             string    `json:"recorded_by"`
	Detail                 string    `json:"detail"`
	CausalityStatus        string    `json:"causality_status,omitempty"`
	RemediationType        string    `json:"remediation_type,omitempty"`
	RemediationDeployLogID *int64    `json:"remediation_deploy_log_id,omitempty"`
	RemediationPlanStatus  string    `json:"remediation_plan_status,omitempty"`
	IdempotencyKey         string    `json:"-"`
}

type DeliveryIncidentRepository interface {
	DeliveryIncidents(limit int) ([]DeliveryIncident, []DeliveryIncidentEvent, error)
	FindDeliveryIncident(id int64) (DeliveryIncident, []DeliveryIncidentEvent, error)
	CreateDeliveryIncident(DeliveryIncident, DeliveryIncidentEvent) (DeliveryIncident, DeliveryIncidentEvent, error)
	StartDeliveryIncidentIntervention(int64, int64, DeliveryIncidentEvent) (DeliveryIncident, DeliveryIncidentEvent, bool, error)
	ConfirmDeliveryIncidentRecovery(int64, int64, DeliveryIncidentEvent) (DeliveryIncident, DeliveryIncidentEvent, bool, error)
	AssessDeliveryIncidentCausality(int64, int64, DeliveryIncidentEvent) (DeliveryIncident, DeliveryIncidentEvent, bool, error)
	RecordDeliveryIncidentRemediation(int64, int64, DeliveryIncidentEvent) (DeliveryIncident, DeliveryIncidentEvent, bool, error)
}

// DeliveryIncidentRecoverySample is the small, detail-free row needed by the
// metrics view. Repositories query it directly so aggregation is not limited
// by the recent-incidents page size loaded at startup.
type DeliveryIncidentRecoverySample struct {
	Status                 string
	OccurredAt             time.Time
	InterventionStartedAt  *time.Time
	RecoveryOccurredAt     *time.Time
	CausalityStatus        string
	RemediationType        string
	RemediationPlanStatus  string
	RemediationDeployLogID *int64
}

type DeliveryIncidentMetricsRepository interface {
	DeliveryIncidentRecoverySamples(appName string, clusterID int, windowStart time.Time) ([]DeliveryIncidentRecoverySample, error)
}

func (s *Store) deliveryIncidentRecoverySamples(appName string, clusterID int, windowStart time.Time) ([]DeliveryIncidentRecoverySample, error) {
	if s.persistence != nil {
		repo, ok := s.persistence.(DeliveryIncidentMetricsRepository)
		if !ok {
			return nil, fmt.Errorf("生产事件指标存储不可用")
		}
		return repo.DeliveryIncidentRecoverySamples(appName, clusterID, windowStart)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	samples := make([]DeliveryIncidentRecoverySample, 0, len(s.deliveryIncidents))
	for _, incident := range s.deliveryIncidents {
		if (appName != "" && incident.AppName != appName) || (clusterID > 0 && incident.ClusterID != clusterID) || incident.OccurredAt.Before(windowStart) {
			continue
		}
		incident.Events = s.deliveryIncidentEvents[incident.ID]
		applyDeliveryIncidentCausality(&incident)
		applyDeliveryIncidentRemediation(&incident)
		samples = append(samples, DeliveryIncidentRecoverySample{
			Status:                 incident.Status,
			OccurredAt:             incident.OccurredAt,
			InterventionStartedAt:  incident.InterventionStartedAt,
			RecoveryOccurredAt:     incident.RecoveryOccurredAt,
			CausalityStatus:        incident.CausalityStatus,
			RemediationType:        incident.RemediationType,
			RemediationPlanStatus:  incident.RemediationPlanStatus,
			RemediationDeployLogID: copyInt64(incident.RemediationDeployLogID),
		})
	}
	return samples, nil
}

func (s *Store) LoadDeliveryIncidents(incidents []DeliveryIncident, events []DeliveryIncidentEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deliveryIncidents = make([]DeliveryIncident, len(incidents))
	s.deliveryIncidentEvents = make(map[int64][]DeliveryIncidentEvent, len(incidents))
	s.nextDeliveryIncident = 1
	for i, incident := range incidents {
		incident.Events = nil
		s.deliveryIncidents[i] = cloneDeliveryIncident(incident)
		if incident.ID >= s.nextDeliveryIncident {
			s.nextDeliveryIncident = incident.ID + 1
		}
	}
	for _, event := range events {
		s.deliveryIncidentEvents[event.IncidentID] = append(s.deliveryIncidentEvents[event.IncidentID], event)
	}
}

func cloneDeliveryIncident(incident DeliveryIncident) DeliveryIncident {
	if incident.ReleaseID != nil {
		id := *incident.ReleaseID
		incident.ReleaseID = &id
	}
	if incident.RecoveryOccurredAt != nil {
		at := *incident.RecoveryOccurredAt
		incident.RecoveryOccurredAt = &at
	}
	if incident.RecoveryRecordedAt != nil {
		at := *incident.RecoveryRecordedAt
		incident.RecoveryRecordedAt = &at
	}
	if incident.InterventionStartedAt != nil {
		at := *incident.InterventionStartedAt
		incident.InterventionStartedAt = &at
	}
	if incident.InterventionRecordedAt != nil {
		at := *incident.InterventionRecordedAt
		incident.InterventionRecordedAt = &at
	}
	if incident.CausalityRecordedAt != nil {
		at := *incident.CausalityRecordedAt
		incident.CausalityRecordedAt = &at
	}
	if incident.RemediationDeployLogID != nil {
		id := *incident.RemediationDeployLogID
		incident.RemediationDeployLogID = &id
	}
	if incident.RemediationRecordedAt != nil {
		at := *incident.RemediationRecordedAt
		incident.RemediationRecordedAt = &at
	}
	incident.Events = append([]DeliveryIncidentEvent(nil), incident.Events...)
	return incident
}

func (s *Store) DeliveryIncidents(limit int) ([]DeliveryIncident, error) {
	if limit < 1 || limit > MaxDeliveryIncidentLimit {
		limit = DefaultDeliveryIncidentLimit
	}
	if s.persistence != nil {
		repo, ok := s.persistence.(DeliveryIncidentRepository)
		if !ok {
			return nil, fmt.Errorf("生产事件台账存储不可用")
		}
		incidents, events, err := repo.DeliveryIncidents(limit)
		if err != nil {
			return nil, err
		}
		return groupDeliveryIncidentEvents(incidents, events), nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	incidents := append([]DeliveryIncident(nil), s.deliveryIncidents...)
	sort.Slice(incidents, func(i, j int) bool {
		if incidents[i].OccurredAt.Equal(incidents[j].OccurredAt) {
			return incidents[i].ID > incidents[j].ID
		}
		return incidents[i].OccurredAt.After(incidents[j].OccurredAt)
	})
	if len(incidents) > limit {
		incidents = incidents[:limit]
	}
	for i := range incidents {
		incidents[i] = cloneDeliveryIncident(incidents[i])
		incidents[i].Events = append([]DeliveryIncidentEvent(nil), s.deliveryIncidentEvents[incidents[i].ID]...)
		applyDeliveryIncidentCausality(&incidents[i])
		applyDeliveryIncidentRemediation(&incidents[i])
	}
	return incidents, nil
}

func groupDeliveryIncidentEvents(incidents []DeliveryIncident, events []DeliveryIncidentEvent) []DeliveryIncident {
	byIncident := make(map[int64][]DeliveryIncidentEvent, len(incidents))
	for _, event := range events {
		byIncident[event.IncidentID] = append(byIncident[event.IncidentID], event)
	}
	for i := range incidents {
		incidents[i].Events = byIncident[incidents[i].ID]
		applyDeliveryIncidentCausality(&incidents[i])
		applyDeliveryIncidentRemediation(&incidents[i])
	}
	return incidents
}

func applyDeliveryIncidentCausality(incident *DeliveryIncident) {
	incident.CausalityStatus = DeliveryIncidentCausalityUnknown
	incident.CausalityRecordedAt = nil
	incident.CausalityRecordedBy = ""
	incident.CausalityReason = ""
	for i := len(incident.Events) - 1; i >= 0; i-- {
		event := incident.Events[i]
		if event.EventType != "causality_assessed" {
			continue
		}
		incident.CausalityStatus = event.CausalityStatus
		at := event.RecordedAt
		incident.CausalityRecordedAt = &at
		incident.CausalityRecordedBy = event.RecordedBy
		incident.CausalityReason = event.Detail
		return
	}
}

func applyDeliveryIncidentRemediation(incident *DeliveryIncident) {
	incident.RemediationType = DeliveryIncidentRemediationUnknown
	incident.RemediationDeployLogID = nil
	incident.RemediationPlanStatus = DeliveryIncidentRemediationUnknown
	incident.RemediationRecordedAt = nil
	incident.RemediationRecordedBy = ""
	incident.RemediationReason = ""
	for i := len(incident.Events) - 1; i >= 0; i-- {
		event := incident.Events[i]
		if event.EventType != "remediation_classified" {
			continue
		}
		incident.RemediationType = event.RemediationType
		incident.RemediationDeployLogID = event.RemediationDeployLogID
		incident.RemediationPlanStatus = event.RemediationPlanStatus
		at := event.RecordedAt
		incident.RemediationRecordedAt = &at
		incident.RemediationRecordedBy = event.RecordedBy
		incident.RemediationReason = event.Detail
		return
	}
}

func (s *Store) CreateDeliveryIncident(deployLogID int64, occurredAt time.Time, summary, recordedBy, idempotencyKey string) (DeliveryIncident, error) {
	log, err := s.FindLog(deployLogID)
	if err != nil {
		return DeliveryIncident{}, err
	}
	if log.EnvironmentTierSnapshot == nil || *log.EnvironmentTierSnapshot != "production" {
		return DeliveryIncident{}, NewValidationError("故障事件必须关联一条有生产环境快照的部署")
	}
	summary = strings.TrimSpace(summary)
	recordedBy = strings.TrimSpace(recordedBy)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if occurredAt.IsZero() || occurredAt.After(time.Now().UTC().Add(5*time.Minute)) {
		return DeliveryIncident{}, NewValidationError("事件发生时间必填，且不能明显晚于当前时间")
	}
	if summary == "" || utf8.RuneCountInString(summary) > 1000 || len(summary) > 4000 || !utf8.ValidString(summary) {
		return DeliveryIncident{}, NewValidationError("事件摘要必填且不能超过 1000 字符")
	}
	if recordedBy == "" || len(recordedBy) > 64 {
		return DeliveryIncident{}, NewValidationError("登记人无效")
	}
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return DeliveryIncident{}, NewValidationError("idempotency_key 长度必须在 8 到 128 个字符之间")
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	incident := DeliveryIncident{DeployLogID: log.ID, ClusterID: log.ClusterID, AppName: log.AppName, Summary: summary, Status: "open", OccurredAt: occurredAt.UTC().Truncate(time.Microsecond), RecordedAt: now, RecordedBy: recordedBy, Version: 1}
	if log.ReleaseID > 0 {
		id := log.ReleaseID
		incident.ReleaseID = &id
	}
	event := DeliveryIncidentEvent{EventType: "incident_recorded", DeployLogID: log.ID, OccurredAt: incident.OccurredAt, RecordedAt: now, RecordedBy: recordedBy, Detail: summary, IdempotencyKey: idempotencyKey}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.persistence != nil {
		repo, ok := s.persistence.(DeliveryIncidentRepository)
		if !ok {
			return DeliveryIncident{}, fmt.Errorf("生产事件台账存储不可用")
		}
		stored, storedEvent, err := repo.CreateDeliveryIncident(incident, event)
		if err != nil {
			return DeliveryIncident{}, err
		}
		s.rememberDeliveryIncidentLocked(stored, storedEvent)
		return s.deliveryIncidentViewLocked(stored), nil
	}
	if s.deliveryIncidentEvents == nil {
		s.deliveryIncidentEvents = map[int64][]DeliveryIncidentEvent{}
	}
	for _, existing := range s.deliveryIncidentEvents {
		for _, row := range existing {
			if row.IdempotencyKey == idempotencyKey {
				if row.EventType != event.EventType || row.DeployLogID != event.DeployLogID || !row.OccurredAt.Equal(event.OccurredAt) || row.RecordedBy != event.RecordedBy || row.Detail != event.Detail {
					return DeliveryIncident{}, newConflict("idempotency_key 已被不同的生产事件使用")
				}
				for _, current := range s.deliveryIncidents {
					if current.ID == row.IncidentID {
						return s.deliveryIncidentViewLocked(current), nil
					}
				}
			}
		}
	}
	incident.ID = s.nextDeliveryIncident
	s.nextDeliveryIncident++
	event.ID = int64(len(s.deliveryIncidentEvents[incident.ID]) + 1)
	event.IncidentID = incident.ID
	s.deliveryIncidents = append(s.deliveryIncidents, incident)
	s.deliveryIncidentEvents[incident.ID] = append(s.deliveryIncidentEvents[incident.ID], event)
	return s.deliveryIncidentViewLocked(incident), nil
}

func (s *Store) StartDeliveryIncidentIntervention(id, expectedVersion int64, occurredAt time.Time, detail, recordedBy, idempotencyKey string) (DeliveryIncident, bool, error) {
	detail = strings.TrimSpace(detail)
	recordedBy = strings.TrimSpace(recordedBy)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if id <= 0 || expectedVersion < 1 {
		return DeliveryIncident{}, false, NewValidationError("事件 ID 或版本无效")
	}
	if occurredAt.IsZero() || occurredAt.After(time.Now().UTC().Add(5*time.Minute)) {
		return DeliveryIncident{}, false, NewValidationError("干预开始时间必填，且不能明显晚于当前时间")
	}
	if detail == "" || utf8.RuneCountInString(detail) > 1000 || len(detail) > 4000 || !utf8.ValidString(detail) {
		return DeliveryIncident{}, false, NewValidationError("干预措施必填且不能超过 1000 字符")
	}
	if recordedBy == "" || len(recordedBy) > 64 || len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return DeliveryIncident{}, false, NewValidationError("登记人或 idempotency_key 无效")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var repo DeliveryIncidentRepository
	if s.persistence != nil {
		var exists bool
		repo, exists = s.persistence.(DeliveryIncidentRepository)
		if !exists {
			return DeliveryIncident{}, false, fmt.Errorf("生产事件台账存储不可用")
		}
	}
	current, index, ok := s.findDeliveryIncidentLocked(id)
	if !ok {
		if repo == nil {
			return DeliveryIncident{}, false, ErrNotFound
		}
		var events []DeliveryIncidentEvent
		var err error
		current, events, err = repo.FindDeliveryIncident(id)
		if err != nil {
			return DeliveryIncident{}, false, err
		}
		if s.deliveryIncidentEvents == nil {
			s.deliveryIncidentEvents = map[int64][]DeliveryIncidentEvent{}
		}
		index = len(s.deliveryIncidents)
		s.deliveryIncidents = append(s.deliveryIncidents, cloneDeliveryIncident(current))
		s.deliveryIncidentEvents[id] = append([]DeliveryIncidentEvent(nil), events...)
	}
	occurredAt = occurredAt.UTC().Truncate(time.Microsecond)
	if occurredAt.Before(current.OccurredAt) {
		return DeliveryIncident{}, false, NewValidationError("干预开始时间不能早于故障发生时间")
	}
	event := DeliveryIncidentEvent{IncidentID: id, EventType: "intervention_started", DeployLogID: current.DeployLogID, OccurredAt: occurredAt, RecordedAt: time.Now().UTC().Truncate(time.Microsecond), RecordedBy: recordedBy, Detail: detail, IdempotencyKey: idempotencyKey}
	if repo != nil {
		stored, storedEvent, changed, err := repo.StartDeliveryIncidentIntervention(id, expectedVersion, event)
		if err != nil {
			return DeliveryIncident{}, false, err
		}
		s.rememberDeliveryIncidentLocked(stored, storedEvent)
		return s.deliveryIncidentViewLocked(stored), changed, nil
	}
	for _, rows := range s.deliveryIncidentEvents {
		for _, existing := range rows {
			if existing.IdempotencyKey == idempotencyKey {
				if existing.IncidentID == id && deliveryIncidentEventMatches(existing, event) {
					return s.deliveryIncidentViewLocked(current), false, nil
				}
				return DeliveryIncident{}, false, newConflict("idempotency_key 已被不同的生产事件使用")
			}
		}
	}
	if expectedVersion != current.Version {
		return DeliveryIncident{}, false, newConflict("生产事件已被其他操作更新")
	}
	if current.Status != "open" {
		return DeliveryIncident{}, false, newConflict("已确认恢复的生产事件不能开始干预")
	}
	if current.InterventionStartedAt != nil {
		return DeliveryIncident{}, false, newConflict("生产事件已记录干预开始时间")
	}
	current.Version++
	current.InterventionStartedAt = &event.OccurredAt
	current.InterventionRecordedAt = &event.RecordedAt
	current.InterventionRecordedBy = recordedBy
	event.ID = int64(len(s.deliveryIncidentEvents[id]) + 1)
	s.deliveryIncidentEvents[id] = append(s.deliveryIncidentEvents[id], event)
	s.deliveryIncidents[index] = current
	return s.deliveryIncidentViewLocked(current), true, nil
}

func (s *Store) ConfirmDeliveryIncidentRecovery(id, expectedVersion int64, occurredAt time.Time, detail, recordedBy, idempotencyKey string) (DeliveryIncident, bool, error) {
	detail = strings.TrimSpace(detail)
	recordedBy = strings.TrimSpace(recordedBy)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if id <= 0 || expectedVersion < 1 {
		return DeliveryIncident{}, false, NewValidationError("事件 ID 或版本无效")
	}
	if occurredAt.IsZero() || occurredAt.After(time.Now().UTC().Add(5*time.Minute)) {
		return DeliveryIncident{}, false, NewValidationError("业务恢复时间必填，且不能明显晚于当前时间")
	}
	if detail == "" || utf8.RuneCountInString(detail) > 1000 || len(detail) > 4000 || !utf8.ValidString(detail) {
		return DeliveryIncident{}, false, NewValidationError("恢复确认说明必填且不能超过 1000 字符")
	}
	if recordedBy == "" || len(recordedBy) > 64 || len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return DeliveryIncident{}, false, NewValidationError("登记人或 idempotency_key 无效")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var repo DeliveryIncidentRepository
	if s.persistence != nil {
		var exists bool
		repo, exists = s.persistence.(DeliveryIncidentRepository)
		if !exists {
			return DeliveryIncident{}, false, fmt.Errorf("生产事件台账存储不可用")
		}
	}
	current, index, ok := s.findDeliveryIncidentLocked(id)
	if !ok {
		if repo == nil {
			return DeliveryIncident{}, false, ErrNotFound
		}
		var events []DeliveryIncidentEvent
		var err error
		current, events, err = repo.FindDeliveryIncident(id)
		if err != nil {
			return DeliveryIncident{}, false, err
		}
		if s.deliveryIncidentEvents == nil {
			s.deliveryIncidentEvents = map[int64][]DeliveryIncidentEvent{}
		}
		index = len(s.deliveryIncidents)
		s.deliveryIncidents = append(s.deliveryIncidents, cloneDeliveryIncident(current))
		s.deliveryIncidentEvents[id] = append([]DeliveryIncidentEvent(nil), events...)
	}
	if occurredAt.Before(current.OccurredAt) || (current.InterventionStartedAt != nil && occurredAt.Before(*current.InterventionStartedAt)) {
		return DeliveryIncident{}, false, NewValidationError("业务恢复时间不能早于故障发生或干预开始时间")
	}
	event := DeliveryIncidentEvent{IncidentID: id, EventType: "recovery_confirmed", DeployLogID: current.DeployLogID, OccurredAt: occurredAt.UTC().Truncate(time.Microsecond), RecordedAt: time.Now().UTC().Truncate(time.Microsecond), RecordedBy: recordedBy, Detail: detail, IdempotencyKey: idempotencyKey}
	if repo != nil {
		stored, storedEvent, changed, err := repo.ConfirmDeliveryIncidentRecovery(id, expectedVersion, event)
		if err != nil {
			return DeliveryIncident{}, false, err
		}
		s.rememberDeliveryIncidentLocked(stored, storedEvent)
		return s.deliveryIncidentViewLocked(stored), changed, nil
	}
	for _, rows := range s.deliveryIncidentEvents {
		for _, existing := range rows {
			if existing.IdempotencyKey == idempotencyKey {
				if existing.IncidentID == id && deliveryIncidentEventMatches(existing, event) {
					return s.deliveryIncidentViewLocked(current), false, nil
				}
				return DeliveryIncident{}, false, newConflict("idempotency_key 已被不同的生产事件使用")
			}
		}
	}
	if expectedVersion != current.Version {
		return DeliveryIncident{}, false, newConflict("生产事件已被其他操作更新")
	}
	if current.Status != "open" {
		return DeliveryIncident{}, false, newConflict("生产事件已确认恢复")
	}
	current.Status = "recovered"
	current.Version++
	current.RecoveryOccurredAt = &event.OccurredAt
	current.RecoveryRecordedAt = &event.RecordedAt
	current.RecoveryRecordedBy = recordedBy
	current.RecoveryConfirmation = detail
	event.ID = int64(len(s.deliveryIncidentEvents[id]) + 1)
	s.deliveryIncidentEvents[id] = append(s.deliveryIncidentEvents[id], event)
	s.deliveryIncidents[index] = current
	return s.deliveryIncidentViewLocked(current), true, nil
}

// AssessDeliveryIncidentCausality appends an operator's explicit assessment
// of whether the linked production deployment caused the incident. The
// assessment is never inferred and can be superseded by a later versioned
// assessment while the event history remains immutable.
func (s *Store) AssessDeliveryIncidentCausality(id, expectedVersion int64, status, reason, recordedBy, idempotencyKey string) (DeliveryIncident, bool, error) {
	status = strings.TrimSpace(status)
	reason = strings.TrimSpace(reason)
	recordedBy = strings.TrimSpace(recordedBy)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if id <= 0 || expectedVersion < 1 {
		return DeliveryIncident{}, false, NewValidationError("事件 ID 或版本无效")
	}
	if status != DeliveryIncidentCausedByDeployment && status != DeliveryIncidentNotCausedByDeployment {
		return DeliveryIncident{}, false, NewValidationError("因果判定必须明确选择由部署导致或未认定由部署导致")
	}
	if reason == "" || utf8.RuneCountInString(reason) > 1000 || len(reason) > 4000 || !utf8.ValidString(reason) {
		return DeliveryIncident{}, false, NewValidationError("判定依据必填且不能超过 1000 字符")
	}
	if recordedBy == "" || len(recordedBy) > 64 || len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return DeliveryIncident{}, false, NewValidationError("登记人或 idempotency_key 无效")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var repo DeliveryIncidentRepository
	if s.persistence != nil {
		var exists bool
		repo, exists = s.persistence.(DeliveryIncidentRepository)
		if !exists {
			return DeliveryIncident{}, false, fmt.Errorf("生产事件台账存储不可用")
		}
	}
	current, index, ok := s.findDeliveryIncidentLocked(id)
	if !ok {
		if repo == nil {
			return DeliveryIncident{}, false, ErrNotFound
		}
		var events []DeliveryIncidentEvent
		var err error
		current, events, err = repo.FindDeliveryIncident(id)
		if err != nil {
			return DeliveryIncident{}, false, err
		}
		if s.deliveryIncidentEvents == nil {
			s.deliveryIncidentEvents = map[int64][]DeliveryIncidentEvent{}
		}
		index = len(s.deliveryIncidents)
		s.deliveryIncidents = append(s.deliveryIncidents, cloneDeliveryIncident(current))
		s.deliveryIncidentEvents[id] = append([]DeliveryIncidentEvent(nil), events...)
	}
	if current.DeployLogID <= 0 {
		return DeliveryIncident{}, false, NewValidationError("生产事件缺少关联部署，无法判定因果关系")
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	event := DeliveryIncidentEvent{
		IncidentID: id, EventType: "causality_assessed", DeployLogID: current.DeployLogID,
		OccurredAt: now, RecordedAt: now,
		RecordedBy: recordedBy, Detail: reason, CausalityStatus: status, IdempotencyKey: idempotencyKey,
	}
	for _, rows := range s.deliveryIncidentEvents {
		for _, existing := range rows {
			if existing.IdempotencyKey != idempotencyKey {
				continue
			}
			if existing.IncidentID == id && deliveryIncidentCausalityEventMatches(existing, event) {
				return s.deliveryIncidentViewLocked(current), false, nil
			}
			return DeliveryIncident{}, false, newConflict("idempotency_key 已被不同的生产事件使用")
		}
	}
	if repo != nil {
		stored, storedEvent, changed, err := repo.AssessDeliveryIncidentCausality(id, expectedVersion, event)
		if err != nil {
			return DeliveryIncident{}, false, err
		}
		s.rememberDeliveryIncidentLocked(stored, storedEvent)
		return s.deliveryIncidentViewLocked(stored), changed, nil
	}
	if expectedVersion != current.Version {
		return DeliveryIncident{}, false, newConflict("生产事件已被其他操作更新")
	}
	current.Version++
	event.ID = int64(len(s.deliveryIncidentEvents[id]) + 1)
	s.deliveryIncidentEvents[id] = append(s.deliveryIncidentEvents[id], event)
	s.deliveryIncidents[index] = current
	return s.deliveryIncidentViewLocked(current), true, nil
}

// RecordDeliveryIncidentRemediation appends an operator's remediation
// classification. A linked deployment must be a successful production deploy
// for the incident's exact cluster and application; the ledger never infers
// remediation type or plan status from deployment data.
func (s *Store) RecordDeliveryIncidentRemediation(id, expectedVersion int64, remediationType, planStatus string, remediationDeployLogID int64, reason, recordedBy, idempotencyKey string) (DeliveryIncident, bool, error) {
	remediationType = strings.TrimSpace(remediationType)
	planStatus = strings.TrimSpace(planStatus)
	reason = strings.TrimSpace(reason)
	recordedBy = strings.TrimSpace(recordedBy)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if id <= 0 || expectedVersion < 1 {
		return DeliveryIncident{}, false, NewValidationError("事件 ID 或版本无效")
	}
	usesDeployment := remediationType == DeliveryIncidentRemediationRollback || remediationType == DeliveryIncidentRemediationHotfix || remediationType == DeliveryIncidentRemediationConfig
	switch remediationType {
	case DeliveryIncidentRemediationRollback, DeliveryIncidentRemediationHotfix, DeliveryIncidentRemediationConfig:
		if remediationDeployLogID <= 0 || (planStatus != DeliveryIncidentRemediationPlanned && planStatus != DeliveryIncidentRemediationUnplanned) {
			return DeliveryIncident{}, false, NewValidationError("部署类处置必须关联成功的生产部署并标记计划内或计划外")
		}
	case DeliveryIncidentRemediationRestart, DeliveryIncidentRemediationNoAction:
		if remediationDeployLogID != 0 || planStatus != DeliveryIncidentRemediationNotApplicable {
			return DeliveryIncident{}, false, NewValidationError("重启/扩缩容或无需处置不得关联部署，计划状态必须为不适用")
		}
	default:
		return DeliveryIncident{}, false, NewValidationError("请选择有效的修复处置分类")
	}
	if reason == "" || utf8.RuneCountInString(reason) > 1000 || len(reason) > 4000 || !utf8.ValidString(reason) {
		return DeliveryIncident{}, false, NewValidationError("处置依据必填且不能超过 1000 字符")
	}
	if recordedBy == "" || len(recordedBy) > 64 || len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return DeliveryIncident{}, false, NewValidationError("登记人或 idempotency_key 无效")
	}

	var remediationLog DeployLog
	if usesDeployment {
		var err error
		remediationLog, err = s.FindLog(remediationDeployLogID)
		if err != nil {
			return DeliveryIncident{}, false, err
		}
		if remediationLog.EnvironmentTierSnapshot == nil || *remediationLog.EnvironmentTierSnapshot != "production" || remediationLog.Status != "success" {
			return DeliveryIncident{}, false, NewValidationError("修复部署必须是已成功的生产环境部署")
		}
		if remediationType == DeliveryIncidentRemediationRollback && remediationLog.DeployType != "rollback" {
			return DeliveryIncident{}, false, NewValidationError("回滚处置必须关联回滚部署记录")
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var repo DeliveryIncidentRepository
	if s.persistence != nil {
		var exists bool
		repo, exists = s.persistence.(DeliveryIncidentRepository)
		if !exists {
			return DeliveryIncident{}, false, fmt.Errorf("生产事件台账存储不可用")
		}
	}
	current, index, ok := s.findDeliveryIncidentLocked(id)
	if !ok {
		if repo == nil {
			return DeliveryIncident{}, false, ErrNotFound
		}
		var events []DeliveryIncidentEvent
		var err error
		current, events, err = repo.FindDeliveryIncident(id)
		if err != nil {
			return DeliveryIncident{}, false, err
		}
		if s.deliveryIncidentEvents == nil {
			s.deliveryIncidentEvents = map[int64][]DeliveryIncidentEvent{}
		}
		index = len(s.deliveryIncidents)
		s.deliveryIncidents = append(s.deliveryIncidents, cloneDeliveryIncident(current))
		s.deliveryIncidentEvents[id] = append([]DeliveryIncidentEvent(nil), events...)
	}
	if current.DeployLogID <= 0 {
		return DeliveryIncident{}, false, NewValidationError("生产事件缺少关联部署，无法登记修复处置")
	}
	if usesDeployment {
		if remediationDeployLogID == current.DeployLogID {
			return DeliveryIncident{}, false, NewValidationError("修复部署不能与最初关联的故障部署相同")
		}
		if remediationLog.ClusterID != current.ClusterID || remediationLog.AppName != current.AppName {
			return DeliveryIncident{}, false, NewValidationError("修复部署必须属于该故障的同一集群和应用")
		}
	}
	var remediationID *int64
	if usesDeployment {
		remediationID = &remediationDeployLogID
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	event := DeliveryIncidentEvent{
		IncidentID: id, EventType: "remediation_classified", DeployLogID: current.DeployLogID,
		OccurredAt: now, RecordedAt: now, RecordedBy: recordedBy, Detail: reason,
		RemediationType: remediationType, RemediationDeployLogID: remediationID,
		RemediationPlanStatus: planStatus, IdempotencyKey: idempotencyKey,
	}
	for _, rows := range s.deliveryIncidentEvents {
		for _, existing := range rows {
			if existing.IdempotencyKey != idempotencyKey {
				continue
			}
			if existing.IncidentID == id && deliveryIncidentRemediationEventMatches(existing, event) {
				return s.deliveryIncidentViewLocked(current), false, nil
			}
			return DeliveryIncident{}, false, newConflict("idempotency_key 已被不同的生产事件使用")
		}
	}
	if repo != nil {
		stored, storedEvent, changed, err := repo.RecordDeliveryIncidentRemediation(id, expectedVersion, event)
		if err != nil {
			return DeliveryIncident{}, false, err
		}
		s.rememberDeliveryIncidentLocked(stored, storedEvent)
		return s.deliveryIncidentViewLocked(stored), changed, nil
	}
	if expectedVersion != current.Version {
		return DeliveryIncident{}, false, newConflict("生产事件已被其他操作更新")
	}
	current.Version++
	event.ID = int64(len(s.deliveryIncidentEvents[id]) + 1)
	s.deliveryIncidentEvents[id] = append(s.deliveryIncidentEvents[id], event)
	s.deliveryIncidents[index] = current
	return s.deliveryIncidentViewLocked(current), true, nil
}

func deliveryIncidentEventMatches(a, b DeliveryIncidentEvent) bool {
	return a.EventType == b.EventType && a.DeployLogID == b.DeployLogID &&
		a.OccurredAt.Equal(b.OccurredAt) && a.RecordedBy == b.RecordedBy && a.Detail == b.Detail
}

func deliveryIncidentCausalityEventMatches(a, b DeliveryIncidentEvent) bool {
	return a.EventType == b.EventType && a.DeployLogID == b.DeployLogID &&
		a.RecordedBy == b.RecordedBy && a.Detail == b.Detail && a.CausalityStatus == b.CausalityStatus
}

func deliveryIncidentRemediationEventMatches(a, b DeliveryIncidentEvent) bool {
	var aID, bID int64
	if a.RemediationDeployLogID != nil {
		aID = *a.RemediationDeployLogID
	}
	if b.RemediationDeployLogID != nil {
		bID = *b.RemediationDeployLogID
	}
	return a.EventType == b.EventType && a.DeployLogID == b.DeployLogID &&
		a.RecordedBy == b.RecordedBy && a.Detail == b.Detail && a.RemediationType == b.RemediationType &&
		a.RemediationPlanStatus == b.RemediationPlanStatus && aID == bID
}

func (s *Store) findDeliveryIncidentLocked(id int64) (DeliveryIncident, int, bool) {
	for i, incident := range s.deliveryIncidents {
		if incident.ID == id {
			return incident, i, true
		}
	}
	return DeliveryIncident{}, -1, false
}

func (s *Store) deliveryIncidentViewLocked(incident DeliveryIncident) DeliveryIncident {
	incident = cloneDeliveryIncident(incident)
	incident.Events = append([]DeliveryIncidentEvent(nil), s.deliveryIncidentEvents[incident.ID]...)
	applyDeliveryIncidentCausality(&incident)
	applyDeliveryIncidentRemediation(&incident)
	return incident
}

func (s *Store) rememberDeliveryIncidentLocked(incident DeliveryIncident, event DeliveryIncidentEvent) {
	if s.deliveryIncidentEvents == nil {
		s.deliveryIncidentEvents = map[int64][]DeliveryIncidentEvent{}
	}
	if s.nextDeliveryIncident <= incident.ID {
		s.nextDeliveryIncident = incident.ID + 1
	}
	_, index, exists := s.findDeliveryIncidentLocked(incident.ID)
	if exists {
		s.deliveryIncidents[index] = cloneDeliveryIncident(incident)
	} else {
		s.deliveryIncidents = append(s.deliveryIncidents, cloneDeliveryIncident(incident))
	}
	if len(incident.Events) > 0 {
		s.deliveryIncidentEvents[incident.ID] = append([]DeliveryIncidentEvent(nil), incident.Events...)
	}
	if event.ID > 0 {
		rows := s.deliveryIncidentEvents[incident.ID]
		found := false
		for i := range rows {
			if rows[i].ID == event.ID || (event.IdempotencyKey != "" && rows[i].IdempotencyKey == event.IdempotencyKey) {
				rows[i] = event
				found = true
				break
			}
		}
		if !found {
			s.deliveryIncidentEvents[incident.ID] = append(rows, event)
		}
	}
}
