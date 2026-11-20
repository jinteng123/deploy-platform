package httpapi

import (
	"context"
	"deploy-platform/backend-go/internal/service"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"strings"
	"time"
)

func runnerJSON(c *gin.Context, v any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		fail(c, 400, "请求 JSON 无效或包含未支持字段")
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		fail(c, 400, "请求体只能包含一个 JSON 对象")
		return false
	}
	return true
}
func runnerError(c *gin.Context, err error) {
	if errors.Is(err, service.ErrRunnerCredential) {
		fail(c, 401, err.Error())
		return
	}
	writeCDErr(c, err)
}
func runnerCredential(c *gin.Context, store *service.Store) (service.RunnerBinding, bool) {
	id, ok := pathInt64(c, "id")
	if !ok {
		return service.RunnerBinding{}, false
	}
	auth := c.GetHeader("Authorization")
	token := ""
	if strings.HasPrefix(auth, "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	b, err := store.ResolveRunner(id, token)
	if err != nil {
		runnerError(c, err)
		return b, false
	}
	c.Set(ctxUserKey, &service.User{Username: fmt.Sprintf("runner:%d", b.ID), Role: "ci"})
	return b, true
}

type runnerConfigBody struct {
	AppID            int        `json:"app_id"`
	PipelineID       string     `json:"pipeline_id"`
	PrivateClusterID string     `json:"private_cluster_id"`
	PlatformURL      string     `json:"platform_url"`
	ConnectionID     int64      `json:"connection_id"`
	ExpiresAt        *time.Time `json:"expires_at"`
}

type deploymentPipelineInspector interface {
	InspectDeploymentPipeline(context.Context, int64, string) (service.YunxiaoDeploymentPipelineInfo, error)
}

type deploymentPipelineScopeInspector interface {
	InspectDeploymentPipelineScope(context.Context, int64, string) (service.YunxiaoDeploymentPipelineScopeReport, error)
}

type deploymentPipelineSearcher interface {
	SearchDeploymentPipelines(context.Context, int64, string, int) (service.YunxiaoPipelineSearchResult, error)
}

func registerDeploymentPipelineSearchAPI(ops *gin.RouterGroup, store *service.Store, searcher deploymentPipelineSearcher) {
	ops.POST("/deployment-pipelines/search", func(c *gin.Context) {
		var body struct {
			ConnectionID int64  `json:"connection_id" binding:"required,gt=0"`
			Name         string `json:"name" binding:"required,max=100"`
			Page         int    `json:"page"`
		}
		if !runnerJSON(c, &body) {
			return
		}
		if body.Page == 0 {
			body.Page = 1
		}
		result, err := searcher.SearchDeploymentPipelines(c.Request.Context(), body.ConnectionID, body.Name, body.Page)
		audit(c, store, "yunxiao_pipeline_search", 0, "", "yunxiao_connections", fmt.Sprint(body.ConnectionID), "read-only pipeline name search", auditErr(err))
		if err != nil {
			runnerError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})
}

func registerDeploymentPipelineCheckAPI(ops *gin.RouterGroup, store *service.Store, inspector deploymentPipelineInspector) {
	ops.POST("/deployment-pipeline-check", func(c *gin.Context) {
		var body struct {
			ConnectionID int64  `json:"connection_id"`
			PipelineID   string `json:"pipeline_id"`
		}
		if !runnerJSON(c, &body) {
			return
		}
		info, err := inspector.InspectDeploymentPipeline(c.Request.Context(), body.ConnectionID, strings.TrimSpace(body.PipelineID))
		audit(c, store, "yunxiao_pipeline_check", 0, "", "yunxiao_connections", fmt.Sprintf("%d/%s", body.ConnectionID, body.PipelineID), "read-only pipeline metadata check", auditErr(err))
		if err != nil {
			runnerError(c, err)
			return
		}
		c.JSON(http.StatusOK, info)
	})
}

func registerDeploymentPipelineScopeCheckAPI(ops *gin.RouterGroup, store *service.Store, inspector deploymentPipelineScopeInspector) {
	ops.POST("/deployment-pipeline-scope-check", func(c *gin.Context) {
		var body struct {
			ConnectionID int64  `json:"connection_id"`
			PipelineID   string `json:"pipeline_id"`
		}
		if !runnerJSON(c, &body) {
			return
		}
		report, err := inspector.InspectDeploymentPipelineScope(c.Request.Context(), body.ConnectionID, strings.TrimSpace(body.PipelineID))
		detail := "server-side static scope analysis; raw pipeline definition not returned"
		if err != nil {
			detail = "pipeline scope analysis failed; raw pipeline definition not returned"
		}
		audit(c, store, "yunxiao_pipeline_scope_check", 0, "", "yunxiao_connections", fmt.Sprintf("%d/%s", body.ConnectionID, body.PipelineID), detail, auditErr(err))
		if err != nil {
			runnerError(c, err)
			return
		}
		c.JSON(http.StatusOK, report)
	})
}

func registerDeploymentPipelineScopeReviewAPI(ops *gin.RouterGroup, store *service.Store, inspector deploymentPipelineScopeInspector) {
	ops.POST("/deployment-pipeline-scope-review", func(c *gin.Context) {
		var body struct {
			AppID          int    `json:"app_id" binding:"required,gt=0"`
			ConnectionID   int64  `json:"connection_id" binding:"required,gt=0"`
			PipelineID     string `json:"pipeline_id" binding:"required"`
			DefinitionHash string `json:"definition_hash" binding:"required"`
			WriteScope     string `json:"write_scope" binding:"required"`
			Acknowledged   bool   `json:"acknowledged" binding:"required"`
		}
		if !runnerJSON(c, &body) {
			return
		}
		binding, err := store.DeliveryBindingForApp(body.AppID)
		if err == nil && (binding.DeploymentPipeline == nil || binding.DeploymentPipeline.ConnectionID != body.ConnectionID || binding.DeploymentPipeline.PipelineID != strings.TrimSpace(body.PipelineID)) {
			err = service.NewValidationError("请先把当前云效流水线绑定到该环境目标")
		}
		if err == nil && !body.Acknowledged {
			err = service.NewValidationError("请确认已在云效核对完整定义和实际执行身份")
		}
		var report service.YunxiaoDeploymentPipelineScopeReport
		if err == nil {
			report, err = inspector.InspectDeploymentPipelineScope(c.Request.Context(), body.ConnectionID, strings.TrimSpace(body.PipelineID))
		}
		if err == nil && (report.AnalysisStatus != "manual_review_required" || report.DefinitionHash == "") {
			err = fmt.Errorf("%w: 流水线定义不可核对；请在云效检查完整定义后重试", service.ErrConflict)
		}
		if err == nil && report.DefinitionHash != strings.TrimSpace(body.DefinitionHash) {
			err = fmt.Errorf("%w: 流水线定义在人工核对后已变化，请重新分析并核对", service.ErrConflict)
		}
		actor := "unknown"
		if user := currentUser(c); user != nil {
			actor = user.Username
		}
		var review service.DeliveryPipelineScopeReview
		if err == nil {
			review, err = store.RecordDeliveryPipelineScopeReview(service.DeliveryPipelineScopeReview{
				BindingID: binding.ID, ConnectionID: body.ConnectionID, PipelineID: strings.TrimSpace(body.PipelineID),
				DefinitionHash: strings.TrimSpace(body.DefinitionHash), WriteScope: body.WriteScope, Reviewer: actor,
			})
		}
		detail := "pipeline scope review rejected"
		if err == nil {
			detail = fmt.Sprintf("connection=%d; pipeline=%s; definition_sha256=%s; write_scope=%s", review.ConnectionID, review.PipelineID, review.DefinitionHash, review.WriteScope)
		}
		target, _ := store.FindApp(body.AppID)
		audit(c, store, "yunxiao_pipeline_scope_review", target.ClusterID, target.Namespace, "delivery_bindings", fmtInt64(binding.ID), detail, auditErr(err))
		if err != nil {
			writeCDErr(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{
			"review":            review,
			"execution_enabled": false,
			"message":           "核对结论已记录；这不会开放或触发配置流水线。",
		})
	})
}

func (body runnerConfigBody) binding(id int64) service.RunnerBinding {
	b := service.RunnerBinding{ID: id, AppID: body.AppID, PipelineID: strings.TrimSpace(body.PipelineID), PrivateClusterID: strings.TrimSpace(body.PrivateClusterID), PlatformURL: strings.TrimSpace(body.PlatformURL), ConnectionID: body.ConnectionID}
	if body.ExpiresAt != nil {
		b.ExpiresAt = *body.ExpiresAt
	}
	return b
}

type runnerEventBody struct {
	PipelineID      string `json:"pipeline_id"`
	BuildNumber     string `json:"build_number"`
	ImageFull       string `json:"image_full"`
	ImageDigest     string `json:"image_digest"`
	CommitID        string `json:"commit_id"`
	CommitTimestamp string `json:"commit_timestamp"`
}

type runnerEvidenceBody struct {
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	ReportURL  string    `json:"report_url"`
	Summary    string    `json:"summary"`
	ValidUntil time.Time `json:"valid_until"`
}

type runnerScanBody struct {
	Status       string    `json:"status"`
	Tool         string    `json:"tool"`
	ToolVersion  string    `json:"tool_version"`
	RulesVersion string    `json:"rules_version"`
	ReportURL    string    `json:"report_url"`
	Summary      string    `json:"summary"`
	ValidUntil   time.Time `json:"valid_until"`
}

type runnerConfigurationSourceBody struct {
	Repository string `json:"repository_url"`
	Path       string `json:"path"`
	Revision   string `json:"revision"`
}

func runnerList(store *service.Store) []map[string]any {
	out := []map[string]any{}
	for _, b := range store.RunnerBindings() {
		entry := map[string]any{"binding": b, "authorization_expired": !time.Now().Before(b.ExpiresAt)}
		if _, _, err := store.RunnerTarget(b); err != nil {
			entry["configuration_error"] = err.Error()
		}
		for _, r := range store.ImageReleases("", 0) {
			if r.Source == fmt.Sprintf("runner:%d", b.ID) {
				if l, err := store.FindLog(r.TestDeployLogID); err == nil {
					entry["last_release_id"] = r.ID
					entry["last_event_at"] = r.CreatedAt
					entry["last_status"] = l.Status
					entry["last_image"] = r.ImageFull
					entry["last_error"] = l.ErrorMessage
				}
				break
			}
		}
		out = append(out, entry)
	}
	return out
}

func registerRunnerAPI(r *gin.Engine, api *gin.RouterGroup, store *service.Store, cd *service.CDService, cipher *service.CredentialCipher) {
	// Dedicated machine surface intentionally does not use the broad dp_ API keys.
	ext := r.Group("/openapi/v1/runner", openAPIErrShape)
	ext.POST("/bindings/:id/check", func(c *gin.Context) {
		b, ok := runnerCredential(c, store)
		if !ok {
			return
		}
		var body struct {
			PipelineID string `json:"pipeline_id"`
			ImageFull  string `json:"image_full,omitempty"`
		}
		if !runnerJSON(c, &body) {
			return
		}
		if body.PipelineID != b.PipelineID {
			fail(c, 403, "流水线与组件接入不匹配")
			return
		}
		if body.ImageFull != "" {
			if err := service.CheckAppImage(b.Binding, body.ImageFull); err != nil {
				runnerError(c, err)
				return
			}
		}
		if err := store.MarkRunnerChecked(b.ID, b.TokenHash); err != nil {
			runnerError(c, err)
			return
		}
		audit(c, store, "runner_check", b.Binding.ClusterID, b.Binding.Namespace, "runner_bindings", fmt.Sprint(b.ID), "read-only check", "success")
		c.JSON(200, gin.H{"status": "ok", "binding_id": b.ID, "app_name": b.Binding.AppName, "target_cluster": b.EnvCode, "deployment": b.Binding.DeploymentName, "container": b.Binding.ContainerName, "image_repository": b.Binding.ImageRepository, "deploy_created": false})
	})
	ext.POST("/bindings/:id/events", func(c *gin.Context) {
		b, ok := runnerCredential(c, store)
		if !ok {
			return
		}
		var body runnerEventBody
		if !runnerJSON(c, &body) {
			return
		}
		if body.PipelineID != b.PipelineID {
			fail(c, 403, "流水线与组件接入不匹配")
			return
		}
		if body.BuildNumber == "" || service.NormalizeDigest(body.ImageDigest) == "" {
			fail(c, 400, "构建编号与有效 sha256 digest 必填")
			return
		}
		digest := body.ImageDigest
		pipeline := body.PipelineID
		build := body.BuildNumber
		ev := service.ImageEvent{
			EventID: pipeline + ":" + build + ":" + fmt.Sprint(b.ID), Source: fmt.Sprintf("runner:%d", b.ID),
			TargetCluster: b.EnvCode, AppName: b.Binding.AppName, ImageFull: body.ImageFull, ImageDigest: &digest,
			PipelineID: &pipeline, BuildNumber: &build, CommitID: strPtrOrNil(body.CommitID),
			CommitTimestamp: parseCommitTimestamp(body.CommitTimestamp),
		}
		release, log, created, err := cd.ReceiveRunner(c.Request.Context(), b, ev)
		audit(c, store, "runner_image", b.Binding.ClusterID, b.Binding.Namespace, "runner_bindings", fmt.Sprint(b.ID), "build:"+build, auditErr(err))
		if err != nil {
			runnerError(c, err)
			return
		}
		status := 202
		if !created {
			status = 200
		}
		c.JSON(status, gin.H{"release_id": release.ID, "test_log_id": log.ID, "duplicate": !created, "status": log.Status, "result_url": fmt.Sprintf("/openapi/v1/runner/bindings/%d/releases/%d", b.ID, release.ID)})
	})
	ext.GET("/bindings/:id/releases/:release_id", func(c *gin.Context) {
		b, ok := runnerCredential(c, store)
		if !ok {
			return
		}
		id, ok := pathInt64(c, "release_id")
		if !ok {
			return
		}
		release, log, err := store.RunnerRelease(b, id)
		if err != nil {
			runnerError(c, err)
			return
		}
		c.JSON(200, gin.H{"release_id": release.ID, "status": log.Status, "current_step": log.CurrentStep, "error": log.ErrorMessage, "image": log.ImageFull, "finished_at": log.FinishedAt})
	})
	ext.POST("/bindings/:id/releases/:release_id/configuration", func(c *gin.Context) {
		b, ok := runnerCredential(c, store)
		if !ok {
			return
		}
		releaseID, ok := pathInt64(c, "release_id")
		if !ok {
			return
		}
		var body runnerConfigurationSourceBody
		if !runnerJSON(c, &body) {
			return
		}
		updated, created, err := store.UpdateRunnerReleaseConfigurationSource(b, releaseID, service.ConfigurationSourceRevision{
			Repository: body.Repository, Path: body.Path, Revision: body.Revision,
		})
		audit(c, store, "runner_configuration_source", b.Binding.ClusterID, b.Binding.Namespace, "releases", fmt.Sprint(releaseID), "configuration revision", auditErr(err))
		if err != nil {
			runnerError(c, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		c.JSON(status, updated.Metadata.ConfigurationSource)
	})
	ext.POST("/bindings/:id/releases/:release_id/verification", func(c *gin.Context) {
		b, ok := runnerCredential(c, store)
		if !ok {
			return
		}
		releaseID, ok := pathInt64(c, "release_id")
		if !ok {
			return
		}
		var body runnerEvidenceBody
		if !runnerJSON(c, &body) {
			return
		}
		release, _, err := store.RunnerRelease(b, releaseID)
		if err != nil {
			runnerError(c, err)
			return
		}
		if release.ImageDigest == nil {
			fail(c, 400, "制品没有固化 digest，不能上报验证证据")
			return
		}
		evidence := service.VerificationEvidence{
			Digest: *release.ImageDigest, Kind: body.Kind, Status: body.Status,
			ReportURL: body.ReportURL, Summary: body.Summary,
			RecordedBy: fmt.Sprintf("runner:%d", b.ID), ValidUntil: body.ValidUntil,
		}
		updated, err := store.UpdateRunnerReleaseEvidence(b, releaseID, evidence)
		audit(c, store, "runner_verification", b.Binding.ClusterID, b.Binding.Namespace, "releases", fmt.Sprint(releaseID), body.Kind+":"+body.Status, auditErr(err))
		if err != nil {
			runnerError(c, err)
			return
		}
		c.JSON(http.StatusOK, updated.Metadata)
	})
	ext.POST("/bindings/:id/releases/:release_id/scan", func(c *gin.Context) {
		b, ok := runnerCredential(c, store)
		if !ok {
			return
		}
		releaseID, ok := pathInt64(c, "release_id")
		if !ok {
			return
		}
		var body runnerScanBody
		if !runnerJSON(c, &body) {
			return
		}
		evidence := service.VerificationEvidence{
			Kind: "scan", Status: body.Status, Tool: body.Tool,
			ToolVersion: body.ToolVersion, RulesVersion: body.RulesVersion,
			ReportURL: body.ReportURL, Summary: body.Summary, ValidUntil: body.ValidUntil,
		}
		updated, err := store.UpdateRunnerReleaseScan(b, releaseID, evidence)
		audit(c, store, "runner_scan", b.Binding.ClusterID, b.Binding.Namespace, "releases", fmt.Sprint(releaseID), "scan evidence", auditErr(err))
		if err != nil {
			runnerError(c, err)
			return
		}
		c.JSON(http.StatusOK, updated.Metadata)
	})

	ops := api.Group("/runner", requireRole(store, service.RoleOperator))
	ops.GET("/bindings", func(c *gin.Context) { c.JSON(200, runnerList(store)) })
	// Connection metadata excludes the encrypted token and is needed when
	// operators bind a separate deployment pipeline to an environment.
	ops.GET("/connections", func(c *gin.Context) { c.JSON(200, store.YunxiaoConnections()) })
	configure := func(c *gin.Context) {
		id := int64(0)
		if c.Param("id") != "" {
			var ok bool
			id, ok = pathInt64(c, "id")
			if !ok {
				return
			}
		}
		var body runnerConfigBody
		if !runnerJSON(c, &body) {
			return
		}
		b, key, err := store.ConfigureRunner(body.binding(id))
		audit(c, store, "runner_configure", b.Binding.ClusterID, b.Binding.Namespace, "runner_bindings", fmt.Sprint(b.ID), "component-scoped authorization", auditErr(err))
		if err != nil {
			runnerError(c, err)
			return
		}
		scripts, err := service.GenerateRunnerScripts(b)
		if err != nil {
			runnerError(c, err)
			return
		}
		c.JSON(200, gin.H{"binding": b, "key": key, "scripts": scripts})
	}
	ops.POST("/bindings", configure)
	ops.PUT("/bindings/:id/config", configure)
	ops.PUT("/bindings/:id/enabled", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if !runnerJSON(c, &body) {
			return
		}
		b, err := store.SetRunnerEnabled(id, body.Enabled)
		audit(c, store, "runner_enabled", b.Binding.ClusterID, "", "runner_bindings", fmt.Sprint(id), fmt.Sprintf("enabled:%v", body.Enabled), auditErr(err))
		if err != nil {
			runnerError(c, err)
			return
		}
		c.JSON(200, b)
	})
	ops.GET("/bindings/:id/scripts", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		b, err := store.RunnerBinding(id)
		if err != nil {
			runnerError(c, err)
			return
		}
		if _, _, err = store.RunnerTarget(b); err != nil {
			runnerError(c, err)
			return
		}
		scripts, err := service.GenerateRunnerScripts(b)
		if err != nil {
			runnerError(c, err)
			return
		}
		c.JSON(200, scripts)
	})
	cloud := service.NewYunxiaoService(store, cipher)
	registerDeploymentPipelineSearchAPI(ops, store, cloud)
	registerDeploymentPipelineCheckAPI(ops, store, cloud)
	registerDeploymentPipelineScopeCheckAPI(ops, store, cloud)
	registerDeploymentPipelineScopeReviewAPI(ops, store, cloud)
	admin := ops.Group("", requireRole(store, service.RoleAdmin))
	admin.POST("/connections", func(c *gin.Context) {
		var body struct {
			Name           string `json:"name"`
			Edition        string `json:"edition"`
			Domain         string `json:"domain"`
			OrganizationID string `json:"organization_id"`
			Token          string `json:"token"`
		}
		if !runnerJSON(c, &body) {
			return
		}
		if cipher == nil {
			fail(c, 503, "请先配置后端凭据加密密钥")
			return
		}
		if len(body.Token) > 2048 || strings.TrimSpace(body.Token) == "" || strings.ContainsAny(body.Token, "\r\n") {
			fail(c, 400, "访问令牌无效")
			return
		}
		sealed, err := cipher.Encrypt(body.Token)
		if err != nil {
			runnerError(c, err)
			return
		}
		conn, err := store.SaveYunxiaoConnection(service.YunxiaoConnection{Name: strings.TrimSpace(body.Name), Edition: body.Edition, Domain: strings.ToLower(strings.TrimSpace(body.Domain)), OrganizationID: strings.TrimSpace(body.OrganizationID), TokenCiphertext: sealed})
		audit(c, store, "yunxiao_connection", 0, "", "connections", body.Name, "cloud credentials configured", auditErr(err))
		if err != nil {
			runnerError(c, err)
			return
		}
		c.JSON(201, conn)
	})
	getBinding := func(c *gin.Context) (service.RunnerBinding, bool) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return service.RunnerBinding{}, false
		}
		b, err := store.RunnerBinding(id)
		if err != nil {
			runnerError(c, err)
			return b, false
		}
		return b, true
	}
	admin.POST("/bindings/:id/pipeline-preview", func(c *gin.Context) {
		b, ok := getBinding(c)
		if !ok {
			return
		}
		preview, err := cloud.Preview(c.Request.Context(), b)
		if err != nil {
			runnerError(c, err)
			return
		}
		c.JSON(200, preview)
	})
	admin.POST("/bindings/:id/pipeline-apply", func(c *gin.Context) {
		b, ok := getBinding(c)
		if !ok {
			return
		}
		var body struct {
			Fingerprint string `json:"fingerprint"`
			ChangeHash  string `json:"change_hash"`
		}
		if !runnerJSON(c, &body) {
			return
		}
		changed, err := cloud.Apply(c.Request.Context(), b, body.Fingerprint, body.ChangeHash)
		audit(c, store, "yunxiao_pipeline_apply", b.Binding.ClusterID, "", "runner_bindings", fmt.Sprint(b.ID), fmt.Sprintf("pipeline:%s changed:%v", b.PipelineID, changed), auditErr(err))
		if err != nil {
			runnerError(c, err)
			return
		}
		c.JSON(200, gin.H{"saved": true, "changed": changed})
	})
	admin.GET("/bindings/:id/pipeline-backup", func(c *gin.Context) {
		b, ok := getBinding(c)
		if !ok {
			return
		}
		if b.PipelineBackupCiphertext == "" {
			fail(c, 404, "尚无流水线备份")
			return
		}
		if cipher == nil {
			fail(c, 503, "凭据加密密钥未配置")
			return
		}
		before, err := cipher.Decrypt(b.PipelineBackupCiphertext)
		if err != nil {
			fail(c, 500, "流水线备份解密失败")
			return
		}
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=pipeline-%s-before.yaml", b.PipelineID))
		c.Data(200, "application/yaml; charset=utf-8", []byte(before))
	})
}
