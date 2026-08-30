-- Project identity persistence. Authored metadata is immutable after insert;
-- conflict handling and exact replay comparison remain in the repository.

-- name: InsertProjectIdentity :exec
INSERT INTO project.project_identity(project_id, title, description)
VALUES ($1, $2, $3)
ON CONFLICT (project_id) DO NOTHING;

-- name: InsertDefaultProjectIdentity :exec
INSERT INTO project.project_identity(project_id, title, description)
VALUES ($1, $1, '')
ON CONFLICT (project_id) DO NOTHING;

-- name: GetProjectIdentity :one
SELECT project_id, title, description, created_at, updated_at
FROM project.project_identity
WHERE project_id = $1;

-- name: ListProjectIdentities :many
SELECT project_id, title, description, created_at, updated_at
FROM project.project_identity
ORDER BY created_at, project_id;

-- Source delivery persistence. The repository owns validation, replay and
-- caller-owned transaction boundaries; these are fixed sqlc PostgreSQL leaves.

-- name: InsertSourceSyncPlan :exec
INSERT INTO project.source_sync_plan
    (plan_id, operation_id, project_id, storage_security_domain, owner_id,
     candidate_key, source_digest, project_file, request_digest, expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT (operation_id) DO NOTHING;

-- name: GetSourceSyncPlan :one
SELECT plan_id, operation_id, project_id, storage_security_domain, owner_id,
       candidate_key, source_digest, project_file, request_digest, state,
       expires_at, created_at, committed_at
FROM project.source_sync_plan
WHERE plan_id = $1;

-- name: GetSourceSyncPlanForUpdate :one
SELECT plan_id, operation_id, project_id, storage_security_domain, owner_id,
       candidate_key, source_digest, project_file, request_digest, state,
       expires_at, created_at, committed_at
FROM project.source_sync_plan
WHERE plan_id = $1
FOR UPDATE;

-- name: GetSourceSyncPlanByOperation :one
SELECT plan_id, operation_id, project_id, storage_security_domain, owner_id,
       candidate_key, source_digest, project_file, request_digest, state,
       expires_at, created_at, committed_at
FROM project.source_sync_plan
WHERE operation_id = $1;

-- name: SourceSyncPlanActive :one
SELECT state = 'open' AND expires_at > clock_timestamp() AS active
FROM project.source_sync_plan
WHERE plan_id = $1;

-- name: InsertSourceSyncPlanEntry :exec
INSERT INTO project.source_sync_plan_entry(plan_id, path, digest, size_bytes, ordinal)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (plan_id, path) DO NOTHING;

-- name: ListSourceSyncPlanEntries :many
SELECT plan_id, path, digest, size_bytes, ordinal
FROM project.source_sync_plan_entry
WHERE plan_id = $1
ORDER BY ordinal;

-- name: ListMissingSourceBlobDigests :many
SELECT requested.digest::text AS digest
FROM unnest($3::text[]) AS requested(digest)
LEFT JOIN project.source_blob b
  ON b.project_id = $1 AND b.storage_security_domain = $2
 AND b.digest = requested.digest
WHERE b.digest IS NULL
ORDER BY requested.digest;

-- name: InsertSourceBlob :exec
INSERT INTO project.source_blob
    (project_id, storage_security_domain, digest, size_bytes, object_key,
     content_type, metadata_digest)
SELECT $1,$2,$3,$4,$5,$6,$7
WHERE EXISTS (
    SELECT 1
    FROM project.source_sync_plan p
    JOIN project.source_sync_plan_entry e ON e.plan_id = p.plan_id
    WHERE p.plan_id = $8 AND p.owner_id = $9
      AND p.project_id = $1 AND p.storage_security_domain = $2
      AND p.state = 'open' AND p.expires_at > clock_timestamp()
      AND e.digest = $3
)
ON CONFLICT (project_id, storage_security_domain, digest) DO NOTHING;

-- name: GetSourceBlob :one
SELECT project_id, storage_security_domain, digest, size_bytes, object_key,
       content_type, metadata_digest, created_at
FROM project.source_blob
WHERE project_id = $1 AND storage_security_domain = $2 AND digest = $3;

-- name: InsertSourceSnapshot :one
INSERT INTO project.source_snapshot
    (snapshot_id, project_id, storage_security_domain, source_digest,
     project_file, project_digest, project_artifact_object_key,
     project_artifact_digest, project_artifact_size_bytes,
     manifest_object_key, manifest_object_digest, manifest_object_size_bytes,
     compiler_version, schema_version)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
ON CONFLICT DO NOTHING
RETURNING snapshot_id, project_id, storage_security_domain, source_digest,
          project_file, project_digest, project_artifact_object_key,
          project_artifact_digest, project_artifact_size_bytes,
          manifest_object_key, manifest_object_digest, manifest_object_size_bytes,
          compiler_version, schema_version, created_at;

-- name: GetSourceSnapshot :one
SELECT snapshot_id, project_id, storage_security_domain, source_digest,
       project_file, project_digest, project_artifact_object_key,
       project_artifact_digest, project_artifact_size_bytes,
       manifest_object_key, manifest_object_digest, manifest_object_size_bytes,
       compiler_version, schema_version, created_at
FROM project.source_snapshot
WHERE project_id = $1 AND storage_security_domain = $2 AND source_digest = $3
  AND state = 'sealed';

-- name: InsertSourceSnapshotEntry :exec
INSERT INTO project.source_snapshot_entry
    (snapshot_id, project_id, storage_security_domain, path, digest, size_bytes, ordinal)
VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (snapshot_id, path) DO NOTHING;

-- name: ListSourceSnapshotEntries :many
SELECT snapshot_id, project_id, storage_security_domain, path, digest, size_bytes, ordinal
FROM project.source_snapshot_entry
WHERE snapshot_id = $1
ORDER BY ordinal;

-- name: ListSealedSourceSnapshotObjectRefs :many
SELECT e.snapshot_id, e.project_id, e.storage_security_domain,
       e.path, e.digest, e.size_bytes, e.ordinal,
       b.digest AS blob_digest, b.size_bytes AS blob_size_bytes,
       b.object_key, b.content_type, b.metadata_digest
FROM project.source_snapshot s
JOIN project.source_snapshot_entry e
  ON e.snapshot_id = s.snapshot_id
 AND e.project_id = s.project_id
 AND e.storage_security_domain = s.storage_security_domain
JOIN project.source_blob b
  ON b.project_id = e.project_id
 AND b.storage_security_domain = e.storage_security_domain
 AND b.digest = e.digest
WHERE s.project_id = $1
  AND s.storage_security_domain = $2
  AND s.source_digest = $3
  AND s.state = 'sealed'
ORDER BY e.ordinal;

-- name: InsertSourceAttestation :exec
INSERT INTO project.source_attestation
    (attestation_id, snapshot_id, source_digest, attestation_digest, payload,
     revision, repository, ref, change_id)
VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9)
ON CONFLICT (attestation_id) DO NOTHING;

-- name: GetSourceAttestation :one
SELECT attestation_id, snapshot_id, source_digest, attestation_digest,
       payload::text AS payload, revision, repository, ref, change_id, created_at
FROM project.source_attestation
WHERE snapshot_id = $1 AND attestation_digest = $2;

-- name: TransitionSourceSyncPlanCommitted :execrows
UPDATE project.source_sync_plan
SET state = 'committed'
WHERE plan_id = $1 AND owner_id = $2 AND state = 'open' AND expires_at > clock_timestamp();

-- name: TransitionSourceSnapshotSealed :execrows
UPDATE project.source_snapshot
SET state = 'sealed'
WHERE snapshot_id = $1 AND state = 'building';
