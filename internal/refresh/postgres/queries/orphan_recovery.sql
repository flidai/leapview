-- Refresh-owned lease evidence used by the narrow River orphan reclaimer.

-- name: ListExpiredRefreshJobRuns :many
SELECT run_id, job_id, status, attempt_count, fence_generation, lease_owner
FROM refresh.run
WHERE job_id IS NOT NULL
  AND status IN ('running', 'prepared')
  AND lease_expires_at <= clock_timestamp()
ORDER BY lease_expires_at, run_id
LIMIT sqlc.arg(page_limit);

-- name: LockExpiredRefreshJobRun :one
SELECT run_id, job_id, status, attempt_count, fence_generation, lease_owner
FROM refresh.run
WHERE run_id = sqlc.arg(run_id)
  AND job_id = sqlc.arg(job_id)
  AND status IN ('running', 'prepared')
  AND lease_expires_at <= clock_timestamp()
FOR UPDATE;

-- name: LockCurrentRefreshAttemptForOrphanRescue :one
SELECT true AS current_attempt
FROM refresh.attempt
WHERE run_id = sqlc.arg(run_id)
  AND attempt_number = sqlc.arg(attempt_number)
  AND fence_generation = sqlc.arg(fence_generation)
  AND owner_id = sqlc.arg(owner_id)
  AND status = 'running'
  AND lease_expires_at <= clock_timestamp()
FOR UPDATE;
