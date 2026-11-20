package service

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

const CanaryStageCheckpointSchemaVersion = 2

const (
	CanaryStageAwaitingWeights    = "awaiting_weights"
	CanaryStageObserving          = "observing"
	CanaryStageAwaitingConfirm    = "awaiting_confirmation"
	CanaryStageRestoreRecommended = "restore_recommended"
	CanaryStagePaused             = "paused"
	CanaryStageNeedsAttention     = "needs_attention"
	CanaryStageCompleted          = "completed"
)

// CanaryStageCheckpoint is the versioned, credential-free snapshot attached
// to a delivery-run event. It captures enough verified state to reconstruct
// the current stage after restart; requested weights alone never stand in for
// gateway readback.
type CanaryStageCheckpoint struct {
	SchemaVersion              int                      `json:"schema_version"`
	StageIndex                 int                      `json:"stage_index"`
	CandidateWeightPercent     int                      `json:"candidate_weight_percent"`
	ObservationDurationSeconds int64                    `json:"observation_duration_seconds"`
	Phase                      string                   `json:"phase"`
	ConfirmedStageIndex        *int                     `json:"confirmed_stage_index,omitempty"`
	ConfirmedBy                string                   `json:"confirmed_by,omitempty"`
	ConfirmedAt                *time.Time               `json:"confirmed_at,omitempty"`
	StageStartedAt             *time.Time               `json:"stage_started_at,omitempty"`
	EffectiveWeights           *CanaryTrafficWeights    `json:"effective_weights,omitempty"`
	Observation                *CanaryMetricObservation `json:"observation,omitempty"`
	Decision                   *CanaryStageDecision     `json:"decision,omitempty"`
}

// CanaryStageCheckpointEvent builds the canonical event envelope for a typed
// checkpoint. Run status remains in DeliveryRunEvent.Status; the strategy
// phase and metrics live in the structured payload.
func CanaryStageCheckpointEvent(eventKey, runStatus, detail string, checkpoint CanaryStageCheckpoint) (DeliveryRunEvent, error) {
	if eventKey == "" || len(eventKey) > 160 || len(detail) > 2000 || !utf8ValidString(detail) {
		return DeliveryRunEvent{}, NewValidationError("金丝雀阶段事件键或说明无效")
	}
	if runStatus != "running" && runStatus != "paused" &&
		runStatus != "success" && runStatus != "failed" && runStatus != "interrupted" && runStatus != "cancelled" {
		return DeliveryRunEvent{}, NewValidationError("金丝雀阶段运行状态无效")
	}
	if err := validateCanaryStageCheckpoint(checkpoint); err != nil {
		return DeliveryRunEvent{}, err
	}
	switch checkpoint.Phase {
	case CanaryStagePaused:
		if runStatus != "paused" {
			return DeliveryRunEvent{}, NewValidationError("暂停阶段必须使用 paused 运行状态")
		}
	case CanaryStageNeedsAttention:
		if runStatus != "running" {
			return DeliveryRunEvent{}, NewValidationError("待核对阶段必须保持运行状态以保留资源锁")
		}
	case CanaryStageCompleted:
		if runStatus != "success" {
			return DeliveryRunEvent{}, NewValidationError("完成阶段必须使用 success 运行状态")
		}
	default:
		if runStatus != "running" {
			return DeliveryRunEvent{}, NewValidationError("未完成的金丝雀阶段必须保持 running 状态")
		}
	}
	payload, err := json.Marshal(checkpoint)
	if err != nil {
		return DeliveryRunEvent{}, fmt.Errorf("编码金丝雀阶段快照失败: %w", err)
	}
	if err := validateDeliveryRunEventPayload(payload); err != nil {
		return DeliveryRunEvent{}, err
	}
	return DeliveryRunEvent{
		EventKey: eventKey, EventType: "canary_checkpoint",
		Stage:  fmt.Sprintf("canary_stage_%d", checkpoint.StageIndex+1),
		Status: runStatus, Detail: detail, Payload: payload,
	}, nil
}

