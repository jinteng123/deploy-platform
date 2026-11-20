package service

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestEvaluateCanaryObservationFailsClosedAndRequiresHumanConfirmation(t *testing.T) {
	now := time.Date(2026, 10, 11, 8, 0, 0, 0, time.UTC)
	policy := testCanaryAnalysisPolicy()
	valid := testCanaryObservation(now)

	tests := []struct {
		name       string
		mutate     func(*CanaryMetricObservation)
		wantStatus string
		wantReason string
	}{
		{name: "healthy data waits for human", wantStatus: "awaiting_confirmation", wantReason: "within_thresholds_manual_confirmation_required"},
		{name: "stale window pauses", mutate: func(o *CanaryMetricObservation) {
			o.WindowEnd = now.Add(-4 * time.Minute)
			o.WindowStart = o.WindowEnd.Add(-time.Minute)
			o.ObservedAt = o.WindowEnd.Add(time.Second)
		}, wantStatus: "pause", wantReason: "observation_stale_or_future"},
		{name: "future observation pauses", mutate: func(o *CanaryMetricObservation) { o.ObservedAt = now.Add(time.Second) }, wantStatus: "pause", wantReason: "observation_stale_or_future"},
		{name: "window after observation pauses", mutate: func(o *CanaryMetricObservation) { o.ObservedAt = o.WindowEnd.Add(-time.Second) }, wantStatus: "pause", wantReason: "observation_window_invalid"},
		{name: "tag is not an immutable version identity", mutate: func(o *CanaryMetricObservation) { o.StableImageDigest = "registry/app:latest" }, wantStatus: "pause", wantReason: "version_identity_invalid"},
		{name: "full image reference is not a digest", mutate: func(o *CanaryMetricObservation) { o.Candidate.ImageDigest = "registry/app@" + canaryDigest("2") }, wantStatus: "pause", wantReason: "candidate_metrics_invalid"},
		{name: "valid but different metric digest pauses", mutate: func(o *CanaryMetricObservation) { o.Candidate.ImageDigest = canaryDigest("3") }, wantStatus: "pause", wantReason: "version_identity_mismatch"},
		{name: "short digest pauses", mutate: func(o *CanaryMetricObservation) { o.CandidateImageDigest = "sha256:1234" }, wantStatus: "pause", wantReason: "version_identity_invalid"},
		{name: "same versions pause", mutate: func(o *CanaryMetricObservation) {
			o.CandidateImageDigest = o.StableImageDigest
			o.Candidate.ImageDigest = o.Stable.ImageDigest
		}, wantStatus: "pause", wantReason: "stable_and_candidate_versions_match"},
		{name: "insufficient candidate samples pause", mutate: func(o *CanaryMetricObservation) { o.Candidate.Requests = policy.MinRequests - 1 }, wantStatus: "pause", wantReason: "insufficient_version_samples"},
		{name: "missing latency data pauses", mutate: func(o *CanaryMetricObservation) { o.Candidate.P95LatencyMillis = nil }, wantStatus: "pause", wantReason: "candidate_metrics_invalid"},
		{name: "stable baseline degradation pauses", mutate: func(o *CanaryMetricObservation) { o.Stable.Health = "degraded" }, wantStatus: "pause", wantReason: "stable_baseline_not_healthy"},
		{name: "unknown candidate health pauses", mutate: func(o *CanaryMetricObservation) { o.Candidate.Health = "unknown" }, wantStatus: "pause", wantReason: "candidate_health_unknown"},
		{name: "candidate health breach recommends restore", mutate: func(o *CanaryMetricObservation) { o.Candidate.Health = "degraded" }, wantStatus: "restore_recommended", wantReason: "candidate_health_degraded"},
		{name: "absolute error threshold breach recommends restore", mutate: func(o *CanaryMetricObservation) { o.Candidate.Errors = 80 }, wantStatus: "restore_recommended", wantReason: "candidate_threshold_breached"},
		{name: "error increase breach recommends restore", mutate: func(o *CanaryMetricObservation) { o.Candidate.Errors = 35 }, wantStatus: "restore_recommended", wantReason: "candidate_threshold_breached"},
		{name: "absolute latency breach recommends restore", mutate: func(o *CanaryMetricObservation) { setCanaryP95(&o.Candidate, 260) }, wantStatus: "restore_recommended", wantReason: "candidate_threshold_breached"},
		{name: "relative latency breach recommends restore", mutate: func(o *CanaryMetricObservation) { setCanaryP95(&o.Candidate, 160) }, wantStatus: "restore_recommended", wantReason: "candidate_threshold_breached"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := valid
			if test.mutate != nil {
				test.mutate(&observation)
			}
			decision, err := EvaluateCanaryObservation(policy, observation, now)
			if err != nil {
				t.Fatalf("EvaluateCanaryObservation() error = %v", err)
			}
			if decision.Status != test.wantStatus || decision.Reason != test.wantReason {
				t.Fatalf("decision = %+v, want status=%q reason=%q", decision, test.wantStatus, test.wantReason)
			}
			if test.wantStatus == "awaiting_confirmation" && (decision.CandidateErrorRate == nil || *decision.CandidateErrorRate != 0.02 || decision.CandidateP95LatencyMillis == nil || *decision.CandidateP95LatencyMillis != 120) {
				t.Fatalf("healthy decision must report measured values: %+v", decision)
			}
			if test.wantStatus == "pause" && (decision.CandidateErrorRate != nil || decision.CandidateP95LatencyMillis != nil) {
				t.Fatalf("paused result must not encode unknown values as zero: %+v", decision)
			}
		})
	}
}

