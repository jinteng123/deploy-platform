USE deploy_platform;

-- Migration 010 may have been run by a mysql client using latin1 while the
-- SQL file was UTF-8. Only repair the exact generated legacy name; a reviewed
-- or manually edited environment name must remain untouched. Hex literals
-- make this data repair independent of the client's character set.
UPDATE delivery_environments AS environment
JOIN clusters AS cluster ON cluster.id = environment.legacy_cluster_id
SET environment.name = LEFT(CONCAT(
  CONVERT(0xE5BE85E7A1AEE8AEA420C2B720 USING utf8mb4),
  cluster.environment, ' / ', cluster.name
), 128)
WHERE environment.code = CONCAT('legacy-cluster-', cluster.id)
  AND environment.requires_review = 1
  AND BINARY environment.name = BINARY LEFT(CONCAT(
    CONVERT(0xC3A5C2BEE280A6C3A7C2A1C2AEC3A8C2AEC2A420C382C2B720 USING utf8mb4),
    cluster.environment, ' / ', cluster.name
  ), 128);
