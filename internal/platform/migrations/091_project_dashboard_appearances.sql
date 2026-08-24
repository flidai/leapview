-- +goose Up
CREATE TABLE IF NOT EXISTS project_dashboard_appearances (
  project_id TEXT NOT NULL,
  dashboard_id TEXT NOT NULL,
  icon TEXT,
  color TEXT,
  revision INTEGER NOT NULL DEFAULT 1 CHECK(revision > 0),
  updated_by TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (project_id, dashboard_id),
  CHECK(icon IS NULL OR length(trim(icon)) > 0),
  CHECK(color IS NULL OR color IN ('gray', 'blue', 'green', 'yellow', 'orange', 'red', 'purple', 'pink', 'coral'))
);

-- +goose Down
DROP TABLE IF EXISTS project_dashboard_appearances;
