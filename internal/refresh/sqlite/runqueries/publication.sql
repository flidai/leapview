-- Fenced refresh publication is a refresh-owned cross-table unit of work.

-- name: RefreshPublicationCandidate :one
SELECT id, project_id, environment, status, ducklake_snapshot_id
FROM serving_states
WHERE id = sqlc.arg(generation_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment);

-- name: RefreshPublicationFenceActive :one
SELECT EXISTS(
  SELECT 1
  FROM refresh_job_runs candidate
  JOIN refresh_jobs candidate_job ON candidate_job.id = candidate.job_id
  WHERE candidate.id = sqlc.arg(run_id)
    AND candidate.status = 'prepared'
    AND candidate_job.project_id = sqlc.arg(project_id)
    AND candidate_job.generation_id = sqlc.arg(generation_id)
    AND candidate.environment = sqlc.arg(environment)
    AND candidate.target_revision = sqlc.arg(target_revision)
    AND candidate_job.status = 'running'
    AND candidate_job.lease_owner = sqlc.arg(lease_owner)
    AND candidate_job.lease_revision = sqlc.arg(lease_revision)
    AND candidate_job.lease_expires_at IS NOT NULL
    AND candidate_job.lease_expires_at > CURRENT_TIMESTAMP
    AND NOT EXISTS (
      SELECT 1
      FROM refresh_job_runs newer
      JOIN refresh_jobs newer_job ON newer_job.id = newer.job_id
      WHERE newer_job.project_id = candidate_job.project_id
        AND newer_job.generation_id = candidate_job.generation_id
        AND newer.environment = candidate.environment
        AND newer.target_type = candidate.target_type
        AND newer.target_id = candidate.target_id
        AND newer.target_revision > candidate.target_revision
    )
) AS active;

-- name: DrainOtherRefreshServingStates :exec
UPDATE serving_states
SET status = 'draining', superseded_at = CURRENT_TIMESTAMP, error = ''
WHERE project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND id <> sqlc.arg(generation_id)
  AND status = 'active';

-- name: ActivateRefreshServingState :exec
UPDATE serving_states
SET status = 'active', activated_at = CURRENT_TIMESTAMP, error = ''
WHERE id = sqlc.arg(generation_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment);

-- name: SetRefreshActiveServingState :exec
INSERT INTO project_active_serving_states (project_id, environment, generation_id, updated_at)
VALUES (sqlc.arg(project_id), sqlc.arg(environment), sqlc.arg(generation_id), CURRENT_TIMESTAMP)
ON CONFLICT(project_id, environment) DO UPDATE SET
  generation_id = excluded.generation_id,
  updated_at = CURRENT_TIMESTAMP;

-- name: AdvanceRefreshSemanticModelDataVersions :exec
UPDATE semantic_model_data_versions
SET snapshot_id = sqlc.arg(snapshot_id), generation_id = sqlc.arg(generation_id)
WHERE project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND generation_id = sqlc.arg(generation_id)
  AND semantic_model_id <> sqlc.arg(semantic_model_id);

-- name: UpsertRefreshPublicationDataVersion :exec
INSERT INTO semantic_model_data_versions (
  project_id, environment, semantic_model_id, snapshot_id, generation_id, refreshed_at, source, pipeline_id, run_id
) VALUES (
  sqlc.arg(project_id), sqlc.arg(environment), sqlc.arg(semantic_model_id), sqlc.arg(snapshot_id),
  sqlc.arg(generation_id), sqlc.arg(refreshed_at), 'refresh', NULLIF(sqlc.arg(pipeline_id), ''), NULLIF(sqlc.arg(run_id), '')
)
ON CONFLICT (project_id, environment, semantic_model_id, generation_id) DO UPDATE SET
  snapshot_id = excluded.snapshot_id,
  generation_id = excluded.generation_id,
  refreshed_at = excluded.refreshed_at,
  source = excluded.source,
  pipeline_id = excluded.pipeline_id,
  run_id = excluded.run_id;

