-- +goose Up
-- Canonical project-generation capability grants. Resource identity is the
-- graph ResourceID plus kind; no workspace, path, domain, or parent scope is
-- persisted in authorization.

ALTER TABLE principals ADD COLUMN kind TEXT NOT NULL DEFAULT 'user';

CREATE TABLE IF NOT EXISTS role_grant_templates (
  role_name TEXT NOT NULL,
  capability TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(role_name, capability)
);

CREATE TABLE IF NOT EXISTS authorization_snapshots (
  project_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  generation_id TEXT NOT NULL,
  digest TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id, environment, generation_id),
  FOREIGN KEY(generation_id, project_id, environment)
    REFERENCES serving_states(id, project_id, environment)
    ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS authorization_grants (
  id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  generation_id TEXT NOT NULL,
  subject_kind TEXT NOT NULL CHECK(subject_kind IN ('principal', 'group')),
  subject_id TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  resource_kind TEXT NOT NULL,
  capability TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id, environment, generation_id, id),
  FOREIGN KEY(project_id, environment, generation_id)
    REFERENCES authorization_snapshots(project_id, environment, generation_id)
    ON DELETE CASCADE,
  UNIQUE(project_id, environment, generation_id, subject_kind, subject_id, resource_id, capability)
);
CREATE INDEX IF NOT EXISTS authorization_grants_subject_idx
  ON authorization_grants(project_id, environment, generation_id, subject_kind, subject_id);
CREATE INDEX IF NOT EXISTS authorization_grants_resource_idx
  ON authorization_grants(project_id, environment, generation_id, resource_id, capability);

CREATE TABLE IF NOT EXISTS authorization_data_policies (
  id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  generation_id TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  resource_kind TEXT NOT NULL,
  subject_kind TEXT CHECK(subject_kind IS NULL OR subject_kind IN ('principal', 'group')),
  subject_id TEXT,
  policy_type TEXT NOT NULL,
  expression_json TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id, environment, generation_id, id),
  CHECK ((subject_kind IS NULL AND subject_id IS NULL) OR (subject_kind IS NOT NULL AND subject_id IS NOT NULL)),
  FOREIGN KEY(project_id, environment, generation_id)
    REFERENCES authorization_snapshots(project_id, environment, generation_id)
    ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS authorization_data_policies_resource_idx
  ON authorization_data_policies(project_id, environment, generation_id, resource_id);

-- +goose Down
DROP INDEX IF EXISTS authorization_data_policies_resource_idx;
DROP TABLE IF EXISTS authorization_data_policies;
DROP INDEX IF EXISTS authorization_grants_resource_idx;
DROP INDEX IF EXISTS authorization_grants_subject_idx;
DROP TABLE IF EXISTS authorization_grants;
DROP TABLE IF EXISTS authorization_snapshots;
DROP TABLE IF EXISTS role_grant_templates;
