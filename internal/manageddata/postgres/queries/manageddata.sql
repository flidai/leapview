-- Managed-data PostgreSQL leaf queries. Repository methods retain validation,
-- domain mapping, transaction ownership, and cross-store orchestration.

-- name: InsertCollection :exec
INSERT INTO managed_data.collection
    (collection_id, project_id, connection_id, name, description, created_by, request_digest)
VALUES (sqlc.arg(collection_id), sqlc.arg(project_id), sqlc.arg(connection_id),
        sqlc.arg(name), sqlc.arg(description), sqlc.arg(created_by), sqlc.arg(request_digest))
ON CONFLICT DO NOTHING;

-- name: GetCollectionRequestDigest :one
SELECT request_digest FROM managed_data.collection
WHERE collection_id = sqlc.arg(collection_id);

-- name: GetCollectionByID :one
SELECT collection_id, project_id, connection_id, name, description, status,
       created_by, created_at, updated_at, archived_at
FROM managed_data.collection
WHERE collection_id = sqlc.arg(collection_id);

-- name: GetCollectionByProjectConnection :one
SELECT collection_id, project_id, connection_id, name, description, status,
       created_by, created_at, updated_at, archived_at
FROM managed_data.collection
WHERE project_id = sqlc.arg(project_id) AND connection_id = sqlc.arg(connection_id);

-- name: ListCollections :many
SELECT collection_id, project_id, connection_id, name, description, status,
       created_by, created_at, updated_at, archived_at
FROM managed_data.collection
ORDER BY project_id, connection_id, collection_id;

-- name: ListActiveCollections :many
SELECT collection_id, project_id, connection_id, name, description, status,
       created_by, created_at, updated_at, archived_at
FROM managed_data.collection
WHERE status = 'active'
ORDER BY project_id, connection_id, collection_id;

-- name: ArchiveCollection :execresult
UPDATE managed_data.collection
SET status = 'archived', archived_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE collection_id = sqlc.arg(collection_id) AND status = 'active';

-- name: InsertUploadSession :exec
INSERT INTO managed_data.upload_session
    (upload_id, collection_id, base_revision_id, manifest, expected_file_count,
     expected_size_bytes, storage_backend, staging_prefix, created_by, expires_at,
     request_digest, manifest_digest)
VALUES (sqlc.arg(upload_id), sqlc.arg(collection_id), sqlc.narg(base_revision_id),
        sqlc.arg(manifest)::jsonb, sqlc.arg(expected_file_count), sqlc.arg(expected_size_bytes),
        sqlc.arg(storage_backend), sqlc.arg(staging_prefix), sqlc.arg(created_by),
        sqlc.arg(expires_at), sqlc.arg(request_digest), sqlc.arg(manifest_digest))
ON CONFLICT (upload_id) DO NOTHING;

-- name: GetUploadRequestDigest :one
SELECT request_digest FROM managed_data.upload_session
WHERE upload_id = sqlc.arg(upload_id);

-- name: GetUploadSessionByID :one
SELECT upload_id, collection_id, COALESCE(base_revision_id, ''),
       COALESCE(revision_id, ''), status, manifest::text, expected_file_count,
       expected_size_bytes, uploaded_file_count, uploaded_size_bytes, storage_backend,
       staging_prefix, created_by, created_at, updated_at, expires_at,
       completed_at, error
FROM managed_data.upload_session
WHERE upload_id = sqlc.arg(upload_id);

-- name: ListUploadSessionsByCollection :many
SELECT upload_id, collection_id, COALESCE(base_revision_id, ''),
       COALESCE(revision_id, ''), status, manifest::text, expected_file_count,
       expected_size_bytes, uploaded_file_count, uploaded_size_bytes, storage_backend,
       staging_prefix, created_by, created_at, updated_at, expires_at,
       completed_at, error
FROM managed_data.upload_session
WHERE collection_id = sqlc.arg(collection_id)
ORDER BY created_at DESC, upload_id DESC;

-- name: ListUploadSessionsForCleanup :many
SELECT upload_id, collection_id, COALESCE(base_revision_id, ''),
       COALESCE(revision_id, ''), status, manifest::text, expected_file_count,
       expected_size_bytes, uploaded_file_count, uploaded_size_bytes, storage_backend,
       staging_prefix, created_by, created_at, updated_at, expires_at,
       completed_at, error
FROM managed_data.upload_session
WHERE status IN ('complete', 'aborted', 'expired', 'failed')
  AND cleanup_completed_at IS NULL
