USE deploy_platform;

-- Bounded reservations protect multi-object operator cleanup from concurrent
-- releases, while allowing another process to recover after a crash.
ALTER TABLE delivery_resource_locks
  ADD COLUMN expires_at DATETIME NULL DEFAULT NULL AFTER created_at;

CREATE INDEX idx_delivery_resource_lock_expiry
  ON delivery_resource_locks(expires_at);
