package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/goccy/go-yaml"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Only vendor endpoints may receive a cloud PAT; redirects never forward it.
func ValidateYunxiaoEndpoint(c YunxiaoConnection) error {
	if c.Edition != "central" && c.Edition != "region" {
		return NewValidationError("云效版本须为 central 或 region")
	}
	u, err := url.Parse("https://" + c.Domain)
	if err != nil || u.User != nil || u.Port() != "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Hostname() != c.Domain {
		return NewValidationError("云效接入点须为域名，不能包含协议、端口或路径")
	}
	if c.Edition == "central" {
		if c.Domain != "openapi-rdc.aliyuncs.com" || !runnerIdentifier.MatchString(c.OrganizationID) {
			return NewValidationError("中心版须使用 openapi-rdc.aliyuncs.com 并填写组织 ID")
		}
	} else {
		allowed := false
		for _, suffix := range []string{".devops.aliyuncs.com", ".devops.aliyun.com", ".devops.alibabacloudcs.com", ".devops.alibabacloud.com"} {
			if strings.HasSuffix(c.Domain, suffix) && len(c.Domain) > len(suffix) {
				allowed = true
			}
		}
		if !allowed {
			return NewValidationError("Region 版须使用云效实例的官方 devops 域名")
		}
	}
	return nil
}

type YunxiaoPipeline struct {
	Name           string `json:"name"`
	Type           string `json:"type"`
	UpdateTime     int64  `json:"updateTime"`
	Content        string `json:"content"`
	PipelineConfig struct {
		Flow string `json:"flow"`
	} `json:"pipelineConfig"`
}

