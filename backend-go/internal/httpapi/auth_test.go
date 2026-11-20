package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/config"
	"deploy-platform/backend-go/internal/service"

	"github.com/gin-gonic/gin"
)

type unavailableIdentityRepository struct {
	service.Persistence
	err error
}

func (r unavailableIdentityRepository) Users() ([]service.User, error) {
	return nil, r.err
}
func (r unavailableIdentityRepository) UserByID(int64) (service.User, error) {
	return service.User{}, r.err
}
func (r unavailableIdentityRepository) UserByUsername(string) (service.User, error) {
	return service.User{}, r.err
}
func (r unavailableIdentityRepository) APIKeys() ([]service.APIKey, error) {
	return nil, r.err
}
func (r unavailableIdentityRepository) APIKeyByID(int64) (service.APIKey, error) {
	return service.APIKey{}, r.err
}
func (r unavailableIdentityRepository) APIKeyByHash(string) (service.APIKey, error) {
	return service.APIKey{}, r.err
}

// login performs a real POST /auth/login and returns the session cookie.
func login(t *testing.T, e *gin.Engine, username, password string) (token string, code int) {
	t.Helper()
	w := do(t, e, "POST", "/api/v1/auth/login", map[string]any{
		"username": username, "password": password,
	}, "")
	if w.Code != http.StatusOK {
		return "", w.Code
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Token == "" {
		t.Fatal("login returned no token")
	}
	return body.Token, w.Code
}

func TestAuthLoginSessionFlow(t *testing.T) {
	app, e, _ := newTestApp(t)

	// Bad credentials → 401, no cookie.
	w := do(t, e, "POST", "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": "wrong-password",
	}, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("bad login → %d", w.Code)
	}

	// The seeded admin logs in (seedAdmin uses the dev default when no
	// ADMIN_INITIAL_PASSWORD is configured).
	token, code := login(t, e, "admin", "admin123")
	if code != http.StatusOK {
		t.Fatalf("admin login → %d", code)
	}

	// Cookie path and Bearer path both resolve the session.
	if w := do(t, e, "GET", "/api/v1/auth/me", nil, token); w.Code != http.StatusOK {
		t.Fatalf("/auth/me → %d", w.Code)
	}
	var me struct {
		User service.PublicUser `json:"user"`
	}
	if err := json.Unmarshal(do(t, e, "GET", "/api/v1/auth/me", nil, token).Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if me.User.Username != "admin" || me.User.Role != service.RoleAdmin {
		t.Fatalf("me = %+v", me.User)
	}

	// Logout kills the session — reuse fails.
	if w := do(t, e, "POST", "/api/v1/auth/logout", nil, token); w.Code != http.StatusOK {
		t.Fatalf("logout → %d", w.Code)
	}
	if w := do(t, e, "GET", "/api/v1/auth/me", nil, token); w.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout → %d", w.Code)
	}
	_ = app
}

func TestIdentityStorageOutageReturnsServiceUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := unavailableIdentityRepository{err: errors.New("database unavailable")}
	store := service.NewStoreWithData(nil, nil, nil, nil, repository)
	store.LoadUsers([]service.User{{ID: 1, Username: "admin", Role: service.RoleAdmin, Enabled: true}})
	store.SeedSession(testSessionToken, 1, time.Hour)
	engine := newEngine(config.Config{Env: "test"}, store, nil, nil, nil, nil, nil)

	if response := do(t, engine, "GET", "/api/v1/auth/me", nil); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("session user storage outage status=%d body=%s", response.Code, response.Body.String())
	}
	if response := do(t, engine, "POST", "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": "admin123",
	}, ""); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("login storage outage status=%d body=%s", response.Code, response.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "/openapi/v1/releases", nil)
	req.Header.Set("Authorization", "Bearer dp_test-token")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, req)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("API key storage outage status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSeedAdminRequiresExplicitPasswordOutsideMockRuntime(t *testing.T) {
	store := service.NewStore()
	if err := seedAdmin(store, config.Config{Env: "production", MockRuntime: false}); err == nil || !strings.Contains(err.Error(), "ADMIN_INITIAL_PASSWORD") {
		t.Fatalf("real runtime should reject an unset first-admin password, got err=%v", err)
	}
	if users := store.Users(); len(users) != 0 {
		t.Fatalf("failed real-runtime bootstrap must not create an admin with the public default: users=%d", len(users))
	}

	if err := seedAdmin(store, config.Config{Env: "production", MockRuntime: false, AdminInitialPassword: "e2e-admin-password"}); err != nil {
		t.Fatalf("configured real-runtime bootstrap failed: %v", err)
	}
	if users := store.Users(); len(users) != 1 || users[0].PasswordHash == "" {
		t.Fatalf("configured bootstrap should create one hashed admin, got users=%+v", users)
	}

	existingStore := service.NewStore()
	existingHash, err := service.HashPassword("existing-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	existingAdmin, err := existingStore.SaveUser(service.User{
		Username: "admin", Role: service.RoleAdmin, PasswordHash: existingHash, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := seedAdmin(existingStore, config.Config{Env: "production", MockRuntime: false}); err != nil {
		t.Fatalf("existing user database should not require an initial password: %v", err)
	}
	unchanged, err := existingStore.FindUser(existingAdmin.ID)
	if err != nil || unchanged.PasswordHash != existingHash {
		t.Fatalf("existing admin password must remain unchanged, user=%+v err=%v", unchanged, err)
	}
}

func TestAuthRequiredEverywhere(t *testing.T) {
	_, e, _ := newTestApp(t)

	// Every management route refuses anonymous requests with a uniform 401.
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/k8s/pods?cluster_id=1"},
		{"GET", "/api/v1/clusters"},
		{"GET", "/api/v1/releases"},
		{"GET", "/api/v1/auth/me"},
		{"POST", "/api/v1/deploy"},
		{"PUT", "/api/v1/users/1"},
	} {
		if w := do(t, e, tc.method, tc.path, nil, ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s unauth → %d, want 401", tc.method, tc.path, w.Code)
		}
	}
	// Bogus token is no better than none.
	if w := do(t, e, "GET", "/api/v1/clusters", nil, "bogus"); w.Code != http.StatusUnauthorized {
		t.Fatalf("bogus token → %d", w.Code)
	}
}

func TestWebhookStaysPublic(t *testing.T) {
	_, e, _ := newTestApp(t)
	// The webhook authenticates by its own secret — without a configured
	// token it must NOT be stopped by the session middleware (the Yunxiao
	// handler decides, returning its own error).
	w := do(t, e, "POST", "/api/v1/webhooks/yunxiao", map[string]any{}, "")
	if strings.Contains(w.Body.String(), "未登录") {
		t.Fatalf("webhook hit session gate: %d %s", w.Code, w.Body.String())
	}
}

func TestRoleGateViewer(t *testing.T) {
	app, e, _ := newTestApp(t)
	viewer := seedUser(t, app.store, "viewer1", service.RoleViewer)

	// Reads pass.
	if w := do(t, e, "GET", "/api/v1/clusters", nil, viewer); w.Code != http.StatusOK {
		t.Fatalf("viewer GET /clusters → %d", w.Code)
	}
	// Mutating routes refuse with 403 — and are audited as denied.
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/v1/k8s/resources/d1/scale?cluster_id=1&version=v1&resource=deployments&namespace=default"},
		{"DELETE", "/api/v1/k8s/resources/d1?cluster_id=1&version=v1&resource=deployments&namespace=default"},
		{"PUT", "/api/v1/k8s/resources/d1?cluster_id=1&version=v1&resource=deployments&namespace=default"},
		{"POST", "/api/v1/releases/1/promote"},
		{"GET", "/api/v1/users"},
	} {
		if w := do(t, e, tc.method, tc.path, map[string]any{"replicas": 1}, viewer); w.Code != http.StatusForbidden {
			t.Fatalf("viewer %s %s → %d, want 403", tc.method, tc.path, w.Code)
		}
	}
	var found bool
	for _, a := range app.store.Audits(20) {
		if a.Username == "viewer1" && a.Result == service.AuditDenied && a.Action == "role_gate" {
			found = true
		}
	}
	if !found {
		t.Fatal("role-gate denial not audited")
	}
}

