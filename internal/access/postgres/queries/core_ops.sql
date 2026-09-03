-- Stable access core PostgreSQL leaves. Domain validation, hashing, and
-- transaction ownership remain in access_core.go.

-- name: SearchPrincipals :many
SELECT id, principal_type, status, COALESCE(email, '') AS email, display_name,
       disabled_at, blocked_at, last_seen_at, created_at, updated_at
FROM access.principal
WHERE revoked_at IS NULL
  AND (email ILIKE '%' || sqlc.arg(query)::text || '%' OR display_name ILIKE '%' || sqlc.arg(query)::text || '%')
ORDER BY display_name
LIMIT sqlc.arg(page_size)::int;

-- name: UpsertPrincipal :execresult
INSERT INTO access.principal(id, principal_type, status, email, display_name)
VALUES (sqlc.arg(id)::uuid, sqlc.arg(kind), 'active', sqlc.arg(email), sqlc.arg(display_name))
ON CONFLICT (id) DO UPDATE
SET email = EXCLUDED.email, display_name = EXCLUDED.display_name,
    updated_at = clock_timestamp()
WHERE access.principal.principal_type = EXCLUDED.principal_type
  AND access.principal.revoked_at IS NULL;

-- name: PrincipalKind :one
SELECT principal_type
FROM access.principal
WHERE id = sqlc.arg(id)::uuid;

-- name: InsertPlatformRole :exec
INSERT INTO access.platform_role_binding(id, principal_id, role)
VALUES (sqlc.arg(id)::uuid, sqlc.arg(principal_id)::uuid, sqlc.arg(role))
ON CONFLICT (principal_id, role) WHERE revoked_at IS NULL DO NOTHING;

-- name: IsPlatformAdmin :one
SELECT EXISTS (
    SELECT 1
    FROM access.principal p
    JOIN access.platform_role_binding b ON b.principal_id = p.id
    WHERE p.id = sqlc.arg(id)::uuid
      AND p.status = 'active' AND p.revoked_at IS NULL
      AND p.disabled_at IS NULL AND p.blocked_at IS NULL
      AND b.role = 'platform_admin' AND b.revoked_at IS NULL
);

-- name: ListServicePrincipals :many
SELECT id, principal_type, status, COALESCE(email, '') AS email, display_name,
       disabled_at, blocked_at, last_seen_at, created_at, updated_at
FROM access.principal
WHERE principal_type = 'service' AND revoked_at IS NULL
ORDER BY created_at DESC
LIMIT sqlc.arg(page_size)::int;

-- name: DisableServicePrincipal :execresult
UPDATE access.principal
SET status = 'disabled', disabled_at = COALESCE(disabled_at, clock_timestamp()),
    revoked_at = COALESCE(revoked_at, clock_timestamp()), updated_at = clock_timestamp()
WHERE id = sqlc.arg(id)::uuid AND principal_type = 'service' AND revoked_at IS NULL;

-- name: DisablePrincipal :execresult
UPDATE access.principal
SET status = 'disabled', disabled_at = COALESCE(disabled_at, clock_timestamp()),
    updated_at = clock_timestamp()
WHERE id = sqlc.arg(id)::uuid AND revoked_at IS NULL;

-- name: RevokePrincipal :execresult
UPDATE access.principal
SET status = 'disabled', disabled_at = COALESCE(disabled_at, clock_timestamp()),
    revoked_at = COALESCE(revoked_at, clock_timestamp()), updated_at = clock_timestamp()
WHERE id = sqlc.arg(id)::uuid AND revoked_at IS NULL;

-- name: BlockPrincipal :execresult
UPDATE access.principal
SET status = 'disabled', blocked_at = COALESCE(blocked_at, clock_timestamp()),
    updated_at = clock_timestamp()
WHERE id = sqlc.arg(id)::uuid AND revoked_at IS NULL;

-- name: EnablePrincipal :execresult
UPDATE access.principal
SET status = 'active', disabled_at = NULL, blocked_at = NULL,
    updated_at = clock_timestamp()
WHERE id = sqlc.arg(id)::uuid AND revoked_at IS NULL;

-- name: RevokePrincipalSessions :exec
UPDATE access.session SET revoked_at = clock_timestamp()
WHERE principal_id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL;

