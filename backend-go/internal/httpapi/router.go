package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"deploy-platform/backend-go/internal/config"
	"deploy-platform/backend-go/internal/repository"
	"deploy-platform/backend-go/internal/service"
)

// Application bundles the HTTP handler with the resources that must be
// released on shutdown: worker, cleanup scheduler and the DB handle.
type Application struct {
	Engine      *gin.Engine
	store       *service.Store
	worker      *service.Worker
	cleanup     *service.CleanupService
	notifier    *service.Notifier
	driftPoller *service.DriftPoller
	db          *repository.MySQL
	closeOnce   sync.Once
	closeErr    error
}

func NewApplication(cfg config.Config) (*Application, error) {
	if err := service.ValidateDriftPollInterval(cfg.DriftPollInterval); err != nil {
		return nil, err
	}
	store, db, err := initStore(cfg)
	if err != nil {
		return nil, err
	}
	if err := seedAdmin(store, cfg); err != nil {
		if db != nil {
			db.Close()
		}
		return nil, err
	}

	var runner service.CommandRunner = service.OSRunner{
		LocalTimeout: cfg.LocalCommandTimeout,
		SSHTimeout:   cfg.SSHCommandTimeout,
	}
	if cfg.MockRuntime {
		runner = &service.MockRunner{}
	}
	cipher, err := resolveCipher(cfg, store)
	if err != nil {
		if db != nil {
			db.Close()
		}
		return nil, err
	}
	k8s := service.NewK8sService(cfg, runner, cipher)
	var driftPoller *service.DriftPoller
	if cfg.DriftPollInterval > 0 {
		driftPoller, err = service.NewDriftPoller(store, k8s, cfg.DriftPollInterval)
		if err != nil {
			if db != nil {
				db.Close()
			}
			return nil, err
		}
	}
	pipeline := service.NewPipeline(cfg, store, runner, k8s)
	worker, err := service.NewRecoveredWorker(context.Background(), pipeline, 2)
	if err != nil {
		if db != nil {
			db.Close()
		}
		return nil, fmt.Errorf("恢复发布任务失败: %w", err)
	}
	cd := service.NewCDService(cfg, store, worker)

	notifier, err := service.NewNotifierWithPublicURL(store, cfg.NotificationWebhookURL, cfg.NotificationWebhookSecret, cfg.MockRuntime, cfg.NotificationWebhookCAFile, cfg.PlatformPublicURL)
	if err != nil {
		worker.Close()
		if db != nil {
			db.Close()
		}
		return nil, err
	}
	notifier.Start()
	cleanup := service.NewCleanupService(cfg.TransferDir, cfg.CleanupMaxAgeDays)
	cleanup.Start()
	if driftPoller != nil {
		driftPoller.Start()
	}

	var readinessCheck func(context.Context) error
	if db != nil {
		readinessCheck = db.Ping
	}

	return &Application{
		Engine:      newEngine(cfg, store, worker, k8s, cd, cipher, readinessCheck),
		store:       store,
		worker:      worker,
		cleanup:     cleanup,
		notifier:    notifier,
		driftPoller: driftPoller,
		db:          db,
	}, nil
}

