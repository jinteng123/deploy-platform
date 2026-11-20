package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDeliveryPlanArtifactSnapshotPinsSafeBuildProvenance(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	pipelineID, pipelineName, branch, commitID, build := "pipe-42", "orders-build", "main", "commit-123", "build-7"
	release := ImageRelease{
		ID: 17, EventID: "event-17", Source: "yunxiao", ImageFull: "registry.example.com/orders/api:v7",
		ImageDigest: &digest, Version: "v7", PipelineID: &pipelineID,
		PipelineName: &pipelineName, Branch: &branch, CommitID: &commitID, BuildNumber: &build,
		Metadata: ReleaseMetadata{
			DigestSource: "ci",
			ConfigurationSource: &ConfigurationSourceRevision{
				Repository: "https://git.example.com/orders/deploy-config.git",
				Path:       "charts/orders", Revision: strings.Repeat("a", 40),
				RecordedBy: "runner:4", RecordedAt: time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC),
			},
			Binding: &App{
				SourceKind: "repository", SourceRepoURL: "https://git.example.com/orders/mono.git",
				SourcePath: "services/api", ImageRepository: "registry.example.com/orders/api",
				DeploymentConfigRepoURL: "https://git.example.com/orders/deploy-config.git",
				DeploymentConfigPath:    "charts/orders",
			},
		},
	}
	artifact := deliveryPlanArtifactSnapshot(release)
	if artifact.Source != "yunxiao" || artifact.SourceKind != "repository" || artifact.SourceRepository != "https://git.example.com/orders/mono.git" || artifact.SourcePath != "services/api" || artifact.ImageRepository != "registry.example.com/orders/api" || artifact.DigestSource != "ci" {
		t.Fatalf("artifact provenance was not pinned: %+v", artifact)
	}
	if artifact.PipelineID == nil || *artifact.PipelineID != pipelineID || artifact.PipelineName == nil || *artifact.PipelineName != pipelineName || artifact.Branch == nil || *artifact.Branch != branch || artifact.CommitID == nil || *artifact.CommitID != commitID || artifact.Build == nil || *artifact.Build != build {
		t.Fatalf("CI identifiers were not pinned: %+v", artifact)
	}
	if artifact.ConfigurationSource == nil || artifact.ConfigurationSource.Repository != "https://git.example.com/orders/deploy-config.git" || artifact.ConfigurationSource.Path != "charts/orders" || artifact.ConfigurationSource.Revision != strings.Repeat("a", 40) || artifact.ConfigurationSource.RecordedBy != "runner:4" {
		t.Fatalf("configuration source revision was not pinned: %+v", artifact.ConfigurationSource)
	}
	if artifact.DeploymentConfigRepository != "https://git.example.com/orders/deploy-config.git" || artifact.DeploymentConfigPath != "charts/orders" {
		t.Fatalf("component configuration mapping was not pinned: %+v", artifact)
	}

	unsafe := release
	unsafe.Metadata.Binding = &App{SourceKind: "repository", SourceRepoURL: "https://build-user:token-secret@git.example.com/orders/mono.git?access_token=query-secret", SourcePath: "services/api"}
	unsafeArtifact := deliveryPlanArtifactSnapshot(unsafe)
	serialized, err := json.Marshal(unsafeArtifact)
	if err != nil {
		t.Fatal(err)
	}
	if unsafeArtifact.SourceRepository != "" || strings.Contains(string(serialized), "token-secret") || strings.Contains(string(serialized), "query-secret") {
		t.Fatalf("unsafe repository credentials entered immutable snapshot: %s", serialized)
	}
}

