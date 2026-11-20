package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const canaryGatewayActionKind = "gateway_route_weights"

// CanaryGatewayRouteRef is an explicit, credential-free identity captured by a
// versioned policy and plan. Route ownership still must be verified by the
// adapter; callers must never infer it from an application or Deployment name.
type CanaryGatewayRouteRef struct {
	ConnectionID       int64  `json:"connection_id"`
	RouteID            string `json:"route_id"`
	StableBackendID    string `json:"stable_backend_id"`
	CandidateBackendID string `json:"candidate_backend_id"`
}

// CanaryGatewayRouteState is the adapter's normalized effective route
// readback. Revision must be suitable for conditional updates (ETag,
// resourceVersion, generation, or an equivalent provider token).
type CanaryGatewayRouteState struct {
	ConnectionID       int64                `json:"connection_id"`
	RouteID            string               `json:"route_id"`
	Revision           string               `json:"revision"`
	StableBackendID    string               `json:"stable_backend_id"`
	CandidateBackendID string               `json:"candidate_backend_id"`
	Weights            CanaryTrafficWeights `json:"weights"`
}

// CanaryGatewayAdapter must implement revision-conditional writes and
// idempotency-key handling. ApplyCanaryStageWeights always reads the effective
// route after a write, even when SetCanaryRouteWeights returns an error.
type CanaryGatewayAdapter interface {
	ReadCanaryRoute(context.Context, CanaryGatewayRouteRef) (CanaryGatewayRouteState, error)
	SetCanaryRouteWeights(context.Context, CanaryGatewayRouteRef, string, CanaryTrafficWeights, string) error
}

type CanaryGatewayWeightResult struct {
	Status           string                `json:"status"`
	StageIndex       int                   `json:"stage_index"`
	OutcomeCode      string                `json:"outcome_code,omitempty"`
	EffectiveWeights *CanaryTrafficWeights `json:"effective_weights,omitempty"`
}

type canaryGatewayActionIntent struct {
	SchemaVersion    int                   `json:"schema_version"`
	StageIndex       int                   `json:"stage_index"`
	Route            CanaryGatewayRouteRef `json:"route"`
	ExpectedRevision string                `json:"expected_revision"`
	ExpectedWeights  CanaryTrafficWeights  `json:"expected_weights"`
	DesiredWeights   CanaryTrafficWeights  `json:"desired_weights"`
	IdempotencyKey   string                `json:"idempotency_key"`
}

type canaryGatewayActionObservation struct {
	SchemaVersion  int                      `json:"schema_version"`
	RouteState     *CanaryGatewayRouteState `json:"route_state,omitempty"`
	StageStartedAt *time.Time               `json:"stage_started_at,omitempty"`
	OutcomeCode    string                   `json:"outcome_code"`
}

