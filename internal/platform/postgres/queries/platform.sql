-- name: Probe :one
SELECT current_setting('server_version_num') AS server_version_num,
       current_user::text AS runtime_role,
       current_setting('default_transaction_read_only') AS default_transaction_read_only,
       pg_is_in_recovery() AS in_recovery;

-- name: CurrentDatabase :one
SELECT current_database() AS database_name;

-- name: AcquireMigrationLock :exec
SELECT pg_advisory_xact_lock(sqlc.arg(lock_key)::bigint);

-- name: GetSchemaRevision :one
SELECT revision, migration_id, checksum
FROM platform.schema_revision
WHERE revision = sqlc.arg(revision)::bigint;

-- name: InsertSchemaRevision :exec
INSERT INTO platform.schema_revision (revision, migration_id, checksum)
VALUES (sqlc.arg(revision)::bigint, sqlc.arg(migration_id)::text, sqlc.arg(checksum)::text);

-- name: RoleExists :one
SELECT CAST(to_regrole(sqlc.arg(role_name)::text) IS NOT NULL AS boolean) AS exists;
