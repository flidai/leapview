-- Trusted provider-observation capture reads the managed-data authority while
-- the caller-owned transaction holds the same table locks as reachability GC.

-- name: LockProviderObservationTables :exec
LOCK TABLE managed_data.upload_session,
           managed_data.revision,
           managed_data.collection,
           managed_data.revision_file,
           managed_data.reachability_epoch
IN SHARE MODE;

-- name: GetProviderObservationEpoch :one
SELECT epoch, pg_is_in_recovery() AS in_recovery
FROM managed_data.reachability_epoch
WHERE singleton = true;

-- name: GetProviderObservationFsync :one
SELECT current_setting('fsync')::text AS fsync;

-- name: ListProviderObservationRevisions :many
SELECT c.project_id, c.collection_id,
       r.revision_id, r.digest, r.manifest::text AS manifest, r.file_count, r.size_bytes
FROM managed_data.revision AS r
JOIN managed_data.collection AS c ON c.collection_id = r.collection_id
WHERE r.status = 'ready'
  AND r.revision_id > sqlc.arg(after_revision_id)
ORDER BY r.revision_id
LIMIT sqlc.arg(page_size);

-- name: ListProviderObservationRevisionFiles :many
SELECT revision_id, logical_path, size_bytes, sha256, storage_key
FROM managed_data.revision_file
WHERE revision_id = sqlc.arg(revision_id)
ORDER BY logical_path;

-- name: CreateProviderObservationRestorePoint :one
WITH restore AS (
    SELECT pg_create_restore_point(sqlc.arg(restore_point_name)::text) AS restore_lsn
)
SELECT current_database()::text AS database_identity,
       (SELECT system_identifier::text FROM pg_control_system()) AS system_identity,
       restore_lsn::text AS lsn,
       pg_walfile_name(restore_lsn)::text AS wal_file
FROM restore;

-- name: GetProviderObservationWALFlushLSN :one
SELECT pg_current_wal_flush_lsn()::text AS flush_lsn,
       (SELECT system_identifier::text FROM pg_control_system()) AS system_identity,
       pg_walfile_name(pg_current_wal_flush_lsn())::text AS wal_file,
       pg_is_in_recovery() AS in_recovery,
       current_setting('fsync')::text AS fsync;
