USE deploy_platform;

-- Causality is an explicit, versioned operator assessment. Keep each revision
-- in the append-only event ledger; NULL means the event is not an assessment.
ALTER TABLE delivery_incident_events
  MODIFY COLUMN event_type ENUM('incident_recorded','intervention_started','recovery_confirmed','causality_assessed') NOT NULL,
  ADD COLUMN causality_status ENUM('caused_by_deployment','not_caused_by_deployment') NULL AFTER detail;
