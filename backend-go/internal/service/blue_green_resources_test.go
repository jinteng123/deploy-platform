package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"deploy-platform/backend-go/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func blueGreenResourcePlan(t *testing.T) (DeliveryPlan, *unstructured.Unstructured, *unstructured.Unstructured) {
	t.Helper()
	deployment := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{
			"name": "orders-api", "namespace": "prod", "uid": "deployment-uid", "resourceVersion": "42",
			"creationTimestamp": "2026-01-01T00:00:00Z", "ownerReferences": []any{map[string]any{"name": "unexpected"}},
		},
		"spec": map[string]any{
			"replicas": int64(2),
			"selector": map[string]any{"matchLabels": map[string]any{"app": "orders"}},
			"template": map[string]any{
				"metadata": map[string]any{
					"labels":          map[string]any{"app": "orders", "tier": "api"},
					"ownerReferences": []any{map[string]any{"name": "unexpected"}},
				},
				"spec": map[string]any{"containers": []any{map[string]any{"name": "api", "image": "registry.test/orders:v1", "env": []any{map[string]any{"name": "MODE", "value": "prod"}}}}},
			},
		},
		"status": map[string]any{"readyReplicas": int64(2)},
	}}
	service := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{
			"name": "orders-entry", "namespace": "prod", "uid": "service-uid", "resourceVersion": "87",
			"annotations": map[string]any{"cloud.example/lb": "allocate"},
		},
		"spec": map[string]any{
			"type": "LoadBalancer", "clusterIP": "10.0.0.4", "externalIPs": []any{"192.0.2.2"},
			"selector": map[string]any{"app": "orders"},
			"ports":    []any{map[string]any{"name": "http", "port": int64(80), "targetPort": int64(8080), "nodePort": int64(30080)}},
		},
	}}
	hash, err := workloadHash(deployment.DeepCopy(), "api", false)
	if err != nil {
		t.Fatal(err)
	}
	serviceHash, err := serviceConfigurationHash(service)
	if err != nil {
		t.Fatal(err)
	}
	artifactDigest := "sha256:" + strings.Repeat("a", 64)
	plan := DeliveryPlan{
		ID: 19, Strategy: "blue_green",
		Snapshot: DeliveryPlanSnapshot{
			SchemaVersion: 7,
			Artifact:      DeliveryPlanArtifact{ReleaseID: 23, Image: "registry.test/orders@" + artifactDigest, Digest: &artifactDigest},
			Target: DeliveryPlanTarget{
				Namespace: "prod", Deployment: "orders-api", Container: "api", ServiceName: "orders-entry",
				ServiceUID: "service-uid", ServiceSelector: map[string]string{"app": "orders"},
				ServiceConfigHash:  serviceHash,
				ServiceAccountName: "default", ConfigurationReferences: []DeliveryConfigurationReference{},
				StableLabelKey: "delivery.platform.io/app-7", CandidateLabelKey: "delivery.platform.io/run-id",
				CandidateLabelValue: "r-abcdef123456", CandidateRemovedLabels: []string{"app"},
				WorkloadUID: "deployment-uid", CurrentImage: "registry.test/orders:v1", WorkloadHash: hash,
			},
		},
	}
	return plan, deployment, service
}

