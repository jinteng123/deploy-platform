USE deploy_platform;
CREATE TABLE IF NOT EXISTS delivery_notifications (
 id VARCHAR(128) NOT NULL PRIMARY KEY,
 event VARCHAR(64) NOT NULL,
 payload JSON NOT NULL,
 status VARCHAR(16) NOT NULL DEFAULT 'pending',
 attempts INT NOT NULL DEFAULT 0,
 next_attempt_at DATETIME NOT NULL,
 created_at DATETIME NOT NULL,
 last_error VARCHAR(128) NOT NULL DEFAULT '',
 INDEX idx_notification_due (status,next_attempt_at)
);
