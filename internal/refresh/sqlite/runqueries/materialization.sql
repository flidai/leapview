-- Refresh execution, materialization runs, and durable scheduling jobs.

-- name: RefreshTargetHasActiveRun :one
SELECT EXISTS(
  SELECT 1 FROM refresh_job_runs
  WHERE project_id = sqlc.arg(project_id)
    AND environment = sqlc.arg(environment)
    AND parent_run_id IS NULL
    AND target_type = sqlc.arg(target_type)
    AND target_id = sqlc.arg(target_id)
    AND status IN ('queued', 'running', 'prepared')
) AS active;

-- name: RefreshTargetHasActiveExternalRun :one
SELECT EXISTS(
  SELECT 1 FROM refresh_job_runs
  WHERE project_id = sqlc.arg(project_id)
    AND environment = sqlc.arg(environment)
    AND parent_run_id IS NULL
    AND target_type = sqlc.arg(target_type)
    AND target_id = sqlc.arg(target_id)
    AND trigger_type <> 'schedule'
    AND status IN ('queued', 'running', 'prepared')
) AS active;

-- name: CreateRefreshJob :exec
INSERT INTO refresh_jobs (id, project_id, generation_id, semantic_model_id, pipeline_id, principal_id, group_ids_json, estimated_memory_bytes, kind, payload_json, status, queued_at)
VALUES (sqlc.arg(id), sqlc.arg(project_id), sqlc.arg(generation_id), sqlc.arg(semantic_model_id), sqlc.arg(pipeline_id), sqlc.arg(principal_id), sqlc.arg(group_ids_json), sqlc.arg(estimated_memory_bytes), sqlc.arg(kind), sqlc.arg(payload_json), sqlc.arg(status), CURRENT_TIMESTAMP);

-- name: CreateRefreshJobRun :exec
INSERT INTO refresh_job_runs (
  id, job_id, project_id, principal_id, environment, target_type, target_id, trigger_type,
  trigger_id, invocation_source, nominal_time, plan_digest, materialization_scope_json, matching_schedule_ids_json,
  parent_run_id, status, target_revision, created_sequence
)
VALUES (
  sqlc.arg(id), sqlc.arg(job_id),
  COALESCE((SELECT project_id FROM refresh_jobs WHERE id = sqlc.arg(job_id)), ''),
  NULLIF(CAST(sqlc.arg(principal_id) AS TEXT), ''),
  sqlc.arg(environment), sqlc.arg(target_type), sqlc.arg(target_id), sqlc.arg(trigger_type),
  sqlc.arg(trigger_id), sqlc.arg(invocation_source), sqlc.arg(nominal_time), sqlc.arg(plan_digest), sqlc.arg(materialization_scope_json), sqlc.arg(matching_schedule_ids_json),
  NULLIF(CAST(sqlc.arg(parent_run_id) AS TEXT), ''),
  sqlc.arg(status),
  CASE WHEN CAST(sqlc.arg(target_revision) AS INTEGER) > 0 THEN CAST(sqlc.arg(target_revision) AS INTEGER)
    ELSE COALESCE((
      SELECT MAX(existing.target_revision) + 1
      FROM refresh_job_runs existing
      JOIN refresh_jobs existing_job ON existing_job.id = existing.job_id
      JOIN refresh_jobs new_job ON new_job.id = sqlc.arg(job_id)
      WHERE existing_job.project_id = new_job.project_id
        AND existing_job.generation_id = new_job.generation_id
        AND existing.environment = sqlc.arg(environment)
        AND existing.target_type = sqlc.arg(target_type)
        AND existing.target_id = sqlc.arg(target_id)
    ), 1)
  END,
  COALESCE((SELECT MAX(created_sequence) + 1 FROM refresh_job_runs), 1)
);

-- name: SkipRefreshPipelineOccurrence :execrows
UPDATE refresh_pipeline_occurrences
SET run_id = sqlc.arg(run_id), status = 'skipped', outcome = sqlc.arg(outcome),
    terminal_reason = sqlc.arg(terminal_reason), outcome_at = CURRENT_TIMESTAMP
WHERE project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND pipeline_id = sqlc.arg(pipeline_id)
  AND scheduled_at = sqlc.arg(scheduled_at)
  AND status = 'claimed'
  AND run_id IS NULL;

-- name: SupersedeRefreshTargetJobs :exec
UPDATE refresh_jobs
SET status = 'superseded', finished_at = CURRENT_TIMESTAMP, lease_owner = '', lease_expires_at = NULL,
    updated_at = CURRENT_TIMESTAMP, last_error = 'superseded by a newer target revision'
WHERE id IN (
  SELECT candidate.job_id
  FROM refresh_job_runs candidate
  JOIN refresh_jobs candidate_job ON candidate_job.id = candidate.job_id
  WHERE candidate_job.project_id = sqlc.arg(project_id)
    AND candidate.environment = sqlc.arg(environment)
    AND candidate.status IN ('queued', 'running', 'prepared')
    AND (
      (candidate.parent_run_id IS NULL AND candidate.target_type = sqlc.arg(target_type) AND candidate.target_id = sqlc.arg(target_id) AND candidate.trigger_type = 'schedule')
      OR candidate.parent_run_id IN (
        SELECT root.id FROM refresh_job_runs root
        JOIN refresh_jobs root_job ON root_job.id = root.job_id
        WHERE root_job.project_id = sqlc.arg(project_id)
          AND root.environment = sqlc.arg(environment)
          AND root.parent_run_id IS NULL
          AND root.target_type = sqlc.arg(target_type)
          AND root.target_id = sqlc.arg(target_id)
          AND root.status IN ('queued', 'running', 'prepared')
          AND root.trigger_type = 'schedule'
      )
    )
);