// initStore loads state from MySQL. When the DB is unreachable a mock-runtime
// process may continue with an explicit in-memory store; any other failure —
// or any load error after a successful connect — is fatal.
func initStore(cfg config.Config) (*service.Store, *repository.MySQL, error) {
	if strings.TrimSpace(cfg.DatabaseDSN) == "" {
		if !cfg.MockRuntime {
			return nil, nil, errors.New("DATABASE_DSN 不能为空")
		}
		log.Printf("[warn] DATABASE_DSN 为空，使用内存 store")
		return service.NewStore(), nil, nil
	}
	db, err := repository.Open(cfg.DatabaseDSN)
	if err != nil {
		if !cfg.MockRuntime {
			return nil, nil, fmt.Errorf("数据库连接失败: %w", err)
		}
		log.Printf("[warn] 数据库不可用，使用内存 store: %v", err)
		return service.NewStore(), nil, nil
	}
	if err := db.CheckSessionSchema(); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("登录会话存储不可用，请先执行 migrations/033_persistent_user_sessions.sql: %w", err)
	}
	clusters, err := db.Clusters()
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("加载 clusters 失败，请先执行 migrations/002_cluster_connections.sql: %w", err)
	}
	apps, err := db.Apps(0)
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("加载 apps 失败（请确认数据库已升级至 008）: %w", err)
	}
	logs, err := db.Logs()
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("加载 deploy_logs 失败（若为未知列，请确认已执行迁移 021）: %w", err)
	}
	releases, err := db.ImageReleases()
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("加载 image_releases 失败，请执行 migrations/001 与 006: %w", err)
	}
	store := service.NewStoreWithData(clusters, apps, logs, releases, db)
	deliveryModel, err := db.DeliveryModel()
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("加载应用/组件/环境/工作负载引用失败，请确认已按顺序执行 migrations/010_delivery_model.sql 至 migrations/037_delivery_incident_remediation.sql: %w", err)
	}
	store.LoadDeliveryModel(deliveryModel)
	approvalGrants, err := db.DeliveryApprovalGrants()
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("加载发布审批范围失败，请先执行 migrations/031_delivery_approval_grants.sql: %w", err)
	}
	store.LoadDeliveryApprovalGrants(approvalGrants)
	deliveryPolicies, err := db.DeliveryPolicyVersions()
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("加载发布策略版本失败，请先执行 migrations/019_delivery_policy_versions.sql: %w", err)
	}
	store.LoadDeliveryPolicyVersions(deliveryPolicies)
	deliveryEvidencePolicies, err := db.DeliveryEnvironmentEvidencePolicyVersions()
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("加载生产环境证据策略失败，请先执行 migrations/023_delivery_environment_evidence_policy.sql: %w", err)
	}
	store.LoadDeliveryEnvironmentEvidencePolicyVersions(deliveryEvidencePolicies)
	deliveryPlans, err := db.DeliveryPlans()
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("加载发布计划失败，请先执行 migrations/011_delivery_plans.sql: %w", err)
	}
	store.LoadDeliveryPlans(deliveryPlans)
	deliveryRuns, deliveryRunEvents, err := db.DeliveryRuns()
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("加载发布运行状态失败，请确认已执行 migrations/016_delivery_runs.sql、024_delivery_run_event_payloads.sql 和 025_worker_claim_ownership.sql: %w", err)
	}
	store.LoadDeliveryRuns(deliveryRuns, deliveryRunEvents)
	users, err := db.Users()
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("加载 users 失败，请先执行 migrations/003_users_audit.sql: %w", err)
	}
	store.LoadUsers(users)
	keys, err := db.APIKeys()
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("加载 api_keys 失败，请先执行 migrations/004_api_keys.sql: %w", err)
	}
	store.LoadAPIKeys(keys)
	promos, err := db.PromotionRequests()
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("加载 promotion_requests 失败，请确认已执行 migrations/005、006 与 012: %w", err)
	}
	store.LoadPromotions(promos)
	driftStates, err := db.DeliveryDriftStates()
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("加载漂移观测状态失败，请先执行 migrations/020_delivery_drift_observations.sql: %w", err)
	}
	store.LoadDeliveryDriftStates(driftStates)
	deliveryIncidents, deliveryIncidentEvents, err := db.DeliveryIncidents(service.DefaultDeliveryIncidentLimit)
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("加载生产事件台账失败，请确认已按顺序执行 migrations/028_delivery_incident_events.sql 至 migrations/037_delivery_incident_remediation.sql: %w", err)
	}
	store.LoadDeliveryIncidents(deliveryIncidents, deliveryIncidentEvents)
	runnerBindings, err := db.RunnerBindings()
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("加载 Runner 接入失败，请执行 migrations/009_runner_integrations.sql: %w", err)
	}
	store.LoadRunnerBindings(runnerBindings)
	connections, err := db.YunxiaoConnections()
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("加载云效连接失败，请执行 migrations/009_runner_integrations.sql: %w", err)
	}
	store.LoadYunxiaoConnections(connections)
	audits, err := db.Audits(2000)
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("加载 audit_logs 失败，请先执行 migrations/003_users_audit.sql: %w", err)
	}
	// db.Audits returns newest-first; the in-memory ring is chronological.
	for i, j := 0, len(audits)-1; i < j; i, j = i+1, j-1 {
		audits[i], audits[j] = audits[j], audits[i]
	}
	store.LoadAudits(audits)
	return store, db, nil
}

// ValidateStoredCredentials checks the persistent store and every credential
// that a real deployment would decrypt at startup. It starts no workers or
// background services and performs no writes. MockRuntime is forced off so a
// development setting cannot turn this preflight into a false positive.
func ValidateStoredCredentials(cfg config.Config) error {
	cfg.MockRuntime = false
	store, db, err := initStore(cfg)
	if err != nil {
		return fmt.Errorf("加载运行配置失败: %w", err)
	}
	if db != nil {
		defer db.Close()
	}
	if len(store.Users()) == 0 && strings.TrimSpace(cfg.AdminInitialPassword) == "" {
		return errors.New("ADMIN_INITIAL_PASSWORD 未配置：真实运行首次启动必须设置初始管理员口令")
	}
	if strings.TrimSpace(cfg.CredentialEncryptionKey) != "" {
		if _, err := service.NewCredentialCipher(cfg.CredentialEncryptionKey); err != nil {
			return err
		}
	}
	if _, err := resolveCipher(cfg, store); err != nil {
		return err
	}
	return nil
}

