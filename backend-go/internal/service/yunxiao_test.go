package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type cloudTransport func(*http.Request) (*http.Response, error)

func (f cloudTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func cloudResponse(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

const pipelineYAML = "sources:\n  repo:\n    type: codeup\n    branch: main\nstages:\n  z_build:\n    name: build\n    jobs:\n      build:\n        steps:\n          run:\n            step: Command\n            with:\n              run: echo build\n  a_test:\n    name: test\n    jobs:\n      test:\n        steps:\n          run:\n            step: Command\n            with:\n              run: echo test\n"

func TestRunnerYAMLMergePreservesOrderAndIsIdempotent(t *testing.T) {
	_, b, _ := runnerStore(t)
	scripts, err := GenerateRunnerScripts(b)
	if err != nil {
		t.Fatal(err)
	}
	merged, installed, err := MergeRunnerStage(pipelineYAML, b, scripts.Script)
	if err != nil || installed {
		t.Fatalf("merge %v %v", installed, err)
	}
	if !(strings.Index(merged, "z_build:") < strings.Index(merged, "a_test:") && strings.Index(merged, "a_test:") < strings.Index(merged, "deploy_platform_1:")) {
		t.Fatal("stage execution order changed")
	}
	if !strings.Contains(merged, "branch: main") || !strings.Contains(merged, "private/build-1") {
		t.Fatal("source or runner selection lost")
	}
	twice, installed, err := MergeRunnerStage(merged, b, scripts.Script)
	if err != nil || !installed || twice != merged {
		t.Fatalf("not idempotent %v %v", installed, err)
	}
	if _, _, err := MergeRunnerStage("schema: tb\npipeline: []", b, scripts.Script); err == nil {
		t.Fatal("UI engine format converted unsafely")
	}
	if _, _, err := MergeRunnerStage(merged, b, "different script"); err == nil {
		t.Fatal("overwrote same-name external stage")
	}
}
func TestYunxiaoPreviewApplyConflictBackupAndRedaction(t *testing.T) {
	s, b, _ := runnerStore(t)
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
	cipher, err := NewCredentialCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := cipher.Encrypt("pt-secret-private")
	conn, err := s.SaveYunxiaoConnection(YunxiaoConnection{Name: "test", Edition: "central", Domain: "openapi-rdc.aliyuncs.com", OrganizationID: "org1", TokenCiphertext: sealed})
	if err != nil {
		t.Fatal(err)
	}
	b.ConnectionID = conn.ID
	b, _, err = s.ConfigureRunner(b)
	if err != nil {
		t.Fatal(err)
	}
	cloud := NewYunxiaoService(s, cipher)
	version := int64(1)
	puts := 0
	content := pipelineYAML
	cloud.client.Transport = cloudTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://openapi-rdc.aliyuncs.com/oapi/v1/flow/organizations/org1/pipelines/123" || req.Header.Get("x-yunxiao-token") != "pt-secret-private" {
			t.Fatal("cloud request contract incorrect")
		}
		if req.Method == http.MethodGet {
			raw, _ := json.Marshal(map[string]any{"name": "orders", "type": "PIPELINEASCODE", "updateTime": version, "pipelineConfig": map[string]string{"flow": content}})
			return cloudResponse(200, string(raw)), nil
		}
		puts++
		var body map[string]string
		json.NewDecoder(req.Body).Decode(&body)
		if body["name"] != "orders" || !strings.Contains(body["content"], "deploy_platform_1") {
			t.Fatal("wrong update body")
		}
		content = body["content"]
		return cloudResponse(200, "true"), nil
	})
	p, err := cloud.Preview(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	version++
	if _, err := cloud.Apply(context.Background(), b, p.Fingerprint, p.ChangeHash); err == nil || puts != 0 {
		t.Fatal("stale preview wrote remote pipeline")
	}
	p, err = cloud.Preview(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := cloud.Apply(context.Background(), b, p.Fingerprint, p.ChangeHash); err != nil || !changed || puts != 1 {
		t.Fatalf("apply %v %v", changed, err)
	}
	saved, _ := s.RunnerBinding(b.ID)
	before, err := cipher.Decrypt(saved.PipelineBackupCiphertext)
	if err != nil || before != pipelineYAML || saved.LastAppliedAt == nil {
		t.Fatal("missing durable encrypted baseline")
	}
	raw, _ := json.Marshal(saved)
	if strings.Contains(string(raw), "pipeline_backup_ciphertext") || strings.Contains(string(raw), "pt-secret-private") {
		t.Fatal("backup or credential exposed")
	}
	cloud.client.Transport = cloudTransport(func(req *http.Request) (*http.Response, error) {
		return cloudResponse(403, "pt-secret-private sensitive upstream error"), nil
	})
	if _, err = cloud.Get(context.Background(), b); err == nil || strings.Contains(err.Error(), "pt-secret-private") {
		t.Fatal("upstream error leaked PAT")
	}
}

func TestYunxiaoPipelineScopeCheckReadsDefinitionWithoutExposingIt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		edition  string
		domain   string
		orgID    string
		wantPath string
	}{
		{
			name: "central edition", edition: "central", domain: "openapi-rdc.aliyuncs.com", orgID: "org1",
			wantPath: "/oapi/v1/flow/organizations/org1/pipelines/123456",
		},
		{
			name: "region edition", edition: "region", domain: "org.devops.aliyuncs.com",
			wantPath: "/oapi/v1/flow/pipelines/123456",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewStore()
			key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 32)))
			cipher, err := NewCredentialCipher(key)
			if err != nil {
				t.Fatal(err)
			}
			sealed, err := cipher.Encrypt("pt-scope-check-secret")
			if err != nil {
				t.Fatal(err)
			}
			connection, err := store.SaveYunxiaoConnection(YunxiaoConnection{
				Name: "scope-check", Edition: tc.edition, Domain: tc.domain,
				OrganizationID: tc.orgID, TokenCiphertext: sealed,
			})
			if err != nil {
				t.Fatal(err)
			}
			cloud := NewYunxiaoService(store, cipher)
			cloud.client.Transport = cloudTransport(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet || req.URL.Path != tc.wantPath || req.URL.RawQuery != "" || req.Header.Get("x-yunxiao-token") != "pt-scope-check-secret" {
					t.Fatalf("unexpected GetPipeline request: %s %s", req.Method, req.URL.String())
				}
				return cloudResponse(http.StatusOK, `{"name":"orders-config","type":"PIPELINEASCODE","updateTime":1770000000000,"pipelineConfig":{"flow":"schema: tb\npipeline:\n  - stages:\n      - jobs:\n          - params:\n              steps:\n                - stepType: exec-shell\n                  command: kubectl apply -f config.yaml # hidden-inline-secret\n"}}`), nil
			})

			report, err := cloud.InspectDeploymentPipelineScope(context.Background(), connection.ID, "123456")
			if err != nil {
				t.Fatal(err)
			}
			if report.PipelineID != "123456" || report.Name != "orders-config" || report.AnalysisStatus != "manual_review_required" || !report.ReviewRequired || len(report.Findings) == 0 {
				t.Fatalf("pipeline scope report incomplete: %+v", report)
			}
			encoded, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"hidden-inline-secret", "pt-scope-check-secret", "command:", "pipelineConfig", "config.yaml"} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("pipeline source or credential leaked in report: %q", forbidden)
				}
			}
		})
	}
}

