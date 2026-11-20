package service

import (
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	MinEvidenceFreshnessMinutes = 1
	MaxEvidenceFreshnessMinutes = 7 * 24 * 60
)

var verificationEvidenceKinds = map[string]struct{}{
	"ci": {}, "smoke": {}, "manual": {},
}

func runnerEvidenceIdentity(identity string) bool {
	if !strings.HasPrefix(identity, "runner:") {
		return false
	}
	rawID := strings.TrimPrefix(identity, "runner:")
	id, err := strconv.ParseInt(rawID, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == rawID
}

// DeliveryEnvironmentEvidencePolicyVersion is an immutable release gate for
// one logical environment. Every configured kind is mandatory, and its
// reporter must match the explicitly trusted identity list.
type DeliveryEnvironmentEvidencePolicyVersion struct {
	EnvironmentID             int64               `json:"environment_id"`
	Version                   int64               `json:"version"`
	RequiredVerificationKinds []string            `json:"required_verification_kinds"`
	MaxEvidenceAgeMinutes     int                 `json:"max_evidence_age_minutes"`
	TrustedSubmitters         map[string][]string `json:"trusted_submitters"`
	CreatedBy                 string              `json:"created_by"`
	CreatedAt                 time.Time           `json:"created_at"`
}

type DeliveryEnvironmentEvidencePolicyHistory struct {
	EnvironmentID  int64                                      `json:"environment_id"`
	CurrentVersion int64                                      `json:"current_version"`
	Versions       []DeliveryEnvironmentEvidencePolicyVersion `json:"versions"`
}

type DeliveryEnvironmentEvidencePolicyRepository interface {
	DeliveryEnvironmentEvidencePolicyVersions() ([]DeliveryEnvironmentEvidencePolicyVersion, error)
	SaveDeliveryEnvironmentEvidencePolicyVersion(expectedVersion int64, policy DeliveryEnvironmentEvidencePolicyVersion) (DeliveryEnvironmentEvidencePolicyVersion, bool, error)
}

func normalizeEnvironmentEvidencePolicy(requiredKinds []string, maxAgeMinutes int, trusted map[string][]string) (DeliveryEnvironmentEvidencePolicyVersion, error) {
	if maxAgeMinutes < MinEvidenceFreshnessMinutes || maxAgeMinutes > MaxEvidenceFreshnessMinutes {
		return DeliveryEnvironmentEvidencePolicyVersion{}, NewValidationError("证据有效时长必须为 1–10080 分钟")
	}
	if len(requiredKinds) == 0 || len(requiredKinds) > len(verificationEvidenceKinds) {
		return DeliveryEnvironmentEvidencePolicyVersion{}, NewValidationError("至少选择一种必需的业务验证类型")
	}
	normalizedKinds := make([]string, 0, len(requiredKinds))
	seenKinds := map[string]struct{}{}
	for _, raw := range requiredKinds {
		kind := strings.TrimSpace(raw)
		if _, ok := verificationEvidenceKinds[kind]; !ok {
			return DeliveryEnvironmentEvidencePolicyVersion{}, NewValidationError("不支持的必需验证类型：%s", kind)
		}
		if _, exists := seenKinds[kind]; exists {
			return DeliveryEnvironmentEvidencePolicyVersion{}, NewValidationError("验证类型不能重复：%s", kind)
		}
		seenKinds[kind] = struct{}{}
		normalizedKinds = append(normalizedKinds, kind)
	}
	sort.Strings(normalizedKinds)
	normalizedTrusted := make(map[string][]string, len(normalizedKinds))
	for kind := range trusted {
		if _, required := seenKinds[kind]; !required {
			return DeliveryEnvironmentEvidencePolicyVersion{}, NewValidationError("可信来源只能配置在必需验证类型上：%s", kind)
		}
	}
	for _, kind := range normalizedKinds {
		identities := trusted[kind]
		if len(identities) == 0 || len(identities) > 50 {
			return DeliveryEnvironmentEvidencePolicyVersion{}, NewValidationError("%s 至少需要一个可信提交身份，最多 50 个", kind)
		}
		seen := map[string]struct{}{}
		for _, raw := range identities {
			identity := strings.TrimSpace(raw)
			if identity == "" || len([]rune(identity)) > 128 || identity == "*" {
				return DeliveryEnvironmentEvidencePolicyVersion{}, NewValidationError("%s 的可信提交身份无效", kind)
			}
			for _, r := range identity {
				if unicode.IsControl(r) {
					return DeliveryEnvironmentEvidencePolicyVersion{}, NewValidationError("%s 的可信提交身份不能包含控制字符", kind)
				}
			}
			if kind == "manual" && (strings.HasPrefix(identity, "apikey:") || strings.HasPrefix(identity, "runner:")) {
				return DeliveryEnvironmentEvidencePolicyVersion{}, NewValidationError("人工验收只能信任人工账号，不能信任机器身份")
			}
			if kind != "manual" && !((strings.HasPrefix(identity, "apikey:") && len(identity) > len("apikey:")) || runnerEvidenceIdentity(identity)) {
				return DeliveryEnvironmentEvidencePolicyVersion{}, NewValidationError("CI / Smoke 来源必须使用 apikey:<密钥名称> 或 runner:<接入 ID> 身份")
			}
			if _, exists := seen[identity]; exists {
				continue
			}
			seen[identity] = struct{}{}
			normalizedTrusted[kind] = append(normalizedTrusted[kind], identity)
		}
		if len(normalizedTrusted[kind]) == 0 {
			return DeliveryEnvironmentEvidencePolicyVersion{}, NewValidationError("%s 至少需要一个可信提交身份", kind)
		}
		sort.Strings(normalizedTrusted[kind])
	}
	return DeliveryEnvironmentEvidencePolicyVersion{
		RequiredVerificationKinds: normalizedKinds,
		MaxEvidenceAgeMinutes:     maxAgeMinutes,
		TrustedSubmitters:         normalizedTrusted,
	}, nil
}

func cloneEnvironmentEvidencePolicy(policy DeliveryEnvironmentEvidencePolicyVersion) DeliveryEnvironmentEvidencePolicyVersion {
	policy.RequiredVerificationKinds = append([]string{}, policy.RequiredVerificationKinds...)
	policy.TrustedSubmitters = cloneEvidenceSubmitters(policy.TrustedSubmitters)
	return policy
}

func cloneEvidenceSubmitters(submitters map[string][]string) map[string][]string {
	trusted := make(map[string][]string, len(submitters))
	for kind, identities := range submitters {
		trusted[kind] = append([]string{}, identities...)
	}
	return trusted
}

func sameEnvironmentEvidencePolicy(a, b DeliveryEnvironmentEvidencePolicyVersion) bool {
	if a.MaxEvidenceAgeMinutes != b.MaxEvidenceAgeMinutes || len(a.RequiredVerificationKinds) != len(b.RequiredVerificationKinds) {
		return false
	}
	for i := range a.RequiredVerificationKinds {
		if a.RequiredVerificationKinds[i] != b.RequiredVerificationKinds[i] {
			return false
		}
	}
	if len(a.TrustedSubmitters) != len(b.TrustedSubmitters) {
		return false
	}
	for kind, aIdentities := range a.TrustedSubmitters {
		bIdentities := b.TrustedSubmitters[kind]
		if len(aIdentities) != len(bIdentities) {
			return false
		}
		for i := range aIdentities {
			if aIdentities[i] != bIdentities[i] {
				return false
			}
		}
	}
	return true
}

func currentEnvironmentEvidencePolicy(versions []DeliveryEnvironmentEvidencePolicyVersion) (DeliveryEnvironmentEvidencePolicyVersion, bool) {
	if len(versions) == 0 {
		return DeliveryEnvironmentEvidencePolicyVersion{}, false
	}
	return cloneEnvironmentEvidencePolicy(versions[len(versions)-1]), true
}

func (s *Store) LoadDeliveryEnvironmentEvidencePolicyVersions(rows []DeliveryEnvironmentEvidencePolicyVersion) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deliveryEvidencePolicies = make(map[int64][]DeliveryEnvironmentEvidencePolicyVersion)
	for _, row := range rows {
		row = cloneEnvironmentEvidencePolicy(row)
		s.deliveryEvidencePolicies[row.EnvironmentID] = append(s.deliveryEvidencePolicies[row.EnvironmentID], row)
	}
	for id := range s.deliveryEvidencePolicies {
		sort.Slice(s.deliveryEvidencePolicies[id], func(i, j int) bool {
			return s.deliveryEvidencePolicies[id][i].Version < s.deliveryEvidencePolicies[id][j].Version
		})
	}
	for i := range s.delivery.Environments {
		versions := s.deliveryEvidencePolicies[s.delivery.Environments[i].ID]
		s.delivery.Environments[i].EvidencePolicyVersion = 0
		if len(versions) > 0 {
			s.delivery.Environments[i].EvidencePolicyVersion = versions[len(versions)-1].Version
		}
	}
}

