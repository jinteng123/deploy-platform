package httpapi

import (
	"net/http"
	"strconv"

	"deploy-platform/backend-go/internal/service"
	"github.com/gin-gonic/gin"
)

func registerDeliveryRunAPI(api *gin.RouterGroup, store *service.Store, cd *service.CDService) {
	api.GET("/delivery-runs/by-deploy-log/:id", func(c *gin.Context) {
		logID, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		run, err := store.DeliveryRunForDeployLog(logID)
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		view, err := store.DeliveryRunView(run.ID)
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusOK, view)
	})
	api.GET("/delivery-runs/:id", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		view, err := store.DeliveryRunView(id)
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusOK, view)
	})
	api.POST("/delivery-runs/:id/actions", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var body struct {
			Action                            string `json:"action" binding:"required"`
			ExpectedVersion                   int64  `json:"expected_version" binding:"required,min=1"`
			IdempotencyKey                    string `json:"idempotency_key" binding:"required,min=8,max=120"`
			RecoveryCompatibilityAcknowledged bool   `json:"recovery_compatibility_acknowledged"`
		}
		if !bindJSON(c, &body) {
			return
		}
		actor := "-"
		if user := currentUser(c); user != nil {
			actor = user.Username
		}
		view, err := cd.ActOnDeliveryRunWithOptions(c.Request.Context(), id, body.Action, body.ExpectedVersion, body.IdempotencyKey, actor, service.DeliveryRunControlOptions{
			RecoveryCompatibilityAcknowledged: body.RecoveryCompatibilityAcknowledged,
		})
		detail := "action=" + body.Action + ";expected_version=" + strconv.FormatInt(body.ExpectedVersion, 10)
		if body.RecoveryCompatibilityAcknowledged && (body.Action == service.DeliveryActionRestoreStable || body.Action == service.DeliveryActionCancel) {
			detail += ";recovery_compatibility_acknowledged=true"
		}
		if err != nil {
			detail += "; " + err.Error()
		}
		audit(c, store, "delivery_run_control", view.Plan.TargetClusterID, view.Plan.Snapshot.Target.Namespace, "delivery_runs", strconv.FormatInt(id, 10), detail, auditErr(err))
		if err != nil {
			writeCDErr(c, err)
			return
		}
		c.JSON(http.StatusOK, view)
	})
}
