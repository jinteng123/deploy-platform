package service

import (
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

const DefaultBlueGreenRetentionHours = 24

// DeliveryPolicyVersion is an immutable configuration revision attached to a
// single component/environment binding. A new edit appends a version; plans
// keep the exact version they previewed.
type DeliveryPolicyVersion struct {
	BindingID               int64                 `json:"binding_id"`
	Version                 int64                 `json:"version"`
	Strategy                string                `json:"strategy"`
	ServiceName             string                `json:"service_name,omitempty"`
	CandidateRetentionHours int                   `json:"candidate_retention_hours,omitempty"`
	CanaryAnalysis          *CanaryAnalysisPolicy `json:"canary_analysis,omitempty"`
	Readiness               string                `json:"readiness"`
	CreatedBy               string                `json:"created_by"`
	CreatedAt               time.Time             `json:"created_at"`
}

type DeliveryPolicyHistory struct {
	BindingID      int64                   `json:"binding_id"`
	CurrentVersion int64                   `json:"current_version"`
	Versions       []DeliveryPolicyVersion `json:"versions"`
}

type DeliveryBindingPolicyWrite struct {
	ExpectedVersion int64
	Policy          DeliveryPolicyVersion
}

// DeliveryPolicyRepository persists immutable revisions with an optimistic
// current-version check. expectedVersion=0 is valid only before the first row.
type DeliveryPolicyRepository interface {
	DeliveryPolicyVersions() ([]DeliveryPolicyVersion, error)
	SaveDeliveryPolicyVersion(expectedVersion int64, policy DeliveryPolicyVersion) (DeliveryPolicyVersion, bool, error)
}

func normalizeDeliveryPolicy(strategy, serviceName string, retentionHours int, canaryAnalysis ...*CanaryAnalysisPolicy) (DeliveryPolicyVersion, error) {
	strategy = strings.TrimSpace(strategy)
	if strategy != "rolling" && strategy != "blue_green" && strategy != "gateway_canary" {
		return DeliveryPolicyVersion{}, NewValidationError("发布策略必须是 rolling、blue_green 或 gateway_canary")
	}
	serviceName = strings.TrimSpace(serviceName)
	policy := DeliveryPolicyVersion{Strategy: strategy, Readiness: deliveryPolicyReadiness(strategy)}
	var canary *CanaryAnalysisPolicy
	if len(canaryAnalysis) > 0 {
		canary = canaryAnalysis[0]
	}
	if strategy == "blue_green" {
		if serviceName == "" {
			return DeliveryPolicyVersion{}, NewValidationError("蓝绿策略需要明确绑定一个 Service 入口")
		}
		if errs := validation.IsDNS1123Subdomain(serviceName); len(errs) > 0 {
			return DeliveryPolicyVersion{}, NewValidationError("Service 名称无效: %s", strings.Join(errs, "; "))
		}
		if retentionHours == 0 {
			retentionHours = DefaultBlueGreenRetentionHours
		}
		if retentionHours < 1 || retentionHours > 24*30 {
			return DeliveryPolicyVersion{}, NewValidationError("候选版本保留时间必须为 1–720 小时")
		}
		policy.ServiceName = serviceName
		policy.CandidateRetentionHours = retentionHours
		if canary != nil {
			return DeliveryPolicyVersion{}, NewValidationError("金丝雀指标阈值仅适用于网关金丝雀策略")
		}
	} else if strategy == "gateway_canary" {
		if canary == nil {
			return DeliveryPolicyVersion{}, NewValidationError("网关金丝雀草稿需要显式配置指标阈值")
		}
		if err := validateCanaryAnalysisPolicy(*canary); err != nil {
			return DeliveryPolicyVersion{}, NewValidationError("网关金丝雀指标配置无效：%s", err.Error())
		}
		policy.CanaryAnalysis = cloneCanaryAnalysisPolicy(canary)
	} else if retentionHours != 0 {
		return DeliveryPolicyVersion{}, NewValidationError("候选保留时间仅适用于蓝绿策略")
	} else if canary != nil {
		return DeliveryPolicyVersion{}, NewValidationError("金丝雀指标阈值仅适用于网关金丝雀策略")
	}
	return policy, nil
}

func cloneCanaryAnalysisPolicy(policy *CanaryAnalysisPolicy) *CanaryAnalysisPolicy {
	if policy == nil {
		return nil
	}
	clone := *policy
	clone.Stages = append([]CanaryStage(nil), policy.Stages...)
	if policy.GatewayRoute != nil {
		route := *policy.GatewayRoute
		clone.GatewayRoute = &route
	}
	clone.MaxCandidateErrorRate = cloneFloatPointer(policy.MaxCandidateErrorRate)
	clone.MaxErrorRateIncrease = cloneFloatPointer(policy.MaxErrorRateIncrease)
	clone.MaxCandidateP95LatencyMillis = cloneFloatPointer(policy.MaxCandidateP95LatencyMillis)
	clone.MaxP95LatencyIncreaseRatio = cloneFloatPointer(policy.MaxP95LatencyIncreaseRatio)
	return &clone
}

func cloneFloatPointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func sameCanaryAnalysisPolicy(a, b *CanaryAnalysisPolicy) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.MinRequests == b.MinRequests &&
		a.MaxObservationAgeSeconds == b.MaxObservationAgeSeconds &&
		sameCanaryGatewayRoute(a.GatewayRoute, b.GatewayRoute) &&
		sameCanaryStages(a.Stages, b.Stages) &&
		sameFloatPointer(a.MaxCandidateErrorRate, b.MaxCandidateErrorRate) &&
		sameFloatPointer(a.MaxErrorRateIncrease, b.MaxErrorRateIncrease) &&
		sameFloatPointer(a.MaxCandidateP95LatencyMillis, b.MaxCandidateP95LatencyMillis) &&
		sameFloatPointer(a.MaxP95LatencyIncreaseRatio, b.MaxP95LatencyIncreaseRatio)
}

