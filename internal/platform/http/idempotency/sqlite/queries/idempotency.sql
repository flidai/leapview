-- Public API idempotency records and execution leases.

-- name: DeleteExpiredAPIIdempotencyRecord :exec
DELETE FROM api_idempotency_records WHERE scope = ? AND expires_at <= ?;

-- name: CreateAPIIdempotencyRecord :execrows
INSERT INTO api_idempotency_records
  (scope, request_digest, state, owner_id, owner_session, lease_expires_at, created_at, updated_at, expires_at)
VALUES (?, ?, 'pending', ?, ?, ?, ?, ?, ?) ON CONFLICT(scope) DO NOTHING;

-- A pending row left by another server process has an unknowable business
-- outcome. Quarantine it instead of risking a duplicate mutation after restart.
-- name: QuarantineAbandonedAPIIdempotencyRecord :execrows
UPDATE api_idempotency_records
SET state = 'completed', response_status = 409,
  response_headers_json = '{"Content-Type":["application/problem+json"]}',
  response_body = '{"code":"IDEMPOTENCY_OUTCOME_UNKNOWN","detail":"The original request outcome is indeterminate and will not be executed again"}',
  updated_at = sqlc.arg(updated_at)
WHERE scope = sqlc.arg(scope) AND request_digest = sqlc.arg(request_digest)
  AND state = 'pending' AND owner_session <> sqlc.arg(owner_session)
  AND julianday(lease_expires_at) <= julianday('now');

-- name: GetAPIIdempotencyRecord :one
SELECT request_digest, state, owner_id, owner_session, lease_generation, lease_expires_at, response_status,
  response_headers_json, response_body FROM api_idempotency_records WHERE scope = ?;

-- name: RenewAPIIdempotencyRecord :execrows
UPDATE api_idempotency_records
SET lease_expires_at = sqlc.arg(new_lease_expires_at), updated_at = sqlc.arg(updated_at)
WHERE scope = sqlc.arg(scope) AND request_digest = sqlc.arg(request_digest)
  AND owner_id = sqlc.arg(owner_id) AND lease_generation = sqlc.arg(lease_generation)
  AND state = 'pending' AND julianday(lease_expires_at) > julianday('now');

-- name: CompleteAPIIdempotencyRecord :execrows
UPDATE api_idempotency_records
SET state = 'completed', response_status = sqlc.arg(response_status),
  response_headers_json = sqlc.arg(response_headers_json), response_body = sqlc.arg(response_body),
  updated_at = sqlc.arg(updated_at)
WHERE scope = sqlc.arg(scope) AND request_digest = sqlc.arg(request_digest)
  AND owner_id = sqlc.arg(owner_id) AND lease_generation = sqlc.arg(lease_generation)
  AND state = 'pending' AND julianday(lease_expires_at) > julianday('now');

-- name: MarkAPIIdempotencyRecordIndeterminate :execrows
UPDATE api_idempotency_records
SET state = 'completed', response_status = 409,
  response_headers_json = '{"Content-Type":["application/problem+json"]}',
  response_body = '{"code":"IDEMPOTENCY_OUTCOME_UNKNOWN","detail":"The original request outcome is indeterminate and will not be executed again"}',
  updated_at = sqlc.arg(updated_at)
WHERE scope = sqlc.arg(scope) AND request_digest = sqlc.arg(request_digest)
  AND owner_id = sqlc.arg(owner_id) AND lease_generation = sqlc.arg(lease_generation)
  AND state = 'pending';

-- name: AbandonAPIIdempotencyRecord :execrows
DELETE FROM api_idempotency_records
WHERE scope = sqlc.arg(scope) AND request_digest = sqlc.arg(request_digest)
  AND owner_id = sqlc.arg(owner_id) AND lease_generation = sqlc.arg(lease_generation)
  AND state = 'pending' AND julianday(lease_expires_at) > julianday('now');