func TestEvaluateCanaryObservationRequiresExplicitValidPolicy(t *testing.T) {
	now := time.Date(2026, 10, 11, 8, 0, 0, 0, time.UTC)
	validPolicy := testCanaryAnalysisPolicy()
	validObservation := testCanaryObservation(now)

	tests := []struct {
		name   string
		policy CanaryAnalysisPolicy
	}{
		{name: "missing thresholds", policy: CanaryAnalysisPolicy{MinRequests: 100, MaxObservationAgeSeconds: 120}},
		{name: "zero sample floor", policy: func() CanaryAnalysisPolicy { p := testCanaryAnalysisPolicy(); p.MinRequests = 0; return p }()},
		{name: "invalid error rate", policy: func() CanaryAnalysisPolicy {
			p := testCanaryAnalysisPolicy()
			value := 1.1
			p.MaxCandidateErrorRate = &value
			return p
		}()},
		{name: "negative latency threshold", policy: func() CanaryAnalysisPolicy {
			p := testCanaryAnalysisPolicy()
			value := -1.0
			p.MaxCandidateP95LatencyMillis = &value
			return p
		}()},
		{name: "missing stages", policy: func() CanaryAnalysisPolicy {
			p := testCanaryAnalysisPolicy()
			p.Stages = nil
			return p
		}()},
		{name: "final stage is below full traffic", policy: func() CanaryAnalysisPolicy {
			p := testCanaryAnalysisPolicy()
			p.Stages[len(p.Stages)-1].CandidateWeightPercent = 99
			return p
		}()},
		{name: "stages are not strictly increasing", policy: func() CanaryAnalysisPolicy {
			p := testCanaryAnalysisPolicy()
			p.Stages[1].CandidateWeightPercent = p.Stages[0].CandidateWeightPercent
			return p
		}()},
		{name: "stage has no observation window", policy: func() CanaryAnalysisPolicy {
			p := testCanaryAnalysisPolicy()
			p.Stages[0].ObservationDurationSeconds = 0
			return p
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := EvaluateCanaryObservation(test.policy, validObservation, now); err == nil {
				t.Fatal("invalid or incomplete canary policy was accepted")
			}
		})
	}

	badMetric := validObservation
	badMetric.Candidate.P95LatencyMillis = floatPointer(math.NaN())
	decision, err := EvaluateCanaryObservation(validPolicy, badMetric, now)
	if err != nil || decision.Status != "pause" || decision.Reason != "candidate_metrics_invalid" {
		t.Fatalf("non-finite metric must pause: decision=%+v err=%v", decision, err)
	}
}

