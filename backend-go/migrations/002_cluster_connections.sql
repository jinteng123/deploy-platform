USE deploy_platform;

ALTER TABLE `clusters`
  ADD COLUMN `environment` VARCHAR(64) NOT NULL DEFAULT 'default' AFTER `env_code`,
  ADD COLUMN `environment_tier` ENUM('non_production','production') NOT NULL DEFAULT 'non_production' AFTER `environment`,
  ADD COLUMN `deploy_policy` ENUM('automatic','manual') NOT NULL DEFAULT 'manual' AFTER `environment_tier`,
  ADD COLUMN `connection_type` ENUM('kubeconfig','token','legacy_ssh') NOT NULL DEFAULT 'legacy_ssh' AFTER `deploy_policy`,
  ADD COLUMN `api_server` VARCHAR(512) DEFAULT NULL AFTER `connection_type`,
  ADD COLUMN `credential_ciphertext` MEDIUMTEXT DEFAULT NULL AFTER `api_server`,
  ADD COLUMN `ca_cert` MEDIUMTEXT DEFAULT NULL AFTER `credential_ciphertext`,
  ADD COLUMN `insecure_skip_tls_verify` TINYINT(1) NOT NULL DEFAULT 0 AFTER `ca_cert`,
  ADD COLUMN `enabled` TINYINT(1) NOT NULL DEFAULT 1 AFTER `insecure_skip_tls_verify`;
