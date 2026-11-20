package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerComposeInitializesEveryMigrationInOrder(t *testing.T) {
	backendRoot := filepath.Clean("..")
	migrations, err := filepath.Glob(filepath.Join(backendRoot, "migrations", "*.sql"))
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("no SQL migrations found")
	}

	composePath := filepath.Join(backendRoot, "..", "infra", "docker-compose.yml")
	compose, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("read %s: %v", composePath, err)
	}

	lastIndex := -1
	for _, migration := range migrations {
		name := filepath.Base(migration)
		mount := "../backend-go/migrations/" + name + ":/docker-entrypoint-initdb.d/10-" + name + ":ro"
		index := strings.Index(string(compose), mount)
		if index < 0 {
			t.Errorf("docker-compose.yml does not mount migration %s", name)
			continue
		}
		if index <= lastIndex {
			t.Errorf("docker-compose.yml mounts %s out of migration order", name)
		}
		lastIndex = index
	}
}
