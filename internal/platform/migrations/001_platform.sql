-- +goose Up
-- LeapView v1 platform control-plane schema.
-- Runtime migration and sqlc generation both use this migration chain as the
-- authoritative SQLite schema source.

CREATE TABLE IF NOT EXISTS workspaces (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS serving_states (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  environment TEXT NOT NULL DEFAULT 'dev',
  status TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT 'publish',
  digest TEXT NOT NULL DEFAULT '',
  manifest_json TEXT NOT NULL DEFAULT '{}',
  project_digest TEXT NOT NULL DEFAULT '',
  access_policy_json TEXT NOT NULL DEFAULT '{}',
  dashboard_publications_json TEXT NOT NULL DEFAULT '{}',
  dashboard_appearances_json TEXT NOT NULL DEFAULT '{}',
  ducklake_snapshot_id INTEGER NOT NULL DEFAULT 0,
  created_by TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  activated_at TEXT,
  superseded_at TEXT,
  error TEXT NOT NULL DEFAULT '',
  UNIQUE(id, project_id, environment)
);

CREATE TABLE IF NOT EXISTS project_active_serving_states (
  project_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  serving_state_id TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id, environment),
  FOREIGN KEY(serving_state_id, project_id, environment)
    REFERENCES serving_states(id, project_id, environment) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS serving_state_artifacts (
  id TEXT PRIMARY KEY,
  serving_state_id TEXT NOT NULL UNIQUE REFERENCES serving_states(id) ON DELETE CASCADE,
  digest TEXT NOT NULL,
  format TEXT NOT NULL,
  path TEXT NOT NULL,
  manifest_json TEXT NOT NULL DEFAULT '{}',
  size_bytes INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS assets (
  snapshot_id TEXT PRIMARY KEY,
  logical_asset_id TEXT NOT NULL,
  serving_state_id TEXT NOT NULL REFERENCES serving_states(id) ON DELETE CASCADE,
  asset_type TEXT NOT NULL,
  asset_key TEXT NOT NULL,
  parent_logical_asset_id TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  payload_schema TEXT NOT NULL,
  payload_json TEXT NOT NULL DEFAULT '{}',
  content_hash TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(serving_state_id, logical_asset_id)
);

CREATE TABLE IF NOT EXISTS asset_edges (
  id TEXT PRIMARY KEY,
  serving_state_id TEXT NOT NULL REFERENCES serving_states(id) ON DELETE CASCADE,
  from_logical_asset_id TEXT NOT NULL,
  to_logical_asset_id TEXT NOT NULL,
  edge_type TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS query_snapshot_leases (
  id TEXT PRIMARY KEY,
  serving_state_id TEXT NOT NULL REFERENCES serving_states(id) ON DELETE CASCADE,
  ducklake_snapshot_id INTEGER NOT NULL,
  owner_id TEXT NOT NULL DEFAULT '',
  acquired_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  expires_at TEXT NOT NULL,
  released_at TEXT
);

CREATE INDEX IF NOT EXISTS query_snapshot_leases_live_idx
  ON query_snapshot_leases(ducklake_snapshot_id, expires_at)
  WHERE released_at IS NULL;

CREATE TABLE IF NOT EXISTS principals (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL DEFAULT '',
  display_name TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS external_identities (
  id TEXT PRIMARY KEY,
  principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  provider TEXT NOT NULL,
  tenant_id TEXT NOT NULL DEFAULT '',
  subject TEXT NOT NULL,
  email TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(provider, tenant_id, subject)
);

CREATE TABLE IF NOT EXISTS groups (
  id TEXT PRIMARY KEY,
  provider TEXT NOT NULL DEFAULT '',
  external_id TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(provider, external_id)
);

CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  token_fingerprint TEXT NOT NULL UNIQUE,
  token_verifier TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_seen_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS oauth_states (
  id TEXT PRIMARY KEY,
  state_hash TEXT NOT NULL UNIQUE,
  redirect_url TEXT NOT NULL DEFAULT '/',
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS api_tokens (
  id TEXT PRIMARY KEY,
  principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  token_fingerprint TEXT NOT NULL UNIQUE,
  token_verifier TEXT NOT NULL,
  expires_at TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_used_at TEXT
);

CREATE TABLE IF NOT EXISTS refresh_jobs (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  serving_state_id TEXT REFERENCES serving_states(id) ON DELETE SET NULL,
  model_id TEXT NOT NULL,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS refresh_job_runs (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL REFERENCES refresh_jobs(id) ON DELETE CASCADE,
  status TEXT NOT NULL,
  started_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  finished_at TEXT,
  error TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS audit_events (
  id TEXT PRIMARY KEY,
  project_id TEXT,
  principal_id TEXT REFERENCES principals(id) ON DELETE SET NULL,
  action TEXT NOT NULL,
  resource_id TEXT NOT NULL DEFAULT '',
  resource_kind TEXT NOT NULL DEFAULT '',
  capability TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '',
  request_id TEXT NOT NULL DEFAULT '',
  correlation_id TEXT NOT NULL DEFAULT '',
  metadata_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS serving_states_project_environment_created_idx ON serving_states(project_id, environment, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS serving_states_active_project_environment_idx
  ON serving_states(project_id, environment)
  WHERE status = 'active';
CREATE INDEX IF NOT EXISTS assets_serving_state_type_idx ON assets(serving_state_id, asset_type);
CREATE INDEX IF NOT EXISTS assets_serving_state_logical_idx ON assets(serving_state_id, logical_asset_id);
CREATE UNIQUE INDEX IF NOT EXISTS asset_edges_unique_idx
  ON asset_edges(serving_state_id, from_logical_asset_id, to_logical_asset_id, edge_type);
CREATE TABLE IF NOT EXISTS platform_role_bindings (
  id TEXT PRIMARY KEY,
  role TEXT NOT NULL CHECK(role = 'platform_admin'),
  principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(principal_id)
);
