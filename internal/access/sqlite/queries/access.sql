-- name: UpsertPrincipal :exec
INSERT INTO principals (id, kind, email, display_name, updated_at)
VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(id) DO UPDATE SET
  kind = excluded.kind,
  email = excluded.email,
  display_name = excluded.display_name,
  updated_at = CURRENT_TIMESTAMP;

-- name: InsertPrincipalCreateOnly :execresult
INSERT INTO principals (id, kind, email, display_name, updated_at)
VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(id) DO NOTHING;

-- name: GetPrincipal :one
SELECT * FROM principals WHERE id = ?;

-- name: DeletePrincipalByID :execresult
DELETE FROM principals WHERE id = sqlc.arg(id);

-- name: GetPrincipalByEmail :one
SELECT * FROM principals WHERE lower(email) = lower(?) AND email <> '' LIMIT 1;

-- name: DisablePrincipal :exec
UPDATE principals
SET disabled_at = COALESCE(disabled_at, CURRENT_TIMESTAMP),
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: ListSCIMPrincipals :many
SELECT p.*
FROM principals p
JOIN external_identities ei ON ei.principal_id = p.id
WHERE p.kind = 'user'
  AND ei.provider = 'scim'
  AND (sqlc.arg(subject) = '' OR ei.subject = sqlc.arg(subject))
  AND (
    sqlc.arg(user_name) = ''
    OR lower(p.email) = lower(sqlc.arg(user_name))
    OR lower(ei.email) = lower(sqlc.arg(user_name))
  )
ORDER BY p.email, p.display_name, p.id;

-- name: GetExternalIdentityByPrincipalProvider :one
SELECT * FROM external_identities
WHERE principal_id = ? AND provider = ?
LIMIT 1;

-- name: ListServicePrincipals :many
SELECT * FROM principals
WHERE kind = 'service_principal'
ORDER BY display_name, id;

-- name: DeleteServicePrincipal :exec
DELETE FROM principals
WHERE id = ? AND kind = 'service_principal';

-- name: UpsertExternalIdentity :exec
INSERT INTO external_identities (id, principal_id, provider, tenant_id, subject, email, updated_at)
VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(provider, tenant_id, subject) DO UPDATE SET
  principal_id = excluded.principal_id,
  email = excluded.email,
  updated_at = CURRENT_TIMESTAMP;

-- name: GetExternalIdentity :one
SELECT * FROM external_identities
WHERE provider = ? AND tenant_id = ? AND subject = ?;

-- name: UpsertRole :exec
INSERT INTO roles (id, name, privileges_json)
VALUES (?, ?, ?)
ON CONFLICT(name) DO UPDATE SET privileges_json = excluded.privileges_json;

-- name: DeleteRoleGrantTemplates :exec
DELETE FROM role_grant_templates
WHERE role_name = sqlc.arg(role_name);

-- name: InsertRoleGrantTemplate :exec
INSERT INTO role_grant_templates (role_name, privilege)
VALUES (sqlc.arg(role_name), sqlc.arg(privilege))
ON CONFLICT(role_name, privilege) DO NOTHING;

-- name: GetRoleByName :one
SELECT * FROM roles WHERE name = ?;

-- name: ListRoles :many
SELECT * FROM roles ORDER BY name;

-- name: UpsertGroup :exec
INSERT INTO groups (id, workspace_id, provider, external_id, name)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(workspace_id, provider, external_id) DO UPDATE SET
  name = excluded.name;

-- name: GetGroup :one
SELECT * FROM groups
WHERE workspace_id = ? AND id = ?;

-- name: GetGroupByProviderExternalID :one
SELECT * FROM groups
WHERE workspace_id = ? AND provider = ? AND external_id = ?;

-- name: ListGroupsByWorkspace :many
SELECT * FROM groups
WHERE workspace_id = ?
ORDER BY name, id;

-- name: GetSCIMGroup :one
SELECT * FROM groups
WHERE provider = 'scim' AND id = ?;

-- name: ListSCIMGroups :many
SELECT * FROM groups
WHERE workspace_id = ''
  AND provider = 'scim'
  AND (sqlc.arg(external_id) = '' OR external_id = sqlc.arg(external_id))
  AND (sqlc.arg(display_name) = '' OR lower(name) = lower(sqlc.arg(display_name)))
ORDER BY name, id;

-- name: DeleteGroup :exec
DELETE FROM groups
WHERE workspace_id = ? AND id = ?;

-- name: DeleteSCIMGroup :exec
DELETE FROM groups
WHERE workspace_id = '' AND provider = 'scim' AND id = ?;

-- name: InsertGroupMember :exec
INSERT OR IGNORE INTO group_members (workspace_id, group_id, principal_id)
VALUES (?, ?, ?);

-- name: DeleteGroupMember :exec
DELETE FROM group_members
WHERE workspace_id = ? AND group_id = ? AND principal_id = ?;