ORDER BY updated_at, upload_id
LIMIT sqlc.arg(p_limit);

-- name: MarkUploadCleanup :one
SELECT managed_data.mark_upload_cleanup(sqlc.arg(upload_id));

-- name: UpdateUploadProgress :execresult
UPDATE managed_data.upload_session
SET uploaded_file_count = sqlc.arg(uploaded_file_count),
    uploaded_size_bytes = sqlc.arg(uploaded_size_bytes), updated_at = clock_timestamp()
WHERE upload_id = sqlc.arg(upload_id) AND status = 'open'
  AND sqlc.arg(uploaded_file_count) <= expected_file_count
  AND sqlc.arg(uploaded_size_bytes) <= expected_size_bytes;

-- name: BeginUploadFinalization :execresult
UPDATE managed_data.upload_session
SET status = 'committing', updated_at = clock_timestamp()
WHERE upload_id = sqlc.arg(upload_id) AND status = 'open' AND expires_at > clock_timestamp();

-- name: FailUploadFinalization :execresult
UPDATE managed_data.upload_session
SET status = 'failed', error = sqlc.arg(error), updated_at = clock_timestamp()
WHERE upload_id = sqlc.arg(upload_id) AND status = 'committing';

-- name: AbortUploadSession :execresult
UPDATE managed_data.upload_session
SET status = 'aborted', updated_at = clock_timestamp()
WHERE upload_id = sqlc.arg(upload_id) AND status = 'open';

-- name: ExpireUploadSessions :execresult
UPDATE managed_data.upload_session
SET status = 'expired', updated_at = clock_timestamp()
WHERE status = 'open'
  AND expires_at <= LEAST(COALESCE(sqlc.narg(cutoff)::timestamptz, clock_timestamp()), clock_timestamp());

-- name: LockUploadSessionForCompletion :one
SELECT status, collection_id, manifest::text, expected_file_count, expected_size_bytes,
       COALESCE(revision_id, ''), completion_digest
FROM managed_data.upload_session
WHERE upload_id = sqlc.arg(upload_id)
FOR UPDATE;

-- name: LockCollection :one
SELECT collection_id FROM managed_data.collection
WHERE collection_id = sqlc.arg(collection_id)
FOR UPDATE;

-- name: NextRevisionSequence :one
SELECT (COALESCE(MAX(sequence), 0) + 1)::bigint AS sequence
FROM managed_data.revision
WHERE collection_id = sqlc.arg(collection_id);

-- name: InsertRevisionFromUpload :exec
INSERT INTO managed_data.revision
    (revision_id, collection_id, sequence, digest, status, manifest,
     file_count, size_bytes, created_by)
SELECT sqlc.arg(revision_id), sqlc.arg(collection_id), sqlc.arg(sequence), sqlc.arg(digest),
       'pending', sqlc.arg(manifest)::jsonb, sqlc.arg(file_count), sqlc.arg(size_bytes), created_by
FROM managed_data.upload_session
WHERE upload_id = sqlc.arg(upload_id);

-- name: InsertRevisionFile :exec
INSERT INTO managed_data.revision_file
    (revision_id, logical_path, size_bytes, sha256, storage_key, media_type, etag)
VALUES (sqlc.arg(revision_id), sqlc.arg(logical_path), sqlc.arg(size_bytes), sqlc.arg(sha256),
        sqlc.arg(storage_key), sqlc.arg(media_type), sqlc.arg(etag));

-- name: MarkRevisionReady :execresult
UPDATE managed_data.revision
SET status = 'ready', ready_at = clock_timestamp()
WHERE revision_id = sqlc.arg(revision_id) AND status = 'pending';

-- name: CompleteUploadSession :execresult
UPDATE managed_data.upload_session
SET status = 'complete', revision_id = sqlc.arg(revision_id),
    completion_digest = sqlc.arg(completion_digest),
    uploaded_file_count = expected_file_count, uploaded_size_bytes = expected_size_bytes,
    completed_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE upload_id = sqlc.arg(upload_id) AND status = 'committing';

-- name: GetRevisionByID :one
SELECT revision_id, collection_id, sequence, digest, status, manifest::text,
       file_count, size_bytes, created_by, created_at, ready_at, error
FROM managed_data.revision
WHERE revision_id = sqlc.arg(revision_id);

-- name: ListRevisionsByCollection :many
SELECT revision_id, collection_id, sequence, digest, status, manifest::text,
       file_count, size_bytes, created_by, created_at, ready_at, error
