package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config contains process-level settings. Environment variable names follow
// the Python backend so a single .env drives either implementation.
type Config struct {
	Env         string
	HTTPAddr    string
	FrontendDir string
	DatabaseDSN string
	MockRuntime bool
	TransferDir string

	RegistryHost      string
	RegistryProject   string
	RegistryUser      string
	RegistryPassword  string
	RegistryAuthHosts []string

	YunxiaoWebhookToken     string
	CredentialEncryptionKey string
	// PromotionApproval gates prod promotes behind a second identity
	// (requester ≠ approver). Disabling it only affects mock runtimes.
	PromotionApproval         bool
	NotificationWebhookURL    string
	NotificationWebhookSecret string
	NotificationWebhookCAFile string
	PlatformPublicURL         string
	// AdminInitialPassword seeds the first admin account on a fresh
	// install; ignored once any user exists.
	AdminInitialPassword string

	CleanupMaxAgeDays   int
	DriftPollInterval   time.Duration
	LocalCommandTimeout time.Duration
	SSHCommandTimeout   time.Duration
}

func Load() Config {
	_ = godotenv.Load()
	env := envOr("ENV", "dev")
	return Config{
		Env:         env,
		HTTPAddr:    envOr("HTTP_ADDR", ":8000"),
		FrontendDir: envOr("FRONTEND_DIR", "../frontend/dist"),
		DatabaseDSN: envOr("DATABASE_DSN", "deploy:deploy-platform@tcp(127.0.0.1:3306)/deploy_platform?parseTime=true&charset=utf8mb4"),
		MockRuntime: envBool("MOCK_RUNTIME", env == "dev"),
		TransferDir: envOr("TRANSFER_DIR", "/data/transfer_space"),

		RegistryHost:      envOr("REGISTRY_HOST", "registry.example.com"),
		RegistryProject:   envOr("REGISTRY_PROJECT", "Demo"),
		RegistryUser:      envOr("REGISTRY_USER", "admin"),
		RegistryPassword:  os.Getenv("REGISTRY_PASSWORD"),
		RegistryAuthHosts: csvEnv("REGISTRY_AUTH_HOSTS"),

		YunxiaoWebhookToken:       os.Getenv("YUNXIAO_WEBHOOK_TOKEN"),
		CredentialEncryptionKey:   os.Getenv("CREDENTIAL_ENCRYPTION_KEY"),
		PromotionApproval:         envBool("PROMOTION_APPROVAL", true),
		NotificationWebhookURL:    os.Getenv("NOTIFICATION_WEBHOOK_URL"),
		NotificationWebhookSecret: os.Getenv("NOTIFICATION_WEBHOOK_SECRET"),
		NotificationWebhookCAFile: os.Getenv("NOTIFICATION_WEBHOOK_CA_FILE"),
		PlatformPublicURL:         os.Getenv("PLATFORM_PUBLIC_URL"),
		AdminInitialPassword:      os.Getenv("ADMIN_INITIAL_PASSWORD"),

		CleanupMaxAgeDays:   envInt("CLEANUP_MAX_AGE_DAYS", 3),
		DriftPollInterval:   envOptionalDurationSeconds("DRIFT_POLL_INTERVAL_SECONDS"),
		LocalCommandTimeout: envDurationSeconds("LOCAL_COMMAND_TIMEOUT", 600*time.Second),
		SSHCommandTimeout:   envDurationSeconds("SSH_COMMAND_TIMEOUT", 180*time.Second),
	}
}

func csvEnv(key string) []string {
	var values []string
	for _, value := range strings.Split(os.Getenv(key), ",") {
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if value, err := strconv.ParseBool(os.Getenv(key)); err == nil {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if value, err := strconv.Atoi(os.Getenv(key)); err == nil && value > 0 {
		return value
	}
	return fallback
}

// envDurationSeconds reads a timeout expressed in whole seconds.
func envDurationSeconds(key string, fallback time.Duration) time.Duration {
	if value, err := strconv.Atoi(os.Getenv(key)); err == nil && value > 0 {
		return time.Duration(value) * time.Second
	}
	return fallback
}

// A zero interval disables optional polling. Invalid values become negative
// so application startup can reject the configuration instead of silently
// leaving continuous observation off.
func envOptionalDurationSeconds(key string) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 0 || seconds > 86400 {
		return -1
	}
	return time.Duration(seconds) * time.Second
}

// RequiresProductionApproval keeps real production policies independent of a
// development feature flag.
func (c Config) RequiresProductionApproval() bool { return c.PromotionApproval || !c.MockRuntime }
