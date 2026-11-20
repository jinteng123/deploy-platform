package service

import (
	"errors"
	"testing"
	"time"
)

func TestDeliveryIncidentLinksProductionDeploymentAndRecordsBusinessRecovery(t *testing.T) {
	now := time.Date(2026, 10, 14, 2, 0, 0, 123456000, time.UTC)
	prod := "production"
	nonProd := "non_production"
	store := NewStoreWithData(nil, nil, []DeployLog{
		{ID: 21, ClusterID: 4, AppName: "shop-api", ReleaseID: 7, EnvironmentTierSnapshot: &prod, Status: "success"},
		{ID: 22, ClusterID: 5, AppName: "test-api", EnvironmentTierSnapshot: &nonProd, Status: "success"},
	}, nil, nil)

	if _, err := store.CreateDeliveryIncident(22, now, "test environment issue", "operator", "incident-key-test"); !errors.Is(err, ErrValidation) {
		t.Fatalf("non-production deployment accepted: %v", err)
	}
	incident, err := store.CreateDeliveryIncident(21, now, "checkout errors after release", "operator", "incident-key-001")
	if err != nil {
		t.Fatal(err)
	}
	if incident.ID != 1 || incident.DeployLogID != 21 || incident.ReleaseID == nil || *incident.ReleaseID != 7 || incident.Status != "open" || len(incident.Events) != 1 {
		t.Fatalf("incident was not linked or recorded: %+v", incident)
	}
	if !incident.Events[0].OccurredAt.Equal(now) || incident.Events[0].RecordedBy != "operator" || incident.Events[0].EventType != "incident_recorded" {
		t.Fatalf("incident event lacks its stable source timestamps and actor: %+v", incident.Events[0])
	}

	// Replaying the same create key and payload returns the same incident.
	replayed, err := store.CreateDeliveryIncident(21, now, "checkout errors after release", "operator", "incident-key-001")
	if err != nil || replayed.ID != incident.ID {
		t.Fatalf("idempotent create replay = %+v, %v", replayed, err)
	}
	if _, err := store.CreateDeliveryIncident(21, now, "different payload", "operator", "incident-key-001"); !errors.Is(err, ErrConflict) {
		t.Fatalf("idempotency key reused for a different event: %v", err)
	}

	recoveredAt := now.Add(37 * time.Minute)
	recovered, changed, err := store.ConfirmDeliveryIncidentRecovery(incident.ID, 1, recoveredAt, "核心下单链路探针与人工交易检查恢复", "operator", "recovery-key-001")
	if err != nil || !changed {
		t.Fatalf("recovery confirmation failed: changed=%t incident=%+v err=%v", changed, recovered, err)
	}
	if recovered.Status != "recovered" || recovered.Version != 2 || recovered.RecoveryOccurredAt == nil || !recovered.RecoveryOccurredAt.Equal(recoveredAt) || len(recovered.Events) != 2 {
		t.Fatalf("recovery confirmation was not captured: %+v", recovered)
	}
	// The same request remains safe after the status version has advanced.
	replayed, changed, err = store.ConfirmDeliveryIncidentRecovery(incident.ID, 1, recoveredAt, "核心下单链路探针与人工交易检查恢复", "operator", "recovery-key-001")
	if err != nil || changed || replayed.Version != 2 {
		t.Fatalf("idempotent recovery replay = changed %t, %+v, %v", changed, replayed, err)
	}
	if _, _, err := store.ConfirmDeliveryIncidentRecovery(incident.ID, 1, recoveredAt, "different recovery", "operator", "recovery-key-002"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale or second recovery was accepted: %v", err)
	}

	rows, err := store.DeliveryIncidents(10)
	if err != nil || len(rows) != 1 || len(rows[0].Events) != 2 || rows[0].RecoveryRecordedBy != "operator" {
		t.Fatalf("incident timeline read = %+v, %v", rows, err)
	}
}

