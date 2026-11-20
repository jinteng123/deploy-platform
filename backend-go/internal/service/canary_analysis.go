package service

import (
	"fmt"
	"math"
	"time"
)

// CanaryAnalysisPolicy is deliberately explicit: a production canary must not
// inherit guessed thresholds or freshness windows from platform defaults.
// Error-rate increase is an absolute percentage-point delta, while latency
// increase is a relative ratio against the stable version.
type CanaryAnalysisPolicy struct {
	GatewayRoute                 *CanaryGatewayRouteRef `json:"gateway_route,omitempty"`
	MinRequests                  int64                  `json:"min_requests"`
	MaxObservationAgeSeconds     int64                  `json:"max_observation_age_seconds"`
	MaxCandidateErrorRate        *float64               `json:"max_candidate_error_rate"`
	MaxErrorRateIncrease         *float64               `json:"max_error_rate_increase"`
	MaxCandidateP95LatencyMillis *float64               `json:"max_candidate_p95_latency_ms"`
	MaxP95LatencyIncreaseRatio   *float64               `json:"max_p95_latency_increase_ratio"`
	Stages                       []CanaryStage          `json:"stages"`
}

// CanaryStage is one explicitly configured candidate-weight checkpoint. Every
// stage, including 100%, must be observed for its configured duration before
// a human can confirm the next step or finish the release.
type CanaryStage struct {
	CandidateWeightPercent     int   `json:"candidate_weight_percent"`
	ObservationDurationSeconds int64 `json:"observation_duration_seconds"`
}

type CanaryVersionMetrics struct {
	ImageDigest      string   `json:"image_digest"`
	Requests         int64    `json:"requests"`
	Errors           int64    `json:"errors"`
	P95LatencyMillis *float64 `json:"p95_latency_ms"`
	Health           string   `json:"health"` // healthy, degraded, or unknown
}

// CanaryMetricObservation must describe one complete, version-separated
// window. Stable and candidate values may never be combined into one series.
type CanaryMetricObservation struct {
	StableImageDigest    string               `json:"stable_image_digest"`
	CandidateImageDigest string               `json:"candidate_image_digest"`
	WindowStart          time.Time            `json:"window_start"`
	WindowEnd            time.Time            `json:"window_end"`
	ObservedAt           time.Time            `json:"observed_at"`
	Stable               CanaryVersionMetrics `json:"stable"`
	Candidate            CanaryVersionMetrics `json:"candidate"`
}

type CanaryAnalysisDecision struct {
	Status                    string   `json:"status"` // pause, restore_recommended, awaiting_confirmation
	Reason                    string   `json:"reason"`
	CandidateErrorRate        *float64 `json:"candidate_error_rate,omitempty"`
	StableErrorRate           *float64 `json:"stable_error_rate,omitempty"`
	CandidateP95LatencyMillis *float64 `json:"candidate_p95_latency_ms,omitempty"`
	StableP95LatencyMillis    *float64 `json:"stable_p95_latency_ms,omitempty"`
	CandidateRequests         int64    `json:"candidate_requests,omitempty"`
	StableRequests            int64    `json:"stable_requests,omitempty"`
}

// CanaryTrafficWeights is the normalized 0–100 readback for the two approved
// backends. A gateway adapter must populate it from effective route state.
type CanaryTrafficWeights struct {
	StableWeightPercent    int `json:"stable_weight_percent"`
	CandidateWeightPercent int `json:"candidate_weight_percent"`
}

// CanaryStageDecision keeps the configured stage beside the metric decision
// so a caller can present the exact checkpoint and its dwell requirement.
type CanaryStageDecision struct {
	StageIndex                 int                    `json:"stage_index"`
	CandidateWeightPercent     int                    `json:"candidate_weight_percent"`
	ObservationDurationSeconds int64                  `json:"observation_duration_seconds"`
	EvaluatedAt                time.Time              `json:"evaluated_at,omitempty"`
	EffectiveWeights           CanaryTrafficWeights   `json:"effective_weights"`
	Analysis                   CanaryAnalysisDecision `json:"analysis"`
}

