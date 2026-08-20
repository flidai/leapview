-- Managed-data collections, uploads, multipart state, and revisions.

-- name: CreateManagedDataCollection :exec
INSERT INTO managed_data_collections (id, project_id, connection_id, name, description, created_by)
VALUES (?, ?, ?, ?, ?, ?);

-- name: GetManagedDataCollection :one
SELECT * FROM managed_data_collections WHERE id = ?;

-- name: GetManagedDataCollectionByProjectConnection :one
SELECT * FROM managed_data_collections
WHERE project_id = ? AND connection_id = ?;

-- name: ListActiveManagedDataCollections :many
SELECT * FROM managed_data_collections
WHERE status = 'active'
ORDER BY project_id, connection_id, id;

-- name: ListAllManagedDataCollections :many
SELECT * FROM managed_data_collections
ORDER BY project_id, connection_id, id;

-- name: ArchiveManagedDataCollection :execresult
UPDATE managed_data_collections
SET status = 'archived', archived_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status = 'active';

-- name: CreateManagedDataUploadSession :exec
INSERT INTO managed_data_upload_sessions (
  id, collection_id, base_revision_id, status, manifest_json,
  expected_file_count, expected_size_bytes, storage_backend, staging_prefix,
  created_by, expires_at
)
VALUES (?, ?, ?, 'open', ?, ?, ?, ?, ?, ?, ?);

-- name: GetManagedDataUploadSession :one
SELECT * FROM managed_data_upload_sessions WHERE id = ?;

-- name: ListManagedDataUploadSessions :many
SELECT * FROM managed_data_upload_sessions
WHERE collection_id = ?
ORDER BY created_at DESC, id DESC;

-- name: ListManagedDataUploadSessionsForCleanup :many
SELECT * FROM managed_data_upload_sessions
WHERE status IN ('complete', 'aborted', 'expired', 'failed') AND cleanup_completed_at IS NULL
ORDER BY updated_at, id
LIMIT ?;

-- name: ClaimManagedDataMultipartDigest :one
INSERT INTO managed_data_multipart_claims (sha256, owner_id, lease_generation, lease_until)
VALUES (sqlc.arg(sha256), sqlc.arg(owner_id), 1, sqlc.arg(lease_until))
ON CONFLICT(sha256) DO UPDATE SET
  owner_id = excluded.owner_id,
  lease_generation = managed_data_multipart_claims.lease_generation + 1,
  lease_until = excluded.lease_until
WHERE managed_data_multipart_claims.lease_until <= CURRENT_TIMESTAMP
RETURNING lease_generation;

-- name: RenewManagedDataMultipartDigest :execrows
UPDATE managed_data_multipart_claims
SET lease_until = sqlc.arg(lease_until)
WHERE sha256 = sqlc.arg(sha256) AND owner_id = sqlc.arg(owner_id)
  AND lease_generation = sqlc.arg(lease_generation)
  AND managed_data_multipart_claims.lease_until > CURRENT_TIMESTAMP;

-- name: ReleaseManagedDataMultipartDigest :execrows
DELETE FROM managed_data_multipart_claims
WHERE sha256 = sqlc.arg(sha256) AND owner_id = sqlc.arg(owner_id)
  AND lease_generation = sqlc.arg(lease_generation);

-- name: MarkManagedDataUploadCleanupComplete :execresult
UPDATE managed_data_upload_sessions
SET cleanup_completed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status IN ('complete', 'aborted', 'expired', 'failed') AND cleanup_completed_at IS NULL;

-- name: MarkManagedDataUploadCommitting :execresult
UPDATE managed_data_upload_sessions
SET status = 'committing', updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status IN ('open', 'committing');

-- name: BeginManagedDataUploadFinalization :execresult
UPDATE managed_data_upload_sessions
SET status = 'committing', updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status = 'open' AND expires_at > CURRENT_TIMESTAMP;

-- name: FailManagedDataUploadFinalization :execresult
UPDATE managed_data_upload_sessions
SET status = 'failed', error = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status = 'committing';

-- name: UpdateManagedDataUploadProgress :execresult
UPDATE managed_data_upload_sessions
SET uploaded_file_count = ?, uploaded_size_bytes = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status = 'open'
  AND ? <= expected_file_count
  AND ? <= expected_size_bytes;

-- name: AbortManagedDataUploadSession :execresult
UPDATE managed_data_upload_sessions
SET status = 'aborted', updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status = 'open';

