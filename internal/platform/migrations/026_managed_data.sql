-- +goose Up
CREATE TABLE IF NOT EXISTS managed_data_collections (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  connection_id TEXT NOT NULL,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active', 'archived')),
  created_by TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  archived_at TEXT,
  UNIQUE(project_id, connection_id),
  CHECK(length(id) BETWEEN 1 AND 128 AND id = trim(id)),
  CHECK(length(trim(project_id)) > 0 AND project_id = trim(project_id)),
  CHECK(length(trim(connection_id)) > 0 AND connection_id = trim(connection_id)),
  CHECK(length(trim(name)) > 0),
  CHECK((status = 'archived') = (archived_at IS NOT NULL))
);

CREATE TABLE IF NOT EXISTS managed_data_revisions (
  id TEXT PRIMARY KEY,
  collection_id TEXT NOT NULL REFERENCES managed_data_collections(id) ON DELETE RESTRICT,
  sequence INTEGER NOT NULL CHECK(sequence > 0),
  digest TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending', 'ready', 'failed')),
  manifest_json TEXT NOT NULL,
  file_count INTEGER NOT NULL CHECK(file_count >= 0),
  size_bytes INTEGER NOT NULL CHECK(size_bytes >= 0),
  created_by TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  ready_at TEXT,
  error TEXT NOT NULL DEFAULT '',
  UNIQUE(collection_id, id),
  UNIQUE(collection_id, sequence),
  UNIQUE(collection_id, digest),
  CHECK(length(digest) = 71 AND substr(digest, 1, 7) = 'sha256:'),
  CHECK(json_valid(manifest_json)),
  CHECK((status = 'ready') = (ready_at IS NOT NULL)),
  CHECK(status <> 'failed' OR length(error) > 0)
);

-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS managed_data_ready_revision_immutable
BEFORE UPDATE ON managed_data_revisions
WHEN OLD.status = 'ready'
BEGIN
  SELECT RAISE(ABORT, 'ready managed data revisions are immutable');
END;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS managed_data_revision_files (
  revision_id TEXT NOT NULL REFERENCES managed_data_revisions(id) ON DELETE CASCADE,
  logical_path TEXT NOT NULL COLLATE NOCASE,
  size_bytes INTEGER NOT NULL CHECK(size_bytes >= 0),
  sha256 TEXT NOT NULL,
  storage_key TEXT NOT NULL,
  media_type TEXT NOT NULL DEFAULT '',
  etag TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(revision_id, logical_path),
  CHECK(length(logical_path) > 0),
  CHECK(length(sha256) = 64),
  CHECK(length(storage_key) > 0)
);

CREATE TABLE IF NOT EXISTS managed_data_upload_sessions (
  id TEXT PRIMARY KEY,
  collection_id TEXT NOT NULL REFERENCES managed_data_collections(id) ON DELETE RESTRICT,
  base_revision_id TEXT,
  revision_id TEXT,
  status TEXT NOT NULL DEFAULT 'open' CHECK(status IN ('open', 'committing', 'complete', 'aborted', 'expired', 'failed')),
  manifest_json TEXT NOT NULL,
  expected_file_count INTEGER NOT NULL CHECK(expected_file_count >= 0),
  expected_size_bytes INTEGER NOT NULL CHECK(expected_size_bytes >= 0),
  uploaded_file_count INTEGER NOT NULL DEFAULT 0 CHECK(uploaded_file_count >= 0),
  uploaded_size_bytes INTEGER NOT NULL DEFAULT 0 CHECK(uploaded_size_bytes >= 0),
  storage_backend TEXT NOT NULL,
  staging_prefix TEXT NOT NULL,
  created_by TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  expires_at TEXT NOT NULL,
  completed_at TEXT,
  error TEXT NOT NULL DEFAULT '',
  FOREIGN KEY(collection_id, base_revision_id) REFERENCES managed_data_revisions(collection_id, id) ON DELETE RESTRICT,
  FOREIGN KEY(collection_id, revision_id) REFERENCES managed_data_revisions(collection_id, id) ON DELETE RESTRICT,
  CHECK(json_valid(manifest_json)),
  CHECK(length(storage_backend) > 0),
  CHECK(length(staging_prefix) > 0),
  CHECK((status = 'complete') = (revision_id IS NOT NULL)),
  CHECK((status = 'complete') = (completed_at IS NOT NULL)),
  CHECK(status <> 'failed' OR length(error) > 0)
);

CREATE INDEX IF NOT EXISTS managed_data_upload_sessions_expiry_idx
  ON managed_data_upload_sessions(status, expires_at);
CREATE INDEX IF NOT EXISTS managed_data_revisions_collection_created_idx
  ON managed_data_revisions(collection_id, sequence DESC);

CREATE TABLE IF NOT EXISTS project_deployments (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  request_digest TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending', 'active', 'failed', 'cancelled', 'superseded')),
  created_by TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  activated_at TEXT,
  error TEXT NOT NULL DEFAULT '',
  CHECK(length(trim(project_id)) > 0),
  CHECK(length(environment) BETWEEN 1 AND 128),
  CHECK(length(request_digest) > 0),
  CHECK((status IN ('active', 'superseded')) = (activated_at IS NOT NULL)),
  CHECK(status <> 'failed' OR length(error) > 0)
);

