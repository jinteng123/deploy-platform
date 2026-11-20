package service

import (
	"sort"
	"strings"
	"time"
)

const (
	DefaultDeliveryMetricsWindowDays = 30
	MaxDeliveryMetricsWindowDays     = 365
)

// DeliveryMetrics keeps execution indicators separate from incident-based DORA
// metrics. Missing source data is explicit, rather than a fabricated zero.
type DeliveryMetrics struct {
	InvalidTimestampSamples int                      `json:"invalid_timestamp_samples"`
	UnknownTierSamples      int                      `json:"unknown_tier_samples"`
	AppName                 string                   `json:"app_name"`
	ClusterID               int                      `json:"cluster_id,omitempty"`
	WindowDays              int                      `json:"window_days"`
	WindowStart             time.Time                `json:"window_start"`
	AsOf                    time.Time                `json:"as_of"`
	ProductionAttempts      int                      `json:"production_attempts"`
	SuccessfulDeployments   int                      `json:"successful_deployments"`
	ExecutionFailures       int                      `json:"execution_failures"`
	ActiveTasks             int                      `json:"active_tasks"`
	PinnedDeployments       int                      `json:"pinned_deployments"`
	DurationSamples         int                      `json:"duration_samples"`
	ApprovalWaitSamples     int                      `json:"approval_wait_samples"`
	PendingApprovals        int                      `json:"pending_approvals"`
	ExecutionFailureRate    *float64                 `json:"execution_failure_rate"`
	ExecutionP95Seconds     *float64                 `json:"execution_p95_seconds"`
	ApprovalWaitP95Seconds  *float64                 `json:"approval_wait_p95_seconds"`
	IncidentRecovery        IncidentRecoveryMetrics  `json:"incident_recovery"`
	DORA                    map[string]UnknownMetric `json:"dora"`
}

// IncidentRecoveryMetrics reports observed durations for operator-registered
// production events. These records are not automatically attributed to a
// deployment and therefore do not populate the DORA recovery metric.
type IncidentRecoveryMetrics struct {
	DataAvailable                     bool     `json:"data_available"`
	IncidentSamples                   int      `json:"incident_samples"`
	RecoveredSamples                  int      `json:"recovered_samples"`
	UnrecoveredSamples                int      `json:"unrecovered_samples"`
	InterventionRecoverySamples       int      `json:"intervention_recovery_samples"`
	InvalidTimestampSamples           int      `json:"invalid_timestamp_samples"`
	CausalityAssessedSamples          int      `json:"causality_assessed_samples"`
	CausalityPendingSamples           int      `json:"causality_pending_samples"`
	RemediationClassifiedSamples      int      `json:"remediation_classified_samples"`
	RemediationPendingSamples         int      `json:"remediation_pending_samples"`
	MeanRecoverySeconds               *float64 `json:"mean_recovery_seconds"`
	MeanInterventionToRecoverySeconds *float64 `json:"mean_intervention_to_recovery_seconds"`
	MissingData                       string   `json:"missing_data"`
}
type UnknownMetric struct {
	Value       *float64 `json:"value"`
	MissingData string   `json:"missing_data"`
	SampleCount int      `json:"sample_count"`
}

func (s *Store) DeliveryMetrics(appName string) DeliveryMetrics {
	return s.DeliveryMetricsForWindow(appName, DefaultDeliveryMetricsWindowDays, time.Now())
}

// DeliveryMetricsForWindow groups production attempts by task creation time
// and reports the exact observation window and sample counts. DORA metrics are
// only populated where their source associations and scope are complete.
func (s *Store) DeliveryMetricsForWindow(appName string, windowDays int, now time.Time) DeliveryMetrics {
	return s.DeliveryMetricsForTarget(appName, 0, windowDays, now)
}

