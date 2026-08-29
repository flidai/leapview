-- name: InsertJob :exec
INSERT INTO jobs.job (id, kind, workload_class, principal_id, group_ids, partition_key,
 resource_kind, resource_id, estimated_memory_bytes, payload, request_digest)
VALUES (sqlc.arg(id), sqlc.arg(kind), sqlc.arg(workload_class), sqlc.arg(principal_id), sqlc.arg(group_ids), sqlc.arg(partition_key), sqlc.arg(resource_kind), sqlc.arg(resource_id), sqlc.arg(estimated_memory_bytes), sqlc.arg(payload)::jsonb, sqlc.arg(request_digest))
ON CONFLICT (id) DO NOTHING;

-- name: GetRequestDigest :one
SELECT request_digest FROM jobs.job WHERE id = sqlc.arg(id);

-- name: GetAttempt :one
SELECT job_id,attempt_number,fencing_generation,owner,outcome,finished_at,error
FROM jobs.attempt WHERE job_id=sqlc.arg(job_id) AND attempt_number=sqlc.arg(attempt_number) AND fencing_generation=sqlc.arg(fencing_generation);

-- name: GetJob :one
SELECT id, kind, workload_class, principal_id, group_ids, partition_key,
 resource_kind, resource_id, estimated_memory_bytes, payload, status,
 attempt_count, lease_owner, lease_expires_at, lease_generation,
 created_at, started_at, finished_at, error
FROM jobs.job WHERE id = sqlc.arg(id);

-- name: GetActiveRefreshJobs :many
SELECT j.id, j.kind, j.workload_class, j.principal_id, j.group_ids, j.partition_key,
 j.resource_kind, j.resource_id, j.estimated_memory_bytes, j.payload, j.status,
 j.attempt_count, j.lease_owner, j.lease_expires_at, j.lease_generation,
 j.created_at, j.started_at, j.finished_at, j.error
FROM jobs.job j
WHERE j.resource_kind=sqlc.arg(resource_kind) AND j.status IN ('queued','running')
  AND (sqlc.arg(after_created)::timestamptz='epoch'::timestamptz OR (j.created_at,j.id)>(sqlc.arg(after_created)::timestamptz,sqlc.arg(after_id)::text))
ORDER BY j.created_at,j.id LIMIT sqlc.arg(page_limit);

-- name: ListCandidates :many
SELECT j.id, j.kind, j.workload_class, j.principal_id, j.group_ids, j.partition_key,
 j.resource_kind, j.resource_id, j.estimated_memory_bytes, j.payload, j.status,
 j.attempt_count, j.lease_owner, j.lease_expires_at, j.lease_generation,
 j.created_at, j.started_at, j.finished_at, j.error
FROM jobs.job j
WHERE j.workload_class = sqlc.arg(workload_class) AND j.available_at <= clock_timestamp()
  AND (j.status = 'queued' OR (j.status = 'running' AND j.lease_expires_at <= clock_timestamp()))
  AND j.id = (
    SELECT h.id FROM jobs.job h
    WHERE h.principal_id = j.principal_id AND h.partition_key = j.partition_key AND h.workload_class = sqlc.arg(workload_class)
      AND h.available_at <= clock_timestamp()
      AND (h.status = 'queued' OR (h.status = 'running' AND h.lease_expires_at <= clock_timestamp()))
    ORDER BY h.created_at, h.id LIMIT 1
  )
ORDER BY j.created_at, j.id LIMIT sqlc.arg(page_limit);

-- name: ListCandidatesByResourceKind :many
SELECT j.id, j.kind, j.workload_class, j.principal_id, j.group_ids, j.partition_key,
 j.resource_kind, j.resource_id, j.estimated_memory_bytes, j.payload, j.status,
 j.attempt_count, j.lease_owner, j.lease_expires_at, j.lease_generation,
 j.created_at, j.started_at, j.finished_at, j.error
