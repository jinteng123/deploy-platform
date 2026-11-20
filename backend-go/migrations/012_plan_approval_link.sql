USE deploy_platform;

-- Link the existing human approval and rolling deployment path to the exact
-- immutable preview that the operator reviewed. Existing requests stay NULL.
ALTER TABLE promotion_requests
  ADD COLUMN delivery_plan_id BIGINT UNSIGNED DEFAULT NULL AFTER release_id,
  ADD KEY idx_promotion_delivery_plan (delivery_plan_id),
  ADD CONSTRAINT fk_promotion_delivery_plan FOREIGN KEY (delivery_plan_id)
    REFERENCES delivery_plans(id) ON DELETE RESTRICT;
