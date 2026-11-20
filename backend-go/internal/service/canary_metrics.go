package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CanaryMetricsQuery gives a provider enough fixed, credential-free context
// to query one stage without letting it choose a route or version identity.
type CanaryMetricsQuery struct {
	PlanID                    int64
	RunID                     int64
	StageIndex                int
	Route                     CanaryGatewayRouteRef
	StableImageDigest         string
	CandidateImageDigest      string
	WindowStartAfter          time.Time
	MinimumWindowSeconds      int64
	MinimumRequestsPerVersion int64
	MaximumObservationAge     time.Duration
}

// CanaryMetricsAdapter must return real request metrics separated by the
// pinned stable/candidate digests for the named gateway backends. The caller
// validates the window, samples, health values and image identities again.
type CanaryMetricsAdapter interface {
	ObserveCanaryVersions(context.Context, CanaryMetricsQuery) (CanaryMetricObservation, error)
}

type CanaryStageObservationResult struct {
	Status     string               `json:"status"`
	StageIndex int                  `json:"stage_index"`
	Outcome    string               `json:"outcome,omitempty"`
	Decision   *CanaryStageDecision `json:"decision,omitempty"`
}

// ObserveCanaryStage closes the durable boundary between effective gateway
// weights and stage analysis. It reads the route both before and after the
// metrics query so an external route edit cannot be hidden by a healthy sample.
// Production orchestration does not call it until a real provider adapter and
// the canary admission checks are configured.
func (s *Store) ObserveCanaryStage(
	ctx context.Context,
	logID int64,
	eventKey string,
	gateway CanaryGatewayAdapter,
	metrics CanaryMetricsAdapter,
) (CanaryStageObservationResult, error) {
	return s.observeCanaryStageAt(ctx, logID, eventKey, gateway, metrics, time.Now().UTC())
}

