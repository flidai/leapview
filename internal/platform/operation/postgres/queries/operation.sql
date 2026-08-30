-- Durable operation state-machine queries. Each transition remains one SQL
-- statement so PostgreSQL row locks and fencing predicates stay atomic.

-- name: ClockTimestamp :one
SELECT CAST(clock_timestamp() AS timestamptz) AS now;

-- name: InsertOperation :one
INSERT INTO platform.operation
 (scope_id, operation_type, idempotency_key, request_digest, operation_id,
  state, owner_id, lease_expires_at, fencing_generation, outcome,
  attempt_id, attempt_identity, created_at, updated_at, retention_interval, expires_at)
VALUES (sqlc.arg(scope_id), sqlc.arg(operation_type), sqlc.arg(idempotency_key),
        sqlc.arg(request_digest), sqlc.arg(operation_id), 'pending',
        sqlc.arg(owner_id), sqlc.arg(lease_expires_at), 1, '{}'::jsonb,
        NULL, NULL, sqlc.arg(created_at), sqlc.arg(created_at),
        sqlc.arg(retention_interval), sqlc.arg(expires_at))
ON CONFLICT (scope_id, idempotency_key) DO NOTHING
RETURNING operation_id;

-- name: GetOperationForUpdate :one
SELECT scope_id, operation_type, idempotency_key, request_digest,
       operation_id, state, owner_id, lease_expires_at, fencing_generation,
       outcome, attempt_id, attempt_identity, attempt_evidence,
       resolution_evidence, created_at, updated_at, terminal_at, expires_at
FROM platform.operation
WHERE scope_id = sqlc.arg(scope_id) AND idempotency_key = sqlc.arg(idempotency_key)
FOR UPDATE;

-- name: GetOperation :one
SELECT scope_id, operation_type, idempotency_key, request_digest,
       operation_id, state, owner_id, lease_expires_at, fencing_generation,
       outcome, attempt_id, attempt_identity, attempt_evidence,
       resolution_evidence, created_at, updated_at, terminal_at, expires_at
FROM platform.operation
WHERE scope_id = sqlc.arg(scope_id) AND idempotency_key = sqlc.arg(idempotency_key);

-- name: TakeoverOperation :one
UPDATE platform.operation
SET owner_id = sqlc.arg(owner_id), fencing_generation = fencing_generation + 1,
    lease_expires_at = sqlc.arg(lease_expires_at), updated_at = sqlc.arg(updated_at)
WHERE scope_id = sqlc.arg(scope_id) AND idempotency_key = sqlc.arg(idempotency_key)
  AND state = 'pending' AND lease_expires_at <= sqlc.arg(updated_at)
  AND attempt_id IS NULL
RETURNING fencing_generation;

-- name: ExpireOperationAttempt :execresult
UPDATE platform.operation
SET state = 'indeterminate',
    outcome = '{"code":"IDEMPOTENCY_OUTCOME_UNKNOWN","detail":"The original request outcome is indeterminate and requires reconciliation evidence"}'::jsonb,
    attempt_evidence = sqlc.arg(attempt_evidence)::jsonb,
    fencing_generation = fencing_generation + 1,
    updated_at = sqlc.arg(updated_at), terminal_at = sqlc.arg(updated_at),
    expires_at = sqlc.arg(updated_at)::timestamptz + retention_interval
WHERE scope_id = sqlc.arg(scope_id) AND idempotency_key = sqlc.arg(idempotency_key)
  AND operation_id = sqlc.arg(operation_id) AND owner_id = sqlc.arg(owner_id)
  AND fencing_generation = sqlc.arg(fencing_generation)
  AND attempt_id = sqlc.arg(attempt_id) AND attempt_identity = sqlc.arg(attempt_identity)
  AND state = 'pending'
  AND lease_expires_at <= sqlc.arg(updated_at);

-- name: GetExpiredAttemptIndeterminateForUpdate :one
SELECT scope_id, operation_type, idempotency_key, request_digest,
       operation_id, state, owner_id, lease_expires_at, fencing_generation,
       outcome, attempt_id, attempt_identity, attempt_evidence,
       resolution_evidence, created_at, updated_at, terminal_at, expires_at
FROM platform.operation
WHERE scope_id = sqlc.arg(scope_id) AND idempotency_key = sqlc.arg(idempotency_key)
  AND operation_id = sqlc.arg(operation_id) AND owner_id = sqlc.arg(owner_id)
  AND fencing_generation = sqlc.arg(expected_fencing_generation)
  AND attempt_id = sqlc.arg(attempt_id) AND attempt_identity = sqlc.arg(attempt_identity)
  AND state = 'indeterminate'
FOR UPDATE;

-- name: CompleteOperation :execresult
UPDATE platform.operation
SET state = 'completed', outcome = sqlc.arg(outcome)::jsonb,
    updated_at = sqlc.arg(updated_at), terminal_at = sqlc.arg(updated_at),
    expires_at = sqlc.arg(updated_at)::timestamptz + retention_interval
