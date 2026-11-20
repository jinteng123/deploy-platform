USE deploy_platform;

-- Apply once, after 005. Existing releases remain visible but require a pinned
-- artifact and fresh validation evidence before production promotion.
ALTER TABLE image_releases ADD COLUMN metadata JSON NULL;
ALTER TABLE deploy_logs
  MODIFY COLUMN status ENUM('pending','running','success','failed','interrupted') NOT NULL DEFAULT 'pending' COMMENT '发布状态',
  ADD COLUMN release_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
  ADD COLUMN rollback_target_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
  ADD INDEX idx_release_target (release_id, cluster_id, id),
  ADD INDEX idx_workload_task (cluster_id, namespace, deployment_name, status);
ALTER TABLE promotion_requests
  ADD COLUMN snapshot CHAR(64) NOT NULL DEFAULT '',
  ADD COLUMN template_hash CHAR(64) NOT NULL DEFAULT '',
  ADD COLUMN expected_image VARCHAR(512) NOT NULL DEFAULT '',
  ADD COLUMN baseline_log_id BIGINT UNSIGNED NOT NULL DEFAULT 0;
UPDATE deploy_logs l JOIN image_releases r ON r.test_deploy_log_id=l.id
  SET l.release_id=r.id;
UPDATE deploy_logs l JOIN image_releases r ON r.prod_deploy_log_id=l.id
  SET l.release_id=r.id;
-- Requests created before snapshot binding cannot be safely approved.
UPDATE promotion_requests SET status='expired',decided_at=NOW()
  WHERE status='pending';
