package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"deploy-platform/backend-go/internal/service"
	"github.com/gin-gonic/gin"
)

func fmtInt64(v int64) string { return strconv.FormatInt(v, 10) }

func registerDeliveryModelAPI(api *gin.RouterGroup, store *service.Store, cd *service.CDService) {
	api.GET("/delivery/model", func(c *gin.Context) {
		c.JSON(http.StatusOK, store.DeliveryModel())
	})

	api.GET("/delivery/approval-grants", requireRole(store, service.RoleAdmin), func(c *gin.Context) {
		grants, err := store.DeliveryApprovalGrants()
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusOK, grants)
	})

	api.PUT("/delivery/approval-grants/users/:id", requireRole(store, service.RoleAdmin), func(c *gin.Context) {
		userID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var body struct {
			Scopes []service.DeliveryApprovalScope `json:"scopes"`
		}
		if !bindJSON(c, &body) {
			return
		}
		if body.Scopes == nil {
			fail(c, http.StatusBadRequest, "scopes 必须是数组；空数组用于撤销全部范围")
			return
		}
		actor := "unknown"
		if user := currentUser(c); user != nil {
			actor = user.Username
		}
		grants, err := store.ReplaceDeliveryApprovalGrants(userID, body.Scopes, actor)
		detail := fmt.Sprintf("user_id=%d; scopes=%d", userID, len(grants))
		if err != nil {
			detail = fmt.Sprintf("user_id=%d; approval scope update failed", userID)
		}
		audit(c, store, "delivery_approval_scope_update", 0, "", "delivery_approval_grants", strconv.FormatInt(userID, 10), detail, auditErr(err))
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusOK, grants)
	})

	api.GET("/environments/:id/evidence-policy", requireRole(store, service.RoleViewer), func(c *gin.Context) {
		environmentID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		history, err := store.DeliveryEnvironmentEvidencePolicyHistory(environmentID)
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusOK, history)
	})

	api.PUT("/environments/:id/evidence-policy", requireRole(store, service.RoleAdmin), func(c *gin.Context) {
		environmentID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var body struct {
			ExpectedVersion           *int64              `json:"expected_version" binding:"required,min=0"`
			RequiredVerificationKinds []string            `json:"required_verification_kinds" binding:"required,min=1,max=3"`
			MaxEvidenceAgeMinutes     int                 `json:"max_evidence_age_minutes" binding:"required,min=1,max=10080"`
			TrustedSubmitters         map[string][]string `json:"trusted_submitters" binding:"required"`
		}
		if !bindJSON(c, &body) {
			return
		}
		actor := "unknown"
		if user := currentUser(c); user != nil {
			actor = user.Username
		}
		policy, created, err := store.SaveDeliveryEnvironmentEvidencePolicy(environmentID, *body.ExpectedVersion, body.RequiredVerificationKinds, body.MaxEvidenceAgeMinutes, body.TrustedSubmitters, actor)
		detail := fmt.Sprintf("version=%d; kinds=%s; max_age_minutes=%d; created=%t", policy.Version, strings.Join(policy.RequiredVerificationKinds, ","), policy.MaxEvidenceAgeMinutes, created)
		if err != nil {
			detail = "delivery evidence policy save failed"
		}
		audit(c, store, "delivery_environment_evidence_policy_save", 0, "", "delivery_environment_evidence_policy_versions", fmtInt64(environmentID), detail, auditErr(err))
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusOK, policy)
	})

	api.GET("/apps/:id/delivery-policy", requireRole(store, service.RoleViewer), func(c *gin.Context) {
		appID, ok := pathInt(c, "id")
		if !ok {
			return
		}
		history, err := store.DeliveryPolicyHistoryForApp(appID)
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusOK, history)
	})

	api.PUT("/apps/:id/delivery-policy", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		appID, ok := pathInt(c, "id")
		if !ok {
			return
		}
		var body struct {
			ExpectedVersion         *int64                        `json:"expected_version" binding:"required,min=0"`
			Strategy                string                        `json:"strategy" binding:"required"`
			ServiceName             string                        `json:"service_name"`
			CandidateRetentionHours int                           `json:"candidate_retention_hours"`
			CanaryAnalysis          *service.CanaryAnalysisPolicy `json:"canary_analysis"`
		}
		if !bindJSON(c, &body) {
			return
		}
		actor := "unknown"
		if user := currentUser(c); user != nil {
			actor = user.Username
		}
		policy, created, err := store.SaveDeliveryPolicyVersion(appID, *body.ExpectedVersion, body.Strategy, body.ServiceName, body.CandidateRetentionHours, actor, body.CanaryAnalysis)
		detail := fmt.Sprintf("version=%d; strategy=%s; created=%t", policy.Version, policy.Strategy, created)
		if err != nil {
			detail = "delivery policy version save failed"
		}
		audit(c, store, "delivery_policy_version_save", 0, "", "delivery_policy_versions", fmt.Sprintf("app=%d", appID), detail, auditErr(err))
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusOK, policy)
	})

	api.POST("/delivery-preview", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		var body struct {
			ClusterID  int    `json:"cluster_id" binding:"required,min=1"`
			Namespace  string `json:"namespace" binding:"required"`
			Deployment string `json:"deployment_name" binding:"required"`
		}
		if !bindJSON(c, &body) {
			return
		}
		preview, err := cd.PreviewDeliveryCapabilities(c.Request.Context(), body.ClusterID, body.Namespace, body.Deployment, currentUser(c))
		detail := "read-only Kubernetes capability observation"
		if err != nil {
			detail = "delivery capability preview failed"
		}
		audit(c, store, "delivery_capability_preview", body.ClusterID, "", "delivery_preview", body.Namespace+"/"+body.Deployment, detail, auditErr(err))
		if err != nil {
			writeCDErr(c, err)
			return
		}
		c.JSON(http.StatusOK, preview)
	})

	api.POST("/environments", requireRole(store, service.RoleAdmin), func(c *gin.Context) {
		var body struct {
			Name string `json:"name" binding:"required"`
			Tier string `json:"environment_tier" binding:"required"`
		}
		if !bindJSON(c, &body) {
			return
		}
		row, err := store.CreateDeliveryEnvironment(body.Name, body.Tier)
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		audit(c, store, "delivery_environment_create", 0, "", "delivery_environments", fmtInt64(row.ID), "created logical environment", "success")
		c.JSON(http.StatusCreated, row)
	})

	api.PUT("/environments/:id", requireRole(store, service.RoleAdmin), func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var body struct {
			Name string `json:"name" binding:"required"`
		}
		if !bindJSON(c, &body) {
			return
		}
		row, err := store.ConfirmDeliveryEnvironment(id, body.Name)
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		audit(c, store, "delivery_environment_confirm", 0, "", "delivery_environments", fmtInt64(row.ID), "confirmed environment name", "success")
		c.JSON(http.StatusOK, row)
	})

	api.POST("/applications", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		var body struct {
			Name string `json:"name" binding:"required"`
		}
		if !bindJSON(c, &body) {
			return
		}
		row, err := store.CreateDeliveryApplication(body.Name)
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusCreated, row)
	})

	api.POST("/applications/:id/components", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var body struct {
			Name string `json:"name" binding:"required"`
		}
		if !bindJSON(c, &body) {
			return
		}
		row, err := store.CreateDeliveryComponent(id, body.Name)
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusCreated, row)
	})

	api.PUT("/apps/:id/delivery-binding", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		id, ok := pathInt(c, "id")
		if !ok {
			return
		}
		var body struct {
			ApplicationID           int64                                     `json:"application_id" binding:"required,min=1"`
			ComponentID             int64                                     `json:"component_id" binding:"required,min=1"`
			EnvironmentID           int64                                     `json:"environment_id" binding:"required,min=1"`
			StrategyDraft           string                                    `json:"strategy_draft"`
			ServiceName             string                                    `json:"service_name"`
			ExpectedPolicyVersion   *int64                                    `json:"expected_policy_version" binding:"omitempty,min=0"`
			Strategy                string                                    `json:"strategy"`
			CandidateRetentionHours int                                       `json:"candidate_retention_hours"`
			CanaryAnalysis          *service.CanaryAnalysisPolicy             `json:"canary_analysis"`
			DeploymentPipeline      *service.YunxiaoDeploymentPipelineBinding `json:"deployment_pipeline"`
		}
		if !bindJSON(c, &body) {
			return
		}
		var row service.DeliveryBinding
		var policy service.DeliveryPolicyVersion
		var policyCreated bool
		var err error
		if body.ExpectedPolicyVersion != nil {
			if body.Strategy == "" {
				writeStoreErr(c, service.NewValidationError("原子保存绑定时必须提供 strategy"))
				return
			}
			actor := "unknown"
			if user := currentUser(c); user != nil {
				actor = user.Username
			}
			row, policy, policyCreated, err = store.AssignDeliveryBindingWithPolicyAndDeploymentPipeline(id, body.ApplicationID, body.ComponentID, body.EnvironmentID, body.StrategyDraft, body.ServiceName, *body.ExpectedPolicyVersion, body.Strategy, body.CandidateRetentionHours, actor, body.DeploymentPipeline, body.CanaryAnalysis)
		} else {
			row, err = store.AssignDeliveryBindingWithDeploymentPipeline(id, body.ApplicationID, body.ComponentID, body.EnvironmentID, body.StrategyDraft, body.ServiceName, body.DeploymentPipeline)
		}
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		target, _ := store.FindApp(id)
		pipelineIdentity := ""
		if row.DeploymentPipeline != nil {
			pipelineIdentity = fmt.Sprintf("yunxiao:%d/%s", row.DeploymentPipeline.ConnectionID, row.DeploymentPipeline.PipelineID)
		}
		detail := strings.Join([]string{strconv.Itoa(row.AppID), fmtInt64(row.ApplicationID), fmtInt64(row.ComponentID), fmtInt64(row.EnvironmentID), row.StrategyDraft, row.ServiceName, pipelineIdentity}, "/")
		audit(c, store, "delivery_binding_assign", target.ClusterID, target.AppName, "delivery_bindings", fmtInt64(row.ID), detail, "success")
		if body.ExpectedPolicyVersion != nil {
			policyDetail := fmt.Sprintf("version=%d; strategy=%s; created=%t", policy.Version, policy.Strategy, policyCreated)
			audit(c, store, "delivery_policy_version_save", target.ClusterID, target.AppName, "delivery_policy_versions", fmtInt64(row.ID), policyDetail, "success")
		}
		c.JSON(http.StatusOK, row)
	})

	api.POST("/apps/:id/delivery-binding/rebind-workload", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		id, ok := pathInt(c, "id")
		if !ok {
			return
		}
		row, err := cd.RebindDeliveryBindingWorkloadRef(c.Request.Context(), id)
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		target, _ := store.FindApp(id)
		detail := fmt.Sprintf("active=%s/%s/%s/uid=%s", row.ActiveWorkloadRef.Namespace, row.ActiveWorkloadRef.Kind, row.ActiveWorkloadRef.Name, row.ActiveWorkloadRef.UID)
		audit(c, store, "delivery_binding_rebind_workload", target.ClusterID, target.AppName, "delivery_bindings", fmtInt64(row.ID), detail, "success")
		c.JSON(http.StatusOK, row)
	})
}