func TestDeliveryIncidentInterventionIsAppendOnlyVersionedAndIdempotent(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	prod := "production"
	store := NewStoreWithData(nil, nil, []DeployLog{{
		ID: 31, ClusterID: 4, AppName: "shop-api", ReleaseID: 8,
		EnvironmentTierSnapshot: &prod, Status: "success",
	}}, nil, nil)
	incident, err := store.CreateDeliveryIncident(31, now.Add(-10*time.Minute), "checkout errors", "operator", "incident-intervention-create")
	if err != nil {
		t.Fatal(err)
	}

	started, changed, err := store.StartDeliveryIncidentIntervention(incident.ID, incident.Version, now, "开始回退上一稳定版本", "operator", "incident-intervention-start")
	if err != nil || !changed || started.Version != 2 || started.InterventionStartedAt == nil || !started.InterventionStartedAt.Equal(now) || started.InterventionRecordedBy != "operator" || len(started.Events) != 2 {
		t.Fatalf("intervention start was not stored: changed=%t incident=%+v err=%v", changed, started, err)
	}
	if started.Events[1].EventType != "intervention_started" || started.Events[1].Detail != "开始回退上一稳定版本" {
		t.Fatalf("intervention event missing from history: %+v", started.Events)
	}
	replayed, changed, err := store.StartDeliveryIncidentIntervention(incident.ID, incident.Version, now, "开始回退上一稳定版本", "operator", "incident-intervention-start")
	if err != nil || changed || replayed.Version != 2 || len(replayed.Events) != 2 {
		t.Fatalf("intervention replay changed state: changed=%t incident=%+v err=%v", changed, replayed, err)
	}
	if _, _, err := store.ConfirmDeliveryIncidentRecovery(incident.ID, started.Version, now.Add(-time.Minute), "探针恢复", "operator", "incident-recovery-too-early"); !errors.Is(err, ErrValidation) {
		t.Fatalf("recovery before intervention was accepted: %v", err)
	}

	recovered, changed, err := store.ConfirmDeliveryIncidentRecovery(incident.ID, started.Version, now.Add(time.Minute), "探针和人工交易验证均恢复", "operator", "incident-intervention-recovery")
	if err != nil || !changed || recovered.Version != 3 || len(recovered.Events) != 3 {
		t.Fatalf("recovery did not follow intervention: changed=%t incident=%+v err=%v", changed, recovered, err)
	}
	if _, _, err := store.StartDeliveryIncidentIntervention(incident.ID, recovered.Version, now.Add(2*time.Minute), "再次处理", "operator", "incident-intervention-after-recovery"); !errors.Is(err, ErrConflict) {
		t.Fatalf("recovered incident accepted a new intervention: %v", err)
	}
}

func TestDeliveryIncidentCausalityIsExplicitAppendOnlyVersionedAndIdempotent(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	prod := "production"
	store := NewStoreWithData(nil, nil, []DeployLog{{
		ID: 41, ClusterID: 4, AppName: "shop-api", EnvironmentTierSnapshot: &prod, Status: "success",
	}}, nil, nil)
	incident, err := store.CreateDeliveryIncident(41, now.Add(-time.Minute), "checkout errors", "operator", "incident-cause-create")
	if err != nil || incident.CausalityStatus != DeliveryIncidentCausalityUnknown {
		t.Fatalf("new incident should keep causality unknown: %+v err=%v", incident, err)
	}

	assessed, changed, err := store.AssessDeliveryIncidentCausality(
		incident.ID, incident.Version, DeliveryIncidentCausedByDeployment,
		"回退该部署后错误率恢复", "operator", "incident-cause-first",
	)
	if err != nil || !changed || assessed.Version != 2 || assessed.CausalityStatus != DeliveryIncidentCausedByDeployment || assessed.CausalityRecordedBy != "operator" || len(assessed.Events) != 2 {
		t.Fatalf("causality assessment was not recorded: changed=%t incident=%+v err=%v", changed, assessed, err)
	}
	if assessed.Events[1].EventType != "causality_assessed" || assessed.Events[1].CausalityStatus != DeliveryIncidentCausedByDeployment {
		t.Fatalf("assessment missing immutable event fields: %+v", assessed.Events[1])
	}

	replayed, changed, err := store.AssessDeliveryIncidentCausality(
		incident.ID, incident.Version, DeliveryIncidentCausedByDeployment,
		"回退该部署后错误率恢复", "operator", "incident-cause-first",
	)
	if err != nil || changed || replayed.Version != 2 || len(replayed.Events) != 2 {
		t.Fatalf("causality idempotency replay changed state: changed=%t incident=%+v err=%v", changed, replayed, err)
	}
	if _, _, err := store.AssessDeliveryIncidentCausality(incident.ID, 1, DeliveryIncidentNotCausedByDeployment, "证据不足", "operator", "incident-cause-stale"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale causality version was accepted: %v", err)
	}
	if _, _, err := store.AssessDeliveryIncidentCausality(incident.ID, 2, "unknown", "未选择结果", "operator", "incident-cause-invalid"); !errors.Is(err, ErrValidation) {
		t.Fatalf("unknown cannot be submitted as an assessment: %v", err)
	}

	corrected, changed, err := store.AssessDeliveryIncidentCausality(
		incident.ID, assessed.Version, DeliveryIncidentNotCausedByDeployment,
		"核对后发现故障早于该版本发布", "admin", "incident-cause-correction",
	)
	if err != nil || !changed || corrected.Version != 3 || corrected.CausalityStatus != DeliveryIncidentNotCausedByDeployment || corrected.CausalityRecordedBy != "admin" || len(corrected.Events) != 3 {
		t.Fatalf("corrected assessment did not append a new revision: changed=%t incident=%+v err=%v", changed, corrected, err)
	}
	if corrected.CausalityReason != "核对后发现故障早于该版本发布" || corrected.CausalityRecordedAt == nil {
		t.Fatalf("latest assessment projection is incomplete: %+v", corrected)
	}
}