FROM managed_data.revision
WHERE collection_id = sqlc.arg(collection_id)
ORDER BY sequence DESC;

-- name: GetUploadIDByRevision :one
SELECT upload_id FROM managed_data.upload_session
WHERE revision_id = sqlc.arg(revision_id) AND status = 'complete';

-- name: ListRevisionFiles :many
SELECT revision_id, logical_path, size_bytes, sha256, storage_key, media_type, etag, created_at
FROM managed_data.revision_file
WHERE revision_id = sqlc.arg(revision_id)
ORDER BY logical_path;

-- name: GetEnvironmentPointer :one
SELECT collection_id, environment, revision_id, revision_digest,
       deployment_id, generation, updated_by, updated_at
FROM managed_data.environment_pointer
WHERE collection_id = sqlc.arg(collection_id) AND environment = sqlc.arg(environment);

-- name: UpsertEnvironmentPointer :execresult
INSERT INTO managed_data.environment_pointer
    (collection_id, environment, revision_id, revision_digest, deployment_id, generation, updated_by)
VALUES (sqlc.arg(collection_id), sqlc.arg(environment), sqlc.arg(revision_id),
        sqlc.arg(revision_digest), sqlc.arg(deployment_id), sqlc.arg(generation), sqlc.arg(updated_by))
ON CONFLICT (collection_id, environment) DO UPDATE
SET revision_id = EXCLUDED.revision_id, revision_digest = EXCLUDED.revision_digest,
    deployment_id = EXCLUDED.deployment_id, generation = EXCLUDED.generation,
    updated_by = EXCLUDED.updated_by, updated_at = clock_timestamp()
WHERE managed_data.environment_pointer.generation < EXCLUDED.generation;

-- name: PublishBindingSet :exec
SELECT managed_data.publish_binding_set(
    sqlc.arg(project_id), sqlc.arg(environment), sqlc.arg(generation_id),
    sqlc.arg(binding_digest), sqlc.arg(binding_count), sqlc.arg(bindings)::jsonb);

-- name: GetBindingSetMarker :one
SELECT binding_digest, binding_count
FROM managed_data.binding_set
WHERE project_id = sqlc.arg(project_id) AND environment = sqlc.arg(environment)
  AND generation_id = sqlc.arg(generation_id);

-- name: ListBindings :many
SELECT collection_id, revision_id, bound_at
FROM managed_data.binding
WHERE project_id = sqlc.arg(project_id) AND environment = sqlc.arg(environment)
  AND generation_id = sqlc.arg(generation_id)
ORDER BY collection_id;

-- name: AcquireLease :one
INSERT INTO managed_data.lease (lease_key, owner_id, fencing_epoch, expires_at)
SELECT sqlc.arg(lease_key), sqlc.arg(owner_id), 1,
       clock_timestamp() + (sqlc.arg(duration_micros)::bigint * interval '1 microsecond')
WHERE sqlc.arg(duration_micros)::bigint > 0
  AND sqlc.arg(duration_micros)::bigint <= 86400000000
ON CONFLICT (lease_key) DO UPDATE
SET owner_id = EXCLUDED.owner_id, fencing_epoch = managed_data.lease.fencing_epoch + 1,
    expires_at = EXCLUDED.expires_at, state = 'held', released_at = NULL
WHERE managed_data.lease.expires_at <= clock_timestamp()
   OR managed_data.lease.owner_id = sqlc.arg(owner_id)
RETURNING lease_key, owner_id, fencing_epoch, expires_at;

-- name: RenewLease :one
UPDATE managed_data.lease
SET expires_at = clock_timestamp() + (sqlc.arg(duration_micros)::bigint * interval '1 microsecond')
WHERE lease_key = sqlc.arg(lease_key) AND owner_id = sqlc.arg(owner_id)
  AND fencing_epoch = sqlc.arg(fencing_epoch) AND state = 'held'
  AND expires_at > clock_timestamp() AND sqlc.arg(duration_micros)::bigint > 0
  AND sqlc.arg(duration_micros)::bigint <= 86400000000
  AND clock_timestamp() + (sqlc.arg(duration_micros)::bigint * interval '1 microsecond') > expires_at
RETURNING lease_key, owner_id, fencing_epoch, expires_at;

-- name: ReleaseLease :execresult
UPDATE managed_data.lease
SET state = 'released', released_at = clock_timestamp(), expires_at = clock_timestamp()
WHERE lease_key = sqlc.arg(lease_key) AND owner_id = sqlc.arg(owner_id)
  AND fencing_epoch = sqlc.arg(fencing_epoch) AND state = 'held';

