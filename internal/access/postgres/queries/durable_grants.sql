-- Durable grants are independent of serving generations. Domain validation,
-- transaction ownership, idempotency conflict mapping, and conversion remain
-- in durable_grants.go.

-- name: InsertResourceShareGrant :execresult
INSERT INTO access.resource_share_grant
    (id, profile, instance_id, project_id, resource_uid, resource_id, resource_kind,
     issuer_principal_id, issuer_credential_class, issuer_credential_id, issuer_credential_fingerprint,
     recipient_kind, recipient_id, permission_profile, permissions, issued_at, expires_at,
     fingerprint, idempotency_key, request_digest, allow_onward_delegation)
VALUES (sqlc.arg(id)::text, sqlc.arg(profile)::text, sqlc.arg(instance_id)::text,
        sqlc.arg(project_id)::text, sqlc.arg(resource_uid)::uuid, sqlc.arg(resource_id)::text,
        sqlc.arg(resource_kind)::text, sqlc.arg(issuer_principal_id)::uuid,
        sqlc.arg(issuer_credential_class)::text, sqlc.arg(issuer_credential_id)::text,
        sqlc.arg(issuer_credential_fingerprint)::text, sqlc.arg(recipient_kind)::text,
        sqlc.arg(recipient_id)::uuid, sqlc.arg(permission_profile)::text,
        sqlc.arg(permissions)::jsonb, sqlc.arg(issued_at)::timestamptz,
        sqlc.narg(expires_at)::timestamptz, sqlc.arg(fingerprint)::text,
        sqlc.arg(idempotency_key)::text, sqlc.arg(request_digest)::text,
        sqlc.arg(allow_onward_delegation)::boolean)
ON CONFLICT (issuer_principal_id, issuer_credential_id, idempotency_key) DO NOTHING;

-- name: InsertExecutionGrant :execresult
INSERT INTO access.execution_grant
    (id, profile, instance_id, project_id, resource_uid, resource_id, resource_kind,
     issuer_principal_id, issuer_credential_class, issuer_credential_id, issuer_credential_fingerprint,
     execution_principal_id, permission_profile, permissions, workflow_id, workflow_revision,
     closure_digest, binding_digest, destination_digest, trigger_digest, issued_at, expires_at,
     fingerprint, idempotency_key, request_digest)
VALUES (sqlc.arg(id)::text, sqlc.arg(profile)::text, sqlc.arg(instance_id)::text,
        sqlc.arg(project_id)::text, sqlc.arg(resource_uid)::uuid, sqlc.arg(resource_id)::text,
        sqlc.arg(resource_kind)::text, sqlc.arg(issuer_principal_id)::uuid,
        sqlc.arg(issuer_credential_class)::text, sqlc.arg(issuer_credential_id)::text,
        sqlc.arg(issuer_credential_fingerprint)::text, sqlc.arg(execution_principal_id)::uuid,
        sqlc.arg(permission_profile)::text, sqlc.arg(permissions)::jsonb,
        sqlc.arg(workflow_id)::text, sqlc.arg(workflow_revision)::text,
        sqlc.arg(closure_digest)::text, sqlc.arg(binding_digest)::text,
        sqlc.arg(destination_digest)::text, sqlc.arg(trigger_digest)::text,
        sqlc.arg(issued_at)::timestamptz, sqlc.arg(expires_at)::timestamptz,
        sqlc.arg(fingerprint)::text, sqlc.arg(idempotency_key)::text,
        sqlc.arg(request_digest)::text)
ON CONFLICT (issuer_principal_id, issuer_credential_id, idempotency_key) DO NOTHING;

-- name: InsertGrantAdminEnvelope :execresult
INSERT INTO access.grant_admin_envelope
    (id, profile, issuer_principal_id, issuer_credential_class, issuer_credential_id, issuer_credential_fingerprint,
     bound_principal_id, permission_profile, permissions, target_project_id, target_resource_kind, target_resource_id,
     recipient_selector, role_version, issued_at, expires_at, fingerprint, idempotency_key, request_digest, allow_onward_delegation)
