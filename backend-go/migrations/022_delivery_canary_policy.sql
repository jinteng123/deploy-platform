USE deploy_platform;

-- Canary analysis thresholds and explicitly ordered traffic stages belong to
-- the immutable strategy revision and later approval snapshots. The nested
-- JSON contract can evolve compatibly without another column. Legacy drafts
-- remain readable with NULL/incomplete configuration and stay non-executable.
ALTER TABLE delivery_policy_versions
  ADD COLUMN canary_analysis_json JSON NULL AFTER candidate_retention_hours;
