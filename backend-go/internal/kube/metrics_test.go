package kube

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestMetricsResourceAllowlist(t *testing.T) {
	for _, tc := range []struct {
		in      string
		wantErr bool
	}{
		{"nodes", false},
		{"pods", false},
		{"secrets", true},
		{"", true},
		{"foo", true},
	} {
		gvr, err := MetricsResource(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("MetricsResource(%q) should fail, got %v", tc.in, gvr)
			}
			continue
		}
		if err != nil || gvr.Resource != tc.in || gvr.Group != "metrics.k8s.io" || gvr.Version != "v1beta1" {
			t.Fatalf("MetricsResource(%q) = %v, %v", tc.in, gvr, err)
		}
	}
}

func metricObj(containers ...any) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "metrics.k8s.io/v1beta1",
		"kind":       "PodMetrics",
		"metadata":   map[string]any{"name": "p"},
		"containers": containers,
	}}
}

func TestNodeUsageParsesQuantities(t *testing.T) {
	item := unstructured.Unstructured{Object: map[string]any{
		"usage": map[string]any{"cpu": "250m", "memory": "1Gi"},
	}}
	u := NodeUsage(item)
	if u.CPUMillis != 250 || u.MemBytes != 1073741824 {
		t.Fatalf("usage = %+v", u)
	}
}

func TestNodeUsageToleratesMissing(t *testing.T) {
	u := NodeUsage(unstructured.Unstructured{Object: map[string]any{}})
	if u.CPUMillis != 0 || u.MemBytes != 0 {
		t.Fatalf("empty usage = %+v", u)
	}
	bad := unstructured.Unstructured{Object: map[string]any{
		"usage": map[string]any{"cpu": "banana", "memory": "1Gi"},
	}}
	u = NodeUsage(bad)
	if u.CPUMillis != 0 || u.MemBytes != 1073741824 {
		t.Fatalf("garbage cpu should drop to 0, got %+v", u)
	}
}

func TestPodUsageSumsContainers(t *testing.T) {
	item := metricObj(
		map[string]any{"name": "app", "usage": map[string]any{"cpu": "10m", "memory": "64Mi"}},
		map[string]any{"name": "sidecar", "usage": map[string]any{"cpu": "5m", "memory": "32Mi"}},
	)
	u := PodUsage(item)
	if u.CPUMillis != 15 || u.MemBytes != 96*1024*1024 {
		t.Fatalf("sum = %+v", u)
	}
	if u := PodUsage(metricObj()); u.CPUMillis != 0 || u.MemBytes != 0 {
		t.Fatalf("no containers = %+v", u)
	}
}