-- name: SupersedeRefreshTargetRuns :exec
UPDATE refresh_job_runs
SET status = 'superseded', finished_at = CURRENT_TIMESTAMP, error = 'superseded by a newer target revision'
WHERE id IN (
  SELECT candidate.id
  FROM refresh_job_runs candidate
  JOIN refresh_jobs candidate_job ON candidate_job.id = candidate.job_id
  WHERE candidate_job.project_id = sqlc.arg(project_id)
    AND candidate.environment = sqlc.arg(environment)
    AND candidate.status IN ('queued', 'running', 'prepared')
    AND (
      (candidate.parent_run_id IS NULL AND candidate.target_type = sqlc.arg(target_type) AND candidate.target_id = sqlc.arg(target_id) AND candidate.trigger_type = 'schedule')
      OR candidate.parent_run_id IN (
        SELECT root.id FROM refresh_job_runs root
        JOIN refresh_jobs root_job ON root_job.id = root.job_id
        WHERE root_job.project_id = sqlc.arg(project_id)
          AND root.environment = sqlc.arg(environment)
          AND root.parent_run_id IS NULL
          AND root.target_type = sqlc.arg(target_type)
          AND root.target_id = sqlc.arg(target_id)
          AND root.status IN ('queued', 'running', 'prepared')
          AND root.trigger_type = 'schedule'
      )
    )
);

-- name: SupersedeRefreshTargetOccurrences :exec
UPDATE refresh_pipeline_occurrences
SET status = 'superseded', outcome = 'superseded', terminal_reason = 'superseded by a newer target revision',
    outcome_at = CURRENT_TIMESTAMP
WHERE refresh_pipeline_occurrences.project_id = sqlc.arg(project_id)
  AND refresh_pipeline_occurrences.environment = sqlc.arg(environment)
  AND refresh_pipeline_occurrences.pipeline_id = sqlc.arg(target_id)
  AND refresh_pipeline_occurrences.run_id IN (
    SELECT root.id
    FROM refresh_job_runs root
    JOIN refresh_jobs root_job ON root_job.id = root.job_id
    WHERE root_job.project_id = sqlc.arg(project_id)
      AND root.environment = sqlc.arg(environment)
      AND root.parent_run_id IS NULL
      AND root.target_type = sqlc.arg(target_type)
      AND root.target_id = sqlc.arg(target_id)
      AND root.trigger_type = 'schedule'
      AND root.status IN ('queued', 'running', 'prepared')
  );

-- name: NextExecutableRefreshJob :one
SELECT j.id, j.project_id, r.environment, j.generation_id, j.semantic_model_id, j.pipeline_id, j.principal_id, j.group_ids_json, j.estimated_memory_bytes, j.kind, j.payload_json,
       r.id AS run_id, r.target_type, r.target_id, r.target_revision, r.trigger_type, r.trigger_id, r.invocation_source, r.nominal_time, r.plan_digest, r.materialization_scope_json, r.matching_schedule_ids_json, j.attempt_count, j.lease_owner, j.lease_revision
FROM refresh_jobs j
JOIN refresh_job_runs r ON r.job_id = j.id
WHERE COALESCE(r.parent_run_id, '') = ''
  AND j.kind = sqlc.arg(refresh_pipeline_kind)
  AND j.project_id = sqlc.arg(project_id)
  AND j.generation_id = sqlc.arg(generation_id)
  AND r.environment = sqlc.arg(environment)
  AND (
    (j.status = sqlc.arg(queued_status) AND r.status = sqlc.arg(run_queued_status))
    OR (j.status = sqlc.arg(running_status) AND (j.lease_expires_at IS NULL OR j.lease_expires_at <= CURRENT_TIMESTAMP))
  )
ORDER BY COALESCE(NULLIF(j.queued_at, ''), j.created_at) ASC, j.id ASC
LIMIT 1;

-- name: ListExecutableRefreshJobHeads :many
WITH eligible AS (
  SELECT j.id, j.project_id, r.environment, j.generation_id, j.semantic_model_id, j.pipeline_id, j.principal_id, j.group_ids_json, j.estimated_memory_bytes, j.kind, j.payload_json,
         r.id AS run_id, r.target_type, r.target_id, r.target_revision, r.trigger_type, r.trigger_id, r.invocation_source, r.nominal_time, r.plan_digest, r.materialization_scope_json, r.matching_schedule_ids_json, j.attempt_count, j.lease_owner, j.lease_revision,
         ROW_NUMBER() OVER (
           PARTITION BY j.principal_id
           ORDER BY COALESCE(NULLIF(j.queued_at, ''), j.created_at) ASC, j.id ASC
         ) AS principal_position,
         COALESCE(NULLIF(j.queued_at, ''), j.created_at) AS queue_position
  FROM refresh_jobs j
  JOIN refresh_job_runs r ON r.job_id = j.id
  WHERE COALESCE(r.parent_run_id, '') = ''
    AND j.kind = sqlc.arg(refresh_pipeline_kind)
    AND j.project_id = sqlc.arg(project_id)
    AND r.environment = sqlc.arg(environment)
    AND (
      (j.status = sqlc.arg(queued_status) AND r.status = sqlc.arg(run_queued_status))
      OR (j.status = sqlc.arg(running_status) AND (j.lease_expires_at IS NULL OR j.lease_expires_at <= CURRENT_TIMESTAMP))
    )
)
SELECT id, project_id, environment, generation_id, semantic_model_id, pipeline_id, principal_id, group_ids_json, estimated_memory_bytes, kind, payload_json,
       run_id, target_type, target_id, target_revision, trigger_type, trigger_id, invocation_source, nominal_time, plan_digest, materialization_scope_json, matching_schedule_ids_json, attempt_count, lease_owner, lease_revision
FROM eligible
WHERE principal_position = 1
ORDER BY queue_position ASC, id ASC
LIMIT sqlc.arg(result_limit);

