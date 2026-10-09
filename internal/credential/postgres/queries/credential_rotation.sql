-- name: IsCredentialRotationOperator :one
SELECT current_user = 'leapview_control_maintenance' AS allowed;

-- name: ListEnvelopeRewraps :many
SELECT v.version_id, v.deployment_id, v.owner_id, v.scope_kind, v.target_id,
       v.project_id, v.environment, v.resource_id, v.purpose, v.provider,
       v.destination, v.actor_id, v.created_at,
       e.format AS envelope_format, e.key_id AS envelope_key_id,
       e.ciphertext, e.envelope_revision
FROM credential.draft_version AS v
JOIN credential.envelope AS e USING(version_id,deployment_id)
WHERE v.deployment_id=sqlc.arg(deployment_id) AND e.key_id<>sqlc.arg(active_key_id)
ORDER BY v.version_id
LIMIT sqlc.arg(batch_size)::integer;

-- name: RewrapEnvelope :one
SELECT credential.rewrap_envelope(
    sqlc.arg(deployment_id)::text, sqlc.arg(version_id)::text,
    sqlc.arg(previous_revision)::bigint, sqlc.arg(previous_key_id)::text,
    sqlc.arg(key_id)::text, sqlc.arg(format)::text, sqlc.arg(ciphertext)::bytea,
    sqlc.arg(audit_id)::uuid, sqlc.arg(intent_digest)::text
)::bigint AS envelope_revision;