func (s *Store) observeCanaryStageAt(
	ctx context.Context,
	logID int64,
	eventKey string,
	gateway CanaryGatewayAdapter,
	metrics CanaryMetricsAdapter,
	now time.Time,
) (CanaryStageObservationResult, error) {
	operationStartedAt := time.Now()
	if s == nil || ctx == nil || logID <= 0 || gateway == nil || metrics == nil ||
		eventKey == "" || len(eventKey) > 160 || !utf8ValidString(eventKey) || now.IsZero() {
		return CanaryStageObservationResult{}, NewValidationError("金丝雀观测请求或适配器无效")
	}
	run, err := s.DeliveryRunForDeployLog(logID)
	if err != nil {
		return CanaryStageObservationResult{}, err
	}
	if run.Strategy != "gateway_canary" || run.Status != "running" {
		return CanaryStageObservationResult{}, NewValidationError("只有运行中的网关金丝雀可以采集阶段观测")
	}
	plan, err := s.FindDeliveryPlan(run.DeliveryPlanID)
	if err != nil {
		return CanaryStageObservationResult{}, err
	}
	if plan.Strategy != "gateway_canary" || plan.Snapshot.Policy.CanaryAnalysis == nil {
		return CanaryStageObservationResult{}, NewValidationError("不可变发布计划缺少金丝雀分析策略")
	}
	events, err := s.DeliveryRunEvents(run.ID)
	if err != nil {
		return CanaryStageObservationResult{}, err
	}
	for _, event := range events {
		if event.EventKey != eventKey {
			continue
		}
		if event.EventType != "canary_checkpoint" || !strings.HasPrefix(event.Detail, "source=canary_metrics;") {
			return CanaryStageObservationResult{}, newConflict("该事件键已用于其他发布阶段动作")
		}
		checkpoint, decodeErr := DecodeCanaryStageCheckpoint(event)
		if decodeErr != nil {
			return CanaryStageObservationResult{}, decodeErr
		}
		if validateErr := validateCanaryCheckpointAgainstPlan(checkpoint, plan); validateErr != nil {
			return CanaryStageObservationResult{}, validateErr
		}
		return canaryObservationResult(checkpoint), nil
	}
	checkpoint, err := LatestCanaryStageCheckpoint(events)
	if err != nil {
		return CanaryStageObservationResult{}, err
	}
	if checkpoint == nil || (checkpoint.Phase != CanaryStageObserving && checkpoint.Phase != CanaryStageNeedsAttention) ||
		checkpoint.StageStartedAt == nil || checkpoint.EffectiveWeights == nil {
		return CanaryStageObservationResult{}, NewValidationError("当前金丝雀阶段没有可采集指标的有效权重回读")
	}
	stageName := fmt.Sprintf("canary_stage_%d", checkpoint.StageIndex+1)
	log, err := s.FindLog(logID)
	if err != nil {
		return CanaryStageObservationResult{}, err
	}
	if run.CurrentStage != stageName || log.CurrentStep == nil || *log.CurrentStep != stageName || log.Status != "running" {
		return CanaryStageObservationResult{}, NewValidationError("运行阶段与金丝雀指标观测关口不一致")
	}
	policy := *plan.Snapshot.Policy.CanaryAnalysis
	if checkpoint.StageIndex >= len(policy.Stages) {
		return CanaryStageObservationResult{}, NewValidationError("金丝雀阶段超出不可变计划")
	}
	stage := policy.Stages[checkpoint.StageIndex]
	if now.Before(checkpoint.StageStartedAt.Add(time.Duration(stage.ObservationDurationSeconds) * time.Second)) {
		return CanaryStageObservationResult{Status: checkpoint.Phase, StageIndex: checkpoint.StageIndex, Outcome: "observation_window_incomplete"}, nil
	}
	actions, err := s.ExecutionActions(logID)
	if err != nil {
		return CanaryStageObservationResult{}, err
	}
	if err := validateCanaryCheckpointActionEvidence(*checkpoint, actions); err != nil {
		return s.recordCanaryObservationAttention(run, logID, eventKey, *checkpoint, "gateway_action_evidence_invalid")
	}
	stageActionKey := fmt.Sprintf("canary-gateway-stage-%d", checkpoint.StageIndex+1)
	var routeRef CanaryGatewayRouteRef
	var appliedState CanaryGatewayRouteState
	for _, action := range actions {
		if action.ActionKey != stageActionKey || action.ActionKind != canaryGatewayActionKind || action.Status != ExecutionActionApplied {
			continue
		}
		var intent canaryGatewayActionIntent
		var observed canaryGatewayActionObservation
		if json.Unmarshal(action.Intent, &intent) != nil || json.Unmarshal(action.Observed, &observed) != nil ||
			observed.RouteState == nil || !validCanaryGatewayRouteRef(intent.Route) || !validCanaryGatewayReadback(intent.Route, *observed.RouteState) {
			return s.recordCanaryObservationAttention(run, logID, eventKey, *checkpoint, "gateway_action_evidence_invalid")
		}
		routeRef, appliedState = intent.Route, *observed.RouteState
		break
	}
	if !validCanaryGatewayRouteRef(routeRef) {
		return s.recordCanaryObservationAttention(run, logID, eventKey, *checkpoint, "gateway_action_evidence_missing")
	}
	before, readErr := gateway.ReadCanaryRoute(ctx, routeRef)
	if readErr != nil {
		return s.recordCanaryObservationAttention(run, logID, eventKey, *checkpoint, "gateway_read_failed")
	}
	if !validCanaryGatewayReadback(routeRef, before) || before.Weights != *checkpoint.EffectiveWeights || before.Revision != appliedState.Revision {
		return s.recordCanaryObservationAttention(run, logID, eventKey, *checkpoint, "gateway_state_drifted")
	}
	stableDigest := NormalizeDigest(plan.Snapshot.Target.StableImageDigest)
	candidateDigest := ""
	if plan.Snapshot.Artifact.Digest != nil {
		candidateDigest = NormalizeDigest(*plan.Snapshot.Artifact.Digest)
	}
	if candidateDigest == "" {
		candidateDigest = imageReferenceDigest(plan.Snapshot.Artifact.Image)
	}
	if stableDigest == "" || candidateDigest == "" || stableDigest == candidateDigest {
		return s.recordCanaryObservationAttention(run, logID, eventKey, *checkpoint, "planned_image_identity_invalid")
	}
	query := CanaryMetricsQuery{
		PlanID: plan.ID, RunID: run.ID, StageIndex: checkpoint.StageIndex, Route: routeRef,
		StableImageDigest: stableDigest, CandidateImageDigest: candidateDigest,
		WindowStartAfter: *checkpoint.StageStartedAt, MinimumWindowSeconds: stage.ObservationDurationSeconds,
		MinimumRequestsPerVersion: policy.MinRequests,
		MaximumObservationAge:     time.Duration(policy.MaxObservationAgeSeconds) * time.Second,
	}
	observation, metricsErr := metrics.ObserveCanaryVersions(ctx, query)
	if metricsErr != nil {
		return s.recordCanaryObservationAttention(run, logID, eventKey, *checkpoint, "metrics_read_failed")
	}
	evaluationTime := now.Add(time.Since(operationStartedAt))
	if err := validateCanaryObservationPayload(observation, *checkpoint.StageStartedAt, stage.ObservationDurationSeconds); err != nil {
		if errors.Is(err, errCanaryObservationWindowIncomplete) {
			return CanaryStageObservationResult{Status: checkpoint.Phase, StageIndex: checkpoint.StageIndex, Outcome: "observation_window_incomplete"}, nil
		}
		return s.recordCanaryObservationAttention(run, logID, eventKey, *checkpoint, "metrics_payload_invalid")
	}
	if observation.ObservedAt.After(evaluationTime) {
		return s.recordCanaryObservationAttention(run, logID, eventKey, *checkpoint, "metrics_timestamp_in_future")
	}
	after, readErr := gateway.ReadCanaryRoute(ctx, routeRef)
	if readErr != nil {
		return s.recordCanaryObservationAttention(run, logID, eventKey, *checkpoint, "gateway_read_failed_after_metrics")
	}
	if !validCanaryGatewayReadback(routeRef, after) || after.Weights != before.Weights || after.Revision != before.Revision {
		return s.recordCanaryObservationAttention(run, logID, eventKey, *checkpoint, "gateway_changed_during_observation")
	}
	evaluationTime = now.Add(time.Since(operationStartedAt))
	decision, err := EvaluateCanaryStageObservation(policy, checkpoint.StageIndex, *checkpoint.StageStartedAt, *checkpoint.EffectiveWeights, observation, evaluationTime)
	if err != nil {
		return CanaryStageObservationResult{}, err
	}
	if NormalizeDigest(observation.StableImageDigest) != stableDigest || NormalizeDigest(observation.CandidateImageDigest) != candidateDigest {
		decision.Analysis = CanaryAnalysisDecision{Status: "pause", Reason: "version_identity_mismatch"}
	}
	next := *checkpoint
	next.Observation = &observation
	next.Decision = &decision
	switch decision.Analysis.Status {
	case "awaiting_confirmation":
		next.Phase = CanaryStageAwaitingConfirm
	case "restore_recommended":
		next.Phase = CanaryStageRestoreRecommended
	default:
		next.Phase = CanaryStageNeedsAttention
	}
	detail := "source=canary_metrics;result=" + decision.Analysis.Reason + ";阶段指标已按固定 digest 和观察窗口复核。"
	if _, _, err := s.RecordCanaryStageCheckpoint(logID, run.Version, eventKey, "running", detail, next); err != nil {
		return CanaryStageObservationResult{}, err
	}
	return CanaryStageObservationResult{Status: next.Phase, StageIndex: next.StageIndex, Outcome: decision.Analysis.Reason, Decision: &decision}, nil
}

