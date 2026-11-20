package config

import (
	"os"
	"testing"
	"time"
)

// isolateEnv clears every variable Load reads and chdirs into an empty temp
// dir so godotenv cannot pick up a developer's real .env.
func isolateEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"ENV", "HTTP_ADDR", "FRONTEND_DIR", "DATABASE_DSN",
		"MOCK_RUNTIME", "TRANSFER_DIR", "REGISTRY_HOST", "REGISTRY_PROJECT",
		"REGISTRY_USER", "REGISTRY_PASSWORD", "REGISTRY_AUTH_HOSTS", "CLEANUP_MAX_AGE_DAYS",
		"LOCAL_COMMAND_TIMEOUT", "SSH_COMMAND_TIMEOUT",
		"DRIFT_POLL_INTERVAL_SECONDS",
		"YUNXIAO_WEBHOOK_TOKEN", "TEST_ENV_CODE", "PROD_ENV_CODE",
		"PROMOTION_APPROVAL", "NOTIFICATION_WEBHOOK_URL", "NOTIFICATION_WEBHOOK_SECRET", "NOTIFICATION_WEBHOOK_CA_FILE", "PLATFORM_PUBLIC_URL", "ADMIN_INITIAL_PASSWORD", "CREDENTIAL_ENCRYPTION_KEY",
	} {
		t.Setenv(key, "")
	}
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
}

func TestLoadProdDoesNotMockByDefault(t *testing.T) {
	isolateEnv(t)
	t.Setenv("ENV", "prod")
	cfg := Load()
	if cfg.MockRuntime {
		t.Fatal("prod must not default to mock runtime")
	}
}

func TestLoadMockRuntimeExplicit(t *testing.T) {
	isolateEnv(t)
	t.Setenv("MOCK_RUNTIME", "true")
	if !Load().MockRuntime {
		t.Fatal("MOCK_RUNTIME=true must enable mock even in prod")
	}
	isolateEnv(t)
	t.Setenv("ENV", "prod")
	t.Setenv("MOCK_RUNTIME", "false")
	if Load().MockRuntime {
		t.Fatal("MOCK_RUNTIME=false must disable mock")
	}
	t.Setenv("MOCK_RUNTIME", "notabool")
	if Load().MockRuntime {
		t.Fatal("invalid MOCK_RUNTIME must fall back to prod default (false)")
	}
}

func TestLoadDevMocksByDefault(t *testing.T) {
	isolateEnv(t)
	if !Load().MockRuntime {
		t.Fatal("dev must default to mock runtime")
	}
}

func TestLoadInvalidIntFallsBack(t *testing.T) {
	isolateEnv(t)
	for _, bad := range []string{"0", "-5", "abc"} {
		t.Setenv("CLEANUP_MAX_AGE_DAYS", bad)
		if got := Load().CleanupMaxAgeDays; got != 3 {
			t.Fatalf("CLEANUP_MAX_AGE_DAYS=%q → %d, want fallback 3", bad, got)
		}
	}
	t.Setenv("CLEANUP_MAX_AGE_DAYS", "7")
	if got := Load().CleanupMaxAgeDays; got != 7 {
		t.Fatalf("valid value ignored: %d", got)
	}
	t.Setenv("LOCAL_COMMAND_TIMEOUT", "0")
	if got := Load().LocalCommandTimeout; got != 600*time.Second {
		t.Fatalf("timeout fallback broken: %v", got)
	}
}

func TestLoadDefaults(t *testing.T) {
	isolateEnv(t)
	cfg := Load()
	if cfg.TransferDir != "/data/transfer_space" {
		t.Fatalf("TransferDir=%s", cfg.TransferDir)
	}
	if cfg.RegistryHost != "registry.example.com" || cfg.RegistryProject != "Demo" {
		t.Fatalf("registry defaults broken: %s/%s", cfg.RegistryHost, cfg.RegistryProject)
	}
}

func TestLoadPlatformPublicURL(t *testing.T) {
	isolateEnv(t)
	t.Setenv("PLATFORM_PUBLIC_URL", "https://deploy.example.com/platform")
	if got := Load().PlatformPublicURL; got != "https://deploy.example.com/platform" {
		t.Fatalf("PlatformPublicURL=%q", got)
	}
}

func TestLoadRegistryAuthHosts(t *testing.T) {
	isolateEnv(t)
	t.Setenv("REGISTRY_AUTH_HOSTS", " auth.example.com, token.example.com ,, ")
	got := Load().RegistryAuthHosts
	if len(got) != 2 || got[0] != "auth.example.com" || got[1] != "token.example.com" {
		t.Fatalf("RegistryAuthHosts=%v", got)
	}
}

func TestLoadOptionalDriftPollIntervalRejectsInvalidValues(t *testing.T) {
	isolateEnv(t)
	if got := Load().DriftPollInterval; got != 0 {
		t.Fatalf("unset polling interval=%v, want disabled", got)
	}
	for _, invalid := range []string{"bad", "-1", "86401"} {
		t.Setenv("DRIFT_POLL_INTERVAL_SECONDS", invalid)
		if got := Load().DriftPollInterval; got >= 0 {
			t.Fatalf("invalid polling interval %q became %v", invalid, got)
		}
	}
	t.Setenv("DRIFT_POLL_INTERVAL_SECONDS", "300")
	if got := Load().DriftPollInterval; got != 5*time.Minute {
		t.Fatalf("polling interval=%v, want five minutes", got)
	}
	t.Setenv("DRIFT_POLL_INTERVAL_SECONDS", "0")
	if got := Load().DriftPollInterval; got != 0 {
		t.Fatalf("zero polling interval=%v, want disabled", got)
	}
}
