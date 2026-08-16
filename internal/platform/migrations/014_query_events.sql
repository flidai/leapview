-- +goose Up
CREATE TABLE IF NOT EXISTS query_events (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL DEFAULT '',
  principal_id TEXT NOT NULL DEFAULT '',
  surface TEXT NOT NULL DEFAULT '',
  operation TEXT NOT NULL DEFAULT '',
  query_kind TEXT NOT NULL DEFAULT '',
  model_id TEXT NOT NULL DEFAULT '',
  target TEXT NOT NULL DEFAULT '',
  object_type TEXT NOT NULL DEFAULT '',
  object_id TEXT NOT NULL DEFAULT '',
  request_id TEXT NOT NULL DEFAULT '',
  correlation_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '',
  duration_ms INTEGER NOT NULL DEFAULT 0,
  rows_returned INTEGER NOT NULL DEFAULT 0,
  bytes_estimate INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  sql_text TEXT NOT NULL DEFAULT '',
  plan_text TEXT NOT NULL DEFAULT '',
  query_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS query_events_project_created_idx ON query_events(project_id, created_at DESC);
CREATE INDEX IF NOT EXISTS query_events_principal_created_idx ON query_events(principal_id, created_at DESC);
CREATE INDEX IF NOT EXISTS query_events_surface_created_idx ON query_events(surface, created_at DESC);
CREATE INDEX IF NOT EXISTS query_events_status_created_idx ON query_events(status, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS query_events_status_created_idx;
DROP INDEX IF EXISTS query_events_surface_created_idx;
DROP INDEX IF EXISTS query_events_principal_created_idx;
DROP INDEX IF EXISTS query_events_project_created_idx;
DROP TABLE IF EXISTS query_events;
