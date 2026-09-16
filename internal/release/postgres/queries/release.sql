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

-- Per-artifact migration capability authority. Publication is insert-only and
-- owner-authenticated; runtime resolution loads exact canonical evidence for
-- one admitted artifact, target, and subsystem.

-- name: GetOCIArtifactReferenceByAdmissionDigest :one
SELECT artifact_reference
FROM release.oci_artifact_admission
WHERE admission_digest = $1;

-- name: LockOCIArtifactAdmissionByReference :one
SELECT l.artifact_reference
FROM release.lock_oci_artifact_admission($1) AS l(artifact_reference);

-- name: InsertMigrationCapability :execrows
INSERT INTO release.migration_capability
    (artifact_admission_digest, target_identity_digest, subsystem,
     owner_identity, owner_contract_version, capability_version,
     capability_digest, capability_bytes, owner_evidence_version,
     owner_evidence_digest, owner_evidence_bytes)
VALUES (sqlc.arg(artifact_admission_digest), sqlc.arg(target_identity_digest),
        sqlc.arg(subsystem), sqlc.arg(owner_identity),
        sqlc.arg(owner_contract_version), sqlc.arg(capability_version),
        sqlc.arg(capability_digest), sqlc.arg(capability_bytes),
        sqlc.arg(owner_evidence_version), sqlc.arg(owner_evidence_digest),
        sqlc.arg(owner_evidence_bytes))
ON CONFLICT DO NOTHING;

-- name: GetMigrationCapability :one
SELECT artifact_admission_digest, target_identity_digest, subsystem,
       owner_identity, owner_contract_version, capability_version,
       capability_digest, capability_bytes, owner_evidence_version,
       owner_evidence_digest, owner_evidence_bytes, published_at
FROM release.migration_capability
WHERE artifact_admission_digest = $1
  AND target_identity_digest = $2
  AND subsystem = $3;

-- Durable transition operation persistence. The repository performs the
-- canonical identity comparison and maps affected-row counts to its typed
-- conflict/fence errors.
-- name: InsertTransitionOperation :execrows
INSERT INTO release.release_transition_operation
    (operation_id, target_identity_digest, predecessor_artifact_digest,
     candidate_artifact_digest, recovery_frontier_id, recovery_frontier_digest,
     preflight_evidence_digest, preflight_evidence, idempotency_key,
     request_digest, status, current_phase)
VALUES (sqlc.arg(operation_id)::uuid, sqlc.arg(target_identity_digest),
        sqlc.arg(predecessor_artifact_digest), sqlc.arg(candidate_artifact_digest),
        sqlc.arg(recovery_frontier_id), sqlc.arg(recovery_frontier_digest),
        sqlc.arg(preflight_evidence_digest), sqlc.arg(preflight_evidence),
        sqlc.arg(idempotency_key), sqlc.arg(request_digest), 'pending', 'preflight')
ON CONFLICT (target_identity_digest, idempotency_key) DO NOTHING;

-- name: GetTransitionOperation :one
SELECT operation_id, target_identity_digest, predecessor_artifact_digest,
       candidate_artifact_digest, recovery_frontier_id, recovery_frontier_digest,
       preflight_evidence_digest, preflight_evidence, idempotency_key,
       request_digest, status, current_phase, owner_id, fencing_generation,
       COALESCE(lease_expires_at, 'epoch'::timestamptz), created_at, updated_at,
       COALESCE(terminal_at, 'epoch'::timestamptz)
FROM release.release_transition_operation
WHERE operation_id = $1::uuid;

-- name: GetTransitionOperationByIdempotency :one
SELECT operation_id, target_identity_digest, predecessor_artifact_digest,
       candidate_artifact_digest, recovery_frontier_id, recovery_frontier_digest,
       preflight_evidence_digest, preflight_evidence, idempotency_key,
       request_digest, status, current_phase, owner_id, fencing_generation,
       COALESCE(lease_expires_at, 'epoch'::timestamptz), created_at, updated_at,
       COALESCE(terminal_at, 'epoch'::timestamptz)
