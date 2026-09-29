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

-- name: CreateInitialPasswordSetup :exec
INSERT INTO access.initial_password_setup (claim_credential_id, principal_id, instance_id)
VALUES (sqlc.arg(claim_credential_id), sqlc.arg(principal_id), sqlc.arg(instance_id));

-- name: CloseInitialPasswordSetup :exec
UPDATE access.initial_password_setup SET closed_at = clock_timestamp()
WHERE principal_id = sqlc.arg(principal_id) AND closed_at IS NULL;

-- name: RecordInitialPublisherOrigin :exec
INSERT INTO access.initial_publisher_origin (publisher_credential_id, claim_credential_id, project_id)
SELECT sqlc.arg(publisher_credential_id)::uuid, s.claim_credential_id, sqlc.arg(project_id)::text
FROM access.initial_password_setup s
WHERE s.claim_credential_id = sqlc.arg(claim_credential_id)
  AND s.principal_id = sqlc.arg(principal_id) AND s.instance_id = sqlc.arg(instance_id);

-- name: InitialPublisherOrigin :one
SELECT o.claim_credential_id, s.principal_id, s.instance_id, o.project_id, s.closed_at
FROM access.initial_publisher_origin o
JOIN access.initial_password_setup s USING (claim_credential_id)
JOIN access.api_token publisher ON publisher.id = o.publisher_credential_id AND publisher.principal_id = s.principal_id
WHERE o.publisher_credential_id = sqlc.arg(publisher_credential_id)
  AND s.principal_id = sqlc.arg(principal_id);

-- name: HasInitialPublisherOrigin :one
SELECT EXISTS (SELECT 1 FROM access.initial_publisher_origin WHERE publisher_credential_id = sqlc.arg(publisher_credential_id));
