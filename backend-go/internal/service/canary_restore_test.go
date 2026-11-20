package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRestoreCanaryStageStableJournalsReadbackAndKeepsRunLock(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	store, _, _, route, state := newCanaryObservationFixture(t, now)
	store.mu.Lock()
	store.resourceLocks[workloadResourceLock(DeployLog{ID: 31, ClusterID: 1, Namespace: "prod", DeploymentName: "checkout"}).Identity()] = 31
	store.mu.Unlock()
	adapter := &fakeCanaryGateway{state: state, applyWrites: true}

	result, err := store.RestoreCanaryStageStable(context.Background(), 31, "restore-canary-001", adapter)
	if err != nil || result.Status != "awaiting_restore_verification" || result.Outcome != "stable_weights_readback_confirmed" ||
		result.Checkpoint == nil || result.Checkpoint.EffectiveWeights != (CanaryTrafficWeights{StableWeightPercent: 100}) ||
		result.Checkpoint.Route != route || adapter.setCalls != 1 || adapter.readCalls != 2 {
		t.Fatalf("restore result=%+v set=%d reads=%d err=%v", result, adapter.setCalls, adapter.readCalls, err)
	}
	if adapter.lastIdempotencyKey != "canary:31:restore" {
		t.Fatalf("gateway restore used unexpected idempotency key %q", adapter.lastIdempotencyKey)
	}
	actions, err := store.ExecutionActions(31)
	if err != nil || len(actions) != 2 || actions[1].ActionKey != canaryGatewayRestoreActionKey || actions[1].Status != ExecutionActionApplied {
		t.Fatalf("restore action was not durably applied: actions=%+v err=%v", actions, err)
	}
	run, err := store.DeliveryRunForDeployLog(31)
	if err != nil || run.Status != "running" || run.CurrentStage != "awaiting_restore_verification" {
		t.Fatalf("restore prematurely completed the release: run=%+v err=%v", run, err)
	}
	if owner, locked := store.resourceLocks[(ResourceLock{ClusterID: 1, Namespace: "prod", Kind: "deployment", Name: "checkout"}).Identity()]; !locked || owner != 31 {
		t.Fatalf("restore released the active run lock: owner=%d locked=%v", owner, locked)
	}
	events, err := store.DeliveryRunEvents(run.ID)
	if err != nil || len(events) == 0 || events[len(events)-1].EventType != "canary_restore" {
		t.Fatalf("restore progress was not persisted: events=%+v err=%v", events, err)
	}
	decoded, err := DecodeCanaryRestoreCheckpoint(events[len(events)-1])
	if err != nil || decoded.RouteRevision != "revision-2" || decoded.EffectiveWeights != (CanaryTrafficWeights{StableWeightPercent: 100}) {
		t.Fatalf("restore readback snapshot=%+v err=%v", decoded, err)
	}
	plan, err := store.FindDeliveryPlan(run.DeliveryPlanID)
	if err != nil || validateCanaryRestoreCheckpointAgainstPlan(decoded, plan) != nil {
		t.Fatalf("restore snapshot did not remain bound to the plan: plan=%+v err=%v", plan, err)
	}

	duplicate, err := store.RestoreCanaryStageStable(context.Background(), 31, "restore-canary-001", adapter)
	if err != nil || duplicate.Status != "awaiting_restore_verification" || adapter.setCalls != 1 || adapter.readCalls != 2 {
		t.Fatalf("duplicate restore=%+v set=%d reads=%d err=%v", duplicate, adapter.setCalls, adapter.readCalls, err)
	}
}

