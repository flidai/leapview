-- PostgreSQL OAuth persistence.  These statements are intentionally small,
-- deterministic DML/read leaves; transaction ownership and fosite replay
-- semantics remain in postgres_store.go.

-- name: CreateOAuthClient :exec
INSERT INTO access.oauth_client
    (id, name, redirect_uris, grant_types, response_types, scopes, audience,
     public_client, secret_hash, token_endpoint_auth_method, principal_id)
VALUES (sqlc.arg(id), sqlc.arg(name), sqlc.arg(redirect_uris)::jsonb,
        sqlc.arg(grant_types)::jsonb, sqlc.arg(response_types)::jsonb,
        sqlc.arg(scopes)::jsonb, sqlc.arg(audience)::jsonb,
        sqlc.arg(public_client), sqlc.arg(secret_hash),
        sqlc.arg(token_endpoint_auth_method), sqlc.narg(principal_id));

-- name: EnsureOAuthClient :exec
-- Infer the repeated id parameter as text once, then cast that text to the
-- principal UUID. PostgreSQL rejects one prepared parameter inferred as both
-- text and uuid.
INSERT INTO access.oauth_client
    (id, name, redirect_uris, grant_types, response_types, scopes, audience,
     public_client, token_endpoint_auth_method, principal_id)
VALUES (sqlc.arg(id)::text, sqlc.arg(name), sqlc.arg(redirect_uris)::jsonb,
        sqlc.arg(grant_types)::jsonb, sqlc.arg(response_types)::jsonb,
        sqlc.arg(scopes)::jsonb, sqlc.arg(audience)::jsonb,
        false, 'client_secret_post', (sqlc.arg(id)::text)::uuid)
ON CONFLICT (id) DO UPDATE
SET name = EXCLUDED.name,
    scopes = EXCLUDED.scopes,
    audience = EXCLUDED.audience,
    principal_id = EXCLUDED.principal_id,
    updated_at = clock_timestamp();

-- name: GetOAuthClient :one
SELECT id, name, redirect_uris, grant_types, response_types, scopes, audience,
       public_client, secret_hash, token_endpoint_auth_method,
       principal_id
FROM access.oauth_client
WHERE id = sqlc.arg(id);

-- name: GetOAuthClientName :one
SELECT name
FROM access.oauth_client
WHERE id = sqlc.arg(id);

-- name: GetClientAssertion :one
SELECT expires_at, expires_at > clock_timestamp() AS valid
FROM access.oauth_client_assertion
WHERE jti = sqlc.arg(jti);

-- name: DeleteClientAssertion :exec
DELETE FROM access.oauth_client_assertion
WHERE jti = sqlc.arg(jti);

-- name: InsertClientAssertion :exec
INSERT INTO access.oauth_client_assertion (jti, expires_at)
VALUES (sqlc.arg(jti), sqlc.arg(expires_at));

-- name: OAuthClock :one
SELECT CAST(clock_timestamp() AS timestamptz) AS now;

-- name: CreateOAuthSession :exec
INSERT INTO access.oauth_session
    (kind, signature, request_id, request_json, access_signature)
VALUES (sqlc.arg(kind), sqlc.arg(signature), sqlc.arg(request_id),
        sqlc.arg(request_json)::jsonb, sqlc.arg(access_signature));

-- name: DeleteExpiredOAuthSessions :exec
DELETE FROM access.oauth_session
WHERE created_at < sqlc.arg(created_at);

-- name: DeleteExpiredClientAssertions :exec
DELETE FROM access.oauth_client_assertion
WHERE expires_at < sqlc.arg(expires_at);

-- name: GetOAuthSession :one
SELECT request_json, active
FROM access.oauth_session
WHERE kind = sqlc.arg(kind) AND signature = sqlc.arg(signature);

-- name: DeleteOAuthSession :exec
DELETE FROM access.oauth_session
WHERE kind = sqlc.arg(kind) AND signature = sqlc.arg(signature);

-- name: InvalidateAuthorizeCode :execrows
UPDATE access.oauth_session
SET active = false
WHERE kind = sqlc.arg(kind) AND signature = sqlc.arg(signature) AND active = true;

-- name: RotateRefreshToken :one
WITH refresh AS (
    UPDATE access.oauth_session AS refresh_session
    SET active = false
    WHERE refresh_session.kind = sqlc.arg(kind)
      AND refresh_session.signature = sqlc.arg(signature)
      AND refresh_session.request_id = sqlc.arg(request_id)
      AND refresh_session.active = true
    RETURNING refresh_session.request_id
), access_tokens AS (
    UPDATE access.oauth_session AS access_session
    SET active = false
    FROM refresh
    WHERE access_session.kind = sqlc.arg(access_kind)
      AND access_session.request_id = refresh.request_id
    RETURNING access_session.signature
)
SELECT count(*)::bigint AS refreshed
FROM refresh;

-- name: RevokeRefreshToken :one
WITH refresh AS (
    UPDATE access.oauth_session AS refresh_session
    SET active = false
    WHERE refresh_session.kind = sqlc.arg(kind)
      AND refresh_session.request_id = sqlc.arg(request_id)
      AND refresh_session.active = true
    RETURNING refresh_session.request_id
), access_tokens AS (
    UPDATE access.oauth_session AS access_session
    SET active = false
    FROM refresh
    WHERE access_session.kind = sqlc.arg(access_kind)
      AND access_session.request_id = refresh.request_id
    RETURNING access_session.signature
)
SELECT count(*)::bigint AS revoked
FROM refresh;

-- name: RevokeAccessToken :execrows
UPDATE access.oauth_session
SET active = false
WHERE kind = sqlc.arg(kind) AND request_id = sqlc.arg(request_id) AND active = true;
