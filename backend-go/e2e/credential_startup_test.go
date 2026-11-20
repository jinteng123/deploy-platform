package e2e

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"os/exec"
	"strings"
	"testing"

	"deploy-platform/backend-go/internal/config"
	"deploy-platform/backend-go/internal/httpapi"
	"deploy-platform/backend-go/internal/service"
)

func TestApplicationRestartKeepsStoredClusterCredentialsReadable(t *testing.T) {
	if os.Getenv("KB_E2E_MYSQL_RESTART") != "1" {
		t.Skip("KB_E2E_MYSQL_RESTART != 1 — skipping disposable MySQL credential startup test")
	}

	mysql := startMigrationE2EMySQL(t)
	applyE2ESchemaThroughMigration(t, mysql.container, mysql.rootPassword, mysql.database, "036")

	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		t.Fatalf("generate test encryption key: %v", err)
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	cipher, err := service.NewCredentialCipher(key)
	if err != nil {
		t.Fatalf("create test credential cipher: %v", err)
	}
	const credential = "test-cluster-token-do-not-log"
	sealed, err := cipher.Encrypt(credential)
	if err != nil {
		t.Fatalf("encrypt test cluster credential: %v", err)
	}
	if _, err := mysql.db.Exec(`INSERT INTO clusters(name,env_code,environment,environment_tier,deploy_policy,connection_type,api_server,credential_ciphertext,enabled,k8s_master_host,k8s_master_port,default_namespace)
		VALUES('credential restart test','cred-restart','test','non_production','manual','token','https://kubernetes.example.test',?,1,'',0,'default')`, sealed); err != nil {
		t.Fatalf("persist encrypted test cluster credential: %v", err)
	}

	cfg := config.Config{
		Env:                     "prod",
		DatabaseDSN:             mysql.dsn,
		CredentialEncryptionKey: key,
		PromotionApproval:       true,
		AdminInitialPassword:    "e2e-temporary-admin-password",
		TransferDir:             t.TempDir(),
	}
	preflightCfg := cfg
	preflightCfg.MockRuntime = true
	if err := httpapi.ValidateStoredCredentials(preflightCfg); err != nil {
		t.Fatalf("credential preflight should verify persisted credentials even when MOCK_RUNTIME is set: %v", err)
	}
	usersBeforeCLI, err := countCredentialTestUsers(mysql)
	if err != nil || usersBeforeCLI != 0 {
		t.Fatalf("fresh database should not have users before read-only preflight: count=%d err=%v", usersBeforeCLI, err)
	}
	missingPasswordOutput, missingPasswordErr := runCredentialCheckCLIWithAdmin(t, mysql.dsn, key, "")
	if missingPasswordErr == nil || !strings.Contains(string(missingPasswordOutput), "ADMIN_INITIAL_PASSWORD") {
		t.Fatalf("check-credentials CLI should reject a fresh real database without an initial-admin password: err=%v output=%s", missingPasswordErr, missingPasswordOutput)
	}
	goodOutput, err := runCredentialCheckCLI(t, mysql.dsn, key)
	if err != nil || !strings.Contains(string(goodOutput), "当前配置中的凭据密钥") {
		t.Fatalf("check-credentials CLI should pass with the stored key: err=%v output=%s", err, goodOutput)
	}
	usersAfterCLI, err := countCredentialTestUsers(mysql)
	if err != nil || usersAfterCLI != 0 {
		t.Fatalf("check-credentials CLI must not write users or other bootstrap state: count=%d err=%v", usersAfterCLI, err)
	}
	first, err := httpapi.NewApplication(cfg)
	if err != nil {
		t.Fatalf("start backend with the key used to save the cluster credential: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("stop first backend application: %v", err)
	}
	second, err := httpapi.NewApplication(cfg)
	if err != nil {
		t.Fatalf("restart backend with the same key and persisted database: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("stop restarted backend application: %v", err)
	}

	wrongBytes := make([]byte, 32)
	for i := range wrongBytes {
		wrongBytes[i] = keyBytes[i] ^ 0xff
	}
	wrongKeyConfig := cfg
	wrongKeyConfig.CredentialEncryptionKey = base64.StdEncoding.EncodeToString(wrongBytes)
	wrongKeyConfig.MockRuntime = true
	if err := httpapi.ValidateStoredCredentials(wrongKeyConfig); err == nil {
		t.Fatal("credential preflight accepted a wrong key because MOCK_RUNTIME was set")
	} else if !strings.Contains(err.Error(), "credential restart test") || strings.Contains(err.Error(), credential) || strings.Contains(err.Error(), sealed) || strings.Contains(err.Error(), wrongKeyConfig.CredentialEncryptionKey) {
		t.Fatalf("preflight error should identify the affected cluster without leaking credential material: %v", err)
	}
	wrongOutput, err := runCredentialCheckCLI(t, mysql.dsn, wrongKeyConfig.CredentialEncryptionKey)
	if err == nil || !strings.Contains(string(wrongOutput), "credential restart test") || strings.Contains(string(wrongOutput), credential) || strings.Contains(string(wrongOutput), sealed) || strings.Contains(string(wrongOutput), wrongKeyConfig.CredentialEncryptionKey) {
		t.Fatalf("check-credentials CLI should reject a wrong key safely: err=%v output=%s", err, wrongOutput)
	}

	cfg.CredentialEncryptionKey = wrongKeyConfig.CredentialEncryptionKey
	if app, err := httpapi.NewApplication(cfg); err == nil {
		_ = app.Close()
		t.Fatal("backend started with a different encryption key while a direct cluster credential exists")
	} else if !strings.Contains(err.Error(), "credential restart test") || !strings.Contains(err.Error(), "恢复保存该凭据时使用的 CREDENTIAL_ENCRYPTION_KEY") {
		t.Fatalf("wrong-key startup error should identify the affected cluster and recovery action: %v", err)
	} else if strings.Contains(err.Error(), credential) || strings.Contains(err.Error(), sealed) || strings.Contains(err.Error(), cfg.CredentialEncryptionKey) {
		t.Fatalf("wrong-key startup error leaked credential material: %v", err)
	}
}

func countCredentialTestUsers(mysql migrationE2EMySQL) (int, error) {
	var count int
	err := mysql.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
	return count, err
}

func runCredentialCheckCLI(t *testing.T, dsn, key string) ([]byte, error) {
	return runCredentialCheckCLIWithAdmin(t, dsn, key, "e2e-temporary-admin-password")
}

func runCredentialCheckCLIWithAdmin(t *testing.T, dsn, key, adminPassword string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("go", "run", "./cmd/server", "check-credentials")
	cmd.Dir = backendRoot(t)
	cmd.Env = credentialTestEnvironment(dsn, key, adminPassword)
	return cmd.CombinedOutput()
}

func credentialTestEnvironment(dsn, key, adminPassword string) []string {
	values := map[string]string{
		"DATABASE_DSN":              dsn,
		"CREDENTIAL_ENCRYPTION_KEY": key,
		"ADMIN_INITIAL_PASSWORD":    adminPassword,
		"MOCK_RUNTIME":              "true",
		"ENV":                       "dev",
	}
	result := make([]string, 0, len(os.Environ())+len(values))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, replaced := values[name]; !replaced {
			result = append(result, entry)
		}
	}
	for name, value := range values {
		result = append(result, name+"="+value)
	}
	return result
}
