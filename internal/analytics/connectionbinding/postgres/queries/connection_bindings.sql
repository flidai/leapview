-- Static PostgreSQL query leaves for the connection-binding authority.

-- name: CreateTargetConnectionBinding :execrows
INSERT INTO connection_binding.target_connection_binding (
    id, target_id, connection_id, connector_kind, authentication_mode,
    project_id, environment, endpoint_json,
    credential_project_id, credential_environment, credential_secret_path, credential_secret_key,
    enabled, validated_version, health, health_reason, last_validated_at,
    created_at, updated_at, revision
) VALUES (
    sqlc.arg(id), sqlc.arg(target_id), sqlc.arg(connection_id), sqlc.arg(connector_kind), sqlc.arg(authentication_mode),
    sqlc.arg(project_id), sqlc.arg(environment), sqlc.arg(endpoint_json)::jsonb,
    sqlc.arg(credential_project_id), sqlc.arg(credential_environment), sqlc.arg(credential_secret_path), sqlc.arg(credential_secret_key),
    sqlc.arg(enabled), sqlc.arg(validated_version), sqlc.arg(health), sqlc.arg(health_reason), sqlc.narg(last_validated_at)::timestamptz,
    sqlc.arg(created_at)::timestamptz, sqlc.arg(updated_at)::timestamptz, sqlc.arg(revision)
) ON CONFLICT DO NOTHING;

-- name: GetTargetConnectionBinding :one
SELECT id, target_id, connection_id, connector_kind, authentication_mode,
       project_id, environment, endpoint_json,
       credential_project_id, credential_environment, credential_secret_path, credential_secret_key,
       enabled, validated_version, health, health_reason, last_validated_at,
       created_at, updated_at, revision
FROM connection_binding.target_connection_binding
WHERE target_id = sqlc.arg(target_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND connection_id = sqlc.arg(connection_id);

-- name: ListTargetConnectionBindings :many
SELECT id, target_id, connection_id, connector_kind, authentication_mode,
       project_id, environment, endpoint_json,
       credential_project_id, credential_environment, credential_secret_path, credential_secret_key,
       enabled, validated_version, health, health_reason, last_validated_at,
       created_at, updated_at, revision
FROM connection_binding.target_connection_binding
WHERE target_id = sqlc.arg(target_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
ORDER BY connection_id ASC;

-- name: UpdateTargetConnectionBinding :execrows
UPDATE connection_binding.target_connection_binding
SET connector_kind = sqlc.arg(connector_kind),
    authentication_mode = sqlc.arg(authentication_mode),
    endpoint_json = sqlc.arg(endpoint_json)::jsonb,
    credential_project_id = sqlc.arg(credential_project_id),
    credential_environment = sqlc.arg(credential_environment),
    credential_secret_path = sqlc.arg(credential_secret_path),
    credential_secret_key = sqlc.arg(credential_secret_key),
    enabled = sqlc.arg(enabled),
    validated_version = sqlc.arg(validated_version),
    health = sqlc.arg(health),
    health_reason = sqlc.arg(health_reason),
    last_validated_at = sqlc.narg(last_validated_at)::timestamptz,
    updated_at = sqlc.arg(updated_at)::timestamptz,
    revision = sqlc.arg(revision)
WHERE id = sqlc.arg(id)
  AND revision = sqlc.arg(expected_revision)
  AND target_id = sqlc.arg(target_id)
  AND connection_id = sqlc.arg(connection_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment);
