package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func newCanaryConfirmationFixture(t *testing.T) (*Store, *CDService, DeliveryRun, CanaryAnalysisPolicy, CanaryStageCheckpoint) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	policy, _, baseConfirmation := canaryCheckpointFixture(now)
	step := "queued"
	store := NewStore()
	log, err := store.AddLog(DeployLog{
		ID: 71, ClusterID: 2, AppName: "checkout", DeploymentName: "checkout-api", Namespace: "prod",
		DeployType: "image_deploy", Status: "running", CurrentStep: &step, StepDetail: "[]",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := DeliveryRun{
		ID: 27, DeliveryPlanID: 12, DeployLogID: log.ID, Strategy: "gateway_canary",
		Status: "running", CurrentStage: step, Version: 1,
	}
	store.LoadDeliveryRuns([]DeliveryRun{run}, []DeliveryRunEvent{{
		ID: 1, RunID: run.ID, Version: 1, EventKey: "created", EventType: "queued", Stage: "queued", Status: "queued",
	}})
	store.LoadDeliveryPlans([]DeliveryPlan{canaryCheckpointPlan(policy)})
	first := CanaryStageCheckpoint{
		SchemaVersion: CanaryStageCheckpointSchemaVersion, StageIndex: 0,
		CandidateWeightPercent:     policy.Stages[0].CandidateWeightPercent,
		ObservationDurationSeconds: policy.Stages[0].ObservationDurationSeconds, Phase: CanaryStageAwaitingWeights,
	}
	if _, _, err := store.RecordCanaryStageCheckpoint(log.ID, run.Version, "canary:first-weights", "running", "等待第一阶段权重。", first); err != nil {
		t.Fatal(err)
	}
	confirm := makeHealthyCanaryConfirmation(t, policy, 0, baseConfirmation)
	recordCanaryGatewayActionEvidence(t, store, log.ID, 0, policy, confirm)
	if _, _, err := store.RecordCanaryStageCheckpoint(log.ID, 2, "canary:first-observing", "running", "第一阶段权重已回读。", observingFromConfirmation(confirm)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RecordCanaryStageCheckpoint(log.ID, 3, "canary:first-confirmation", "running", "第一阶段观测健康。", confirm); err != nil {
		t.Fatal(err)
	}
	cd := &CDService{store: store}
	return store, cd, run, policy, confirm
}

func newQueuedCanaryFixture(t *testing.T) (*Store, *CDService, DeliveryRun) {
	t.Helper()
	policy, _, _ := canaryCheckpointFixture(time.Now().UTC())
	store := NewStore()
	step := "queued"
	log, err := store.AddLog(DeployLog{
		ID: 72, ClusterID: 2, AppName: "checkout", DeploymentName: "checkout-api", Namespace: "prod",
		DeployType: "image_deploy", Status: "pending", CurrentStep: &step, StepDetail: "[]",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := DeliveryRun{
		ID: 28, DeliveryPlanID: 12, DeployLogID: log.ID, Strategy: "gateway_canary",
		Status: "queued", CurrentStage: "queued", Version: 1,
	}
	store.LoadDeliveryRuns([]DeliveryRun{run}, []DeliveryRunEvent{{
		ID: 1, RunID: run.ID, Version: 1, EventKey: "created", EventType: "queued", Stage: "queued", Status: "queued",
	}})
	store.LoadDeliveryPlans([]DeliveryPlan{canaryCheckpointPlan(policy)})
	return store, &CDService{store: store}, run
}

func TestCancelQueuedCanaryBeforeWorkerClaimAndGatewayAction(t *testing.T) {
	store, cd, run := newQueuedCanaryFixture(t)
	lock := workloadResourceLock(DeployLog{ID: run.DeployLogID, ClusterID: 2, Namespace: "prod", DeploymentName: "checkout-api"})

	view, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionCancel, run.Version, "cancel-canary-queued-001", "operator")
	if err != nil {
		t.Fatalf("cancel queued canary without gateway or Kubernetes runtime: %v", err)
	}
	if view.Run.Status != "cancelled" || view.Run.CurrentStage != "cancelled" || view.Run.Version != run.Version+1 ||
		view.Deploy.Status != "failed" || view.Deploy.CurrentStep == nil || *view.Deploy.CurrentStep != "cancelled" {
		t.Fatalf("pre-write canary cancellation state=%+v deploy=%+v", view.Run, view.Deploy)
	}
	if len(view.Actions) != 0 || len(view.Events) != 2 || !strings.Contains(view.Events[1].Detail, "没有网关或集群动作记录") {
		t.Fatalf("cancellation should record one clear event and no external action: %+v", view)
	}
	if err := store.CheckResourceLock(lock); err != nil {
		t.Fatalf("terminal cancellation retained the Deployment lock: %v", err)
	}
	if !DeliveryRunPipelineStopped(view.Run.Status) {
		t.Fatal("cancelled canary was not treated as a stopped worker run")
	}
	duplicate, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionCancel, run.Version, "cancel-canary-queued-001", "operator")
	if err != nil || duplicate.Run.Version != view.Run.Version || len(duplicate.Events) != 2 {
		t.Fatalf("idempotent queued cancellation view=%+v err=%v", duplicate, err)
	}
}

func TestCancelClaimedCanaryBeforeExternalActionAndRejectAfterAction(t *testing.T) {
	t.Run("worker already claimed", func(t *testing.T) {
		store, cd, run := newQueuedCanaryFixture(t)
		if claimed, err := store.ClaimDeployLog(run.DeployLogID); err != nil || !claimed {
			t.Fatalf("claim queued canary: claimed=%t err=%v", claimed, err)
		}
		staleWorkerLog, _ := store.FindLog(run.DeployLogID)
		view, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionCancel, run.Version, "cancel-canary-claimed", "operator")
		if err != nil || view.Run.Status != "cancelled" || view.Run.Version != run.Version+1 || view.Deploy.Status != "failed" {
			t.Fatalf("claimed canary without external actions should cancel safely: view=%+v err=%v", view, err)
		}
		if _, _, err := store.BeginExecutionAction(ExecutionAction{
			DeployLogID: run.DeployLogID, ActionKey: "stale-worker-write", ActionKind: canaryGatewayActionKind,
			Intent: []byte(`{"route_id":"orders"}`),
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("revoked Worker claim recorded an action: %v", err)
		}
		staleWorkerLog.Status = "running"
		if _, err := store.UpdateLogExecutionAndRecordDeliveryRunEvent(staleWorkerLog, DeliveryRunEvent{
			EventKey: "stale-worker-checkpoint", EventType: "checkpoint", Stage: "awaiting_weights", Status: "running",
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("revoked Worker claim wrote a checkpoint: %v", err)
		}
		current, _ := store.DeliveryRun(run.ID)
		log, _ := store.FindLog(run.DeployLogID)
		if current.Status != "cancelled" || current.Version != run.Version+1 || log.Status != "failed" || len(view.Events) != 2 || len(view.Actions) != 0 {
			t.Fatalf("stale Worker changed cancelled canary state: run=%+v log=%+v", current, log)
		}
	})

	t.Run("gateway write was journaled", func(t *testing.T) {
		store, cd, run := newQueuedCanaryFixture(t)
		if claimed, err := store.ClaimDeployLog(run.DeployLogID); err != nil || !claimed {
			t.Fatalf("claim queued canary: claimed=%t err=%v", claimed, err)
		}
		if _, _, err := store.BeginExecutionAction(ExecutionAction{
			DeployLogID: run.DeployLogID, ActionKey: "canary-gateway-stage-1", ActionKind: canaryGatewayActionKind,
			Intent: []byte(`{"route_id":"orders"}`),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionCancel, run.Version, "cancel-canary-action-exists", "operator"); !errors.Is(err, ErrConflict) {
			t.Fatalf("cancel with an external action error=%v, want conflict", err)
		}
		current, _ := store.DeliveryRun(run.ID)
		if current.Status != "queued" || current.Version != run.Version {
			t.Fatalf("blocked cancellation changed run: %+v", current)
		}
	})
}

func makeHealthyCanaryConfirmation(t *testing.T, policy CanaryAnalysisPolicy, stageIndex int, base CanaryStageCheckpoint) CanaryStageCheckpoint {
	t.Helper()
	started := time.Now().UTC().Add(-3 * time.Minute).Truncate(time.Second)
	weights := canaryWeights(policy.Stages[stageIndex].CandidateWeightPercent)
	observation := *base.Observation
	observation.WindowStart = started
	observation.WindowEnd = started.Add(2 * time.Minute)
	observation.ObservedAt = started.Add(2*time.Minute + time.Second)
	decision, err := EvaluateCanaryStageObservation(policy, stageIndex, started, weights, observation, observation.ObservedAt)
	if err != nil {
		t.Fatal(err)
	}
	return CanaryStageCheckpoint{
		SchemaVersion: CanaryStageCheckpointSchemaVersion, StageIndex: stageIndex,
		CandidateWeightPercent:     policy.Stages[stageIndex].CandidateWeightPercent,
		ObservationDurationSeconds: policy.Stages[stageIndex].ObservationDurationSeconds,
		Phase:                      CanaryStageAwaitingConfirm, StageStartedAt: &started, EffectiveWeights: &weights,
		Observation: &observation, Decision: &decision,
	}
}

func observingFromConfirmation(confirmation CanaryStageCheckpoint) CanaryStageCheckpoint {
	observing := confirmation
	observing.Phase = CanaryStageObserving
	observing.Observation = nil
	observing.Decision = nil
	return observing
}

func TestConfirmCanaryStageAdvancesOneStageAndIsIdempotent(t *testing.T) {
	store, cd, run, _, _ := newCanaryConfirmationFixture(t)
	view, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionConfirmCanaryStage, 4, "confirm-canary-001", "operator")
	if err != nil {
		t.Fatal(err)
	}
	if view.Run.Status != "running" || view.Run.CurrentStage != "canary_stage_2" || view.Run.Version != 5 {
		t.Fatalf("confirmation advanced to unexpected run state: %+v", view.Run)
	}
	checkpoint, err := store.LatestCanaryStageCheckpoint(run.ID)
	if err != nil || checkpoint == nil || checkpoint.StageIndex != 1 || checkpoint.Phase != CanaryStageAwaitingWeights ||
		checkpoint.ConfirmedStageIndex == nil || *checkpoint.ConfirmedStageIndex != 0 || checkpoint.ConfirmedBy != "operator" || checkpoint.ConfirmedAt == nil {
		t.Fatalf("next checkpoint did not preserve operator confirmation: checkpoint=%+v err=%v", checkpoint, err)
	}
	duplicate, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionConfirmCanaryStage, 4, "confirm-canary-001", "operator")
	if err != nil || duplicate.Run.Version != view.Run.Version {
		t.Fatalf("idempotent confirmation view=%+v err=%v", duplicate.Run, err)
	}
	if _, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionConfirmCanaryStage, 4, "confirm-canary-001", "other-operator"); !errors.Is(err, ErrConflict) {
		t.Fatalf("same idempotency key with a different operator should conflict: %v", err)
	}
}

func TestConfirmCanaryFinalStageCompletesOnlyAfterHealthyHundredPercentObservation(t *testing.T) {
	store, cd, run, policy, baseConfirmation := newCanaryConfirmationFixture(t)
	first, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionConfirmCanaryStage, 4, "confirm-canary-stage-1", "operator")
	if err != nil {
		t.Fatal(err)
	}
	final := makeHealthyCanaryConfirmation(t, policy, 1, baseConfirmation)
	prior, err := store.LatestCanaryStageCheckpoint(run.ID)
	if err != nil || prior == nil {
		t.Fatalf("read next-stage confirmation metadata: checkpoint=%+v err=%v", prior, err)
	}
	final.ConfirmedStageIndex = prior.ConfirmedStageIndex
	final.ConfirmedBy = prior.ConfirmedBy
	final.ConfirmedAt = prior.ConfirmedAt
	recordCanaryGatewayActionEvidence(t, store, run.DeployLogID, 1, policy, final)
	if _, _, err := store.RecordCanaryStageCheckpoint(run.DeployLogID, first.Run.Version, "canary:final-observing", "running", "100% 权重已回读。", observingFromConfirmation(final)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RecordCanaryStageCheckpoint(run.DeployLogID, first.Run.Version+1, "canary:final-confirmation", "running", "最终观测健康。", final); err != nil {
		t.Fatal(err)
	}
	view, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionConfirmCanaryStage, first.Run.Version+2, "confirm-canary-final", "operator")
	if err != nil {
		t.Fatal(err)
	}
	if view.Run.Status != "success" || view.Deploy.Status != "success" || view.Run.Version != first.Run.Version+3 {
		t.Fatalf("final confirmation did not complete the run: run=%+v deploy=%+v", view.Run, view.Deploy)
	}
	checkpoint, err := store.LatestCanaryStageCheckpoint(run.ID)
	if err != nil || checkpoint == nil || checkpoint.Phase != CanaryStageCompleted || checkpoint.ConfirmedStageIndex == nil ||
		*checkpoint.ConfirmedStageIndex != 1 || checkpoint.ConfirmedBy != "operator" {
		t.Fatalf("completed checkpoint lacks final operator confirmation: checkpoint=%+v err=%v", checkpoint, err)
	}
}