// seedAdmin creates the initial admin account when the store has no users
// at all (fresh install or memory store). The password comes from
// ADMIN_INITIAL_PASSWORD; the dev fallback is logged loudly because it is
// a well-known credential that must be rotated on real deployments.
func seedAdmin(store *service.Store, cfg config.Config) error {
	if len(store.Users()) > 0 {
		return nil
	}
	password := strings.TrimSpace(cfg.AdminInitialPassword)
	if password == "" {
		if !cfg.MockRuntime {
			return errors.New("ADMIN_INITIAL_PASSWORD 未配置：真实运行拒绝使用默认管理员口令")
		}
		password = "admin123"
		log.Printf("[warn] MOCK_RUNTIME 已启用但未配置 ADMIN_INITIAL_PASSWORD，开发环境初始管理员使用 admin/admin123")
	}
	hash, err := service.HashPassword(password)
	if err != nil {
		return err
	}
	_, err = store.SaveUser(service.User{
		Username:     "admin",
		DisplayName:  "管理员",
		Role:         service.RoleAdmin,
		PasswordHash: hash,
		Enabled:      true,
	})
	if err != nil {
		return fmt.Errorf("初始管理员创建失败: %w", err)
	}
	log.Printf("初始管理员 admin 已创建（请尽快修改口令并配置 K8s 身份映射）")
	return nil
}

// resolveCipher builds the credential cipher. When any enabled cluster uses a
// direct kubeconfig/token connection the key is mandatory — starting without
// it would strand those clusters; legacy-only setups may run without a key.
func resolveCipher(cfg config.Config, store *service.Store) (*service.CredentialCipher, error) {
	needs := len(store.YunxiaoConnections()) > 0
	for _, c := range store.Clusters() {
		if c.Enabled && (c.ConnectionType == "kubeconfig" || c.ConnectionType == "token") {
			needs = true
			break
		}
	}
	cipher, err := service.NewCredentialCipher(cfg.CredentialEncryptionKey)
	if err != nil {
		if needs {
			return nil, fmt.Errorf("存在直连集群或云效连接，但 %w", err)
		}
		if strings.TrimSpace(cfg.CredentialEncryptionKey) != "" {
			log.Printf("[warn] CREDENTIAL_ENCRYPTION_KEY 无效，直连集群凭据将不可用: %v", err)
		}
		return nil, nil
	}
	if !cfg.MockRuntime {
		for _, connection := range store.YunxiaoConnections() {
			if _, err := cipher.Decrypt(connection.TokenCiphertext); err != nil {
				return nil, fmt.Errorf("云效连接 %s 访问令牌解密失败，请恢复原 CREDENTIAL_ENCRYPTION_KEY", connection.Name)
			}
		}
		for _, b := range store.RunnerBindings() {
			if b.PipelineBackupCiphertext != "" {
				if _, err := cipher.Decrypt(b.PipelineBackupCiphertext); err != nil {
					return nil, fmt.Errorf("Runner 接入 %d 流水线备份解密失败，请恢复原 CREDENTIAL_ENCRYPTION_KEY", b.ID)
				}
			}
		}
		for _, c := range store.Clusters() {
			if !c.Enabled || (c.ConnectionType != "kubeconfig" && c.ConnectionType != "token") || c.CredentialCiphertext == nil || *c.CredentialCiphertext == "" {
				continue
			}
			if _, err := cipher.Decrypt(*c.CredentialCiphertext); err != nil {
				return nil, fmt.Errorf("集群 %s 凭据解密失败，请恢复保存该凭据时使用的 CREDENTIAL_ENCRYPTION_KEY；不要生成新密钥替换", c.Name)
			}
		}
	}
	return cipher, nil
}

// Close stops the worker and cleanup scheduler, then closes the DB. It is
// idempotent: repeated calls return the cached result.
func (a *Application) Close() error {
	a.closeOnce.Do(func() {
		if a.driftPoller != nil {
			a.driftPoller.Close()
		}
		if a.worker != nil {
			a.worker.Close()
		}
		if a.cleanup != nil {
			a.cleanup.Stop()
		}
		if a.notifier != nil {
			a.notifier.Close()
		}
		if a.db != nil {
			a.closeErr = a.db.Close()
		}
	})
	return a.closeErr
}