func TestEvaluateCanaryStageObservationRequiresEffectiveWeightAndFullWindow(t *testing.T) {
	now := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	policy := testCanaryAnalysisPolicy()
	stageStartedAt := now.Add(-2 * time.Minute)
	observation := testCanaryObservation(now)

	tests := []struct {
		name              string
		stageIndex        int
		stageStartedAt    time.Time
		effectiveWeights  CanaryTrafficWeights
		mutateObservation func(*CanaryMetricObservation)
		wantStatus        string
		wantReason        string
	}{
		{name: "effective weight and complete stage wait for operator", stageIndex: 0, stageStartedAt: stageStartedAt, effectiveWeights: CanaryTrafficWeights{StableWeightPercent: 90, CandidateWeightPercent: 10}, wantStatus: "awaiting_confirmation", wantReason: "within_thresholds_manual_confirmation_required"},
		{name: "requested but ineffective weight pauses", stageIndex: 0, stageStartedAt: stageStartedAt, effectiveWeights: CanaryTrafficWeights{StableWeightPercent: 100}, wantStatus: "pause", wantReason: "gateway_weight_not_effective"},
		{name: "candidate weight alone does not prove the route is correct", stageIndex: 0, stageStartedAt: stageStartedAt, effectiveWeights: CanaryTrafficWeights{StableWeightPercent: 30, CandidateWeightPercent: 10}, wantStatus: "pause", wantReason: "gateway_weight_not_effective"},
		{name: "stage dwell has not elapsed", stageIndex: 0, stageStartedAt: now.Add(-30 * time.Second), effectiveWeights: CanaryTrafficWeights{StableWeightPercent: 90, CandidateWeightPercent: 10}, wantStatus: "pause", wantReason: "stage_observation_window_incomplete"},
		{name: "metric window is shorter than stage dwell", stageIndex: 0, stageStartedAt: stageStartedAt, effectiveWeights: CanaryTrafficWeights{StableWeightPercent: 90, CandidateWeightPercent: 10}, mutateObservation: func(o *CanaryMetricObservation) {
			o.WindowStart = now.Add(-60 * time.Second)
			o.WindowEnd = now.Add(-30 * time.Second)
			o.ObservedAt = o.WindowEnd.Add(time.Second)
		}, wantStatus: "pause", wantReason: "stage_observation_window_incomplete"},
		{name: "metric window includes traffic before stage began", stageIndex: 0, stageStartedAt: stageStartedAt, effectiveWeights: CanaryTrafficWeights{StableWeightPercent: 90, CandidateWeightPercent: 10}, mutateObservation: func(o *CanaryMetricObservation) {
			o.WindowStart = stageStartedAt.Add(-time.Second)
		}, wantStatus: "pause", wantReason: "stage_observation_window_incomplete"},
		{name: "longer configured stage is evaluated against its own dwell", stageIndex: 1, stageStartedAt: now.Add(-4 * time.Minute), effectiveWeights: CanaryTrafficWeights{StableWeightPercent: 50, CandidateWeightPercent: 50}, mutateObservation: func(o *CanaryMetricObservation) {
			o.WindowStart = now.Add(-4 * time.Minute)
			o.WindowEnd = now.Add(-2 * time.Minute)
			o.ObservedAt = o.WindowEnd.Add(time.Second)
		}, wantStatus: "awaiting_confirmation", wantReason: "within_thresholds_manual_confirmation_required"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			measured := observation
			if test.mutateObservation != nil {
				test.mutateObservation(&measured)
			}
			decision, err := EvaluateCanaryStageObservation(policy, test.stageIndex, test.stageStartedAt, test.effectiveWeights, measured, now)
			if err != nil {
				t.Fatalf("EvaluateCanaryStageObservation() error = %v", err)
			}
			if decision.Analysis.Status != test.wantStatus || decision.Analysis.Reason != test.wantReason {
				t.Fatalf("decision = %+v, want status=%q reason=%q", decision, test.wantStatus, test.wantReason)
			}
			if decision.StageIndex != test.stageIndex || decision.CandidateWeightPercent != policy.Stages[test.stageIndex].CandidateWeightPercent || decision.ObservationDurationSeconds != policy.Stages[test.stageIndex].ObservationDurationSeconds {
				t.Fatalf("stage context lost from decision: %+v", decision)
			}
		})
	}

	if _, err := EvaluateCanaryStageObservation(policy, len(policy.Stages), stageStartedAt, CanaryTrafficWeights{CandidateWeightPercent: 100}, observation, now); err == nil {
		t.Fatal("out-of-range stage index was accepted")
	}
}

func testCanaryAnalysisPolicy() CanaryAnalysisPolicy {
	maxErrorRate := 0.05
	maxErrorRateIncrease := 0.02
	maxP95Latency := 250.0
	maxP95LatencyIncrease := 0.5
	return CanaryAnalysisPolicy{
		MinRequests:                  100,
		MaxObservationAgeSeconds:     120,
		MaxCandidateErrorRate:        &maxErrorRate,
		MaxErrorRateIncrease:         &maxErrorRateIncrease,
		MaxCandidateP95LatencyMillis: &maxP95Latency,
		MaxP95LatencyIncreaseRatio:   &maxP95LatencyIncrease,
		Stages: []CanaryStage{
			{CandidateWeightPercent: 10, ObservationDurationSeconds: 60},
			{CandidateWeightPercent: 50, ObservationDurationSeconds: 120},
			{CandidateWeightPercent: 100, ObservationDurationSeconds: 300},
		},
	}
}

func testCanaryObservation(now time.Time) CanaryMetricObservation {
	windowEnd := now.Add(-time.Minute)
	windowStart := windowEnd.Add(-time.Minute)
	observedAt := windowEnd.Add(time.Second)
	stableP95 := 100.0
	candidateP95 := 120.0
	stableDigest := canaryDigest("1")
	candidateDigest := canaryDigest("2")
	return CanaryMetricObservation{
		StableImageDigest:    stableDigest,
		CandidateImageDigest: candidateDigest,
		WindowStart:          windowStart,
		WindowEnd:            windowEnd,
		ObservedAt:           observedAt,
		Stable: CanaryVersionMetrics{
			ImageDigest: stableDigest, Requests: 1000, Errors: 10,
			P95LatencyMillis: &stableP95, Health: "healthy",
		},
		Candidate: CanaryVersionMetrics{
			ImageDigest: candidateDigest, Requests: 1000, Errors: 20,
			P95LatencyMillis: &candidateP95, Health: "healthy",
		},
	}
}

func canaryDigest(hexDigit string) string { return "sha256:" + strings.Repeat(hexDigit, 64) }

func setCanaryP95(metrics *CanaryVersionMetrics, value float64) { metrics.P95LatencyMillis = &value }

func floatPointer(value float64) *float64 { return &value }