-- name: ClaimRefreshJob :execresult
UPDATE refresh_jobs
SET status = sqlc.arg(running_status), started_at = COALESCE(started_at, CURRENT_TIMESTAMP), finished_at = NULL,
    lease_owner = sqlc.arg(lease_owner), lease_expires_at = datetime('now', CAST(sqlc.arg(lease_modifier) AS TEXT)),
    attempt_count = attempt_count + 1, lease_revision = lease_revision + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id)
  AND project_id = sqlc.arg(project_id)
  AND generation_id = sqlc.arg(generation_id)
  AND (
    status = sqlc.arg(queued_status)
    OR (status = sqlc.arg(previous_running_status) AND (lease_expires_at IS NULL OR lease_expires_at <= CURRENT_TIMESTAMP))
  );

-- name: MarkRefreshJobRunClaimed :exec
UPDATE refresh_job_runs
SET status = sqlc.arg(status), started_at = CURRENT_TIMESTAMP, finished_at = NULL, error = ''
WHERE refresh_job_runs.id = sqlc.arg(id)
  AND refresh_job_runs.environment = sqlc.arg(environment)
  AND refresh_job_runs.job_id IN (SELECT refresh_jobs.id FROM refresh_jobs
	                 WHERE refresh_jobs.project_id = sqlc.arg(project_id)
	                   AND refresh_jobs.generation_id = sqlc.arg(generation_id));

-- name: MarkRefreshRunPrepared :execrows
UPDATE refresh_job_runs
SET status = 'prepared', finished_at = NULL, error = ''
WHERE refresh_job_runs.id = sqlc.arg(run_id) AND refresh_job_runs.status = 'running'
  AND refresh_job_runs.environment = sqlc.arg(environment)
  AND refresh_job_runs.job_id IN (
	SELECT refresh_jobs.id FROM refresh_jobs
	    WHERE refresh_jobs.project_id = sqlc.arg(project_id) AND refresh_jobs.generation_id = sqlc.arg(generation_id) AND status = 'running'
      AND lease_owner = sqlc.arg(lease_owner) AND lease_revision = sqlc.arg(lease_revision)
      AND lease_expires_at IS NOT NULL AND lease_expires_at > CURRENT_TIMESTAMP
  );

-- name: RefreshRunMayPublish :one
SELECT EXISTS(
  SELECT 1
  FROM refresh_job_runs candidate
  JOIN refresh_jobs candidate_job ON candidate_job.id = candidate.job_id
  WHERE candidate.id = sqlc.arg(run_id)
    AND candidate_job.project_id = sqlc.arg(project_id)
    AND candidate_job.generation_id = sqlc.arg(generation_id)
    AND candidate.environment = sqlc.arg(environment)
    AND candidate.status = 'prepared'
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
) AS may_publish;

-- name: MarkRefreshRunSuperseded :execrows
UPDATE refresh_job_runs
SET status = 'superseded', finished_at = CURRENT_TIMESTAMP, error = 'superseded by a newer target revision'
WHERE refresh_job_runs.id = sqlc.arg(run_id)
  AND refresh_job_runs.environment = sqlc.arg(environment)
	  AND refresh_job_runs.job_id IN (SELECT refresh_jobs.id FROM refresh_jobs WHERE refresh_jobs.project_id = sqlc.arg(project_id) AND refresh_jobs.generation_id = sqlc.arg(generation_id))
  AND refresh_job_runs.status IN ('running', 'prepared');

-- name: RenewRefreshJobLease :execrows
UPDATE refresh_jobs
SET lease_expires_at = datetime('now', CAST(sqlc.arg(lease_modifier) AS TEXT)), updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id) AND lease_owner = sqlc.arg(lease_owner)
	AND refresh_jobs.project_id = sqlc.arg(project_id) AND refresh_jobs.generation_id = sqlc.arg(generation_id)
  AND lease_revision = sqlc.arg(lease_revision) AND status = sqlc.arg(status)
  AND lease_expires_at IS NOT NULL AND lease_expires_at > CURRENT_TIMESTAMP;

-- name: GetRefreshJobQueueStats :one
SELECT
  CAST(COALESCE(SUM(CASE WHEN j.status = sqlc.arg(queued_status) THEN 1 ELSE 0 END), 0) AS INTEGER) AS queued_jobs,
  CAST(COALESCE(SUM(CASE WHEN j.status = sqlc.arg(running_status) AND j.lease_expires_at IS NOT NULL AND j.lease_expires_at > CURRENT_TIMESTAMP THEN 1 ELSE 0 END), 0) AS INTEGER) AS running_jobs,
  CAST(COALESCE(SUM(CASE WHEN j.status = sqlc.arg(stale_running_status) AND (j.lease_expires_at IS NULL OR j.lease_expires_at <= CURRENT_TIMESTAMP) THEN 1 ELSE 0 END), 0) AS INTEGER) AS stale_leased_jobs
FROM refresh_jobs j
JOIN refresh_job_runs r ON r.job_id = j.id
WHERE COALESCE(r.parent_run_id, '') = ''
    AND j.kind = sqlc.arg(refresh_pipeline_kind)
    AND j.project_id = sqlc.arg(project_id)
  AND r.environment = sqlc.arg(environment);

-- name: GetMaterializationRun :one
SELECT r.id, j.project_id, r.environment, j.generation_id, j.semantic_model_id, j.pipeline_id, r.principal_id,
       COALESCE(NULLIF(p.display_name, ''), NULLIF(p.email, ''), r.principal_id, '') AS principal_display_name,
       r.target_type, r.target_id, r.target_revision, r.trigger_type, r.trigger_id, r.invocation_source, r.nominal_time, r.plan_digest, r.materialization_scope_json, r.matching_schedule_ids_json, r.parent_run_id, r.status, j.created_at, j.updated_at,
       r.started_at, r.finished_at, r.error
