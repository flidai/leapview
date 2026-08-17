-- Shared API async jobs and events.

-- name: EnqueueAPIAsyncJob :exec
INSERT INTO api_async_jobs (id, job_kind, workload_class, principal_id, group_ids_json, resource_kind, resource_id, estimated_memory_bytes, payload_json, request_digest, status)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued');

-- name: GetAPIAsyncJobDigest :one
SELECT request_digest FROM api_async_jobs WHERE id = ?;

-- name: GetAPIAsyncJob :one
SELECT id, job_kind, workload_class, principal_id, group_ids_json, resource_kind, resource_id, estimated_memory_bytes, payload_json, status, attempt_count, lease_owner, lease_generation,
  COALESCE(lease_expires_at, '') AS lease_expires_at, created_at, COALESCE(started_at, '') AS started_at,
  COALESCE(finished_at, '') AS finished_at, error_json
FROM api_async_jobs WHERE id = ?;

-- name: ListAPIAsyncJobCandidates :many
WITH eligible AS (
  SELECT id, job_kind, workload_class, principal_id, group_ids_json, resource_kind, resource_id, estimated_memory_bytes, payload_json, status,
    attempt_count, lease_owner, lease_generation, COALESCE(lease_expires_at, '') AS lease_expires_at, created_at,
    COALESCE(started_at, '') AS started_at, COALESCE(finished_at, '') AS finished_at, error_json,
    ROW_NUMBER() OVER (PARTITION BY principal_id ORDER BY created_at, id) AS principal_position
  FROM api_async_jobs
  WHERE workload_class = sqlc.arg(workload_class)
    AND (status = 'queued' OR (status = 'running' AND (lease_expires_at IS NULL OR lease_expires_at <= CURRENT_TIMESTAMP)))
)
SELECT id, job_kind, workload_class, principal_id, group_ids_json, resource_kind, resource_id, estimated_memory_bytes, payload_json, status,
  attempt_count, lease_owner, lease_generation, lease_expires_at, created_at, started_at, finished_at, error_json
FROM eligible
WHERE principal_position = 1
ORDER BY created_at, id
LIMIT sqlc.arg(result_limit);

-- name: ClaimAPIAsyncJobByID :one
UPDATE api_async_jobs SET status = 'running', started_at = COALESCE(started_at, CURRENT_TIMESTAMP),
  lease_owner = sqlc.arg(lease_owner), lease_expires_at = datetime('now', sqlc.arg(lease_modifier)),
  attempt_count = attempt_count + 1, lease_generation = lease_generation + 1
WHERE id = sqlc.arg(id)
  AND workload_class = sqlc.arg(workload_class)
  AND (status = 'queued' OR (status = 'running' AND (lease_expires_at IS NULL OR lease_expires_at <= CURRENT_TIMESTAMP)))
RETURNING id, job_kind, workload_class, principal_id, group_ids_json, resource_kind, resource_id, estimated_memory_bytes, payload_json, status, attempt_count, lease_owner, lease_generation,
  COALESCE(lease_expires_at, '') AS lease_expires_at, created_at, COALESCE(started_at, '') AS started_at,
  COALESCE(finished_at, '') AS finished_at, error_json;

-- name: RenewAPIAsyncJob :execrows
UPDATE api_async_jobs SET lease_expires_at = datetime('now', sqlc.arg(lease_modifier))
WHERE id = sqlc.arg(id) AND status = 'running' AND lease_owner = sqlc.arg(lease_owner)
  AND lease_generation = sqlc.arg(lease_generation)
  AND lease_expires_at IS NOT NULL AND lease_expires_at > CURRENT_TIMESTAMP;

-- name: CompleteAPIAsyncJob :execrows
UPDATE api_async_jobs SET status = 'succeeded', finished_at = CURRENT_TIMESTAMP,
  lease_owner = '', lease_expires_at = NULL, error_json = '{}'
WHERE id = sqlc.arg(id) AND status = 'running' AND lease_owner = sqlc.arg(lease_owner)
  AND lease_generation = sqlc.arg(lease_generation)
  AND lease_expires_at IS NOT NULL AND lease_expires_at > CURRENT_TIMESTAMP;

-- name: FailAPIAsyncJob :execrows
UPDATE api_async_jobs SET status = 'failed', finished_at = CURRENT_TIMESTAMP,
  lease_owner = '', lease_expires_at = NULL, error_json = ?
WHERE id = sqlc.arg(id) AND status = 'running' AND lease_owner = sqlc.arg(lease_owner)
  AND lease_generation = sqlc.arg(lease_generation)
  AND lease_expires_at IS NOT NULL AND lease_expires_at > CURRENT_TIMESTAMP;

-- name: CancelQueuedAPIAsyncJob :execrows
UPDATE api_async_jobs SET status = 'cancelled', finished_at = CURRENT_TIMESTAMP,
  lease_owner = '', lease_expires_at = NULL WHERE id = ? AND status = 'queued';

-- name: CancelClaimedAPIAsyncJob :execrows
UPDATE api_async_jobs SET status = 'cancelled', finished_at = CURRENT_TIMESTAMP,
  lease_owner = '', lease_expires_at = NULL
WHERE id = sqlc.arg(id) AND status = 'running' AND lease_owner = sqlc.arg(lease_owner)
  AND lease_generation = sqlc.arg(lease_generation)
  AND lease_expires_at IS NOT NULL AND lease_expires_at > CURRENT_TIMESTAMP;

-- name: AppendAPIAsyncEvent :one
INSERT INTO api_async_events (resource_kind, resource_id, event_id, event_type, data_json)
SELECT sqlc.arg(resource_kind), sqlc.arg(resource_id), COALESCE(MAX(event_id), 0) + 1,
  sqlc.arg(event_type), sqlc.arg(data_json)
FROM api_async_events existing
WHERE existing.resource_kind = sqlc.arg(resource_kind) AND existing.resource_id = sqlc.arg(resource_id)
RETURNING event_id, resource_kind, resource_id, event_type, data_json, created_at;

-- name: AppendAPIAsyncWorkflowEvent :one
INSERT INTO api_async_events (resource_kind, resource_id, event_id, event_type, data_json, event_key)
SELECT sqlc.arg(resource_kind), sqlc.arg(resource_id), COALESCE(MAX(event_id), 0) + 1,
  sqlc.arg(event_type), sqlc.arg(data_json), sqlc.arg(event_key)
FROM api_async_events existing
WHERE existing.resource_kind = sqlc.arg(resource_kind) AND existing.resource_id = sqlc.arg(resource_id)
ON CONFLICT(resource_kind, resource_id, event_key) WHERE event_key <> '' DO UPDATE SET event_key = excluded.event_key
RETURNING event_id, resource_kind, resource_id, event_type, data_json, created_at;

-- name: ListAPIAsyncEvents :many
SELECT event_id, resource_kind, resource_id, event_type, data_json, created_at FROM api_async_events
WHERE resource_kind = ? AND resource_id = ? AND event_id > ? ORDER BY event_id LIMIT ?;

-- name: GetAPIAsyncEvent :one
SELECT event_id, resource_kind, resource_id, event_type, data_json, created_at FROM api_async_events
WHERE resource_kind = ? AND resource_id = ? AND event_id = ?;
