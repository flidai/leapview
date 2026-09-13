-- +goose Up

-- Instance-wide daily Agent model request usage. SQLite date('now') is UTC;
-- reservation queries roll the row forward when the database date advances.
CREATE TABLE IF NOT EXISTS agent_model_request_usage (
  singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
  usage_day TEXT NOT NULL,
  used_requests INTEGER NOT NULL DEFAULT 0 CHECK (used_requests >= 0)
);

INSERT INTO agent_model_request_usage (singleton_id, usage_day, used_requests)
VALUES (1, date('now'), 0)
ON CONFLICT(singleton_id) DO NOTHING;

-- +goose Down

DROP TABLE agent_model_request_usage;
