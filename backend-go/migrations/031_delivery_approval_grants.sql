USE deploy_platform;

-- Operator approval authority is explicitly scoped to a business application
-- and confirmed production environment. Existing users receive no implicit
-- grants; administrators must assign scopes deliberately.
CREATE TABLE IF NOT EXISTS delivery_approval_grants (
  user_id BIGINT UNSIGNED NOT NULL,
  application_id BIGINT UNSIGNED NOT NULL,
  environment_id BIGINT UNSIGNED NOT NULL,
  granted_by VARCHAR(64) NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (user_id, application_id, environment_id),
  KEY idx_delivery_approval_scope (application_id, environment_id, user_id),
  CONSTRAINT fk_delivery_approval_user FOREIGN KEY (user_id)
    REFERENCES users(id) ON DELETE CASCADE,
  CONSTRAINT fk_delivery_approval_application FOREIGN KEY (application_id)
    REFERENCES delivery_applications(id) ON DELETE RESTRICT,
  CONSTRAINT fk_delivery_approval_environment FOREIGN KEY (environment_id)
    REFERENCES delivery_environments(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
