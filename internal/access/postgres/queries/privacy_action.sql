-- FAI-966 execution leaves. The principal UUID, never an email/name match,
-- is the only discovery join key. OAuth signatures are hashed before they
-- leave PostgreSQL so the manifest never exposes bearer material.

-- name: GetPrivacyInstanceIdentity :one
SELECT instance_id FROM platform.instance_identity WHERE singleton_id = 1;

-- name: LockPrivacyPrincipal :one
SELECT principal_type, status, revoked_at
FROM access.principal WHERE id = sqlc.arg(principal_id)::uuid FOR UPDATE NOWAIT;

-- name: ListPrivacySubjectRecords :many
WITH records AS (
    SELECT 'access.principal'::text AS store, id::text AS record_id, true AS actionable
      FROM access.principal WHERE id = sqlc.arg(principal_id)::uuid
    UNION ALL SELECT 'access.external_identity', id::text, false
      FROM access.external_identity WHERE principal_id = sqlc.arg(principal_id)::uuid
    UNION ALL SELECT 'access.platform_role_binding', id::text, true
      FROM access.platform_role_binding WHERE principal_id = sqlc.arg(principal_id)::uuid
    UNION ALL SELECT 'access.principal_group', membership_id::text, true
      FROM access.principal_group WHERE principal_id = sqlc.arg(principal_id)::uuid
    UNION ALL SELECT 'access.session', id::text, true
      FROM access.session WHERE principal_id = sqlc.arg(principal_id)::uuid
    UNION ALL SELECT 'access.local_credential', principal_id::text, true
      FROM access.local_credential WHERE principal_id = sqlc.arg(principal_id)::uuid
    UNION ALL SELECT 'access.api_token', id::text, true
      FROM access.api_token WHERE principal_id = sqlc.arg(principal_id)::uuid
    UNION ALL SELECT 'access.service_principal_secret', id::text, true
      FROM access.service_principal_secret WHERE service_principal_id = sqlc.arg(principal_id)::uuid
    UNION ALL SELECT 'access.authoring_session', id, true
      FROM access.authoring_session WHERE principal_id = sqlc.arg(principal_id)::uuid
    UNION ALL SELECT 'access.authoring_credential', c.id, false
      FROM access.authoring_credential c JOIN access.authoring_session s ON s.id = c.session_id
     WHERE s.principal_id = sqlc.arg(principal_id)::uuid
    UNION ALL SELECT 'access.oauth_session', kind || ':' || encode(sha256(convert_to(signature, 'UTF8')), 'hex'), true
      FROM access.oauth_session WHERE request_json->'session'->>'subject' = sqlc.arg(principal_id)::text
    UNION ALL SELECT 'access.oauth_client', id, false
      FROM access.oauth_client WHERE principal_id = sqlc.arg(principal_id)::uuid
    UNION ALL SELECT 'access.principal_preferences', preference_id::text, false
      FROM access.principal_preferences WHERE principal_id = sqlc.arg(principal_id)::uuid
    UNION ALL SELECT 'access.principal_avatar', avatar_id::text, false
      FROM access.principal_avatar WHERE principal_id = sqlc.arg(principal_id)::uuid
)
SELECT store, record_id, actionable FROM records
WHERE (store, record_id) > (sqlc.arg(after_store)::text, sqlc.arg(after_record_id)::text)
ORDER BY store, record_id LIMIT sqlc.arg(page_size)::int;

-- name: InsertPrivacyActionRun :execresult
INSERT INTO access.privacy_action_run
  (run_id, customer_id, deployment_id, case_id, correlation_id, actor_id,
   principal_id, principal_type, action, graph_digest, manifest_digest)
VALUES (sqlc.arg(run_id)::uuid, sqlc.arg(customer_id), sqlc.arg(deployment_id),
        sqlc.arg(case_id), sqlc.arg(correlation_id), sqlc.arg(actor_id)::uuid,
        sqlc.arg(principal_id)::uuid, sqlc.arg(principal_type), sqlc.arg(action),
        sqlc.arg(graph_digest), sqlc.arg(manifest_digest))
