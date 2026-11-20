package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"deploy-platform/backend-go/internal/config"
	"deploy-platform/backend-go/internal/service"

	"github.com/gin-gonic/gin"
)

// apiKeyAuth guards the /openapi/v1 surface: `Authorization: Bearer dp_…`
// or `X-Api-Key`. A resolved key becomes a synthetic session user
// ("apikey:<name>") so audit() and requireRole() work unchanged and every
// external call stays traceable to the credential that made it.
func apiKeyAuth(store *service.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := ""
		if h := c.GetHeader("Authorization"); strings.HasPrefix(h, "Bearer ") {
			token = strings.TrimSpace(h[7:])
		}
		if token == "" {
			token = strings.TrimSpace(c.GetHeader("X-Api-Key"))
		}
		key, err := store.ResolveAPIKey(token)
		if err != nil {
			if !errors.Is(err, service.ErrNotFound) {
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "API 密钥存储暂时不可用"})
				return
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "无效或已过期的 API 密钥"})
			return
		}
		store.TouchAPIKey(key.ID)
		c.Set(ctxUserKey, &service.User{Username: "apikey:" + key.Name, Role: key.Role})
		c.Next()
	}
}

// openAPIErrShape marks every request on the external surface so fail()
// and the helpers built on it emit the documented {error: …} envelope —
// the session API's {detail: …} shape stays internal-only.
func openAPIErrShape(c *gin.Context) {
	c.Set(ctxOpenAPIErr, true)
}

