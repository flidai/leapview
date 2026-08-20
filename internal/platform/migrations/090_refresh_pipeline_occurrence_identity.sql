-- +goose Up

-- ADR-0014: pipeline-wide scheduling policy and logical occurrence identity.
-- Occurrences are unique by project, environment, pipeline, and nominal UTC
-- instant. Captured generation and matching schedule IDs are evidence only.
DROP INDEX IF EXISTS refresh_pipeline_active_run_idx;
ALTER TABLE refresh_job_runs ADD COLUMN project_id TEXT NOT NULL DEFAULT '';
UPDATE refresh_job_runs
SET project_id = COALESCE((SELECT project_id FROM refresh_jobs WHERE refresh_jobs.id = refresh_job_runs.job_id), '')
WHERE project_id = '';
ALTER TABLE refresh_job_runs ADD COLUMN trigger_id TEXT NOT NULL DEFAULT '';
ALTER TABLE refresh_job_runs ADD COLUMN nominal_time TEXT NOT NULL DEFAULT '';
ALTER TABLE refresh_job_runs ADD COLUMN plan_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE refresh_job_runs ADD COLUMN materialization_scope_json TEXT NOT NULL DEFAULT '[]'
  CHECK (json_valid(materialization_scope_json) AND json_type(materialization_scope_json) = 'array');
ALTER TABLE refresh_job_runs ADD COLUMN matching_schedule_ids_json TEXT NOT NULL DEFAULT '[]'
  CHECK (json_valid(matching_schedule_ids_json) AND json_type(matching_schedule_ids_json) = 'array');

CREATE UNIQUE INDEX refresh_pipeline_active_run_idx
  ON refresh_job_runs(project_id, environment, target_type, target_id)
  WHERE parent_run_id IS NULL
    AND target_type = 'refresh_pipeline'
    AND status IN ('queued', 'running', 'prepared');

CREATE TABLE refresh_pipeline_schedules_v2 (
  project_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  pipeline_id TEXT NOT NULL,
  trigger_id TEXT NOT NULL,
  semantic_model_id TEXT NOT NULL,
  generation_id TEXT NOT NULL,
  artifact_digest TEXT NOT NULL,
  cron TEXT NOT NULL,
  timezone TEXT NOT NULL,
  starting_deadline_seconds INTEGER NOT NULL DEFAULT 0 CHECK (starting_deadline_seconds >= 0),
  concurrency_policy TEXT NOT NULL DEFAULT '' CHECK (concurrency_policy IN ('', 'Forbid', 'Replace')),
  next_run_at TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (project_id, environment, pipeline_id, generation_id, trigger_id),
  FOREIGN KEY (generation_id, project_id, environment)
    REFERENCES serving_states(id, project_id, environment) ON DELETE CASCADE
);

INSERT INTO refresh_pipeline_schedules_v2 (
  project_id, environment, pipeline_id, trigger_id, semantic_model_id,
  generation_id, artifact_digest, cron, timezone, starting_deadline_seconds,
  concurrency_policy, next_run_at, updated_at
)
SELECT project_id, environment, pipeline_id,
       'legacy:' || cron || ':' || timezone,
       semantic_model_id, generation_id, artifact_digest, cron, timezone,
       0, 'Forbid', next_run_at, updated_at
FROM refresh_pipeline_schedules;

DROP TABLE refresh_pipeline_schedules;
ALTER TABLE refresh_pipeline_schedules_v2 RENAME TO refresh_pipeline_schedules;
CREATE INDEX refresh_pipeline_schedules_due_idx
  ON refresh_pipeline_schedules(next_run_at, project_id, environment, pipeline_id, trigger_id);

CREATE TABLE refresh_pipeline_occurrences_v2 (
  project_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  pipeline_id TEXT NOT NULL,
  generation_id TEXT NOT NULL,
  artifact_digest TEXT NOT NULL,
  scheduled_at TEXT NOT NULL,
  timezone TEXT NOT NULL DEFAULT 'UTC',
  matching_schedule_ids_json TEXT NOT NULL DEFAULT '[]'
    CHECK (json_valid(matching_schedule_ids_json) AND json_type(matching_schedule_ids_json) = 'array'),
  run_id TEXT REFERENCES refresh_job_runs(id) ON DELETE SET NULL,
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'claimed', 'attached', 'skipped', 'superseded', 'failed')),
  outcome TEXT NOT NULL DEFAULT 'pending'
    CHECK (outcome IN ('pending', 'admitted', 'skipped', 'superseded', 'dispatch_failed', 'claim_expired', 'admission_denied_external_active')),
  terminal_reason TEXT NOT NULL DEFAULT '',
  claimed_at TEXT,
  outcome_at TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  PRIMARY KEY (project_id, environment, pipeline_id, scheduled_at)
);

