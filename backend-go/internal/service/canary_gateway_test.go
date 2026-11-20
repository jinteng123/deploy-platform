package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type fakeCanaryGateway struct {
	state              CanaryGatewayRouteState
	setCalls           int
	readCalls          int
	readFailures       map[int]bool
	applyWrites        bool
	setErr             error
	beforeSet          func() error
	lastIdempotencyKey string
}

func (f *fakeCanaryGateway) ReadCanaryRoute(context.Context, CanaryGatewayRouteRef) (CanaryGatewayRouteState, error) {
	f.readCalls++
	if f.readFailures[f.readCalls] {
		return CanaryGatewayRouteState{}, errors.New("simulated gateway read timeout")
	}
	return f.state, nil
}

func (f *fakeCanaryGateway) SetCanaryRouteWeights(_ context.Context, _ CanaryGatewayRouteRef, expectedRevision string, desired CanaryTrafficWeights, idempotencyKey string) error {
	f.setCalls++
	f.lastIdempotencyKey = idempotencyKey
	if f.beforeSet != nil {
		if err := f.beforeSet(); err != nil {
			return err
		}
	}
	if f.state.Revision != expectedRevision {
		return errors.New("simulated conditional write conflict")
	}
	if f.applyWrites {
		f.state.Weights = desired
		f.state.Revision = "revision-2"
	}
	return f.setErr
}

func TestApplyCanaryStageWeightsJournalsIntentAndRequiresReadback(t *testing.T) {
	store, route := canaryGatewayStore(t)
	adapter := &fakeCanaryGateway{
		state:       canaryGatewayState(route, "revision-1", CanaryTrafficWeights{StableWeightPercent: 100}),
		applyWrites: true,
		setErr:      errors.New("simulated response lost after gateway applied update"),
	}
	adapter.beforeSet = func() error {
		actions, err := store.ExecutionActions(31)
		if err != nil {
			return err
		}
		if len(actions) != 1 || actions[0].Status != ExecutionActionIntent {
			return errors.New("gateway was called before the durable intent was recorded")
		}
		var intent canaryGatewayActionIntent
		if err := json.Unmarshal(actions[0].Intent, &intent); err != nil {
			return err
		}
		if intent.ExpectedRevision != "revision-1" || intent.ExpectedWeights.StableWeightPercent != 100 ||
			intent.DesiredWeights != (CanaryTrafficWeights{StableWeightPercent: 90, CandidateWeightPercent: 10}) {
			return errors.New("durable intent did not capture the exact conditional change")
		}
		return nil
	}

	result, err := store.ApplyCanaryStageWeights(context.Background(), 31, route, adapter)
	if err != nil || result.Status != CanaryStageObserving || result.EffectiveWeights == nil || adapter.setCalls != 1 || adapter.readCalls != 2 || adapter.lastIdempotencyKey != "canary:31:stage:1" {
		t.Fatalf("apply result=%+v set=%d reads=%d err=%v", result, adapter.setCalls, adapter.readCalls, err)
	}
	actions, err := store.ExecutionActions(31)
	if err != nil || len(actions) != 1 || actions[0].Status != ExecutionActionApplied || actions[0].OutcomeCode != "readback_confirmed" {
		t.Fatalf("execution action=%+v err=%v", actions, err)
	}
	checkpoint, err := store.LatestCanaryStageCheckpoint(8)
	if err != nil || checkpoint == nil || checkpoint.Phase != CanaryStageObserving || checkpoint.StageStartedAt == nil ||
		checkpoint.EffectiveWeights == nil || *checkpoint.EffectiveWeights != adapter.state.Weights {
		t.Fatalf("persisted checkpoint=%+v err=%v", checkpoint, err)
	}

	// A duplicate request after the checkpoint must not write the gateway again.
	duplicate, err := store.ApplyCanaryStageWeights(context.Background(), 31, route, adapter)
	if err != nil || duplicate.Status != CanaryStageObserving || adapter.setCalls != 1 || adapter.readCalls != 2 {
		t.Fatalf("duplicate apply=%+v set=%d reads=%d err=%v", duplicate, adapter.setCalls, adapter.readCalls, err)
	}
}

