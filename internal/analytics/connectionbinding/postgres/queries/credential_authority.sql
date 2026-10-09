-- name: LockCredentialTargetConnectionBinding :one
SELECT id, target_id, connection_id, connector_kind, authentication_mode,
       project_id, environment, endpoint_json,
       credential_project_id, credential_environment, credential_secret_path, credential_secret_key,
       enabled, validated_version, health, health_reason, last_validated_at,
       created_at, updated_at, revision
FROM connection_binding.target_connection_binding
WHERE target_id = sqlc.arg(target_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND connection_id = sqlc.arg(connection_id)
FOR SHARE;