func DecodeCanaryStageCheckpoint(event DeliveryRunEvent) (CanaryStageCheckpoint, error) {
	if event.EventType != "canary_checkpoint" || len(event.Payload) == 0 {
		return CanaryStageCheckpoint{}, NewValidationError("发布事件不是结构化金丝雀阶段快照")
	}
	var checkpoint CanaryStageCheckpoint
	if err := json.Unmarshal(event.Payload, &checkpoint); err != nil {
		return CanaryStageCheckpoint{}, NewValidationError("金丝雀阶段快照无法解析")
	}
	if err := validateCanaryStageCheckpoint(checkpoint); err != nil {
		return CanaryStageCheckpoint{}, err
	}
	if event.Stage != fmt.Sprintf("canary_stage_%d", checkpoint.StageIndex+1) {
		return CanaryStageCheckpoint{}, NewValidationError("金丝雀事件阶段与结构化快照不一致")
	}
	return checkpoint, nil
}

// LatestCanaryStageCheckpoint replays the append-only run timeline. A corrupt
// newest checkpoint fails closed instead of silently resuming an older stage.
func LatestCanaryStageCheckpoint(events []DeliveryRunEvent) (*CanaryStageCheckpoint, error) {
	var latest *CanaryStageCheckpoint
	for _, event := range events {
		if event.EventType != "canary_checkpoint" {
			continue
		}
		checkpoint, err := DecodeCanaryStageCheckpoint(event)
		if err != nil {
			return nil, err
		}
		latest = &checkpoint
	}
	return latest, nil
}