// EvaluateCanaryObservation is a pure decision core for a future gateway and
// metrics adapter. A healthy result only permits a human to confirm the next
// stage; this function never changes gateway weights or advances a run.
func EvaluateCanaryObservation(policy CanaryAnalysisPolicy, observation CanaryMetricObservation, now time.Time) (CanaryAnalysisDecision, error) {
	if err := validateCanaryAnalysisPolicy(policy); err != nil {
		return CanaryAnalysisDecision{}, err
	}
	if now.IsZero() {
		return CanaryAnalysisDecision{}, fmt.Errorf("当前时间不能为空")
	}
	pause := func(reason string) CanaryAnalysisDecision {
		return CanaryAnalysisDecision{Status: "pause", Reason: reason}
	}

	stableDigest := NormalizeDigest(observation.StableImageDigest)
	candidateDigest := NormalizeDigest(observation.CandidateImageDigest)
	if stableDigest == "" || candidateDigest == "" {
		return pause("version_identity_invalid"), nil
	}
	if stableDigest == candidateDigest {
		return pause("stable_and_candidate_versions_match"), nil
	}
	if observation.WindowStart.IsZero() || observation.WindowEnd.IsZero() || observation.ObservedAt.IsZero() || !observation.WindowStart.Before(observation.WindowEnd) || observation.WindowEnd.After(observation.ObservedAt) {
		return pause("observation_window_invalid"), nil
	}
	maxAge := time.Duration(policy.MaxObservationAgeSeconds) * time.Second
	if observation.ObservedAt.After(now) || now.Sub(observation.ObservedAt) > maxAge || now.Sub(observation.WindowEnd) > maxAge {
		return pause("observation_stale_or_future"), nil
	}
	if err := validateCanaryVersionMetrics(observation.Stable); err != nil {
		return pause("stable_metrics_invalid"), nil
	}
	if err := validateCanaryVersionMetrics(observation.Candidate); err != nil {
		return pause("candidate_metrics_invalid"), nil
	}
	if NormalizeDigest(observation.Stable.ImageDigest) != stableDigest || NormalizeDigest(observation.Candidate.ImageDigest) != candidateDigest {
		return pause("version_identity_mismatch"), nil
	}
	if observation.Stable.Requests < policy.MinRequests || observation.Candidate.Requests < policy.MinRequests {
		return pause("insufficient_version_samples"), nil
	}
	if observation.Stable.Health != "healthy" {
		return pause("stable_baseline_not_healthy"), nil
	}
	if observation.Candidate.Health == "unknown" {
		return pause("candidate_health_unknown"), nil
	}

	stableErrorRate := float64(observation.Stable.Errors) / float64(observation.Stable.Requests)
	candidateErrorRate := float64(observation.Candidate.Errors) / float64(observation.Candidate.Requests)
	candidateP95 := *observation.Candidate.P95LatencyMillis
	stableP95 := *observation.Stable.P95LatencyMillis
	decision := CanaryAnalysisDecision{
		Status:                    "awaiting_confirmation",
		Reason:                    "within_thresholds_manual_confirmation_required",
		CandidateErrorRate:        &candidateErrorRate,
		StableErrorRate:           &stableErrorRate,
		CandidateP95LatencyMillis: observation.Candidate.P95LatencyMillis,
		StableP95LatencyMillis:    observation.Stable.P95LatencyMillis,
		CandidateRequests:         observation.Candidate.Requests,
		StableRequests:            observation.Stable.Requests,
	}
	if observation.Candidate.Health == "degraded" {
		decision.Status = "restore_recommended"
		decision.Reason = "candidate_health_degraded"
		return decision, nil
	}
	if candidateErrorRate > *policy.MaxCandidateErrorRate || candidateErrorRate-stableErrorRate > *policy.MaxErrorRateIncrease || candidateP95 > *policy.MaxCandidateP95LatencyMillis || latencyIncreaseRatio(candidateP95, stableP95) > *policy.MaxP95LatencyIncreaseRatio {
		decision.Status = "restore_recommended"
		decision.Reason = "candidate_threshold_breached"
	}
	return decision, nil
}

