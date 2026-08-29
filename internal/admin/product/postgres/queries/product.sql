-- Static PostgreSQL query leaves for admin product identity.

-- name: GetIdentity :one
SELECT display_name, logo_sha256, logo_media_type, logo_size_bytes,
       logo_width, logo_height, revision, updated_at
FROM admin.product_identity
WHERE singleton_id = 1;

-- name: Ping :one
SELECT 1 AS ping;

-- name: UpdateDisplayName :execrows
UPDATE admin.product_identity
SET display_name = sqlc.arg(display_name), revision = revision + 1,
    updated_at = clock_timestamp()
WHERE singleton_id = 1 AND revision = sqlc.arg(expected_revision);

-- name: UpdateLogo :execrows
UPDATE admin.product_identity
SET logo_sha256 = sqlc.arg(logo_sha256), logo_media_type = sqlc.arg(logo_media_type),
    logo_size_bytes = sqlc.arg(logo_size_bytes), logo_width = sqlc.arg(logo_width),
    logo_height = sqlc.arg(logo_height), revision = revision + 1,
    updated_at = clock_timestamp()
WHERE singleton_id = 1 AND revision = sqlc.arg(expected_revision);

-- name: DeleteLogo :execrows
UPDATE admin.product_identity
SET logo_sha256 = NULL, logo_media_type = NULL, logo_size_bytes = NULL,
    logo_width = NULL, logo_height = NULL, revision = revision + 1,
    updated_at = clock_timestamp()
WHERE singleton_id = 1 AND revision = sqlc.arg(expected_revision)
  AND logo_sha256 IS NOT NULL;

-- name: ResetIdentity :execrows
UPDATE admin.product_identity
SET display_name = sqlc.arg(display_name), logo_sha256 = NULL,
    logo_media_type = NULL, logo_size_bytes = NULL, logo_width = NULL,
    logo_height = NULL, revision = revision + 1, updated_at = clock_timestamp()
WHERE singleton_id = 1 AND revision = sqlc.arg(expected_revision);