WHERE scope_id = sqlc.arg(scope_id) AND idempotency_key = sqlc.arg(idempotency_key)
  AND operation_id = sqlc.arg(operation_id) AND owner_id = sqlc.arg(owner_id)
  AND fencing_generation = sqlc.arg(fencing_generation) AND state = 'pending'
  AND lease_expires_at > sqlc.arg(updated_at);

-- name: FailOperation :execresult
UPDATE platform.operation
SET state = 'failed', outcome = sqlc.arg(outcome)::jsonb,
    updated_at = sqlc.arg(updated_at), terminal_at = sqlc.arg(updated_at),
    expires_at = sqlc.arg(updated_at)::timestamptz + retention_interval
WHERE scope_id = sqlc.arg(scope_id) AND idempotency_key = sqlc.arg(idempotency_key)
  AND operation_id = sqlc.arg(operation_id) AND owner_id = sqlc.arg(owner_id)
  AND fencing_generation = sqlc.arg(fencing_generation) AND state = 'pending'
  AND lease_expires_at > sqlc.arg(updated_at);

-- name: MarkLeaseIndeterminate :execresult
UPDATE platform.operation
SET state = 'indeterminate',
    outcome = '{"code":"IDEMPOTENCY_OUTCOME_UNKNOWN","detail":"The original request outcome is indeterminate and requires reconciliation evidence"}'::jsonb,
    attempt_evidence = sqlc.arg(attempt_evidence)::jsonb,
    fencing_generation = fencing_generation + 1,
    updated_at = sqlc.arg(updated_at), terminal_at = sqlc.arg(updated_at),
    expires_at = sqlc.arg(updated_at)::timestamptz + retention_interval
WHERE scope_id = sqlc.arg(scope_id) AND idempotency_key = sqlc.arg(idempotency_key)
  AND operation_id = sqlc.arg(operation_id) AND owner_id = sqlc.arg(owner_id)
  AND fencing_generation = sqlc.arg(fencing_generation)
  AND attempt_id = sqlc.arg(attempt_id) AND attempt_identity = sqlc.arg(attempt_identity)
  AND state = 'pending' AND lease_expires_at > sqlc.arg(updated_at);

-- name: RenewOperationLease :execresult
UPDATE platform.operation
SET lease_expires_at = sqlc.arg(lease_expires_at), updated_at = sqlc.arg(updated_at)
WHERE scope_id = sqlc.arg(scope_id) AND idempotency_key = sqlc.arg(idempotency_key)
  AND operation_id = sqlc.arg(operation_id) AND owner_id = sqlc.arg(owner_id)
  AND fencing_generation = sqlc.arg(fencing_generation) AND state = 'pending'
  AND attempt_id IS NOT DISTINCT FROM sqlc.arg(attempt_id)
  AND attempt_identity IS NOT DISTINCT FROM sqlc.arg(attempt_identity)
  AND lease_expires_at > sqlc.arg(updated_at);

-- name: BindOperationAttempt :execresult
UPDATE platform.operation
SET attempt_id = sqlc.arg(attempt_id), attempt_identity = sqlc.arg(attempt_identity),
    updated_at = sqlc.arg(updated_at)
WHERE scope_id = sqlc.arg(scope_id) AND idempotency_key = sqlc.arg(idempotency_key)
  AND operation_id = sqlc.arg(operation_id) AND owner_id = sqlc.arg(owner_id)
  AND fencing_generation = sqlc.arg(fencing_generation) AND state = 'pending'
  AND lease_expires_at > sqlc.arg(updated_at)
  AND (attempt_id IS NULL OR (attempt_id = sqlc.arg(attempt_id)
                             AND attempt_identity = sqlc.arg(attempt_identity)));

-- name: ReconcileOperation :execresult
UPDATE platform.operation
SET state = sqlc.arg(state), outcome = sqlc.arg(outcome)::jsonb,
    attempt_evidence = COALESCE(attempt_evidence, sqlc.arg(evidence)::jsonb),
    resolution_evidence = sqlc.arg(evidence)::jsonb,
    updated_at = sqlc.arg(updated_at), terminal_at = sqlc.arg(updated_at),
    expires_at = sqlc.arg(updated_at)::timestamptz + retention_interval
WHERE scope_id = sqlc.arg(scope_id) AND idempotency_key = sqlc.arg(idempotency_key)
  AND attempt_id = sqlc.arg(attempt_id) AND attempt_identity = sqlc.arg(attempt_identity)
  AND state = 'indeterminate';

-- name: PruneOperations :one
SELECT platform.prune_operations(sqlc.arg(p_before), sqlc.arg(p_limit));

-- name: GetOperationTransitionState :one
SELECT state, owner_id, fencing_generation, lease_expires_at
FROM platform.operation
WHERE scope_id = sqlc.arg(scope_id) AND idempotency_key = sqlc.arg(idempotency_key)
  AND operation_id = sqlc.arg(operation_id);
