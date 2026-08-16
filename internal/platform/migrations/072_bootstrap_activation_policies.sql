-- +goose Up

-- A permanent one-shot binding for the first protected activation. The row is
-- deliberately not consumed: retries of the bound deployment remain safe,
-- while a different deployment can never reuse the bootstrap authorization.
CREATE TABLE bootstrap_activation_policies (
  deployment_id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  request_digest TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  credential_id TEXT NOT NULL,
  credential_expires_at TEXT NOT NULL,
  armed_at TEXT NOT NULL
);

CREATE UNIQUE INDEX bootstrap_activation_policy_scope_idx
  ON bootstrap_activation_policies(project_id, environment);

-- +goose Down

DROP INDEX bootstrap_activation_policy_scope_idx;
DROP TABLE bootstrap_activation_policies;