func TestYunxiaoEndpointBoundaries(t *testing.T) {
	for _, domain := range []string{"evil.example.com", "openapi-rdc.aliyuncs.com.evil.com", "openapi-rdc.aliyuncs.com:443", "user@openapi-rdc.aliyuncs.com", "openapi-rdc.aliyuncs.com/path", "127.0.0.1"} {
		if err := ValidateYunxiaoEndpoint(YunxiaoConnection{Edition: "central", Domain: domain, OrganizationID: "org1"}); err == nil {
			t.Fatal(fmt.Sprintf("accepted %s", domain))
		}
	}
}

func TestYunxiaoDeploymentPipelineCreateAndGetRun(t *testing.T) {
	store := NewStore()
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	cipher, err := NewCredentialCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := cipher.Encrypt("pt-deployment-secret")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := store.SaveYunxiaoConnection(YunxiaoConnection{
		Name: "production", Edition: "central", Domain: "openapi-rdc.aliyuncs.com",
		OrganizationID: "org1", TokenCiphertext: sealed,
	})
	if err != nil {
		t.Fatal(err)
	}
	cloud := NewYunxiaoService(store, cipher)
	invocation := YunxiaoDeploymentPipelineInvocation{
		RequestID: "request-42", ConfigRevision: strings.Repeat("b", 40),
		ImageFull:     "registry.example.com/shop/api@sha256:" + strings.Repeat("a", 64),
		EnvironmentID: 12, ApplicationID: 34, Application: "shop-api",
		Namespace: "production", Deployment: "shop-api", Container: "api",
	}
	requests := 0
	cloud.client.Transport = cloudTransport(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.Header.Get("x-yunxiao-token") != "pt-deployment-secret" {
			t.Fatal("deployment token was not sent to Yunxiao")
		}
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/oapi/v1/flow/organizations/org1/pipelines/123/runs":
			var body struct {
				Params string `json:"params"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatalf("decode CreatePipelineRun body: %v", err)
			}
			var params struct {
				Envs map[string]string `json:"envs"`
			}
			if err := json.Unmarshal([]byte(body.Params), &params); err != nil {
				t.Fatalf("params must be a JSON string: %v", err)
			}
			if params.Envs["DEPLOY_CONFIG_REVISION"] != invocation.ConfigRevision || params.Envs["IMAGE_FULL"] != invocation.ImageFull || params.Envs["DEPLOY_PLATFORM_REQUEST_ID"] != invocation.RequestID || params.Envs["DEPLOY_ENVIRONMENT_ID"] != "12" || params.Envs["DEPLOY_APPLICATION_ID"] != "34" || params.Envs["DEPLOY_APPLICATION_NAME"] != "shop-api" || params.Envs["DEPLOY_NAMESPACE"] != "production" || params.Envs["DEPLOYMENT_NAME"] != "shop-api" || params.Envs["DEPLOY_CONTAINER_NAME"] != "api" {
				t.Fatalf("CreatePipelineRun variables=%+v", params.Envs)
			}
			return cloudResponse(http.StatusOK, "17"), nil
		case req.Method == http.MethodGet && req.URL.Path == "/oapi/v1/flow/organizations/org1/pipelines/123/runs" && req.URL.Query().Get("startTime") == "1770000000000":
			if req.URL.Query().Get("endTme") != "1770000010000" || req.URL.Query().Get("page") != "1" || req.URL.Query().Get("perPage") != "30" {
				t.Fatalf("unexpected ListPipelineRuns query: %s", req.URL.RawQuery)
			}
			return cloudResponse(http.StatusOK, `[{"pipelineId":123,"pipelineRunId":17,"startTime":1770000001000,"triggerMode":1}]`), nil
		case req.Method == http.MethodGet && req.URL.Path == "/oapi/v1/flow/organizations/org1/pipelines/123/runs/17":
			return cloudResponse(http.StatusOK, `{"pipelineId":123,"pipelineRunId":17,"status":"RUNNING","createTime":1770000000000,"updateTime":1770000005000,"globalParams":[{"key":"DEPLOY_PLATFORM_REQUEST_ID","value":"request-42","encrypted":false},{"key":"DEPLOY_CONFIG_REVISION","value":"`+strings.Repeat("b", 40)+`","encrypted":false},{"key":"IMAGE_FULL","value":"`+invocation.ImageFull+`","encrypted":false},{"key":"DEPLOY_ENVIRONMENT_ID","value":"12","encrypted":false},{"key":"DEPLOY_APPLICATION_ID","value":"34","encrypted":false},{"key":"DEPLOY_APPLICATION_NAME","value":"shop-api","encrypted":false},{"key":"DEPLOY_NAMESPACE","value":"production","encrypted":false},{"key":"DEPLOYMENT_NAME","value":"shop-api","encrypted":false},{"key":"DEPLOY_CONTAINER_NAME","value":"api","encrypted":false}]}`), nil
		default:
			t.Fatalf("unexpected Yunxiao request %s %s", req.Method, req.URL.String())
			return nil, nil
		}
	})
	runID, err := cloud.CreateDeploymentPipelineRun(context.Background(), connection.ID, "123", invocation)
	if err != nil || runID != "17" {
		t.Fatalf("CreateDeploymentPipelineRun runID=%q err=%v", runID, err)
	}
	runs, err := cloud.ListDeploymentPipelineRuns(context.Background(), connection.ID, "123", 1770000000000, 1770000010000, 1, 30)
	if err != nil || len(runs) != 1 || runs[0].PipelineRunID != 17 || runs[0].StartTime != 1770000001000 {
		t.Fatalf("ListDeploymentPipelineRuns runs=%+v err=%v", runs, err)
	}
	run, err := cloud.GetDeploymentPipelineRun(context.Background(), connection.ID, "123", runID)
	if err != nil || run.PipelineID != 123 || run.PipelineRunID != 17 || run.Status != "RUNNING" || run.CreateTime == 0 || run.UpdateTime == 0 || len(run.GlobalParams) < 9 || run.GlobalParams[0].Key != "DEPLOY_PLATFORM_REQUEST_ID" || run.GlobalParams[0].Value != "request-42" || run.GlobalParams[0].Encrypted {
		t.Fatalf("GetDeploymentPipelineRun run=%+v err=%v", run, err)
	}
	reconciled, found, err := cloud.FindDeploymentPipelineRunByRequestID(context.Background(), connection.ID, "123", invocation, 1770000000000, 1770000010000)
	if err != nil || !found || reconciled.PipelineRunID != 17 {
		t.Fatalf("FindDeploymentPipelineRunByRequestID run=%+v found=%t err=%v", reconciled, found, err)
	}
	if requests != 5 {
		t.Fatalf("expected create, status, list and reconciliation calls, got %d requests", requests)
	}
	invalid := invocation
	invalid.ImageFull = "registry.example.com/shop/api:latest"
	if _, err := cloud.CreateDeploymentPipelineRun(context.Background(), connection.ID, "123", invalid); err == nil {
		t.Fatal("mutable image must be rejected before a request")
	}
	if requests != 5 {
		t.Fatal("invalid create request reached Yunxiao")
	}
}

func TestYunxiaoDeploymentPipelineReturnsUnknownStatusesUnchanged(t *testing.T) {
	store := NewStore()
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("q", 32)))
	cipher, err := NewCredentialCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := cipher.Encrypt("pt-read")
	connection, err := store.SaveYunxiaoConnection(YunxiaoConnection{
		Name: "production", Edition: "central", Domain: "openapi-rdc.aliyuncs.com",
		OrganizationID: "org1", TokenCiphertext: sealed,
	})
	if err != nil {
		t.Fatal(err)
	}
	cloud := NewYunxiaoService(store, cipher)
	cloud.client.Transport = cloudTransport(func(req *http.Request) (*http.Response, error) {
		return cloudResponse(http.StatusOK, `{"pipelineId":123,"pipelineRunId":17,"status":"CANCELLED"}`), nil
	})
	run, err := cloud.GetDeploymentPipelineRun(context.Background(), connection.ID, "123", "17")
	if err != nil || run.Status != "CANCELLED" {
		t.Fatalf("unknown Yunxiao status must remain explicit: run=%+v err=%v", run, err)
	}
}

func TestInspectDeploymentPipelineReturnsOnlyMetadata(t *testing.T) {
	store := NewStore()
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("m", 32)))
	cipher, err := NewCredentialCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := cipher.Encrypt("pt-inspect-secret")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := store.SaveYunxiaoConnection(YunxiaoConnection{
		Name: "production", Edition: "central", Domain: "openapi-rdc.aliyuncs.com",
		OrganizationID: "org1", TokenCiphertext: sealed,
	})
	if err != nil {
		t.Fatal(err)
	}
	cloud := NewYunxiaoService(store, cipher)
	cloud.client.Transport = cloudTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Path != "/oapi/v1/flow/organizations/org1/pipelines/123456" || req.Header.Get("x-yunxiao-token") != "pt-inspect-secret" {
			t.Fatalf("unexpected metadata check request: %s %s", req.Method, req.URL.String())
		}
		return cloudResponse(http.StatusOK, `{"name":"orders-config","type":"PIPELINEASCODE","updateTime":1770000000000,"content":"private pipeline yaml","pipelineConfig":{"flow":"private pipeline yaml"}}`), nil
	})

	info, err := cloud.InspectDeploymentPipeline(context.Background(), connection.ID, "123456")
	if err != nil || info.PipelineID != "123456" || info.Name != "orders-config" || info.Type != "PIPELINEASCODE" || info.UpdateTime != 1770000000000 {
		t.Fatalf("InspectDeploymentPipeline info=%+v err=%v", info, err)
	}
	raw, _ := json.Marshal(info)
	if strings.Contains(string(raw), "private pipeline yaml") || strings.Contains(string(raw), "pt-inspect-secret") {
		t.Fatalf("pipeline body or credential leaked: %s", raw)
	}
	if _, err := cloud.InspectDeploymentPipeline(context.Background(), connection.ID, "../../other"); err == nil {
		t.Fatal("invalid pipeline ID accepted")
	}
}

func TestSearchDeploymentPipelinesUsesReadOnlyNameFilterAndPagination(t *testing.T) {
	store := NewStore()
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 32)))
	cipher, err := NewCredentialCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := cipher.Encrypt("pt-list-secret")
	if err != nil {
		t.Fatal(err)
	}
	central, err := store.SaveYunxiaoConnection(YunxiaoConnection{
		Name: "central", Edition: "central", Domain: "openapi-rdc.aliyuncs.com", OrganizationID: "org-list", TokenCiphertext: sealed,
	})
	if err != nil {
		t.Fatal(err)
	}
	region, err := store.SaveYunxiaoConnection(YunxiaoConnection{
		Name: "region", Edition: "region", Domain: "team.devops.aliyuncs.com", TokenCiphertext: sealed,
	})
	if err != nil {
		t.Fatal(err)
	}
	cloud := NewYunxiaoService(store, cipher)
	requests := 0
	cloud.client.Transport = cloudTransport(func(req *http.Request) (*http.Response, error) {
		requests++
		wantPath := "/oapi/v1/flow/organizations/org-list/pipelines"
		if requests == 2 {
			wantPath = "/oapi/v1/flow/pipelines"
		}
		if req.Method != http.MethodGet || req.URL.Path != wantPath || req.URL.Query().Get("pipelineName") != "orders config" || req.URL.Query().Get("page") != "2" || req.URL.Query().Get("perPage") != "30" || req.Header.Get("x-yunxiao-token") != "pt-list-secret" {
			t.Fatalf("unexpected pipeline search request: %s %s", req.Method, req.URL.String())
		}
		response := cloudResponse(http.StatusOK, `[{"pipelineId":123456,"pipelineName":"orders-config-deploy"},{"pipelineId":0,"pipelineName":"invalid"},{"pipelineId":123457,"pipelineName":"  "}]`)
		response.Header.Set("X-Total", "61")
		response.Header.Set("X-Total-Pages", "3")
		return response, nil
	})
	for _, connection := range []YunxiaoConnection{central, region} {
		result, err := cloud.SearchDeploymentPipelines(context.Background(), connection.ID, " orders config ", 2)
		if err != nil || len(result.Items) != 1 || result.Items[0].PipelineID != 123456 || result.Items[0].Name != "orders-config-deploy" || result.Page != 2 || result.PerPage != 30 || result.Total != 61 || result.TotalPages != 3 {
			t.Fatalf("pipeline search result=%+v err=%v", result, err)
		}
	}
	if requests != 2 {
		t.Fatalf("expected both Yunxiao editions to be queried, got %d requests", requests)
	}
	if _, err := cloud.SearchDeploymentPipelines(context.Background(), central.ID, " ", 1); err == nil {
		t.Fatal("empty pipeline search name accepted")
	}
	if _, err := cloud.SearchDeploymentPipelines(context.Background(), central.ID, "orders", 0); err == nil {
		t.Fatal("invalid pipeline search page accepted")
	}
}

func TestClassifyDeploymentPipelineRunRequiresExactPlaintextInputs(t *testing.T) {
	invocation := YunxiaoDeploymentPipelineInvocation{
		RequestID: "request-verify", ConfigRevision: strings.Repeat("c", 40),
		ImageFull:     "registry.example.com/shop/api@sha256:" + strings.Repeat("d", 64),
		EnvironmentID: 12, ApplicationID: 34, Application: "shop-api",
		Namespace: "production", Deployment: "shop-api", Container: "api",
	}
	variables, err := invocation.runtimeVariables()
	if err != nil {
		t.Fatal(err)
	}
	globalParams := make([]YunxiaoPipelineRunParameter, 0, len(variables))
	for key, value := range variables {
		globalParams = append(globalParams, YunxiaoPipelineRunParameter{Key: key, Value: value})
	}
	base := YunxiaoPipelineRun{PipelineID: 123, PipelineRunID: 17, GlobalParams: globalParams}

	for _, test := range []struct {
		status string
		want   YunxiaoDeploymentRunState
	}{
		{status: "RUNNING", want: YunxiaoDeploymentRunRunning},
		{status: "SUCCESS", want: YunxiaoDeploymentRunSucceeded},
		{status: "FAIL", want: YunxiaoDeploymentRunFailed},
		{status: "CANCELLED", want: YunxiaoDeploymentRunUnknown},
	} {
		run := base
		run.Status = test.status
		got, err := ClassifyDeploymentPipelineRun(run, "123", "17", invocation)
		if err != nil || got != test.want {
			t.Fatalf("status %q classified as %q, err=%v; want %q", test.status, got, err, test.want)
		}
	}

	badCases := []struct {
		name string
		run  YunxiaoPipelineRun
	}{
		{name: "wrong pipeline", run: YunxiaoPipelineRun{PipelineID: 124, PipelineRunID: 17, Status: "SUCCESS", GlobalParams: globalParams}},
		{name: "missing fixed input", run: YunxiaoPipelineRun{PipelineID: 123, PipelineRunID: 17, Status: "SUCCESS", GlobalParams: globalParams[:len(globalParams)-1]}},
	}
	wrongValue := append([]YunxiaoPipelineRunParameter(nil), globalParams...)
	for i := range wrongValue {
		if wrongValue[i].Key == "IMAGE_FULL" {
			wrongValue[i].Value = "registry.example.com/shop/api:latest"
		}
	}
	badCases = append(badCases, struct {
		name string
		run  YunxiaoPipelineRun
	}{name: "mutable image echoed", run: YunxiaoPipelineRun{PipelineID: 123, PipelineRunID: 17, Status: "SUCCESS", GlobalParams: wrongValue}})
	encrypted := append([]YunxiaoPipelineRunParameter(nil), globalParams...)
	for i := range encrypted {
		if encrypted[i].Key == "DEPLOY_CONFIG_REVISION" {
			encrypted[i].Encrypted = true
		}
	}
	badCases = append(badCases, struct {
		name string
		run  YunxiaoPipelineRun
	}{name: "encrypted commit", run: YunxiaoPipelineRun{PipelineID: 123, PipelineRunID: 17, Status: "SUCCESS", GlobalParams: encrypted}})
	duplicated := append(append([]YunxiaoPipelineRunParameter(nil), globalParams...), globalParams[0])
	badCases = append(badCases, struct {
		name string
		run  YunxiaoPipelineRun
	}{name: "duplicate parameter", run: YunxiaoPipelineRun{PipelineID: 123, PipelineRunID: 17, Status: "SUCCESS", GlobalParams: duplicated}})
	for _, test := range badCases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ClassifyDeploymentPipelineRun(test.run, "123", "17", invocation); err == nil {
				t.Fatal("unsafe or mismatched pipeline run was accepted")
			}
		})
	}
}
