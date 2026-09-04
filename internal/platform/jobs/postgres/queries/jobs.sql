-- Static PostgreSQL query leaves for the LeapView jobs product-history authority.

-- name: InsertJobHistory :one
INSERT INTO jobs.job_history
    (id, kind, workload_class, principal_id, group_ids, partition_key,
     resource_kind, resource_id, estimated_memory_bytes, payload, request_digest)
VALUES (sqlc.arg(id), sqlc.arg(kind), sqlc.arg(workload_class), sqlc.arg(principal_id),
        sqlc.arg(group_ids)::jsonb, sqlc.arg(partition_key), sqlc.arg(resource_kind),
        sqlc.arg(resource_id), sqlc.arg(estimated_memory_bytes),
        sqlc.arg(payload)::jsonb, sqlc.arg(request_digest))
ON CONFLICT (id) DO NOTHING
RETURNING true AS inserted;

-- name: GetJobRequestDigest :one
SELECT request_digest
FROM jobs.job_history
WHERE id = sqlc.arg(id);

-- name: UpdateRiverJobID :execrows
UPDATE jobs.job_history
SET river_job_id = sqlc.arg(river_job_id)
WHERE id = sqlc.arg(id) AND river_job_id IS NULL;

-- name: GetJob :one
SELECT id, kind, workload_class, principal_id, group_ids::text AS group_ids,
       partition_key, resource_kind, resource_id, estimated_memory_bytes,
       payload::text AS payload, request_digest, status, attempt_count,
       created_at, started_at, finished_at, river_job_id,
       COALESCE(error, 'null'::jsonb)::text AS error
FROM jobs.job_history
WHERE id = sqlc.arg(id);

-- name: GetRiverJobID :one
SELECT river_job_id
FROM jobs.job_history
WHERE id = sqlc.arg(id);

-- name: LockRiverJobFence :one
SELECT id, state::text AS state, attempt, attempted_by
FROM public.river_job
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: MarkJobRunning :execrows
UPDATE jobs.job_history
SET status = 'running',
    attempt_count = GREATEST(attempt_count, sqlc.arg(attempt)),
    started_at = COALESCE(started_at, clock_timestamp()),
    finished_at = NULL,
    error = NULL
WHERE id = sqlc.arg(id) AND status IN ('queued', 'running');

-- name: SetJobTerminalWithError :execrows
UPDATE jobs.job_history
SET status = sqlc.arg(status), finished_at = clock_timestamp(),
    error = sqlc.arg(problem)::jsonb
WHERE id = sqlc.arg(id) AND status IN ('queued', 'running')
  AND (sqlc.arg(fence_generation) = 0 OR attempt_count = sqlc.arg(fence_generation));

-- name: SetJobTerminal :execrows
UPDATE jobs.job_history
SET status = sqlc.arg(status), finished_at = clock_timestamp(), error = NULL
WHERE id = sqlc.arg(id) AND status IN ('queued', 'running')
  AND (sqlc.arg(fence_generation) = 0 OR attempt_count = sqlc.arg(fence_generation));

-- name: RequeueJobAfterFailure :execrows
UPDATE jobs.job_history
SET status = 'queued',
    attempt_count = GREATEST(attempt_count, sqlc.arg(attempt)),
    error = sqlc.arg(problem)::jsonb
WHERE id = sqlc.arg(id) AND status = 'running';

-- name: EnsureEventSequence :exec
INSERT INTO jobs.event_sequence(resource_kind, resource_id, next_event_id)
VALUES (sqlc.arg(resource_kind), sqlc.arg(resource_id), 1)
ON CONFLICT (resource_kind, resource_id) DO NOTHING;

-- name: LockEventSequence :one
SELECT next_event_id
FROM jobs.event_sequence
WHERE resource_kind = sqlc.arg(resource_kind) AND resource_id = sqlc.arg(resource_id)
FOR UPDATE;

-- name: GetEventByKey :one
SELECT event_id, event_type, data::text AS data, created_at
FROM jobs.event
WHERE resource_kind = sqlc.arg(resource_kind)
  AND resource_id = sqlc.arg(resource_id)
  AND event_key = sqlc.arg(event_key);

-- name: NextEventID :one
UPDATE jobs.event_sequence
SET next_event_id = next_event_id + 1
WHERE resource_kind = sqlc.arg(resource_kind) AND resource_id = sqlc.arg(resource_id)
RETURNING (next_event_id - 1)::bigint AS event_id;

-- name: InsertEvent :one
INSERT INTO jobs.event(resource_kind, resource_id, event_id, event_type, event_key, data)
VALUES (sqlc.arg(resource_kind), sqlc.arg(resource_id), sqlc.arg(event_id),
        sqlc.arg(event_type), sqlc.arg(event_key), sqlc.arg(data)::jsonb)
RETURNING event_id, created_at;

-- name: ListEvents :many
SELECT event_id, event_type, data::text AS data, created_at
FROM jobs.event
WHERE resource_kind = sqlc.arg(resource_kind)
  AND resource_id = sqlc.arg(resource_id)
  AND event_id > sqlc.arg(after_id)
ORDER BY event_id
LIMIT sqlc.arg(page_limit);

-- name: Prune :one
SELECT jobs.prune(sqlc.arg(before), sqlc.arg(batch_limit)) AS removed;

-- name: ReleasePartitionAdvisoryLock :exec
SELECT pg_advisory_unlock(hashtextextended(sqlc.arg(partition_key), 0));

-- name: TryPartitionAdvisoryLock :one
SELECT pg_try_advisory_lock(hashtextextended(sqlc.arg(partition_key), 0)) AS locked;

-- name: PartitionIsHead :one
SELECT NOT EXISTS (
    SELECT 1
    FROM jobs.job_history AS history
    WHERE history.partition_key = sqlc.arg(partition_key)
      AND history.status IN ('queued', 'running')
      AND (history.created_at, history.id) < (
          (SELECT target.created_at FROM jobs.job_history AS target WHERE target.id = sqlc.arg(id)),
          sqlc.arg(id)
      )
) AS is_head;
