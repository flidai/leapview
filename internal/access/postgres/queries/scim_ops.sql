-- Stable SCIM PostgreSQL leaves. Transaction ownership and reconciliation
-- ordering remain in scim.go.

-- name: LockSCIMSubject :exec
SELECT pg_advisory_xact_lock(hashtextextended('leapview:scim:' || sqlc.arg(subject)::text, 0));

-- name: LockSCIMGroupExternal :exec
SELECT pg_advisory_xact_lock(hashtextextended('leapview:scim-group:' || sqlc.arg(external_id)::text, 0));

-- name: GetPrincipalIdentityManagement :one
SELECT (COALESCE((SELECT provider::text
                  FROM access.external_identity
                  WHERE principal_id = p.id AND revoked_at IS NULL
                  ORDER BY created_at LIMIT 1), ''::text))::text AS provider,
       p.principal_type,
       EXISTS (SELECT 1 FROM access.local_credential c
               WHERE c.principal_id = p.id AND c.revoked_at IS NULL) AS has_local_password
FROM access.principal p
WHERE p.id = sqlc.arg(principal_id)::uuid;

-- name: FindSCIMPrincipalBySubject :one
SELECT principal_id
FROM access.external_identity
WHERE provider = 'scim' AND tenant_id = ''
  AND subject = sqlc.arg(subject) AND revoked_at IS NULL
FOR UPDATE;

-- name: InsertSCIMPrincipal :exec
INSERT INTO access.principal(id, principal_type, status, email, display_name)
VALUES (sqlc.arg(id)::uuid, 'user', sqlc.arg(status), sqlc.arg(email), sqlc.arg(display_name));

-- name: InsertSCIMExternalIdentity :exec
INSERT INTO access.external_identity
    (id, principal_id, provider, tenant_id, subject, user_name, external_id, email, display_name)
VALUES (sqlc.arg(id)::uuid, sqlc.arg(principal_id)::uuid, 'scim', '', sqlc.arg(subject),
        sqlc.arg(user_name), sqlc.arg(external_id), sqlc.arg(email), sqlc.arg(display_name));

-- name: UpdateSCIMExternalIdentity :exec
UPDATE access.external_identity
SET user_name = sqlc.arg(user_name),
    external_id = sqlc.arg(external_id),
    email = sqlc.arg(email),
    display_name = sqlc.arg(display_name),
    updated_at = clock_timestamp()
WHERE provider = 'scim' AND tenant_id = ''
  AND subject = sqlc.arg(subject) AND revoked_at IS NULL;

-- name: UpdateSCIMPrincipal :exec
UPDATE access.principal
SET status = sqlc.arg(status),
    email = CASE WHEN sqlc.arg(email)::text = '' THEN email ELSE sqlc.arg(email)::text END,
    display_name = CASE WHEN sqlc.arg(display_name)::text = '' THEN display_name ELSE sqlc.arg(display_name)::text END,
    disabled_at = CASE WHEN sqlc.arg(status) = 'disabled'
                       THEN COALESCE(disabled_at, clock_timestamp())
                       ELSE NULL END,
    updated_at = clock_timestamp()
WHERE id = sqlc.arg(id)::uuid;

-- name: ListSCIMUsers :many
SELECT p.id, p.principal_type, p.status, COALESCE(p.email, '') AS email,
       p.display_name, p.disabled_at, p.blocked_at, p.last_seen_at,
       p.created_at, p.updated_at, ei.external_id
FROM access.external_identity ei
JOIN access.principal p ON p.id = ei.principal_id
WHERE ei.provider = 'scim' AND ei.revoked_at IS NULL
  AND (sqlc.arg(id)::text = '' OR p.id::text = sqlc.arg(id)::text)
  AND (sqlc.arg(external_id)::text = '' OR ei.external_id = sqlc.arg(external_id)::text)
  AND (sqlc.arg(user_name)::text = '' OR ei.user_name = sqlc.arg(user_name)::text)
ORDER BY p.created_at
LIMIT sqlc.arg(page_size)::int;

-- name: HasSCIMIdentity :one
SELECT EXISTS (
    SELECT 1 FROM access.external_identity
    WHERE principal_id = sqlc.arg(principal_id)::uuid
      AND provider = 'scim' AND revoked_at IS NULL
);

-- name: RevokePrincipalLocalCredential :exec
UPDATE access.local_credential
SET revoked_at = clock_timestamp()
WHERE principal_id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL;

-- name: FindSCIMGroupByExternal :one
SELECT id
FROM access.access_group
WHERE provider = 'scim' AND external_id = sqlc.arg(external_id)
  AND revoked_at IS NULL
FOR UPDATE;

-- name: InsertSCIMGroup :exec
INSERT INTO access.access_group(id, name, provider, external_id)
VALUES (sqlc.arg(id)::uuid, sqlc.arg(name), 'scim', sqlc.arg(external_id));

-- name: UpdateSCIMGroup :exec
UPDATE access.access_group
SET name = sqlc.arg(name)
WHERE id = sqlc.arg(id)::uuid AND provider = 'scim' AND revoked_at IS NULL;

-- name: RevokeSCIMGroupMembersExcept :exec
UPDATE access.principal_group
SET revoked_at = clock_timestamp()
WHERE group_id = sqlc.arg(group_id)::uuid AND revoked_at IS NULL
  AND NOT (principal_id = ANY(sqlc.arg(member_ids)::uuid[]));

-- name: ListSCIMGroups :many
SELECT id, provider, external_id, name, created_at
FROM access.access_group
WHERE provider = 'scim' AND revoked_at IS NULL
  AND (sqlc.arg(id)::text = '' OR id::text = sqlc.arg(id)::text)
  AND (sqlc.arg(external_id)::text = '' OR external_id = sqlc.arg(external_id)::text)
  AND (sqlc.arg(name)::text = '' OR name ILIKE '%' || sqlc.arg(name)::text || '%')
ORDER BY name
LIMIT sqlc.arg(page_size)::int;

-- name: RevokeSCIMGroup :execresult
UPDATE access.access_group
SET revoked_at = COALESCE(revoked_at, clock_timestamp())
WHERE id = sqlc.arg(id)::uuid AND provider = 'scim' AND revoked_at IS NULL;

-- name: GetSCIMGroupProvider :one
SELECT provider
FROM access.access_group
WHERE id = sqlc.arg(id)::uuid AND revoked_at IS NULL;
