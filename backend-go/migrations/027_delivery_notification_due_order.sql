USE deploy_platform;
ALTER TABLE delivery_notifications
 ADD INDEX idx_notification_due_order (status,next_attempt_at,created_at,id);