func TestRoleGateOperator(t *testing.T) {
	app, e, _ := newTestApp(t)
	op := seedUser(t, app.store, "op1", service.RoleOperator)

	// Operator may write K8s but not touch admin surfaces.
	if w := do(t, e, "GET", "/api/v1/users", nil, op); w.Code != http.StatusForbidden {
		t.Fatalf("operator GET /users → %d", w.Code)
	}
	if w := do(t, e, "POST", "/api/v1/clusters", map[string]any{}, op); w.Code != http.StatusForbidden {
		t.Fatalf("operator POST /clusters → %d", w.Code)
	}
}

func TestUsersCRUD(t *testing.T) {
	_, e, _ := newTestApp(t)

	w := do(t, e, "POST", "/api/v1/users", map[string]any{
		"username": "dev1", "password": "dev1-password", "role": "operator",
		"k8s_username": "dev-one", "k8s_groups": []string{"devs"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create user → %d %s", w.Code, w.Body.String())
	}
	var u service.PublicUser
	if err := json.Unmarshal(w.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	if u.K8sUsername != "dev-one" || len(u.K8sGroups) != 1 {
		t.Fatalf("k8s identity = %+v", u)
	}
	// Password hash never leaves the API.
	if strings.Contains(w.Body.String(), "password_hash") || strings.Contains(w.Body.String(), "$2a$") {
		t.Fatal("user response leaks password hash")
	}
	// Duplicate username → 409.
	if w := do(t, e, "POST", "/api/v1/users", map[string]any{
		"username": "dev1", "password": "dev1-password", "role": "viewer",
	}); w.Code != http.StatusConflict {
		t.Fatalf("dup user → %d", w.Code)
	}
	// Short password → 400.
	if w := do(t, e, "POST", "/api/v1/users", map[string]any{
		"username": "dev2", "password": "short", "role": "viewer",
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("short password → %d", w.Code)
	}
	// Update: disable the user → their session dies.
	w = do(t, e, "PUT", "/api/v1/users/"+strconv.FormatInt(u.ID, 10), map[string]any{"enabled": false})
	if w.Code != http.StatusOK {
		t.Fatalf("disable user → %d %s", w.Code, w.Body.String())
	}
}

func TestCapabilitiesEndpoint(t *testing.T) {
	_, e, _ := newTestApp(t)
	w := do(t, e, "GET", "/api/v1/k8s/capabilities?cluster_id=1&namespace=default", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("capabilities → %d %s", w.Code, w.Body.String())
	}
	var caps service.Capabilities
	if err := json.Unmarshal(w.Body.Bytes(), &caps); err != nil {
		t.Fatal(err)
	}
	if len(caps.Resources) == 0 || !caps.Exec || !caps.Logs {
		t.Fatalf("caps = %+v", caps)
	}
}

func TestAuditRecordedOnWrite(t *testing.T) {
	app, e, _ := newTestApp(t)
	if w := do(t, e, "POST", "/api/v1/k8s/resources/d1/scale?cluster_id=1&group=apps&version=v1&resource=deployments&namespace=default",
		map[string]any{"replicas": 3}); w.Code != http.StatusOK {
		t.Fatalf("scale → %d %s", w.Code, w.Body.String())
	}
	var found bool
	for _, a := range app.store.Audits(20) {
		if a.Action == "scale" && a.Username == "admin" && a.Name == "d1" && a.Result == service.AuditSuccess {
			found = true
		}
	}
	if !found {
		t.Fatalf("scale audit missing: %+v", app.store.Audits(5))
	}
}

func TestSessionExpiry(t *testing.T) {
	app, e, _ := newTestApp(t)
	u, err := app.store.FindUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	app.store.SeedSession("expired-token", u.ID, -time.Hour)
	if w := do(t, e, "GET", "/api/v1/clusters", nil, "expired-token"); w.Code != http.StatusUnauthorized {
		t.Fatalf("expired session → %d", w.Code)
	}
}