// YunxiaoDeploymentPipelineInfo is the minimum metadata exposed to operators
// when they verify an environment pipeline binding. Pipeline contents and
// credentials stay server-side.
type YunxiaoDeploymentPipelineInfo struct {
	PipelineID string `json:"pipeline_id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	UpdateTime int64  `json:"update_time"`
}

func (p YunxiaoPipeline) YAML() string {
	if p.Content != "" {
		return p.Content
	}
	return p.PipelineConfig.Flow
}
func (p YunxiaoPipeline) Fingerprint() string {
	raw, _ := json.Marshal(p)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

type PipelinePreview struct {
	Before           string `json:"before"`
	After            string `json:"after"`
	Name             string `json:"name"`
	Fingerprint      string `json:"fingerprint"`
	ChangeHash       string `json:"change_hash"`
	AlreadyInstalled bool   `json:"already_installed"`
	Note             string `json:"note"`
}

type YunxiaoService struct {
	store   *Store
	cipher  *CredentialCipher
	client  *http.Client
	writeMu sync.Mutex
}

func NewYunxiaoService(store *Store, cipher *CredentialCipher) *YunxiaoService {
	return &YunxiaoService{store: store, cipher: cipher, client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (y *YunxiaoService) request(ctx context.Context, c YunxiaoConnection, pipeline, method string, body any, out any) error {
	if !runnerPipelineID.MatchString(pipeline) {
		return NewValidationError("流水线 ID 无效")
	}
	return y.requestPath(ctx, c, yunxiaoPipelinePath(c, pipeline), method, body, out)
}

func yunxiaoPipelinePath(c YunxiaoConnection, pipeline string) string {
	return yunxiaoPipelinesPath(c) + "/" + url.PathEscape(pipeline)
}

func yunxiaoPipelinesPath(c YunxiaoConnection) string {
	path := "/oapi/v1/flow/"
	if c.Edition == "central" {
		path += "organizations/" + url.PathEscape(c.OrganizationID) + "/"
	}
	return path + "pipelines"
}

func (y *YunxiaoService) requestPath(ctx context.Context, c YunxiaoConnection, path, method string, body any, out any) error {
	_, err := y.requestPathWithHeaders(ctx, c, path, method, body, out)
	return err
}

func (y *YunxiaoService) requestPathWithHeaders(ctx context.Context, c YunxiaoConnection, path, method string, body any, out any) (http.Header, error) {
	if err := ValidateYunxiaoEndpoint(c); err != nil {
		return nil, err
	}
	if y.cipher == nil {
		return nil, ErrCipherNotConfigured
	}
	token, err := y.cipher.Decrypt(c.TokenCiphertext)
	if err != nil {
		return nil, fmt.Errorf("云效访问令牌解密失败")
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://"+c.Domain+path, reader)
	if err != nil {
		return nil, fmt.Errorf("创建云效请求失败")
	}
	req.Header.Set("x-yunxiao-token", token)
	req.Header.Set("Content-Type", "application/json")
	res, err := y.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("云效请求失败，请检查出站网络或接入点")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("云效请求返回 HTTP %d，请检查访问令牌和流水线权限", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 2<<20+1))
	if err != nil || len(raw) > 2<<20 {
		return nil, fmt.Errorf("云效响应超过限制或读取失败")
	}
	if err = json.Unmarshal(raw, out); err != nil {
		return nil, fmt.Errorf("云效响应格式无效")
	}
	return res.Header.Clone(), nil
}

type YunxiaoPipelineSummary struct {
	PipelineID int64  `json:"pipeline_id"`
	Name       string `json:"name"`
}

type yunxiaoPipelineRow struct {
	PipelineID   int64  `json:"pipelineId"`
	PipelineName string `json:"pipelineName"`
}

type YunxiaoPipelineSearchResult struct {
	Items      []YunxiaoPipelineSummary `json:"items"`
	Page       int                      `json:"page"`
	PerPage    int                      `json:"per_page"`
	Total      int                      `json:"total"`
	TotalPages int                      `json:"total_pages"`
}

// SearchDeploymentPipelines is a read-only, name-filtered picker for operators.
// Yunxiao returns at most 30 pipelines per page; names and IDs are the only
// fields exposed to the platform UI.
func (y *YunxiaoService) SearchDeploymentPipelines(ctx context.Context, connectionID int64, name string, page int) (YunxiaoPipelineSearchResult, error) {
	name = strings.TrimSpace(name)
	if connectionID <= 0 || name == "" || len(name) > 100 || page < 1 || page > 10000 {
		return YunxiaoPipelineSearchResult{}, NewValidationError("请选择云效连接并填写有效的流水线名称")
	}
	connection, err := y.store.YunxiaoConnection(connectionID)
	if err != nil {
		return YunxiaoPipelineSearchResult{}, err
	}
	query := url.Values{}
	query.Set("pipelineName", name)
	query.Set("page", strconv.Itoa(page))
	query.Set("perPage", "30")
	var rows []yunxiaoPipelineRow
	headers, err := y.requestPathWithHeaders(ctx, connection, yunxiaoPipelinesPath(connection)+"?"+query.Encode(), http.MethodGet, nil, &rows)
	if err != nil {
		return YunxiaoPipelineSearchResult{}, err
	}
	result := YunxiaoPipelineSearchResult{Items: make([]YunxiaoPipelineSummary, 0, len(rows)), Page: page, PerPage: 30}
	for _, row := range rows {
		pipelineName := strings.TrimSpace(row.PipelineName)
		if row.PipelineID <= 0 || pipelineName == "" || len(pipelineName) > 200 {
			continue
		}
		result.Items = append(result.Items, YunxiaoPipelineSummary{PipelineID: row.PipelineID, Name: pipelineName})
	}
	result.Total = parseYunxiaoPageHeader(headers, "X-Total")
	result.TotalPages = parseYunxiaoPageHeader(headers, "X-Total-Pages")
	if result.TotalPages == 0 && len(rows) == 30 {
		result.TotalPages = page + 1 // Older gateways may omit pagination headers.
	}
	return result, nil
}

func parseYunxiaoPageHeader(headers http.Header, key string) int {
	value, err := strconv.Atoi(strings.TrimSpace(headers.Get(key)))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

type YunxiaoPipelineRun struct {
	PipelineID    int64                         `json:"pipelineId"`
	PipelineRunID int64                         `json:"pipelineRunId"`
	Status        string                        `json:"status"`
	CreateTime    int64                         `json:"createTime"`
	UpdateTime    int64                         `json:"updateTime"`
	GlobalParams  []YunxiaoPipelineRunParameter `json:"globalParams,omitempty"`
}

type YunxiaoPipelineRunParameter struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Encrypted bool   `json:"encrypted"`
}

type YunxiaoPipelineRunSummary struct {
	PipelineID    int64 `json:"pipelineId"`
	PipelineRunID int64 `json:"pipelineRunId"`
	StartTime     int64 `json:"startTime"`
	EndTime       int64 `json:"endTime"`
	TriggerMode   int   `json:"triggerMode"`
}

type YunxiaoDeploymentRunState string

const (
	YunxiaoDeploymentRunRunning   YunxiaoDeploymentRunState = "running"
	YunxiaoDeploymentRunSucceeded YunxiaoDeploymentRunState = "succeeded"
	YunxiaoDeploymentRunFailed    YunxiaoDeploymentRunState = "failed"
	YunxiaoDeploymentRunUnknown   YunxiaoDeploymentRunState = "unknown"
)

// YunxiaoDeploymentPipelineInvocation is the contract passed to a separately
// configured deployment pipeline. The request ID is also injected into the
// run variables so an operator can reconcile a lost CreatePipelineRun response
// against the Yunxiao run history without blindly starting a second run.
type YunxiaoDeploymentPipelineInvocation struct {
	RequestID      string
	ConfigRevision string
	ImageFull      string
	EnvironmentID  int64
	ApplicationID  int64
	Application    string
	Namespace      string
	Deployment     string
	Container      string
}

func (i YunxiaoDeploymentPipelineInvocation) runtimeVariables() (map[string]string, error) {
	requestID := strings.TrimSpace(i.RequestID)
	if requestID == "" || len(requestID) > 128 || strings.Trim(requestID, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.:") != "" {
		return nil, NewValidationError("配置流水线请求 ID 无效")
	}
	commit := strings.ToLower(strings.TrimSpace(i.ConfigRevision))
	if (len(commit) != 40 && len(commit) != 64) || strings.Trim(commit, "0123456789abcdef") != "" {
		return nil, NewValidationError("配置流水线必须使用完整 Git commit SHA")
	}
	if !PinnedImage(strings.TrimSpace(i.ImageFull)) {
		return nil, NewValidationError("配置流水线必须使用固定 sha256 digest 镜像")
	}
	if i.EnvironmentID <= 0 || i.ApplicationID <= 0 || strings.TrimSpace(i.Application) == "" || strings.TrimSpace(i.Namespace) == "" || strings.TrimSpace(i.Deployment) == "" || strings.TrimSpace(i.Container) == "" {
		return nil, NewValidationError("配置流水线目标环境、应用、namespace、Deployment 和容器必须完整")
	}
	return map[string]string{
		"DEPLOY_PLATFORM_REQUEST_ID": requestID,
		"DEPLOY_CONFIG_REVISION":     commit,
		"IMAGE_FULL":                 strings.TrimSpace(i.ImageFull),
		"DEPLOY_ENVIRONMENT_ID":      strconv.FormatInt(i.EnvironmentID, 10),
		"DEPLOY_APPLICATION_ID":      strconv.FormatInt(i.ApplicationID, 10),
		"DEPLOY_APPLICATION_NAME":    strings.TrimSpace(i.Application),
		"DEPLOY_NAMESPACE":           strings.TrimSpace(i.Namespace),
		"DEPLOYMENT_NAME":            strings.TrimSpace(i.Deployment),
		"DEPLOY_CONTAINER_NAME":      strings.TrimSpace(i.Container),
	}, nil
}

func normalizeYunxiaoRunID(value string) (string, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return "", NewValidationError("云效流水线运行 ID 无效")
	}
	return strconv.FormatInt(parsed, 10), nil
}

// CreateDeploymentPipelineRun starts an explicitly-bound environment
// pipeline. Yunxiao accepts runtime variables inside a JSON-encoded params
// string and returns the run ID as a JSON integer. The caller must persist its
// intent before calling this method and must not retry on an ambiguous error.
func (y *YunxiaoService) CreateDeploymentPipelineRun(ctx context.Context, connectionID int64, pipelineID string, invocation YunxiaoDeploymentPipelineInvocation) (string, error) {
	if connectionID <= 0 {
		return "", NewValidationError("云效连接无效")
	}
	if !runnerPipelineID.MatchString(pipelineID) {
		return "", NewValidationError("流水线 ID 无效")
	}
	envs, err := invocation.runtimeVariables()
	if err != nil {
		return "", err
	}
	connection, err := y.store.YunxiaoConnection(connectionID)
	if err != nil {
		return "", NewValidationError("请先选择云效连接")
	}
	params, err := json.Marshal(map[string]any{"envs": envs})
	if err != nil {
		return "", err
	}
	var response json.RawMessage
	err = y.requestPath(ctx, connection, yunxiaoPipelinePath(connection, pipelineID)+"/runs", http.MethodPost, map[string]string{"params": string(params)}, &response)
	if err != nil {
		return "", err
	}
	var runID string
	if json.Unmarshal(response, &runID) != nil {
		runID = string(response)
	}
	runID, err = normalizeYunxiaoRunID(runID)
	if err != nil {
		return "", fmt.Errorf("云效返回的流水线运行 ID 无效；创建结果需要人工核对")
	}
	return runID, nil
}

// GetDeploymentPipelineRun reads a single environment pipeline run. Unknown
// vendor status strings are returned unchanged so callers can fail closed.
func (y *YunxiaoService) GetDeploymentPipelineRun(ctx context.Context, connectionID int64, pipelineID, runID string) (YunxiaoPipelineRun, error) {
	if connectionID <= 0 {
		return YunxiaoPipelineRun{}, NewValidationError("云效连接无效")
	}
	if !runnerPipelineID.MatchString(pipelineID) {
		return YunxiaoPipelineRun{}, NewValidationError("流水线 ID 或运行 ID 无效")
	}
	runID, err := normalizeYunxiaoRunID(runID)
	if err != nil {
		return YunxiaoPipelineRun{}, NewValidationError("流水线 ID 或运行 ID 无效")
	}
	connection, err := y.store.YunxiaoConnection(connectionID)
	if err != nil {
		return YunxiaoPipelineRun{}, NewValidationError("请先选择云效连接")
	}
	path := yunxiaoPipelinePath(connection, pipelineID) + "/runs/" + url.PathEscape(runID)
	var raw json.RawMessage
	if err := y.requestPath(ctx, connection, path, http.MethodGet, nil, &raw); err != nil {
		return YunxiaoPipelineRun{}, err
	}
	var run YunxiaoPipelineRun
	if err := json.Unmarshal(raw, &run); err != nil {
		return YunxiaoPipelineRun{}, fmt.Errorf("云效流水线运行响应格式无效")
	}
	if run.PipelineRunID != 0 && strconv.FormatInt(run.PipelineRunID, 10) != runID {
		return YunxiaoPipelineRun{}, fmt.Errorf("云效返回的流水线运行 ID 与请求不一致")
	}
	if run.PipelineID != 0 && strconv.FormatInt(run.PipelineID, 10) != pipelineID {
		return YunxiaoPipelineRun{}, fmt.Errorf("云效返回的流水线 ID 与请求不一致")
	}
	return run, nil
}

// ClassifyDeploymentPipelineRun only treats a vendor SUCCESS as deployment
// success after the run echoes every immutable input as a plaintext runtime
// parameter. This verifies the run's declared inputs, not the resulting
// cluster state; callers must still perform workload/configuration readback.
func ClassifyDeploymentPipelineRun(run YunxiaoPipelineRun, pipelineID, runID string, invocation YunxiaoDeploymentPipelineInvocation) (YunxiaoDeploymentRunState, error) {
	if !runnerPipelineID.MatchString(pipelineID) {
		return YunxiaoDeploymentRunUnknown, NewValidationError("云效流水线 ID 无效")
	}
	parsedPipelineID, err := strconv.ParseInt(pipelineID, 10, 64)
	if err != nil || parsedPipelineID <= 0 || run.PipelineID != parsedPipelineID {
		return YunxiaoDeploymentRunUnknown, newConflict("云效运行流水线身份与目标不一致")
	}
	normalizedRunID, err := normalizeYunxiaoRunID(runID)
	if err != nil || run.PipelineRunID <= 0 || strconv.FormatInt(run.PipelineRunID, 10) != normalizedRunID {
		return YunxiaoDeploymentRunUnknown, newConflict("云效运行 ID 与目标不一致")
	}
	expected, err := invocation.runtimeVariables()
	if err != nil {
		return YunxiaoDeploymentRunUnknown, err
	}
	actual := make(map[string]YunxiaoPipelineRunParameter, len(run.GlobalParams))
	for _, parameter := range run.GlobalParams {
		if _, exists := actual[parameter.Key]; exists {
			return YunxiaoDeploymentRunUnknown, newConflict("云效运行参数 %s 重复，不能核验", parameter.Key)
		}
		actual[parameter.Key] = parameter
	}
	for key, value := range expected {
		parameter, exists := actual[key]
		if !exists || parameter.Encrypted || parameter.Value != value {
			return YunxiaoDeploymentRunUnknown, newConflict("云效运行参数 %s 缺失、加密或与固定计划不一致", key)
		}
	}
	switch run.Status {
	case "RUNNING":
		return YunxiaoDeploymentRunRunning, nil
	case "SUCCESS":
		return YunxiaoDeploymentRunSucceeded, nil
	case "FAIL":
		return YunxiaoDeploymentRunFailed, nil
	default:
		return YunxiaoDeploymentRunUnknown, nil
	}
}

// ListDeploymentPipelineRuns provides the vendor history needed to reconcile
// a create request whose response was lost. It only lists a bounded time
// window and page; a caller must inspect each candidate's global parameters
// with GetDeploymentPipelineRun and require a unique platform request ID.
func (y *YunxiaoService) ListDeploymentPipelineRuns(ctx context.Context, connectionID int64, pipelineID string, startTime, endTime int64, page, perPage int) ([]YunxiaoPipelineRunSummary, error) {
	if connectionID <= 0 || !runnerPipelineID.MatchString(pipelineID) {
		return nil, NewValidationError("云效连接或流水线 ID 无效")
	}
	if startTime <= 0 || endTime < startTime || page < 1 || perPage < 1 || perPage > 30 {
		return nil, NewValidationError("云效运行查询时间范围或分页参数无效")
	}
	connection, err := y.store.YunxiaoConnection(connectionID)
	if err != nil {
		return nil, NewValidationError("请先选择云效连接")
	}
	query := url.Values{}
	query.Set("startTime", strconv.FormatInt(startTime, 10))
	// Yunxiao's current ListPipelineRuns OpenAPI spells this parameter endTme.
	query.Set("endTme", strconv.FormatInt(endTime, 10))
	query.Set("page", strconv.Itoa(page))
	query.Set("perPage", strconv.Itoa(perPage))
	var runs []YunxiaoPipelineRunSummary
	path := yunxiaoPipelinePath(connection, pipelineID) + "/runs?" + query.Encode()
	if err := y.requestPath(ctx, connection, path, http.MethodGet, nil, &runs); err != nil {
		return nil, err
	}
	if len(runs) > perPage {
		return nil, fmt.Errorf("云效返回运行列表超过请求分页上限")
	}
	return runs, nil
}

// FindDeploymentPipelineRunByRequestID reconciles an ambiguous create response
// by scanning a bounded run-history window and checking the run's echoed,
// non-encrypted runtime variables. found=false is not proof that Yunxiao did
// not create the run (history may be eventually consistent); callers must
// leave the operation unknown and must not automatically retry Create.
func (y *YunxiaoService) FindDeploymentPipelineRunByRequestID(ctx context.Context, connectionID int64, pipelineID string, invocation YunxiaoDeploymentPipelineInvocation, startTime, endTime int64) (YunxiaoPipelineRun, bool, error) {
	variables, err := invocation.runtimeVariables()
	if err != nil {
		return YunxiaoPipelineRun{}, false, err
	}
	const perPage = 30
	const maxPages = 10
	var matches []YunxiaoPipelineRun
	for page := 1; page <= maxPages; page++ {
		summaries, err := y.ListDeploymentPipelineRuns(ctx, connectionID, pipelineID, startTime, endTime, page, perPage)
		if err != nil {
			return YunxiaoPipelineRun{}, false, err
		}
		for _, summary := range summaries {
			if summary.PipelineRunID <= 0 || summary.StartTime < startTime || summary.StartTime > endTime || (summary.PipelineID > 0 && strconv.FormatInt(summary.PipelineID, 10) != pipelineID) {
				return YunxiaoPipelineRun{}, false, fmt.Errorf("云效运行历史包含与目标流水线不一致的记录")
			}
			run, err := y.GetDeploymentPipelineRun(ctx, connectionID, pipelineID, strconv.FormatInt(summary.PipelineRunID, 10))
			if err != nil {
				return YunxiaoPipelineRun{}, false, err
			}
			requestID, requestIDFound := yunxiaoRunVariable(run, "DEPLOY_PLATFORM_REQUEST_ID")
			if !requestIDFound || requestID != variables["DEPLOY_PLATFORM_REQUEST_ID"] {
				continue
			}
			for key, expected := range variables {
				actual, found := yunxiaoRunVariable(run, key)
				if !found || actual != expected {
					return YunxiaoPipelineRun{}, false, newConflict("云效运行 request ID 相同，但固定参数 %s 不匹配", key)
				}
			}
			matches = append(matches, run)
			if len(matches) > 1 {
				return YunxiaoPipelineRun{}, false, newConflict("云效运行历史中发现多个相同 request ID，不能自动选择")
			}
		}
		if len(summaries) < perPage {
			if len(matches) == 1 {
				return matches[0], true, nil
			}
			return YunxiaoPipelineRun{}, false, nil
		}
	}
	return YunxiaoPipelineRun{}, false, fmt.Errorf("云效运行历史超过核对上限，操作结果仍未知")
}

func yunxiaoRunVariable(run YunxiaoPipelineRun, key string) (string, bool) {
	for _, param := range run.GlobalParams {
		if param.Key == key && !param.Encrypted {
			return param.Value, true
		}
	}
	return "", false
}
func (y *YunxiaoService) Get(ctx context.Context, b RunnerBinding) (YunxiaoPipeline, error) {
	c, err := y.store.YunxiaoConnection(b.ConnectionID)
	if err != nil {
		return YunxiaoPipeline{}, NewValidationError("请先选择云效连接")
	}
	var p YunxiaoPipeline
	err = y.request(ctx, c, b.PipelineID, http.MethodGet, nil, &p)
	return p, err
}

// InspectDeploymentPipeline performs a read-only connection check for an
// explicitly selected environment pipeline. It intentionally returns only
// metadata; it neither previews nor mutates the pipeline definition.
func (y *YunxiaoService) InspectDeploymentPipeline(ctx context.Context, connectionID int64, pipelineID string) (YunxiaoDeploymentPipelineInfo, error) {
	if connectionID <= 0 {
		return YunxiaoDeploymentPipelineInfo{}, NewValidationError("云效连接无效")
	}
	if !runnerPipelineID.MatchString(pipelineID) {
		return YunxiaoDeploymentPipelineInfo{}, NewValidationError("流水线 ID 无效")
	}
	connection, err := y.store.YunxiaoConnection(connectionID)
	if err != nil {
		return YunxiaoDeploymentPipelineInfo{}, NewValidationError("请先选择云效连接")
	}
	var pipeline YunxiaoPipeline
	if err := y.request(ctx, connection, pipelineID, http.MethodGet, nil, &pipeline); err != nil {
		return YunxiaoDeploymentPipelineInfo{}, err
	}
	return YunxiaoDeploymentPipelineInfo{
		PipelineID: pipelineID,
		Name:       pipeline.Name,
		Type:       pipeline.Type,
		UpdateTime: pipeline.UpdateTime,
	}, nil
}

func (y *YunxiaoService) Preview(ctx context.Context, b RunnerBinding) (PipelinePreview, error) {
	if _, _, err := y.store.RunnerTarget(b); err != nil {
		return PipelinePreview{}, err
	}
	p, err := y.Get(ctx, b)
	if err != nil {
		return PipelinePreview{}, err
	}
	if p.Type != "PIPELINEASCODE" {
		return PipelinePreview{}, NewValidationError("自动接入仅支持 YAML 流水线；请复制通知任务配置到云效手工接入")
	}
	scripts, err := GenerateRunnerScripts(b)
	if err != nil {
		return PipelinePreview{}, err
	}
	after, installed, err := MergeRunnerStage(p.YAML(), b, scripts.Script)
	if err != nil {
		return PipelinePreview{}, err
	}
	sum := sha256.Sum256([]byte(after))
	return PipelinePreview{Before: p.YAML(), After: after, Name: p.Name, Fingerprint: p.Fingerprint(), ChangeHash: hex.EncodeToString(sum[:]), AlreadyInstalled: installed, Note: "保存前重新检查云效配置；云效接口未提供原子版本条件，其他编辑者应避免同时修改。先在云效配置私密变量和镜像产物输出，平台不写入密钥。"}, nil
}
func (y *YunxiaoService) Apply(ctx context.Context, b RunnerBinding, fingerprint, changeHash string) (bool, error) {
	y.writeMu.Lock()
	defer y.writeMu.Unlock()
	fresh, err := y.store.RunnerBinding(b.ID)
	if err != nil {
		return false, err
	}
	if fresh.UpdatedAt != b.UpdatedAt {
		return false, newConflict("接入配置已变化，请重新预览")
	}
	preview, err := y.Preview(ctx, fresh)
	if err != nil {
		return false, err
	}
	if fingerprint == "" || changeHash == "" || preview.Fingerprint != fingerprint || preview.ChangeHash != changeHash {
		return false, newConflict("云效流水线或接入配置已变化，请重新预览")
	}
	if preview.AlreadyInstalled {
		return false, nil
	}
	if y.cipher == nil {
		return false, ErrCipherNotConfigured
	}
	backup, err := y.cipher.Encrypt(preview.Before)
	if err != nil {
		return false, err
	}
	if _, err = y.store.SaveRunnerPipelineBackup(fresh, backup); err != nil {
		return false, err
	}
	c, err := y.store.YunxiaoConnection(b.ConnectionID)
	if err != nil {
		return false, err
	}
	var success bool
	err = y.request(ctx, c, b.PipelineID, http.MethodPut, map[string]string{"name": preview.Name, "content": preview.After}, &success)
	if err != nil {
		return false, err
	}
	if !success {
		return false, fmt.Errorf("云效未确认保存成功")
	}
	if err = y.store.MarkRunnerPipelineApplied(b.ID); err != nil {
		return true, fmt.Errorf("云效已保存，但本地状态记录失败，请刷新核对")
	}
	return true, nil
}

// Ordered maps preserve execution order of existing stages/jobs. Refuse UI
// engine formats rather than converting them or silently losing tasks.
func MergeRunnerStage(before string, b RunnerBinding, script string) (string, bool, error) {
	var root yaml.MapSlice
	if err := yaml.NewDecoder(strings.NewReader(before), yaml.UseOrderedMap()).Decode(&root); err != nil {
		return "", false, NewValidationError("流水线原始 YAML 无法解析，使用手工合并")
	}
	key := fmt.Sprintf("deploy_platform_%d", b.ID)
	stage := RunnerNotificationStage(b, script)
	for i, item := range root {
		if item.Key == "stages" {
			stages, ok := item.Value.(yaml.MapSlice)
			if !ok || len(stages) == 0 {
				return "", false, NewValidationError("无法安全识别流水线阶段顺序，请手工合并通知任务")
			}
			for _, existing := range stages {
				if existing.Key == key {
					old, _ := yaml.Marshal(existing.Value)
					wanted, _ := yaml.Marshal(stage)
					var a, b any
					yaml.Unmarshal(old, &a)
					yaml.Unmarshal(wanted, &b)
					x, _ := json.Marshal(a)
					z, _ := json.Marshal(b)
					if !bytes.Equal(x, z) {
						return "", false, newConflict("已有同名通知阶段但内容不同，请手工核对；平台不会覆盖")
					}
					return before, true, nil
				}
			}
			stages = append(stages, yaml.MapItem{Key: key, Value: stage})
			root[i].Value = stages
			raw, err := yaml.MarshalWithOptions(root, yaml.UseLiteralStyleIfMultiline(true))
			if err != nil {
				return "", false, err
			}
			return string(raw), false, nil
		}
	}
	return "", false, NewValidationError("云效未返回可直接编辑的 stages YAML，请使用生成片段手工合并")
}
