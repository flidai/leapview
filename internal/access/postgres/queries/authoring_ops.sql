-- Named SQLC leaves for device authorization and authoring credentials.

-- name: InsertDeviceAuthorization :exec
WITH db_now AS (SELECT clock_timestamp() AS ts)
INSERT INTO access.device_authorization
    (id, client_id, device_code_hash, user_code_hash, target_id, project_id,
     capabilities, status, expires_at, created_at, poll_interval_seconds)
SELECT sqlc.arg(id), sqlc.arg(client_id), sqlc.arg(device_code_hash), sqlc.arg(user_code_hash),
       sqlc.arg(target_id), sqlc.arg(project_id), sqlc.arg(capabilities)::jsonb, sqlc.arg(status),
       db_now.ts + sqlc.arg(ttl)::interval, db_now.ts, sqlc.arg(poll_interval_seconds)
FROM db_now;

-- name: GetDeviceAuthorizationByUserCodeHash :one
SELECT id, client_id, device_code_hash, user_code_hash, target_id, project_id,
       capabilities, status, principal_id, expires_at, poll_interval_seconds,
       last_polled_at, created_at, approved_at, denied_at, consumed_at
FROM access.device_authorization
WHERE user_code_hash = sqlc.arg(user_code_hash);

-- name: LockDeviceAuthorizationByDeviceCodeHash :one
SELECT id, client_id, device_code_hash, user_code_hash, target_id, project_id,
       capabilities, status, principal_id, expires_at, poll_interval_seconds,
       last_polled_at, created_at, approved_at, denied_at, consumed_at
FROM access.device_authorization
WHERE device_code_hash = sqlc.arg(device_code_hash)
FOR UPDATE;

-- name: DatabaseNow :one
SELECT (extract(epoch FROM clock_timestamp()) * 1000000)::bigint AS now_epoch_micros;

-- name: DatabaseNowPlus24Hours :one
SELECT (extract(epoch FROM (clock_timestamp() + interval '24 hours')) * 1000000)::bigint AS expires_epoch_micros;

-- name: ApproveDeviceAuthorization :execresult
UPDATE access.device_authorization
SET status = 'approved', principal_id = sqlc.arg(principal_id)::uuid,
    approved_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND status = 'pending' AND expires_at > clock_timestamp();

-- name: DenyDeviceAuthorization :execresult
UPDATE access.device_authorization
SET status = 'denied', principal_id = sqlc.arg(principal_id)::uuid,
    denied_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND status = 'pending' AND expires_at > clock_timestamp();

-- name: TouchDeviceAuthorizationPoll :exec
UPDATE access.device_authorization
SET last_polled_at = clock_timestamp()
WHERE id = sqlc.arg(id);

-- name: GetActiveAuthoringPrincipal :one
SELECT id, principal_type, status, COALESCE(email, '') AS email, display_name,
       disabled_at, blocked_at, last_seen_at, created_at, updated_at
FROM access.principal
WHERE id = sqlc.arg(id)::uuid AND status = 'active' AND revoked_at IS NULL
  AND disabled_at IS NULL AND blocked_at IS NULL
FOR SHARE;

-- name: GetActiveServiceAuthoringPrincipal :one
SELECT id, principal_type, status, COALESCE(email, '') AS email, display_name,
       disabled_at, blocked_at, last_seen_at, created_at, updated_at
FROM access.principal
WHERE id = sqlc.arg(id)::uuid AND principal_type = 'service'
  AND status = 'active' AND revoked_at IS NULL
  AND disabled_at IS NULL AND blocked_at IS NULL
FOR SHARE;

-- name: ConsumeDeviceAuthorization :execresult
UPDATE access.device_authorization
SET status = 'consumed', consumed_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND status = 'approved' AND expires_at > clock_timestamp();

-- name: InsertAuthoringSession :exec
INSERT INTO access.authoring_session
    (id, kind, client_id, principal_id, target_id, project_id, capabilities, created_at, expires_at)
VALUES (sqlc.arg(id), sqlc.arg(kind), sqlc.arg(client_id), sqlc.arg(principal_id)::uuid,
        sqlc.arg(target_id), sqlc.arg(project_id), sqlc.arg(capabilities)::jsonb,
        clock_timestamp(), sqlc.arg(expires_at));

