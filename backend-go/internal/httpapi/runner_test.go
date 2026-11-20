package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"deploy-platform/backend-go/internal/service"
)

type fakeDeploymentPipelineInspector struct {
	info         service.YunxiaoDeploymentPipelineInfo
	connectionID int64
	pipelineID   string
	calls        int
	err          error
}

func (f *fakeDeploymentPipelineInspector) InspectDeploymentPipeline(_ context.Context, connectionID int64, pipelineID string) (service.YunxiaoDeploymentPipelineInfo, error) {
	f.calls++
	f.connectionID = connectionID
	f.pipelineID = pipelineID
	return f.info, f.err
}

type fakeDeploymentPipelineScopeInspector struct {
	report       service.YunxiaoDeploymentPipelineScopeReport
	connectionID int64
	pipelineID   string
	calls        int
	err          error
}

func (f *fakeDeploymentPipelineScopeInspector) InspectDeploymentPipelineScope(_ context.Context, connectionID int64, pipelineID string) (service.YunxiaoDeploymentPipelineScopeReport, error) {
	f.calls++
	f.connectionID = connectionID
	f.pipelineID = pipelineID
	return f.report, f.err
}

type fakeDeploymentPipelineSearcher struct {
	result       service.YunxiaoPipelineSearchResult
	connectionID int64
	name         string
	page         int
	calls        int
	err          error
}

func (f *fakeDeploymentPipelineSearcher) SearchDeploymentPipelines(_ context.Context, connectionID int64, name string, page int) (service.YunxiaoPipelineSearchResult, error) {
	f.calls++
	f.connectionID = connectionID
	f.name = name
	f.page = page
	return f.result, f.err
}

func configureTestRunner(t *testing.T, app *Application, base string) (service.RunnerBinding, string, service.RunnerScripts) {
	return configureTestRunnerWithConfig(t, app, base, "", "")
}

