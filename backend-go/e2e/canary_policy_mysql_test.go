package e2e

import (
	"database/sql"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/repository"
	"deploy-platform/backend-go/internal/service"
	_ "github.com/go-sql-driver/mysql"
)

func TestCanaryPolicyMySQLRoundTrip(t *testing.T) {
	if os.Getenv("KB_E2E_MYSQL_CANARY") != "1" {
		t.Skip("KB_E2E_MYSQL_CANARY != 1 — skipping isolated MySQL canary policy round-trip test")
	}
	mysql := startMigrationE2EMySQL(t)
	applyE2ESchemaThroughMigration(t, mysql.container, mysql.rootPassword, mysql.database, "032")

	setupDB, err := sql.Open("mysql", mysql.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer setupDB.Close()

	clusterResult, err := setupDB.Exec(`INSERT INTO clusters(name,env_code,k8s_master_host,k8s_master_port)
		VALUES('Canary policy E2E','canary-e2e','127.0.0.1',22)`)
	if err != nil {
		t.Fatalf("create test cluster: %v", err)
	}
	clusterID, err := clusterResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	appResult, err := setupDB.Exec(`INSERT INTO apps(cluster_id,app_name,match_keyword,deployment_name,container_name,namespace)
		VALUES(?,?,?,?,?,?)`, clusterID, "canary-policy-e2e", "canary-policy-e2e", "canary-policy-e2e", "api", "default")
	if err != nil {
		t.Fatalf("create test app: %v", err)
	}
	appID, err := appResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	applicationResult, err := setupDB.Exec(`INSERT INTO delivery_applications(name,requires_review) VALUES(?,0)`, "canary-policy-e2e")
	if err != nil {
		t.Fatalf("create logical application: %v", err)
	}
	applicationID, err := applicationResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	componentResult, err := setupDB.Exec(`INSERT INTO delivery_components(application_id,name,requires_review) VALUES(?,?,0)`, applicationID, "api")
	if err != nil {
		t.Fatalf("create logical component: %v", err)
	}
	componentID, err := componentResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	environmentResult, err := setupDB.Exec(`INSERT INTO delivery_environments(code,name,environment_tier,requires_review)
		VALUES('canary-e2e','Canary E2E','non_production',0)`)
	if err != nil {
		t.Fatalf("create logical environment: %v", err)
	}
	environmentID, err := environmentResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	bindingResult, err := setupDB.Exec(`INSERT INTO delivery_bindings(app_id,application_id,component_id,environment_id,strategy_draft,requires_review)
		VALUES(?,?,?,?, 'gateway_canary',0)`, appID, applicationID, componentID, environmentID)
	if err != nil {
		t.Fatalf("create delivery binding: %v", err)
	}
	bindingID, err := bindingResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	maxCandidateErrorRate := 0.02
	maxErrorRateIncrease := 0.01
	maxCandidateP95 := 250.0
	maxP95Increase := 0.2
	analysis := &service.CanaryAnalysisPolicy{
		GatewayRoute: &service.CanaryGatewayRouteRef{
			ConnectionID: 7, RouteID: "orders-public", StableBackendID: "orders-stable", CandidateBackendID: "orders-candidate",
		},
		MinRequests: 1000, MaxObservationAgeSeconds: 120,
		MaxCandidateErrorRate: &maxCandidateErrorRate, MaxErrorRateIncrease: &maxErrorRateIncrease,
		MaxCandidateP95LatencyMillis: &maxCandidateP95, MaxP95LatencyIncreaseRatio: &maxP95Increase,
		Stages: []service.CanaryStage{
			{CandidateWeightPercent: 10, ObservationDurationSeconds: 60},
			{CandidateWeightPercent: 50, ObservationDurationSeconds: 120},
			{CandidateWeightPercent: 100, ObservationDurationSeconds: 300},
		},
	}
	repo, err := repository.Open(mysql.dsn)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	defer repo.Close()

	createdAt := time.Now().UTC().Truncate(time.Second)
	savedPolicy, created, err := repo.SaveDeliveryPolicyVersion(0, service.DeliveryPolicyVersion{
		BindingID: bindingID, Strategy: "gateway_canary", CanaryAnalysis: analysis,
		Readiness: "integration_pending", CreatedBy: "e2e", CreatedAt: createdAt,
	})
	if err != nil || !created || savedPolicy.Version != 1 {
		t.Fatalf("save canary policy: policy=%+v created=%t err=%v", savedPolicy, created, err)
	}
	history, err := repo.DeliveryPolicyVersions()
	if err != nil || len(history) != 1 || history[0].CanaryAnalysis == nil || !reflect.DeepEqual(*history[0].CanaryAnalysis, *analysis) {
		t.Fatalf("read canary policy JSON: history=%+v err=%v", history, err)
	}

	eventKey := strings.Repeat("a", 64)
	releaseResult, err := setupDB.Exec(`INSERT INTO image_releases(event_key,event_id,app_name,image_full,version,test_cluster_id,test_deploy_log_id)
		VALUES(?,?,?,?,?,?,?)`, eventKey, "canary-policy-e2e-build", "canary-policy-e2e", "registry.example/api@sha256:"+strings.Repeat("b", 64), "1.2.3", clusterID, 1)
	if err != nil {
		t.Fatalf("create release for plan snapshot: %v", err)
	}
	releaseID, err := releaseResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	plan := service.DeliveryPlan{
		ReleaseID: releaseID, TargetClusterID: int(clusterID), TargetAppID: int(appID),
		Strategy: "gateway_canary", Status: "preview_only", SnapshotHash: strings.Repeat("c", 64),
		Snapshot: service.DeliveryPlanSnapshot{
			SchemaVersion: 1, Strategy: "gateway_canary",
			Policy: service.DeliveryPlanPolicySnapshot{
				BindingID: bindingID, Version: savedPolicy.Version, Source: "strategy_version",
				Strategy: savedPolicy.Strategy, CanaryAnalysis: savedPolicy.CanaryAnalysis,
				Readiness: "integration_pending",
			},
		},
		Blockers: []string{"gateway integration pending"}, RequestedBy: "e2e", CreatedAt: createdAt,
	}
	savedPlan, created, err := repo.SaveDeliveryPlan(plan)
	if err != nil || !created {
		t.Fatalf("save canary plan snapshot: plan=%+v created=%t err=%v", savedPlan, created, err)
	}
	loadedPlan, err := repo.FindDeliveryPlan(savedPlan.ID)
	if err != nil || loadedPlan.Snapshot.Policy.CanaryAnalysis == nil || !reflect.DeepEqual(*loadedPlan.Snapshot.Policy.CanaryAnalysis, *analysis) {
		t.Fatalf("read canary plan snapshot JSON: plan=%+v err=%v", loadedPlan, err)
	}
}
