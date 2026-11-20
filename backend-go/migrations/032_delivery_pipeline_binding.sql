USE deploy_platform;

-- A target environment may use a separate Yunxiao pipeline to apply/recover
-- the exact deployment-config revision. Keep it distinct from image-build
-- Runner bindings.
ALTER TABLE delivery_bindings
  ADD COLUMN deployment_pipeline_connection_id BIGINT NULL DEFAULT NULL,
  ADD COLUMN deployment_pipeline_id VARCHAR(20) NOT NULL DEFAULT '',
  ADD CONSTRAINT fk_delivery_binding_deployment_pipeline_connection
    FOREIGN KEY (deployment_pipeline_connection_id)
    REFERENCES yunxiao_connections(id) ON DELETE RESTRICT;