func TestCanaryPauseAndContinuePreserveHealthyConfirmationCheckpointAndLocks(t *testing.T) {
	store, cd, run, _, _ := newCanaryConfirmationFixture(t)
	lock := workloadResourceLock(DeployLog{ID: 71, ClusterID: 2, Namespace: "prod", DeploymentName: "checkout-api"})
	store.mu.Lock()
	store.resourceLocks[lock.Identity()] = run.DeployLogID
	store.mu.Unlock()

	paused, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionPause, 4, "canary-pause-control-001", "operator")
	if err != nil {
		t.Fatalf("pause at healthy operator gate without a Kubernetes runtime: %v", err)
	}
	if paused.Run.Status != "paused" || paused.Run.CurrentStage != "canary_stage_1" || paused.Run.Version != 5 || paused.Deploy.Status != "running" {
		t.Fatalf("pause changed the checkpoint or deploy state: run=%+v deploy=%+v", paused.Run, paused.Deploy)
	}
	checkpoint, err := store.LatestCanaryStageCheckpoint(run.ID)
	if err != nil || checkpoint == nil || checkpoint.Phase != CanaryStageAwaitingConfirm || checkpoint.Decision == nil {
		t.Fatalf("pause did not preserve the verified checkpoint: checkpoint=%+v err=%v", checkpoint, err)
	}
	if err := store.CheckResourceLock(lock); !errors.Is(err, ErrConflict) {
		t.Fatalf("pause released the canary resource lock: %v", err)
	}
	duplicate, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionPause, 4, "canary-pause-control-001", "operator")
	if err != nil || duplicate.Run.Version != 5 {
		t.Fatalf("idempotent pause run=%+v error=%v", duplicate.Run, err)
	}

	continued, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionContinue, 5, "canary-continue-control-001", "operator")
	if err != nil {
		t.Fatalf("continue from healthy confirmation gate: %v", err)
	}
	if continued.Run.Status != "running" || continued.Run.CurrentStage != "canary_stage_1" || continued.Run.Version != 6 {
		t.Fatalf("continue advanced the canary unexpectedly: %+v", continued.Run)
	}
	if err := store.CheckResourceLock(lock); !errors.Is(err, ErrConflict) {
		t.Fatalf("continue released the canary resource lock: %v", err)
	}
	if _, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionConfirmCanaryStage, 6, "canary-confirm-after-resume", "operator"); err != nil {
		t.Fatalf("operator could not explicitly confirm after resume: %v", err)
	}
}

