-- name: ReserveEncryptionBudget :one
INSERT INTO credential.encryption_budget (deployment_id, key_id, key_commitment, uses)
VALUES (sqlc.arg(deployment_id), sqlc.arg(key_id), sqlc.arg(key_commitment), 1)
ON CONFLICT (deployment_id, key_id)
DO UPDATE SET uses = credential.encryption_budget.uses + 1
WHERE credential.encryption_budget.key_commitment = EXCLUDED.key_commitment
  AND credential.encryption_budget.uses < 2147483648
RETURNING uses;

-- name: GetEncryptionBudgetKey :one
SELECT key_commitment, uses
FROM credential.encryption_budget
WHERE deployment_id = sqlc.arg(deployment_id)
  AND key_id = sqlc.arg(key_id);

-- name: ListEncryptionBudgetKeys :many
SELECT b.key_id, b.key_commitment,
       EXISTS (
           SELECT 1 FROM credential.envelope AS e
           WHERE e.deployment_id = b.deployment_id AND e.key_id = b.key_id
       ) AS has_envelope
FROM credential.encryption_budget AS b
WHERE b.deployment_id = sqlc.arg(deployment_id)
ORDER BY b.key_id;

-- name: InsertDraftVersion :exec
INSERT INTO credential.draft_version (
    version_id, deployment_id, owner_id, scope_kind, target_id, project_id,
    environment, resource_id, purpose, provider, destination, actor_id, created_at
)
VALUES (
    sqlc.arg(version_id), sqlc.arg(deployment_id), sqlc.arg(owner_id), sqlc.arg(scope_kind),
    sqlc.arg(target_id), sqlc.arg(project_id), sqlc.arg(environment), sqlc.arg(resource_id),
    sqlc.arg(purpose), sqlc.arg(provider), sqlc.arg(destination), sqlc.arg(actor_id), sqlc.arg(created_at)
);

-- name: InsertDraftEnvelope :exec
INSERT INTO credential.envelope (version_id, deployment_id, key_id, format, ciphertext)
VALUES (
    sqlc.arg(version_id), sqlc.arg(deployment_id), sqlc.arg(key_id),
    sqlc.arg(format), sqlc.arg(ciphertext)
);

-- name: GetStoredDraft :one
SELECT
    v.version_id, v.deployment_id, v.owner_id, v.scope_kind, v.target_id,
    v.project_id, v.environment, v.resource_id, v.purpose, v.provider,
    v.destination, v.actor_id, v.created_at,
    e.format AS envelope_format, e.key_id AS envelope_key_id, e.ciphertext
FROM credential.draft_version AS v
JOIN credential.envelope AS e USING (version_id, deployment_id)
WHERE v.deployment_id = sqlc.arg(deployment_id)
  AND v.owner_id = sqlc.arg(owner_id)
  AND v.scope_kind = sqlc.arg(scope_kind)
  AND v.target_id = sqlc.arg(target_id)
  AND v.project_id = sqlc.arg(project_id)
  AND v.environment = sqlc.arg(environment)
  AND v.resource_id = sqlc.arg(resource_id)
  AND v.version_id = sqlc.arg(version_id);

-- name: GetDraftMetadata :one
SELECT
    version_id, deployment_id, owner_id, scope_kind, target_id,
    project_id, environment, resource_id, purpose, provider,
    destination, actor_id, created_at
FROM credential.draft_version
WHERE deployment_id = sqlc.arg(deployment_id)
  AND owner_id = sqlc.arg(owner_id)
  AND scope_kind = sqlc.arg(scope_kind)
  AND target_id = sqlc.arg(target_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND resource_id = sqlc.arg(resource_id)
  AND version_id = sqlc.arg(version_id);

-- name: ListDrafts :many
SELECT
    version_id, deployment_id, owner_id, scope_kind, target_id,
    project_id, environment, resource_id, purpose, provider,
    destination, actor_id, created_at
FROM credential.draft_version AS v
WHERE deployment_id = sqlc.arg(deployment_id)
  AND owner_id = sqlc.arg(owner_id)
  AND scope_kind = sqlc.arg(scope_kind)
  AND target_id = sqlc.arg(target_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND resource_id = sqlc.arg(resource_id)
  AND (
      sqlc.narg(before_created_at)::timestamptz IS NULL
      OR (created_at, version_id) < (
          sqlc.narg(before_created_at)::timestamptz,
          sqlc.narg(before_version_id)::text
      )
  )
ORDER BY created_at DESC, version_id DESC
LIMIT sqlc.arg(page_size)::integer;

-- name: InsertValidationReceipt :execrows
INSERT INTO credential.validation_receipt (
    receipt_id, deployment_id, version_id, owner_id, scope_kind, target_id,
    project_id, environment, resource_id, purpose, provider, destination,
    actor_id, binding_id, binding_revision, configuration_digest,
    validated_at, expires_at
)
SELECT
    sqlc.arg(receipt_id), sqlc.arg(deployment_id), sqlc.arg(version_id),
    sqlc.arg(owner_id), sqlc.arg(scope_kind), sqlc.arg(target_id),
    sqlc.arg(project_id), sqlc.arg(environment), sqlc.arg(resource_id),
    sqlc.arg(purpose), sqlc.arg(provider), sqlc.arg(destination),
    sqlc.arg(actor_id), sqlc.arg(binding_id), sqlc.arg(binding_revision),
    sqlc.arg(configuration_digest), sqlc.arg(validated_at), sqlc.arg(expires_at)
FROM credential.draft_version AS draft
WHERE draft.deployment_id = sqlc.arg(deployment_id)
  AND draft.version_id = sqlc.arg(version_id)
  AND draft.owner_id = sqlc.arg(owner_id)
  AND draft.scope_kind = sqlc.arg(scope_kind)
  AND draft.target_id = sqlc.arg(target_id)
  AND draft.project_id = sqlc.arg(project_id)
  AND draft.environment = sqlc.arg(environment)
  AND draft.resource_id = sqlc.arg(resource_id)
  AND draft.purpose = sqlc.arg(purpose)
  AND draft.provider = sqlc.arg(provider)
  AND draft.destination = sqlc.arg(destination)
  AND sqlc.arg(validated_at)::timestamptz <= transaction_timestamp()
  AND sqlc.arg(expires_at)::timestamptz = sqlc.arg(validated_at)::timestamptz + interval '5 minutes'
  AND sqlc.arg(expires_at)::timestamptz > transaction_timestamp();

-- name: CheckNoPendingActivation :one
SELECT
    current_setting('transaction_isolation') = 'read committed' AS read_committed,
    NOT EXISTS (
        SELECT 1
        FROM credential.activation_preparation AS preparation
        WHERE preparation.deployment_id = sqlc.arg(deployment_id)
          AND preparation.aborted_at IS NULL
    ) AS no_pending;