VALUES (sqlc.arg(id)::text, sqlc.arg(profile)::text, sqlc.arg(issuer_principal_id)::uuid,
        sqlc.arg(issuer_credential_class)::text, sqlc.arg(issuer_credential_id)::text,
        sqlc.arg(issuer_credential_fingerprint)::text, sqlc.arg(bound_principal_id)::uuid,
        sqlc.arg(permission_profile)::text, sqlc.arg(permissions)::jsonb,
        sqlc.arg(target_project_id)::text, sqlc.narg(target_resource_kind)::text,
        sqlc.narg(target_resource_id)::text, sqlc.arg(recipient_selector)::text,
        sqlc.arg(role_version)::text, sqlc.arg(issued_at)::timestamptz,
        sqlc.arg(expires_at)::timestamptz, sqlc.arg(fingerprint)::text,
        sqlc.arg(idempotency_key)::text, sqlc.arg(request_digest)::text,
        sqlc.arg(allow_onward_delegation)::boolean)
ON CONFLICT (issuer_principal_id, issuer_credential_id, idempotency_key) DO NOTHING;

-- name: GetResourceShareGrant :one
SELECT id, profile, instance_id, project_id, resource_uid::text AS resource_uid,
       resource_id, resource_kind, issuer_principal_id::text AS issuer_principal_id,
       issuer_credential_class, issuer_credential_id, issuer_credential_fingerprint,
       recipient_kind, recipient_id::text AS recipient_id, permission_profile, permissions,
       issued_at, COALESCE(expires_at, 'epoch'::timestamptz) AS expires_at,
       fingerprint, idempotency_key, request_digest, allow_onward_delegation,
       COALESCE(revoked_at, 'epoch'::timestamptz) AS revoked_at,
       COALESCE(revoked_by_principal_id::text, ''::text)::text AS revoked_by_principal_id,
       revocation_reason
FROM access.resource_share_grant
WHERE id = sqlc.arg(id)::text;

-- name: GetResourceShareGrantByIdempotency :one
SELECT id
FROM access.resource_share_grant
WHERE issuer_principal_id = sqlc.arg(issuer_principal_id)::uuid
  AND issuer_credential_id = sqlc.arg(issuer_credential_id)::text
  AND idempotency_key = sqlc.arg(idempotency_key)::text;

-- name: GetExecutionGrant :one
SELECT id, profile, instance_id, project_id, resource_uid::text AS resource_uid,
       resource_id, resource_kind, issuer_principal_id::text AS issuer_principal_id,
       issuer_credential_class, issuer_credential_id, issuer_credential_fingerprint,
       execution_principal_id::text AS execution_principal_id, permission_profile, permissions,
       workflow_id, workflow_revision, closure_digest, binding_digest, destination_digest,
       trigger_digest, issued_at, expires_at, fingerprint, idempotency_key, request_digest,
       COALESCE(revoked_at, 'epoch'::timestamptz) AS revoked_at,
       COALESCE(revoked_by_principal_id::text, ''::text)::text AS revoked_by_principal_id,
       revocation_reason
FROM access.execution_grant
WHERE id = sqlc.arg(id)::text;

-- name: GetExecutionGrantByIdempotency :one
SELECT id
FROM access.execution_grant
WHERE issuer_principal_id = sqlc.arg(issuer_principal_id)::uuid
  AND issuer_credential_id = sqlc.arg(issuer_credential_id)::text
  AND idempotency_key = sqlc.arg(idempotency_key)::text;

-- name: SelectCurrentExecutionGrantIDs :many
SELECT g.id
FROM access.execution_grant g
WHERE g.instance_id = sqlc.arg(instance_id)::text
  AND g.project_id = sqlc.arg(project_id)::text
  AND g.resource_id = sqlc.arg(resource_id)::text
  AND g.resource_kind = 'pipeline'
  AND g.revoked_at IS NULL
  AND g.expires_at > clock_timestamp()
  AND EXISTS (
      SELECT 1
      FROM project.resource_uid_registry target
      WHERE target.instance_id = g.instance_id
        AND target.project_id = g.project_id
        AND target.resource_uid = g.resource_uid
        AND target.authored_resource_id = g.resource_id
        AND target.resource_kind = g.resource_kind
        AND target.state = 'active'
  )
  AND EXISTS (
      SELECT 1
      FROM access.principal execution_principal
      WHERE execution_principal.id = g.execution_principal_id
        AND execution_principal.status = 'active'
        AND execution_principal.revoked_at IS NULL
        AND execution_principal.disabled_at IS NULL
        AND execution_principal.blocked_at IS NULL
  )
ORDER BY g.id;

