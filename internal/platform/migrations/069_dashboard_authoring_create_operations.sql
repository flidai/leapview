-- +goose Up

-- Create/fork idempotency is intentionally separate from the command ledger:
-- a create does not have a dashboard foreign key until the operation itself
-- succeeds. The operation row and lifecycle rows are inserted in one
-- transaction by the authoring SQLite repository.
CREATE TABLE dashboard_authoring_create_operations (
  project_id TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  operation_kind TEXT NOT NULL CHECK (operation_kind IN ('create', 'fork')),
  idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 200),
  conversation_id TEXT NOT NULL DEFAULT '',
  tool_call_id TEXT NOT NULL DEFAULT '',
  request_fingerprint TEXT NOT NULL CHECK (length(trim(request_fingerprint)) > 0),
  dashboard_id TEXT NOT NULL,
  result_revision_id TEXT NOT NULL,
  result_revision_number INTEGER NOT NULL CHECK (result_revision_number > 0),
  result_content_hash TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (project_id, actor_id, operation_kind, idempotency_key)
);

CREATE INDEX dashboard_authoring_create_operations_dashboard_idx
  ON dashboard_authoring_create_operations(project_id, dashboard_id);

-- +goose Down

DROP INDEX dashboard_authoring_create_operations_dashboard_idx;
DROP TABLE dashboard_authoring_create_operations;