func sameCanaryGatewayRoute(a, b *CanaryGatewayRouteRef) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameCanaryStages(a, b []CanaryStage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameFloatPointer(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func deliveryPolicyReadiness(strategy string) string {
	if strategy == "gateway_canary" {
		return "integration_pending"
	}
	return "configurable"
}

func sameDeliveryPolicyConfig(a, b DeliveryPolicyVersion) bool {
	return a.Strategy == b.Strategy && a.ServiceName == b.ServiceName && a.CandidateRetentionHours == b.CandidateRetentionHours && sameCanaryAnalysisPolicy(a.CanaryAnalysis, b.CanaryAnalysis)
}

func prepareDeliveryPolicyWrite(strategy, serviceName string, retentionHours int, actor string, canaryAnalysis ...*CanaryAnalysisPolicy) (DeliveryPolicyVersion, error) {
	policy, err := normalizeDeliveryPolicy(strategy, serviceName, retentionHours, canaryAnalysis...)
	if err != nil {
		return DeliveryPolicyVersion{}, err
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = "unknown"
	}
	if len(actor) > 64 {
		return DeliveryPolicyVersion{}, NewValidationError("策略修改人标识过长")
	}
	policy.CreatedBy = actor
	return policy, nil
}

func (s *Store) LoadDeliveryPolicyVersions(rows []DeliveryPolicyVersion) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deliveryPolicyVersions = make(map[int64][]DeliveryPolicyVersion)
	for _, row := range rows {
		row.CanaryAnalysis = cloneCanaryAnalysisPolicy(row.CanaryAnalysis)
		if row.Readiness == "" {
			row.Readiness = deliveryPolicyReadiness(row.Strategy)
		}
		s.deliveryPolicyVersions[row.BindingID] = append(s.deliveryPolicyVersions[row.BindingID], row)
	}
	for bindingID := range s.deliveryPolicyVersions {
		sort.Slice(s.deliveryPolicyVersions[bindingID], func(i, j int) bool {
			return s.deliveryPolicyVersions[bindingID][i].Version < s.deliveryPolicyVersions[bindingID][j].Version
		})
	}
}

func (s *Store) DeliveryPolicyHistoryForApp(appID int) (DeliveryPolicyHistory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var binding DeliveryBinding
	for _, row := range s.delivery.Bindings {
		if row.AppID == appID {
			binding = row
			break
		}
	}
	if binding.ID == 0 {
		return DeliveryPolicyHistory{}, ErrNotFound
	}
	versions := cloneDeliveryPolicyVersions(s.deliveryPolicyVersions[binding.ID])
	history := DeliveryPolicyHistory{BindingID: binding.ID, CurrentVersion: currentPolicyVersion(versions), Versions: versions}
	return history, nil
}

func cloneDeliveryPolicyVersions(versions []DeliveryPolicyVersion) []DeliveryPolicyVersion {
	cloned := make([]DeliveryPolicyVersion, len(versions))
	for i, version := range versions {
		cloned[i] = version
		cloned[i].CanaryAnalysis = cloneCanaryAnalysisPolicy(version.CanaryAnalysis)
	}
	return cloned
}

func currentPolicyVersion(versions []DeliveryPolicyVersion) int64 {
	if len(versions) == 0 {
		return 0
	}
	return versions[len(versions)-1].Version
}

func (s *Store) CurrentDeliveryPolicy(bindingID int64) (DeliveryPolicyVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	versions := s.deliveryPolicyVersions[bindingID]
	if len(versions) == 0 {
		return DeliveryPolicyVersion{}, ErrNotFound
	}
	current := versions[len(versions)-1]
	current.CanaryAnalysis = cloneCanaryAnalysisPolicy(current.CanaryAnalysis)
	return current, nil
}

func (s *Store) SaveDeliveryPolicyVersion(appID int, expectedVersion int64, strategy, serviceName string, retentionHours int, actor string, canaryAnalysis ...*CanaryAnalysisPolicy) (DeliveryPolicyVersion, bool, error) {
	if expectedVersion < 0 {
		return DeliveryPolicyVersion{}, false, NewValidationError("expected_version 不能小于 0")
	}
	policy, err := prepareDeliveryPolicyWrite(strategy, serviceName, retentionHours, actor, canaryAnalysis...)
	if err != nil {
		return DeliveryPolicyVersion{}, false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var binding DeliveryBinding
	for _, row := range s.delivery.Bindings {
		if row.AppID == appID {
			binding = row
			break
		}
	}
	if binding.ID == 0 {
		return DeliveryPolicyVersion{}, false, ErrNotFound
	}
	if binding.RequiresReview {
		return DeliveryPolicyVersion{}, false, NewValidationError("请先完成组件与环境绑定核对，再配置发布策略")
	}
	versions := s.deliveryPolicyVersions[binding.ID]
	current := currentPolicyVersion(versions)
	if current != expectedVersion {
		return DeliveryPolicyVersion{}, false, newConflict("策略已更新到 v%d，请刷新后重试", current)
	}
	policy.BindingID = binding.ID
	if len(versions) > 0 && sameDeliveryPolicyConfig(versions[len(versions)-1], policy) {
		current := versions[len(versions)-1]
		current.CanaryAnalysis = cloneCanaryAnalysisPolicy(current.CanaryAnalysis)
		return current, false, nil
	}
	policy.Version = current + 1
	policy.CreatedAt = time.Now()
	if repo, ok := s.persistence.(DeliveryPolicyRepository); ok {
		var created bool
		policy, created, err = repo.SaveDeliveryPolicyVersion(expectedVersion, policy)
		if err != nil {
			return DeliveryPolicyVersion{}, false, err
		}
		if !created {
			return policy, false, nil
		}
	} else if s.persistence != nil {
		return DeliveryPolicyVersion{}, false, NewValidationError("当前数据库不支持持久化发布策略版本")
	}
	if s.deliveryPolicyVersions == nil {
		s.deliveryPolicyVersions = make(map[int64][]DeliveryPolicyVersion)
	}
	s.deliveryPolicyVersions[binding.ID] = append(s.deliveryPolicyVersions[binding.ID], policy)
	policy.CanaryAnalysis = cloneCanaryAnalysisPolicy(policy.CanaryAnalysis)
	return policy, true, nil
}
