package repository

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"deploy-platform/backend-go/internal/service"
	mysqlDriver "github.com/go-sql-driver/mysql"
)

func scanDeliveryIncident(row interface{ Scan(...any) error }) (service.DeliveryIncident, error) {
	var incident service.DeliveryIncident
	var releaseID sql.NullInt64
	var recoveryOccurred, recoveryRecorded sql.NullTime
	var recoveryBy, recoveryDetail sql.NullString
	var interventionStarted, interventionRecorded sql.NullTime
	var interventionBy sql.NullString
	err := row.Scan(&incident.ID, &incident.DeployLogID, &releaseID, &incident.ClusterID,
		&incident.AppName, &incident.Summary, &incident.Status, &incident.OccurredAt,
		&incident.RecordedAt, &incident.RecordedBy, &incident.Version,
		&recoveryOccurred, &recoveryRecorded, &recoveryBy, &recoveryDetail,
		&interventionStarted, &interventionRecorded, &interventionBy)
	if err != nil {
		return service.DeliveryIncident{}, err
	}
	if releaseID.Valid {
		incident.ReleaseID = &releaseID.Int64
	}
	if recoveryOccurred.Valid {
		incident.RecoveryOccurredAt = &recoveryOccurred.Time
	}
	if recoveryRecorded.Valid {
		incident.RecoveryRecordedAt = &recoveryRecorded.Time
	}
	if recoveryBy.Valid {
		incident.RecoveryRecordedBy = recoveryBy.String
	}
	if recoveryDetail.Valid {
		incident.RecoveryConfirmation = recoveryDetail.String
	}
	if interventionStarted.Valid {
		incident.InterventionStartedAt = &interventionStarted.Time
	}
	if interventionRecorded.Valid {
		incident.InterventionRecordedAt = &interventionRecorded.Time
	}
	if interventionBy.Valid {
		incident.InterventionRecordedBy = interventionBy.String
	}
	return incident, nil
}

const incidentColumns = `id,deploy_log_id,release_id,cluster_id,app_name,summary,status,occurred_at,recorded_at,recorded_by,version,recovery_occurred_at,recovery_recorded_at,recovery_recorded_by,recovery_confirmation,intervention_started_at,intervention_recorded_at,intervention_recorded_by`

func scanDeliveryIncidentEvent(row interface{ Scan(...any) error }) (service.DeliveryIncidentEvent, error) {
	var event service.DeliveryIncidentEvent
	var causalityStatus sql.NullString
	var remediationType, remediationPlanStatus sql.NullString
	var remediationDeployLogID sql.NullInt64
	err := row.Scan(&event.ID, &event.IncidentID, &event.IdempotencyKey, &event.EventType,
		&event.DeployLogID, &event.OccurredAt, &event.RecordedAt, &event.RecordedBy, &event.Detail, &causalityStatus,
		&remediationType, &remediationDeployLogID, &remediationPlanStatus)
	if causalityStatus.Valid {
		event.CausalityStatus = causalityStatus.String
	}
	if remediationType.Valid {
		event.RemediationType = remediationType.String
	}
	if remediationDeployLogID.Valid {
		event.RemediationDeployLogID = &remediationDeployLogID.Int64
	}
	if remediationPlanStatus.Valid {
		event.RemediationPlanStatus = remediationPlanStatus.String
	}
	return event, err
}

const incidentEventColumns = `id,incident_id,event_key,event_type,deploy_log_id,occurred_at,recorded_at,recorded_by,detail,causality_status,remediation_type,remediation_deploy_log_id,remediation_plan_status`

