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

-- Profile application checkpoints retain exact local graph intent separately
-- from the provider/binding evidence observed while applying it.

-- name: InsertProfileApplication :execrows
INSERT INTO connection_binding.profile_application (
    id, checkout_id, runtime_id, target_id, project_id, environment,
    profile_name, graph_digest, source_digest, profile_digest, last_completed_application_id, last_completed_at,
    retired_connections, status, revision,
    created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(checkout_id), sqlc.arg(runtime_id), sqlc.arg(target_id),
    sqlc.arg(project_id), sqlc.arg(environment), sqlc.arg(profile_name),
    sqlc.arg(graph_digest), sqlc.arg(source_digest), sqlc.arg(profile_digest), sqlc.arg(last_completed_application_id), sqlc.narg(last_completed_at)::timestamptz,
    sqlc.arg(retired_connections)::jsonb, sqlc.arg(status),
    sqlc.arg(revision), sqlc.arg(created_at)::timestamptz, sqlc.arg(updated_at)::timestamptz
)
ON CONFLICT DO NOTHING;

-- name: GetProfileApplication :one
SELECT id, checkout_id, runtime_id, target_id, project_id, environment,
       profile_name, graph_digest, source_digest, profile_digest, last_completed_application_id, last_completed_at,
       retired_connections, status, revision,
       created_at, updated_at
FROM connection_binding.profile_application
WHERE checkout_id = sqlc.arg(checkout_id)
  AND runtime_id = sqlc.arg(runtime_id)
  AND target_id = sqlc.arg(target_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment);

-- name: GetProfileApplicationForUpdate :one
SELECT id, checkout_id, runtime_id, target_id, project_id, environment,
       profile_name, graph_digest, source_digest, profile_digest, last_completed_application_id, last_completed_at,
       retired_connections, status, revision,
       created_at, updated_at
