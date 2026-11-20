USE deploy_platform;

-- The Worker polls pending work by state and monotonically increasing ID.
-- Keep queue scans bounded without walking the full deployment history.
ALTER TABLE deploy_logs
  ADD INDEX idx_deploy_log_pending_scan (status, id);
