package httpapi

import (
	"net/http"
	"strconv"

	"deploy-platform/backend-go/internal/service"
	"github.com/gin-gonic/gin"
)

func registerDeliveryPlanAPI(api *gin.RouterGroup, store *service.Store, cd *service.CDService) {
	api.GET("/delivery-plans", func(c *gin.Context) {
		c.JSON(http.StatusOK, store.DeliveryPlans())
	})
	api.GET("/delivery-plans/:id", func(c *gin.Context) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			fail(c, http.StatusBadRequest, "发布计划 ID 无效")
			return
		}
		plan, err := store.FindDeliveryPlan(id)
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusOK, plan)
	})
	api.POST("/delivery-plans/:id/request-approval", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var body struct {
			ManualReviewAcknowledged bool `json:"manual_review_acknowledged"`
		}
		if !bindJSON(c, &body) {
			return
		}
		requester := "-"
		if user := currentUser(c); user != nil {
			requester = user.Username
		}
		plan, _ := store.FindDeliveryPlan(id)
		req, err := cd.RequestDeliveryPlanApproval(c.Request.Context(), id, requester, body.ManualReviewAcknowledged)
		detail := "request_id=" + strconv.FormatInt(req.ID, 10)
		if body.ManualReviewAcknowledged {
			detail += "; manual_preflight_acknowledged=true"
		}
		if err != nil {
			detail = "approval request failed"
		}
		audit(c, store, "delivery_plan_request_approval", plan.TargetClusterID, "", "delivery_plans", strconv.FormatInt(id, 10), detail, auditErr(err))
		if err != nil {
			writeCDErr(c, err)
			return
		}
		c.JSON(http.StatusAccepted, gin.H{
			"request_id":       req.ID,
			"delivery_plan_id": req.DeliveryPlanID,
			"release_id":       req.ReleaseID,
			"prod_cluster_id":  req.ProdClusterID,
			"status":           req.Status,
			"expires_at":       req.ExpiresAt,
		})
	})
	api.POST("/delivery-plans", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		var body struct {
			ReleaseID int64 `json:"release_id" binding:"required,min=1"`
			ClusterID int   `json:"cluster_id" binding:"required,min=1"`
		}
		if !bindJSON(c, &body) {
			return
		}
		user := currentUser(c)
		requester := "unknown"
		if user != nil {
			requester = user.Username
		}
		plan, created, err := cd.CreateDeliveryPlan(c.Request.Context(), body.ReleaseID, body.ClusterID, requester)
		resourceID := strconv.FormatInt(body.ReleaseID, 10)
		detail := "release_id=" + resourceID
		if err == nil {
			resourceID = strconv.FormatInt(plan.ID, 10)
			detail = plan.SnapshotHash
		} else {
			detail += "; " + err.Error()
		}
		audit(c, store, "delivery_plan_preview", body.ClusterID, "", "delivery_plans", resourceID, detail, auditErr(err))
		if err != nil {
			writeCDErr(c, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		c.JSON(status, plan)
	})
}
