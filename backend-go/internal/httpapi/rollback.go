package httpapi

import (
	"net/http"
	"strconv"

	"deploy-platform/backend-go/internal/service"

	"github.com/gin-gonic/gin"
)

type rollbackBody struct {
	ClusterID   int   `json:"cluster_id" binding:"required,min=1"`
	TargetLogID int64 `json:"target_log_id" binding:"required,min=1"`
}

func registerRollbackAPI(api *gin.RouterGroup, store *service.Store, worker *service.Worker) {
	api.POST("/rollback", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		submitRollback(c, store, worker)
	})

	api.GET("/rollback/history/:app_name", func(c *gin.Context) {
		clusterID, ok := queryIntOpt(c, "cluster_id")
		if !ok {
			return
		}
		limit, ok := queryIntRange(c, "limit", 10, 1, 50)
		if !ok {
			return
		}
		records := store.Logs(clusterID, c.Param("app_name"), "success", limit)
		out := make([]gin.H, 0, len(records))
		for _, r := range records {
			if r.ImageFull == nil || !service.PinnedImage(*r.ImageFull) {
				continue
			}
			out = append(out, gin.H{
				"id":          r.ID,
				"version":     r.Version,
				"image_full":  r.ImageFull,
				"deploy_type": r.DeployType,
				"finished_at": fmtTimePtr(r.FinishedAt),
			})
		}
		c.JSON(http.StatusOK, out)
	})
}

// submitRollback is the rollback handler body shared by the session API and
// the external /openapi/v1 surface.
func submitRollback(c *gin.Context, store *service.Store, worker *service.Worker) {
	var b rollbackBody
	if !bindJSON(c, &b) {
		return
	}
	deployLog, err := store.RollbackLog(b.ClusterID, b.TargetLogID)
	if err != nil {
		audit(c, store, "rollback", b.ClusterID, "", "deploy_logs", strconv.FormatInt(b.TargetLogID, 10), "历史目标检查", auditErr(err))
		writeCDErr(c, err)
		return
	}

	submitOK := submitJob(c, store, worker, deployLog)
	result := service.AuditSuccess
	if !submitOK {
		result = service.AuditFailed
	}
	audit(c, store, "rollback", b.ClusterID, deployLog.Namespace, "deployments", deployLog.DeploymentName, deployLog.ContainerName+"@"+deployLog.Version, result)
	if !submitOK {
		return
	}
	c.JSON(http.StatusAccepted, gin.H{
		"log_id":  deployLog.ID,
		"status":  deployLog.Status,
		"message": "回滚任务已提交",
		"steps":   nil,
	})
}