func (m *MySQL) DeliveryIncidentRecoverySamples(appName string, clusterID int, windowStart time.Time) ([]service.DeliveryIncidentRecoverySample, error) {
	// The current assessments live in the append-only event ledger, not on the
	// incident row. Select the latest revision of each kind for every incident.
	query := `SELECT incident.status,incident.occurred_at,incident.intervention_started_at,incident.recovery_occurred_at,
		COALESCE(causality.causality_status,'unknown'),COALESCE(remediation.remediation_type,'unknown'),
		COALESCE(remediation.remediation_plan_status,'unknown'),remediation.remediation_deploy_log_id
		FROM delivery_incidents incident
		LEFT JOIN delivery_incident_events causality ON causality.id=(
			SELECT event.id FROM delivery_incident_events event
			WHERE event.incident_id=incident.id AND event.event_type='causality_assessed'
			ORDER BY event.id DESC LIMIT 1)
		LEFT JOIN delivery_incident_events remediation ON remediation.id=(
			SELECT event.id FROM delivery_incident_events event
			WHERE event.incident_id=incident.id AND event.event_type='remediation_classified'
			ORDER BY event.id DESC LIMIT 1)
		WHERE incident.occurred_at>=?`
	args := []any{windowStart}
	if appName != "" {
		query += ` AND incident.app_name=?`
		args = append(args, appName)
	}
	if clusterID > 0 {
		query += ` AND incident.cluster_id=?`
		args = append(args, clusterID)
	}
	query += ` ORDER BY incident.occurred_at,incident.id`
	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	samples := make([]service.DeliveryIncidentRecoverySample, 0)
	for rows.Next() {
		var sample service.DeliveryIncidentRecoverySample
		var interventionStarted, recoveryOccurred sql.NullTime
		var remediationDeployLogID sql.NullInt64
		if err := rows.Scan(
			&sample.Status, &sample.OccurredAt, &interventionStarted, &recoveryOccurred,
			&sample.CausalityStatus, &sample.RemediationType, &sample.RemediationPlanStatus, &remediationDeployLogID,
		); err != nil {
			return nil, err
		}
		if interventionStarted.Valid {
			sample.InterventionStartedAt = &interventionStarted.Time
		}
		if recoveryOccurred.Valid {
			sample.RecoveryOccurredAt = &recoveryOccurred.Time
		}
		if remediationDeployLogID.Valid {
			id := remediationDeployLogID.Int64
			sample.RemediationDeployLogID = &id
		}
		samples = append(samples, sample)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return samples, nil
}

func (m *MySQL) DeliveryIncidents(limit int) ([]service.DeliveryIncident, []service.DeliveryIncidentEvent, error) {
	if limit < 1 || limit > service.MaxDeliveryIncidentLimit {
		limit = service.DefaultDeliveryIncidentLimit
	}
	rows, err := m.db.Query(`SELECT `+incidentColumns+` FROM delivery_incidents ORDER BY occurred_at DESC,id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, nil, err
	}
	incidents := make([]service.DeliveryIncident, 0, limit)
	ids := make([]int64, 0, limit)
	for rows.Next() {
		incident, scanErr := scanDeliveryIncident(rows)
		if scanErr != nil {
			rows.Close()
			return nil, nil, scanErr
		}
		incidents = append(incidents, incident)
		ids = append(ids, incident.ID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	if len(ids) == 0 {
		return incidents, []service.DeliveryIncidentEvent{}, nil
	}
	marks := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	eventRows, err := m.db.Query(`SELECT `+incidentEventColumns+` FROM delivery_incident_events WHERE incident_id IN (`+marks+`) ORDER BY incident_id,id`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer eventRows.Close()
	events := make([]service.DeliveryIncidentEvent, 0, len(ids)*2)
	for eventRows.Next() {
		event, scanErr := scanDeliveryIncidentEvent(eventRows)
		if scanErr != nil {
			return nil, nil, scanErr
		}
		events = append(events, event)
	}
	return incidents, events, eventRows.Err()
}

func (m *MySQL) FindDeliveryIncident(id int64) (service.DeliveryIncident, []service.DeliveryIncidentEvent, error) {
	incident, err := scanDeliveryIncident(m.db.QueryRow(`SELECT `+incidentColumns+` FROM delivery_incidents WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return service.DeliveryIncident{}, nil, service.ErrNotFound
	}
	if err != nil {
		return service.DeliveryIncident{}, nil, err
	}
	rows, err := m.db.Query(`SELECT `+incidentEventColumns+` FROM delivery_incident_events WHERE incident_id=? ORDER BY id`, id)
	if err != nil {
		return service.DeliveryIncident{}, nil, err
	}
	defer rows.Close()
	events := make([]service.DeliveryIncidentEvent, 0, 2)
	for rows.Next() {
		event, scanErr := scanDeliveryIncidentEvent(rows)
		if scanErr != nil {
			return service.DeliveryIncident{}, nil, scanErr
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return service.DeliveryIncident{}, nil, err
	}
	return incident, events, nil
}

func (m *MySQL) CreateDeliveryIncident(incident service.DeliveryIncident, event service.DeliveryIncidentEvent) (service.DeliveryIncident, service.DeliveryIncidentEvent, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return incident, event, err
	}
	defer tx.Rollback()
	var releaseID any
	if incident.ReleaseID != nil {
		releaseID = *incident.ReleaseID
	}
	result, err := tx.Exec(`INSERT INTO delivery_incidents(deploy_log_id,release_id,cluster_id,app_name,summary,status,occurred_at,recorded_at,recorded_by,version) VALUES(?,?,?,?,?,'open',?,?,?,1)`, incident.DeployLogID, releaseID, incident.ClusterID, incident.AppName, incident.Summary, incident.OccurredAt, incident.RecordedAt, incident.RecordedBy)
	if err != nil {
		if isMySQLDuplicate(err) {
			return m.deliveryIncidentByEventKey(event.IdempotencyKey, incident, event)
		}
		return incident, event, err
	}
	incident.ID, err = result.LastInsertId()
	if err != nil {
		return incident, event, err
	}
	event.IncidentID = incident.ID
	if err := insertDeliveryIncidentEvent(tx, &event); err != nil {
		if isMySQLDuplicate(err) {
			return m.deliveryIncidentByEventKey(event.IdempotencyKey, incident, event)
		}
		return incident, event, err
	}
	if err := tx.Commit(); err != nil {
		return incident, event, err
	}
	return incident, event, nil
}

func (m *MySQL) ConfirmDeliveryIncidentRecovery(id, expectedVersion int64, event service.DeliveryIncidentEvent) (service.DeliveryIncident, service.DeliveryIncidentEvent, bool, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return service.DeliveryIncident{}, event, false, err
	}
	defer tx.Rollback()
	incident, err := scanDeliveryIncident(tx.QueryRow(`SELECT `+incidentColumns+` FROM delivery_incidents WHERE id=? FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return service.DeliveryIncident{}, event, false, service.ErrNotFound
	}
	if err != nil {
		return incident, event, false, err
	}
	if existing, lookupErr := scanDeliveryIncidentEvent(tx.QueryRow(`SELECT `+incidentEventColumns+` FROM delivery_incident_events WHERE event_key=?`, event.IdempotencyKey)); lookupErr == nil {
		if existing.IncidentID != id || !sameIncidentEvent(existing, event) {
			return incident, event, false, service.ErrConflict
		}
		if err := tx.Commit(); err != nil {
			return incident, event, false, err
		}
		return incident, existing, false, nil
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		return incident, event, false, lookupErr
	}
	if incident.Version != expectedVersion || incident.Status != "open" {
		return incident, event, false, service.ErrConflict
	}
	if occurredAt := incident.InterventionStartedAt; occurredAt != nil && event.OccurredAt.Before(*occurredAt) {
		return incident, event, false, service.ErrValidation
	}
	if _, err := tx.Exec(`UPDATE delivery_incidents SET status='recovered',version=version+1,recovery_occurred_at=?,recovery_recorded_at=?,recovery_recorded_by=?,recovery_confirmation=? WHERE id=? AND version=? AND status='open'`, event.OccurredAt, event.RecordedAt, event.RecordedBy, event.Detail, id, expectedVersion); err != nil {
		return incident, event, false, err
	}
	if err := insertDeliveryIncidentEvent(tx, &event); err != nil {
		return incident, event, false, err
	}
	if err := tx.Commit(); err != nil {
		return incident, event, false, err
	}
	incident.Status = "recovered"
	incident.Version++
	incident.RecoveryOccurredAt = &event.OccurredAt
	incident.RecoveryRecordedAt = &event.RecordedAt
	incident.RecoveryRecordedBy = event.RecordedBy
	incident.RecoveryConfirmation = event.Detail
	return incident, event, true, nil
}

func (m *MySQL) StartDeliveryIncidentIntervention(id, expectedVersion int64, event service.DeliveryIncidentEvent) (service.DeliveryIncident, service.DeliveryIncidentEvent, bool, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return service.DeliveryIncident{}, event, false, err
	}
	defer tx.Rollback()
	incident, err := scanDeliveryIncident(tx.QueryRow(`SELECT `+incidentColumns+` FROM delivery_incidents WHERE id=? FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return service.DeliveryIncident{}, event, false, service.ErrNotFound
	}
	if err != nil {
		return service.DeliveryIncident{}, event, false, err
	}
	if existing, lookupErr := scanDeliveryIncidentEvent(tx.QueryRow(`SELECT `+incidentEventColumns+` FROM delivery_incident_events WHERE event_key=?`, event.IdempotencyKey)); lookupErr == nil {
		if existing.IncidentID != id || !sameIncidentEvent(existing, event) {
			return incident, event, false, service.ErrConflict
		}
		if err := tx.Commit(); err != nil {
			return incident, event, false, err
		}
		return incident, existing, false, nil
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		return incident, event, false, lookupErr
	}
	if incident.Version != expectedVersion || incident.Status != "open" || incident.InterventionStartedAt != nil {
		return incident, event, false, service.ErrConflict
	}
	if event.OccurredAt.Before(incident.OccurredAt) {
		return incident, event, false, service.ErrValidation
	}
	result, err := tx.Exec(`UPDATE delivery_incidents SET version=version+1,intervention_started_at=?,intervention_recorded_at=?,intervention_recorded_by=? WHERE id=? AND version=? AND status='open' AND intervention_started_at IS NULL`, event.OccurredAt, event.RecordedAt, event.RecordedBy, id, expectedVersion)
	if err != nil {
		return incident, event, false, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return incident, event, false, err
	} else if affected != 1 {
		return incident, event, false, service.ErrConflict
	}
	if err := insertDeliveryIncidentEvent(tx, &event); err != nil {
		return incident, event, false, err
	}
	if err := tx.Commit(); err != nil {
		return incident, event, false, err
	}
	incident.Version++
	incident.InterventionStartedAt = &event.OccurredAt
	incident.InterventionRecordedAt = &event.RecordedAt
	incident.InterventionRecordedBy = event.RecordedBy
	return incident, event, true, nil
}

func (m *MySQL) AssessDeliveryIncidentCausality(id, expectedVersion int64, event service.DeliveryIncidentEvent) (service.DeliveryIncident, service.DeliveryIncidentEvent, bool, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return service.DeliveryIncident{}, event, false, err
	}
	defer tx.Rollback()
	incident, err := scanDeliveryIncident(tx.QueryRow(`SELECT `+incidentColumns+` FROM delivery_incidents WHERE id=? FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return service.DeliveryIncident{}, event, false, service.ErrNotFound
	}
	if err != nil {
		return incident, event, false, err
	}
	if existing, lookupErr := scanDeliveryIncidentEvent(tx.QueryRow(`SELECT `+incidentEventColumns+` FROM delivery_incident_events WHERE event_key=?`, event.IdempotencyKey)); lookupErr == nil {
		if existing.IncidentID != id || !sameDeliveryIncidentCausalityEvent(existing, event) {
			return incident, event, false, service.ErrConflict
		}
		if err := tx.Commit(); err != nil {
			return incident, event, false, err
		}
		return incident, existing, false, nil
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		return incident, event, false, lookupErr
	}
	if incident.Version != expectedVersion || incident.DeployLogID != event.DeployLogID {
		return incident, event, false, service.ErrConflict
	}
	result, err := tx.Exec(`UPDATE delivery_incidents SET version=version+1 WHERE id=? AND version=?`, id, expectedVersion)
	if err != nil {
		return incident, event, false, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return incident, event, false, err
	} else if affected != 1 {
		return incident, event, false, service.ErrConflict
	}
	if err := insertDeliveryIncidentEvent(tx, &event); err != nil {
		return incident, event, false, err
	}
	if err := tx.Commit(); err != nil {
		return incident, event, false, err
	}
	incident.Version++
	return incident, event, true, nil
}

func (m *MySQL) RecordDeliveryIncidentRemediation(id, expectedVersion int64, event service.DeliveryIncidentEvent) (service.DeliveryIncident, service.DeliveryIncidentEvent, bool, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return service.DeliveryIncident{}, event, false, err
	}
	defer tx.Rollback()
	incident, err := scanDeliveryIncident(tx.QueryRow(`SELECT `+incidentColumns+` FROM delivery_incidents WHERE id=? FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return service.DeliveryIncident{}, event, false, service.ErrNotFound
	}
	if err != nil {
		return incident, event, false, err
	}
	if existing, lookupErr := scanDeliveryIncidentEvent(tx.QueryRow(`SELECT `+incidentEventColumns+` FROM delivery_incident_events WHERE event_key=?`, event.IdempotencyKey)); lookupErr == nil {
		if existing.IncidentID != id || !sameDeliveryIncidentRemediationEvent(existing, event) {
			return incident, event, false, service.ErrConflict
		}
		if err := tx.Commit(); err != nil {
			return incident, event, false, err
		}
		return incident, existing, false, nil
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		return incident, event, false, lookupErr
	}
	if incident.Version != expectedVersion || incident.DeployLogID != event.DeployLogID {
		return incident, event, false, service.ErrConflict
	}
	if event.RemediationDeployLogID != nil {
		var remediationClusterID int
		var remediationApp, remediationStatus, remediationType string
		var remediationTier sql.NullString
		if err := tx.QueryRow(`SELECT cluster_id,app_name,status,deploy_type,environment_tier_snapshot FROM deploy_logs WHERE id=? FOR SHARE`, *event.RemediationDeployLogID).
			Scan(&remediationClusterID, &remediationApp, &remediationStatus, &remediationType, &remediationTier); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return incident, event, false, service.ErrNotFound
			}
			return incident, event, false, err
		}
		if !remediationTier.Valid || remediationTier.String != "production" || remediationStatus != "success" ||
			remediationClusterID != incident.ClusterID || remediationApp != incident.AppName ||
			*event.RemediationDeployLogID == incident.DeployLogID {
			return incident, event, false, service.NewValidationError("修复部署必须是不同于原故障部署的同一生产应用成功部署")
		}
		if event.RemediationType == service.DeliveryIncidentRemediationRollback && remediationType != "rollback" {
			return incident, event, false, service.NewValidationError("回滚处置必须关联回滚部署记录")
		}
	}
	result, err := tx.Exec(`UPDATE delivery_incidents SET version=version+1 WHERE id=? AND version=?`, id, expectedVersion)
	if err != nil {
		return incident, event, false, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return incident, event, false, err
	} else if affected != 1 {
		return incident, event, false, service.ErrConflict
	}
	if err := insertDeliveryIncidentEvent(tx, &event); err != nil {
		return incident, event, false, err
	}
	if err := tx.Commit(); err != nil {
		return incident, event, false, err
	}
	incident.Version++
	return incident, event, true, nil
}

