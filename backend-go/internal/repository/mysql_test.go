package repository

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/service"
	"github.com/DATA-DOG/go-sqlmock"
	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestReleaseMetadataJSONRoundTripsConfigurationSourceRevision(t *testing.T) {
	want := service.ReleaseMetadata{ConfigurationSource: &service.ConfigurationSourceRevision{
		Repository: "https://git.example.com/orders/deploy-config.git",
		Path:       "charts/orders",
		Revision:   strings.Repeat("a", 40),
		RecordedBy: "runner:4",
		RecordedAt: time.Date(2026, time.October, 14, 8, 0, 0, 0, time.UTC),
	}}
	var got service.ReleaseMetadata
	if err := json.Unmarshal([]byte(releaseMetadataJSON(want)), &got); err != nil {
		t.Fatal(err)
	}
	if got.ConfigurationSource == nil || *got.ConfigurationSource != *want.ConfigurationSource {
		t.Fatalf("release metadata lost configuration source revision: want=%+v got=%+v", want.ConfigurationSource, got.ConfigurationSource)
	}
}

func TestPendingDeployLogIDsAfterUsesBoundedKeysetQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}

	mock.ExpectQuery("SELECT id FROM deploy_logs WHERE status='pending' AND id>\\? ORDER BY id ASC LIMIT \\?").
		WithArgs(int64(7), 25).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(8).AddRow(11).AddRow(25))
	ids, err := repo.PendingDeployLogIDsAfter(7, 25)
	if err != nil || !reflect.DeepEqual(ids, []int64{8, 11, 25}) {
		t.Fatalf("pending IDs=%v err=%v", ids, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUserSessionStoragePersistsOnlyHashedTokenAndSupportsRevocation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	created := time.Date(2026, time.October, 15, 8, 0, 0, 0, time.UTC)
	session := service.Session{
		TokenHash: strings.Repeat("a", 64), UserID: 17, CreatedAt: created, ExpiresAt: created.Add(24 * time.Hour),
	}
	mock.ExpectExec("INSERT INTO user_sessions\\(token_hash,user_id,created_at,expires_at\\) VALUES\\(\\?,\\?,\\?,\\?\\)").
		WithArgs(session.TokenHash, session.UserID, session.CreatedAt, session.ExpiresAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.SaveSession(session); err != nil {
		t.Fatalf("save session: %v", err)
	}
	mock.ExpectQuery("SELECT token_hash,user_id,created_at,expires_at FROM user_sessions WHERE token_hash=\\?").
		WithArgs(session.TokenHash).
		WillReturnRows(sqlmock.NewRows([]string{"token_hash", "user_id", "created_at", "expires_at"}).
			AddRow(session.TokenHash, session.UserID, session.CreatedAt, session.ExpiresAt))
	got, err := repo.SessionByTokenHash(session.TokenHash)
	if err != nil || got != session {
		t.Fatalf("read session=%+v err=%v", got, err)
	}
	mock.ExpectExec("DELETE FROM user_sessions WHERE token_hash=\\?").WithArgs(session.TokenHash).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.DeleteSession(session.TokenHash); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	mock.ExpectQuery("SELECT token_hash,user_id,created_at,expires_at FROM user_sessions WHERE token_hash=\\?").
		WithArgs(session.TokenHash).WillReturnError(sql.ErrNoRows)
	if _, err := repo.SessionByTokenHash(session.TokenHash); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("missing session should map to ErrNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckSessionSchemaRequiresSessionMigration(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	mock.ExpectQuery("SELECT token_hash,user_id,created_at,expires_at FROM user_sessions LIMIT 0").
		WillReturnRows(sqlmock.NewRows([]string{"token_hash", "user_id", "created_at", "expires_at"}))
	if err := repo.CheckSessionSchema(); err != nil {
		t.Fatalf("session schema readiness: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAuthoritativeUserAndAPIKeyLookupsMapMissingRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	created := time.Date(2026, time.October, 15, 8, 0, 0, 0, time.UTC)
	userRow := []driver.Value{int64(17), "operator", "Operator", "operator", "bcrypt-hash", "", "", true, created, created}
	mock.ExpectQuery("SELECT " + userColumns + " FROM users WHERE id=\\?").WithArgs(int64(17)).
		WillReturnRows(sqlmock.NewRows(strings.Split(userColumns, ",")).AddRow(userRow...))
	user, err := repo.UserByID(17)
	if err != nil || user.ID != 17 || user.Role != service.RoleOperator || !user.Enabled {
		t.Fatalf("user lookup=%+v err=%v", user, err)
	}
	mock.ExpectQuery("SELECT " + userColumns + " FROM users WHERE username=\\?").WithArgs("operator").
		WillReturnRows(sqlmock.NewRows(strings.Split(userColumns, ",")).AddRow(userRow...))
	user, err = repo.UserByUsername("operator")
	if err != nil || user.ID != 17 {
		t.Fatalf("username lookup=%+v err=%v", user, err)
	}
	mock.ExpectQuery("SELECT " + userColumns + " FROM users WHERE id=\\?").WithArgs(int64(99)).
		WillReturnError(sql.ErrNoRows)
	if _, err := repo.UserByID(99); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("missing user error=%v", err)
	}
	apiKeyColumns := "id,name,key_hash,prefix,role,enabled,expires_at,last_used_at,created_at,updated_at"
	mock.ExpectQuery("SELECT " + apiKeyColumns + " FROM api_keys WHERE id=\\?").WithArgs(int64(22)).
		WillReturnRows(sqlmock.NewRows(strings.Split(apiKeyColumns, ",")).
			AddRow(22, "runner", strings.Repeat("a", 64), "dp_abcdefgh", service.RoleViewer, true, nil, nil, created, created))
	key, err := repo.APIKeyByID(22)
	if err != nil || key.ID != 22 || key.Name != "runner" || key.KeyHash != strings.Repeat("a", 64) {
		t.Fatalf("API key lookup=%+v err=%v", key, err)
	}
	mock.ExpectQuery("SELECT " + apiKeyColumns + " FROM api_keys WHERE id=\\?").WithArgs(int64(23)).
		WillReturnError(sql.ErrNoRows)
	if _, err := repo.APIKeyByID(23); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("missing API key error=%v", err)
	}
	keyHash := strings.Repeat("b", 64)
	mock.ExpectQuery("SELECT " + apiKeyColumns + " FROM api_keys WHERE key_hash=\\?").WithArgs(keyHash).
		WillReturnRows(sqlmock.NewRows(strings.Split(apiKeyColumns, ",")).
			AddRow(24, "runner-hash", keyHash, "dp_abcdefgh", service.RoleOperator, true, nil, nil, created, created))
	key, err = repo.APIKeyByHash(keyHash)
	if err != nil || key.ID != 24 || key.KeyHash != keyHash {
		t.Fatalf("API key hash lookup=%+v err=%v", key, err)
	}
	mock.ExpectQuery("SELECT " + apiKeyColumns + " FROM api_keys WHERE key_hash=\\?").WithArgs(keyHash).
		WillReturnError(sql.ErrNoRows)
	if _, err := repo.APIKeyByHash(keyHash); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("missing API key hash error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryPlanConfigurationAndScopeReviewPersistInSnapshotJSON(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	createdAt := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
	plan := service.DeliveryPlan{
		ReleaseID: 7, TargetClusterID: 2, TargetAppID: 9,
		Strategy: "rolling", Status: "preview_only", SnapshotHash: strings.Repeat("a", 64),
		RequestedBy: "alice", CreatedAt: createdAt,
		Snapshot: service.DeliveryPlanSnapshot{
			SchemaVersion: 12, Strategy: "rolling",
			Target: service.DeliveryPlanTarget{
				DeploymentPipeline: &service.YunxiaoDeploymentPipelineBinding{ConnectionID: 9, PipelineID: "123456"},
				DeploymentPipelineScopeReview: &service.DeliveryPipelineScopeReview{
					ID: 3, BindingID: 41, ConnectionID: 9, PipelineID: "123456",
					DefinitionHash: strings.Repeat("b", 64), WriteScope: service.PipelineScopeConfigurationOnly,
					Reviewer: "operator", ReviewedAt: createdAt,
				},
				ConfigurationReferences: []service.DeliveryConfigurationReference{
					{Kind: "ConfigMap", Name: "orders-config-v3", UID: "cm-uid", ResourceVersion: "12"},
					{Kind: "Secret", Name: "orders-credentials-v2", UID: "secret-uid", ResourceVersion: "15"},
				},
			},
		},
		Blockers: []string{},
	}
	snapshotJSON, err := json.Marshal(plan.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	blockersJSON, err := json.Marshal(plan.Blockers)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("INSERT IGNORE INTO delivery_plans").WithArgs(
		plan.ReleaseID, plan.TargetClusterID, plan.TargetAppID, plan.Strategy, plan.Status,
		plan.SnapshotHash, snapshotJSON, blockersJSON, plan.RequestedBy, plan.CreatedAt,
	).WillReturnResult(sqlmock.NewResult(29, 1))
	saved, created, err := repo.SaveDeliveryPlan(plan)
	if err != nil || !created || saved.ID != 29 {
		t.Fatalf("save plan=%+v created=%t err=%v", saved, created, err)
	}
	mock.ExpectQuery("SELECT .* FROM delivery_plans WHERE id=\\?").WithArgs(int64(29)).WillReturnRows(
		sqlmock.NewRows(strings.Split(deliveryPlanColumns, ",")).AddRow(
			saved.ID, saved.ReleaseID, saved.TargetClusterID, saved.TargetAppID, saved.Strategy,
			saved.Status, saved.SnapshotHash, snapshotJSON, blockersJSON, saved.RequestedBy, saved.CreatedAt,
		),
	)
	loaded, err := repo.FindDeliveryPlan(saved.ID)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	if loaded.Snapshot.SchemaVersion != 12 || len(loaded.Snapshot.Target.ConfigurationReferences) != 2 ||
		loaded.Snapshot.Target.ConfigurationReferences[0] != (service.DeliveryConfigurationReference{Kind: "ConfigMap", Name: "orders-config-v3", UID: "cm-uid", ResourceVersion: "12"}) ||
		loaded.Snapshot.Target.ConfigurationReferences[1] != (service.DeliveryConfigurationReference{Kind: "Secret", Name: "orders-credentials-v2", UID: "secret-uid", ResourceVersion: "15"}) ||
		loaded.Snapshot.Target.DeploymentPipelineScopeReview == nil || loaded.Snapshot.Target.DeploymentPipelineScopeReview.DefinitionHash != strings.Repeat("b", 64) {
		t.Fatalf("configuration snapshot changed across persistence: %+v", loaded.Snapshot.Target)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAtomicApprovalRollsBackWhenDecisionWriteFails(t *testing.T) {
	for _, failAt := range []string{"decision", "commit", "success"} {
		t.Run(failAt, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			repo := &MySQL{db: db}
			now := time.Now()
			expires := now.Add(time.Hour)
			snapshot := strings.Repeat("a", 64)
			actor := "bob"
			image := "reg.io/app@sha256:" + strings.Repeat("ab", 32)
			req := service.PromotionRequest{ID: 1, ReleaseID: 2, ProdClusterID: 3, Requester: "alice", Snapshot: snapshot, Status: "approved", Approver: &actor, DecidedAt: &now}
			task := service.DeployLog{ClusterID: 3, ReleaseID: 2, Namespace: "prod", DeploymentName: "app", ImageFull: &image, DeployType: "image_deploy", Status: "pending"}
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT .* FROM promotion_requests WHERE id=\\? FOR UPDATE").WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows(strings.Split(promotionRequestColumns, ",")).AddRow(1, 2, 3, "alice", "pending", nil, nil, nil, now, nil, expires, snapshot, 0, "", "", nil))
			mock.ExpectQuery("SELECT id FROM deploy_logs WHERE cluster_id=\\?").WithArgs(3, "prod", "app").WillReturnError(sql.ErrNoRows)
			mock.ExpectQuery("SELECT .* FROM image_releases WHERE id=\\? FOR UPDATE").WithArgs(int64(2)).WillReturnRows(sqlmock.NewRows(strings.Split(imageReleaseColumns, ",")).AddRow(2, "key", "event", "ci", nil, nil, nil, nil, nil, "app", image, "sha256:"+strings.Repeat("ab", 32), "v1", 1, 10, nil, nil, now, now, "{}"))
			mock.ExpectQuery("SELECT id,status FROM deploy_logs WHERE release_id=\\?").WithArgs(int64(2), 3).WillReturnError(sql.ErrNoRows)
			mock.ExpectExec("INSERT INTO deploy_logs").WillReturnResult(sqlmock.NewResult(20, 1))
			lock := service.ResourceLock{ClusterID: 3, Namespace: "prod", Kind: "deployment", Name: "app"}
			mock.ExpectExec("INSERT INTO delivery_resource_locks").WithArgs(resourceLockKey(lock), 3, "prod", "deployment", "app", int64(20)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("UPDATE image_releases SET prod_cluster_id=\\?").WithArgs(3, int64(20), int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
			decision := mock.ExpectExec("UPDATE promotion_requests SET status='approved'").WithArgs(&actor, int64(20), &now, int64(1))
			if failAt == "decision" {
				decision.WillReturnError(errors.New("db down"))
				mock.ExpectRollback()
			} else {
				decision.WillReturnResult(sqlmock.NewResult(0, 1))
				commit := mock.ExpectCommit()
				if failAt == "commit" {
					commit.WillReturnError(errors.New("commit failed"))
				}
			}
			got, _, log, err := repo.ApprovePromotion(req, task)
			if failAt == "success" {
				if err != nil || got.DeployLogID == nil || *got.DeployLogID != 20 || log.ID != 20 {
					t.Fatalf("atomic outcome: %+v %+v %v", got, log, err)
				}
			} else if err == nil {
				t.Fatal("failure hidden")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDeliveryEnvironmentEvidencePolicyReadsAndAppendsImmutableVersion(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	createdAt := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	kinds := `["ci","smoke"]`
	sources := `{"ci":["apikey:yunxiao"],"smoke":["apikey:smoke"]}`
	mock.ExpectQuery("SELECT .* FROM delivery_environment_evidence_policy_versions").WillReturnRows(
		sqlmock.NewRows(strings.Split(deliveryEnvironmentEvidencePolicyColumns, ",")).AddRow(9, 1, kinds, 120, sources, "admin", createdAt),
	)
	rows, err := repo.DeliveryEnvironmentEvidencePolicyVersions()
	if err != nil || len(rows) != 1 || rows[0].EnvironmentID != 9 || rows[0].Version != 1 || rows[0].MaxEvidenceAgeMinutes != 120 || rows[0].TrustedSubmitters["ci"][0] != "apikey:yunxiao" {
		t.Fatalf("loaded policy=%+v err=%v", rows, err)
	}

	policy := service.DeliveryEnvironmentEvidencePolicyVersion{
		EnvironmentID: 9, Version: 1, RequiredVerificationKinds: []string{"ci", "smoke"},
		MaxEvidenceAgeMinutes: 120,
		TrustedSubmitters:     map[string][]string{"ci": {"apikey:yunxiao"}, "smoke": {"apikey:smoke"}},
		CreatedBy:             "admin", CreatedAt: createdAt,
	}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT environment_tier,requires_review FROM delivery_environments WHERE id=\\? FOR UPDATE").WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"environment_tier", "requires_review"}).AddRow("production", false))
	mock.ExpectQuery("SELECT .* FROM delivery_environment_evidence_policy_versions WHERE environment_id=\\?").WithArgs(int64(9)).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO delivery_environment_evidence_policy_versions").
		WithArgs(int64(9), int64(1), []byte(kinds), 120, []byte(sources), "admin", createdAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	saved, created, err := repo.SaveDeliveryEnvironmentEvidencePolicyVersion(0, policy)
	if err != nil || !created || saved.Version != 1 {
		t.Fatalf("saved policy=%+v created=%v err=%v", saved, created, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClaimIsConditionalOnDurablePendingState(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	mock.ExpectExec("UPDATE deploy_logs SET status='running'.*WHERE id=\\? AND status='pending'").WithArgs(int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE deploy_logs SET status='running'.*WHERE id=\\? AND status='pending'").WithArgs(int64(1)).WillReturnResult(sqlmock.NewResult(0, 0))
	first, err := repo.ClaimLog(1)
	if err != nil || !first {
		t.Fatalf("claim=%v %v", first, err)
	}
	second, err := repo.ClaimLog(1)
	if err != nil || second {
		t.Fatalf("duplicate claim=%v %v", second, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClaimLogWithOwnershipFencesCompetingWorkers(t *testing.T) {
	t.Run("pending row is claimed", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		repo := &MySQL{db: db}
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT status,worker_claim_id FROM deploy_logs WHERE id=\\? FOR UPDATE").WithArgs(int64(8)).WillReturnRows(sqlmock.NewRows([]string{"status", "worker_claim_id"}).AddRow("pending", nil))
		mock.ExpectExec("UPDATE deploy_logs SET status='running'.*worker_claim_id=\\?.*WHERE id=\\? AND status='pending'").WithArgs("claim-a", int64(8)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		claimed, err := repo.ClaimLogWithOwnership(8, "claim-a")
		if err != nil || !claimed {
			t.Fatalf("claim=%v err=%v", claimed, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("foreign running owner is preserved", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		repo := &MySQL{db: db}
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT status,worker_claim_id FROM deploy_logs WHERE id=\\? FOR UPDATE").WithArgs(int64(8)).WillReturnRows(sqlmock.NewRows([]string{"status", "worker_claim_id"}).AddRow("running", "claim-a"))
		mock.ExpectCommit()
		claimed, err := repo.ClaimLogWithOwnership(8, "claim-b")
		if err != nil || claimed {
			t.Fatalf("competing claim=%v err=%v", claimed, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("same claim retries idempotently", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		repo := &MySQL{db: db}
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT status,worker_claim_id FROM deploy_logs WHERE id=\\? FOR UPDATE").WithArgs(int64(8)).WillReturnRows(sqlmock.NewRows([]string{"status", "worker_claim_id"}).AddRow("running", "claim-a"))
		mock.ExpectCommit()
		claimed, err := repo.ClaimLogWithOwnership(8, "claim-a")
		if err != nil || !claimed {
			t.Fatalf("same claim retry=%v err=%v", claimed, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDeployLogPersistsAndReadsEnvironmentTierSnapshot(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	tier := "production"
	logRow := service.DeployLog{
		ClusterID: 2, AppName: "shop", DeploymentName: "shop-api", ContainerName: "web",
		Namespace: "prod", Version: "v1", FileName: "registry", DeployType: "image_deploy",
		Status: "pending", EnvironmentTierSnapshot: &tier,
	}
	mock.ExpectExec("INSERT INTO deploy_logs.*environment_tier_snapshot").WithArgs(
		2, "shop", "shop-api", "web", "prod", "v1", "registry", nil,
		"image_deploy", "pending", nil, nil, nil, nil, nil, int64(0), int64(0), &tier,
	).WillReturnResult(sqlmock.NewResult(41, 1))
	saved, err := repo.SaveLog(logRow)
	if err != nil || saved.ID != 41 || saved.EnvironmentTierSnapshot == nil || *saved.EnvironmentTierSnapshot != "production" {
		t.Fatalf("save log tier snapshot: %+v %v", saved, err)
	}

	now := time.Now()
	mock.ExpectQuery("SELECT .* FROM deploy_logs WHERE id=\\?").WithArgs(int64(41)).WillReturnRows(
		sqlmock.NewRows(strings.Split(deployLogColumns, ",")).AddRow(
			41, 2, "shop", "shop-api", "web", "prod", "v1", "registry", nil,
			"image_deploy", "pending", nil, "[]", nil, nil, nil, now, int64(0), int64(0), "production", nil,
		),
	)
	loaded, err := repo.LogByID(41)
	if err != nil || loaded.EnvironmentTierSnapshot == nil || *loaded.EnvironmentTierSnapshot != "production" {
		t.Fatalf("read log tier snapshot: %+v %v", loaded, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSaveDeliveryPolicyVersionUsesLockedExpectedVersion(t *testing.T) {
	now := time.Now().UTC()
	t.Run("append", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		repo := &MySQL{db: db}
		policy := service.DeliveryPolicyVersion{BindingID: 41, Strategy: "blue_green", ServiceName: "orders-entry", CandidateRetentionHours: 48, Readiness: "configurable", CreatedBy: "alice", CreatedAt: now}
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT requires_review FROM delivery_bindings WHERE id=\\? FOR UPDATE").WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"requires_review"}).AddRow(false))
		mock.ExpectQuery("SELECT binding_id,version,strategy,service_name,candidate_retention_hours,canary_analysis_json,created_by,created_at FROM delivery_policy_versions WHERE binding_id=\\? ORDER BY version DESC LIMIT 1 FOR UPDATE").WithArgs(int64(41)).WillReturnError(sql.ErrNoRows)
		mock.ExpectExec("INSERT INTO delivery_policy_versions").WithArgs(int64(41), int64(1), "blue_green", "orders-entry", 48, nil, "alice", now).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		got, created, err := repo.SaveDeliveryPolicyVersion(0, policy)
		if err != nil || !created || got.Version != 1 || got.BindingID != 41 {
			t.Fatalf("saved=%+v created=%t err=%v", got, created, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("canary configuration is serialized with revision", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		repo := &MySQL{db: db}
		maxErrorRate := 0.02
		maxErrorIncrease := 0.01
		maxP95 := 250.0
		maxP95Increase := 0.2
		analysis := &service.CanaryAnalysisPolicy{
			GatewayRoute: &service.CanaryGatewayRouteRef{
				ConnectionID: 7, RouteID: "orders-public", StableBackendID: "orders-stable", CandidateBackendID: "orders-candidate",
			},
			MinRequests: 1000, MaxObservationAgeSeconds: 120,
			MaxCandidateErrorRate: &maxErrorRate, MaxErrorRateIncrease: &maxErrorIncrease,
			MaxCandidateP95LatencyMillis: &maxP95, MaxP95LatencyIncreaseRatio: &maxP95Increase,
			Stages: []service.CanaryStage{
				{CandidateWeightPercent: 10, ObservationDurationSeconds: 60},
				{CandidateWeightPercent: 50, ObservationDurationSeconds: 120},
				{CandidateWeightPercent: 100, ObservationDurationSeconds: 300},
			},
		}
		policy := service.DeliveryPolicyVersion{BindingID: 41, Strategy: "gateway_canary", CanaryAnalysis: analysis, CreatedBy: "alice", CreatedAt: now}
		serialized := []byte(`{"gateway_route":{"connection_id":7,"route_id":"orders-public","stable_backend_id":"orders-stable","candidate_backend_id":"orders-candidate"},"min_requests":1000,"max_observation_age_seconds":120,"max_candidate_error_rate":0.02,"max_error_rate_increase":0.01,"max_candidate_p95_latency_ms":250,"max_p95_latency_increase_ratio":0.2,"stages":[{"candidate_weight_percent":10,"observation_duration_seconds":60},{"candidate_weight_percent":50,"observation_duration_seconds":120},{"candidate_weight_percent":100,"observation_duration_seconds":300}]}`)
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT requires_review FROM delivery_bindings WHERE id=\\? FOR UPDATE").WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"requires_review"}).AddRow(false))
		mock.ExpectQuery("SELECT binding_id,version,strategy,service_name,candidate_retention_hours,canary_analysis_json,created_by,created_at FROM delivery_policy_versions WHERE binding_id=\\? ORDER BY version DESC LIMIT 1 FOR UPDATE").WithArgs(int64(41)).WillReturnError(sql.ErrNoRows)
		mock.ExpectExec("INSERT INTO delivery_policy_versions").WithArgs(int64(41), int64(1), "gateway_canary", "", 0, serialized, "alice", now).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		got, created, err := repo.SaveDeliveryPolicyVersion(0, policy)
		if err != nil || !created || got.CanaryAnalysis == nil || got.CanaryAnalysis.MinRequests != 1000 || *got.CanaryAnalysis.MaxCandidateErrorRate != 0.02 || got.CanaryAnalysis.GatewayRoute == nil || *got.CanaryAnalysis.GatewayRoute != *analysis.GatewayRoute {
			t.Fatalf("saved canary policy=%+v created=%t err=%v", got, created, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("stale", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		repo := &MySQL{db: db}
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT requires_review FROM delivery_bindings WHERE id=\\? FOR UPDATE").WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"requires_review"}).AddRow(false))
		mock.ExpectQuery("SELECT binding_id,version,strategy,service_name,candidate_retention_hours,canary_analysis_json,created_by,created_at FROM delivery_policy_versions WHERE binding_id=\\? ORDER BY version DESC LIMIT 1 FOR UPDATE").WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"binding_id", "version", "strategy", "service_name", "candidate_retention_hours", "canary_analysis_json", "created_by", "created_at"}).AddRow(41, 2, "rolling", "", 0, nil, "bob", now))
		mock.ExpectRollback()
		_, _, err = repo.SaveDeliveryPolicyVersion(0, service.DeliveryPolicyVersion{BindingID: 41, Strategy: "blue_green", ServiceName: "orders-entry", CreatedBy: "alice", CreatedAt: now})
		if !errors.Is(err, service.ErrConflict) {
			t.Fatalf("stale expected version error=%v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("no-op", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		repo := &MySQL{db: db}
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT requires_review FROM delivery_bindings WHERE id=\\? FOR UPDATE").WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"requires_review"}).AddRow(false))
		mock.ExpectQuery("SELECT binding_id,version,strategy,service_name,candidate_retention_hours,canary_analysis_json,created_by,created_at FROM delivery_policy_versions WHERE binding_id=\\? ORDER BY version DESC LIMIT 1 FOR UPDATE").WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"binding_id", "version", "strategy", "service_name", "candidate_retention_hours", "canary_analysis_json", "created_by", "created_at"}).AddRow(41, 2, "blue_green", "orders-entry", 48, nil, "alice", now))
		mock.ExpectCommit()
		got, created, err := repo.SaveDeliveryPolicyVersion(2, service.DeliveryPolicyVersion{BindingID: 41, Strategy: "blue_green", ServiceName: "orders-entry", CandidateRetentionHours: 48, CreatedBy: "bob", CreatedAt: now})
		if err != nil || created || got.Version != 2 || got.CreatedBy != "alice" {
			t.Fatalf("no-op=%+v created=%t err=%v", got, created, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDeliveryPolicyVersionsHydrateInBindingVersionOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now().UTC()
	canaryJSON := []byte(`{"gateway_route":{"connection_id":7,"route_id":"orders-public","stable_backend_id":"orders-stable","candidate_backend_id":"orders-candidate"},"min_requests":1000,"max_observation_age_seconds":120,"max_candidate_error_rate":0.02,"max_error_rate_increase":0.01,"max_candidate_p95_latency_ms":250,"max_p95_latency_increase_ratio":0.2,"stages":[{"candidate_weight_percent":10,"observation_duration_seconds":60},{"candidate_weight_percent":50,"observation_duration_seconds":120},{"candidate_weight_percent":100,"observation_duration_seconds":300}]}`)
	mock.ExpectQuery("SELECT binding_id,version,strategy,service_name,candidate_retention_hours,canary_analysis_json,created_by,created_at FROM delivery_policy_versions ORDER BY binding_id,version").
		WillReturnRows(sqlmock.NewRows([]string{"binding_id", "version", "strategy", "service_name", "candidate_retention_hours", "canary_analysis_json", "created_by", "created_at"}).
			AddRow(41, 1, "rolling", "", 0, nil, "migration-019", now).
			AddRow(41, 2, "blue_green", "orders-entry", 24, nil, "alice", now).
			AddRow(41, 3, "gateway_canary", "", 0, canaryJSON, "bob", now))
	rows, err := repo.DeliveryPolicyVersions()
	if err != nil || len(rows) != 3 || rows[0].BindingID != 41 || rows[0].Version != 1 || rows[1].Version != 2 || rows[1].ServiceName != "orders-entry" || rows[2].Version != 3 || rows[2].CanaryAnalysis == nil || rows[2].CanaryAnalysis.MinRequests != 1000 || *rows[2].CanaryAnalysis.MaxP95LatencyIncreaseRatio != 0.2 || rows[2].CanaryAnalysis.GatewayRoute == nil || rows[2].CanaryAnalysis.GatewayRoute.RouteID != "orders-public" {
		t.Fatalf("policy rows=%+v err=%v", rows, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAtomicApprovalCanAcquireStrategyLocks(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now()
	expires := now.Add(time.Hour)
	snapshot := strings.Repeat("a", 64)
	actor := "bob"
	image := "reg.io/app@sha256:" + strings.Repeat("ab", 32)
	req := service.PromotionRequest{ID: 1, ReleaseID: 2, ProdClusterID: 3, Requester: "alice", Snapshot: snapshot, Status: "approved", Approver: &actor, DecidedAt: &now}
	task := service.DeployLog{ClusterID: 3, ReleaseID: 2, Namespace: "prod", DeploymentName: "app", ImageFull: &image, DeployType: "image_deploy", Status: "pending"}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM promotion_requests WHERE id=\\? FOR UPDATE").WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows(strings.Split(promotionRequestColumns, ",")).AddRow(1, 2, 3, "alice", "pending", nil, nil, nil, now, nil, expires, snapshot, 0, "", "", nil))
	mock.ExpectQuery("SELECT id FROM deploy_logs WHERE cluster_id=\\?").WithArgs(3, "prod", "app").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT .* FROM image_releases WHERE id=\\? FOR UPDATE").WithArgs(int64(2)).WillReturnRows(sqlmock.NewRows(strings.Split(imageReleaseColumns, ",")).AddRow(2, "key", "event", "ci", nil, nil, nil, nil, nil, "app", image, "sha256:"+strings.Repeat("ab", 32), "v1", 1, 10, nil, nil, now, now, "{}"))
	mock.ExpectQuery("SELECT id,status FROM deploy_logs WHERE release_id=\\?").WithArgs(int64(2), 3).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO deploy_logs").WillReturnResult(sqlmock.NewResult(20, 1))
	locks := []service.ResourceLock{
		{ClusterID: 3, Namespace: "prod", Kind: "deployment", Name: "app"},
		{ClusterID: 3, Namespace: "prod", Kind: "service", Name: "entry"},
	}
	for _, lock := range locks {
		mock.ExpectExec("INSERT INTO delivery_resource_locks").WithArgs(resourceLockKey(lock), lock.ClusterID, lock.Namespace, lock.Kind, lock.Name, int64(20)).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec("UPDATE image_releases SET prod_cluster_id=\\?").WithArgs(3, int64(20), int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE promotion_requests SET status='approved'").WithArgs(&actor, int64(20), &now, int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	got, _, log, err := repo.ApprovePromotionWithLocks(req, task, locks)
	if err != nil || got.DeployLogID == nil || *got.DeployLogID != 20 || log.ID != 20 {
		t.Fatalf("approval with strategy locks: %+v %+v %v", got, log, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAtomicApprovalCreatesDeliveryRunAndInitialEvent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	snapshot := strings.Repeat("a", 64)
	actor := "bob"
	image := "reg.io/app@sha256:" + strings.Repeat("ab", 32)
	planID := int64(41)
	req := service.PromotionRequest{
		ID: 1, ReleaseID: 2, ProdClusterID: 3, Requester: "alice", Snapshot: snapshot,
		Status: "approved", Approver: &actor, DecidedAt: &now, DeliveryPlanID: &planID,
	}
	task := service.DeployLog{ClusterID: 3, ReleaseID: 2, Namespace: "prod", DeploymentName: "app", ImageFull: &image, DeployType: "image_deploy", Status: "pending"}
	run := service.DeliveryRun{
		DeliveryPlanID: planID, PromotionRequestID: req.ID, Strategy: "rolling", Status: "queued",
		CurrentStage: "queued", Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	lockedRequest := sqlmock.NewRows(strings.Split(promotionRequestColumns, ",")).AddRow(1, 2, 3, "alice", "pending", nil, nil, nil, now, nil, expires, snapshot, 0, "", "", planID)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM promotion_requests WHERE id=\\? FOR UPDATE").WithArgs(int64(1)).WillReturnRows(lockedRequest)
	mock.ExpectQuery("SELECT id FROM deploy_logs WHERE cluster_id=\\?").WithArgs(3, "prod", "app").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT .* FROM image_releases WHERE id=\\? FOR UPDATE").WithArgs(int64(2)).WillReturnRows(sqlmock.NewRows(strings.Split(imageReleaseColumns, ",")).AddRow(2, "key", "event", "ci", nil, nil, nil, nil, nil, "app", image, "sha256:"+strings.Repeat("ab", 32), "v1", 1, 10, nil, nil, now, now, "{}"))
	mock.ExpectQuery("SELECT id,status FROM deploy_logs WHERE release_id=\\?").WithArgs(int64(2), 3).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO deploy_logs").WillReturnResult(sqlmock.NewResult(20, 1))
	lock := service.ResourceLock{ClusterID: 3, Namespace: "prod", Kind: "deployment", Name: "app"}
	mock.ExpectExec("INSERT INTO delivery_resource_locks").WithArgs(resourceLockKey(lock), 3, "prod", "deployment", "app", int64(20)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE image_releases SET prod_cluster_id=\\?").WithArgs(3, int64(20), int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE promotion_requests SET status='approved'").WithArgs(&actor, int64(20), &now, int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO delivery_runs").WithArgs(planID, int64(1), int64(20), "rolling", "queued", "queued", int64(1), nil, now, now).WillReturnResult(sqlmock.NewResult(70, 1))
	mock.ExpectExec("INSERT INTO delivery_run_events").WithArgs(int64(70), int64(1), "run-created", "queued", "queued", "queued", "生产审批通过，发布运行已排队。", nil, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	saved, _, log, savedRun, err := repo.ApprovePromotionWithRun(req, task, nil, run)
	if err != nil || saved.DeployLogID == nil || *saved.DeployLogID != 20 || log.ID != 20 || savedRun.ID != 70 || savedRun.DeployLogID != 20 {
		t.Fatalf("atomic approval with run saved=%+v log=%+v run=%+v err=%v", saved, log, savedRun, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAppendDeliveryRunEventAdvancesVersionTransactionally(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now().UTC()
	columns := strings.Split(deliveryRunColumns, ",")
	eventColumns := strings.Split(deliveryRunEventColumns, ",")
	initialRun := sqlmock.NewRows(columns).AddRow(7, 41, 1, 20, "rolling", "running", "queued", 1, nil, now, now)
	updatedRun := sqlmock.NewRows(columns).AddRow(7, 41, 1, 20, "rolling", "running", "rollout", 2, nil, now, now)
	updatedEvent := sqlmock.NewRows(eventColumns).AddRow(9, 7, 2, "step:rollout", "stage", "rollout", "running", "等待工作负载就绪", nil, now)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE id=\\? FOR UPDATE").WithArgs(int64(7)).WillReturnRows(initialRun)
	mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), "step:rollout").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("UPDATE delivery_runs SET status=\\?").WithArgs("running", "rollout", int64(2), nil, sqlmock.AnyArg(), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO delivery_run_events").WithArgs(int64(7), int64(2), "step:rollout", "stage", "rollout", "running", "等待工作负载就绪", nil, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(9, 1))
	mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE id=\\?").WithArgs(int64(7)).WillReturnRows(updatedRun)
	mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), "step:rollout").WillReturnRows(updatedEvent)
	mock.ExpectCommit()

	run, event, err := repo.AppendDeliveryRunEvent(7, 1, service.DeliveryRunEvent{
		EventKey: "step:rollout", EventType: "stage", Stage: "rollout", Status: "running", Detail: "等待工作负载就绪",
	})
	if err != nil || run.Version != 2 || run.CurrentStage != "rollout" || event.Version != 2 || event.ID != 9 {
		t.Fatalf("append delivery run event run=%+v event=%+v err=%v", run, event, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAppendDeliveryRunEventCannotReopenTerminalRun(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now().UTC()
	columns := strings.Split(deliveryRunColumns, ",")
	terminalRun := sqlmock.NewRows(columns).AddRow(7, 41, 1, 20, "blue_green", "failed", "restored", 3, nil, now, now)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE id=\\? FOR UPDATE").WithArgs(int64(7)).WillReturnRows(terminalRun)
	mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), "late-worker-write").WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	if _, _, err := repo.AppendDeliveryRunEvent(7, 3, service.DeliveryRunEvent{
		EventKey: "late-worker-write", EventType: "recovery_required", Stage: "needs_attention", Status: "running",
	}); !errors.Is(err, service.ErrConflict) {
		t.Fatalf("terminal run was reopened: err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAppendDeliveryRunEventRejectsStaleVersion(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now().UTC()
	columns := strings.Split(deliveryRunColumns, ",")
	currentRun := sqlmock.NewRows(columns).AddRow(7, 41, 1, 20, "blue_green", "running", "awaiting_live_verification", 2, nil, now, now)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE id=\\? FOR UPDATE").WithArgs(int64(7)).WillReturnRows(currentRun)
	mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), "stale-stage").WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	if _, _, err := repo.AppendDeliveryRunEvent(7, 1, service.DeliveryRunEvent{
		EventKey: "stale-stage", EventType: "stage", Stage: "queued", Status: "running",
	}); !errors.Is(err, service.ErrConflict) {
		t.Fatalf("stale run version appended an event: err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAppendDeliveryRunEventAllowsTerminalAuditWithoutChangingStatus(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now().UTC()
	columns := strings.Split(deliveryRunColumns, ",")
	eventColumns := strings.Split(deliveryRunEventColumns, ",")
	initialRun := sqlmock.NewRows(columns).AddRow(7, 41, 1, 20, "blue_green", "failed", "restored", 3, "原始失败原因", now, now)
	updatedRun := sqlmock.NewRows(columns).AddRow(7, 41, 1, 20, "blue_green", "failed", "restored", 4, "原始失败原因", now, now)
	updatedEvent := sqlmock.NewRows(eventColumns).AddRow(10, 7, 4, "cleanup-audit", "control", "restored", "failed", "候选清理记录", nil, now)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE id=\\? FOR UPDATE").WithArgs(int64(7)).WillReturnRows(initialRun)
	mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), "cleanup-audit").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("UPDATE delivery_runs SET status=\\?").WithArgs("failed", "restored", int64(4), "原始失败原因", sqlmock.AnyArg(), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO delivery_run_events").WithArgs(int64(7), int64(4), "cleanup-audit", "control", "restored", "failed", "候选清理记录", nil, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(10, 1))
	mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE id=\\?").WithArgs(int64(7)).WillReturnRows(updatedRun)
	mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), "cleanup-audit").WillReturnRows(updatedEvent)
	mock.ExpectCommit()

	run, event, err := repo.AppendDeliveryRunEvent(7, 3, service.DeliveryRunEvent{
		EventKey: "cleanup-audit", EventType: "control", Stage: "restored", Status: "failed", Detail: "候选清理记录",
	})
	if err != nil || run.Status != "failed" || run.Version != 4 || run.ErrorMessage == nil || *run.ErrorMessage != "原始失败原因" || event.Status != "failed" {
		t.Fatalf("terminal audit changed run status: run=%+v event=%+v err=%v", run, event, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestTransitionDeliveryRunExecutionCannotReopenTerminalRun(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now().UTC()
	columns := strings.Split(deliveryRunColumns, ",")
	terminalRun := sqlmock.NewRows(columns).AddRow(7, 41, 1, 20, "blue_green", "failed", "restored", 3, "原始失败原因", now, now)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE deploy_log_id=\\? FOR UPDATE").WithArgs(int64(20)).WillReturnRows(terminalRun)
	mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), "stale-reopen").WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, _, _, err = repo.TransitionDeliveryRunExecution(service.DeployLog{ID: 20, Status: "running"}, 3, service.DeliveryRunEvent{
		EventKey: "stale-reopen", EventType: "recovery_required", Stage: "needs_attention", Status: "running",
	})
	if !errors.Is(err, service.ErrConflict) {
		t.Fatalf("terminal run was reopened: err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAppendDeliveryRunEventPersistsStructuredPayload(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now().UTC()
	payload := json.RawMessage(`{"schema_version":1,"stage_index":0,"phase":"observing"}`)
	columns := strings.Split(deliveryRunColumns, ",")
	eventColumns := strings.Split(deliveryRunEventColumns, ",")
	initialRun := sqlmock.NewRows(columns).AddRow(7, 41, 1, 20, "gateway_canary", "running", "queued", 1, nil, now, now)
	updatedRun := sqlmock.NewRows(columns).AddRow(7, 41, 1, 20, "gateway_canary", "running", "canary_stage_1", 2, nil, now, now)
	updatedEvent := sqlmock.NewRows(eventColumns).AddRow(9, 7, 2, "canary:stage-1", "canary_checkpoint", "canary_stage_1", "running", "观察开始", string(payload), now)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE id=\\? FOR UPDATE").WithArgs(int64(7)).WillReturnRows(initialRun)
	mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), "canary:stage-1").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("UPDATE delivery_runs SET status=\\?").WithArgs("running", "canary_stage_1", int64(2), nil, sqlmock.AnyArg(), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO delivery_run_events").WithArgs(int64(7), int64(2), "canary:stage-1", "canary_checkpoint", "canary_stage_1", "running", "观察开始", []byte(payload), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(9, 1))
	mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE id=\\?").WithArgs(int64(7)).WillReturnRows(updatedRun)
	mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), "canary:stage-1").WillReturnRows(updatedEvent)
	mock.ExpectCommit()

	_, saved, err := repo.AppendDeliveryRunEvent(7, 1, service.DeliveryRunEvent{
		EventKey: "canary:stage-1", EventType: "canary_checkpoint", Stage: "canary_stage_1",
		Status: "running", Detail: "观察开始", Payload: payload,
	})
	if err != nil || string(saved.Payload) != string(payload) {
		t.Fatalf("structured event payload=%s err=%v", saved.Payload, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineCheckpointUpdatesLegacyLogAndRunEventAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now().UTC()
	columns := strings.Split(deliveryRunColumns, ",")
	eventColumns := strings.Split(deliveryRunEventColumns, ",")
	initialRun := sqlmock.NewRows(columns).AddRow(7, 41, 1, 20, "rolling", "running", "queued", 1, nil, now, now)
	updatedRun := sqlmock.NewRows(columns).AddRow(7, 41, 1, 20, "rolling", "running", "rollout", 2, nil, now, now)
	updatedEvent := sqlmock.NewRows(eventColumns).AddRow(9, 7, 2, "step:rollout", "stage", "rollout", "running", "等待工作负载就绪", nil, now)
	step := "rollout"
	log := service.DeployLog{ID: 20, Status: "running", CurrentStep: &step, StepDetail: "[]", StartedAt: &now}
	event := service.DeliveryRunEvent{EventKey: "step:rollout", EventType: "stage", Stage: "rollout", Status: "running", Detail: "等待工作负载就绪"}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE deploy_log_id=\\? FOR UPDATE").WithArgs(int64(20)).WillReturnRows(initialRun)
	mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), "step:rollout").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("UPDATE deploy_logs SET status=").WithArgs("running", "rollout", nil, "[]", nil, &now, nil, int64(20)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE delivery_runs SET status=\\?").WithArgs("running", "rollout", int64(2), nil, sqlmock.AnyArg(), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO delivery_run_events").WithArgs(int64(7), int64(2), "step:rollout", "stage", "rollout", "running", "等待工作负载就绪", nil, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(9, 1))
	mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE id=\\?").WithArgs(int64(7)).WillReturnRows(updatedRun)
	mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), "step:rollout").WillReturnRows(updatedEvent)
	mock.ExpectCommit()

	run, savedEvent, err := repo.UpdateDeployLogAndAppendDeliveryRunEvent(log, event)
	if err != nil || run.Version != 2 || run.CurrentStage != "rollout" || savedEvent.Version != 2 || savedEvent.ID != 9 {
		t.Fatalf("atomic checkpoint run=%+v event=%+v err=%v", run, savedEvent, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineCheckpointRollsBackLogWhenRunEventCannotPersist(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now().UTC()
	columns := strings.Split(deliveryRunColumns, ",")
	run := sqlmock.NewRows(columns).AddRow(7, 41, 1, 20, "rolling", "running", "queued", 1, nil, now, now)
	step := "rollout"
	log := service.DeployLog{ID: 20, Status: "running", CurrentStep: &step, StepDetail: "[]", StartedAt: &now}
	event := service.DeliveryRunEvent{EventKey: "step:rollout", EventType: "stage", Stage: "rollout", Status: "running", Detail: "等待工作负载就绪"}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE deploy_log_id=\\? FOR UPDATE").WithArgs(int64(20)).WillReturnRows(run)
	mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), "step:rollout").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("UPDATE deploy_logs SET status=").WithArgs("running", "rollout", nil, "[]", nil, &now, nil, int64(20)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE delivery_runs SET status=\\?").WithArgs("running", "rollout", int64(2), nil, sqlmock.AnyArg(), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO delivery_run_events").WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()

	if _, _, err := repo.UpdateDeployLogAndAppendDeliveryRunEvent(log, event); err == nil {
		t.Fatal("expected checkpoint persistence error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineCheckpointCannotOverwriteOperatorPause(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now().UTC()
	columns := strings.Split(deliveryRunColumns, ",")
	paused := sqlmock.NewRows(columns).AddRow(7, 41, 1, 20, "rolling", "paused", "rollout_wait", 3, nil, now, now)
	step := "verify_image"
	log := service.DeployLog{ID: 20, Status: "running", CurrentStep: &step, StepDetail: "[]", StartedAt: &now}
	event := service.DeliveryRunEvent{EventKey: "step:verify_image", EventType: "stage", Stage: "verify_image", Status: "running", Detail: "执行阶段：verify_image"}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE deploy_log_id=\\? FOR UPDATE").WithArgs(int64(20)).WillReturnRows(paused)
	mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), "step:verify_image").WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	if _, _, err := repo.UpdateDeployLogAndAppendDeliveryRunEvent(log, event); !errors.Is(err, service.ErrDeliveryRunStopped) {
		t.Fatalf("paused pipeline checkpoint error=%v, want graceful stop", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVersionedDeliveryRunTransitionCommitsTaskRunEventAndLockReleaseTogether(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now().UTC()
	runColumns := strings.Split(deliveryRunColumns, ",")
	eventColumns := strings.Split(deliveryRunEventColumns, ",")
	initialRun := sqlmock.NewRows(runColumns).AddRow(7, 41, 1, 20, "blue_green", "running", "awaiting_live_verification", 3, nil, now, now)
	updatedRun := sqlmock.NewRows(runColumns).AddRow(7, 41, 1, 20, "blue_green", "success", "success", 4, nil, now, now)
	updatedEvent := sqlmock.NewRows(eventColumns).AddRow(11, 7, 4, "control:finalize", "completed", "success", "success", "业务验证通过。", nil, now)
	step := "success"
	log := service.DeployLog{ID: 20, Status: "success", CurrentStep: &step, StepDetail: "[]", StartedAt: &now, FinishedAt: &now}
	event := service.DeliveryRunEvent{EventKey: "control:finalize", EventType: "completed", Stage: "success", Status: "success", Detail: "业务验证通过。"}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE deploy_log_id=\\? FOR UPDATE").WithArgs(int64(20)).WillReturnRows(initialRun)
	mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), "control:finalize").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT id,status FROM deploy_logs WHERE id=\\? FOR UPDATE").WithArgs(int64(20)).WillReturnRows(sqlmock.NewRows([]string{"id", "status"}).AddRow(20, "running"))
	mock.ExpectExec("UPDATE deploy_logs SET status=").WithArgs("success", "success", nil, "[]", nil, &now, &now, int64(20)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM delivery_resource_locks WHERE deploy_log_id=\\?").WithArgs(int64(20)).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("UPDATE delivery_runs SET status=\\?.*WHERE id=\\? AND version=\\?").WithArgs("success", "success", int64(4), nil, sqlmock.AnyArg(), int64(7), int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO delivery_run_events").WithArgs(int64(7), int64(4), "control:finalize", "completed", "success", "success", "业务验证通过。", nil, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(11, 1))
	mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE id=\\?").WithArgs(int64(7)).WillReturnRows(updatedRun)
	mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), "control:finalize").WillReturnRows(updatedEvent)
	mock.ExpectCommit()

	run, savedEvent, duplicate, err := repo.TransitionDeliveryRunExecution(log, 3, event)
	if err != nil || duplicate || run.Status != "success" || run.Version != 4 || savedEvent.ID != 11 {
		t.Fatalf("versioned transition run=%+v event=%+v duplicate=%v err=%v", run, savedEvent, duplicate, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVersionedDeliveryRunTransitionAcceptsUnchangedDeployLog(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now().UTC()
	runColumns := strings.Split(deliveryRunColumns, ",")
	eventColumns := strings.Split(deliveryRunEventColumns, ",")
	initialRun := sqlmock.NewRows(runColumns).AddRow(7, 41, 1, 20, "blue_green", "running", "awaiting_candidate_review", 5, nil, now, now)
	updatedRun := sqlmock.NewRows(runColumns).AddRow(7, 41, 1, 20, "blue_green", "paused", "awaiting_candidate_review", 6, nil, now, now)
	updatedEvent := sqlmock.NewRows(eventColumns).AddRow(12, 7, 6, "control:pause-1", "control", "awaiting_candidate_review", "paused", "operator paused", nil, now)
	step := "awaiting_candidate_review"
	log := service.DeployLog{ID: 20, Status: "running", CurrentStep: &step, StepDetail: "[]"}
	event := service.DeliveryRunEvent{EventKey: "control:pause-1", EventType: "control", Stage: step, Status: "paused", Detail: "operator paused"}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE deploy_log_id=\\? FOR UPDATE").WithArgs(int64(20)).WillReturnRows(initialRun)
	mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), event.EventKey).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT id,status FROM deploy_logs WHERE id=\\? FOR UPDATE").WithArgs(int64(20)).WillReturnRows(sqlmock.NewRows([]string{"id", "status"}).AddRow(20, "running"))
	// MySQL reports zero changed rows for an UPDATE that writes the existing
	// task state. The locked existence check above distinguishes that from a
	// missing deploy log.
	mock.ExpectExec("UPDATE deploy_logs SET status=").WithArgs("running", step, nil, "[]", nil, nil, nil, int64(20)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("UPDATE delivery_runs SET status=\\?.*WHERE id=\\? AND version=\\?").WithArgs("paused", step, int64(6), nil, sqlmock.AnyArg(), int64(7), int64(5)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO delivery_run_events").WithArgs(int64(7), int64(6), event.EventKey, event.EventType, event.Stage, event.Status, event.Detail, nil, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(12, 1))
	mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE id=\\?").WithArgs(int64(7)).WillReturnRows(updatedRun)
	mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), event.EventKey).WillReturnRows(updatedEvent)
	mock.ExpectCommit()

	run, _, duplicate, err := repo.TransitionDeliveryRunExecution(log, 5, event)
	if err != nil || duplicate || run.Status != "paused" || run.Version != 6 {
		t.Fatalf("unchanged task transition run=%+v duplicate=%v err=%v", run, duplicate, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQueuedCanaryCancellationRequiresAndCommitsAgainstPendingLog(t *testing.T) {
	now := time.Now().UTC()
	runColumns := strings.Split(deliveryRunColumns, ",")
	eventColumns := strings.Split(deliveryRunEventColumns, ",")
	initialRunRows := func() *sqlmock.Rows {
		return sqlmock.NewRows(runColumns).AddRow(7, 41, 1, 20, "gateway_canary", "queued", "queued", 1, nil, now, now)
	}
	updatedRunRows := func() *sqlmock.Rows {
		return sqlmock.NewRows(runColumns).AddRow(7, 41, 1, 20, "gateway_canary", "cancelled", "cancelled", 2, nil, now, now)
	}
	message := "金丝雀在首次外部动作前取消；事务确认没有网关或集群动作记录，并撤销 Worker 认领。"
	step := "cancelled"
	log := service.DeployLog{ID: 20, Status: "failed", CurrentStep: &step, StepDetail: "[]", ErrorMessage: &message, FinishedAt: &now}
	event := service.DeliveryRunEvent{
		EventKey: "control:cancel-canary-queued-001", EventType: "control", Stage: "cancelled", Status: "cancelled",
		Detail: "action=cancel;actor=operator;" + message,
	}
	updatedEventRows := func() *sqlmock.Rows {
		return sqlmock.NewRows(eventColumns).AddRow(11, 7, 2, event.EventKey, "control", "cancelled", "cancelled", event.Detail, nil, now)
	}

	t.Run("pending task is cancelled atomically", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		repo := &MySQL{db: db}
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE deploy_log_id=\\? FOR UPDATE").WithArgs(int64(20)).WillReturnRows(initialRunRows())
		mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), event.EventKey).WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery("SELECT id,status,worker_claim_id FROM deploy_logs WHERE id=\\? FOR UPDATE").WithArgs(int64(20)).WillReturnRows(sqlmock.NewRows([]string{"id", "status", "worker_claim_id"}).AddRow(20, "pending", nil))
		mock.ExpectQuery("SELECT id FROM delivery_execution_actions WHERE deploy_log_id=\\? ORDER BY id LIMIT 1 FOR UPDATE").WithArgs(int64(20)).WillReturnError(sql.ErrNoRows)
		mock.ExpectExec("UPDATE deploy_logs SET status=").WithArgs("failed", &step, nil, "[]", &message, nil, &now, int64(20)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("DELETE FROM delivery_resource_locks WHERE deploy_log_id=\\?").WithArgs(int64(20)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("UPDATE delivery_runs SET status=\\?.*WHERE id=\\? AND version=\\?").WithArgs("cancelled", "cancelled", int64(2), nil, sqlmock.AnyArg(), int64(7), int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("INSERT INTO delivery_run_events").WithArgs(int64(7), int64(2), event.EventKey, event.EventType, event.Stage, event.Status, event.Detail, nil, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(11, 1))
		mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE id=\\?").WithArgs(int64(7)).WillReturnRows(updatedRunRows())
		mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), event.EventKey).WillReturnRows(updatedEventRows())
		mock.ExpectCommit()

		run, savedEvent, duplicate, err := repo.TransitionDeliveryRunExecution(log, 1, event)
		if err != nil || duplicate || run.Status != "cancelled" || run.Version != 2 || savedEvent.EventKey != event.EventKey {
			t.Fatalf("cancelled run=%+v event=%+v duplicate=%t err=%v", run, savedEvent, duplicate, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("claimed task without external action can be cancelled", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		repo := &MySQL{db: db}
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE deploy_log_id=\\? FOR UPDATE").WithArgs(int64(20)).WillReturnRows(initialRunRows())
		mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), event.EventKey).WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery("SELECT id,status,worker_claim_id FROM deploy_logs WHERE id=\\? FOR UPDATE").WithArgs(int64(20)).WillReturnRows(sqlmock.NewRows([]string{"id", "status", "worker_claim_id"}).AddRow(20, "running", "worker-claim"))
		mock.ExpectQuery("SELECT id FROM delivery_execution_actions WHERE deploy_log_id=\\? ORDER BY id LIMIT 1 FOR UPDATE").WithArgs(int64(20)).WillReturnError(sql.ErrNoRows)
		mock.ExpectExec("UPDATE deploy_logs SET status=").WithArgs("failed", &step, nil, "[]", &message, nil, &now, int64(20)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("DELETE FROM delivery_resource_locks WHERE deploy_log_id=\\?").WithArgs(int64(20)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("UPDATE delivery_runs SET status=\\?.*WHERE id=\\? AND version=\\?").WithArgs("cancelled", "cancelled", int64(2), nil, sqlmock.AnyArg(), int64(7), int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("INSERT INTO delivery_run_events").WithArgs(int64(7), int64(2), event.EventKey, event.EventType, event.Stage, event.Status, event.Detail, nil, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(11, 1))
		mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE id=\\?").WithArgs(int64(7)).WillReturnRows(updatedRunRows())
		mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), event.EventKey).WillReturnRows(updatedEventRows())
		mock.ExpectCommit()

		run, _, duplicate, err := repo.TransitionDeliveryRunExecution(log, 1, event)
		if err != nil || duplicate || run.Status != "cancelled" {
			t.Fatalf("claimed task cancellation run=%+v duplicate=%t err=%v", run, duplicate, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("external action intent prevents cancellation", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		repo := &MySQL{db: db}
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE deploy_log_id=\\? FOR UPDATE").WithArgs(int64(20)).WillReturnRows(initialRunRows())
		mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), event.EventKey).WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery("SELECT id,status,worker_claim_id FROM deploy_logs WHERE id=\\? FOR UPDATE").WithArgs(int64(20)).WillReturnRows(sqlmock.NewRows([]string{"id", "status", "worker_claim_id"}).AddRow(20, "running", "worker-claim"))
		mock.ExpectQuery("SELECT id FROM delivery_execution_actions WHERE deploy_log_id=\\? ORDER BY id LIMIT 1 FOR UPDATE").WithArgs(int64(20)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(99))
		mock.ExpectRollback()

		_, _, _, err = repo.TransitionDeliveryRunExecution(log, 1, event)
		if !errors.Is(err, service.ErrConflict) {
			t.Fatalf("cancellation with an external action intent error=%v, want conflict", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestVersionedDeliveryRunTransitionRejectsStaleVersionBeforeWrites(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now().UTC()
	runColumns := strings.Split(deliveryRunColumns, ",")
	initialRun := sqlmock.NewRows(runColumns).AddRow(7, 41, 1, 20, "blue_green", "running", "awaiting_live_verification", 3, nil, now, now)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE deploy_log_id=\\? FOR UPDATE").WithArgs(int64(20)).WillReturnRows(initialRun)
	mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(7), "control:stale").WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	_, _, _, err = repo.TransitionDeliveryRunExecution(service.DeployLog{ID: 20, Status: "running"}, 2, service.DeliveryRunEvent{
		EventKey: "control:stale", EventType: "stage", Stage: "awaiting_live_verification", Status: "running",
	})
	if !errors.Is(err, service.ErrConflict) {
		t.Fatalf("stale run version error=%v, want conflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateDeployLogAcquiresSortedResourceLocksInOneTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	task := service.DeployLog{ClusterID: 3, AppName: "app", DeploymentName: "candidate", ContainerName: "app", Namespace: "prod", Status: "pending"}
	locks := []service.ResourceLock{
		{ClusterID: 3, Namespace: "prod", Kind: "service", Name: "entry"},
		{ClusterID: 3, Namespace: "prod", Kind: "deployment", Name: "candidate"},
	}
	sorted, err := service.NormalizeResourceLocks(locks)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO deploy_logs").WillReturnResult(sqlmock.NewResult(41, 1))
	for _, lock := range sorted {
		mock.ExpectExec("INSERT INTO delivery_resource_locks").WithArgs(resourceLockKey(lock), lock.ClusterID, lock.Namespace, lock.Kind, lock.Name, int64(41)).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()

	created, err := repo.CreateDeployLog(task, locks)
	if err != nil || created.ID != 41 {
		t.Fatalf("CreateDeployLog=%+v err=%v", created, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateDeployLogRollsBackPartialLockAcquisition(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	task := service.DeployLog{ClusterID: 3, AppName: "app", DeploymentName: "candidate", Namespace: "prod", Status: "pending"}
	locks, err := service.NormalizeResourceLocks([]service.ResourceLock{
		{ClusterID: 3, Namespace: "prod", Kind: "deployment", Name: "candidate"},
		{ClusterID: 3, Namespace: "prod", Kind: "service", Name: "entry"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO deploy_logs").WillReturnResult(sqlmock.NewResult(42, 1))
	mock.ExpectExec("INSERT INTO delivery_resource_locks").WithArgs(resourceLockKey(locks[0]), locks[0].ClusterID, locks[0].Namespace, locks[0].Kind, locks[0].Name, int64(42)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO delivery_resource_locks").WithArgs(resourceLockKey(locks[1]), locks[1].ClusterID, locks[1].Namespace, locks[1].Kind, locks[1].Name, int64(42)).WillReturnError(&mysqldriver.MySQLError{Number: 1062, Message: "resource already locked"})
	mock.ExpectRollback()

	if _, err := repo.CreateDeployLog(task, locks); !errors.Is(err, service.ErrConflict) {
		t.Fatalf("lock conflict=%v, want ErrConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalExecutionUpdateReleasesResourceLocksAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now()
	task := service.DeployLog{ID: 41, Status: "success", FinishedAt: &now}
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE deploy_logs SET status=").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM delivery_resource_locks WHERE deploy_log_id=\\?").WithArgs(int64(41)).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()
	if err := repo.UpdateLogExecution(task); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerClaimFencesTaskAndExternalActionWrites(t *testing.T) {
	t.Run("matching claim updates checkpoint", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		repo := &MySQL{db: db}
		task := service.DeployLog{ID: 41, Status: "running", StepDetail: "[]"}
		mock.ExpectBegin()
		mock.ExpectExec("UPDATE deploy_logs SET status=.*worker_claim_id=CASE.*WHERE id=\\? AND worker_claim_id=\\?").WithArgs(
			"running", nil, nil, "[]", nil, nil, nil, "running", int64(41), "claim-a",
		).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		if err := repo.UpdateLogExecutionWithClaim(task, "claim-a"); err != nil {
			t.Fatalf("fenced checkpoint update: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("stale claim cannot journal external action", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		repo := &MySQL{db: db}
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT worker_claim_id FROM deploy_logs WHERE id=\\? FOR UPDATE").WithArgs(int64(41)).WillReturnRows(
			sqlmock.NewRows([]string{"worker_claim_id"}).AddRow("claim-a"),
		)
		mock.ExpectRollback()
		_, _, err = repo.BeginExecutionActionWithClaim(service.ExecutionAction{
			DeployLogID: 41, ActionKey: "stale", ActionKind: "set_image", Status: service.ExecutionActionIntent,
			Intent: json.RawMessage(`{"target":"registry.local/app@sha256:deadbeef"}`), CreatedAt: time.Now().UTC(),
		}, "claim-b")
		if !errors.Is(err, service.ErrConflict) {
			t.Fatalf("stale external action error=%v, want ErrConflict", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestWorkerClaimFencesVersionedRunTransition(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM delivery_runs WHERE deploy_log_id=\\? FOR UPDATE").WithArgs(int64(41)).WillReturnRows(
		sqlmock.NewRows(strings.Split(deliveryRunColumns, ",")).AddRow(10, 2, 3, 41, "blue_green", "running", "candidate_ready", 1, nil, now, now),
	)
	mock.ExpectQuery("SELECT .* FROM delivery_run_events WHERE run_id=\\? AND event_key=\\?").WithArgs(int64(10), "stale-transition").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT id,status,worker_claim_id FROM deploy_logs WHERE id=\\? FOR UPDATE").WithArgs(int64(41)).WillReturnRows(
		sqlmock.NewRows([]string{"id", "status", "worker_claim_id"}).AddRow(41, "running", "claim-a"),
	)
	mock.ExpectRollback()
	_, _, _, err = repo.TransitionDeliveryRunExecutionWithClaim(
		service.DeployLog{ID: 41, Status: "running"}, 1,
		service.DeliveryRunEvent{EventKey: "stale-transition", EventType: "progress", Stage: "candidate_ready", Status: "running", Detail: "stale worker"},
		"claim-b",
	)
	if !errors.Is(err, service.ErrConflict) {
		t.Fatalf("stale run transition error=%v, want ErrConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestResourceLockOwnerReadsSharedOwnershipIndex(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	lock := service.ResourceLock{ClusterID: 3, Namespace: "prod", Kind: "service", Name: "entry"}
	mock.ExpectQuery("SELECT deploy_log_id FROM delivery_resource_locks WHERE resource_key=\\?").WithArgs(resourceLockKey(lock)).WillReturnRows(sqlmock.NewRows([]string{"deploy_log_id"}).AddRow(41))
	mock.ExpectQuery("SELECT deploy_log_id FROM delivery_resource_locks WHERE resource_key=\\?").WithArgs(resourceLockKey(lock)).WillReturnError(sql.ErrNoRows)
	owner, found, err := repo.ResourceLockOwner(lock)
	if err != nil || !found || owner != 41 {
		t.Fatalf("owner=%d found=%v err=%v", owner, found, err)
	}
	if _, found, err := repo.ResourceLockOwner(lock); err != nil || found {
		t.Fatalf("missing lock found=%v err=%v", found, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestResourceLockLeaseUsesUniqueKeysAndCanBeReleased(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	locks, err := service.NormalizeResourceLocks([]service.ResourceLock{
		{ClusterID: 3, Namespace: "prod", Kind: "service", Name: "entry"},
		{ClusterID: 3, Namespace: "prod", Kind: "deployment", Name: "candidate"},
	})
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().Add(15 * time.Minute).UTC()
	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM delivery_resource_locks WHERE expires_at IS NOT NULL").WillReturnResult(sqlmock.NewResult(0, 0))
	for _, lock := range locks {
		mock.ExpectExec("INSERT INTO delivery_resource_locks").WithArgs(resourceLockKey(lock), lock.ClusterID, lock.Namespace, lock.Kind, lock.Name, int64(41), expiresAt).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()
	if err := repo.ReserveResourceLockLease(41, locks, expiresAt); err != nil {
		t.Fatalf("ReserveResourceLockLease: %v", err)
	}
	mock.ExpectBegin()
	for _, lock := range locks {
		mock.ExpectExec("DELETE FROM delivery_resource_locks WHERE resource_key=\\? AND deploy_log_id=\\? AND expires_at IS NOT NULL").WithArgs(resourceLockKey(lock), int64(41)).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()
	if err := repo.ReleaseResourceLockLease(41, locks); err != nil {
		t.Fatalf("ReleaseResourceLockLease: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionActionIntentAndReadbackAreDurableAndIdempotent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now().UTC().Truncate(time.Second)
	intent := json.RawMessage(`{"previous_image":"reg.io/app:v1","target_image":"reg.io/app:v2"}`)
	columns := strings.Split(executionActionColumns, ",")
	row := func(status string, observed any, outcome any, finished any) *sqlmock.Rows {
		return sqlmock.NewRows(columns).AddRow(9, 7, "set-image", "set_deployment_image", status, string(intent), observed, outcome, now, finished)
	}

	mock.ExpectExec("INSERT INTO delivery_execution_actions").WithArgs(int64(7), "set-image", "set_deployment_image", string(intent), now, now).WillReturnResult(sqlmock.NewResult(9, 1))
	mock.ExpectQuery("SELECT .* FROM delivery_execution_actions WHERE deploy_log_id=\\? AND action_key=\\?").WithArgs(int64(7), "set-image").WillReturnRows(row("intent", nil, nil, nil))
	created, inserted, err := repo.BeginExecutionAction(service.ExecutionAction{DeployLogID: 7, ActionKey: "set-image", ActionKind: "set_deployment_image", Status: service.ExecutionActionIntent, Intent: intent, CreatedAt: now})
	if err != nil || !inserted || created.ID != 9 || created.Status != service.ExecutionActionIntent {
		t.Fatalf("BeginExecutionAction=%+v inserted=%v err=%v", created, inserted, err)
	}

	mock.ExpectExec("INSERT INTO delivery_execution_actions").WillReturnResult(sqlmock.NewResult(9, 0))
	mock.ExpectQuery("SELECT .* FROM delivery_execution_actions WHERE deploy_log_id=\\? AND action_key=\\?").WithArgs(int64(7), "set-image").WillReturnRows(row("intent", nil, nil, nil))
	_, inserted, err = repo.BeginExecutionAction(service.ExecutionAction{DeployLogID: 7, ActionKey: "set-image", ActionKind: "set_deployment_image", Status: service.ExecutionActionIntent, Intent: intent, CreatedAt: now})
	if err != nil || inserted {
		t.Fatalf("idempotent BeginExecutionAction inserted=%v err=%v", inserted, err)
	}

	observed := json.RawMessage(`{"current_image":"reg.io/app:v2","readback":"completed"}`)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM delivery_execution_actions WHERE deploy_log_id=\\? AND action_key=\\? FOR UPDATE").WithArgs(int64(7), "set-image").WillReturnRows(row("intent", nil, nil, nil))
	mock.ExpectExec("UPDATE delivery_execution_actions SET status=").WithArgs("applied", string(observed), "target_confirmed", sqlmock.AnyArg(), sqlmock.AnyArg(), int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT .* FROM delivery_execution_actions WHERE id=\\?").WithArgs(int64(9)).WillReturnRows(row("applied", string(observed), "target_confirmed", now))
	mock.ExpectCommit()
	completed, err := repo.CompleteExecutionAction(7, "set-image", service.ExecutionActionApplied, observed, "target_confirmed", now)
	if err != nil || completed.Status != service.ExecutionActionApplied || completed.OutcomeCode != "target_confirmed" {
		t.Fatalf("CompleteExecutionAction=%+v err=%v", completed, err)
	}

	mock.ExpectQuery("SELECT .* FROM delivery_execution_actions WHERE deploy_log_id=\\? ORDER BY id").WithArgs(int64(7)).WillReturnRows(row("applied", string(observed), "target_confirmed", now))
	rows, err := repo.ExecutionActions(7)
	if err != nil || len(rows) != 1 || rows[0].Status != service.ExecutionActionApplied {
		t.Fatalf("ExecutionActions=%+v err=%v", rows, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReopenInterruptedDeployLogReacquiresLockAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Now()
	columns := strings.Split(deployLogColumns, ",")
	interrupted := sqlmock.NewRows(columns).AddRow(7, 1, "app", "api", "api", "prod", "v2", "", "reg.io/api:v2", "image_deploy", "interrupted", "k8s_set_image", "[]", "unknown", now, now, now, 42, 0, nil, nil)
	pending := sqlmock.NewRows(columns).AddRow(7, 1, "app", "api", "api", "prod", "v2", "", "reg.io/api:v2", "image_deploy", "pending", nil, "[]", nil, nil, nil, now, 42, 0, nil, nil)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM deploy_logs WHERE id=\\? FOR UPDATE").WithArgs(int64(7)).WillReturnRows(interrupted)
	lock := service.ResourceLock{ClusterID: 1, Namespace: "prod", Kind: "deployment", Name: "api"}
	mock.ExpectExec("INSERT INTO delivery_resource_locks").WithArgs(resourceLockKey(lock), 1, "prod", "deployment", "api", int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE deploy_logs SET status='pending'").WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT .* FROM deploy_logs WHERE id=\\?").WithArgs(int64(7)).WillReturnRows(pending)
	mock.ExpectCommit()

	got, err := repo.ReopenInterruptedDeployLog(7)
	if err != nil || got.Status != "pending" || got.StepDetail != "[]" || got.ErrorMessage != nil {
		t.Fatalf("ReopenInterruptedDeployLog=%+v err=%v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseEvidenceHistorySurvivesMetadataPersistence(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	ci := service.VerificationEvidence{Digest: "sha256:" + strings.Repeat("ab", 32), Status: "passed", Kind: "ci", RecordedBy: "runner:4", RecordedAt: now, ValidUntil: now.Add(time.Hour)}
	smoke := service.VerificationEvidence{Digest: ci.Digest, Status: "failed", Kind: "smoke", RecordedBy: "runner:4", RecordedAt: now.Add(time.Minute), ValidUntil: now.Add(time.Hour)}
	scan := service.VerificationEvidence{Digest: ci.Digest, Status: "passed", Kind: "scan", RecordedBy: "scanner:1", RecordedAt: now.Add(2 * time.Minute), ValidUntil: now.Add(time.Hour)}
	metadata := service.ReleaseMetadata{
		DigestSource:        "ci",
		SourceRepositories:  []string{"codeup.aliyun.com/team/app"},
		SourcePath:          "services/api",
		Verification:        &smoke,
		VerificationHistory: []service.VerificationEvidence{ci, smoke},
		Scan:                &scan,
		ScanHistory:         []service.VerificationEvidence{scan},
	}
	raw := releaseMetadataJSON(metadata)
	mock.ExpectExec("UPDATE image_releases SET metadata=\\? WHERE id=\\?").WithArgs(raw, int64(8)).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.UpdateReleaseMetadata(8, metadata); err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("SELECT .* FROM image_releases WHERE event_key=\\?").WithArgs("event-key").WillReturnRows(
		sqlmock.NewRows(strings.Split(imageReleaseColumns, ",")).AddRow(
			8, "event-key", "event", "ci", nil, nil, nil, nil, nil, "app", "reg.io/app:v1", ci.Digest,
			"v1", 1, 7, nil, nil, now, now, raw,
		),
	)
	loaded, err := repo.imageReleaseByEventKey("event-key")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Metadata.VerificationHistory) != 2 || len(loaded.Metadata.ScanHistory) != 1 || loaded.Metadata.Verification == nil || loaded.Metadata.Verification.Kind != "smoke" {
		t.Fatalf("evidence history did not round trip: %+v", loaded.Metadata)
	}
	if len(loaded.Metadata.SourceRepositories) != 1 || loaded.Metadata.SourceRepositories[0] != "codeup.aliyun.com/team/app" || loaded.Metadata.SourcePath != "services/api" {
		t.Fatalf("build source metadata did not round trip: repositories=%#v path=%q", loaded.Metadata.SourceRepositories, loaded.Metadata.SourcePath)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
