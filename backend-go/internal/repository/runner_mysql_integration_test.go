package repository

import (
	"deploy-platform/backend-go/internal/service"
	"os"
	"strings"
	"testing"
	"time"
)

// Opt-in check against MySQL, using connection-local temporary tables to avoid
// changing existing authorizations or cloud credentials in the target database.
func TestRunnerMySQLRoundTrip(t *testing.T) {
	dsn := os.Getenv("DEPLOY_RUNNER_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("set DEPLOY_RUNNER_MYSQL_TEST_DSN for MySQL serialization check")
	}
	m, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.db.SetMaxOpenConns(1)
	m.db.SetMaxIdleConns(1)
	for _, name := range []string{"runner_bindings", "yunxiao_connections"} {
		var table, ddl string
		if err := m.db.QueryRow("SHOW CREATE TABLE "+name).Scan(&table, &ddl); err != nil {
			t.Fatal(err)
		}
		var lines []string
		for _, line := range strings.Split(ddl, "\n") {
			if !strings.Contains(line, "CONSTRAINT ") {
				lines = append(lines, line)
			}
		}
		ddl = strings.Replace(strings.Join(lines, "\n"), "CREATE TABLE ", "CREATE TEMPORARY TABLE ", 1)
		ddl = strings.ReplaceAll(ddl, ",\n)", "\n)")
		if _, err := m.db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	key, hash, prefix, err := service.NewRunnerToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.SaveRunnerBinding(service.RunnerBinding{AppID: 123, PipelineID: "42", PrivateClusterID: "build-test", PlatformURL: "http://internal", TokenHash: hash, TokenPrefix: prefix, Binding: service.App{ID: 123, AppName: "orders", ClusterID: 1, Namespace: "default", DeploymentName: "orders", ContainerName: "api", ImageRepository: "reg.io/orders/api"}, Enabled: true, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), PipelineBackupCiphertext: "encrypted-backup"})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := m.SaveYunxiaoConnection(service.YunxiaoConnection{Name: "isolated", Edition: "central", Domain: "openapi-rdc.aliyuncs.com", OrganizationID: "org", TokenCiphertext: "encrypted-pat", CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := m.RunnerBindings()
	if err != nil {
		t.Fatal(err)
	}
	connections, err := m.YunxiaoConnections()
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 || bindings[0].ID != b.ID || bindings[0].TokenHash != hash || bindings[0].Binding.ImageRepository != b.Binding.ImageRepository || bindings[0].PipelineBackupCiphertext != "encrypted-backup" {
		t.Fatal("binding metadata/secret did not round trip")
	}
	if len(connections) != 1 || connections[0].ID != conn.ID || connections[0].TokenCiphertext != "encrypted-pat" {
		t.Fatal("cloud credentials did not round trip")
	}
	var jsonData string
	if err := m.db.QueryRow("SELECT data_json FROM runner_bindings WHERE id=?", b.ID).Scan(&jsonData); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(jsonData, key) || strings.Contains(jsonData, hash) || strings.Contains(jsonData, "encrypted-backup") {
		t.Fatal("private fields leaked into public metadata")
	}
	b.Enabled = false
	b.TokenHash = strings.Repeat("a", 64)
	if _, err := m.SaveRunnerBinding(b); err != nil {
		t.Fatal(err)
	}
	bindings, err = m.RunnerBindings()
	if err != nil || bindings[0].Enabled || bindings[0].TokenHash != b.TokenHash {
		t.Fatal("updated authorization did not reload")
	}
}