-- name: ListGroupMembers :many
SELECT
  gm.group_id,
  gm.workspace_id,
  gm.principal_id,
  p.kind,
  p.email,
  p.display_name,
  gm.created_at
FROM group_members gm
JOIN principals p ON p.id = gm.principal_id
WHERE gm.workspace_id = ? AND gm.group_id = ?
ORDER BY p.email, p.display_name, gm.principal_id;

-- name: ListSCIMGroupMembers :many
SELECT
  gm.group_id,
  gm.workspace_id,
  gm.principal_id,
  p.kind,
  p.email,
  p.display_name,
  gm.created_at
FROM group_members gm
JOIN principals p ON p.id = gm.principal_id
WHERE gm.workspace_id = '' AND gm.group_id = ?
ORDER BY p.email, p.display_name, gm.principal_id;

-- name: DeleteSCIMGroupMembers :exec
DELETE FROM group_members
WHERE workspace_id = '' AND group_id = ?;

-- name: DeleteSCIMGroupMembersByPrincipal :exec
DELETE FROM group_members
WHERE workspace_id = '' AND principal_id = ?;

-- name: InsertRoleBinding :exec
INSERT OR IGNORE INTO role_bindings (id, workspace_id, role_id, principal_id, group_id)
VALUES (?, ?, ?, ?, ?);

-- name: InsertPlatformRoleBinding :exec
INSERT OR IGNORE INTO platform_role_bindings (id, role_id, principal_id)
VALUES (?, ?, ?);

-- name: GetRoleBindingByID :one
SELECT
  rb.id,
  rb.workspace_id,
  CASE
    WHEN NULLIF(rb.principal_id, '') IS NOT NULL AND p.kind = 'service_principal' THEN 'service_principal'
    WHEN NULLIF(rb.principal_id, '') IS NOT NULL THEN 'principal'
    ELSE 'group'
  END AS subject_type,
  COALESCE(NULLIF(rb.principal_id, ''), rb.group_id, '') AS subject_id,
  rb.principal_id,
  rb.group_id,
  p.email,
  p.display_name,
  g.name AS group_name,
  r.name AS role_name,
  rb.created_at
FROM role_bindings rb
JOIN roles r ON r.id = rb.role_id
LEFT JOIN principals p ON p.id = NULLIF(rb.principal_id, '')
LEFT JOIN groups g ON g.id = rb.group_id
WHERE rb.workspace_id = ? AND rb.id = ?;

-- name: UpdateRoleBindingByID :exec
UPDATE role_bindings
SET role_id = ?, principal_id = ?, group_id = ?
WHERE workspace_id = ? AND id = ?;

-- name: DeleteRoleBindingByID :exec
DELETE FROM role_bindings
WHERE workspace_id = ? AND id = ?;

-- name: ListRoleBindingsByWorkspace :many
SELECT
  rb.id,
  rb.workspace_id,
  CASE
    WHEN NULLIF(rb.principal_id, '') IS NOT NULL AND p.kind = 'service_principal' THEN 'service_principal'
    WHEN NULLIF(rb.principal_id, '') IS NOT NULL THEN 'principal'
    ELSE 'group'
  END AS subject_type,
  COALESCE(NULLIF(rb.principal_id, ''), rb.group_id, '') AS subject_id,
  rb.principal_id,
  rb.group_id,
  p.email,
  p.display_name,
  g.name AS group_name,
  r.name AS role_name,
  rb.created_at
FROM role_bindings rb
JOIN roles r ON r.id = rb.role_id
LEFT JOIN principals p ON p.id = NULLIF(rb.principal_id, '')
LEFT JOIN groups g ON g.id = rb.group_id
WHERE rb.workspace_id = ?
ORDER BY subject_type, p.email, g.name, r.name;

-- name: DeletePrincipalRoleBindings :exec
DELETE FROM role_bindings
WHERE workspace_id = ? AND principal_id = ?;

-- name: CreateSession :exec
INSERT INTO sessions (id, principal_id, token_fingerprint, token_verifier, expires_at)
VALUES (?, ?, ?, ?, ?);

-- name: GetSessionByTokenFingerprint :one
SELECT * FROM sessions
WHERE token_fingerprint = ?
  AND datetime(expires_at) > CURRENT_TIMESTAMP
  AND revoked_at IS NULL
  AND NOT EXISTS (
    SELECT 1
    FROM desktop_sessions
    WHERE desktop_sessions.session_id = sessions.id
      AND (
        datetime(desktop_sessions.absolute_expires_at) <= CURRENT_TIMESTAMP
        OR datetime(sessions.last_seen_at, '+30 minutes') <= CURRENT_TIMESTAMP
      )
  );