// DeliveryMetricsForTarget scopes execution and approval metrics to one production
// target when clusterID is positive. A zero clusterID keeps the all-target view.
// The recorded environment tier remains the source of truth for historical rows.
func (s *Store) DeliveryMetricsForTarget(appName string, clusterID, windowDays int, now time.Time) DeliveryMetrics {
	if windowDays < 1 || windowDays > MaxDeliveryMetricsWindowDays {
		windowDays = DefaultDeliveryMetricsWindowDays
	}
	if now.IsZero() {
		now = time.Now()
	}
	asOf := now.UTC()
	windowStart := asOf.Add(-time.Duration(windowDays) * 24 * time.Hour)
	frequencyMissingData := "请选择一个应用；跨应用的去重口径尚未定义"
	if appName != "" && clusterID == 0 {
		frequencyMissingData = "跨生产目标按 release ID 合并；缺少完整制品关联时显示未知"
	} else if appName != "" {
		frequencyMissingData = "窗口内有生产成功任务缺少有效时间或环境快照，无法准确归入统计窗口"
	}
	result := DeliveryMetrics{AppName: appName, ClusterID: clusterID, WindowDays: windowDays, WindowStart: windowStart, AsOf: asOf, DORA: map[string]UnknownMetric{
		"change_lead_time":                {MissingData: "需要单个生产目标和应用，并关联 Git commit 时间与成功生产部署"},
		"deployment_frequency":            {MissingData: frequencyMissingData},
		"failed_deployment_recovery_time": {MissingData: "已展示人工登记事件恢复时长；缺少全量发布失败的完整归因覆盖，不能作为 DORA 统计"},
		"change_fail_rate":                {MissingData: "因果判定、修复处置及计划内/外记录覆盖率尚不完整"},
		"deployment_rework_rate":          {MissingData: "缺少计划内标记、修复类型与生产事件因果关联"},
	}}
	result.IncidentRecovery.MissingData = "窗口内没有已恢复的人工登记事件"
	incidentSamples, incidentErr := s.deliveryIncidentRecoverySamples(appName, clusterID, windowStart)
	if incidentErr != nil {
		result.IncidentRecovery.MissingData = "无法读取线上事件恢复数据"
	} else {
		result.IncidentRecovery = summarizeIncidentRecovery(incidentSamples, asOf)
	}
	if appName != "" {
		logs := s.Logs(clusterID, appName, "", 0)
		releases := s.ImageReleases(appName, 0)
		if clusterID > 0 {
			result.DORA["deployment_frequency"] = deliveryFrequencyForTarget(logs, windowStart, asOf)
			result.DORA["change_lead_time"] = deliveryChangeLeadTime(logs, releases, clusterID, appName, windowStart, asOf)
		} else {
			result.DORA["deployment_frequency"] = deliveryFrequencyForApp(logs, releases, appName, windowStart, asOf)
		}
	}
	durations := []float64{}
	completed := 0
	for _, l := range s.Logs(clusterID, appName, "", 0) {
		if l.CreatedAt.IsZero() || l.CreatedAt.After(asOf) {
			result.InvalidTimestampSamples++
			continue
		}
		if l.CreatedAt.Before(windowStart) {
			continue
		}
		if l.EnvironmentTierSnapshot == nil || (*l.EnvironmentTierSnapshot != "production" && *l.EnvironmentTierSnapshot != "non_production") {
			result.UnknownTierSamples++
			continue
		}
		if *l.EnvironmentTierSnapshot != "production" {
			continue
		}
		result.ProductionAttempts++
		if l.ImageFull != nil && PinnedImage(*l.ImageFull) {
			result.PinnedDeployments++
		}
		switch l.Status {
		case "pending", "running":
			result.ActiveTasks++
		case "success":
			result.SuccessfulDeployments++
			completed++
		case "failed", "interrupted":
			result.ExecutionFailures++
			completed++
		}
		terminal := l.Status == "success" || l.Status == "failed" || l.Status == "interrupted"
		if terminal {
			if l.StartedAt == nil || l.FinishedAt == nil || l.StartedAt.IsZero() || l.FinishedAt.IsZero() {
				result.InvalidTimestampSamples++
				continue
			}
			if l.StartedAt.Before(l.CreatedAt) || l.StartedAt.After(asOf) || l.FinishedAt.After(asOf) {
				result.InvalidTimestampSamples++
				continue
			}
			d := l.FinishedAt.Sub(*l.StartedAt).Seconds()
			if d >= 0 {
				durations = append(durations, d)
			} else {
				result.InvalidTimestampSamples++
			}
		}
	}
	result.DurationSamples = len(durations)
	if completed > 0 {
		v := float64(result.ExecutionFailures) / float64(completed)
		result.ExecutionFailureRate = &v
	}
	result.ExecutionP95Seconds = percentile95(durations)
	waits := []float64{}
	for _, p := range s.PromotionRequests("", 0) {
		if clusterID > 0 && p.ProdClusterID != clusterID {
			continue
		}
		if appName != "" {
			release, err := s.FindImageRelease(p.ReleaseID)
			if err != nil || release.AppName != appName {
				continue
			}
		}
		if p.CreatedAt.IsZero() || p.CreatedAt.After(asOf) {
			result.InvalidTimestampSamples++
			continue
		}
		if p.CreatedAt.Before(windowStart) {
			continue
		}
		if p.Status == "pending" {
			result.PendingApprovals++
			continue
		}
		if p.DecidedAt == nil {
			result.InvalidTimestampSamples++
			continue
		}
		if p.DecidedAt.After(asOf) {
			result.InvalidTimestampSamples++
			continue
		}
		d := p.DecidedAt.Sub(p.CreatedAt).Seconds()
		if d >= 0 {
			waits = append(waits, d)
		} else {
			result.InvalidTimestampSamples++
		}
	}
	result.ApprovalWaitSamples = len(waits)
	result.ApprovalWaitP95Seconds = percentile95(waits)
	return result
}

