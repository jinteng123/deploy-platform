package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"deploy-platform/backend-go/internal/config"
	"deploy-platform/backend-go/internal/service"

	"github.com/gin-gonic/gin"
)

// webhookAuthorized enforces the shared webhook token when configured. A
// configured token accepts either `Authorization: Bearer <token>` or
// `X-Webhook-Token: <token>`. Without a token only mock mode is allowed to
// receive events; a real runtime must not accept unsigned webhooks.
func webhookAuthorized(c *gin.Context, cfg config.Config) bool {
	if cfg.YunxiaoWebhookToken == "" {
		if !cfg.MockRuntime {
			fail(c, http.StatusServiceUnavailable, "不安全的 Webhook 未配置")
			return false
		}
		return true
	}
	token := c.GetHeader("X-Webhook-Token")
	if auth := c.GetHeader("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(cfg.YunxiaoWebhookToken)) != 1 {
		fail(c, http.StatusUnauthorized, "非法 webhook token")
		return false
	}
	return true
}

type customEventBody struct {
	EventID            string   `json:"event_id"`
	SourceRepository   string   `json:"source_repository"`
	SourceRepositories []string `json:"source_repositories"`
	SourcePath         string   `json:"source_path"`
	TargetCluster      string   `json:"target_cluster"`
	AppName            string   `json:"app_name"`
	ImageFull          string   `json:"image_full"`
	ImageDigest        *string  `json:"image_digest"`
	PipelineID         *string  `json:"pipeline_id"`
	PipelineName       *string  `json:"pipeline_name"`
	BuildNumber        *string  `json:"build_number"`
	Branch             *string  `json:"branch"`
	CommitID           *string  `json:"commit_id"`
	CommitTimestamp    string   `json:"commit_timestamp"`
}