-- name: ExpireManagedDataUploadSessions :execresult
UPDATE managed_data_upload_sessions
SET status = 'expired', updated_at = CURRENT_TIMESTAMP
WHERE status = 'open' AND expires_at <= ?;

-- name: CreateManagedDataS3MultipartUpload :exec
INSERT INTO managed_data_s3_multipart_uploads (
  id, upload_session_id, logical_path, sha256, size_bytes, idempotency_identity
)
VALUES (?, ?, ?, ?, ?, ?);

-- name: GetManagedDataS3MultipartUpload :one
SELECT * FROM managed_data_s3_multipart_uploads WHERE id = ?;

-- name: GetManagedDataS3MultipartUploadByIdentity :one
SELECT * FROM managed_data_s3_multipart_uploads
WHERE upload_session_id = ? AND idempotency_identity = ?;

-- name: InitializeManagedDataS3MultipartUpload :execresult
UPDATE managed_data_s3_multipart_uploads
SET object_key = ?, provider_upload_id = ?, status = 'open', updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status = 'creating' AND existing = 0;

-- name: InitializeExistingManagedDataS3MultipartUpload :execresult
UPDATE managed_data_s3_multipart_uploads
SET object_key = ?, provider_upload_id = '', status = 'completed', existing = 1,
    completed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status = 'creating';

-- name: CreateManagedDataS3MultipartPart :exec
INSERT INTO managed_data_s3_multipart_parts (
  multipart_upload_id, part_number, size_bytes, sha256
)
VALUES (?, ?, ?, ?);

-- name: GetManagedDataS3MultipartPart :one
SELECT * FROM managed_data_s3_multipart_parts
WHERE multipart_upload_id = ? AND part_number = ?;

-- name: ListManagedDataS3MultipartParts :many
SELECT * FROM managed_data_s3_multipart_parts
WHERE multipart_upload_id = ?
ORDER BY part_number;

-- name: SumManagedDataS3MultipartPartSizes :one
SELECT CAST(COALESCE(SUM(size_bytes), 0) AS INTEGER)
FROM managed_data_s3_multipart_parts
WHERE multipart_upload_id = ?;

-- name: BeginManagedDataS3MultipartCompletion :execresult
UPDATE managed_data_s3_multipart_uploads
SET status = 'completing', completion_identity = ?, completion_request_hash = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status = 'open';

-- name: FinishManagedDataS3MultipartCompletion :execresult
UPDATE managed_data_s3_multipart_uploads
SET status = 'completed', completed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status = 'completing';

-- name: BeginManagedDataS3MultipartAbort :execresult
UPDATE managed_data_s3_multipart_uploads
SET status = 'aborting', abort_identity = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status IN ('creating', 'open', 'failed');

-- name: FinishManagedDataS3MultipartAbort :execresult
UPDATE managed_data_s3_multipart_uploads
SET status = 'aborted', aborted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status = 'aborting';

-- name: FailManagedDataS3MultipartUpload :execresult
UPDATE managed_data_s3_multipart_uploads
SET status = 'failed', error = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status IN ('creating', 'open', 'completing');

-- name: ListRecoverableManagedDataS3MultipartUploads :many
SELECT multipart.*
FROM managed_data_s3_multipart_uploads AS multipart
JOIN managed_data_upload_sessions AS session ON session.id = multipart.upload_session_id
WHERE (multipart.provider_upload_id <> '' OR multipart.status = 'creating')
  AND multipart.updated_at <= sqlc.arg(updated_cutoff)
  AND (
    multipart.status IN ('aborting', 'failed', 'creating', 'completing')
    OR (
      multipart.status = 'open'
      AND (
        session.status IN ('complete', 'aborted', 'expired', 'failed')
        OR (session.status = 'open' AND session.expires_at <= sqlc.arg(expiry_cutoff))
      )
    )
  )
ORDER BY multipart.updated_at, multipart.id
LIMIT sqlc.arg(row_limit);

-- name: ListManagedDataS3MultipartProviderIDsByDigest :many
SELECT provider_upload_id FROM managed_data_s3_multipart_uploads
WHERE sha256 = ? AND provider_upload_id <> ''
ORDER BY provider_upload_id;

-- name: ListCreatingManagedDataS3MultipartIDsByDigest :many
SELECT id FROM managed_data_s3_multipart_uploads
WHERE sha256 = ? AND status = 'creating'
ORDER BY id;

-- name: NextManagedDataRevisionSequence :one
SELECT COALESCE(MAX(sequence), 0) + 1
FROM managed_data_revisions
WHERE collection_id = ?;

