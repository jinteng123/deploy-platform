package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func canaryCheckpointFixture(now time.Time) (CanaryAnalysisPolicy, CanaryStageCheckpoint, CanaryStageCheckpoint) {
	started := now.Add(-3 * time.Minute)
	stableP95, candidateP95 := 100.0, 105.0
	policy := CanaryAnalysisPolicy{
		MinRequests: 100, MaxObservationAgeSeconds: 300,
		GatewayRoute: &CanaryGatewayRouteRef{
			ConnectionID: 7, RouteID: "checkout-public", StableBackendID: "checkout-stable", CandidateBackendID: "checkout-candidate",
		},
		MaxCandidateErrorRate: float64Pointer(0.02), MaxErrorRateIncrease: float64Pointer(0.01),
		MaxCandidateP95LatencyMillis: float64Pointer(200), MaxP95LatencyIncreaseRatio: float64Pointer(0.5),
		Stages: []CanaryStage{{CandidateWeightPercent: 10, ObservationDurationSeconds: 60}, {CandidateWeightPercent: 100, ObservationDurationSeconds: 60}},
	}
	observation := CanaryMetricObservation{
		StableImageDigest: "sha256:" + strings.Repeat("a", 64), CandidateImageDigest: "sha256:" + strings.Repeat("b", 64),
		WindowStart: started, WindowEnd: now.Add(-time.Minute), ObservedAt: now,
		Stable:    CanaryVersionMetrics{ImageDigest: "sha256:" + strings.Repeat("a", 64), Requests: 200, Errors: 1, P95LatencyMillis: &stableP95, Health: "healthy"},
		Candidate: CanaryVersionMetrics{ImageDigest: "sha256:" + strings.Repeat("b", 64), Requests: 200, Errors: 1, P95LatencyMillis: &candidateP95, Health: "healthy"},
	}
	weights := &CanaryTrafficWeights{StableWeightPercent: 90, CandidateWeightPercent: 10}
	observing := CanaryStageCheckpoint{
		SchemaVersion: CanaryStageCheckpointSchemaVersion, StageIndex: 0, CandidateWeightPercent: 10,
		ObservationDurationSeconds: 60, Phase: CanaryStageObserving, StageStartedAt: &started, EffectiveWeights: weights,
	}
	decision, _ := EvaluateCanaryStageObservation(policy, 0, started, *weights, observation, now)
	awaitingConfirmation := observing
	awaitingConfirmation.Phase = CanaryStageAwaitingConfirm
	awaitingConfirmation.Observation = &observation
	awaitingConfirmation.Decision = &decision
	return policy, observing, awaitingConfirmation
}

func canaryCheckpointPlan(policy CanaryAnalysisPolicy) DeliveryPlan {
	stableDigest := "sha256:" + strings.Repeat("a", 64)
	candidateDigest := "sha256:" + strings.Repeat("b", 64)
	return DeliveryPlan{
		ID: 12, Strategy: "gateway_canary",
		Snapshot: DeliveryPlanSnapshot{
			Strategy: "gateway_canary",
			Artifact: DeliveryPlanArtifact{Image: "registry.example.com/app@" + candidateDigest, Digest: &candidateDigest},
			Target:   DeliveryPlanTarget{StableImageDigest: stableDigest},
			Policy:   DeliveryPlanPolicySnapshot{Strategy: "gateway_canary", CanaryAnalysis: &policy},
		},
	}
}

