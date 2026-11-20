package service

import (
	"strings"
	"testing"
	"time"
)

func TestDeliveryChangeLeadTimeUsesCanonicalCommitAndDeploymentLinks(t *testing.T) {
	asOf := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	commitOne := asOf.Add(-6 * time.Hour)
	commitTwo := asOf.Add(-2 * time.Hour)
	finishedOne := asOf.Add(-3 * time.Hour)
	finishedTwo := asOf.Add(-time.Hour)
	clusterID := 1
	otherClusterID := 2
	logOneID, logTwoID := int64(11), int64(12)
	releaseOneID, releaseTwoID := int64(21), int64(22)
	digestOne := "sha256:" + strings.Repeat("a", 64)
	digestTwo := "sha256:" + strings.Repeat("b", 64)
	logs := []DeployLog{
		{ID: logOneID, ClusterID: clusterID, AppName: "shop", ReleaseID: releaseOneID, ImageFull: strPtr("reg.io/shop@" + digestOne), Status: "success", EnvironmentTierSnapshot: strPtr("production"), CreatedAt: asOf.Add(-5 * time.Hour), FinishedAt: &finishedOne},
		{ID: logTwoID, ClusterID: clusterID, AppName: "shop", ReleaseID: releaseTwoID, ImageFull: strPtr("reg.io/shop@" + digestTwo), Status: "success", EnvironmentTierSnapshot: strPtr("production"), CreatedAt: asOf.Add(-3 * time.Hour), FinishedAt: &finishedTwo},
	}
	commitIDOne, commitIDTwo := "commit-one", "commit-two"
	releases := []ImageRelease{
		// The legacy release summary can point at another target/latest deployment;
		// each immutable DeployLog.ReleaseID still identifies the artifact.
		{ID: releaseOneID, AppName: "shop", ImageFull: "reg.io/shop:v1", ImageDigest: &digestOne, CommitID: &commitIDOne, ProdClusterID: &otherClusterID, Metadata: ReleaseMetadata{CommitTimestamp: &commitOne}},
		{ID: releaseTwoID, AppName: "shop", ImageFull: "reg.io/shop:v2", ImageDigest: &digestTwo, CommitID: &commitIDTwo, ProdClusterID: &otherClusterID, ProdDeployLogID: &logTwoID, Metadata: ReleaseMetadata{CommitTimestamp: &commitTwo}},
	}

	store := NewStoreWithData(nil, nil, logs, releases, nil)
	metric := store.DeliveryMetricsForTarget("shop", clusterID, 7, asOf).DORA["change_lead_time"]
	if metric.Value == nil || metric.MissingData != "" || metric.SampleCount != 2 {
		t.Fatalf("complete production commit links should produce a metric: %+v", metric)
	}
	// Durations are 3h and 1h; the even-sized median is 2h.
	if want := float64(2 * time.Hour / time.Second); *metric.Value != want {
		t.Fatalf("median change lead time = %v seconds, want %v", *metric.Value, want)
	}
}

