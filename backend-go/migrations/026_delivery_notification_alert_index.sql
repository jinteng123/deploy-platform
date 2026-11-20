USE deploy_platform;
ALTER TABLE delivery_notifications
 ADD INDEX idx_notification_alerts (status,created_at,id);