type yunxiaoEventBody struct {
	Event  string `json:"event"`
	Action string `json:"action"`
	Task   struct {
		PipelineID   json.RawMessage `json:"pipelineId"`
		PipelineName string          `json:"pipelineName"`
		BuildNumber  json.RawMessage `json:"buildNumber"`
		StatusCode   string          `json:"statusCode"`
	} `json:"task"`
	Sources []struct {
		Repo     string `json:"repo"`
		Branch   string `json:"branch"`
		CommitID string `json:"commitId"`
	} `json:"sources"`
	GlobalParams []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"globalParams"`
}

// rawToString renders a JSON scalar (string or number) as a plain string.
func rawToString(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String()
	}
	return ""
}

func strPtrOrNil(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

// parseCommitTimestamp accepts RFC3339 commit metadata when the CI source
// provides it. Invalid or absent telemetry is ignored so it cannot block an
// otherwise valid image event.
func parseCommitTimestamp(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	parsed = parsed.UTC()
	return &parsed
}

// parseImageEvent accepts both payload shapes: the custom fixed JSON and the
// standard Yunxiao pipeline webhook. Standard events that did not succeed are
// reported as ignored rather than errors.
func parseImageEvent(c *gin.Context) (service.ImageEvent, bool, bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	body, err := c.GetRawData()
	if err != nil || len(body) == 0 {
		fail(c, http.StatusBadRequest, "请求体不能为空")
		return service.ImageEvent{}, false, false
	}
	var probe struct {
		Task         json.RawMessage `json:"task"`
		GlobalParams json.RawMessage `json:"globalParams"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		fail(c, http.StatusBadRequest, "非法 JSON")
		return service.ImageEvent{}, false, false
	}
	if len(probe.Task) == 0 && len(probe.GlobalParams) == 0 {
		var b customEventBody
		if err := json.Unmarshal(body, &b); err != nil {
			fail(c, http.StatusBadRequest, "非法 JSON")
			return service.ImageEvent{}, false, false
		}
		sourceRepositories := append([]string(nil), b.SourceRepositories...)
		if strings.TrimSpace(b.SourceRepository) != "" {
			sourceRepositories = append(sourceRepositories, b.SourceRepository)
		}
		return service.ImageEvent{
			EventID:            b.EventID,
			SourceRepositories: sourceRepositories,
			SourcePath:         b.SourcePath,
			Source:             "custom",
			TargetCluster:      b.TargetCluster,
			AppName:            b.AppName,
			ImageFull:          b.ImageFull,
			ImageDigest:        b.ImageDigest,
			PipelineID:         b.PipelineID,
			PipelineName:       b.PipelineName,
			BuildNumber:        b.BuildNumber,
			Branch:             b.Branch,
			CommitID:           b.CommitID,
			CommitTimestamp:    parseCommitTimestamp(b.CommitTimestamp),
		}, true, false
	}

	var y yunxiaoEventBody
	if err := json.Unmarshal(body, &y); err != nil {
		fail(c, http.StatusBadRequest, "非法 JSON")
		return service.ImageEvent{}, false, false
	}
	if !strings.EqualFold(y.Task.StatusCode, "SUCCESS") {
		c.JSON(http.StatusAccepted, gin.H{"ignored": true, "status": y.Task.StatusCode})
		return service.ImageEvent{}, false, true
	}
	params := map[string]string{}
	for _, p := range y.GlobalParams {
		key := strings.ToUpper(strings.TrimSpace(p.Key))
		if key == "APP_NAME" || key == "IMAGE_FULL" || key == "IMAGE_DIGEST" || key == "TARGET_CLUSTER" || key == "SOURCE_PATH" || key == "COMMIT_TIMESTAMP" {
			params[key] = strings.TrimSpace(p.Value)
		}
	}
	pipelineID := rawToString(y.Task.PipelineID)
	buildNumber := rawToString(y.Task.BuildNumber)
	if pipelineID == "" || buildNumber == "" {
		fail(c, http.StatusBadRequest, "云效事件缺少 pipelineId 或 buildNumber")
		return service.ImageEvent{}, false, false
	}
	appName := params["APP_NAME"]
	imageFull := params["IMAGE_FULL"]
	targetCluster := params["TARGET_CLUSTER"]
	if appName == "" || imageFull == "" || targetCluster == "" {
		fail(c, http.StatusBadRequest, "globalParams 缺少 APP_NAME、IMAGE_FULL 或 TARGET_CLUSTER")
		return service.ImageEvent{}, false, false
	}
	ev := service.ImageEvent{
		EventID:         pipelineID + ":" + buildNumber,
		Source:          "yunxiao",
		TargetCluster:   targetCluster,
		SourcePath:      params["SOURCE_PATH"],
		PipelineID:      strPtrOrNil(pipelineID),
		PipelineName:    strPtrOrNil(y.Task.PipelineName),
		BuildNumber:     strPtrOrNil(buildNumber),
		AppName:         appName,
		ImageFull:       imageFull,
		ImageDigest:     strPtrOrNil(params["IMAGE_DIGEST"]),
		CommitTimestamp: parseCommitTimestamp(params["COMMIT_TIMESTAMP"]),
	}
	for _, source := range y.Sources {
		if strings.TrimSpace(source.Repo) != "" {
			ev.SourceRepositories = append(ev.SourceRepositories, source.Repo)
		}
	}
	if len(y.Sources) > 0 {
		ev.Branch = strPtrOrNil(y.Sources[0].Branch)
		ev.CommitID = strPtrOrNil(y.Sources[0].CommitID)
	}
	return ev, true, false
}

func writeCDErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrValidation):
		fail(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, service.ErrConflict):
		fail(c, http.StatusConflict, err.Error())
	case errors.Is(err, service.ErrNotFound):
		fail(c, http.StatusNotFound, err.Error())
	case errors.Is(err, service.ErrForbidden):
		fail(c, http.StatusForbidden, err.Error())
	default:
		fail(c, http.StatusInternalServerError, err.Error())
	}
}

// registerCDWebhook registers the token-authed Yunxiao receiver on the
// PUBLIC group — it authenticates by webhook secret, not user session.
func registerCDWebhook(api *gin.RouterGroup, store *service.Store, cfg config.Config, cd *service.CDService) {
	api.POST("/webhooks/yunxiao", func(c *gin.Context) {
		if !webhookAuthorized(c, cfg) {
			return
		}
		ev, ok, ignored := parseImageEvent(c)
		if !ok || ignored {
			return
		}
		release, deployLog, created, err := cd.Receive(c.Request.Context(), ev)
		// Unauthenticated token path — audit shows username "-" plus the
		// release it triggered; no secret material in detail.
		audit(c, store, "webhook_deploy", release.TestClusterID, "", "releases", ev.AppName, ev.ImageFull, auditErr(err))
		if err != nil {
			writeCDErr(c, err)
			return
		}
		if !created {
			c.JSON(http.StatusOK, gin.H{
				"release_id":  release.ID,
				"duplicate":   true,
				"test_log_id": release.TestDeployLogID,
			})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{
			"release_id":  release.ID,
			"duplicate":   false,
			"test_log_id": deployLog.ID,
			"status":      deployLog.Status,
		})
	})
}

