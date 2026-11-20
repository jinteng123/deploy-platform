USE deploy_platform;

-- Policy edits are append-only. Plans pin one revision and remain immutable
-- when an operator changes the binding's current strategy later.
CREATE TABLE IF NOT EXISTS delivery_policy_versions (
  binding_id BIGINT UNSIGNED NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  strategy VARCHAR(24) NOT NULL,
  service_name VARCHAR(253) NOT NULL DEFAULT '',
  candidate_retention_hours INT UNSIGNED NOT NULL DEFAULT 0,
  created_by VARCHAR(64) NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (binding_id, version),
  CONSTRAINT fk_delivery_policy_binding FOREIGN KEY (binding_id)
    REFERENCES delivery_bindings(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='不可变发布策略版本';

-- Preserve the current binding draft as its first explicit version. Empty
-- legacy drafts mean the existing rolling strategy; no canary is enabled by
-- this backfill because gateway_canary remains blocked by the runtime gate.
INSERT IGNORE INTO delivery_policy_versions
  (binding_id, version, strategy, service_name, candidate_retention_hours, created_by)
SELECT id,
       1,
       CASE WHEN strategy_draft IN ('blue_green', 'gateway_canary') THEN strategy_draft ELSE 'rolling' END,
       CASE WHEN strategy_draft = 'blue_green' THEN service_name ELSE '' END,
       CASE WHEN strategy_draft = 'blue_green' THEN 24 ELSE 0 END,
       'migration-019'
FROM delivery_bindings;
