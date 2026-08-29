-- name: GetPrincipal :one
SELECT id,
       principal_type,
       status,
       COALESCE(email, '') AS email,
       display_name,
       disabled_at,
       blocked_at,
       last_seen_at,
       created_at,
       updated_at
FROM access.principal
WHERE id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL;

-- name: ListPrincipals :many
SELECT id,
       principal_type,
       status,
       COALESCE(email, '') AS email,
       display_name,
       disabled_at,
       blocked_at,
       last_seen_at,
       created_at,
       updated_at
FROM access.principal
WHERE revoked_at IS NULL
  AND (sqlc.arg(email)::text = '' OR lower(email) = lower(sqlc.arg(email)::text))
  AND (sqlc.arg(query)::text = '' OR email ILIKE '%' || sqlc.arg(query)::text || '%' OR display_name ILIKE '%' || sqlc.arg(query)::text || '%')
ORDER BY created_at DESC
LIMIT sqlc.arg(page_size)::int;
