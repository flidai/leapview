-- name: GetActiveRecoveryQualificationSchedule :one
SELECT schedule_revision_id, enabled, closed_at FROM refresh.recovery_qualification_schedule WHERE schedule_id=$1 AND closed_at IS NULL FOR UPDATE;
-- name: SetRecoveryQualificationScheduleEnabled :exec
UPDATE refresh.recovery_qualification_schedule SET enabled=$1, updated_at=$2 WHERE schedule_revision_id=$3 AND closed_at IS NULL;
-- name: CloseRecoveryQualificationSchedule :exec
UPDATE refresh.recovery_qualification_schedule SET enabled=false, closed_at=$1, updated_at=$1 WHERE schedule_revision_id=$2 AND closed_at IS NULL;
-- name: InsertRecoveryQualificationSchedule :exec
INSERT INTO refresh.recovery_qualification_schedule(schedule_revision_id,schedule_id,scenario,operation,policy_version,policy_sha256,target_scope,artifact_identity,cron,timezone,stale_after,next_run_at,enabled,valid_from,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14);
-- name: ListActiveRecoveryQualificationSchedules :many
SELECT schedule_id,scenario,operation,policy_version,policy_sha256,target_scope,artifact_identity,cron,timezone,stale_after FROM refresh.recovery_qualification_schedule WHERE enabled AND closed_at IS NULL ORDER BY schedule_id;
-- name: GetRecoveryQualificationScheduleForMaterialize :one
SELECT schedule_id,scenario,operation,policy_version,policy_sha256,target_scope,artifact_identity,cron,timezone,stale_after,next_run_at,enabled FROM refresh.recovery_qualification_schedule WHERE schedule_revision_id=$1 FOR UPDATE;
-- name: AdvanceRecoveryQualificationSchedule :execrows
UPDATE refresh.recovery_qualification_schedule SET next_run_at=$1,updated_at=$2 WHERE schedule_revision_id=$3 AND closed_at IS NULL AND next_run_at=$4;
-- name: ListDueRecoveryQualificationSchedules :many
SELECT schedule_revision_id,schedule_id,scenario,operation,policy_version,policy_sha256,target_scope,artifact_identity,cron,timezone,stale_after,next_run_at
FROM refresh.recovery_qualification_schedule WHERE enabled AND closed_at IS NULL AND next_run_at<=$1 ORDER BY schedule_id,schedule_revision_id FOR UPDATE;
-- name: GetRecoveryQualificationEnqueueCursor :one
SELECT last_schedule_id,last_schedule_revision_id FROM refresh.recovery_qualification_enqueue_cursor WHERE singleton_id=true FOR UPDATE;
-- name: UpdateRecoveryQualificationEnqueueCursor :execrows
UPDATE refresh.recovery_qualification_enqueue_cursor SET last_schedule_id=$1,last_schedule_revision_id=$2,updated_at=$3 WHERE singleton_id=true;