func TestRestoreCanaryStageStableAfterConfirmationUsesLastAppliedStageReadback(t *testing.T) {
	store, cd, run, _, confirmed := newCanaryConfirmationFixture(t)
	view, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionConfirmCanaryStage, 4, "confirm-stage-before-restore", "operator")
	if err != nil || view.Run.CurrentStage != "canary_stage_2" {
		t.Fatalf("advance to next stage: view=%+v err=%v", view.Run, err)
	}

	route := CanaryGatewayRouteRef{
		ConnectionID: 7, RouteID: "checkout-public", StableBackendID: "checkout-stable", CandidateBackendID: "checkout-candidate",
	}
	adapter := &fakeCanaryGateway{
		state:       canaryGatewayState(route, "after-stage", *confirmed.EffectiveWeights),
		applyWrites: true,
	}
	result, err := store.RestoreCanaryStageStable(context.Background(), run.DeployLogID, "restore-after-confirm", adapter)
	if err != nil || result.Status != "awaiting_restore_verification" || result.StageIndex != 0 ||
		result.Checkpoint == nil || adapter.setCalls != 1 || adapter.readCalls != 2 ||
		adapter.state.Weights != (CanaryTrafficWeights{StableWeightPercent: 100}) {
		t.Fatalf("stage-boundary restore=%+v gateway=%+v set=%d reads=%d err=%v", result, adapter.state, adapter.setCalls, adapter.readCalls, err)
	}
	if result.Checkpoint.StageIndex != 0 {
		t.Fatalf("restore checkpoint must identify the last stage with applied traffic, got stage %d", result.Checkpoint.StageIndex)
	}
	intent, err := canaryStageActionIntent(0, mustExecutionActions(t, store, run.DeployLogID))
	if err != nil || intent.Route != route {
		t.Fatalf("recovery route was not derived from the applied stage action: intent=%+v err=%v", intent, err)
	}
}

func TestRestoreCanaryStageStableAfterConfirmationRefusesGatewayDrift(t *testing.T) {
	store, cd, run, _, confirmed := newCanaryConfirmationFixture(t)
	if _, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionConfirmCanaryStage, 4, "confirm-stage-drift", "operator"); err != nil {
		t.Fatal(err)
	}
	route := CanaryGatewayRouteRef{
		ConnectionID: 7, RouteID: "checkout-public", StableBackendID: "checkout-stable", CandidateBackendID: "checkout-candidate",
	}
	drifted := CanaryTrafficWeights{StableWeightPercent: 80, CandidateWeightPercent: 20}
	state := canaryGatewayState(route, "externally-changed", drifted)
	adapter := &fakeCanaryGateway{state: state, applyWrites: true}
	result, err := store.RestoreCanaryStageStable(context.Background(), run.DeployLogID, "restore-after-confirm-drift", adapter)
	if err != nil || result.Status != CanaryStageNeedsAttention || result.Outcome != "gateway_weights_drifted" || adapter.setCalls != 0 {
		t.Fatalf("drifted stage-boundary restore=%+v set=%d err=%v", result, adapter.setCalls, err)
	}
	if *confirmed.EffectiveWeights == drifted {
		t.Fatal("drift fixture unexpectedly matches the last confirmed stage")
	}
}

func TestRestoreCanaryStageStableReconcilesAppliedNextStageBeforeCheckpoint(t *testing.T) {
	store, cd, run, policy, _ := newCanaryConfirmationFixture(t)
	if _, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionConfirmCanaryStage, 4, "confirm-stage-before-crash", "operator"); err != nil {
		t.Fatal(err)
	}
	latest, err := store.LatestCanaryStageCheckpoint(run.ID)
	if err != nil || latest == nil || latest.Phase != CanaryStageAwaitingWeights || latest.StageIndex != 1 {
		t.Fatalf("expected next-stage checkpoint: checkpoint=%+v err=%v", latest, err)
	}
	started := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	effective := canaryWeights(policy.Stages[1].CandidateWeightPercent)
	appliedEvidence := *latest
	appliedEvidence.Phase = CanaryStageObserving
	appliedEvidence.StageStartedAt = &started
	appliedEvidence.EffectiveWeights = &effective
	// Model a crash after the stage action was durably marked applied but before
	// its observing checkpoint was appended to the run timeline.
	recordCanaryGatewayActionEvidence(t, store, run.DeployLogID, 1, policy, appliedEvidence)

	route := CanaryGatewayRouteRef{
		ConnectionID: 7, RouteID: "checkout-public", StableBackendID: "checkout-stable", CandidateBackendID: "checkout-candidate",
	}
	adapter := &fakeCanaryGateway{
		state:       canaryGatewayState(route, "after-stage-2", effective),
		applyWrites: true,
	}
	result, err := store.RestoreCanaryStageStable(context.Background(), run.DeployLogID, "restore-after-stage-write", adapter)
	if err != nil || result.Status != "awaiting_restore_verification" || result.StageIndex != 1 ||
		result.Checkpoint == nil || adapter.setCalls != 1 || adapter.state.Weights != (CanaryTrafficWeights{StableWeightPercent: 100}) {
		t.Fatalf("restore from applied action=%+v state=%+v set=%d err=%v", result, adapter.state, adapter.setCalls, err)
	}
}