FROM jobs.job j
WHERE j.workload_class = sqlc.arg(workload_class) AND j.resource_kind = sqlc.arg(resource_kind) AND j.available_at <= clock_timestamp()
  AND (j.status = 'queued' OR (j.status = 'running' AND j.lease_expires_at <= clock_timestamp()))
  AND (sqlc.narg(after_created)::timestamptz IS NULL OR (j.created_at,j.id) > (sqlc.narg(after_created)::timestamptz,sqlc.narg(after_id)::text))
  AND j.id = (
    SELECT h.id FROM jobs.job h
    WHERE h.principal_id = j.principal_id AND h.partition_key = j.partition_key AND h.workload_class = sqlc.arg(workload_class) AND h.resource_kind = sqlc.arg(resource_kind)
      AND h.available_at <= clock_timestamp()
      AND (h.status = 'queued' OR (h.status = 'running' AND h.lease_expires_at <= clock_timestamp()))
    ORDER BY h.created_at, h.id LIMIT 1
  )
ORDER BY j.created_at, j.id LIMIT sqlc.arg(page_limit);

-- name: ClaimByID :one
WITH candidate AS (
    SELECT j.id, j.status, j.attempt_count, j.lease_generation
    FROM jobs.job j
    WHERE j.id = sqlc.arg(id) AND j.workload_class = sqlc.arg(workload_class) AND j.available_at <= clock_timestamp()
      AND (j.status = 'queued' OR (j.status = 'running' AND j.lease_expires_at <= clock_timestamp()))
      AND NOT EXISTS (
          SELECT 1 FROM jobs.job h
          WHERE h.principal_id = j.principal_id
            AND h.partition_key = j.partition_key
            AND h.workload_class = j.workload_class
            AND h.available_at <= clock_timestamp()
            AND (h.status = 'queued' OR (h.status = 'running' AND h.lease_expires_at <= clock_timestamp()))
            AND (h.created_at,h.id) < (j.created_at,j.id)
      )
    ORDER BY j.created_at, j.id LIMIT 1 FOR UPDATE SKIP LOCKED
), expired_attempt AS (
    UPDATE jobs.attempt a
    SET finished_at = clock_timestamp(),
        outcome = CASE WHEN c.attempt_count >= (SELECT max_attempts FROM jobs.job WHERE id = c.id) THEN 'failed' ELSE 'expired' END,
        error = CASE WHEN c.attempt_count >= (SELECT max_attempts FROM jobs.job WHERE id = c.id) THEN '{"code":"MAX_ATTEMPTS_EXCEEDED"}'::jsonb ELSE a.error END,
        retry_at = NULL
    FROM candidate c
    WHERE c.status = 'running' AND a.job_id = c.id AND a.attempt_number = c.attempt_count
      AND a.fencing_generation = c.lease_generation AND a.outcome = 'running'
      AND a.owner = (SELECT j.lease_owner FROM jobs.job j WHERE j.id = c.id)
    RETURNING a.job_id
), transitioned AS (
    UPDATE jobs.job j
    SET status = CASE WHEN j.attempt_count >= j.max_attempts THEN 'failed' ELSE 'running' END,
        started_at = CASE WHEN j.attempt_count >= j.max_attempts THEN j.started_at ELSE COALESCE(j.started_at, clock_timestamp()) END,
        finished_at = CASE WHEN j.attempt_count >= j.max_attempts THEN clock_timestamp() ELSE j.finished_at END,
        lease_owner = CASE WHEN j.attempt_count >= j.max_attempts THEN '' ELSE sqlc.arg(owner) END,
        lease_expires_at = CASE WHEN j.attempt_count >= j.max_attempts THEN NULL ELSE clock_timestamp() + (sqlc.arg(lease_microseconds)::bigint * interval '1 microsecond') END,
        attempt_count = CASE WHEN j.attempt_count >= j.max_attempts THEN j.attempt_count ELSE j.attempt_count + 1 END,
        lease_generation = CASE WHEN j.attempt_count >= j.max_attempts THEN j.lease_generation ELSE j.lease_generation + 1 END,
        error = CASE WHEN j.attempt_count >= j.max_attempts THEN '{"code":"MAX_ATTEMPTS_EXCEEDED"}'::jsonb ELSE j.error END
    FROM candidate c
    WHERE j.id = c.id AND (c.status = 'queued' OR EXISTS (SELECT 1 FROM expired_attempt e WHERE e.job_id = c.id))
    RETURNING j.id, j.kind, j.workload_class, j.principal_id, j.group_ids, j.partition_key,
      j.resource_kind, j.resource_id, j.estimated_memory_bytes, j.payload, j.status,
      j.attempt_count, j.lease_owner, j.lease_expires_at, j.lease_generation,
      j.created_at, j.started_at, j.finished_at, j.error
), claimed AS (SELECT * FROM transitioned WHERE status = 'running'), recorded AS (
    INSERT INTO jobs.attempt (job_id, attempt_number, fencing_generation, owner, lease_expires_at)
    SELECT id, attempt_count, lease_generation, lease_owner, lease_expires_at FROM claimed
    RETURNING job_id
)
SELECT id, kind, workload_class, principal_id, group_ids, partition_key,
 resource_kind, resource_id, estimated_memory_bytes, payload, status,
 attempt_count, lease_owner, lease_expires_at, lease_generation,
 created_at, started_at, finished_at, error
