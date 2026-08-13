-- +goose Up
ALTER TABLE serving_states ADD COLUMN dashboard_appearances_json TEXT NOT NULL DEFAULT '{}';

CREATE TABLE dashboard_appearances (
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  dashboard_id TEXT NOT NULL,
  project_id TEXT NOT NULL DEFAULT '',
  icon TEXT,
  color TEXT,
  revision INTEGER NOT NULL DEFAULT 1 CHECK(revision > 0),
  updated_by TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (workspace_id, dashboard_id),
  CHECK(icon IS NULL OR length(trim(icon)) > 0),
  CHECK(color IS NULL OR color IN ('gray', 'blue', 'green', 'yellow', 'orange', 'red', 'purple', 'pink', 'coral'))
);

-- +goose Down
DROP TABLE IF EXISTS dashboard_appearances;
ALTER TABLE serving_states DROP COLUMN dashboard_appearances_json;
