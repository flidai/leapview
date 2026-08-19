-- +goose Up

-- LEA-414: append-only, non-secret delivery lifecycle evidence. Mutable
-- delivery_* rows are projections used for CAS; this ledger is the audit
-- authority for plan/build/publish/rollback/GC outcomes and survives a
-- process crash or retry without rewriting prior observations.
CREATE TABLE delivery_events (
  id TEXT PRIMARY KEY
    CHECK (length(id) BETWEEN 1 AND 128
      AND id = trim(id)
      AND id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  target_id TEXT NOT NULL REFERENCES delivery_target_revisions(target_id),
  project_id TEXT NOT NULL
    CHECK (length(project_id) BETWEEN 1 AND 128
      AND project_id = trim(project_id)
      AND project_id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  environment TEXT NOT NULL
    CHECK (length(environment) BETWEEN 1 AND 128
      AND environment = trim(environment)
      AND environment NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  actor_id TEXT NOT NULL
    CHECK (length(actor_id) BETWEEN 1 AND 128
      AND actor_id = trim(actor_id)
      AND actor_id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  event_kind TEXT NOT NULL CHECK (event_kind IN (
    'plan_created', 'plan_expired', 'build_started', 'build_transitioned', 'build_artifact_bound',
    'candidate_qualified', 'candidate_sealed', 'candidate_retired',
    'approval_requested', 'approval_granted', 'approval_rejected', 'approval_revoked',
    'restatement_requested', 'publish_requested',
    'publish_committed', 'publish_rejected', 'publish_indeterminate',
    'activation_committed', 'rollback_requested', 'rollback_committed',
    'retirement_committed', 'gc_marked', 'gc_deleted', 'cleanup_completed',
    'gc_aborted', 'lease_acquired', 'lease_expired', 'lease_released')),
  object_kind TEXT NOT NULL CHECK (object_kind IN (
    'plan', 'build_attempt', 'candidate', 'generation', 'publication', 'approval',
    'rollback', 'gc_cycle', 'writer_lease', 'query_lease')),
  object_id TEXT NOT NULL
    CHECK (length(object_id) BETWEEN 1 AND 128
      AND object_id = trim(object_id)
      AND object_id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  request_digest TEXT NOT NULL CHECK (length(request_digest) = 71
    AND substr(request_digest, 1, 7) = 'sha256:'
    AND substr(request_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  plan_digest TEXT CHECK (plan_digest IS NULL OR (length(plan_digest) = 71
    AND substr(plan_digest, 1, 7) = 'sha256:'
    AND substr(plan_digest, 8) NOT GLOB '*[^0-9a-f]*')),
  result_digest TEXT CHECK (result_digest IS NULL OR (length(result_digest) = 71
    AND substr(result_digest, 1, 7) = 'sha256:'
    AND substr(result_digest, 8) NOT GLOB '*[^0-9a-f]*')),
  outcome TEXT NOT NULL CHECK (outcome IN ('accepted', 'rejected', 'failed', 'indeterminate', 'observed')),
  details_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(details_json) AND json_type(details_json) = 'object'),
  created_at TEXT NOT NULL,
  UNIQUE(target_id, id),
  UNIQUE(target_id, request_digest, event_kind, object_kind, object_id)
);

CREATE INDEX delivery_events_scope_idx
  ON delivery_events(project_id, environment, target_id, created_at DESC, id);

-- +goose StatementBegin
CREATE TRIGGER delivery_events_append_only_update
BEFORE UPDATE ON delivery_events
BEGIN
  SELECT RAISE(ABORT, 'delivery event ledger is append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER delivery_events_append_only_delete
BEFORE DELETE ON delivery_events
BEGIN
  SELECT RAISE(ABORT, 'delivery event ledger is append-only');
END;
-- +goose StatementEnd

-- +goose Down

DROP TRIGGER delivery_events_append_only_delete;
DROP TRIGGER delivery_events_append_only_update;
DROP INDEX delivery_events_scope_idx;
DROP TABLE delivery_events;
