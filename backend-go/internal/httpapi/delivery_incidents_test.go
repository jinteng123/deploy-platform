package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/service"
)

func TestDeliveryIncidentAPIRequiresOperatorAndLinksRecovery(t *testing.T) {
	app, engine, _ := newTestApp(t)
	cluster, err := app.store.FindCluster(1)
	if err != nil {
		t.Fatal(err)
	}
	cluster.Environment = "production"
	cluster.EnvironmentTier = "production"
	if _, err := app.store.UpdateCluster(cluster); err != nil {
		t.Fatal(err)
	}
	prod := "production"
	log, err := app.store.AddLog(service.DeployLog{
		ClusterID: 1, AppName: "storefront", DeploymentName: "storefront", ContainerName: "api",
		Namespace: "shop", Status: "success", EnvironmentTierSnapshot: &prod,
	})
	if err != nil {
		t.Fatal(err)
	}
	logResponse := do(t, engine, http.MethodGet, "/api/v1/deploy/logs?cluster_id=1", nil)
	var visibleLogs []struct {
		EnvironmentTierSnapshot *string `json:"environment_tier_snapshot"`
	}
	if logResponse.Code != http.StatusOK || json.Unmarshal(logResponse.Body.Bytes(), &visibleLogs) != nil || len(visibleLogs) != 1 || visibleLogs[0].EnvironmentTierSnapshot == nil || *visibleLogs[0].EnvironmentTierSnapshot != "production" {
		t.Fatalf("deployment API did not expose its creation-time environment tier: %d %s", logResponse.Code, logResponse.Body.String())
	}
	remediationLog, err := app.store.AddLog(service.DeployLog{
		ClusterID: 1, AppName: "storefront", DeploymentName: "storefront", ContainerName: "api",
		Namespace: "shop", DeployType: "rollback", Status: "success", EnvironmentTierSnapshot: &prod,
	})
	if err != nil {
		t.Fatalf("seed successful rollback deployment: %v", err)
	}
	operator := seedUser(t, app.store, "operator-a", service.RoleOperator)
	viewer := seedUser(t, app.store, "viewer-a", service.RoleViewer)
	occurredAt := time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC)
	create := map[string]any{
		"deploy_log_id": log.ID, "occurred_at": occurredAt, "summary": "下单接口持续返回 5xx",
		"idempotency_key": "incident-api-001",
	}
	if response := do(t, engine, http.MethodPost, "/api/v1/delivery/incidents", create, viewer); response.Code != http.StatusForbidden {
		t.Fatalf("viewer created incident: %d %s", response.Code, response.Body.String())
	}
	created := do(t, engine, http.MethodPost, "/api/v1/delivery/incidents", create, operator)
	if created.Code != http.StatusCreated {
		t.Fatalf("operator could not create incident: %d %s", created.Code, created.Body.String())
	}
	var incident service.DeliveryIncident
	if err := json.Unmarshal(created.Body.Bytes(), &incident); err != nil {
		t.Fatal(err)
	}
	if incident.DeployLogID != log.ID || incident.AppName != log.AppName || incident.Status != "open" || len(incident.Events) != 1 {
		t.Fatalf("created incident has incorrect deployment association: %+v", incident)
	}
	replay := do(t, engine, http.MethodPost, "/api/v1/delivery/incidents", create, operator)
	var replayed service.DeliveryIncident
	if replay.Code != http.StatusCreated || json.Unmarshal(replay.Body.Bytes(), &replayed) != nil || replayed.ID != incident.ID {
		t.Fatalf("create replay did not preserve incident identity: %d %s", replay.Code, replay.Body.String())
	}
	interventionAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	intervention := map[string]any{
		"expected_version": incident.Version, "occurred_at": interventionAt,
		"detail": "开始回退上一稳定版本", "idempotency_key": "intervention-api-001",
	}
	interventionPath := "/api/v1/delivery/incidents/" + fmtInt64(incident.ID) + "/intervention"
	if response := do(t, engine, http.MethodPost, interventionPath, intervention, viewer); response.Code != http.StatusForbidden {
		t.Fatalf("viewer started incident intervention: %d %s", response.Code, response.Body.String())
	}
	started := do(t, engine, http.MethodPost, interventionPath, intervention, operator)
	if started.Code != http.StatusOK || json.Unmarshal(started.Body.Bytes(), &incident) != nil || incident.Version != 2 || incident.InterventionStartedAt == nil || len(incident.Events) != 2 {
		t.Fatalf("operator could not record intervention start: %d %s", started.Code, started.Body.String())
	}
	if incident.Events[1].EventType != "intervention_started" {
		t.Fatalf("intervention was not appended to timeline: %+v", incident.Events)
	}

	recoveredAt := time.Now().UTC().Truncate(time.Microsecond)
	recovery := map[string]any{
		"expected_version": incident.Version, "occurred_at": recoveredAt,
		"confirmation": "回归探针和一笔真实下单均成功", "idempotency_key": "recovery-api-001",
	}
	path := "/api/v1/delivery/incidents/" + fmtInt64(incident.ID) + "/recovery"
	if response := do(t, engine, http.MethodPost, path, recovery, viewer); response.Code != http.StatusForbidden {
		t.Fatalf("viewer confirmed recovery: %d %s", response.Code, response.Body.String())
	}
	confirmed := do(t, engine, http.MethodPost, path, recovery, operator)
	if confirmed.Code != http.StatusOK {
		t.Fatalf("operator could not confirm recovery: %d %s", confirmed.Code, confirmed.Body.String())
	}
	if err := json.Unmarshal(confirmed.Body.Bytes(), &incident); err != nil {
		t.Fatal(err)
	}
	if incident.Status != "recovered" || incident.Version != 3 || len(incident.Events) != 3 {
		t.Fatalf("recovery timeline is incomplete: %+v", incident)
	}
	causality := map[string]any{
		"expected_version": incident.Version,
		"status":           service.DeliveryIncidentCausedByDeployment,
		"reason":           "回退该版本后错误率恢复",
		"idempotency_key":  "causality-api-001",
	}
	causalityPath := "/api/v1/delivery/incidents/" + fmtInt64(incident.ID) + "/causality"
	if response := do(t, engine, http.MethodPost, causalityPath, causality, viewer); response.Code != http.StatusForbidden {
		t.Fatalf("viewer assessed incident causality: %d %s", response.Code, response.Body.String())
	}
	assessed := do(t, engine, http.MethodPost, causalityPath, causality, operator)
	if assessed.Code != http.StatusOK || json.Unmarshal(assessed.Body.Bytes(), &incident) != nil || incident.Version != 4 || incident.CausalityStatus != service.DeliveryIncidentCausedByDeployment || incident.CausalityRecordedBy != "operator-a" || len(incident.Events) != 4 {
		t.Fatalf("operator could not assess incident causality: %d %s", assessed.Code, assessed.Body.String())
	}
	replayedAssessment := do(t, engine, http.MethodPost, causalityPath, causality, operator)
	if replayedAssessment.Code != http.StatusOK || json.Unmarshal(replayedAssessment.Body.Bytes(), &incident) != nil || incident.Version != 4 || len(incident.Events) != 4 {
		t.Fatalf("causality idempotency replay changed incident: %d %s", replayedAssessment.Code, replayedAssessment.Body.String())
	}
	conflictingAssessment := map[string]any{
		"expected_version": 3, "status": service.DeliveryIncidentNotCausedByDeployment,
		"reason": "同一幂等键不得改写结论", "idempotency_key": "causality-api-001",
	}
	if response := do(t, engine, http.MethodPost, causalityPath, conflictingAssessment, operator); response.Code != http.StatusConflict {
		t.Fatalf("causality idempotency key accepted a different conclusion: %d %s", response.Code, response.Body.String())
	}
	remediation := map[string]any{
		"expected_version": incident.Version, "remediation_type": service.DeliveryIncidentRemediationRollback,
		"plan_status": service.DeliveryIncidentRemediationUnplanned, "remediation_deploy_log_id": remediationLog.ID,
		"reason": "回滚到上一稳定镜像后探针恢复", "idempotency_key": "remediation-api-001",
	}
	remediationPath := "/api/v1/delivery/incidents/" + fmtInt64(incident.ID) + "/remediation"
	if response := do(t, engine, http.MethodPost, remediationPath, remediation, viewer); response.Code != http.StatusForbidden {
		t.Fatalf("viewer classified incident remediation: %d %s", response.Code, response.Body.String())
	}
	recordedRemediation := do(t, engine, http.MethodPost, remediationPath, remediation, operator)
	if recordedRemediation.Code != http.StatusOK || json.Unmarshal(recordedRemediation.Body.Bytes(), &incident) != nil || incident.Version != 5 || incident.RemediationType != service.DeliveryIncidentRemediationRollback || incident.RemediationPlanStatus != service.DeliveryIncidentRemediationUnplanned || incident.RemediationDeployLogID == nil || *incident.RemediationDeployLogID != remediationLog.ID || incident.RemediationRecordedBy != "operator-a" || len(incident.Events) != 5 {
		t.Fatalf("operator could not record incident remediation: %d %s", recordedRemediation.Code, recordedRemediation.Body.String())
	}
	replayedRemediation := do(t, engine, http.MethodPost, remediationPath, remediation, operator)
	if replayedRemediation.Code != http.StatusOK || json.Unmarshal(replayedRemediation.Body.Bytes(), &incident) != nil || incident.Version != 5 || len(incident.Events) != 5 {
		t.Fatalf("remediation idempotency replay changed incident: %d %s", replayedRemediation.Code, replayedRemediation.Body.String())
	}
	rows := do(t, engine, http.MethodGet, "/api/v1/delivery/incidents?limit=25", nil, viewer)
	var listed []service.DeliveryIncident
	if rows.Code != http.StatusOK || json.Unmarshal(rows.Body.Bytes(), &listed) != nil || len(listed) != 1 || len(listed[0].Events) != 5 || listed[0].CausalityStatus != service.DeliveryIncidentCausedByDeployment || listed[0].RemediationType != service.DeliveryIncidentRemediationRollback {
		t.Fatalf("viewer could not read incident timeline: %d %s", rows.Code, rows.Body.String())
	}
}

func TestDeliveryIncidentAPIRejectsInvalidProductionLinkAndLimit(t *testing.T) {
	app, engine, _ := newTestApp(t)
	if response := do(t, engine, http.MethodGet, "/api/v1/delivery/incidents?limit=101", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("out-of-range limit accepted: %d %s", response.Code, response.Body.String())
	}
	if response := do(t, engine, http.MethodGet, "/api/v1/delivery/incidents", nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list accepted: %d %s", response.Code, response.Body.String())
	}
	unknownLog, err := app.store.AddLog(service.DeployLog{ClusterID: 99, AppName: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"deploy_log_id": unknownLog.ID, "occurred_at": time.Now().UTC(), "summary": "issue", "idempotency_key": "incident-invalid"}
	if response := do(t, engine, http.MethodPost, "/api/v1/delivery/incidents", body); response.Code != http.StatusBadRequest {
		t.Fatalf("missing production environment snapshot was accepted: %d %s", response.Code, response.Body.String())
	}
}