func mustExecutionActions(t *testing.T, store *Store, logID int64) []ExecutionAction {
	t.Helper()
	actions, err := store.ExecutionActions(logID)
	if err != nil {
		t.Fatal(err)
	}
	return actions
}

func TestRestoreCanaryStageReconcilesAppliedWriteAfterReadbackFailure(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	store, _, _, _, state := newCanaryObservationFixture(t, now)
	adapter := &fakeCanaryGateway{
		state: state, applyWrites: true,
		setErr: errors.New("gateway response lost after write"), readFailures: map[int]bool{2: true},
	}
	first, err := store.RestoreCanaryStageStable(context.Background(), 31, "restore-canary-retry", adapter)
	if err != nil || first.Status != CanaryStageNeedsAttention || first.Outcome != "readback_failed" || adapter.setCalls != 1 {
		t.Fatalf("ambiguous first restore=%+v set=%d err=%v", first, adapter.setCalls, err)
	}
	actions, err := store.ExecutionActions(31)
	if err != nil || len(actions) != 2 || actions[1].Status != ExecutionActionUnknown {
		t.Fatalf("restore write outcome should remain unknown until readback: actions=%+v err=%v", actions, err)
	}
	adapter.readFailures = nil
	second, err := store.RestoreCanaryStageStable(context.Background(), 31, "restore-canary-retry-2", adapter)
	if err != nil || second.Status != "awaiting_restore_verification" || adapter.setCalls != 1 {
		t.Fatalf("restore reconciliation=%+v set=%d err=%v", second, adapter.setCalls, err)
	}
}

func TestRestoreCanaryStageDoesNotOverwriteUnexpectedGatewayWeights(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	store, _, _, route, state := newCanaryObservationFixture(t, now)
	state.Weights = CanaryTrafficWeights{StableWeightPercent: 80, CandidateWeightPercent: 20}
	adapter := &fakeCanaryGateway{state: state, applyWrites: true}
	result, err := store.RestoreCanaryStageStable(context.Background(), 31, "restore-canary-drift", adapter)
	if err != nil || result.Status != CanaryStageNeedsAttention || result.Outcome != "gateway_weights_drifted" || adapter.setCalls != 0 {
		t.Fatalf("drifted route was overwritten: result=%+v set=%d err=%v", result, adapter.setCalls, err)
	}
	run, err := store.DeliveryRunForDeployLog(31)
	if err != nil || run.CurrentStage != "needs_attention" || run.Status != "running" {
		t.Fatalf("route drift did not retain the run for reconciliation: run=%+v err=%v", run, err)
	}
	events, err := store.DeliveryRunEvents(run.ID)
	if err != nil || len(events) == 0 || !strings.Contains(events[len(events)-1].Detail, "请核对当前流量") {
		t.Fatalf("operator event did not explain the route drift: events=%+v err=%v", events, err)
	}
	if !validCanaryGatewayRouteRef(route) {
		t.Fatal("fixture returned an invalid route")
	}
}