func registerCDAPI(api *gin.RouterGroup, store *service.Store, cfg config.Config, cd *service.CDService, k8s *service.K8sService) {
	api.GET("/releases/:id", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		r, err := cd.Get(id)
		if err != nil {
			writeCDErr(c, err)
			return
		}
		c.JSON(http.StatusOK, r)
	})
	api.POST("/releases/:id/verification", requireRole(store, service.RoleOperator), func(c *gin.Context) { recordEvidence(c, store, false) })
	api.POST("/releases/:id/scan", requireRole(store, service.RoleOperator), func(c *gin.Context) { recordEvidence(c, store, true) })
	api.GET("/releases", func(c *gin.Context) {
		limit, ok := queryIntRange(c, "limit", 50, 1, 200)
		if !ok {
			return
		}
		c.JSON(http.StatusOK, cd.List(strings.TrimSpace(c.Query("app_name")), limit))
	})

	api.GET("/releases/:id/promotion-targets", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		targets, err := cd.PromotionTargets(id)
		if err != nil {
			writeCDErr(c, err)
			return
		}
		out := make([]clusterOut, 0, len(targets))
		for _, cl := range targets {
			out = append(out, toClusterOut(cl))
		}
		c.JSON(http.StatusOK, out)
	})

	// current-image answers "what is this app running on that cluster now"
	// — the promote dialog's current-vs-candidate comparison. It resolves
	// through the release's app mapping on the target cluster, so a cluster
	// without a mapping gets a 404 just as PromotionTargets excludes it.
	api.GET("/releases/:id/current-image", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		clusterID, ok := queryIntOpt(c, "cluster_id")
		if !ok {
			return
		}
		if clusterID <= 0 {
			fail(c, http.StatusBadRequest, "cluster_id 必填")
			return
		}
		release, err := store.FindImageRelease(id)
		if err != nil {
			fail(c, http.StatusNotFound, "发布单不存在")
			return
		}
		app, err := store.FindAppByName(clusterID, release.AppName)
		if err != nil {
			fail(c, http.StatusNotFound, "该集群未配置此应用")
			return
		}
		cluster, err := store.FindCluster(clusterID)
		if err != nil {
			fail(c, http.StatusNotFound, err.Error())
			return
		}
		rt, err := k8s.For(cluster)
		if err != nil {
			if errors.Is(err, service.ErrClusterDisabled) {
				fail(c, http.StatusForbidden, err.Error())
			} else {
				fail(c, http.StatusBadGateway, err.Error())
			}
			return
		}
		image, err := rt.CurrentImage(c.Request.Context(), app.Namespace, app.DeploymentName, app.ContainerName)
		if err != nil {
			fail(c, http.StatusBadGateway, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"image": image, "app": app})
	})

	api.POST("/releases/:id/promote", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		promoteRelease(c, store, cd, cfg)
	})
	api.GET("/promotion-requests", func(c *gin.Context) {
		rows, err := cd.ListPromotionRequests(strings.TrimSpace(c.Query("status")), 100)
		if err != nil {
			writeCDErr(c, err)
			return
		}
		decoratePromotionApprovalEligibility(store, rows, currentUser(c))
		c.JSON(http.StatusOK, rows)
	})
	api.POST("/promotion-requests/:id/approve", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		approvePromotion(c, store, cd)
	})
	api.POST("/promotion-requests/:id/reject", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		rejectPromotion(c, store, cd)
	})
}

// decoratePromotionApprovalEligibility provides a session-specific UI hint.
// Approval and rejection still re-check authorization in CDService at write time.
func decoratePromotionApprovalEligibility(store *service.Store, rows []service.PromotionRequest, user *service.User) {
	if user == nil {
		return
	}
	for i := range rows {
		allowed := false
		if rows[i].Status == "pending" && rows[i].Requester != user.Username {
			release, err := store.FindImageRelease(rows[i].ReleaseID)
			if err == nil {
				app, appErr := store.FindAppByName(rows[i].ProdClusterID, release.AppName)
				if appErr == nil {
					allowed, _ = store.CanApproveDeliveryTarget(user.Username, app.ID)
				}
			}
		}
		rows[i].CanApprove = &allowed
	}
}

