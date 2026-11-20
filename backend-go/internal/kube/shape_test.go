package kube

import "testing"

// PodSummary is also fed by `kubectl get pods -o json` over SSH, so its
// restart counting must match kubectl's RESTARTS column: the sum of every
// containerStatuses entry plus restartable init containers (native sidecars).
func TestPodSummaryRestartAccounting(t *testing.T) {
	pod := map[string]any{
		"metadata": map[string]any{"name": "p", "labels": map[string]any{"app": "a"}},
		"spec": map[string]any{
			"containers": []any{
				map[string]any{"name": "main", "image": "img:v1"},
				map[string]any{"name": "side", "image": "img:v2"},
			},
			"initContainers": []any{
				map[string]any{"name": "init-setup"},
				map[string]any{"name": "init-sidecar", "restartPolicy": "Always"},
			},
		},
		"status": map[string]any{
			"phase": "Running",
			"containerStatuses": []any{
				map[string]any{"name": "main", "ready": true, "restartCount": 4},
				map[string]any{"name": "side", "ready": false, "restartCount": 7},
			},
			"initContainerStatuses": []any{
				map[string]any{"name": "init-setup", "ready": true, "restartCount": 9},
				map[string]any{"name": "init-sidecar", "ready": true, "restartCount": 2},
			},
		},
	}
	s := PodSummary(pod)
	// 4 + 7 regular restarts + 2 restartable-init restarts; plain init not counted
	if s["restarts"] != 13 {
		t.Fatalf("restarts = %v, want 13", s["restarts"])
	}
	if s["ready"] != "1/2" {
		t.Fatalf("ready = %v, want 1/2", s["ready"])
	}
}

func TestPodSummaryEmptyPod(t *testing.T) {
	s := PodSummary(map[string]any{})
	if s["name"] != "" || s["status"] != "Unknown" || s["ready"] != "0/0" || s["restarts"] != 0 {
		t.Fatalf("empty pod summary = %+v", s)
	}
}
