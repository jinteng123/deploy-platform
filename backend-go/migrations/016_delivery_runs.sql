USE deploy_platform;

-- Independent orchestration identity and append-only progress events for an
-- approved immutable delivery plan. deploy_logs remains the legacy task API.
CREATE TABLE IF NOT EXISTS delivery_runs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  delivery_plan_id BIGINT UNSIGNED NOT NULL,
  promotion_request_id BIGINT UNSIGNED NOT NULL,
  deploy_log_id BIGINT UNSIGNED NOT NULL,
  strategy VARCHAR(24) NOT NULL,
  status VARCHAR(24) NOT NULL DEFAULT 'queued',
  current_stage VARCHAR(64) NOT NULL DEFAULT 'queued',
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  error_message VARCHAR(2000) NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_delivery_run_log (deploy_log_id),
  KEY idx_delivery_run_plan (delivery_plan_id, created_at),
  KEY idx_delivery_run_status (status, updated_at),
  CONSTRAINT fk_delivery_run_plan FOREIGN KEY (delivery_plan_id)
    REFERENCES delivery_plans(id) ON DELETE RESTRICT,
  CONSTRAINT fk_delivery_run_approval FOREIGN KEY (promotion_request_id)
    REFERENCES promotion_requests(id) ON DELETE RESTRICT,
  CONSTRAINT fk_delivery_run_log FOREIGN KEY (deploy_log_id)
    REFERENCES deploy_logs(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='发布计划的独立执行实例';

CREATE TABLE IF NOT EXISTS delivery_run_events (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  run_id BIGINT UNSIGNED NOT NULL,
  run_version BIGINT UNSIGNED NOT NULL,
  event_key VARCHAR(160) NOT NULL,
  event_type VARCHAR(32) NOT NULL,
  stage VARCHAR(64) NOT NULL,
  status VARCHAR(24) NOT NULL,
  detail VARCHAR(2000) NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_delivery_run_event_key (run_id, event_key),
  KEY idx_delivery_run_event_order (run_id, id),
  CONSTRAINT fk_delivery_run_event_run FOREIGN KEY (run_id)
    REFERENCES delivery_runs(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='发布执行的不可变阶段时间线';