FROM refresh_job_runs r
JOIN refresh_jobs j ON j.id = r.job_id
LEFT JOIN principals p ON p.id = r.principal_id
WHERE r.id = sqlc.arg(run_id) AND j.project_id = sqlc.arg(project_id)
  AND r.environment = sqlc.arg(environment);

-- name: ListChildMaterializationRuns :many
SELECT r.id, j.project_id, r.environment, j.generation_id, j.semantic_model_id, j.pipeline_id, r.principal_id,
       COALESCE(NULLIF(p.display_name, ''), NULLIF(p.email, ''), r.principal_id, '') AS principal_display_name,
       r.target_type, r.target_id, r.target_revision, r.trigger_type, r.trigger_id, r.invocation_source, r.nominal_time, r.plan_digest, r.materialization_scope_json, r.matching_schedule_ids_json, r.parent_run_id, r.status, j.created_at, j.updated_at,
       r.started_at, r.finished_at, r.error
FROM refresh_job_runs r
JOIN refresh_jobs j ON j.id = r.job_id
LEFT JOIN principals p ON p.id = r.principal_id
WHERE j.project_id = sqlc.arg(project_id) AND r.environment = sqlc.arg(environment)
  AND r.parent_run_id = sqlc.arg(parent_run_id)
ORDER BY r.rowid ASC;

-- name: LatestSuccessfulMaterializationRun :one
SELECT r.id, j.project_id, r.environment, j.generation_id, j.semantic_model_id, j.pipeline_id, r.principal_id,
       COALESCE(NULLIF(p.display_name, ''), NULLIF(p.email, ''), r.principal_id, '') AS principal_display_name,
       r.target_type, r.target_id, r.target_revision, r.trigger_type, r.trigger_id, r.invocation_source, r.nominal_time, r.plan_digest, r.materialization_scope_json, r.matching_schedule_ids_json, r.parent_run_id, r.status, j.created_at, j.updated_at,
       r.started_at, r.finished_at, r.error
FROM refresh_job_runs r
JOIN refresh_jobs j ON j.id = r.job_id
LEFT JOIN principals p ON p.id = r.principal_id
WHERE j.project_id = sqlc.arg(project_id) AND r.target_type = sqlc.arg(target_type)
  AND r.environment = sqlc.arg(environment)
  AND r.target_id = sqlc.arg(target_id) AND r.status = sqlc.arg(status)
ORDER BY j.created_at DESC, r.rowid DESC
LIMIT 1;

-- name: FailTerminalServingStateRuns :exec
UPDATE refresh_job_runs
SET status = sqlc.arg(failed_status), finished_at = CURRENT_TIMESTAMP,
    error = CASE WHEN error <> '' THEN error ELSE sqlc.arg(error_message) END
WHERE refresh_job_runs.status IN (sqlc.arg(queued_status), sqlc.arg(running_status))
  AND job_id IN (
    SELECT j.id FROM refresh_jobs j
    JOIN serving_states d ON d.id = j.generation_id AND d.project_id = j.project_id AND d.environment = sqlc.arg(environment)
    WHERE d.environment = sqlc.arg(environment)
      AND d.status IN ('failed', 'delete_scheduled', 'deleted')
  );

-- name: FailTerminalServingStateJobs :exec
UPDATE refresh_jobs
SET status = sqlc.arg(failed_status), updated_at = CURRENT_TIMESTAMP
WHERE refresh_jobs.status IN (sqlc.arg(queued_status), sqlc.arg(running_status))
  AND generation_id IN (
    SELECT id FROM serving_states
    WHERE environment = sqlc.arg(environment)
      AND status IN ('failed', 'delete_scheduled', 'deleted')
  );

-- name: MarkMaterializationRunActive :execresult
UPDATE refresh_job_runs
SET status = sqlc.arg(status), finished_at = finished_at, error = sqlc.arg(error_message)
WHERE refresh_job_runs.id = sqlc.arg(run_id)
  AND environment = sqlc.arg(environment)
  AND job_id IN (SELECT refresh_jobs.id FROM refresh_jobs
	                 WHERE refresh_jobs.project_id = sqlc.arg(project_id)
	                   AND refresh_jobs.generation_id = sqlc.arg(generation_id));

-- name: MarkMaterializationRunTerminal :execresult
UPDATE refresh_job_runs
SET status = sqlc.arg(status), finished_at = CURRENT_TIMESTAMP, error = sqlc.arg(error_message)
WHERE refresh_job_runs.id = sqlc.arg(run_id)
  AND refresh_job_runs.status IN ('queued', 'running', 'prepared')
  AND refresh_job_runs.environment = sqlc.arg(environment)
  AND job_id IN (SELECT refresh_jobs.id FROM refresh_jobs
	                 WHERE refresh_jobs.project_id = sqlc.arg(project_id)
	                   AND refresh_jobs.generation_id = sqlc.arg(generation_id));

-- Worker-owned terminal transitions are fenced by the currently claimed root
-- job. Child runs intentionally inherit their parent's claim fence because
-- only the pipeline job is dispatched; both mutations remain in one caller
-- transaction and must affect exactly one row.
-- name: MarkRefreshRunSucceededClaimed :execrows
UPDATE refresh_job_runs
SET status = 'succeeded', finished_at = CURRENT_TIMESTAMP, error = ''
WHERE refresh_job_runs.id = sqlc.arg(run_id)
  AND refresh_job_runs.status IN ('queued', 'running', 'prepared')
  AND EXISTS (
    SELECT 1 FROM refresh_jobs claim_job
    WHERE claim_job.project_id = sqlc.arg(project_id) AND claim_job.generation_id = sqlc.arg(generation_id)
      AND EXISTS (
        SELECT 1 FROM refresh_job_runs scoped
        JOIN refresh_jobs scoped_job ON scoped_job.id = scoped.job_id
        WHERE scoped.id = refresh_job_runs.id
          AND scoped.environment = sqlc.arg(environment)
          AND scoped_job.project_id = sqlc.arg(project_id)
          AND scoped_job.generation_id = sqlc.arg(generation_id)
      )
      AND claim_job.status = 'running'
      AND claim_job.lease_owner = sqlc.arg(lease_owner)
      AND claim_job.lease_revision = sqlc.arg(lease_revision)
      AND claim_job.lease_expires_at IS NOT NULL
      AND claim_job.lease_expires_at > CURRENT_TIMESTAMP
      AND (
        claim_job.id = refresh_job_runs.job_id
        OR claim_job.id = (
          SELECT parent.job_id FROM refresh_job_runs parent
          WHERE parent.id = refresh_job_runs.parent_run_id
        )
      )
  );