// promoteRelease is the promote handler body shared by the session API and
// the external /openapi/v1 surface. With promotion approval enabled the call
// files a pending request instead of deploying — a second identity approves.
func promoteRelease(c *gin.Context, store *service.Store, cd *service.CDService, cfg config.Config) {
	id, ok := pathInt64(c, "id")
	if !ok {
		return
	}
	var b struct {
		ClusterID int `json:"cluster_id" binding:"required,min=1"`
	}
	if !bindJSON(c, &b) {
		return
	}
	detail := ""
	if rel, rerr := store.FindImageRelease(id); rerr == nil {
		detail = rel.AppName + "@" + rel.ImageFull
	}
	if cfg.RequiresProductionApproval() {
		requester := "-"
		if u := currentUser(c); u != nil {
			requester = u.Username
		}
		req, err := cd.RequestPromotion(id, b.ClusterID, requester)
		audit(c, store, "promotion_request", b.ClusterID, "", "releases", strconv.FormatInt(id, 10), detail, auditErr(err))
		if err != nil {
			writeCDErr(c, err)
			return
		}
		c.JSON(http.StatusAccepted, gin.H{
			"request_id":      req.ID,
			"release_id":      id,
			"prod_cluster_id": req.ProdClusterID,
			"status":          req.Status,
			"expires_at":      req.ExpiresAt,
		})
		return
	}
	release, deployLog, err := cd.Promote(c.Request.Context(), id, b.ClusterID)
	// Production promotion is the highest-impact CD action — always
	// audited, success or failure. Image refs are recorded (not
	// secret-bearing); no credential material ever reaches detail.
	audit(c, store, "promote", b.ClusterID, "", "releases", strconv.FormatInt(id, 10), detail, auditErr(err))
	if err != nil {
		writeCDErr(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{
		"release_id":  release.ID,
		"prod_log_id": deployLog.ID,
		"status":      deployLog.Status,
	})
}

// approvePromotion executes a pending request as a second identity — the
// requester may not approve their own request (403 from the service).
func approvePromotion(c *gin.Context, store *service.Store, cd *service.CDService) {
	reqID, ok := pathInt64(c, "id")
	if !ok {
		return
	}
	approver := "-"
	if u := currentUser(c); u != nil {
		approver = u.Username
	}
	req, deployLog, err := cd.ApprovePromotion(c.Request.Context(), reqID, approver)
	detail := req.Requester
	if rel, rerr := store.FindImageRelease(req.ReleaseID); rerr == nil {
		detail = req.Requester + " → " + rel.AppName + "@" + rel.ImageFull
	}
	audit(c, store, "promotion_approve", req.ProdClusterID, "", "promotion_requests", strconv.FormatInt(reqID, 10), detail, auditErr(err))
	if err != nil {
		writeCDErr(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{
		"request_id":  req.ID,
		"prod_log_id": deployLog.ID,
		"status":      deployLog.Status,
	})
}

// rejectPromotion closes a pending request; the requester withdrawing their
// own request records "cancelled", anyone else records "rejected".
func rejectPromotion(c *gin.Context, store *service.Store, cd *service.CDService) {
	reqID, ok := pathInt64(c, "id")
	if !ok {
		return
	}
	var b struct {
		Comment string `json:"comment"`
	}
	if !bindJSON(c, &b) {
		return
	}
	actor := "-"
	if u := currentUser(c); u != nil {
		actor = u.Username
	}
	req, err := cd.RejectPromotion(reqID, actor, strings.TrimSpace(b.Comment))
	audit(c, store, "promotion_reject", req.ProdClusterID, "", "promotion_requests", strconv.FormatInt(reqID, 10), req.Requester, auditErr(err))
	if err != nil {
		writeCDErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"request_id": req.ID, "status": req.Status})
}

// Evidence is attested by the authenticated caller, never inferred from rollout.
func recordEvidence(c *gin.Context, store *service.Store, scan bool) {
	id, ok := pathInt64(c, "id")
	if !ok {
		return
	}
	var e service.VerificationEvidence
	if !bindJSON(c, &e) {
		return
	}
	u := currentUser(c)
	if e.Kind == "manual" && strings.HasPrefix(u.Username, "apikey:") {
		writeCDErr(c, service.NewValidationError("人工验收需要人工账号"))
		return
	}
	e.RecordedBy = u.Username
	r, err := store.UpdateReleaseEvidence(id, e, scan)
	action := "release_verification"
	if scan {
		action = "release_scan"
	}
	audit(c, store, action, 0, "", "releases", strconv.FormatInt(id, 10), e.Status, auditErr(err))
	if err != nil {
		writeCDErr(c, err)
		return
	}
	c.JSON(http.StatusOK, r.Metadata)
}