// EvaluateCanaryStageObservation adds the run-time checks that are specific to
// one configured traffic stage. effectiveWeights must come from the gateway's
// effective-configuration readback, not from the requested write; this first
// release supports only stable and candidate backends. The metric window must
// start after the stage became effective and cover its full minimum
// observation duration. A passing result still waits for an operator; this
// function never advances the run or changes traffic.
func EvaluateCanaryStageObservation(
	policy CanaryAnalysisPolicy,
	stageIndex int,
	stageStartedAt time.Time,
	effectiveWeights CanaryTrafficWeights,
	observation CanaryMetricObservation,
	now time.Time,
) (CanaryStageDecision, error) {
	if err := validateCanaryAnalysisPolicy(policy); err != nil {
		return CanaryStageDecision{}, err
	}
	if stageIndex < 0 || stageIndex >= len(policy.Stages) {
		return CanaryStageDecision{}, fmt.Errorf("金丝雀阶段序号无效")
	}
	stage := policy.Stages[stageIndex]
	result := CanaryStageDecision{
		StageIndex: stageIndex, CandidateWeightPercent: stage.CandidateWeightPercent,
		ObservationDurationSeconds: stage.ObservationDurationSeconds, EvaluatedAt: now,
		EffectiveWeights: effectiveWeights,
	}
	pause := func(reason string) (CanaryStageDecision, error) {
		result.Analysis = CanaryAnalysisDecision{Status: "pause", Reason: reason}
		return result, nil
	}
	if now.IsZero() {
		return CanaryStageDecision{}, fmt.Errorf("当前时间不能为空")
	}
	if stageStartedAt.IsZero() || stageStartedAt.After(now) {
		return pause("stage_start_time_missing_or_future")
	}
	if effectiveWeights.CandidateWeightPercent != stage.CandidateWeightPercent ||
		effectiveWeights.StableWeightPercent != 100-stage.CandidateWeightPercent ||
		effectiveWeights.StableWeightPercent+effectiveWeights.CandidateWeightPercent != 100 {
		return pause("gateway_weight_not_effective")
	}
	duration := time.Duration(stage.ObservationDurationSeconds) * time.Second
	if now.Before(stageStartedAt.Add(duration)) {
		return pause("stage_observation_window_incomplete")
	}
	if observation.WindowStart.IsZero() || observation.WindowEnd.IsZero() ||
		observation.WindowStart.Before(stageStartedAt) ||
		observation.WindowEnd.Sub(observation.WindowStart) < duration ||
		observation.WindowEnd.Before(stageStartedAt.Add(duration)) {
		return pause("stage_observation_window_incomplete")
	}
	analysis, err := EvaluateCanaryObservation(policy, observation, now)
	if err != nil {
		return CanaryStageDecision{}, err
	}
	result.Analysis = analysis
	return result, nil
}

func validateCanaryAnalysisPolicy(policy CanaryAnalysisPolicy) error {
	if policy.GatewayRoute != nil && !validCanaryGatewayRouteRef(*policy.GatewayRoute) {
		return fmt.Errorf("网关连接、路由及稳定/候选后端身份必须完整且有效")
	}
	if policy.MinRequests <= 0 || policy.MaxObservationAgeSeconds <= 0 || policy.MaxObservationAgeSeconds > int64((24*time.Hour)/time.Second) {
		return fmt.Errorf("金丝雀分析必须明确配置正数最小请求样本和最大观测年龄")
	}
	if policy.MaxCandidateErrorRate == nil || policy.MaxErrorRateIncrease == nil || policy.MaxCandidateP95LatencyMillis == nil || policy.MaxP95LatencyIncreaseRatio == nil {
		return fmt.Errorf("金丝雀错误率、错误率差值、P95 延迟和延迟增幅阈值必须全部显式配置")
	}
	if !validRate(*policy.MaxCandidateErrorRate) || !validRate(*policy.MaxErrorRateIncrease) || !finiteNonNegative(*policy.MaxCandidateP95LatencyMillis) || !finiteNonNegative(*policy.MaxP95LatencyIncreaseRatio) {
		return fmt.Errorf("金丝雀分析阈值超出有效范围")
	}
	if err := validateCanaryStages(policy.Stages); err != nil {
		return err
	}
	return nil
}

func validateCanaryStages(stages []CanaryStage) error {
	if len(stages) < 2 || len(stages) > 10 {
		return fmt.Errorf("金丝雀阶段必须配置 2–10 个流量检查点")
	}
	previousWeight := 0
	for index, stage := range stages {
		if stage.CandidateWeightPercent <= previousWeight || stage.CandidateWeightPercent > 100 {
			return fmt.Errorf("金丝雀候选流量比例必须严格递增且不超过 100%%")
		}
		if stage.ObservationDurationSeconds <= 0 || stage.ObservationDurationSeconds > int64((24*time.Hour)/time.Second) {
			return fmt.Errorf("每个金丝雀阶段的观察时长必须为 1–86400 秒")
		}
		if index == len(stages)-1 && stage.CandidateWeightPercent != 100 {
			return fmt.Errorf("最后一个金丝雀阶段必须观察 100%% 候选流量")
		}
		previousWeight = stage.CandidateWeightPercent
	}
	return nil
}

func validateCanaryVersionMetrics(metrics CanaryVersionMetrics) error {
	if NormalizeDigest(metrics.ImageDigest) == "" || metrics.Requests < 0 || metrics.Errors < 0 || metrics.Errors > metrics.Requests || metrics.P95LatencyMillis == nil || !finiteNonNegative(*metrics.P95LatencyMillis) {
		return fmt.Errorf("版本指标字段无效")
	}
	if metrics.Health != "healthy" && metrics.Health != "degraded" && metrics.Health != "unknown" {
		return fmt.Errorf("版本健康状态无效")
	}
	return nil
}

func validRate(value float64) bool { return finiteNonNegative(value) && value <= 1 }

func finiteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func latencyIncreaseRatio(candidate, stable float64) float64 {
	if stable == 0 {
		if candidate == 0 {
			return 0
		}
		return math.Inf(1)
	}
	return candidate/stable - 1
}