-- name: MarkRefreshRunFailedClaimed :execrows
UPDATE refresh_job_runs
SET status = 'failed', finished_at = CURRENT_TIMESTAMP, error = sqlc.arg(error_message)
WHERE refresh_job_runs.id = sqlc.arg(run_id)
  AND refresh_job_runs.status IN ('queued', 'running', 'prepared')
  AND EXISTS (
    SELECT 1 FROM refresh_jobs claim_job
    WHERE claim_job.project_id = sqlc.arg(project_id) AND claim_job.generation_id = sqlc.arg(generation_id)
      AND EXISTS (
        SELECT 1 FROM refresh_job_runs scoped
        JOIN refresh_jobs scoped_job ON scoped_job.id = scoped.job_id
        WHERE scoped.id = refresh_job_runs.id
          AND scoped.environment = sqlc.arg(environment)
          AND scoped_job.project_id = sqlc.arg(project_id)
          AND scoped_job.generation_id = sqlc.arg(generation_id)
      )
      AND claim_job.status = 'running'
      AND claim_job.lease_owner = sqlc.arg(lease_owner)
      AND claim_job.lease_revision = sqlc.arg(lease_revision)
      AND claim_job.lease_expires_at IS NOT NULL
      AND claim_job.lease_expires_at > CURRENT_TIMESTAMP
      AND (
        claim_job.id = refresh_job_runs.job_id
        OR claim_job.id = (
          SELECT parent.job_id FROM refresh_job_runs parent
          WHERE parent.id = refresh_job_runs.parent_run_id
        )
      )
  );

-- name: MarkRefreshRunTreeFailedClaimed :execrows
UPDATE refresh_job_runs
SET status = 'failed', finished_at = CURRENT_TIMESTAMP, error = sqlc.arg(error_message)
WHERE refresh_job_runs.status IN ('queued', 'running', 'prepared')
  AND (refresh_job_runs.id = @run_id OR refresh_job_runs.parent_run_id = @run_id)
  AND EXISTS (
    SELECT 1 FROM refresh_job_runs root
    JOIN refresh_jobs claim_job ON claim_job.id = root.job_id
    WHERE root.id = @run_id AND claim_job.project_id = sqlc.arg(project_id) AND claim_job.generation_id = sqlc.arg(generation_id)
      AND root.environment = sqlc.arg(environment)
      AND claim_job.status = 'running' AND claim_job.lease_owner = sqlc.arg(lease_owner)
      AND claim_job.lease_revision = sqlc.arg(lease_revision)
      AND claim_job.lease_expires_at IS NOT NULL AND claim_job.lease_expires_at > CURRENT_TIMESTAMP
  );

-- name: CountRefreshRunTreeClaimed :one
SELECT COUNT(*) FROM refresh_job_runs runs
JOIN refresh_jobs jobs ON jobs.id = runs.job_id
WHERE jobs.project_id = sqlc.arg(project_id)
  AND jobs.generation_id = sqlc.arg(generation_id)
  AND runs.environment = sqlc.arg(environment)
  AND (runs.id = sqlc.arg(run_id) OR runs.parent_run_id = sqlc.arg(run_id));

-- name: CountRefreshJobTreeClaimed :one
SELECT COUNT(*) FROM refresh_jobs
WHERE refresh_jobs.project_id = sqlc.arg(project_id)
  AND refresh_jobs.generation_id = sqlc.arg(generation_id)
  AND refresh_jobs.id IN (
  SELECT refresh_job_runs.job_id FROM refresh_job_runs
  WHERE refresh_job_runs.environment = sqlc.arg(environment)
    AND (refresh_job_runs.id = sqlc.arg(run_id) OR refresh_job_runs.parent_run_id = sqlc.arg(run_id))
);

-- name: CompleteRefreshJobTreeFailedClaimed :execrows
UPDATE refresh_jobs
SET status = 'failed', updated_at = CURRENT_TIMESTAMP, finished_at = CURRENT_TIMESTAMP,
    lease_owner = '', lease_expires_at = NULL, last_error = sqlc.arg(error_message)
  WHERE refresh_jobs.id IN (
      SELECT refresh_job_runs.job_id FROM refresh_job_runs
      JOIN refresh_jobs tree_job ON tree_job.id = refresh_job_runs.job_id
      WHERE tree_job.project_id = sqlc.arg(project_id)
        AND tree_job.generation_id = sqlc.arg(generation_id)
        AND refresh_job_runs.environment = sqlc.arg(environment)
        AND (refresh_job_runs.id = @run_id OR refresh_job_runs.parent_run_id = @run_id)
  )
  AND refresh_jobs.status IN ('queued', 'running', 'prepared')
  AND EXISTS (
    SELECT 1 FROM refresh_job_runs root
    JOIN refresh_jobs claim_job ON claim_job.id = root.job_id
    WHERE root.id = @run_id AND claim_job.project_id = sqlc.arg(project_id) AND claim_job.generation_id = sqlc.arg(generation_id)
      AND root.environment = sqlc.arg(environment)
      AND claim_job.status = 'running' AND claim_job.lease_owner = sqlc.arg(lease_owner)
      AND claim_job.lease_revision = sqlc.arg(lease_revision)
      AND claim_job.lease_expires_at IS NOT NULL AND claim_job.lease_expires_at > CURRENT_TIMESTAMP
  );