func TestDeliveryIncidentRemediationRequiresScopedDeploymentAndAppendsRevisions(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	prod, nonProd := "production", "non_production"
	store := NewStoreWithData(nil, nil, []DeployLog{
		{ID: 51, ClusterID: 4, AppName: "shop-api", EnvironmentTierSnapshot: &prod, Status: "success", DeployType: "image"},
		{ID: 52, ClusterID: 4, AppName: "shop-api", EnvironmentTierSnapshot: &prod, Status: "success", DeployType: "image"},
		{ID: 53, ClusterID: 4, AppName: "shop-api", EnvironmentTierSnapshot: &prod, Status: "success", DeployType: "rollback"},
		{ID: 54, ClusterID: 5, AppName: "shop-api", EnvironmentTierSnapshot: &prod, Status: "success", DeployType: "image"},
		{ID: 55, ClusterID: 4, AppName: "other-api", EnvironmentTierSnapshot: &prod, Status: "success", DeployType: "image"},
		{ID: 56, ClusterID: 4, AppName: "shop-api", EnvironmentTierSnapshot: &nonProd, Status: "success", DeployType: "image"},
		{ID: 57, ClusterID: 4, AppName: "shop-api", EnvironmentTierSnapshot: &prod, Status: "failed", DeployType: "image"},
		{ID: 58, ClusterID: 4, AppName: "shop-api", EnvironmentTierSnapshot: &prod, Status: "success", DeployType: "image"},
	}, nil, nil)
	incident, err := store.CreateDeliveryIncident(51, now.Add(-time.Minute), "checkout unavailable", "operator", "incident-remediation-create")
	if err != nil || incident.RemediationType != DeliveryIncidentRemediationUnknown || incident.RemediationPlanStatus != DeliveryIncidentRemediationUnknown {
		t.Fatalf("new incident remediation should remain unknown: incident=%+v err=%v", incident, err)
	}

	if _, _, err := store.RecordDeliveryIncidentRemediation(incident.ID, incident.Version, DeliveryIncidentRemediationHotfix, DeliveryIncidentRemediationPlanned, 54, "wrong cluster", "operator", "remed-wrong-cluster"); !errors.Is(err, ErrValidation) {
		t.Fatalf("remediation deployment from another cluster accepted: %v", err)
	}
	if _, _, err := store.RecordDeliveryIncidentRemediation(incident.ID, incident.Version, DeliveryIncidentRemediationHotfix, DeliveryIncidentRemediationPlanned, 51, "original deployment", "operator", "remed-original"); !errors.Is(err, ErrValidation) {
		t.Fatalf("original incident deployment was accepted as its own remediation: %v", err)
	}
	if _, _, err := store.RecordDeliveryIncidentRemediation(incident.ID, incident.Version, DeliveryIncidentRemediationConfig, DeliveryIncidentRemediationUnplanned, 55, "wrong app", "operator", "remed-wrong-app"); !errors.Is(err, ErrValidation) {
		t.Fatalf("remediation deployment from another app accepted: %v", err)
	}
	if _, _, err := store.RecordDeliveryIncidentRemediation(incident.ID, incident.Version, DeliveryIncidentRemediationHotfix, DeliveryIncidentRemediationPlanned, 56, "test environment", "operator", "remed-nonprod"); !errors.Is(err, ErrValidation) {
		t.Fatalf("non-production remediation deployment accepted: %v", err)
	}
	if _, _, err := store.RecordDeliveryIncidentRemediation(incident.ID, incident.Version, DeliveryIncidentRemediationHotfix, DeliveryIncidentRemediationPlanned, 57, "failed deploy", "operator", "remed-failed"); !errors.Is(err, ErrValidation) {
		t.Fatalf("failed remediation deployment accepted: %v", err)
	}
	if _, _, err := store.RecordDeliveryIncidentRemediation(incident.ID, incident.Version, DeliveryIncidentRemediationRollback, DeliveryIncidentRemediationPlanned, 58, "not a rollback", "operator", "remed-fake-rollback"); !errors.Is(err, ErrValidation) {
		t.Fatalf("ordinary deployment was accepted as a rollback: %v", err)
	}

	classified, changed, err := store.RecordDeliveryIncidentRemediation(
		incident.ID, incident.Version, DeliveryIncidentRemediationHotfix, DeliveryIncidentRemediationUnplanned, 52,
		"回滚不可用，热修复版本通过生产验证", "operator", "incident-remediation-first",
	)
	if err != nil || !changed || classified.Version != 2 || classified.RemediationType != DeliveryIncidentRemediationHotfix || classified.RemediationPlanStatus != DeliveryIncidentRemediationUnplanned || classified.RemediationDeployLogID == nil || *classified.RemediationDeployLogID != 52 || classified.RemediationRecordedBy != "operator" || len(classified.Events) != 2 {
		t.Fatalf("remediation classification was not recorded: changed=%t incident=%+v err=%v", changed, classified, err)
	}
	if event := classified.Events[1]; event.EventType != "remediation_classified" || event.RemediationDeployLogID == nil || *event.RemediationDeployLogID != 52 {
		t.Fatalf("remediation event lost its deployment link: %+v", event)
	}
	replayed, changed, err := store.RecordDeliveryIncidentRemediation(
		incident.ID, incident.Version, DeliveryIncidentRemediationHotfix, DeliveryIncidentRemediationUnplanned, 52,
		"回滚不可用，热修复版本通过生产验证", "operator", "incident-remediation-first",
	)
	if err != nil || changed || replayed.Version != 2 || len(replayed.Events) != 2 {
		t.Fatalf("remediation idempotency replay changed state: changed=%t incident=%+v err=%v", changed, replayed, err)
	}
	if _, _, err := store.RecordDeliveryIncidentRemediation(incident.ID, 1, DeliveryIncidentRemediationNoAction, DeliveryIncidentRemediationNotApplicable, 0, "stale edit", "operator", "incident-remediation-stale"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale remediation version was accepted: %v", err)
	}
	if _, _, err := store.RecordDeliveryIncidentRemediation(incident.ID, 1, DeliveryIncidentRemediationHotfix, DeliveryIncidentRemediationPlanned, 52, "different payload", "operator", "incident-remediation-first"); !errors.Is(err, ErrConflict) {
		t.Fatalf("idempotency key was reused with a different remediation: %v", err)
	}

	corrected, changed, err := store.RecordDeliveryIncidentRemediation(
		incident.ID, classified.Version, DeliveryIncidentRemediationNoAction, DeliveryIncidentRemediationNotApplicable, 0,
		"复测确认无需进一步操作", "admin", "incident-remediation-correction",
	)
	if err != nil || !changed || corrected.Version != 3 || corrected.RemediationType != DeliveryIncidentRemediationNoAction || corrected.RemediationPlanStatus != DeliveryIncidentRemediationNotApplicable || corrected.RemediationDeployLogID != nil || corrected.RemediationRecordedBy != "admin" || len(corrected.Events) != 3 {
		t.Fatalf("corrected remediation did not append a new revision: changed=%t incident=%+v err=%v", changed, corrected, err)
	}
	rows, err := store.DeliveryIncidents(10)
	if err != nil || len(rows) != 1 || rows[0].RemediationType != DeliveryIncidentRemediationNoAction || rows[0].RemediationReason != "复测确认无需进一步操作" || len(rows[0].Events) != 3 {
		t.Fatalf("latest remediation projection is incomplete: incidents=%+v err=%v", rows, err)
	}
}
