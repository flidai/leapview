-- +goose Up
-- Immutable project role assignments captured alongside one serving
-- generation. Role capability bundles are persisted on the binding so later
-- edits to mutable role templates cannot alter an installed generation.
CREATE TABLE IF NOT EXISTS authorization_role_bindings (
  id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  generation_id TEXT NOT NULL,
  subject_kind TEXT NOT NULL CHECK(subject_kind IN ('principal', 'group')),
  subject_id TEXT NOT NULL,
  role TEXT NOT NULL,
  capabilities_json TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id, environment, generation_id, id),
  FOREIGN KEY(project_id, environment, generation_id)
    REFERENCES authorization_snapshots(project_id, environment, generation_id)
    ON DELETE CASCADE,
  UNIQUE(project_id, environment, generation_id, subject_kind, subject_id, role)
);

CREATE INDEX IF NOT EXISTS authorization_role_bindings_subject_idx
  ON authorization_role_bindings(project_id, environment, generation_id, subject_kind, subject_id);

CREATE TABLE IF NOT EXISTS authorization_audit_events (
  -- These three columns are the complete immutable ServingIdentity evidence
  -- for the event. They intentionally are not foreign keys: audit history
  -- must survive serving-generation retention and snapshot deletion.
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  generation_id TEXT NOT NULL,
  principal_id TEXT NOT NULL,
  action TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  resource_kind TEXT NOT NULL,
  capability TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT '',
  request_id TEXT NOT NULL DEFAULT '',
  correlation_id TEXT NOT NULL DEFAULT '',
  metadata_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS authorization_audit_events_scope_idx
  ON authorization_audit_events(project_id, environment, generation_id, created_at DESC, id);

-- +goose Down
DROP INDEX IF EXISTS authorization_role_bindings_subject_idx;
DROP TABLE IF EXISTS authorization_role_bindings;
DROP INDEX IF EXISTS authorization_audit_events_scope_idx;
DROP TABLE IF EXISTS authorization_audit_events;
