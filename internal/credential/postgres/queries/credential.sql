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

-- name: InsertActivationPreparation :one
WITH wall_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now
)
INSERT INTO credential.activation_preparation (
    operation_id, deployment_id, receipt_id, expected_target_revision,
    predecessor_generation_id, candidate_id, generation_id, publication_id, created_at
)
SELECT
    sqlc.arg(operation_id), receipt.deployment_id, receipt.receipt_id,
    sqlc.arg(expected_target_revision),
    NULLIF(sqlc.arg(predecessor_generation_id)::text, ''),
    sqlc.arg(candidate_id), sqlc.arg(generation_id), sqlc.arg(publication_id), wall_clock.now
FROM credential.validation_receipt AS receipt
CROSS JOIN wall_clock
WHERE receipt.receipt_id = sqlc.arg(receipt_id)
  AND receipt.deployment_id = sqlc.arg(deployment_id)
  AND receipt.version_id = sqlc.arg(version_id)
  AND receipt.owner_id = sqlc.arg(owner_id)
  AND receipt.scope_kind = sqlc.arg(scope_kind)
  AND receipt.target_id = sqlc.arg(target_id)
  AND receipt.project_id = sqlc.arg(project_id)
  AND receipt.environment = sqlc.arg(environment)
  AND receipt.resource_id = sqlc.arg(resource_id)
  AND receipt.purpose = sqlc.arg(purpose)
  AND receipt.provider = sqlc.arg(provider)
  AND receipt.destination = sqlc.arg(destination)
  AND receipt.actor_id = sqlc.arg(actor_id)
  AND receipt.binding_id = sqlc.arg(binding_id)
  AND receipt.binding_revision = sqlc.arg(binding_revision)
  AND receipt.configuration_digest = sqlc.arg(configuration_digest)
  AND receipt.validated_at = sqlc.arg(validated_at)::timestamptz
  AND receipt.expires_at = sqlc.arg(expires_at)::timestamptz
  AND credential.activation_receipt_is_fresh(receipt.deployment_id, sqlc.arg(operation_id), receipt.receipt_id, wall_clock.now)
RETURNING created_at;

-- name: ActivationPreparationReceiptIsFresh :one
WITH wall_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now
)
SELECT credential.activation_receipt_is_fresh(preparation.deployment_id, preparation.operation_id, preparation.receipt_id, wall_clock.now)::boolean AS fresh
FROM credential.activation_preparation AS preparation
JOIN credential.validation_receipt AS receipt
  ON receipt.deployment_id = preparation.deployment_id
 AND receipt.receipt_id = preparation.receipt_id
CROSS JOIN wall_clock
WHERE preparation.deployment_id = sqlc.arg(deployment_id)
  AND preparation.operation_id = sqlc.arg(operation_id)
  AND preparation.aborted_at IS NULL AND preparation.completed_at IS NULL;

-- name: GetActivationPreparation :one
SELECT
    preparation.operation_id, preparation.expected_target_revision,
    COALESCE(preparation.predecessor_generation_id, '') AS predecessor_generation_id,
    preparation.candidate_id, preparation.generation_id, preparation.publication_id,
    preparation.created_at, preparation.switching_at, preparation.committed_at, preparation.completed_at, preparation.aborted_at,
    COALESCE(preparation.aborted_by, '') AS aborted_by,
    receipt.receipt_id, receipt.deployment_id, receipt.version_id, receipt.owner_id,
    receipt.scope_kind, receipt.target_id, receipt.project_id, receipt.environment,
    receipt.resource_id, receipt.purpose, receipt.provider, receipt.destination,
    receipt.actor_id, receipt.binding_id, receipt.binding_revision,
    receipt.configuration_digest, receipt.validated_at, receipt.expires_at
FROM credential.activation_preparation AS preparation
JOIN credential.validation_receipt AS receipt
  ON receipt.deployment_id = preparation.deployment_id
 AND receipt.receipt_id = preparation.receipt_id
WHERE preparation.deployment_id = sqlc.arg(deployment_id)
  AND preparation.operation_id = sqlc.arg(operation_id);

-- name: GetPendingActivation :one
SELECT
    preparation.operation_id, preparation.expected_target_revision,
    COALESCE(preparation.predecessor_generation_id, '') AS predecessor_generation_id,
    preparation.candidate_id, preparation.generation_id, preparation.publication_id,
    preparation.created_at, preparation.switching_at, preparation.committed_at, preparation.completed_at, preparation.aborted_at,
    COALESCE(preparation.aborted_by, '') AS aborted_by,
    receipt.receipt_id, receipt.deployment_id, receipt.version_id, receipt.owner_id,
    receipt.scope_kind, receipt.target_id, receipt.project_id, receipt.environment,
    receipt.resource_id, receipt.purpose, receipt.provider, receipt.destination,
    receipt.actor_id, receipt.binding_id, receipt.binding_revision,
    receipt.configuration_digest, receipt.validated_at, receipt.expires_at
