USE deploy_platform;

-- Persist both the original workload template identity and the workload that
-- currently receives traffic. Legacy rows are seeded by name only; the UID is
-- intentionally unknown until the platform reads the live Deployment.
ALTER TABLE delivery_bindings
  ADD COLUMN template_workload_cluster_id INT UNSIGNED NOT NULL DEFAULT 0,
  ADD COLUMN template_workload_namespace VARCHAR(253) NOT NULL DEFAULT '',
  ADD COLUMN template_workload_kind VARCHAR(64) NOT NULL DEFAULT '',
  ADD COLUMN template_workload_name VARCHAR(253) NOT NULL DEFAULT '',
  ADD COLUMN template_workload_uid VARCHAR(128) NOT NULL DEFAULT '',
  ADD COLUMN template_workload_resource_version VARCHAR(64) NOT NULL DEFAULT '',
  ADD COLUMN active_workload_cluster_id INT UNSIGNED NOT NULL DEFAULT 0,
  ADD COLUMN active_workload_namespace VARCHAR(253) NOT NULL DEFAULT '',
  ADD COLUMN active_workload_kind VARCHAR(64) NOT NULL DEFAULT '',
  ADD COLUMN active_workload_name VARCHAR(253) NOT NULL DEFAULT '',
  ADD COLUMN active_workload_uid VARCHAR(128) NOT NULL DEFAULT '',
  ADD COLUMN active_workload_resource_version VARCHAR(64) NOT NULL DEFAULT '';

UPDATE delivery_bindings b
JOIN apps a ON a.id = b.app_id
SET b.template_workload_cluster_id = a.cluster_id,
    b.template_workload_namespace = a.namespace,
    b.template_workload_kind = 'Deployment',
    b.template_workload_name = a.deployment_name,
    b.active_workload_cluster_id = a.cluster_id,
    b.active_workload_namespace = a.namespace,
    b.active_workload_kind = 'Deployment',
    b.active_workload_name = a.deployment_name
WHERE b.template_workload_name = '' OR b.active_workload_name = '';