-- name: RevokePrincipalTokens :exec
UPDATE access.api_token SET revoked_at = clock_timestamp()
WHERE principal_id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL;

-- name: RevokePrincipalSecrets :exec
UPDATE access.service_principal_secret SET revoked_at = clock_timestamp()
WHERE service_principal_id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL;

-- name: RevokePrincipalGroups :exec
UPDATE access.principal_group SET revoked_at = clock_timestamp()
WHERE principal_id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL;

-- name: FindGroupByExternal :one
SELECT id
FROM access.access_group
WHERE provider = sqlc.arg(provider) AND external_id = sqlc.arg(external_id)
  AND revoked_at IS NULL;

-- name: UpsertGroup :exec
INSERT INTO access.access_group(id, name, provider, external_id)
VALUES (sqlc.arg(id)::uuid, sqlc.arg(name), sqlc.arg(provider), sqlc.arg(external_id))
ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name;

-- name: GetGroup :one
SELECT id, provider, external_id, name, created_at
FROM access.access_group
WHERE revoked_at IS NULL
  AND (id = sqlc.arg(id)::uuid OR (provider = sqlc.arg(provider) AND external_id = sqlc.arg(external_id)));

-- name: ListGroups :many
SELECT id, provider, external_id, name, created_at
FROM access.access_group
WHERE revoked_at IS NULL
ORDER BY name
LIMIT sqlc.arg(page_size)::int;

-- name: SearchGroups :many
SELECT id, provider, external_id, name, created_at
FROM access.access_group
WHERE revoked_at IS NULL
  AND (name ILIKE '%' || sqlc.arg(query)::text || '%' OR external_id ILIKE '%' || sqlc.arg(query)::text || '%')
ORDER BY name
LIMIT sqlc.arg(page_size)::int;

-- name: RevokeGroup :execresult
UPDATE access.access_group SET revoked_at = clock_timestamp()
WHERE id = sqlc.arg(id)::uuid AND revoked_at IS NULL;

-- name: AddGroupMember :exec
INSERT INTO access.principal_group(group_id, principal_id)
SELECT sqlc.arg(group_id)::uuid, sqlc.arg(principal_id)::uuid
WHERE EXISTS (SELECT 1 FROM access.access_group WHERE id = sqlc.arg(group_id)::uuid AND revoked_at IS NULL)
  AND EXISTS (SELECT 1 FROM access.principal WHERE id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL)
  AND NOT EXISTS (
      SELECT 1 FROM access.principal_group
      WHERE group_id = sqlc.arg(group_id)::uuid AND principal_id = sqlc.arg(principal_id)::uuid
        AND revoked_at IS NULL
  )
ON CONFLICT DO NOTHING;

-- name: RemoveGroupMember :execresult
UPDATE access.principal_group SET revoked_at = clock_timestamp()
WHERE group_id = sqlc.arg(group_id)::uuid AND principal_id = sqlc.arg(principal_id)::uuid
  AND revoked_at IS NULL;

-- name: ListGroupMembers :many
SELECT g.id, p.id, p.principal_type, COALESCE(p.email, ''), p.display_name, pg.created_at
FROM access.principal_group pg
JOIN access.access_group g ON g.id = pg.group_id
JOIN access.principal p ON p.id = pg.principal_id
WHERE g.id = sqlc.arg(group_id)::uuid AND pg.revoked_at IS NULL AND g.revoked_at IS NULL
ORDER BY p.display_name;

-- name: ListGroupIDs :many
SELECT pg.group_id
FROM access.principal_group pg
JOIN access.access_group g ON g.id = pg.group_id
WHERE pg.principal_id = sqlc.arg(principal_id)::uuid
  AND pg.revoked_at IS NULL AND g.revoked_at IS NULL
ORDER BY pg.group_id;

-- name: InsertPrincipal :exec
INSERT INTO access.principal(id, principal_type, status, email, display_name)
VALUES (sqlc.arg(id)::uuid, 'user', 'active', sqlc.arg(email), sqlc.arg(display_name));

-- name: InsertLocalCredential :exec
INSERT INTO access.local_credential(principal_id, verifier, must_change, password_changed_at)
VALUES (sqlc.arg(principal_id)::uuid, sqlc.arg(verifier), sqlc.arg(must_change), clock_timestamp());

