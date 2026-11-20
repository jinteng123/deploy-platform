USE deploy_platform;

-- Cross-process ownership of Kubernetes resources during active delivery
-- tasks. One run may own multiple resources; unique resource_key acquisition
-- makes a multi-resource reservation all-or-nothing inside its run transaction.
CREATE TABLE IF NOT EXISTS delivery_resource_locks (
  resource_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  cluster_id INT UNSIGNED NOT NULL,
  namespace VARCHAR(64) NOT NULL,
  resource_kind VARCHAR(32) NOT NULL,
  resource_name VARCHAR(253) NOT NULL,
  deploy_log_id BIGINT UNSIGNED NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (resource_key),
  KEY idx_delivery_resource_lock_owner (deploy_log_id),
  KEY idx_delivery_resource_lock_target (cluster_id, namespace, resource_kind),
  CONSTRAINT fk_delivery_resource_lock_log FOREIGN KEY (deploy_log_id)
    REFERENCES deploy_logs(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='活动发布任务占用的 Kubernetes 资源';

-- Reconstruct locks for tasks that were active before this migration. A
-- duplicate-key error is intentional: it exposes overlapping active work that
-- must be reconciled before the lock table can be enabled safely.
INSERT INTO delivery_resource_locks(resource_key,cluster_id,namespace,resource_kind,resource_name,deploy_log_id)
SELECT SHA2(CONCAT(cluster_id, CHAR(0), namespace, CHAR(0), 'deployment', CHAR(0), deployment_name),256),
       cluster_id, namespace, 'deployment', deployment_name, id
FROM deploy_logs
WHERE status IN ('pending','running');