-- name: GetSessionByTokenFingerprintForAudit :one
SELECT * FROM sessions
WHERE token_fingerprint = ?
ORDER BY created_at DESC
LIMIT 1;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: DeleteSessionByTokenFingerprint :exec
DELETE FROM sessions WHERE token_fingerprint = ?;

-- name: ListSessionsByPrincipal :many
SELECT
  sessions.id,
  sessions.principal_id,
  sessions.token_fingerprint,
  sessions.token_verifier,
  sessions.expires_at,
  sessions.created_at,
  sessions.last_seen_at,
  sessions.revoked_at,
  COALESCE(desktop_sessions.instance_id, '') AS desktop_instance_id,
  COALESCE(desktop_sessions.profile_id, '') AS desktop_profile_id,
  COALESCE(desktop_sessions.client_id, '') AS desktop_client_id,
  COALESCE(desktop_sessions.absolute_expires_at, '') AS desktop_absolute_expires_at
FROM sessions
LEFT JOIN desktop_sessions ON desktop_sessions.session_id = sessions.id
WHERE sessions.principal_id = ?
ORDER BY sessions.created_at DESC;

-- name: RevokeSession :exec
UPDATE sessions
SET revoked_at = COALESCE(revoked_at, CURRENT_TIMESTAMP)
WHERE id = ?;

-- name: RevokeSessionForPrincipal :one
UPDATE sessions
SET revoked_at = COALESCE(revoked_at, CURRENT_TIMESTAMP)
WHERE principal_id = ? AND id = ?
RETURNING *;

-- name: RevokeSessionsByPrincipal :exec
UPDATE sessions
SET revoked_at = COALESCE(revoked_at, CURRENT_TIMESTAMP)
WHERE principal_id = ? AND revoked_at IS NULL;

-- name: CleanDesktopAuthorizationCodes :exec
DELETE FROM desktop_authorization_codes
WHERE expires_at <= sqlc.arg(now) OR consumed_at IS NOT NULL;

-- name: CreateDesktopAuthorizationCode :exec
INSERT INTO desktop_authorization_codes (
  code_hash, principal_id, client_id, instance_id, profile_id,
  redirect_uri, code_challenge, return_path, expires_at, consumed_at, created_at
) VALUES (
  sqlc.arg(code_hash), sqlc.arg(principal_id), sqlc.arg(client_id),
  sqlc.arg(instance_id), sqlc.arg(profile_id), sqlc.arg(redirect_uri),
  sqlc.arg(code_challenge), sqlc.arg(return_path), sqlc.arg(expires_at),
  NULL, sqlc.arg(created_at)
);

-- name: GetDesktopAuthorizationCode :one
SELECT code_hash, principal_id, client_id, instance_id, profile_id,
       redirect_uri, code_challenge, return_path, expires_at, consumed_at,
       created_at
FROM desktop_authorization_codes
WHERE code_hash = sqlc.arg(code_hash);

-- name: ConsumeDesktopAuthorizationCode :execrows
UPDATE desktop_authorization_codes
SET consumed_at = sqlc.arg(consumed_at)
WHERE code_hash = sqlc.arg(code_hash) AND consumed_at IS NULL;

-- name: CreateDesktopSessionBinding :exec
INSERT INTO desktop_sessions (
  session_id, instance_id, profile_id, client_id, absolute_expires_at, created_at
) VALUES (
  sqlc.arg(session_id), sqlc.arg(instance_id), sqlc.arg(profile_id),
  sqlc.arg(client_id), sqlc.arg(absolute_expires_at), sqlc.arg(created_at)
);

-- name: GetDesktopSessionByTokenFingerprint :one
SELECT s.id, s.principal_id, s.token_verifier, s.expires_at,
       ds.instance_id, ds.profile_id, ds.client_id,
       ds.absolute_expires_at, ds.created_at
FROM sessions s
JOIN desktop_sessions ds ON ds.session_id = s.id
WHERE s.token_fingerprint = sqlc.arg(token_fingerprint)
  AND s.revoked_at IS NULL
  AND datetime(s.expires_at) > CURRENT_TIMESTAMP
  AND datetime(ds.absolute_expires_at) > CURRENT_TIMESTAMP
  AND datetime(s.last_seen_at) > datetime(sqlc.arg(idle_cutoff));

-- name: RevokeActiveSessionByID :execrows
UPDATE sessions
SET revoked_at = COALESCE(revoked_at, CURRENT_TIMESTAMP)
WHERE id = sqlc.arg(id) AND revoked_at IS NULL;