func TestDeliveryChangeLeadTimeFailsClosedOnMissingOrInvalidEvidence(t *testing.T) {
	asOf := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	windowStart := asOf.Add(-7 * 24 * time.Hour)
	finishedAt := asOf.Add(-time.Hour)
	createdAt := asOf.Add(-2 * time.Hour)
	commitAt := asOf.Add(-3 * time.Hour)
	clusterID := 1
	logID, releaseID := int64(11), int64(21)
	commitID := "commit-one"
	digest := "sha256:" + strings.Repeat("c", 64)
	baseLog := DeployLog{ID: logID, ClusterID: clusterID, AppName: "shop", ReleaseID: releaseID, ImageFull: strPtr("reg.io/shop@" + digest), Status: "success", EnvironmentTierSnapshot: strPtr("production"), CreatedAt: createdAt, FinishedAt: &finishedAt}
	baseRelease := ImageRelease{ID: releaseID, AppName: "shop", ImageFull: "reg.io/shop:v1", ImageDigest: &digest, CommitID: &commitID, ProdClusterID: &clusterID, ProdDeployLogID: &logID, Metadata: ReleaseMetadata{CommitTimestamp: &commitAt}}
	tests := []struct {
		name    string
		log     DeployLog
		release ImageRelease
	}{
		{name: "missing release linkage", log: func() DeployLog { l := baseLog; l.ReleaseID = 0; return l }(), release: baseRelease},
		{name: "missing commit id", log: baseLog, release: func() ImageRelease { r := baseRelease; r.CommitID = nil; return r }()},
		{name: "missing commit timestamp", log: baseLog, release: func() ImageRelease { r := baseRelease; r.Metadata.CommitTimestamp = nil; return r }()},
		{name: "missing immutable digest", log: baseLog, release: func() ImageRelease { r := baseRelease; r.ImageDigest = nil; return r }()},
		{name: "commit occurs after deployment", log: baseLog, release: func() ImageRelease {
			r := baseRelease
			futureCommit := finishedAt.Add(time.Minute)
			r.Metadata.CommitTimestamp = &futureCommit
			return r
		}()},
		{name: "release belongs to another app", log: baseLog, release: func() ImageRelease {
			r := baseRelease
			r.AppName = "other-app"
			return r
		}()},
		{name: "deployed image does not match release", log: func() DeployLog {
			l := baseLog
			l.ImageFull = strPtr("reg.io/shop:v2")
			return l
		}(), release: baseRelease},
		{name: "missing deployment finish time", log: func() DeployLog { l := baseLog; l.FinishedAt = nil; return l }(), release: baseRelease},
		{name: "missing environment snapshot", log: func() DeployLog { l := baseLog; l.EnvironmentTierSnapshot = nil; return l }(), release: baseRelease},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewStoreWithData(nil, nil, []DeployLog{test.log}, []ImageRelease{test.release}, nil)
			metric := store.DeliveryMetricsForTarget("shop", clusterID, 7, asOf).DORA["change_lead_time"]
			if metric.Value != nil || metric.MissingData == "" {
				t.Fatalf("incomplete source evidence should not produce a DORA value: %+v", metric)
			}
		})
	}

	missingLinkLog := baseLog
	missingLinkLog.ID = logID + 1
	missingLinkLog.ReleaseID = 0
	metric := deliveryChangeLeadTime([]DeployLog{baseLog, missingLinkLog}, []ImageRelease{baseRelease}, clusterID, "shop", windowStart, asOf)
	if metric.Value != nil || metric.SampleCount != 1 || metric.MissingData == "" {
		t.Fatalf("unknown result should still count every valid sample in the window: %+v", metric)
	}

	// The window is based on successful production deployment completion, not
	// build or webhook time. A complete older deployment does not enter it.
	oldFinished := windowStart.Add(-time.Minute)
	oldLog := baseLog
	oldLog.FinishedAt = &oldFinished
	metric = NewStoreWithData(nil, nil, []DeployLog{oldLog}, []ImageRelease{baseRelease}, nil).
		DeliveryMetricsForTarget("shop", clusterID, 7, asOf).DORA["change_lead_time"]
	if metric.Value != nil || metric.MissingData == "" {
		t.Fatalf("empty in-window sample should be unknown: %+v", metric)
	}
}

