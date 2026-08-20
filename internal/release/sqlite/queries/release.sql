-- Canonical project-generation releases.

-- name: RetainCandidateProvenance :execrows
INSERT OR IGNORE INTO release_candidate_provenance (project_id, candidate_id, candidate_revision, provenance_digest, provenance_json)
VALUES (?, ?, ?, ?, ?);

-- name: GetCandidateProvenance :one
SELECT provenance_json FROM release_candidate_provenance
WHERE project_id = ? AND candidate_id = ? AND candidate_revision = ?;

-- name: CreateAPIRelease :exec
INSERT INTO api_releases (id, project_id, environment, generation_id, project_digest, artifact_digest, request_digest, idempotency_key, status, provenance_json, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'draft', ?, ?);

-- name: CreateAPIReleaseConnection :exec
INSERT INTO api_release_connections (release_id, connection_id, revision_id) VALUES (?, ?, ?);

-- name: GetAPIReleaseByID :one
SELECT id, project_id, environment, generation_id, project_digest, artifact_digest, artifact_actual_digest, artifact_size_bytes,
  COALESCE(artifact_uploaded_at, '') AS artifact_uploaded_at, request_digest, idempotency_key, status, provenance_json,
  created_by, created_at, COALESCE(finalized_at, '') AS finalized_at, error
FROM api_releases WHERE project_id = ? AND id = ?;

-- name: GetAPIReleaseByIdempotencyKey :one
SELECT id, project_id, environment, generation_id, project_digest, artifact_digest, artifact_actual_digest, artifact_size_bytes,
  COALESCE(artifact_uploaded_at, '') AS artifact_uploaded_at, request_digest, idempotency_key, status, provenance_json,
  created_by, created_at, COALESCE(finalized_at, '') AS finalized_at, error
FROM api_releases WHERE project_id = ? AND idempotency_key = ?;

-- name: ListAPIReleaseIDs :many
SELECT id FROM api_releases WHERE project_id = ? ORDER BY created_at DESC, id DESC;

-- name: GetAPIReleaseConnections :many
SELECT connection_id, revision_id FROM api_release_connections WHERE release_id = ? ORDER BY connection_id;

-- name: GetReadyReleaseProvenanceByGeneration :one
SELECT provenance_json FROM api_releases
WHERE project_id = ? AND environment = ? AND generation_id = ? AND status = 'ready'
ORDER BY finalized_at DESC, id DESC LIMIT 1;

-- name: GetCandidateProvenanceByGeneration :many
SELECT provenance_json FROM release_candidate_provenance
WHERE project_id = ?
  AND json_extract(provenance_json, '$.plan.identity.environment') = sqlc.arg(environment)
  AND json_extract(provenance_json, '$.plan.identity.generationId') = sqlc.arg(generation_id)
ORDER BY retained_at DESC, candidate_id DESC, candidate_revision DESC
LIMIT 2;

-- name: RecordAPIReleaseArtifact :execrows
UPDATE api_releases
SET artifact_actual_digest = ?, artifact_size_bytes = ?, artifact_uploaded_at = CURRENT_TIMESTAMP
WHERE id = ? AND project_id = ? AND environment = ? AND generation_id = ?
  AND artifact_digest = ? AND status = 'draft' AND artifact_uploaded_at IS NULL;

-- name: MarkAPIReleaseValidating :execrows
UPDATE api_releases SET status = 'validating' WHERE id = ? AND project_id = ? AND status = 'draft';

-- name: MarkAPIReleaseReady :execrows
UPDATE api_releases SET status = 'ready', finalized_at = CURRENT_TIMESTAMP
WHERE id = ? AND project_id = ? AND status = 'validating';

-- name: MarkAPIReleaseFailed :execrows
UPDATE api_releases SET status = 'failed', error = ?, finalized_at = CURRENT_TIMESTAMP
WHERE id = ? AND project_id = ? AND status = 'validating';

-- name: GetAPIReleaseDeployment :one
SELECT release_id, COALESCE(rollback_of, '') AS rollback_of FROM api_deployment_releases WHERE project_id = ? AND deployment_id = ?;

