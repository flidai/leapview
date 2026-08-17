-- +goose Up

CREATE TABLE target_connection_bindings (
  id TEXT PRIMARY KEY,
  target_id TEXT NOT NULL,
  connection_id TEXT NOT NULL,
  connector_kind TEXT NOT NULL,
  authentication_mode TEXT NOT NULL
    CHECK (authentication_mode IN ('none', 'external_bundle', 'workload_identity')),
  project_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  endpoint_json TEXT NOT NULL,
  credential_project_id TEXT NOT NULL DEFAULT '',
  credential_environment TEXT NOT NULL DEFAULT '',
  credential_secret_path TEXT NOT NULL DEFAULT '',
  credential_secret_key TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
  validated_version TEXT NOT NULL DEFAULT '',
  health TEXT NOT NULL CHECK (health IN ('pending', 'healthy', 'degraded', 'disabled')),
  health_reason TEXT NOT NULL DEFAULT '',
  last_validated_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  revision INTEGER NOT NULL CHECK (revision > 0),
  CHECK(project_id = trim(project_id) AND length(project_id) > 0),
  CHECK(connection_id = trim(connection_id) AND length(connection_id) > 0),
  CHECK(target_id = trim(target_id) AND length(target_id) > 0)
);

CREATE UNIQUE INDEX target_connection_bindings_scope_idx
  ON target_connection_bindings(target_id, project_id, environment, connection_id);

CREATE INDEX target_connection_bindings_health_idx
  ON target_connection_bindings(target_id, environment, health, updated_at DESC);

-- +goose Down

DROP INDEX target_connection_bindings_health_idx;
DROP INDEX target_connection_bindings_scope_idx;
DROP TABLE target_connection_bindings;