-- name: CreateAPIToken :exec
INSERT INTO api_tokens (id, principal_id, workspace_id, name, token_fingerprint, token_verifier, privileges_json, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetAPITokenByFingerprint :one
SELECT * FROM api_tokens
WHERE token_fingerprint = ?
  AND revoked_at IS NULL
  AND (expires_at IS NULL OR datetime(expires_at) > CURRENT_TIMESTAMP);

-- name: GetAPITokenByFingerprintForAudit :one
SELECT * FROM api_tokens
WHERE token_fingerprint = ?
ORDER BY created_at DESC
LIMIT 1;

-- name: TouchAPIToken :exec
UPDATE api_tokens SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: ListAPITokensByPrincipal :many
SELECT * FROM api_tokens
WHERE principal_id = ?
ORDER BY created_at DESC;

-- name: RevokeAPIToken :exec
UPDATE api_tokens
SET revoked_at = COALESCE(revoked_at, CURRENT_TIMESTAMP)
WHERE id = ?;

-- name: RevokeAPITokenForPrincipal :one
UPDATE api_tokens
SET revoked_at = COALESCE(revoked_at, CURRENT_TIMESTAMP)
WHERE principal_id = ? AND id = ?
RETURNING *;

-- name: RevokeAPITokensByPrincipal :exec
UPDATE api_tokens
SET revoked_at = COALESCE(revoked_at, CURRENT_TIMESTAMP)
WHERE principal_id = ? AND revoked_at IS NULL;

-- name: CreateServicePrincipalSecret :exec
INSERT INTO service_principal_secrets (id, service_principal_id, name, secret_fingerprint, secret_verifier, expires_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: GetServicePrincipalSecretByFingerprint :one
SELECT s.*
FROM service_principal_secrets s
JOIN principals p ON p.id = s.service_principal_id
WHERE p.kind = 'service_principal'
  AND s.service_principal_id = ?
  AND s.secret_fingerprint = ?
  AND s.revoked_at IS NULL
  AND (s.expires_at IS NULL OR datetime(s.expires_at) > CURRENT_TIMESTAMP);

-- name: RevokeServicePrincipalSecret :exec
UPDATE service_principal_secrets
SET revoked_at = COALESCE(revoked_at, CURRENT_TIMESTAMP)
WHERE service_principal_id = ? AND id = ?;

-- name: ListServicePrincipalSecretsByPrincipal :many
SELECT * FROM service_principal_secrets
WHERE service_principal_id = sqlc.arg(service_principal_id)
ORDER BY created_at DESC, id DESC;

-- name: GetServicePrincipalSecretByID :one
SELECT * FROM service_principal_secrets
WHERE service_principal_id = sqlc.arg(service_principal_id)
  AND id = sqlc.arg(id);

-- name: InsertAuditEvent :exec
INSERT INTO audit_events (id, workspace_id, principal_id, action, target_type, target_id, privilege, status, request_id, correlation_id, metadata_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListAuditEvents :many
SELECT * FROM audit_events
WHERE (? = '' OR workspace_id = ?)
  AND (? = '' OR principal_id = ?)
  AND (? = '' OR action = ?)
  AND (? = '' OR target_type = ?)
  AND (? = '' OR target_id = ?)
  AND (? = '' OR created_at >= ?)
  AND (? = '' OR created_at <= ?)
  AND (? = '' OR created_at < ? OR (created_at = ? AND id < ?))
ORDER BY created_at DESC, id DESC
LIMIT ?;

-- name: ListPrincipals :many
WITH params AS (
  SELECT CAST(sqlc.arg(email) AS TEXT) AS email, CAST(sqlc.arg(search) AS TEXT) AS search
)
SELECT principals.id, principals.email, principals.display_name,
       principals.created_at, principals.updated_at, principals.kind, principals.disabled_at
FROM principals CROSS JOIN params
WHERE (params.email = '' OR lower(principals.email) = lower(params.email))
  AND (params.search = '' OR lower(principals.email) LIKE '%' || lower(params.search) || '%'
       OR lower(display_name) LIKE '%' || lower(params.search) || '%')
ORDER BY principals.email, principals.id;

-- name: SearchPrincipals :many
SELECT principals.id, principals.email, principals.display_name,
       principals.created_at, principals.updated_at, principals.kind, principals.disabled_at
FROM principals
WHERE principals.kind = 'user'
  AND (principals.disabled_at IS NULL OR principals.disabled_at = '')
  AND (lower(principals.email) LIKE '%' || lower(sqlc.arg(search)) || '%'
       OR lower(principals.display_name) LIKE '%' || lower(sqlc.arg(search)) || '%')
ORDER BY lower(principals.display_name), principals.email, principals.id
LIMIT sqlc.arg(result_limit);

-- name: ChangeLocalCredentialPassword :exec
UPDATE local_user_credentials
SET password_verifier = sqlc.arg(password_verifier), must_change_password = 0,
    updated_at = CURRENT_TIMESTAMP, password_changed_at = CURRENT_TIMESTAMP
WHERE principal_id = sqlc.arg(principal_id);

-- name: UpsertLocalCredential :exec
INSERT INTO local_user_credentials (principal_id, password_verifier, must_change_password, updated_at, password_changed_at)
VALUES (sqlc.arg(principal_id), sqlc.arg(password_verifier), sqlc.arg(must_change_password), CURRENT_TIMESTAMP, NULL)
ON CONFLICT(principal_id) DO UPDATE SET
  password_verifier = excluded.password_verifier,
  must_change_password = excluded.must_change_password,
  updated_at = CURRENT_TIMESTAMP,
  password_changed_at = NULL;

-- name: GetLocalCredentialByEmail :one
SELECT p.id, p.kind, p.email, p.display_name, p.disabled_at, p.created_at, p.updated_at,
       c.password_verifier, c.must_change_password, c.created_at AS credential_created_at,
       c.updated_at AS credential_updated_at, c.password_changed_at
FROM principals p
JOIN local_user_credentials c ON c.principal_id = p.id
WHERE lower(p.email) = lower(sqlc.arg(email)) AND p.email <> ''
LIMIT 1;

-- name: GetLocalCredentialByPrincipalID :one
SELECT p.id, p.kind, p.email, p.display_name, p.disabled_at, p.created_at, p.updated_at,
       c.password_verifier, c.must_change_password, c.created_at AS credential_created_at,
       c.updated_at AS credential_updated_at, c.password_changed_at
FROM principals p
JOIN local_user_credentials c ON c.principal_id = p.id
WHERE p.id = sqlc.arg(principal_id)
LIMIT 1;

-- name: ListAllRoleBindings :many
SELECT rb.id, rb.workspace_id, COALESCE(p.id, '') AS principal_id, COALESCE(g.id, '') AS group_id,
       COALESCE(p.email, '') AS email, COALESCE(p.display_name, '') AS display_name,
       COALESCE(g.name, '') AS group_name, roles.name AS role, rb.created_at
FROM role_bindings rb
JOIN roles ON roles.id = rb.role_id
LEFT JOIN principals p ON p.id = rb.principal_id
LEFT JOIN groups g ON g.id = rb.group_id
ORDER BY rb.workspace_id, rb.created_at, rb.id;

-- name: EnablePrincipal :exec
UPDATE principals SET disabled_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = sqlc.arg(id);

-- name: ListAllGroups :many
SELECT id, workspace_id, provider, external_id, name, created_at
FROM groups ORDER BY workspace_id, name, id;

-- name: SearchGroups :many
SELECT id, workspace_id, provider, external_id, name, created_at
FROM groups
WHERE (workspace_id = sqlc.arg(workspace_id) OR (workspace_id = '' AND provider = 'scim'))
  AND (lower(name) LIKE '%' || lower(sqlc.arg(search)) || '%'
       OR lower(id) LIKE '%' || lower(sqlc.arg(search)) || '%'
       OR lower(external_id) LIKE '%' || lower(sqlc.arg(search)) || '%')
ORDER BY lower(name), id
LIMIT sqlc.arg(result_limit);

-- name: ListGroupMembersByGroup :many
SELECT gm.group_id, g.workspace_id, gm.principal_id, p.kind, p.email, p.display_name, gm.created_at
FROM group_members gm
JOIN groups g ON g.id = gm.group_id
JOIN principals p ON p.id = gm.principal_id
WHERE gm.group_id = sqlc.arg(group_id)
ORDER BY p.email, p.display_name, gm.principal_id;

-- name: DeleteWorkspaceGrants :exec
DELETE FROM grants
WHERE grants.object_id IN (
  SELECT securable_objects.id FROM securable_objects
  WHERE (
    securable_objects.workspace_id = sqlc.arg(workspace_id)
    OR securable_objects.id = sqlc.arg(workspace_object_id)
  )
  AND securable_objects.object_type <> 'project_environment'
);

-- name: DeleteWorkspaceDataPolicies :exec
DELETE FROM data_policies WHERE workspace_id = sqlc.arg(workspace_id);

-- name: InitializeSecurableObjectOwner :exec
UPDATE securable_objects
SET owner_principal_id = COALESCE(NULLIF(owner_principal_id, ''), sqlc.arg(owner_principal_id)),
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id);

-- name: GetScopedGrant :one
SELECT g.id, g.object_id, so.object_type, so.workspace_id, g.subject_type, g.subject_id, g.privilege, g.created_at
FROM grants g
JOIN securable_objects so ON so.id = g.object_id
WHERE g.id = sqlc.arg(id)
  AND (so.workspace_id = sqlc.arg(workspace_id) OR so.id = sqlc.arg(workspace_object_id));

-- name: DeleteScopedGrant :exec
DELETE FROM grants
WHERE grants.id = sqlc.arg(id)
  AND grants.object_id IN (
    SELECT securable_objects.id FROM securable_objects
    WHERE securable_objects.workspace_id = sqlc.arg(workspace_id) OR securable_objects.id = sqlc.arg(workspace_object_id)
  );

-- name: UpdateGrantByID :execresult
UPDATE grants
SET object_id = sqlc.arg(object_id),
    subject_type = sqlc.arg(subject_type),
    subject_id = sqlc.arg(subject_id),
    privilege = sqlc.arg(privilege)
WHERE id = sqlc.arg(id);

-- name: UpsertDataPolicy :exec
INSERT INTO data_policies (id, workspace_id, object_id, subject_type, subject_id, policy_type, expression_json)
VALUES (sqlc.arg(id), sqlc.arg(workspace_id), sqlc.arg(object_id), sqlc.arg(subject_type),
        sqlc.arg(subject_id), sqlc.arg(policy_type), sqlc.arg(expression_json))
ON CONFLICT(id) DO UPDATE SET
  workspace_id = excluded.workspace_id, object_id = excluded.object_id,
  subject_type = excluded.subject_type, subject_id = excluded.subject_id,
  policy_type = excluded.policy_type, expression_json = excluded.expression_json,
  updated_at = CURRENT_TIMESTAMP;

-- name: GetDataPolicy :one
SELECT id, workspace_id, object_id, subject_type, subject_id, policy_type, expression_json, created_at, updated_at
FROM data_policies WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id);

-- name: GroupMemberExists :one
SELECT EXISTS (
  SELECT 1
  FROM group_members gm
  JOIN groups member_group
    ON member_group.id = gm.group_id
   AND member_group.workspace_id = gm.workspace_id
  WHERE gm.group_id = sqlc.arg(group_id)
    AND gm.principal_id = sqlc.arg(principal_id)
);

-- name: DeleteDataPolicy :exec
DELETE FROM data_policies WHERE workspace_id = sqlc.arg(workspace_id) AND id = sqlc.arg(id);

-- name: SetSecurableObjectOwner :exec
UPDATE securable_objects
SET owner_principal_id = sqlc.arg(owner_principal_id), updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id);

