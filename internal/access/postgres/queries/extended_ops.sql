-- Stable access extended PostgreSQL leaves. Multi-write orchestration stays
-- in access_extended.go; every statement below is a named SQLC leaf.

-- name: InsertPlatformSetting :execresult
INSERT INTO access.platform_setting(key, value)
VALUES (sqlc.arg(key), sqlc.arg(value))
ON CONFLICT (key) DO NOTHING;

-- name: ListServiceSecrets :many
SELECT id, service_principal_id, name, expires_at, created_at, revoked_at
FROM access.service_principal_secret
WHERE service_principal_id = sqlc.arg(principal_id)::uuid
ORDER BY created_at DESC
LIMIT sqlc.arg(page_size)::int;

-- name: GetServiceSecretForPrincipal :one
SELECT id, service_principal_id, name, expires_at, created_at, revoked_at
FROM access.service_principal_secret
WHERE id = sqlc.arg(id)::uuid AND service_principal_id = sqlc.arg(principal_id)::uuid;

-- name: GetPrincipalPreferences :one
SELECT theme, updated_at
FROM access.principal_preferences
WHERE principal_id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL;

-- name: RevokePrincipalPreferences :exec
UPDATE access.principal_preferences SET revoked_at = clock_timestamp()
WHERE principal_id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL;

-- name: InsertPrincipalPreferences :execresult
INSERT INTO access.principal_preferences(principal_id, theme)
SELECT sqlc.arg(principal_id)::uuid, sqlc.arg(theme)
WHERE EXISTS (
    SELECT 1 FROM access.principal
    WHERE id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL
);

-- name: GetAvatar :one
SELECT principal_id, sha256, media_type, size_bytes, width, height, updated_at
FROM access.principal_avatar
WHERE principal_id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL
ORDER BY updated_at DESC
LIMIT 1;

-- name: AvatarPrincipalExists :one
SELECT EXISTS (
    SELECT 1 FROM access.principal
    WHERE id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL
);

-- name: InsertAvatarObject :execresult
INSERT INTO access.avatar_object(sha256, object_key, media_type, size_bytes)
VALUES (sqlc.arg(sha256), 'avatars/' || sqlc.arg(sha256), 'image/png', sqlc.arg(size_bytes))
ON CONFLICT (sha256) DO NOTHING;

-- name: GetAvatarObject :one
SELECT object_key, media_type, size_bytes
FROM access.avatar_object
WHERE sha256 = sqlc.arg(sha256);

-- name: RevokePrincipalAvatar :execresult
UPDATE access.principal_avatar SET revoked_at = clock_timestamp()
WHERE principal_id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL;

-- name: InsertPrincipalAvatar :exec
INSERT INTO access.principal_avatar(principal_id, sha256, media_type, size_bytes, width, height)
SELECT sqlc.arg(principal_id)::uuid, sqlc.arg(sha256), 'image/png', sqlc.arg(size_bytes), 256, 256
WHERE EXISTS (
    SELECT 1 FROM access.principal
    WHERE id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL
);

-- name: InsertAuthorizationSnapshot :execresult
INSERT INTO access.authorization_snapshot(project_id, environment, generation_id, digest)
VALUES (sqlc.arg(project_id), sqlc.arg(environment), sqlc.arg(generation_id), sqlc.arg(digest))
ON CONFLICT (project_id, environment, generation_id) DO NOTHING;

-- name: GetAuthorizationSnapshotDigest :one
SELECT digest
FROM access.authorization_snapshot
WHERE project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND generation_id = sqlc.arg(generation_id);

-- name: InsertAuthorizationRoleBinding :exec
INSERT INTO access.authorization_role_binding
    (id, project_id, environment, generation_id, subject_kind, subject_id, role, capabilities, name)
VALUES (sqlc.arg(id), sqlc.arg(project_id), sqlc.arg(environment), sqlc.arg(generation_id),
        sqlc.arg(subject_kind), sqlc.arg(subject_id), sqlc.arg(role), sqlc.arg(capabilities)::jsonb, sqlc.arg(name));

-- name: InsertAuthorizationGrant :exec
INSERT INTO access.authorization_grant
    (id, project_id, environment, generation_id, subject_kind, subject_id,
     resource_id, resource_kind, capability, name)
VALUES (sqlc.arg(id), sqlc.arg(project_id), sqlc.arg(environment), sqlc.arg(generation_id),
        sqlc.arg(subject_kind), sqlc.arg(subject_id), sqlc.arg(resource_id),
        sqlc.arg(resource_kind), sqlc.arg(capability), sqlc.arg(name));

-- name: InsertAuthorizationDataPolicy :exec
INSERT INTO access.authorization_data_policy
    (id, project_id, environment, generation_id, resource_id, resource_kind,
     subject_kind, subject_id, policy_type, expression)
VALUES (sqlc.arg(id), sqlc.arg(project_id), sqlc.arg(environment), sqlc.arg(generation_id),
        sqlc.arg(resource_id), sqlc.arg(resource_kind), sqlc.narg(subject_kind),
        sqlc.narg(subject_id), sqlc.arg(policy_type), sqlc.arg(expression)::jsonb);

-- name: RecordCanonicalAudit :exec
INSERT INTO audit.audit_event
    (audit_id, principal_id, source, operation, action, resource_kind, resource_id,
     project_id, environment, generation_id, capability, outcome, request_id,
     correlation_id, aggregate_key, aggregate_sequence, intent_digest, metadata)
VALUES (sqlc.arg(audit_id)::uuid, sqlc.arg(principal_id)::uuid, 'access', 'authorization',
        sqlc.arg(action), sqlc.arg(resource_kind), sqlc.arg(resource_id), sqlc.arg(project_id),
        sqlc.arg(environment), sqlc.arg(generation_id), sqlc.arg(capability), sqlc.arg(outcome),
        sqlc.narg(request_id), sqlc.narg(correlation_id), sqlc.arg(aggregate_key), 0,
        sqlc.arg(intent_digest), sqlc.arg(metadata)::jsonb);

-- name: CreateDesktopAuthorizationCode :execresult
WITH db_now AS (SELECT clock_timestamp() AS ts)
INSERT INTO access.desktop_authorization_code
    (code_hash, principal_id, client_id, instance_id, profile_id, redirect_uri,
     code_challenge, return_path, expires_at, created_at)
SELECT sqlc.arg(code_hash), sqlc.arg(principal_id)::uuid, sqlc.arg(client_id),
       sqlc.arg(instance_id), sqlc.arg(profile_id), sqlc.arg(redirect_uri),
       sqlc.arg(code_challenge), sqlc.arg(return_path), db_now.ts + sqlc.arg(ttl)::interval,
       db_now.ts
FROM db_now
WHERE EXISTS (
    SELECT 1 FROM access.principal
    WHERE id = sqlc.arg(principal_id)::uuid AND status = 'active'
      AND revoked_at IS NULL AND disabled_at IS NULL AND blocked_at IS NULL
);

-- name: FindAuthorizationCode :one
SELECT principal_id, client_id, instance_id, profile_id, redirect_uri,
       code_challenge, return_path, expires_at, created_at, consumed_at
FROM access.desktop_authorization_code
WHERE code_hash = sqlc.arg(code_hash)
  AND expires_at > clock_timestamp()
FOR UPDATE;

-- name: ConsumeAuthorizationCode :execresult
UPDATE access.desktop_authorization_code
SET consumed_at = clock_timestamp()
WHERE code_hash = sqlc.arg(code_hash)
  AND consumed_at IS NULL AND expires_at > clock_timestamp();
