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
UPDATE platform.operation AS operation
SET state = sqlc.arg(state), outcome = sqlc.arg(outcome)::jsonb,
    attempt_evidence = COALESCE(operation.attempt_evidence, sqlc.arg(evidence)::jsonb),
    resolution_evidence = sqlc.arg(evidence)::jsonb,
    updated_at = sqlc.arg(updated_at), terminal_at = sqlc.arg(updated_at),
    expires_at = sqlc.arg(updated_at)::timestamptz + operation.retention_interval
WHERE operation.scope_id = sqlc.arg(scope_id) AND operation.idempotency_key = sqlc.arg(idempotency_key)
  AND operation.attempt_id = sqlc.arg(attempt_id) AND operation.attempt_identity = sqlc.arg(attempt_identity)
  AND operation.state = 'indeterminate'
  AND NOT EXISTS (
      SELECT 1
      FROM platform.operation_successor_attempt successor
      WHERE successor.operation_id = operation.operation_id
  );

-- The successor path settles the immutable public root only after the
-- addressed leaf has reached a terminal state.  Keeping this as a separate
-- query means the ordinary public-root reconciliation cannot be reused as a
-- stale-root bypass once a successor exists.
-- name: ReconcileOperationFromSuccessor :execresult
UPDATE platform.operation AS operation
SET state = sqlc.arg(state), outcome = sqlc.arg(outcome)::jsonb,
    attempt_evidence = COALESCE(operation.attempt_evidence, sqlc.arg(evidence)::jsonb),
    resolution_evidence = sqlc.arg(evidence)::jsonb,
    updated_at = sqlc.arg(updated_at), terminal_at = sqlc.arg(updated_at),
    expires_at = sqlc.arg(updated_at)::timestamptz + operation.retention_interval
WHERE operation.scope_id = sqlc.arg(scope_id) AND operation.idempotency_key = sqlc.arg(idempotency_key)
  AND operation.attempt_id = sqlc.arg(attempt_id)::uuid
  AND operation.attempt_identity = sqlc.arg(attempt_identity)
  AND operation.state = 'indeterminate'
  AND EXISTS (
      SELECT 1
      FROM platform.operation_successor_attempt successor
      WHERE successor.operation_id = operation.operation_id
        AND successor.attempt_id = sqlc.arg(successor_attempt_id)::uuid
        AND successor.attempt_identity = sqlc.arg(successor_attempt_identity)
        AND successor.state = sqlc.arg(state)
        AND successor.resolution_evidence = sqlc.arg(evidence)::jsonb
        AND NOT EXISTS (
            SELECT 1
            FROM platform.operation_successor_attempt child
            WHERE child.operation_id = successor.operation_id
              AND child.predecessor_attempt_id = successor.attempt_id
        )
  );

-- name: PruneOperations :one
SELECT platform.prune_operations(sqlc.arg(p_before), sqlc.arg(p_limit));

-- name: GetOperationTransitionState :one
SELECT state, owner_id, fencing_generation, lease_expires_at
FROM platform.operation
WHERE scope_id = sqlc.arg(scope_id) AND idempotency_key = sqlc.arg(idempotency_key)
  AND operation_id = sqlc.arg(operation_id);

-- Native build successor execution leaves.  The public operation row remains
-- immutable after indeterminate; these rows carry only the executable leaf.
-- name: InsertOperationSuccessorAttempt :execresult
INSERT INTO platform.operation_successor_attempt
 (operation_id, predecessor_attempt_id, predecessor_attempt_identity,
  attempt_id, attempt_identity, owner_id, fencing_generation,
  lease_expires_at, state, attempt_evidence, resolution_evidence,
  created_at, updated_at, terminal_at)
VALUES (sqlc.arg(operation_id)::uuid, sqlc.arg(predecessor_attempt_id)::uuid,
        sqlc.arg(predecessor_attempt_identity), sqlc.arg(attempt_id)::uuid,
        sqlc.arg(attempt_identity), sqlc.arg(owner_id),
        sqlc.arg(fencing_generation), sqlc.arg(lease_expires_at), 'pending',
        NULL, NULL, sqlc.arg(created_at), sqlc.arg(created_at), NULL)
ON CONFLICT (operation_id, predecessor_attempt_id) DO NOTHING;

-- name: GetOperationSuccessorAttemptForUpdate :one
SELECT operation_id::text, predecessor_attempt_id::text,
       predecessor_attempt_identity, attempt_id::text, attempt_identity,
       owner_id, fencing_generation, lease_expires_at, state,
       attempt_evidence, resolution_evidence, created_at, updated_at,
       terminal_at
