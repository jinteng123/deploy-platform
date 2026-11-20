package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"deploy-platform/backend-go/internal/config"
	"deploy-platform/backend-go/internal/service"

	"github.com/gin-gonic/gin"
)

type deployBody struct {
	ClusterID      int    `json:"cluster_id" binding:"required,min=1"`
	FileName       string `json:"file_name" binding:"required"`
	DeploymentName string `json:"deployment_name" binding:"required"`
	ContainerName  string `json:"container_name" binding:"required"`
	Version        string `json:"version" binding:"required"`
	Namespace      string `json:"namespace"`
}

// deployLogOut matches the Python DeployLogOut schema plus step_detail so the
// async pipeline steps stay retrievable.
type deployLogOut struct {
	ID                      int64   `json:"id"`
	ReleaseID               int64   `json:"release_id,omitempty"`
	RollbackTargetID        int64   `json:"rollback_target_id,omitempty"`
	ClusterID               int     `json:"cluster_id"`
	AppName                 string  `json:"app_name"`
	DeploymentName          string  `json:"deployment_name"`
	ContainerName           string  `json:"container_name"`
	Namespace               string  `json:"namespace"`
	Version                 string  `json:"version"`
	ImageFull               *string `json:"image_full"`
	DeployType              string  `json:"deploy_type"`
	Status                  string  `json:"status"`
	CurrentStep             *string `json:"current_step"`
	StepDetail              string  `json:"step_detail,omitempty"`
	ErrorMessage            *string `json:"error_message"`
	StartedAt               *string `json:"started_at"`
	FinishedAt              *string `json:"finished_at"`
	EnvironmentTierSnapshot *string `json:"environment_tier_snapshot,omitempty"`
	CreatedAt               string  `json:"created_at"`
}

func toLogOut(l service.DeployLog) deployLogOut {
	return deployLogOut{
		ID:                      l.ID,
		ReleaseID:               l.ReleaseID,
		RollbackTargetID:        l.RollbackTargetID,
		ClusterID:               l.ClusterID,
		AppName:                 l.AppName,
		DeploymentName:          l.DeploymentName,
		ContainerName:           l.ContainerName,
		Namespace:               l.Namespace,
		Version:                 l.Version,
		ImageFull:               l.ImageFull,
		DeployType:              l.DeployType,
		Status:                  l.Status,
		CurrentStep:             l.CurrentStep,
		StepDetail:              l.StepDetail,
		ErrorMessage:            l.ErrorMessage,
		StartedAt:               fmtTimePtr(l.StartedAt),
		FinishedAt:              fmtTimePtr(l.FinishedAt),
		EnvironmentTierSnapshot: l.EnvironmentTierSnapshot,
		CreatedAt:               fmtTime(l.CreatedAt),
	}
}

// submitJob queues a log; on failure the log is marked failed so it does not
// stay pending forever. If that status write also fails, the response reports
// both errors.
func submitJob(c *gin.Context, store *service.Store, worker *service.Worker, deployLog service.DeployLog) bool {
	if err := service.SubmitDeployLog(c.Request.Context(), worker, store, deployLog); err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return false
	}
	return true
}

