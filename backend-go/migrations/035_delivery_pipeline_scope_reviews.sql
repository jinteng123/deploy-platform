USE deploy_platform;

-- Keep operator scope reviews tied to the exact environment binding and
-- Yunxiao Flow fingerprint. Reviews are append-only audit evidence and never
-- authorize pipeline execution.
CREATE TABLE delivery_pipeline_scope_reviews (
  id BIGINT NOT NULL AUTO_INCREMENT,
  delivery_binding_id BIGINT UNSIGNED NOT NULL,
  connection_id BIGINT NOT NULL,
  pipeline_id VARCHAR(20) NOT NULL,
  definition_hash CHAR(64) NOT NULL,
  write_scope VARCHAR(32) NOT NULL,
  reviewer VARCHAR(120) NOT NULL,
  reviewed_at DATETIME NOT NULL,
  PRIMARY KEY (id),
  INDEX idx_pipeline_scope_review_latest (delivery_binding_id, id),
  CONSTRAINT fk_pipeline_scope_review_binding
    FOREIGN KEY (delivery_binding_id) REFERENCES delivery_bindings(id)
    ON DELETE CASCADE,
  CONSTRAINT chk_pipeline_scope_review_scope
    CHECK (write_scope IN ('configuration_only', 'workload_or_image', 'unknown'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
