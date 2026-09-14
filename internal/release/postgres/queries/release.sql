-- Static PostgreSQL query leaves for the release authority. Domain validation,
-- transaction ownership, and immutable replay comparison remain in Go.

-- name: InsertRelease :execrows
INSERT INTO release.release_record
    (release_id, project_id, environment, generation_id, project_digest,
     artifact_digest, request_digest, idempotency_key, status, provenance,
     created_by)
VALUES (sqlc.arg(release_id), sqlc.arg(project_id), sqlc.arg(environment), sqlc.arg(generation_id), sqlc.arg(project_digest), sqlc.arg(artifact_digest), sqlc.arg(request_digest), sqlc.arg(idempotency_key), 'draft', sqlc.arg(provenance)::jsonb, sqlc.arg(created_by))
ON CONFLICT (release_id) DO NOTHING;

-- name: GetRelease :one
SELECT release_id, project_id, environment, generation_id, project_digest,
       artifact_digest, COALESCE(artifact_actual_digest, '') AS artifact_actual_digest,
       artifact_size_bytes, artifact_uploaded_at, request_digest,
       idempotency_key, status, provenance::text, created_by, created_at,
       finalized_at, error
FROM release.release_record
WHERE project_id = $1 AND release_id = $2;

-- name: GetReleaseForUpdate :one
SELECT release_id, project_id, environment, generation_id, project_digest,
       artifact_digest, COALESCE(artifact_actual_digest, '') AS artifact_actual_digest,
       artifact_size_bytes, artifact_uploaded_at, request_digest,
       idempotency_key, status, provenance::text, created_by, created_at,
       finalized_at, error
FROM release.release_record
WHERE project_id = $1 AND release_id = $2
FOR UPDATE;

-- name: GetReleaseByIdempotency :one
SELECT release_id, project_id, environment, generation_id, project_digest,
       artifact_digest, COALESCE(artifact_actual_digest, '') AS artifact_actual_digest,
       artifact_size_bytes, artifact_uploaded_at, request_digest,
       idempotency_key, status, provenance::text, created_by, created_at,
       finalized_at, error
FROM release.release_record
WHERE project_id = $1 AND idempotency_key = $2;

-- name: ListReleases :many
SELECT release_id, project_id, environment, generation_id, project_digest,
       artifact_digest, COALESCE(artifact_actual_digest, '') AS artifact_actual_digest,
       artifact_size_bytes, artifact_uploaded_at, request_digest,
       idempotency_key, status, provenance::text, created_by, created_at,
       finalized_at, error
FROM release.release_record
WHERE project_id = $1
ORDER BY created_at DESC, release_id DESC;

-- name: ListReleaseConnections :many
SELECT connection_id, revision_id
FROM release.release_connection
WHERE release_id = $1
ORDER BY connection_id;

-- name: InsertReleaseConnection :exec
INSERT INTO release.release_connection (release_id, connection_id, revision_id)
VALUES ($1, $2, $3)
ON CONFLICT (release_id, connection_id) DO NOTHING;

-- name: RecordArtifact :execrows
UPDATE release.release_record
SET artifact_actual_digest = $1, artifact_size_bytes = $2,
    artifact_uploaded_at = clock_timestamp()
WHERE release_id = $3 AND project_id = $4 AND environment = $5
  AND generation_id = $6 AND artifact_digest = $7
  AND status = 'draft' AND artifact_uploaded_at IS NULL;

-- name: MarkValidating :execrows
UPDATE release.release_record
SET status = 'validating'
WHERE release_id = $1 AND project_id = $2 AND status = 'draft';

-- name: MarkReady :execrows
UPDATE release.release_record
SET status = 'ready', finalized_at = clock_timestamp()
WHERE release_id = $1 AND project_id = $2 AND status = 'validating';

-- name: MarkFailed :execrows
UPDATE release.release_record
SET status = 'failed', error = $1, finalized_at = clock_timestamp()
WHERE release_id = $2 AND project_id = $3 AND status = 'validating';

-- name: InsertCandidateProvenance :exec
INSERT INTO release.candidate_provenance
    (project_id, candidate_id, candidate_revision, provenance_digest, provenance)
VALUES (sqlc.arg(project_id), sqlc.arg(candidate_id), sqlc.arg(candidate_revision), sqlc.arg(provenance_digest), sqlc.arg(provenance)::jsonb)
ON CONFLICT (project_id, candidate_id, candidate_revision) DO NOTHING;

-- name: GetCandidateProvenance :one
SELECT provenance_digest, provenance::text
FROM release.candidate_provenance
WHERE project_id = $1 AND candidate_id = $2 AND candidate_revision = $3;

-- name: ListCandidateProvenanceByGeneration :many
SELECT provenance::text
FROM release.candidate_provenance
WHERE project_id = sqlc.arg(project_id)
  AND provenance -> 'plan' -> 'identity' ->> 'environment' = sqlc.arg(environment)::text
  AND provenance -> 'plan' -> 'identity' ->> 'generationId' = sqlc.arg(generation_id)::text
ORDER BY retained_at DESC, candidate_id DESC, candidate_revision DESC
LIMIT 2;

-- name: InsertDeploymentLinkage :exec
INSERT INTO release.deployment_linkage
    (deployment_id, project_id, release_id, rollback_of)
VALUES ($1, $2, $3, NULLIF(sqlc.arg(rollback_of), ''))
ON CONFLICT (deployment_id) DO NOTHING;