func registerDeployAPI(api *gin.RouterGroup, cfg config.Config, store *service.Store, worker *service.Worker) {
	api.POST("/deploy", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		var b deployBody
		if !bindJSON(c, &b) {
			return
		}
		b.FileName = strings.TrimSpace(b.FileName)
		b.DeploymentName = strings.TrimSpace(b.DeploymentName)
		b.ContainerName = strings.TrimSpace(b.ContainerName)
		b.Version = strings.TrimSpace(b.Version)
		b.Namespace = strings.TrimSpace(b.Namespace)
		if !requireStrings(c, map[string]string{
			"file_name": b.FileName, "deployment_name": b.DeploymentName,
			"container_name": b.ContainerName, "version": b.Version,
		}) {
			return
		}
		ns := b.Namespace
		if ns == "" {
			ns = "default"
		}
		if _, ok := service.SafeTransferFile(cfg.TransferDir, b.FileName); !ok {
			fail(c, http.StatusBadRequest, "非法文件名")
			return
		}
		if err := store.CheckLegacyDeploy(b.ClusterID); err != nil {
			audit(c, store, "deploy", b.ClusterID, ns, "deployments", b.DeploymentName, "生产策略检查", auditErr(err))
			writeCDErr(c, err)
			return
		}
		imageFull := fmt.Sprintf("%s/%s/%s:%s", cfg.RegistryHost, cfg.RegistryProject, b.ContainerName, b.Version)
		deployLog, err := store.AddDeployLog(service.DeployLog{
			ClusterID:      b.ClusterID,
			AppName:        b.DeploymentName,
			DeploymentName: b.DeploymentName,
			ContainerName:  b.ContainerName,
			Namespace:      ns,
			Version:        b.Version,
			FileName:       b.FileName,
			ImageFull:      &imageFull,
			DeployType:     "deploy",
			Status:         "pending",
		})
		if err != nil {
			audit(c, store, "deploy", b.ClusterID, ns, "deployments", b.DeploymentName, "创建任务", auditErr(err))
			writeCDErr(c, err)
			return
		}
		submitOK := submitJob(c, store, worker, deployLog)
		detail := fmt.Sprintf("%s/%s@%s", b.DeploymentName, b.ContainerName, b.Version)
		result := service.AuditSuccess
		if !submitOK {
			result = service.AuditFailed
		}
		audit(c, store, "deploy", b.ClusterID, ns, "deployments", b.DeploymentName, detail, result)
		if !submitOK {
			return
		}
		c.JSON(http.StatusAccepted, gin.H{
			"log_id":  deployLog.ID,
			"status":  deployLog.Status,
			"message": "发布任务已提交",
			"steps":   nil,
		})
	})

	api.GET("/deploy/logs/:id", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		l, err := store.FindLog(id)
		if err != nil {
			fail(c, http.StatusNotFound, "部署记录不存在")
			return
		}
		c.JSON(http.StatusOK, toLogOut(l))
	})

	api.GET("/deploy/logs/:id/actions", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		if _, err := store.FindLog(id); err != nil {
			fail(c, http.StatusNotFound, "部署记录不存在")
			return
		}
		actions, err := store.ExecutionActions(id)
		if err != nil {
			fail(c, http.StatusInternalServerError, "读取发布动作记录失败")
			return
		}
		c.JSON(http.StatusOK, actions)
	})

	api.POST("/deploy/logs/:id/reconcile", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		deployLog, err := store.FindLog(id)
		if err != nil {
			writeCDErr(c, err)
			return
		}
		err = worker.ReconcileAndResume(c.Request.Context(), id)
		detail := "核对镜像并继续发布检查"
		if err != nil {
			detail = "核对未继续: " + err.Error()
		}
		audit(c, store, "deploy_reconcile", deployLog.ClusterID, deployLog.Namespace, "deployments", deployLog.DeploymentName, detail, auditErr(err))
		if err != nil {
			writeCDErr(c, err)
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"log_id": id, "status": "pending", "message": "目标镜像已确认，发布任务已恢复"})
	})

	api.GET("/deploy/logs", func(c *gin.Context) {
		clusterID, ok := queryIntOpt(c, "cluster_id")
		if !ok {
			return
		}
		limit, ok := queryIntRange(c, "limit", 20, 1, 100)
		if !ok {
			return
		}
		logs := store.Logs(clusterID, strings.TrimSpace(c.Query("app_name")), strings.TrimSpace(c.Query("status")), limit)
		out := make([]deployLogOut, 0, len(logs))
		for _, l := range logs {
			out = append(out, toLogOut(l))
		}
		c.JSON(http.StatusOK, out)
	})
}