-- name: MarkRefreshRunTreeSupersededClaimed :execrows
UPDATE refresh_job_runs
SET status = 'superseded', finished_at = CURRENT_TIMESTAMP, error = sqlc.arg(error_message)
WHERE refresh_job_runs.status IN ('queued', 'running', 'prepared')
  AND (refresh_job_runs.id = sqlc.arg(run_id) OR refresh_job_runs.parent_run_id = sqlc.arg(run_id))
  AND EXISTS (
    SELECT 1 FROM refresh_job_runs root
    JOIN refresh_jobs claim_job ON claim_job.id = root.job_id
    WHERE root.id = sqlc.arg(run_id)
      AND claim_job.project_id = sqlc.arg(project_id)
      AND claim_job.generation_id = sqlc.arg(generation_id)
      AND root.environment = sqlc.arg(environment)
      AND claim_job.status = 'running'
      AND claim_job.lease_owner = sqlc.arg(lease_owner)
      AND claim_job.lease_revision = sqlc.arg(lease_revision)
      AND claim_job.lease_expires_at IS NOT NULL
      AND claim_job.lease_expires_at > CURRENT_TIMESTAMP
  );

-- name: SupersedeRefreshPipelineOccurrenceClaimed :execrows
UPDATE refresh_pipeline_occurrences
SET status = 'superseded', outcome = 'superseded',
    terminal_reason = sqlc.arg(error_message), claimed_at = NULL,
    outcome_at = CURRENT_TIMESTAMP
WHERE refresh_pipeline_occurrences.run_id = CAST(sqlc.arg(run_id) AS TEXT)
  AND refresh_pipeline_occurrences.project_id = sqlc.arg(project_id)
  AND refresh_pipeline_occurrences.environment = sqlc.arg(environment)
  AND refresh_pipeline_occurrences.status = 'attached'
  AND EXISTS (
    SELECT 1 FROM refresh_job_runs root
    JOIN refresh_jobs claim_job ON claim_job.id = root.job_id
    WHERE root.id = sqlc.arg(run_id)
      AND claim_job.project_id = sqlc.arg(project_id)
      AND claim_job.generation_id = sqlc.arg(generation_id)
      AND root.environment = sqlc.arg(environment)
      AND claim_job.status = 'running'
      AND claim_job.lease_owner = sqlc.arg(lease_owner)
      AND claim_job.lease_revision = sqlc.arg(lease_revision)
      AND claim_job.lease_expires_at IS NOT NULL
      AND claim_job.lease_expires_at > CURRENT_TIMESTAMP
  );

-- name: CompleteRefreshJobTreeSupersededClaimed :execrows
UPDATE refresh_jobs
SET status = 'superseded', updated_at = CURRENT_TIMESTAMP, finished_at = CURRENT_TIMESTAMP,
    lease_owner = '', lease_expires_at = NULL, last_error = sqlc.arg(error_message)
WHERE refresh_jobs.id IN (
  SELECT refresh_job_runs.job_id FROM refresh_job_runs
  JOIN refresh_jobs tree_job ON tree_job.id = refresh_job_runs.job_id
  WHERE tree_job.project_id = sqlc.arg(project_id)
    AND tree_job.generation_id = sqlc.arg(generation_id)
    AND refresh_job_runs.environment = sqlc.arg(environment)
    AND (refresh_job_runs.id = sqlc.arg(run_id) OR refresh_job_runs.parent_run_id = sqlc.arg(run_id))
)
AND refresh_jobs.status IN ('queued', 'running', 'prepared')
AND EXISTS (
  SELECT 1 FROM refresh_job_runs root
  JOIN refresh_jobs claim_job ON claim_job.id = root.job_id
  WHERE root.id = sqlc.arg(run_id)
    AND claim_job.project_id = sqlc.arg(project_id)
    AND claim_job.generation_id = sqlc.arg(generation_id)
    AND root.environment = sqlc.arg(environment)
    AND claim_job.status = 'running'
    AND claim_job.lease_owner = sqlc.arg(lease_owner)
    AND claim_job.lease_revision = sqlc.arg(lease_revision)
    AND claim_job.lease_expires_at IS NOT NULL
    AND claim_job.lease_expires_at > CURRENT_TIMESTAMP
);

-- name: UpdateRefreshJobForActiveRun :exec
UPDATE refresh_jobs
SET status = sqlc.arg(new_status), updated_at = CURRENT_TIMESTAMP
WHERE refresh_jobs.id = (SELECT refresh_job_runs.job_id FROM refresh_job_runs WHERE refresh_job_runs.id = sqlc.arg(run_id))
	AND refresh_jobs.project_id = sqlc.arg(project_id) AND refresh_jobs.generation_id = sqlc.arg(generation_id)
  AND EXISTS (SELECT 1 FROM refresh_job_runs scoped WHERE scoped.id = sqlc.arg(run_id) AND scoped.environment = sqlc.arg(environment));
-- name: CompleteRefreshJobSucceeded :exec
UPDATE refresh_jobs
SET status = 'succeeded', updated_at = CURRENT_TIMESTAMP, finished_at = CURRENT_TIMESTAMP,
    lease_owner = '', lease_expires_at = NULL
WHERE refresh_jobs.id = (SELECT refresh_job_runs.job_id FROM refresh_job_runs WHERE refresh_job_runs.id = sqlc.arg(run_id))
	AND refresh_jobs.project_id = sqlc.arg(project_id) AND refresh_jobs.generation_id = sqlc.arg(generation_id)
  AND EXISTS (SELECT 1 FROM refresh_job_runs scoped WHERE scoped.id = sqlc.arg(run_id) AND scoped.environment = sqlc.arg(environment));