INSERT OR IGNORE INTO refresh_pipeline_occurrences_v2 (
  project_id, environment, pipeline_id, generation_id, artifact_digest,
  scheduled_at, run_id, status, outcome, claimed_at, outcome_at, created_at,
  matching_schedule_ids_json
)
SELECT project_id, environment, pipeline_id, generation_id, artifact_digest,
       scheduled_at, run_id,
       CASE WHEN run_id IS NULL THEN 'pending' ELSE 'attached' END,
       CASE WHEN run_id IS NULL THEN 'pending' ELSE 'admitted' END,
       CASE WHEN run_id IS NULL THEN claimed_at ELSE NULL END,
       CASE WHEN run_id IS NULL THEN NULL ELSE claimed_at END,
       claimed_at, '[]'
FROM refresh_pipeline_occurrences
ORDER BY claimed_at, generation_id;

DROP TABLE refresh_pipeline_occurrences;
ALTER TABLE refresh_pipeline_occurrences_v2 RENAME TO refresh_pipeline_occurrences;
CREATE INDEX refresh_pipeline_occurrences_claim_idx
  ON refresh_pipeline_occurrences(project_id, environment, status, scheduled_at, pipeline_id);

-- +goose Down
DROP INDEX IF EXISTS refresh_pipeline_occurrences_claim_idx;
CREATE TABLE refresh_pipeline_occurrences_old (
  project_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  pipeline_id TEXT NOT NULL,
  generation_id TEXT NOT NULL,
  artifact_digest TEXT NOT NULL,
  scheduled_at TEXT NOT NULL,
  run_id TEXT REFERENCES refresh_job_runs(id) ON DELETE SET NULL,
  claimed_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  PRIMARY KEY (project_id, environment, pipeline_id, generation_id, scheduled_at),
  FOREIGN KEY (generation_id, project_id, environment)
    REFERENCES serving_states(id, project_id, environment) ON DELETE CASCADE
);
INSERT OR IGNORE INTO refresh_pipeline_occurrences_old (
  project_id, environment, pipeline_id, generation_id, artifact_digest,
  scheduled_at, run_id, claimed_at
)
SELECT project_id, environment, pipeline_id, generation_id, artifact_digest,
       scheduled_at, run_id, COALESCE(claimed_at, created_at)
FROM refresh_pipeline_occurrences
ORDER BY project_id, environment, pipeline_id, scheduled_at, generation_id;
DROP TABLE refresh_pipeline_occurrences;
ALTER TABLE refresh_pipeline_occurrences_old RENAME TO refresh_pipeline_occurrences;

DROP INDEX IF EXISTS refresh_pipeline_schedules_due_idx;
CREATE TABLE refresh_pipeline_schedules_old (
  project_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  pipeline_id TEXT NOT NULL,
  semantic_model_id TEXT NOT NULL,
  generation_id TEXT NOT NULL,
  artifact_digest TEXT NOT NULL,
  cron TEXT NOT NULL,
  timezone TEXT NOT NULL,
  next_run_at TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (project_id, environment, pipeline_id, generation_id, cron, timezone),
  FOREIGN KEY (generation_id, project_id, environment)
    REFERENCES serving_states(id, project_id, environment) ON DELETE CASCADE
);
INSERT OR IGNORE INTO refresh_pipeline_schedules_old (
  project_id, environment, pipeline_id, semantic_model_id, generation_id,
  artifact_digest, cron, timezone, next_run_at, updated_at
)
SELECT project_id, environment, pipeline_id, semantic_model_id, generation_id,
       artifact_digest, cron, timezone, next_run_at, updated_at
FROM refresh_pipeline_schedules
ORDER BY project_id, environment, pipeline_id, generation_id, cron, timezone, trigger_id;
DROP TABLE refresh_pipeline_schedules;
ALTER TABLE refresh_pipeline_schedules_old RENAME TO refresh_pipeline_schedules;
CREATE INDEX refresh_pipeline_schedules_due_idx
  ON refresh_pipeline_schedules(next_run_at, project_id, environment, pipeline_id);

DROP INDEX IF EXISTS refresh_pipeline_active_run_idx;
ALTER TABLE refresh_job_runs DROP COLUMN materialization_scope_json;
ALTER TABLE refresh_job_runs DROP COLUMN matching_schedule_ids_json;
ALTER TABLE refresh_job_runs DROP COLUMN plan_digest;
ALTER TABLE refresh_job_runs DROP COLUMN nominal_time;
ALTER TABLE refresh_job_runs DROP COLUMN trigger_id;
ALTER TABLE refresh_job_runs DROP COLUMN project_id;
CREATE UNIQUE INDEX refresh_pipeline_active_run_idx
  ON refresh_job_runs(environment, target_type, target_id)
  WHERE parent_run_id IS NULL
    AND target_type = 'refresh_pipeline'
    AND status IN ('queued', 'running');
