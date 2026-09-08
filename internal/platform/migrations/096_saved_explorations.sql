-- +goose Up

-- FAI-656: durable saved-exploration identity and authored history. The
-- authored payload is the canonical, versioned ExplorationSpec envelope only;
-- query results, SQL/plans, transient UI state, compiled visuals, and Git
-- configuration are intentionally outside this schema.
--
-- Both the lifecycle row and its first revision are inserted by one mutation
-- transaction. The deferred foreign keys permit either insertion order while
-- requiring the exact current-revision tuple before that transaction commits.
CREATE TABLE saved_explorations (
  project_id TEXT NOT NULL
    CHECK (length(project_id) >= 1
      AND project_id = trim(project_id)
      AND project_id GLOB '[A-Za-z0-9]*'
      AND project_id NOT GLOB '*[^A-Za-z0-9_.:-]*'),
  exploration_id TEXT NOT NULL
    CHECK (length(exploration_id) BETWEEN 1 AND 128
      AND exploration_id = trim(exploration_id)
      AND exploration_id GLOB '[A-Za-z0-9]*'
      AND exploration_id NOT GLOB '*[^A-Za-z0-9_.:-]*'),
  owner_principal_id TEXT NOT NULL
    REFERENCES principals(id) ON DELETE RESTRICT
    CHECK (length(owner_principal_id) BETWEEN 1 AND 256
      AND owner_principal_id = trim(owner_principal_id)
      AND instr(owner_principal_id, char(0)) = 0
      AND instr(owner_principal_id, char(9)) = 0
      AND instr(owner_principal_id, char(10)) = 0
      AND instr(owner_principal_id, char(13)) = 0),
  title TEXT NOT NULL
    CHECK (length(title) BETWEEN 1 AND 200
      AND title = trim(title)
      AND instr(title, char(0)) = 0
      AND instr(title, char(9)) = 0
      AND instr(title, char(10)) = 0
      AND instr(title, char(13)) = 0),
  slug TEXT NOT NULL
    CHECK (length(slug) BETWEEN 1 AND 128
      AND slug = trim(slug)
      AND slug GLOB '[a-z0-9]*'
      AND slug NOT GLOB '*[^a-z0-9-]*'),
  visibility TEXT NOT NULL
    CHECK (visibility IN ('private', 'restricted', 'organization')),
  status TEXT NOT NULL
    CHECK (status IN ('active', 'archived')),
  semantic_model_id TEXT NOT NULL
    CHECK (length(semantic_model_id) >= 1
      AND semantic_model_id = trim(semantic_model_id)
      AND semantic_model_id GLOB '[A-Za-z0-9]*'
      AND semantic_model_id NOT GLOB '*[^A-Za-z0-9_.:-]*'),
  created_at TEXT NOT NULL
    CHECK (julianday(created_at) IS NOT NULL AND substr(created_at, -1) = 'Z'),
  updated_at TEXT NOT NULL
    CHECK (julianday(updated_at) IS NOT NULL
      AND substr(updated_at, -1) = 'Z'
      AND julianday(updated_at) >= julianday(created_at)),
  archived_at TEXT
    CHECK (archived_at IS NULL OR
      (julianday(archived_at) IS NOT NULL
        AND substr(archived_at, -1) = 'Z'
        AND julianday(archived_at) >= julianday(updated_at))),
  current_revision_id TEXT NOT NULL,
  current_revision_number INTEGER NOT NULL CHECK (current_revision_number > 0),
  current_content_hash TEXT NOT NULL
    CHECK (length(current_content_hash) = 71
      AND substr(current_content_hash, 1, 7) = 'sha256:'
      AND substr(current_content_hash, 8) NOT GLOB '*[^0-9a-f]*'),
  PRIMARY KEY (project_id, exploration_id),
  UNIQUE (project_id, slug),
  CHECK ((status = 'archived') = (archived_at IS NOT NULL)),
  FOREIGN KEY (
    project_id, exploration_id, current_revision_id,
    current_revision_number, current_content_hash
  ) REFERENCES saved_exploration_revisions(
    project_id, exploration_id, revision_id, revision_number, content_hash
  ) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE saved_exploration_revisions (
  project_id TEXT NOT NULL
    CHECK (length(project_id) >= 1
      AND project_id = trim(project_id)
      AND project_id GLOB '[A-Za-z0-9]*'
      AND project_id NOT GLOB '*[^A-Za-z0-9_.:-]*'),
  exploration_id TEXT NOT NULL,
  revision_id TEXT NOT NULL
    CHECK (length(revision_id) BETWEEN 1 AND 128
      AND revision_id = trim(revision_id)
      AND revision_id GLOB '[A-Za-z0-9]*'
      AND revision_id NOT GLOB '*[^A-Za-z0-9_.:-]*'),
  revision_number INTEGER NOT NULL CHECK (revision_number > 0),
  spec_envelope_version INTEGER NOT NULL CHECK (spec_envelope_version > 0),
  spec_canonical_json TEXT NOT NULL
    CHECK (length(CAST(spec_canonical_json AS BLOB)) BETWEEN 1 AND (256 * 1024)
      AND json_valid(spec_canonical_json)
      AND json_type(spec_canonical_json) = 'object'
      AND json_type(spec_canonical_json, '$.spec') = 'object'
      AND json_type(spec_canonical_json, '$.spec.modelId') = 'text'
      AND length(trim(json_extract(spec_canonical_json, '$.spec.modelId'))) > 0
      AND json_extract(spec_canonical_json, '$.spec.modelId') GLOB '[A-Za-z0-9]*'
      AND json_extract(spec_canonical_json, '$.spec.modelId') NOT GLOB '*[^A-Za-z0-9_.:-]*'
      AND json_extract(spec_canonical_json, '$.version') = spec_envelope_version),
  content_hash TEXT NOT NULL
    CHECK (length(content_hash) = 71
      AND substr(content_hash, 1, 7) = 'sha256:'
      AND substr(content_hash, 8) NOT GLOB '*[^0-9a-f]*'),
  created_by TEXT NOT NULL
    CHECK (length(created_by) BETWEEN 1 AND 256
      AND created_by = trim(created_by)
      AND instr(created_by, char(0)) = 0
      AND instr(created_by, char(9)) = 0
      AND instr(created_by, char(10)) = 0
      AND instr(created_by, char(13)) = 0),
  created_at TEXT NOT NULL CHECK (julianday(created_at) IS NOT NULL AND substr(created_at, -1) = 'Z'),
  serving_project_id TEXT NOT NULL
    CHECK (length(serving_project_id) >= 1
      AND serving_project_id = trim(serving_project_id)
      AND serving_project_id GLOB '[A-Za-z0-9]*'
      AND serving_project_id NOT GLOB '*[^A-Za-z0-9_.:-]*'),
  serving_environment TEXT NOT NULL
    CHECK (length(serving_environment) >= 1
      AND serving_environment = trim(serving_environment)
      AND serving_environment GLOB '[A-Za-z0-9]*'
      AND serving_environment NOT GLOB '*[^A-Za-z0-9_.:-]*'),
  serving_generation_id TEXT NOT NULL
    CHECK (length(serving_generation_id) >= 1
      AND serving_generation_id = trim(serving_generation_id)
      AND serving_generation_id GLOB '[A-Za-z0-9]*'
      AND serving_generation_id NOT GLOB '*[^A-Za-z0-9_.:-]*'),
  PRIMARY KEY (project_id, exploration_id, revision_id),
  UNIQUE (project_id, exploration_id, revision_number),
  UNIQUE (project_id, exploration_id, revision_id, revision_number, content_hash),
  CHECK (serving_project_id = project_id),
  FOREIGN KEY (project_id, exploration_id)
    REFERENCES saved_explorations(project_id, exploration_id)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

-- The operation ledger is deliberately separate from generic HTTP
-- idempotency. It is durable, actor-scoped, and stores the exact lifecycle
-- target and revision token needed to replay create/update/duplicate/archive
-- results after a process restart. The request fingerprint is retained so a
-- same-key/different-request reuse is a conflict, never a second mutation.
-- For archive, result_revision_* is the applied existing revision token; the
-- archive operation never creates a revision row.
-- SQLite enforces the bounded control-character subset below; repository
-- validation remains authoritative for UTF-8, Unicode whitespace, strict JSON,
-- and canonical payload/hash rules that SQL cannot reproduce reliably.
CREATE TABLE saved_exploration_operations (
  project_id TEXT NOT NULL
    CHECK (length(project_id) >= 1
      AND project_id = trim(project_id)
      AND project_id GLOB '[A-Za-z0-9]*'
      AND project_id NOT GLOB '*[^A-Za-z0-9_.:-]*'),
  actor_id TEXT NOT NULL
    CHECK (length(actor_id) BETWEEN 1 AND 256
      AND actor_id = trim(actor_id)
      AND instr(actor_id, char(0)) = 0
      AND instr(actor_id, char(9)) = 0
      AND instr(actor_id, char(10)) = 0
      AND instr(actor_id, char(13)) = 0),
  operation_kind TEXT NOT NULL
    CHECK (operation_kind IN ('create', 'update', 'duplicate', 'archive')),
  idempotency_key TEXT NOT NULL
    CHECK (length(idempotency_key) BETWEEN 1 AND 200
      AND idempotency_key = trim(idempotency_key)
      AND instr(idempotency_key, char(0)) = 0
      AND instr(idempotency_key, char(9)) = 0
      AND instr(idempotency_key, char(10)) = 0
      AND instr(idempotency_key, char(13)) = 0),
  request_fingerprint TEXT NOT NULL
    CHECK (length(request_fingerprint) = 71
      AND substr(request_fingerprint, 1, 7) = 'sha256:'
      AND substr(request_fingerprint, 8) NOT GLOB '*[^0-9a-f]*'),
  result_exploration_id TEXT NOT NULL,
  result_owner_principal_id TEXT NOT NULL
    CHECK (length(result_owner_principal_id) BETWEEN 1 AND 256
      AND result_owner_principal_id = trim(result_owner_principal_id)
      AND instr(result_owner_principal_id, char(0)) = 0
      AND instr(result_owner_principal_id, char(9)) = 0
      AND instr(result_owner_principal_id, char(10)) = 0
      AND instr(result_owner_principal_id, char(13)) = 0),
  result_title TEXT NOT NULL
    CHECK (length(result_title) BETWEEN 1 AND 200
      AND result_title = trim(result_title)
      AND instr(result_title, char(0)) = 0
      AND instr(result_title, char(9)) = 0
      AND instr(result_title, char(10)) = 0
      AND instr(result_title, char(13)) = 0),
  result_slug TEXT NOT NULL
    CHECK (length(result_slug) BETWEEN 1 AND 128
      AND result_slug = trim(result_slug)
      AND result_slug GLOB '[a-z0-9]*'
      AND result_slug NOT GLOB '*[^a-z0-9-]*'),
  result_visibility TEXT NOT NULL
    CHECK (result_visibility IN ('private', 'restricted', 'organization')),
  result_status TEXT NOT NULL
    CHECK (result_status IN ('active', 'archived')),
  result_semantic_model_id TEXT NOT NULL
    CHECK (length(result_semantic_model_id) >= 1
      AND result_semantic_model_id = trim(result_semantic_model_id)
      AND result_semantic_model_id GLOB '[A-Za-z0-9]*'
      AND result_semantic_model_id NOT GLOB '*[^A-Za-z0-9_.:-]*'),
  result_created_at TEXT NOT NULL
    CHECK (julianday(result_created_at) IS NOT NULL AND substr(result_created_at, -1) = 'Z'),
  result_updated_at TEXT NOT NULL
    CHECK (julianday(result_updated_at) IS NOT NULL
      AND substr(result_updated_at, -1) = 'Z'
      AND julianday(result_updated_at) >= julianday(result_created_at)),
  result_archived_at TEXT
    CHECK (result_archived_at IS NULL OR
      (julianday(result_archived_at) IS NOT NULL
        AND substr(result_archived_at, -1) = 'Z'
        AND julianday(result_archived_at) >= julianday(result_updated_at))),
  result_revision_id TEXT NOT NULL,
  result_revision_number INTEGER NOT NULL CHECK (result_revision_number > 0),
  result_content_hash TEXT NOT NULL
    CHECK (length(result_content_hash) = 71
      AND substr(result_content_hash, 1, 7) = 'sha256:'
      AND substr(result_content_hash, 8) NOT GLOB '*[^0-9a-f]*'),
  result_revision_created_at TEXT NOT NULL
    CHECK (julianday(result_revision_created_at) IS NOT NULL AND substr(result_revision_created_at, -1) = 'Z'),
  result_revision_created_by TEXT NOT NULL
    CHECK (length(result_revision_created_by) BETWEEN 1 AND 256
      AND result_revision_created_by = trim(result_revision_created_by)
      AND instr(result_revision_created_by, char(0)) = 0
      AND instr(result_revision_created_by, char(9)) = 0
      AND instr(result_revision_created_by, char(10)) = 0
      AND instr(result_revision_created_by, char(13)) = 0),
  result_serving_project_id TEXT NOT NULL
    CHECK (length(result_serving_project_id) >= 1
      AND result_serving_project_id = trim(result_serving_project_id)
      AND result_serving_project_id GLOB '[A-Za-z0-9]*'
      AND result_serving_project_id NOT GLOB '*[^A-Za-z0-9_.:-]*'),
  result_serving_environment TEXT NOT NULL
    CHECK (length(result_serving_environment) >= 1
      AND result_serving_environment = trim(result_serving_environment)
      AND result_serving_environment GLOB '[A-Za-z0-9]*'
      AND result_serving_environment NOT GLOB '*[^A-Za-z0-9_.:-]*'),
  result_serving_generation_id TEXT NOT NULL
    CHECK (length(result_serving_generation_id) >= 1
      AND result_serving_generation_id = trim(result_serving_generation_id)
      AND result_serving_generation_id GLOB '[A-Za-z0-9]*'
      AND result_serving_generation_id NOT GLOB '*[^A-Za-z0-9_.:-]*'),
  evidence_version INTEGER NOT NULL CHECK (evidence_version = 1),
  evidence_request_id TEXT NOT NULL
    CHECK (length(evidence_request_id) BETWEEN 1 AND 256
      AND evidence_request_id = trim(evidence_request_id)
      AND instr(evidence_request_id, char(0)) = 0
      AND instr(evidence_request_id, char(9)) = 0
      AND instr(evidence_request_id, char(10)) = 0
      AND instr(evidence_request_id, char(13)) = 0),
  evidence_correlation_id TEXT NOT NULL
    CHECK (length(evidence_correlation_id) BETWEEN 1 AND 256
      AND evidence_correlation_id = trim(evidence_correlation_id)
      AND instr(evidence_correlation_id, char(0)) = 0
      AND instr(evidence_correlation_id, char(9)) = 0
      AND instr(evidence_correlation_id, char(10)) = 0
      AND instr(evidence_correlation_id, char(13)) = 0),
  evidence_admin_override INTEGER NOT NULL CHECK (evidence_admin_override IN (0, 1)),
  evidence_admin_reason TEXT NOT NULL DEFAULT ''
    CHECK ((evidence_admin_override = 0 AND evidence_admin_reason = '')
      OR (evidence_admin_override = 1
        AND length(evidence_admin_reason) BETWEEN 1 AND 500
        AND evidence_admin_reason = trim(evidence_admin_reason)
        AND instr(evidence_admin_reason, char(0)) = 0
        AND instr(evidence_admin_reason, char(9)) = 0
        AND instr(evidence_admin_reason, char(10)) = 0
        AND instr(evidence_admin_reason, char(13)) = 0)),
  evidence_occurred_at TEXT NOT NULL
    CHECK (julianday(evidence_occurred_at) IS NOT NULL AND substr(evidence_occurred_at, -1) = 'Z'),
  created_at TEXT NOT NULL CHECK (julianday(created_at) IS NOT NULL AND substr(created_at, -1) = 'Z'),
  PRIMARY KEY (project_id, actor_id, operation_kind, idempotency_key),
  CHECK ((result_status = 'archived') = (result_archived_at IS NOT NULL)),
  CHECK ((operation_kind = 'archive') = (result_status = 'archived')),
  CHECK (operation_kind NOT IN ('create', 'duplicate')
    OR result_owner_principal_id = actor_id),
  CHECK (operation_kind = 'archive'
    OR result_revision_created_by = actor_id),
  CHECK (result_serving_project_id = project_id),
  FOREIGN KEY (project_id, result_exploration_id)
    REFERENCES saved_explorations(project_id, exploration_id)
    ON DELETE RESTRICT,
  FOREIGN KEY (
    project_id, result_exploration_id, result_revision_id,
    result_revision_number, result_content_hash
  ) REFERENCES saved_exploration_revisions(
    project_id, exploration_id, revision_id, revision_number, content_hash
  ) ON DELETE RESTRICT
);

CREATE INDEX saved_explorations_project_status_idx
  ON saved_explorations(project_id, status, updated_at DESC, exploration_id);
CREATE INDEX saved_exploration_revisions_lookup_idx
  ON saved_exploration_revisions(project_id, exploration_id, revision_number DESC);
CREATE INDEX saved_exploration_operations_result_idx
  ON saved_exploration_operations(project_id, result_exploration_id, created_at DESC);

-- Lifecycle identity and authored history are not replaceable records. Slug,
-- title, visibility, status, and model metadata may change through a service
-- CAS, but project/ID/owner/creation identity and revisions cannot.
-- +goose StatementBegin
CREATE TRIGGER saved_explorations_initial_revision_number
BEFORE INSERT ON saved_explorations
WHEN NEW.current_revision_number <> 1
BEGIN
  SELECT RAISE(ABORT, 'saved exploration must begin at revision 1');
END;
-- +goose StatementEnd

-- A saved exploration is always created active with its first revision. An
-- archive is a subsequent lifecycle transition, never an alternate create
-- shape.
-- +goose StatementBegin
CREATE TRIGGER saved_explorations_initial_state
BEFORE INSERT ON saved_explorations
WHEN NEW.status <> 'active' OR NEW.archived_at IS NOT NULL
BEGIN
  SELECT RAISE(ABORT, 'saved exploration must begin active');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER saved_explorations_identity_immutable
BEFORE UPDATE OF project_id, exploration_id, owner_principal_id, created_at
ON saved_explorations
BEGIN
  SELECT RAISE(ABORT, 'saved exploration identity is immutable');
END;
-- +goose StatementEnd

-- A pointer or semantic-model transition must select a revision whose
-- canonical envelope carries the same model. This keeps the identity row and
-- immutable authored record aligned even for direct SQL adapters.
-- +goose StatementBegin
CREATE TRIGGER saved_explorations_initial_revision_consistent
BEFORE INSERT ON saved_explorations
WHEN EXISTS (
  SELECT 1
  FROM saved_exploration_revisions revision
  WHERE revision.project_id = NEW.project_id
    AND revision.exploration_id = NEW.exploration_id
    AND revision.revision_id = NEW.current_revision_id
    AND revision.revision_number = NEW.current_revision_number
    AND revision.content_hash = NEW.current_content_hash
    AND (json_extract(revision.spec_canonical_json, '$.spec.modelId') <> NEW.semantic_model_id
      OR revision.created_at <> NEW.created_at)
)
BEGIN
  SELECT RAISE(ABORT, 'saved exploration initial revision is inconsistent');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER saved_explorations_current_revision_consistent
BEFORE UPDATE ON saved_explorations
WHEN NOT EXISTS (
  SELECT 1
  FROM saved_exploration_revisions revision
  WHERE revision.project_id = NEW.project_id
    AND revision.exploration_id = NEW.exploration_id
    AND revision.revision_id = NEW.current_revision_id
    AND revision.revision_number = NEW.current_revision_number
    AND revision.content_hash = NEW.current_content_hash
    AND json_extract(revision.spec_canonical_json, '$.spec.modelId') = NEW.semantic_model_id
    AND (
      OLD.status <> 'active'
      OR NEW.status <> 'active'
      OR NEW.current_revision_id = OLD.current_revision_id
      OR revision.created_at = NEW.updated_at
    )
)
OR (
  NEW.current_revision_id <> OLD.current_revision_id
  AND NEW.current_revision_number <> OLD.current_revision_number + 1
)
BEGIN
  SELECT RAISE(ABORT, 'saved exploration current revision is inconsistent');
END;
-- +goose StatementEnd

-- Active lifecycle metadata is part of the authored version boundary. An
-- active-to-active mutation must therefore advance to a new revision in the
-- same statement; archive is the sole lifecycle transition that may retain
-- the current revision without appending one.
-- +goose StatementBegin
CREATE TRIGGER saved_explorations_active_update_requires_revision
BEFORE UPDATE ON saved_explorations
WHEN OLD.status = 'active'
  AND NEW.status = 'active'
  AND (
    NEW.title <> OLD.title
    OR NEW.slug <> OLD.slug
    OR NEW.visibility <> OLD.visibility
    OR NEW.semantic_model_id <> OLD.semantic_model_id
    OR NEW.updated_at <> OLD.updated_at
    OR NEW.current_revision_id <> OLD.current_revision_id
    OR NEW.current_revision_number <> OLD.current_revision_number
    OR NEW.current_content_hash <> OLD.current_content_hash
  )
  AND NOT (
    NEW.current_revision_id <> OLD.current_revision_id
    AND NEW.current_revision_number = OLD.current_revision_number + 1
  )
BEGIN
  SELECT RAISE(ABORT, 'active saved exploration update requires a new revision');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER saved_exploration_revision_model_consistent
AFTER INSERT ON saved_exploration_revisions
WHEN EXISTS (
  SELECT 1
  FROM saved_explorations exploration
  WHERE exploration.project_id = NEW.project_id
    AND exploration.exploration_id = NEW.exploration_id
    AND exploration.current_revision_id = NEW.revision_id
    AND exploration.current_revision_number = NEW.revision_number
    AND exploration.current_content_hash = NEW.content_hash
    AND (
      json_extract(NEW.spec_canonical_json, '$.spec.modelId') <> exploration.semantic_model_id
      OR (NEW.revision_number = 1 AND NEW.created_at <> exploration.created_at)
    )
)
BEGIN
  SELECT RAISE(ABORT, 'saved exploration revision model is inconsistent');
END;
-- +goose StatementEnd

-- Revisions are appendable only while the lifecycle is active. Archiving does
-- not create a new authored version and cannot be followed by one.
-- +goose StatementBegin
CREATE TRIGGER saved_exploration_revisions_active_only
BEFORE INSERT ON saved_exploration_revisions
WHEN EXISTS (
  SELECT 1
  FROM saved_explorations exploration
  WHERE exploration.project_id = NEW.project_id
    AND exploration.exploration_id = NEW.exploration_id
    AND exploration.status = 'archived'
)
BEGIN
  SELECT RAISE(ABORT, 'archived saved exploration cannot receive revisions');
END;
-- +goose StatementEnd

-- The normalized revision reference and bounded lifecycle columns form an
-- immutable replay snapshot. Operation references are immediate, so the
-- repository must insert and validate the lifecycle and revision first; the
-- snapshot trigger always sees a real target.
-- +goose StatementBegin
CREATE TRIGGER saved_exploration_operation_snapshot_consistent
AFTER INSERT ON saved_exploration_operations
WHEN EXISTS (
  SELECT 1
  FROM saved_explorations exploration
  JOIN saved_exploration_revisions revision
    ON revision.project_id = exploration.project_id
   AND revision.exploration_id = exploration.exploration_id
   AND revision.revision_id = NEW.result_revision_id
   AND revision.revision_number = NEW.result_revision_number
   AND revision.content_hash = NEW.result_content_hash
  WHERE exploration.project_id = NEW.project_id
    AND exploration.exploration_id = NEW.result_exploration_id
)
AND NOT EXISTS (
  SELECT 1
  FROM saved_explorations exploration
  JOIN saved_exploration_revisions revision
    ON revision.project_id = exploration.project_id
   AND revision.exploration_id = exploration.exploration_id
   AND revision.revision_id = NEW.result_revision_id
   AND revision.revision_number = NEW.result_revision_number
   AND revision.content_hash = NEW.result_content_hash
  WHERE exploration.project_id = NEW.project_id
    AND exploration.exploration_id = NEW.result_exploration_id
    AND exploration.owner_principal_id = NEW.result_owner_principal_id
    AND exploration.title = NEW.result_title
    AND exploration.slug = NEW.result_slug
    AND exploration.visibility = NEW.result_visibility
    AND exploration.status = NEW.result_status
    AND exploration.semantic_model_id = NEW.result_semantic_model_id
    AND exploration.created_at = NEW.result_created_at
    AND exploration.updated_at = NEW.result_updated_at
    AND exploration.archived_at IS NEW.result_archived_at
    AND exploration.current_revision_id = NEW.result_revision_id
    AND exploration.current_revision_number = NEW.result_revision_number
    AND exploration.current_content_hash = NEW.result_content_hash
    AND revision.created_at = NEW.result_revision_created_at
    AND revision.created_by = NEW.result_revision_created_by
    AND revision.serving_project_id = NEW.result_serving_project_id
    AND revision.serving_environment = NEW.result_serving_environment
    AND revision.serving_generation_id = NEW.result_serving_generation_id
)
BEGIN
  SELECT RAISE(ABORT, 'saved exploration operation snapshot is inconsistent');
END;
-- +goose StatementEnd

-- Updated timestamps are monotonic, archived rows are immutable, and archiving
-- cannot move the current-revision pointer.
-- The archive timestamp is required by the lifecycle CHECK above and history
-- remains in saved_exploration_revisions because no archive operation deletes
-- rows.
-- +goose StatementBegin
CREATE TRIGGER saved_explorations_lifecycle_guards
BEFORE UPDATE ON saved_explorations
WHEN julianday(NEW.updated_at) < julianday(OLD.updated_at)
  OR OLD.status = 'archived'
  OR (
    OLD.status = 'active'
    AND NEW.status = 'archived'
    AND (
      NEW.current_revision_id <> OLD.current_revision_id
      OR NEW.current_revision_number <> OLD.current_revision_number
      OR NEW.current_content_hash <> OLD.current_content_hash
    )
  )
BEGIN
  SELECT RAISE(ABORT, 'saved exploration lifecycle transition is invalid');
END;
-- +goose StatementEnd

-- Revision rows are append-only. The composite unique key is intentionally
-- retained for exact current-pointer and operation-result foreign keys.
-- +goose StatementBegin
CREATE TRIGGER saved_exploration_revisions_immutable_update
BEFORE UPDATE ON saved_exploration_revisions
BEGIN
  SELECT RAISE(ABORT, 'saved exploration revisions are immutable');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER saved_exploration_revisions_immutable_delete
BEFORE DELETE ON saved_exploration_revisions
BEGIN
  SELECT RAISE(ABORT, 'saved exploration revisions are immutable');
END;
-- +goose StatementEnd

-- Operation evidence is the replay authority and must not be rewritten after
-- commit. A retry with a different fingerprint collides on the scoped primary
-- key and is classified by the repository as command reuse.
-- +goose StatementBegin
CREATE TRIGGER saved_exploration_operations_immutable_update
BEFORE UPDATE ON saved_exploration_operations
BEGIN
  SELECT RAISE(ABORT, 'saved exploration operation evidence is immutable');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER saved_exploration_operations_immutable_delete
BEFORE DELETE ON saved_exploration_operations
BEGIN
  SELECT RAISE(ABORT, 'saved exploration operation evidence is immutable');
END;
-- +goose StatementEnd

-- +goose Down

DROP TRIGGER saved_exploration_revisions_immutable_delete;
DROP TRIGGER saved_exploration_revisions_immutable_update;
DROP TRIGGER saved_exploration_revisions_active_only;
DROP TRIGGER saved_exploration_operations_immutable_delete;
DROP TRIGGER saved_exploration_operations_immutable_update;
DROP TRIGGER saved_exploration_operation_snapshot_consistent;
DROP TRIGGER saved_explorations_lifecycle_guards;
DROP TRIGGER saved_explorations_active_update_requires_revision;
DROP TRIGGER saved_explorations_initial_state;
DROP TRIGGER saved_exploration_revision_model_consistent;
DROP TRIGGER saved_explorations_current_revision_consistent;
DROP TRIGGER saved_explorations_initial_revision_consistent;
DROP TRIGGER saved_explorations_identity_immutable;
DROP TRIGGER saved_explorations_initial_revision_number;
DROP INDEX saved_exploration_operations_result_idx;
DROP INDEX saved_exploration_revisions_lookup_idx;
DROP INDEX saved_explorations_project_status_idx;

-- The lifecycle and revision tables intentionally have a deferred circular
-- relationship for atomic creation. Defer all remaining checks while Goose
-- removes this complete schema so a populated database can be downgraded.
PRAGMA defer_foreign_keys = ON;
DROP TABLE saved_exploration_operations;
DROP TABLE saved_explorations;
DROP TABLE saved_exploration_revisions;