-- name: GetSecurableObjectID :one
SELECT id FROM securable_objects WHERE id = sqlc.arg(id);

-- name: UpsertSecurableObject :exec
INSERT INTO securable_objects (id, object_type, workspace_id, parent_id, display_name)
VALUES (sqlc.arg(id), sqlc.arg(object_type), sqlc.arg(workspace_id), sqlc.arg(parent_id), sqlc.arg(display_name))
ON CONFLICT(id) DO UPDATE SET
  object_type = excluded.object_type, workspace_id = excluded.workspace_id,
  parent_id = excluded.parent_id,
  display_name = COALESCE(NULLIF(excluded.display_name, ''), securable_objects.display_name),
  updated_at = CURRENT_TIMESTAMP;

-- name: UpsertOwnedSecurableObject :exec
INSERT INTO securable_objects (id, object_type, workspace_id, parent_id, owner_principal_id, display_name)
VALUES (sqlc.arg(id), sqlc.arg(object_type), sqlc.arg(workspace_id), sqlc.arg(parent_id),
        sqlc.arg(owner_principal_id), sqlc.arg(display_name))
ON CONFLICT(id) DO UPDATE SET
  object_type = excluded.object_type, workspace_id = excluded.workspace_id,
  parent_id = excluded.parent_id,
  owner_principal_id = COALESCE(NULLIF(securable_objects.owner_principal_id, ''), NULLIF(excluded.owner_principal_id, ''), ''),
  display_name = COALESCE(NULLIF(excluded.display_name, ''), securable_objects.display_name),
  updated_at = CURRENT_TIMESTAMP;