func summarizeIncidentRecovery(samples []DeliveryIncidentRecoverySample, asOf time.Time) IncidentRecoveryMetrics {
	result := IncidentRecoveryMetrics{DataAvailable: true}
	recoveryDurations := make([]float64, 0, len(samples))
	interventionDurations := make([]float64, 0, len(samples))
	for _, sample := range samples {
		if sample.OccurredAt.IsZero() || sample.OccurredAt.After(asOf) {
			result.InvalidTimestampSamples++
			continue
		}
		result.IncidentSamples++
		if sample.CausalityStatus == DeliveryIncidentCausedByDeployment || sample.CausalityStatus == DeliveryIncidentNotCausedByDeployment {
			result.CausalityAssessedSamples++
		} else {
			result.CausalityPendingSamples++
		}
		if deliveryIncidentRemediationClassified(sample) {
			result.RemediationClassifiedSamples++
		} else {
			result.RemediationPendingSamples++
		}
		if sample.RecoveryOccurredAt == nil {
			result.UnrecoveredSamples++
			continue
		}
		if sample.Status != "recovered" || sample.RecoveryOccurredAt.Before(sample.OccurredAt) || sample.RecoveryOccurredAt.After(asOf) {
			result.InvalidTimestampSamples++
			result.UnrecoveredSamples++
			continue
		}
		result.RecoveredSamples++
		recoveryDurations = append(recoveryDurations, sample.RecoveryOccurredAt.Sub(sample.OccurredAt).Seconds())
		if sample.InterventionStartedAt == nil {
			continue
		}
		if sample.InterventionStartedAt.Before(sample.OccurredAt) || sample.InterventionStartedAt.After(*sample.RecoveryOccurredAt) || sample.InterventionStartedAt.After(asOf) {
			result.InvalidTimestampSamples++
			continue
		}
		result.InterventionRecoverySamples++
		interventionDurations = append(interventionDurations, sample.RecoveryOccurredAt.Sub(*sample.InterventionStartedAt).Seconds())
	}
	result.MeanRecoverySeconds = mean(recoveryDurations)
	result.MeanInterventionToRecoverySeconds = mean(interventionDurations)
	switch {
	case result.IncidentSamples == 0:
		result.MissingData = "窗口内没有人工登记事件"
	case result.RecoveredSamples == 0:
		result.MissingData = "窗口内没有有效的业务恢复记录"
	}
	return result
}

func deliveryIncidentRemediationClassified(sample DeliveryIncidentRecoverySample) bool {
	switch sample.RemediationType {
	case DeliveryIncidentRemediationRollback, DeliveryIncidentRemediationHotfix, DeliveryIncidentRemediationConfig:
		return (sample.RemediationPlanStatus == DeliveryIncidentRemediationPlanned || sample.RemediationPlanStatus == DeliveryIncidentRemediationUnplanned) &&
			sample.RemediationDeployLogID != nil && *sample.RemediationDeployLogID > 0
	case DeliveryIncidentRemediationRestart, DeliveryIncidentRemediationNoAction:
		return sample.RemediationPlanStatus == DeliveryIncidentRemediationNotApplicable && sample.RemediationDeployLogID == nil
	default:
		return false
	}
}

func mean(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	result := total / float64(len(values))
	return &result
}

