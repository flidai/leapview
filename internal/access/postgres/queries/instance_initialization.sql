-- name: LockProjectClaimPublisher :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(claim_credential_id)::text, 0));

-- name: RevokeNamedProjectClaimPublishers :many
WITH revoked AS (
    UPDATE access.api_token
    SET revoked_at = clock_timestamp()
    WHERE principal_id = sqlc.arg(principal_id)::uuid
      AND name = sqlc.arg(token_name)
      AND revoked_at IS NULL
    RETURNING id
)
SELECT id FROM revoked ORDER BY id;