// registerOpenAPI mounts the versioned external surface. Handlers reuse the
// same services (and shared handler bodies) as the session API — the
// contract differs only in auth (API key vs cookie) and stability
// guarantees.
func registerOpenAPI(r *gin.Engine, store *service.Store, worker *service.Worker, cd *service.CDService, cfg config.Config) {
	g := r.Group("/openapi/v1", openAPIErrShape, apiKeyAuth(store))

	g.POST("/releases/:id/verification", requireRole(store, service.RoleOperator), func(c *gin.Context) { recordEvidence(c, store, false) })
	g.POST("/releases/:id/scan", requireRole(store, service.RoleOperator), func(c *gin.Context) { recordEvidence(c, store, true) })
	g.GET("/releases", func(c *gin.Context) {
		limit, ok := queryIntRange(c, "limit", 50, 1, 200)
		if !ok {
			return
		}
		c.JSON(http.StatusOK, cd.List(strings.TrimSpace(c.Query("app_name")), limit))
	})

	g.GET("/releases/:id", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		v, err := cd.Get(id)
		if err != nil {
			writeCDErr(c, err)
			return
		}
		c.JSON(http.StatusOK, v)
	})

	g.GET("/releases/:id/promotion-targets", func(c *gin.Context) {
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

	g.POST("/releases/:id/promote", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		promoteRelease(c, store, cd, cfg)
	})

	g.GET("/promotion-requests", func(c *gin.Context) {
		rows, err := cd.ListPromotionRequests(strings.TrimSpace(c.Query("status")), 100)
		if err != nil {
			writeCDErr(c, err)
			return
		}
		c.JSON(http.StatusOK, rows)
	})

	g.POST("/promotion-requests/:id/approve", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		approvePromotion(c, store, cd)
	})

	g.POST("/promotion-requests/:id/reject", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		rejectPromotion(c, store, cd)
	})

	g.GET("/deploy-logs/:id", func(c *gin.Context) {
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
	g.GET("/deploy-logs/:id/actions", func(c *gin.Context) {
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

	g.POST("/rollback", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		submitRollback(c, store, worker)
	})
}

// registerAPIKeyAPI is the admin-only key management surface (session auth
// + admin role — mounted by the caller on the gated subgroup).
func registerAPIKeyAPI(api *gin.RouterGroup, store *service.Store) {
	api.GET("/api-keys", func(c *gin.Context) {
		keys, err := store.ListAPIKeys()
		if err != nil {
			fail(c, http.StatusServiceUnavailable, "暂时无法读取 API 密钥")
			return
		}
		c.JSON(http.StatusOK, keys)
	})

	api.POST("/api-keys", func(c *gin.Context) {
		var b struct {
			Name      string `json:"name"`
			Role      string `json:"role"`
			ExpiresAt string `json:"expires_at"`
		}
		if !bindJSON(c, &b) {
			return
		}
		b.Name = strings.TrimSpace(b.Name)
		if b.Name == "" || len(b.Name) > 64 {
			fail(c, http.StatusBadRequest, "name 必填且不超过 64 字符")
			return
		}
		// Keys are automation credentials — admin scope stays browser-only.
		role := b.Role
		if role == "" {
			role = service.RoleViewer
		}
		if role != service.RoleViewer && role != service.RoleOperator {
			fail(c, http.StatusBadRequest, "role 只能是 viewer 或 operator")
			return
		}
		var expiresAt *time.Time
		if b.ExpiresAt != "" {
			t, err := time.Parse(time.RFC3339, strings.TrimSpace(b.ExpiresAt))
			if err != nil {
				fail(c, http.StatusBadRequest, "expires_at 需为 RFC3339 格式")
				return
			}
			expiresAt = &t
		}
		token, hash, prefix, err := service.NewAPIKeyToken()
		if err != nil {
			fail(c, http.StatusInternalServerError, "密钥生成失败")
			return
		}
		k, err := store.SaveAPIKey(service.APIKey{
			Name: b.Name, KeyHash: hash, Prefix: prefix,
			Role: role, Enabled: true, ExpiresAt: expiresAt,
		})
		audit(c, store, "apikey_create", 0, "", "api_keys", b.Name, "role:"+role, auditErr(err))
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		// plaintext is returned exactly once — the UI must show-and-hide it.
		c.JSON(http.StatusCreated, gin.H{
			"id": k.ID, "name": k.Name, "prefix": k.Prefix, "role": k.Role,
			"enabled": k.Enabled, "expires_at": k.ExpiresAt,
			"created_at": k.CreatedAt, "key": token,
		})
	})

	api.PUT("/api-keys/:id", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		cur, err := store.FindAPIKey(id)
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		var b struct {
			Name      *string `json:"name"`
			Role      *string `json:"role"`
			Enabled   *bool   `json:"enabled"`
			ExpiresAt *string `json:"expires_at"`
		}
		if !bindJSON(c, &b) {
			return
		}
		if b.Name != nil {
			name := strings.TrimSpace(*b.Name)
			if name == "" || len(name) > 64 {
				fail(c, http.StatusBadRequest, "name 必填且不超过 64 字符")
				return
			}
			cur.Name = name
		}
		if b.Role != nil {
			if *b.Role != service.RoleViewer && *b.Role != service.RoleOperator {
				fail(c, http.StatusBadRequest, "role 只能是 viewer 或 operator")
				return
			}
			cur.Role = *b.Role
		}
		if b.Enabled != nil {
			cur.Enabled = *b.Enabled
		}
		if b.ExpiresAt != nil {
			if *b.ExpiresAt == "" {
				cur.ExpiresAt = nil
			} else if t, err := time.Parse(time.RFC3339, strings.TrimSpace(*b.ExpiresAt)); err == nil {
				cur.ExpiresAt = &t
			} else {
				fail(c, http.StatusBadRequest, "expires_at 需为 RFC3339 格式")
				return
			}
		}
		saved, err := store.UpdateAPIKey(cur)
		audit(c, store, "apikey_update", 0, "", "api_keys", strconv.FormatInt(id, 10), cur.Name, auditErr(err))
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusOK, saved)
	})

	api.DELETE("/api-keys/:id", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		k, err := store.FindAPIKey(id)
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		err = store.DeleteAPIKey(id)
		audit(c, store, "apikey_delete", 0, "", "api_keys", strconv.FormatInt(id, 10), k.Name, auditErr(err))
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
}
