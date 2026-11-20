package kube

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// metrics.k8s.io is served by metrics-server as an aggregated API. These
// GVRs are fixed server-side — like the resource registry, callers can
// only pick between the two constants, never an arbitrary path.
var (
	MetricsNodesGVR = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "nodes"}
	MetricsPodsGVR  = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
)

// MetricsResource maps the narrow "nodes"|"pods" selector to its GVR.
// Anything else is rejected before it can reach the API server.
func MetricsResource(resource string) (schema.GroupVersionResource, error) {
	switch resource {
	case "nodes":
		return MetricsNodesGVR, nil
	case "pods":
		return MetricsPodsGVR, nil
	default:
		return schema.GroupVersionResource{}, fmt.Errorf("unsupported metrics resource %q (want nodes|pods)", resource)
	}
}

// Usage is a CPU/memory sample from metrics.k8s.io. CPU is milliCPU
// (1000m = 1 core), Memory is bytes — quantities are pre-parsed so
// consumers never juggle suffixes.
type Usage struct {
	CPUMillis int64 `json:"cpu_millis"`
	MemBytes  int64 `json:"mem_bytes"`
}

// NodeUsage parses one metrics.k8s.io/v1beta1 NodeMetrics item.
// Missing/unparseable quantities degrade to zero rather than failing.
func NodeUsage(item unstructured.Unstructured) Usage {
	return usageOf(item.Object)
}

// PodUsage sums container usages of one PodMetrics item — the same
// aggregation kubectl top pod displays.
func PodUsage(item unstructured.Unstructured) Usage {
	total := Usage{}
	containers, _, _ := unstructured.NestedSlice(item.Object, "containers")
	for _, c := range containers {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		u := usageOf(m)
		total.CPUMillis += u.CPUMillis
		total.MemBytes += u.MemBytes
	}
	return total
}

// usageOf reads usage.cpu / usage.memory from a metrics object or a
// container entry, tolerating absent fields.
func usageOf(obj map[string]any) Usage {
	u := Usage{}
	if s, ok, _ := unstructured.NestedString(obj, "usage", "cpu"); ok {
		if q, err := resource.ParseQuantity(s); err == nil {
			u.CPUMillis = q.MilliValue()
		}
	}
	if s, ok, _ := unstructured.NestedString(obj, "usage", "memory"); ok {
		if q, err := resource.ParseQuantity(s); err == nil {
			u.MemBytes = q.Value()
		}
	}
	return u
}