ON CONFLICT (customer_id, deployment_id, case_id, action) DO NOTHING;

-- name: LockPrivacyActionRun :one
SELECT run_id, customer_id, deployment_id, case_id, correlation_id, actor_id,
       principal_id, principal_type, action, graph_digest, manifest_digest,
       status, cursor, created_at, updated_at, completed_at
FROM access.privacy_action_run
WHERE customer_id = sqlc.arg(customer_id) AND deployment_id = sqlc.arg(deployment_id)
  AND case_id = sqlc.arg(case_id) AND action = sqlc.arg(action)
FOR UPDATE NOWAIT;

-- name: InsertPrivacyActionItem :exec
INSERT INTO access.privacy_action_item(run_id, ordinal, store, record_id, actionable, status)
VALUES (sqlc.arg(run_id)::uuid, sqlc.arg(ordinal), sqlc.arg(store),
        sqlc.arg(record_id), sqlc.arg(actionable), sqlc.arg(status));

-- name: ListPendingPrivacyActionItems :many
SELECT ordinal, store, record_id
FROM access.privacy_action_item
WHERE run_id = sqlc.arg(run_id)::uuid AND status = 'pending'
ORDER BY CASE WHEN store = 'access.principal' THEN 0 ELSE 1 END, ordinal
LIMIT sqlc.arg(page_size)::int FOR UPDATE NOWAIT;

-- name: CompletePrivacyActionItem :execresult
UPDATE access.privacy_action_item
SET status = 'completed', outcome = sqlc.arg(outcome), completed_at = clock_timestamp()
WHERE run_id = sqlc.arg(run_id)::uuid AND ordinal = sqlc.arg(ordinal) AND status = 'pending';

-- name: AdvancePrivacyActionRun :exec
UPDATE access.privacy_action_run
SET cursor = sqlc.arg(cursor), updated_at = clock_timestamp()
WHERE run_id = sqlc.arg(run_id)::uuid AND status = 'pending' AND cursor < sqlc.arg(cursor);

-- name: CompletePrivacyActionRun :execresult
UPDATE access.privacy_action_run
SET status = 'completed', completed_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE run_id = sqlc.arg(run_id)::uuid AND status = 'pending'
  AND NOT EXISTS (SELECT 1 FROM access.privacy_action_item WHERE run_id = sqlc.arg(run_id)::uuid AND status = 'pending');

-- name: ListPrivacyActionStoreResults :many
SELECT store, status, outcome, count(*)::bigint AS item_count
FROM access.privacy_action_item WHERE run_id = sqlc.arg(run_id)::uuid
GROUP BY store, status, outcome ORDER BY store, status, outcome;

-- name: RevokePrivacyGroupMembership :execresult
UPDATE access.principal_group SET revoked_at = clock_timestamp()
WHERE membership_id = sqlc.arg(membership_id)::uuid AND principal_id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL;

-- name: RevokePrivacyPlatformRole :execresult
UPDATE access.platform_role_binding SET revoked_at = clock_timestamp()
WHERE id = sqlc.arg(role_id)::uuid AND principal_id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL;

-- name: RevokePrivacyOAuthSession :execresult
UPDATE access.oauth_session SET active = false
WHERE kind = sqlc.arg(kind)
  AND encode(sha256(convert_to(signature, 'UTF8')), 'hex') = sqlc.arg(signature_digest)
  AND request_json->'session'->>'subject' = sqlc.arg(principal_id)::text
  AND active = true;

-- name: RevokePrivacyAuthoringSession :one
WITH revoked AS (
    UPDATE access.authoring_session AS s SET revoked_at = clock_timestamp()
    WHERE s.id = sqlc.arg(session_id) AND s.principal_id = sqlc.arg(principal_id)::uuid
      AND s.revoked_at IS NULL
    RETURNING s.id
), credentials AS (
    UPDATE access.authoring_credential SET active = false
    WHERE session_id IN (SELECT id FROM revoked) AND active = true
)
SELECT count(*)::bigint FROM revoked;
