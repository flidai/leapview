-- name: CountActiveCursorSigningKeys :one
SELECT count(*)
FROM platform.api_cursor_signing_keys
WHERE active AND verify_until IS NULL;

-- name: LockCursorSigningKeys :exec
LOCK TABLE platform.api_cursor_signing_keys IN SHARE ROW EXCLUSIVE MODE;

-- name: InsertActiveCursorSigningKey :exec
INSERT INTO platform.api_cursor_signing_keys (key_id, secret, active, created_at)
VALUES (sqlc.arg(key_id), sqlc.arg(secret), true, clock_timestamp());

-- name: RetireActiveCursorSigningKeys :exec
UPDATE platform.api_cursor_signing_keys
SET active = false, verify_until = clock_timestamp() + sqlc.arg(verification_interval)::interval
WHERE active;

-- name: ListVerifiableCursorSigningKeys :many
SELECT key_id, secret, active
FROM platform.api_cursor_signing_keys
WHERE verify_until IS NULL OR verify_until > clock_timestamp()
ORDER BY created_at, key_id;

-- name: PruneExpiredCursorSigningKeys :one
SELECT platform.prune_expired_cursor_signing_keys(sqlc.arg(p_limit));
