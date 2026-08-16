-- +goose Up
CREATE TABLE api_async_jobs (
  id TEXT PRIMARY KEY,
  job_kind TEXT NOT NULL,
  workload_class TEXT NOT NULL CHECK(workload_class IN ('background', 'control')),
  principal_id TEXT NOT NULL CHECK(length(principal_id) > 0 AND principal_id = trim(principal_id)),
  group_ids_json TEXT NOT NULL CHECK(json_valid(group_ids_json)),
  resource_kind TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  estimated_memory_bytes INTEGER NOT NULL CHECK(estimated_memory_bytes > 0),
  payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
  request_digest TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
  attempt_count INTEGER NOT NULL DEFAULT 0 CHECK(attempt_count >= 0),
  lease_owner TEXT NOT NULL DEFAULT '',
  lease_expires_at TEXT,
  error_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(error_json)),
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  started_at TEXT,
  finished_at TEXT,
  UNIQUE(resource_kind, resource_id, job_kind),
  CHECK(length(trim(job_kind)) > 0),
  CHECK(length(trim(resource_kind)) > 0),
  CHECK(length(trim(resource_id)) > 0)
);
CREATE INDEX api_async_jobs_claim_idx ON api_async_jobs(workload_class, status, lease_expires_at, created_at, id);
CREATE INDEX api_async_events_resource_created_idx ON api_async_events(resource_kind, resource_id, event_id);

-- +goose Down
DROP INDEX IF EXISTS api_async_events_resource_created_idx;
DROP INDEX IF EXISTS api_async_jobs_claim_idx;
DROP TABLE api_async_jobs;