func validateCanaryStageCheckpoint(checkpoint CanaryStageCheckpoint) error {
	if (checkpoint.SchemaVersion != 1 && checkpoint.SchemaVersion != CanaryStageCheckpointSchemaVersion) || checkpoint.StageIndex < 0 || checkpoint.StageIndex >= 10 ||
		checkpoint.CandidateWeightPercent < 1 || checkpoint.CandidateWeightPercent > 100 ||
		checkpoint.ObservationDurationSeconds < 1 || checkpoint.ObservationDurationSeconds > int64((24*time.Hour)/time.Second) {
		return NewValidationError("金丝雀阶段快照字段无效")
	}
	switch checkpoint.Phase {
	case CanaryStageAwaitingWeights, CanaryStageObserving, CanaryStageAwaitingConfirm,
		CanaryStageRestoreRecommended, CanaryStagePaused, CanaryStageNeedsAttention, CanaryStageCompleted:
	default:
		return NewValidationError("金丝雀阶段快照状态无效")
	}
	hasConfirmation := checkpoint.ConfirmedStageIndex != nil || checkpoint.ConfirmedBy != "" || checkpoint.ConfirmedAt != nil
	if hasConfirmation {
		if checkpoint.ConfirmedStageIndex == nil || checkpoint.ConfirmedBy == "" ||
			len(checkpoint.ConfirmedBy) > 128 || !utf8ValidString(checkpoint.ConfirmedBy) ||
			checkpoint.ConfirmedAt == nil || checkpoint.ConfirmedAt.IsZero() ||
			strings.TrimSpace(checkpoint.ConfirmedBy) != checkpoint.ConfirmedBy ||
			*checkpoint.ConfirmedStageIndex < 0 || *checkpoint.ConfirmedStageIndex > checkpoint.StageIndex ||
			(*checkpoint.ConfirmedStageIndex == checkpoint.StageIndex && checkpoint.Phase != CanaryStageCompleted) ||
			(*checkpoint.ConfirmedStageIndex < checkpoint.StageIndex && checkpoint.Phase == CanaryStageCompleted) {
			return NewValidationError("金丝雀人工确认记录与阶段快照不一致")
		}
	} else if checkpoint.Phase == CanaryStageCompleted || checkpoint.StageIndex > 0 {
		return NewValidationError("金丝雀推进或完成阶段必须保留操作员确认记录")
	}
	if checkpoint.EffectiveWeights != nil &&
		(checkpoint.EffectiveWeights.StableWeightPercent != 100-checkpoint.CandidateWeightPercent ||
			checkpoint.EffectiveWeights.CandidateWeightPercent != checkpoint.CandidateWeightPercent ||
			checkpoint.EffectiveWeights.StableWeightPercent+checkpoint.EffectiveWeights.CandidateWeightPercent != 100) {
		return NewValidationError("金丝雀阶段有效权重与计划比例不一致")
	}
	if checkpoint.Phase == CanaryStageObserving || checkpoint.Phase == CanaryStageAwaitingConfirm ||
		checkpoint.Phase == CanaryStageRestoreRecommended || checkpoint.Phase == CanaryStageCompleted {
		if checkpoint.StageStartedAt == nil || checkpoint.StageStartedAt.IsZero() || checkpoint.EffectiveWeights == nil {
			return NewValidationError("已生效金丝雀阶段必须保存起始时间和网关权重回读")
		}
	}
	if checkpoint.Decision != nil {
		if checkpoint.Observation == nil || checkpoint.EffectiveWeights == nil || checkpoint.Decision.StageIndex != checkpoint.StageIndex ||
			checkpoint.Decision.CandidateWeightPercent != checkpoint.CandidateWeightPercent ||
			checkpoint.Decision.EffectiveWeights != *checkpoint.EffectiveWeights ||
			checkpoint.SchemaVersion >= 2 && (checkpoint.Decision.EvaluatedAt.IsZero() || checkpoint.Decision.EvaluatedAt.Before(checkpoint.Observation.ObservedAt)) {
			return NewValidationError("金丝雀指标结论与阶段快照不一致")
		}
	}
	if checkpoint.Observation != nil {
		observation := checkpoint.Observation
		if checkpoint.StageStartedAt == nil || checkpoint.EffectiveWeights == nil ||
			observation.WindowStart.IsZero() || observation.WindowEnd.IsZero() || observation.ObservedAt.IsZero() ||
			!observation.WindowStart.Before(observation.WindowEnd) || observation.WindowEnd.After(observation.ObservedAt) ||
			observation.WindowStart.Before(*checkpoint.StageStartedAt) ||
			observation.WindowEnd.Sub(observation.WindowStart) < time.Duration(checkpoint.ObservationDurationSeconds)*time.Second ||
			NormalizeDigest(observation.StableImageDigest) == "" || NormalizeDigest(observation.CandidateImageDigest) == "" ||
			NormalizeDigest(observation.StableImageDigest) == NormalizeDigest(observation.CandidateImageDigest) ||
			NormalizeDigest(observation.Stable.ImageDigest) != NormalizeDigest(observation.StableImageDigest) ||
			NormalizeDigest(observation.Candidate.ImageDigest) != NormalizeDigest(observation.CandidateImageDigest) ||
			validateCanaryVersionMetrics(observation.Stable) != nil || validateCanaryVersionMetrics(observation.Candidate) != nil {
			return NewValidationError("金丝雀观测与已生效阶段的身份、窗口或指标字段不一致")
		}
	}
	if checkpoint.Phase == CanaryStageAwaitingConfirm &&
		(checkpoint.Decision == nil || checkpoint.Decision.Analysis.Status != "awaiting_confirmation") {
		return NewValidationError("只有健康且待人工确认的金丝雀阶段可以进入确认关口")
	}
	if checkpoint.Phase == CanaryStageRestoreRecommended &&
		(checkpoint.Decision == nil || checkpoint.Decision.Analysis.Status != "restore_recommended") {
		return NewValidationError("恢复建议阶段必须关联越界或降级结论")
	}
	if checkpoint.Phase == CanaryStageAwaitingWeights &&
		(checkpoint.StageStartedAt != nil || checkpoint.EffectiveWeights != nil || checkpoint.Observation != nil || checkpoint.Decision != nil) {
		return NewValidationError("等待权重阶段不能包含尚未生效的阶段观测")
	}
	if checkpoint.Phase == CanaryStageCompleted &&
		(checkpoint.CandidateWeightPercent != 100 || checkpoint.Observation == nil || checkpoint.Decision == nil || checkpoint.Decision.Analysis.Status != "awaiting_confirmation") {
		return NewValidationError("金丝雀完成阶段必须包含 100%% 候选流量的完整健康观测")
	}
	if checkpoint.Observation != nil && checkpoint.Decision == nil {
		return NewValidationError("已保存金丝雀观测时必须同时保存判定")
	}
	return nil
}

