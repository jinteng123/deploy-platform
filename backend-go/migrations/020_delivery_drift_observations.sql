USE deploy_platform;

-- Current drift state is one row per observed target. History stores only
-- meaningful state/baseline/image/reason transitions, not every refresh.
CREATE TABLE IF NOT EXISTS delivery_drift_states (
  target_key CHAR(64) NOT NULL,
  app_id INT UNSIGNED NOT NULL,
  app_name VARCHAR(255) NOT NULL,
  cluster_id INT UNSIGNED NOT NULL,
  namespace VARCHAR(253) NOT NULL,
  deployment_name VARCHAR(253) NOT NULL,
  container_name VARCHAR(253) NOT NULL,
  status ENUM('in_sync','drifted','unknown') NOT NULL,
  expected_image TEXT NULL,
  actual_image TEXT NULL,
  baseline_log_id BIGINT UNSIGNED NULL,
  reason TEXT NULL,
  drift_started_at DATETIME NULL,
  last_observed_at DATETIME NOT NULL,
  observation_count BIGINT UNSIGNED NOT NULL DEFAULT 1,
  observed_by VARCHAR(64) NOT NULL,
  source VARCHAR(32) NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (target_key),
  KEY idx_delivery_drift_states_cluster (cluster_id, status, last_observed_at),
  KEY idx_delivery_drift_states_app (app_id, last_observed_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='按需读取的当前 Deployment 镜像漂移状态';

CREATE TABLE IF NOT EXISTS delivery_drift_events (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  target_key CHAR(64) NOT NULL,
  app_id INT UNSIGNED NOT NULL,
  app_name VARCHAR(255) NOT NULL,
  cluster_id INT UNSIGNED NOT NULL,
  namespace VARCHAR(253) NOT NULL,
  deployment_name VARCHAR(253) NOT NULL,
  container_name VARCHAR(253) NOT NULL,
  previous_status ENUM('in_sync','drifted','unknown') NULL,
  status ENUM('in_sync','drifted','unknown') NOT NULL,
  change_kind ENUM('initial','status','baseline','image','reason') NOT NULL,
  expected_image TEXT NULL,
  actual_image TEXT NULL,
  baseline_log_id BIGINT UNSIGNED NULL,
  reason TEXT NULL,
  drift_started_at DATETIME NULL,
  drift_duration_seconds BIGINT UNSIGNED NULL,
  observed_at DATETIME NOT NULL,
  observed_by VARCHAR(64) NOT NULL,
  source VARCHAR(32) NOT NULL,
  PRIMARY KEY (id),
  KEY idx_delivery_drift_events_cluster_time (cluster_id, observed_at, id),
  KEY idx_delivery_drift_events_app_time (app_id, observed_at, id),
  KEY idx_delivery_drift_events_target_time (target_key, observed_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Deployment 镜像漂移状态变化历史';
