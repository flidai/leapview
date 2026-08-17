-- name: CreateTargetConnectionBinding :exec
INSERT INTO target_connection_bindings (
  id, target_id, connection_id, connector_kind, authentication_mode,
  project_id, environment, endpoint_json,
  credential_project_id, credential_environment, credential_secret_path, credential_secret_key,
  enabled, validated_version, health, health_reason, last_validated_at,
  created_at, updated_at, revision
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetTargetConnectionBinding :one
SELECT *
FROM target_connection_bindings
WHERE target_id = ?
  AND project_id = ?
  AND environment = ?
  AND connection_id = ?;

-- name: ListTargetConnectionBindings :many
SELECT *
FROM target_connection_bindings
WHERE target_id = ?
  AND project_id = ?
  AND environment = ?
ORDER BY connection_id ASC;

-- name: UpdateTargetConnectionBinding :execrows
UPDATE target_connection_bindings
SET endpoint_json = ?,
    credential_project_id = ?,
    credential_environment = ?,
    credential_secret_path = ?,
    credential_secret_key = ?,
    enabled = ?,
    validated_version = ?,
    health = ?,
    health_reason = ?,
    last_validated_at = ?,
    updated_at = ?,
    revision = ?
WHERE id = ?
  AND revision = ?
  AND target_id = ?
  AND connection_id = ?
  AND connector_kind = ?
  AND authentication_mode = ?
  AND project_id = ?
  AND environment = ?;
