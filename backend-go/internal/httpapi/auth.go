package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"deploy-platform/backend-go/internal/service"

	"github.com/gin-gonic/gin"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// sessionCookie carries the opaque session token. HttpOnly + SameSite=Lax
// blocks JS theft and cross-site sends; Secure only on HTTPS so local
// http dev still works.
const sessionCookie = "dp_session"

// ctxUserKey stores the authenticated *service.User on the gin context.
const ctxUserKey = "kb.auth.user"

// currentUser returns the session user or nil — nil only inside handlers
// registered before the auth middleware (login, webhook), which must not
// call this.
func currentUser(c *gin.Context) *service.User {
	if v, ok := c.Get(ctxUserKey); ok {
		if u, ok := v.(*service.User); ok {
			return u
		}
	}
	return nil
}

// sessionToken reads the credential from the HttpOnly cookie first, then
// the Authorization bearer — non-browser clients (scripts, WS probes)
// authenticate the same way.
func sessionToken(c *gin.Context) string {
	if cookie, err := c.Cookie(sessionCookie); err == nil && cookie != "" {
		return cookie
	}
	h := c.GetHeader("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// authRequired rejects unauthenticated requests before any handler runs.
// The resolved user is placed on the context for handlers and audit.
func authRequired(store *service.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := sessionToken(c)
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "未登录或会话已过期"})
			return
		}
		u, err := store.SessionUser(token)
		if err != nil {
			if !errors.Is(err, service.ErrBadCredentials) {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "暂时无法验证登录会话，请稍后重试"})
				c.Abort()
				return
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "未登录或会话已过期"})
			return
		}
		c.Set(ctxUserKey, &u)
		c.Next()
	}
}

// requireRole gates a route to a minimum platform role. K8s RBAC is a
// separate axis enforced by impersonation — this gate is about which
// platform surfaces a user may reach at all. Denials are audited.
func requireRole(store *service.Store, minRole string) gin.HandlerFunc {
	min := service.RoleRank(minRole)
	return func(c *gin.Context) {
		u := currentUser(c)
		if u == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "未登录或会话已过期"})
			return
		}
		if service.RoleRank(u.Role) < min {
			audit(c, store, "role_gate", 0, "", "", "", c.Request.Method+" "+c.Request.URL.Path, service.AuditDenied)
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "当前角色无权执行该操作"})
			return
		}
		c.Next()
	}
}

// audit writes one audit entry for the current request's user. It is
// best-effort — audit persistence failures never fail the operation,
// they are logged by the store layer instead.
func audit(c *gin.Context, store *service.Store, action string, clusterID int, ns, gvr, name, detail, result string) {
	username := "-"
	if u := currentUser(c); u != nil {
		username = u.Username
	}
	_ = store.AddAudit(service.AuditEntry{
		Username: username, Action: action, ClusterID: clusterID,
		Namespace: ns, GVR: gvr, Name: name, Detail: detail, Result: result,
	})
}

// auditErr picks the audit result from an operation error.
func auditErr(err error) string {
	switch {
	case err == nil:
		return service.AuditSuccess
	case apierrors.IsConflict(err):
		return service.AuditConflict
	case apierrors.IsForbidden(err):
		return service.AuditDenied
	case errors.Is(err, service.ErrForbidden):
		return service.AuditDenied
	default:
		return service.AuditFailed
	}
}

// registerAuthAPI exposes session lifecycle and (admin-only) user
// management. login is registered on the public group — everything else
// requires auth.
func registerAuthAPI(api *gin.RouterGroup, store *service.Store) {
	api.POST("/auth/login", func(c *gin.Context) {
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.Username == "" || body.Password == "" {
			fail(c, http.StatusBadRequest, "username 与 password 必填")
			return
		}
		u, err := store.Authenticate(body.Username, body.Password)
		if err != nil {
			if !errors.Is(err, service.ErrBadCredentials) {
				fail(c, http.StatusServiceUnavailable, "暂时无法验证用户，请稍后重试")
				return
			}
			fail(c, http.StatusUnauthorized, "用户名或密码错误")
			return
		}
		token, err := store.CreateSession(u.ID)
		if err != nil {
			fail(c, http.StatusInternalServerError, "会话创建失败")
			return
		}
		setSessionCookie(c, token)
		c.JSON(http.StatusOK, gin.H{"token": token, "user": u.Public()})
	})
}