-- name: InsertRetentionRoot :exec
INSERT INTO managed_data.retention_root
    (root_id, project_id, environment, revision_id, state, evidence)
VALUES (sqlc.arg(root_id), sqlc.arg(project_id), sqlc.arg(environment), sqlc.arg(revision_id),
        sqlc.arg(state), sqlc.arg(evidence)::jsonb)
ON CONFLICT (root_id) DO NOTHING;

-- name: GetRetentionRoot :one
SELECT root_id, project_id, environment, revision_id, state, evidence, created_at, updated_at
FROM managed_data.retention_root
WHERE root_id = sqlc.arg(root_id);

-- name: TransitionRetentionRoot :execresult
UPDATE managed_data.retention_root
SET state = sqlc.arg(state), updated_at = clock_timestamp()
WHERE root_id = sqlc.arg(root_id)
  AND ((state = 'live' AND sqlc.arg(state) = 'retiring')
    OR (state = 'retiring' AND sqlc.arg(state) = 'expired'));

-- name: InsertReconciliationEvidence :one
INSERT INTO managed_data.reconciliation_evidence
    (project_id, environment, object_key, observed_state, action, evidence)
VALUES (sqlc.arg(project_id), sqlc.arg(environment), sqlc.arg(object_key),
        sqlc.arg(observed_state), sqlc.arg(action), sqlc.arg(evidence)::jsonb)
RETURNING evidence_id, project_id, environment, object_key, observed_state, action, evidence, observed_at;

-- name: PruneUploadSessions :one
SELECT managed_data.prune_upload_sessions(sqlc.narg(cutoff)::timestamptz, sqlc.arg(p_limit));

-- name: InsertMultipartUpload :exec
INSERT INTO managed_data.multipart_upload
    (multipart_id, upload_id, logical_path, sha256, size_bytes, idempotency_identity)
VALUES (sqlc.arg(multipart_id), sqlc.arg(upload_id), sqlc.arg(logical_path),
        sqlc.arg(sha256), sqlc.arg(size_bytes), sqlc.arg(idempotency_identity))
ON CONFLICT DO NOTHING;

-- name: GetMultipartByID :one
SELECT multipart_id, upload_id, logical_path, sha256, size_bytes, object_key,
       provider_upload_id, status, existing, idempotency_identity, completion_identity,
       completion_request_hash, abort_identity, created_at, updated_at, completed_at,
       aborted_at, error
FROM managed_data.multipart_upload
WHERE multipart_id = sqlc.arg(multipart_id);

-- name: GetMultipartByIdentity :one
SELECT multipart_id FROM managed_data.multipart_upload
WHERE upload_id = sqlc.arg(upload_id) AND idempotency_identity = sqlc.arg(idempotency_identity);

-- name: InitializeMultipart :execresult
UPDATE managed_data.multipart_upload
SET object_key = sqlc.arg(object_key), provider_upload_id = sqlc.arg(provider_upload_id),
    status = CASE WHEN sqlc.arg(existing) THEN 'completed' ELSE 'open' END,
    existing = sqlc.arg(existing),
    completed_at = CASE WHEN sqlc.arg(existing) THEN clock_timestamp() ELSE completed_at END,
    updated_at = clock_timestamp()
WHERE multipart_id = sqlc.arg(multipart_id) AND status = 'creating';

-- name: InsertMultipartPart :exec
INSERT INTO managed_data.multipart_part (multipart_id, part_number, size_bytes, sha256)
VALUES (sqlc.arg(multipart_id), sqlc.arg(part_number), sqlc.arg(size_bytes), sqlc.arg(sha256))
ON CONFLICT (multipart_id, part_number) DO NOTHING;

-- name: GetMultipartPart :one
SELECT multipart_id, part_number, size_bytes, sha256
FROM managed_data.multipart_part
WHERE multipart_id = sqlc.arg(multipart_id) AND part_number = sqlc.arg(part_number);

-- name: ListMultipartParts :many
SELECT multipart_id, part_number, size_bytes, sha256
FROM managed_data.multipart_part
WHERE multipart_id = sqlc.arg(multipart_id)
ORDER BY part_number;

-- name: BeginMultipartCompletion :execresult
UPDATE managed_data.multipart_upload
SET status = 'completing', completion_identity = sqlc.arg(completion_identity),
    completion_request_hash = sqlc.arg(completion_request_hash), updated_at = clock_timestamp()
WHERE multipart_id = sqlc.arg(multipart_id) AND status = 'open';