func TestCanaryPauseRejectsWhenRunAndHealthyCheckpointStagesDiffer(t *testing.T) {
	store, cd, run, _, _ := newCanaryConfirmationFixture(t)
	events, err := store.DeliveryRunEvents(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	run.CurrentStage = "canary_stage_2"
	run.Version = 4
	store.LoadDeliveryRuns([]DeliveryRun{run}, events)
	if _, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionPause, 4, "canary-pause-wrong-stage", "operator"); err == nil {
		t.Fatal("pause accepted a run stage that did not match the healthy checkpoint")
	}
	current, err := store.DeliveryRun(run.ID)
	if err != nil || current.Status != "running" || current.CurrentStage != "canary_stage_2" {
		t.Fatalf("rejected pause changed the run: run=%+v err=%v", current, err)
	}
}

func TestConfirmCanaryRejectsWrongPhaseStaleVersionAndNonOperatorActor(t *testing.T) {
	_, cd, run, _, _ := newCanaryConfirmationFixture(t)
	if _, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionConfirmCanaryStage, 3, "confirm-canary-stale", "operator"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale run version should conflict: %v", err)
	}
	if _, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionConfirmCanaryStage, 4, "confirm-canary-api-key", "apikey:token"); err == nil {
		t.Fatal("API-key actor was accepted")
	}
}

func TestConfirmCanaryRejectsAnExpiredMetricDecision(t *testing.T) {
	store, cd, run, _, _ := newCanaryConfirmationFixture(t)
	plan, err := store.FindDeliveryPlan(run.DeliveryPlanID)
	if err != nil {
		t.Fatal(err)
	}
	plan.Snapshot.Policy.CanaryAnalysis.MaxObservationAgeSeconds = 30
	store.LoadDeliveryPlans([]DeliveryPlan{plan})

	if _, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionConfirmCanaryStage, 4, "confirm-canary-expired", "operator"); err == nil {
		t.Fatal("operator confirmation advanced a canary stage with an expired metric decision")
	}
	checkpoint, err := store.LatestCanaryStageCheckpoint(run.ID)
	if err != nil || checkpoint == nil || checkpoint.Phase != CanaryStageAwaitingConfirm {
		t.Fatalf("expired confirmation changed the checkpoint: checkpoint=%+v err=%v", checkpoint, err)
	}
}
