USE deploy_platform;

-- Blue-green plans must identify the exact existing Service entry they intend
-- to switch. This records configuration only; no Kubernetes resource changes.
ALTER TABLE delivery_bindings
  ADD COLUMN service_name VARCHAR(253) NOT NULL DEFAULT '' AFTER strategy_draft;
