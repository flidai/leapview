-- +goose Up

CREATE TABLE dashboard_authoring_dashboards (
  project_id TEXT NOT NULL,
  dashboard_id TEXT NOT NULL,
  owner_principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
  slug TEXT NOT NULL,
  title TEXT NOT NULL,
  semantic_model TEXT NOT NULL,
  visibility TEXT NOT NULL CHECK (visibility IN ('private', 'restricted', 'organization')),
  status TEXT NOT NULL CHECK (status IN ('draft', 'published', 'archived')),
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (project_id, dashboard_id),
  UNIQUE (project_id, slug),
  CHECK (length(trim(dashboard_id)) > 0),
  CHECK (length(trim(slug)) > 0),
  CHECK (length(slug) <= 128),
  CHECK (slug NOT GLOB '*[^a-z0-9-]*'),
  CHECK (substr(slug, 1, 1) GLOB '[a-z0-9]'),
  CHECK (length(trim(title)) > 0),
  CHECK (length(trim(semantic_model)) > 0)
);

CREATE TABLE dashboard_authoring_revisions (
  project_id TEXT NOT NULL,
  dashboard_id TEXT NOT NULL,
  revision_id TEXT NOT NULL,
  revision_number INTEGER NOT NULL CHECK (revision_number > 0),
  document_json TEXT NOT NULL CHECK (length(trim(document_json)) > 0),
  content_hash TEXT NOT NULL CHECK (length(content_hash) = 71 AND substr(content_hash, 1, 7) = 'sha256:' AND substr(content_hash, 8) NOT GLOB '*[^0-9a-f]*'),
  provenance_json TEXT NOT NULL CHECK (length(trim(provenance_json)) > 0),
  created_at TEXT NOT NULL,
  PRIMARY KEY (project_id, dashboard_id, revision_id),
  UNIQUE (project_id, dashboard_id, revision_number),
  UNIQUE (project_id, dashboard_id, revision_id, revision_number, content_hash),
  FOREIGN KEY (project_id, dashboard_id)
    REFERENCES dashboard_authoring_dashboards(project_id, dashboard_id)
    ON DELETE CASCADE
);

CREATE TABLE dashboard_authoring_drafts (
  project_id TEXT NOT NULL,
  dashboard_id TEXT NOT NULL,
  draft_id TEXT NOT NULL,
  revision_id TEXT NOT NULL,
  revision_number INTEGER NOT NULL CHECK (revision_number > 0),
  content_hash TEXT NOT NULL,
  provenance_json TEXT NOT NULL CHECK (length(trim(provenance_json)) > 0),
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (project_id, dashboard_id),
  UNIQUE (project_id, dashboard_id, draft_id),
  FOREIGN KEY (project_id, dashboard_id, revision_id, revision_number, content_hash)
    REFERENCES dashboard_authoring_revisions(project_id, dashboard_id, revision_id, revision_number, content_hash)
    ON DELETE RESTRICT,
  FOREIGN KEY (project_id, dashboard_id)
    REFERENCES dashboard_authoring_dashboards(project_id, dashboard_id)
    ON DELETE CASCADE
);

-- A compiled artifact is immutable and keyed by the complete compilation
-- token (authored revision plus definition hash and semantic serving state).
-- Reusing that key with a different payload is a conflict, never an update.
CREATE TABLE dashboard_authoring_compiled_revisions (
  project_id TEXT NOT NULL,
  dashboard_id TEXT NOT NULL,
  revision_id TEXT NOT NULL,
  revision_number INTEGER NOT NULL CHECK (revision_number > 0),
  content_hash TEXT NOT NULL CHECK (length(content_hash) = 71 AND substr(content_hash, 1, 7) = 'sha256:' AND substr(content_hash, 8) NOT GLOB '*[^0-9a-f]*'),
  definition_json TEXT NOT NULL CHECK (length(trim(definition_json)) > 0),
  definition_hash TEXT NOT NULL CHECK (length(definition_hash) = 71 AND substr(definition_hash, 1, 7) = 'sha256:' AND substr(definition_hash, 8) NOT GLOB '*[^0-9a-f]*'),
  semantic_identity_json TEXT NOT NULL CHECK (length(trim(semantic_identity_json)) > 0),
  compiled_at TEXT NOT NULL,
  PRIMARY KEY (project_id, dashboard_id, revision_id, revision_number, content_hash, definition_hash, semantic_identity_json),
  FOREIGN KEY (project_id, dashboard_id, revision_id, revision_number, content_hash)
    REFERENCES dashboard_authoring_revisions(project_id, dashboard_id, revision_id, revision_number, content_hash)
    ON DELETE RESTRICT,
  FOREIGN KEY (project_id, dashboard_id)
    REFERENCES dashboard_authoring_dashboards(project_id, dashboard_id)
    ON DELETE CASCADE
);