FROM release.release_transition_operation
WHERE target_identity_digest = $1 AND idempotency_key = $2;

-- name: LockTransitionOperationByIdempotency :one
SELECT operation_id, target_identity_digest, predecessor_artifact_digest,
       candidate_artifact_digest, recovery_frontier_id, recovery_frontier_digest,
       preflight_evidence_digest, preflight_evidence, idempotency_key,
       request_digest, status, current_phase, owner_id, fencing_generation,
       COALESCE(lease_expires_at, 'epoch'::timestamptz), created_at, updated_at,
       COALESCE(terminal_at, 'epoch'::timestamptz)
FROM release.release_transition_operation
WHERE target_identity_digest = $1 AND idempotency_key = $2
FOR UPDATE;

-- name: LockTransitionOperation :one
SELECT operation_id, target_identity_digest, predecessor_artifact_digest,
       candidate_artifact_digest, recovery_frontier_id, recovery_frontier_digest,
       preflight_evidence_digest, preflight_evidence, idempotency_key,
       request_digest, status, current_phase, owner_id, fencing_generation,
       COALESCE(lease_expires_at, 'epoch'::timestamptz), created_at, updated_at,
       COALESCE(terminal_at, 'epoch'::timestamptz)
FROM release.release_transition_operation
WHERE operation_id = $1::uuid
FOR UPDATE;

-- name: LockTransitionFence :one
SELECT target_identity_digest, operation_id, owner_id, fencing_generation,
       lease_expires_at, updated_at
FROM release.release_transition_fence
WHERE target_identity_digest = $1
FOR UPDATE;

-- name: CurrentTransitionDatabaseTime :one
SELECT clock_timestamp()::timestamptz;

-- name: EnsureTransitionFence :execrows
INSERT INTO release.release_transition_fence(target_identity_digest)
VALUES ($1)
ON CONFLICT (target_identity_digest) DO NOTHING;

-- name: MarkTransitionIndeterminate :execrows
UPDATE release.release_transition_operation
SET status = 'indeterminate', terminal_at = clock_timestamp(), owner_id = '',
    lease_expires_at = NULL, updated_at = clock_timestamp()
WHERE operation_id = $1::uuid AND status = 'running';

-- name: ClearTransitionFence :execrows
UPDATE release.release_transition_fence
SET operation_id = NULL, owner_id = '', lease_expires_at = NULL,
    updated_at = clock_timestamp()
WHERE target_identity_digest = $1
  AND ($2::uuid IS NULL OR operation_id = $2::uuid)
  AND ($3::text = '' OR owner_id = $3)
  AND ($4::bigint < 0 OR fencing_generation = $4);

-- name: ClaimTransitionFence :execrows
UPDATE release.release_transition_fence
SET operation_id = $1::uuid, owner_id = $2, fencing_generation = $3,
    lease_expires_at = clock_timestamp() + $4::interval,
    updated_at = clock_timestamp()
WHERE target_identity_digest = $5;

-- name: ClaimTransitionOperation :execrows
UPDATE release.release_transition_operation
SET owner_id = $1, fencing_generation = $2,
    lease_expires_at = clock_timestamp() + $3::interval,
    updated_at = clock_timestamp(), status = 'running'
WHERE operation_id = $4::uuid AND status IN ('pending','running');

-- name: InsertTransitionPhaseResult :execrows
INSERT INTO release.release_transition_phase_result
    (operation_id, phase, result_status, result_digest, result_bytes,
     started_at, completed_at)
VALUES (sqlc.arg(operation_id)::uuid, sqlc.arg(phase), sqlc.arg(result_status),
        sqlc.arg(result_digest), sqlc.arg(result_bytes),
        sqlc.arg(started_at), sqlc.arg(completed_at))
ON CONFLICT (operation_id, phase) DO NOTHING;