func TestCanaryStageCheckpointPersistsAndReplaysAfterRestart(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	policy, observing, awaitingConfirmation := canaryCheckpointFixture(now)
	store := NewStore()
	step := "canary_stage_1"
	if _, err := store.AddLog(DeployLog{ID: 31, Status: "running", CurrentStep: &step, StepDetail: "[]"}); err != nil {
		t.Fatal(err)
	}
	store.LoadDeliveryRuns([]DeliveryRun{{
		ID: 8, DeliveryPlanID: 12, PromotionRequestID: 4, DeployLogID: 31,
		Strategy: "gateway_canary", Status: "running", CurrentStage: step, Version: 1,
	}}, []DeliveryRunEvent{{ID: 1, RunID: 8, Version: 1, EventKey: "created", EventType: "queued", Stage: "queued", Status: "queued"}})
	store.LoadDeliveryPlans([]DeliveryPlan{canaryCheckpointPlan(policy)})
	awaitingWeights := CanaryStageCheckpoint{
		SchemaVersion: CanaryStageCheckpointSchemaVersion, StageIndex: 0, CandidateWeightPercent: 10,
		ObservationDurationSeconds: 60, Phase: CanaryStageAwaitingWeights,
	}
	run, found, err := store.RecordCanaryStageCheckpoint(31, 1, "canary:stage-1:awaiting-weights", "running", "等待网关权重阶段动作。", awaitingWeights)
	if err != nil || !found || run.Version != 2 {
		t.Fatalf("record initial checkpoint run=%+v found=%v err=%v", run, found, err)
	}

	if _, _, err := store.RecordCanaryStageCheckpoint(31, 1, "canary:stage-1:unverified", "running", "", observing); err == nil {
		t.Fatal("checkpoint accepted effective gateway weights without a persisted applied action")
	}
	recordCanaryGatewayActionEvidence(t, store, 31, 0, policy, observing)
	run, found, err = store.RecordCanaryStageCheckpoint(31, 2, "canary:stage-1:observing", "running", "10% 权重已回读，开始观察。", observing)
	if err != nil || !found || run.Version != 3 {
		t.Fatalf("record observing checkpoint run=%+v found=%v err=%v", run, found, err)
	}
	run, found, err = store.RecordCanaryStageCheckpoint(31, 3, "canary:stage-1:awaiting-confirmation", "running", "指标通过，等待人工确认。", awaitingConfirmation)
	if err != nil || !found || run.Version != 4 {
		t.Fatalf("record confirmation checkpoint run=%+v found=%v err=%v", run, found, err)
	}

	log, err := store.FindLog(31)
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.DeliveryRunEvents(8)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewStore()
	if _, err := restarted.AddLog(log); err != nil {
		t.Fatal(err)
	}
	restarted.LoadDeliveryRuns([]DeliveryRun{run}, events)
	restarted.LoadDeliveryPlans([]DeliveryPlan{canaryCheckpointPlan(policy)})
	replayed, err := restarted.LatestCanaryStageCheckpoint(8)
	if err != nil || replayed == nil || replayed.Phase != CanaryStageAwaitingConfirm || replayed.Decision == nil || replayed.Observation == nil {
		t.Fatalf("replayed checkpoint=%+v err=%v", replayed, err)
	}
	if replayed.EffectiveWeights == nil || replayed.EffectiveWeights.CandidateWeightPercent != 10 || replayed.Observation.CandidateImageDigest != awaitingConfirmation.Observation.CandidateImageDigest {
		t.Fatalf("replayed checkpoint lost its effective state or image identity: %+v", replayed)
	}
	if !json.Valid(events[len(events)-1].Payload) {
		t.Fatal("checkpoint payload is not valid JSON")
	}
}

func TestCanaryStageCheckpointSchemaV1RemainsReadableWithoutEvaluationTime(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	policy, _, checkpoint := canaryCheckpointFixture(now)
	checkpoint.SchemaVersion = 1
	checkpoint.Decision.EvaluatedAt = time.Time{}
	if _, err := CanaryStageCheckpointEvent("legacy-v1", "running", "legacy snapshot", checkpoint); err != nil {
		t.Fatalf("schema-v1 checkpoint no longer decodes without evaluated_at: %v", err)
	}
	if err := validateCanaryCheckpointAgainstPlan(checkpoint, canaryCheckpointPlan(policy)); err != nil {
		t.Fatalf("schema-v1 checkpoint no longer replays with observation time fallback: %v", err)
	}

	checkpoint.SchemaVersion = CanaryStageCheckpointSchemaVersion
	if _, err := CanaryStageCheckpointEvent("current-v2", "running", "missing evaluation time", checkpoint); err == nil {
		t.Fatal("schema-v2 checkpoint accepted a missing evaluation timestamp")
	}
}