func newEngine(cfg config.Config, store *service.Store, worker *service.Worker, k8s *service.K8sService, cd *service.CDService, cipher *service.CredentialCipher, readinessCheck func(context.Context) error) *gin.Engine {
	// 部署侧约定 ENV=production（Dockerfile/chart），历史上也出现过
	// ENV=prod——前缀匹配覆盖两种取值，否则生产会静默留在 gin debug 模式。
	if strings.HasPrefix(cfg.Env, "prod") {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery(), cors(), apiNoStore)

	// /health remains as the legacy liveness endpoint for existing probes.
	r.GET("/health", health(cfg))
	r.GET("/health/live", health(cfg))
	r.GET("/health/ready", readiness(cfg, readinessCheck))

	// Public endpoints authenticate by their own mechanism: the webhook by
	// its shared secret, login by password. Everything else sits behind
	// the session middleware.
	pub := r.Group("/api/v1")
	pub.GET("", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"service": "deploy-platform", "status": "ready"})
	})
	registerAuthAPI(pub, store)
	registerCDWebhook(pub, store, cfg, cd)

	// External surface: API-key-authed, versioned. Separate group so the
	// session middleware never shadows key auth and vice versa.
	registerOpenAPI(r, store, worker, cd, cfg)

	api := r.Group("/api/v1", authRequired(store))
	registerAuthzAPI(api, store)
	admin := api.Group("", requireRole(store, service.RoleAdmin))
	registerUsersAPI(admin, store)
	registerAPIKeyAPI(admin, store)
	registerClusterAPI(api, store, k8s, cipher)
	registerAppAPI(api, store, k8s)
	registerDeliveryModelAPI(api, store, cd)
	registerDeliveryPlanAPI(api, store, cd)
	registerDeliveryRunAPI(api, store, cd)
	registerDeployAPI(api, cfg, store, worker)
	registerFileAPI(api, cfg, store)
	registerRollbackAPI(api, store, worker)
	registerK8sAPI(api, store, k8s)
	registerK8sStreamAPI(api, store, k8s)
	registerCDAPI(api, store, cfg, cd, k8s)
	registerDeliveryAPI(api, store, k8s)
	registerRunnerAPI(r, api, store, cd, cipher)

	registerFrontend(r, cfg.FrontendDir)
	return r
}

func health(cfg config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"env":     cfg.Env,
			"version": "2.0.0-go",
		})
	}
}

func readiness(cfg config.Config, check func(context.Context) error) gin.HandlerFunc {
	return func(c *gin.Context) {
		if check != nil {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
			defer cancel()
			if err := check(ctx); err != nil {
				// Keep dependency details out of the public probe response. The
				// readiness status is enough for the orchestrator to stop routing.
				c.JSON(http.StatusServiceUnavailable, gin.H{
					"status": "not_ready",
					"env":    cfg.Env,
				})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{
			"status":  "ready",
			"env":     cfg.Env,
			"version": "2.0.0-go",
		})
	}
}

// apiNoStore marks API responses uncacheable. Without it a browser applies
// heuristic caching to GETs that carry Last-Modified (e.g. a SPA-fallback
// HTML page served by a stale binary missing a route) and keeps answering
// from cache after the backend is fixed.
func apiNoStore(c *gin.Context) {
	if strings.HasPrefix(c.Request.URL.Path, "/api/") ||
		strings.HasPrefix(c.Request.URL.Path, "/openapi/") {
		c.Header("Cache-Control", "no-store")
	}
	c.Next()
}

func cors() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func registerFrontend(r *gin.Engine, frontendDir string) {
	index := filepath.Join(frontendDir, "index.html")
	if _, err := os.Stat(index); err != nil {
		return
	}

	r.Static("/assets", filepath.Join(frontendDir, "assets"))
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		// API paths never fall back to the SPA — a missed route answers a
		// JSON 404 in the surface's envelope, not a 200 HTML page that
		// clients misread as a payload (e.g. a stale binary missing a
		// newer route).
		switch {
		case strings.HasPrefix(p, "/openapi/"):
			c.JSON(http.StatusNotFound, gin.H{"error": "接口不存在"})
		case strings.HasPrefix(p, "/api/"):
			c.JSON(http.StatusNotFound, gin.H{"detail": "接口不存在"})
		case filepath.Ext(p) != "":
			c.Status(http.StatusNotFound)
		default:
			c.File(index)
		}
	})
}