func TestDeliveryConfigurationReferencesCaptureNamesWithoutValues(t *testing.T) {
	deployment := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{
				"env": []any{
					map[string]any{"name": "REGION", "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "app-settings", "key": "region"}}},
					map[string]any{"name": "PASSWORD", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "app-credentials", "key": "password"}}},
				},
				"envFrom": []any{
					map[string]any{"configMapRef": map[string]any{"name": "app-settings"}},
					map[string]any{"secretRef": map[string]any{"name": "app-credentials"}},
				},
			}},
			"initContainers": []any{map[string]any{"envFrom": []any{
				map[string]any{"configMapRef": map[string]any{"name": "migration-settings"}},
			}}},
			"volumes": []any{
				map[string]any{"name": "config", "configMap": map[string]any{"name": "volume-settings"}},
				map[string]any{"name": "credentials", "secret": map[string]any{"secretName": "volume-credentials"}},
				map[string]any{"name": "projected", "projected": map[string]any{"sources": []any{
					map[string]any{"configMap": map[string]any{"name": "projected-settings"}},
					map[string]any{"secret": map[string]any{"name": "projected-credentials"}},
				}}},
			},
			"imagePullSecrets": []any{map[string]any{"name": "registry-auth"}},
		}}},
	}}
	refs, known := deliveryConfigurationReferences(deployment)
	if !known {
		t.Fatal("valid Pod template references reported unknown")
	}
	want := []DeliveryConfigurationReference{
		{Kind: "ConfigMap", Name: "app-settings"},
		{Kind: "ConfigMap", Name: "migration-settings"},
		{Kind: "ConfigMap", Name: "projected-settings"},
		{Kind: "ConfigMap", Name: "volume-settings"},
		{Kind: "Secret", Name: "app-credentials"},
		{Kind: "Secret", Name: "projected-credentials"},
		{Kind: "Secret", Name: "registry-auth"},
		{Kind: "Secret", Name: "volume-credentials"},
	}
	if len(refs) != len(want) {
		t.Fatalf("references=%+v, want %+v", refs, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("references=%+v, want %+v", refs, want)
		}
	}
	serialized, err := json.Marshal(refs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), "password") || strings.Contains(string(serialized), "secret-value") {
		t.Fatalf("configuration reference snapshot contains a value: %s", serialized)
	}
}

func TestDeliveryConfigurationReferencesFailClosedOnMalformedPodTemplates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "env list has unexpected type",
			mutate: func(podSpec map[string]any) {
				podSpec["containers"] = []any{map[string]any{"name": "app", "env": "not-a-list"}}
			},
		},
		{
			name: "config map selector has no name",
			mutate: func(podSpec map[string]any) {
				podSpec["containers"] = []any{map[string]any{
					"name": "app",
					"env": []any{map[string]any{
						"name":      "SETTING",
						"valueFrom": map[string]any{"configMapKeyRef": map[string]any{"key": "setting"}},
					}},
				}}
			},
		},
		{
			name: "projected source is malformed",
			mutate: func(podSpec map[string]any) {
				podSpec["volumes"] = []any{map[string]any{
					"name":      "settings",
					"projected": map[string]any{"sources": []any{"not-an-object"}},
				}}
			},
		},
		{
			name: "image pull secrets has unexpected type",
			mutate: func(podSpec map[string]any) {
				podSpec["imagePullSecrets"] = "not-a-list"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			podSpec := map[string]any{"containers": []any{map[string]any{"name": "app"}}}
			test.mutate(podSpec)
			deployment := &unstructured.Unstructured{Object: map[string]any{
				"spec": map[string]any{"template": map[string]any{"spec": podSpec}},
			}}
			refs, known := deliveryConfigurationReferences(deployment)
			if known || refs != nil {
				t.Fatalf("malformed Pod template returned a trusted reference list: refs=%+v known=%t", refs, known)
			}
		})
	}
}

func TestValidateLiveDeliveryConfigurationReferencesDetectsPostApprovalDrift(t *testing.T) {
	runtime := &recordingK8s{deliveryEnvFrom: []any{
		map[string]any{"configMapRef": map[string]any{"name": "orders-runtime-v3"}},
	}}
	plan := DeliveryPlan{Snapshot: DeliveryPlanSnapshot{SchemaVersion: 7, Target: DeliveryPlanTarget{
		Namespace: "ns-prod", Deployment: "orders-api", WorkloadUID: "mock-orders-api", ServiceAccountName: "default",
		ConfigurationReferences: []DeliveryConfigurationReference{{
			Kind: "ConfigMap", Name: "orders-runtime-v3", UID: "uid-configmaps-orders-runtime-v3", ResourceVersion: "1",
		}},
	}}}
	if err := validateLiveDeliveryConfigurationReferences(context.Background(), runtime, plan); err != nil {
		t.Fatalf("unchanged approved configuration references: %v", err)
	}

	runtime.configurationMetadata = map[string]metav1.PartialObjectMetadata{
		"configmaps/ns-prod/orders-runtime-v3": {ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns-prod", Name: "orders-runtime-v3", UID: "uid-configmaps-orders-runtime-v3", ResourceVersion: "2",
		}},
	}
	if err := validateLiveDeliveryConfigurationReferences(context.Background(), runtime, plan); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed ConfigMap version error=%v, want conflict", err)
	}
}