-- name: GetGrantAdminEnvelope :one
SELECT id, profile, issuer_principal_id::text AS issuer_principal_id,
       issuer_credential_class, issuer_credential_id, issuer_credential_fingerprint,
       bound_principal_id::text AS bound_principal_id, permission_profile, permissions,
       target_project_id, COALESCE(target_resource_kind, '') AS target_resource_kind,
       COALESCE(target_resource_id, '') AS target_resource_id, recipient_selector,
       role_version, issued_at, expires_at, fingerprint, idempotency_key, request_digest,
       allow_onward_delegation, COALESCE(revoked_at, 'epoch'::timestamptz) AS revoked_at,
       COALESCE(revoked_by_principal_id::text, ''::text)::text AS revoked_by_principal_id,
       revocation_reason
FROM access.grant_admin_envelope
WHERE id = sqlc.arg(id)::text;

-- name: GetGrantAdminEnvelopeByIdempotency :one
SELECT id
FROM access.grant_admin_envelope
WHERE issuer_principal_id = sqlc.arg(issuer_principal_id)::uuid
  AND issuer_credential_id = sqlc.arg(issuer_credential_id)::text
  AND idempotency_key = sqlc.arg(idempotency_key)::text;

-- name: IsCurrentPrincipal :one
SELECT EXISTS (
    SELECT 1 FROM access.principal
    WHERE id = sqlc.arg(id)::uuid AND status = 'active'
      AND revoked_at IS NULL AND disabled_at IS NULL AND blocked_at IS NULL
);

-- name: IsCurrentGroup :one
SELECT EXISTS (
    SELECT 1 FROM access.access_group
    WHERE id = sqlc.arg(id)::uuid AND revoked_at IS NULL
);

-- name: IsActiveSessionCredential :one
SELECT EXISTS (
    SELECT 1 FROM access.session
    WHERE id = sqlc.arg(id)::uuid AND principal_id = sqlc.arg(principal_id)::uuid
      AND token_fingerprint = sqlc.arg(fingerprint)::bytea
      AND revoked_at IS NULL AND expires_at > clock_timestamp()
);

-- name: IsActiveAPITokenCredential :one
SELECT EXISTS (
    SELECT 1 FROM access.api_token
    WHERE id = sqlc.arg(id)::uuid AND principal_id = sqlc.arg(principal_id)::uuid
      AND token_fingerprint = sqlc.arg(fingerprint)::bytea
      AND revoked_at IS NULL AND expires_at > clock_timestamp()
);

-- name: GetAPITokenPermissionCeiling :one
SELECT COALESCE(permission_profile, '') AS permission_profile,
       COALESCE(permissions, 'null'::jsonb) AS permissions
FROM access.api_token
WHERE id = sqlc.arg(id)::uuid AND principal_id = sqlc.arg(principal_id)::uuid;

-- name: IsActiveResourceTarget :one
SELECT EXISTS (
    SELECT 1 FROM project.resource_uid_registry
    WHERE instance_id = sqlc.arg(instance_id)::text
      AND project_id = sqlc.arg(project_id)::text
      AND resource_uid = sqlc.arg(resource_uid)::uuid
      AND authored_resource_id = sqlc.arg(resource_id)::text
      AND resource_kind = sqlc.arg(resource_kind)::text
      AND state = 'active'
);

-- name: RevokeResourceShareGrant :one
UPDATE access.resource_share_grant
SET revoked_at = clock_timestamp(), revoked_by_principal_id = sqlc.arg(actor_id)::uuid,
    revocation_reason = sqlc.arg(reason)::text
WHERE id = sqlc.arg(id)::text AND revoked_at IS NULL
RETURNING issuer_principal_id::text AS issuer_principal_id, project_id, resource_id,
          resource_kind, recipient_id::text AS recipient_id;

-- name: RevokeExecutionGrant :one
UPDATE access.execution_grant
SET revoked_at = clock_timestamp(), revoked_by_principal_id = sqlc.arg(actor_id)::uuid,
    revocation_reason = sqlc.arg(reason)::text
WHERE id = sqlc.arg(id)::text AND revoked_at IS NULL
RETURNING issuer_principal_id::text AS issuer_principal_id, project_id, resource_id,
          resource_kind, execution_principal_id::text AS recipient_id;

-- name: RevokeGrantAdminEnvelope :one
UPDATE access.grant_admin_envelope
SET revoked_at = clock_timestamp(), revoked_by_principal_id = sqlc.arg(actor_id)::uuid,
    revocation_reason = sqlc.arg(reason)::text
WHERE id = sqlc.arg(id)::text AND revoked_at IS NULL
RETURNING issuer_principal_id::text AS issuer_principal_id, target_project_id AS project_id,
          COALESCE(target_resource_id, '') AS resource_id,
          COALESCE(target_resource_kind, '') AS resource_kind,
          bound_principal_id::text AS recipient_id;