-- name: GetSecurableObjectParent :one
SELECT parent_id FROM securable_objects WHERE id = sqlc.arg(id);

-- name: GetSecurableObjectOwner :one
SELECT owner_principal_id FROM securable_objects WHERE id = sqlc.arg(id);

-- name: GetSecurableObject :one
SELECT id, object_type, workspace_id, parent_id, owner_principal_id, display_name, created_at, updated_at
FROM securable_objects WHERE id = sqlc.arg(id);

-- name: DeleteRoleBindingGrants :exec
DELETE FROM grants WHERE id LIKE sqlc.arg(id_pattern);

-- name: ListRolePrivileges :many
SELECT privilege FROM role_grant_templates WHERE role_name = sqlc.arg(role_name) ORDER BY privilege;

-- name: UpsertGrant :exec
INSERT INTO grants (id, object_id, subject_type, subject_id, privilege)
VALUES (sqlc.arg(id), sqlc.arg(object_id), sqlc.arg(subject_type), sqlc.arg(subject_id), sqlc.arg(privilege))
ON CONFLICT(object_id, subject_type, subject_id, privilege) DO UPDATE SET id = excluded.id;

-- name: ListDataPoliciesByObjectScope :many
WITH params AS (
  SELECT CAST(sqlc.arg(object_ids_json) AS TEXT) AS object_ids_json
), object_scope AS (
  SELECT CAST(key AS INTEGER) AS ordinal, CAST(value AS TEXT) AS object_id
  FROM params, json_each(params.object_ids_json)
)
SELECT dp.id, dp.workspace_id, dp.object_id, dp.subject_type, dp.subject_id,
       dp.policy_type, dp.expression_json, dp.created_at, dp.updated_at