func validateCanaryCheckpointAgainstPlan(checkpoint CanaryStageCheckpoint, plan DeliveryPlan) error {
	if plan.Strategy != "gateway_canary" || plan.Snapshot.Strategy != "gateway_canary" ||
		plan.Snapshot.Policy.Strategy != "gateway_canary" || plan.Snapshot.Policy.CanaryAnalysis == nil {
		return NewValidationError("发布计划没有可执行的金丝雀阶段策略")
	}
	stages := plan.Snapshot.Policy.CanaryAnalysis.Stages
	if checkpoint.StageIndex >= len(stages) {
		return NewValidationError("金丝雀阶段超出发布计划中的阶段范围")
	}
	stage := stages[checkpoint.StageIndex]
	if checkpoint.CandidateWeightPercent != stage.CandidateWeightPercent || checkpoint.ObservationDurationSeconds != stage.ObservationDurationSeconds {
		return NewValidationError("金丝雀阶段比例或观察时长与不可变发布计划不一致")
	}
	if checkpoint.Decision != nil {
		stableDigest := NormalizeDigest(plan.Snapshot.Target.StableImageDigest)
		candidateDigest := ""
		if plan.Snapshot.Artifact.Digest != nil {
			candidateDigest = NormalizeDigest(*plan.Snapshot.Artifact.Digest)
		}
		if candidateDigest == "" {
			candidateDigest = imageReferenceDigest(plan.Snapshot.Artifact.Image)
		}
		if stableDigest == "" || candidateDigest == "" {
			return NewValidationError("金丝雀计划缺少稳定版或候选版不可变 digest")
		}
		versionMismatch := NormalizeDigest(checkpoint.Observation.StableImageDigest) != stableDigest ||
			NormalizeDigest(checkpoint.Observation.CandidateImageDigest) != candidateDigest
		evaluatedAt := checkpoint.Observation.ObservedAt // Schema v1 did not persist decision time.
		if checkpoint.SchemaVersion >= 2 {
			evaluatedAt = checkpoint.Decision.EvaluatedAt
		}
		analysis, err := EvaluateCanaryStageObservation(
			*plan.Snapshot.Policy.CanaryAnalysis,
			checkpoint.StageIndex,
			*checkpoint.StageStartedAt,
			*checkpoint.EffectiveWeights,
			*checkpoint.Observation,
			evaluatedAt,
		)
		if versionMismatch {
			analysis.Analysis = CanaryAnalysisDecision{Status: "pause", Reason: "version_identity_mismatch"}
		}
		expectedDecision := *checkpoint.Decision
		if checkpoint.SchemaVersion == 1 {
			// Schema v1 predates evaluated_at. Its only durable evaluation time
			// was the provider observation timestamp.
			expectedDecision.EvaluatedAt = checkpoint.Observation.ObservedAt
		}
		if err != nil || !reflect.DeepEqual(analysis, expectedDecision) {
			return NewValidationError("金丝雀判定无法由计划阈值、阶段权重和观测数据复算")
		}
	}
	return nil
}

