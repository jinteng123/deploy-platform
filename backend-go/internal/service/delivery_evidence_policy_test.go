package service

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func evidencePolicyStore() *Store {
	store := NewStoreWithData(
		[]Cluster{{ID: 2, Name: "prod", EnvironmentTier: "production", DeployPolicy: "manual", Enabled: true}},
		[]App{{ID: 7, ClusterID: 2, AppName: "shop", Namespace: "prod", DeploymentName: "shop", ContainerName: "api"}},
		nil, nil, nil,
	)
	store.LoadDeliveryModel(DeliveryModel{
		Environments: []DeliveryEnvironment{{ID: 12, Code: "prod", Name: "生产", Tier: "production"}},
		Applications: []DeliveryApplication{{ID: 3, Name: "商城"}},
		Components:   []DeliveryComponent{{ID: 4, ApplicationID: 3, Name: "shop"}},
		Bindings:     []DeliveryBinding{{ID: 5, AppID: 7, ApplicationID: 3, ComponentID: 4, EnvironmentID: 12}},
	})
	return store
}

func validEvidencePolicyInput() ([]string, int, map[string][]string) {
	return []string{"ci", "smoke"}, 60, map[string][]string{
		"ci":    {"apikey:yunxiao-ci"},
		"smoke": {"apikey:smoke-runner"},
	}
}

func TestEnvironmentEvidencePolicyIsVersionedAndFailClosed(t *testing.T) {
	store := evidencePolicyStore()
	if _, err := store.DeliveryEnvironmentEvidencePolicyForApp(7); !errors.Is(err, ErrValidation) {
		t.Fatalf("unconfigured production policy should block: %v", err)
	}
	kinds, freshness, trusted := validEvidencePolicyInput()
	first, created, err := store.SaveDeliveryEnvironmentEvidencePolicy(12, 0, kinds, freshness, trusted, "admin")
	if err != nil || !created || first.Version != 1 || len(first.RequiredVerificationKinds) != 2 {
		t.Fatalf("create policy=%+v created=%v err=%v", first, created, err)
	}
	kindsFromRead, err := store.CurrentDeliveryEnvironmentEvidencePolicy(12)
	if err != nil || kindsFromRead.Version != 1 || kindsFromRead.TrustedSubmitters["ci"][0] != "apikey:yunxiao-ci" {
		t.Fatalf("read policy=%+v err=%v", kindsFromRead, err)
	}
	// Returned maps and slices must not be able to mutate the stored revision.
	kindsFromRead.RequiredVerificationKinds[0] = "manual"
	kindsFromRead.TrustedSubmitters["ci"][0] = "apikey:attacker"
	unchanged, _ := store.CurrentDeliveryEnvironmentEvidencePolicy(12)
	if unchanged.RequiredVerificationKinds[0] == "manual" || unchanged.TrustedSubmitters["ci"][0] == "apikey:attacker" {
		t.Fatal("caller mutated the stored immutable policy")
	}
	if _, created, err := store.SaveDeliveryEnvironmentEvidencePolicy(12, 0, kinds, freshness, trusted, "admin"); !errors.Is(err, ErrConflict) || created {
		t.Fatalf("stale version should conflict: created=%v err=%v", created, err)
	}
	if _, created, err := store.SaveDeliveryEnvironmentEvidencePolicy(12, 1, kinds, freshness, trusted, "admin"); err != nil || created {
		t.Fatalf("identical save should reuse revision: created=%v err=%v", created, err)
	}
	_, created, err = store.SaveDeliveryEnvironmentEvidencePolicy(12, 1, kinds, 30, trusted, "admin")
	if err != nil || !created {
		t.Fatalf("changed policy should append revision: created=%v err=%v", created, err)
	}
	history, err := store.DeliveryEnvironmentEvidencePolicyHistory(12)
	if err != nil || history.CurrentVersion != 2 || len(history.Versions) != 2 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
}

func TestEnvironmentEvidencePolicyValidationRequiresTrustedSourcePerKind(t *testing.T) {
	store := evidencePolicyStore()
	cases := []struct {
		name      string
		kinds     []string
		age       int
		trusted   map[string][]string
		wantError string
	}{
		{"no requirements", nil, 60, nil, "至少选择一种"},
		{"unsupported type", []string{"scan"}, 60, map[string][]string{"scan": {"apikey:scanner"}}, "不支持"},
		{"missing identity", []string{"ci"}, 60, nil, "至少需要一个可信"},
		{"wildcard", []string{"ci"}, 60, map[string][]string{"ci": {"*"}}, "身份无效"},
		{"human cannot report CI", []string{"ci"}, 60, map[string][]string{"ci": {"alice"}}, "apikey"},
		{"API key cannot report manual", []string{"manual"}, 60, map[string][]string{"manual": {"apikey:operator"}}, "人工账号"},
		{"runner cannot report manual", []string{"manual"}, 60, map[string][]string{"manual": {"runner:12"}}, "机器身份"},
		{"runner identity must be canonical positive ID", []string{"ci"}, 60, map[string][]string{"ci": {"runner:012"}}, "runner:<接入 ID>"},
		{"runner ID must be positive", []string{"smoke"}, 60, map[string][]string{"smoke": {"runner:0"}}, "runner:<接入 ID>"},
		{"age too long", []string{"ci"}, MaxEvidenceFreshnessMinutes + 1, map[string][]string{"ci": {"apikey:ci"}}, "1–10080"},
		{"unused trusted kind", []string{"ci"}, 60, map[string][]string{"ci": {"apikey:ci"}, "smoke": {"apikey:smoke"}}, "只能配置在必需"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := store.SaveDeliveryEnvironmentEvidencePolicy(12, 0, test.kinds, test.age, test.trusted, "admin")
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error=%v, want substring %q", err, test.wantError)
			}
		})
	}
	if _, _, err := store.SaveDeliveryEnvironmentEvidencePolicy(12, 0, []string{"ci"}, 60, map[string][]string{"ci": {"apikey:ci"}}, "admin"); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	if _, _, err := store.SaveDeliveryEnvironmentEvidencePolicy(12, 1, []string{"ci"}, 60, map[string][]string{"ci": {"runner:12"}}, "admin"); err != nil {
		t.Fatalf("valid component-scoped Runner identity rejected: %v", err)
	}
}

