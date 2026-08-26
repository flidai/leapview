-- +goose Up

CREATE TABLE recovery_qualification_schedules (
  schedule_revision_id TEXT PRIMARY KEY,
  schedule_id TEXT NOT NULL,
  scenario TEXT NOT NULL,
  operation TEXT NOT NULL CHECK (operation IN ('backup', 'restore', 'upgrade', 'rollback')),
  policy_version TEXT NOT NULL,
  target_scope TEXT NOT NULL,
  artifact_identity TEXT NOT NULL,
  cron TEXT NOT NULL,
  timezone TEXT NOT NULL,
  stale_after_seconds INTEGER NOT NULL CHECK (stale_after_seconds > 0),
  next_run_at TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  valid_from TEXT NOT NULL,
  closed_at TEXT,
  updated_at TEXT NOT NULL
);

CREATE INDEX recovery_qualification_schedules_due_idx
  ON recovery_qualification_schedules(enabled, closed_at, next_run_at, schedule_id);
CREATE UNIQUE INDEX recovery_qualification_schedules_active_idx
  ON recovery_qualification_schedules(schedule_id) WHERE closed_at IS NULL;

CREATE TABLE recovery_qualification_occurrences (
  occurrence_id TEXT PRIMARY KEY,
  request_digest TEXT NOT NULL,
  schedule_id TEXT NOT NULL,
  schedule_revision_id TEXT NOT NULL,
  scenario TEXT NOT NULL,
  operation TEXT NOT NULL CHECK (operation IN ('backup', 'restore', 'upgrade', 'rollback')),
  policy_version TEXT NOT NULL,
  target_scope TEXT NOT NULL,
  artifact_identity TEXT NOT NULL,
  planned_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'claimed', 'running', 'succeeded', 'failed', 'canceled', 'expired')),
  result TEXT NOT NULL DEFAULT 'pending'
    CHECK (result IN ('pending', 'success', 'failure', 'canceled', 'expired')),
  attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  fence_generation INTEGER NOT NULL DEFAULT 0 CHECK (fence_generation >= 0),
  lease_owner TEXT NOT NULL DEFAULT '',
  lease_expires_at TEXT,
  actor TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  claimed_at TEXT,
  started_at TEXT,
  finished_at TEXT,
  recovery_point_at TEXT,
  recovery_point_age_seconds INTEGER CHECK (recovery_point_age_seconds IS NULL OR recovery_point_age_seconds >= 0),
  restore_duration_millis INTEGER CHECK (restore_duration_millis IS NULL OR restore_duration_millis >= 0),
  readiness_duration_millis INTEGER CHECK (readiness_duration_millis IS NULL OR readiness_duration_millis >= 0),
  failure_reason_redacted TEXT NOT NULL DEFAULT '',
  failure_code TEXT NOT NULL DEFAULT '' CHECK (length(failure_code) <= 64),
  evidence_refs_json TEXT NOT NULL DEFAULT '[]'
    CHECK (json_valid(evidence_refs_json) AND json_type(evidence_refs_json) = 'array'),
  evidence_status TEXT NOT NULL DEFAULT 'pending'
    CHECK (evidence_status IN ('pending', 'claimed', 'published', 'failed')),
  evidence_attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (evidence_attempt_count >= 0),
  evidence_fence_generation INTEGER NOT NULL DEFAULT 0 CHECK (evidence_fence_generation >= 0),
  evidence_lease_owner TEXT NOT NULL DEFAULT '',
  evidence_lease_expires_at TEXT,
  evidence_published_at TEXT,
  evidence_failure_reason_redacted TEXT NOT NULL DEFAULT '',
  evidence_failure_code TEXT NOT NULL DEFAULT '' CHECK (length(evidence_failure_code) <= 64)
);

CREATE INDEX recovery_qualification_occurrences_claim_idx
  ON recovery_qualification_occurrences(status, planned_at, occurrence_id);
CREATE INDEX recovery_qualification_occurrences_retention_idx
  ON recovery_qualification_occurrences(scenario, operation, status, finished_at, occurrence_id);
CREATE INDEX recovery_qualification_evidence_claim_idx
  ON recovery_qualification_occurrences(evidence_status, finished_at, occurrence_id);

CREATE TABLE recovery_qualification_attempts (
  occurrence_id TEXT NOT NULL REFERENCES recovery_qualification_occurrences(occurrence_id) ON DELETE CASCADE,
  attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
  fence_generation INTEGER NOT NULL CHECK (fence_generation > 0),
  worker_id TEXT NOT NULL,
  actor TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('claimed', 'running', 'succeeded', 'failed', 'canceled', 'abandoned')),
  claimed_at TEXT NOT NULL,
  started_at TEXT,
  lease_expires_at TEXT NOT NULL,
  finished_at TEXT,
  failure_reason_redacted TEXT NOT NULL DEFAULT '',
  failure_code TEXT NOT NULL DEFAULT '' CHECK (length(failure_code) <= 64),
  PRIMARY KEY (occurrence_id, attempt_number),
  UNIQUE (occurrence_id, fence_generation)
);

CREATE TABLE recovery_qualification_evidence_attempts (
  occurrence_id TEXT NOT NULL REFERENCES recovery_qualification_occurrences(occurrence_id) ON DELETE CASCADE,
  attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
  fence_generation INTEGER NOT NULL CHECK (fence_generation > 0),
  publisher_id TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('claimed', 'published', 'failed', 'abandoned')),
  claimed_at TEXT NOT NULL,
  lease_expires_at TEXT NOT NULL,
  finished_at TEXT,
  failure_reason_redacted TEXT NOT NULL DEFAULT '',
  failure_code TEXT NOT NULL DEFAULT '' CHECK (length(failure_code) <= 64),
  PRIMARY KEY (occurrence_id, attempt_number),
  UNIQUE (occurrence_id, fence_generation)
);

-- +goose Down
DROP TABLE recovery_qualification_evidence_attempts;
DROP TABLE recovery_qualification_attempts;
DROP INDEX recovery_qualification_evidence_claim_idx;
DROP INDEX recovery_qualification_occurrences_retention_idx;
DROP INDEX recovery_qualification_occurrences_claim_idx;
DROP TABLE recovery_qualification_occurrences;
DROP INDEX recovery_qualification_schedules_active_idx;
DROP INDEX recovery_qualification_schedules_due_idx;
DROP TABLE recovery_qualification_schedules;