FROM object_scope scope
JOIN data_policies dp ON dp.object_id = scope.object_id
ORDER BY scope.ordinal, dp.policy_type, dp.id;

-- name: ListGrantsByObjectScope :many
WITH params AS (
  SELECT CAST(sqlc.arg(object_ids_json) AS TEXT) AS object_ids_json
), object_scope AS (
  SELECT CAST(key AS INTEGER) AS ordinal, CAST(value AS TEXT) AS object_id
  FROM params, json_each(params.object_ids_json)
)
SELECT g.id, g.object_id, so.object_type, so.workspace_id, so.parent_id,
       parent.object_type AS parent_type, parent.id AS parent_id,
       g.subject_type, g.subject_id, g.privilege, g.created_at
FROM object_scope scope
JOIN grants g ON g.object_id = scope.object_id
JOIN securable_objects so ON so.id = g.object_id
LEFT JOIN securable_objects parent ON parent.id = so.parent_id
ORDER BY scope.ordinal, g.subject_type, g.subject_id, g.privilege;

-- name: FindAuthorizingGrant :one
WITH params AS (
  SELECT CAST(sqlc.arg(object_ids_json) AS TEXT) AS object_ids_json
), object_scope AS (
  SELECT CAST(key AS INTEGER) AS ordinal, CAST(value AS TEXT) AS object_id
  FROM params, json_each(params.object_ids_json)
)
SELECT g.id, g.object_id, g.subject_type, g.subject_id
FROM object_scope scope
JOIN grants g ON g.object_id = scope.object_id
LEFT JOIN groups member_group
  ON g.subject_type = 'group'
 AND member_group.id = g.subject_id
LEFT JOIN group_members gm
  ON g.subject_type = 'group'
 AND gm.group_id = member_group.id
 AND gm.workspace_id = member_group.workspace_id
 AND gm.principal_id = sqlc.arg(principal_id)
WHERE g.privilege = sqlc.arg(privilege)
  AND (
    g.subject_type IN ('principal', 'service_principal') AND g.subject_id = sqlc.arg(principal_id)
    OR gm.principal_id IS NOT NULL
  )
ORDER BY scope.ordinal
LIMIT 1;

-- name: CreateDeviceAuthorization :exec
INSERT INTO oauth_device_authorizations (
  id, client_id, device_code_hash, user_code_hash, target_id, project_id,
  privileges_json, status, expires_at, poll_interval_seconds, created_at
) VALUES (
  sqlc.arg(id), sqlc.arg(client_id), sqlc.arg(device_code_hash), sqlc.arg(user_code_hash),
  sqlc.arg(target_id), sqlc.arg(project_id), sqlc.arg(privileges_json), sqlc.arg(status),
  sqlc.arg(expires_at), sqlc.arg(poll_interval_seconds), sqlc.arg(created_at)
);

-- name: GetDeviceAuthorizationByUserCodeHash :one
SELECT id, client_id, device_code_hash, user_code_hash, target_id, project_id,
       privileges_json, status, principal_id, expires_at, poll_interval_seconds,
       last_polled_at, created_at, approved_at, denied_at, consumed_at
FROM oauth_device_authorizations
WHERE user_code_hash = sqlc.arg(user_code_hash);

-- name: GetDeviceAuthorizationByDeviceCodeHash :one
SELECT id, client_id, device_code_hash, user_code_hash, target_id, project_id,
       privileges_json, status, principal_id, expires_at, poll_interval_seconds,
       last_polled_at, created_at, approved_at, denied_at, consumed_at
FROM oauth_device_authorizations
WHERE device_code_hash = sqlc.arg(device_code_hash);

-- name: GetDeviceAuthorizationByID :one
SELECT id, client_id, device_code_hash, user_code_hash, target_id, project_id,
       privileges_json, status, principal_id, expires_at, poll_interval_seconds,
       last_polled_at, created_at, approved_at, denied_at, consumed_at
FROM oauth_device_authorizations
WHERE id = sqlc.arg(id);

-- name: ApproveDeviceAuthorization :execrows
UPDATE oauth_device_authorizations
SET status = 'approved', principal_id = sqlc.arg(principal_id), approved_at = sqlc.arg(approved_at)
WHERE id = sqlc.arg(id) AND status = 'pending' AND expires_at > sqlc.arg(approved_at);

-- name: DenyDeviceAuthorization :execrows
UPDATE oauth_device_authorizations
SET status = 'denied', principal_id = sqlc.arg(principal_id), denied_at = sqlc.arg(denied_at)
WHERE id = sqlc.arg(id) AND status = 'pending' AND expires_at > sqlc.arg(denied_at);

