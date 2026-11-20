package service

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBlueGreenQuotaCheckBlocksKnownInsufficientQuota(t *testing.T) {
	deployment := quotaTestDeployment()
	quotas := quotaTestList(map[string]any{
		"pods": "20", "requests.cpu": "1", "requests.memory": "2Gi", "limits.cpu": "4", "limits.memory": "4Gi",
		"services": "10", "count/deployments.apps": "10",
	}, map[string]any{
		"pods": "5", "requests.cpu": "750m", "requests.memory": "512Mi", "limits.cpu": "1", "limits.memory": "1Gi",
		"services": "2", "count/deployments.apps": "2",
	})

	status, detail := blueGreenQuotaCheck(deployment, quotas, 2)
	if status != "blocked" {
		t.Fatalf("status=%q, want blocked; detail=%s", status, detail)
	}
	if detail == "" {
		t.Fatal("blocked quota check must explain the limiting quota")
	}
}

func TestBlueGreenQuotaCheckKeepsSchedulabilityAsManualReview(t *testing.T) {
	deployment := quotaTestDeployment()
	quotas := quotaTestList(map[string]any{
		"pods": "20", "requests.cpu": "2", "requests.memory": "2Gi", "limits.cpu": "4", "limits.memory": "4Gi",
		"services": "10", "count/deployments.apps": "10",
	}, map[string]any{
		"pods": "5", "requests.cpu": "500m", "requests.memory": "512Mi", "limits.cpu": "1", "limits.memory": "1Gi",
		"services": "2", "count/deployments.apps": "2",
	})

	status, detail := blueGreenQuotaCheck(deployment, quotas, 2)
	if status != "review" {
		t.Fatalf("status=%q, want review; detail=%s", status, detail)
	}
	if detail == "" {
		t.Fatal("manual capacity review must explain its remaining uncertainty")
	}
}

func TestBlueGreenQuotaCheckReturnsUnknownWhenResourcesCannotBeEstimated(t *testing.T) {
	deployment := quotaTestDeployment()
	containers, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	container := containers[0].(map[string]any)
	container["resources"] = map[string]any{}
	_ = unstructured.SetNestedSlice(deployment.Object, containers, "spec", "template", "spec", "containers")
	quotas := quotaTestList(map[string]any{"requests.cpu": "2"}, map[string]any{"requests.cpu": "500m"})

	status, _ := blueGreenQuotaCheck(deployment, quotas, 2)
	if status != "unknown" {
		t.Fatalf("status=%q, want unknown", status)
	}
}

func TestBlueGreenQuotaCheckReturnsUnknownForPodLevelResourceBudgets(t *testing.T) {
	deployment := quotaTestDeployment()
	if err := unstructured.SetNestedMap(deployment.Object, map[string]any{"requests": map[string]any{"cpu": "1"}}, "spec", "template", "spec", "resources"); err != nil {
		t.Fatal(err)
	}
	quotas := quotaTestList(map[string]any{"requests.cpu": "2"}, map[string]any{"requests.cpu": "500m"})
	status, _ := blueGreenQuotaCheck(deployment, quotas, 2)
	if status != "unknown" {
		t.Fatalf("status=%q, want unknown", status)
	}
}

func TestBlueGreenQuotaCheckDoesNotClaimNodeCapacityFromQuota(t *testing.T) {
	status, detail := blueGreenQuotaCheck(quotaTestDeployment(), &unstructured.UnstructuredList{}, 2)
	if status != "review" || detail == "" {
		t.Fatalf("empty quota list status=%q detail=%q, want explained manual review", status, detail)
	}
}

func TestBlueGreenQuotaCheckKeepsControllerManagedEndpointSliceCountsUnknown(t *testing.T) {
	quotas := quotaTestList(
		map[string]any{"count/endpointslices.discovery.k8s.io": "10"},
		map[string]any{"count/endpointslices.discovery.k8s.io": "2"},
	)
	status, _ := blueGreenQuotaCheck(quotaTestDeployment(), quotas, 2)
	if status != "unknown" {
		t.Fatalf("status=%q, want unknown", status)
	}
}

func quotaTestDeployment() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{"name": "app", "resources": map[string]any{
				"requests": map[string]any{"cpu": "250m", "memory": "128Mi"},
				"limits":   map[string]any{"cpu": "500m", "memory": "256Mi"},
			}}},
		}}},
	}}
}

func quotaTestList(hard, used map[string]any) *unstructured.UnstructuredList {
	return &unstructured.UnstructuredList{Items: []unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "v1", "kind": "ResourceQuota",
		"metadata": map[string]any{"name": "prod-hard"},
		"status":   map[string]any{"hard": hard, "used": used},
	}}}}
}