FROM claimed;

-- name: Renew :one
WITH changed AS (
    UPDATE jobs.job j SET lease_expires_at = GREATEST(j.lease_expires_at, clock_timestamp() + (sqlc.arg(lease_microseconds)::bigint * interval '1 microsecond'))
    WHERE j.id = sqlc.arg(id) AND j.status = 'running' AND j.lease_owner = sqlc.arg(owner) AND j.lease_generation = sqlc.arg(generation)
      AND j.lease_expires_at > clock_timestamp()
      AND EXISTS (SELECT 1 FROM jobs.attempt a WHERE a.job_id = j.id AND a.attempt_number = j.attempt_count AND a.fencing_generation = j.lease_generation AND a.owner = j.lease_owner AND a.outcome = 'running')
    RETURNING j.id, j.attempt_count, j.lease_generation, j.lease_expires_at
), attempt_changed AS (
    UPDATE jobs.attempt a SET lease_expires_at = c.lease_expires_at
    FROM changed c WHERE a.job_id = c.id AND a.attempt_number = c.attempt_count AND a.fencing_generation = c.lease_generation AND a.owner = sqlc.arg(owner) AND a.outcome = 'running'
    RETURNING a.job_id
)
SELECT count(*)::bigint FROM attempt_changed;

-- name: Terminal :one
WITH changed AS (
    UPDATE jobs.job j SET status = sqlc.arg(outcome), finished_at = clock_timestamp(), lease_owner = '', lease_expires_at = NULL, error = sqlc.arg(error)::jsonb
    WHERE j.id = sqlc.arg(id) AND j.status = 'running' AND j.lease_owner = sqlc.arg(owner) AND j.lease_generation = sqlc.arg(generation) AND j.lease_expires_at > clock_timestamp()
      AND EXISTS (SELECT 1 FROM jobs.attempt a WHERE a.job_id = j.id AND a.attempt_number = j.attempt_count AND a.fencing_generation = j.lease_generation AND a.owner = j.lease_owner AND a.outcome = 'running')
    RETURNING j.id, j.attempt_count, j.lease_generation
), attempt_changed AS (
    UPDATE jobs.attempt a SET finished_at = clock_timestamp(), outcome = sqlc.arg(outcome), error = sqlc.arg(error)::jsonb
    FROM changed c WHERE a.job_id = c.id AND a.attempt_number = c.attempt_count AND a.fencing_generation = c.lease_generation AND a.owner = sqlc.arg(owner) AND a.outcome = 'running'
    RETURNING a.job_id
)
SELECT count(*)::bigint FROM attempt_changed;

-- name: Retry :one
WITH changed AS (
    UPDATE jobs.job j SET status = 'queued', available_at = clock_timestamp() + (sqlc.arg(delay_microseconds)::bigint * interval '1 microsecond'), lease_owner = '', lease_expires_at = NULL, error = sqlc.arg(error)::jsonb
    WHERE j.id = sqlc.arg(id) AND j.status = 'running' AND j.lease_owner = sqlc.arg(owner) AND j.lease_generation = sqlc.arg(generation) AND j.lease_expires_at > clock_timestamp() AND j.attempt_count < j.max_attempts
      AND EXISTS (SELECT 1 FROM jobs.attempt a WHERE a.job_id = j.id AND a.attempt_number = j.attempt_count AND a.fencing_generation = j.lease_generation AND a.owner = j.lease_owner AND a.outcome = 'running')
    RETURNING j.id, j.attempt_count, j.lease_generation
), attempt_changed AS (
    UPDATE jobs.attempt a SET finished_at = clock_timestamp(), outcome = 'retrying', retry_at = clock_timestamp() + (sqlc.arg(delay_microseconds)::bigint * interval '1 microsecond'), error = sqlc.arg(error)::jsonb
    FROM changed c WHERE a.job_id = c.id AND a.attempt_number = c.attempt_count AND a.fencing_generation = c.lease_generation AND a.owner = sqlc.arg(owner) AND a.outcome = 'running'
    RETURNING a.job_id
)
SELECT count(*)::bigint FROM attempt_changed;

