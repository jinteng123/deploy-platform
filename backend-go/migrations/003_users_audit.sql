USE deploy_platform;

-- 平台用户：角色控制平台级路由可达性；k8s_username/k8s_groups 为
-- Kubernetes impersonation 目标（空 k8s_username = 使用集群凭据自身身份）。
CREATE TABLE IF NOT EXISTS `users` (
  `id`            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `username`      VARCHAR(64)     NOT NULL COMMENT '登录名（不可变）',
  `display_name`  VARCHAR(128)    NOT NULL DEFAULT '',
  `role`          ENUM('admin','operator','viewer') NOT NULL DEFAULT 'viewer',
  `password_hash` VARCHAR(128)    NOT NULL COMMENT 'bcrypt',
  `k8s_username`  VARCHAR(128)    NOT NULL DEFAULT '' COMMENT 'impersonation 用户名',
  `k8s_groups`    VARCHAR(512)    NOT NULL DEFAULT '' COMMENT '逗号分隔 impersonation 组',
  `enabled`       TINYINT(1)      NOT NULL DEFAULT 1,
  `created_at`    DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`    DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_username` (`username`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='平台用户';

-- 操作审计：记录 K8s 写操作与敏感动作的操作者、对象与结果。
-- 绝不记录 Secret 数据、Token、KubeConfig 或完整 manifest。
CREATE TABLE IF NOT EXISTS `audit_logs` (
  `id`         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `username`   VARCHAR(64)     NOT NULL COMMENT '操作者登录名',
  `action`     VARCHAR(32)     NOT NULL COMMENT 'scale/image/restart/delete/update/exec/secret_view/...',
  `cluster_id` INT UNSIGNED    NOT NULL DEFAULT 0,
  `namespace`  VARCHAR(64)     NOT NULL DEFAULT '',
  `gvr`        VARCHAR(128)    NOT NULL DEFAULT '' COMMENT 'group/version/resource',
  `name`       VARCHAR(256)    NOT NULL DEFAULT '' COMMENT '资源名',
  `detail`     VARCHAR(512)    NOT NULL DEFAULT '' COMMENT '脱敏后的补充信息',
  `result`     VARCHAR(16)     NOT NULL DEFAULT 'success' COMMENT 'success/failed/denied/conflict',
  `created_at` DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_created` (`created_at`),
  KEY `idx_user` (`username`),
  KEY `idx_cluster` (`cluster_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='操作审计日志';