func TestApplyCanaryStageWeightsRejectsRouteChangeBetweenStages(t *testing.T) {
	store, cd, run, _, _ := newCanaryConfirmationFixture(t)
	if _, err := cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionConfirmCanaryStage, 4, "confirm-route-pinned-stage", "operator"); err != nil {
		t.Fatal(err)
	}

	actions, err := store.ExecutionActions(run.DeployLogID)
	if err != nil || len(actions) != 1 {
		t.Fatalf("read first-stage gateway action: actions=%+v err=%v", actions, err)
	}
	var firstIntent canaryGatewayActionIntent
	if err := json.Unmarshal(actions[0].Intent, &firstIntent); err != nil {
		t.Fatal(err)
	}
	otherRoute := firstIntent.Route
	otherRoute.RouteID = "another-service-route"
	adapter := &fakeCanaryGateway{state: canaryGatewayState(otherRoute, "revision-2", CanaryTrafficWeights{StableWeightPercent: 90, CandidateWeightPercent: 10})}

	if _, err := store.ApplyCanaryStageWeights(context.Background(), run.DeployLogID, otherRoute, adapter); err == nil {
		t.Fatal("later stage accepted a different route after the first stage pinned the run identity")
	}
	if adapter.readCalls != 0 || adapter.setCalls != 0 {
		t.Fatalf("route mismatch reached gateway adapter: reads=%d writes=%d", adapter.readCalls, adapter.setCalls)
	}
}

func TestApplyCanaryStageWeightsLeavesUnchangedRouteForAttention(t *testing.T) {
	store, route := canaryGatewayStore(t)
	adapter := &fakeCanaryGateway{
		state: canaryGatewayState(route, "revision-1", CanaryTrafficWeights{StableWeightPercent: 100}),
	}
	result, err := store.ApplyCanaryStageWeights(context.Background(), 31, route, adapter)
	if err != nil || result.Status != CanaryStageNeedsAttention || result.OutcomeCode != "write_not_effective" || adapter.setCalls != 1 || adapter.readCalls != 2 {
		t.Fatalf("apply result=%+v set=%d reads=%d err=%v", result, adapter.setCalls, adapter.readCalls, err)
	}
	actions, err := store.ExecutionActions(31)
	if err != nil || len(actions) != 1 || actions[0].Status != ExecutionActionNotApplied {
		t.Fatalf("execution action=%+v err=%v", actions, err)
	}
	checkpoint, err := store.LatestCanaryStageCheckpoint(8)
	if err != nil || checkpoint == nil || checkpoint.Phase != CanaryStageNeedsAttention {
		t.Fatalf("latest checkpoint=%+v err=%v", checkpoint, err)
	}
	_, err = store.ApplyCanaryStageWeights(context.Background(), 31, route, adapter)
	if err != nil || adapter.setCalls != 1 {
		t.Fatalf("known-not-applied action was automatically replayed: set=%d err=%v", adapter.setCalls, err)
	}
}

func TestApplyCanaryStageWeightsReconcilesLostReadbackWithoutSecondWrite(t *testing.T) {
	store, route := canaryGatewayStore(t)
	adapter := &fakeCanaryGateway{
		state:        canaryGatewayState(route, "revision-1", CanaryTrafficWeights{StableWeightPercent: 100}),
		applyWrites:  true,
		readFailures: map[int]bool{2: true},
	}
	result, err := store.ApplyCanaryStageWeights(context.Background(), 31, route, adapter)
	if err != nil || result.Status != CanaryStageNeedsAttention || result.OutcomeCode != "readback_failed" || adapter.setCalls != 1 {
		t.Fatalf("first apply=%+v set=%d err=%v", result, adapter.setCalls, err)
	}
	actions, err := store.ExecutionActions(31)
	if err != nil || len(actions) != 1 || actions[0].Status != ExecutionActionUnknown {
		t.Fatalf("uncertain action=%+v err=%v", actions, err)
	}

	adapter.readFailures = nil
	result, err = store.ApplyCanaryStageWeights(context.Background(), 31, route, adapter)
	if err != nil || result.Status != CanaryStageObserving || adapter.setCalls != 1 {
		t.Fatalf("reconcile=%+v set=%d err=%v", result, adapter.setCalls, err)
	}
	actions, err = store.ExecutionActions(31)
	if err != nil || len(actions) != 1 || actions[0].Status != ExecutionActionApplied {
		t.Fatalf("reconciled action=%+v err=%v", actions, err)
	}
}

