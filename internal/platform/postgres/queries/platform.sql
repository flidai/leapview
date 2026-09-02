-- name: AcquireMigrationLock :exec
SELECT pg_advisory_xact_lock(sqlc.arg(lock_key)::bigint);

-- name: GetSchemaRevision :one
SELECT revision, migration_id, checksum
FROM platform.schema_revision
WHERE revision = sqlc.arg(revision)::bigint;

-- name: InsertSchemaRevision :exec
INSERT INTO platform.schema_revision (revision, migration_id, checksum)
VALUES (sqlc.arg(revision)::bigint, sqlc.arg(migration_id)::text, sqlc.arg(checksum)::text);
