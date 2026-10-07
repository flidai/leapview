-- Source lifecycle facts and restored-principal checks. Transaction control
-- and the SECURITY DEFINER append are owned by lifecycle.go.

-- name: LifecycleActionByOccurrence :one
SELECT sequence, occurrence_id, customer_id, deployment_id, resource_id,
       store, action, source_revision, completed, occurred_at
FROM access.lifecycle_action
WHERE occurrence_id = sqlc.arg(occurrence_id)::uuid;

-- name: LifecycleAuthorityID :one
SELECT authority_id FROM access.lifecycle_authority WHERE singleton;

-- name: LifecycleHighWater :one
SELECT COALESCE(MAX(sequence), 0)::bigint FROM access.lifecycle_action;

-- name: PostFrontierLifecycleActions :many
SELECT sequence, occurrence_id, customer_id, deployment_id, resource_id,
       store, action, source_revision, completed, occurred_at
FROM access.lifecycle_action
WHERE deployment_id = sqlc.arg(deployment_id) AND sequence > sqlc.arg(last_sequence)::bigint
ORDER BY sequence LIMIT 10001;

-- name: LockLifecyclePrincipal :one
SELECT status, disabled_at, revoked_at
FROM access.principal WHERE id = sqlc.arg(id)::uuid FOR UPDATE;

-- name: PrincipalHasActiveCredential :one
SELECT EXISTS (
    SELECT 1 FROM access.session WHERE principal_id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL
    UNION ALL SELECT 1 FROM access.api_token WHERE principal_id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL
    UNION ALL SELECT 1 FROM access.service_principal_secret WHERE service_principal_id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL
    UNION ALL SELECT 1 FROM access.authoring_session WHERE principal_id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL
    UNION ALL SELECT 1 FROM access.oauth_session WHERE active AND request_json->'session'->>'subject' = (sqlc.arg(principal_id)::uuid)::text
);