-- name: GetTransitionPhaseResult :one
SELECT result_status, result_digest, result_bytes
FROM release.release_transition_phase_result
WHERE operation_id = $1::uuid AND phase = $2;

-- name: ListTransitionPhaseResults :many
SELECT phase, result_status, result_digest, result_bytes, started_at,
       completed_at
FROM release.release_transition_phase_result
WHERE operation_id = $1::uuid
ORDER BY CASE phase WHEN 'preflight' THEN 1 WHEN 'migrations' THEN 2
                    WHEN 'candidate-staged' THEN 3 WHEN 'candidate-activated' THEN 4
                    WHEN 'candidate-restarted' THEN 5 WHEN 'post-validated' THEN 6
                    WHEN 'success' THEN 7 END;

-- name: AdvanceTransitionOperation :execrows
UPDATE release.release_transition_operation
SET status = $1, current_phase = $2, updated_at = clock_timestamp(),
    terminal_at = CASE WHEN $1 IN ('completed','failed','indeterminate') THEN clock_timestamp() ELSE NULL END,
    owner_id = CASE WHEN $1 IN ('completed','failed','indeterminate') THEN '' ELSE owner_id END,
    lease_expires_at = CASE WHEN $1 IN ('completed','failed','indeterminate') THEN NULL ELSE lease_expires_at END
WHERE operation_id = $3::uuid AND owner_id = $4 AND fencing_generation = $5
  AND (lease_expires_at > clock_timestamp() OR $1 = 'indeterminate')
  AND status IN ('pending','running');

-- name: RenewTransitionFence :one
UPDATE release.release_transition_fence
SET lease_expires_at = clock_timestamp() + $1::interval,
    updated_at = clock_timestamp()
WHERE target_identity_digest = $5 AND operation_id = $2::uuid AND owner_id = $3
  AND fencing_generation = $4 AND lease_expires_at > clock_timestamp()
RETURNING target_identity_digest, operation_id, owner_id, fencing_generation,
          lease_expires_at, updated_at;

-- name: UpdateTransitionOperationLease :execrows
UPDATE release.release_transition_operation
SET lease_expires_at = $1, updated_at = clock_timestamp()
WHERE operation_id = $2::uuid AND owner_id = $3 AND fencing_generation = $4
  AND lease_expires_at > clock_timestamp();

-- name: ValidateTransitionFence :one
SELECT f.lease_expires_at > clock_timestamp() AS active
FROM release.release_transition_fence f
JOIN release.release_transition_operation o
  ON o.target_identity_digest = f.target_identity_digest
WHERE f.operation_id = $1::uuid AND f.owner_id = $2
  AND f.fencing_generation = $3 AND f.operation_id = o.operation_id;

-- name: ReleaseTransitionFence :execrows
UPDATE release.release_transition_fence f
SET operation_id = NULL, owner_id = '', lease_expires_at = NULL,
    updated_at = clock_timestamp()
FROM release.release_transition_operation o
WHERE f.target_identity_digest = o.target_identity_digest
  AND f.operation_id = $1::uuid AND f.owner_id = $2
  AND f.fencing_generation = $3 AND o.operation_id = f.operation_id
  AND o.status IN ('completed','failed','indeterminate');

-- name: CompleteTransitionOperation :execrows
UPDATE release.release_transition_operation
SET status = $1, terminal_at = clock_timestamp(), owner_id = '',
    lease_expires_at = NULL, updated_at = clock_timestamp()
WHERE operation_id = $2::uuid AND owner_id = $3 AND fencing_generation = $4
  AND lease_expires_at > clock_timestamp();

-- name: CompleteRecordedTransitionOperation :execrows
UPDATE release.release_transition_operation
SET status = 'completed', terminal_at = clock_timestamp(), owner_id = '',
    lease_expires_at = NULL, updated_at = clock_timestamp()
WHERE operation_id = $1::uuid AND status = 'running';