-- name: LockJobForCancel :one
SELECT status, attempt_count, lease_generation FROM jobs.job WHERE id=sqlc.arg(id) FOR UPDATE;

-- name: LockAttemptForCancel :one
SELECT outcome, finished_at, retry_at FROM jobs.attempt WHERE job_id=sqlc.arg(job_id) AND attempt_number=sqlc.arg(attempt_number) AND fencing_generation=sqlc.arg(fencing_generation) FOR UPDATE;

-- name: CancelAttempt :execresult
UPDATE jobs.attempt SET finished_at=clock_timestamp(),outcome='cancelled',retry_at=NULL,error='{"code":"JOB_CANCELLED"}'::jsonb WHERE job_id=sqlc.arg(job_id) AND attempt_number=sqlc.arg(attempt_number) AND fencing_generation=sqlc.arg(fencing_generation) AND outcome='retrying';

-- name: CancelJob :execresult
UPDATE jobs.job SET status='cancelled',finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL,error='{"code":"JOB_CANCELLED"}'::jsonb WHERE id=sqlc.arg(id) AND status='queued';

-- name: LockJobReconcile :one
SELECT status,attempt_count,lease_generation,lease_owner,error FROM jobs.job WHERE id=sqlc.arg(id) FOR UPDATE;

-- name: LatestAttemptForReconcile :one
SELECT job_id,attempt_number,fencing_generation,owner,outcome,finished_at,retry_at,error FROM jobs.attempt WHERE job_id=sqlc.arg(job_id) ORDER BY attempt_number DESC LIMIT 1 FOR UPDATE;

-- name: CloseRetryingAttempt :execresult
UPDATE jobs.attempt SET finished_at=clock_timestamp(),outcome=sqlc.arg(outcome),retry_at=NULL,error=sqlc.arg(error)::jsonb WHERE job_id=sqlc.arg(job_id) AND attempt_number=sqlc.arg(attempt_number) AND fencing_generation=sqlc.arg(fencing_generation) AND outcome='retrying';

-- name: CloseRunningAttempt :execresult
UPDATE jobs.attempt SET finished_at=clock_timestamp(),outcome=sqlc.arg(outcome),error=sqlc.arg(error)::jsonb WHERE job_id=sqlc.arg(job_id) AND attempt_number=sqlc.arg(attempt_number) AND fencing_generation=sqlc.arg(fencing_generation) AND outcome='running';

-- name: ReconcileJob :execresult
UPDATE jobs.job SET status=sqlc.arg(status),finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL,error=sqlc.arg(error)::jsonb WHERE id=sqlc.arg(id) AND status IN ('queued','running');

-- name: LockJobQuarantine :one
SELECT status,attempt_count,lease_generation,error FROM jobs.job WHERE id=sqlc.arg(id) FOR UPDATE;

-- name: LockRetryingAttempt :one
SELECT outcome,finished_at,retry_at FROM jobs.attempt WHERE job_id=sqlc.arg(job_id) AND attempt_number=sqlc.arg(attempt_number) AND fencing_generation=sqlc.arg(fencing_generation) FOR UPDATE;

-- name: QuarantineAttempt :execresult
UPDATE jobs.attempt SET finished_at=clock_timestamp(),outcome='cancelled',retry_at=NULL,error=sqlc.arg(error)::jsonb WHERE job_id=sqlc.arg(job_id) AND attempt_number=sqlc.arg(attempt_number) AND fencing_generation=sqlc.arg(fencing_generation) AND outcome='retrying';

-- name: QuarantineJob :execresult
UPDATE jobs.job SET status='cancelled',finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL,error=sqlc.arg(error)::jsonb WHERE id=sqlc.arg(id) AND status='queued';

