package httpapi

import (
	"encoding/json"
	"testing"

	"deploy-platform/backend-go/internal/service"
)

func TestDeliveryMetricsWindowQueryContract(t *testing.T) {
	_, engine, _ := newTestApp(t)
	for _, tc := range []struct {
		path string
		want int
	}{
		{path: "/api/v1/delivery/metrics", want: service.DefaultDeliveryMetricsWindowDays},
		{path: "/api/v1/delivery/metrics?window_days=7", want: 7},
		{path: "/api/v1/delivery/metrics?window_days=90", want: 90},
		{path: "/api/v1/delivery/metrics?window_days=7&cluster_id=4", want: 7},
	} {
		response := do(t, engine, "GET", tc.path, nil)
		if response.Code != 200 {
			t.Fatalf("GET %s → %d %s", tc.path, response.Code, response.Body.String())
		}
		var result service.DeliveryMetrics
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode metrics for %s: %v", tc.path, err)
		}
		if result.WindowDays != tc.want || result.AsOf.IsZero() || result.WindowStart.IsZero() || result.AsOf.Sub(result.WindowStart).Hours() != float64(tc.want*24) {
			t.Fatalf("window response for %s = %+v", tc.path, result)
		}
		if tc.path == "/api/v1/delivery/metrics?window_days=7&cluster_id=4" && result.ClusterID != 4 {
			t.Fatalf("target filter was not echoed for %s: %+v", tc.path, result)
		}
	}
	for _, path := range []string{
		"/api/v1/delivery/metrics?window_days=abc",
		"/api/v1/delivery/metrics?window_days=0",
		"/api/v1/delivery/metrics?window_days=366",
		"/api/v1/delivery/metrics?cluster_id=abc",
		"/api/v1/delivery/metrics?cluster_id=0",
		"/api/v1/delivery/metrics?cluster_id=-1",
	} {
		if response := do(t, engine, "GET", path, nil); response.Code != 400 {
			t.Fatalf("invalid metrics window accepted %s: %d %s", path, response.Code, response.Body.String())
		}
	}
}