-- name: CreateReadyManagedDataRevision :exec
INSERT INTO managed_data_revisions (
  id, collection_id, sequence, digest, status, manifest_json,
  file_count, size_bytes, created_by, ready_at
)
VALUES (?, ?, ?, ?, 'ready', ?, ?, ?, ?, CURRENT_TIMESTAMP);

-- name: CreateManagedDataRevisionFile :exec
INSERT INTO managed_data_revision_files (
  revision_id, logical_path, size_bytes, sha256, storage_key, media_type, etag
)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: CompleteManagedDataUploadSession :execresult
UPDATE managed_data_upload_sessions
SET status = 'complete', revision_id = ?,
    uploaded_file_count = expected_file_count,
    uploaded_size_bytes = expected_size_bytes,
    completed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status = 'committing';

-- name: GetManagedDataRevision :one
SELECT * FROM managed_data_revisions WHERE id = ?;

-- name: ListManagedDataRevisions :many
SELECT * FROM managed_data_revisions
WHERE collection_id = ?
ORDER BY sequence DESC;

-- name: ListManagedDataReachabilitySources :many
SELECT source_type, source_id, source_status, revision_digest, manifest_json, file_count, size_bytes
FROM (
  SELECT
    'revision' AS source_type,
    id AS source_id,
    status AS source_status,
    digest AS revision_digest,
    manifest_json,
    file_count,
    size_bytes
  FROM managed_data_revisions
  WHERE status = 'ready'
  UNION ALL
  SELECT
    'upload' AS source_type,
    id AS source_id,
    status AS source_status,
    '' AS revision_digest,
    manifest_json,
    expected_file_count AS file_count,
    expected_size_bytes AS size_bytes
  FROM managed_data_upload_sessions
  WHERE status IN ('open', 'committing')
)
ORDER BY source_type, source_id;

-- name: GetManagedDataUploadSessionIDByRevision :one
SELECT id FROM managed_data_upload_sessions
WHERE revision_id = ? AND status = 'complete';

-- name: ListManagedDataRevisionFiles :many
SELECT * FROM managed_data_revision_files
WHERE revision_id = ?
ORDER BY logical_path;

-- Managed-data-owned environment pointers and serving-generation bindings.

-- name: GetManagedDataEnvironmentPointer :one
SELECT * FROM managed_data_environment_pointers
WHERE collection_id = ? AND environment = ?;

-- name: GetActiveManagedDataServingPointer :one
SELECT
  binding.collection_id,
  target.environment,
  binding.revision_id,
  CAST(COALESCE((
    SELECT publication.id
    FROM delivery_publications publication
    WHERE publication.target_id = target.target_id
      AND publication.generation_id = target.active_generation_id
      AND publication.status = 'committed'
    ORDER BY publication.completed_at DESC, publication.id DESC
    LIMIT 1
  ), '') AS TEXT) AS deployment_id,
  target.target_revision AS generation,
  '' AS updated_by,
  target.updated_at
FROM managed_data_collections collection
JOIN delivery_target_revisions target
  ON target.project_id = collection.project_id
 AND target.environment = sqlc.arg(environment)
 AND target.active_generation_id IS NOT NULL
JOIN managed_data_serving_state_bindings binding
  ON binding.project_id = target.project_id
 AND binding.environment = target.environment
 AND binding.generation_id = target.active_generation_id
 AND binding.collection_id = collection.id
WHERE collection.id = sqlc.arg(collection_id);

-- name: InstallManagedDataServingStateBindingSet :execrows
INSERT INTO managed_data_serving_state_binding_sets (
  project_id, environment, generation_id, binding_digest, binding_count
)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(project_id, environment, generation_id) DO NOTHING;

-- name: GetManagedDataServingStateBindingSet :one
SELECT * FROM managed_data_serving_state_binding_sets
WHERE project_id = ? AND environment = ? AND generation_id = ?;

-- name: InstallManagedDataServingStateBinding :exec
INSERT INTO managed_data_serving_state_bindings (
  project_id, environment, generation_id, collection_id, revision_id
)
VALUES (?, ?, ?, ?, ?)
;

-- name: ListManagedDataServingStateBindings :many
SELECT * FROM managed_data_serving_state_bindings
WHERE project_id = ? AND environment = ? AND generation_id = ?
ORDER BY collection_id;

-- name: GetManagedDataServingStateBinding :one
SELECT * FROM managed_data_serving_state_bindings
WHERE project_id = ? AND environment = ? AND generation_id = ? AND collection_id = ?;
