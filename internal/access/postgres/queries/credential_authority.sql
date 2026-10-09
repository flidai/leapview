-- name: LockCurrentCredentialPrincipal :one
SELECT id
FROM access.principal
WHERE id = sqlc.arg(id)::uuid AND status = 'active'
  AND revoked_at IS NULL AND disabled_at IS NULL AND blocked_at IS NULL
FOR SHARE;

-- name: LockCurrentPlatformAdministrator :one
SELECT b.id
FROM access.principal p
JOIN access.platform_role_binding b ON b.principal_id = p.id
WHERE p.id = sqlc.arg(id)::uuid
  AND p.status = 'active' AND p.revoked_at IS NULL
  AND p.disabled_at IS NULL AND p.blocked_at IS NULL
  AND b.role = 'platform_admin' AND b.revoked_at IS NULL
FOR SHARE OF p, b;