// RecordCanaryStageCheckpoint appends a typed checkpoint only to a canary run.
// Real adapters must first journal external write intent and perform an
// effective-state readback before recording an observing checkpoint.
func (s *Store) RecordCanaryStageCheckpoint(logID int64, expectedVersion int64, eventKey, runStatus, detail string, checkpoint CanaryStageCheckpoint) (DeliveryRun, bool, error) {
	run, err := s.DeliveryRunForDeployLog(logID)
	if err != nil {
		return DeliveryRun{}, false, err
	}
	if run.Strategy != "gateway_canary" {
		return DeliveryRun{}, false, NewValidationError("结构化金丝雀阶段快照只能用于网关金丝雀运行")
	}
	plan, err := s.FindDeliveryPlan(run.DeliveryPlanID)
	if err != nil {
		return DeliveryRun{}, false, err
	}
	if err := validateCanaryCheckpointAgainstPlan(checkpoint, plan); err != nil {
		return DeliveryRun{}, false, err
	}
	event, err := CanaryStageCheckpointEvent(eventKey, runStatus, detail, checkpoint)
	if err != nil {
		return DeliveryRun{}, false, err
	}
	events, err := s.DeliveryRunEvents(run.ID)
	if err != nil {
		return DeliveryRun{}, false, err
	}
	for _, existing := range events {
		if existing.EventKey != event.EventKey {
			continue
		}
		if !sameDeliveryRunEventContent(existing, event) {
			return run, true, fmt.Errorf("%w: 金丝雀阶段事件幂等键已用于不同快照", ErrConflict)
		}
		return run, true, nil
	}
	previous, err := LatestCanaryStageCheckpoint(events)
	if err != nil {
		return DeliveryRun{}, false, err
	}
	if previous != nil {
		if err := validateCanaryCheckpointAgainstPlan(*previous, plan); err != nil {
			return DeliveryRun{}, false, err
		}
	}
	if err := validateCanaryCheckpointTransition(previous, checkpoint, runStatus, plan); err != nil {
		return DeliveryRun{}, false, err
	}
	if checkpoint.EffectiveWeights != nil {
		actions, err := s.ExecutionActions(logID)
		if err != nil {
			return DeliveryRun{}, false, err
		}
		if err := validateCanaryCheckpointActionEvidence(checkpoint, actions); err != nil {
			return DeliveryRun{}, false, err
		}
	}
	log, err := s.FindLog(logID)
	if err != nil {
		return DeliveryRun{}, false, err
	}
	log.CurrentStep = strPtr(event.Stage)
	setFinishedAt := func() {
		finishedAt := time.Now().UTC()
		log.FinishedAt = &finishedAt
	}
	switch runStatus {
	case "running", "paused":
		log.Status = "running"
		log.FinishedAt = nil
	case "success":
		log.Status = "success"
		setFinishedAt()
	case "failed", "cancelled":
		log.Status = "failed"
		setFinishedAt()
	case "interrupted":
		log.Status = "interrupted"
		setFinishedAt()
	}
	return s.TransitionDeliveryRun(log, expectedVersion, event)
}

func validateCanaryCheckpointTransition(previous *CanaryStageCheckpoint, next CanaryStageCheckpoint, runStatus string, plan DeliveryPlan) error {
	if previous == nil {
		if next.StageIndex != 0 || next.Phase != CanaryStageAwaitingWeights || runStatus != "running" {
			return NewValidationError("金丝雀运行必须从第一阶段的权重关口开始")
		}
		return nil
	}
	if next.StageIndex < previous.StageIndex {
		return NewValidationError("金丝雀阶段不能回退到较早阶段")
	}
	confirmationUnchanged := sameCanaryStageConfirmation(*previous, next)
	sameStage := next.StageIndex == previous.StageIndex
	allowed := false
	switch previous.Phase {
	case CanaryStageAwaitingWeights:
		allowed = sameStage && confirmationUnchanged && (next.Phase == CanaryStageObserving || next.Phase == CanaryStageNeedsAttention)
	case CanaryStageObserving:
		allowed = sameStage && confirmationUnchanged && (next.Phase == CanaryStageAwaitingConfirm || next.Phase == CanaryStageRestoreRecommended || next.Phase == CanaryStageNeedsAttention)
	case CanaryStageNeedsAttention:
		if previous.EffectiveWeights != nil && previous.StageStartedAt != nil {
			allowed = sameStage && confirmationUnchanged && (next.Phase == CanaryStageAwaitingConfirm || next.Phase == CanaryStageRestoreRecommended || next.Phase == CanaryStageNeedsAttention)
		} else {
			allowed = sameStage && confirmationUnchanged && (next.Phase == CanaryStageObserving || next.Phase == CanaryStageNeedsAttention)
		}
	case CanaryStageAwaitingConfirm:
		if next.StageIndex == previous.StageIndex+1 && next.Phase == CanaryStageAwaitingWeights && runStatus == "running" {
			allowed = validCanaryStageConfirmation(next, previous.StageIndex, previous)
		}
		if sameStage && next.Phase == CanaryStageCompleted && runStatus == "success" &&
			previous.CandidateWeightPercent == 100 && len(plan.Snapshot.Policy.CanaryAnalysis.Stages) == previous.StageIndex+1 {
			allowed = validCanaryStageConfirmation(next, previous.StageIndex, previous)
		}
	case CanaryStageRestoreRecommended, CanaryStageCompleted, CanaryStagePaused:
		allowed = false
	}
	if !allowed {
		return NewValidationError("金丝雀阶段状态不能从 %s 转换为 %s", previous.Phase, next.Phase)
	}
	if next.Phase == CanaryStageAwaitingWeights {
		stages := plan.Snapshot.Policy.CanaryAnalysis.Stages
		if next.StageIndex >= len(stages) || next.CandidateWeightPercent != stages[next.StageIndex].CandidateWeightPercent ||
			next.ObservationDurationSeconds != stages[next.StageIndex].ObservationDurationSeconds {
			return NewValidationError("下一阶段与不可变计划不一致")
		}
	}
	return nil
}

