USE deploy_platform;

-- Optional for legacy mappings; new guided onboarding writes explicit sources.
ALTER TABLE apps
 ADD COLUMN source_repo_url VARCHAR(1024) NOT NULL DEFAULT '',
 ADD COLUMN source_path VARCHAR(512) NOT NULL DEFAULT '',
 ADD COLUMN image_repository VARCHAR(512) NOT NULL DEFAULT '';