func TestValidateLiveDeliveryConfigurationReferencesChecksNamesAndServiceAccount(t *testing.T) {
	runtime := &recordingK8s{deliveryEnvFrom: []any{
		map[string]any{"secretRef": map[string]any{"name": "orders-runtime-v2"}},
	}}
	plan := DeliveryPlan{Snapshot: DeliveryPlanSnapshot{SchemaVersion: 7, Target: DeliveryPlanTarget{
		Namespace: "ns-prod", Deployment: "orders-api", WorkloadUID: "mock-orders-api", ServiceAccountName: "orders-runtime",
		ConfigurationReferences: []DeliveryConfigurationReference{{Kind: "Secret", Name: "orders-runtime-v2"}},
	}}}
	if err := validateLiveDeliveryConfigurationReferences(context.Background(), runtime, plan); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed ServiceAccount error=%v, want conflict", err)
	}

	plan.Snapshot.Target.ServiceAccountName = "default"
	plan.Snapshot.Target.ConfigurationReferences = []DeliveryConfigurationReference{{Kind: "ConfigMap", Name: "orders-runtime-v2"}}
	if err := validateLiveDeliveryConfigurationReferences(context.Background(), runtime, plan); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed reference kind error=%v, want conflict", err)
	}
}

func TestValidateLiveDeliveryConfigurationReferencesAllowsReviewedNamesOnlySnapshot(t *testing.T) {
	runtime := &recordingK8s{
		deliveryEnvFrom:           []any{map[string]any{"configMapRef": map[string]any{"name": "orders-runtime-v3"}}},
		configurationMetadataErrs: map[string]error{"configmaps/ns-prod/orders-runtime-v3": errors.New("metadata access unavailable")},
	}
	plan := DeliveryPlan{Snapshot: DeliveryPlanSnapshot{SchemaVersion: 7, Target: DeliveryPlanTarget{
		Namespace: "ns-prod", Deployment: "orders-api", WorkloadUID: "mock-orders-api", ServiceAccountName: "default",
		ConfigurationReferences: []DeliveryConfigurationReference{{Kind: "ConfigMap", Name: "orders-runtime-v3"}},
	}}}
	if err := validateLiveDeliveryConfigurationReferences(context.Background(), runtime, plan); err != nil {
		t.Fatalf("reviewed name-only snapshot should compare names without claiming version proof: %v", err)
	}
}

func TestValidateLiveDeliveryConfigurationReferencesRejectsRecreatedDeployment(t *testing.T) {
	runtime := &recordingK8s{deploymentUID: "original-uid"}
	plan := DeliveryPlan{Snapshot: DeliveryPlanSnapshot{SchemaVersion: 7, Target: DeliveryPlanTarget{
		Namespace: "ns-prod", Deployment: "orders-api", WorkloadUID: "original-uid", ServiceAccountName: "default",
		ConfigurationReferences: []DeliveryConfigurationReference{},
	}}}
	if err := validateLiveDeliveryConfigurationReferences(context.Background(), runtime, plan); err != nil {
		t.Fatalf("unchanged Deployment identity: %v", err)
	}
	runtime.deploymentUID = "replacement-uid"
	if err := validateLiveDeliveryConfigurationReferences(context.Background(), runtime, plan); !errors.Is(err, ErrConflict) {
		t.Fatalf("recreated Deployment error=%v, want conflict", err)
	}
}

