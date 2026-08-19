-- +goose Up

DROP INDEX deployment_approvals_expiry_idx;
DROP INDEX deployment_approvals_project_history_idx;
DROP INDEX deployment_approvals_live_deployment_idx;

ALTER TABLE deployment_approvals RENAME TO deployment_approvals_v052;

CREATE TABLE deployment_approvals (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  -- Approval parents are polymorphic: the legacy project deployment or the
  -- canonical delivery publication. Migration 089 installs exact-scope parent
  -- and cascade triggers after both parent tables exist.
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
FROM deployment_approvals_v052;

DROP TABLE deployment_approvals_v052;

CREATE UNIQUE INDEX deployment_approvals_live_deployment_idx
  ON deployment_approvals(deployment_id)
  WHERE status IN ('pending', 'approved');

CREATE INDEX deployment_approvals_project_history_idx
  ON deployment_approvals(project_id, requested_at DESC, id DESC);

CREATE INDEX deployment_approvals_expiry_idx
  ON deployment_approvals(status, expires_at);

-- +goose Down

-- Forward-only: removing explicit denial evidence could reinterpret a denied
-- production publication as approved, revoked, or expired. Preserve the safer
-- schema when an operator rolls back application binaries.
SELECT 1;
