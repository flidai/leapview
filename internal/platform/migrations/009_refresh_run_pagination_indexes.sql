-- +goose Up
-- Forward-only migration: platform migrations do not rebuild SQLite tables for rollback.

CREATE INDEX IF NOT EXISTS refresh_jobs_project_created_idx
  ON refresh_jobs(project_id, created_at DESC);

CREATE INDEX IF NOT EXISTS refresh_job_runs_target_job_idx
  ON refresh_job_runs(target_type, target_id, job_id);
