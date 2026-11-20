USE deploy_platform;

-- Production evidence requirements are explicit and versioned independently
-- from rollout strategy. No permissive policy is backfilled: production
-- promotion remains blocked until an administrator chooses trusted sources.
CREATE TABLE IF NOT EXISTS delivery_environment_evidence_policy_versions (
  environment_id BIGINT UNSIGNED NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  required_verification_kinds JSON NOT NULL,
  max_evidence_age_minutes SMALLINT UNSIGNED NOT NULL,
  trusted_submitters JSON NOT NULL,
  created_by VARCHAR(128) NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (environment_id, version),
  CONSTRAINT fk_delivery_evidence_policy_environment FOREIGN KEY (environment_id)
    REFERENCES delivery_environments(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='不可变生产验证证据门禁版本';
