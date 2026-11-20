package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeCanaryMetrics struct {
	observation CanaryMetricObservation
	err         error
	calls       int
	query       CanaryMetricsQuery
	onObserve   func()
}

func (f *fakeCanaryMetrics) ObserveCanaryVersions(_ context.Context, query CanaryMetricsQuery) (CanaryMetricObservation, error) {
	f.calls++
	f.query = query
	if f.onObserve != nil {
		f.onObserve()
	}
	return f.observation, f.err
}

func newCanaryObservationFixture(t *testing.T, now time.Time) (*Store, CanaryAnalysisPolicy, CanaryStageCheckpoint, CanaryGatewayRouteRef, CanaryGatewayRouteState) {
	t.Helper()
	policy, _, _ := canaryCheckpointFixture(now)
	started := now.Add(-2 * time.Minute)
	weights := canaryWeights(policy.Stages[0].CandidateWeightPercent)
	checkpoint := CanaryStageCheckpoint{
		SchemaVersion: CanaryStageCheckpointSchemaVersion, StageIndex: 0,
		CandidateWeightPercent:     policy.Stages[0].CandidateWeightPercent,
		ObservationDurationSeconds: policy.Stages[0].ObservationDurationSeconds,
		Phase:                      CanaryStageObserving, StageStartedAt: &started, EffectiveWeights: &weights,
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
	awaitingWeights := CanaryStageCheckpoint{
		SchemaVersion: CanaryStageCheckpointSchemaVersion, StageIndex: 0,
		CandidateWeightPercent:     policy.Stages[0].CandidateWeightPercent,
		ObservationDurationSeconds: policy.Stages[0].ObservationDurationSeconds, Phase: CanaryStageAwaitingWeights,
	}
	if _, _, err := store.RecordCanaryStageCheckpoint(31, 1, "canary:first-weights", "running", "", awaitingWeights); err != nil {
		t.Fatal(err)
	}
	recordCanaryGatewayActionEvidence(t, store, 31, 0, policy, checkpoint)
	if _, _, err := store.RecordCanaryStageCheckpoint(31, 2, "canary:first-observing", "running", "", checkpoint); err != nil {
		t.Fatal(err)
	}
	route := CanaryGatewayRouteRef{
		ConnectionID: 7, RouteID: "checkout-public", StableBackendID: "checkout-stable", CandidateBackendID: "checkout-candidate",
	}
	state := canaryGatewayState(route, "after-stage", weights)
	return store, policy, checkpoint, route, state
}

func healthyCanaryMetricsObservation(startedAt, now time.Time) CanaryMetricObservation {
	stableDigest := "sha256:" + strings.Repeat("a", 64)
	candidateDigest := "sha256:" + strings.Repeat("b", 64)
	stableP95, candidateP95 := 100.0, 105.0
	windowEnd := startedAt.Add(time.Minute)
	return CanaryMetricObservation{
		StableImageDigest: stableDigest, CandidateImageDigest: candidateDigest,
		WindowStart: startedAt, WindowEnd: windowEnd, ObservedAt: now,
		Stable:    CanaryVersionMetrics{ImageDigest: stableDigest, Requests: 200, Errors: 1, P95LatencyMillis: &stableP95, Health: "healthy"},
		Candidate: CanaryVersionMetrics{ImageDigest: candidateDigest, Requests: 200, Errors: 1, P95LatencyMillis: &candidateP95, Health: "healthy"},
	}
}

func TestObserveCanaryStagePersistsHealthyDigestBoundDecision(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	store, _, checkpoint, route, state := newCanaryObservationFixture(t, now)
	gateway := &fakeCanaryGateway{state: state}
	observation := healthyCanaryMetricsObservation(*checkpoint.StageStartedAt, now)
	observation.ObservedAt = now.Add(5 * time.Millisecond)
	metrics := &fakeCanaryMetrics{observation: observation}
	metrics.onObserve = func() { time.Sleep(10 * time.Millisecond) }

	result, err := store.observeCanaryStageAt(context.Background(), 31, "canary:metrics:stage-1", gateway, metrics, now)
	if err != nil || result.Status != CanaryStageAwaitingConfirm || result.Decision == nil ||
		result.Decision.Analysis.Status != "awaiting_confirmation" || metrics.calls != 1 || gateway.readCalls != 2 {
		t.Fatalf("observation result=%+v metrics=%d gateway reads=%d err=%v", result, metrics.calls, gateway.readCalls, err)
	}
	if metrics.query.Route != route || metrics.query.StableImageDigest != "sha256:"+strings.Repeat("a", 64) ||
		metrics.query.CandidateImageDigest != "sha256:"+strings.Repeat("b", 64) || metrics.query.WindowStartAfter != *checkpoint.StageStartedAt {
		t.Fatalf("metrics query was not bound to the approved stage identities: %+v", metrics.query)
	}
	if result.Decision.EvaluatedAt.Before(now) || result.Decision.EvaluatedAt.After(now.Add(2*time.Second)) {
		t.Fatalf("decision evaluation time=%s does not reflect the post-read decision time near %s", result.Decision.EvaluatedAt, now)
	}

	latest, err := store.LatestCanaryStageCheckpoint(8)
	if err != nil || latest == nil || latest.Phase != CanaryStageAwaitingConfirm || latest.Decision == nil ||
		!latest.Decision.EvaluatedAt.Equal(result.Decision.EvaluatedAt) || latest.Observation == nil {
		t.Fatalf("decision was not persisted for replay: checkpoint=%+v err=%v", latest, err)
	}
	duplicate, err := store.observeCanaryStageAt(context.Background(), 31, "canary:metrics:stage-1", gateway, metrics, now.Add(time.Minute))
	if err != nil || duplicate.Status != CanaryStageAwaitingConfirm || metrics.calls != 1 || gateway.readCalls != 2 {
		t.Fatalf("idempotent observation replay=%+v metrics=%d gateway reads=%d err=%v", duplicate, metrics.calls, gateway.readCalls, err)
	}
}

func TestObserveCanaryStageDoesNotAdvanceIncompleteWindow(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	store, _, checkpoint, route, state := newCanaryObservationFixture(t, now)
	observation := healthyCanaryMetricsObservation(*checkpoint.StageStartedAt, now)
	observation.WindowEnd = checkpoint.StageStartedAt.Add(30 * time.Second)
	observation.ObservedAt = observation.WindowEnd
	metrics := &fakeCanaryMetrics{observation: observation}
	result, err := store.observeCanaryStageAt(context.Background(), 31, "canary:metrics:incomplete", &fakeCanaryGateway{state: state}, metrics, now)
	if err != nil || result.Status != CanaryStageObserving || result.Outcome != "observation_window_incomplete" || metrics.calls != 1 {
		t.Fatalf("incomplete observation result=%+v calls=%d err=%v", result, metrics.calls, err)
	}
	latest, err := store.LatestCanaryStageCheckpoint(8)
	if err != nil || latest == nil || latest.Phase != CanaryStageObserving || latest.Observation != nil {
		t.Fatalf("incomplete observation changed the stage checkpoint: checkpoint=%+v err=%v", latest, err)
	}
	_ = route
}

func TestObserveCanaryStageStopsWhenGatewayChangesDuringMetricsRead(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	store, _, checkpoint, route, state := newCanaryObservationFixture(t, now)
	gateway := &fakeCanaryGateway{state: state}
	metrics := &fakeCanaryMetrics{observation: healthyCanaryMetricsObservation(*checkpoint.StageStartedAt, now)}
	metrics.onObserve = func() {
		gateway.state.Revision = "externally-changed"
	}

	result, err := store.observeCanaryStageAt(context.Background(), 31, "canary:metrics:drift", gateway, metrics, now)
	if err != nil || result.Status != CanaryStageNeedsAttention || result.Outcome != "gateway_changed_during_observation" || metrics.calls != 1 || gateway.readCalls != 2 {
		t.Fatalf("drift during metrics read result=%+v calls=%d reads=%d err=%v", result, metrics.calls, gateway.readCalls, err)
	}
	latest, err := store.LatestCanaryStageCheckpoint(8)
	if err != nil || latest == nil || latest.Phase != CanaryStageNeedsAttention || latest.Observation != nil {
		t.Fatalf("drift checkpoint=%+v err=%v", latest, err)
	}
	_ = route
}

func TestObserveCanaryStagePausesForDigestMismatchAndAdapterErrors(t *testing.T) {
	for _, test := range []struct {
		name        string
		adapterErr  error
		mutate      func(*CanaryMetricObservation)
		wantOutcome string
	}{
		{
			name: "wrong candidate identity", wantOutcome: "version_identity_mismatch",
			mutate: func(observation *CanaryMetricObservation) {
				wrong := "sha256:" + strings.Repeat("c", 64)
				observation.CandidateImageDigest = wrong
				observation.Candidate.ImageDigest = wrong
			},
		},
		{name: "metrics unavailable", adapterErr: errors.New("provider credentials must not escape"), wantOutcome: "metrics_read_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			store, _, checkpoint, route, state := newCanaryObservationFixture(t, now)
			observation := healthyCanaryMetricsObservation(*checkpoint.StageStartedAt, now)
			if test.mutate != nil {
				test.mutate(&observation)
			}
			metrics := &fakeCanaryMetrics{observation: observation, err: test.adapterErr}
			result, err := store.observeCanaryStageAt(context.Background(), 31, "canary:metrics:"+test.name, &fakeCanaryGateway{state: state}, metrics, now)
			if err != nil || result.Status != CanaryStageNeedsAttention || result.Outcome != test.wantOutcome {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			latest, err := store.LatestCanaryStageCheckpoint(8)
			if err != nil || latest == nil || latest.Phase != CanaryStageNeedsAttention {
				t.Fatalf("latest checkpoint=%+v err=%v", latest, err)
			}
			if strings.Contains(latestEventDetail(t, store), "provider credentials") {
				t.Fatal("provider error details leaked to the run timeline")
			}
			_ = route
		})
	}
}

func latestEventDetail(t *testing.T, store *Store) string {
	t.Helper()
	events, err := store.DeliveryRunEvents(8)
	if err != nil || len(events) == 0 {
		t.Fatalf("read run events: events=%+v err=%v", events, err)
	}
	return events[len(events)-1].Detail
}