func configureTestRunnerWithConfig(t *testing.T, app *Application, base, configRepository, configPath string) (service.RunnerBinding, string, service.RunnerScripts) {
	t.Helper()
	a, err := app.store.SaveApp(service.App{ClusterID: 1, AppName: "orders", MatchKeyword: "orders", Namespace: "default", DeploymentName: "mock-deployments-1", ContainerName: "app", ImageRepository: "registry.example.com/orders/api", DeploymentConfigRepoURL: configRepository, DeploymentConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	w := do(t, app.Engine, "POST", "/api/v1/runner/bindings", map[string]any{"app_id": a.ID, "pipeline_id": "123", "private_cluster_id": "build-1", "platform_url": base, "connection_id": 0})
	if w.Code != 200 {
		t.Fatalf("configure %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Binding service.RunnerBinding `json:"binding"`
		Key     string                `json:"key"`
		Scripts service.RunnerScripts `json:"scripts"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Binding, out.Key, out.Scripts
}
func TestRunnerMachineCheckScopeAndNoDeploymentSideEffects(t *testing.T) {
	app, _, _ := newTestApp(t)
	b, key, _ := configureTestRunner(t, app, "http://localhost:8000")
	path := fmt.Sprintf("/openapi/v1/runner/bindings/%d/check", b.ID)
	for _, credential := range []string{"", key + "x", "dp_fake"} {
		if w := doKey(t, app.Engine, "POST", path, map[string]string{"pipeline_id": "123"}, credential); w.Code != 401 {
			t.Fatalf("bad auth %d", w.Code)
		}
	}
	if w := doKey(t, app.Engine, "POST", path, map[string]string{"pipeline_id": "999"}, key); w.Code != 403 {
		t.Fatalf("wrong pipeline %d", w.Code)
	}
	if w := doKey(t, app.Engine, "POST", path, map[string]string{"pipeline_id": "123", "target_cluster": "prod"}, key); w.Code != 400 {
		t.Fatalf("routing override accepted %d", w.Code)
	}
	if w := doKey(t, app.Engine, "POST", path, map[string]string{"pipeline_id": "123"}, key); w.Code != 200 || !strings.Contains(w.Body.String(), `"deploy_created":false`) {
		t.Fatalf("check %d %s", w.Code, w.Body.String())
	}
	if len(app.store.Logs(0, "", "", 0)) != 0 || len(app.store.ImageReleases("", 0)) != 0 {
		t.Fatal("check deployed an image")
	}
	stored, _ := app.store.RunnerBinding(b.ID)
	if stored.LastCheckAt == nil {
		t.Fatal("check not recorded")
	}
	w := do(t, app.Engine, "GET", "/api/v1/runner/bindings", nil)
	if strings.Contains(w.Body.String(), key) || strings.Contains(w.Body.String(), service.HashAPIKey(key)) {
		t.Fatal("credential leaked on list")
	}
	viewer := seedUser(t, app.store, "runner-viewer", service.RoleViewer)
	if w = do(t, app.Engine, "PUT", fmt.Sprintf("/api/v1/runner/bindings/%d/enabled", b.ID), map[string]bool{"enabled": false}, viewer); w.Code != 403 {
		t.Fatal("viewer changed CI authorization")
	}
}

func TestRunnerConnectionMetadataIsOperatorReadableAndNeverReturnsSecret(t *testing.T) {
	app, _, _ := newTestApp(t)
	app.store.LoadYunxiaoConnections([]service.YunxiaoConnection{{
		ID: 42, Name: "production", Edition: "central", Domain: "openapi-rdc.aliyuncs.com",
		OrganizationID: "org-1", TokenCiphertext: "encrypted-yunxiao-token-secret",
	}})
	operator := seedUser(t, app.store, "pipeline-binding-operator", service.RoleOperator)
	response := do(t, app.Engine, "GET", "/api/v1/runner/connections", nil, operator)
	if response.Code != 200 {
		t.Fatalf("operator connection metadata: %d %s", response.Code, response.Body.String())
	}
	var connections []service.YunxiaoConnection
	if err := json.Unmarshal(response.Body.Bytes(), &connections); err != nil || len(connections) != 1 || connections[0].ID != 42 || connections[0].OrganizationID != "org-1" {
		t.Fatalf("connection metadata=%+v err=%v", connections, err)
	}
	if strings.Contains(response.Body.String(), "encrypted-yunxiao-token-secret") || strings.Contains(response.Body.String(), "token_ciphertext") {
		t.Fatalf("connection endpoint exposed credential data: %s", response.Body.String())
	}
	viewer := seedUser(t, app.store, "pipeline-binding-viewer", service.RoleViewer)
	if response = do(t, app.Engine, "GET", "/api/v1/runner/connections", nil, viewer); response.Code != 403 {
		t.Fatalf("viewer connection metadata: %d %s", response.Code, response.Body.String())
	}
}

func TestDeploymentPipelineCheckIsReadOnlyAndOperatorScoped(t *testing.T) {
	store := service.NewStore()
	gin.SetMode(gin.TestMode)
	seedTestAuth(t, store)
	engine := gin.New()
	inspector := &fakeDeploymentPipelineInspector{info: service.YunxiaoDeploymentPipelineInfo{
		PipelineID: "123456", Name: "orders-config", Type: "PIPELINEASCODE", UpdateTime: 1770000000000,
	}}
	scopeInspector := &fakeDeploymentPipelineScopeInspector{report: service.YunxiaoDeploymentPipelineScopeReport{
		PipelineID: "123456", Name: "orders-config", Type: "PIPELINEASCODE", UpdateTime: 1770000000000,
		AnalysisStatus: "manual_review_required", ReviewRequired: true,
		Findings:        []service.YunxiaoPipelineScopeFinding{{NodeRef: "流水线 / 阶段 1 / 任务 1 / 步骤 1", Category: "possible_config_write", Summary: "检测到 ConfigMap/Secret 相关写入标记。"}},
		UnresolvedNodes: []string{"流水线 / 阶段 1 / 任务 1 / 步骤 1"},
		Warning:         "静态检查无法证明流水线完整写入范围。",
	}}
	ops := engine.Group("/api/v1/runner", authRequired(store), requireRole(store, service.RoleOperator))
	searcher := &fakeDeploymentPipelineSearcher{result: service.YunxiaoPipelineSearchResult{
		Items: []service.YunxiaoPipelineSummary{{PipelineID: 123456, Name: "orders-config"}},
		Page:  1, PerPage: 30, Total: 1, TotalPages: 1,
	}}
	registerDeploymentPipelineSearchAPI(ops, store, searcher)
	registerDeploymentPipelineCheckAPI(ops, store, inspector)
	registerDeploymentPipelineScopeCheckAPI(ops, store, scopeInspector)
	searchBody := map[string]any{"connection_id": 9, "name": "orders", "page": 1}
	response := do(t, engine, http.MethodPost, "/api/v1/runner/deployment-pipelines/search", searchBody)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"pipeline_id":123456`) || !strings.Contains(response.Body.String(), `"name":"orders-config"`) {
		t.Fatalf("pipeline search: %d %s", response.Code, response.Body.String())
	}
	if searcher.calls != 1 || searcher.connectionID != 9 || searcher.name != "orders" || searcher.page != 1 {
		t.Fatalf("search request connection=%d name=%q page=%d calls=%d", searcher.connectionID, searcher.name, searcher.page, searcher.calls)
	}
	if response = do(t, engine, http.MethodPost, "/api/v1/runner/deployment-pipelines/search", map[string]any{"connection_id": 9, "name": "orders", "token": "secret"}); response.Code != http.StatusBadRequest {
		t.Fatalf("unsupported credential field accepted in search: %d %s", response.Code, response.Body.String())
	}

	body := map[string]any{"connection_id": 9, "pipeline_id": "123456"}
	response = do(t, engine, http.MethodPost, "/api/v1/runner/deployment-pipeline-check", body)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"name":"orders-config"`) || !strings.Contains(response.Body.String(), `"type":"PIPELINEASCODE"`) {
		t.Fatalf("pipeline metadata check: %d %s", response.Code, response.Body.String())
	}
	if inspector.calls != 1 || inspector.connectionID != 9 || inspector.pipelineID != "123456" {
		t.Fatalf("inspector call connection=%d pipeline=%q calls=%d", inspector.connectionID, inspector.pipelineID, inspector.calls)
	}
	if strings.Contains(response.Body.String(), "content") || strings.Contains(response.Body.String(), "flow") || strings.Contains(response.Body.String(), "token") {
		t.Fatalf("pipeline definition or credential leaked: %s", response.Body.String())
	}
	if response = do(t, engine, http.MethodPost, "/api/v1/runner/deployment-pipeline-check", map[string]any{"connection_id": 9, "pipeline_id": "123456", "token": "secret"}); response.Code != http.StatusBadRequest {
		t.Fatalf("unsupported credential field accepted: %d %s", response.Code, response.Body.String())
	}
	response = do(t, engine, http.MethodPost, "/api/v1/runner/deployment-pipeline-scope-check", body)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"analysis_status":"manual_review_required"`) || !strings.Contains(response.Body.String(), `"category":"possible_config_write"`) || !strings.Contains(response.Body.String(), `"review_required":true`) {
		t.Fatalf("pipeline scope check: %d %s", response.Code, response.Body.String())
	}
	if scopeInspector.calls != 1 || scopeInspector.connectionID != 9 || scopeInspector.pipelineID != "123456" {
		t.Fatalf("scope inspector call connection=%d pipeline=%q calls=%d", scopeInspector.connectionID, scopeInspector.pipelineID, scopeInspector.calls)
	}
	for _, forbidden := range []string{"pipelineConfig", "content", "command:", "token", "script"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("raw definition/credential field %q leaked: %s", forbidden, response.Body.String())
		}
	}
	if response = do(t, engine, http.MethodPost, "/api/v1/runner/deployment-pipeline-scope-check", map[string]any{"connection_id": 9, "pipeline_id": "123456", "token": "secret"}); response.Code != http.StatusBadRequest {
		t.Fatalf("unsupported credential field accepted in scope check: %d %s", response.Code, response.Body.String())
	}
	viewer := seedUser(t, store, "pipeline-check-viewer", service.RoleViewer)
	if response = do(t, engine, http.MethodPost, "/api/v1/runner/deployment-pipelines/search", searchBody, viewer); response.Code != http.StatusForbidden {
		t.Fatalf("viewer may not search configured cloud connections: %d %s", response.Code, response.Body.String())
	}
	if response = do(t, engine, http.MethodPost, "/api/v1/runner/deployment-pipeline-check", body, viewer); response.Code != http.StatusForbidden {
		t.Fatalf("viewer may not check configured cloud connections: %d %s", response.Code, response.Body.String())
	}
	if response = do(t, engine, http.MethodPost, "/api/v1/runner/deployment-pipeline-scope-check", body, viewer); response.Code != http.StatusForbidden {
		t.Fatalf("viewer may not inspect configured cloud pipeline definitions: %d %s", response.Code, response.Body.String())
	}
	if response = do(t, engine, http.MethodPost, "/api/v1/runner/deployment-pipeline-check", body, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous pipeline check: %d %s", response.Code, response.Body.String())
	}
	if response = do(t, engine, http.MethodPost, "/api/v1/runner/deployment-pipeline-scope-check", body, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous pipeline scope check: %d %s", response.Code, response.Body.String())
	}
}

func TestDeploymentPipelineScopeReviewPersistsOnlyForExactCheckedDefinition(t *testing.T) {
	store := service.NewStore()
	seedTestAuth(t, store)
	hash := strings.Repeat("a", 64)
	store.LoadDeliveryModel(service.DeliveryModel{Bindings: []service.DeliveryBinding{{
		ID: 41, AppID: 2,
		DeploymentPipeline: &service.YunxiaoDeploymentPipelineBinding{ConnectionID: 9, PipelineID: "123456"},
	}}})
	engine := gin.New()
	inspector := &fakeDeploymentPipelineScopeInspector{report: service.YunxiaoDeploymentPipelineScopeReport{
		PipelineID: "123456", DefinitionHash: hash,
		AnalysisStatus: "manual_review_required", ReviewRequired: true,
	}}
	ops := engine.Group("/api/v1/runner", authRequired(store), requireRole(store, service.RoleOperator))
	registerDeploymentPipelineScopeReviewAPI(ops, store, inspector)
	body := map[string]any{
		"app_id": 2, "connection_id": 9, "pipeline_id": "123456",
		"definition_hash": hash, "write_scope": service.PipelineScopeConfigurationOnly,
		"acknowledged": true,
	}
	response := do(t, engine, http.MethodPost, "/api/v1/runner/deployment-pipeline-scope-review", body)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"execution_enabled":false`) || !strings.Contains(response.Body.String(), `"write_scope":"configuration_only"`) {
		t.Fatalf("scope review response: %d %s", response.Code, response.Body.String())
	}
	if inspector.calls != 1 || inspector.connectionID != 9 || inspector.pipelineID != "123456" {
		t.Fatalf("review did not re-read the bound definition: %+v", inspector)
	}
	model := store.DeliveryModel()
	if len(model.PipelineScopeReviews) != 1 || model.PipelineScopeReviews[0].BindingID != 41 || model.PipelineScopeReviews[0].DefinitionHash != hash || model.PipelineScopeReviews[0].Reviewer == "" {
		t.Fatalf("review was not surfaced with its binding and reviewer: %+v", model.PipelineScopeReviews)
	}

	inspector.report.DefinitionHash = strings.Repeat("b", 64)
	response = do(t, engine, http.MethodPost, "/api/v1/runner/deployment-pipeline-scope-review", body)
	if response.Code != http.StatusConflict || len(store.DeliveryModel().PipelineScopeReviews) != 1 {
		t.Fatalf("changed definition must not create a current review: %d %s %+v", response.Code, response.Body.String(), store.DeliveryModel().PipelineScopeReviews)
	}
	viewer := seedUser(t, store, "pipeline-scope-review-viewer", service.RoleViewer)
	if response = do(t, engine, http.MethodPost, "/api/v1/runner/deployment-pipeline-scope-review", body, viewer); response.Code != http.StatusForbidden {
		t.Fatalf("viewer may not attest to pipeline write scope: %d %s", response.Code, response.Body.String())
	}
	inspector.report.DefinitionHash = hash
	body["acknowledged"] = false
	if response = do(t, engine, http.MethodPost, "/api/v1/runner/deployment-pipeline-scope-review", body); response.Code != http.StatusBadRequest {
		t.Fatalf("missing full-definition acknowledgement accepted: %d %s", response.Code, response.Body.String())
	}
}

func TestRunnerEventRequiresDigestAndRejectsWrongRepository(t *testing.T) {
	app, _, _ := newTestApp(t)
	b, key, _ := configureTestRunner(t, app, "http://localhost:8000")
	path := fmt.Sprintf("/openapi/v1/runner/bindings/%d/events", b.ID)
	body := map[string]string{"pipeline_id": "123", "build_number": "1", "image_full": "registry.example.com/other/api:v1", "image_digest": "sha256:" + strings.Repeat("a", 64)}
	if w := doKey(t, app.Engine, "POST", path, body, key); w.Code != 400 {
		t.Fatalf("wrong repo %d %s", w.Code, w.Body.String())
	}
	body["image_full"] = "registry.example.com/orders/api:v1"
	body["image_digest"] = ""
	if w := doKey(t, app.Engine, "POST", path, body, key); w.Code != 400 {
		t.Fatal("missing digest accepted")
	}
	if len(app.store.Logs(0, "", "", 0)) != 0 {
		t.Fatal("invalid event created task")
	}
	body["image_digest"] = "sha256:" + strings.Repeat("a", 64)
	w := doKey(t, app.Engine, "POST", path, body, key)
	if w.Code != 202 {
		t.Fatalf("event %d %s", w.Code, w.Body.String())
	}
	var result struct {
		ReleaseID int64 `json:"release_id"`
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	if w = doKey(t, app.Engine, "POST", path, body, key); w.Code != 200 || !strings.Contains(w.Body.String(), `"duplicate":true`) {
		t.Fatalf("duplicate %d %s", w.Code, w.Body.String())
	}
	if len(app.store.ImageReleases("", 0)) != 1 {
		t.Fatal("duplicate release created")
	}
	if w = doKey(t, app.Engine, "GET", fmt.Sprintf("/openapi/v1/runner/bindings/%d/releases/%d", b.ID, result.ReleaseID), nil, key); w.Code != 200 {
		t.Fatalf("own result %d", w.Code)
	}
	if w = doKey(t, app.Engine, "GET", fmt.Sprintf("/openapi/v1/runner/bindings/%d/releases/999999", b.ID), nil, key); w.Code != 404 {
		t.Fatal("unrelated result exposed")
	}
	if w = doKey(t, app.Engine, "GET", "/openapi/v1/releases", nil, key); w.Code != 401 {
		t.Fatal("runner credential accessed broad API")
	}
	body["image_full"] = "registry.example.com/orders/api:rewritten"
	if w = doKey(t, app.Engine, "POST", path, body, key); w.Code != 409 {
		t.Fatalf("rewritten same-build event %d %s", w.Code, w.Body.String())
	}
}

func TestRunnerCanReportOwnCIEvidenceAndSmokeAfterSuccessfulTestRollout(t *testing.T) {
	app, _, _ := newTestApp(t)
	binding, key, _ := configureTestRunner(t, app, "http://localhost:8000")
	binding, _ = app.store.RunnerBinding(binding.ID)
	digest := "sha256:" + strings.Repeat("c", 64)
	pipeline, build := binding.PipelineID, "42"
	release, log, created, err := app.store.CreateImageRelease(service.ImageRelease{
		Source: fmt.Sprintf("runner:%d", binding.ID), EventKey: "build42",
		EventID: fmt.Sprintf("%s:42:%d", binding.PipelineID, binding.ID),
		AppName: binding.Binding.AppName, TestClusterID: binding.Binding.ClusterID,
		ImageFull: "registry.example.com/orders/api:v42", ImageDigest: &digest,
		PipelineID: &pipeline, BuildNumber: &build,
		RunnerAuthorizationHash: binding.TokenHash,
		Metadata:                service.ReleaseMetadata{Binding: &binding.Binding},
	}, service.DeployLog{
		ClusterID: binding.Binding.ClusterID, AppName: binding.Binding.AppName,
		Namespace: binding.Binding.Namespace, DeploymentName: binding.Binding.DeploymentName,
		ContainerName: binding.Binding.ContainerName, Status: "running", DeployType: "image_deploy",
	})
	if err != nil || !created {
		t.Fatalf("create Runner release: created=%v err=%v", created, err)
	}
	path := fmt.Sprintf("/openapi/v1/runner/bindings/%d/releases/%d/verification", binding.ID, release.ID)
	body := map[string]any{
		"kind": "ci", "status": "passed", "summary": "unit tests passed",
		"report_url":  "https://ci.example.com/build/42",
		"valid_until": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	tampered := map[string]any{}
	for field, value := range body {
		tampered[field] = value
	}
	tampered["digest"] = "sha256:" + strings.Repeat("0", 64)
	if w := doKey(t, app.Engine, "POST", path, tampered, key); w.Code != 400 {
		t.Fatalf("Runner could override the server-derived digest: %d %s", w.Code, w.Body.String())
	}
	w := doKey(t, app.Engine, "POST", path, body, key)
	if w.Code != 200 {
		t.Fatalf("CI evidence %d %s", w.Code, w.Body.String())
	}
	var metadata service.ReleaseMetadata
	if err := json.Unmarshal(w.Body.Bytes(), &metadata); err != nil || metadata.Verification == nil {
		t.Fatalf("CI evidence response: metadata=%+v err=%v", metadata, err)
	}
	if metadata.Verification.RecordedBy != fmt.Sprintf("runner:%d", binding.ID) || metadata.Verification.Digest != digest {
		t.Fatalf("server did not bind identity and digest: %+v", metadata.Verification)
	}
	body["kind"] = "smoke"
	if w = doKey(t, app.Engine, "POST", path, body, key); w.Code != 400 || !strings.Contains(w.Body.String(), "部署成功后") {
		t.Fatalf("Smoke evidence accepted before rollout success: %d %s", w.Code, w.Body.String())
	}
	body["kind"] = "manual"
	if w = doKey(t, app.Engine, "POST", path, body, key); w.Code != 400 {
		t.Fatalf("Runner submitted manual evidence: %d %s", w.Code, w.Body.String())
	}
	log.Status = "success"
	if err := app.store.UpdateLogExecution(log); err != nil {
		t.Fatal(err)
	}
	body["kind"] = "smoke"
	if w = doKey(t, app.Engine, "POST", path, body, key); w.Code != 200 {
		t.Fatalf("Smoke evidence after rollout success %d %s", w.Code, w.Body.String())
	}
	if w = doKey(t, app.Engine, "POST", fmt.Sprintf("/openapi/v1/runner/bindings/%d/releases/999999/verification", binding.ID), body, key); w.Code != 404 {
		t.Fatalf("unrelated release was accessible: %d %s", w.Code, w.Body.String())
	}
}

func TestRunnerCanReportOwnScanWithDigestAndScannerVersionsPinned(t *testing.T) {
	app, _, _ := newTestApp(t)
	binding, key, _ := configureTestRunner(t, app, "http://localhost:8000")
	binding, _ = app.store.RunnerBinding(binding.ID)
	digest := "sha256:" + strings.Repeat("d", 64)
	pipeline, build := binding.PipelineID, "7"
	release, _, _, err := app.store.CreateImageRelease(service.ImageRelease{
		Source: fmt.Sprintf("runner:%d", binding.ID), EventKey: "scan-build-7",
		EventID: fmt.Sprintf("%s:%s:%d", binding.PipelineID, build, binding.ID),
		AppName: binding.Binding.AppName, TestClusterID: binding.Binding.ClusterID,
		ImageFull: "registry.example.com/orders/api:v7", ImageDigest: &digest,
		PipelineID: &pipeline, BuildNumber: &build,
		RunnerAuthorizationHash: binding.TokenHash,
		Metadata:                service.ReleaseMetadata{Binding: &binding.Binding},
	}, service.DeployLog{
		ClusterID: binding.Binding.ClusterID, AppName: binding.Binding.AppName,
		Namespace: binding.Binding.Namespace, DeploymentName: binding.Binding.DeploymentName,
		ContainerName: binding.Binding.ContainerName, Status: "running", DeployType: "image_deploy",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/openapi/v1/runner/bindings/%d/releases/%d/scan", binding.ID, release.ID)
	body := map[string]any{
		"status": "passed", "tool": "Trivy", "tool_version": "0.57.1",
		"rules_version": "db-2026-10-12", "summary": "no critical vulnerabilities",
		"report_url":  "https://ci.example.com/scan/7",
		"valid_until": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	}
	w := doKey(t, app.Engine, "POST", path, body, key)
	if w.Code != 200 {
		t.Fatalf("scan report %d %s", w.Code, w.Body.String())
	}
	w = doKey(t, app.Engine, "POST", path, body, key)
	if w.Code != 200 {
		t.Fatalf("scan retry %d %s", w.Code, w.Body.String())
	}
	updated := app.store.ImageReleases("", 0)[0]
	if len(updated.Metadata.ScanHistory) != 1 || updated.Metadata.Scan == nil {
		t.Fatalf("identical scan retry duplicated report: %+v", updated.Metadata.ScanHistory)
	}
	scan := updated.Metadata.Scan
	if scan.Digest != digest || scan.RecordedBy != fmt.Sprintf("runner:%d", binding.ID) || scan.Tool != "Trivy" || scan.ToolVersion != "0.57.1" || scan.RulesVersion != "db-2026-10-12" {
		t.Fatalf("scan identity or tool versions not bound to report: %+v", scan)
	}
	if len(updated.Metadata.VerificationHistory) != 0 {
		t.Fatal("scan report polluted the CI/Smoke verification history")
	}
	delete(body, "rules_version")
	if w = doKey(t, app.Engine, "POST", path, body, key); w.Code != 400 {
		t.Fatalf("scan without rule version accepted: %d %s", w.Code, w.Body.String())
	}
}

func TestRunnerConfigurationReportIsValidatedAndImmutable(t *testing.T) {
	app, _, _ := newTestApp(t)
	binding, key, _ := configureTestRunner(t, app, "http://localhost:8000")
	event := map[string]any{
		"pipeline_id": binding.PipelineID, "build_number": "43",
		"image_full":       "registry.example.com/orders/api:v1",
		"image_digest":     "sha256:" + strings.Repeat("d", 64),
		"commit_id":        strings.Repeat("1", 40),
		"commit_timestamp": "2026-10-15T09:30:00+08:00",
	}
	eventPath := fmt.Sprintf("/openapi/v1/runner/bindings/%d/events", binding.ID)
	w := doKey(t, app.Engine, "POST", eventPath, event, key)
	if w.Code != 202 {
		t.Fatalf("event %d %s", w.Code, w.Body.String())
	}
	var result struct {
		ReleaseID int64 `json:"release_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	release, err := app.store.FindImageRelease(result.ReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	wantCommitTime := time.Date(2026, 10, 15, 1, 30, 0, 0, time.UTC)
	if release.Metadata.CommitTimestamp == nil || !release.Metadata.CommitTimestamp.Equal(wantCommitTime) {
		t.Fatalf("Runner commit time was not persisted in UTC: %+v", release.Metadata.CommitTimestamp)
	}
	path := fmt.Sprintf("/openapi/v1/runner/bindings/%d/releases/%d/configuration", binding.ID, result.ReleaseID)
	body := map[string]any{
		"repository_url": "https://git.example.com/orders/deploy-config.git",
		"path":           "charts/orders",
		"revision":       strings.Repeat("a", 40),
	}
	if w = doKey(t, app.Engine, "POST", path, body, key); w.Code != 201 {
		t.Fatalf("configuration report %d %s", w.Code, w.Body.String())
	}
	if w = doKey(t, app.Engine, "POST", path, body, key); w.Code != 200 {
		t.Fatalf("idempotent configuration report %d %s", w.Code, w.Body.String())
	}
	body["revision"] = strings.Repeat("b", 40)
	if w = doKey(t, app.Engine, "POST", path, body, key); w.Code != 409 {
		t.Fatalf("different revision overwrote pinned source: %d %s", w.Code, w.Body.String())
	}
	body["revision"] = "short"
	if w = doKey(t, app.Engine, "POST", path, body, key); w.Code != 400 {
		t.Fatalf("short revision accepted: %d %s", w.Code, w.Body.String())
	}
	body["revision"] = strings.Repeat("a", 40)
	body["repository_url"] = "https://user:secret@git.example.com/orders/config.git"
	if w = doKey(t, app.Engine, "POST", path, body, key); w.Code != 400 {
		t.Fatalf("credential-bearing repository accepted: %d %s", w.Code, w.Body.String())
	}
	if w = doKey(t, app.Engine, "POST", fmt.Sprintf("/openapi/v1/runner/bindings/%d/releases/999999/configuration", binding.ID), body, key); w.Code != 404 {
		t.Fatalf("unrelated release exposed: %d %s", w.Code, w.Body.String())
	}
	if w = doKey(t, app.Engine, "POST", path, body, ""); w.Code != 401 {
		t.Fatalf("configuration report accepted without runner token: %d %s", w.Code, w.Body.String())
	}
}

func TestGeneratedRunnerScriptsExecuteAgainstActualAPI(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python 3 required")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl required")
	}
	app, _, dir := newTestApp(t)
	var events atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/events") && events.Add(1) == 1 {
			w.WriteHeader(503)
			w.Write([]byte(`{"error":"temporary outage"}`))
			return
		}
		app.Engine.ServeHTTP(w, r)
	}))
	defer server.Close()
	const configRepository = "https://git.example.com/orders/deploy-config.git"
	binding, key, scripts := configureTestRunnerWithConfig(t, app, server.URL, configRepository, "charts/orders")
	repoDir := filepath.Join(dir, "config-checkout")
	configDir := filepath.Join(repoDir, "charts/orders")
	valuesPath := filepath.Join(configDir, "values.yaml")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(valuesPath, []byte("replicaCount: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", repoDir}, {"-C", repoDir, "config", "user.email", "runner-test@example.com"}, {"-C", repoDir, "config", "user.name", "Runner Test"}, {"-C", repoDir, "add", "."}, {"-C", repoDir, "commit", "-qm", "config"}, {"-C", repoDir, "remote", "add", "origin", configRepository}} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("prepare config checkout: %v %s", err, output)
		}
	}
	revisionBytes, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	revision := strings.TrimSpace(string(revisionBytes))
	run := func(script string, extra ...string) {
		t.Helper()
		filename := filepath.Join(dir, "notify.sh")
		if err := os.WriteFile(filename, []byte(script), 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", filename)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), scripts.SecretVariable+"="+key, "PIPELINE_ID=123", "BUILD_NUMBER=1", "DEPLOY_IMAGE_FULL=registry.example.com/orders/api:v1", "DEPLOY_IMAGE_DIGEST=sha256:"+strings.Repeat("a", 64), "CI_COMMIT_TIMESTAMP=2026-10-15T09:30:00+08:00", "DEPLOY_CONFIG_WORKDIR="+repoDir, "DEPLOY_WAIT=0")
		cmd.Env = append(cmd.Env, extra...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("generated script failed: %v %s", err, out)
		}
		if strings.Contains(string(out), key) {
			t.Fatal("script logged credential")
		}
	}
	badOrigin := exec.Command("git", "-C", repoDir, "remote", "set-url", "origin", "https://git.example.com/orders/other.git")
	if output, err := badOrigin.CombinedOutput(); err != nil {
		t.Fatalf("set bad config origin: %v %s", err, output)
	}
	failed := exec.Command("bash", filepath.Join(dir, "notify.sh"))
	if err := os.WriteFile(filepath.Join(dir, "notify.sh"), []byte(scripts.Script), 0600); err != nil {
		t.Fatal(err)
	}
	failed.Dir = dir
	failed.Env = append(os.Environ(), scripts.SecretVariable+"="+key, "PIPELINE_ID=123", "BUILD_NUMBER=1", "DEPLOY_IMAGE_FULL=registry.example.com/orders/api:v1", "DEPLOY_IMAGE_DIGEST=sha256:"+strings.Repeat("a", 64), "DEPLOY_CONFIG_WORKDIR="+repoDir, "DEPLOY_WAIT=0")
	if output, err := failed.CombinedOutput(); err == nil || !strings.Contains(string(output), "配置 checkout 的 origin 与组件登记仓库不一致") {
		t.Fatalf("wrong checkout origin was not rejected: err=%v output=%s", err, output)
	}
	if events.Load() != 0 {
		t.Fatal("invalid config checkout created a release event")
	}
	if output, err := exec.Command("git", "-C", repoDir, "remote", "set-url", "origin", configRepository).CombinedOutput(); err != nil {
		t.Fatalf("restore config origin: %v %s", err, output)
	}
	failed = exec.Command("bash", filepath.Join(dir, "notify.sh"))
	failed.Dir = dir
	failed.Env = append(os.Environ(), scripts.SecretVariable+"="+key, "PIPELINE_ID=123", "BUILD_NUMBER=1", "DEPLOY_IMAGE_FULL=registry.example.com/orders/api:v1", "DEPLOY_IMAGE_DIGEST=sha256:"+strings.Repeat("a", 64), "DEPLOY_CONFIG_WORKDIR="+repoDir, "DEPLOY_CONFIG_REVISION="+strings.Repeat("b", 40), "DEPLOY_WAIT=0")
	if output, err := failed.CombinedOutput(); err == nil || !strings.Contains(string(output), "DEPLOY_CONFIG_REVISION 与实际 checkout HEAD 不一致") {
		t.Fatalf("revision mismatch was not rejected: err=%v output=%s", err, output)
	}
	if events.Load() != 0 {
		t.Fatal("mismatched config revision created a release event")
	}
	if err := os.WriteFile(valuesPath, []byte("replicaCount: 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	failed = exec.Command("bash", filepath.Join(dir, "notify.sh"))
	failed.Dir = dir
	failed.Env = append(os.Environ(), scripts.SecretVariable+"="+key, "PIPELINE_ID=123", "BUILD_NUMBER=1", "DEPLOY_IMAGE_FULL=registry.example.com/orders/api:v1", "DEPLOY_IMAGE_DIGEST=sha256:"+strings.Repeat("a", 64), "DEPLOY_CONFIG_WORKDIR="+repoDir, "DEPLOY_WAIT=0")
	if output, err := failed.CombinedOutput(); err == nil || !strings.Contains(string(output), "配置目录存在未提交或忽略文件") {
		t.Fatalf("dirty config checkout was not rejected: err=%v output=%s", err, output)
	}
	if events.Load() != 0 {
		t.Fatal("dirty config checkout created a release event")
	}
	if err := os.WriteFile(valuesPath, []byte("replicaCount: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run(scripts.CheckScript)
	if len(app.store.ImageReleases("", 0)) != 0 {
		t.Fatal("check script published")
	}
	run(scripts.Script)
	run(scripts.Script)
	if events.Load() != 3 {
		t.Fatal("transient failure was not retried")
	}
	if len(app.store.ImageReleases("", 0)) != 1 {
		t.Fatal("script retry not idempotent")
	}
	releases := app.store.ImageReleases("", 0)
	release := releases[0]
	if config := release.Metadata.ConfigurationSource; config == nil || config.Repository != configRepository || config.Path != "charts/orders" || config.Revision != revision || config.RecordedBy != fmt.Sprintf("runner:%d", binding.ID) {
		t.Fatalf("notification script did not report the immutable configuration source: %+v", config)
	}
	wantCommitTime := time.Date(2026, 10, 15, 1, 30, 0, 0, time.UTC)
	if release.Metadata.CommitTimestamp == nil || !release.Metadata.CommitTimestamp.Equal(wantCommitTime) {
		t.Fatalf("notification script did not report the Git commit timestamp: %+v", release.Metadata.CommitTimestamp)
	}
	releaseIDBytes, err := os.ReadFile(filepath.Join(dir, scripts.ReleaseIDFile))
	if err != nil || string(releaseIDBytes) != fmt.Sprintf("%d\n", release.ID) {
		t.Fatalf("notification script did not write release ID handoff file: %q, %v", releaseIDBytes, err)
	}
	log, err := app.store.FindLog(release.TestDeployLogID)
	if err != nil {
		t.Fatal(err)
	}
	log.Status = "success"
	if err := app.store.UpdateLogExecution(log); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(dir, "verify.sh")
	if err := os.WriteFile(filename, []byte(scripts.VerificationScript), 0600); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"ci", "smoke", "scan"} {
		cmd := exec.Command("bash", filename)
		cmd.Dir = dir
		status := "passed"
		if kind == "scan" {
			status = "failed"
		}
		cmd.Env = append(os.Environ(), scripts.SecretVariable+"="+key, "PIPELINE_ID=123", "RELEASE_ID=", "DEPLOY_EVIDENCE_KIND="+kind, "DEPLOY_EVIDENCE_STATUS="+status, "DEPLOY_EVIDENCE_VALID_HOURS=48", "DEPLOY_EVIDENCE_REPORT_URL=https://ci.example.com/build/1", "DEPLOY_EVIDENCE_SUMMARY="+kind+" "+status, "DEPLOY_SCAN_TOOL=Trivy", "DEPLOY_SCAN_TOOL_VERSION=0.57.1", "DEPLOY_SCAN_RULES_VERSION=db-2026-10-12")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("generated %s evidence script failed: %v %s", kind, err, out)
		}
		if kind == "scan" && !strings.Contains(string(out), "不阻断发布") {
			t.Fatalf("failed scan should be saved without blocking: %s", out)
		}
		if strings.Contains(string(out), key) {
			t.Fatal("verification script logged credential")
		}
	}
	updated := app.store.ImageReleases("", 0)[0]
	if len(updated.Metadata.VerificationHistory) != 2 {
		t.Fatalf("verification script did not record both evidence kinds: %+v", updated.Metadata.VerificationHistory)
	}
	for i, kind := range []string{"ci", "smoke"} {
		evidence := updated.Metadata.VerificationHistory[i]
		if evidence.Kind != kind || evidence.Status != "passed" || evidence.Digest != *release.ImageDigest || evidence.RecordedBy != fmt.Sprintf("runner:%d", binding.ID) || evidence.ReportURL != "https://ci.example.com/build/1" {
			t.Fatalf("unexpected generated evidence: %+v", evidence)
		}
	}
	if len(updated.Metadata.ScanHistory) != 1 || updated.Metadata.Scan == nil || updated.Metadata.Scan.Status != "failed" || updated.Metadata.Scan.Tool != "Trivy" || updated.Metadata.Scan.ToolVersion != "0.57.1" || updated.Metadata.Scan.RulesVersion != "db-2026-10-12" || updated.Metadata.Scan.Digest != *release.ImageDigest {
		t.Fatalf("generated scan evidence missing scanner identity: %+v", updated.Metadata.ScanHistory)
	}
}