func sameCanaryStageConfirmation(previous, next CanaryStageCheckpoint) bool {
	if previous.ConfirmedStageIndex == nil || next.ConfirmedStageIndex == nil {
		return previous.ConfirmedStageIndex == nil && next.ConfirmedStageIndex == nil &&
			previous.ConfirmedBy == next.ConfirmedBy && previous.ConfirmedAt == nil && next.ConfirmedAt == nil
	}
	return *previous.ConfirmedStageIndex == *next.ConfirmedStageIndex && previous.ConfirmedBy == next.ConfirmedBy &&
		previous.ConfirmedAt != nil && next.ConfirmedAt != nil && previous.ConfirmedAt.Equal(*next.ConfirmedAt)
}

func validCanaryStageConfirmation(next CanaryStageCheckpoint, confirmedStage int, previous *CanaryStageCheckpoint) bool {
	if next.ConfirmedStageIndex == nil || *next.ConfirmedStageIndex != confirmedStage || next.ConfirmedBy == "" || next.ConfirmedAt == nil {
		return false
	}
	if previous != nil && previous.Observation != nil && next.ConfirmedAt.Before(previous.Observation.ObservedAt) {
		return false
	}
	return true
}

func validateCanaryCheckpointActionEvidence(checkpoint CanaryStageCheckpoint, actions []ExecutionAction) error {
	key := fmt.Sprintf("canary-gateway-stage-%d", checkpoint.StageIndex+1)
	for _, action := range actions {
		if action.ActionKey != key {
			continue
		}
		if action.ActionKind != canaryGatewayActionKind || action.Status != ExecutionActionApplied {
			return NewValidationError("金丝雀有效权重缺少已确认的网关动作")
		}
		var intent canaryGatewayActionIntent
		var observed canaryGatewayActionObservation
		if json.Unmarshal(action.Intent, &intent) != nil || json.Unmarshal(action.Observed, &observed) != nil ||
			intent.SchemaVersion != 1 || intent.StageIndex != checkpoint.StageIndex ||
			intent.DesiredWeights != *checkpoint.EffectiveWeights ||
			!validCanaryGatewayRouteRef(intent.Route) ||
			observed.SchemaVersion != 1 || observed.OutcomeCode != "readback_confirmed" ||
			observed.RouteState == nil || observed.StageStartedAt == nil ||
			!validCanaryGatewayReadback(intent.Route, *observed.RouteState) ||
			observed.RouteState.Weights != *checkpoint.EffectiveWeights ||
			checkpoint.StageStartedAt == nil || !observed.StageStartedAt.Equal(*checkpoint.StageStartedAt) {
			return NewValidationError("金丝雀有效权重与已确认网关回读不一致")
		}
		return nil
	}
	return NewValidationError("金丝雀阶段不能在缺少持久化网关回读时记录有效权重")
}

func (s *Store) LatestCanaryStageCheckpoint(runID int64) (*CanaryStageCheckpoint, error) {
	run, err := s.DeliveryRun(runID)
	if err != nil {
		return nil, err
	}
	if run.Strategy != "gateway_canary" {
		return nil, NewValidationError("该发布运行不是网关金丝雀策略")
	}
	plan, err := s.FindDeliveryPlan(run.DeliveryPlanID)
	if err != nil {
		return nil, err
	}
	events, err := s.DeliveryRunEvents(runID)
	if err != nil {
		return nil, err
	}
	for _, event := range events {
		if event.EventType != "canary_checkpoint" {
			continue
		}
		checkpoint, err := DecodeCanaryStageCheckpoint(event)
		if err != nil {
			return nil, err
		}
		if err := validateCanaryCheckpointAgainstPlan(checkpoint, plan); err != nil {
			return nil, err
		}
	}
	return LatestCanaryStageCheckpoint(events)
}
