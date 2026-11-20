USE deploy_platform;
SET NAMES utf8mb4;

INSERT INTO clusters (
  id, name, env_code, k8s_master_host, k8s_master_port,
  k8s_master_user, default_namespace, description
) VALUES (
  1, '本地 Kind 环境', 'local', 'kind-kbdev', 6443,
  'kubeconfig', 'default', 'Local development cluster managed by kind'
) ON DUPLICATE KEY UPDATE
  name = VALUES(name),
  default_namespace = VALUES(default_namespace),
  description = VALUES(description);
