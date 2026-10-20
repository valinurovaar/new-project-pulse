ALTER TABLE checks DROP CONSTRAINT IF EXISTS checks_attempts_nonnegative;
ALTER TABLE checks ADD CONSTRAINT checks_attempts_nonnegative CHECK(attempts >= 0);

ALTER TABLE credit_transactions DROP CONSTRAINT IF EXISTS credit_transactions_semantics;
ALTER TABLE credit_transactions ADD CONSTRAINT credit_transactions_semantics CHECK (
  (type='WELCOME_BONUS' AND amount>0 AND check_id IS NULL AND project_id IS NULL AND reward_day IS NULL) OR
  (type='HEALTH_REWARD' AND amount>0 AND check_id IS NOT NULL AND project_id IS NOT NULL AND reward_day IS NOT NULL) OR
  (type='AUDIT_PAYMENT' AND amount<0 AND check_id IS NOT NULL AND project_id IS NOT NULL AND reward_day IS NULL) OR
  (type='AUDIT_REFUND' AND amount>0 AND check_id IS NOT NULL AND reward_day IS NULL)
);
ALTER TABLE credit_transactions ALTER COLUMN created_at SET DEFAULT clock_timestamp();

CREATE INDEX IF NOT EXISTS tx_health_user_day
  ON credit_transactions(user_id,reward_day)
  WHERE type='HEALTH_REWARD';
