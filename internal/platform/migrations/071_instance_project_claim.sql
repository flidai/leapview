-- +goose Up

-- One immutable project/environment binding per platform database instance.
-- The singleton key is deliberately explicit so this table cannot grow a
-- second claim without a schema change.
CREATE TABLE instance_project_claim (
  singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
  project_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  claimed_by TEXT NOT NULL,
  claimed_at TEXT NOT NULL
);

-- +goose Down

DROP TABLE instance_project_claim;