func insertDeliveryIncidentEvent(tx *sql.Tx, event *service.DeliveryIncidentEvent) error {
	var causalityStatus any
	if event.CausalityStatus != "" {
		causalityStatus = event.CausalityStatus
	}
	var remediationType, remediationDeployLogID, remediationPlanStatus any
	if event.RemediationType != "" {
		remediationType = event.RemediationType
	}
	if event.RemediationDeployLogID != nil {
		remediationDeployLogID = *event.RemediationDeployLogID
	}
	if event.RemediationPlanStatus != "" {
		remediationPlanStatus = event.RemediationPlanStatus
	}
	result, err := tx.Exec(`INSERT INTO delivery_incident_events(incident_id,event_key,event_type,deploy_log_id,occurred_at,recorded_at,recorded_by,detail,causality_status,remediation_type,remediation_deploy_log_id,remediation_plan_status) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, event.IncidentID, event.IdempotencyKey, event.EventType, event.DeployLogID, event.OccurredAt, event.RecordedAt, event.RecordedBy, event.Detail, causalityStatus, remediationType, remediationDeployLogID, remediationPlanStatus)
	if err != nil {
		return err
	}
	event.ID, err = result.LastInsertId()
	return err
}

func (m *MySQL) deliveryIncidentByEventKey(key string, proposed service.DeliveryIncident, proposedEvent service.DeliveryIncidentEvent) (service.DeliveryIncident, service.DeliveryIncidentEvent, error) {
	event, err := scanDeliveryIncidentEvent(m.db.QueryRow(`SELECT `+incidentEventColumns+` FROM delivery_incident_events WHERE event_key=?`, key))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return proposed, proposedEvent, service.ErrConflict
		}
		return proposed, proposedEvent, err
	}
	incident, err := scanDeliveryIncident(m.db.QueryRow(`SELECT `+incidentColumns+` FROM delivery_incidents WHERE id=?`, event.IncidentID))
	if err != nil {
		return proposed, event, err
	}
	if event.EventType != proposedEvent.EventType || event.IncidentID == 0 || event.DeployLogID != proposedEvent.DeployLogID || !event.OccurredAt.Equal(proposedEvent.OccurredAt) || event.RecordedBy != proposedEvent.RecordedBy || event.Detail != proposedEvent.Detail || incident.DeployLogID != proposed.DeployLogID {
		return incident, event, service.ErrConflict
	}
	rows, err := m.db.Query(`SELECT `+incidentEventColumns+` FROM delivery_incident_events WHERE incident_id=? ORDER BY id`, incident.ID)
	if err != nil {
		return incident, event, err
	}
	defer rows.Close()
	incident.Events = make([]service.DeliveryIncidentEvent, 0, 2)
	for rows.Next() {
		historyEvent, scanErr := scanDeliveryIncidentEvent(rows)
		if scanErr != nil {
			return incident, event, scanErr
		}
		incident.Events = append(incident.Events, historyEvent)
	}
	if err := rows.Err(); err != nil {
		return incident, event, err
	}
	return incident, event, nil
}

func sameIncidentEvent(a, b service.DeliveryIncidentEvent) bool {
	return a.EventType == b.EventType && a.DeployLogID == b.DeployLogID &&
		a.OccurredAt.Equal(b.OccurredAt) && a.RecordedBy == b.RecordedBy && a.Detail == b.Detail
}

func sameDeliveryIncidentCausalityEvent(a, b service.DeliveryIncidentEvent) bool {
	return a.EventType == b.EventType && a.DeployLogID == b.DeployLogID &&
		a.RecordedBy == b.RecordedBy && a.Detail == b.Detail && a.CausalityStatus == b.CausalityStatus
}

func sameDeliveryIncidentRemediationEvent(a, b service.DeliveryIncidentEvent) bool {
	var aID, bID int64
	if a.RemediationDeployLogID != nil {
		aID = *a.RemediationDeployLogID
	}
	if b.RemediationDeployLogID != nil {
		bID = *b.RemediationDeployLogID
	}
	return a.EventType == b.EventType && a.DeployLogID == b.DeployLogID &&
		a.RecordedBy == b.RecordedBy && a.Detail == b.Detail && a.RemediationType == b.RemediationType &&
		a.RemediationPlanStatus == b.RemediationPlanStatus && aID == bID
}

func isMySQLDuplicate(err error) bool {
	var mysqlErr *mysqlDriver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
