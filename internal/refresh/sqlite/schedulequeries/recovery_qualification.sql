-- name: InsertRecoveryQualificationSchedule :exec
INSERT INTO recovery_qualification_schedules (
  schedule_revision_id, schedule_id, scenario, operation, policy_version, policy_sha256, target_scope,
  artifact_identity, cron, timezone, stale_after_seconds, next_run_at,
  enabled, valid_from, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetActiveRecoveryQualificationSchedule :one
SELECT * FROM recovery_qualification_schedules
WHERE schedule_id = ? AND closed_at IS NULL;

-- name: UpdateRecoveryQualificationScheduleEnabled :execrows
UPDATE recovery_qualification_schedules
SET enabled = sqlc.arg(enabled), updated_at = sqlc.arg(updated_at)
WHERE schedule_revision_id = sqlc.arg(schedule_revision_id) AND closed_at IS NULL;

-- name: CloseRecoveryQualificationSchedule :execrows
UPDATE recovery_qualification_schedules
SET enabled = 0, closed_at = sqlc.arg(closed_at), updated_at = sqlc.arg(updated_at)
WHERE schedule_revision_id = sqlc.arg(schedule_revision_id) AND closed_at IS NULL;

-- name: ListRecoveryQualificationSchedules :many
SELECT * FROM recovery_qualification_schedules ORDER BY valid_from, schedule_revision_id;

-- name: ListDueRecoveryQualificationSchedules :many
SELECT schedule_revision_id, schedule_id, scenario, operation, policy_version, policy_sha256, target_scope,
       artifact_identity, cron, timezone, stale_after_seconds, next_run_at
FROM recovery_qualification_schedules
WHERE enabled = 1 AND closed_at IS NULL AND next_run_at <= ?
ORDER BY next_run_at, schedule_id;

-- name: GetRecoveryQualificationEnqueueCursor :one
SELECT last_schedule_id, last_schedule_revision_id
FROM recovery_qualification_enqueue_cursor
WHERE singleton_id = 1;

-- name: UpdateRecoveryQualificationEnqueueCursor :execrows
UPDATE recovery_qualification_enqueue_cursor
SET last_schedule_id = sqlc.arg(last_schedule_id),
    last_schedule_revision_id = sqlc.arg(last_schedule_revision_id),
    updated_at = sqlc.arg(updated_at)
WHERE singleton_id = 1;

-- name: AdvanceRecoveryQualificationSchedule :execrows
UPDATE recovery_qualification_schedules
SET next_run_at = sqlc.arg(next_run_at), updated_at = sqlc.arg(updated_at)
WHERE schedule_revision_id = sqlc.arg(schedule_revision_id)
  AND closed_at IS NULL AND next_run_at = sqlc.arg(previous_run_at);

-- name: InsertRecoveryQualificationOccurrence :execrows
INSERT OR IGNORE INTO recovery_qualification_occurrences (
  occurrence_id, request_digest, schedule_id, schedule_revision_id, scenario, operation,
  policy_version, policy_sha256, target_scope, artifact_identity, planned_at, expires_at,
  created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetRecoveryQualificationOccurrence :one
SELECT * FROM recovery_qualification_occurrences WHERE occurrence_id = ?;

-- name: ExpirePendingRecoveryQualificationOccurrences :execrows
UPDATE recovery_qualification_occurrences
SET status = 'expired', result = 'expired', finished_at = sqlc.arg(finished_at),
    evidence_status = 'none', evidence_next_attempt_at = NULL,
    failure_reason_redacted = 'scheduled recovery evidence became stale before execution',
    failure_code = 'qualification_expired'
WHERE status = 'pending' AND expires_at <= sqlc.arg(expires_at);

-- name: ListExpiredRecoveryQualificationLeases :many
SELECT occurrence_id, fence_generation, attempt_count
FROM recovery_qualification_occurrences
WHERE status IN ('claimed', 'running')
  AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?
ORDER BY lease_expires_at, occurrence_id;

-- name: AbandonRecoveryQualificationAttempt :execrows
UPDATE recovery_qualification_attempts
SET status = 'abandoned', finished_at = sqlc.arg(finished_at),
    failure_reason_redacted = 'worker lease expired', failure_code = 'worker_lease_expired'
WHERE occurrence_id = sqlc.arg(occurrence_id)
  AND fence_generation = sqlc.arg(fence_generation)
  AND status IN ('claimed', 'running');

-- name: RequeueRecoveryQualificationOccurrence :execrows
UPDATE recovery_qualification_occurrences
SET status = 'pending', result = 'pending', lease_owner = '', lease_expires_at = NULL,
    actor = '', claimed_at = NULL, started_at = NULL,
    restore_started_at = NULL, restore_completed_at = NULL,
    readiness_started_at = NULL, readiness_completed_at = NULL,
    failure_reason_redacted = '', failure_code = ''
WHERE occurrence_id = sqlc.arg(occurrence_id)
  AND fence_generation = sqlc.arg(fence_generation)
  AND status IN ('claimed', 'running');

-- name: NextPendingRecoveryQualificationOccurrence :one
SELECT occurrence_id
FROM recovery_qualification_occurrences
WHERE status = 'pending' AND planned_at <= sqlc.arg(planned_at) AND expires_at > sqlc.arg(expires_at)
ORDER BY planned_at, occurrence_id
LIMIT 1;

-- name: ClaimRecoveryQualificationOccurrence :execrows
UPDATE recovery_qualification_occurrences
SET status = 'claimed', result = 'pending', attempt_count = attempt_count + 1,
    fence_generation = fence_generation + 1, lease_owner = sqlc.arg(lease_owner),
    lease_expires_at = sqlc.arg(lease_expires_at), actor = sqlc.arg(actor),
    claimed_at = sqlc.arg(claimed_at), started_at = NULL,
    restore_started_at = NULL, restore_completed_at = NULL,
    readiness_started_at = NULL, readiness_completed_at = NULL, finished_at = NULL,
    failure_reason_redacted = '', failure_code = ''
WHERE occurrence_id = sqlc.arg(occurrence_id) AND status = 'pending'
  AND planned_at <= sqlc.arg(planned_at) AND expires_at > sqlc.arg(expires_at);

-- name: InsertRecoveryQualificationAttempt :exec
INSERT INTO recovery_qualification_attempts (
  occurrence_id, attempt_number, fence_generation, worker_id, actor, status,
  claimed_at, lease_expires_at
) VALUES (?, ?, ?, ?, ?, 'claimed', ?, ?);

-- name: StartRecoveryQualificationOccurrence :execrows
UPDATE recovery_qualification_occurrences
SET status = 'running', started_at = sqlc.arg(started_at)
WHERE occurrence_id = sqlc.arg(occurrence_id) AND status = 'claimed'
  AND lease_owner = sqlc.arg(lease_owner) AND fence_generation = sqlc.arg(fence_generation)
  AND lease_expires_at > sqlc.arg(lease_valid_at);

-- name: StartRecoveryQualificationAttempt :execrows
UPDATE recovery_qualification_attempts
SET status = 'running', started_at = sqlc.arg(started_at)
WHERE occurrence_id = sqlc.arg(occurrence_id)
  AND fence_generation = sqlc.arg(fence_generation) AND status = 'claimed';

-- name: HeartbeatRecoveryQualificationOccurrence :execrows
UPDATE recovery_qualification_occurrences
SET lease_expires_at = sqlc.arg(lease_expires_at)
WHERE occurrence_id = sqlc.arg(occurrence_id) AND status IN ('claimed', 'running')
  AND lease_owner = sqlc.arg(lease_owner) AND fence_generation = sqlc.arg(fence_generation)
  AND lease_expires_at > sqlc.arg(lease_valid_at);

-- name: HeartbeatRecoveryQualificationAttempt :execrows
UPDATE recovery_qualification_attempts
SET lease_expires_at = sqlc.arg(lease_expires_at)
WHERE occurrence_id = sqlc.arg(occurrence_id)
  AND fence_generation = sqlc.arg(fence_generation) AND status IN ('claimed', 'running');

-- name: StartRecoveryRestorePhase :execrows
UPDATE recovery_qualification_occurrences
SET restore_started_at = sqlc.arg(phase_at)
WHERE occurrence_id = sqlc.arg(occurrence_id) AND operation = 'restore' AND status = 'running'
  AND restore_started_at IS NULL AND restore_completed_at IS NULL
  AND lease_owner = sqlc.arg(lease_owner) AND fence_generation = sqlc.arg(fence_generation)
  AND lease_expires_at > sqlc.arg(lease_valid_at);

-- name: CompleteRecoveryRestorePhase :execrows
UPDATE recovery_qualification_occurrences
SET restore_completed_at = sqlc.arg(phase_at)
WHERE occurrence_id = sqlc.arg(occurrence_id) AND operation = 'restore' AND status = 'running'
  AND restore_started_at IS NOT NULL AND restore_completed_at IS NULL
  AND restore_started_at <= sqlc.arg(phase_at)
  AND lease_owner = sqlc.arg(lease_owner) AND fence_generation = sqlc.arg(fence_generation)
  AND lease_expires_at > sqlc.arg(lease_valid_at);

-- name: StartRecoveryReadinessPhase :execrows
UPDATE recovery_qualification_occurrences
SET readiness_started_at = sqlc.arg(phase_at)
WHERE occurrence_id = sqlc.arg(occurrence_id) AND operation IN ('upgrade', 'rollback') AND status = 'running'
  AND readiness_started_at IS NULL AND readiness_completed_at IS NULL
  AND lease_owner = sqlc.arg(lease_owner) AND fence_generation = sqlc.arg(fence_generation)
  AND lease_expires_at > sqlc.arg(lease_valid_at);

-- name: CompleteRecoveryReadinessPhase :execrows
UPDATE recovery_qualification_occurrences
SET readiness_completed_at = sqlc.arg(phase_at)
WHERE occurrence_id = sqlc.arg(occurrence_id) AND operation IN ('upgrade', 'rollback') AND status = 'running'
  AND readiness_started_at IS NOT NULL AND readiness_completed_at IS NULL
  AND readiness_started_at <= sqlc.arg(phase_at)
  AND lease_owner = sqlc.arg(lease_owner) AND fence_generation = sqlc.arg(fence_generation)
  AND lease_expires_at > sqlc.arg(lease_valid_at);

-- name: CompleteRecoveryQualificationOccurrence :execrows
UPDATE recovery_qualification_occurrences
SET status = 'succeeded', result = 'success', finished_at = sqlc.arg(finished_at),
    recovery_point_at = sqlc.arg(recovery_point_at),
    recovery_point_age_seconds = sqlc.arg(recovery_point_age_seconds),
    restore_duration_millis = sqlc.arg(restore_duration_millis),
    readiness_duration_millis = sqlc.arg(readiness_duration_millis),
    qualification_duration_millis = sqlc.arg(qualification_duration_millis),
    evidence_refs_json = sqlc.arg(evidence_refs_json), evidence_status = 'pending',
    evidence_next_attempt_at = sqlc.arg(evidence_next_attempt_at),
    lease_owner = '', lease_expires_at = NULL, failure_reason_redacted = '', failure_code = ''
WHERE occurrence_id = sqlc.arg(occurrence_id) AND status = 'running'
  AND lease_owner = sqlc.arg(lease_owner) AND fence_generation = sqlc.arg(fence_generation)
  AND lease_expires_at > sqlc.arg(lease_valid_at);

-- name: CompleteRecoveryQualificationAttempt :execrows
UPDATE recovery_qualification_attempts
SET status = 'succeeded', finished_at = sqlc.arg(finished_at), failure_reason_redacted = '', failure_code = ''
WHERE occurrence_id = sqlc.arg(occurrence_id)
  AND fence_generation = sqlc.arg(fence_generation) AND status = 'running';

-- name: FailRecoveryQualificationOccurrence :execrows
UPDATE recovery_qualification_occurrences
SET status = 'failed', result = 'failure', finished_at = sqlc.arg(finished_at),
    recovery_point_at = sqlc.arg(recovery_point_at),
    recovery_point_age_seconds = sqlc.arg(recovery_point_age_seconds),
    restore_duration_millis = sqlc.arg(restore_duration_millis),
    readiness_duration_millis = sqlc.arg(readiness_duration_millis),
    qualification_duration_millis = sqlc.arg(qualification_duration_millis),
    evidence_refs_json = sqlc.arg(evidence_refs_json), evidence_status = sqlc.arg(evidence_status),
    evidence_next_attempt_at = sqlc.arg(evidence_next_attempt_at),
    lease_owner = '', lease_expires_at = NULL,
    failure_reason_redacted = sqlc.arg(failure_reason_redacted),
    failure_code = sqlc.arg(failure_code)
WHERE occurrence_id = sqlc.arg(occurrence_id) AND status IN ('claimed', 'running')
  AND lease_owner = sqlc.arg(lease_owner) AND fence_generation = sqlc.arg(fence_generation)
  AND lease_expires_at > sqlc.arg(lease_valid_at);

-- name: FailRecoveryQualificationAttempt :execrows
UPDATE recovery_qualification_attempts
SET status = 'failed', finished_at = sqlc.arg(finished_at),
    failure_reason_redacted = sqlc.arg(failure_reason_redacted),
    failure_code = sqlc.arg(failure_code)
WHERE occurrence_id = sqlc.arg(occurrence_id)
  AND fence_generation = sqlc.arg(fence_generation) AND status IN ('claimed', 'running');

-- name: CancelRecoveryQualificationOccurrence :execrows
UPDATE recovery_qualification_occurrences
SET status = 'canceled', result = 'canceled', finished_at = sqlc.arg(finished_at),
    qualification_duration_millis = sqlc.arg(qualification_duration_millis),
    evidence_status = 'none', evidence_next_attempt_at = NULL, lease_owner = '', lease_expires_at = NULL,
    failure_reason_redacted = sqlc.arg(failure_reason_redacted),
    failure_code = sqlc.arg(failure_code)
WHERE occurrence_id = sqlc.arg(occurrence_id) AND status IN ('claimed', 'running')
  AND lease_owner = sqlc.arg(lease_owner) AND fence_generation = sqlc.arg(fence_generation)
  AND lease_expires_at > sqlc.arg(lease_valid_at);

-- name: CancelRecoveryQualificationAttempt :execrows
UPDATE recovery_qualification_attempts
SET status = 'canceled', finished_at = sqlc.arg(finished_at),
    failure_reason_redacted = sqlc.arg(failure_reason_redacted),
    failure_code = sqlc.arg(failure_code)
WHERE occurrence_id = sqlc.arg(occurrence_id)
  AND fence_generation = sqlc.arg(fence_generation) AND status IN ('claimed', 'running');

-- name: ListRecoveryQualificationAttempts :many
SELECT * FROM recovery_qualification_attempts
WHERE occurrence_id = ? ORDER BY attempt_number;

-- name: ListRecoveryQualificationOccurrences :many
SELECT * FROM recovery_qualification_occurrences
ORDER BY planned_at, occurrence_id;

-- name: CountAbandonedRecoveryQualificationAttempts :one
SELECT COUNT(*) FROM recovery_qualification_attempts WHERE status = 'abandoned';

-- name: ListExpiredRecoveryEvidenceLeases :many
SELECT occurrence_id, evidence_fence_generation, evidence_attempt_count
FROM recovery_qualification_occurrences
WHERE evidence_status = 'claimed' AND evidence_lease_expires_at IS NOT NULL
  AND evidence_lease_expires_at <= ?
ORDER BY evidence_lease_expires_at, occurrence_id;

-- name: AbandonRecoveryEvidenceAttempt :execrows
UPDATE recovery_qualification_evidence_attempts
SET status = 'abandoned', finished_at = sqlc.arg(finished_at),
    failure_reason_redacted = 'publisher lease expired', failure_code = 'publisher_lease_expired'
WHERE occurrence_id = sqlc.arg(occurrence_id)
  AND fence_generation = sqlc.arg(fence_generation) AND status = 'claimed';

-- name: RequeueRecoveryEvidence :execrows
UPDATE recovery_qualification_occurrences
SET evidence_status = 'failed', evidence_lease_owner = '', evidence_lease_expires_at = NULL,
    evidence_next_attempt_at = sqlc.arg(evidence_next_attempt_at),
    evidence_failure_reason_redacted = 'publisher lease expired',
    evidence_failure_code = 'publisher_lease_expired'
WHERE occurrence_id = sqlc.arg(occurrence_id)
  AND evidence_fence_generation = sqlc.arg(fence_generation) AND evidence_status = 'claimed';

-- name: NextPendingRecoveryEvidence :one
SELECT occurrence_id
FROM recovery_qualification_occurrences
WHERE status IN ('succeeded', 'failed', 'canceled', 'expired')
  AND evidence_status IN ('pending', 'failed')
  AND evidence_next_attempt_at IS NOT NULL AND evidence_next_attempt_at <= sqlc.arg(eligible_at)
ORDER BY finished_at, occurrence_id
LIMIT 1;

-- name: ClaimRecoveryEvidence :execrows
UPDATE recovery_qualification_occurrences
SET evidence_status = 'claimed', evidence_attempt_count = evidence_attempt_count + 1,
    evidence_fence_generation = evidence_fence_generation + 1,
    evidence_lease_owner = sqlc.arg(evidence_lease_owner),
    evidence_lease_expires_at = sqlc.arg(evidence_lease_expires_at),
    evidence_failure_reason_redacted = '', evidence_failure_code = ''
WHERE occurrence_id = sqlc.arg(occurrence_id)
  AND status IN ('succeeded', 'failed', 'canceled', 'expired')
  AND evidence_status IN ('pending', 'failed')
  AND evidence_next_attempt_at IS NOT NULL AND evidence_next_attempt_at <= sqlc.arg(eligible_at);

-- name: InsertRecoveryEvidenceAttempt :exec
INSERT INTO recovery_qualification_evidence_attempts (
  occurrence_id, attempt_number, fence_generation, publisher_id, status,
  claimed_at, lease_expires_at
) VALUES (?, ?, ?, ?, 'claimed', ?, ?);

-- name: PublishRecoveryEvidence :execrows
UPDATE recovery_qualification_occurrences
SET evidence_status = 'published', evidence_lease_owner = '', evidence_lease_expires_at = NULL,
    evidence_next_attempt_at = NULL,
    evidence_published_at = sqlc.arg(evidence_published_at), evidence_failure_reason_redacted = '',
    evidence_failure_code = ''
WHERE occurrence_id = sqlc.arg(occurrence_id) AND evidence_status = 'claimed'
  AND evidence_lease_owner = sqlc.arg(evidence_lease_owner)
  AND evidence_fence_generation = sqlc.arg(evidence_fence_generation)
  AND evidence_lease_expires_at > sqlc.arg(lease_valid_at);

-- name: PublishRecoveryEvidenceAttempt :execrows
UPDATE recovery_qualification_evidence_attempts
SET status = 'published', finished_at = sqlc.arg(finished_at), failure_reason_redacted = '', failure_code = ''
WHERE occurrence_id = sqlc.arg(occurrence_id)
  AND fence_generation = sqlc.arg(fence_generation) AND status = 'claimed';

-- name: FailRecoveryEvidence :execrows
UPDATE recovery_qualification_occurrences
SET evidence_status = 'failed', evidence_lease_owner = '', evidence_lease_expires_at = NULL,
    evidence_next_attempt_at = sqlc.arg(evidence_next_attempt_at),
    evidence_failure_reason_redacted = sqlc.arg(evidence_failure_reason_redacted),
    evidence_failure_code = sqlc.arg(evidence_failure_code)
WHERE occurrence_id = sqlc.arg(occurrence_id) AND evidence_status = 'claimed'
  AND evidence_lease_owner = sqlc.arg(evidence_lease_owner)
  AND evidence_fence_generation = sqlc.arg(evidence_fence_generation)
  AND evidence_lease_expires_at > sqlc.arg(lease_valid_at);

-- name: FailRecoveryEvidenceAttempt :execrows
UPDATE recovery_qualification_evidence_attempts
SET status = 'failed', finished_at = sqlc.arg(finished_at),
    failure_reason_redacted = sqlc.arg(failure_reason_redacted),
    failure_code = sqlc.arg(failure_code)
WHERE occurrence_id = sqlc.arg(occurrence_id)
  AND fence_generation = sqlc.arg(fence_generation) AND status = 'claimed';

-- name: ListRecoveryEvidenceAttempts :many
SELECT * FROM recovery_qualification_evidence_attempts
WHERE occurrence_id = ? ORDER BY attempt_number;

-- name: DeleteRecoveryQualificationOccurrence :execrows
DELETE FROM recovery_qualification_occurrences
WHERE occurrence_id = sqlc.arg(occurrence_id)
  AND status IN ('succeeded', 'failed', 'canceled', 'expired')
  AND (evidence_status <> 'claimed' OR evidence_lease_expires_at <= sqlc.arg(active_at))
  AND finished_at < sqlc.arg(finished_before);
