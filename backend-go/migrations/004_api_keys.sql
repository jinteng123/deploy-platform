USE deploy_platform;

-- 对外 OpenAPI 访问密钥：只存 SHA-256 散列与展示前缀，明文令牌仅创建时
-- 返回一次。role 决定 /openapi/v1 下的能力面（viewer 只读，operator 可
-- 触发发布/晋级/回滚）；审计按 apikey:<name> 记操作者。
CREATE TABLE IF NOT EXISTS `api_keys` (
  `id`           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `name`         VARCHAR(64)     NOT NULL COMMENT '密钥用途标注，全局唯一',
  `key_hash`     CHAR(64)        NOT NULL COMMENT 'SHA-256(token) hex',
  `prefix`       VARCHAR(16)     NOT NULL COMMENT 'dp_xxxxxxxx 展示前缀',
  `role`         ENUM('viewer','operator') NOT NULL DEFAULT 'viewer',
  `enabled`      TINYINT(1)      NOT NULL DEFAULT 1,
  `expires_at`   DATETIME        NULL     COMMENT 'NULL = 永不过期',
  `last_used_at` DATETIME        NULL,
  `created_at`   DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`   DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_name` (`name`),
  KEY `idx_key_hash` (`key_hash`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='对外 OpenAPI 访问密钥';