-- name: GetAPIReleaseDeploymentByDeployment :one
SELECT project_id, release_id, COALESCE(rollback_of, '') AS rollback_of
FROM api_deployment_releases
WHERE deployment_id = ?;

-- name: CreateAPIReleaseDeployment :exec
INSERT INTO api_deployment_releases (deployment_id, project_id, release_id, rollback_of)
VALUES (?, ?, ?, ?)
ON CONFLICT(deployment_id) DO NOTHING;

-- name: ListAPIReleaseDeploymentIDs :many
SELECT deployment_id FROM api_deployment_releases WHERE project_id = ? ORDER BY created_at DESC, deployment_id DESC;

-- name: GetPriorAPIReleaseDeployment :one
SELECT prior.release_id
FROM api_deployment_releases current
JOIN project_deployments current_deployment ON current_deployment.id = current.deployment_id
JOIN api_deployment_releases prior ON prior.project_id = current.project_id
JOIN project_deployments prior_deployment ON prior_deployment.id = prior.deployment_id
WHERE current.project_id = ? AND current.deployment_id = ?
  AND current_deployment.status IN ('active', 'superseded')
  AND prior_deployment.status IN ('active', 'superseded')
  AND prior_deployment.activated_at < current_deployment.activated_at
ORDER BY prior_deployment.activated_at DESC, prior.deployment_id DESC LIMIT 1;

-- name: ListAPIProjects :many
SELECT project_id, CAST(MIN(created_at) AS TEXT) AS created_at, CAST(MAX(updated_at) AS TEXT) AS updated_at FROM (
  SELECT project_id, created_at, COALESCE(finalized_at, created_at) AS updated_at FROM api_releases
  UNION ALL SELECT project_id, created_at, updated_at FROM managed_data_collections
) GROUP BY project_id ORDER BY project_id;

-- name: GetAPIProject :one
SELECT CAST(COALESCE(MIN(created_at), '') AS TEXT) AS created_at,
  CAST(COALESCE(MAX(updated_at), '') AS TEXT) AS updated_at FROM (
  SELECT created_at, COALESCE(finalized_at, created_at) AS updated_at FROM api_releases WHERE api_releases.project_id = sqlc.arg(project_id)
  UNION ALL SELECT created_at, updated_at FROM managed_data_collections WHERE managed_data_collections.project_id = sqlc.arg(project_id)
);

-- name: GetLatestAPIProjectReleaseID :one
SELECT id FROM api_releases WHERE project_id = ? ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: GetActiveAPIProjectDeploymentID :one
SELECT publication.id
FROM delivery_target_revisions target
JOIN delivery_publications publication
  ON publication.target_id = target.target_id
 AND publication.generation_id = target.active_generation_id
 AND publication.status = 'committed'
WHERE target.project_id = ?
ORDER BY publication.completed_at DESC, publication.id DESC
LIMIT 1;

-- name: ListAPIProjectConnections :many
SELECT c.connection_id, c.name, c.description, COALESCE(rev.digest, '') AS active_revision_id
FROM managed_data_collections c
LEFT JOIN delivery_target_revisions target ON target.project_id = c.project_id AND target.environment = ?
LEFT JOIN managed_data_serving_state_bindings binding
  ON binding.project_id = target.project_id
 AND binding.environment = target.environment
 AND binding.generation_id = target.active_generation_id
 AND binding.collection_id = c.id
LEFT JOIN managed_data_revisions rev ON rev.id = binding.revision_id
WHERE c.project_id = ? AND c.status = 'active' ORDER BY c.connection_id;

-- name: GetAPIProjectConnection :one
SELECT c.name, c.description, COALESCE(rev.digest, '') AS active_revision_id
FROM managed_data_collections c
LEFT JOIN delivery_target_revisions target ON target.project_id = c.project_id AND target.environment = ?
LEFT JOIN managed_data_serving_state_bindings binding
  ON binding.project_id = target.project_id
 AND binding.environment = target.environment
 AND binding.generation_id = target.active_generation_id
 AND binding.collection_id = c.id
LEFT JOIN managed_data_revisions rev ON rev.id = binding.revision_id
WHERE c.project_id = ? AND c.connection_id = ? AND c.status = 'active';
