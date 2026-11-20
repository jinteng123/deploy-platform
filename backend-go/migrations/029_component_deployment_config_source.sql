USE deploy_platform;

ALTER TABLE apps
 ADD COLUMN deployment_config_repo_url VARCHAR(1024) NOT NULL DEFAULT '',
 ADD COLUMN deployment_config_path VARCHAR(512) NOT NULL DEFAULT '';