// deliveryFrequencyForTarget reports successful production deployments by
// their completion time. It is only used for a single app/cluster scope so
// this metric does not guess how one multi-cluster rollout should be deduped.
func deliveryFrequencyForTarget(logs []DeployLog, windowStart, asOf time.Time) UnknownMetric {
	count := 0.0
	for _, l := range logs {
		if l.Status != "success" {
			continue
		}
		if l.EnvironmentTierSnapshot == nil || (*l.EnvironmentTierSnapshot != "production" && *l.EnvironmentTierSnapshot != "non_production") {
			if l.FinishedAt == nil || l.FinishedAt.IsZero() || !l.FinishedAt.Before(windowStart) {
				return UnknownMetric{MissingData: "有成功任务的生产环境等级快照缺失或无效，无法确认部署范围"}
			}
			continue
		}
		if *l.EnvironmentTierSnapshot != "production" {
			continue
		}
		if l.FinishedAt == nil || l.FinishedAt.IsZero() {
			return UnknownMetric{MissingData: "有生产成功任务缺少完成时间，无法准确归入统计窗口"}
		}
		if l.FinishedAt.Before(windowStart) {
			continue
		}
		if l.CreatedAt.IsZero() || l.CreatedAt.After(asOf) || l.FinishedAt.After(asOf) || l.FinishedAt.Before(l.CreatedAt) {
			return UnknownMetric{MissingData: "有生产成功任务的创建/完成时间缺失或异常，无法准确归入统计窗口"}
		}
		count++
	}
	return UnknownMetric{Value: &count}
}

// deliveryFrequencyForApp counts each immutable release once across all
// production targets. A multi-target promotion of the same release is one
// service delivery; unrelated releases remain separate events.
func deliveryFrequencyForApp(logs []DeployLog, releases []ImageRelease, appName string, windowStart, asOf time.Time) UnknownMetric {
	releaseByID := make(map[int64]ImageRelease, len(releases))
	for _, release := range releases {
		releaseByID[release.ID] = release
	}
	seenReleases := make(map[int64]struct{})
	count := 0.0
	for _, log := range logs {
		if log.Status != "success" {
			continue
		}
		if log.EnvironmentTierSnapshot != nil && *log.EnvironmentTierSnapshot == "non_production" {
			continue
		}
		if log.FinishedAt == nil || log.FinishedAt.IsZero() {
			return UnknownMetric{MissingData: "有成功任务缺少完成时间，无法确定是否属于统计窗口"}
		}
		finishedAt := log.FinishedAt.UTC()
		if finishedAt.After(asOf) {
			return UnknownMetric{MissingData: "有成功任务完成时间晚于观察时间，时间数据无效"}
		}
		if finishedAt.Before(windowStart) {
			continue
		}
		if log.EnvironmentTierSnapshot == nil || *log.EnvironmentTierSnapshot != "production" && *log.EnvironmentTierSnapshot != "non_production" {
			return UnknownMetric{MissingData: "窗口内有成功任务缺少有效环境快照，无法确认生产部署范围"}
		}
		if log.ClusterID <= 0 || log.AppName != appName || log.CreatedAt.IsZero() || log.CreatedAt.After(finishedAt) {
			return UnknownMetric{MissingData: "有生产成功任务的目标或创建/完成时间异常"}
		}
		if log.ReleaseID <= 0 {
			return UnknownMetric{MissingData: "窗口内有生产成功任务未关联发布制品，无法按 release 去重"}
		}
		release, ok := releaseByID[log.ReleaseID]
		if !ok || release.AppName != appName {
			return UnknownMetric{MissingData: "生产部署与发布制品的应用关联不完整或不一致"}
		}
		expectedImage := deployImage(release)
		if release.ImageDigest == nil || NormalizeDigest(*release.ImageDigest) == "" || !PinnedImage(expectedImage) || log.ImageFull == nil || strings.TrimSpace(*log.ImageFull) != expectedImage {
			return UnknownMetric{MissingData: "生产部署的镜像未能与发布制品 digest 一致核对"}
		}
		if _, duplicate := seenReleases[release.ID]; duplicate {
			continue
		}
		seenReleases[release.ID] = struct{}{}
		count++
	}
	return UnknownMetric{Value: &count}
}

