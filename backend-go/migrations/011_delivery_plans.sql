USE deploy_platform;

-- Immutable, server-derived release previews. Plans are intentionally not
-- executable until the approval/run orchestration phases are delivered.
CREATE TABLE IF NOT EXISTS delivery_plans (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  release_id BIGINT UNSIGNED NOT NULL,
  target_cluster_id INT UNSIGNED NOT NULL,
  target_app_id INT UNSIGNED NOT NULL,
  strategy VARCHAR(24) NOT NULL,
  status VARCHAR(24) NOT NULL DEFAULT 'preview_only',
  snapshot_hash CHAR(64) NOT NULL,
  snapshot_json JSON NOT NULL,
  blockers_json JSON NOT NULL,
  requested_by VARCHAR(64) NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_delivery_plan_snapshot (snapshot_hash),
  KEY idx_delivery_plan_release (release_id, created_at),
  KEY idx_delivery_plan_target (target_cluster_id, target_app_id, created_at),
  CONSTRAINT fk_delivery_plan_release FOREIGN KEY (release_id)
    REFERENCES image_releases(id) ON DELETE RESTRICT,
  CONSTRAINT fk_delivery_plan_cluster FOREIGN KEY (target_cluster_id)
    REFERENCES clusters(id) ON DELETE RESTRICT,
  CONSTRAINT fk_delivery_plan_app FOREIGN KEY (target_app_id)
    REFERENCES apps(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='不可变发布计划预览';