-- name: GetLocalCredential :one
SELECT principal_id, must_change, created_at, updated_at, password_changed_at
FROM access.local_credential
WHERE principal_id = sqlc.arg(principal_id)::uuid;

-- name: FindLocalCredentialByEmail :one
SELECT p.id, c.verifier, p.disabled_at, p.blocked_at
FROM access.principal p
JOIN access.local_credential c ON c.principal_id = p.id
WHERE lower(p.email) = lower(sqlc.arg(email)::text)
  AND p.status = 'active' AND p.revoked_at IS NULL AND c.revoked_at IS NULL;

-- name: LockLocalVerifier :one
SELECT verifier
FROM access.local_credential
WHERE principal_id = sqlc.arg(principal_id)::uuid
FOR UPDATE;

-- name: UpdateLocalCredential :execresult
UPDATE access.local_credential
SET verifier = sqlc.arg(verifier), must_change = sqlc.arg(must_change),
    updated_at = clock_timestamp(), password_changed_at = clock_timestamp()
WHERE principal_id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL;

-- name: CreateBrowserSession :execresult
INSERT INTO access.session(id, principal_id, token_fingerprint, verifier, expires_at, kind)
SELECT sqlc.arg(id)::uuid, sqlc.arg(principal_id)::uuid, sqlc.arg(token_fingerprint),
       sqlc.arg(verifier), clock_timestamp() + sqlc.arg(ttl)::interval, 'browser'
WHERE EXISTS (
    SELECT 1 FROM access.principal
    WHERE id = sqlc.arg(principal_id)::uuid AND status = 'active'
      AND disabled_at IS NULL AND blocked_at IS NULL
);

-- name: FindBrowserSession :one
SELECT p.id, s.token_fingerprint, s.verifier
FROM access.session s
JOIN access.principal p ON p.id = s.principal_id
WHERE s.token_fingerprint = sqlc.arg(token_fingerprint)
  AND s.revoked_at IS NULL AND s.expires_at > clock_timestamp()
  AND p.status = 'active' AND p.revoked_at IS NULL
  AND p.disabled_at IS NULL AND p.blocked_at IS NULL;

-- name: TouchBrowserSession :exec
UPDATE access.session SET last_seen_at = clock_timestamp()
WHERE token_fingerprint = sqlc.arg(token_fingerprint);

-- name: RevokeBrowserSession :execresult
UPDATE access.session SET revoked_at = clock_timestamp()
WHERE token_fingerprint = sqlc.arg(token_fingerprint) AND revoked_at IS NULL;

-- name: ListSessions :many
SELECT id, principal_id, kind, instance_id, profile_id, client_id,
       expires_at, absolute_expires_at, created_at, last_seen_at, revoked_at
FROM access.session
WHERE principal_id = sqlc.arg(principal_id)::uuid
ORDER BY created_at DESC
LIMIT sqlc.arg(page_size)::int;

-- name: RevokeSession :execresult
UPDATE access.session SET revoked_at = clock_timestamp()
WHERE id = sqlc.arg(id)::uuid AND revoked_at IS NULL;

-- name: RevokeSessionForPrincipal :execresult
UPDATE access.session SET revoked_at = clock_timestamp()
WHERE id = sqlc.arg(id)::uuid AND principal_id = sqlc.arg(principal_id)::uuid
  AND revoked_at IS NULL;

-- name: CheckExpiry :one
SELECT sqlc.arg(expires_at)::timestamptz > clock_timestamp()
   AND sqlc.arg(expires_at)::timestamptz <= clock_timestamp() + interval '365 days';

-- name: CreateAPIToken :execresult
INSERT INTO access.api_token(id, principal_id, name, token_fingerprint, verifier, capabilities, expires_at)
SELECT sqlc.arg(id)::uuid, sqlc.arg(principal_id)::uuid, sqlc.arg(name),
       sqlc.arg(token_fingerprint), sqlc.arg(verifier), sqlc.arg(capabilities)::jsonb,
       sqlc.arg(expires_at)
WHERE sqlc.arg(expires_at)::timestamptz > clock_timestamp()
  AND sqlc.arg(expires_at)::timestamptz <= clock_timestamp() + interval '365 days'
  AND EXISTS (
      SELECT 1 FROM access.principal
      WHERE id = sqlc.arg(principal_id)::uuid AND status = 'active'
        AND revoked_at IS NULL AND disabled_at IS NULL AND blocked_at IS NULL
  );