// deliveryChangeLeadTime reports the median commit-to-production duration
// only when every successful production deployment in scope has a canonical
// release link, matching immutable image digest, and valid source commit time.
// Receipt/build time is never substituted for the Git commit timestamp.
func deliveryChangeLeadTime(logs []DeployLog, releases []ImageRelease, clusterID int, appName string, windowStart, asOf time.Time) UnknownMetric {
	releaseByID := make(map[int64]ImageRelease, len(releases))
	for _, release := range releases {
		releaseByID[release.ID] = release
	}
	leadTimes := make([]float64, 0)
	seenReleases := make(map[int64]struct{})
	missingData := ""
	markUnknown := func(message string) {
		if missingData == "" {
			missingData = message
		}
	}
	for _, log := range logs {
		if log.Status != "success" {
			continue
		}
		if log.ClusterID != clusterID || log.AppName != appName {
			markUnknown("生产成功任务超出当前应用或目标范围")
			continue
		}
		if log.EnvironmentTierSnapshot != nil && *log.EnvironmentTierSnapshot == "non_production" {
			continue
		}
		if log.FinishedAt == nil || log.FinishedAt.IsZero() {
			markUnknown("有成功任务缺少完成时间，无法确定是否属于统计窗口")
			continue
		}
		finishedAt := log.FinishedAt.UTC()
		if finishedAt.After(asOf) {
			markUnknown("有成功任务完成时间晚于观察时间，时间数据无效")
			continue
		}
		if finishedAt.Before(windowStart) {
			continue
		}
		if log.EnvironmentTierSnapshot == nil || *log.EnvironmentTierSnapshot != "production" && *log.EnvironmentTierSnapshot != "non_production" {
			markUnknown("窗口内有成功任务缺少有效环境快照，无法确认生产部署范围")
			continue
		}
		if log.CreatedAt.IsZero() || log.CreatedAt.After(finishedAt) {
			markUnknown("有生产成功任务的创建/完成时间异常")
			continue
		}
		if log.ReleaseID <= 0 {
			markUnknown("窗口内有生产成功任务未关联发布制品，无法找到 Git commit")
			continue
		}
		release, ok := releaseByID[log.ReleaseID]
		if !ok || release.AppName != appName {
			markUnknown("生产部署与发布制品的应用关联不完整或不一致")
			continue
		}
		expectedImage := deployImage(release)
		if release.ImageDigest == nil || NormalizeDigest(*release.ImageDigest) == "" || !PinnedImage(expectedImage) || log.ImageFull == nil || strings.TrimSpace(*log.ImageFull) != expectedImage {
			markUnknown("生产部署的镜像未能与发布制品 digest 一致核对")
			continue
		}
		if _, duplicate := seenReleases[release.ID]; duplicate {
			continue
		}
		seenReleases[release.ID] = struct{}{}
		if release.CommitID == nil || strings.TrimSpace(*release.CommitID) == "" || release.Metadata.CommitTimestamp == nil || release.Metadata.CommitTimestamp.IsZero() {
			markUnknown("有生产制品缺少 Git commit ID 或 commit 时间")
			continue
		}
		commitAt := release.Metadata.CommitTimestamp.UTC()
		if commitAt.After(asOf) || commitAt.After(finishedAt) {
			markUnknown("有 Git commit 时间晚于观察时间或生产部署完成时间")
			continue
		}
		leadTimes = append(leadTimes, finishedAt.Sub(commitAt).Seconds())
	}
	if missingData != "" {
		return UnknownMetric{MissingData: missingData, SampleCount: len(leadTimes)}
	}
	if len(leadTimes) == 0 {
		return UnknownMetric{MissingData: "统计窗口内没有可计算的 Git commit 到生产部署样本"}
	}
	sort.Float64s(leadTimes)
	middle := len(leadTimes) / 2
	median := leadTimes[middle]
	if len(leadTimes)%2 == 0 {
		median = (leadTimes[middle-1] + leadTimes[middle]) / 2
	}
	return UnknownMetric{Value: &median, SampleCount: len(leadTimes)}
}

func percentile95(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	sort.Float64s(values)
	index := (95*len(values)+99)/100 - 1
	v := values[index]
	return &v
}

// Latest successful target is the observed expectation, not an automatic
// reconciliation policy. A failed attempt is never promoted to this baseline.
func (s *Store) SuccessfulWorkloadLog(clusterID int, namespace, name, container string) (DeployLog, bool) {
	for _, l := range s.Logs(clusterID, "", "success", 0) {
		if l.Namespace == namespace && l.DeploymentName == name && l.ContainerName == container {
			return l, true
		}
	}
	return DeployLog{}, false
}

func (s *Store) FailInterruptedLog(id int64, msg string) error {
	l, err := s.FindLog(id)
	if err != nil {
		return err
	}
	if l.Status != "running" && l.Status != "pending" {
		return nil
	}
	now := time.Now()
	l.Status = "interrupted"
	l.ErrorMessage = &msg
	l.FinishedAt = &now
	_, err = s.UpdateLogExecutionAndRecordDeliveryRunEvent(l, DeliveryRunEvent{
		EventKey: deliveryRunAttemptEventKey(l, "recovery:worker-persistence-failure"), EventType: "interrupted", Stage: interruptionStage(l), Status: "interrupted", Detail: msg,
	})
	return err
}