-- name: RecordDeviceAuthorizationPoll :exec
UPDATE oauth_device_authorizations
SET last_polled_at = sqlc.arg(last_polled_at)
WHERE id = sqlc.arg(id);

-- name: ConsumeDeviceAuthorization :execrows
UPDATE oauth_device_authorizations
SET status = 'consumed', consumed_at = sqlc.arg(consumed_at)
WHERE id = sqlc.arg(id) AND status = 'approved' AND expires_at > sqlc.arg(consumed_at);

-- name: CreateAuthoringSession :exec
INSERT INTO oauth_authoring_sessions (
  id, kind, client_id, principal_id, target_id, project_id, privileges_json,
  created_at, expires_at
) VALUES (
  sqlc.arg(id), sqlc.arg(kind), sqlc.arg(client_id), sqlc.arg(principal_id),
  sqlc.arg(target_id), sqlc.arg(project_id), sqlc.arg(privileges_json),
  sqlc.arg(created_at), sqlc.arg(expires_at)
);

-- name: CreateAuthoringCredential :exec
INSERT INTO oauth_authoring_credentials (
  id, session_id, access_token_hash, refresh_token_hash, access_expires_at,
  refresh_expires_at, active, created_at
) VALUES (
  sqlc.arg(id), sqlc.arg(session_id), sqlc.arg(access_token_hash),
  sqlc.narg(refresh_token_hash), sqlc.arg(access_expires_at),
  sqlc.narg(refresh_expires_at), 1, sqlc.arg(created_at)
);

-- name: GetAuthoringCredentialByAccessHash :one
SELECT
  c.id AS credential_id, c.session_id, c.access_expires_at, c.refresh_expires_at,
  c.active, c.replaced_at,
  s.kind, s.client_id, s.principal_id, s.target_id, s.project_id,
  s.privileges_json, s.created_at AS session_created_at,
  s.last_used_at, s.expires_at AS session_expires_at, s.revoked_at,
  p.id, p.kind AS principal_kind, p.email, p.display_name, p.disabled_at,
  p.created_at AS principal_created_at, p.updated_at AS principal_updated_at
FROM oauth_authoring_credentials c
JOIN oauth_authoring_sessions s ON s.id = c.session_id
JOIN principals p ON p.id = s.principal_id
WHERE c.access_token_hash = sqlc.arg(access_token_hash);

-- name: GetAuthoringCredentialByRefreshHash :one
SELECT
  c.id AS credential_id, c.session_id, c.access_expires_at, c.refresh_expires_at,
  c.active, c.replaced_at,
  s.kind, s.client_id, s.principal_id, s.target_id, s.project_id,
  s.privileges_json, s.created_at AS session_created_at,
  s.last_used_at, s.expires_at AS session_expires_at, s.revoked_at,
  p.id, p.kind AS principal_kind, p.email, p.display_name, p.disabled_at,
  p.created_at AS principal_created_at, p.updated_at AS principal_updated_at
FROM oauth_authoring_credentials c
JOIN oauth_authoring_sessions s ON s.id = c.session_id
JOIN principals p ON p.id = s.principal_id
WHERE c.refresh_token_hash = sqlc.arg(refresh_token_hash);

-- name: ReplaceAuthoringCredential :execrows
UPDATE oauth_authoring_credentials
SET active = 0, replaced_at = sqlc.arg(replaced_at)
WHERE id = sqlc.arg(id) AND active = 1;

-- name: UpdateAuthoringSessionExpiry :exec
UPDATE oauth_authoring_sessions
SET expires_at = sqlc.arg(expires_at)
WHERE id = sqlc.arg(id) AND revoked_at IS NULL;

-- name: TouchAuthoringSession :exec
UPDATE oauth_authoring_sessions
SET last_used_at = sqlc.arg(last_used_at)
WHERE id = sqlc.arg(id) AND revoked_at IS NULL;

-- name: RevokeAuthoringSession :execrows
UPDATE oauth_authoring_sessions
SET revoked_at = COALESCE(revoked_at, sqlc.arg(revoked_at))
WHERE id = sqlc.arg(id) AND principal_id = sqlc.arg(principal_id);

-- name: RevokeAuthoringSessionByID :exec
UPDATE oauth_authoring_sessions
SET revoked_at = COALESCE(revoked_at, sqlc.arg(revoked_at))
WHERE id = sqlc.arg(id);

-- name: ListAuthoringSessions :many
SELECT id, kind, client_id, principal_id, target_id, project_id, privileges_json,
       created_at, last_used_at, expires_at, revoked_at
FROM oauth_authoring_sessions
WHERE principal_id = sqlc.arg(principal_id)
ORDER BY created_at DESC, id;