func stabilizeBlueGreenTestResources(t *testing.T, plan DeliveryPlan, deployment, service *unstructured.Unstructured) {
	t.Helper()
	labels, _, err := unstructured.NestedStringMap(deployment.Object, "spec", "template", "metadata", "labels")
	if err != nil {
		t.Fatal(err)
	}
	labels[plan.Snapshot.Target.StableLabelKey] = "stable"
	if err := unstructured.SetNestedStringMap(deployment.Object, labels, "spec", "template", "metadata", "labels"); err != nil {
		t.Fatal(err)
	}
	selector, err := BlueGreenStableServiceSelector(plan.Snapshot.Target.ServiceSelector, plan.Snapshot.Target.StableLabelKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedStringMap(service.Object, selector, "spec", "selector"); err != nil {
		t.Fatal(err)
	}
	service.SetResourceVersion("88")
}

func TestBuildBlueGreenCandidateResourcesIsolatesAndSanitizes(t *testing.T) {
	plan, deployment, service := blueGreenResourcePlan(t)
	stabilizeBlueGreenTestResources(t, plan, deployment, service)
	resources, err := BuildBlueGreenCandidateResources(plan, 77, deployment, service)
	if err != nil {
		t.Fatalf("BuildBlueGreenCandidateResources: %v", err)
	}
	wantDeployment := blueGreenResourceName(plan.Snapshot.Target.Deployment, plan.ID)
	wantService := blueGreenResourceName(plan.Snapshot.Target.ServiceName, plan.ID)
	if resources.Deployment.GetName() != wantDeployment || resources.Service.GetName() != wantService {
		t.Fatalf("candidate names: Deployment=%s Service=%s", resources.Deployment.GetName(), resources.Service.GetName())
	}
	if resources.Deployment.GetUID() != "" || resources.Deployment.GetResourceVersion() != "" || resources.Deployment.Object["status"] != nil || len(resources.Deployment.GetOwnerReferences()) != 0 {
		t.Fatalf("candidate Deployment copied server metadata: %+v", resources.Deployment.Object["metadata"])
	}
	selector, _, _ := unstructured.NestedStringMap(resources.Deployment.Object, "spec", "selector", "matchLabels")
	if !sameStringMap(selector, resources.CandidateServiceSelector) {
		t.Fatalf("candidate Deployment selector=%v, Service selector=%v", selector, resources.CandidateServiceSelector)
	}
	labels, _, _ := unstructured.NestedStringMap(resources.Deployment.Object, "spec", "template", "metadata", "labels")
	if labels["app"] != "" || labels[plan.Snapshot.Target.StableLabelKey] != "stable" || labels[plan.Snapshot.Target.CandidateLabelKey] != plan.Snapshot.Target.CandidateLabelValue || labels["tier"] != "api" {
		t.Fatalf("candidate Pod labels are not isolated: %v", labels)
	}
	containers, _, _ := unstructured.NestedSlice(resources.Deployment.Object, "spec", "template", "spec", "containers")
	if got := containers[0].(map[string]any)["image"]; got != plan.Snapshot.Artifact.Image {
		t.Fatalf("candidate image=%v, want pinned artifact %s", got, plan.Snapshot.Artifact.Image)
	}
	serviceType, _, _ := unstructured.NestedString(resources.Service.Object, "spec", "type")
	serviceSelector, _, _ := unstructured.NestedStringMap(resources.Service.Object, "spec", "selector")
	if serviceType != "ClusterIP" || !sameStringMap(serviceSelector, resources.CandidateServiceSelector) {
		t.Fatalf("candidate preview Service type=%q selector=%v", serviceType, serviceSelector)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(resources.Service.Object, "spec", "externalIPs"); found {
		t.Fatal("candidate Service copied external IPs")
	}
	ports, _, _ := unstructured.NestedSlice(resources.Service.Object, "spec", "ports")
	if _, found := ports[0].(map[string]any)["nodePort"]; found {
		t.Fatal("candidate Service copied a NodePort allocation")
	}
	if len(resources.Service.GetAnnotations()) != 0 || len(resources.Service.GetOwnerReferences()) != 0 {
		t.Fatalf("candidate Service copied cloud/provider metadata: %+v", resources.Service.Object["metadata"])
	}
}

func TestBlueGreenPlanReservesStableAndCandidateResources(t *testing.T) {
	plan, _, _ := blueGreenResourcePlan(t)
	plan.ID = 33
	plan.Snapshot.Target.ClusterID = 8
	locks, err := BlueGreenPlanLocks(plan)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, lock := range locks {
		got[lock.Kind+"/"+lock.Name] = true
		if lock.ClusterID != 8 || lock.Namespace != plan.Snapshot.Target.Namespace {
			t.Fatalf("lock escaped plan target: %+v", lock)
		}
	}
	for _, want := range []string{
		"service/orders-entry",
		"deployment/orders-api-bg-33",
		"service/orders-entry-bg-33",
	} {
		if !got[want] {
			t.Errorf("blue-green plan did not reserve %s: %v", want, got)
		}
	}
}

func TestValidateBlueGreenCandidateIsolationRechecksLiveSelectors(t *testing.T) {
	runtime := &recordingK8s{}
	ctx := context.Background()
	if err := ValidateBlueGreenCandidateIsolation(ctx, runtime, "prod", map[string]string{
		"delivery.platform.io/run-id": "r-only-this-run",
	}); err != nil {
		t.Fatalf("unique candidate labels should be isolated: %v", err)
	}
	if err := ValidateBlueGreenCandidateIsolation(ctx, runtime, "prod", map[string]string{"app": "app"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("overlapping candidate labels error=%v, want conflict", err)
	}
}

type emptyListRuntime struct{ *recordingK8s }

func (emptyListRuntime) ListResources(context.Context, kube.ResourceDef, string, metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return nil, nil
}

func TestValidateBlueGreenCandidateIsolationRejectsEmptyRead(t *testing.T) {
	err := ValidateBlueGreenCandidateIsolation(context.Background(), emptyListRuntime{&recordingK8s{}}, "prod", map[string]string{"run": "r1"})
	if err == nil || strings.Contains(err.Error(), "%!w(<nil>)") {
		t.Fatalf("empty live resource read must fail clearly, got %v", err)
	}
}

func TestBuildBlueGreenCandidateResourcesRequiresStableEntry(t *testing.T) {
	plan, deployment, service := blueGreenResourcePlan(t)
	if _, err := BuildBlueGreenCandidateResources(plan, 77, deployment, service); !errors.Is(err, ErrConflict) {
		t.Fatalf("candidate construction before Service takeover error=%v, want conflict", err)
	}
	stabilizeBlueGreenTestResources(t, plan, deployment, service)
	if _, err := BuildBlueGreenCandidateResources(plan, 77, deployment, service); err != nil {
		t.Fatalf("candidate construction after stable takeover: %v", err)
	}
}

func TestBuildBlueGreenCandidateResourcesRejectsServiceConfigDrift(t *testing.T) {
	plan, deployment, service := blueGreenResourcePlan(t)
	stabilizeBlueGreenTestResources(t, plan, deployment, service)
	ports, _, _ := unstructured.NestedSlice(service.Object, "spec", "ports")
	ports[0].(map[string]any)["targetPort"] = int64(9090)
	if err := unstructured.SetNestedSlice(service.Object, ports, "spec", "ports"); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildBlueGreenCandidateResources(plan, 77, deployment, service); !errors.Is(err, ErrConflict) {
		t.Fatalf("candidate construction after Service port drift error=%v, want conflict", err)
	}
}

func TestBlueGreenConditionalPatchesTestRevisionAndSelector(t *testing.T) {
	plan, deployment, service := blueGreenResourcePlan(t)
	stablePatch, err := BlueGreenStableLabelPatch(deployment, plan.Snapshot.Target.StableLabelKey)
	if err != nil {
		t.Fatal(err)
	}
	var stableOps []map[string]any
	if err := json.Unmarshal(stablePatch, &stableOps); err != nil {
		t.Fatal(err)
	}
	if len(stableOps) != 2 || stableOps[0]["op"] != "test" || stableOps[0]["value"] != "42" || stableOps[1]["op"] != "add" || stableOps[1]["path"] != "/spec/template/metadata/labels/delivery.platform.io~1app-7" {
		t.Fatalf("stable label patch lacks RV precondition or escaped path: %s", stablePatch)
	}
	if err := unstructured.SetNestedStringMap(service.Object, map[string]string{"app": "orders"}, "spec", "selector"); err != nil {
		t.Fatal(err)
	}
	stableSelector, err := BlueGreenStableServiceSelector(plan.Snapshot.Target.ServiceSelector, plan.Snapshot.Target.StableLabelKey)
	if err != nil {
		t.Fatal(err)
	}
	servicePatch, err := BlueGreenServiceSelectorPatch(service, plan.Snapshot.Target.ServiceSelector, stableSelector)
	if err != nil {
		t.Fatal(err)
	}
	var serviceOps []map[string]any
	if err := json.Unmarshal(servicePatch, &serviceOps); err != nil {
		t.Fatal(err)
	}
	if len(serviceOps) != 3 || serviceOps[0]["value"] != "87" || serviceOps[1]["path"] != "/spec/selector" || serviceOps[2]["op"] != "replace" {
		t.Fatalf("service selector patch lacks atomic resourceVersion/selector tests: %s", servicePatch)
	}
	if _, err := BlueGreenServiceSelectorPatch(service, map[string]string{"app": "changed"}, stableSelector); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Service selector error=%v, want conflict", err)
	}
}

func TestBlueGreenLabelKeysAreIdempotentAndNamesBounded(t *testing.T) {
	plan, deployment, _ := blueGreenResourcePlan(t)
	labels, _, _ := unstructured.NestedStringMap(deployment.Object, "spec", "template", "metadata", "labels")
	labels[plan.Snapshot.Target.StableLabelKey] = "stable"
	if err := unstructured.SetNestedStringMap(deployment.Object, labels, "spec", "template", "metadata", "labels"); err != nil {
		t.Fatal(err)
	}
	patch, err := BlueGreenStableLabelPatch(deployment, plan.Snapshot.Target.StableLabelKey)
	if err != nil || string(patch) != "[]" {
		t.Fatalf("already stable marker patch=%s err=%v", patch, err)
	}
	longName := strings.Repeat("a", 253)
	name := blueGreenResourceName(longName, 9)
	if len(name) > 253 || !strings.HasSuffix(name, "-bg-9") {
		t.Fatalf("candidate DNS name not bounded: length=%d name=%s", len(name), name)
	}
}
