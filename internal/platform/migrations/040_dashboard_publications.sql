-- +goose Up
-- Dashboard publication metadata is unchanged; serving-state publication JSON
-- is canonical in the baseline.

CREATE TABLE dashboard_publications (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  public_id TEXT NOT NULL UNIQUE,
  dashboard TEXT NOT NULL DEFAULT '',
  default_page TEXT NOT NULL DEFAULT '',
  configuration_digest TEXT NOT NULL DEFAULT '',
  allowed_origins_json TEXT NOT NULL DEFAULT '[]',
  dependency_asset_ids_json TEXT NOT NULL DEFAULT '[]',
  configured INTEGER NOT NULL DEFAULT 0 CHECK (configured IN (0, 1)),
  active_serving_state_id TEXT REFERENCES serving_states(id) ON DELETE SET NULL,
  suspended_at TEXT,
  suspended_by TEXT NOT NULL DEFAULT '',
  configured_at TEXT,
  disabled_at TEXT,
  rotated_at TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, name)
);

CREATE INDEX dashboard_publications_project_idx
  ON dashboard_publications(project_id, configured, name);

CREATE TABLE dashboard_publication_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  publication_id TEXT NOT NULL REFERENCES dashboard_publications(id) ON DELETE CASCADE,
  event_type TEXT NOT NULL CHECK (event_type IN ('configured', 'configuration_changed', 'disabled', 'suspended', 'resumed', 'rotated')),
  actor_id TEXT NOT NULL DEFAULT '',
  serving_state_id TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX dashboard_publication_events_publication_idx
  ON dashboard_publication_events(publication_id, id DESC);

CREATE TABLE dashboard_publication_streams (
  publication_id TEXT NOT NULL REFERENCES dashboard_publications(id) ON DELETE CASCADE,
  stream_id TEXT NOT NULL,
  public_id TEXT NOT NULL,
  serving_state_id TEXT NOT NULL,
  registration_id TEXT NOT NULL,
  filters_json TEXT NOT NULL DEFAULT '{"controls":{},"selections":[]}',
  generation INTEGER NOT NULL DEFAULT 1,
  expires_at TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(publication_id, stream_id)
);

CREATE INDEX dashboard_publication_streams_expiry_idx
  ON dashboard_publication_streams(expires_at);

CREATE TABLE dashboard_publication_stream_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  stream_id TEXT NOT NULL,
  envelope_json TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE INDEX dashboard_publication_stream_events_stream_idx
  ON dashboard_publication_stream_events(stream_id, id);

-- +goose Down
DROP INDEX dashboard_publication_stream_events_stream_idx;
DROP TABLE dashboard_publication_stream_events;
DROP INDEX dashboard_publication_streams_expiry_idx;
DROP TABLE dashboard_publication_streams;
DROP INDEX dashboard_publication_events_publication_idx;
DROP TABLE dashboard_publication_events;
DROP INDEX dashboard_publications_project_idx;
DROP TABLE dashboard_publications;
