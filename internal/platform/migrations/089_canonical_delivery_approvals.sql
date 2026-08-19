-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA foreign_keys = OFF;

DROP INDEX deployment_approvals_expiry_idx;
DROP INDEX deployment_approvals_project_history_idx;
DROP INDEX deployment_approvals_live_deployment_idx;

ALTER TABLE deployment_approvals RENAME TO deployment_approvals_v088;

CREATE TABLE deployment_approvals (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  deployment_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  request_digest TEXT NOT NULL,
  release_id TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'denied', 'revoked', 'expired')),
  requested_by TEXT NOT NULL REFERENCES principals(id),
  request_credential_class TEXT NOT NULL CHECK (
    request_credential_class IN ('human', 'workload', 'api_token', 'session')
  ),
  request_credential_id TEXT NOT NULL,
  requested_at TEXT NOT NULL,
  approved_by TEXT REFERENCES principals(id),
  approval_credential_class TEXT CHECK (
    approval_credential_class IS NULL OR
    approval_credential_class IN ('human', 'workload', 'api_token', 'session')
  ),
  approval_credential_id TEXT,
  approval_credential_expires_at TEXT,
  approved_at TEXT,
  revoked_by TEXT REFERENCES principals(id),
  revoked_at TEXT,
  expires_at TEXT NOT NULL,
  revision INTEGER NOT NULL CHECK (revision > 0)
);

INSERT INTO deployment_approvals (
  id, project_id, deployment_id, environment, request_digest, release_id,
  status, requested_by, request_credential_class, request_credential_id,
  requested_at, approved_by, approval_credential_class,
  approval_credential_id, approval_credential_expires_at, approved_at,
  revoked_by, revoked_at, expires_at, revision
)
SELECT
  id, project_id, deployment_id, environment, request_digest, release_id,
  status, requested_by, request_credential_class, request_credential_id,
  requested_at, approved_by, approval_credential_class,
  approval_credential_id, approval_credential_expires_at, approved_at,
  revoked_by, revoked_at, expires_at, revision
FROM deployment_approvals_v088;

DROP TABLE deployment_approvals_v088;

CREATE UNIQUE INDEX deployment_approvals_live_deployment_idx
  ON deployment_approvals(deployment_id)
  WHERE status IN ('pending', 'approved');

CREATE INDEX deployment_approvals_project_history_idx
  ON deployment_approvals(project_id, requested_at DESC, id DESC);

CREATE INDEX deployment_approvals_expiry_idx
  ON deployment_approvals(status, expires_at);

CREATE TRIGGER deployment_approvals_parent_insert
BEFORE INSERT ON deployment_approvals
WHEN NOT EXISTS (SELECT 1 FROM project_deployments WHERE id = NEW.deployment_id)
 AND NOT EXISTS (SELECT 1 FROM delivery_publications WHERE id = NEW.deployment_id)
BEGIN
  SELECT RAISE(ABORT, 'deployment approval parent is missing');
END;

CREATE TRIGGER deployment_approvals_parent_update
BEFORE UPDATE OF deployment_id ON deployment_approvals
WHEN NOT EXISTS (SELECT 1 FROM project_deployments WHERE id = NEW.deployment_id)
 AND NOT EXISTS (SELECT 1 FROM delivery_publications WHERE id = NEW.deployment_id)
BEGIN
  SELECT RAISE(ABORT, 'deployment approval parent is missing');
END;

CREATE TRIGGER project_deployments_approval_delete
AFTER DELETE ON project_deployments
BEGIN
  DELETE FROM deployment_approvals WHERE deployment_id = OLD.id;
END;

CREATE TRIGGER delivery_publications_approval_delete
AFTER DELETE ON delivery_publications
BEGIN
  DELETE FROM deployment_approvals WHERE deployment_id = OLD.id;
END;

PRAGMA foreign_keys = ON;
-- +goose StatementEnd

-- +goose Down

-- Forward-only: canonical publication approvals are durable authorization
-- evidence and must not be reinterpreted as legacy-only deployment records.
SELECT 1;
