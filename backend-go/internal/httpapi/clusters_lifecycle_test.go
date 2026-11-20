package httpapi

import (
	"context"
	"strconv"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/config"
	"deploy-platform/backend-go/internal/service"

	"github.com/gin-gonic/gin"
)

// newLifecycleEngine wires an explicit K8sService so tests can observe
// cache/stream invalidation through the public API.
func newLifecycleEngine(t *testing.T, store *service.Store) (*gin.Engine, *service.K8sService) {
	t.Helper()
	cfg := config.Config{Env: "dev", MockRuntime: true, TransferDir: t.TempDir()}
	runner := &service.MockRunner{}
	k8s := service.NewK8sService(cfg, runner, nil)
	worker := service.NewWorker(service.NewPipeline(cfg, store, runner, k8s), 1)
	t.Cleanup(worker.Close)
	seedTestAuth(t, store)
	return newEngine(cfg, store, worker, k8s, service.NewCDService(cfg, store, worker), nil, nil), k8s
}

// TestClusterUpdateInvalidates freezes the step-10 lifecycle contract:
// updating a cluster cancels its tracked streams immediately, so no
// watcher outlives the credential it was opened with.
func TestClusterUpdateInvalidates(t *testing.T) {
	store := service.NewStore()
	e, k8s := newLifecycleEngine(t, store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	k8s.TrackStream(1, cancel)

	w := do(t, e, "PUT", "/api/v1/clusters/1", map[string]any{
		"name": "本地环境-renamed", "env_code": "local", "environment": "local",
		"connection_type": "legacy_ssh", "k8s_master_host": "127.0.0.1",
		"k8s_master_port": 22, "k8s_master_user": "u", "enabled": true,
		"default_namespace": "default",
	})
	if w.Code != 200 {
		t.Fatalf("update cluster → %d %s", w.Code, w.Body.String())
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("cluster update must cancel tracked streams")
	}
}

// TestClusterDeleteInvalidates — delete cancels tracked streams too.
func TestClusterDeleteInvalidates(t *testing.T) {
	store := service.NewStore()
	e, k8s := newLifecycleEngine(t, store)
	// Cluster 2 has no app/release references → deletable.
	saved, err := store.SaveCluster(service.Cluster{
		Name: "temp", EnvCode: "temp", Environment: "test",
		ConnectionType: "legacy_ssh", Enabled: true, DefaultNamespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	k8s.TrackStream(saved.ID, cancel)

	w := do(t, e, "DELETE", "/api/v1/clusters/"+strconv.Itoa(saved.ID), nil)
	if w.Code != 200 {
		t.Fatalf("delete cluster → %d %s", w.Code, w.Body.String())
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("cluster delete must cancel tracked streams")
	}
}

// TestClusterDisableStopsReads — after enabled=false every subsequent
// read is refused, independent of any cache.
func TestClusterDisableStopsReads(t *testing.T) {
	store := service.NewStore()
	e, _ := newLifecycleEngine(t, store)

	read := "/api/v1/k8s/resources?cluster_id=1&version=v1&resource=pods"
	if w := do(t, e, "GET", read, nil); w.Code != 200 {
		t.Fatalf("read before disable → %d", w.Code)
	}
	w := do(t, e, "PUT", "/api/v1/clusters/1", map[string]any{
		"name": "本地环境", "env_code": "local", "environment": "local",
		"connection_type": "legacy_ssh", "k8s_master_host": "127.0.0.1",
		"k8s_master_port": 22, "k8s_master_user": "u", "enabled": false,
		"default_namespace": "default",
	})
	if w.Code != 200 {
		t.Fatalf("disable cluster → %d %s", w.Code, w.Body.String())
	}
	if w := do(t, e, "GET", read, nil); w.Code != 403 {
		t.Fatalf("read after disable → %d, want 403", w.Code)
	}
}
