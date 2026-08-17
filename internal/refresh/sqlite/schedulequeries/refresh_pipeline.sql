-- name: ListRefreshPipelineSchedules :many
SELECT pipeline_id, cron, timezone, artifact_digest, next_run_at
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
  project_id, environment, pipeline_id, semantic_model_id, generation_id, artifact_digest, cron, timezone, next_run_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP);

-- name: ListDueRefreshPipelineSchedules :many
SELECT project_id, environment, pipeline_id, semantic_model_id, generation_id, cron, timezone, artifact_digest, next_run_at
FROM refresh_pipeline_schedules
WHERE project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND generation_id = sqlc.arg(generation_id)
  AND next_run_at <= sqlc.arg(next_run_at)
ORDER BY project_id, environment, pipeline_id, next_run_at;

-- name: RequeueAbandonedRefreshPipelineSchedules :exec
UPDATE refresh_pipeline_schedules
SET next_run_at = (
  SELECT MIN(occurrence.scheduled_at)
  FROM refresh_pipeline_occurrences occurrence
  WHERE occurrence.project_id = refresh_pipeline_schedules.project_id
    AND occurrence.environment = refresh_pipeline_schedules.environment
    AND occurrence.pipeline_id = refresh_pipeline_schedules.pipeline_id
    AND occurrence.generation_id = refresh_pipeline_schedules.generation_id
    AND occurrence.artifact_digest = refresh_pipeline_schedules.artifact_digest
    AND occurrence.run_id IS NULL
    AND occurrence.claimed_at <= sqlc.arg(claimed_before)
), updated_at = CURRENT_TIMESTAMP
WHERE refresh_pipeline_schedules.project_id = sqlc.arg(project_id)
  AND refresh_pipeline_schedules.environment = sqlc.arg(environment)
  AND refresh_pipeline_schedules.generation_id = sqlc.arg(generation_id)
  AND EXISTS (
  SELECT 1
  FROM refresh_pipeline_occurrences occurrence
  WHERE occurrence.project_id = refresh_pipeline_schedules.project_id
    AND occurrence.environment = refresh_pipeline_schedules.environment
    AND occurrence.pipeline_id = refresh_pipeline_schedules.pipeline_id
    AND occurrence.generation_id = refresh_pipeline_schedules.generation_id
    AND occurrence.artifact_digest = refresh_pipeline_schedules.artifact_digest
    AND occurrence.run_id IS NULL
    AND occurrence.claimed_at <= sqlc.arg(claimed_before)
);

-- name: DeleteAbandonedRefreshPipelineOccurrences :exec
DELETE FROM refresh_pipeline_occurrences
WHERE project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND generation_id = sqlc.arg(generation_id)
  AND run_id IS NULL AND claimed_at <= sqlc.arg(claimed_before);

-- name: AdvanceRefreshPipelineSchedule :exec
UPDATE refresh_pipeline_schedules SET next_run_at = ?, updated_at = CURRENT_TIMESTAMP
WHERE project_id = ? AND environment = ? AND pipeline_id = ? AND generation_id = ? AND cron = ? AND timezone = ?;

-- name: ClaimRefreshPipelineOccurrence :execresult
INSERT OR IGNORE INTO refresh_pipeline_occurrences (
  project_id, environment, pipeline_id, generation_id, artifact_digest, scheduled_at, claimed_at
) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: AttachRefreshPipelineRun :execresult
UPDATE refresh_pipeline_occurrences SET run_id = ?
WHERE project_id = ? AND environment = ? AND pipeline_id = ? AND generation_id = ? AND scheduled_at = ?;

-- name: DeleteUnattachedRefreshPipelineOccurrence :execresult
DELETE FROM refresh_pipeline_occurrences
WHERE project_id = ? AND environment = ? AND pipeline_id = ? AND generation_id = ? AND scheduled_at = ? AND run_id IS NULL;

-- name: RetryRefreshPipelineSchedules :exec
UPDATE refresh_pipeline_schedules SET next_run_at = sqlc.arg(retry_at), updated_at = CURRENT_TIMESTAMP
WHERE project_id = sqlc.arg(project_id) AND environment = sqlc.arg(environment)
  AND pipeline_id = sqlc.arg(pipeline_id) AND generation_id = sqlc.arg(generation_id) AND artifact_digest = sqlc.arg(artifact_digest)
  AND next_run_at > sqlc.arg(retry_at);

-- name: UpsertSemanticModelDataVersion :exec
INSERT INTO semantic_model_data_versions (
  project_id, environment, semantic_model_id, snapshot_id, generation_id, refreshed_at, source, pipeline_id, run_id
) VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(sqlc.arg(pipeline_id), ''), NULLIF(sqlc.arg(run_id), ''))
ON CONFLICT (project_id, environment, semantic_model_id, generation_id) DO UPDATE SET
  snapshot_id = excluded.snapshot_id,
  generation_id = excluded.generation_id,
  refreshed_at = excluded.refreshed_at,
  source = excluded.source,
  pipeline_id = excluded.pipeline_id,
  run_id = excluded.run_id;

-- name: GetSemanticModelDataVersion :one
SELECT project_id, environment, semantic_model_id, snapshot_id, generation_id, refreshed_at, source,
       COALESCE(pipeline_id, '') AS pipeline_id, COALESCE(run_id, '') AS run_id
FROM semantic_model_data_versions
WHERE project_id = ? AND environment = ? AND generation_id = ? AND semantic_model_id = ?;
