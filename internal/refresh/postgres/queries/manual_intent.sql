-- Named PostgreSQL leaves for durable manual refresh requests. Callers retain
-- transaction ownership so scope locks, run attachment, and audit handoff
-- share the same commit.

-- name: GetManualIntentByKey :one
SELECT * FROM refresh.manual_intent
WHERE project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment)
  AND principal_id=sqlc.arg(principal_id) AND idempotency_key=sqlc.arg(idempotency_key);

-- name: CountWaitingManualIntents :one
SELECT count(*) FROM refresh.manual_intent
WHERE project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment) AND status='waiting';

-- name: InsertManualIntent :one
INSERT INTO refresh.manual_intent
 (intent_id,reserved_run_id,project_id,environment,pipeline_id,target_id,principal_id,source_digest,idempotency_key,request_digest,audit_intent,authority_envelope)
VALUES (sqlc.arg(intent_id),sqlc.arg(reserved_run_id),sqlc.arg(project_id),sqlc.arg(environment),sqlc.arg(pipeline_id),sqlc.arg(target_id),sqlc.arg(principal_id),sqlc.arg(source_digest),sqlc.arg(idempotency_key),sqlc.arg(request_digest),sqlc.arg(audit_intent)::jsonb,sqlc.arg(authority_envelope)::jsonb)
ON CONFLICT (project_id,environment,principal_id,idempotency_key) DO NOTHING
RETURNING *;

-- name: GetNextManualIntentForUpdate :one
SELECT * FROM refresh.manual_intent
WHERE project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment)
  AND status IN ('waiting','claimed')
ORDER BY created_at,intent_id LIMIT 1 FOR UPDATE;

-- name: GetManualIntentActiveRoots :one
SELECT EXISTS(SELECT 1 FROM refresh.run r
 WHERE r.project_id=sqlc.arg(scope_project_id) AND r.environment=sqlc.arg(scope_environment)
   AND r.parent_run_id IS NULL AND r.status IN ('queued','running','prepared')
   AND r.run_id<>sqlc.arg(reserved_run_id)) AS other_active_root,
 EXISTS(SELECT 1 FROM refresh.run r
 WHERE r.project_id=sqlc.arg(scope_project_id) AND r.environment=sqlc.arg(scope_environment)
   AND r.parent_run_id IS NULL AND r.status IN ('queued','running','prepared')
   AND r.run_id=sqlc.arg(reserved_run_id)) AS reserved_run_active;

-- name: ClaimManualIntent :one
UPDATE refresh.manual_intent
SET status='claimed',lease_owner=sqlc.arg(lease_owner),
    lease_expires_at=clock_timestamp()+(sqlc.arg(lease_micros)::bigint * interval '1 microsecond'),
    claimed_at=clock_timestamp(),fence_generation=fence_generation+1
WHERE intent_id=sqlc.arg(intent_id) AND status IN ('waiting','claimed')
  AND (status='waiting' OR lease_expires_at <= clock_timestamp())
RETURNING *;

-- name: AttachManualIntent :execrows
UPDATE refresh.manual_intent i
SET status='attached',attached_run_id=i.reserved_run_id,lease_owner='',lease_expires_at=NULL
WHERE i.intent_id=sqlc.arg(intent_id) AND i.reserved_run_id=sqlc.arg(run_id)
  AND i.status='claimed' AND i.lease_owner=sqlc.arg(lease_owner)
  AND i.fence_generation=sqlc.arg(fence_generation) AND i.lease_expires_at>clock_timestamp()
  AND EXISTS (SELECT 1 FROM refresh.run r
    WHERE r.run_id=i.reserved_run_id AND r.project_id=i.project_id AND r.environment=i.environment
      AND r.pipeline_id=i.pipeline_id AND r.target_type='refresh_pipeline' AND r.target_id=i.pipeline_id
      AND r.principal_id=i.principal_id AND r.trigger_type='manual' AND r.invocation_source='manual'
      AND r.parent_run_id IS NULL AND r.job_id IS NOT NULL
      AND r.status IN ('queued','running','prepared','succeeded','failed','cancelled','superseded','skipped'));