// ApplyCanaryStageWeights applies the stage already persisted as
// awaiting_weights. It is a service-layer execution primitive only: no
// production API or worker calls it until a real, configured adapter and its
// gateway/metrics admission checks are wired. Run admission remains blocked.
func (s *Store) ApplyCanaryStageWeights(ctx context.Context, logID int64, route CanaryGatewayRouteRef, adapter CanaryGatewayAdapter) (CanaryGatewayWeightResult, error) {
	if ctx == nil || adapter == nil || logID <= 0 || !validCanaryGatewayRouteRef(route) {
		return CanaryGatewayWeightResult{}, NewValidationError("金丝雀网关路由或适配器无效")
	}
	run, err := s.DeliveryRunForDeployLog(logID)
	if err != nil {
		return CanaryGatewayWeightResult{}, err
	}
	if run.Strategy != "gateway_canary" || run.Status != "running" {
		return CanaryGatewayWeightResult{}, NewValidationError("只有运行中的网关金丝雀可以调整阶段权重")
	}
	log, err := s.FindLog(logID)
	if err != nil {
		return CanaryGatewayWeightResult{}, err
	}
	if log.Status != "running" {
		return CanaryGatewayWeightResult{}, NewValidationError("部署任务不在运行状态，不能调整金丝雀权重")
	}
	plan, err := s.FindDeliveryPlan(run.DeliveryPlanID)
	if err != nil {
		return CanaryGatewayWeightResult{}, err
	}
	if plan.Strategy != "gateway_canary" || plan.Snapshot.Policy.CanaryAnalysis == nil {
		return CanaryGatewayWeightResult{}, NewValidationError("不可变发布计划缺少金丝雀阶段策略")
	}
	if plan.Snapshot.Policy.CanaryAnalysis.GatewayRoute == nil || *plan.Snapshot.Policy.CanaryAnalysis.GatewayRoute != route {
		return CanaryGatewayWeightResult{}, NewValidationError("请求的网关连接、路由及稳定/候选后端身份与不可变发布计划不一致")
	}
	events, err := s.DeliveryRunEvents(run.ID)
	if err != nil {
		return CanaryGatewayWeightResult{}, err
	}
	checkpoint, err := LatestCanaryStageCheckpoint(events)
	if err != nil {
		return CanaryGatewayWeightResult{}, err
	}
	if checkpoint == nil {
		return CanaryGatewayWeightResult{}, NewValidationError("金丝雀阶段尚未到达权重调整关口")
	}
	if err := validateCanaryCheckpointAgainstPlan(*checkpoint, plan); err != nil {
		return CanaryGatewayWeightResult{}, err
	}
	stageName := fmt.Sprintf("canary_stage_%d", checkpoint.StageIndex+1)
	if run.CurrentStage != stageName || log.CurrentStep == nil || *log.CurrentStep != stageName {
		return CanaryGatewayWeightResult{}, NewValidationError("运行阶段与金丝雀权重关口不一致")
	}
	if checkpoint.Phase != CanaryStageAwaitingWeights && checkpoint.Phase != CanaryStageNeedsAttention {
		weights := checkpoint.EffectiveWeights
		return CanaryGatewayWeightResult{Status: checkpoint.Phase, StageIndex: checkpoint.StageIndex, EffectiveWeights: weights}, nil
	}
	if checkpoint.Phase == CanaryStageNeedsAttention && checkpoint.EffectiveWeights != nil {
		return CanaryGatewayWeightResult{}, NewValidationError("阶段已有有效权重和观测证据；请使用指标核对或恢复控制，不能重置观察阶段")
	}
	policyStages := plan.Snapshot.Policy.CanaryAnalysis.Stages
	if checkpoint.StageIndex >= len(policyStages) {
		return CanaryGatewayWeightResult{}, NewValidationError("金丝雀阶段超出不可变发布计划")
	}
	desired := canaryWeights(policyStages[checkpoint.StageIndex].CandidateWeightPercent)
	expected := CanaryTrafficWeights{StableWeightPercent: 100, CandidateWeightPercent: 0}
	if checkpoint.StageIndex > 0 {
		expected = canaryWeights(policyStages[checkpoint.StageIndex-1].CandidateWeightPercent)
	}
	result := CanaryGatewayWeightResult{Status: "needs_attention", StageIndex: checkpoint.StageIndex}
	actionKey := fmt.Sprintf("canary-gateway-stage-%d", checkpoint.StageIndex+1)
	idempotencyKey := fmt.Sprintf("canary:%d:stage:%d", logID, checkpoint.StageIndex+1)

	actions, err := s.ExecutionActions(logID)
	if err != nil {
		return result, err
	}
	if err := validateCanaryRunGatewayRoute(actions, route); err != nil {
		return result, err
	}
	var action *ExecutionAction
	for i := range actions {
		if actions[i].ActionKey == actionKey {
			action = &actions[i]
			break
		}
	}
	if action != nil {
		var intent canaryGatewayActionIntent
		if err := json.Unmarshal(action.Intent, &intent); err != nil || intent.SchemaVersion != 1 ||
			intent.StageIndex != checkpoint.StageIndex || intent.Route != route ||
			intent.ExpectedWeights != expected || intent.DesiredWeights != desired || intent.IdempotencyKey != idempotencyKey {
			return result, NewValidationError("已持久化的网关动作意图与当前阶段不一致")
		}
		if action.ActionKind != canaryGatewayActionKind {
			return result, NewValidationError("网关动作类型与阶段不一致")
		}
		if action.Status == ExecutionActionApplied {
			var observed canaryGatewayActionObservation
			if err := json.Unmarshal(action.Observed, &observed); err != nil || observed.SchemaVersion != 1 ||
				observed.OutcomeCode != "readback_confirmed" || observed.RouteState == nil || observed.StageStartedAt == nil ||
				!validCanaryGatewayReadback(route, *observed.RouteState) || observed.RouteState.Weights != desired {
				return result, NewValidationError("已完成的网关动作缺少有效权重回读")
			}
			return s.recordCanaryGatewayObserving(run, logID, *checkpoint, *observed.RouteState, *observed.StageStartedAt)
		}
		if action.Status == ExecutionActionNotApplied {
			result.OutcomeCode = action.OutcomeCode
			return result, nil
		}
		if action.Status != ExecutionActionIntent && action.Status != ExecutionActionUnknown {
			return result, NewValidationError("网关动作记录状态无效")
		}
		return s.reconcileCanaryGatewayAction(ctx, run, logID, *checkpoint, route, adapter, actionKey, idempotencyKey, intent, action.Status)
	}

	state, readErr := adapter.ReadCanaryRoute(ctx, route)
	if readErr != nil {
		return s.recordCanaryGatewayAttention(run, logID, *checkpoint, "route_read_failed")
	}
	if !validCanaryGatewayReadback(route, state) {
		return s.recordCanaryGatewayAttention(run, logID, *checkpoint, "route_identity_invalid")
	}
	if state.Weights == desired {
		// Desired state without a journaled write cannot be attributed to this run.
		return s.recordCanaryGatewayAttention(run, logID, *checkpoint, "desired_state_without_action")
	}
	if state.Weights != expected {
		return s.recordCanaryGatewayAttention(run, logID, *checkpoint, "route_weights_drifted")
	}
	intent := canaryGatewayActionIntent{
		SchemaVersion: 1, StageIndex: checkpoint.StageIndex, Route: route,
		ExpectedRevision: state.Revision, ExpectedWeights: expected, DesiredWeights: desired,
		IdempotencyKey: idempotencyKey,
	}
	intentJSON, err := json.Marshal(intent)
	if err != nil {
		return result, err
	}
	saved, _, err := s.BeginExecutionAction(ExecutionAction{
		DeployLogID: logID, ActionKey: actionKey, ActionKind: canaryGatewayActionKind,
		Status: ExecutionActionIntent, Intent: intentJSON, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return result, err
	}
	var savedIntent canaryGatewayActionIntent
	if json.Unmarshal(saved.Intent, &savedIntent) != nil || savedIntent != intent {
		return result, NewValidationError("并发写入的网关动作意图与当前路由或阶段不一致")
	}
	intent = savedIntent
	if saved.Status != ExecutionActionIntent {
		if saved.Status == ExecutionActionApplied {
			var observed canaryGatewayActionObservation
			if err := json.Unmarshal(saved.Observed, &observed); err != nil || observed.SchemaVersion != 1 ||
				observed.OutcomeCode != "readback_confirmed" || observed.RouteState == nil || observed.StageStartedAt == nil ||
				!validCanaryGatewayReadback(route, *observed.RouteState) || observed.RouteState.Weights != desired {
				return result, NewValidationError("已完成的网关动作缺少有效权重回读")
			}
			return s.recordCanaryGatewayObserving(run, logID, *checkpoint, *observed.RouteState, *observed.StageStartedAt)
		}
		return s.reconcileCanaryGatewayAction(ctx, run, logID, *checkpoint, route, adapter, actionKey, idempotencyKey, intent, saved.Status)
	}
	return s.writeAndVerifyCanaryGatewayWeights(ctx, run, logID, *checkpoint, route, adapter, actionKey, intent)
}

// validateCanaryRunGatewayRoute prevents a later stage from silently moving a
// running canary to a different connection, route, or backend pair. The caller
// separately must match the first action to the immutable plan snapshot.
func validateCanaryRunGatewayRoute(actions []ExecutionAction, requested CanaryGatewayRouteRef) error {
	for _, action := range actions {
		if action.ActionKind != canaryGatewayActionKind {
			continue
		}
		var intent canaryGatewayActionIntent
		if json.Unmarshal(action.Intent, &intent) != nil || intent.SchemaVersion != 1 || !validCanaryGatewayRouteRef(intent.Route) {
			return NewValidationError("已记录的金丝雀网关路由身份无效，不能继续切换流量")
		}
		if intent.Route != requested {
			return NewValidationError("同一金丝雀运行不能更换网关连接、路由或稳定/候选后端")
		}
	}
	return nil
}

func (s *Store) reconcileCanaryGatewayAction(ctx context.Context, run DeliveryRun, logID int64, checkpoint CanaryStageCheckpoint, route CanaryGatewayRouteRef, adapter CanaryGatewayAdapter, actionKey, idempotencyKey string, intent canaryGatewayActionIntent, actionStatus string) (CanaryGatewayWeightResult, error) {
	result := CanaryGatewayWeightResult{Status: "needs_attention", StageIndex: checkpoint.StageIndex}
	if intent.ExpectedRevision == "" {
		return s.recordCanaryGatewayAttention(run, logID, checkpoint, "write_revision_missing")
	}
	state, err := adapter.ReadCanaryRoute(ctx, route)
	if err != nil {
		return s.completeCanaryGatewayUnknown(run, logID, checkpoint, actionKey, "route_read_failed", nil)
	}
	if !validCanaryGatewayReadback(route, state) {
		return s.completeCanaryGatewayUnknown(run, logID, checkpoint, actionKey, "route_identity_invalid", &state)
	}
	if state.Weights == intent.DesiredWeights {
		return s.completeCanaryGatewayApplied(run, logID, checkpoint, actionKey, state, time.Now().UTC(), "readback_confirmed")
	}
	if state.Weights != intent.ExpectedWeights || state.Revision != intent.ExpectedRevision {
		return s.completeCanaryGatewayUnknown(run, logID, checkpoint, actionKey, "route_state_drifted", &state)
	}
	if actionStatus == ExecutionActionUnknown || actionStatus == ExecutionActionIntent {
		return s.writeAndVerifyCanaryGatewayWeights(ctx, run, logID, checkpoint, route, adapter, actionKey, intent)
	}
	return result, nil
}

func (s *Store) writeAndVerifyCanaryGatewayWeights(ctx context.Context, run DeliveryRun, logID int64, checkpoint CanaryStageCheckpoint, route CanaryGatewayRouteRef, adapter CanaryGatewayAdapter, actionKey string, intent canaryGatewayActionIntent) (CanaryGatewayWeightResult, error) {
	result := CanaryGatewayWeightResult{Status: "needs_attention", StageIndex: checkpoint.StageIndex}
	setErr := adapter.SetCanaryRouteWeights(ctx, route, intent.ExpectedRevision, intent.DesiredWeights, intent.IdempotencyKey)
	state, readErr := adapter.ReadCanaryRoute(ctx, route) // The write response is never proof of effective routing.
	if readErr != nil {
		return s.completeCanaryGatewayUnknown(run, logID, checkpoint, actionKey, "readback_failed", nil)
	}
	if !validCanaryGatewayReadback(route, state) {
		return s.completeCanaryGatewayUnknown(run, logID, checkpoint, actionKey, "readback_identity_invalid", &state)
	}
	if state.Weights == intent.DesiredWeights {
		return s.completeCanaryGatewayApplied(run, logID, checkpoint, actionKey, state, time.Now().UTC(), "readback_confirmed")
	}
	if state.Weights == intent.ExpectedWeights && state.Revision == intent.ExpectedRevision {
		outcome := "write_not_effective"
		if setErr != nil {
			outcome = "write_not_applied"
		}
		observed := canaryGatewayActionObservation{SchemaVersion: 1, RouteState: &state, OutcomeCode: outcome}
		if _, err := s.completeCanaryGatewayAction(logID, actionKey, ExecutionActionNotApplied, observed, outcome); err != nil {
			return result, err
		}
		return s.recordCanaryGatewayAttention(run, logID, checkpoint, outcome)
	}
	return s.completeCanaryGatewayUnknown(run, logID, checkpoint, actionKey, "route_state_drifted", &state)
}

func (s *Store) completeCanaryGatewayApplied(run DeliveryRun, logID int64, checkpoint CanaryStageCheckpoint, actionKey string, state CanaryGatewayRouteState, startedAt time.Time, outcome string) (CanaryGatewayWeightResult, error) {
	observed := canaryGatewayActionObservation{SchemaVersion: 1, RouteState: &state, StageStartedAt: &startedAt, OutcomeCode: outcome}
	if _, err := s.completeCanaryGatewayAction(logID, actionKey, ExecutionActionApplied, observed, outcome); err != nil {
		return CanaryGatewayWeightResult{}, err
	}
	return s.recordCanaryGatewayObserving(run, logID, checkpoint, state, startedAt)
}

func (s *Store) completeCanaryGatewayUnknown(run DeliveryRun, logID int64, checkpoint CanaryStageCheckpoint, actionKey, outcome string, state *CanaryGatewayRouteState) (CanaryGatewayWeightResult, error) {
	observed := canaryGatewayActionObservation{SchemaVersion: 1, RouteState: state, OutcomeCode: outcome}
	if _, err := s.completeCanaryGatewayAction(logID, actionKey, ExecutionActionUnknown, observed, outcome); err != nil {
		return CanaryGatewayWeightResult{}, err
	}
	return s.recordCanaryGatewayAttention(run, logID, checkpoint, outcome)
}

func (s *Store) completeCanaryGatewayAction(logID int64, actionKey, status string, observation canaryGatewayActionObservation, outcome string) (ExecutionAction, error) {
	observedJSON, err := json.Marshal(observation)
	if err != nil {
		return ExecutionAction{}, err
	}
	return s.CompleteExecutionAction(logID, actionKey, status, observedJSON, outcome)
}

func (s *Store) recordCanaryGatewayObserving(run DeliveryRun, logID int64, checkpoint CanaryStageCheckpoint, state CanaryGatewayRouteState, startedAt time.Time) (CanaryGatewayWeightResult, error) {
	weights := state.Weights
	checkpoint.Phase = CanaryStageObserving
	checkpoint.StageStartedAt = &startedAt
	checkpoint.EffectiveWeights = &weights
	checkpoint.Observation = nil
	checkpoint.Decision = nil
	eventKey := fmt.Sprintf("canary:stage-%d:weights-applied", checkpoint.StageIndex+1)
	detail := "网关有效权重已回读，开始阶段观察。"
	_, _, err := s.RecordCanaryStageCheckpoint(logID, run.Version, eventKey, "running", detail, checkpoint)
	if err != nil {
		return CanaryGatewayWeightResult{}, err
	}
	return CanaryGatewayWeightResult{Status: "observing", StageIndex: checkpoint.StageIndex, OutcomeCode: "readback_confirmed", EffectiveWeights: &weights}, nil
}

func (s *Store) recordCanaryGatewayAttention(run DeliveryRun, logID int64, checkpoint CanaryStageCheckpoint, outcome string) (CanaryGatewayWeightResult, error) {
	checkpoint.Phase = CanaryStageNeedsAttention
	checkpoint.StageStartedAt = nil
	checkpoint.EffectiveWeights = nil
	checkpoint.Observation = nil
	checkpoint.Decision = nil
	eventKey := fmt.Sprintf("canary:stage-%d:weights-attention:%s", checkpoint.StageIndex+1, outcome)
	detail := "网关权重状态无法确认；阶段未推进，请核对路由与动作回读。"
	if _, _, err := s.RecordCanaryStageCheckpoint(logID, run.Version, eventKey, "running", detail, checkpoint); err != nil {
		return CanaryGatewayWeightResult{}, err
	}
	return CanaryGatewayWeightResult{Status: "needs_attention", StageIndex: checkpoint.StageIndex, OutcomeCode: outcome}, nil
}

func validCanaryGatewayRouteRef(route CanaryGatewayRouteRef) bool {
	return route.ConnectionID > 0 && safeCanaryGatewayID(route.RouteID) &&
		safeCanaryGatewayID(route.StableBackendID) && safeCanaryGatewayID(route.CandidateBackendID) &&
		route.StableBackendID != route.CandidateBackendID
}

func safeCanaryGatewayID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\x00\r\n\t") && len(value) <= 256 && utf8ValidString(value)
}

func validCanaryGatewayReadback(ref CanaryGatewayRouteRef, state CanaryGatewayRouteState) bool {
	return state.ConnectionID == ref.ConnectionID && state.RouteID == ref.RouteID && state.Revision != "" && len(state.Revision) <= 256 &&
		state.StableBackendID == ref.StableBackendID && state.CandidateBackendID == ref.CandidateBackendID &&
		state.Weights.StableWeightPercent >= 0 && state.Weights.CandidateWeightPercent >= 0 &&
		state.Weights.StableWeightPercent <= 100 && state.Weights.CandidateWeightPercent <= 100 &&
		state.Weights.StableWeightPercent+state.Weights.CandidateWeightPercent == 100
}

func canaryWeights(candidatePercent int) CanaryTrafficWeights {
	return CanaryTrafficWeights{StableWeightPercent: 100 - candidatePercent, CandidateWeightPercent: candidatePercent}
}
