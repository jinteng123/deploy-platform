package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"deploy-platform/backend-go/internal/kube"
	"deploy-platform/backend-go/internal/service"

	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type appBody struct {
	SourceKind              string  `json:"source_kind"`
	ClusterID               int     `json:"cluster_id" binding:"required,min=1"`
	AppName                 string  `json:"app_name" binding:"required"`
	MatchKeyword            string  `json:"match_keyword"`
	SourceRepoURL           string  `json:"source_repo_url"`
	SourcePath              string  `json:"source_path"`
	DeploymentConfigRepoURL string  `json:"deployment_config_repo_url"`
	DeploymentConfigPath    string  `json:"deployment_config_path"`
	ImageRepository         string  `json:"image_repository"`
	DeploymentName          string  `json:"deployment_name" binding:"required"`
	ContainerName           string  `json:"container_name" binding:"required"`
	Namespace               string  `json:"namespace"`
	HarborProject           string  `json:"harbor_project"`
	Description             *string `json:"description"`
}

func (b *appBody) normalize() {
	b.AppName = strings.TrimSpace(b.AppName)
	b.MatchKeyword = strings.TrimSpace(b.MatchKeyword)
	if b.MatchKeyword == "" {
		b.MatchKeyword = b.AppName
	}
	b.SourceRepoURL = strings.TrimSpace(b.SourceRepoURL)
	b.SourceKind = strings.TrimSpace(b.SourceKind)
	if b.SourceKind == "" {
		if b.SourceRepoURL != "" {
			b.SourceKind = "repository"
		} else {
			b.SourceKind = "unknown"
		}
	}
	b.SourcePath = strings.TrimSpace(b.SourcePath)
	b.DeploymentConfigRepoURL = strings.TrimSpace(b.DeploymentConfigRepoURL)
	b.DeploymentConfigPath = strings.TrimSpace(b.DeploymentConfigPath)
	if b.DeploymentConfigRepoURL != "" && b.DeploymentConfigPath == "" {
		b.DeploymentConfigPath = "."
	}
	b.ImageRepository = strings.TrimSpace(b.ImageRepository)
	b.DeploymentName = strings.TrimSpace(b.DeploymentName)
	b.ContainerName = strings.TrimSpace(b.ContainerName)
	b.Namespace = strings.TrimSpace(b.Namespace)
	b.HarborProject = strings.TrimSpace(b.HarborProject)
	b.Description = trimPtr(b.Description)
}

func (b *appBody) toApp(id int) service.App {
	ns := b.Namespace
	if ns == "" {
		ns = "default"
	}
	project := b.HarborProject
	if project == "" {
		project = "library"
	}
	return service.App{
		SourceKind: b.SourceKind, SourceRepoURL: b.SourceRepoURL, SourcePath: b.SourcePath,
		DeploymentConfigRepoURL: b.DeploymentConfigRepoURL, DeploymentConfigPath: b.DeploymentConfigPath,
		ImageRepository: b.ImageRepository,
		ID:              id,
		ClusterID:       b.ClusterID,
		AppName:         b.AppName,
		MatchKeyword:    b.MatchKeyword,
		DeploymentName:  b.DeploymentName,
		ContainerName:   b.ContainerName,
		Namespace:       ns,
		HarborProject:   project,
		Description:     b.Description,
	}
}