-- name: GetAPIToken :one
SELECT id, principal_id, name, capabilities, expires_at, created_at, last_used_at, revoked_at
FROM access.api_token
WHERE id = sqlc.arg(id)::uuid;

-- name: FindAPITokenByFingerprint :one
SELECT t.id, t.verifier
FROM access.api_token t
JOIN access.principal p ON p.id = t.principal_id
WHERE t.token_fingerprint = sqlc.arg(token_fingerprint)
  AND t.revoked_at IS NULL AND t.expires_at > clock_timestamp()
  AND p.status = 'active' AND p.revoked_at IS NULL
  AND p.disabled_at IS NULL AND p.blocked_at IS NULL;

-- name: TouchAPIToken :exec
UPDATE access.api_token SET last_used_at = clock_timestamp()
WHERE id = sqlc.arg(id)::uuid;

-- name: ListAPITokenIDs :many
SELECT id FROM access.api_token
WHERE principal_id = sqlc.arg(principal_id)::uuid
ORDER BY created_at DESC
LIMIT sqlc.arg(page_size)::int;

-- name: RevokeAPIToken :execresult
UPDATE access.api_token SET revoked_at = clock_timestamp()
WHERE id = sqlc.arg(id)::uuid AND revoked_at IS NULL;

-- name: RevokeAPITokenForPrincipal :execresult
UPDATE access.api_token SET revoked_at = clock_timestamp()
WHERE id = sqlc.arg(id)::uuid AND principal_id = sqlc.arg(principal_id)::uuid
  AND revoked_at IS NULL;

-- name: CreateServiceSecret :execresult
INSERT INTO access.service_principal_secret(id, service_principal_id, name, secret_fingerprint, verifier, expires_at)
SELECT sqlc.arg(id)::uuid, sqlc.arg(principal_id)::uuid, sqlc.arg(name),
       sqlc.arg(secret_fingerprint), sqlc.arg(verifier), sqlc.arg(expires_at)
WHERE sqlc.arg(expires_at)::timestamptz > clock_timestamp()
  AND sqlc.arg(expires_at)::timestamptz <= clock_timestamp() + interval '365 days'
  AND EXISTS (
      SELECT 1 FROM access.principal
      WHERE id = sqlc.arg(principal_id)::uuid AND principal_type = 'service'
        AND status = 'active' AND revoked_at IS NULL
        AND disabled_at IS NULL AND blocked_at IS NULL
  );

-- name: GetServiceSecret :one
SELECT id, service_principal_id, name, expires_at, created_at, revoked_at
FROM access.service_principal_secret
WHERE id = sqlc.arg(id)::uuid;

-- name: RevokeServiceSecret :execresult
UPDATE access.service_principal_secret SET revoked_at = clock_timestamp()
WHERE id = sqlc.arg(id)::uuid AND service_principal_id = sqlc.arg(principal_id)::uuid
  AND revoked_at IS NULL;

-- name: FindServiceSecretByFingerprint :one
SELECT s.service_principal_id, s.verifier
FROM access.service_principal_secret s
JOIN access.principal p ON p.id = s.service_principal_id
WHERE s.service_principal_id = sqlc.arg(principal_id)::uuid
  AND s.secret_fingerprint = sqlc.arg(secret_fingerprint)
  AND s.revoked_at IS NULL AND s.expires_at > clock_timestamp()
  AND p.status = 'active' AND p.revoked_at IS NULL
  AND p.disabled_at IS NULL AND p.blocked_at IS NULL;

-- name: FindPrincipalByEmail :one
SELECT id FROM access.principal
WHERE lower(email) = lower(sqlc.arg(email)::text) AND revoked_at IS NULL
ORDER BY created_at
LIMIT 1;

-- name: FindExternalIdentity :one
SELECT principal_id
FROM access.external_identity
WHERE provider = sqlc.arg(provider) AND tenant_id = sqlc.arg(tenant_id)
  AND subject = sqlc.arg(subject) AND revoked_at IS NULL
FOR UPDATE;

-- name: InsertExternalPrincipal :exec
INSERT INTO access.principal(id, principal_type, status, email, display_name)
VALUES (sqlc.arg(id)::uuid, 'user', 'active', sqlc.arg(email), sqlc.arg(display_name));

