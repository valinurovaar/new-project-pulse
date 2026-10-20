CREATE TYPE project_status AS ENUM ('UNKNOWN','HEALTHY','DEGRADED','CRITICAL','ARCHIVED');
CREATE TYPE check_type AS ENUM ('BASIC','PERFORMANCE','SECURITY','CONFIGURATION');
CREATE TYPE check_status AS ENUM ('NEW','QUEUED','RUNNING','COMPLETED','FAILED','CANCELLED');
CREATE TYPE transaction_type AS ENUM ('WELCOME_BONUS','HEALTH_REWARD','AUDIT_PAYMENT','AUDIT_REFUND');

CREATE TABLE users (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), login text NOT NULL UNIQUE,
  password_hash text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
  balance integer NOT NULL DEFAULT 0 CHECK (balance >= 0)
);
CREATE TABLE projects (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), owner_id uuid NOT NULL REFERENCES users(id),
  name varchar(100) NOT NULL, url text NOT NULL, status project_status NOT NULL DEFAULT 'UNKNOWN',
  score integer CHECK (score BETWEEN 0 AND 100), created_at timestamptz NOT NULL DEFAULT now(),
  last_checked_at timestamptz, normalized_url text NOT NULL,
  UNIQUE(owner_id, normalized_url)
);
CREATE TABLE checks (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), project_id uuid NOT NULL REFERENCES projects(id),
  type check_type NOT NULL, status check_status NOT NULL DEFAULT 'NEW', external_task_id text,
  score integer CHECK (score BETWEEN 0 AND 100), result jsonb, error text,
  created_at timestamptz NOT NULL DEFAULT now(), started_at timestamptz, completed_at timestamptz,
  next_attempt_at timestamptz NOT NULL DEFAULT now(), attempts integer NOT NULL DEFAULT 0 CONSTRAINT checks_attempts_nonnegative CHECK(attempts >= 0),
  charged_credits integer NOT NULL DEFAULT 0 CHECK (charged_credits >= 0), refunded boolean NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX checks_one_active_type ON checks(project_id,type)
  WHERE status IN ('NEW','QUEUED','RUNNING');
CREATE INDEX checks_due ON checks(next_attempt_at) WHERE status IN ('NEW','QUEUED','RUNNING');
CREATE INDEX checks_project_created ON checks(project_id,created_at DESC);

CREATE TABLE credit_transactions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL REFERENCES users(id),
  type transaction_type NOT NULL, amount integer NOT NULL, balance_after integer NOT NULL CHECK(balance_after >= 0),
  description text NOT NULL, check_id uuid REFERENCES checks(id), project_id uuid REFERENCES projects(id),
  reward_day date, created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CONSTRAINT credit_transactions_semantics CHECK (
    (type='WELCOME_BONUS' AND amount>0 AND check_id IS NULL AND project_id IS NULL AND reward_day IS NULL) OR
    (type='HEALTH_REWARD' AND amount>0 AND check_id IS NOT NULL AND project_id IS NOT NULL AND reward_day IS NOT NULL) OR
    (type='AUDIT_PAYMENT' AND amount<0 AND check_id IS NOT NULL AND project_id IS NOT NULL AND reward_day IS NULL) OR
    (type='AUDIT_REFUND' AND amount>0 AND check_id IS NOT NULL AND reward_day IS NULL)
  )
);
CREATE UNIQUE INDEX tx_single_welcome ON credit_transactions(user_id) WHERE type='WELCOME_BONUS';
CREATE UNIQUE INDEX tx_single_payment ON credit_transactions(check_id) WHERE type='AUDIT_PAYMENT';
CREATE UNIQUE INDEX tx_single_refund ON credit_transactions(check_id) WHERE type='AUDIT_REFUND';
CREATE UNIQUE INDEX tx_single_health_reward ON credit_transactions(check_id) WHERE type='HEALTH_REWARD';
CREATE UNIQUE INDEX tx_health_project_day ON credit_transactions(project_id,reward_day) WHERE type='HEALTH_REWARD';
CREATE INDEX tx_user_created ON credit_transactions(user_id,created_at DESC);
CREATE INDEX tx_health_user_day ON credit_transactions(user_id,reward_day) WHERE type='HEALTH_REWARD';