func TestApplyCanaryStageWeightsRejectsRouteDriftBeforeWrite(t *testing.T) {
	store, route := canaryGatewayStore(t)
	state := canaryGatewayState(route, "revision-1", CanaryTrafficWeights{StableWeightPercent: 80, CandidateWeightPercent: 20})
	adapter := &fakeCanaryGateway{state: state, applyWrites: true}
	result, err := store.ApplyCanaryStageWeights(context.Background(), 31, route, adapter)
	if err != nil || result.Status != CanaryStageNeedsAttention || result.OutcomeCode != "route_weights_drifted" || adapter.setCalls != 0 {
		t.Fatalf("drift result=%+v set=%d err=%v", result, adapter.setCalls, err)
	}
	if actions, err := store.ExecutionActions(31); err != nil || len(actions) != 0 {
		t.Fatalf("drift unexpectedly created a write action: actions=%+v err=%v", actions, err)
	}
}

func canaryGatewayStore(t *testing.T) (*Store, CanaryGatewayRouteRef) {
	t.Helper()
	policy, _, _ := canaryCheckpointFixture(time.Now().UTC().Truncate(time.Second))
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
		SchemaVersion: CanaryStageCheckpointSchemaVersion, StageIndex: 0,
		CandidateWeightPercent: 10, ObservationDurationSeconds: 60, Phase: CanaryStageAwaitingWeights,
	}
	if _, found, err := store.RecordCanaryStageCheckpoint(31, 1, "canary:stage-1:awaiting-weights", "running", "等待应用网关阶段权重。", awaitingWeights); err != nil || !found {
		t.Fatalf("seed awaiting-weights checkpoint found=%v err=%v", found, err)
	}
	return store, *policy.GatewayRoute
}

func TestApplyCanaryStageWeightsRequiresIdentityFromImmutablePlan(t *testing.T) {
	store, route := canaryGatewayStore(t)
	otherRoute := route
	otherRoute.RouteID = "another-service-route"
	adapter := &fakeCanaryGateway{state: canaryGatewayState(otherRoute, "revision-1", CanaryTrafficWeights{StableWeightPercent: 100})}
	if _, err := store.ApplyCanaryStageWeights(context.Background(), 31, otherRoute, adapter); err == nil {
		t.Fatal("caller supplied a gateway route that differs from the immutable policy snapshot")
	}
	if adapter.readCalls != 0 || adapter.setCalls != 0 {
		t.Fatalf("route mismatch must be rejected before gateway I/O: reads=%d writes=%d", adapter.readCalls, adapter.setCalls)
	}
}

func TestApplyCanaryStageWeightsRejectsMissingPlanIdentityBeforeGatewayIO(t *testing.T) {
	store, route := canaryGatewayStore(t)
	plan, err := store.FindDeliveryPlan(12)
	if err != nil {
		t.Fatal(err)
	}
	plan.Snapshot.Policy.CanaryAnalysis.GatewayRoute = nil
	store.LoadDeliveryPlans([]DeliveryPlan{plan})
	adapter := &fakeCanaryGateway{state: canaryGatewayState(route, "revision-1", CanaryTrafficWeights{StableWeightPercent: 100})}
	if _, err := store.ApplyCanaryStageWeights(context.Background(), 31, route, adapter); err == nil {
		t.Fatal("gateway action ran without route identity in the immutable plan")
	}
	if adapter.readCalls != 0 || adapter.setCalls != 0 {
		t.Fatalf("missing plan identity must be rejected before gateway I/O: reads=%d writes=%d", adapter.readCalls, adapter.setCalls)
	}
}

func canaryGatewayState(route CanaryGatewayRouteRef, revision string, weights CanaryTrafficWeights) CanaryGatewayRouteState {
	return CanaryGatewayRouteState{
		ConnectionID: route.ConnectionID, RouteID: route.RouteID, Revision: revision,
		StableBackendID: route.StableBackendID, CandidateBackendID: route.CandidateBackendID, Weights: weights,
	}
}
