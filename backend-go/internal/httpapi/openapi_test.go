package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/service"

	"github.com/gin-gonic/gin"
)

// doKey issues a JSON request against the /openapi surface carrying an API
// key in the Authorization bearer (or X-Api-Key when xHeader is true).
func doKey(t *testing.T, e *gin.Engine, method, path string, body any, apiKey string, xHeader ...bool) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		if len(xHeader) > 0 && xHeader[0] {
			req.Header.Set("X-Api-Key", apiKey)
		} else {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w
}

// createKey exercises the admin CRUD path and returns the plaintext token.
func createKey(t *testing.T, e *gin.Engine, name, role string) string {
	t.Helper()
	w := do(t, e, "POST", "/api/v1/api-keys", map[string]any{"name": name, "role": role})
	if w.Code != 201 {
		t.Fatalf("create key %s → %d %s", name, w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	key, _ := out["key"].(string)
	if key == "" {
		t.Fatalf("create response missing plaintext key: %v", out)
	}
	return key
}

func TestAPIKeyAdminCRUD(t *testing.T) {
	app, e, _ := newTestApp(t)

	key := createKey(t, e, "ci-bot", "operator")

	// 列表不回显散列/明文
	w := do(t, e, "GET", "/api/v1/api-keys", nil)
	if w.Code != 200 {
		t.Fatalf("list → %d", w.Code)
	}
	var keys []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0]["name"] != "ci-bot" || keys[0]["prefix"] == "" {
		t.Fatalf("keys=%v", keys)
	}
	if _, has := keys[0]["key_hash"]; has {
		t.Fatal("list leaks key_hash")
	}
	if _, has := keys[0]["key"]; has {
		t.Fatal("list leaks plaintext key")
	}
	keyID := keys[0]["id"].(float64)

	// 重名 → 409
	if w := do(t, e, "POST", "/api/v1/api-keys", map[string]any{"name": "ci-bot", "role": "viewer"}); w.Code != 409 {
		t.Fatalf("dup name → %d", w.Code)
	}
	// admin 角色不允许
	if w := do(t, e, "POST", "/api/v1/api-keys", map[string]any{"name": "x2", "role": "admin"}); w.Code != 400 {
		t.Fatalf("admin role → %d", w.Code)
	}

	// 禁用后 openapi 拒绝；重新启用恢复
	w = do(t, e, "PUT", fmt.Sprintf("/api/v1/api-keys/%d", int64(keyID)), map[string]any{"enabled": false})
	if w.Code != 200 {
		t.Fatalf("disable → %d", w.Code)
	}
	if w := doKey(t, e, "GET", "/openapi/v1/releases", nil, key); w.Code != 401 {
		t.Fatalf("disabled key → %d", w.Code)
	}
	w = do(t, e, "PUT", fmt.Sprintf("/api/v1/api-keys/%d", int64(keyID)), map[string]any{"enabled": true})
	if w.Code != 200 {
		t.Fatalf("enable → %d", w.Code)
	}
	if w := doKey(t, e, "GET", "/openapi/v1/releases", nil, key); w.Code != 200 {
		t.Fatalf("re-enabled key → %d", w.Code)
	}

	// 删除后 openapi 拒绝
	w = do(t, e, "DELETE", fmt.Sprintf("/api/v1/api-keys/%d", int64(keyID)), nil)
	if w.Code != 200 {
		t.Fatalf("delete → %d", w.Code)
	}
	if w := doKey(t, e, "GET", "/openapi/v1/releases", nil, key); w.Code != 401 {
		t.Fatalf("deleted key → %d", w.Code)
	}

	// viewer 无权管理密钥
	viewer := seedUser(t, app.store, "v1", service.RoleViewer)
	if w := do(t, e, "GET", "/api/v1/api-keys", nil, viewer); w.Code != 403 {
		t.Fatalf("viewer list keys → %d", w.Code)
	}
}

func TestOpenAPIKeyAuthAndRoles(t *testing.T) {
	cfg := cdConfig(t)
	store := cdStore()
	e := newCDEngine(t, cfg, store)

	// operator 密钥：Bearer 与 X-Api-Key 双通道
	opKey := createKey(t, e, "ci-op", "operator")
	// viewer 密钥
	viewKey := createKey(t, e, "ci-view", "viewer")

	// 无密钥 / 坏密钥 → 401
	if w := doKey(t, e, "GET", "/openapi/v1/releases", nil, ""); w.Code != 401 {
		t.Fatalf("no key → %d", w.Code)
	}
	if w := doKey(t, e, "GET", "/openapi/v1/releases", nil, "dp_wrong"); w.Code != 401 {
		t.Fatalf("bad key → %d", w.Code)
	}
	// Bearer + X-Api-Key 都可用
	if w := doKey(t, e, "GET", "/openapi/v1/releases", nil, opKey); w.Code != 200 {
		t.Fatalf("bearer → %d %s", w.Code, w.Body.String())
	}
	if w := doKey(t, e, "GET", "/openapi/v1/releases", nil, opKey, true); w.Code != 200 {
		t.Fatalf("x-api-key → %d", w.Code)
	}

	// 触发一条 release 供读取/晋级
	w := do(t, e, "POST", "/api/v1/webhooks/yunxiao", map[string]any{
		"event_id": "op-1", "app_name": "app", "target_cluster": "test",
		"image_full": "reg.io/Demo/app:v9",
	})
	if w.Code != 202 {
		t.Fatalf("webhook → %d %s", w.Code, w.Body.String())
	}
	var created map[string]any
	json.Unmarshal(w.Body.Bytes(), &created)
	releaseID := int64(created["release_id"].(float64))
	attestHTTPRelease(t, store, releaseID)
	testLogID := int64(created["test_log_id"].(float64))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if l, _ := store.FindLog(testLogID); l.Status == "success" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// viewer key 读 release + deploy log + promote 目标
	if w := doKey(t, e, "GET", fmt.Sprintf("/openapi/v1/releases/%d", releaseID), nil, viewKey); w.Code != 200 {
		t.Fatalf("viewer get release → %d", w.Code)
	}
	if w := doKey(t, e, "GET", fmt.Sprintf("/openapi/v1/deploy-logs/%d", testLogID), nil, viewKey); w.Code != 200 {
		t.Fatalf("viewer get log → %d", w.Code)
	}
	w = doKey(t, e, "GET", fmt.Sprintf("/openapi/v1/deploy-logs/%d/actions", testLogID), nil, viewKey)
	if w.Code != 200 {
		t.Fatalf("viewer get deploy actions → %d %s", w.Code, w.Body.String())
	}
	var actions []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &actions); err != nil || len(actions) != 1 || actions[0]["status"] != "applied" {
		t.Fatalf("viewer deploy actions=%v err=%v", actions, err)
	}
	if w := doKey(t, e, "GET", fmt.Sprintf("/openapi/v1/releases/%d/promotion-targets", releaseID), nil, viewKey); w.Code != 200 {
		t.Fatalf("viewer targets → %d", w.Code)
	}
	// viewer key 写操作 → 403（角色门禁），且留下 apikey: 前缀的审计
	if w := doKey(t, e, "POST", fmt.Sprintf("/openapi/v1/releases/%d/promote", releaseID), map[string]any{"cluster_id": 2}, viewKey); w.Code != 403 {
		t.Fatalf("viewer promote → %d", w.Code)
	}
	if w := doKey(t, e, "POST", "/openapi/v1/rollback", map[string]any{
		"cluster_id": 1, "app_name": "app", "deployment_name": "app",
		"container_name": "app", "namespace": "ns",
		"image_full": "reg.io/Demo/app:v8", "version": "v8",
	}, viewKey); w.Code != 403 {
		t.Fatalf("viewer rollback → %d", w.Code)
	}

	// operator key 晋级生产 → 202 + 审计记 apikey:ci-op
	w = doKey(t, e, "POST", fmt.Sprintf("/openapi/v1/releases/%d/promote", releaseID), map[string]any{"cluster_id": 2}, opKey)
	if w.Code != 202 {
		t.Fatalf("operator promote → %d %s", w.Code, w.Body.String())
	}
	found := false
	for _, a := range store.Audits(50) {
		if a.Action == "promote" && a.Username == "apikey:ci-op" {
			found = true
		}
	}
	if !found {
		t.Fatal("promote audit missing apikey:ci-op operator")
	}

	// Handler-level errors on /openapi/v1 follow the documented
	// {"error": …} envelope — never the session API's {"detail": …}.
	// Covered paths: handler 404 (writeCDErr→fail), validation 400
	// (requireStrings→fail), and role-gate 403 (writes error directly).
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   any
		key    string
		want   int
	}{
		{"missing release", "GET", "/openapi/v1/releases/999999", nil, opKey, 404},
		{"bad path id", "GET", "/openapi/v1/releases/abc", nil, opKey, 400},
		{"rollback missing fields", "POST", "/openapi/v1/rollback", map[string]any{"cluster_id": 1}, opKey, 400},
		{"viewer write", "POST", "/openapi/v1/rollback", map[string]any{
			"cluster_id": 1, "app_name": "a", "deployment_name": "d",
			"container_name": "c", "namespace": "n",
			"image_full": "i", "version": "v",
		}, viewKey, 403},
	} {
		w := doKey(t, e, tc.method, tc.path, tc.body, tc.key)
		if w.Code != tc.want {
			t.Fatalf("%s → %d, want %d (%s)", tc.name, w.Code, tc.want, w.Body.String())
		}
		var env map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s: error body not JSON: %s", tc.name, w.Body.String())
		}
		if _, ok := env["error"]; !ok {
			t.Fatalf("%s: openapi error must carry \"error\": %v", tc.name, env)
		}
		if _, ok := env["detail"]; ok {
			t.Fatalf("%s: openapi error must not carry \"detail\": %v", tc.name, env)
		}
	}

	// The session API keeps its own {"detail": …} envelope — the two
	// contracts stay distinct on purpose.
	w = do(t, e, "GET", "/api/v1/deploy/logs/999999", nil)
	if w.Code != 404 {
		t.Fatalf("session 404 → %d %s", w.Code, w.Body.String())
	}
	var env map[string]any
	json.Unmarshal(w.Body.Bytes(), &env)
	if _, ok := env["detail"]; !ok {
		t.Fatalf("session API error must carry \"detail\": %v", env)
	}
}