FROM platform.operation_successor_attempt
WHERE operation_id = sqlc.arg(operation_id)::uuid
  AND predecessor_attempt_id = sqlc.arg(predecessor_attempt_id)::uuid
FOR UPDATE;

-- name: GetCurrentOperationSuccessorAttempt :one
SELECT o.scope_id, o.idempotency_key,
       s.operation_id::text AS operation_id,
       s.predecessor_attempt_id::text AS predecessor_attempt_id,
       s.predecessor_attempt_identity,
       s.attempt_id::text AS attempt_id,
       s.attempt_identity,
       s.owner_id, s.fencing_generation, s.lease_expires_at, s.state,
       s.attempt_evidence, s.resolution_evidence, s.created_at, s.updated_at,
       s.terminal_at
FROM platform.operation_successor_attempt s
JOIN platform.operation o ON o.operation_id = s.operation_id
WHERE s.operation_id = sqlc.arg(operation_id)::uuid
  AND s.state IN ('pending', 'indeterminate')
  AND NOT EXISTS (
      SELECT 1 FROM platform.operation_successor_attempt child
       WHERE child.operation_id = s.operation_id
         AND child.predecessor_attempt_id = s.attempt_id
  )
ORDER BY s.attempt_id
LIMIT 1;

-- name: GetOperationSuccessorAttemptByAttemptForUpdate :one
SELECT operation_id::text, predecessor_attempt_id::text,
       predecessor_attempt_identity, attempt_id::text, attempt_identity,
       owner_id, fencing_generation, lease_expires_at, state,
       attempt_evidence, resolution_evidence, created_at, updated_at,
       terminal_at
FROM platform.operation_successor_attempt
WHERE operation_id = sqlc.arg(operation_id)::uuid
  AND attempt_id = sqlc.arg(attempt_id)::uuid
FOR UPDATE;

-- name: RenewOperationSuccessorLease :execresult
UPDATE platform.operation_successor_attempt
SET lease_expires_at = sqlc.arg(lease_expires_at), updated_at = sqlc.arg(updated_at)
WHERE operation_id = sqlc.arg(operation_id)::uuid
  AND attempt_id = sqlc.arg(attempt_id)::uuid
  AND owner_id = sqlc.arg(owner_id)
  AND fencing_generation = sqlc.arg(fencing_generation)
  AND state = 'pending'
  AND lease_expires_at > sqlc.arg(updated_at)
  AND lease_expires_at <= sqlc.arg(lease_expires_at);

-- name: MarkOperationSuccessorIndeterminate :execresult
UPDATE platform.operation_successor_attempt
SET state = 'indeterminate', attempt_evidence = sqlc.arg(attempt_evidence)::jsonb,
    updated_at = sqlc.arg(updated_at), terminal_at = sqlc.arg(updated_at)
WHERE operation_id = sqlc.arg(operation_id)::uuid
  AND attempt_id = sqlc.arg(attempt_id)::uuid
  AND owner_id = sqlc.arg(owner_id)
  AND fencing_generation = sqlc.arg(fencing_generation)
  AND state = 'pending'
  AND lease_expires_at > sqlc.arg(updated_at);

-- name: ExpireOperationSuccessorAttempt :execresult
UPDATE platform.operation_successor_attempt
SET state = 'indeterminate', attempt_evidence = sqlc.arg(attempt_evidence)::jsonb,
    updated_at = sqlc.arg(updated_at), terminal_at = sqlc.arg(updated_at)
WHERE operation_id = sqlc.arg(operation_id)::uuid
  AND attempt_id = sqlc.arg(attempt_id)::uuid
  AND owner_id = sqlc.arg(owner_id)
  AND fencing_generation = sqlc.arg(fencing_generation)
  AND state = 'pending'
  AND lease_expires_at <= sqlc.arg(updated_at);

-- name: ReconcileOperationSuccessor :execresult
UPDATE platform.operation_successor_attempt
SET state = sqlc.arg(state), resolution_evidence = sqlc.arg(resolution_evidence)::jsonb,
    updated_at = sqlc.arg(updated_at), terminal_at = sqlc.arg(updated_at)
WHERE operation_id = sqlc.arg(operation_id)::uuid
  AND attempt_id = sqlc.arg(attempt_id)::uuid
  AND attempt_identity = sqlc.arg(attempt_identity)
  AND (
      state = 'indeterminate'
      OR (state = 'pending' AND lease_expires_at > sqlc.arg(updated_at))
  );
