USE deploy_platform;

-- Operator-recorded production incidents are linked to immutable deployment
-- log IDs. The summary row is a current-state projection; event rows preserve
-- the original occurrence and later business recovery confirmation.
CREATE TABLE IF NOT EXISTS delivery_incidents (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  deploy_log_id BIGINT UNSIGNED NOT NULL,
  release_id BIGINT UNSIGNED NULL,
  cluster_id INT UNSIGNED NOT NULL,
  app_name VARCHAR(255) NOT NULL,
  summary VARCHAR(1000) NOT NULL,
  status ENUM('open','recovered') NOT NULL DEFAULT 'open',
  occurred_at DATETIME(6) NOT NULL,
  recorded_at DATETIME(6) NOT NULL,
  recorded_by VARCHAR(64) NOT NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  recovery_occurred_at DATETIME(6) NULL,
  recovery_recorded_at DATETIME(6) NULL,
  recovery_recorded_by VARCHAR(64) NULL,
  recovery_confirmation VARCHAR(1000) NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_delivery_incident_app_time (app_name, occurred_at, id),
  KEY idx_delivery_incident_target_time (cluster_id, occurred_at, id),
  KEY idx_delivery_incident_deploy (deploy_log_id),
  CONSTRAINT fk_delivery_incident_deploy FOREIGN KEY (deploy_log_id)
    REFERENCES deploy_logs(id) ON DELETE RESTRICT,
  CONSTRAINT fk_delivery_incident_release FOREIGN KEY (release_id)
    REFERENCES image_releases(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='关联生产部署的人工故障与业务恢复状态';

CREATE TABLE IF NOT EXISTS delivery_incident_events (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  incident_id BIGINT UNSIGNED NOT NULL,
  event_key VARCHAR(128) NOT NULL,
  event_type ENUM('incident_recorded','recovery_confirmed') NOT NULL,
  deploy_log_id BIGINT UNSIGNED NOT NULL,
  occurred_at DATETIME(6) NOT NULL,
  recorded_at DATETIME(6) NOT NULL,
  recorded_by VARCHAR(64) NOT NULL,
  detail VARCHAR(1000) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_delivery_incident_event_key (event_key),
  KEY idx_delivery_incident_event_order (incident_id, id),
  CONSTRAINT fk_delivery_incident_event_incident FOREIGN KEY (incident_id)
    REFERENCES delivery_incidents(id) ON DELETE RESTRICT,
  CONSTRAINT fk_delivery_incident_event_deploy FOREIGN KEY (deploy_log_id)
    REFERENCES deploy_logs(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='生产故障与恢复确认的不可变事件记录';