func TestRestoreCanaryStageDoesNotBlindlyRetryKnownNotAppliedWrite(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	store, _, _, _, state := newCanaryObservationFixture(t, now)
	adapter := &fakeCanaryGateway{state: state}
	first, err := store.RestoreCanaryStageStable(context.Background(), 31, "restore-canary-not-applied", adapter)
	if err != nil || first.Status != CanaryStageNeedsAttention || first.Outcome != "restore_write_not_effective" || adapter.setCalls != 1 {
		t.Fatalf("not-applied restore=%+v set=%d err=%v", first, adapter.setCalls, err)
	}
	second, err := store.RestoreCanaryStageStable(context.Background(), 31, "restore-canary-not-applied-retry", adapter)
	if err != nil || second.Status != CanaryStageNeedsAttention || adapter.setCalls != 1 {
		t.Fatalf("known not-applied restore was blindly retried: result=%+v set=%d err=%v", second, adapter.setCalls, err)
	}
}

func TestConfirmCanaryRestoreBusinessHealthEndsFailedRunAndReleasesLock(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	store, _, _, _, state := newCanaryObservationFixture(t, now)
	log, err := store.FindLog(31)
	if err != nil {
		t.Fatal(err)
	}
	lock := workloadResourceLock(log)
	store.mu.Lock()
	store.resourceLocks[lock.Identity()] = log.ID
	store.mu.Unlock()
	adapter := &fakeCanaryGateway{state: state, applyWrites: true}
	if _, err := store.RestoreCanaryStageStable(context.Background(), log.ID, "restore-canary-verify-001", adapter); err != nil {
		t.Fatalf("restore stable traffic: %v", err)
	}
	run, err := store.DeliveryRunForDeployLog(log.ID)
	if err != nil {
		t.Fatal(err)
	}
	cd := &CDService{store: store}
	view, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionConfirmCanaryRestore, run.Version, "confirm-restore-001", "operator")
	if err != nil {
		t.Fatalf("confirm business health after restore: %v", err)
	}
	if view.Run.Status != "failed" || view.Run.CurrentStage != "restore_verified" || view.Deploy.Status != "failed" ||
		view.Deploy.FinishedAt == nil || view.Deploy.ErrorMessage == nil || !strings.Contains(*view.Deploy.ErrorMessage, "业务验证通过") {
		t.Fatalf("verification should end the failed candidate release: run=%+v deploy=%+v", view.Run, view.Deploy)
	}
	if len(view.Events) == 0 || view.Events[len(view.Events)-1].EventType != "canary_restore_verification" ||
		!strings.Contains(view.Events[len(view.Events)-1].Detail, "actor=operator") {
		t.Fatalf("business verification actor was not recorded: %+v", view.Events)
	}
	store.mu.RLock()
	_, locked := store.resourceLocks[lock.Identity()]
	store.mu.RUnlock()
	if locked {
		t.Fatal("terminal verified recovery retained the resource lock")
	}
	duplicate, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionConfirmCanaryRestore, run.Version, "confirm-restore-001", "operator")
	if err != nil || duplicate.Run.Version != view.Run.Version {
		t.Fatalf("idempotent business verification view=%+v err=%v", duplicate.Run, err)
	}
	if _, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionConfirmCanaryRestore, view.Run.Version, "confirm-restore-002", "operator"); err == nil {
		t.Fatal("a new verification action must not run after the release has ended")
	}
}

func TestConfirmCanaryRestoreRequiresVerifiedRestoreAndHumanActor(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	store, _, _, _, _ := newCanaryObservationFixture(t, now)
	run, err := store.DeliveryRunForDeployLog(31)
	if err != nil {
		t.Fatal(err)
	}
	cd := &CDService{store: store}
	if _, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionConfirmCanaryRestore, run.Version, "confirm-restore-before", "operator"); err == nil {
		t.Fatal("business health was confirmed before stable weight readback")
	}
	if _, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionConfirmCanaryRestore, run.Version, "confirm-restore-machine", "apikey:runner"); err == nil {
		t.Fatal("machine actor was allowed to confirm business health")
	}
}