func (s *Store) DeliveryEnvironmentEvidencePolicyHistory(environmentID int64) (DeliveryEnvironmentEvidencePolicyHistory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, exists := s.deliveryEnvironmentLocked(environmentID); !exists {
		return DeliveryEnvironmentEvidencePolicyHistory{}, ErrNotFound
	}
	versions := s.deliveryEvidencePolicies[environmentID]
	result := DeliveryEnvironmentEvidencePolicyHistory{
		EnvironmentID:  environmentID,
		CurrentVersion: currentEnvironmentEvidencePolicyVersion(versions),
		Versions:       make([]DeliveryEnvironmentEvidencePolicyVersion, len(versions)),
	}
	for i, version := range versions {
		result.Versions[i] = cloneEnvironmentEvidencePolicy(version)
	}
	return result, nil
}

func currentEnvironmentEvidencePolicyVersion(versions []DeliveryEnvironmentEvidencePolicyVersion) int64 {
	if len(versions) == 0 {
		return 0
	}
	return versions[len(versions)-1].Version
}

func (s *Store) CurrentDeliveryEnvironmentEvidencePolicy(environmentID int64) (DeliveryEnvironmentEvidencePolicyVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, exists := s.deliveryEnvironmentLocked(environmentID); !exists {
		return DeliveryEnvironmentEvidencePolicyVersion{}, ErrNotFound
	}
	policy, ok := currentEnvironmentEvidencePolicy(s.deliveryEvidencePolicies[environmentID])
	if !ok {
		return DeliveryEnvironmentEvidencePolicyVersion{}, NewValidationError("生产环境尚未配置必需验证证据策略")
	}
	return policy, nil
}

