USE deploy_platform;

CREATE TABLE IF NOT EXISTS `image_releases` (
  `id`                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `event_key`           CHAR(64)        NOT NULL COMMENT 'Webhook 幂等键 SHA-256',
  `event_id`            VARCHAR(255)    NOT NULL COMMENT '云效事件或构建标识',
  `source`              VARCHAR(32)     NOT NULL DEFAULT 'yunxiao',
  `pipeline_id`         VARCHAR(128)    DEFAULT NULL,
  `pipeline_name`       VARCHAR(255)    DEFAULT NULL,
  `build_number`        VARCHAR(64)     DEFAULT NULL,
  `branch`              VARCHAR(255)    DEFAULT NULL,
  `commit_id`           VARCHAR(128)    DEFAULT NULL,
  `app_name`            VARCHAR(128)    NOT NULL,
  `image_full`          VARCHAR(512)    NOT NULL,
  `image_digest`        VARCHAR(255)    DEFAULT NULL,
  `version`             VARCHAR(128)    NOT NULL,
  `test_cluster_id`     INT UNSIGNED    NOT NULL,
  `test_deploy_log_id`  BIGINT UNSIGNED NOT NULL,
  `prod_cluster_id`     INT UNSIGNED    DEFAULT NULL,
  `prod_deploy_log_id`  BIGINT UNSIGNED DEFAULT NULL,
  `created_at`          DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`          DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_image_release_event` (`event_key`),
  KEY `idx_image_release_app` (`app_name`, `created_at`),
  KEY `idx_image_release_test_log` (`test_deploy_log_id`),
  KEY `idx_image_release_prod_log` (`prod_deploy_log_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='云效镜像发布单';