-- name: InsertAuthoringCredential :exec
INSERT INTO access.authoring_credential
    (id, session_id, access_token_hash, refresh_token_hash,
     access_expires_at, refresh_expires_at)
VALUES (sqlc.arg(id), sqlc.arg(session_id), sqlc.arg(access_token_hash),
        sqlc.narg(refresh_token_hash), sqlc.arg(access_expires_at), sqlc.narg(refresh_expires_at));

-- name: GetAuthoringSessionCreatedAt :one
SELECT created_at FROM access.authoring_session WHERE id = sqlc.arg(id);

-- name: LockAuthoringCredentialForRotation :one
SELECT c.id, c.session_id, c.access_expires_at,
       COALESCE(c.refresh_expires_at, 'epoch'::timestamptz) AS refresh_expires_at,
       c.active, s.principal_id, s.kind, s.client_id, s.target_id, s.project_id,
       s.capabilities, s.expires_at, s.created_at, s.revoked_at
FROM access.authoring_credential c
JOIN access.authoring_session s ON s.id = c.session_id
WHERE c.refresh_token_hash = sqlc.arg(refresh_token_hash)
FOR UPDATE;

-- name: LookupAuthoringPrincipalIDForRotation :one
SELECT s.principal_id
FROM access.authoring_credential c
JOIN access.authoring_session s ON s.id = c.session_id
WHERE c.refresh_token_hash = sqlc.arg(refresh_token_hash);

-- name: RevokeAuthoringSession :execresult
UPDATE access.authoring_session
SET revoked_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND principal_id = sqlc.arg(principal_id)::uuid
  AND revoked_at IS NULL;

-- name: RevokePrincipalAuthoringSessions :exec
UPDATE access.authoring_session
SET revoked_at = clock_timestamp()
WHERE principal_id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL;

-- name: RevokeAuthoringSessionByAccessTokenHash :execresult
UPDATE access.authoring_session
SET revoked_at = clock_timestamp()
WHERE id = (SELECT session_id FROM access.authoring_credential WHERE access_token_hash = sqlc.arg(access_token_hash))
  AND revoked_at IS NULL;

-- name: MarkAuthoringCredentialInactive :exec
UPDATE access.authoring_credential
SET active = false, replaced_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND active;

-- name: ExtendAuthoringSession :execresult
UPDATE access.authoring_session
SET expires_at = sqlc.arg(expires_at)
WHERE id = sqlc.arg(id) AND revoked_at IS NULL
  AND sqlc.arg(expires_at) > clock_timestamp();

-- name: InsertRotatedAuthoringCredential :exec
INSERT INTO access.authoring_credential
    (id, session_id, access_token_hash, refresh_token_hash,
     access_expires_at, refresh_expires_at)
VALUES (sqlc.arg(id), sqlc.arg(session_id), sqlc.arg(access_token_hash),
        sqlc.arg(refresh_token_hash), sqlc.arg(access_expires_at), sqlc.arg(refresh_expires_at));

-- name: GetAuthoringCredentialByAccessTokenHash :one
SELECT c.id, c.session_id, s.principal_id, s.kind, s.client_id, s.target_id,
       s.project_id, s.capabilities, c.access_expires_at, s.expires_at
FROM access.authoring_credential c
JOIN access.authoring_session s ON s.id = c.session_id
JOIN access.principal p ON p.id = s.principal_id
WHERE c.access_token_hash = sqlc.arg(access_token_hash) AND c.active
  AND c.access_expires_at > clock_timestamp() AND s.expires_at > clock_timestamp()
  AND s.revoked_at IS NULL AND p.status = 'active' AND p.revoked_at IS NULL
  AND p.disabled_at IS NULL AND p.blocked_at IS NULL;

-- name: TouchAuthoringSession :exec
UPDATE access.authoring_session
SET last_used_at = clock_timestamp()
WHERE id = sqlc.arg(id);

-- name: ListAuthoringSessions :many
SELECT id, kind, client_id, target_id, project_id, capabilities,
       created_at, last_used_at, expires_at, revoked_at
FROM access.authoring_session
WHERE principal_id = sqlc.arg(principal_id)::uuid
ORDER BY created_at DESC
LIMIT sqlc.arg(page_size)::int;
