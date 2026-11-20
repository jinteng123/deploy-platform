USE deploy_platform;

-- 生产晋级审批：发起人与审批人分离（SoD），request 落库后由第二个身份
-- 批准才真正部署。pending 唯一性由 Store 串行保证（单进程写路径）。
CREATE TABLE IF NOT EXISTS `promotion_requests` (
  `id`              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `release_id`      BIGINT UNSIGNED NOT NULL COMMENT 'image_releases.id',
  `prod_cluster_id` INT             NOT NULL COMMENT '目标生产集群',
  `requester`       VARCHAR(64)     NOT NULL COMMENT '发起人（session 用户或 apikey:<name>）',
  `status`          ENUM('pending','approved','rejected','cancelled','expired') NOT NULL DEFAULT 'pending',
  `approver`        VARCHAR(64)     NULL COMMENT '审批人/决定人',
  `comment`         VARCHAR(255)    NULL,
  `deploy_log_id`   BIGINT UNSIGNED NULL COMMENT '批准后产生的 deploy_logs.id',
  `created_at`      DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `decided_at`      DATETIME        NULL,
  `expires_at`      DATETIME        NULL COMMENT '超过后不可批准',
  PRIMARY KEY (`id`),
  KEY `idx_release` (`release_id`, `status`),
  KEY `idx_cluster` (`prod_cluster_id`, `status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='生产晋级审批请求';