-- name: CompleteRefreshJobFailed :exec
UPDATE refresh_jobs
SET status = 'failed', updated_at = CURRENT_TIMESTAMP, finished_at = CURRENT_TIMESTAMP,
    lease_owner = '', lease_expires_at = NULL, last_error = sqlc.arg(error_message)
WHERE refresh_jobs.id = (SELECT refresh_job_runs.job_id FROM refresh_job_runs WHERE refresh_job_runs.id = sqlc.arg(run_id))
  AND refresh_jobs.project_id = sqlc.arg(project_id) AND refresh_jobs.generation_id = sqlc.arg(generation_id)
  AND EXISTS (SELECT 1 FROM refresh_job_runs scoped WHERE scoped.id = sqlc.arg(run_id) AND scoped.environment = sqlc.arg(environment));

-- name: CompleteRefreshJobSucceededClaimed :execrows
UPDATE refresh_jobs
SET status = 'succeeded', updated_at = CURRENT_TIMESTAMP, finished_at = CURRENT_TIMESTAMP,
    lease_owner = '', lease_expires_at = NULL, last_error = ''
WHERE refresh_jobs.id = (SELECT job_id FROM refresh_job_runs WHERE refresh_job_runs.id = sqlc.arg(run_id))
  AND refresh_jobs.project_id = sqlc.arg(project_id)
  AND refresh_jobs.generation_id = sqlc.arg(generation_id)
  AND EXISTS (SELECT 1 FROM refresh_job_runs scoped WHERE scoped.id = sqlc.arg(run_id) AND scoped.environment = sqlc.arg(environment))
  AND (
    (refresh_jobs.status = 'running' AND refresh_jobs.lease_owner = sqlc.arg(lease_owner)
      AND refresh_jobs.lease_revision = sqlc.arg(lease_revision)
      AND refresh_jobs.lease_expires_at IS NOT NULL AND refresh_jobs.lease_expires_at > CURRENT_TIMESTAMP)
    OR EXISTS (
      SELECT 1
      FROM refresh_job_runs child
      JOIN refresh_jobs claim_job ON claim_job.id = (
        SELECT parent.job_id FROM refresh_job_runs parent WHERE parent.id = child.parent_run_id
      )
      WHERE child.id = sqlc.arg(run_id)
        AND child.parent_run_id IS NOT NULL
        AND claim_job.project_id = sqlc.arg(project_id)
        AND claim_job.generation_id = sqlc.arg(generation_id)
        AND child.environment = sqlc.arg(environment)
        AND claim_job.status = 'running' AND claim_job.lease_owner = sqlc.arg(lease_owner)
        AND claim_job.lease_revision = sqlc.arg(lease_revision)
        AND claim_job.lease_expires_at IS NOT NULL AND claim_job.lease_expires_at > CURRENT_TIMESTAMP
    )
  );

-- name: CompleteRefreshJobFailedClaimed :execrows
UPDATE refresh_jobs
SET status = 'failed', updated_at = CURRENT_TIMESTAMP, finished_at = CURRENT_TIMESTAMP,
    lease_owner = '', lease_expires_at = NULL, last_error = sqlc.arg(error_message)
WHERE refresh_jobs.id = (SELECT job_id FROM refresh_job_runs WHERE refresh_job_runs.id = sqlc.arg(run_id))
  AND refresh_jobs.project_id = sqlc.arg(project_id)
  AND refresh_jobs.generation_id = sqlc.arg(generation_id)
  AND EXISTS (SELECT 1 FROM refresh_job_runs scoped WHERE scoped.id = sqlc.arg(run_id) AND scoped.environment = sqlc.arg(environment))
  AND (
    (refresh_jobs.status = 'running' AND refresh_jobs.lease_owner = sqlc.arg(lease_owner)
      AND refresh_jobs.lease_revision = sqlc.arg(lease_revision)
      AND refresh_jobs.lease_expires_at IS NOT NULL AND refresh_jobs.lease_expires_at > CURRENT_TIMESTAMP)
    OR EXISTS (
      SELECT 1
      FROM refresh_job_runs child
      JOIN refresh_jobs claim_job ON claim_job.id = (
        SELECT parent.job_id FROM refresh_job_runs parent WHERE parent.id = child.parent_run_id
      )
      WHERE child.id = sqlc.arg(run_id)
        AND child.parent_run_id IS NOT NULL
        AND claim_job.project_id = sqlc.arg(project_id)
        AND claim_job.generation_id = sqlc.arg(generation_id)
        AND child.environment = sqlc.arg(environment)
        AND claim_job.status = 'running' AND claim_job.lease_owner = sqlc.arg(lease_owner)
        AND claim_job.lease_revision = sqlc.arg(lease_revision)
        AND claim_job.lease_expires_at IS NOT NULL AND claim_job.lease_expires_at > CURRENT_TIMESTAMP
    )
  );
-- name: CancelQueuedMaterializationRun :execresult
UPDATE refresh_job_runs
SET status = sqlc.arg(cancelled_status), finished_at = CURRENT_TIMESTAMP, error = ''
WHERE refresh_job_runs.id = sqlc.arg(run_id)
  AND refresh_job_runs.status = sqlc.arg(queued_status)
  AND refresh_job_runs.environment = sqlc.arg(environment)
  AND refresh_job_runs.job_id IN (
    SELECT refresh_jobs.id FROM refresh_jobs
    WHERE refresh_jobs.project_id = sqlc.arg(project_id)
      AND refresh_jobs.generation_id = sqlc.arg(generation_id)
  );

-- name: CancelQueuedRefreshJobForRun :exec
UPDATE refresh_jobs
SET status = sqlc.arg(cancelled_status), finished_at = CURRENT_TIMESTAMP,
    lease_owner = '', lease_expires_at = NULL, updated_at = CURRENT_TIMESTAMP
