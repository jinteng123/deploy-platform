USE deploy_platform;

-- Typed, bounded event snapshots let progressive strategies recover their
-- exact stage/observation state after a restart without adding CRDs.
ALTER TABLE delivery_run_events
  ADD COLUMN payload_json JSON NULL AFTER detail;