func TestEnvironmentEvidencePolicyEnforcesLatestRequiredEvidenceAndSource(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	digest := "sha256:" + strings.Repeat("ab", 32)
	release := ImageRelease{ImageDigest: &digest}
	policy := DeliveryEnvironmentEvidencePolicyVersion{
		EnvironmentID: 12, Version: 3,
		RequiredVerificationKinds: []string{"ci", "smoke"}, MaxEvidenceAgeMinutes: 60,
		TrustedSubmitters: map[string][]string{"ci": {"apikey:yunxiao-ci"}, "smoke": {"apikey:smoke-runner"}},
	}
	ci := VerificationEvidence{Digest: digest, Kind: "ci", Status: "passed", RecordedBy: "apikey:yunxiao-ci", RecordedAt: now.Add(-5 * time.Minute), ValidUntil: now.Add(time.Hour)}
	smoke := VerificationEvidence{Digest: digest, Kind: "smoke", Status: "passed", RecordedBy: "apikey:smoke-runner", RecordedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour)}
	release.Metadata.VerificationHistory = []VerificationEvidence{ci, smoke}
	release.Metadata.Verification = &smoke
	if err := release.VerifiedForEnvironment(policy, now); err != nil {
		t.Fatalf("complete fresh evidence rejected: %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(*ImageRelease)
		wantErr string
	}{
		{"missing smoke", func(r *ImageRelease) {
			r.Metadata.VerificationHistory = []VerificationEvidence{ci}
			r.Metadata.Verification = &ci
		}, "缺少必需的 Smoke"},
		{"failed latest smoke", func(r *ImageRelease) {
			passed := smoke
			passed.RecordedAt = now.Add(-2 * time.Minute)
			failed := smoke
			failed.Status = "failed"
			failed.RecordedAt = now.Add(-time.Minute)
			r.Metadata.VerificationHistory = []VerificationEvidence{ci, passed, failed}
			r.Metadata.Verification = &failed
		}, "Smoke 验证未通过"},
		{"untrusted source", func(r *ImageRelease) {
			forged := smoke
			forged.RecordedBy = "apikey:other"
			r.Metadata.VerificationHistory = []VerificationEvidence{ci, forged}
			r.Metadata.Verification = &forged
		}, "不在目标环境的可信来源策略中"},
		{"stale evidence", func(r *ImageRelease) {
			stale := ci
			stale.RecordedAt = now.Add(-61 * time.Minute)
			r.Metadata.VerificationHistory = []VerificationEvidence{stale, smoke}
			r.Metadata.Verification = &smoke
		}, "CI 验证证据已过期"},
		{"wrong digest", func(r *ImageRelease) {
			wrong := smoke
			wrong.Digest = "sha256:" + strings.Repeat("cd", 32)
			r.Metadata.VerificationHistory = []VerificationEvidence{ci, wrong}
			r.Metadata.Verification = &wrong
		}, "不属于当前制品"},
		{"equal timestamps prefer later failure", func(r *ImageRelease) {
			failed := smoke
			failed.Status = "failed"
			r.Metadata.VerificationHistory = []VerificationEvidence{ci, smoke, failed}
			r.Metadata.Verification = &failed
		}, "Smoke 验证未通过"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			candidate := release
			candidate.Metadata.VerificationHistory = append([]VerificationEvidence{}, release.Metadata.VerificationHistory...)
			test.mutate(&candidate)
			if err := candidate.VerifiedForEnvironment(policy, now); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("verification error=%v, want substring %q", err, test.wantErr)
			}
		})
	}
	invalidPolicy := policy
	invalidPolicy.TrustedSubmitters = nil
	if err := release.VerifiedForEnvironment(invalidPolicy, now); err == nil || !strings.Contains(err.Error(), "策略无效") {
		t.Fatalf("invalid persisted policy should fail closed, got %v", err)
	}
	runnerPolicy := policy
	runnerPolicy.TrustedSubmitters = map[string][]string{"ci": {"runner:12"}, "smoke": {"runner:12"}}
	runnerRelease := release
	runnerCI, runnerSmoke := ci, smoke
	runnerCI.RecordedBy, runnerSmoke.RecordedBy = "runner:12", "runner:12"
	runnerRelease.Metadata.VerificationHistory = []VerificationEvidence{runnerCI, runnerSmoke}
	runnerRelease.Metadata.Verification = &runnerSmoke
	if err := runnerRelease.VerifiedForEnvironment(runnerPolicy, now); err != nil {
		t.Fatalf("trusted component-scoped Runner evidence rejected: %v", err)
	}
}