func registerAppAPI(api *gin.RouterGroup, store *service.Store, k8s *service.K8sService) {
	// Preview deliberately creates no mapping, release or deploy task. The same
	// target check runs again on saving explicitly bound mappings.
	api.POST("/apps/preview", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		var b appBody
		if !bindJSON(c, &b) {
			return
		}
		b.normalize()
		a := b.toApp(0)
		image, ok := checkAppBinding(c, store, k8s, a, true)
		if !ok {
			return
		}
		warnings := []string{}
		if a.ImageRepository == "" {
			warnings = append(warnings, "尚未绑定镜像仓库，新镜像仅按 APP_NAME 路由")
		} else if service.ImageRepository(image) != a.ImageRepository {
			warnings = append(warnings, "当前镜像与绑定仓库不同，保存不会更新镜像；请确认迁移意图")
		}
		c.JSON(http.StatusOK, gin.H{"app": a, "current_image": image, "warnings": warnings})
	})
	api.GET("/apps", func(c *gin.Context) {
		clusterID, ok := queryIntOpt(c, "cluster_id")
		if !ok {
			return
		}
		c.JSON(http.StatusOK, store.Apps(clusterID))
	})

	api.POST("/apps", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		var b appBody
		if !bindJSON(c, &b) {
			return
		}
		b.normalize()
		if !requireStrings(c, map[string]string{
			"app_name": b.AppName, "match_keyword": b.MatchKeyword,
			"deployment_name": b.DeploymentName, "container_name": b.ContainerName,
		}) {
			return
		}
		if _, ok := checkAppBinding(c, store, k8s, b.toApp(0), b.ImageRepository != ""); !ok {
			return
		}
		saved, err := store.SaveApp(b.toApp(0))
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusOK, saved)
	})

	api.PUT("/apps/:id", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		id, ok := pathInt(c, "id")
		if !ok {
			return
		}
		if _, err := store.FindApp(id); err != nil {
			fail(c, http.StatusNotFound, "应用不存在")
			return
		}
		var b appBody
		if !bindJSON(c, &b) {
			return
		}
		b.normalize()
		if !requireStrings(c, map[string]string{
			"app_name": b.AppName, "match_keyword": b.MatchKeyword,
			"deployment_name": b.DeploymentName, "container_name": b.ContainerName,
		}) {
			return
		}
		if _, ok := checkAppBinding(c, store, k8s, b.toApp(id), b.ImageRepository != ""); !ok {
			return
		}
		saved, err := store.UpdateApp(b.toApp(id))
		if err != nil {
			if isNotFound(err) {
				fail(c, http.StatusNotFound, "应用不存在")
				return
			}
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusOK, saved)
	})

	api.DELETE("/apps/:id", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		id, ok := pathInt(c, "id")
		if !ok {
			return
		}
		app, err := store.FindApp(id)
		if err != nil {
			fail(c, http.StatusNotFound, "应用不存在")
			return
		}
		if err := store.DeleteApp(id); err != nil {
			if isNotFound(err) {
				fail(c, http.StatusNotFound, "应用不存在")
				return
			}
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("应用 %s 已删除", app.AppName)})
	})
}

func checkAppBinding(c *gin.Context, store *service.Store, k8s *service.K8sService, a service.App, readTarget bool) (string, bool) {
	if !requireStrings(c, map[string]string{"app_name": a.AppName, "deployment_name": a.DeploymentName, "container_name": a.ContainerName}) {
		return "", false
	}
	if len(a.AppName) > 128 || len(a.DeploymentName) > 128 || len(a.ContainerName) > 128 || len(a.Namespace) > 64 || len(a.MatchKeyword) > 128 || len(a.HarborProject) > 128 {
		fail(c, http.StatusBadRequest, "组件或目标名称过长")
		return "", false
	}
	if err := service.ValidateAppSources(a); err != nil {
		writeStoreErr(c, err)
		return "", false
	}
	cluster, err := store.FindCluster(a.ClusterID)
	if err != nil {
		writeStoreErr(c, err)
		return "", false
	}
	if !readTarget {
		return "", true
	}
	rt, err := k8s.ForUser(cluster, currentUser(c))
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return "", false
	}
	def, _ := kube.LookupResource("apps", "v1", "deployments")
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	obj, err := rt.GetResource(ctx, def, a.Namespace, a.DeploymentName, metav1.GetOptions{})
	if err != nil {
		writeResourceErr(c, err)
		return "", false
	}
	if obj == nil {
		fail(c, http.StatusBadGateway, "无法读取目标 Deployment")
		return "", false
	}
	containers, _, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	if err != nil {
		fail(c, http.StatusBadGateway, "目标 Deployment 的容器信息不可用")
		return "", false
	}
	for _, raw := range containers {
		if container, ok := raw.(map[string]any); ok && container["name"] == a.ContainerName {
			image, _ := container["image"].(string)
			if image == "" {
				fail(c, http.StatusBadGateway, "目标容器缺少镜像")
				return "", false
			}
			return image, true
		}
	}
	fail(c, http.StatusBadRequest, "目标 Deployment 中不存在所选容器，请重新选择")
	return "", false
}
