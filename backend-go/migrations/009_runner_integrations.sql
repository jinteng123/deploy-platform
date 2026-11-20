USE deploy_platform;

CREATE TABLE runner_bindings (
 id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
 app_id INT UNSIGNED NOT NULL,
 token_hash CHAR(64) NOT NULL,
 data_json JSON NOT NULL,
 pipeline_backup_ciphertext MEDIUMTEXT NOT NULL,
 UNIQUE KEY runner_app (app_id),
 CONSTRAINT runner_app_fk FOREIGN KEY (app_id) REFERENCES apps(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE yunxiao_connections (
 id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
 token_ciphertext TEXT NOT NULL,
 data_json JSON NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
