USE deploy_platform;

-- A unique claim ID distinguishes a worker's committed claim from another
-- process winning the same conditional UPDATE. Execution writes use the same
-- ID as a fencing token so stale workers cannot advance a reopened task.
ALTER TABLE deploy_logs
  ADD COLUMN worker_claim_id VARCHAR(64) DEFAULT NULL AFTER environment_tier_snapshot;