func (s *Store) SaveDeliveryEnvironmentEvidencePolicy(environmentID, expectedVersion int64, requiredKinds []string, maxAgeMinutes int, trusted map[string][]string, actor string) (DeliveryEnvironmentEvidencePolicyVersion, bool, error) {
	if expectedVersion < 0 {
		return DeliveryEnvironmentEvidencePolicyVersion{}, false, NewValidationError("expected_version 不能小于 0")
	}
	policy, err := normalizeEnvironmentEvidencePolicy(requiredKinds, maxAgeMinutes, trusted)
	if err != nil {
		return DeliveryEnvironmentEvidencePolicyVersion{}, false, err
	}
	actor = strings.TrimSpace(actor)
	if actor == "" || len([]rune(actor)) > 128 {
		return DeliveryEnvironmentEvidencePolicyVersion{}, false, NewValidationError("策略修改人标识无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	environment, exists := s.deliveryEnvironmentLocked(environmentID)
	if !exists {
		return DeliveryEnvironmentEvidencePolicyVersion{}, false, ErrNotFound
	}
	if environment.Tier != "production" {
		return DeliveryEnvironmentEvidencePolicyVersion{}, false, NewValidationError("仅生产环境需要配置晋级证据策略")
	}
	if environment.RequiresReview {
		return DeliveryEnvironmentEvidencePolicyVersion{}, false, NewValidationError("请先核对并确认逻辑环境")
	}
	versions := s.deliveryEvidencePolicies[environmentID]
	current := currentEnvironmentEvidencePolicyVersion(versions)
	if current != expectedVersion {
		return DeliveryEnvironmentEvidencePolicyVersion{}, false, newConflict("证据策略已更新到 v%d，请刷新后重试", current)
	}
	policy.EnvironmentID = environmentID
	policy.CreatedBy = actor
	if len(versions) > 0 && sameEnvironmentEvidencePolicy(versions[len(versions)-1], policy) {
		return cloneEnvironmentEvidencePolicy(versions[len(versions)-1]), false, nil
	}
	policy.Version = current + 1
	policy.CreatedAt = time.Now()
	if repo, ok := s.persistence.(DeliveryEnvironmentEvidencePolicyRepository); ok {
		policy, _, err = repo.SaveDeliveryEnvironmentEvidencePolicyVersion(expectedVersion, policy)
		if err != nil {
			return DeliveryEnvironmentEvidencePolicyVersion{}, false, err
		}
	} else if s.persistence != nil {
		return DeliveryEnvironmentEvidencePolicyVersion{}, false, NewValidationError("当前数据库不支持持久化环境证据策略")
	}
	policy = cloneEnvironmentEvidencePolicy(policy)
	if s.deliveryEvidencePolicies == nil {
		s.deliveryEvidencePolicies = make(map[int64][]DeliveryEnvironmentEvidencePolicyVersion)
	}
	s.deliveryEvidencePolicies[environmentID] = append(versions, policy)
	for i := range s.delivery.Environments {
		if s.delivery.Environments[i].ID == environmentID {
			s.delivery.Environments[i].EvidencePolicyVersion = policy.Version
			break
		}
	}
	return cloneEnvironmentEvidencePolicy(policy), true, nil
}

func (s *Store) DeliveryEnvironmentEvidencePolicyForApp(appID int) (DeliveryEnvironmentEvidencePolicyVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentDeliveryEnvironmentEvidencePolicyForAppLocked(appID)
}

func (s *Store) currentDeliveryEnvironmentEvidencePolicyForAppLocked(appID int) (DeliveryEnvironmentEvidencePolicyVersion, error) {
	for _, binding := range s.delivery.Bindings {
		if binding.AppID != appID {
			continue
		}
		environment, exists := s.deliveryEnvironmentLocked(binding.EnvironmentID)
		if !exists || environment.Tier != "production" {
			return DeliveryEnvironmentEvidencePolicyVersion{}, NewValidationError("目标应用未绑定到已确认的生产逻辑环境")
		}
		policy, ok := currentEnvironmentEvidencePolicy(s.deliveryEvidencePolicies[binding.EnvironmentID])
		if !ok {
			return DeliveryEnvironmentEvidencePolicyVersion{}, NewValidationError("目标生产环境尚未配置必需验证证据策略")
		}
		return policy, nil
	}
	return DeliveryEnvironmentEvidencePolicyVersion{}, NewValidationError("目标生产应用尚未绑定逻辑环境，不能确认晋级证据策略")
}

func (s *Store) verifyReleaseEvidenceForApp(release ImageRelease, appID int, now time.Time) error {
	policy, err := s.DeliveryEnvironmentEvidencePolicyForApp(appID)
	if err != nil {
		return err
	}
	return release.VerifiedForEnvironment(policy, now)
}

func (s *Store) verifyReleaseEvidenceForAppLocked(release ImageRelease, appID int, now time.Time) error {
	policy, err := s.currentDeliveryEnvironmentEvidencePolicyForAppLocked(appID)
	if err != nil {
		return err
	}
	return release.VerifiedForEnvironment(policy, now)
}

func (r ImageRelease) VerifiedForEnvironment(policy DeliveryEnvironmentEvidencePolicyVersion, now time.Time) error {
	if r.ImageDigest == nil || NormalizeDigest(*r.ImageDigest) == "" {
		return NewValidationError("制品未固化 digest，不能晋级")
	}
	if policy.EnvironmentID <= 0 || policy.Version <= 0 {
		return NewValidationError("目标环境验证证据策略未配置")
	}
	validated, err := normalizeEnvironmentEvidencePolicy(policy.RequiredVerificationKinds, policy.MaxEvidenceAgeMinutes, policy.TrustedSubmitters)
	if err != nil || !sameEnvironmentEvidencePolicy(policy, validated) {
		return NewValidationError("目标环境验证证据策略无效，请联系管理员重新保存")
	}
	latestByKind := map[string]VerificationEvidence{}
	for _, evidence := range r.Metadata.VerificationHistory {
		if current, exists := latestByKind[evidence.Kind]; !exists || !evidence.RecordedAt.Before(current.RecordedAt) {
			latestByKind[evidence.Kind] = evidence
		}
	}
	if latest := r.Metadata.Verification; latest != nil {
		if current, exists := latestByKind[latest.Kind]; !exists || !latest.RecordedAt.Before(current.RecordedAt) {
			latestByKind[latest.Kind] = *latest
		}
	}
	for _, kind := range policy.RequiredVerificationKinds {
		evidence, exists := latestByKind[kind]
		if !exists {
			return NewValidationError("缺少必需的 %s 验证证据", evidenceKindLabel(kind))
		}
		if NormalizeDigest(evidence.Digest) != NormalizeDigest(*r.ImageDigest) {
			return NewValidationError("%s 验证证据不属于当前制品", evidenceKindLabel(kind))
		}
		if evidence.Status != "passed" {
			return NewValidationError("必需的 %s 验证未通过", evidenceKindLabel(kind))
		}
		if evidence.RecordedAt.IsZero() || evidence.RecordedAt.After(now) || now.Sub(evidence.RecordedAt) > time.Duration(policy.MaxEvidenceAgeMinutes)*time.Minute || evidence.ValidUntil.IsZero() || !now.Before(evidence.ValidUntil) {
			return NewValidationError("必需的 %s 验证证据已过期", evidenceKindLabel(kind))
		}
		trusted := false
		for _, identity := range policy.TrustedSubmitters[kind] {
			if identity == evidence.RecordedBy {
				trusted = true
				break
			}
		}
		if !trusted {
			return NewValidationError("%s 验证来源 %q 不在目标环境的可信来源策略中", evidenceKindLabel(kind), evidence.RecordedBy)
		}
	}
	return nil
}

func evidenceKindLabel(kind string) string {
	switch kind {
	case "ci":
		return "CI"
	case "smoke":
		return "Smoke"
	case "manual":
		return "人工验收"
	default:
		return kind
	}
}
