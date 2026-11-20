USE deploy_platform;

-- Historical cluster metadata is mutable. Preserve the target's tier on each
-- newly-created deployment log so reporting never reclassifies old attempts.
-- Existing rows remain NULL because their creation-time tier cannot be proven.
ALTER TABLE deploy_logs
  ADD COLUMN environment_tier_snapshot VARCHAR(32) NULL DEFAULT NULL
  COMMENT '任务创建时的目标环境等级；历史未知保持 NULL';
