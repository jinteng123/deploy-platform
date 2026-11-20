package repository

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDeliveryIncidentRecoverySamplesQueryFullFilteredWindow(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := &MySQL{db: db}
	windowStart := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	occurredAt := windowStart.Add(time.Hour)
	interventionAt := occurredAt.Add(10 * time.Minute)
	recoveryAt := interventionAt.Add(20 * time.Minute)
	query := `(?s)SELECT incident\.status.*LEFT JOIN delivery_incident_events causality.*event\.event_type='causality_assessed'.*LEFT JOIN delivery_incident_events remediation.*event\.event_type='remediation_classified'.*WHERE incident\.occurred_at>=\? AND incident\.app_name=\? AND incident\.cluster_id=\? ORDER BY incident\.occurred_at,incident\.id`
	mock.ExpectQuery(query).WithArgs(windowStart, "shop", 2).WillReturnRows(
		sqlmock.NewRows([]string{"status", "occurred_at", "intervention_started_at", "recovery_occurred_at", "causality_status", "remediation_type", "remediation_plan_status", "remediation_deploy_log_id"}).
			AddRow("recovered", occurredAt, interventionAt, recoveryAt, "caused_by_deployment", "rollback", "unplanned", 99).
			AddRow("open", occurredAt.Add(time.Hour), nil, nil, "unknown", "unknown", "unknown", nil),
	)

	samples, err := repo.DeliveryIncidentRecoverySamples("shop", 2, windowStart)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 || samples[0].Status != "recovered" || samples[0].InterventionStartedAt == nil || !samples[0].InterventionStartedAt.Equal(interventionAt) || samples[0].RecoveryOccurredAt == nil || !samples[0].RecoveryOccurredAt.Equal(recoveryAt) || samples[0].CausalityStatus != "caused_by_deployment" || samples[0].RemediationType != "rollback" || samples[0].RemediationPlanStatus != "unplanned" || samples[0].RemediationDeployLogID == nil || *samples[0].RemediationDeployLogID != 99 {
		t.Fatalf("unexpected recovery samples: %+v", samples)
	}
	if samples[1].Status != "open" || samples[1].InterventionStartedAt != nil || samples[1].RecoveryOccurredAt != nil || samples[1].CausalityStatus != "unknown" || samples[1].RemediationType != "unknown" || samples[1].RemediationPlanStatus != "unknown" || samples[1].RemediationDeployLogID != nil {
		t.Fatalf("nullable recovery fields were not preserved: %+v", samples[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
