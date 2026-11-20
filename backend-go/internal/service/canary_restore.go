package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const CanaryRestoreCheckpointSchemaVersion = 1

const canaryGatewayRestoreActionKey = "canary-gateway-restore"

type CanaryRestoreCheckpoint struct {
	SchemaVersion        int                   `json:"schema_version"`
	StageIndex           int                   `json:"stage_index"`
	StableImageDigest    string                `json:"stable_image_digest"`
	CandidateImageDigest string                `json:"candidate_image_digest"`
	Route                CanaryGatewayRouteRef `json:"route"`
	EffectiveWeights     CanaryTrafficWeights  `json:"effective_weights"`
	RouteRevision        string                `json:"route_revision"`
	RestoredAt           time.Time             `json:"restored_at"`
}

type CanaryRestoreResult struct {
	Status     string                   `json:"status"`
	StageIndex int                      `json:"stage_index"`
	Outcome    string                   `json:"outcome,omitempty"`
	Checkpoint *CanaryRestoreCheckpoint `json:"checkpoint,omitempty"`
}

// RestoreCanaryStageStable explicitly routes traffic back to the stable
// backend after an operator asks for recovery. The write is revision
// conditional and journaled first; a successful readback only moves the run
// to awaiting_restore_verification. It never declares the release successful
// or releases the run's resource locks.
//
// This is a service primitive only. Production routes do not call it until a
// real gateway adapter and an operator control endpoint are configured.
func (s *Store) RestoreCanaryStageStable(
	ctx context.Context,
	logID int64,
	idempotencyKey string,
	adapter CanaryGatewayAdapter,
) (CanaryRestoreResult, error) {
	if s == nil || ctx == nil || logID <= 0 || adapter == nil || !validCanaryRestoreIdempotencyKey(idempotencyKey) {
		return CanaryRestoreResult{}, NewValidationError("金丝雀恢复请求或网关适配器无效")
	}
	run, err := s.DeliveryRunForDeployLog(logID)
	if err != nil {
		return CanaryRestoreResult{}, err
	}
	if run.Strategy != "gateway_canary" || run.Status != "running" {
		return CanaryRestoreResult{}, NewValidationError("只有运行中的网关金丝雀可以恢复稳定流量")
	}
	logRow, err := s.FindLog(logID)
	if err != nil {
		return CanaryRestoreResult{}, err
	}
	if logRow.Status != "running" {
		return CanaryRestoreResult{}, NewValidationError("金丝雀任务不在运行状态，不能恢复网关流量")
	}
	plan, err := s.FindDeliveryPlan(run.DeliveryPlanID)
	if err != nil {
		return CanaryRestoreResult{}, err
	}
	if plan.Strategy != "gateway_canary" || plan.Snapshot.Policy.CanaryAnalysis == nil {
		return CanaryRestoreResult{}, NewValidationError("不可变发布计划缺少金丝雀恢复身份")
	}
	eventKey := "canary:restore:" + strings.TrimSpace(idempotencyKey)
	events, err := s.DeliveryRunEvents(run.ID)
	if err != nil {
		return CanaryRestoreResult{}, err
	}
	for _, event := range events {
		if event.EventKey != eventKey {
			continue
		}
		if event.EventType != "canary_restore" {
			return CanaryRestoreResult{}, newConflict("该幂等键已用于其他发布动作")
		}
		checkpoint, decodeErr := DecodeCanaryRestoreCheckpoint(event)
		if decodeErr != nil {
			return CanaryRestoreResult{}, decodeErr
		}
		if validationErr := validateCanaryRestoreCheckpointAgainstPlan(checkpoint, plan); validationErr != nil {
			return CanaryRestoreResult{}, validationErr
		}
		return CanaryRestoreResult{Status: "awaiting_restore_verification", StageIndex: checkpoint.StageIndex, Outcome: "stable_weights_readback_confirmed", Checkpoint: &checkpoint}, nil
	}
	for _, event := range events {
		if event.EventType == "canary_restore" {
			return CanaryRestoreResult{}, newConflict("稳定流量恢复已回读，请先完成功能验证")
		}
	}
	checkpoint, err := LatestCanaryStageCheckpoint(events)
	if err != nil {
		return CanaryRestoreResult{}, err
	}
	if checkpoint == nil {
		return CanaryRestoreResult{}, NewValidationError("当前金丝雀阶段没有可恢复的有效权重回读")
	}
	if err := validateCanaryCheckpointAgainstPlan(*checkpoint, plan); err != nil {
		return CanaryRestoreResult{}, err
	}
	stageName := fmt.Sprintf("canary_stage_%d", checkpoint.StageIndex+1)
	if run.CurrentStage != stageName && run.CurrentStage != "needs_attention" {
		return CanaryRestoreResult{}, NewValidationError("运行阶段与金丝雀恢复关口不一致")
	}
	actions, err := s.ExecutionActions(logID)
	if err != nil {
		return CanaryRestoreResult{}, err
	}
	recoveryCheckpoint, stageIntent, err := resolveCanaryRestoreBasis(events, *checkpoint, plan, actions)
	if err != nil {
		return CanaryRestoreResult{}, err
	}
	var restoreAction *ExecutionAction
	for i := range actions {
		if actions[i].ActionKey == canaryGatewayRestoreActionKey {
			restoreAction = &actions[i]
			break
		}
	}
	if restoreAction != nil {
		return s.reconcileCanaryRestore(ctx, run, plan, recoveryCheckpoint, stageIntent.Route, idempotencyKey, adapter, *restoreAction)
	}
	state, readErr := adapter.ReadCanaryRoute(ctx, stageIntent.Route)
	if readErr != nil {
		return s.recordCanaryRestoreAttention(run, logRow, eventKey, recoveryCheckpoint, "gateway_read_failed")
	}
	if !validCanaryGatewayReadback(stageIntent.Route, state) {
		return s.recordCanaryRestoreAttention(run, logRow, eventKey, recoveryCheckpoint, "gateway_identity_mismatch")
	}
	stableWeights := CanaryTrafficWeights{StableWeightPercent: 100, CandidateWeightPercent: 0}
	if state.Weights != stableWeights && state.Weights != *recoveryCheckpoint.EffectiveWeights {
		return s.recordCanaryRestoreAttention(run, logRow, eventKey, recoveryCheckpoint, "gateway_weights_drifted")
	}
	intent := canaryGatewayActionIntent{
		SchemaVersion: 1, StageIndex: recoveryCheckpoint.StageIndex, Route: stageIntent.Route,
		ExpectedRevision: state.Revision, ExpectedWeights: state.Weights, DesiredWeights: stableWeights,
		IdempotencyKey: fmt.Sprintf("canary:%d:restore", logID),
	}
	intentJSON, err := json.Marshal(intent)
	if err != nil {
		return CanaryRestoreResult{}, err
	}
	saved, _, err := s.BeginExecutionAction(ExecutionAction{
		DeployLogID: logID, ActionKey: canaryGatewayRestoreActionKey, ActionKind: canaryGatewayActionKind,
		Status: ExecutionActionIntent, Intent: intentJSON, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return CanaryRestoreResult{}, err
	}
	if saved.Status != ExecutionActionIntent {
		return s.reconcileCanaryRestore(ctx, run, plan, recoveryCheckpoint, stageIntent.Route, idempotencyKey, adapter, saved)
	}
	return s.writeCanaryRestore(ctx, run, plan, logRow, recoveryCheckpoint, eventKey, adapter, intent)
}

// resolveCanaryRestoreBasis chooses the last gateway state this run can prove
// it applied. At a stage boundary the newest checkpoint is awaiting_weights,
// so its weights are intentionally empty. It uses a durable applied action for
// that stage if the process stopped before recording its observing checkpoint;
// otherwise it falls back to the immediately preceding healthy stage.
func resolveCanaryRestoreBasis(
	events []DeliveryRunEvent,
	latest CanaryStageCheckpoint,
	plan DeliveryPlan,
	actions []ExecutionAction,
) (CanaryStageCheckpoint, canaryGatewayActionIntent, error) {
	checkpoint := latest
	if latest.Phase == CanaryStageAwaitingWeights {
		var previous *CanaryStageCheckpoint
		var newest *CanaryStageCheckpoint
		for _, event := range events {
			if event.EventType != "canary_checkpoint" {
				continue
			}
			decoded, err := DecodeCanaryStageCheckpoint(event)
			if err != nil {
				return CanaryStageCheckpoint{}, canaryGatewayActionIntent{}, err
			}
			previous = newest
			copy := decoded
			newest = &copy
		}
		if newest == nil || newest.StageIndex != latest.StageIndex || newest.Phase != latest.Phase {
			return CanaryStageCheckpoint{}, canaryGatewayActionIntent{}, NewValidationError("最新金丝雀阶段快照与事件时间线不一致")
		}
		if latest.StageIndex > 0 {
			if previous == nil || previous.StageIndex != latest.StageIndex-1 || previous.Phase != CanaryStageAwaitingConfirm ||
				!validCanaryStageConfirmation(latest, previous.StageIndex, previous) {
				return CanaryStageCheckpoint{}, canaryGatewayActionIntent{}, NewValidationError("阶段边界没有连续且已确认的上一阶段回读，不能恢复流量")
			}
			if err := validateCanaryCheckpointAgainstPlan(*previous, plan); err != nil {
				return CanaryStageCheckpoint{}, canaryGatewayActionIntent{}, err
			}
			if err := validateCanaryStageActionForRestore(*previous, actions); err != nil {
				return CanaryStageCheckpoint{}, canaryGatewayActionIntent{}, err
			}
		}

		currentActionKey := fmt.Sprintf("canary-gateway-stage-%d", latest.StageIndex+1)
		for _, action := range actions {
			if action.ActionKey != currentActionKey || action.Status != ExecutionActionApplied {
				continue
			}
			var intent canaryGatewayActionIntent
			var observation canaryGatewayActionObservation
			if action.ActionKind != canaryGatewayActionKind || json.Unmarshal(action.Intent, &intent) != nil ||
				json.Unmarshal(action.Observed, &observation) != nil {
				return CanaryStageCheckpoint{}, canaryGatewayActionIntent{}, NewValidationError("阶段边界的已应用网关动作无法核对")
			}
			if err := validateCanaryStageActionIntent(intent, latest.StageIndex, plan, action.DeployLogID); err != nil {
				return CanaryStageCheckpoint{}, canaryGatewayActionIntent{}, err
			}
			if observation.SchemaVersion != 1 || observation.OutcomeCode != "readback_confirmed" ||
				observation.RouteState == nil || observation.StageStartedAt == nil || observation.StageStartedAt.IsZero() ||
				!validCanaryGatewayReadback(intent.Route, *observation.RouteState) ||
				observation.RouteState.Weights != intent.DesiredWeights {
				return CanaryStageCheckpoint{}, canaryGatewayActionIntent{}, NewValidationError("阶段边界的已应用网关动作缺少有效权重回读")
			}
			if previous != nil {
				previousIntent, err := canaryStageActionIntent(previous.StageIndex, actions)
				if err != nil || previousIntent.Route != intent.Route ||
					validateCanaryStageActionIntent(previousIntent, previous.StageIndex, plan, action.DeployLogID) != nil {
					return CanaryStageCheckpoint{}, canaryGatewayActionIntent{}, NewValidationError("相邻金丝雀阶段的网关路由身份不一致")
				}
			}
			startedAt := *observation.StageStartedAt
			weights := intent.DesiredWeights
			checkpoint = latest
			checkpoint.Phase = CanaryStageObserving
			checkpoint.StageStartedAt = &startedAt
			checkpoint.EffectiveWeights = &weights
			if err := validateCanaryStageCheckpoint(checkpoint); err != nil {
				return CanaryStageCheckpoint{}, canaryGatewayActionIntent{}, err
			}
			if err := validateCanaryCheckpointAgainstPlan(checkpoint, plan); err != nil {
				return CanaryStageCheckpoint{}, canaryGatewayActionIntent{}, err
			}
			if err := validateCanaryStageActionForRestore(checkpoint, actions); err != nil {
				return CanaryStageCheckpoint{}, canaryGatewayActionIntent{}, err
			}
			return checkpoint, intent, nil
		}
		if latest.StageIndex == 0 {
			return CanaryStageCheckpoint{}, canaryGatewayActionIntent{}, NewValidationError("第一阶段尚无已应用的网关权重回读，不能恢复流量")
		}
		checkpoint = *previous
	}
	if checkpoint.EffectiveWeights == nil || checkpoint.StageStartedAt == nil ||
		(checkpoint.Phase != CanaryStageObserving && checkpoint.Phase != CanaryStageAwaitingConfirm &&
			checkpoint.Phase != CanaryStageRestoreRecommended && checkpoint.Phase != CanaryStageNeedsAttention) {
		return CanaryStageCheckpoint{}, canaryGatewayActionIntent{}, NewValidationError("当前金丝雀阶段没有可恢复的有效权重回读")
	}
	if err := validateCanaryCheckpointAgainstPlan(checkpoint, plan); err != nil {
		return CanaryStageCheckpoint{}, canaryGatewayActionIntent{}, err
	}
	if err := validateCanaryStageActionForRestore(checkpoint, actions); err != nil {
		return CanaryStageCheckpoint{}, canaryGatewayActionIntent{}, err
	}
	intent, err := canaryStageActionIntent(checkpoint.StageIndex, actions)
	if err != nil {
		return CanaryStageCheckpoint{}, canaryGatewayActionIntent{}, err
	}
	stageActionKey := fmt.Sprintf("canary-gateway-stage-%d", checkpoint.StageIndex+1)
	stageLogID := int64(0)
	for _, action := range actions {
		if action.ActionKey == stageActionKey {
			stageLogID = action.DeployLogID
			break
		}
	}
	if err := validateCanaryStageActionIntent(intent, checkpoint.StageIndex, plan, stageLogID); err != nil {
		return CanaryStageCheckpoint{}, canaryGatewayActionIntent{}, err
	}
	return checkpoint, intent, nil
}

func validateCanaryStageActionIntent(intent canaryGatewayActionIntent, stageIndex int, plan DeliveryPlan, logID int64) error {
	if plan.Snapshot.Policy.CanaryAnalysis == nil || stageIndex < 0 || stageIndex >= len(plan.Snapshot.Policy.CanaryAnalysis.Stages) ||
		logID <= 0 || intent.SchemaVersion != 1 || intent.StageIndex != stageIndex || !validCanaryGatewayRouteRef(intent.Route) {
		return NewValidationError("金丝雀阶段网关动作身份无效")
	}
	stages := plan.Snapshot.Policy.CanaryAnalysis.Stages
	expected := CanaryTrafficWeights{StableWeightPercent: 100, CandidateWeightPercent: 0}
	if stageIndex > 0 {
		expected = canaryWeights(stages[stageIndex-1].CandidateWeightPercent)
	}
	desired := canaryWeights(stages[stageIndex].CandidateWeightPercent)
	if intent.ExpectedRevision == "" || intent.ExpectedWeights != expected || intent.DesiredWeights != desired ||
		intent.IdempotencyKey != fmt.Sprintf("canary:%d:stage:%d", logID, stageIndex+1) {
		return NewValidationError("阶段网关动作的前置权重或目标权重与不可变计划不一致")
	}
	return nil
}

func (s *Store) reconcileCanaryRestore(
	ctx context.Context,
	run DeliveryRun,
	plan DeliveryPlan,
	checkpoint CanaryStageCheckpoint,
	route CanaryGatewayRouteRef,
	idempotencyKey string,
	adapter CanaryGatewayAdapter,
	action ExecutionAction,
) (CanaryRestoreResult, error) {
	attention := func(outcome string) (CanaryRestoreResult, error) {
		logRow, err := s.FindLog(run.DeployLogID)
		if err != nil {
			return CanaryRestoreResult{}, err
		}
		return s.recordCanaryRestoreAttention(run, logRow, "canary:restore:"+strings.TrimSpace(idempotencyKey), checkpoint, outcome)
	}
	var intent canaryGatewayActionIntent
	if json.Unmarshal(action.Intent, &intent) != nil || intent.SchemaVersion != 1 ||
		intent.StageIndex != checkpoint.StageIndex || intent.Route != route ||
		intent.DesiredWeights != (CanaryTrafficWeights{StableWeightPercent: 100}) ||
		intent.IdempotencyKey != fmt.Sprintf("canary:%d:restore", run.DeployLogID) ||
		(intent.ExpectedWeights != *checkpoint.EffectiveWeights && intent.ExpectedWeights != intent.DesiredWeights) {
		return CanaryRestoreResult{}, NewValidationError("已持久化的金丝雀恢复意图与当前阶段不一致")
	}
	if action.ActionKind != canaryGatewayActionKind {
		return CanaryRestoreResult{}, NewValidationError("金丝雀恢复动作类型无效")
	}
	if action.Status == ExecutionActionNotApplied {
		return attention(action.OutcomeCode)
	}
	if action.Status != ExecutionActionApplied && action.Status != ExecutionActionIntent && action.Status != ExecutionActionUnknown {
		return CanaryRestoreResult{}, NewValidationError("金丝雀恢复动作状态无效")
	}
	state, err := adapter.ReadCanaryRoute(ctx, route)
	if err != nil {
		return attention("gateway_read_failed")
	}
	if !validCanaryGatewayReadback(route, state) {
		return attention("gateway_identity_mismatch")
	}
	if state.Weights == intent.DesiredWeights {
		if action.Status != ExecutionActionApplied {
			observation := canaryGatewayActionObservation{SchemaVersion: 1, RouteState: &state, OutcomeCode: "restore_readback_confirmed"}
			if _, err := s.completeCanaryGatewayAction(run.DeployLogID, canaryGatewayRestoreActionKey, ExecutionActionApplied, observation, observation.OutcomeCode); err != nil {
				return CanaryRestoreResult{}, err
			}
		}
		return s.recordCanaryRestoreSuccess(run, plan, checkpoint, idempotencyKey, route, state)
	}
	if action.Status == ExecutionActionApplied {
		return attention("restored_weights_drifted")
	}
	if state.Weights != intent.ExpectedWeights || state.Revision != intent.ExpectedRevision {
		observation := canaryGatewayActionObservation{SchemaVersion: 1, RouteState: &state, OutcomeCode: "restore_state_drifted"}
		if _, err := s.completeCanaryGatewayAction(run.DeployLogID, canaryGatewayRestoreActionKey, ExecutionActionUnknown, observation, observation.OutcomeCode); err != nil {
			return CanaryRestoreResult{}, err
		}
		return attention("restore_state_drifted")
	}
	logRow, err := s.FindLog(run.DeployLogID)
	if err != nil {
		return CanaryRestoreResult{}, err
	}
	return s.writeCanaryRestore(ctx, run, plan, logRow, checkpoint, "canary:restore:"+strings.TrimSpace(idempotencyKey), adapter, intent)
}

func (s *Store) writeCanaryRestore(
	ctx context.Context,
	run DeliveryRun,
	plan DeliveryPlan,
	logRow DeployLog,
	checkpoint CanaryStageCheckpoint,
	eventKey string,
	adapter CanaryGatewayAdapter,
	intent canaryGatewayActionIntent,
) (CanaryRestoreResult, error) {
	state := CanaryGatewayRouteState{}
	var setErr error
	if intent.ExpectedWeights != intent.DesiredWeights {
		setErr = adapter.SetCanaryRouteWeights(ctx, intent.Route, intent.ExpectedRevision, intent.DesiredWeights, intent.IdempotencyKey)
	}
	state, readErr := adapter.ReadCanaryRoute(ctx, intent.Route)
	if readErr != nil || !validCanaryGatewayReadback(intent.Route, state) {
		outcome := "readback_failed"
		if readErr == nil {
			outcome = "readback_identity_invalid"
		}
		observation := canaryGatewayActionObservation{SchemaVersion: 1, OutcomeCode: outcome}
		if _, err := s.completeCanaryGatewayAction(run.DeployLogID, canaryGatewayRestoreActionKey, ExecutionActionUnknown, observation, outcome); err != nil {
			return CanaryRestoreResult{}, err
		}
		return s.recordCanaryRestoreAttention(run, logRow, eventKey, checkpoint, outcome)
	}
	if state.Weights == intent.DesiredWeights {
		observation := canaryGatewayActionObservation{SchemaVersion: 1, RouteState: &state, OutcomeCode: "restore_readback_confirmed"}
		if _, err := s.completeCanaryGatewayAction(run.DeployLogID, canaryGatewayRestoreActionKey, ExecutionActionApplied, observation, observation.OutcomeCode); err != nil {
			return CanaryRestoreResult{}, err
		}
		return s.recordCanaryRestoreSuccess(run, plan, checkpoint, strings.TrimPrefix(eventKey, "canary:restore:"), intent.Route, state)
	}
	outcome := "restore_state_drifted"
	status := ExecutionActionUnknown
	if state.Weights == intent.ExpectedWeights && state.Revision == intent.ExpectedRevision {
		status = ExecutionActionNotApplied
		outcome = "restore_write_not_effective"
		if setErr != nil {
			outcome = "restore_write_not_applied"
		}
	}
	observation := canaryGatewayActionObservation{SchemaVersion: 1, RouteState: &state, OutcomeCode: outcome}
	if _, err := s.completeCanaryGatewayAction(run.DeployLogID, canaryGatewayRestoreActionKey, status, observation, outcome); err != nil {
		return CanaryRestoreResult{}, err
	}
	return s.recordCanaryRestoreAttention(run, logRow, eventKey, checkpoint, outcome)
}

func (s *Store) recordCanaryRestoreSuccess(
	run DeliveryRun,
	plan DeliveryPlan,
	stage CanaryStageCheckpoint,
	idempotencyKey string,
	route CanaryGatewayRouteRef,
	state CanaryGatewayRouteState,
) (CanaryRestoreResult, error) {
	checkpoint := CanaryRestoreCheckpoint{
		SchemaVersion: CanaryRestoreCheckpointSchemaVersion, StageIndex: stage.StageIndex,
		StableImageDigest:    NormalizeDigest(plan.Snapshot.Target.StableImageDigest),
		CandidateImageDigest: canaryPlanCandidateDigest(plan), Route: route,
		EffectiveWeights: state.Weights, RouteRevision: state.Revision, RestoredAt: time.Now().UTC(),
	}
	if err := validateCanaryRestoreCheckpointAgainstPlan(checkpoint, plan); err != nil {
		return CanaryRestoreResult{}, err
	}
	payload, err := json.Marshal(checkpoint)
	if err != nil {
		return CanaryRestoreResult{}, err
	}
	eventKey := "canary:restore:" + strings.TrimSpace(idempotencyKey)
	logRow, err := s.FindLog(run.DeployLogID)
	if err != nil {
		return CanaryRestoreResult{}, err
	}
	logRow.Status = "running"
	logRow.CurrentStep = strPtr("awaiting_restore_verification")
	logRow.ErrorMessage = nil
	event := DeliveryRunEvent{
		EventKey: eventKey, EventType: "canary_restore", Stage: "awaiting_restore_verification", Status: "running",
		Detail: "稳定版流量已回读为 100%，候选版为 0%；请完成业务验证，运行锁将在确认前保留。", Payload: payload,
	}
	updated, _, err := s.TransitionDeliveryRun(logRow, run.Version, event)
	if err != nil {
		return CanaryRestoreResult{}, err
	}
	return CanaryRestoreResult{Status: updated.CurrentStage, StageIndex: checkpoint.StageIndex, Outcome: "stable_weights_readback_confirmed", Checkpoint: &checkpoint}, nil
}

func (s *Store) recordCanaryRestoreAttention(
	run DeliveryRun,
	logRow DeployLog,
	idempotencyEventKey string,
	stage CanaryStageCheckpoint,
	outcome string,
) (CanaryRestoreResult, error) {
	message := canaryRestoreOutcomeDetail(outcome)
	logRow.Status = "running"
	logRow.CurrentStep = strPtr("needs_attention")
	logRow.ErrorMessage = &message
	event := DeliveryRunEvent{
		EventKey:  idempotencyEventKey + ":attention:" + outcome,
		EventType: "recovery_required", Stage: "needs_attention", Status: "running", Detail: message,
	}
	if len(event.EventKey) > 160 {
		return CanaryRestoreResult{}, NewValidationError("金丝雀恢复幂等键过长")
	}
	if _, _, err := s.TransitionDeliveryRun(logRow, run.Version, event); err != nil {
		return CanaryRestoreResult{}, err
	}
	return CanaryRestoreResult{Status: CanaryStageNeedsAttention, StageIndex: stage.StageIndex, Outcome: outcome}, nil
}

func canaryRestoreOutcomeDetail(outcome string) string {
	switch outcome {
	case "gateway_read_failed", "readback_failed":
		return "无法读取网关当前状态，稳定流量恢复结果未确认；运行锁保持占用。"
	case "gateway_identity_mismatch", "readback_identity_invalid":
		return "网关路由身份与已批准阶段不一致，未确认恢复；运行锁保持占用。"
	case "gateway_weights_drifted", "restore_state_drifted":
		return "网关权重在恢复期间发生变化，平台没有继续写入；请核对当前流量。"
	case "restore_write_not_effective", "restore_write_not_applied":
		return "网关未应用稳定版恢复权重；当前状态已回读，运行锁保持占用。"
	case "restored_weights_drifted":
		return "稳定版恢复后网关权重再次变化，不能确认业务恢复。"
	default:
		return "稳定版流量恢复状态无法确认；请核对网关与发布动作回读。"
	}
}

func canaryStageActionIntent(stageIndex int, actions []ExecutionAction) (canaryGatewayActionIntent, error) {
	key := fmt.Sprintf("canary-gateway-stage-%d", stageIndex+1)
	for _, action := range actions {
		if action.ActionKey != key || action.ActionKind != canaryGatewayActionKind || action.Status != ExecutionActionApplied {
			continue
		}
		var intent canaryGatewayActionIntent
		if json.Unmarshal(action.Intent, &intent) != nil || intent.SchemaVersion != 1 || intent.StageIndex != stageIndex || !validCanaryGatewayRouteRef(intent.Route) {
			return canaryGatewayActionIntent{}, NewValidationError("当前金丝雀阶段缺少有效的网关路由身份")
		}
		return intent, nil
	}
	return canaryGatewayActionIntent{}, NewValidationError("金丝雀恢复缺少已确认的阶段网关动作")
}

func validateCanaryStageActionForRestore(checkpoint CanaryStageCheckpoint, actions []ExecutionAction) error {
	if err := validateCanaryCheckpointActionEvidence(checkpoint, actions); err != nil {
		return err
	}
	_, err := canaryStageActionIntent(checkpoint.StageIndex, actions)
	return err
}

func canaryPlanCandidateDigest(plan DeliveryPlan) string {
	if plan.Snapshot.Artifact.Digest != nil {
		if digest := NormalizeDigest(*plan.Snapshot.Artifact.Digest); digest != "" {
			return digest
		}
	}
	return imageReferenceDigest(plan.Snapshot.Artifact.Image)
}

func validateCanaryRestoreCheckpointAgainstPlan(checkpoint CanaryRestoreCheckpoint, plan DeliveryPlan) error {
	if plan.Strategy != "gateway_canary" || plan.Snapshot.Policy.CanaryAnalysis == nil ||
		checkpoint.SchemaVersion != CanaryRestoreCheckpointSchemaVersion || checkpoint.StageIndex < 0 || checkpoint.StageIndex >= len(plan.Snapshot.Policy.CanaryAnalysis.Stages) ||
		!validCanaryGatewayRouteRef(checkpoint.Route) || checkpoint.RouteRevision == "" || len(checkpoint.RouteRevision) > 512 ||
		checkpoint.RestoredAt.IsZero() || checkpoint.EffectiveWeights != (CanaryTrafficWeights{StableWeightPercent: 100}) ||
		NormalizeDigest(checkpoint.StableImageDigest) == "" || NormalizeDigest(checkpoint.CandidateImageDigest) == "" ||
		NormalizeDigest(checkpoint.StableImageDigest) != NormalizeDigest(plan.Snapshot.Target.StableImageDigest) ||
		NormalizeDigest(checkpoint.CandidateImageDigest) != canaryPlanCandidateDigest(plan) {
		return NewValidationError("金丝雀恢复快照与不可变计划或稳定权重不一致")
	}
	return nil
}

func DecodeCanaryRestoreCheckpoint(event DeliveryRunEvent) (CanaryRestoreCheckpoint, error) {
	if event.EventType != "canary_restore" || len(event.Payload) == 0 {
		return CanaryRestoreCheckpoint{}, NewValidationError("发布事件不是金丝雀恢复快照")
	}
	var checkpoint CanaryRestoreCheckpoint
	if json.Unmarshal(event.Payload, &checkpoint) != nil || event.Stage != "awaiting_restore_verification" || event.Status != "running" {
		return CanaryRestoreCheckpoint{}, NewValidationError("金丝雀恢复快照无法解析")
	}
	if checkpoint.StageIndex < 0 || event.Detail == "" {
		return CanaryRestoreCheckpoint{}, NewValidationError("金丝雀恢复事件阶段或说明无效")
	}
	return checkpoint, nil
}

func validCanaryRestoreIdempotencyKey(value string) bool {
	return len(value) >= 8 && len(value) <= 80 && strings.TrimSpace(value) == value &&
		utf8ValidString(value) && !strings.ContainsAny(value, ";\r\n")
}
