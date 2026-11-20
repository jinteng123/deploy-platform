package e2e

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/repository"
	"deploy-platform/backend-go/internal/service"
	_ "github.com/go-sql-driver/mysql"
)

func TestMigrations029And030UpgradeExistingDatabase(t *testing.T) {
	if os.Getenv("KB_E2E_MYSQL_MIGRATION") != "1" {
		t.Skip("KB_E2E_MYSQL_MIGRATION != 1 — skipping disposable MySQL migration upgrade test")
	}
	mysql := startMigrationE2EMySQL(t)
	applyE2ESchemaThroughMigration(t, mysql.container, mysql.rootPassword, mysql.database, "028")
	db := mysql.db

	const appName = "migration-029-preserve-app"
	if _, err := db.Exec(`INSERT INTO apps(cluster_id,app_name,match_keyword,deployment_name,container_name,namespace,harbor_project)
		VALUES(1,?,?,?,?,'default','library')`, appName, appName, appName, "api"); err != nil {
		t.Fatalf("seed app on schema 028: %v", err)
	}
	applyE2EMigrationFile(t, mysql.container, mysql.rootPassword, mysql.database, "029_component_deployment_config_source.sql")

	var count int
	var repoURL, configPath string
	if err := db.QueryRow(`SELECT COUNT(*),MAX(deployment_config_repo_url),MAX(deployment_config_path)
		FROM apps WHERE app_name=?`, appName).Scan(&count, &repoURL, &configPath); err != nil {
		t.Fatalf("read preserved app after migration 029: %v", err)
	}
	if count != 1 || repoURL != "" || configPath != "" {
		t.Fatalf("migration 029 did not preserve existing app with empty config-source defaults: count=%d repo=%q path=%q", count, repoURL, configPath)
	}

	const registeredRepo = "https://git.example.com/platform/deploy-config.git"
	const registeredPath = "charts/orders"
	if _, err := db.Exec(`UPDATE apps SET deployment_config_repo_url=?,deployment_config_path=? WHERE app_name=?`, registeredRepo, registeredPath, appName); err != nil {
		t.Fatalf("write migrated app configuration source: %v", err)
	}
	var persistedRepo, persistedPath string
	if err := db.QueryRow(`SELECT deployment_config_repo_url,deployment_config_path FROM apps WHERE app_name=?`, appName).Scan(&persistedRepo, &persistedPath); err != nil {
		t.Fatalf("read migrated app configuration source: %v", err)
	}
	if persistedRepo != registeredRepo || persistedPath != registeredPath {
		t.Fatalf("migration 029 fields did not persist values: repo=%q path=%q", persistedRepo, persistedPath)
	}

	deployLog, err := db.Exec(`INSERT INTO deploy_logs(cluster_id,app_name,deployment_name,container_name,namespace,version,file_name,deploy_type,status)
		VALUES(1,?,?,'api','default','1.2.3','release.zip','deploy','success')`, appName, appName)
	if err != nil {
		t.Fatalf("seed deploy log for schema 029: %v", err)
	}
	deployLogID, err := deployLog.LastInsertId()
	if err != nil {
		t.Fatalf("read deploy log ID: %v", err)
	}
	occurredAt := time.Now().UTC().Truncate(time.Microsecond)
	incident, err := db.Exec(`INSERT INTO delivery_incidents(deploy_log_id,cluster_id,app_name,summary,status,occurred_at,recorded_at,recorded_by,version)
		VALUES(?,1,?,'legacy event before migration 030','open',?,?,'e2e-operator',1)`, deployLogID, appName, occurredAt, occurredAt)
	if err != nil {
		t.Fatalf("seed legacy incident on schema 029: %v", err)
	}
	incidentID, err := incident.LastInsertId()
	if err != nil {
		t.Fatalf("read incident ID: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO delivery_incident_events(incident_id,event_key,event_type,deploy_log_id,occurred_at,recorded_at,recorded_by,detail)
		VALUES(?,'legacy-incident-event-029','incident_recorded',?,?,?,'e2e-operator','legacy incident')`, incidentID, deployLogID, occurredAt, occurredAt); err != nil {
		t.Fatalf("seed legacy incident event on schema 029: %v", err)
	}
	applyE2EMigrationFile(t, mysql.container, mysql.rootPassword, mysql.database, "030_delivery_incident_intervention.sql")

	var eventType string
	if err := db.QueryRow(`SELECT event_type FROM delivery_incident_events WHERE event_key='legacy-incident-event-029'`).Scan(&eventType); err != nil {
		t.Fatalf("read legacy event after migration 030: %v", err)
	}
	if eventType != "incident_recorded" {
		t.Fatalf("migration 030 changed legacy event type to %q", eventType)
	}
	var interventionStartedAt, interventionRecordedAt sql.NullTime
	var interventionRecordedBy sql.NullString
	if err := db.QueryRow(`SELECT intervention_started_at,intervention_recorded_at,intervention_recorded_by
		FROM delivery_incidents WHERE id=?`, incidentID).Scan(&interventionStartedAt, &interventionRecordedAt, &interventionRecordedBy); err != nil {
		t.Fatalf("read new nullable fields after migration 030: %v", err)
	}
	if interventionStartedAt.Valid || interventionRecordedAt.Valid || interventionRecordedBy.Valid {
		t.Fatalf("migration 030 should leave prior intervention fields unknown: started=%v recorded=%v by=%v", interventionStartedAt, interventionRecordedAt, interventionRecordedBy)
	}

	interventionAt := occurredAt.Add(time.Minute)
	if _, err := db.Exec(`UPDATE delivery_incidents SET intervention_started_at=?,intervention_recorded_at=?,intervention_recorded_by=? WHERE id=?`, interventionAt, interventionAt, "e2e-operator", incidentID); err != nil {
		t.Fatalf("write intervention fields after migration 030: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO delivery_incident_events(incident_id,event_key,event_type,deploy_log_id,occurred_at,recorded_at,recorded_by,detail)
		VALUES(?,'intervention-event-030','intervention_started',?,?,?,'e2e-operator','restart rollout')`, incidentID, deployLogID, interventionAt, interventionAt); err != nil {
		t.Fatalf("write intervention event after migration 030: %v", err)
	}
	var interventionType string
	if err := db.QueryRow(`SELECT event_type FROM delivery_incident_events WHERE event_key='intervention-event-030'`).Scan(&interventionType); err != nil {
		t.Fatalf("read intervention event after migration 030: %v", err)
	}
	if interventionType != "intervention_started" {
		t.Fatalf("migration 030 did not persist intervention event type: got %q", interventionType)
	}
}

func TestLegacyEnvironmentNameMigrationRepairsOnlyGeneratedMojibake(t *testing.T) {
	if os.Getenv("KB_E2E_MYSQL_MIGRATION") != "1" {
		t.Skip("KB_E2E_MYSQL_MIGRATION != 1 — skipping disposable MySQL migration upgrade test")
	}

	mysql := startMigrationE2EMySQL(t)
	applyE2ESchemaThroughMigration(t, mysql.container, mysql.rootPassword, mysql.database, "009")
	for _, row := range []struct {
		id          int
		name        string
		environment string
	}{
		{1, "本地 Kind kbdev", "local"},
		{2, "已整理集群", "prod"},
		{3, "待整理集群", "test"},
	} {
		if _, err := mysql.db.Exec(`INSERT INTO clusters(id,name,env_code,environment,environment_tier,deploy_policy,connection_type,k8s_master_host,k8s_master_port,default_namespace,enabled)
			VALUES(?,?,?,?,'non_production','manual','legacy_ssh','127.0.0.1',22,'default',1)`, row.id, row.name, fmt.Sprintf("legacy-%d", row.id), row.environment); err != nil {
			t.Fatalf("seed legacy cluster %d: %v", row.id, err)
		}
	}
	// A UTF-8 SQL file must backfill the correct name even if the mysql client
	// runs with latin1 as its default connection character set.
	source, err := os.ReadFile(filepath.Join(backendRoot(t), "migrations", "010_delivery_model.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sqlText := strings.ReplaceAll(string(source), "USE deploy_platform;", "USE "+mysql.database+";")
	apply := exec.Command("docker", "exec", "-i", mysql.container, "mysql", "--default-character-set=latin1", "-uroot", "-p"+mysql.rootPassword)
	apply.Stdin = strings.NewReader(sqlText)
	if out, err := apply.CombinedOutput(); err != nil {
		t.Fatalf("apply migration 010 with latin1 client: %v\n%s", err, out)
	}
	var freshName string
	if err := mysql.db.QueryRow(`SELECT name FROM delivery_environments WHERE legacy_cluster_id=1`).Scan(&freshName); err != nil || freshName != "待确认 · local / 本地 Kind kbdev" {
		t.Fatalf("migration 010 backfilled an incorrectly encoded name: %q err=%v", freshName, err)
	}
	if _, err := mysql.db.Exec(`UPDATE delivery_environments SET name=? WHERE legacy_cluster_id=1`, "å¾…ç¡®è®¤ Â· local / 本地 Kind kbdev"); err != nil {
		t.Fatalf("seed old mojibake name: %v", err)
	}
	if _, err := mysql.db.Exec(`UPDATE delivery_environments SET name='生产自定义',requires_review=0 WHERE legacy_cluster_id=2`); err != nil {
		t.Fatalf("seed reviewed custom name: %v", err)
	}
	if _, err := mysql.db.Exec(`UPDATE delivery_environments SET name='业务自定义' WHERE legacy_cluster_id=3`); err != nil {
		t.Fatalf("seed unreviewed custom name: %v", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		applyE2EMigrationFile(t, mysql.container, mysql.rootPassword, mysql.database, "038_repair_legacy_environment_name.sql")
		rows, err := mysql.db.Query(`SELECT legacy_cluster_id,name FROM delivery_environments ORDER BY legacy_cluster_id`)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]string, 0, 3)
		for rows.Next() {
			var id int
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			got = append(got, fmt.Sprintf("%d:%s", id, name))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		want := []string{"1:待确认 · local / 本地 Kind kbdev", "2:生产自定义", "3:业务自定义"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("migration 038 changed the wrong legacy names on attempt %d: got=%q want=%q", attempt+1, got, want)
		}
	}
}

func TestMigrations010Through038UpgradeExistingRunnerDatabase(t *testing.T) {
	if os.Getenv("KB_E2E_MYSQL_MIGRATION") != "1" {
		t.Skip("KB_E2E_MYSQL_MIGRATION != 1 — skipping disposable MySQL migration upgrade test")
	}

	mysql := startMigrationE2EMySQL(t)
	applyE2ESchemaThroughMigration(t, mysql.container, mysql.rootPassword, mysql.database, "009")
	db := mysql.db

	if _, err := db.Exec(`INSERT INTO clusters(id,name,env_code,environment,environment_tier,deploy_policy,connection_type,k8s_master_host,k8s_master_port,default_namespace,enabled)
		VALUES(1,'legacy test','legacy-test','test','non_production','manual','legacy_ssh','127.0.0.1',22,'default',1)`); err != nil {
		t.Fatalf("seed legacy cluster on schema 009: %v", err)
	}
	const appName = "migration-009-preserve-runner"
	app, err := db.Exec(`INSERT INTO apps(cluster_id,app_name,match_keyword,deployment_name,container_name,namespace,harbor_project,source_repo_url,source_path,image_repository)
		VALUES(1,?,?,'orders-api','api','default','library','https://git.example.com/orders.git','services/orders','registry.example.com/team/orders')`, appName, appName)
	if err != nil {
		t.Fatalf("seed legacy app on schema 009: %v", err)
	}
	appID, err := app.LastInsertId()
	if err != nil {
		t.Fatalf("read legacy app ID: %v", err)
	}
	deployLog, err := db.Exec(`INSERT INTO deploy_logs(cluster_id,app_name,deployment_name,container_name,namespace,version,file_name,image_full,deploy_type,status)
		VALUES(1,?,'orders-api','api','default','1.2.3','orders.zip','registry.example.com/team/orders:1.2.3','deploy','success')`, appName)
	if err != nil {
		t.Fatalf("seed legacy deploy log on schema 009: %v", err)
	}
	deployLogID, err := deployLog.LastInsertId()
	if err != nil {
		t.Fatalf("read legacy deploy log ID: %v", err)
	}
	const imageDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	release, err := db.Exec(`INSERT INTO image_releases(event_key,event_id,source,pipeline_id,pipeline_name,build_number,branch,commit_id,app_name,image_full,image_digest,version,test_cluster_id,test_deploy_log_id)
		VALUES(REPEAT('a',64),'legacy-event-009','yunxiao','orders-pipeline','Orders build','build-42','main','abc123',?, 'registry.example.com/team/orders:1.2.3',?,'1.2.3',1,?)`, appName, imageDigest, deployLogID)
	if err != nil {
		t.Fatalf("seed legacy image release on schema 009: %v", err)
	}
	releaseID, err := release.LastInsertId()
	if err != nil {
		t.Fatalf("read legacy image release ID: %v", err)
	}
	if _, err := db.Exec(`UPDATE deploy_logs SET release_id=? WHERE id=?`, releaseID, deployLogID); err != nil {
		t.Fatalf("link legacy deploy log to image release: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO runner_bindings(app_id,token_hash,data_json,pipeline_backup_ciphertext)
		VALUES(?,'runner-token-hash-009',JSON_OBJECT('pipeline_id','orders-pipeline'),'encrypted-pipeline-backup')`, appID); err != nil {
		t.Fatalf("seed legacy Runner binding on schema 009: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO yunxiao_connections(id,token_ciphertext,data_json)
		VALUES(1,'encrypted-yunxiao-token',JSON_OBJECT('region','cn-hangzhou'))`); err != nil {
		t.Fatalf("seed legacy Yunxiao connection on schema 009: %v", err)
	}

	for _, migration := range []string{
		"010_delivery_model.sql", "011_delivery_plans.sql", "012_plan_approval_link.sql",
		"013_delivery_resource_locks.sql", "014_delivery_execution_actions.sql", "015_delivery_blue_green_entry.sql",
		"016_delivery_runs.sql", "017_delivery_resource_lock_leases.sql", "018_delivery_workload_references.sql",
		"019_delivery_policy_versions.sql", "020_delivery_drift_observations.sql", "021_deploy_environment_tier_snapshot.sql",
		"022_delivery_canary_policy.sql", "023_delivery_environment_evidence_policy.sql", "024_delivery_run_event_payloads.sql",
		"025_worker_claim_ownership.sql", "026_delivery_notification_alert_index.sql", "027_delivery_notification_due_order.sql",
		"028_delivery_incident_events.sql", "029_component_deployment_config_source.sql", "030_delivery_incident_intervention.sql",
		"031_delivery_approval_grants.sql", "032_delivery_pipeline_binding.sql", "033_persistent_user_sessions.sql",
		"034_pending_deploy_log_scan_index.sql", "035_delivery_pipeline_scope_reviews.sql", "036_delivery_incident_causality.sql",
		"037_delivery_incident_remediation.sql", "038_repair_legacy_environment_name.sql",
	} {
		applyE2EMigrationFile(t, mysql.container, mysql.rootPassword, mysql.database, migration)
	}

	var count int
	var sourceKind, sourceRepository, sourcePath, imageRepository, configRepository, configPath string
	if err := db.QueryRow(`SELECT COUNT(*),MAX(source_kind),MAX(source_repo_url),MAX(source_path),MAX(image_repository),MAX(deployment_config_repo_url),MAX(deployment_config_path)
		FROM apps WHERE id=?`, appID).Scan(&count, &sourceKind, &sourceRepository, &sourcePath, &imageRepository, &configRepository, &configPath); err != nil {
		t.Fatalf("read legacy app after migrations 010–038: %v", err)
	}
	if count != 1 || sourceKind != "repository" || sourceRepository != "https://git.example.com/orders.git" || sourcePath != "services/orders" || imageRepository != "registry.example.com/team/orders" || configRepository != "" || configPath != "" {
		t.Fatalf("legacy app was not preserved with safe migration defaults: count=%d source=%q source_repo=%q source_path=%q image_repo=%q config_repo=%q config_path=%q", count, sourceKind, sourceRepository, sourcePath, imageRepository, configRepository, configPath)
	}
	var bindingCount int
	var deploymentPipelineConnection sql.NullInt64
	var deploymentPipelineID string
	if err := db.QueryRow(`SELECT COUNT(*),MAX(deployment_pipeline_connection_id),MAX(deployment_pipeline_id)
		FROM delivery_bindings WHERE app_id=?`, appID).Scan(&bindingCount, &deploymentPipelineConnection, &deploymentPipelineID); err != nil {
		t.Fatalf("read legacy delivery binding after migration 032: %v", err)
	}
	if bindingCount != 1 || deploymentPipelineConnection.Valid || deploymentPipelineID != "" {
		t.Fatalf("migration 032 should leave existing config pipeline unbound: count=%d connection=%v pipeline=%q", bindingCount, deploymentPipelineConnection, deploymentPipelineID)
	}
	if _, err := db.Exec(`UPDATE delivery_bindings SET deployment_pipeline_connection_id=1,deployment_pipeline_id='123456' WHERE app_id=?`, appID); err != nil {
		t.Fatalf("save independent config deployment pipeline after migration 032: %v", err)
	}
	var approvalGrantTableCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='delivery_approval_grants'`).Scan(&approvalGrantTableCount); err != nil {
		t.Fatalf("check approval grant migration: %v", err)
	}
	if approvalGrantTableCount != 1 {
		t.Fatalf("migration 031 did not create approval grant storage")
	}
	var sessionTableCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='user_sessions'`).Scan(&sessionTableCount); err != nil {
		t.Fatalf("check persistent session migration: %v", err)
	}
	if sessionTableCount != 1 {
		t.Fatalf("migration 033 did not create user_sessions")
	}
	var pendingScanIndexCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='deploy_logs' AND index_name='idx_deploy_log_pending_scan'`).Scan(&pendingScanIndexCount); err != nil {
		t.Fatalf("check pending deploy scan index: %v", err)
	}
	if pendingScanIndexCount != 2 {
		t.Fatalf("migration 034 did not create the composite status/id index: rows=%d", pendingScanIndexCount)
	}
	var scopeReviewTableCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='delivery_pipeline_scope_reviews'`).Scan(&scopeReviewTableCount); err != nil {
		t.Fatalf("check pipeline scope review migration: %v", err)
	}
	if scopeReviewTableCount != 1 {
		t.Fatalf("migration 035 did not create pipeline scope review history")
	}
	var causalityColumnCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='delivery_incident_events' AND column_name='causality_status'`).Scan(&causalityColumnCount); err != nil {
		t.Fatalf("check incident causality migration: %v", err)
	}
	if causalityColumnCount != 1 {
		t.Fatalf("migration 036 did not add the optional causality assessment column")
	}
	var remediationColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='delivery_incident_events' AND column_name IN ('remediation_type','remediation_deploy_log_id','remediation_plan_status')`).Scan(&remediationColumns); err != nil {
		t.Fatalf("check incident remediation migration: %v", err)
	}
	if remediationColumns != 3 {
		t.Fatalf("migration 037 did not add all remediation classification columns: %d", remediationColumns)
	}
	var bindingID int64
	if err := db.QueryRow(`SELECT id FROM delivery_bindings WHERE app_id=?`, appID).Scan(&bindingID); err != nil {
		t.Fatalf("find migrated binding for scope review: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO delivery_pipeline_scope_reviews(delivery_binding_id,connection_id,pipeline_id,definition_hash,write_scope,reviewer,reviewed_at)
		VALUES(?,1,'123456',REPEAT('a',64),'unknown','migration-reviewer',UTC_TIMESTAMP())`, bindingID); err != nil {
		t.Fatalf("persist scope review after migration 035: %v", err)
	}
	var reviewedPipeline string
	if err := db.QueryRow(`SELECT pipeline_id FROM delivery_pipeline_scope_reviews WHERE delivery_binding_id=? ORDER BY id DESC LIMIT 1`, bindingID).Scan(&reviewedPipeline); err != nil || reviewedPipeline != "123456" {
		t.Fatalf("read scope review after migration 035: pipeline=%q err=%v", reviewedPipeline, err)
	}
	var applicationID int64
	if err := db.QueryRow(`SELECT id FROM delivery_applications WHERE legacy_app_id=?`, appID).Scan(&applicationID); err != nil {
		t.Fatalf("find migrated logical application: %v", err)
	}
	environmentResult, err := db.Exec(`INSERT INTO delivery_environments(code,name,environment_tier) VALUES('approval-scope-prod','生产环境','production')`)
	if err != nil {
		t.Fatalf("create production environment for approval scope: %v", err)
	}
	environmentID, err := environmentResult.LastInsertId()
	if err != nil {
		t.Fatalf("read approval environment ID: %v", err)
	}
	userResult, err := db.Exec(`INSERT INTO users(username,display_name,role,password_hash,enabled) VALUES('migration-approver','发布审批人','operator','unused-test-hash',1)`)
	if err != nil {
		t.Fatalf("create approval user: %v", err)
	}
	userID, err := userResult.LastInsertId()
	if err != nil {
		t.Fatalf("read approval user ID: %v", err)
	}
	repo, err := repository.Open(mysql.dsn)
	if err != nil {
		t.Fatalf("open repository after migration 032: %v", err)
	}
	defer repo.Close()
	deliveryModel, err := repo.DeliveryModel()
	if err != nil {
		t.Fatalf("read delivery model after migration 032: %v", err)
	}
	var loadedPipeline bool
	for _, binding := range deliveryModel.Bindings {
		if binding.AppID == int(appID) {
			loadedPipeline = binding.DeploymentPipeline != nil && binding.DeploymentPipeline.ConnectionID == 1 && binding.DeploymentPipeline.PipelineID == "123456"
		}
	}
	if !loadedPipeline {
		t.Fatalf("repository did not read independent deployment pipeline binding: %+v", deliveryModel.Bindings)
	}
	if err := repo.ReplaceDeliveryApprovalGrants(userID, []service.DeliveryApprovalGrant{{
		UserID: userID, ApplicationID: applicationID, EnvironmentID: environmentID, CreatedAt: time.Now().UTC(),
	}}, "migration-admin"); err != nil {
		t.Fatalf("persist approval grant: %v", err)
	}
	grants, err := repo.DeliveryApprovalGrants()
	if err != nil || len(grants) != 1 || grants[0].Username != "migration-approver" || grants[0].ApplicationName != appName || grants[0].EnvironmentName != "生产环境" {
		t.Fatalf("approval grant did not survive repository read: grants=%+v err=%v", grants, err)
	}
	var legacyBindingCount int
	var templateKind, templateName, activeName string
	if err := db.QueryRow(`SELECT COUNT(*),MAX(template_workload_kind),MAX(template_workload_name),MAX(active_workload_name)
		FROM delivery_bindings WHERE app_id=?`, appID).Scan(&legacyBindingCount, &templateKind, &templateName, &activeName); err != nil {
		t.Fatalf("read legacy workload binding after migrations 010–038: %v", err)
	}
	if legacyBindingCount != 1 || templateKind != "Deployment" || templateName != "orders-api" || activeName != "orders-api" {
		t.Fatalf("legacy workload binding did not preserve its target identity: count=%d kind=%q template=%q active=%q", legacyBindingCount, templateKind, templateName, activeName)
	}
	var releaseCount int
	var persistedDigest, pipelineName, buildNumber string
	if err := db.QueryRow(`SELECT COUNT(*),MAX(image_digest),MAX(pipeline_name),MAX(build_number)
		FROM image_releases WHERE id=? AND app_name=?`, releaseID, appName).Scan(&releaseCount, &persistedDigest, &pipelineName, &buildNumber); err != nil {
		t.Fatalf("read legacy image release after migrations 010–038: %v", err)
	}
	if releaseCount != 1 || persistedDigest != imageDigest || pipelineName != "Orders build" || buildNumber != "build-42" {
		t.Fatalf("legacy image release was not preserved: count=%d digest=%q pipeline=%q build=%q", releaseCount, persistedDigest, pipelineName, buildNumber)
	}
	var runnerCount int
	var runnerTokenHash, runnerBackup string
	var runnerData []byte
	if err := db.QueryRow(`SELECT COUNT(*),MAX(token_hash),MAX(data_json),MAX(pipeline_backup_ciphertext)
		FROM runner_bindings WHERE app_id=?`, appID).Scan(&runnerCount, &runnerTokenHash, &runnerData, &runnerBackup); err != nil {
		t.Fatalf("read legacy Runner binding after migrations 010–038: %v", err)
	}
	if runnerCount != 1 || runnerTokenHash != "runner-token-hash-009" || runnerBackup != "encrypted-pipeline-backup" || !strings.Contains(string(runnerData), "orders-pipeline") {
		t.Fatalf("legacy Runner binding changed during migration: count=%d token_hash=%q backup=%q data=%s", runnerCount, runnerTokenHash, runnerBackup, runnerData)
	}
	var connectionCount int
	var connectionCiphertext string
	if err := db.QueryRow(`SELECT COUNT(*),MAX(token_ciphertext) FROM yunxiao_connections WHERE id=1`).Scan(&connectionCount, &connectionCiphertext); err != nil {
		t.Fatalf("read legacy Yunxiao connection after migrations 010–038: %v", err)
	}
	if connectionCount != 1 || connectionCiphertext != "encrypted-yunxiao-token" {
		t.Fatalf("legacy Yunxiao connection changed during migration: count=%d token_ciphertext=%q", connectionCount, connectionCiphertext)
	}
	var logStatus string
	var linkedReleaseID uint64
	if err := db.QueryRow(`SELECT status,release_id FROM deploy_logs WHERE id=?`, deployLogID).Scan(&logStatus, &linkedReleaseID); err != nil {
		t.Fatalf("read legacy deploy log after migrations 010–038: %v", err)
	}
	if logStatus != "success" || linkedReleaseID != uint64(releaseID) {
		t.Fatalf("legacy deploy log changed during migration: status=%q release_id=%d", logStatus, linkedReleaseID)
	}
	var tierSnapshot sql.NullString
	if err := db.QueryRow(`SELECT environment_tier_snapshot FROM deploy_logs WHERE id=?`, deployLogID).Scan(&tierSnapshot); err != nil {
		t.Fatalf("read historical environment tier snapshot after migrations 010–038: %v", err)
	}
	if tierSnapshot.Valid {
		t.Fatalf("migration assigned a historical tier that was not knowable at log creation: %q", tierSnapshot.String)
	}
}

func TestMigrationUpgradeMatrixFrom018024And032(t *testing.T) {
	if os.Getenv("KB_E2E_MYSQL_MIGRATION") != "1" {
		t.Skip("KB_E2E_MYSQL_MIGRATION != 1 — skipping disposable MySQL migration upgrade test")
	}

	mysql := startMigrationE2EMySQL(t)
	for _, baseline := range []string{"018", "024", "032"} {
		t.Run("from_"+baseline, func(t *testing.T) {
			database := "matrix_" + baseline
			if _, err := mysql.db.Exec("CREATE DATABASE " + database + " CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"); err != nil {
				t.Fatalf("create isolated migration matrix database: %v", err)
			}
			applyE2ESchemaThroughMigration(t, mysql.container, mysql.rootPassword, database, baseline)
			applyE2EMigrationsAfter(t, mysql.container, mysql.rootPassword, database, baseline)

			dsn := fmt.Sprintf("root:%s@tcp(%s)/%s?parseTime=true&charset=utf8mb4", mysql.rootPassword, mysql.address, database)
			db, err := sql.Open("mysql", dsn)
			if err != nil {
				t.Fatalf("open upgraded migration matrix database: %v", err)
			}
			defer db.Close()

			checks := []struct {
				name  string
				query string
			}{
				{"delivery_pipeline_scope_reviews table", `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='delivery_pipeline_scope_reviews'`},
				{"user_sessions table", `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='user_sessions'`},
				{"deployment pipeline binding column", `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='delivery_bindings' AND column_name='deployment_pipeline_connection_id'`},
				{"pending deploy scan index", `SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='deploy_logs' AND index_name='idx_deploy_log_pending_scan'`},
				{"incident causality status column", `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='delivery_incident_events' AND column_name='causality_status'`},
				{"incident remediation type column", `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='delivery_incident_events' AND column_name='remediation_type'`},
			}
			for _, check := range checks {
				var count int
				if err := db.QueryRow(check.query).Scan(&count); err != nil {
					t.Fatalf("check %s after upgrade from %s: %v", check.name, baseline, err)
				}
				if count == 0 {
					t.Errorf("%s is missing after upgrade from schema %s", check.name, baseline)
				}
			}
		})
	}
}

type migrationE2EMySQL struct {
	container    string
	database     string
	rootPassword string
	address      string
	dsn          string
	db           *sql.DB
}

func startMigrationE2EMySQL(t *testing.T) migrationE2EMySQL {
	t.Helper()
	if out, err := exec.Command("docker", "image", "inspect", "mysql:8.4").CombinedOutput(); err != nil {
		t.Skipf("mysql:8.4 image is unavailable locally: %v\n%s", err, out)
	}

	runID := e2eRunID(t)
	container := "deploy-platform-e2e-migration-" + runID
	database := "e2e_migration_" + runID
	rootPassword := "e2e-root-" + runID
	portListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve isolated MySQL port: %v", err)
	}
	hostPort := portListener.Addr().(*net.TCPAddr).Port
	if err := portListener.Close(); err != nil {
		t.Fatalf("release isolated MySQL port: %v", err)
	}
	t.Cleanup(func() {
		out, err := exec.Command("docker", "rm", "-f", container).CombinedOutput()
		if err != nil && !strings.Contains(string(out), "No such container") {
			t.Errorf("remove isolated MySQL container %s: %v\n%s", container, err, out)
		}
	})

	create := exec.Command("docker", "run", "-d", "--name", container,
		"--tmpfs", "/var/lib/mysql:rw,size=1g",
		"-e", "MYSQL_ROOT_PASSWORD="+rootPassword,
		"-e", "MYSQL_DATABASE="+database,
		"-p", fmt.Sprintf("127.0.0.1:%d:3306", hostPort),
		"mysql:8.4")
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("start isolated MySQL container: %v\n%s", err, out)
	}
	waitForE2EMySQL(t, container, rootPassword, database)
	address := fmt.Sprintf("127.0.0.1:%d", hostPort)
	dsn := fmt.Sprintf("root:%s@tcp(%s)/%s?parseTime=true&charset=utf8mb4", rootPassword, address, database)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open isolated schema connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return migrationE2EMySQL{container: container, database: database, rootPassword: rootPassword, address: address, dsn: dsn, db: db}
}

func applyE2ESchemaThroughMigration(t *testing.T, container, rootPassword, database, through string) {
	t.Helper()
	root := backendRoot(t)
	applyE2EMigrationFile(t, container, rootPassword, database, "schema.sql")
	migrations, err := filepath.Glob(filepath.Join(root, "migrations", "*.sql"))
	if err != nil {
		t.Fatalf("list SQL migrations: %v", err)
	}
	found := false
	for _, migrationPath := range migrations {
		name := filepath.Base(migrationPath)
		if !strings.HasPrefix(name, through+"_") {
			if name > through+"_" {
				break
			}
			applyE2EMigrationFile(t, container, rootPassword, database, name)
			continue
		}
		applyE2EMigrationFile(t, container, rootPassword, database, name)
		found = true
		break
	}
	if !found {
		t.Fatalf("migration prefix %s was not found", through)
	}
}

func applyE2EMigrationsAfter(t *testing.T, container, rootPassword, database, after string) {
	t.Helper()
	migrations, err := filepath.Glob(filepath.Join(backendRoot(t), "migrations", "*.sql"))
	if err != nil {
		t.Fatalf("list SQL migrations: %v", err)
	}
	found := false
	for _, migrationPath := range migrations {
		name := filepath.Base(migrationPath)
		if strings.HasPrefix(name, after+"_") {
			found = true
			continue
		}
		if !found {
			continue
		}
		applyE2EMigrationFile(t, container, rootPassword, database, name)
	}
	if !found {
		t.Fatalf("migration prefix %s was not found", after)
	}
}

func applyE2EMigrationFile(t *testing.T, container, rootPassword, database, name string) {
	t.Helper()
	path := filepath.Join(backendRoot(t), "migrations", name)
	if name == "schema.sql" {
		path = filepath.Join(backendRoot(t), name)
	}
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read SQL file %s: %v", path, err)
	}
	sqlText := strings.ReplaceAll(string(source), "USE deploy_platform;", "USE "+database+";")
	apply := exec.Command("docker", "exec", "-i", container, "mysql", "-uroot", "-p"+rootPassword)
	apply.Stdin = strings.NewReader(sqlText)
	if out, err := apply.CombinedOutput(); err != nil {
		t.Fatalf("apply %s to isolated MySQL: %v\n%s", name, err, out)
	}
}