func TestCloneDeliveryPlanPreservesLegacyConfigurationSnapshotAbsence(t *testing.T) {
	legacy := cloneDeliveryPlan(DeliveryPlan{
		Snapshot: DeliveryPlanSnapshot{SchemaVersion: 5},
	})
	if legacy.Snapshot.Target.ConfigurationReferences != nil {
		t.Fatalf("legacy snapshot absence became an empty verified list: %+v", legacy.Snapshot.Target.ConfigurationReferences)
	}
	if legacy.Snapshot.Checks != nil {
		t.Fatalf("legacy preflight absence became an empty list: %+v", legacy.Snapshot.Checks)
	}

	current := cloneDeliveryPlan(DeliveryPlan{
		Snapshot: DeliveryPlanSnapshot{SchemaVersion: 7, Target: DeliveryPlanTarget{WorkloadUID: "deployment-1", ServiceAccountName: "default", ConfigurationReferences: []DeliveryConfigurationReference{}}},
	})
	if current.Snapshot.Target.ConfigurationReferences == nil {
		t.Fatal("v7 empty configuration reference inventory lost its explicit empty value")
	}
	if err := validateDeliveryPlanConfigurationSnapshot(current); err != nil {
		t.Fatalf("complete v7 empty configuration inventory rejected: %v", err)
	}
	missingUID := current
	missingUID.Snapshot.Target.WorkloadUID = ""
	if err := validateDeliveryPlanConfigurationSnapshot(missingUID); err == nil {
		t.Fatal("v7 configuration snapshot without a Deployment UID remained approvable")
	}
	legacyV6 := current
	legacyV6.Snapshot.SchemaVersion = 6
	if err := validateDeliveryPlanConfigurationSnapshot(legacyV6); err == nil {
		t.Fatal("v6 configuration reference snapshot remained approvable after metadata-version support")
	}
	incomplete := current
	incomplete.Snapshot.Target.ServiceAccountName = ""
	if err := validateDeliveryPlanConfigurationSnapshot(incomplete); err == nil {
		t.Fatal("v7 configuration list without the ServiceAccount source was accepted")
	}
}

func snapshotObject() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"uid": "deployment-1"},
		"spec": map[string]any{"replicas": int64(3), "template": map[string]any{"spec": map[string]any{"containers": []any{
			map[string]any{"name": "app", "image": "reg.io/app:v1", "env": []any{map[string]any{"name": "PASSWORD", "value": "secret-not-to-store"}}},
			map[string]any{"name": "sidecar", "image": "reg.io/sidecar:v1"},
		}}}},
	}}
}
func TestConfigurationFingerprintSeparatesImageAndAutoscalingFromOtherChanges(t *testing.T) {
	obj := snapshotObject()
	base, err := workloadHash(obj, "app", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(base) != 64 {
		t.Fatal("not a SHA256 fingerprint")
	}
	for _, tc := range []struct {
		name   string
		change func(*unstructured.Unstructured)
		same   bool
	}{
		{"candidate image", func(o *unstructured.Unstructured) {
			cs, _, _ := unstructured.NestedSlice(o.Object, "spec", "template", "spec", "containers")
			cs[0].(map[string]any)["image"] = "reg.io/app:v2"
			_ = unstructured.SetNestedSlice(o.Object, cs, "spec", "template", "spec", "containers")
		}, true},
		{"autoscale", func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, int64(5), "spec", "replicas")
		}, true},
		{"environment", func(o *unstructured.Unstructured) {
			cs, _, _ := unstructured.NestedSlice(o.Object, "spec", "template", "spec", "containers")
			cs[0].(map[string]any)["env"] = []any{map[string]any{"name": "PASSWORD", "value": "changed"}}
			_ = unstructured.SetNestedSlice(o.Object, cs, "spec", "template", "spec", "containers")
		}, false},
		{"sidecar image", func(o *unstructured.Unstructured) {
			cs, _, _ := unstructured.NestedSlice(o.Object, "spec", "template", "spec", "containers")
			cs[1].(map[string]any)["image"] = "reg.io/sidecar:v2"
			_ = unstructured.SetNestedSlice(o.Object, cs, "spec", "template", "spec", "containers")
		}, false},
		{"recreated workload", func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, "deployment-2", "metadata", "uid")
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := obj.DeepCopy()
			tc.change(changed)
			got, err := workloadHash(changed, "app", false)
			if err != nil {
				t.Fatal(err)
			}
			if (got == base) != tc.same {
				t.Fatalf("configuration identity did not reflect %s", tc.name)
			}
		})
	}
	cs, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	if cs[0].(map[string]any)["image"] != "reg.io/app:v1" {
		t.Fatal("fingerprint mutated the source object")
	}
	if _, err := workloadHash(obj, "missing", false); err == nil {
		t.Fatal("missing target container accepted")
	}
}

