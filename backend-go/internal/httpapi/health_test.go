package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"deploy-platform/backend-go/internal/config"
	"deploy-platform/backend-go/internal/service"
)

func healthTestEngine(t *testing.T, check func(context.Context) error) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.Config{Env: "test", MockRuntime: true, TransferDir: t.TempDir()}
	store := service.NewStore()
	runner := &service.MockRunner{}
	cipher, err := service.NewCredentialCipher(testCipherKey())
	if err != nil {
		t.Fatal(err)
	}
	k8s := service.NewK8sService(cfg, runner, cipher)
	worker := service.NewWorker(service.NewPipeline(cfg, store, runner, k8s), 1)
	t.Cleanup(worker.Close)
	return newEngine(cfg, store, worker, k8s, service.NewCDService(cfg, store, worker), cipher, check)
}

func healthRequest(t *testing.T, engine *gin.Engine, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, req)
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s response %q: %v", path, response.Body.String(), err)
	}
	return response, body
}

func TestLivenessDoesNotDependOnMySQLReadiness(t *testing.T) {
	dependencyErr := errors.New("mysql connection details must not be exposed")
	checks := 0
	engine := healthTestEngine(t, func(context.Context) error {
		checks++
		return dependencyErr
	})

	for _, path := range []string{"/health", "/health/live"} {
		response, body := healthRequest(t, engine, path)
		if response.Code != http.StatusOK || body["status"] != "ok" {
			t.Fatalf("%s returned status=%d body=%v, want live 200", path, response.Code, body)
		}
	}
	if checks != 0 {
		t.Fatalf("liveness called the dependency check %d times", checks)
	}

	response, body := healthRequest(t, engine, "/health/ready")
	if response.Code != http.StatusServiceUnavailable || body["status"] != "not_ready" {
		t.Fatalf("readiness returned status=%d body=%v, want not_ready 503", response.Code, body)
	}
	if checks != 1 {
		t.Fatalf("readiness dependency check count = %d, want 1", checks)
	}
	if got := response.Body.String(); strings.Contains(got, dependencyErr.Error()) {
		t.Fatalf("readiness response leaked dependency error: %s", got)
	}
}

func TestReadinessRecoversWhenMySQLReturns(t *testing.T) {
	dependencyErr := errors.New("database unavailable")
	engine := healthTestEngine(t, func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 2*time.Second {
			return errors.New("readiness dependency check is not bounded")
		}
		return dependencyErr
	})

	failed, failedBody := healthRequest(t, engine, "/health/ready")
	if failed.Code != http.StatusServiceUnavailable || failedBody["status"] != "not_ready" {
		t.Fatalf("failed readiness returned status=%d body=%v", failed.Code, failedBody)
	}

	dependencyErr = nil
	ready, readyBody := healthRequest(t, engine, "/health/ready")
	if ready.Code != http.StatusOK || readyBody["status"] != "ready" {
		t.Fatalf("recovered readiness returned status=%d body=%v", ready.Code, readyBody)
	}
}

func TestReadinessWithoutExternalStoreForMockRuntime(t *testing.T) {
	engine := healthTestEngine(t, nil)
	response, body := healthRequest(t, engine, "/health/ready")
	if response.Code != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("in-memory mock readiness returned status=%d body=%v", response.Code, body)
	}
}
