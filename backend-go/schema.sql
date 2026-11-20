-- ============================================================
-- 轻量化文件触发式发布平台 — DDL (MySQL 5.7)
-- ============================================================

CREATE DATABASE IF NOT EXISTS deploy_platform
  CHARACTER SET utf8mb4
  COLLATE utf8mb4_general_ci;

USE deploy_platform;

-- K8s 集群/环境配置表
CREATE TABLE IF NOT EXISTS `clusters` (
  `id`                  INT UNSIGNED    NOT NULL AUTO_INCREMENT,
  `name`                VARCHAR(64)     NOT NULL COMMENT '环境名称，如 测试环境、生产环境',
  `env_code`            VARCHAR(16)     NOT NULL COMMENT '环境代码: test, prod',
  `k8s_master_host`     VARCHAR(128)    NOT NULL,
  `k8s_master_port`     INT             NOT NULL DEFAULT 22,
  `k8s_master_user`     VARCHAR(64)     NOT NULL DEFAULT 'root',
  `k8s_master_key_path` VARCHAR(256)    DEFAULT NULL,
  `default_namespace`   VARCHAR(64)     NOT NULL DEFAULT 'default',
  `description`         VARCHAR(256)    DEFAULT NULL,
  `created_at`          DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`          DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_env_code` (`env_code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='K8s 集群/环境配置';

-- 应用信息表
CREATE TABLE IF NOT EXISTS `apps` (
  `id`               INT UNSIGNED    NOT NULL AUTO_INCREMENT,
  `cluster_id`       INT UNSIGNED    NOT NULL DEFAULT 1 COMMENT '所属集群',
  `app_name`         VARCHAR(128)    NOT NULL COMMENT '应用名称（唯一标识）',
  `match_keyword`    VARCHAR(128)    NOT NULL COMMENT '文件名匹配关键词',
  `deployment_name`  VARCHAR(128)    NOT NULL COMMENT 'K8s Deployment 名称',
  `container_name`   VARCHAR(128)    NOT NULL COMMENT 'K8s 容器名称',
  `namespace`        VARCHAR(64)     NOT NULL DEFAULT 'default' COMMENT 'K8s 命名空间',
  `harbor_project`   VARCHAR(128)    NOT NULL DEFAULT 'library' COMMENT 'Harbor 仓库项目名',
  `description`      VARCHAR(512)    DEFAULT NULL COMMENT '应用描述',
  `created_at`       DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`       DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_app_cluster` (`app_name`, `cluster_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='应用信息表';

-- 发布审计日志表
CREATE TABLE IF NOT EXISTS `deploy_logs` (
  `id`               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `cluster_id`       INT UNSIGNED    NOT NULL DEFAULT 1 COMMENT '目标集群',
  `app_name`         VARCHAR(128)    NOT NULL COMMENT '应用名称',
  `deployment_name`  VARCHAR(128)    NOT NULL,
  `container_name`   VARCHAR(128)    NOT NULL,
  `namespace`        VARCHAR(64)     NOT NULL,
  `version`          VARCHAR(64)     NOT NULL COMMENT '发布版本号',
  `file_name`        VARCHAR(256)    NOT NULL COMMENT '触发文件名',
  `image_full`       VARCHAR(512)    DEFAULT NULL COMMENT '完整镜像地址',
  `deploy_type`      VARCHAR(16)     NOT NULL DEFAULT 'deploy' COMMENT '操作类型: deploy=发布, rollback=回滚',
  `status`           ENUM('pending','running','success','failed') NOT NULL DEFAULT 'pending' COMMENT '发布状态',
  `current_step`     VARCHAR(64)     DEFAULT NULL COMMENT '当前执行步骤',
  `step_detail`      TEXT            DEFAULT NULL COMMENT '各步骤执行详情 (JSON)',
  `error_message`    TEXT            DEFAULT NULL COMMENT '错误信息 (stderr)',
  `started_at`       DATETIME        DEFAULT NULL,
  `finished_at`      DATETIME        DEFAULT NULL,
  `created_at`       DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_app_status` (`app_name`, `status`),
  KEY `idx_created` (`created_at`),
  KEY `idx_cluster` (`cluster_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='发布审计日志表';