WHERE refresh_jobs.id = (SELECT refresh_job_runs.job_id FROM refresh_job_runs WHERE refresh_job_runs.id = sqlc.arg(run_id))
  AND refresh_jobs.project_id = sqlc.arg(project_id)
  AND refresh_jobs.generation_id = sqlc.arg(generation_id)
  AND EXISTS (SELECT 1 FROM refresh_job_runs scoped WHERE scoped.id = sqlc.arg(run_id) AND scoped.environment = sqlc.arg(environment))
  AND refresh_jobs.status = sqlc.arg(queued_status);

-- name: CancelQueuedChildMaterializationRuns :exec
UPDATE refresh_job_runs
SET status = sqlc.arg(cancelled_status), finished_at = CURRENT_TIMESTAMP, error = ''
WHERE refresh_job_runs.parent_run_id = sqlc.arg(parent_run_id)
  AND refresh_job_runs.status = sqlc.arg(queued_status)
  AND refresh_job_runs.environment = sqlc.arg(environment)
  AND refresh_job_runs.job_id IN (SELECT id FROM refresh_jobs
	                                  WHERE refresh_jobs.project_id = sqlc.arg(project_id)
	                                    AND refresh_jobs.generation_id = sqlc.arg(generation_id));

-- name: CancelQueuedChildRefreshJobs :exec
UPDATE refresh_jobs
SET status = sqlc.arg(cancelled_status), finished_at = CURRENT_TIMESTAMP,
    lease_owner = '', lease_expires_at = NULL, updated_at = CURRENT_TIMESTAMP
WHERE refresh_jobs.project_id = sqlc.arg(project_id)
  AND refresh_jobs.generation_id = sqlc.arg(generation_id)
  AND refresh_jobs.status = sqlc.arg(queued_status)
  AND refresh_jobs.id IN (SELECT job_id FROM refresh_job_runs
                          WHERE parent_run_id = sqlc.arg(parent_run_id)
                            AND environment = sqlc.arg(environment));

-- name: FailCancelledRefreshCandidate :execresult
UPDATE serving_states
SET status = 'failed', error = 'refresh cancelled'
WHERE id = sqlc.arg(generation_id)
  AND source = 'refresh'
  AND status = 'validated';

-- name: ListMaterializationRuns :many
SELECT r.id, j.project_id, r.environment, j.generation_id, j.semantic_model_id, j.pipeline_id, r.principal_id,
       COALESCE(NULLIF(p.display_name, ''), NULLIF(p.email, ''), r.principal_id, '') AS principal_display_name,
       r.target_type, r.target_id, r.target_revision, r.trigger_type, r.trigger_id, r.invocation_source, r.nominal_time, r.plan_digest, r.materialization_scope_json, r.matching_schedule_ids_json, r.parent_run_id, r.status,
       j.created_at, j.updated_at, r.started_at, r.finished_at, r.error
FROM refresh_job_runs r
JOIN refresh_jobs j ON j.id = r.job_id
LEFT JOIN principals p ON p.id = r.principal_id
WHERE j.project_id = sqlc.arg(project_id)
  AND r.environment = sqlc.arg(environment)
  AND COALESCE(r.parent_run_id, '') = ''
  AND r.target_type = 'refresh_pipeline'
  AND (
    CAST(sqlc.arg(cursor_created_at) AS TEXT) = ''
    OR j.created_at < CAST(sqlc.arg(cursor_created_at) AS TEXT)
    OR (j.created_at = CAST(sqlc.arg(cursor_created_at) AS TEXT) AND r.created_sequence < sqlc.arg(cursor_sequence))
  )
ORDER BY j.created_at DESC, r.created_sequence DESC
LIMIT sqlc.arg(limit);

-- name: ListTargetMaterializationRuns :many
SELECT r.id, j.project_id, r.environment, j.generation_id, j.semantic_model_id, j.pipeline_id, r.principal_id,
       COALESCE(NULLIF(p.display_name, ''), NULLIF(p.email, ''), r.principal_id, '') AS principal_display_name,
       r.target_type, r.target_id, r.target_revision, r.trigger_type, r.trigger_id, r.invocation_source, r.nominal_time, r.plan_digest, r.materialization_scope_json, r.matching_schedule_ids_json, r.parent_run_id, r.status,
       j.created_at, j.updated_at, r.started_at, r.finished_at, r.error
FROM refresh_job_runs r
JOIN refresh_jobs j ON j.id = r.job_id
LEFT JOIN principals p ON p.id = r.principal_id
WHERE j.project_id = sqlc.arg(project_id)
  AND r.environment = sqlc.arg(environment)
  AND r.target_type = sqlc.arg(target_type)
  AND r.target_id = sqlc.arg(target_id)
  AND (
    CAST(sqlc.arg(cursor_created_at) AS TEXT) = ''
    OR j.created_at < CAST(sqlc.arg(cursor_created_at) AS TEXT)
    OR (j.created_at = CAST(sqlc.arg(cursor_created_at) AS TEXT) AND r.created_sequence < sqlc.arg(cursor_sequence))
  )
ORDER BY j.created_at DESC, r.created_sequence DESC
LIMIT sqlc.arg(limit);

-- name: GetMaterializationRunCursor :one
SELECT j.created_at, r.created_sequence
FROM refresh_job_runs r
JOIN refresh_jobs j ON j.id = r.job_id
WHERE r.id = sqlc.arg(run_id)
  AND j.project_id = sqlc.arg(project_id)
  AND r.environment = sqlc.arg(environment)
  AND (
    CAST(sqlc.arg(target_type) AS TEXT) = ''
    OR (r.target_type = CAST(sqlc.arg(target_type) AS TEXT) AND r.target_id = sqlc.arg(target_id))
  );
