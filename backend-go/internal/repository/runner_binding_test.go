package repository

import (
	"deploy-platform/backend-go/internal/service"
	"encoding/json"
	"github.com/DATA-DOG/go-sqlmock"
	"strings"
	"testing"
	"time"
)

func TestRunnerBindingPersistenceReloadAndSecretSeparation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now().UTC()
	b := service.RunnerBinding{AppID: 5, PipelineID: "123", PrivateClusterID: "build-1", PlatformURL: "https://deploy.internal", Binding: service.App{ID: 5, AppName: "orders", ImageRepository: "reg.io/orders/api"}, TokenHash: strings.Repeat("a", 64), TokenPrefix: "dpci_prefix", Enabled: true, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now, PipelineBackupCiphertext: "v1:encrypted-backup"}
	raw, _ := json.Marshal(b)
	if strings.Contains(string(raw), b.TokenHash) || strings.Contains(string(raw), b.PipelineBackupCiphertext) {
		t.Fatal("secrets leaked into public JSON")
	}
	mock.ExpectExec("INSERT INTO runner_bindings").WithArgs(b.AppID, b.TokenHash, string(raw), b.PipelineBackupCiphertext).WillReturnResult(sqlmock.NewResult(7, 1))
	saved, err := repo.SaveRunnerBinding(b)
	if err != nil || saved.ID != 7 {
		t.Fatalf("save %v", err)
	}
	raw, _ = json.Marshal(saved)
	mock.ExpectQuery("SELECT .* FROM runner_bindings ORDER BY id").WillReturnRows(sqlmock.NewRows([]string{"id", "app_id", "token_hash", "data_json", "pipeline_backup_ciphertext"}).AddRow(7, 5, saved.TokenHash, string(raw), saved.PipelineBackupCiphertext))
	rows, err := repo.RunnerBindings()
	if err != nil || len(rows) != 1 || rows[0].ID != 7 || rows[0].TokenHash != saved.TokenHash || rows[0].PipelineBackupCiphertext != saved.PipelineBackupCiphertext || rows[0].Binding.ImageRepository != saved.Binding.ImageRepository {
		t.Fatalf("reload %v %+v", err, rows)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestCloudConnectionPersistenceDoesNotExposePAT(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	c := service.YunxiaoConnection{Name: "cloud", Edition: "central", Domain: "openapi-rdc.aliyuncs.com", OrganizationID: "org1", TokenCiphertext: "v1:encrypted-pat"}
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), c.TokenCiphertext) {
		t.Fatal("ciphertext serialized")
	}
	mock.ExpectExec("INSERT INTO yunxiao_connections").WithArgs(c.TokenCiphertext, string(raw)).WillReturnResult(sqlmock.NewResult(2, 1))
	saved, err := repo.SaveYunxiaoConnection(c)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(saved)
	mock.ExpectQuery("SELECT .* FROM yunxiao_connections ORDER BY id").WillReturnRows(sqlmock.NewRows([]string{"id", "token_ciphertext", "data_json"}).AddRow(2, c.TokenCiphertext, string(raw)))
	rows, err := repo.YunxiaoConnections()
	if err != nil || len(rows) != 1 || rows[0].TokenCiphertext != c.TokenCiphertext {
		t.Fatal("connection reload failed")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