-- name: InsertRecoveryQualificationOccurrence :execrows
INSERT INTO refresh.recovery_qualification_occurrence(occurrence_id,request_digest,schedule_id,schedule_revision_id,scenario,operation,policy_version,policy_sha256,target_scope,artifact_identity,planned_at,expires_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT (occurrence_id) DO NOTHING;
-- name: ExpirePendingRecoveryQualificationOccurrences :exec
UPDATE refresh.recovery_qualification_occurrence SET status='expired',result='expired',finished_at=$1,evidence_status='none',evidence_next_attempt_at=NULL,failure_reason_redacted='scheduled recovery evidence became stale before execution',failure_code='qualification_expired' WHERE status='pending' AND expires_at<=$1;
-- name: NextPendingRecoveryQualificationOccurrence :one
SELECT occurrence_id FROM refresh.recovery_qualification_occurrence WHERE status='pending' AND planned_at<=$1 AND expires_at>$1 ORDER BY planned_at,occurrence_id LIMIT 1 FOR UPDATE SKIP LOCKED;
-- name: ClaimRecoveryQualificationOccurrence :one
UPDATE refresh.recovery_qualification_occurrence SET status='claimed',attempt_count=attempt_count+1,fence_generation=fence_generation+1,lease_owner=$2,lease_expires_at=$3,actor=$4,claimed_at=$1,started_at=NULL,restore_started_at=NULL,restore_completed_at=NULL,readiness_started_at=NULL,readiness_completed_at=NULL,finished_at=NULL,failure_reason_redacted='',failure_code='' WHERE occurrence_id=$5 AND status='pending' AND planned_at<=$1 AND expires_at>$1 RETURNING attempt_count,fence_generation;
-- name: InsertRecoveryQualificationAttempt :exec
INSERT INTO refresh.recovery_qualification_attempt(occurrence_id,attempt_number,fence_generation,worker_id,actor,status,claimed_at,lease_expires_at) VALUES($1,$2,$3,$4,$5,'claimed',$6,$7);
-- name: ListExpiredRecoveryQualificationLeases :many
SELECT occurrence_id,fence_generation FROM refresh.recovery_qualification_occurrence WHERE status IN ('claimed','running') AND lease_expires_at<=$1 ORDER BY lease_expires_at,occurrence_id FOR UPDATE;
-- name: AbandonRecoveryQualificationAttempt :exec
UPDATE refresh.recovery_qualification_attempt SET status='abandoned',finished_at=$1,failure_reason_redacted='worker lease expired',failure_code='worker_lease_expired' WHERE occurrence_id=$2 AND fence_generation=$3 AND status IN ('claimed','running');
-- name: RequeueRecoveryQualificationOccurrence :exec
UPDATE refresh.recovery_qualification_occurrence SET status='pending',result='pending',lease_owner='',lease_expires_at=NULL,actor='',claimed_at=NULL,started_at=NULL,restore_started_at=NULL,restore_completed_at=NULL,readiness_started_at=NULL,readiness_completed_at=NULL,failure_reason_redacted='',failure_code='' WHERE occurrence_id=$1 AND fence_generation=$2 AND status IN ('claimed','running');
-- name: StartRecoveryQualificationOccurrence :execrows
UPDATE refresh.recovery_qualification_occurrence SET status='running',started_at=$4 WHERE occurrence_id=$1 AND lease_owner=$2 AND fence_generation=$3 AND lease_expires_at>$4 AND status='claimed' AND claimed_at IS NOT NULL AND planned_at<=claimed_at AND claimed_at<=$4;
-- name: StartRecoveryQualificationAttempt :execrows
UPDATE refresh.recovery_qualification_attempt SET status='running',started_at=$3 WHERE occurrence_id=$1 AND fence_generation=$2 AND status='claimed' AND claimed_at<=$3;
-- name: HeartbeatRecoveryQualificationOccurrence :execrows
UPDATE refresh.recovery_qualification_occurrence SET lease_expires_at=$5 WHERE occurrence_id=$1 AND lease_owner=$2 AND fence_generation=$3 AND lease_expires_at>$4 AND status IN ('claimed','running');
-- name: HeartbeatRecoveryQualificationAttempt :execrows
UPDATE refresh.recovery_qualification_attempt SET lease_expires_at=$3 WHERE occurrence_id=$1 AND fence_generation=$2 AND status IN ('claimed','running');
-- name: StartRecoveryRestorePhase :execrows
UPDATE refresh.recovery_qualification_occurrence SET restore_started_at=$4 WHERE occurrence_id=$1 AND lease_owner=$2 AND fence_generation=$3 AND lease_expires_at>$4 AND status='running' AND operation='restore' AND started_at IS NOT NULL AND started_at<=$4 AND restore_started_at IS NULL AND restore_completed_at IS NULL;
-- name: CompleteRecoveryRestorePhase :execrows
UPDATE refresh.recovery_qualification_occurrence SET restore_completed_at=$4 WHERE occurrence_id=$1 AND lease_owner=$2 AND fence_generation=$3 AND lease_expires_at>$4 AND status='running' AND operation='restore' AND restore_started_at IS NOT NULL AND restore_completed_at IS NULL AND restore_started_at<=$4;
-- name: RecordRecoveryQualificationCheckpoint :execrows
UPDATE refresh.recovery_qualification_occurrence SET evidence_refs=sqlc.arg(evidence_refs)::jsonb WHERE occurrence_id=sqlc.arg(occurrence_id) AND lease_owner=sqlc.arg(lease_owner) AND fence_generation=sqlc.arg(fence_generation) AND lease_expires_at>sqlc.arg(active_at) AND status='running';
-- name: StartRecoveryReadinessPhase :execrows
UPDATE refresh.recovery_qualification_occurrence SET readiness_started_at=$4 WHERE occurrence_id=$1 AND lease_owner=$2 AND fence_generation=$3 AND lease_expires_at>$4 AND status='running' AND operation IN ('upgrade','rollback') AND started_at IS NOT NULL AND started_at<=$4 AND readiness_started_at IS NULL AND readiness_completed_at IS NULL;
-- name: CompleteRecoveryReadinessPhase :execrows
UPDATE refresh.recovery_qualification_occurrence SET readiness_completed_at=$4 WHERE occurrence_id=$1 AND lease_owner=$2 AND fence_generation=$3 AND lease_expires_at>$4 AND status='running' AND operation IN ('upgrade','rollback') AND readiness_started_at IS NOT NULL AND readiness_completed_at IS NULL AND readiness_started_at<=$4;
-- name: CompleteRecoveryQualificationOccurrence :execrows
UPDATE refresh.recovery_qualification_occurrence SET status='succeeded',result='success',finished_at=$4,recovery_point_at=$5,recovery_point_age_seconds=$6,restore_duration_millis=$7,readiness_duration_millis=$8,qualification_duration_millis=$9,evidence_refs=$10,evidence_status='pending',evidence_next_attempt_at=$4,lease_owner='',lease_expires_at=NULL,failure_reason_redacted='',failure_code='' WHERE occurrence_id=$1 AND lease_owner=$2 AND fence_generation=$3 AND lease_expires_at>$4 AND status='running';
-- name: CompleteRecoveryQualificationAttempt :execrows
UPDATE refresh.recovery_qualification_attempt SET status='succeeded',finished_at=$3,failure_reason_redacted='',failure_code='' WHERE occurrence_id=$1 AND fence_generation=$2 AND status='running';
-- name: FailRecoveryQualificationOccurrence :execrows
UPDATE refresh.recovery_qualification_occurrence SET status='failed',result='failure',finished_at=$4,recovery_point_at=$5,recovery_point_age_seconds=$6,restore_duration_millis=$7,readiness_duration_millis=$8,qualification_duration_millis=$9,evidence_refs=$10,evidence_status=$11,evidence_next_attempt_at=$12,lease_owner='',lease_expires_at=NULL,failure_reason_redacted=$13,failure_code=$14 WHERE occurrence_id=$1 AND lease_owner=$2 AND fence_generation=$3 AND lease_expires_at>$4 AND status IN ('claimed','running');
-- name: FailRecoveryQualificationAttempt :execrows
UPDATE refresh.recovery_qualification_attempt SET status='failed',finished_at=$3,failure_reason_redacted=$4,failure_code=$5 WHERE occurrence_id=$1 AND fence_generation=$2 AND status IN ('claimed','running');
-- name: CancelRecoveryQualificationOccurrence :execrows
UPDATE refresh.recovery_qualification_occurrence SET status='canceled',result='canceled',finished_at=$4,qualification_duration_millis=$5,evidence_status='none',evidence_next_attempt_at=NULL,lease_owner='',lease_expires_at=NULL,failure_reason_redacted=$6,failure_code=$7 WHERE occurrence_id=$1 AND lease_owner=$2 AND fence_generation=$3 AND lease_expires_at>$4 AND status IN ('claimed','running');
-- name: CancelRecoveryQualificationAttempt :execrows
UPDATE refresh.recovery_qualification_attempt SET status='canceled',finished_at=$3,failure_reason_redacted=$4,failure_code=$5 WHERE occurrence_id=$1 AND fence_generation=$2 AND status IN ('claimed','running');

-- name: NextPendingRecoveryEvidence :one
SELECT occurrence_id FROM refresh.recovery_qualification_occurrence WHERE status IN ('succeeded','failed','canceled','expired') AND evidence_status IN ('pending','failed') AND evidence_next_attempt_at<=$1 ORDER BY finished_at,occurrence_id LIMIT 1 FOR UPDATE SKIP LOCKED;
-- name: ClaimRecoveryEvidence :one
UPDATE refresh.recovery_qualification_occurrence SET evidence_status='claimed',evidence_attempt_count=evidence_attempt_count+1,evidence_fence_generation=evidence_fence_generation+1,evidence_lease_owner=$2,evidence_lease_expires_at=$3,evidence_failure_reason_redacted='',evidence_failure_code='' WHERE occurrence_id=$1 RETURNING evidence_attempt_count,evidence_fence_generation;
-- name: InsertRecoveryEvidenceAttempt :exec
INSERT INTO refresh.recovery_qualification_evidence_attempt(occurrence_id,attempt_number,fence_generation,publisher_id,status,claimed_at,lease_expires_at) VALUES($1,$2,$3,$4,'claimed',$5,$6);
-- name: ListExpiredRecoveryEvidenceLeases :many
SELECT occurrence_id,evidence_fence_generation FROM refresh.recovery_qualification_occurrence WHERE evidence_status='claimed' AND evidence_lease_expires_at<=$1 FOR UPDATE;
-- name: AbandonRecoveryEvidenceAttempt :exec
UPDATE refresh.recovery_qualification_evidence_attempt SET status='abandoned',finished_at=$3,failure_reason_redacted='publisher lease expired',failure_code='publisher_lease_expired' WHERE occurrence_id=$1 AND fence_generation=$2 AND status='claimed';
-- name: RequeueRecoveryEvidence :exec
UPDATE refresh.recovery_qualification_occurrence SET evidence_status='failed',evidence_lease_owner='',evidence_lease_expires_at=NULL,evidence_next_attempt_at=$3,evidence_failure_reason_redacted='publisher lease expired',evidence_failure_code='publisher_lease_expired' WHERE occurrence_id=$1 AND evidence_fence_generation=$2 AND evidence_status='claimed';
-- name: PublishRecoveryEvidence :execrows
UPDATE refresh.recovery_qualification_occurrence SET evidence_status='published',evidence_lease_owner='',evidence_lease_expires_at=NULL,evidence_next_attempt_at=NULL,evidence_published_at=$4,evidence_failure_reason_redacted='',evidence_failure_code='' WHERE occurrence_id=$1 AND evidence_lease_owner=$2 AND evidence_fence_generation=$3 AND evidence_lease_expires_at>$4 AND evidence_status='claimed';
-- name: PublishRecoveryEvidenceAttempt :execrows
UPDATE refresh.recovery_qualification_evidence_attempt SET status='published',finished_at=$3,failure_reason_redacted='',failure_code='' WHERE occurrence_id=$1 AND fence_generation=$2 AND status='claimed';
-- name: GetRecoveryEvidenceAttemptCount :one
SELECT evidence_attempt_count FROM refresh.recovery_qualification_occurrence WHERE occurrence_id=$1;
-- name: FailRecoveryEvidence :execrows
UPDATE refresh.recovery_qualification_occurrence SET evidence_status='failed',evidence_lease_owner='',evidence_lease_expires_at=NULL,evidence_next_attempt_at=$5,evidence_failure_reason_redacted=$6,evidence_failure_code=$7 WHERE occurrence_id=$1 AND evidence_lease_owner=$2 AND evidence_fence_generation=$3 AND evidence_lease_expires_at>$4 AND evidence_status='claimed';
-- name: FailRecoveryEvidenceAttempt :execrows
UPDATE refresh.recovery_qualification_evidence_attempt SET status='failed',finished_at=$3,failure_reason_redacted=$4,failure_code=$5 WHERE occurrence_id=$1 AND fence_generation=$2 AND status='claimed';

-- name: ListRecoveryQualificationOccurrenceIDs :many
SELECT occurrence_id FROM refresh.recovery_qualification_occurrence ORDER BY planned_at,occurrence_id;
-- name: GetRecoveryQualificationOccurrence :one
SELECT * FROM refresh.recovery_qualification_occurrence WHERE occurrence_id=$1;
-- name: ListRecoveryQualificationAttempts :many
SELECT * FROM refresh.recovery_qualification_attempt WHERE occurrence_id=$1 ORDER BY attempt_number;
-- name: ListRecoveryEvidenceAttempts :many
SELECT * FROM refresh.recovery_qualification_evidence_attempt WHERE occurrence_id=$1 ORDER BY attempt_number;
-- name: ListRecoveryQualificationScheduleStatus :many
SELECT cron,timezone,stale_after,next_run_at FROM refresh.recovery_qualification_schedule WHERE enabled AND closed_at IS NULL;
-- name: CountAbandonedRecoveryQualificationAttempts :one
SELECT count(*) FROM refresh.recovery_qualification_attempt WHERE status='abandoned';
-- name: RetainRecoveryQualificationOccurrences :many
SELECT refresh.retain_recovery_qualification_occurrences($1,$2,$3) AS occurrence_id;
-- name: ListRecoveryQualificationRetentionProtectedIDs :many
WITH ranked AS (
  SELECT occurrence_id,status,
         row_number() OVER (PARTITION BY scenario,operation,
           CASE WHEN status='succeeded' THEN 'success' ELSE 'failure' END
           ORDER BY finished_at DESC,occurrence_id DESC) AS rank
  FROM refresh.recovery_qualification_occurrence
  WHERE status IN ('succeeded','failed','expired')
)
SELECT occurrence_id FROM ranked WHERE rank=1 ORDER BY occurrence_id LIMIT $1;
