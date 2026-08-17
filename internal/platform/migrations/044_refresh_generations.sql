-- +goose Up
ALTER TABLE refresh_job_runs ADD COLUMN target_revision INTEGER NOT NULL DEFAULT 0 CHECK(target_revision >= 0);
ALTER TABLE refresh_jobs ADD COLUMN lease_revision INTEGER NOT NULL DEFAULT 0 CHECK(lease_revision >= 0);

-- Existing histories are ordered deterministically into target revisions. New
-- writes allocate monotonically from the maximum for their target; this fence
-- is independent from the serving generation identity.
UPDATE refresh_job_runs AS current
SET target_revision = (
  SELECT COUNT(*)
  FROM refresh_job_runs AS prior
  JOIN refresh_jobs AS prior_job ON prior_job.id = prior.job_id
  JOIN refresh_jobs AS current_job ON current_job.id = current.job_id
  WHERE prior_job.project_id = current_job.project_id
    AND prior_job.generation_id = current_job.generation_id
    AND prior.environment = current.environment
    AND prior.target_type = current.target_type
    AND prior.target_id = current.target_id
    AND (prior.created_sequence < current.created_sequence OR prior.id = current.id)
);

-- +goose Down
ALTER TABLE refresh_jobs DROP COLUMN lease_revision;
ALTER TABLE refresh_job_runs DROP COLUMN target_revision;
