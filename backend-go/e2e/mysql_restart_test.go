package e2e

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/config"
	"deploy-platform/backend-go/internal/httpapi"
	"deploy-platform/backend-go/internal/repository"
	"deploy-platform/backend-go/internal/service"
	_ "github.com/go-sql-driver/mysql"
)

// TestMySQLInstanceRestartPersistsDeliveryData exercises a fresh installation
// through all migrations, including component configuration sources,
// production incident events, and Worker claim fencing, followed by a real
// MySQL process restart against a disposable container. It never touches the
// shared DB.
func TestMySQLInstanceRestartPersistsDeliveryData(t *testing.T) {
	if os.Getenv("KB_E2E_MYSQL_RESTART") != "1" {
		t.Skip("KB_E2E_MYSQL_RESTART != 1 — skipping disposable MySQL restart drill")
	}
	if out, err := exec.Command("docker", "image", "inspect", "mysql:8.4").CombinedOutput(); err != nil {
		t.Skipf("mysql:8.4 image is unavailable locally: %v\n%s", err, out)
	}

	runID := e2eRunID(t)
	container := "deploy-platform-e2e-mysql-" + runID
	database := "e2e_delivery_" + runID
	rootPassword := "e2e-root-" + runID
	mysqlDataDir, err := os.MkdirTemp(backendRoot(t), ".e2e-mysql-data-"+runID+"-")
	if err != nil {
		t.Fatalf("create isolated MySQL data directory: %v", err)
	}
	if err := os.Chmod(mysqlDataDir, 0o777); err != nil {
		t.Fatalf("make isolated MySQL data directory writable: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(mysqlDataDir); err != nil {
			t.Errorf("remove isolated MySQL data directory: %v", err)
		}
	})
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
		"--mount", "type=bind,source="+mysqlDataDir+",target=/var/lib/mysql",
		"-e", "MYSQL_ROOT_PASSWORD="+rootPassword,
		"-e", "MYSQL_DATABASE="+database,
		"-p", fmt.Sprintf("127.0.0.1:%d:3306", hostPort),
		"mysql:8.4")
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("start isolated MySQL container: %v\n%s", err, out)
	}
	waitForE2EMySQL(t, container, rootPassword, database)

	dsn := fmt.Sprintf("root:%s@tcp(127.0.0.1:%d)/%s?parseTime=true&charset=utf8mb4", rootPassword, hostPort, database)
	applyFullSchemaToE2EMySQL(t, container, rootPassword, database)

	repo, err := repository.Open(dsn)
	if err != nil {
		t.Fatalf("open repository against isolated MySQL: %v", err)
	}
	t.Cleanup(func() {
		if repo != nil {
			_ = repo.Close()
		}
	})

	sqlDB, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open isolated schema connection: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := repo.CheckSessionSchema(); err != nil {
		t.Fatalf("validate persistent login session schema: %v", err)
	}

	probeApp, err := httpapi.NewApplication(config.Config{
		Env:                  "production",
		DatabaseDSN:          dsn,
		TransferDir:          t.TempDir(),
		AdminInitialPassword: "e2e-health-probe-password",
	})
	if err != nil {
		t.Fatalf("start isolated application for real MySQL health probes: %v", err)
	}
	probeServer := httptest.NewServer(probeApp.Engine)
	probeClient := &http.Client{Timeout: 5 * time.Second}
	probeClosed := false
	t.Cleanup(func() {
		probeServer.Close()
		if !probeClosed {
			if err := probeApp.Close(); err != nil {
				t.Errorf("close isolated health probe application: %v", err)
			}
		}
	})
	probeStatus := func(path string) int {
		t.Helper()
		response, err := probeClient.Get(probeServer.URL + path)
		if err != nil {
			t.Fatalf("request health probe %s: %v", path, err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	if status := probeStatus("/health/live"); status != http.StatusOK {
		t.Fatalf("live probe before MySQL outage = %d, want 200", status)
	}
	if status := probeStatus("/health/ready"); status != http.StatusOK {
		t.Fatalf("ready probe before MySQL outage = %d, want 200", status)
	}
	if out, err := exec.Command("docker", "stop", "--time", "1", container).CombinedOutput(); err != nil {
		t.Fatalf("stop isolated MySQL for health probe: %v\n%s", err, out)
	}
	if status := probeStatus("/health/live"); status != http.StatusOK {
		t.Fatalf("live probe during MySQL outage = %d, want 200", status)
	}
	if status := probeStatus("/health/ready"); status != http.StatusServiceUnavailable {
		t.Fatalf("ready probe during MySQL outage = %d, want 503", status)
	}
	if out, err := exec.Command("docker", "start", container).CombinedOutput(); err != nil {
		t.Fatalf("restart isolated MySQL after health probe: %v\n%s", err, out)
	}
	waitForE2EMySQL(t, container, rootPassword, database)
	probeDeadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(probeDeadline) {
		if status := probeStatus("/health/ready"); status == http.StatusOK {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if status := probeStatus("/health/ready"); status != http.StatusOK {
		t.Fatalf("ready probe did not recover after MySQL restart; final status=%d", status)
	}
	if err := probeApp.Close(); err != nil {
		t.Fatalf("close isolated health probe application: %v", err)
	}
	probeClosed = true
	probeServer.Close()

	sessionUser, err := repo.SaveUser(service.User{
		Username: "session-e2e-" + runID, DisplayName: "会话重启验证", Role: service.RoleOperator,
		PasswordHash: "unused-by-session-test", Enabled: true,
	})
	if err != nil {
		t.Fatalf("save session test user: %v", err)
	}
	sessionStore := service.NewStoreWithData(nil, nil, nil, nil, repo)
	sessionStore.LoadUsers([]service.User{sessionUser})
	sessionToken, err := sessionStore.CreateSession(sessionUser.ID)
	if err != nil {
		t.Fatalf("create durable session before MySQL restart: %v", err)
	}
	sessionTokenHash := sha256.Sum256([]byte(sessionToken))
	var storedSessionHash string
	if err := sqlDB.QueryRow(`SELECT token_hash FROM user_sessions WHERE user_id=?`, sessionUser.ID).Scan(&storedSessionHash); err != nil {
		t.Fatalf("read persisted session token hash: %v", err)
	}
	if storedSessionHash != hex.EncodeToString(sessionTokenHash[:]) || storedSessionHash == sessionToken {
		t.Fatalf("session must persist only token hash: hash=%q", storedSessionHash)
	}
	assertIdentityStateSharedAcrossStores(t, repo, runID)

	canary := seedCanaryRun(t, sqlDB, repo, runID)
	assertWorkerClaimOwnershipAcrossStores(t, sqlDB, repo, canary.log.ClusterID, runID)
	assertQueuedCanaryCancellationSerializesWithWorkerClaim(t, sqlDB, repo, canary, runID)
	productionTier := "production"
	incidentLog := canary.log
	incidentLog.EnvironmentTierSnapshot = &productionTier
	remediationLog := incidentLog
	remediationLog.ID = 0
	remediationLog.DeployType = "deploy"
	remediationLog.Status = "success"
	remediationLog.RollbackTargetID = 0
	remediationLog.CreatedAt = time.Time{}
	remediationLog.StartedAt = nil
	remediationLog.FinishedAt = nil
	remediationLog, err = repo.SaveLog(remediationLog)
	if err != nil {
		t.Fatalf("seed same-target production remediation deployment: %v", err)
	}
	incidentStore := service.NewStoreWithData(nil, nil, []service.DeployLog{incidentLog, remediationLog}, nil, repo)
	incidentOccurredAt := time.Now().UTC().Truncate(time.Microsecond)
	incident, err := incidentStore.CreateDeliveryIncident(
		canary.deployLogID, incidentOccurredAt, "E2E 线上故障事件", "e2e-operator", "incident-e2e-"+runID,
	)
	if err != nil || incident.Status != "open" || len(incident.Events) != 1 {
		t.Fatalf("persist production incident before MySQL restart: incident=%+v err=%v", incident, err)
	}
	interventionAt := incidentOccurredAt.Add(time.Second)
	incident, changed, err := incidentStore.StartDeliveryIncidentIntervention(
		incident.ID, incident.Version, interventionAt, "E2E 回退上一稳定版本", "e2e-operator", "intervention-e2e-"+runID,
	)
	if err != nil || !changed || incident.Version != 2 || len(incident.Events) != 2 {
		t.Fatalf("persist intervention start before MySQL restart: incident=%+v changed=%t err=%v", incident, changed, err)
	}
	incident, changed, err = incidentStore.AssessDeliveryIncidentCausality(
		incident.ID, incident.Version, service.DeliveryIncidentCausedByDeployment,
		"E2E 回退该部署后故障停止", "e2e-operator", "causality-e2e-"+runID,
	)
	if err != nil || !changed || incident.Version != 3 || incident.CausalityStatus != service.DeliveryIncidentCausedByDeployment || len(incident.Events) != 3 {
		t.Fatalf("persist causality assessment before MySQL restart: incident=%+v changed=%t err=%v", incident, changed, err)
	}
	incident, changed, err = incidentStore.RecordDeliveryIncidentRemediation(
		incident.ID, incident.Version, service.DeliveryIncidentRemediationHotfix,
		service.DeliveryIncidentRemediationPlanned, remediationLog.ID, "E2E 热修复部署后探针恢复",
		"e2e-operator", "remediation-e2e-"+runID,
	)
	if err != nil || !changed || incident.Version != 4 || incident.RemediationType != service.DeliveryIncidentRemediationHotfix || incident.RemediationDeployLogID == nil || *incident.RemediationDeployLogID != remediationLog.ID || len(incident.Events) != 4 {
		t.Fatalf("persist remediation classification before MySQL restart: incident=%+v changed=%t err=%v", incident, changed, err)
	}
	incidentReplayStore := service.NewStoreWithData(nil, nil, []service.DeployLog{incidentLog, remediationLog}, nil, repo)
	replayedIncident, err := incidentReplayStore.CreateDeliveryIncident(
		canary.deployLogID, incidentOccurredAt, "E2E 线上故障事件", "e2e-operator", "incident-e2e-"+runID,
	)
	if err != nil || replayedIncident.ID != incident.ID || replayedIncident.Version != 4 || len(replayedIncident.Events) != 4 || replayedIncident.CausalityStatus != service.DeliveryIncidentCausedByDeployment {
		t.Fatalf("incident idempotency replay created a duplicate: incident=%+v err=%v", replayedIncident, err)
	}
	replayedIncident, changed, err = incidentReplayStore.StartDeliveryIncidentIntervention(
		incident.ID, 1, interventionAt, "E2E 回退上一稳定版本", "e2e-operator", "intervention-e2e-"+runID,
	)
	if err != nil || changed || replayedIncident.Version != 4 || len(replayedIncident.Events) != 4 {
		t.Fatalf("intervention idempotency replay created a duplicate: incident=%+v changed=%t err=%v", replayedIncident, changed, err)
	}
	replayedIncident, changed, err = incidentReplayStore.AssessDeliveryIncidentCausality(
		incident.ID, 1, service.DeliveryIncidentCausedByDeployment,
		"E2E 回退该部署后故障停止", "e2e-operator", "causality-e2e-"+runID,
	)
	if err != nil || changed || replayedIncident.Version != 4 || replayedIncident.CausalityStatus != service.DeliveryIncidentCausedByDeployment {
		t.Fatalf("causality idempotency replay created a duplicate: incident=%+v changed=%t err=%v", replayedIncident, changed, err)
	}
	replayedIncident, changed, err = incidentReplayStore.RecordDeliveryIncidentRemediation(
		incident.ID, 1, service.DeliveryIncidentRemediationHotfix,
		service.DeliveryIncidentRemediationPlanned, remediationLog.ID, "E2E 热修复部署后探针恢复",
		"e2e-operator", "remediation-e2e-"+runID,
	)
	if err != nil || changed || replayedIncident.Version != 4 || len(replayedIncident.Events) != 4 || replayedIncident.RemediationType != service.DeliveryIncidentRemediationHotfix || replayedIncident.RemediationDeployLogID == nil || *replayedIncident.RemediationDeployLogID != remediationLog.ID {
		t.Fatalf("remediation idempotency replay created a duplicate: incident=%+v changed=%t err=%v", replayedIncident, changed, err)
	}
	beforePause, err := canary.store.DeliveryRun(canary.runID)
	if err != nil {
		t.Fatalf("read seeded canary run before pause: %v", err)
	}
	paused, err := service.NewCDService(config.Config{}, canary.store, nil).ActOnDeliveryRun(
		context.Background(), canary.runID, service.DeliveryActionPause, beforePause.Version,
		"mysql-restart-canary-pause-"+runID, "e2e-operator",
	)
	if err != nil || paused.Run.Status != "paused" || paused.Run.Version != beforePause.Version+1 {
		t.Fatalf("pause canary before MySQL restart: run=%+v err=%v", paused.Run, err)
	}
	assertCanaryCheckpointPersists(t, repo, canary, "paused", paused.Run.Version, 0, service.CanaryStageAwaitingConfirm)

	result, err := sqlDB.Exec(`INSERT INTO delivery_environments(code,name,environment_tier,requires_review) VALUES('e2e-prod','E2E Production','production',0)`)
	if err != nil {
		t.Fatalf("insert production environment: %v", err)
	}
	environmentID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read production environment id: %v", err)
	}
	firstPolicy := service.DeliveryEnvironmentEvidencePolicyVersion{
		EnvironmentID: environmentID, RequiredVerificationKinds: []string{"ci"},
		MaxEvidenceAgeMinutes: 120, TrustedSubmitters: map[string][]string{"ci": {"apikey:yunxiao-prod"}},
		CreatedBy: "e2e-admin", CreatedAt: time.Now().UTC(),
	}
	firstPolicy, created, err := repo.SaveDeliveryEnvironmentEvidencePolicyVersion(0, firstPolicy)
	if err != nil || !created || firstPolicy.Version != 1 {
		t.Fatalf("persist first production evidence policy: policy=%+v created=%t err=%v", firstPolicy, created, err)
	}
	secondPolicy := service.DeliveryEnvironmentEvidencePolicyVersion{
		EnvironmentID: environmentID, RequiredVerificationKinds: []string{"ci", "smoke"},
		MaxEvidenceAgeMinutes: 60, TrustedSubmitters: map[string][]string{
			"ci": {"apikey:yunxiao-prod"}, "smoke": {"apikey:smoke-prod"},
		},
		CreatedBy: "e2e-admin", CreatedAt: time.Now().UTC(),
	}
	secondPolicy, created, err = repo.SaveDeliveryEnvironmentEvidencePolicyVersion(1, secondPolicy)
	if err != nil || !created || secondPolicy.Version != 2 {
		t.Fatalf("append production evidence policy: policy=%+v created=%t err=%v", secondPolicy, created, err)
	}
	versions, err := repo.DeliveryEnvironmentEvidencePolicyVersions()
	if err != nil || len(versions) != 2 || versions[1].Version != 2 || len(versions[1].RequiredVerificationKinds) != 2 {
		t.Fatalf("read appended evidence policy versions: %+v err=%v", versions, err)
	}

	observedAt := time.Now().UTC().Truncate(time.Second)
	expected := "registry.example.com/orders/api@sha256:" + strings.Repeat("a", 64)
	actual := "registry.example.com/orders/api@sha256:" + strings.Repeat("b", 64)
	states, err := repo.ObserveDeliveryDrift([]service.DeliveryDriftObservation{{
		AppID: 1, AppName: "orders-api", ClusterID: 1, Namespace: "orders-test",
		Workload: "orders-api", Container: "api", Status: "drifted",
		ExpectedImage: &expected, ActualImage: &actual, ObservedBy: "e2e",
		ObservedAt: observedAt, Source: service.DriftObservationSource,
	}})
	if err != nil || len(states) != 1 || states[0].Status != "drifted" {
		t.Fatalf("persist initial drift state: states=%+v err=%v", states, err)
	}
	if _, err := repo.DeliveryDriftHistory(service.DeliveryDriftHistoryFilter{
		ClusterID: 1, AppIDs: []int{1}, Limit: 10,
	}); err != nil {
		t.Fatalf("warm history connection before MySQL restart: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close test SQL connection before simulated process restart: %v", err)
	}

	if out, err := exec.Command("docker", "stop", "--time", "1", container).CombinedOutput(); err != nil {
		t.Fatalf("stop isolated MySQL instance: %v\n%s", err, out)
	}
	if _, err := repo.DeliveryDriftStates(); err == nil {
		t.Fatal("repository query unexpectedly succeeded while its MySQL instance was stopped")
	}
	if out, err := exec.Command("docker", "start", container).CombinedOutput(); err != nil {
		t.Fatalf("restart isolated MySQL instance: %v\n%s", err, out)
	}
	waitForE2EMySQL(t, container, rootPassword, database)
	samePoolDeadline := time.Now().Add(30 * time.Second)
	recoveredOnSamePool := false
	var lastPoolErr error
	for time.Now().Before(samePoolDeadline) {
		states, queryErr := repo.DeliveryDriftStates()
		if queryErr == nil && len(states) == 1 && states[0].Status == "drifted" &&
			states[0].ActualImage != nil && *states[0].ActualImage == actual {
			recoveredOnSamePool = true
			break
		}
		lastPoolErr = queryErr
		if lastPoolErr == nil {
			lastPoolErr = errors.New("persisted drift state not yet observable through the existing repository")
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !recoveredOnSamePool {
		t.Fatalf("existing repository connection pool did not recover persisted drift state after MySQL restart: err=%v", lastPoolErr)
	}
	if err := repo.Close(); err != nil {
		t.Fatalf("close application repository before simulated process restart: %v", err)
	}
	repo = nil
	repo, err = repository.Open(dsn)
	if err != nil {
		t.Fatalf("reopen repository after isolated MySQL restart: %v", err)
	}
	restartedSessionStore := service.NewStoreWithData(nil, nil, nil, nil, repo)
	restartedSessionStore.LoadUsers([]service.User{sessionUser})
	resolvedSessionUser, err := restartedSessionStore.SessionUser(sessionToken)
	if err != nil || resolvedSessionUser.ID != sessionUser.ID {
		t.Fatalf("login session did not survive MySQL restart: user=%+v err=%v", resolvedSessionUser, err)
	}
	if err := restartedSessionStore.DeleteSession(sessionToken); err != nil {
		t.Fatalf("revoke durable session after restart: %v", err)
	}
	canaryStore := rebuildCanaryStoreAfterRestart(t, repo, canary)
	incidents, incidentEvents, err := repo.DeliveryIncidents(10)
	if err != nil || len(incidents) != 1 || incidents[0].Status != "open" || incidents[0].InterventionStartedAt == nil || len(incidentEvents) != 4 || incidentEvents[1].EventType != "intervention_started" || incidentEvents[2].EventType != "causality_assessed" || incidentEvents[2].CausalityStatus != service.DeliveryIncidentCausedByDeployment || incidentEvents[3].EventType != "remediation_classified" {
		t.Fatalf("production incident did not survive MySQL restart: incidents=%+v events=%+v err=%v", incidents, incidentEvents, err)
	}
	recoveryStore := service.NewStoreWithData(nil, nil, []service.DeployLog{incidentLog}, nil, repo)
	persistedIncidents, err := recoveryStore.DeliveryIncidents(10)
	if err != nil || len(persistedIncidents) != 1 || persistedIncidents[0].CausalityStatus != service.DeliveryIncidentCausedByDeployment || persistedIncidents[0].RemediationType != service.DeliveryIncidentRemediationHotfix || persistedIncidents[0].RemediationDeployLogID == nil || *persistedIncidents[0].RemediationDeployLogID != remediationLog.ID {
		t.Fatalf("incident assessment was not reconstructed from persisted events: incidents=%+v err=%v", persistedIncidents, err)
	}
	metricSamples, err := repo.DeliveryIncidentRecoverySamples(incident.AppName, incident.ClusterID, incident.OccurredAt.Add(-time.Second))
	if err != nil || len(metricSamples) != 1 || metricSamples[0].CausalityStatus != service.DeliveryIncidentCausedByDeployment || metricSamples[0].RemediationType != service.DeliveryIncidentRemediationHotfix || metricSamples[0].RemediationPlanStatus != service.DeliveryIncidentRemediationPlanned || metricSamples[0].RemediationDeployLogID == nil || *metricSamples[0].RemediationDeployLogID != remediationLog.ID {
		t.Fatalf("incident metric assessment was not reconstructed from persisted events: samples=%+v err=%v", metricSamples, err)
	}
	recoveredAt := time.Now().UTC().Truncate(time.Microsecond)
	recovered, changed, err := recoveryStore.ConfirmDeliveryIncidentRecovery(
		incident.ID, incident.Version, recoveredAt, "E2E 业务探针确认恢复", "e2e-operator", "recovery-e2e-"+runID,
	)
	if err != nil || !changed || recovered.Status != "recovered" || recovered.Version != 5 || len(recovered.Events) != 5 || recovered.CausalityStatus != service.DeliveryIncidentCausedByDeployment {
		t.Fatalf("persist business recovery confirmation after MySQL restart: incident=%+v changed=%t err=%v", recovered, changed, err)
	}
	incidents, incidentEvents, err = repo.DeliveryIncidents(10)
	if err != nil || len(incidents) != 1 || incidents[0].Status != "recovered" || len(incidentEvents) != 5 {
		t.Fatalf("recovery event was not durable: incidents=%+v events=%+v err=%v", incidents, incidentEvents, err)
	}
	invalidRemediationID := incident.DeployLogID
	_, _, changed, err = repo.RecordDeliveryIncidentRemediation(incident.ID, recovered.Version, service.DeliveryIncidentEvent{
		IncidentID: incident.ID, EventType: "remediation_classified", DeployLogID: incident.DeployLogID,
		OccurredAt: time.Now().UTC(), RecordedAt: time.Now().UTC(), RecordedBy: "e2e-operator",
		Detail: "E2E 错误关联故障部署", RemediationType: service.DeliveryIncidentRemediationHotfix,
		RemediationDeployLogID: &invalidRemediationID, RemediationPlanStatus: service.DeliveryIncidentRemediationPlanned,
		IdempotencyKey: "invalid-remediation-e2e-" + runID,
	})
	if !errors.Is(err, service.ErrValidation) || changed {
		t.Fatalf("repository accepted the incident's own deployment as remediation: changed=%t err=%v", changed, err)
	}
	corrected, changed, err := recoveryStore.AssessDeliveryIncidentCausality(
		incident.ID, recovered.Version, service.DeliveryIncidentNotCausedByDeployment,
		"E2E 复盘后确认故障来自上游", "e2e-operator", "causality-correction-e2e-"+runID,
	)
	if err != nil || !changed || corrected.Version != 6 {
		t.Fatalf("correct persisted causality after recovery: incident=%+v changed=%t err=%v", corrected, changed, err)
	}
	corrected, changed, err = recoveryStore.RecordDeliveryIncidentRemediation(
		incident.ID, corrected.Version, service.DeliveryIncidentRemediationNoAction,
		service.DeliveryIncidentRemediationNotApplicable, 0, "E2E 复盘后修正处置分类",
		"e2e-operator", "remediation-correction-e2e-"+runID,
	)
	if err != nil || !changed || corrected.Version != 7 {
		t.Fatalf("correct persisted remediation after recovery: incident=%+v changed=%t err=%v", corrected, changed, err)
	}
	metricSamples, err = repo.DeliveryIncidentRecoverySamples(incident.AppName, incident.ClusterID, incident.OccurredAt.Add(-time.Second))
	if err != nil || len(metricSamples) != 1 || metricSamples[0].CausalityStatus != service.DeliveryIncidentNotCausedByDeployment || metricSamples[0].RemediationType != service.DeliveryIncidentRemediationNoAction || metricSamples[0].RemediationPlanStatus != service.DeliveryIncidentRemediationNotApplicable || metricSamples[0].RemediationDeployLogID != nil {
		t.Fatalf("incident metrics did not use the latest persisted revisions: samples=%+v err=%v", metricSamples, err)
	}
	assertCanaryCheckpointPersists(t, repo, canary, "paused", paused.Run.Version, 0, service.CanaryStageAwaitingConfirm)
	pausedRetry, err := service.NewCDService(config.Config{}, canaryStore, nil).ActOnDeliveryRun(
		context.Background(), canary.runID, service.DeliveryActionPause, beforePause.Version,
		"mysql-restart-canary-pause-"+runID, "e2e-operator",
	)
	if err != nil || pausedRetry.Run.Version != paused.Run.Version {
		t.Fatalf("pause idempotency did not survive application reconstruction: run=%+v err=%v", pausedRetry.Run, err)
	}
	_, canaryEvents, err := repo.DeliveryRuns()
	if err != nil {
		t.Fatalf("read canary control events after idempotent retry: %v", err)
	}
	pauseEventCount := 0
	for _, event := range canaryEvents {
		if event.RunID == canary.runID && event.EventKey == "control:mysql-restart-canary-pause-"+runID && event.EventType == "control" && event.Status == "paused" {
			pauseEventCount++
		}
	}
	if pauseEventCount != 1 {
		t.Fatalf("idempotent pause replay should preserve exactly one durable control event; count=%d events=%+v", pauseEventCount, canaryEvents)
	}
	continued, err := service.NewCDService(config.Config{}, canaryStore, nil).ActOnDeliveryRun(
		context.Background(), canary.runID, service.DeliveryActionContinue, paused.Run.Version,
		"mysql-restart-canary-continue-"+runID, "e2e-operator",
	)
	if err != nil || continued.Run.Status != "running" || continued.Run.CurrentStage != "canary_stage_1" || continued.Run.Version != paused.Run.Version+1 {
		t.Fatalf("continue after application reconstruction: run=%+v err=%v", continued.Run, err)
	}
	if err := canaryStore.CheckResourceLock(canary.lock); err == nil {
		t.Fatal("continued canary released its durable Deployment lock")
	}
	advanced, err := service.NewCDService(config.Config{}, canaryStore, nil).ActOnDeliveryRun(
		context.Background(), canary.runID, service.DeliveryActionConfirmCanaryStage, continued.Run.Version,
		"mysql-restart-canary-confirm-"+runID, "e2e-operator",
	)
	if err != nil || advanced.Run.CurrentStage != "canary_stage_2" || advanced.Run.Status != "running" {
		t.Fatalf("reconstructed canary could not explicitly advance after rechecking its persisted observation: run=%+v err=%v", advanced.Run, err)
	}
	if err := canaryStore.CheckResourceLock(canary.lock); err == nil {
		t.Fatal("advancing the canary stage released its durable Deployment lock")
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		states, err = repo.DeliveryDriftStates()
		if err == nil && len(states) == 1 && states[0].Status == "drifted" && states[0].ActualImage != nil && *states[0].ActualImage == actual {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil || len(states) != 1 || states[0].Status != "drifted" || states[0].ActualImage == nil || *states[0].ActualImage != actual {
		t.Fatalf("repository did not recover persisted state after MySQL restart: states=%+v err=%v", states, err)
	}
	events, err := repo.DeliveryDriftHistory(service.DeliveryDriftHistoryFilter{
		ClusterID: 1, AppIDs: []int{1}, Limit: 10,
	})
	if err != nil || len(events) != 1 || events[0].Status != "drifted" {
		t.Fatalf("history did not survive MySQL restart: events=%+v err=%v", events, err)
	}
	assertCanaryCheckpointPersists(t, repo, canary, "running", advanced.Run.Version, 1, service.CanaryStageAwaitingWeights)
	versions, err = repo.DeliveryEnvironmentEvidencePolicyVersions()
	if err != nil || len(versions) != 2 || versions[0].Version != 1 || versions[1].Version != 2 ||
		versions[1].EnvironmentID != environmentID || versions[1].TrustedSubmitters["smoke"][0] != "apikey:smoke-prod" {
		t.Fatalf("evidence policy history did not survive MySQL restart: policies=%+v err=%v", versions, err)
	}
}

func assertWorkerClaimOwnershipAcrossStores(t *testing.T, db *sql.DB, repo *repository.MySQL, clusterID int, runID string) {
	t.Helper()
	logRow, err := repo.SaveLog(service.DeployLog{
		ClusterID: clusterID, AppName: "claim-race-" + runID, DeploymentName: "claim-race", ContainerName: "app",
		Namespace: "claim-e2e", Version: "v1", FileName: "claim-race.tar", DeployType: "image_deploy", Status: "pending", StepDetail: "[]",
	})
	if err != nil {
		t.Fatalf("create pending task for competing Worker claims: %v", err)
	}
	defer func() {
		if _, err := db.Exec(`DELETE FROM deploy_logs WHERE id=?`, logRow.ID); err != nil {
			t.Errorf("remove competing Worker claim task: %v", err)
		}
	}()
	storeA := service.NewStoreWithData(nil, nil, []service.DeployLog{logRow}, nil, repo)
	storeB := service.NewStoreWithData(nil, nil, []service.DeployLog{logRow}, nil, repo)
	if claimed, err := storeA.ClaimDeployLog(logRow.ID, "worker-claim-a"); err != nil || !claimed {
		t.Fatalf("first Store claim: claimed=%t err=%v", claimed, err)
	}
	if claimed, err := storeB.ClaimDeployLog(logRow.ID, "worker-claim-b"); err != nil || claimed {
		t.Fatalf("competing Store claim: claimed=%t err=%v", claimed, err)
	}
	durable, err := repo.LogByID(logRow.ID)
	if err != nil || durable.Status != "running" || durable.WorkerClaimID != "worker-claim-a" {
		t.Fatalf("competing claim changed durable owner: task=%+v err=%v", durable, err)
	}
	progress := durable
	step := "claim-checkpoint"
	progress.CurrentStep = &step
	if err := storeA.UpdateLogExecution(progress); err != nil {
		t.Fatalf("current Worker claim could not persist a fenced checkpoint: %v", err)
	}
	if _, created, err := storeA.BeginExecutionAction(service.ExecutionAction{
		DeployLogID: logRow.ID, ActionKey: "current-write", ActionKind: "set_image", Status: service.ExecutionActionIntent,
		Intent: json.RawMessage(`{"target":"registry.local/app@sha256:deadbeef"}`), CreatedAt: time.Now().UTC(),
	}); err != nil || !created {
		t.Fatalf("current Worker claim could not journal its external action: created=%t err=%v", created, err)
	}
	staleUpdate := durable
	staleUpdate.Status = "failed"
	if err := repo.UpdateLogExecutionWithClaim(staleUpdate, "worker-claim-b"); !errors.Is(err, service.ErrConflict) {
		t.Fatalf("stale Worker task write error=%v, want claim conflict", err)
	}
	if _, _, err := repo.BeginExecutionActionWithClaim(service.ExecutionAction{
		DeployLogID: logRow.ID, ActionKey: "stale-write", ActionKind: "set_image", Status: service.ExecutionActionIntent,
		Intent: json.RawMessage(`{"target":"registry.local/app@sha256:deadbeef"}`), CreatedAt: time.Now().UTC(),
	}, "worker-claim-b"); !errors.Is(err, service.ErrConflict) {
		t.Fatalf("stale Worker action journal error=%v, want claim conflict", err)
	}
	recoveredStore := service.NewStoreWithData(nil, nil, []service.DeployLog{durable}, nil, repo)
	recoveredWorker, err := service.NewRecoveredWorker(
		context.Background(), service.NewPipeline(config.Config{}, recoveredStore, nil, nil), 1,
	)
	if err != nil {
		t.Fatalf("recover task with abandoned Worker claim: %v", err)
	}
	recoveredWorker.Close()
	durable, err = repo.LogByID(logRow.ID)
	if err != nil || durable.Status != "interrupted" || durable.WorkerClaimID != "" {
		t.Fatalf("restart recovery did not interrupt ambiguity and clear old ownership: task=%+v err=%v", durable, err)
	}
	staleUpdate = durable
	staleUpdate.Status = "failed"
	if err := storeA.UpdateLogExecution(staleUpdate); !errors.Is(err, service.ErrConflict) {
		t.Fatalf("old process wrote after restart recovery error=%v, want claim conflict", err)
	}
	actions, err := repo.ExecutionActions(logRow.ID)
	if err != nil || len(actions) != 1 || actions[0].ActionKey != "current-write" || actions[0].Status != service.ExecutionActionIntent {
		t.Fatalf("restart recovery lost unresolved external action evidence: actions=%+v err=%v", actions, err)
	}
}

func assertQueuedCanaryCancellationSerializesWithWorkerClaim(
	t *testing.T,
	db *sql.DB,
	repo *repository.MySQL,
	canary mysqlRestartCanaryFixture,
	runID string,
) {
	t.Helper()

	cancelFirst := seedQueuedCanaryControlRun(t, db, repo, canary, "cancel-first-"+runID)
	cd := service.NewCDService(config.Config{}, cancelFirst.store, nil)
	cancelled, err := cd.ActOnDeliveryRun(context.Background(), cancelFirst.runID, service.DeliveryActionCancel, 1,
		"mysql-canary-cancel-first-"+runID, "e2e-operator")
	if err != nil || cancelled.Run.Status != "cancelled" || cancelled.Run.CurrentStage != "cancelled" || cancelled.Run.Version != 2 {
		t.Fatalf("cancel queued canary before Worker claim: run=%+v err=%v", cancelled.Run, err)
	}
	duplicate, err := cd.ActOnDeliveryRun(context.Background(), cancelFirst.runID, service.DeliveryActionCancel, 1,
		"mysql-canary-cancel-first-"+runID, "e2e-operator")
	if err != nil || duplicate.Run.Status != "cancelled" || duplicate.Run.Version != 2 {
		t.Fatalf("replay queued canary cancellation: run=%+v err=%v", duplicate.Run, err)
	}
	claimedAfterCancel, err := repo.ClaimLog(cancelFirst.deployLogID)
	if err != nil || claimedAfterCancel {
		t.Fatalf("Worker claim after queued cancellation: claimed=%t err=%v, want terminal task to stay unclaimed", claimedAfterCancel, err)
	}
	assertQueuedCanaryControlPersistence(t, db, repo, cancelFirst, "cancelled", "failed", 1, 0)
	cleanupQueuedCanaryControlRun(t, db, cancelFirst)

	claimFirst := seedQueuedCanaryControlRun(t, db, repo, canary, "claim-first-"+runID)
	claimed, err := claimFirst.store.ClaimDeployLog(claimFirst.deployLogID)
	if err != nil || !claimed {
		t.Fatalf("claim queued canary task before cancellation: claimed=%t err=%v", claimed, err)
	}
	claimCD := service.NewCDService(config.Config{}, claimFirst.store, nil)
	cancelledAfterClaim, err := claimCD.ActOnDeliveryRun(
		context.Background(), claimFirst.runID, service.DeliveryActionCancel, 1,
		"mysql-canary-claim-first-"+runID, "e2e-operator",
	)
	if err != nil || cancelledAfterClaim.Run.Status != "cancelled" || cancelledAfterClaim.Run.Version != 2 {
		t.Fatalf("cancellation after Worker claim but before external actions: run=%+v err=%v", cancelledAfterClaim.Run, err)
	}
	if _, _, err := claimFirst.store.BeginExecutionAction(service.ExecutionAction{
		DeployLogID: claimFirst.deployLogID, ActionKey: "stale-canary-write-" + runID, ActionKind: "gateway_weights",
		Intent: json.RawMessage(`{"route_id":"orders"}`), CreatedAt: time.Now().UTC(),
	}); !errors.Is(err, service.ErrConflict) {
		t.Fatalf("revoked Worker claim created an external action: %v", err)
	}
	assertQueuedCanaryControlPersistence(t, db, repo, claimFirst, "cancelled", "failed", 1, 0)
	claimedAfterCancel, err = repo.ClaimLogWithOwnership(claimFirst.deployLogID, "another-worker")
	if err != nil || claimedAfterCancel {
		t.Fatalf("cancelled task was claimed again: claimed=%t err=%v", claimedAfterCancel, err)
	}
	cleanupQueuedCanaryControlRun(t, db, claimFirst)

	actionFirst := seedQueuedCanaryControlRun(t, db, repo, canary, "action-first-"+runID)
	claimed, err = actionFirst.store.ClaimDeployLog(actionFirst.deployLogID)
	if err != nil || !claimed {
		t.Fatalf("claim canary before action-journal conflict test: claimed=%t err=%v", claimed, err)
	}
	if _, _, err := actionFirst.store.BeginExecutionAction(service.ExecutionAction{
		DeployLogID: actionFirst.deployLogID, ActionKey: "canary-write-" + runID, ActionKind: "gateway_weights",
		Intent: json.RawMessage(`{"route_id":"orders"}`), CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("journal canary external action: %v", err)
	}
	if _, err := service.NewCDService(config.Config{}, actionFirst.store, nil).ActOnDeliveryRun(
		context.Background(), actionFirst.runID, service.DeliveryActionCancel, 1,
		"mysql-canary-action-first-"+runID, "e2e-operator",
	); !errors.Is(err, service.ErrConflict) {
		t.Fatalf("cancellation after external action intent error=%v, want conflict", err)
	}
	assertQueuedCanaryControlPersistence(t, db, repo, actionFirst, "queued", "running", 0, 1)
	cleanupQueuedCanaryControlRun(t, db, actionFirst)
}

func seedQueuedCanaryControlRun(
	t *testing.T,
	db *sql.DB,
	repo *repository.MySQL,
	canary mysqlRestartCanaryFixture,
	suffix string,
) mysqlRestartCanaryFixture {
	t.Helper()

	appName := "e2e-" + suffix
	namespace := "cancel-e2e"
	result, err := db.Exec(`INSERT INTO deploy_logs(cluster_id,app_name,deployment_name,container_name,namespace,version,file_name,status,step_detail)
		VALUES(?,?,?,'app',?,'v1','canary-control-e2e.tar','pending','[]')`, canary.log.ClusterID, appName, appName, namespace)
	if err != nil {
		t.Fatalf("insert queued canary control task: %v", err)
	}
	deployLogID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read queued canary control task id: %v", err)
	}
	lock := service.ResourceLock{ClusterID: canary.log.ClusterID, Namespace: namespace, Kind: "deployment", Name: appName}
	lockDigest := sha256.Sum256([]byte(lock.Identity()))
	if _, err := db.Exec(`INSERT INTO delivery_resource_locks(resource_key,cluster_id,namespace,resource_kind,resource_name,deploy_log_id)
		VALUES(?,?,?,?,?,?)`, hex.EncodeToString(lockDigest[:]), lock.ClusterID, lock.Namespace, lock.Kind, lock.Name, deployLogID); err != nil {
		t.Fatalf("reserve queued canary control task lock: %v", err)
	}
	result, err = db.Exec(`INSERT INTO delivery_runs(delivery_plan_id,promotion_request_id,deploy_log_id,strategy,status,current_stage,version)
		VALUES(?,?,?,'gateway_canary','queued','queued',1)`, canary.planID, canary.promotionRequestID, deployLogID)
	if err != nil {
		t.Fatalf("insert queued canary control run: %v", err)
	}
	runRowID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read queued canary control run id: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := db.Exec(`INSERT INTO delivery_run_events(run_id,run_version,event_key,event_type,stage,status,detail,created_at)
		VALUES(?,1,'run-created','queued','queued','queued','生产审批通过，发布运行已排队。',?)`, runRowID, now); err != nil {
		t.Fatalf("insert queued canary control event: %v", err)
	}
	log := service.DeployLog{
		ID: deployLogID, ClusterID: canary.log.ClusterID, Status: "pending", AppName: appName,
		DeploymentName: appName, ContainerName: "app", Namespace: namespace, StepDetail: "[]",
	}
	store := service.NewStoreWithData(nil, nil, []service.DeployLog{log}, nil, repo)
	store.LoadDeliveryPlans([]service.DeliveryPlan{canary.plan})
	store.LoadDeliveryRuns([]service.DeliveryRun{{
		ID: runRowID, DeliveryPlanID: canary.planID, PromotionRequestID: canary.promotionRequestID, DeployLogID: deployLogID,
		Strategy: "gateway_canary", Status: "queued", CurrentStage: "queued", Version: 1, CreatedAt: now, UpdatedAt: now,
	}}, []service.DeliveryRunEvent{{
		RunID: runRowID, Version: 1, EventKey: "run-created", EventType: "queued", Stage: "queued", Status: "queued",
		Detail: "生产审批通过，发布运行已排队。", CreatedAt: now,
	}})
	return mysqlRestartCanaryFixture{
		runID: runRowID, deployLogID: deployLogID, planID: canary.planID, promotionRequestID: canary.promotionRequestID,
		log: log, plan: canary.plan, store: store, lock: lock,
	}
}

func cleanupQueuedCanaryControlRun(t *testing.T, db *sql.DB, fixture mysqlRestartCanaryFixture) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM delivery_execution_actions WHERE deploy_log_id=?`, fixture.deployLogID); err != nil {
		t.Fatalf("remove queued canary control actions: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM delivery_runs WHERE id=?`, fixture.runID); err != nil {
		t.Fatalf("remove queued canary control run: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM delivery_resource_locks WHERE deploy_log_id=?`, fixture.deployLogID); err != nil {
		t.Fatalf("remove queued canary control resource lock: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM deploy_logs WHERE id=?`, fixture.deployLogID); err != nil {
		t.Fatalf("remove queued canary control task: %v", err)
	}
}

func assertQueuedCanaryControlPersistence(
	t *testing.T,
	db *sql.DB,
	repo *repository.MySQL,
	fixture mysqlRestartCanaryFixture,
	expectedRunStatus string,
	expectedTaskStatus string,
	expectedControlEventCount int,
	expectedLockCount int,
) {
	t.Helper()
	runs, events, err := repo.DeliveryRuns()
	if err != nil {
		t.Fatalf("read queued canary control persistence: %v", err)
	}
	var run *service.DeliveryRun
	controlEventCount := 0
	for i := range runs {
		if runs[i].ID == fixture.runID {
			run = &runs[i]
			break
		}
	}
	for _, event := range events {
		if event.RunID == fixture.runID && event.EventType == "control" {
			controlEventCount++
		}
	}
	if run == nil || run.Status != expectedRunStatus || controlEventCount != expectedControlEventCount {
		t.Fatalf("queued canary control run/events: run=%+v control_event_count=%d, want status=%q and %d control events", run, controlEventCount, expectedRunStatus, expectedControlEventCount)
	}
	log, err := repo.LogByID(fixture.deployLogID)
	if err != nil || log.Status != expectedTaskStatus {
		t.Fatalf("queued canary control task status=%q err=%v, want %q", log.Status, err, expectedTaskStatus)
	}
	var lockCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM delivery_resource_locks WHERE deploy_log_id=?`, fixture.deployLogID).Scan(&lockCount); err != nil {
		t.Fatalf("count queued canary control locks: %v", err)
	}
	if lockCount != expectedLockCount {
		t.Fatalf("queued canary control lock count=%d, want %d", lockCount, expectedLockCount)
	}
}

type mysqlRestartCanaryFixture struct {
	runID              int64
	deployLogID        int64
	planID             int64
	promotionRequestID int64
	log                service.DeployLog
	plan               service.DeliveryPlan
	store              *service.Store
	lock               service.ResourceLock
}

func seedCanaryRun(t *testing.T, db *sql.DB, repo *repository.MySQL, runID string) mysqlRestartCanaryFixture {
	t.Helper()
	insertID := func(query string, args ...any) int64 {
		t.Helper()
		result, err := db.Exec(query, args...)
		if err != nil {
			t.Fatalf("insert isolated canary persistence fixture: %v", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("read isolated canary fixture id: %v", err)
		}
		return id
	}

	clusterID := insertID(`INSERT INTO clusters(name,env_code,k8s_master_host,k8s_master_port,default_namespace)
		VALUES(?,?,?,6443,'default')`, "E2E "+runID, "e2e-"+runID, "https://127.0.0.1")
	appName := "e2e-canary-" + runID
	appID := insertID(`INSERT INTO apps(cluster_id,app_name,match_keyword,deployment_name,container_name,namespace)
		VALUES(?,?,?,?,?,'default')`, clusterID, appName, appName, appName, "app")
	deployLogID := insertID(`INSERT INTO deploy_logs(cluster_id,app_name,deployment_name,container_name,namespace,version,file_name)
		VALUES(?,?,?,?,?,'v1','canary-e2e.tar')`, clusterID, appName, appName, "app", "default")
	lock := service.ResourceLock{ClusterID: int(clusterID), Namespace: "default", Kind: "deployment", Name: appName}
	lockDigest := sha256.Sum256([]byte(lock.Identity()))
	if _, err := db.Exec(`INSERT INTO delivery_resource_locks(resource_key,cluster_id,namespace,resource_kind,resource_name,deploy_log_id)
		VALUES(?,?,?,?,?,?)`, hex.EncodeToString(lockDigest[:]), lock.ClusterID, lock.Namespace, lock.Kind, lock.Name, deployLogID); err != nil {
		t.Fatalf("reserve durable canary Deployment lock: %v", err)
	}
	releaseID := insertID(`INSERT INTO image_releases(event_key,event_id,app_name,image_full,version,test_cluster_id,test_deploy_log_id)
		VALUES(?,?,?,?,'v1',?,?)`, strings.Repeat("a", 52)+runID, "e2e-"+runID, appName,
		"registry.example.com/e2e/"+appName+"@sha256:"+strings.Repeat("c", 64), clusterID, deployLogID)
	policy := mysqlRestartCanaryPolicy()
	route := service.CanaryGatewayRouteRef{ConnectionID: 7, RouteID: "orders-route", StableBackendID: "orders-stable", CandidateBackendID: "orders-candidate"}
	policy.GatewayRoute = &route
	stableDigest := "sha256:" + strings.Repeat("d", 64)
	candidateDigest := "sha256:" + strings.Repeat("c", 64)
	snapshot := service.DeliveryPlanSnapshot{
		SchemaVersion: 4, Strategy: "gateway_canary",
		Artifact: service.DeliveryPlanArtifact{Image: "registry.example.com/e2e/" + appName + "@" + candidateDigest, Digest: &candidateDigest},
		Target:   service.DeliveryPlanTarget{StableImageDigest: stableDigest},
		Policy:   service.DeliveryPlanPolicySnapshot{Strategy: "gateway_canary", CanaryAnalysis: &policy},
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("encode isolated canary plan snapshot: %v", err)
	}
	planID := insertID(`INSERT INTO delivery_plans(release_id,target_cluster_id,target_app_id,strategy,status,snapshot_hash,snapshot_json,blockers_json,requested_by)
		VALUES(?,?,?,'gateway_canary','preview_only',?,?,JSON_ARRAY(),'e2e')`,
		releaseID, clusterID, appID, strings.Repeat("b", 52)+runID, string(snapshotJSON))
	promotionID := insertID(`INSERT INTO promotion_requests(release_id,delivery_plan_id,prod_cluster_id,requester,snapshot,template_hash,expected_image,baseline_log_id)
		VALUES(?,?,?,'e2e','snapshot','template','registry.example.com/e2e/canary:v1',?)`,
		releaseID, planID, clusterID, deployLogID)
	runIDValue := insertID(`INSERT INTO delivery_runs(delivery_plan_id,promotion_request_id,deploy_log_id,strategy,status,current_stage,version)
		VALUES(?,?,?,'gateway_canary','running','queued',1)`, planID, promotionID, deployLogID)

	now := time.Now().UTC().Truncate(time.Second)
	log := service.DeployLog{ID: deployLogID, Status: "pending", AppName: appName, DeploymentName: appName, ContainerName: "app", Namespace: "default", StepDetail: "[]"}
	store := service.NewStoreWithData(nil, nil, []service.DeployLog{log}, nil, repo)
	plan := service.DeliveryPlan{
		ID: planID, ReleaseID: releaseID, TargetClusterID: int(clusterID), TargetAppID: int(appID),
		Strategy: "gateway_canary", Status: "preview_only", Snapshot: snapshot,
	}
	store.LoadDeliveryPlans([]service.DeliveryPlan{plan})
	store.LoadDeliveryRuns([]service.DeliveryRun{{
		ID: runIDValue, DeliveryPlanID: planID, PromotionRequestID: promotionID, DeployLogID: deployLogID,
		Strategy: "gateway_canary", Status: "running", CurrentStage: "queued", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}}, nil)
	awaitingWeights := service.CanaryStageCheckpoint{
		SchemaVersion: service.CanaryStageCheckpointSchemaVersion, StageIndex: 0,
		CandidateWeightPercent: 10, ObservationDurationSeconds: policy.Stages[0].ObservationDurationSeconds, Phase: service.CanaryStageAwaitingWeights,
	}
	if _, found, err := store.RecordCanaryStageCheckpoint(deployLogID, 1, "canary:stage-1:awaiting-weights", "running", "等待网关权重回读。", awaitingWeights); err != nil || !found {
		t.Fatalf("persist initial canary checkpoint: found=%t err=%v", found, err)
	}
	adapter := &mysqlRestartCanaryGateway{state: service.CanaryGatewayRouteState{
		ConnectionID: route.ConnectionID, RouteID: route.RouteID, Revision: "revision-1",
		StableBackendID: route.StableBackendID, CandidateBackendID: route.CandidateBackendID,
		Weights: service.CanaryTrafficWeights{StableWeightPercent: 100},
	}}
	result, err := store.ApplyCanaryStageWeights(context.Background(), deployLogID, route, adapter)
	if err != nil || result.Status != service.CanaryStageObserving || adapter.setCalls != 1 {
		t.Fatalf("persist canary write intent/readback/checkpoint: result=%+v set_calls=%d err=%v", result, adapter.setCalls, err)
	}
	observing, err := store.LatestCanaryStageCheckpoint(runIDValue)
	if err != nil || observing == nil || observing.StageStartedAt == nil || observing.EffectiveWeights == nil {
		t.Fatalf("load persisted observing checkpoint: checkpoint=%+v err=%v", observing, err)
	}
	windowEnd := observing.StageStartedAt.Add(time.Duration(policy.Stages[0].ObservationDurationSeconds) * time.Second)
	if wait := time.Until(windowEnd); wait > 0 {
		timer := time.NewTimer(wait)
		<-timer.C
	}
	observedAt := time.Now().UTC()
	stableP95, candidateP95 := 80.0, 90.0
	stableDigest = snapshot.Target.StableImageDigest
	observation := service.CanaryMetricObservation{
		StableImageDigest: stableDigest, CandidateImageDigest: candidateDigest,
		WindowStart: *observing.StageStartedAt, WindowEnd: windowEnd, ObservedAt: observedAt,
		Stable:    service.CanaryVersionMetrics{ImageDigest: stableDigest, Requests: 100, Errors: 0, P95LatencyMillis: &stableP95, Health: "healthy"},
		Candidate: service.CanaryVersionMetrics{ImageDigest: candidateDigest, Requests: 100, Errors: 0, P95LatencyMillis: &candidateP95, Health: "healthy"},
	}
	decision, err := service.EvaluateCanaryStageObservation(policy, 0, *observing.StageStartedAt, *observing.EffectiveWeights, observation, observedAt)
	if err != nil || decision.Analysis.Status != "awaiting_confirmation" {
		t.Fatalf("evaluate healthy restart canary observation: decision=%+v err=%v", decision, err)
	}
	awaitingConfirm := *observing
	awaitingConfirm.Phase = service.CanaryStageAwaitingConfirm
	awaitingConfirm.Observation = &observation
	awaitingConfirm.Decision = &decision
	currentRun, err := store.DeliveryRun(runIDValue)
	if err != nil {
		t.Fatalf("read run before persisting healthy observation: %v", err)
	}
	if _, found, err := store.RecordCanaryStageCheckpoint(deployLogID, currentRun.Version, "canary:stage-1:awaiting-confirmation", "running", "source=canary_metrics;result=within_thresholds_manual_confirmation_required;健康指标等待人工确认。", awaitingConfirm); err != nil || !found {
		t.Fatalf("persist healthy canary confirmation checkpoint: found=%t err=%v", found, err)
	}

	return mysqlRestartCanaryFixture{
		runID: runIDValue, deployLogID: deployLogID, planID: planID, promotionRequestID: promotionID,
		log: log, plan: plan, store: store, lock: lock,
	}
}

func assertCanaryCheckpointPersists(
	t *testing.T,
	repo *repository.MySQL,
	fixture mysqlRestartCanaryFixture,
	expectedRunStatus string,
	expectedRunVersion int64,
	expectedStageIndex int,
	expectedCheckpointPhase string,
) {
	t.Helper()
	runs, events, err := repo.DeliveryRuns()
	if err != nil {
		t.Fatalf("read delivery runs and event payloads: %v", err)
	}
	var runFound bool
	var deployLogID int64
	var latestCheckpointEvent *service.DeliveryRunEvent
	for _, run := range runs {
		if run.ID == fixture.runID && run.Strategy == "gateway_canary" && run.Status == expectedRunStatus && run.Version == expectedRunVersion &&
			run.CurrentStage == fmt.Sprintf("canary_stage_%d", expectedStageIndex+1) {
			runFound = true
			deployLogID = run.DeployLogID
		}
	}
	for _, event := range events {
		if event.RunID != fixture.runID || event.EventType != "canary_checkpoint" {
			continue
		}
		if latestCheckpointEvent == nil || event.Version > latestCheckpointEvent.Version {
			copy := event
			latestCheckpointEvent = &copy
		}
	}
	var eventFound bool
	if latestCheckpointEvent != nil {
		checkpoint, err := service.DecodeCanaryStageCheckpoint(*latestCheckpointEvent)
		if err != nil {
			t.Fatalf("decode persisted canary checkpoint: %v", err)
		}
		if checkpoint.StageIndex != expectedStageIndex || checkpoint.CandidateWeightPercent != fixture.plan.Snapshot.Policy.CanaryAnalysis.Stages[expectedStageIndex].CandidateWeightPercent ||
			checkpoint.Phase != expectedCheckpointPhase || expectedCheckpointPhase != service.CanaryStageAwaitingWeights && checkpoint.StageStartedAt == nil {
			t.Fatalf("unexpected persisted canary checkpoint: %+v", checkpoint)
		}
		if expectedStageIndex == 0 && (checkpoint.EffectiveWeights == nil || *checkpoint.EffectiveWeights != (service.CanaryTrafficWeights{StableWeightPercent: 90, CandidateWeightPercent: 10}) || checkpoint.Decision == nil || checkpoint.Decision.Analysis.Status != "awaiting_confirmation") {
			t.Fatalf("first-stage healthy readback or decision was not preserved: %+v", checkpoint)
		}
		if expectedCheckpointPhase == service.CanaryStageAwaitingWeights && (checkpoint.EffectiveWeights != nil || checkpoint.Observation != nil || checkpoint.Decision != nil) {
			t.Fatalf("new stage checkpoint unexpectedly inherited previous-stage evidence: %+v", checkpoint)
		}
		eventFound = true
	}
	actions, err := repo.ExecutionActions(deployLogID)
	if err != nil {
		t.Fatalf("read canary gateway execution action: %v", err)
	}
	if !runFound || !eventFound || deployLogID != fixture.deployLogID || len(actions) != 1 || actions[0].Status != service.ExecutionActionApplied || actions[0].ActionKind != "gateway_route_weights" {
		t.Fatalf("canary run/event/action evidence did not persist: run_found=%t event_found=%t actions=%+v", runFound, eventFound, actions)
	}
	owner, found, err := repo.ResourceLockOwner(fixture.lock)
	if err != nil || !found || owner != fixture.deployLogID {
		t.Fatalf("canary resource lock did not survive run transition: owner=%d found=%t err=%v", owner, found, err)
	}
}

func rebuildCanaryStoreAfterRestart(t *testing.T, repo *repository.MySQL, fixture mysqlRestartCanaryFixture) *service.Store {
	t.Helper()
	runs, events, err := repo.DeliveryRuns()
	if err != nil {
		t.Fatalf("reload delivery runs after MySQL restart: %v", err)
	}
	log, err := repo.LogByID(fixture.deployLogID)
	if err != nil {
		t.Fatalf("reload canary deploy log after MySQL restart: %v", err)
	}
	plan, err := repo.FindDeliveryPlan(fixture.planID)
	if err != nil {
		t.Fatalf("reload canary plan after MySQL restart: %v", err)
	}
	store := service.NewStoreWithData(nil, nil, []service.DeployLog{log}, nil, repo)
	store.LoadDeliveryPlans([]service.DeliveryPlan{plan})
	store.LoadDeliveryRuns(runs, events)
	return store
}

func assertIdentityStateSharedAcrossStores(t *testing.T, repo *repository.MySQL, runID string) {
	t.Helper()
	passwordHash, err := service.HashPassword("e2e-identity-password")
	if err != nil {
		t.Fatalf("hash identity fixture password: %v", err)
	}
	user, err := repo.SaveUser(service.User{
		Username: "identity-e2e-" + runID, Role: service.RoleOperator,
		PasswordHash: passwordHash, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create shared identity user: %v", err)
	}
	first := service.NewStoreWithData(nil, nil, nil, nil, repo)
	second := service.NewStoreWithData(nil, nil, nil, nil, repo)
	first.LoadUsers([]service.User{user})
	second.LoadUsers([]service.User{user})

	firstUser, err := second.FindUser(user.ID)
	if err != nil {
		t.Fatalf("read identity user from second Store: %v", err)
	}
	firstUser.Enabled = false
	firstUser.Role = service.RoleViewer
	if _, err := second.UpdateUser(firstUser); err != nil {
		t.Fatalf("disable shared identity user: %v", err)
	}
	refreshedUser, err := first.FindUser(user.ID)
	if err != nil || refreshedUser.Enabled || refreshedUser.Role != service.RoleViewer {
		t.Fatalf("first Store retained stale user authorization: user=%+v err=%v", refreshedUser, err)
	}
	listedUsers, err := first.ListUsers()
	var listedDisabledUser bool
	for _, listed := range listedUsers {
		if listed.ID == user.ID {
			listedDisabledUser = !listed.Enabled && listed.Role == service.RoleViewer
		}
	}
	if err != nil || !listedDisabledUser {
		t.Fatalf("first Store retained stale user list: users=%+v err=%v", listedUsers, err)
	}

	token, keyHash, prefix, err := service.NewAPIKeyToken()
	if err != nil {
		t.Fatalf("mint shared API key: %v", err)
	}
	first.LoadAPIKeys([]service.APIKey{})
	second.LoadAPIKeys([]service.APIKey{})
	key, err := first.SaveAPIKey(service.APIKey{
		Name: "identity-e2e-" + runID, KeyHash: keyHash, Prefix: prefix,
		Role: service.RoleOperator, Enabled: true,
	})
	if err != nil {
		t.Fatalf("persist shared API key: %v", err)
	}
	resolved, err := second.ResolveAPIKey(token)
	if err != nil || resolved.ID != key.ID || resolved.Role != service.RoleOperator {
		t.Fatalf("second Store did not see API key created by first: key=%+v err=%v", resolved, err)
	}
	key, err = second.FindAPIKey(key.ID)
	if err != nil {
		t.Fatalf("reload shared API key before revocation: %v", err)
	}
	key.Enabled = false
	if _, err := second.UpdateAPIKey(key); err != nil {
		t.Fatalf("revoke shared API key: %v", err)
	}
	if _, err := first.ResolveAPIKey(token); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("first Store accepted API key revoked by second: %v", err)
	}

	if err := first.AddAudit(service.AuditEntry{
		Username: user.Username, Action: "identity_shared_e2e", Detail: runID, Result: service.AuditSuccess,
	}); err != nil {
		t.Fatalf("persist shared audit entry: %v", err)
	}
	entries, err := second.RecentAudits(50)
	if err != nil {
		t.Fatalf("read shared audit entries: %v", err)
	}
	for _, entry := range entries {
		if entry.Action == "identity_shared_e2e" && entry.Detail == runID {
			return
		}
	}
	t.Fatalf("second Store did not read audit entry from first: entries=%+v", entries)
}

func mysqlRestartCanaryPolicy() service.CanaryAnalysisPolicy {
	maxCandidateErrorRate, maxErrorRateIncrease := 0.02, 0.01
	maxP95, maxP95Increase := 200.0, 0.5
	return service.CanaryAnalysisPolicy{
		MinRequests: 100, MaxObservationAgeSeconds: 300,
		MaxCandidateErrorRate: &maxCandidateErrorRate, MaxErrorRateIncrease: &maxErrorRateIncrease,
		MaxCandidateP95LatencyMillis: &maxP95, MaxP95LatencyIncreaseRatio: &maxP95Increase,
		Stages: []service.CanaryStage{{CandidateWeightPercent: 10, ObservationDurationSeconds: 1}, {CandidateWeightPercent: 100, ObservationDurationSeconds: 60}},
	}
}

type mysqlRestartCanaryGateway struct {
	state    service.CanaryGatewayRouteState
	setCalls int
}

func (g *mysqlRestartCanaryGateway) ReadCanaryRoute(context.Context, service.CanaryGatewayRouteRef) (service.CanaryGatewayRouteState, error) {
	return g.state, nil
}

func (g *mysqlRestartCanaryGateway) SetCanaryRouteWeights(_ context.Context, _ service.CanaryGatewayRouteRef, expectedRevision string, desired service.CanaryTrafficWeights, _ string) error {
	g.setCalls++
	if expectedRevision != g.state.Revision {
		return fmt.Errorf("conditional revision mismatch")
	}
	g.state.Weights = desired
	g.state.Revision = "revision-2"
	return nil
}

func waitForE2EMySQL(t *testing.T, container, rootPassword, database string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var lastErr error
	var lastOutput []byte
	for time.Now().Before(deadline) {
		query := "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='" + database + "'"
		lastOutput, lastErr = exec.Command("docker", "exec", container,
			"mysql", "--protocol=TCP", "-h127.0.0.1", "-uroot", "-p"+rootPassword, "-Nse", query).CombinedOutput()
		lines := strings.Split(strings.TrimSpace(string(lastOutput)), "\n")
		if lastErr == nil && len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "1" {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("MySQL container %s did not become ready: %v\n%s", container, lastErr, lastOutput)
}

func applyFullSchemaToE2EMySQL(t *testing.T, container, rootPassword, database string) {
	t.Helper()
	mysqlCommand := "docker exec -i " + container + " mysql -uroot -p" + rootPassword
	env := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "MYSQL_CMD=") {
			env = append(env, item)
		}
	}
	env = append(env, "MYSQL_CMD="+mysqlCommand)
	script := filepath.Join(backendRoot(t), "..", "scripts", "apply-schema.sh")
	apply := exec.Command("bash", script, database)
	apply.Env = env
	if out, err := apply.CombinedOutput(); err != nil {
		t.Fatalf("apply schema and migrations 001–038 to isolated MySQL: %v\n%s", err, out)
	}
}