-- name: GetDeploymentLinkage :one
SELECT deployment_id, project_id, release_id, COALESCE(rollback_of, '') AS rollback_of, created_at
FROM release.deployment_linkage
WHERE project_id = $1 AND deployment_id = $2;

-- name: GetDeploymentLinkageByID :one
SELECT deployment_id, project_id, release_id, COALESCE(rollback_of, '') AS rollback_of, created_at
FROM release.deployment_linkage
WHERE deployment_id = $1;

-- name: ListDeploymentIDs :many
SELECT deployment_id
FROM release.deployment_linkage
WHERE project_id = $1
ORDER BY created_at DESC, deployment_id DESC;

-- name: GetPriorDeploymentRelease :one
SELECT prior.release_id
FROM release.deployment_linkage current
JOIN release.deployment_linkage prior
  ON prior.project_id = current.project_id
 AND (prior.created_at, prior.deployment_id) < (current.created_at, current.deployment_id)
WHERE current.project_id = $1 AND current.deployment_id = $2
ORDER BY prior.created_at DESC, prior.deployment_id DESC
LIMIT 1;

-- name: GetReadyReleaseProvenanceByGeneration :one
SELECT provenance::text
FROM release.release_record
WHERE project_id = $1 AND environment = $2 AND generation_id = $3 AND status = 'ready'
ORDER BY finalized_at DESC, release_id DESC
LIMIT 1;

-- Transition policy authority.  Publication is insert-only and the exact
-- pair is read back so the repository can distinguish an idempotent replay
-- from a conflicting policy for the same immutable artifacts.

-- name: InsertReleaseTransitionPolicy :execrows
INSERT INTO release.release_transition_policy
    (predecessor_artifact_digest, candidate_artifact_digest,
     policy_version, policy_digest, policy_json)
VALUES (sqlc.arg(predecessor_artifact_digest), sqlc.arg(candidate_artifact_digest),
        sqlc.arg(policy_version), sqlc.arg(policy_digest), sqlc.arg(policy_json)::jsonb)
ON CONFLICT (predecessor_artifact_digest, candidate_artifact_digest) DO NOTHING;

-- name: GetReleaseTransitionPolicy :one
SELECT predecessor_artifact_digest, candidate_artifact_digest,
       policy_version, policy_digest, policy_json::text, published_at
FROM release.release_transition_policy
WHERE predecessor_artifact_digest = $1 AND candidate_artifact_digest = $2;

-- OCI artifact admission authority. Publication and revocation are append-only;
-- runtime resolution loads canonical evidence and any independent revocation.

-- name: InsertOCIArtifactAdmission :execrows
INSERT INTO release.oci_artifact_admission
    (artifact_reference, repository_identity, oci_digest, admission_version,
     admission_digest, admission_bytes, admitted_at)
VALUES (sqlc.arg(artifact_reference), sqlc.arg(repository_identity), sqlc.arg(oci_digest),
        sqlc.arg(admission_version), sqlc.arg(admission_digest),
        sqlc.arg(admission_bytes), sqlc.arg(admitted_at))
ON CONFLICT DO NOTHING;

-- name: GetOCIArtifactAdmission :one
SELECT a.artifact_reference, a.repository_identity, a.oci_digest,
       a.admission_version, a.admission_digest, a.admission_bytes,
       a.admitted_at, a.published_at,
       r.admission_digest AS revoked_admission_digest,
       r.revoked_at, r.reason AS revocation_reason
FROM release.oci_artifact_admission a
LEFT JOIN release.oci_artifact_admission_revocation r
  ON r.artifact_reference = a.artifact_reference
WHERE a.artifact_reference = $1;

-- name: InsertOCIArtifactAdmissionRevocation :execrows
INSERT INTO release.oci_artifact_admission_revocation
    (artifact_reference, admission_digest, revoked_at, reason)
VALUES (sqlc.arg(artifact_reference), sqlc.arg(admission_digest),
        sqlc.arg(revoked_at), sqlc.arg(reason))
ON CONFLICT (artifact_reference) DO NOTHING;

-- Per-artifact migration capability authority. Publication is insert-only;
-- runtime resolution loads exact canonical evidence for one admitted artifact,
-- target, and subsystem.

-- name: GetOCIArtifactReferenceByAdmissionDigest :one
SELECT artifact_reference
FROM release.oci_artifact_admission
WHERE admission_digest = $1;

-- name: InsertMigrationCapability :execrows
INSERT INTO release.migration_capability
    (artifact_admission_digest, target_identity_digest, subsystem,
     owner_identity, owner_contract_version, capability_version,
     capability_digest, capability_bytes)
VALUES (sqlc.arg(artifact_admission_digest), sqlc.arg(target_identity_digest),
        sqlc.arg(subsystem), sqlc.arg(owner_identity),
        sqlc.arg(owner_contract_version), sqlc.arg(capability_version),
        sqlc.arg(capability_digest), sqlc.arg(capability_bytes))
ON CONFLICT DO NOTHING;

-- name: GetMigrationCapability :one
SELECT artifact_admission_digest, target_identity_digest, subsystem,
       owner_identity, owner_contract_version, capability_version,
       capability_digest, capability_bytes, published_at
FROM release.migration_capability
WHERE artifact_admission_digest = $1
  AND target_identity_digest = $2
  AND subsystem = $3;