CREATE TABLE IF NOT EXISTS project_deployment_targets (
  deployment_id TEXT NOT NULL REFERENCES project_deployments(id) ON DELETE CASCADE,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  serving_state_id TEXT NOT NULL REFERENCES serving_states(id) ON DELETE RESTRICT,
  prior_serving_state_id TEXT REFERENCES serving_states(id) ON DELETE RESTRICT,
  status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending', 'active', 'failed')),
  activated_at TEXT,
  error TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(deployment_id, workspace_id),
  UNIQUE(deployment_id, serving_state_id),
  CHECK(serving_state_id <> COALESCE(prior_serving_state_id, '')),
  CHECK((status = 'active') = (activated_at IS NOT NULL)),
  CHECK(status <> 'failed' OR length(error) > 0)
);

CREATE INDEX IF NOT EXISTS project_deployment_targets_serving_state_idx
  ON project_deployment_targets(serving_state_id);

CREATE TABLE IF NOT EXISTS project_deployment_connections (
  deployment_id TEXT NOT NULL REFERENCES project_deployments(id) ON DELETE CASCADE,
  collection_id TEXT NOT NULL,
  revision_id TEXT NOT NULL,
  prior_revision_id TEXT,
  prior_generation INTEGER NOT NULL DEFAULT 0 CHECK(prior_generation >= 0),
  activated_generation INTEGER CHECK(activated_generation > 0),
  PRIMARY KEY(deployment_id, collection_id),
  FOREIGN KEY(collection_id, revision_id) REFERENCES managed_data_revisions(collection_id, id) ON DELETE RESTRICT,
  FOREIGN KEY(collection_id, prior_revision_id) REFERENCES managed_data_revisions(collection_id, id) ON DELETE RESTRICT,
  CHECK((prior_generation = 0) = (prior_revision_id IS NULL))
);

CREATE TABLE IF NOT EXISTS managed_data_environment_pointers (
  collection_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  revision_id TEXT NOT NULL,
  deployment_id TEXT NOT NULL REFERENCES project_deployments(id) ON DELETE RESTRICT,
  generation INTEGER NOT NULL CHECK(generation > 0),
  updated_by TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(collection_id, environment),
  FOREIGN KEY(collection_id, revision_id) REFERENCES managed_data_revisions(collection_id, id) ON DELETE RESTRICT,
  CHECK(length(environment) BETWEEN 1 AND 128 AND environment = trim(environment))
);

CREATE INDEX IF NOT EXISTS managed_data_environment_pointers_revision_idx
  ON managed_data_environment_pointers(revision_id);

CREATE INDEX IF NOT EXISTS project_deployments_project_environment_idx
  ON project_deployments(project_id, environment, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS managed_data_serving_state_bindings (
  project_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  generation_id TEXT NOT NULL,
  collection_id TEXT NOT NULL,
  revision_id TEXT NOT NULL,
  bound_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id, environment, generation_id, collection_id),
  FOREIGN KEY(generation_id, project_id, environment) REFERENCES serving_states(id, project_id, environment) ON DELETE CASCADE,
  FOREIGN KEY(collection_id, revision_id) REFERENCES managed_data_revisions(collection_id, id) ON DELETE RESTRICT,
  CHECK(length(environment) BETWEEN 1 AND 128 AND environment = trim(environment)),
  CHECK(length(project_id) > 0 AND project_id = trim(project_id)),
  CHECK(length(generation_id) > 0 AND generation_id = trim(generation_id)),
  CHECK(length(collection_id) > 0 AND collection_id = trim(collection_id)),
  CHECK(length(revision_id) > 0 AND revision_id = trim(revision_id))
);

CREATE INDEX IF NOT EXISTS managed_data_serving_state_bindings_revision_idx
  ON managed_data_serving_state_bindings(revision_id);

-- A serving generation's managed-data binding set is installed as one
-- immutable evidence record.  The marker also seals an intentionally empty
-- set, preventing a later request from appending bindings to the generation.
CREATE TABLE IF NOT EXISTS managed_data_serving_state_binding_sets (
  project_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  generation_id TEXT NOT NULL,
  binding_digest TEXT NOT NULL,
  binding_count INTEGER NOT NULL CHECK(binding_count >= 0),
  installed_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id, environment, generation_id),
  FOREIGN KEY(generation_id, project_id, environment) REFERENCES serving_states(id, project_id, environment) ON DELETE CASCADE,
  CHECK(length(environment) BETWEEN 1 AND 128 AND environment = trim(environment)),
  CHECK(length(project_id) > 0 AND project_id = trim(project_id)),
  CHECK(length(generation_id) > 0 AND generation_id = trim(generation_id)),
  CHECK(length(binding_digest) = 71 AND substr(binding_digest, 1, 7) = 'sha256:' AND binding_digest = lower(binding_digest))
);

-- +goose Down
DROP TABLE IF EXISTS managed_data_serving_state_binding_sets;
DROP TABLE IF EXISTS managed_data_serving_state_bindings;
DROP TABLE IF EXISTS managed_data_environment_pointers;
DROP TABLE IF EXISTS project_deployment_connections;
DROP TABLE IF EXISTS project_deployment_targets;
DROP TABLE IF EXISTS project_deployments;
DROP TABLE IF EXISTS managed_data_upload_sessions;
DROP TABLE IF EXISTS managed_data_revision_files;
DROP TRIGGER IF EXISTS managed_data_ready_revision_immutable;
DROP TABLE IF EXISTS managed_data_revisions;
DROP TABLE IF EXISTS managed_data_collections;
