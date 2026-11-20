USE deploy_platform;

ALTER TABLE delivery_incidents
  ADD COLUMN intervention_started_at DATETIME(6) NULL,
  ADD COLUMN intervention_recorded_at DATETIME(6) NULL,
  ADD COLUMN intervention_recorded_by VARCHAR(64) NULL,
  ADD INDEX idx_delivery_incident_time (occurred_at,id),
  ADD INDEX idx_delivery_incident_intervention (cluster_id,app_name,intervention_started_at,id);

ALTER TABLE delivery_incident_events
  MODIFY COLUMN event_type ENUM('incident_recorded','intervention_started','recovery_confirmed') NOT NULL;