-- name: CancelClaimed :one
WITH changed AS (
    UPDATE jobs.job j SET status='cancelled',finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL,error='{"code":"JOB_CANCELLED"}'::jsonb
    WHERE j.id=sqlc.arg(id) AND j.status='running' AND j.lease_owner=sqlc.arg(owner) AND j.lease_generation=sqlc.arg(generation) AND j.lease_expires_at>clock_timestamp()
      AND EXISTS (SELECT 1 FROM jobs.attempt a WHERE a.job_id=j.id AND a.attempt_number=j.attempt_count AND a.fencing_generation=j.lease_generation AND a.owner=j.lease_owner AND a.outcome='running')
    RETURNING j.id,j.attempt_count,j.lease_generation
), attempt_changed AS (
    UPDATE jobs.attempt a SET finished_at=clock_timestamp(),outcome='cancelled',error='{"code":"JOB_CANCELLED"}'::jsonb
    FROM changed c WHERE a.job_id=c.id AND a.attempt_number=c.attempt_count AND a.fencing_generation=c.lease_generation AND a.owner=sqlc.arg(owner) AND a.outcome='running'
    RETURNING a.job_id
)
SELECT count(*)::bigint FROM attempt_changed;

-- name: LockJobSupersede :one
SELECT status,attempt_count,lease_generation,lease_owner FROM jobs.job WHERE id=sqlc.arg(id) FOR UPDATE;

-- name: LockAttemptSupersede :one
SELECT owner,outcome,finished_at,retry_at FROM jobs.attempt WHERE job_id=sqlc.arg(job_id) AND attempt_number=sqlc.arg(attempt_number) AND fencing_generation=sqlc.arg(fencing_generation) FOR UPDATE;

-- name: SupersedeAttempt :execresult
UPDATE jobs.attempt SET finished_at=clock_timestamp(),outcome='cancelled',retry_at=NULL,error='{"code":"REFRESH_SUPERSEDED"}'::jsonb WHERE job_id=sqlc.arg(job_id) AND attempt_number=sqlc.arg(attempt_number) AND fencing_generation=sqlc.arg(fencing_generation) AND outcome IN ('running','retrying');

-- name: SupersedeJob :execresult
UPDATE jobs.job SET status='cancelled',finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL,error='{"code":"REFRESH_SUPERSEDED"}'::jsonb WHERE id=sqlc.arg(id) AND status IN ('queued','running');

-- name: ListEvents :many
SELECT event_id, resource_kind, resource_id, event_type, data, created_at FROM jobs.event WHERE resource_kind=sqlc.arg(resource_kind) AND resource_id=sqlc.arg(resource_id) AND event_id>sqlc.arg(after_id) ORDER BY event_id LIMIT sqlc.arg(page_limit);

-- name: Prune :one
SELECT jobs.prune(sqlc.arg(before), sqlc.arg(batch_limit));

-- name: Observe :many
SELECT id, kind, workload_class, principal_id, status, health::text, attempt_count, max_attempts, lease_owner, lease_expires_at, available_at, retry_count, expired_count, last_retry_at::timestamptz
FROM jobs.job_observability WHERE workload_class=sqlc.arg(workload_class) ORDER BY available_at,id LIMIT sqlc.arg(page_limit);

-- name: EnsureEventSequence :exec
INSERT INTO jobs.event_sequence(resource_kind,resource_id,next_event_id) VALUES (sqlc.arg(resource_kind),sqlc.arg(resource_id),1) ON CONFLICT (resource_kind,resource_id) DO NOTHING;

-- name: LockEventSequence :one
SELECT next_event_id FROM jobs.event_sequence WHERE resource_kind=sqlc.arg(resource_kind) AND resource_id=sqlc.arg(resource_id) FOR UPDATE;

-- name: GetEventByKey :one
SELECT event_id,resource_kind,resource_id,event_type,data,created_at FROM jobs.event WHERE resource_kind=sqlc.arg(resource_kind) AND resource_id=sqlc.arg(resource_id) AND event_key=sqlc.arg(event_key);

-- name: NextEventID :one
UPDATE jobs.event_sequence SET next_event_id=next_event_id+1 WHERE resource_kind=sqlc.arg(resource_kind) AND resource_id=sqlc.arg(resource_id) RETURNING (next_event_id-1)::bigint;

-- name: InsertEvent :one
INSERT INTO jobs.event(resource_kind,resource_id,event_id,event_type,event_key,data) VALUES (sqlc.arg(resource_kind),sqlc.arg(resource_id),sqlc.arg(event_id),sqlc.arg(event_type),sqlc.arg(event_key),sqlc.arg(data)::jsonb)
RETURNING event_id,resource_kind,resource_id,event_type,data,created_at;