-- name: InsertExternalIdentity :exec
INSERT INTO access.external_identity(id, principal_id, provider, tenant_id, subject,
    user_name, external_id, email, display_name)
VALUES (sqlc.arg(id)::uuid, sqlc.arg(principal_id)::uuid, sqlc.arg(provider),
    sqlc.arg(tenant_id), sqlc.arg(subject), sqlc.arg(user_name), sqlc.arg(external_id),
    sqlc.arg(email), sqlc.arg(display_name));

-- name: UpdateExternalIdentity :exec
UPDATE access.external_identity
SET email = sqlc.arg(email), display_name = sqlc.arg(display_name),
    updated_at = clock_timestamp()
WHERE provider = sqlc.arg(provider) AND tenant_id = sqlc.arg(tenant_id)
  AND subject = sqlc.arg(subject) AND revoked_at IS NULL;

-- name: UpdateExternalPrincipal :exec
UPDATE access.principal
SET email = CASE WHEN sqlc.arg(email)::text = '' THEN email ELSE sqlc.arg(email)::text END,
    display_name = CASE WHEN sqlc.arg(display_name)::text = '' THEN display_name ELSE sqlc.arg(display_name)::text END,
    updated_at = clock_timestamp()
WHERE id = sqlc.arg(id)::uuid;

-- name: CreateDesktopSession :execresult
INSERT INTO access.session(id, principal_id, token_fingerprint, verifier, expires_at,
    absolute_expires_at, kind, instance_id, profile_id, client_id)
SELECT sqlc.arg(id)::uuid, sqlc.arg(principal_id)::uuid, sqlc.arg(token_fingerprint),
       sqlc.arg(verifier), LEAST(clock_timestamp() + sqlc.arg(ttl)::interval,
       clock_timestamp() + sqlc.arg(absolute_ttl)::interval),
       clock_timestamp() + sqlc.arg(absolute_ttl)::interval, 'desktop',
       sqlc.arg(instance_id), sqlc.arg(profile_id), 'leapview-desktop'
WHERE EXISTS (
    SELECT 1 FROM access.principal
    WHERE id = sqlc.arg(principal_id)::uuid AND status = 'active'
      AND disabled_at IS NULL AND blocked_at IS NULL
);

-- name: FindDesktopSession :one
SELECT s.id, s.principal_id, s.instance_id, s.profile_id, s.client_id, s.verifier
FROM access.session s
JOIN access.principal p ON p.id = s.principal_id
WHERE s.token_fingerprint = sqlc.arg(token_fingerprint)
  AND s.kind = 'desktop' AND s.revoked_at IS NULL
  AND s.expires_at > clock_timestamp()
  AND s.last_seen_at > clock_timestamp() - sqlc.arg(idle_ttl)::interval
  AND p.status = 'active' AND p.revoked_at IS NULL
  AND p.disabled_at IS NULL AND p.blocked_at IS NULL
FOR UPDATE;

-- name: TouchDesktopSession :one
UPDATE access.session
SET last_seen_at = clock_timestamp(),
    expires_at = LEAST(absolute_expires_at, clock_timestamp() + sqlc.arg(idle_ttl)::interval)
WHERE id = sqlc.arg(id)::uuid AND token_fingerprint = sqlc.arg(token_fingerprint)
  AND revoked_at IS NULL AND expires_at > clock_timestamp()
  AND last_seen_at > clock_timestamp() - sqlc.arg(idle_ttl_check)::interval
RETURNING expires_at, absolute_expires_at, created_at;

-- name: FindDesktopSessionForRevoke :one
SELECT s.id, s.verifier
FROM access.session s
JOIN access.principal p ON p.id = s.principal_id
WHERE s.token_fingerprint = sqlc.arg(token_fingerprint)
  AND s.kind = 'desktop' AND s.instance_id = sqlc.arg(instance_id)
  AND s.profile_id = sqlc.arg(profile_id) AND s.revoked_at IS NULL
  AND p.status = 'active' AND p.revoked_at IS NULL
  AND p.disabled_at IS NULL AND p.blocked_at IS NULL
FOR UPDATE;

-- name: RevokeDesktopSession :execresult
UPDATE access.session SET revoked_at = clock_timestamp()
WHERE id = sqlc.arg(id)::uuid AND revoked_at IS NULL;
