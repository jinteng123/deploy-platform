package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/config"
	"deploy-platform/backend-go/internal/service"

	"github.com/gin-gonic/gin"
)

// testSessionToken is the deterministic session every test request
// carries — seeded per store by seedTestAuth so do() stays one line.
const testSessionToken = "test-session-token"

// seedTestAuth creates the admin user (when absent) plus a fixed-token
// session so engine-level tests authenticate like a real login.
func seedTestAuth(t *testing.T, store *service.Store) {
	t.Helper()
	u, err := store.FindUserByUsername("admin")
	if err != nil {
		hash, herr := service.HashPassword("test-admin-pass")
		if herr != nil {
			t.Fatal(herr)
		}
		u, err = store.SaveUser(service.User{
			Username: "admin", Role: service.RoleAdmin, PasswordHash: hash, Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	store.SeedSession(testSessionToken, u.ID, time.Hour)
}

// seedUser adds a role user with its own session token; returns the
// token for do()'s optional override.
func seedUser(t *testing.T, store *service.Store, username, role string) string {
	t.Helper()
	hash, err := service.HashPassword("test-user-pass")
	if err != nil {
		t.Fatal(err)
	}
	u, err := store.SaveUser(service.User{
		Username: username, Role: role, PasswordHash: hash, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	token := "token-" + username
	store.SeedSession(token, u.ID, time.Hour)
	return token
}

func newTestApp(t *testing.T) (*Application, *gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	cfg := config.Config{
		Env:         "dev",
		MockRuntime: true,
		TransferDir: dir,
		// DatabaseDSN 为空 → repository.Open 失败 → mock 模式降级为内存 store
	}
	app, err := NewApplication(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	seedTestAuth(t, app.store)
	return app, app.Engine, dir
}

// do issues an authenticated JSON request — every test carries the seeded
// admin session unless a token override is passed (do(t,e,m,p,b,"token-x")).
// Passing token "" exercises the unauthenticated path.
func do(t *testing.T, e *gin.Engine, method, path string, body any, token ...string) *httptest.ResponseRecorder {
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
	tok := testSessionToken
	if len(token) > 0 {
		tok = token[0]
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w
}

func TestDeployValidation(t *testing.T) {
	_, e, _ := newTestApp(t)

	if w := do(t, e, "POST", "/api/v1/deploy", map[string]any{}); w.Code != 400 {
		t.Fatalf("empty body → %d", w.Code)
	}
	if w := do(t, e, "POST", "/api/v1/deploy", map[string]any{
		"cluster_id": 1, "file_name": "x.tar.gz", "deployment_name": "d",
		"container_name": "c", "version": "   ",
	}); w.Code != 400 {
		t.Fatalf("blank version → %d", w.Code)
	}
	if w := do(t, e, "POST", "/api/v1/deploy", map[string]any{
		"cluster_id": 999, "file_name": "x.tar.gz", "deployment_name": "d",
		"container_name": "c", "version": "v1",
	}); w.Code != 404 {
		t.Fatalf("unknown cluster → %d", w.Code)
	}
	if w := do(t, e, "POST", "/api/v1/deploy", map[string]any{
		"cluster_id": 1, "file_name": "../escape.tar.gz", "deployment_name": "d",
		"container_name": "c", "version": "v1",
	}); w.Code != 400 {
		t.Fatalf("path escape → %d", w.Code)
	}
}

func TestDeployMockEndToEnd(t *testing.T) {
	_, e, dir := newTestApp(t)
	if err := os.WriteFile(filepath.Join(dir, "app-v1.0.tar.gz"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := do(t, e, "POST", "/api/v1/deploy", map[string]any{
		"cluster_id": 1, "file_name": "app-v1.0.tar.gz", "deployment_name": "myapp",
		"container_name": "myapp", "version": "v1.0",
	})
	if w.Code != 202 {
		t.Fatalf("deploy → %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		LogID  int64  `json:"log_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.LogID == 0 || resp.Status != "pending" {
		t.Fatalf("response=%+v", resp)
	}

	deadline := time.Now().Add(5 * time.Second)
	var detail string
	for {
		w = do(t, e, "GET", "/api/v1/deploy/logs?limit=1", nil)
		if w.Code != 200 {
			t.Fatalf("logs → %d", w.Code)
		}
		var logs []struct {
			ID         int64  `json:"id"`
			Status     string `json:"status"`
			StepDetail string `json:"step_detail"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &logs); err != nil {
			t.Fatal(err)
		}
		if len(logs) == 1 && logs[0].ID == resp.LogID && logs[0].Status == "success" {
			detail = logs[0].StepDetail
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("log did not reach success: %s", w.Body.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	var steps []struct {
		Step    string `json:"step"`
		Success bool   `json:"success"`
	}
	if err := json.Unmarshal([]byte(detail), &steps); err != nil || len(steps) != 6 {
		t.Fatalf("step_detail not retrievable: %q err=%v", detail, err)
	}
	want := []string{"check_file", "docker_load", "docker_tag", "docker_push", "k8s_set_image", "rollout_wait"}
	for i, s := range steps {
		if s.Step != want[i] || !s.Success {
			t.Fatalf("step %d = %+v", i, s)
		}
	}
}

func TestClusterCRUD(t *testing.T) {
	_, e, _ := newTestApp(t)

	body := map[string]any{"name": "测试", "env_code": "test", "environment": "staging",
		"connection_type": "legacy_ssh", "k8s_master_host": "10.0.0.1"}
	if w := do(t, e, "POST", "/api/v1/clusters", body); w.Code != 200 {
		t.Fatalf("create → %d %s", w.Code, w.Body.String())
	}
	if w := do(t, e, "POST", "/api/v1/clusters", body); w.Code != 409 {
		t.Fatalf("duplicate env_code → %d", w.Code)
	}
	badPort := map[string]any{"name": "x", "env_code": "x", "environment": "e",
		"connection_type": "legacy_ssh", "k8s_master_host": "h", "k8s_master_port": 70000}
	if w := do(t, e, "POST", "/api/v1/clusters", badPort); w.Code != 400 {
		t.Fatalf("bad port → %d", w.Code)
	}
	if w := do(t, e, "PUT", "/api/v1/clusters/abc", body); w.Code != 400 {
		t.Fatalf("bad id → %d", w.Code)
	}
	if w := do(t, e, "PUT", "/api/v1/clusters/999", body); w.Code != 404 {
		t.Fatalf("missing cluster → %d", w.Code)
	}
	if w := do(t, e, "DELETE", "/api/v1/clusters/2", nil); w.Code != 200 {
		t.Fatalf("delete → %d", w.Code)
	}
	if w := do(t, e, "DELETE", "/api/v1/clusters/2", nil); w.Code != 404 {
		t.Fatalf("re-delete → %d", w.Code)
	}
}

const testKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: https://127.0.0.1:6443
contexts:
- name: c
  context:
    cluster: c
    user: u
current-context: c
users:
- name: u
  user:
    token: t
`

func newCipherEngine(t *testing.T, key string, store *service.Store) *gin.Engine {
	t.Helper()
	cipher, err := service.NewCredentialCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Env: "dev", MockRuntime: true, TransferDir: t.TempDir()}
	runner := &service.MockRunner{}
	k8s := service.NewK8sService(cfg, runner, cipher)
	worker := service.NewWorker(service.NewPipeline(cfg, store, runner, k8s), 1)
	t.Cleanup(worker.Close)
	seedTestAuth(t, store)
	return newEngine(cfg, store, worker, k8s, service.NewCDService(cfg, store, worker), cipher, nil)
}

func testCipherKey() string {
	return base64.StdEncoding.EncodeToString(make([]byte, 32))
}

func TestClusterConnectionAPI(t *testing.T) {
	store := service.NewStore()
	e := newCipherEngine(t, testCipherKey(), store)

	// kubeconfig 创建成功；响应只有安全字段
	body := map[string]any{
		"name": "直连测试", "env_code": "direct-test", "environment": "staging",
		"connection_type": "kubeconfig", "credential": testKubeconfig,
	}
	w := do(t, e, "POST", "/api/v1/clusters", body)
	if w.Code != 200 {
		t.Fatalf("create kubeconfig cluster → %d %s", w.Code, w.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created["credential_configured"] != true {
		t.Fatalf("credential_configured missing: %v", created)
	}
	for _, leaked := range []string{"credential", "credential_ciphertext", "k8s_master_key_path"} {
		if _, has := created[leaked]; has {
			t.Fatalf("response leaks %s", leaked)
		}
	}
	if strings.Contains(w.Body.String(), "secret-token") || strings.Contains(w.Body.String(), "v1:") {
		t.Fatal("response leaks credential material")
	}
	id := int(created["id"].(float64))

	// 列表同样不泄漏
	w = do(t, e, "GET", "/api/v1/clusters", nil)
	if strings.Contains(w.Body.String(), "credential_ciphertext") {
		t.Fatal("list leaks credential_ciphertext")
	}

	// update 不传 credential → 保留旧凭据
	w = do(t, e, "PUT", fmt.Sprintf("/api/v1/clusters/%d", id), map[string]any{
		"name": "直连测试", "env_code": "direct-test", "environment": "staging-2",
		"connection_type": "kubeconfig",
	})
	if w.Code != 200 {
		t.Fatalf("update without credential → %d %s", w.Code, w.Body.String())
	}
	var updated map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated["credential_configured"] != true || updated["environment"] != "staging-2" {
		t.Fatalf("credential lost on update: %v", updated)
	}
	stored, _ := store.FindCluster(id)
	if stored.CredentialCiphertext == nil || !strings.HasPrefix(*stored.CredentialCiphertext, "v1:") {
		t.Fatal("stored ciphertext missing or wrong format")
	}

	// production + automatic 拒绝
	w = do(t, e, "POST", "/api/v1/clusters", map[string]any{
		"name": "bad", "env_code": "bad-prod", "environment": "prod",
		"environment_tier": "production", "deploy_policy": "automatic",
		"connection_type": "kubeconfig", "credential": testKubeconfig,
	})
	if w.Code != 400 {
		t.Fatalf("production+automatic → %d", w.Code)
	}

	// token 模式：URL 校验 + credential 必填
	for _, bad := range []map[string]any{
		{"name": "t", "env_code": "t1", "environment": "e", "connection_type": "token", "credential": "tok"},                                           // 无 api_server
		{"name": "t", "env_code": "t2", "environment": "e", "connection_type": "token", "api_server": "ftp://x", "credential": "tok"},                  // 非法 scheme
		{"name": "t", "env_code": "t3", "environment": "e", "connection_type": "token", "api_server": "https://ok"},                                    // 无 credential
		{"name": "t", "env_code": "t4", "environment": "e", "connection_type": "bogus", "credential": "x"},                                             // 未知类型
		{"name": "t", "env_code": "t5", "connection_type": "kubeconfig", "credential": testKubeconfig},                                                 // 无 environment
		{"name": "t", "env_code": "t6", "environment": "e", "environment_tier": "gold", "connection_type": "kubeconfig", "credential": testKubeconfig}, // 非法 tier
	} {
		if w := do(t, e, "POST", "/api/v1/clusters", bad); w.Code != 400 {
			t.Fatalf("body %v → %d", bad, w.Code)
		}
	}
	// token 创建成功
	w = do(t, e, "POST", "/api/v1/clusters", map[string]any{
		"name": "token集群", "env_code": "tok-env", "environment": "staging",
		"connection_type": "token", "api_server": "https://k8s.example.com:6443",
		"credential": "tok", "insecure_skip_tls_verify": true,
	})
	if w.Code != 200 {
		t.Fatalf("token cluster → %d %s", w.Code, w.Body.String())
	}

	// 非法 kubeconfig 语法拒绝
	w = do(t, e, "POST", "/api/v1/clusters", map[string]any{
		"name": "bad", "env_code": "bad-kc", "environment": "e",
		"connection_type": "kubeconfig", "credential": "not-a-kubeconfig",
	})
	if w.Code != 400 {
		t.Fatalf("invalid kubeconfig → %d", w.Code)
	}

	// mock 模式下 test-connection 返回 mock
	w = do(t, e, "POST", "/api/v1/clusters/test-connection", map[string]any{
		"name": "t", "env_code": "tc", "environment": "e",
		"connection_type": "kubeconfig", "credential": testKubeconfig,
	})
	if w.Code != 200 {
		t.Fatalf("test-connection → %d %s", w.Code, w.Body.String())
	}
	var tc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &tc); err != nil {
		t.Fatal(err)
	}
	if tc["ok"] != true || tc["version"] != "mock" {
		t.Fatalf("test-connection response %v", tc)
	}
	// test-connection 不落库
	if _, err := store.FindClusterByEnvCode("tc"); err == nil {
		t.Fatal("test-connection must not persist")
	}
}

const testKubeconfigWithCA = `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: https://127.0.0.1:6443
    certificate-authority-data: LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCg==
contexts:
- name: c
  context:
    cluster: c
    user: u
current-context: c
users:
- name: u
  user:
    token: t
`

func kubeconfigWith(userBlock, clusterBlock string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: https://127.0.0.1:6443
%s
contexts:
- name: c
  context:
    cluster: c
    user: u
current-context: c
users:
- name: u
  user:
%s
`, clusterBlock, userBlock)
}

func TestKubeconfigCredentialSecurity(t *testing.T) {
	// 内嵌 token + certificate-authority-data 允许
	if err := validateKubeconfigCredential(testKubeconfigWithCA); err != nil {
		t.Fatalf("embedded-data kubeconfig rejected: %v", err)
	}
	if err := validateKubeconfigCredential(testKubeconfig); err != nil {
		t.Fatalf("minimal kubeconfig rejected: %v", err)
	}

	rejects := map[string]string{
		"exec":          "    exec:\n      command: /bin/evil\n      apiVersion: client.authentication.k8s.io/v1\n",
		"auth-provider": "    auth-provider:\n      name: gcp\n",
		"client-cert":   "    client-certificate: /root/.kube/admin.crt\n    client-key-data: ZGF0YQ==\n",
		"client-key":    "    client-key: /root/.kube/admin.key\n",
		"tokenFile":     "    tokenFile: /var/run/secrets/token\n",
	}
	for name, user := range rejects {
		if err := validateKubeconfigCredential(kubeconfigWith(user, "")); err == nil {
			t.Fatalf("%s kubeconfig accepted", name)
		}
	}
	// certificate-authority 路径拒绝（必须 data）
	caPath := kubeconfigWith("    token: t\n", "    certificate-authority: /etc/k8s/ca.crt\n")
	if err := validateKubeconfigCredential(caPath); err == nil {
		t.Fatal("certificate-authority path accepted")
	}
	// 语法损坏拒绝
	if err := validateKubeconfigCredential("::: not yaml"); err == nil {
		t.Fatal("broken kubeconfig accepted")
	}
}

func TestWebhookRejectsOversizedBody(t *testing.T) {
	e := newCDEngine(t, cdConfig(t), cdStore())
	big := `{"event_id":"` + strings.Repeat("x", (1<<20)+100) + `","target_cluster":"test","app_name":"app","image_full":"r/a:v1"}`
	w := do(t, e, "POST", "/api/v1/webhooks/yunxiao", json.RawMessage(big))
	if w.Code != 400 {
		t.Fatalf("oversized webhook → %d", w.Code)
	}
}

func TestResolveCipherStartupGate(t *testing.T) {
	direct := service.NewStoreWithData(
		[]service.Cluster{{ID: 1, Enabled: true, ConnectionType: "kubeconfig"}}, nil, nil, nil, nil)
	// enabled direct cluster + no key → startup must fail
	if _, err := resolveCipher(config.Config{}, direct); err == nil {
		t.Fatal("missing key with enabled direct cluster must fail")
	}
	// enabled direct cluster + invalid key → startup must fail
	if _, err := resolveCipher(config.Config{CredentialEncryptionKey: "AAAA"}, direct); err == nil {
		t.Fatal("invalid key with enabled direct cluster must fail")
	}
	// valid key → cipher
	if _, err := resolveCipher(config.Config{CredentialEncryptionKey: testCipherKey()}, direct); err != nil {
		t.Fatalf("valid key err=%v", err)
	}
	// only legacy/disabled clusters → no key needed
	legacy := service.NewStoreWithData(
		[]service.Cluster{
			{ID: 1, Enabled: true, ConnectionType: "legacy_ssh"},
			{ID: 2, Enabled: false, ConnectionType: "token"},
		}, nil, nil, nil, nil)
	if _, err := resolveCipher(config.Config{}, legacy); err != nil {
		t.Fatalf("legacy-only store must not need a key: %v", err)
	}
}

func TestResolveCipherRejectsWrongStoredCredentialKey(t *testing.T) {
	key := testCipherKey()
	cipher, err := service.NewCredentialCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := cipher.Encrypt("private kubeconfig content")
	if err != nil {
		t.Fatal(err)
	}
	cluster := service.Cluster{ID: 1, Name: "local", Enabled: true, ConnectionType: "kubeconfig", CredentialCiphertext: &sealed}
	store := service.NewStoreWithData([]service.Cluster{cluster}, nil, nil, nil, nil)
	if _, err := resolveCipher(config.Config{CredentialEncryptionKey: key}, store); err != nil {
		t.Fatal(err)
	}
	wrongKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{99}, 32))
	if _, err := resolveCipher(config.Config{CredentialEncryptionKey: wrongKey}, store); err == nil || !strings.Contains(err.Error(), "凭据解密失败") || strings.Contains(err.Error(), sealed) || strings.Contains(err.Error(), wrongKey) || strings.Contains(err.Error(), "private kubeconfig content") {
		t.Fatalf("wrong key must fail safely: %v", err)
	}
	cluster.Enabled = false
	disabled := service.NewStoreWithData([]service.Cluster{cluster}, nil, nil, nil, nil)
	if _, err := resolveCipher(config.Config{CredentialEncryptionKey: wrongKey}, disabled); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveCipher(config.Config{MockRuntime: true, CredentialEncryptionKey: wrongKey}, store); err != nil {
		t.Fatal(err)
	}
}

func TestClusterNoKeyRejectsDirectSave(t *testing.T) {
	// cipher=nil 的引擎：保存直连凭据必须失败
	_, e, _ := newTestApp(t)
	w := do(t, e, "POST", "/api/v1/clusters", map[string]any{
		"name": "x", "env_code": "no-key", "environment": "e",
		"connection_type": "kubeconfig", "credential": testKubeconfig,
	})
	if w.Code != 400 {
		t.Fatalf("save without key → %d", w.Code)
	}
	// enabled 直连集群即使只保留旧凭据（不传 credential）也必须有 key
	store := service.NewStore()
	sealed := "v1:cGxhaWNlaG9sZGVy"
	saved, err := store.SaveCluster(service.Cluster{
		Name: "direct", EnvCode: "direct1", Environment: "e",
		EnvironmentTier: "non_production", DeployPolicy: "manual",
		ConnectionType: "kubeconfig", Enabled: true,
		CredentialCiphertext: &sealed,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg2 := config.Config{Env: "dev", MockRuntime: true, TransferDir: t.TempDir()}
	runner2 := &service.MockRunner{}
	k8s2 := service.NewK8sService(cfg2, runner2, nil)
	worker2 := service.NewWorker(service.NewPipeline(cfg2, store, runner2, k8s2), 1)
	t.Cleanup(worker2.Close)
	seedTestAuth(t, store)
	e2 := newEngine(cfg2, store, worker2, k8s2, service.NewCDService(cfg2, store, worker2), nil, nil)
	w = do(t, e2, "PUT", fmt.Sprintf("/api/v1/clusters/%d", saved.ID), map[string]any{
		"name": "direct", "env_code": "direct1", "environment": "e",
		"connection_type": "kubeconfig", "enabled": true,
	})
	if w.Code != 400 {
		t.Fatalf("update enabled direct cluster without key → %d", w.Code)
	}
}

func TestClusterFieldBoundaries(t *testing.T) {
	store := service.NewStore()
	e := newCipherEngine(t, testCipherKey(), store)
	base := func() map[string]any {
		return map[string]any{
			"name": "ok", "env_code": "ok-1", "environment": "staging",
			"connection_type": "kubeconfig", "credential": testKubeconfig,
		}
	}
	long := func(n int) string { return strings.Repeat("a", n) }

	b := base()
	b["name"] = long(65)
	if w := do(t, e, "POST", "/api/v1/clusters", b); w.Code != 400 {
		t.Fatalf("name>64 → %d", w.Code)
	}
	// VARCHAR 按字符计：64 个中文字符允许，65 拒绝
	b = base()
	b["env_code"] = "cn-64"
	b["name"] = strings.Repeat("中", 64)
	if w := do(t, e, "POST", "/api/v1/clusters", b); w.Code != 200 {
		t.Fatalf("64-char Chinese name → %d %s", w.Code, w.Body.String())
	}
	b = base()
	b["env_code"] = "cn-65"
	b["name"] = strings.Repeat("中", 65)
	if w := do(t, e, "POST", "/api/v1/clusters", b); w.Code != 400 {
		t.Fatalf("65-char Chinese name → %d", w.Code)
	}
	for _, badCode := range []string{long(17), "_lead", "has space", "中文code", "a!b"} {
		b = base()
		b["env_code"] = badCode
		if w := do(t, e, "POST", "/api/v1/clusters", b); w.Code != 400 {
			t.Fatalf("env_code %q → %d", badCode, w.Code)
		}
	}
	for _, okCode := range []string{"a", "test", "prod-east.1", "a_b-c.d"} {
		b = base()
		b["env_code"] = okCode
		b["name"] = "ok-" + okCode
		if w := do(t, e, "POST", "/api/v1/clusters", b); w.Code != 200 {
			t.Fatalf("env_code %q → %d %s", okCode, w.Code, w.Body.String())
		}
	}
	b = base()
	b["environment"] = long(65)
	if w := do(t, e, "POST", "/api/v1/clusters", b); w.Code != 400 {
		t.Fatalf("environment>64 → %d", w.Code)
	}
	b = base()
	b["credential"] = long(1<<20 + 1)
	if w := do(t, e, "POST", "/api/v1/clusters", b); w.Code != 400 {
		t.Fatalf("credential>1MiB → %d", w.Code)
	}
	// api_server 安全：拒绝 userinfo/query/fragment，允许 path
	for _, u := range []string{"https://user@h:6443", "https://h?x=1", "https://h#f", "ftp://h"} {
		b = base()
		b["env_code"] = "url-" + strings.Repeat("x", 4)
		b["connection_type"] = "token"
		b["api_server"] = u
		if w := do(t, e, "POST", "/api/v1/clusters", b); w.Code != 400 {
			t.Fatalf("api_server %q → %d", u, w.Code)
		}
	}
	b = base()
	b["env_code"] = "path-ok"
	b["connection_type"] = "token"
	b["api_server"] = "https://h:6443/proxy/prefix"
	if w := do(t, e, "POST", "/api/v1/clusters", b); w.Code != 200 {
		t.Fatalf("api_server with path → %d %s", w.Code, w.Body.String())
	}
}

func TestClusterUpdatePreservesAndSwitchRules(t *testing.T) {
	store := service.NewStore()
	e := newCipherEngine(t, testCipherKey(), store)

	// 创建一个 disabled 的 token 集群
	w := do(t, e, "POST", "/api/v1/clusters", map[string]any{
		"name": "tok", "env_code": "tok-1", "environment": "staging",
		"environment_tier": "production", "deploy_policy": "manual",
		"connection_type": "token", "api_server": "https://k8s:6443",
		"credential": "tok", "insecure_skip_tls_verify": true, "enabled": false,
	})
	if w.Code != 200 {
		t.Fatalf("create → %d %s", w.Code, w.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := int(created["id"].(float64))

	// update 不传 enabled/insecure/tier/policy/api_server → 全部保留
	w = do(t, e, "PUT", fmt.Sprintf("/api/v1/clusters/%d", id), map[string]any{
		"name": "tok", "env_code": "tok-1", "environment": "staging",
	})
	if w.Code != 200 {
		t.Fatalf("update → %d %s", w.Code, w.Body.String())
	}
	got, _ := store.FindCluster(id)
	if got.Enabled {
		t.Fatal("disabled cluster re-enabled by update omitting enabled")
	}
	if !got.InsecureSkipTLSVerify || got.EnvironmentTier != "production" ||
		got.DeployPolicy != "manual" || got.ConnectionType != "token" {
		t.Fatalf("update dropped preserved fields: %+v", got)
	}
	if got.APIServer == nil || *got.APIServer != "https://k8s:6443" {
		t.Fatalf("api_server not preserved: %+v", got.APIServer)
	}
	if got.CredentialCiphertext == nil {
		t.Fatal("credential lost on update")
	}

	// 切换连接类型不提供新 credential → 400
	w = do(t, e, "PUT", fmt.Sprintf("/api/v1/clusters/%d", id), map[string]any{
		"name": "tok", "env_code": "tok-1", "environment": "staging",
		"connection_type": "kubeconfig",
	})
	if w.Code != 400 {
		t.Fatalf("type switch without credential → %d", w.Code)
	}

	// legacy 集群 update 省略 key path → 保留
	keyPath := "/root/.ssh/id_rsa"
	leg, err := store.SaveCluster(service.Cluster{
		Name: "leg", EnvCode: "leg-1", Environment: "e", Enabled: true,
		EnvironmentTier: "non_production", DeployPolicy: "manual",
		ConnectionType: "legacy_ssh", K8sMasterHost: "h", K8sMasterPort: 22,
		K8sMasterUser: "u", K8sMasterKeyPath: &keyPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	w = do(t, e, "PUT", fmt.Sprintf("/api/v1/clusters/%d", leg.ID), map[string]any{
		"name": "leg", "env_code": "leg-1", "environment": "e",
		"connection_type": "legacy_ssh",
	})
	if w.Code != 200 {
		t.Fatalf("legacy update → %d %s", w.Code, w.Body.String())
	}
	legGot, _ := store.FindCluster(leg.ID)
	if legGot.K8sMasterKeyPath == nil || *legGot.K8sMasterKeyPath != keyPath {
		t.Fatal("legacy key path not preserved")
	}
	if legGot.K8sMasterHost != "h" || legGot.UpdatedAt.IsZero() || legGot.CreatedAt.IsZero() {
		t.Fatalf("legacy update lost fields or timestamps: %+v", legGot)
	}
}

func TestDeleteClusterReferencedConflict(t *testing.T) {
	store := service.NewStore()
	e := newCipherEngine(t, testCipherKey(), store)

	// 种子集群 id=1 无引用 → 可删
	if w := do(t, e, "DELETE", "/api/v1/clusters/1", nil); w.Code != 200 {
		t.Fatalf("delete unreferenced → %d", w.Code)
	}

	// 被 app 引用 → 409
	cl, err := store.SaveCluster(service.Cluster{
		Name: "c2", EnvCode: "c2", Environment: "e", Enabled: true,
		EnvironmentTier: "non_production", DeployPolicy: "manual", ConnectionType: "legacy_ssh",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveApp(service.App{
		ClusterID: cl.ID, AppName: "app", DeploymentName: "d", ContainerName: "c", Namespace: "n",
	}); err != nil {
		t.Fatal(err)
	}
	if w := do(t, e, "DELETE", fmt.Sprintf("/api/v1/clusters/%d", cl.ID), nil); w.Code != 409 {
		t.Fatalf("delete referenced cluster → %d", w.Code)
	}
	if _, err := store.FindCluster(cl.ID); err != nil {
		t.Fatal("referenced cluster must survive delete attempt")
	}

	// 被 release 引用 → 409
	cl2, err := store.SaveCluster(service.Cluster{
		Name: "c3", EnvCode: "c3", Environment: "e", Enabled: true,
		EnvironmentTier: "production", DeployPolicy: "manual", ConnectionType: "legacy_ssh",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, created, err := store.CreateImageRelease(
		service.ImageRelease{
			EventKey: "k-rel", EventID: "e", Source: "custom", AppName: "app",
			ImageFull: "r/a:v1", Version: "v1", TestClusterID: cl2.ID,
		},
		service.DeployLog{
			ClusterID: cl2.ID, AppName: "app", DeploymentName: "d", ContainerName: "c",
			Namespace: "n", Version: "v1", FileName: "registry", DeployType: "image_deploy", Status: "pending",
		})
	if err != nil || !created {
		t.Fatalf("seed release: created=%v err=%v", created, err)
	}
	if w := do(t, e, "DELETE", fmt.Sprintf("/api/v1/clusters/%d", cl2.ID), nil); w.Code != 409 {
		t.Fatalf("delete release-referenced cluster → %d", w.Code)
	}
}

func TestUpdateToDuplicateConflict(t *testing.T) {
	_, e, _ := newTestApp(t)

	// 种子集群 env_code=local；新建 env_code=b 后把它改成 local → 409
	if w := do(t, e, "POST", "/api/v1/clusters", map[string]any{
		"name": "B", "env_code": "b", "environment": "staging",
		"connection_type": "legacy_ssh", "k8s_master_host": "10.0.0.2",
	}); w.Code != 200 {
		t.Fatalf("create cluster B → %d", w.Code)
	}
	if w := do(t, e, "PUT", "/api/v1/clusters/2", map[string]any{
		"name": "B", "env_code": "local", "environment": "staging",
		"connection_type": "legacy_ssh", "k8s_master_host": "10.0.0.2",
	}); w.Code != 409 {
		t.Fatalf("update to dup env_code → %d %s", w.Code, w.Body.String())
	}
	// 更新自身 env_code 不冲突
	if w := do(t, e, "PUT", "/api/v1/clusters/2", map[string]any{
		"name": "B", "env_code": "b", "environment": "staging",
		"connection_type": "legacy_ssh", "k8s_master_host": "10.0.0.2",
	}); w.Code != 200 {
		t.Fatalf("self update → %d", w.Code)
	}

	mk := func(name string) map[string]any {
		return map[string]any{"cluster_id": 1, "app_name": name, "match_keyword": name, "deployment_name": "d", "container_name": "c"}
	}
	if w := do(t, e, "POST", "/api/v1/apps", mk("a1")); w.Code != 200 {
		t.Fatalf("create a1 → %d", w.Code)
	}
	if w := do(t, e, "POST", "/api/v1/apps", mk("a2")); w.Code != 200 {
		t.Fatalf("create a2 → %d", w.Code)
	}
	if w := do(t, e, "PUT", "/api/v1/apps/2", mk("a1")); w.Code != 409 {
		t.Fatalf("update to dup app_name → %d", w.Code)
	}
}

func TestAppValidation(t *testing.T) {
	_, e, _ := newTestApp(t)

	app := map[string]any{
		"cluster_id": 1, "app_name": "myapp", "match_keyword": "myapp",
		"deployment_name": "d", "container_name": "c",
	}
	if w := do(t, e, "POST", "/api/v1/apps", app); w.Code != 200 {
		t.Fatalf("create → %d %s", w.Code, w.Body.String())
	}
	if w := do(t, e, "POST", "/api/v1/apps", app); w.Code != 409 {
		t.Fatalf("duplicate app → %d", w.Code)
	}
	if w := do(t, e, "PUT", "/api/v1/apps/notanint", app); w.Code != 400 {
		t.Fatalf("bad id → %d", w.Code)
	}
	if w := do(t, e, "PUT", "/api/v1/apps/999", app); w.Code != 404 {
		t.Fatalf("missing app → %d", w.Code)
	}
	missing := map[string]any{"cluster_id": 1}
	if w := do(t, e, "POST", "/api/v1/apps", missing); w.Code != 400 {
		t.Fatalf("missing fields → %d", w.Code)
	}
}

func TestLogsLimitValidation(t *testing.T) {
	_, e, _ := newTestApp(t)
	if w := do(t, e, "GET", "/api/v1/deploy/logs?limit=200", nil); w.Code != 400 {
		t.Fatalf("limit=200 → %d", w.Code)
	}
	if w := do(t, e, "GET", "/api/v1/deploy/logs?limit=abc", nil); w.Code != 400 {
		t.Fatalf("limit=abc → %d", w.Code)
	}
	if w := do(t, e, "GET", "/api/v1/deploy/logs?cluster_id=abc", nil); w.Code != 400 {
		t.Fatalf("cluster_id=abc → %d", w.Code)
	}
	if w := do(t, e, "GET", "/api/v1/deploy/logs?limit=5", nil); w.Code != 200 {
		t.Fatalf("limit=5 → %d", w.Code)
	}
}

func TestFilesEndpoints(t *testing.T) {
	_, e, dir := newTestApp(t)

	w := do(t, e, "GET", "/api/v1/files", nil)
	if w.Code != 200 || w.Body.String() != "[]" {
		t.Fatalf("empty files → %d %s", w.Code, w.Body.String())
	}
	if err := os.WriteFile(filepath.Join(dir, "app-20260707185356.tar.gz"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	w = do(t, e, "GET", "/api/v1/files", nil)
	if w.Code != 200 {
		t.Fatalf("files → %d", w.Code)
	}
	var files []struct {
		FileName   string  `json:"file_name"`
		SizeMB     float64 `json:"size_mb"`
		ModifiedAt string  `json:"modified_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &files); err != nil || len(files) != 1 {
		t.Fatalf("files parse: %v %s", err, w.Body.String())
	}
	if files[0].FileName != "app-20260707185356.tar.gz" || files[0].ModifiedAt == "" {
		t.Fatalf("file info: %+v", files[0])
	}

	w = do(t, e, "GET", "/api/v1/files/parse-version?file_name=app-20260707185356.tar.gz", nil)
	var parsed struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &parsed); err != nil || parsed.Version != "v20260707185356" {
		t.Fatalf("parse-version → %s", w.Body.String())
	}
	if w := do(t, e, "GET", "/api/v1/files/parse-version", nil); w.Code != 400 {
		t.Fatalf("missing file_name → %d", w.Code)
	}
}

func TestK8sHandlersValidation(t *testing.T) {
	_, e, _ := newTestApp(t)

	if w := do(t, e, "GET", "/api/v1/k8s/pods", nil); w.Code != 400 {
		t.Fatalf("no cluster_id → %d", w.Code)
	}
	if w := do(t, e, "GET", "/api/v1/k8s/pods?cluster_id=999", nil); w.Code != 404 {
		t.Fatalf("unknown cluster → %d", w.Code)
	}
	w := do(t, e, "GET", "/api/v1/k8s/pods?cluster_id=1", nil)
	if w.Code != 200 {
		t.Fatalf("pods → %d %s", w.Code, w.Body.String())
	}
	var pods []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &pods); err != nil || len(pods) == 0 {
		t.Fatalf("pods parse: %v", err)
	}
	if w := do(t, e, "GET", "/api/v1/k8s/pods/p1/logs?cluster_id=1&tail=5", nil); w.Code != 400 {
		t.Fatalf("tail=5 → %d", w.Code)
	}
	w = do(t, e, "GET", "/api/v1/k8s/pods/p1/logs?cluster_id=1&tail=200", nil)
	if w.Code != 200 {
		t.Fatalf("logs → %d", w.Code)
	}
	w = do(t, e, "GET", "/api/v1/k8s/deployments/dev-demo-002-demo-gateway?cluster_id=1", nil)
	if w.Code != 200 {
		t.Fatalf("deployment → %d", w.Code)
	}
	var info map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info["name"] != "dev-demo-002-demo-gateway" {
		t.Fatalf("deployment info: %v", info["name"])
	}
	if w := do(t, e, "GET", "/api/v1/k8s/deployments/d/events?cluster_id=1", nil); w.Code != 200 {
		t.Fatalf("events → %d", w.Code)
	}
	if w := do(t, e, "POST", "/api/v1/k8s/pods/p1/restart?cluster_id=1", nil); w.Code != 200 {
		t.Fatalf("restart → %d", w.Code)
	}
}

func TestRollbackValidationAndHistory(t *testing.T) {
	app, e, _ := newTestApp(t)
	store := app.store

	if w := do(t, e, "POST", "/api/v1/rollback", map[string]any{"cluster_id": 1}); w.Code != 400 {
		t.Fatalf("missing fields → %d", w.Code)
	}
	image := "reg.io/app@sha256:" + strings.Repeat("12", 32)
	old, err := store.AddLog(service.DeployLog{ClusterID: 1, AppName: "myapp", DeploymentName: "d", ContainerName: "c", Namespace: "ns", ImageFull: &image, Version: "v1", Status: "success", DeployType: "image_deploy"})
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"cluster_id": 1, "target_log_id": old.ID}

	if w := do(t, e, "POST", "/api/v1/rollback", body); w.Code != 202 {
		t.Fatalf("rollback → %d %s", w.Code, w.Body.String())
	}

	w := do(t, e, "GET", "/api/v1/rollback/history/myapp", nil)
	if w.Code != 200 {
		t.Fatalf("history → %d", w.Code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		w = do(t, e, "GET", "/api/v1/rollback/history/myapp", nil)
		var history []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil {
			t.Fatal(err)
		}
		if len(history) == 2 && history[0]["deploy_type"] == "rollback" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("history never showed success: %s", w.Body.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if w := do(t, e, "GET", "/api/v1/rollback/history/myapp?limit=99", nil); w.Code != 400 {
		t.Fatalf("limit=99 → %d", w.Code)
	}
}

// failExecPersist accepts inserts/reads but fails every execution update.
type failExecPersist struct{ err error }

func (f failExecPersist) SaveCluster(c service.Cluster) (service.Cluster, error) { return c, nil }
func (f failExecPersist) UpdateCluster(service.Cluster) error                    { return nil }
func (f failExecPersist) DeleteCluster(int) error                                { return nil }
func (f failExecPersist) SaveApp(a service.App) (service.App, error)             { return a, nil }
func (f failExecPersist) UpdateApp(service.App) error                            { return nil }
func (f failExecPersist) DeleteApp(int) error                                    { return nil }
func (f failExecPersist) SaveLog(l service.DeployLog) (service.DeployLog, error) { return l, nil }
func (f failExecPersist) UpdateLogExecution(service.DeployLog) error             { return f.err }
func (f failExecPersist) CreateImageRelease(r service.ImageRelease, l service.DeployLog) (service.ImageRelease, service.DeployLog, bool, error) {
	return r, l, true, nil
}
func (f failExecPersist) SaveUser(u service.User) (service.User, error) { return u, nil }
func (f failExecPersist) UpdateUser(service.User) error                 { return nil }
func (f failExecPersist) SaveAudit(service.AuditEntry) error            { return nil }
func (f failExecPersist) SaveAPIKey(k service.APIKey) (service.APIKey, error) {
	return k, nil
}
func (f failExecPersist) UpdateAPIKey(service.APIKey) error  { return nil }
func (f failExecPersist) DeleteAPIKey(int64) error           { return nil }
func (f failExecPersist) TouchAPIKey(int64, time.Time) error { return nil }
func (f failExecPersist) SavePromotionRequest(p service.PromotionRequest) (service.PromotionRequest, error) {
	return p, nil
}
func (f failExecPersist) UpdatePromotionRequest(service.PromotionRequest) error { return nil }
func (f failExecPersist) PromoteImageRelease(_ int64, _ int, l service.DeployLog) (service.ImageRelease, service.DeployLog, error) {
	return service.ImageRelease{}, l, nil
}

func TestSubmitJobDoubleFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := config.Config{Env: "dev", MockRuntime: true, TransferDir: t.TempDir()}
	persistErr := errors.New("db write down")
	store := service.NewStoreWithData(
		[]service.Cluster{{ID: 1, EnvCode: "local", Enabled: true}}, nil, nil, nil,
		failExecPersist{err: persistErr})
	runner := &service.MockRunner{}
	k8s := service.NewK8sService(cfg, runner, nil)
	worker := service.NewWorker(service.NewPipeline(cfg, store, runner, k8s), 1)
	worker.Close() // every Submit now fails
	seedTestAuth(t, store)
	e := newEngine(cfg, store, worker, k8s, service.NewCDService(cfg, store, worker), nil, nil)

	w := do(t, e, "POST", "/api/v1/deploy", map[string]any{
		"cluster_id": 1, "file_name": "x.tar.gz", "deployment_name": "d",
		"container_name": "c", "version": "v1",
	})
	if w.Code != 500 {
		t.Fatalf("closed worker → %d %s", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	detail := body["detail"]
	for _, want := range []string{"任务提交失败", "状态记录失败", persistErr.Error()} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail %q missing %q", detail, want)
		}
	}
}

func TestInitStoreEmptyDSN(t *testing.T) {
	store, db, err := initStore(config.Config{MockRuntime: true, DatabaseDSN: "   "})
	if err != nil {
		t.Fatalf("mock + empty DSN must not fail: %v", err)
	}
	if db != nil || store == nil {
		t.Fatalf("expected memory store with nil db, got db=%v store=%v", db, store)
	}
	if _, _, err := initStore(config.Config{MockRuntime: false, DatabaseDSN: ""}); err == nil {
		t.Fatal("prod + empty DSN must fail before dialing")
	}
}

func newCDEngine(t *testing.T, cfg config.Config, store *service.Store) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	runner := &service.MockRunner{}
	k8s := service.NewK8sService(cfg, runner, nil)
	worker := service.NewWorker(service.NewPipeline(cfg, store, runner, k8s), 1)
	t.Cleanup(worker.Close)
	seedTestAuth(t, store)
	return newEngine(cfg, store, worker, k8s, service.NewCDService(cfg, store, worker), nil, nil)
}

func cdStore() *service.Store {
	store := service.NewStoreWithData(
		[]service.Cluster{
			{ID: 1, Name: "测试", EnvCode: "test", Environment: "staging",
				EnvironmentTier: "non_production", DeployPolicy: "automatic", ConnectionType: "kubeconfig", Enabled: true},
			{ID: 2, Name: "生产", EnvCode: "prod", Environment: "prod",
				EnvironmentTier: "production", DeployPolicy: "manual", ConnectionType: "kubeconfig", Enabled: true},
		},
		[]service.App{
			{ID: 1, ClusterID: 1, AppName: "app", DeploymentName: "app", ContainerName: "app", Namespace: "ns"},
			{ID: 2, ClusterID: 2, AppName: "app", DeploymentName: "app", ContainerName: "app", Namespace: "ns"},
			{ID: 3, ClusterID: 1, AppName: "app2", DeploymentName: "app2", ContainerName: "app2", Namespace: "ns"},
		},
		nil, nil, nil)
	store.LoadDeliveryEnvironmentEvidencePolicyVersions([]service.DeliveryEnvironmentEvidencePolicyVersion{{
		EnvironmentID: 2, Version: 1, RequiredVerificationKinds: []string{"ci"},
		MaxEvidenceAgeMinutes: service.MaxEvidenceFreshnessMinutes,
		TrustedSubmitters:     map[string][]string{"ci": {"apikey:ci-test"}}, CreatedBy: "test", CreatedAt: time.Now(),
	}})
	return store
}

func cdConfig(t *testing.T) config.Config {
	return config.Config{
		Env: "dev", MockRuntime: true, TransferDir: t.TempDir(),
	}
}

func TestWebhookCustomPayloadAndIdempotency(t *testing.T) {
	e := newCDEngine(t, cdConfig(t), cdStore())
	payload := map[string]any{
		"event_id": "build-1", "app_name": "app", "target_cluster": "test",
		"image_full": "reg.io/Demo/app:v1", "branch": "main",
	}
	w := do(t, e, "POST", "/api/v1/webhooks/yunxiao", payload)
	if w.Code != 202 {
		t.Fatalf("custom webhook → %d %s", w.Code, w.Body.String())
	}
	var first map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first["duplicate"] != false || first["release_id"] == nil || first["test_log_id"] == nil {
		t.Fatalf("unexpected response %v", first)
	}

	w = do(t, e, "POST", "/api/v1/webhooks/yunxiao", payload)
	if w.Code != 200 {
		t.Fatalf("duplicate webhook → %d %s", w.Code, w.Body.String())
	}
	var dup map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &dup); err != nil {
		t.Fatal(err)
	}
	if dup["duplicate"] != true || dup["release_id"] != first["release_id"] {
		t.Fatalf("duplicate response wrong: %v", dup)
	}
}

func TestWebhookYunxiaoStandardPayload(t *testing.T) {
	store := cdStore()
	e := newCDEngine(t, cdConfig(t), store)

	std := func(status string) map[string]any {
		return map[string]any{
			"event": "PIPELINE", "action": "FINISHED",
			"task": map[string]any{
				"pipelineId": 12345, "pipelineName": "demo-app",
				"buildNumber": "42", "statusCode": status,
			},
			"sources": []map[string]any{{"branch": "main", "commitId": "abc123"}},
			"globalParams": []map[string]any{
				{"key": "app_name", "value": "app"},
				{"key": "IMAGE_FULL", "value": "reg.io/Demo/app:v2"},
				{"key": "Image_Digest", "value": "sha256:" + strings.Repeat("ab", 32)},
				{"key": "target_cluster", "value": "test"},
				{"key": "commit_timestamp", "value": "2026-10-15T09:30:00+08:00"},
				{"key": "UNRELATED", "value": "ignored"},
			},
		}
	}
	w := do(t, e, "POST", "/api/v1/webhooks/yunxiao", std("SUCCESS"))
	if w.Code != 202 {
		t.Fatalf("standard webhook → %d %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["duplicate"] != false {
		t.Fatalf("unexpected %v", body)
	}
	releaseID := int64(body["release_id"].(float64))
	release, err := store.FindImageRelease(releaseID)
	if err != nil {
		t.Fatal(err)
	}
	wantCommitTime := time.Date(2026, 10, 15, 1, 30, 0, 0, time.UTC)
	if release.Metadata.CommitTimestamp == nil || !release.Metadata.CommitTimestamp.Equal(wantCommitTime) {
		t.Fatalf("Yunxiao commit time was not persisted in UTC: %+v", release.Metadata.CommitTimestamp)
	}
	firstLog, err := store.FindLog(release.TestDeployLogID)
	if err != nil {
		t.Fatal(err)
	}
	firstLog.Status = "success"
	finishedAt := time.Now()
	firstLog.FinishedAt = &finishedAt
	if err := store.UpdateLogExecution(firstLog); err != nil {
		t.Fatal(err)
	}
	invalidTimestamp := std("SUCCESS")
	invalidTask := invalidTimestamp["task"].(map[string]any)
	invalidTask["buildNumber"] = "43"
	invalidParams := invalidTimestamp["globalParams"].([]map[string]any)
	invalidParams = append(invalidParams, map[string]any{"key": "COMMIT_TIMESTAMP", "value": "not-a-timestamp"})
	invalidTimestamp["globalParams"] = invalidParams
	if response := do(t, e, "POST", "/api/v1/webhooks/yunxiao", invalidTimestamp); response.Code != 202 {
		t.Fatalf("invalid optional commit telemetry rejected valid image event: %d %s", response.Code, response.Body.String())
	}
	invalidRelease, found := store.ReleaseByEventKey(service.EventKey("yunxiao", "12345:43", "test"))
	if !found || invalidRelease.Metadata.CommitTimestamp != nil {
		t.Fatalf("invalid optional commit timestamp should remain unknown: %+v, found=%v", invalidRelease.Metadata.CommitTimestamp, found)
	}

	w = do(t, e, "POST", "/api/v1/webhooks/yunxiao", std("RUNNING"))
	if w.Code != 202 {
		t.Fatalf("non-success → %d", w.Code)
	}
	var ig map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &ig); err != nil {
		t.Fatal(err)
	}
	if ig["ignored"] != true || ig["status"] != "RUNNING" {
		t.Fatalf("ignored response wrong: %v", ig)
	}

	missing := std("SUCCESS")
	missing["globalParams"] = []map[string]any{{"key": "IMAGE_FULL", "value": "r/a:v1"}}
	if w := do(t, e, "POST", "/api/v1/webhooks/yunxiao", missing); w.Code != 400 {
		t.Fatalf("missing APP_NAME → %d", w.Code)
	}
	missing = std("SUCCESS")
	missing["globalParams"] = []map[string]any{
		{"key": "APP_NAME", "value": "app"},
		{"key": "IMAGE_FULL", "value": "r/a:v1"},
	}
	if w := do(t, e, "POST", "/api/v1/webhooks/yunxiao", missing); w.Code != 400 {
		t.Fatalf("missing TARGET_CLUSTER → %d", w.Code)
	}
	missing = std("SUCCESS")
	missing["task"] = map[string]any{"statusCode": "SUCCESS", "pipelineName": "p"}
	if w := do(t, e, "POST", "/api/v1/webhooks/yunxiao", missing); w.Code != 400 {
		t.Fatalf("missing pipelineId/buildNumber → %d", w.Code)
	}
}

func TestWebhookYunxiaoRequiresRegisteredSourceRepository(t *testing.T) {
	store := cdStore()
	app, err := store.FindAppByName(1, "app")
	if err != nil {
		t.Fatal(err)
	}
	app.SourceKind = "repository"
	app.SourceRepoURL = "https://codeup.aliyun.com/team/app.git"
	app.SourcePath = "services/api"
	app.ImageRepository = "reg.io/Demo/app"
	if _, err := store.UpdateApp(app); err != nil {
		t.Fatal(err)
	}
	e := newCDEngine(t, cdConfig(t), store)

	standardEvent := func(buildNumber string, repo, sourcePath string) map[string]any {
		sources := []map[string]any{{"branch": "main", "commitId": "abc123"}}
		if repo != "" {
			sources[0]["repo"] = repo
		}
		globalParams := []map[string]any{
			{"key": "APP_NAME", "value": "app"},
			{"key": "IMAGE_FULL", "value": "reg.io/Demo/app:v2"},
			{"key": "IMAGE_DIGEST", "value": "sha256:" + strings.Repeat("ab", 32)},
			{"key": "TARGET_CLUSTER", "value": "test"},
		}
		if sourcePath != "" {
			globalParams = append(globalParams, map[string]any{"key": "SOURCE_PATH", "value": sourcePath})
		}
		return map[string]any{
			"event": "PIPELINE", "action": "FINISHED",
			"task": map[string]any{
				"pipelineId": 12345, "pipelineName": "demo-app",
				"buildNumber": buildNumber, "statusCode": "SUCCESS",
			},
			"sources":      sources,
			"globalParams": globalParams,
		}
	}

	w := do(t, e, "POST", "/api/v1/webhooks/yunxiao", standardEvent("42", "git@codeup.aliyun.com:team/app.git", "services/api"))
	if w.Code != 202 {
		t.Fatalf("matching Yunxiao source → %d %s", w.Code, w.Body.String())
	}
	var accepted map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	releaseID := int64(accepted["release_id"].(float64))
	w = do(t, e, "GET", fmt.Sprintf("/api/v1/releases/%d", releaseID), nil)
	if w.Code != 200 {
		t.Fatalf("release detail → %d %s", w.Code, w.Body.String())
	}
	var detail struct {
		Metadata service.ReleaseMetadata `json:"metadata"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Metadata.SourceRepositories) != 1 || detail.Metadata.SourceRepositories[0] != "codeup.aliyun.com/team/app" || detail.Metadata.SourcePath != "services/api" {
		t.Fatalf("release source metadata = repositories:%#v path:%q", detail.Metadata.SourceRepositories, detail.Metadata.SourcePath)
	}

	for _, tc := range []struct {
		name       string
		repo       string
		sourcePath string
	}{
		{name: "mismatched repository", repo: "https://codeup.aliyun.com/team/other.git", sourcePath: "services/api"},
		{name: "missing repository", sourcePath: "services/api"},
		{name: "mismatched source path", repo: "https://codeup.aliyun.com/team/app.git", sourcePath: "services/web"},
		{name: "missing source path", repo: "https://codeup.aliyun.com/team/app.git"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, e, "POST", "/api/v1/webhooks/yunxiao", standardEvent("43-"+tc.name, tc.repo, tc.sourcePath))
			if w.Code != 400 {
				t.Fatalf("invalid source → %d %s", w.Code, w.Body.String())
			}
		})
	}

	rootApp, err := store.FindAppByName(1, "app2")
	if err != nil {
		t.Fatal(err)
	}
	rootApp.SourceKind = "repository"
	rootApp.SourceRepoURL = "https://codeup.aliyun.com/team/app2.git"
	rootApp.SourcePath = "."
	rootApp.ImageRepository = "reg.io/Demo/app2"
	if _, err := store.UpdateApp(rootApp); err != nil {
		t.Fatal(err)
	}
	rootEvent := standardEvent("1", "https://codeup.aliyun.com/team/app2.git", "")
	rootEvent["globalParams"] = []map[string]any{
		{"key": "APP_NAME", "value": "app2"},
		{"key": "IMAGE_FULL", "value": "reg.io/Demo/app2:v1"},
		{"key": "IMAGE_DIGEST", "value": "sha256:" + strings.Repeat("cd", 32)},
		{"key": "TARGET_CLUSTER", "value": "test"},
	}
	if w := do(t, e, "POST", "/api/v1/webhooks/yunxiao", rootEvent); w.Code != 202 {
		t.Fatalf("root repository without SOURCE_PATH → %d %s", w.Code, w.Body.String())
	}

	w = do(t, e, "GET", "/api/v1/releases", nil)
	if w.Code != 200 {
		t.Fatalf("list releases → %d %s", w.Code, w.Body.String())
	}
	var releases []any
	if err := json.Unmarshal(w.Body.Bytes(), &releases); err != nil {
		t.Fatal(err)
	}
	if len(releases) != 2 {
		t.Fatalf("expected only the matching subdirectory and root releases, got %d", len(releases))
	}
}

func TestWebhookTokenAuth(t *testing.T) {
	payload := map[string]any{
		"event_id": "e", "app_name": "app", "target_cluster": "test",
		"image_full": "reg.io/Demo/app:v1",
	}
	post := func(e *gin.Engine, headers map[string]string) *httptest.ResponseRecorder {
		data, _ := json.Marshal(payload)
		req := httptest.NewRequest("POST", "/api/v1/webhooks/yunxiao", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		e.ServeHTTP(w, req)
		return w
	}

	cfg := cdConfig(t)
	cfg.YunxiaoWebhookToken = "s3cret"
	e := newCDEngine(t, cfg, cdStore())
	if w := post(e, nil); w.Code != 401 {
		t.Fatalf("no token → %d", w.Code)
	}
	if w := post(e, map[string]string{"Authorization": "Bearer wrong"}); w.Code != 401 {
		t.Fatalf("wrong token → %d", w.Code)
	}
	if w := post(e, map[string]string{"Authorization": "Bearer s3cret"}); w.Code != 202 {
		t.Fatalf("bearer token → %d %s", w.Code, w.Body.String())
	}
	// 换 app 避开同 app+cluster 的发布互斥——本测试只关心令牌认证
	payload["event_id"] = "e2"
	payload["app_name"] = "app2"
	if w := post(e, map[string]string{"X-Webhook-Token": "s3cret"}); w.Code != 202 {
		t.Fatalf("x-webhook-token → %d %s", w.Code, w.Body.String())
	}

	// 非 mock 且未配置 token → 拒绝服务
	cfg = cdConfig(t)
	cfg.MockRuntime = false
	e = newCDEngine(t, cfg, cdStore())
	payload["event_id"] = "e3"
	if w := post(e, nil); w.Code != 503 {
		t.Fatalf("unconfigured real webhook → %d", w.Code)
	}
}

func TestReleaseListAndPromote(t *testing.T) {
	cfg := cdConfig(t)
	store := cdStore()
	e := newCDEngine(t, cfg, store)

	w := do(t, e, "POST", "/api/v1/webhooks/yunxiao", map[string]any{
		"event_id": "e1", "app_name": "app", "target_cluster": "test",
		"image_full": "reg.io/Demo/app:v1",
	})
	if w.Code != 202 {
		t.Fatalf("webhook → %d %s", w.Code, w.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	releaseID := int64(created["release_id"].(float64))
	attestHTTPRelease(t, store, releaseID)
	testLogID := int64(created["test_log_id"].(float64))

	// 等测试发布成功
	deadline := time.Now().Add(5 * time.Second)
	for {
		l, _ := store.FindLog(testLogID)
		if l.Status == "success" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("test deploy never succeeded: %+v", l)
		}
		time.Sleep(20 * time.Millisecond)
	}

	w = do(t, e, "GET", "/api/v1/releases", nil)
	if w.Code != 200 {
		t.Fatalf("releases → %d", w.Code)
	}
	var views []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0]["can_promote"] != true || views[0]["test_status"] != "success" {
		t.Fatalf("views=%v", views)
	}
	if w := do(t, e, "GET", "/api/v1/releases?limit=999", nil); w.Code != 400 {
		t.Fatalf("limit=999 → %d", w.Code)
	}

	if w := do(t, e, "POST", "/api/v1/releases/999/promote", map[string]any{"cluster_id": 2}); w.Code != 404 {
		t.Fatalf("unknown release → %d", w.Code)
	}
	if w := do(t, e, "POST", fmt.Sprintf("/api/v1/releases/%d/promote", releaseID), nil); w.Code != 400 {
		t.Fatalf("missing cluster_id → %d", w.Code)
	}
	if w := do(t, e, "POST", fmt.Sprintf("/api/v1/releases/%d/promote", releaseID), map[string]any{"cluster_id": -1}); w.Code != 400 {
		t.Fatalf("cluster_id=-1 → %d", w.Code)
	}
	if w := do(t, e, "POST", fmt.Sprintf("/api/v1/releases/%d/promote", releaseID), map[string]any{"cluster_id": 1}); w.Code != 400 {
		t.Fatalf("non-prod cluster → %d", w.Code)
	}
	// 可选生产目标列表只含 cluster 2
	w = do(t, e, "GET", fmt.Sprintf("/api/v1/releases/%d/promotion-targets", releaseID), nil)
	if w.Code != 200 {
		t.Fatalf("promotion-targets → %d", w.Code)
	}
	var targets []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &targets); err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0]["id"].(float64) != 2 {
		t.Fatalf("targets=%v", targets)
	}
	if _, has := targets[0]["credential_ciphertext"]; has {
		t.Fatal("promotion target leaks credential_ciphertext")
	}

	// 单条部署记录查询 —— 详情 drawer 的数据源
	w = do(t, e, "GET", fmt.Sprintf("/api/v1/deploy/logs/%d", testLogID), nil)
	if w.Code != 200 {
		t.Fatalf("deploy log by id → %d", w.Code)
	}
	var oneLog map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &oneLog); err != nil {
		t.Fatal(err)
	}
	if oneLog["id"].(float64) != float64(testLogID) || oneLog["step_detail"] == "" {
		t.Fatalf("log detail %v", oneLog)
	}
	w = do(t, e, "GET", fmt.Sprintf("/api/v1/deploy/logs/%d/actions", testLogID), nil)
	if w.Code != 200 {
		t.Fatalf("deploy action journal → %d: %s", w.Code, w.Body.String())
	}
	var actionJournal []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &actionJournal); err != nil || actionJournal == nil {
		t.Fatalf("deploy action journal=%v err=%v", actionJournal, err)
	}
	if w := do(t, e, "GET", "/api/v1/deploy/logs/999", nil); w.Code != 404 {
		t.Fatalf("missing log → %d", w.Code)
	}
	if w := do(t, e, "GET", "/api/v1/deploy/logs/999/actions", nil); w.Code != 404 {
		t.Fatalf("missing deploy action journal → %d", w.Code)
	}

	// 晋级对话框的当前版本：cluster_id 必填、无映射 404、有映射返回 mock 镜像
	if w := do(t, e, "GET", fmt.Sprintf("/api/v1/releases/%d/current-image", releaseID), nil); w.Code != 400 {
		t.Fatalf("current-image missing cluster_id → %d", w.Code)
	}
	if w := do(t, e, "GET", fmt.Sprintf("/api/v1/releases/%d/current-image?cluster_id=99", releaseID), nil); w.Code != 404 {
		t.Fatalf("current-image unmapped cluster → %d", w.Code)
	}
	if w := do(t, e, "GET", "/api/v1/releases/999/current-image?cluster_id=2", nil); w.Code != 404 {
		t.Fatalf("current-image unknown release → %d", w.Code)
	}
	w = do(t, e, "GET", fmt.Sprintf("/api/v1/releases/%d/current-image?cluster_id=2", releaseID), nil)
	if w.Code != 200 {
		t.Fatalf("current-image → %d %s", w.Code, w.Body.String())
	}
	var cur map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &cur); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cur["image"].(string), "/app:mock-current") {
		t.Fatalf("current image %v", cur["image"])
	}

	w = do(t, e, "POST", fmt.Sprintf("/api/v1/releases/%d/promote", releaseID), map[string]any{"cluster_id": 2})
	if w.Code != 202 {
		t.Fatalf("promote → %d %s", w.Code, w.Body.String())
	}
	var promoted map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &promoted); err != nil {
		t.Fatal(err)
	}
	if promoted["prod_log_id"] == nil {
		t.Fatalf("promote response %v", promoted)
	}
	prodLog, err := store.FindLog(int64(promoted["prod_log_id"].(float64)))
	if err != nil || prodLog.ClusterID != 2 || prodLog.DeployType != "image_deploy" {
		t.Fatalf("prod log %+v err=%v", prodLog, err)
	}
	if prodLog.ImageFull == nil || *prodLog.ImageFull != deployImageForTest(store, releaseID) {
		t.Fatalf("prod image changed: %+v", prodLog.ImageFull)
	}

	// 生产发布在 pending/running/success 任一状态都禁止重复 promote
	w = do(t, e, "POST", fmt.Sprintf("/api/v1/releases/%d/promote", releaseID), map[string]any{"cluster_id": 2})
	if w.Code != 409 {
		t.Fatalf("second promote → %d %s", w.Code, w.Body.String())
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		l, _ := store.FindLog(prodLog.ID)
		if l.Status == "success" || l.Status == "failed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	w = do(t, e, "POST", fmt.Sprintf("/api/v1/releases/%d/promote", releaseID), map[string]any{"cluster_id": 2})
	if w.Code != 409 {
		t.Fatalf("promote after prod terminal → %d %s", w.Code, w.Body.String())
	}
}

func TestAppsListAndDelete(t *testing.T) {
	_, e, _ := newTestApp(t)

	mk := func(name string, clusterID int) map[string]any {
		return map[string]any{"cluster_id": clusterID, "app_name": name, "match_keyword": name,
			"deployment_name": "d-" + name, "container_name": "c-" + name}
	}
	if w := do(t, e, "POST", "/api/v1/apps", mk("a1", 1)); w.Code != 200 {
		t.Fatalf("create a1 → %d", w.Code)
	}
	if w := do(t, e, "POST", "/api/v1/clusters", map[string]any{
		"name": "c2", "env_code": "c2", "environment": "e",
		"connection_type": "legacy_ssh", "k8s_master_host": "h",
	}); w.Code != 200 {
		t.Fatalf("create cluster c2 → %d", w.Code)
	}
	if w := do(t, e, "POST", "/api/v1/apps", mk("a2", 2)); w.Code != 200 {
		t.Fatalf("create a2 → %d", w.Code)
	}

	w := do(t, e, "GET", "/api/v1/apps", nil)
	if w.Code != 200 {
		t.Fatalf("apps → %d", w.Code)
	}
	var apps []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &apps); err != nil || len(apps) != 2 {
		t.Fatalf("apps=%v err=%v", apps, err)
	}

	w = do(t, e, "GET", "/api/v1/apps?cluster_id=2", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &apps); err != nil ||
		len(apps) != 1 || apps[0]["app_name"] != "a2" {
		t.Fatalf("filtered apps=%v", apps)
	}
	if w := do(t, e, "GET", "/api/v1/apps?cluster_id=abc", nil); w.Code != 400 {
		t.Fatalf("cluster_id=abc → %d", w.Code)
	}

	if w := do(t, e, "DELETE", "/api/v1/apps/1", nil); w.Code != 200 {
		t.Fatalf("delete app → %d %s", w.Code, w.Body.String())
	}
	if w := do(t, e, "DELETE", "/api/v1/apps/1", nil); w.Code != 404 {
		t.Fatalf("re-delete → %d", w.Code)
	}
	w = do(t, e, "GET", "/api/v1/apps", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &apps); err != nil || len(apps) != 1 {
		t.Fatalf("apps after delete=%v", apps)
	}
}

// TestK8sDisabledClusterContract freezes the refined error contract: any
// /k8s endpoint on a disabled cluster fails 403 with a stable detail.
func TestK8sDisabledClusterContract(t *testing.T) {
	store := service.NewStore()
	cl, err := store.SaveCluster(service.Cluster{
		Name: "disabled", EnvCode: "disabled", Environment: "e", Enabled: false,
		EnvironmentTier: "non_production", DeployPolicy: "manual", ConnectionType: "legacy_ssh",
	})
	if err != nil {
		t.Fatal(err)
	}
	e := newCipherEngine(t, testCipherKey(), store)

	for _, ep := range []string{
		fmt.Sprintf("/api/v1/k8s/pods?cluster_id=%d", cl.ID),
		fmt.Sprintf("/api/v1/k8s/pods/p/logs?cluster_id=%d", cl.ID),
		fmt.Sprintf("/api/v1/k8s/deployments/d?cluster_id=%d", cl.ID),
		fmt.Sprintf("/api/v1/k8s/deployments/d/events?cluster_id=%d", cl.ID),
		fmt.Sprintf("/api/v1/k8s/resources?cluster_id=%d&version=v1&resource=pods", cl.ID),
		fmt.Sprintf("/api/v1/k8s/resources/p?cluster_id=%d&version=v1&resource=pods", cl.ID),
		fmt.Sprintf("/api/v1/k8s/overview?cluster_id=%d", cl.ID),
	} {
		w := do(t, e, "GET", ep, nil)
		if w.Code != 403 {
			t.Fatalf("%s → %d, want 403", ep, w.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil ||
			!strings.Contains(body["detail"], "已禁用") {
			t.Fatalf("%s body=%s", ep, w.Body.String())
		}
	}
	if w := do(t, e, "POST", fmt.Sprintf("/api/v1/k8s/pods/p/restart?cluster_id=%d", cl.ID), nil); w.Code != 403 {
		t.Fatalf("restart on disabled cluster → %d", w.Code)
	}
	// Generic write paths must also respect the disabled cluster gate.
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"POST", fmt.Sprintf("/api/v1/k8s/resources/d/scale?cluster_id=%d&group=apps&version=v1&resource=deployments&namespace=default", cl.ID), map[string]any{"replicas": 1}},
		{"POST", fmt.Sprintf("/api/v1/k8s/resources/d/image?cluster_id=%d&group=apps&version=v1&resource=deployments&namespace=default", cl.ID), map[string]any{"container": "c", "image": "i"}},
		{"POST", fmt.Sprintf("/api/v1/k8s/resources/d/restart?cluster_id=%d&group=apps&version=v1&resource=deployments&namespace=default", cl.ID), nil},
		{"DELETE", fmt.Sprintf("/api/v1/k8s/resources/p?cluster_id=%d&version=v1&resource=pods&namespace=default", cl.ID), nil},
	} {
		if w := do(t, e, tc.method, tc.path, tc.body); w.Code != 403 {
			t.Fatalf("%s %s → %d, want 403", tc.method, tc.path, w.Code)
		}
	}
}

func TestRollbackUnknownCluster(t *testing.T) {
	_, e, _ := newTestApp(t)
	body := map[string]any{
		"target_log_id": 1, "cluster_id": 999, "app_name": "a", "deployment_name": "d",
		"container_name": "c", "namespace": "n", "image_full": "r/a:v1", "version": "v1",
	}
	if w := do(t, e, "POST", "/api/v1/rollback", body); w.Code != 404 {
		t.Fatalf("unknown cluster rollback → %d", w.Code)
	}
}

// TestWebhookCustomValidation freezes the HTTP-level validation contract of
// the custom webhook payload: missing fields and unparseable image → 400,
// unknown cluster/app → 404, wrong tier/policy → 400.
func TestWebhookCustomValidation(t *testing.T) {
	e := newCDEngine(t, cdConfig(t), cdStore())
	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"no event_id", map[string]any{"app_name": "app", "target_cluster": "test", "image_full": "r/a:v1"}, 400},
		{"no app_name", map[string]any{"event_id": "e", "target_cluster": "test", "image_full": "r/a:v1"}, 400},
		{"no target_cluster", map[string]any{"event_id": "e", "app_name": "app", "image_full": "r/a:v1"}, 400},
		{"no image_full", map[string]any{"event_id": "e", "app_name": "app", "target_cluster": "test"}, 400},
		{"unversioned image", map[string]any{"event_id": "e", "app_name": "app", "target_cluster": "test", "image_full": "nginx"}, 400},
		{"blank in image", map[string]any{"event_id": "e", "app_name": "app", "target_cluster": "test", "image_full": "r/a: v1"}, 400},
		{"prod cluster as target", map[string]any{"event_id": "e", "app_name": "app", "target_cluster": "prod", "image_full": "r/a:v1"}, 400},
		{"unknown cluster", map[string]any{"event_id": "e", "app_name": "app", "target_cluster": "ghost", "image_full": "r/a:v1"}, 404},
		{"unknown app", map[string]any{"event_id": "e", "app_name": "ghost", "target_cluster": "test", "image_full": "r/a:v1"}, 404},
	}
	for _, tc := range cases {
		if w := do(t, e, "POST", "/api/v1/webhooks/yunxiao", tc.body); w.Code != tc.want {
			t.Fatalf("%s → %d (want %d) %s", tc.name, w.Code, tc.want, w.Body.String())
		}
	}
}

func TestDeployLogsFilters(t *testing.T) {
	store := service.NewStore()
	e := newCipherEngine(t, testCipherKey(), store)
	now := time.Now()
	img := "r/a:v1"
	for _, l := range []service.DeployLog{
		{ClusterID: 1, AppName: "a", DeploymentName: "d", ContainerName: "c", Namespace: "n",
			Version: "v1", FileName: "f", ImageFull: &img, DeployType: "deploy", Status: "success"},
		{ClusterID: 1, AppName: "b", DeploymentName: "d", ContainerName: "c", Namespace: "n",
			Version: "v2", FileName: "f", ImageFull: &img, DeployType: "deploy", Status: "failed"},
		{ClusterID: 2, AppName: "a", DeploymentName: "d", ContainerName: "c", Namespace: "n",
			Version: "v3", FileName: "f", ImageFull: &img, DeployType: "deploy", Status: "success"},
	} {
		l.CreatedAt = now
		if _, err := store.AddLog(l); err != nil {
			t.Fatal(err)
		}
	}

	get := func(q string) []map[string]any {
		w := do(t, e, "GET", "/api/v1/deploy/logs"+q, nil)
		if w.Code != 200 {
			t.Fatalf("logs%s → %d", q, w.Code)
		}
		var logs []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &logs); err != nil {
			t.Fatal(err)
		}
		return logs
	}
	if logs := get("?limit=10"); len(logs) != 3 || logs[0]["version"] != "v3" {
		t.Fatalf("unfiltered=%v", logs)
	}
	if logs := get("?app_name=a"); len(logs) != 2 {
		t.Fatalf("app_name=a → %v", logs)
	}
	if logs := get("?app_name=a&cluster_id=1"); len(logs) != 1 || logs[0]["version"] != "v1" {
		t.Fatalf("a+c1 → %v", logs)
	}
	if logs := get("?status=failed"); len(logs) != 1 || logs[0]["version"] != "v2" {
		t.Fatalf("failed → %v", logs)
	}
	if logs := get("?status=success&limit=1"); len(logs) != 1 || logs[0]["version"] != "v3" {
		t.Fatalf("success limit → %v", logs)
	}
}

// TestNoRouteAndEmptyLists freezes two contracts behind a real crash:
// API paths never fall back to the SPA index.html (a missed route must
// answer a JSON 404 in the surface's own envelope), and empty list
// endpoints marshal as [] — never null, which clients can't range over.
func TestNoRouteAndEmptyLists(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>spa</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	// FrontendDir must carry an index.html for registerFrontend to mount
	// the SPA fallback at all — production parity for the NoRoute path.
	app, err := NewApplication(config.Config{
		Env:         "dev",
		MockRuntime: true,
		TransferDir: t.TempDir(),
		FrontendDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	seedTestAuth(t, app.store)
	e := app.Engine

	// Missed API routes answer JSON 404 in each surface's envelope —
	// never the SPA page.
	for _, tc := range []struct {
		path, key string
	}{
		{"/api/v1/api-keys-typo", "detail"},
		{"/openapi/v1/nope", "error"},
	} {
		w := do(t, e, "GET", tc.path, nil)
		if w.Code != 404 {
			t.Fatalf("%s → %d %s", tc.path, w.Code, w.Body.String())
		}
		var env map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || env[tc.key] == nil {
			t.Fatalf("%s: want {%s:…} body, got %s", tc.path, tc.key, w.Body.String())
		}
	}

	// SPA fallback intact: extensionless page paths serve index.html,
	// asset-looking paths get a bare 404.
	w := do(t, e, "GET", "/settings/api-keys", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "spa") {
		t.Fatalf("spa fallback → %d %s", w.Code, w.Body.String())
	}
	w = do(t, e, "GET", "/tasks/logs/781", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "spa") {
		t.Fatalf("deploy task deep link → %d %s", w.Code, w.Body.String())
	}
	if w := do(t, e, "GET", "/assets/missing.js", nil); w.Code != 404 {
		t.Fatalf("missing asset → %d", w.Code)
	}

	// The api-keys store is empty on a fresh app — the body must be a
	// literal [] since a null body crashes clients ranging over it.
	w = do(t, e, "GET", "/api/v1/api-keys", nil)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("empty api-keys → %d %s", w.Code, w.Body.String())
	}
	// Seeded lists must marshal as JSON arrays too, never null.
	for _, path := range []string{"/api/v1/clusters", "/api/v1/users"} {
		w := do(t, e, "GET", path, nil)
		if w.Code != 200 {
			t.Fatalf("%s → %d %s", path, w.Code, w.Body.String())
		}
		var arr []any
		if err := json.Unmarshal(w.Body.Bytes(), &arr); err != nil || arr == nil {
			t.Fatalf("%s must answer a JSON array, got %s", path, w.Body.String())
		}
	}
}

func TestPromotionApprovalEndpoints(t *testing.T) {
	cfg := cdConfig(t)
	cfg.PromotionApproval = true
	store := cdStore()
	confirmApprovalFixture(store)
	e := newCDEngine(t, cfg, store)

	w := do(t, e, "POST", "/api/v1/webhooks/yunxiao", map[string]any{
		"event_id": "ap-1", "app_name": "app", "target_cluster": "test",
		"image_full": "reg.io/Demo/app:v9",
	})
	if w.Code != 202 {
		t.Fatalf("webhook → %d %s", w.Code, w.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	testLogID := int64(created["test_log_id"].(float64))
	deadline := time.Now().Add(5 * time.Second)
	for {
		l, _ := store.FindLog(testLogID)
		if l.Status == "success" {
			break
		}
		if l.Status == "failed" || time.Now().After(deadline) {
			t.Fatalf("test deploy did not succeed (status=%s)", l.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	releaseID := int64(created["release_id"].(float64))
	attestHTTPRelease(t, store, releaseID)

	// promote 现在只建审批单，不直接部署
	w = do(t, e, "POST", "/api/v1/releases/"+strconv.FormatInt(releaseID, 10)+"/promote",
		map[string]any{"cluster_id": 2})
	if w.Code != 202 {
		t.Fatalf("promote → %d %s", w.Code, w.Body.String())
	}
	var req map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &req); err != nil {
		t.Fatal(err)
	}
	if req["status"] != "pending" || req["request_id"] == nil {
		t.Fatalf("expected pending request: %v", req)
	}
	reqID := strconv.FormatInt(int64(req["request_id"].(float64)), 10)

	// 发起人(admin session)不能自审
	w = do(t, e, "POST", "/api/v1/promotion-requests/"+reqID+"/approve", nil)
	if w.Code != 403 {
		t.Fatalf("self-approve → %d %s", w.Code, w.Body.String())
	}
	// 第二 operator 批准 → 生产部署入队
	opsToken := seedUser(t, store, "ops2", service.RoleOperator)
	w = do(t, e, "GET", "/api/v1/promotion-requests?status=pending", nil, opsToken)
	if w.Code != 200 {
		t.Fatalf("operator approval inbox → %d %s", w.Code, w.Body.String())
	}
	var scopedList []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &scopedList); err != nil {
		t.Fatal(err)
	}
	if len(scopedList) != 1 || scopedList[0]["can_approve"] != false {
		t.Fatalf("unscoped operator should see non-actionable request: %v", scopedList)
	}
	w = do(t, e, "POST", "/api/v1/promotion-requests/"+reqID+"/approve", nil, opsToken)
	if w.Code != 403 {
		t.Fatalf("unscoped operator approval → %d %s", w.Code, w.Body.String())
	}
	operator, err := store.FindUserByUsername("ops2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceDeliveryApprovalGrants(operator.ID, []service.DeliveryApprovalScope{{ApplicationID: 2, EnvironmentID: 2}}, "admin"); err != nil {
		t.Fatal(err)
	}
	w = do(t, e, "GET", "/api/v1/promotion-requests?status=pending", nil, opsToken)
	if w.Code != 200 {
		t.Fatalf("authorized operator approval inbox → %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &scopedList); err != nil {
		t.Fatal(err)
	}
	if len(scopedList) != 1 || scopedList[0]["can_approve"] != true {
		t.Fatalf("scoped operator should see actionable request: %v", scopedList)
	}
	w = do(t, e, "POST", "/api/v1/promotion-requests/"+reqID+"/approve", nil, opsToken)
	if w.Code != 202 {
		t.Fatalf("approve → %d %s", w.Code, w.Body.String())
	}
	var ap map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &ap); err != nil {
		t.Fatal(err)
	}
	if ap["prod_log_id"] == nil {
		t.Fatalf("approve must queue prod deploy: %v", ap)
	}
	// 列表能看到已批准记录
	w = do(t, e, "GET", "/api/v1/promotion-requests?status=approved", nil)
	if w.Code != 200 {
		t.Fatalf("list → %d", w.Code)
	}
	var list []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0]["approver"] != "ops2" {
		t.Fatalf("approved list wrong: %v", list)
	}
}

func (f failExecPersist) ApprovePromotion(p service.PromotionRequest, l service.DeployLog) (service.PromotionRequest, service.ImageRelease, service.DeployLog, error) {
	return p, service.ImageRelease{}, l, errors.New("atomic promotion unavailable in failing persistence")
}
func (f failExecPersist) UpdateReleaseMetadata(int64, service.ReleaseMetadata) error { return nil }
func (f failExecPersist) ClaimLog(int64) (bool, error)                               { return true, nil }

func attestHTTPRelease(t *testing.T, store *service.Store, id int64) {
	t.Helper()
	r, err := store.FindImageRelease(id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateReleaseEvidence(id, service.VerificationEvidence{Digest: *r.ImageDigest, Status: "passed", Kind: "ci", RecordedBy: "apikey:ci-test", ValidUntil: time.Now().Add(time.Hour)}, false)
	if err != nil {
		t.Fatal(err)
	}
}

func deployImageForTest(store *service.Store, id int64) string {
	r, _ := store.FindImageRelease(id)
	return service.DigestRef(r.ImageFull, *r.ImageDigest)
}

func TestInterruptedReconcileRequiresTargetAlreadyActive(t *testing.T) {
	store := cdStore()
	engine := newCDEngine(t, cdConfig(t), store)
	previous := "registry.example.com/Demo/app:mock-current"
	target := "reg.io/Demo/app@sha256:" + strings.Repeat("ab", 32)
	logRow, err := store.AddLog(service.DeployLog{
		ClusterID: 1, AppName: "app", DeploymentName: "app", ContainerName: "app",
		Namespace: "ns", DeployType: "image_deploy", Status: "interrupted", ImageFull: &target,
	})
	if err != nil {
		t.Fatal(err)
	}
	intent, _ := json.Marshal(map[string]string{"previous_image": previous, "target_image": target})
	if _, _, err := store.BeginExecutionAction(service.ExecutionAction{
		DeployLogID: logRow.ID, ActionKey: "set-image", ActionKind: "set_deployment_image", Intent: intent,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteExecutionAction(logRow.ID, "set-image", service.ExecutionActionUnknown,
		json.RawMessage(`{"readback":"failed"}`), "readback_failed"); err != nil {
		t.Fatal(err)
	}

	w := do(t, engine, "POST", fmt.Sprintf("/api/v1/deploy/logs/%d/reconcile", logRow.ID), nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("reconcile response=%d %s, want conflict while original image remains", w.Code, w.Body.String())
	}
	actual, err := store.FindLog(logRow.ID)
	if err != nil || actual.Status != "interrupted" {
		t.Fatalf("log=%+v err=%v, want interrupted", actual, err)
	}
	actions, err := store.ExecutionActions(logRow.ID)
	if err != nil || len(actions) != 1 || actions[0].Status != service.ExecutionActionNotApplied {
		t.Fatalf("actions=%+v err=%v, want not_applied", actions, err)
	}
}
