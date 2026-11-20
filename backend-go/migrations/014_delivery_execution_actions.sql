USE deploy_platform;

-- Durable intent and readback records for writes performed by release workers.
-- The API stores only explicitly constructed JSON, never credentials or full
-- Kubernetes manifests. A log can have one idempotent action per stable key.
CREATE TABLE IF NOT EXISTS delivery_execution_actions (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  deploy_log_id BIGINT UNSIGNED NOT NULL,
  action_key VARCHAR(128) NOT NULL,
  action_kind VARCHAR(64) NOT NULL,
  status VARCHAR(24) NOT NULL DEFAULT 'intent',
  intent_json JSON NOT NULL,
  observed_json JSON NULL,
  outcome_code VARCHAR(64) NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  finished_at DATETIME NULL,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_delivery_execution_action (deploy_log_id, action_key),
  KEY idx_delivery_execution_action_status (status, updated_at),
  CONSTRAINT fk_delivery_execution_action_log FOREIGN KEY (deploy_log_id)
    REFERENCES deploy_logs(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='发布外部写入意图与回读结果';
