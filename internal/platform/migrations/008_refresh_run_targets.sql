-- +goose Up
-- Forward-only migration: platform migrations do not rebuild SQLite tables for rollback.

ALTER TABLE refresh_job_runs
  ADD COLUMN target_type TEXT NOT NULL DEFAULT 'semantic_model';

ALTER TABLE refresh_job_runs
  ADD COLUMN target_id TEXT NOT NULL DEFAULT '';

ALTER TABLE refresh_job_runs
  ADD COLUMN trigger_type TEXT NOT NULL DEFAULT 'direct';

ALTER TABLE refresh_job_runs
  ADD COLUMN parent_run_id TEXT REFERENCES refresh_job_runs(id) ON DELETE SET NULL;

UPDATE refresh_job_runs
SET target_id = COALESCE((
  SELECT semantic_model_id
  FROM refresh_jobs
  WHERE refresh_jobs.id = refresh_job_runs.job_id
), '')
WHERE target_id = '';

CREATE INDEX IF NOT EXISTS refresh_job_runs_target_idx
  ON refresh_job_runs(target_type, target_id, started_at DESC);

CREATE INDEX IF NOT EXISTS refresh_job_runs_parent_idx
  ON refresh_job_runs(parent_run_id);