func validateCanaryObservationPayload(observation CanaryMetricObservation, stageStartedAt time.Time, minimumWindowSeconds int64) error {
	if NormalizeDigest(observation.StableImageDigest) == "" || NormalizeDigest(observation.CandidateImageDigest) == "" ||
		NormalizeDigest(observation.StableImageDigest) == NormalizeDigest(observation.CandidateImageDigest) ||
		NormalizeDigest(observation.Stable.ImageDigest) != NormalizeDigest(observation.StableImageDigest) ||
		NormalizeDigest(observation.Candidate.ImageDigest) != NormalizeDigest(observation.CandidateImageDigest) ||
		validateCanaryVersionMetrics(observation.Stable) != nil || validateCanaryVersionMetrics(observation.Candidate) != nil ||
		observation.WindowStart.IsZero() || observation.WindowEnd.IsZero() || observation.ObservedAt.IsZero() ||
		!observation.WindowStart.Before(observation.WindowEnd) || observation.WindowEnd.After(observation.ObservedAt) ||
		observation.WindowStart.Before(stageStartedAt) {
		return NewValidationError("金丝雀指标适配器返回的版本身份或时间窗口无效")
	}
	minimumWindow := time.Duration(minimumWindowSeconds) * time.Second
	if observation.WindowEnd.Sub(observation.WindowStart) < minimumWindow || observation.WindowEnd.Before(stageStartedAt.Add(minimumWindow)) {
		return errCanaryObservationWindowIncomplete
	}
	return nil
}

var errCanaryObservationWindowIncomplete = fmt.Errorf("金丝雀指标观察窗口尚未完成")

func (s *Store) recordCanaryObservationAttention(
	run DeliveryRun,
	logID int64,
	eventKey string,
	checkpoint CanaryStageCheckpoint,
	outcome string,
) (CanaryStageObservationResult, error) {
	checkpoint.Phase = CanaryStageNeedsAttention
	if checkpoint.Observation == nil {
		checkpoint.Decision = nil
	}
	detail := "source=canary_metrics;result=" + outcome + ";指标或网关状态无法核实，阶段未推进。"
	if _, _, err := s.RecordCanaryStageCheckpoint(logID, run.Version, eventKey, "running", detail, checkpoint); err != nil {
		return CanaryStageObservationResult{}, err
	}
	return CanaryStageObservationResult{Status: CanaryStageNeedsAttention, StageIndex: checkpoint.StageIndex, Outcome: outcome}, nil
}

func canaryObservationResult(checkpoint CanaryStageCheckpoint) CanaryStageObservationResult {
	result := CanaryStageObservationResult{Status: checkpoint.Phase, StageIndex: checkpoint.StageIndex}
	if checkpoint.Decision != nil {
		result.Decision = checkpoint.Decision
		result.Outcome = checkpoint.Decision.Analysis.Reason
	}
	return result
}
