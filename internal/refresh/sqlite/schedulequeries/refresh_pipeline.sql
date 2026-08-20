-- name: ListRefreshPipelineSchedules :many
SELECT pipeline_id, trigger_id, semantic_model_id, cron, timezone,
       starting_deadline_seconds, concurrency_policy, artifact_digest, next_run_at
FROM refresh_pipeline_schedules
WHERE project_id = ? AND environment = ? AND generation_id = ?;

-- name: DeleteRefreshPipelineSchedules :exec
DELETE FROM refresh_pipeline_schedules
WHERE project_id = ? AND environment = ? AND generation_id = ?;

-- name: GetRefreshPipelineNextRun :one
SELECT next_run_at
FROM refresh_pipeline_schedules
WHERE project_id = ? AND environment = ? AND generation_id = ? AND pipeline_id = ?
ORDER BY next_run_at
LIMIT 1;

-- name: CreateRefreshPipelineSchedule :exec
INSERT INTO refresh_pipeline_schedules (
  project_id, environment, pipeline_id, trigger_id, semantic_model_id,
  generation_id, artifact_digest, cron, timezone, starting_deadline_seconds,
  concurrency_policy, next_run_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP);

-- name: ListDueRefreshPipelineSchedules :many
SELECT project_id, environment, pipeline_id, trigger_id, semantic_model_id,
       generation_id, cron, timezone, starting_deadline_seconds,
       concurrency_policy, artifact_digest, next_run_at
FROM refresh_pipeline_schedules
WHERE project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND generation_id = sqlc.arg(generation_id)
  AND next_run_at <= sqlc.arg(next_run_at)
ORDER BY next_run_at, pipeline_id, cron, trigger_id;

-- name: RequeueAbandonedRefreshPipelineSchedules :exec
UPDATE refresh_pipeline_schedules
SET next_run_at = COALESCE((
  SELECT MIN(occurrence.scheduled_at)
  FROM refresh_pipeline_occurrences occurrence
  WHERE occurrence.project_id = refresh_pipeline_schedules.project_id
    AND occurrence.environment = refresh_pipeline_schedules.environment
    AND occurrence.pipeline_id = refresh_pipeline_schedules.pipeline_id
    AND occurrence.status = 'claimed'
    AND occurrence.claimed_at <= sqlc.arg(claimed_before)
), next_run_at), updated_at = CURRENT_TIMESTAMP
WHERE refresh_pipeline_schedules.project_id = sqlc.arg(project_id)
  AND refresh_pipeline_schedules.environment = sqlc.arg(environment)
  AND refresh_pipeline_schedules.generation_id = sqlc.arg(generation_id)
  AND EXISTS (
    SELECT 1 FROM refresh_pipeline_occurrences occurrence
    WHERE occurrence.project_id = refresh_pipeline_schedules.project_id
      AND occurrence.environment = refresh_pipeline_schedules.environment
      AND occurrence.pipeline_id = refresh_pipeline_schedules.pipeline_id
      AND occurrence.status = 'claimed'
      AND occurrence.claimed_at <= sqlc.arg(claimed_before)
  );

-- name: RequeueAbandonedRefreshPipelineOccurrences :exec
UPDATE refresh_pipeline_occurrences
SET status = 'pending', outcome = 'claim_expired', terminal_reason = 'claim expired',
    claimed_at = NULL, outcome_at = CURRENT_TIMESTAMP
WHERE project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND status = 'claimed' AND claimed_at <= sqlc.arg(claimed_before);

-- name: AdvanceRefreshPipelineSchedule :exec
UPDATE refresh_pipeline_schedules SET next_run_at = ?, updated_at = CURRENT_TIMESTAMP
WHERE project_id = ? AND environment = ? AND pipeline_id = ? AND generation_id = ? AND trigger_id = ?;

-- name: ClaimRefreshPipelineOccurrence :execresult
INSERT OR IGNORE INTO refresh_pipeline_occurrences (
  project_id, environment, pipeline_id, generation_id, artifact_digest,
  scheduled_at, timezone, matching_schedule_ids_json, status, outcome, claimed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'claimed', 'pending', ?);

-- name: ClaimPendingRefreshPipelineOccurrence :execresult
UPDATE refresh_pipeline_occurrences
SET status = 'claimed', outcome = 'pending', terminal_reason = '', claimed_at = ?, outcome_at = NULL
WHERE project_id = ? AND environment = ? AND pipeline_id = ?
  AND scheduled_at = ? AND status = 'pending';

-- name: GetRefreshPipelineOccurrence :one
SELECT project_id, environment, pipeline_id, generation_id, artifact_digest,
       scheduled_at, timezone, matching_schedule_ids_json, status, outcome
FROM refresh_pipeline_occurrences
WHERE project_id = ? AND environment = ? AND pipeline_id = ? AND scheduled_at = ?;

-- name: AttachRefreshPipelineRun :execresult
UPDATE refresh_pipeline_occurrences
SET run_id = ?, status = 'attached', outcome = 'admitted', terminal_reason = '',
    outcome_at = CURRENT_TIMESTAMP
WHERE project_id = ? AND environment = ? AND pipeline_id = ?
  AND scheduled_at = ? AND status = 'claimed' AND run_id IS NULL;

-- name: ReleaseRefreshPipelineOccurrence :execresult
UPDATE refresh_pipeline_occurrences
SET status = 'pending', outcome = 'dispatch_failed', terminal_reason = ?,
    claimed_at = NULL, outcome_at = CURRENT_TIMESTAMP
WHERE project_id = ? AND environment = ? AND pipeline_id = ?
  AND scheduled_at = ? AND status = 'claimed' AND run_id IS NULL;

-- name: MarkRefreshPipelineOccurrenceOutcome :execresult
UPDATE refresh_pipeline_occurrences
SET status = ?, outcome = ?, terminal_reason = ?, outcome_at = CURRENT_TIMESTAMP
WHERE project_id = ? AND environment = ? AND pipeline_id = ? AND scheduled_at = ?;

-- name: RetryRefreshPipelineSchedules :exec
UPDATE refresh_pipeline_schedules SET next_run_at = sqlc.arg(retry_at), updated_at = CURRENT_TIMESTAMP
WHERE project_id = sqlc.arg(project_id) AND environment = sqlc.arg(environment)
  AND pipeline_id = sqlc.arg(pipeline_id) AND next_run_at > sqlc.arg(retry_at);

-- name: UpsertSemanticModelDataVersion :exec
INSERT INTO semantic_model_data_versions (
  project_id, environment, semantic_model_id, snapshot_id, generation_id, refreshed_at, source, pipeline_id, run_id
) VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(sqlc.arg(pipeline_id), ''), NULLIF(sqlc.arg(run_id), ''))
ON CONFLICT (project_id, environment, semantic_model_id, generation_id) DO UPDATE SET
  snapshot_id = excluded.snapshot_id, generation_id = excluded.generation_id,
  refreshed_at = excluded.refreshed_at, source = excluded.source,
  pipeline_id = excluded.pipeline_id, run_id = excluded.run_id;

-- name: GetSemanticModelDataVersion :one
SELECT project_id, environment, semantic_model_id, snapshot_id, generation_id, refreshed_at, source,
       COALESCE(pipeline_id, '') AS pipeline_id, COALESCE(run_id, '') AS run_id
FROM semantic_model_data_versions
WHERE project_id = ? AND environment = ? AND generation_id = ? AND semantic_model_id = ?;