FROM connection_binding.profile_application
WHERE checkout_id = sqlc.arg(checkout_id)
  AND runtime_id = sqlc.arg(runtime_id)
  AND target_id = sqlc.arg(target_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
FOR UPDATE;

-- name: GetProfileApplicationByIDForUpdate :one
SELECT id, checkout_id, runtime_id, target_id, project_id, environment,
       profile_name, graph_digest, source_digest, profile_digest, last_completed_application_id, last_completed_at,
       retired_connections, status, revision,
       created_at, updated_at
FROM connection_binding.profile_application
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: UpdateProfileApplication :execrows
UPDATE connection_binding.profile_application
SET status = sqlc.arg(status),
    updated_at = sqlc.arg(updated_at)::timestamptz,
    revision = sqlc.arg(revision)
WHERE id = sqlc.arg(id)
  AND revision = sqlc.arg(expected_revision)
  AND checkout_id = sqlc.arg(checkout_id)
  AND runtime_id = sqlc.arg(runtime_id)
  AND target_id = sqlc.arg(target_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment);

-- name: CompleteProfileApplication :one
SELECT connection_binding.complete_profile_application(
    sqlc.arg(application_id),
    sqlc.arg(expected_revision),
    sqlc.arg(expected_checkout_id),
    sqlc.arg(expected_runtime_id),
    sqlc.arg(expected_target_id),
    sqlc.arg(expected_project_id),
    sqlc.arg(expected_environment),
    sqlc.arg(completion_updated_at)::timestamptz
)::bigint AS affected;

-- name: InsertProfileApplicationRequiredConnection :execrows
INSERT INTO connection_binding.profile_application_required_connection (
    application_id, connection_id, connector_kind
) VALUES (
    sqlc.arg(application_id), sqlc.arg(connection_id), sqlc.arg(connector_kind)
)
ON CONFLICT DO NOTHING;

-- name: InsertProfileApplicationExpectedConnection :execrows
INSERT INTO connection_binding.profile_application_expected_connection (
    application_id, binding_id, connection_id, connector_kind, authentication_mode, endpoint_json,
    credential_project_id, credential_environment, credential_secret_path, credential_secret_key,
    binding_revision, provider_version
) VALUES (
    sqlc.arg(application_id), sqlc.arg(binding_id), sqlc.arg(connection_id), sqlc.arg(connector_kind), sqlc.arg(authentication_mode), sqlc.arg(endpoint_json)::jsonb,
    sqlc.arg(credential_project_id), sqlc.arg(credential_environment), sqlc.arg(credential_secret_path), sqlc.arg(credential_secret_key),
    sqlc.arg(binding_revision), sqlc.arg(provider_version)
)
ON CONFLICT DO NOTHING;

-- name: ListProfileApplicationExpectedConnections :many
SELECT application_id, binding_id, connection_id, connector_kind, authentication_mode, endpoint_json,
       credential_project_id, credential_environment, credential_secret_path, credential_secret_key,
       binding_revision, provider_version
FROM connection_binding.profile_application_expected_connection
WHERE application_id = sqlc.arg(application_id)
ORDER BY connection_id ASC;

-- name: ListProfileApplicationRequiredConnections :many
SELECT application_id, connection_id, connector_kind
FROM connection_binding.profile_application_required_connection
WHERE application_id = sqlc.arg(application_id)
ORDER BY connection_id ASC;

-- name: InsertProfileApplicationAppliedConnection :execrows
INSERT INTO connection_binding.profile_application_applied_connection (
    application_id, binding_id, connection_id, connector_kind, authentication_mode, endpoint_json,
    credential_project_id, credential_environment, credential_secret_path, credential_secret_key,
    binding_revision, provider_version
) VALUES (
    sqlc.arg(application_id), sqlc.arg(binding_id), sqlc.arg(connection_id), sqlc.arg(connector_kind), sqlc.arg(authentication_mode), sqlc.arg(endpoint_json)::jsonb,
    sqlc.arg(credential_project_id), sqlc.arg(credential_environment), sqlc.arg(credential_secret_path), sqlc.arg(credential_secret_key),
    sqlc.arg(binding_revision), sqlc.arg(provider_version)
)
ON CONFLICT (application_id, connection_id) DO UPDATE
SET connector_kind = EXCLUDED.connector_kind,
    binding_id = EXCLUDED.binding_id,
    authentication_mode = EXCLUDED.authentication_mode,
    endpoint_json = EXCLUDED.endpoint_json,
    credential_project_id = EXCLUDED.credential_project_id,
    credential_environment = EXCLUDED.credential_environment,
    credential_secret_path = EXCLUDED.credential_secret_path,
    credential_secret_key = EXCLUDED.credential_secret_key,
    binding_revision = EXCLUDED.binding_revision,
    provider_version = EXCLUDED.provider_version;

-- name: ListProfileApplicationAppliedConnections :many
SELECT application_id, binding_id, connection_id, connector_kind, authentication_mode, endpoint_json,
       credential_project_id, credential_environment, credential_secret_path, credential_secret_key,
       binding_revision, provider_version
FROM connection_binding.profile_application_applied_connection
WHERE application_id = sqlc.arg(application_id)
ORDER BY connection_id ASC;

-- name: DeleteProfileApplicationAppliedConnections :execrows
DELETE FROM connection_binding.profile_application_applied_connection
WHERE application_id = sqlc.arg(application_id);

-- name: DeleteProfileApplicationForReplacement :one
SELECT connection_binding.delete_profile_application_for_replacement(
    sqlc.arg(application_id),
    sqlc.arg(expected_revision),
    sqlc.arg(expected_checkout_id),
    sqlc.arg(expected_runtime_id),
    sqlc.arg(expected_target_id),
    sqlc.arg(expected_project_id),
    sqlc.arg(expected_environment),
    sqlc.arg(replacement_application_id)
)::bigint AS affected;
