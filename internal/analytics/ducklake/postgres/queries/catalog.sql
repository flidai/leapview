-- name: ReadDatabaseIdentity :one
SELECT current_database()::text AS database_name,
       current_user::text AS user_name,
       session_user::text AS session_user_name;

-- name: InsertCatalogIdentity :exec
INSERT INTO ducklake.catalog_identity
    (physical_pool_id, catalog_database, catalog_id, catalog_uuid,
     metadata_schema, compatibility_digest, catalog_schema_version)
VALUES
    (sqlc.arg(physical_pool_id), sqlc.arg(catalog_database), sqlc.arg(catalog_id),
     sqlc.arg(catalog_uuid), sqlc.arg(metadata_schema),
     sqlc.arg(compatibility_digest), sqlc.arg(catalog_schema_version))
ON CONFLICT (physical_pool_id) DO NOTHING;

-- name: GetCatalogIdentity :one
SELECT physical_pool_id, catalog_database, catalog_id, catalog_uuid,
       metadata_schema, compatibility_digest, catalog_schema_version, created_at
FROM ducklake.catalog_identity
WHERE physical_pool_id = sqlc.arg(physical_pool_id);

-- name: InsertInitialCatalogRuntimeCompatibility :exec
INSERT INTO ducklake.catalog_runtime_compatibility
    (physical_pool_id, catalog_id, duckdb_runtime, ducklake_extension,
     catalog_format, compatibility_digest, catalog_schema_version)
VALUES
    (sqlc.arg(physical_pool_id), sqlc.arg(catalog_id), sqlc.arg(duckdb_runtime),
     sqlc.arg(ducklake_extension), sqlc.arg(catalog_format),
     sqlc.arg(compatibility_digest), sqlc.arg(catalog_schema_version))
ON CONFLICT (physical_pool_id) DO NOTHING;

-- name: GetCatalogRuntimeCompatibility :one
SELECT physical_pool_id, catalog_id, duckdb_runtime, ducklake_extension,
       catalog_format, compatibility_digest, catalog_schema_version, updated_at
FROM ducklake.catalog_runtime_compatibility
WHERE physical_pool_id = sqlc.arg(physical_pool_id);