func TestDeliveryFrequencyForAppDeduplicatesTheSameReleaseAcrossTargets(t *testing.T) {
	asOf := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	windowStart := asOf.Add(-7 * 24 * time.Hour)
	finishedOne := asOf.Add(-time.Hour)
	finishedTwo := asOf.Add(-30 * time.Minute)
	clusterOne, clusterTwo, clusterThree := 1, 2, 3
	releaseOneID, releaseTwoID := int64(21), int64(22)
	digestOne := "sha256:" + strings.Repeat("a", 64)
	digestTwo := "sha256:" + strings.Repeat("b", 64)
	logs := []DeployLog{
		{ID: 11, ClusterID: clusterOne, AppName: "shop", ReleaseID: releaseOneID, ImageFull: strPtr("reg.io/shop@" + digestOne), Status: "success", EnvironmentTierSnapshot: strPtr("production"), CreatedAt: asOf.Add(-2 * time.Hour), FinishedAt: &finishedOne},
		{ID: 12, ClusterID: clusterTwo, AppName: "shop", ReleaseID: releaseOneID, ImageFull: strPtr("reg.io/shop@" + digestOne), Status: "success", EnvironmentTierSnapshot: strPtr("production"), CreatedAt: asOf.Add(-90 * time.Minute), FinishedAt: &finishedTwo},
		{ID: 13, ClusterID: clusterThree, AppName: "shop", ReleaseID: releaseTwoID, ImageFull: strPtr("reg.io/shop@" + digestTwo), Status: "success", EnvironmentTierSnapshot: strPtr("production"), CreatedAt: asOf.Add(-time.Hour), FinishedAt: &finishedOne},
	}
	releases := []ImageRelease{
		{ID: releaseOneID, AppName: "shop", ImageFull: "reg.io/shop:v1", ImageDigest: &digestOne},
		{ID: releaseTwoID, AppName: "shop", ImageFull: "reg.io/shop:v2", ImageDigest: &digestTwo},
	}
	metric := NewStoreWithData(nil, nil, logs, releases, nil).DeliveryMetricsForTarget("shop", 0, 7, asOf).DORA["deployment_frequency"]
	if metric.Value == nil || *metric.Value != 2 || metric.MissingData != "" {
		t.Fatalf("same release across targets should count once, distinct release twice: %+v", metric)
	}

	logs[2].ReleaseID = 0
	metric = deliveryFrequencyForApp(logs, releases, "shop", windowStart, asOf)
	if metric.Value != nil || metric.MissingData == "" {
		t.Fatalf("unlinked successful production deployment must leave frequency unknown: %+v", metric)
	}
}

func TestDeliveryFrequencyFailsClosedOnInvalidScopeOrTime(t *testing.T) {
	asOf := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	createdAt := asOf.Add(-time.Hour)
	finishedAt := asOf.Add(-30 * time.Minute)
	oldFinishedAt := asOf.Add(-8 * 24 * time.Hour)
	tests := []struct {
		name        string
		log         DeployLog
		wantUnknown bool
	}{
		{
			name: "invalid non-empty environment tier",
			log: DeployLog{
				ID: 1, ClusterID: 1, AppName: "shop", Status: "success",
				EnvironmentTierSnapshot: strPtr("prod"), CreatedAt: createdAt, FinishedAt: &finishedAt,
			},
			wantUnknown: true,
		},
		{
			name: "missing creation time",
			log: DeployLog{
				ID: 1, ClusterID: 1, AppName: "shop", Status: "success",
				EnvironmentTierSnapshot: strPtr("production"), FinishedAt: &finishedAt,
			},
			wantUnknown: true,
		},
		{
			name: "invalid environment tier outside the window",
			log: DeployLog{
				ID: 1, ClusterID: 1, AppName: "shop", Status: "success",
				EnvironmentTierSnapshot: strPtr("prod"), CreatedAt: oldFinishedAt, FinishedAt: &oldFinishedAt,
			},
		},
		{
			name: "missing creation time outside the window",
			log: DeployLog{
				ID: 1, ClusterID: 1, AppName: "shop", Status: "success",
				EnvironmentTierSnapshot: strPtr("production"), FinishedAt: &oldFinishedAt,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewStoreWithData(nil, nil, []DeployLog{test.log}, nil, nil)
			metric := store.DeliveryMetricsForTarget("shop", 1, 7, asOf).DORA["deployment_frequency"]
			if test.wantUnknown {
				if metric.Value != nil || metric.MissingData == "" {
					t.Fatalf("incomplete in-window deployment was reported as a frequency value: %+v", metric)
				}
			} else if metric.Value == nil || *metric.Value != 0 || metric.MissingData != "" {
				t.Fatalf("out-of-window deployment affected the frequency result: %+v", metric)
			}
		})
	}
}
