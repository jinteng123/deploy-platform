USE deploy_platform;

-- Existing blank repository metadata is unknown until an operator explicitly
-- records a third-party image source; never infer it from an empty URL.
ALTER TABLE apps
  ADD COLUMN source_kind VARCHAR(20) NOT NULL DEFAULT 'unknown' AFTER image_repository;
UPDATE apps SET source_kind='repository' WHERE source_repo_url <> '';

-- Logical names are intentionally separate from the legacy `apps` target row.
-- Every existing target receives its own unconfirmed identity; identical names
-- are not treated as proof that two targets are the same component/environment.
CREATE TABLE IF NOT EXISTS delivery_applications (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  name VARCHAR(128) NOT NULL,
  legacy_app_id INT UNSIGNED DEFAULT NULL,
  requires_review TINYINT(1) NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_delivery_application_legacy (legacy_app_id),
  KEY idx_delivery_application_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS delivery_components (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  application_id BIGINT UNSIGNED NOT NULL,
  name VARCHAR(128) NOT NULL,
  legacy_app_id INT UNSIGNED DEFAULT NULL,
  requires_review TINYINT(1) NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_delivery_component_legacy (legacy_app_id),
  UNIQUE KEY uk_delivery_component_name (application_id, name),
  CONSTRAINT fk_delivery_component_application FOREIGN KEY (application_id)
    REFERENCES delivery_applications(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS delivery_environments (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  code VARCHAR(64) NOT NULL,
  name VARCHAR(128) NOT NULL,
  environment_tier VARCHAR(24) NOT NULL,
  legacy_cluster_id INT UNSIGNED DEFAULT NULL,
  requires_review TINYINT(1) NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_delivery_environment_code (code),
  UNIQUE KEY uk_delivery_environment_legacy_cluster (legacy_cluster_id),
  KEY idx_delivery_environment_tier (environment_tier)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS delivery_bindings (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  app_id INT UNSIGNED NOT NULL,
  application_id BIGINT UNSIGNED NOT NULL,
  component_id BIGINT UNSIGNED NOT NULL,
  environment_id BIGINT UNSIGNED NOT NULL,
  strategy_draft VARCHAR(24) DEFAULT NULL,
  requires_review TINYINT(1) NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_delivery_binding_app (app_id),
  KEY idx_delivery_binding_environment (environment_id),
  KEY idx_delivery_binding_component (component_id),
  CONSTRAINT fk_delivery_binding_app FOREIGN KEY (app_id)
    REFERENCES apps(id) ON DELETE CASCADE,
  CONSTRAINT fk_delivery_binding_application FOREIGN KEY (application_id)
    REFERENCES delivery_applications(id) ON DELETE RESTRICT,
  CONSTRAINT fk_delivery_binding_component FOREIGN KEY (component_id)
    REFERENCES delivery_components(id) ON DELETE RESTRICT,
  CONSTRAINT fk_delivery_binding_environment FOREIGN KEY (environment_id)
    REFERENCES delivery_environments(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Environment identity is per existing cluster until an administrator reviews
-- and deliberately consolidates it. The current cluster tier is preserved.
INSERT IGNORE INTO delivery_environments
  (code, name, environment_tier, legacy_cluster_id, requires_review)
SELECT CONCAT('legacy-cluster-', c.id),
       LEFT(CONCAT(CONVERT(0xE5BE85E7A1AEE8AEA420C2B720 USING utf8mb4), c.environment, ' / ', c.name), 128),
       CASE WHEN c.environment_tier = 'production' THEN 'production' ELSE 'non_production' END,
       c.id, 1
FROM clusters c;

-- Never merge same-named legacy apps across clusters. Operators can later
-- select an existing logical application/component in the delivery model UI.
INSERT IGNORE INTO delivery_applications (name, legacy_app_id, requires_review)
SELECT a.app_name, a.id, 1 FROM apps a;

INSERT IGNORE INTO delivery_components (application_id, name, legacy_app_id, requires_review)
SELECT da.id, a.app_name, a.id, 1
FROM apps a
JOIN delivery_applications da ON da.legacy_app_id = a.id;

INSERT IGNORE INTO delivery_bindings
  (app_id, application_id, component_id, environment_id, requires_review)
SELECT a.id, da.id, dc.id, de.id, 1
FROM apps a
JOIN delivery_applications da ON da.legacy_app_id = a.id
JOIN delivery_components dc ON dc.legacy_app_id = a.id
JOIN delivery_environments de ON de.legacy_cluster_id = a.cluster_id;
