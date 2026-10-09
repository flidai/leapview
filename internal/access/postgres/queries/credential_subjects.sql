-- name: LockCredentialAuthorizationGroups :many
SELECT pg.group_id
FROM access.principal_group pg
JOIN access.access_group g ON g.id = pg.group_id
WHERE pg.principal_id = sqlc.arg(principal_id)::uuid
  AND pg.revoked_at IS NULL AND g.revoked_at IS NULL
ORDER BY pg.group_id
FOR SHARE OF pg, g;