CREATE TABLE dashboard_authoring_published (
  project_id TEXT NOT NULL,
  dashboard_id TEXT NOT NULL,
  revision_id TEXT NOT NULL,
  revision_number INTEGER NOT NULL CHECK (revision_number > 0),
  content_hash TEXT NOT NULL,
  compiled_revision_id TEXT NOT NULL,
  compiled_revision_number INTEGER NOT NULL CHECK (compiled_revision_number > 0),
  compiled_content_hash TEXT NOT NULL,
  compiled_definition_hash TEXT NOT NULL CHECK (length(compiled_definition_hash) = 71 AND substr(compiled_definition_hash, 1, 7) = 'sha256:' AND substr(compiled_definition_hash, 8) NOT GLOB '*[^0-9a-f]*'),
  compiled_semantic_identity_json TEXT NOT NULL CHECK (length(trim(compiled_semantic_identity_json)) > 0),
  provenance_json TEXT NOT NULL CHECK (length(trim(provenance_json)) > 0),
  published_at TEXT NOT NULL,
  PRIMARY KEY (project_id, dashboard_id),
  FOREIGN KEY (project_id, dashboard_id, revision_id, revision_number, content_hash)
    REFERENCES dashboard_authoring_revisions(project_id, dashboard_id, revision_id, revision_number, content_hash)
    ON DELETE RESTRICT,
  FOREIGN KEY (project_id, dashboard_id, compiled_revision_id, compiled_revision_number, compiled_content_hash, compiled_definition_hash, compiled_semantic_identity_json)
    REFERENCES dashboard_authoring_compiled_revisions(project_id, dashboard_id, revision_id, revision_number, content_hash, definition_hash, semantic_identity_json)
    ON DELETE RESTRICT,
  FOREIGN KEY (project_id, dashboard_id)
    REFERENCES dashboard_authoring_dashboards(project_id, dashboard_id)
    ON DELETE CASCADE,
  CHECK (revision_id = compiled_revision_id AND revision_number = compiled_revision_number AND content_hash = compiled_content_hash)
);

CREATE TABLE dashboard_authoring_commands (
  project_id TEXT NOT NULL,
  dashboard_id TEXT NOT NULL,
  command_id TEXT NOT NULL,
  request_fingerprint TEXT NOT NULL,
  action TEXT NOT NULL CHECK (action IN ('edit', 'publish', 'archive')),
  provenance_json TEXT NOT NULL CHECK (length(trim(provenance_json)) > 0),
  occurred_at TEXT NOT NULL,
  result_revision_id TEXT,
  result_revision_number INTEGER,
  result_content_hash TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (project_id, dashboard_id, command_id),
  FOREIGN KEY (project_id, dashboard_id)
    REFERENCES dashboard_authoring_dashboards(project_id, dashboard_id)
    ON DELETE CASCADE,
  FOREIGN KEY (project_id, dashboard_id, result_revision_id, result_revision_number, result_content_hash)
    REFERENCES dashboard_authoring_revisions(project_id, dashboard_id, revision_id, revision_number, content_hash)
    ON DELETE RESTRICT,
  CHECK ((result_revision_id IS NULL AND result_revision_number IS NULL AND result_content_hash IS NULL)
      OR (result_revision_id IS NOT NULL AND result_revision_number > 0 AND length(trim(result_content_hash)) > 0))
);

CREATE INDEX dashboard_authoring_dashboards_project_idx
  ON dashboard_authoring_dashboards(project_id, semantic_model, status, visibility, slug, dashboard_id);

CREATE INDEX dashboard_authoring_revisions_project_idx
  ON dashboard_authoring_revisions(project_id, dashboard_id, revision_number);

CREATE INDEX dashboard_authoring_compiled_project_idx
  ON dashboard_authoring_compiled_revisions(project_id, dashboard_id, revision_number);

-- +goose Down

DROP INDEX dashboard_authoring_compiled_project_idx;
DROP INDEX dashboard_authoring_revisions_project_idx;
DROP INDEX dashboard_authoring_dashboards_project_idx;
DROP TABLE dashboard_authoring_commands;
DROP TABLE dashboard_authoring_published;
DROP TABLE dashboard_authoring_compiled_revisions;
DROP TABLE dashboard_authoring_drafts;
DROP TABLE dashboard_authoring_revisions;
DROP TABLE dashboard_authoring_dashboards;