-- name: BeginMultipartAbort :execresult
UPDATE managed_data.multipart_upload
SET status = 'aborting', abort_identity = sqlc.arg(abort_identity), updated_at = clock_timestamp()
WHERE multipart_id = sqlc.arg(multipart_id) AND status IN ('creating', 'open', 'failed');

-- name: FinishMultipart :execresult
UPDATE managed_data.multipart_upload
SET status = sqlc.arg(to_status),
    completed_at = CASE WHEN sqlc.arg(to_status) = 'completed' THEN clock_timestamp() ELSE completed_at END,
    aborted_at = CASE WHEN sqlc.arg(to_status) = 'aborted' THEN clock_timestamp() ELSE aborted_at END,
    updated_at = clock_timestamp()
WHERE multipart_id = sqlc.arg(multipart_id) AND status = sqlc.arg(from_status);

-- name: FailMultipart :execresult
UPDATE managed_data.multipart_upload
SET status = 'failed', error = sqlc.arg(error), updated_at = clock_timestamp()
WHERE multipart_id = sqlc.arg(multipart_id) AND status IN ('creating', 'open', 'completing');

-- name: ListRecoverableMultipart :many
SELECT m.multipart_id, m.upload_id, m.logical_path, m.sha256, m.size_bytes, m.object_key,
       m.provider_upload_id, m.status, m.existing, m.idempotency_identity,
       m.completion_identity, m.completion_request_hash, m.abort_identity,
       m.created_at, m.updated_at, m.completed_at, m.aborted_at, m.error
FROM managed_data.multipart_upload m
JOIN managed_data.upload_session s ON s.upload_id = m.upload_id
WHERE m.updated_at <= LEAST(COALESCE(sqlc.narg(cutoff)::timestamptz, clock_timestamp()), clock_timestamp())
  AND (m.status IN ('aborting', 'failed', 'creating', 'completing')
    OR (m.status = 'open' AND (s.status IN ('complete', 'aborted', 'expired', 'failed')
      OR (s.status = 'open' AND s.expires_at <= clock_timestamp()))))
ORDER BY m.updated_at, m.multipart_id
LIMIT sqlc.arg(p_limit);

-- name: ListProviderIDsByDigest :many
SELECT provider_upload_id FROM managed_data.multipart_upload
WHERE sha256 = sqlc.arg(sha256) AND provider_upload_id <> ''
ORDER BY provider_upload_id;

-- name: ListCreatingIDsByDigest :many
SELECT multipart_id FROM managed_data.multipart_upload
WHERE sha256 = sqlc.arg(sha256) AND status = 'creating'
ORDER BY multipart_id;

-- name: ClaimDigestLease :one
INSERT INTO managed_data.multipart_digest_lease
    (sha256, owner_id, fencing_epoch, state, lease_until)
SELECT sqlc.arg(sha256), sqlc.arg(owner_id), 1, 'held', sqlc.arg(lease_until)
WHERE sqlc.arg(lease_until)::timestamptz > clock_timestamp()
  AND sqlc.arg(lease_until)::timestamptz <= clock_timestamp() + interval '24 hours'
ON CONFLICT (sha256) DO UPDATE
SET owner_id = EXCLUDED.owner_id,
    fencing_epoch = managed_data.multipart_digest_lease.fencing_epoch + 1,
    state = 'held', lease_until = EXCLUDED.lease_until
WHERE managed_data.multipart_digest_lease.lease_until <= clock_timestamp()
   OR managed_data.multipart_digest_lease.owner_id = sqlc.arg(owner_id)
RETURNING fencing_epoch;

-- name: RenewDigestLease :execresult
UPDATE managed_data.multipart_digest_lease
SET lease_until = sqlc.arg(lease_until)
WHERE sha256 = sqlc.arg(sha256) AND owner_id = sqlc.arg(owner_id)
  AND fencing_epoch = sqlc.arg(fencing_epoch) AND state = 'held'
  AND lease_until > clock_timestamp()
  AND sqlc.arg(lease_until)::timestamptz > lease_until
  AND sqlc.arg(lease_until)::timestamptz <= clock_timestamp() + interval '24 hours';

-- name: ReleaseDigestLease :execresult
UPDATE managed_data.multipart_digest_lease
SET state = 'released', lease_until = clock_timestamp()
WHERE sha256 = sqlc.arg(sha256) AND owner_id = sqlc.arg(owner_id)
  AND fencing_epoch = sqlc.arg(fencing_epoch) AND state = 'held';