func TestCanaryStageCheckpointRejectsInvalidStateAndPayloadKeyReuse(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	policy, observing, awaitingConfirmation := canaryCheckpointFixture(now)
	goodEvent, err := CanaryStageCheckpointEvent("good", "running", "", observing)
	if err != nil {
		t.Fatal(err)
	}
	corruptNewest := DeliveryRunEvent{
		EventKey: "corrupt", EventType: "canary_checkpoint", Stage: "canary_stage_1", Status: "running",
		Payload: json.RawMessage(`{"schema_version":999}`),
	}
	if _, err := LatestCanaryStageCheckpoint([]DeliveryRunEvent{goodEvent, corruptNewest}); err == nil {
		t.Fatal("replay fell back to an older stage after the newest checkpoint was corrupt")
	}
	wrongWeights := observing
	wrongWeights.EffectiveWeights = &CanaryTrafficWeights{StableWeightPercent: 80, CandidateWeightPercent: 20}
	if _, err := CanaryStageCheckpointEvent("invalid", "running", "", wrongWeights); err == nil {
		t.Fatal("checkpoint accepted weights that differ from the planned stage")
	}
	incomplete := awaitingConfirmation
	incomplete.Observation = nil
	if _, err := CanaryStageCheckpointEvent("invalid-observation", "running", "", incomplete); err == nil {
		t.Fatal("checkpoint accepted an awaiting-confirmation phase without its observation")
	}
	incompleteFinal := observing
	incompleteFinal.CandidateWeightPercent = 100
	incompleteFinal.Phase = CanaryStageCompleted
	incompleteFinal.EffectiveWeights = &CanaryTrafficWeights{CandidateWeightPercent: 100}
	if _, err := CanaryStageCheckpointEvent("invalid-completion", "success", "", incompleteFinal); err == nil {
		t.Fatal("checkpoint accepted completion without a healthy full-traffic observation")
	}

	finalWeights := CanaryTrafficWeights{CandidateWeightPercent: 100}
	finalDecision, err := EvaluateCanaryStageObservation(policy, 1, *awaitingConfirmation.StageStartedAt, finalWeights, *awaitingConfirmation.Observation, now)
	if err != nil {
		t.Fatal(err)
	}
	completed := awaitingConfirmation
	completed.StageIndex = 1
	completed.CandidateWeightPercent = 100
	completed.Phase = CanaryStageCompleted
	completed.EffectiveWeights = &finalWeights
	completed.Decision = &finalDecision
	confirmedStage := 1
	completed.ConfirmedStageIndex = &confirmedStage
	completed.ConfirmedBy = "operator"
	confirmedAt := now.Add(time.Second)
	completed.ConfirmedAt = &confirmedAt
	if _, err := CanaryStageCheckpointEvent("valid-completion", "success", "", completed); err != nil {
		t.Fatalf("complete healthy full-traffic checkpoint was rejected: %v", err)
	}

	store := NewStore()
	step := "canary_stage_1"
	if _, err := store.AddLog(DeployLog{ID: 31, Status: "running", CurrentStep: &step, StepDetail: "[]"}); err != nil {
		t.Fatal(err)
	}
	store.LoadDeliveryRuns([]DeliveryRun{{
		ID: 8, DeliveryPlanID: 12, PromotionRequestID: 4, DeployLogID: 31,
		Strategy: "gateway_canary", Status: "running", CurrentStage: step, Version: 1,
	}}, []DeliveryRunEvent{{ID: 1, RunID: 8, Version: 1, EventKey: "created", EventType: "queued", Stage: "queued", Status: "queued"}})
	store.LoadDeliveryPlans([]DeliveryPlan{canaryCheckpointPlan(policy)})
	mismatchedPlanStage := observing
	mismatchedPlanStage.CandidateWeightPercent = 20
	mismatchedPlanStage.EffectiveWeights = &CanaryTrafficWeights{StableWeightPercent: 80, CandidateWeightPercent: 20}
	if _, _, err := store.RecordCanaryStageCheckpoint(31, 1, "canary:wrong-stage", "running", "", mismatchedPlanStage); err == nil {
		t.Fatal("checkpoint accepted a stage that differs from its immutable plan")
	}
	awaitingWeights := CanaryStageCheckpoint{
		SchemaVersion: CanaryStageCheckpointSchemaVersion, StageIndex: 0, CandidateWeightPercent: 10,
		ObservationDurationSeconds: 60, Phase: CanaryStageAwaitingWeights,
	}
	if _, _, err := store.RecordCanaryStageCheckpoint(31, 1, "canary:awaiting-weights", "running", "", awaitingWeights); err != nil {
		t.Fatal(err)
	}
	recordCanaryGatewayActionEvidence(t, store, 31, 0, policy, observing)
	if _, _, err := store.RecordCanaryStageCheckpoint(31, 2, "canary:stage-1", "running", "observing", observing); err != nil {
		t.Fatal(err)
	}
	_, duplicate, err := store.RecordCanaryStageCheckpoint(31, 2, "canary:stage-1", "running", "observing", awaitingConfirmation)
	if !duplicate || !errors.Is(err, ErrConflict) {
		t.Fatalf("reusing checkpoint key with changed payload duplicate=%v err=%v", duplicate, err)
	}
}