FROM credential.activation_preparation AS preparation
JOIN credential.validation_receipt AS receipt
  ON receipt.deployment_id = preparation.deployment_id
 AND receipt.receipt_id = preparation.receipt_id
WHERE preparation.deployment_id = sqlc.arg(deployment_id)
  AND preparation.aborted_at IS NULL AND preparation.completed_at IS NULL;

-- name: BeginActivationSwitching :one
WITH wall_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now
)
UPDATE credential.activation_preparation AS preparation
SET switching_at = GREATEST(wall_clock.now, preparation.created_at)
FROM credential.validation_receipt AS receipt
CROSS JOIN wall_clock
WHERE preparation.deployment_id = sqlc.arg(deployment_id)
  AND preparation.operation_id = sqlc.arg(operation_id)
  AND preparation.receipt_id = receipt.receipt_id
  AND preparation.deployment_id = receipt.deployment_id
  AND receipt.actor_id = sqlc.arg(actor_id)
  AND credential.activation_receipt_is_fresh(preparation.deployment_id, preparation.operation_id, preparation.receipt_id, wall_clock.now)
  AND preparation.switching_at IS NULL
  AND preparation.aborted_at IS NULL
RETURNING preparation.switching_at;

-- name: CommitActivationPublication :one
-- The trigger rechecks freshness after the row lock and stamps authoritative committed_at.
WITH wall_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now
)
UPDATE credential.activation_preparation AS preparation
SET committed_at = wall_clock.now
FROM credential.validation_receipt AS receipt
CROSS JOIN wall_clock
WHERE preparation.operation_id = sqlc.arg(operation_id)
  AND preparation.deployment_id = sqlc.arg(deployment_id)
  AND preparation.receipt_id = receipt.receipt_id
  AND preparation.deployment_id = receipt.deployment_id
  AND preparation.receipt_id = sqlc.arg(receipt_id)
  AND preparation.expected_target_revision = sqlc.arg(expected_target_revision)
  AND preparation.predecessor_generation_id IS NOT DISTINCT FROM NULLIF(sqlc.arg(predecessor_generation_id)::text, '')
  AND preparation.candidate_id = sqlc.arg(candidate_id)
  AND preparation.generation_id = sqlc.arg(generation_id)
  AND preparation.publication_id = sqlc.arg(publication_id)
  AND preparation.created_at = sqlc.arg(created_at)::timestamptz
  AND preparation.switching_at = sqlc.narg(expected_switching_at)::timestamptz
  AND preparation.switching_at <= wall_clock.now
  AND preparation.committed_at IS NULL
  AND preparation.aborted_at IS NULL
  AND preparation.aborted_by IS NULL
  AND receipt.actor_id = sqlc.arg(actor_id)
  AND credential.activation_receipt_is_fresh(preparation.deployment_id, preparation.operation_id, preparation.receipt_id, wall_clock.now)
RETURNING preparation.committed_at;

-- name: AbortActivationPreparation :one
UPDATE credential.activation_preparation
SET aborted_at = GREATEST(clock_timestamp(), COALESCE(switching_at, created_at)),
    aborted_by = sqlc.arg(aborted_by)
WHERE deployment_id = sqlc.arg(deployment_id)
  AND operation_id = sqlc.arg(operation_id)
  AND aborted_at IS NULL
  AND committed_at IS NULL
  AND switching_at IS NOT DISTINCT FROM sqlc.narg(expected_switching_at)::timestamptz
RETURNING aborted_at, aborted_by;

-- name: CheckNoPendingActivation :one
SELECT
    current_setting('transaction_isolation') = 'read committed' AS read_committed,
    NOT EXISTS (
        SELECT 1
        FROM credential.activation_preparation AS preparation
        WHERE preparation.deployment_id = sqlc.arg(deployment_id)
          AND preparation.aborted_at IS NULL AND preparation.completed_at IS NULL
    ) AND NOT EXISTS (
        SELECT 1 FROM credential.activation_request WHERE deployment_id=sqlc.arg(deployment_id) AND state NOT IN ('completed','aborted')
    ) AS no_pending;

-- name: CompleteActivation :one
UPDATE credential.activation_preparation
SET completed_at = GREATEST(clock_timestamp(), committed_at)
WHERE deployment_id = sqlc.arg(deployment_id)
  AND operation_id = sqlc.arg(operation_id)
  AND committed_at = sqlc.arg(expected_committed_at)::timestamptz
  AND completed_at IS NULL AND aborted_at IS NULL
RETURNING completed_at;
