USE deploy_platform;

-- Remediation classifications are versioned event revisions. A deployment
-- link is optional for operational actions and required by service policy for
-- rollback, hotfix, and configuration deployment classifications.
ALTER TABLE delivery_incident_events
  MODIFY COLUMN event_type ENUM('incident_recorded','intervention_started','recovery_confirmed','causality_assessed','remediation_classified') NOT NULL,
  ADD COLUMN remediation_type ENUM('rollback','hotfix_deployment','configuration_deployment','restart_or_scale','no_action') NULL AFTER causality_status,
  ADD COLUMN remediation_deploy_log_id BIGINT UNSIGNED NULL AFTER remediation_type,
  ADD COLUMN remediation_plan_status ENUM('planned','unplanned','not_applicable') NULL AFTER remediation_deploy_log_id,
  ADD KEY idx_delivery_incident_remediation_deploy (remediation_deploy_log_id),
  ADD CONSTRAINT fk_delivery_incident_remediation_deploy FOREIGN KEY (remediation_deploy_log_id)
    REFERENCES deploy_logs(id) ON DELETE RESTRICT;
