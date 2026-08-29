-- Named audit persistence and bounded read leaves. Canonicalization,
-- validation, replay comparison, and transaction ownership remain in the Go
-- audit adapters.

-- name: InsertAuditIntent :exec
INSERT INTO audit.audit_event
    (audit_id, scope_id, principal_id, source, operation, action,
     resource_kind, resource_id, capability, outcome, request_id,
     correlation_id, aggregate_key, aggregate_sequence, intent_digest,
     metadata)
VALUES (sqlc.arg(audit_id)::uuid, NULLIF(sqlc.arg(scope_id)::text, ''),
        NULLIF(sqlc.arg(principal_id)::text, '')::uuid, sqlc.arg(source),
        sqlc.arg(operation), sqlc.arg(action), NULLIF(sqlc.arg(resource_kind)::text, ''),
        NULLIF(sqlc.arg(resource_id)::text, ''), sqlc.arg(capability), sqlc.arg(outcome),
        NULLIF(sqlc.arg(request_id)::text, '')::uuid, NULLIF(sqlc.arg(correlation_id)::text, '')::uuid,
        sqlc.arg(aggregate_key), sqlc.arg(aggregate_sequence), sqlc.arg(intent_digest),
        sqlc.arg(metadata)::jsonb)
ON CONFLICT (audit_id) DO NOTHING;

-- name: GetAuditIntent :one
SELECT audit_id::text AS audit_id,
       COALESCE(scope_id, '') AS scope_id,
       principal_id,
       source, operation, action, COALESCE(resource_kind, '') AS resource_kind,
       COALESCE(resource_id, '') AS resource_id, capability, outcome,
       request_id, correlation_id,
       aggregate_key, aggregate_sequence, intent_digest, metadata::text AS metadata_json,
       metadata = sqlc.arg(metadata)::jsonb AS metadata_equal, occurred_at
FROM audit.audit_event
WHERE audit_id = sqlc.arg(audit_id)::uuid;

-- name: GetAuditIntentByID :one
SELECT audit_id::text AS audit_id,
       COALESCE(scope_id, '') AS scope_id,
       principal_id,
       source, operation, action, COALESCE(resource_kind, '') AS resource_kind,
       COALESCE(resource_id, '') AS resource_id, capability, outcome,
       request_id, correlation_id,
       aggregate_key, aggregate_sequence, intent_digest, metadata::text AS metadata_json,
       occurred_at
FROM audit.audit_event
WHERE audit_id = sqlc.arg(audit_id)::uuid;

-- name: ListAuditIntents :many
SELECT audit_id::text AS audit_id,
       COALESCE(scope_id, '') AS scope_id,
       principal_id,
       source, operation, action, COALESCE(resource_kind, '') AS resource_kind,
       COALESCE(resource_id, '') AS resource_id, capability, outcome,
       request_id, correlation_id,
       aggregate_key, aggregate_sequence, intent_digest, metadata::text AS metadata_json,
       occurred_at
FROM audit.audit_event
ORDER BY occurred_at DESC, audit_id DESC
LIMIT sqlc.arg(page_size)::int;

-- name: InsertAccessAuditEvent :exec
INSERT INTO audit.audit_event
    (audit_id, principal_id, source, operation, action, resource_kind,
     resource_id, capability, outcome, request_id, correlation_id,
     aggregate_key, aggregate_sequence, intent_digest, metadata)
VALUES (sqlc.arg(audit_id)::uuid, NULLIF(sqlc.arg(principal_id)::text, '')::uuid,
        'access', 'repository', sqlc.arg(action), sqlc.arg(resource_kind),
        sqlc.arg(resource_id), sqlc.arg(capability),
        CASE WHEN sqlc.arg(status)::text = '' THEN 'success' ELSE sqlc.arg(status)::text END,
        NULLIF(sqlc.arg(request_id)::text, '')::uuid, NULLIF(sqlc.arg(correlation_id)::text, '')::uuid,
        sqlc.arg(aggregate_key), 0, sqlc.arg(intent_digest), sqlc.arg(metadata)::jsonb);

-- name: ListAccessAuditEvents :many
SELECT audit_id::text AS audit_id,
       principal_id,
       action, COALESCE(resource_kind, '') AS resource_kind,
       COALESCE(resource_id, '') AS resource_id, capability, outcome,
       request_id, correlation_id,
       metadata::text AS metadata_json, occurred_at
FROM audit.audit_event
WHERE (sqlc.arg(principal_id)::text = '' OR principal_id = sqlc.arg(principal_id)::uuid)
  AND (sqlc.arg(action)::text = '' OR action = sqlc.arg(action)::text)
  AND (sqlc.arg(resource_kind)::text = '' OR resource_kind = sqlc.arg(resource_kind)::text)
  AND (sqlc.arg(resource_id)::text = '' OR resource_id = sqlc.arg(resource_id)::text)
  AND (sqlc.arg(capability)::text = '' OR capability = sqlc.arg(capability)::text)
  AND (sqlc.arg(from_time)::text = '' OR occurred_at >= sqlc.arg(from_time)::timestamptz)
  AND (sqlc.arg(to_time)::text = '' OR occurred_at < sqlc.arg(to_time)::timestamptz)
  AND (sqlc.arg(cursor_time)::text = '' OR
       (occurred_at, audit_id) < (sqlc.arg(cursor_time)::timestamptz, sqlc.arg(cursor_id)::uuid))
ORDER BY occurred_at DESC, audit_id DESC
LIMIT sqlc.arg(page_size)::int;

-- name: HasBootstrapAPITokenEvidence :one
SELECT EXISTS (
    SELECT 1
    FROM access.api_token t
    JOIN access.principal p ON p.id = t.principal_id
    JOIN access.platform_role_binding b ON b.principal_id = p.id
    WHERE t.id = sqlc.arg(token_id)::uuid
      AND t.principal_id = sqlc.arg(principal_id)::uuid
      AND t.revoked_at IS NULL AND t.expires_at > clock_timestamp()
      AND p.status = 'active' AND p.revoked_at IS NULL
      AND p.disabled_at IS NULL AND p.blocked_at IS NULL
      AND b.role = 'platform_admin' AND b.revoked_at IS NULL
);
