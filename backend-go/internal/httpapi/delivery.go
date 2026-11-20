package httpapi

import (
	"context"
	"deploy-platform/backend-go/internal/service"
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func registerDeliveryAPI(api *gin.RouterGroup, store *service.Store, k8s *service.K8sService) {
	registerDeliveryIncidentAPI(api, store)
	api.GET("/delivery/notifications", requireRole(store, service.RoleAdmin), func(c *gin.Context) {
		rows, err := store.NotificationHistory()
		if err != nil {
			fail(c, http.StatusInternalServerError, "读取通知失败")
			return
		}
		c.JSON(http.StatusOK, rows)
	})
	api.GET("/delivery/notifications/active", requireRole(store, service.RoleAdmin), func(c *gin.Context) {
		limit := 20
		if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 100 {
				fail(c, http.StatusBadRequest, "limit 必须在 1 到 100 之间")
				return
			}
			limit = parsed
		}
		cursor := strings.TrimSpace(c.Query("cursor"))
		if len(cursor) > 512 {
			fail(c, http.StatusBadRequest, "cursor 长度不能超过 512")
			return
		}
		page, err := store.NotificationAlertsPage(c.Request.Context(), cursor, limit)
		if err != nil {
			if errors.Is(err, service.ErrInvalidNotificationCursor) {
				fail(c, http.StatusBadRequest, "通知游标无效")
				return
			}
			fail(c, http.StatusInternalServerError, "读取通知失败")
			return
		}
		c.JSON(http.StatusOK, page)
	})
	api.POST("/delivery/notifications/:id/retry", requireRole(store, service.RoleAdmin), func(c *gin.Context) {
		var b struct {
			Reason string `json:"reason" binding:"required,min=6,max=1000"`
		}
		if !bindJSON(c, &b) {
			return
		}
		err := store.RetryNotification(c.Param("id"))
		audit(c, store, "notification_retry", 0, "", "delivery_notifications", c.Param("id"), b.Reason, auditErr(err))
		if err != nil {
			writeCDErr(c, err)
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"status": "pending"})
	})
	api.GET("/delivery/metrics", func(c *gin.Context) {
		windowDays := service.DefaultDeliveryMetricsWindowDays
		if raw := strings.TrimSpace(c.Query("window_days")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				fail(c, http.StatusBadRequest, "window_days 必须是整数")
				return
			}
			windowDays = parsed
		}
		if windowDays < 1 || windowDays > service.MaxDeliveryMetricsWindowDays {
			fail(c, http.StatusBadRequest, "window_days 必须在 1 到 365 之间")
			return
		}
		clusterID := 0
		if raw := strings.TrimSpace(c.Query("cluster_id")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 {
				fail(c, http.StatusBadRequest, "cluster_id 必须是正整数")
				return
			}
			clusterID = parsed
		}
		c.JSON(http.StatusOK, store.DeliveryMetricsForTarget(strings.TrimSpace(c.Query("app_name")), clusterID, windowDays, time.Now()))
	})
	api.GET("/delivery/drift", func(c *gin.Context) {
		clusterID, selectedAppIDs, ok := parseDeliveryDriftQuery(c)
		if !ok {
			return
		}
		cluster, err := store.FindCluster(clusterID)
		if err != nil {
			writeCDErr(c, err)
			return
		}
		rt, err := k8s.ForUser(cluster, currentUser(c))
		if err != nil {
			writeCDErr(c, err)
			return
		}
		observeCtx, stop := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer stop()
		apps := store.Apps(clusterID)
		if len(selectedAppIDs) > 0 {
			selected := make([]service.App, 0, len(selectedAppIDs))
			selectedSet := make(map[int]struct{}, len(selectedAppIDs))
			for _, id := range selectedAppIDs {
				selectedSet[id] = struct{}{}
			}
			for _, app := range apps {
				if _, ok := selectedSet[app.ID]; ok {
					selected = append(selected, app)
				}
			}
			apps = selected
		}
		observations := observeDeliveryDriftTargets(observeCtx, store, clusterID, apps, currentUser(c).Username,
			func(ctx context.Context, app service.App) (string, error) {
				readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				return rt.CurrentImage(readCtx, app.Namespace, app.DeploymentName, app.ContainerName)
			})
		states, err := store.ObserveDeliveryDrift(observations)
		if err != nil {
			fail(c, http.StatusInternalServerError, "集群读取已完成，但漂移历史保存失败；请稍后重试")
			return
		}
		c.JSON(http.StatusOK, states)
	})
	api.GET("/delivery/drift/history", func(c *gin.Context) {
		clusterID, selectedAppIDs, ok := parseDeliveryDriftQuery(c)
		if !ok {
			return
		}
		if _, err := store.FindCluster(clusterID); err != nil {
			writeCDErr(c, err)
			return
		}
		limit := service.DefaultDriftHistoryLimit
		if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				fail(c, http.StatusBadRequest, "limit 必须是整数")
				return
			}
			limit = parsed
		}
		events, err := store.DeliveryDriftHistory(service.DeliveryDriftHistoryFilter{ClusterID: clusterID, AppIDs: selectedAppIDs, Limit: limit})
		if err != nil {
			writeCDErr(c, err)
			return
		}
		c.JSON(http.StatusOK, events)
	})
}

func parseDeliveryDriftQuery(c *gin.Context) (int, []int, bool) {
	clusterID, ok := queryIntOpt(c, "cluster_id")
	if !ok {
		return 0, nil, false
	}
	if clusterID <= 0 {
		fail(c, http.StatusBadRequest, "cluster_id 必填")
		return 0, nil, false
	}
	appIDsParam := strings.TrimSpace(c.Query("app_ids"))
	selectedAppIDs := map[int]struct{}{}
	if appIDsParam != "" {
		for _, rawID := range strings.Split(appIDsParam, ",") {
			appID, err := strconv.Atoi(strings.TrimSpace(rawID))
			if err != nil || appID <= 0 {
				fail(c, http.StatusBadRequest, "app_ids 必须是逗号分隔的正整数")
				return 0, nil, false
			}
			selectedAppIDs[appID] = struct{}{}
		}
	}
	appIDs := make([]int, 0, len(selectedAppIDs))
	for appID := range selectedAppIDs {
		appIDs = append(appIDs, appID)
	}
	return clusterID, appIDs, true
}