func TestCanaryStageCheckpointCannotSkipInitialOrIntermediateStages(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	policy, observing, _ := canaryCheckpointFixture(now)
	store := NewStore()
	step := "queued"
	if _, err := store.AddLog(DeployLog{ID: 41, Status: "running", CurrentStep: &step, StepDetail: "[]"}); err != nil {
		t.Fatal(err)
	}
	store.LoadDeliveryRuns([]DeliveryRun{{
		ID: 18, DeliveryPlanID: 22, PromotionRequestID: 14, DeployLogID: 41,
		Strategy: "gateway_canary", Status: "running", CurrentStage: "queued", Version: 1,
	}}, []DeliveryRunEvent{{ID: 1, RunID: 18, Version: 1, EventKey: "created", EventType: "queued", Stage: "queued", Status: "queued"}})
	plan := canaryCheckpointPlan(policy)
	plan.ID = 22
	store.LoadDeliveryPlans([]DeliveryPlan{plan})

	if _, _, err := store.RecordCanaryStageCheckpoint(41, 1, "canary:observing-first", "running", "", observing); err == nil {
		t.Fatal("checkpoint accepted observing as the first canary phase")
	}
	firstStage := CanaryStageCheckpoint{
		SchemaVersion: CanaryStageCheckpointSchemaVersion, StageIndex: 0, CandidateWeightPercent: 10,
		ObservationDurationSeconds: 60, Phase: CanaryStageAwaitingWeights,
	}
	if _, _, err := store.RecordCanaryStageCheckpoint(41, 1, "canary:first-weights", "running", "", firstStage); err != nil {
		t.Fatal(err)
	}
	secondStage := CanaryStageCheckpoint{
		SchemaVersion: CanaryStageCheckpointSchemaVersion, StageIndex: 1, CandidateWeightPercent: 100,
		ObservationDurationSeconds: 60, Phase: CanaryStageAwaitingWeights,
	}
	if _, _, err := store.RecordCanaryStageCheckpoint(41, 2, "canary:skip-to-final", "running", "", secondStage); err == nil {
		t.Fatal("checkpoint skipped the first stage and advanced directly to the final weight gate")
	}
}

func float64Pointer(value float64) *float64 { return &value }

func recordCanaryGatewayActionEvidence(t *testing.T, store *Store, logID int64, stageIndex int, policy CanaryAnalysisPolicy, checkpoint CanaryStageCheckpoint) {
	t.Helper()
	weights := canaryWeights(policy.Stages[stageIndex].CandidateWeightPercent)
	expected := CanaryTrafficWeights{StableWeightPercent: 100, CandidateWeightPercent: 0}
	if stageIndex > 0 {
		expected = canaryWeights(policy.Stages[stageIndex-1].CandidateWeightPercent)
	}
	route := CanaryGatewayRouteRef{
		ConnectionID: 7, RouteID: "checkout-public", StableBackendID: "checkout-stable", CandidateBackendID: "checkout-candidate",
	}
	intent := canaryGatewayActionIntent{
		SchemaVersion: 1, StageIndex: stageIndex, Route: route,
		ExpectedRevision: "before-stage", ExpectedWeights: expected, DesiredWeights: weights,
		IdempotencyKey: fmt.Sprintf("canary:%d:stage:%d", logID, stageIndex+1),
	}
	intentJSON, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("canary-gateway-stage-%d", stageIndex+1)
	if _, _, err := store.BeginExecutionAction(ExecutionAction{
		DeployLogID: logID, ActionKey: key, ActionKind: canaryGatewayActionKind,
		Status: ExecutionActionIntent, Intent: intentJSON, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	startedAt := *checkpoint.StageStartedAt
	state := CanaryGatewayRouteState{
		ConnectionID: route.ConnectionID, RouteID: route.RouteID, Revision: "after-stage",
		StableBackendID: route.StableBackendID, CandidateBackendID: route.CandidateBackendID, Weights: weights,
	}
	observedJSON, err := json.Marshal(canaryGatewayActionObservation{
		SchemaVersion: 1, RouteState: &state, StageStartedAt: &startedAt, OutcomeCode: "readback_confirmed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteExecutionAction(logID, key, ExecutionActionApplied, observedJSON, "readback_confirmed"); err != nil {
		t.Fatal(err)
	}
}