func TestChangedConfigurationInvalidatesApprovalAndTaskExecution(t *testing.T) {
	_, store, _, rec, w, cd := cdFixture(t)
	w.Close()
	r := seedRelease(store, "success", nil)
	store.releases = append(store.releases, r)
	req, err := cd.RequestPromotion(r.ID, 2, "alice")
	if err != nil {
		t.Fatal(err)
	}
	rec.mu.Lock()
	rec.templateEnv = "changed"
	rec.mu.Unlock()
	if _, _, err := cd.ApprovePromotion(context.Background(), req.ID, "bob"); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed config approved: %v", err)
	}
	rec.mu.Lock()
	rec.templateEnv = ""
	rec.mu.Unlock()
	c, _ := store.FindCluster(2)
	a, _ := store.FindAppByName(2, "app")
	_, task, err := store.ApproveAndCreateDeploy(req, "bob", promotionLog(r, c, a))
	if err != nil {
		t.Fatal(err)
	}
	rec.mu.Lock()
	rec.templateEnv = "changed-after-approval"
	rec.mu.Unlock()
	if err := w.pipeline.Run(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	actual, _ := store.FindLog(task.ID)
	if actual.Status != "failed" || len(rec.recorded()) != 0 {
		t.Fatalf("stale task applied: %+v calls=%v", actual, rec.recorded())
	}
}

func TestPendingProductionTaskCannotBecomeAutomaticAfterTierChange(t *testing.T) {
	_, store, _, rec, w, cd := cdFixture(t)
	w.Close()
	r := seedRelease(store, "success", nil)
	store.releases = append(store.releases, r)
	req, err := cd.RequestPromotion(r.ID, 2, "alice")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := store.FindCluster(2)
	a, _ := store.FindAppByName(2, "app")
	_, task, err := store.ApproveAndCreateDeploy(req, "bob", promotionLog(r, c, a))
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.clusters[1].EnvironmentTier = "non_production"
	store.mu.Unlock()
	if err := w.pipeline.Run(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	actual, _ := store.FindLog(task.ID)
	if actual.Status != "failed" || len(rec.recorded()) != 0 {
		t.Fatalf("tier change bypassed approval snapshot: %+v", actual)
	}
}

func TestApprovalCanReserveAdditionalStrategyResources(t *testing.T) {
	_, store, _, _, worker, cd := cdFixture(t)
	worker.Close()
	release := seedRelease(store, "success", nil)
	store.releases = append(store.releases, release)
	request, err := cd.RequestPromotion(release.ID, 2, "alice")
	if err != nil {
		t.Fatal(err)
	}
	cluster, _ := store.FindCluster(2)
	app, _ := store.FindAppByName(2, "app")
	entry := ResourceLock{ClusterID: 2, Namespace: app.Namespace, Kind: "service", Name: "orders-entry"}
	_, task, err := store.ApproveAndCreateDeploy(request, "bob", promotionLog(release, cluster, app), entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CheckManagedWrite(2, "services", app.Namespace, "orders-entry"); !errors.Is(err, ErrConflict) {
		t.Fatalf("approval did not reserve the strategy Service: %v", err)
	}
	task.Status = "success"
	if err := store.UpdateLogExecution(task); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckManagedWrite(2, "services", app.Namespace, "orders-entry"); err != nil {
		t.Fatalf("terminal task retained the Service lock: %v", err)
	}
}