-- name: CompleteRefreshPublicationRun :execrows
UPDATE refresh_job_runs
SET status = 'succeeded', finished_at = CURRENT_TIMESTAMP, error = ''
  WHERE ((refresh_job_runs.id = @run_id AND refresh_job_runs.status = 'prepared')
    OR (refresh_job_runs.parent_run_id = @run_id AND refresh_job_runs.status IN ('queued', 'running', 'prepared')))
  AND refresh_job_runs.target_revision = sqlc.arg(target_revision)
  AND refresh_job_runs.job_id IN (
    SELECT refresh_jobs.id FROM refresh_jobs
    WHERE refresh_jobs.project_id = sqlc.arg(project_id)
      AND refresh_jobs.generation_id = sqlc.arg(generation_id)
      AND (
        refresh_jobs.id = (SELECT job_id FROM refresh_job_runs WHERE id = @run_id)
        OR refresh_jobs.id IN (
        SELECT child.job_id FROM refresh_job_runs child
        JOIN refresh_jobs child_job ON child_job.id = child.job_id
        WHERE child.parent_run_id = @run_id
          AND child.environment = sqlc.arg(environment)
          AND child_job.project_id = sqlc.arg(project_id)
          AND child_job.generation_id = sqlc.arg(generation_id)
        )
      )
  )
  AND EXISTS (
    SELECT 1 FROM refresh_jobs root_job
    WHERE root_job.id = (SELECT job_id FROM refresh_job_runs WHERE id = @run_id)
      AND root_job.project_id = sqlc.arg(project_id)
      AND root_job.generation_id = sqlc.arg(generation_id)
      AND EXISTS (SELECT 1 FROM refresh_job_runs root_run WHERE root_run.id = @run_id AND root_run.environment = sqlc.arg(environment))
      AND root_job.status = 'running' AND root_job.lease_owner = sqlc.arg(lease_owner)
      AND root_job.lease_revision = sqlc.arg(lease_revision)
      AND root_job.lease_expires_at IS NOT NULL AND root_job.lease_expires_at > CURRENT_TIMESTAMP
  );

-- name: CompleteRefreshPublicationJob :execrows
UPDATE refresh_jobs
SET status = 'succeeded', updated_at = CURRENT_TIMESTAMP, finished_at = CURRENT_TIMESTAMP,
    lease_owner = '', lease_expires_at = NULL, last_error = ''
  WHERE refresh_jobs.id IN (
    SELECT refresh_job_runs.job_id FROM refresh_job_runs
    JOIN refresh_jobs tree_job ON tree_job.id = refresh_job_runs.job_id
    WHERE tree_job.project_id = sqlc.arg(project_id)
      AND tree_job.generation_id = sqlc.arg(generation_id)
      AND refresh_job_runs.environment = sqlc.arg(environment)
      AND (refresh_job_runs.id = @run_id OR refresh_job_runs.parent_run_id = @run_id)
  )
  AND refresh_jobs.project_id = sqlc.arg(project_id)
  AND refresh_jobs.generation_id = sqlc.arg(generation_id)
  AND refresh_jobs.status IN ('queued', 'running')
  AND (
    (refresh_jobs.status = 'running' AND refresh_jobs.lease_owner = sqlc.arg(lease_owner)
      AND refresh_jobs.lease_revision = sqlc.arg(lease_revision)
      AND refresh_jobs.lease_expires_at IS NOT NULL AND refresh_jobs.lease_expires_at > CURRENT_TIMESTAMP)
    OR EXISTS (
      SELECT 1 FROM refresh_jobs root_job
      WHERE root_job.id = (SELECT job_id FROM refresh_job_runs WHERE id = @run_id)
        AND root_job.project_id = sqlc.arg(project_id)
        AND root_job.generation_id = sqlc.arg(generation_id)
        AND EXISTS (SELECT 1 FROM refresh_job_runs root_run WHERE root_run.id = @run_id AND root_run.environment = sqlc.arg(environment))
        AND root_job.status = 'running' AND root_job.lease_owner = sqlc.arg(lease_owner)
        AND root_job.lease_revision = sqlc.arg(lease_revision)
        AND root_job.lease_expires_at IS NOT NULL AND root_job.lease_expires_at > CURRENT_TIMESTAMP
    )
  );

-- name: CountRefreshPublicationTreeRuns :one
SELECT COUNT(*) FROM refresh_job_runs runs
JOIN refresh_jobs jobs ON jobs.id = runs.job_id
WHERE jobs.project_id = sqlc.arg(project_id)
  AND jobs.generation_id = sqlc.arg(generation_id)
  AND runs.environment = sqlc.arg(environment)
  AND (runs.id = sqlc.arg(run_id) OR runs.parent_run_id = sqlc.arg(run_id));

-- name: CountRefreshPublicationTreeJobs :one
SELECT COUNT(*) FROM refresh_jobs
WHERE refresh_jobs.project_id = sqlc.arg(project_id)
  AND refresh_jobs.generation_id = sqlc.arg(generation_id)
  AND refresh_jobs.id IN (
  SELECT refresh_job_runs.job_id FROM refresh_job_runs
  WHERE refresh_job_runs.environment = sqlc.arg(environment)
    AND (refresh_job_runs.id = sqlc.arg(run_id) OR refresh_job_runs.parent_run_id = sqlc.arg(run_id))
);