-- name: TransitionManualIntentClaim :execrows
UPDATE refresh.manual_intent
SET status=sqlc.arg(status),stale_reason=sqlc.arg(stale_reason),attached_run_id=NULL,lease_owner='',lease_expires_at=NULL
WHERE intent_id=sqlc.arg(intent_id) AND status='claimed' AND lease_owner=sqlc.arg(lease_owner)
  AND fence_generation=sqlc.arg(fence_generation) AND lease_expires_at>clock_timestamp();

-- name: GetManualIntent :one
SELECT * FROM refresh.manual_intent
WHERE project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment) AND intent_id=sqlc.arg(intent_id);

-- name: ListManualIntents :many
SELECT * FROM refresh.manual_intent
WHERE project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment)
  AND (sqlc.arg(target_id)::text='' OR target_id=sqlc.arg(target_id)::text)
  AND status IN ('waiting','claimed')
ORDER BY created_at,intent_id LIMIT sqlc.arg(page_limit);

-- name: ListRecentStaleManualIntents :many
SELECT * FROM refresh.manual_intent
WHERE project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment)
  AND (sqlc.arg(target_id)::text='' OR target_id=sqlc.arg(target_id)::text)
  AND status='stale'
ORDER BY created_at DESC,intent_id DESC LIMIT sqlc.arg(page_limit);

-- name: GetManualIntentForUpdate :one
SELECT * FROM refresh.manual_intent
WHERE project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment)
  AND intent_id=sqlc.arg(intent_id) FOR UPDATE;

-- name: ManualIntentReservedRunExists :one
SELECT EXISTS(SELECT 1 FROM refresh.run WHERE run_id=sqlc.arg(run_id));

-- name: CancelManualIntent :execrows
UPDATE refresh.manual_intent
SET status='cancelled',lease_owner='',lease_expires_at=NULL
WHERE project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment)
  AND intent_id=sqlc.arg(intent_id) AND status IN ('waiting','claimed');

-- name: IsScheduledOccurrenceClaimCurrent :one
SELECT EXISTS (SELECT 1 FROM refresh.schedule_occurrence
 WHERE occurrence_id=sqlc.arg(occurrence_id) AND project_id=sqlc.arg(project_id)
   AND environment=sqlc.arg(environment) AND generation_id=sqlc.arg(generation_id)
   AND pipeline_id=sqlc.arg(pipeline_id) AND status='claimed'
   AND lease_owner=sqlc.arg(lease_owner) AND fence_generation=sqlc.arg(fence_generation)
   AND lease_expires_at > clock_timestamp());

-- name: HasExternalActiveRefreshRoot :one
SELECT EXISTS (SELECT 1 FROM refresh.run
 WHERE project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment)
   AND parent_run_id IS NULL AND status IN ('queued','running','prepared')
   AND trigger_type <> 'schedule'
   AND invocation_source IN ('manual','backfill','external','dependency'));

-- name: SkipClaimedOccurrenceForExternalActiveRoot :execrows
UPDATE refresh.schedule_occurrence
SET status='skipped', outcome='{"reason":"admission_denied_external_active"}'::jsonb,
    finished_at=clock_timestamp(), lease_owner='', lease_expires_at=NULL
WHERE occurrence_id=sqlc.arg(occurrence_id) AND project_id=sqlc.arg(project_id)
  AND environment=sqlc.arg(environment) AND generation_id=sqlc.arg(generation_id)
  AND pipeline_id=sqlc.arg(pipeline_id) AND status='claimed'
  AND lease_owner=sqlc.arg(lease_owner) AND fence_generation=sqlc.arg(fence_generation)
  AND lease_expires_at > clock_timestamp();
