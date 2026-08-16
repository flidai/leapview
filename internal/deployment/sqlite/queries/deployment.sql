-- Canonical project-generation deployment records.

-- name: GetServingStateForDeployment :one
SELECT project_id, environment, digest, status FROM serving_states WHERE id = ?;

-- name: GetActiveServingState :one
SELECT generation_id FROM project_active_serving_states
WHERE project_id = ? AND environment = ?;

-- name: CreateProjectDeployment :exec
INSERT INTO project_deployments (id, project_id, environment, generation_id, artifact_digest, prior_generation_id, request_digest, status, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?);

-- name: GetProjectDeployment :one
SELECT * FROM project_deployments WHERE id = ?;

-- Deployment approval decisions are immutable in scope and optimistic in
-- transition. A revoked decision remains as audit evidence; a later request
-- receives a new identity.

-- name: CreateDeploymentApproval :exec
INSERT INTO deployment_approvals (
  id, project_id, deployment_id, environment, request_digest, release_id,
  status, requested_by, request_credential_class, request_credential_id,
  requested_at, approved_by, approval_credential_class,
  approval_credential_id, approval_credential_expires_at, approved_at, revoked_by, revoked_at,
  expires_at, revision
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetCurrentDeploymentApproval :one
SELECT *
FROM deployment_approvals
WHERE deployment_id = ?
ORDER BY requested_at DESC, id DESC
LIMIT 1;

-- name: UpdateDeploymentApproval :execrows
UPDATE deployment_approvals
SET status = ?,
    approved_by = ?,
    approval_credential_class = ?,
    approval_credential_id = ?,
    approval_credential_expires_at = ?,
    approved_at = ?,
    revoked_by = ?,
    revoked_at = ?,
    expires_at = ?,
    revision = ?
WHERE id = ?
  AND deployment_id = ?
  AND revision = ?;

-- name: ActivateProjectDeployment :execresult
UPDATE project_deployments
SET status = 'active',
    activated_at = CURRENT_TIMESTAMP,
    activation_principal = ?,
    verification_digest = ?,
    verified_at = CURRENT_TIMESTAMP,
    error = ''
WHERE id = ? AND status = 'pending';

-- name: SupersedeOtherProjectDeployments :exec
UPDATE project_deployments
SET status = 'superseded'
WHERE project_id = ? AND environment = ? AND id <> ? AND status = 'active';

-- name: FailProjectDeployment :execresult
UPDATE project_deployments
SET status = 'failed', error = ?
WHERE id = ? AND status = 'pending';

-- name: CancelProjectDeployment :execresult
UPDATE project_deployments
SET status = 'cancelled'
WHERE id = ? AND status = 'pending';

-- Deployment-owned validation projections over managed-data records.

-- Durable private project candidate sessions.

-- name: CreateProjectCandidate :exec
INSERT INTO project_candidates (
  id, project_id, target_id, environment, owner_principal_id, candidate_key,
  base_generation, artifact_digest, provenance_digest, status, failure_reason,
  expires_at, created_at, updated_at, ready_at, cancelled_at,
  expired_at, revision
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetProjectCandidate :one
SELECT *
FROM project_candidates
WHERE id = ?;

-- name: GetActiveProjectCandidateSession :one
SELECT *
FROM project_candidates
WHERE target_id = ?
  AND project_id = ?
  AND owner_principal_id = ?
  AND candidate_key = ?
  AND status IN ('preparing', 'ready', 'failed')
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: GetActiveProjectCandidateByKey :one
SELECT *
FROM project_candidates
WHERE target_id = ?
  AND project_id = ?
  AND owner_principal_id = ?
  AND candidate_key = ?
  AND status IN ('preparing', 'ready', 'failed')
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: CountActiveProjectCandidatesForOwner :one
SELECT count(*)
FROM project_candidates
WHERE owner_principal_id = ?
  AND status IN ('preparing', 'ready', 'failed');

-- name: GetActiveProjectCandidateBaseGeneration :one
SELECT id
FROM project_deployments
WHERE project_id = ?
  AND environment = ?
  AND status = 'active'
ORDER BY activated_at DESC, created_at DESC, id DESC
LIMIT 1;

-- name: UpdateProjectCandidate :execrows
UPDATE project_candidates
SET artifact_digest = ?,
    provenance_digest = ?,
    status = ?,
    failure_reason = ?,
    expires_at = ?,
    updated_at = ?,
    ready_at = ?,
    cancelled_at = ?,
    expired_at = ?,
    revision = ?
WHERE id = ?
  AND revision = ?;

-- name: ExpireProjectCandidates :execrows
UPDATE project_candidates
SET status = 'expired',
    provenance_digest = '',
    failure_reason = '',
    ready_at = NULL,
    expired_at = ?,
    updated_at = ?,
    revision = revision + 1
WHERE target_id = ?
  AND status IN ('preparing', 'ready', 'failed')
  AND expires_at <= ?;

-- name: CancelSupersededProjectCandidate :execrows
UPDATE project_candidates
SET status = 'cancelled', failure_reason = 'candidate base generation superseded',
    cancelled_at = sqlc.arg(cancelled_at), updated_at = sqlc.arg(updated_at),
    revision = revision + 1
WHERE id = sqlc.arg(id) AND status IN ('preparing', 'ready', 'failed');

-- name: GetManagedDataCollection :one
SELECT * FROM managed_data_collections WHERE id = ?;

-- name: GetManagedDataRevision :one
SELECT * FROM managed_data_revisions WHERE id = ?;