// registerAuthzAPI holds the authenticated side of the auth surface.
func registerAuthzAPI(api *gin.RouterGroup, store *service.Store) {
	api.GET("/auth/me", func(c *gin.Context) {
		u := currentUser(c)
		if u == nil {
			fail(c, http.StatusUnauthorized, "未登录或会话已过期")
			return
		}
		c.JSON(http.StatusOK, gin.H{"user": u.Public()})
	})
	api.POST("/auth/logout", func(c *gin.Context) {
		if token := sessionToken(c); token != "" {
			if err := store.DeleteSession(token); err != nil {
				clearSessionCookie(c)
				fail(c, http.StatusServiceUnavailable, "暂时无法撤销登录会话，请稍后确认")
				return
			}
		}
		clearSessionCookie(c)
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
}

func setSessionCookie(c *gin.Context, token string) {
	secure := c.Request.TLS != nil
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/api",
		MaxAge:   int(service.SessionTTL / time.Second),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(c *gin.Context) {
	secure := c.Request.TLS != nil
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/api",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// registerUsersAPI is the admin-only account surface. Usernames are
// immutable identities (audit trails reference them); disable, don't
// delete.
func registerUsersAPI(api *gin.RouterGroup, store *service.Store) {
	api.GET("/users", func(c *gin.Context) {
		users, err := store.ListUsers()
		if err != nil {
			fail(c, http.StatusServiceUnavailable, "暂时无法读取用户列表")
			return
		}
		out := make([]service.PublicUser, 0)
		for _, u := range users {
			out = append(out, u.Public())
		}
		c.JSON(http.StatusOK, out)
	})
	// GET /audit returns recent audit entries, newest first. Admin-only —
	// the records are how "who did what" stays traceable across users.
	api.GET("/audit", func(c *gin.Context) {
		limit := 200
		if raw := c.Query("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 1000 {
				limit = n
			}
		}
		entries, err := store.RecentAudits(limit)
		if err != nil {
			fail(c, http.StatusServiceUnavailable, "暂时无法读取操作审计")
			return
		}
		c.JSON(http.StatusOK, entries)
	})
	api.POST("/users", func(c *gin.Context) {
		var body struct {
			Username    string   `json:"username"`
			DisplayName string   `json:"display_name"`
			Role        string   `json:"role"`
			Password    string   `json:"password"`
			K8sUsername string   `json:"k8s_username"`
			K8sGroups   []string `json:"k8s_groups"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			fail(c, http.StatusBadRequest, "请求体非法")
			return
		}
		if body.Username == "" || len(body.Username) > 64 {
			fail(c, http.StatusBadRequest, "用户名必填且不超过 64 字符")
			return
		}
		if len(body.Password) < 8 {
			fail(c, http.StatusBadRequest, "密码至少 8 位")
			return
		}
		hash, err := service.HashPassword(body.Password)
		if err != nil {
			fail(c, http.StatusInternalServerError, "密码散列失败")
			return
		}
		u, err := store.SaveUser(service.User{
			Username:     body.Username,
			DisplayName:  body.DisplayName,
			Role:         body.Role,
			PasswordHash: hash,
			K8sUsername:  body.K8sUsername,
			K8sGroups:    body.K8sGroups,
			Enabled:      true,
		})
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusCreated, u.Public())
	})
	api.PUT("/users/:id", func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		cur, err := store.FindUser(id)
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		var body struct {
			DisplayName *string  `json:"display_name"`
			Role        *string  `json:"role"`
			Password    string   `json:"password"`
			K8sUsername *string  `json:"k8s_username"`
			K8sGroups   []string `json:"k8s_groups"`
			Enabled     *bool    `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			fail(c, http.StatusBadRequest, "请求体非法")
			return
		}
		if body.DisplayName != nil {
			cur.DisplayName = *body.DisplayName
		}
		if body.Role != nil {
			cur.Role = *body.Role
		}
		if body.K8sUsername != nil {
			cur.K8sUsername = *body.K8sUsername
		}
		if body.K8sGroups != nil {
			cur.K8sGroups = body.K8sGroups
		}
		if body.Enabled != nil {
			cur.Enabled = *body.Enabled
		}
		if body.Password != "" {
			if len(body.Password) < 8 {
				fail(c, http.StatusBadRequest, "密码至少 8 位")
				return
			}
			hash, err := service.HashPassword(body.Password)
			if err != nil {
				fail(c, http.StatusInternalServerError, "密码散列失败")
				return
			}
			cur.PasswordHash = hash
		}
		u := currentUser(c)
		if u != nil && u.ID == cur.ID && body.Enabled != nil && !*body.Enabled {
			fail(c, http.StatusBadRequest, "不能禁用当前登录账户")
			return
		}
		saved, err := store.UpdateUser(cur)
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusOK, saved.Public())
	})
}

// pathInt64/writeStoreErr are shared helpers from handlers.go/clusters.go.
