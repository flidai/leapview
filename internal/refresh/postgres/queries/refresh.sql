-- Static PostgreSQL query leaves for refresh authority.

-- name: GetScheduleRevisionForUpdate :one
SELECT schedule_revision_id, project_id, environment, pipeline_id, schedule_id,
 semantic_model_id, generation_id, artifact_digest, cron, timezone,
 starting_deadline, concurrency_policy, schedule_digest, next_run_at,
 valid_from, COALESCE(closed_at,'epoch'::timestamptz), updated_at, enabled
FROM refresh.schedule_revision
WHERE project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment) AND pipeline_id=sqlc.arg(pipeline_id) AND schedule_id=sqlc.arg(schedule_id)
  AND generation_id=sqlc.arg(generation_id) AND closed_at IS NULL AND enabled FOR UPDATE;

-- name: CloseScheduleRevision :execrows
UPDATE refresh.schedule_revision SET closed_at=clock_timestamp(), enabled=false, updated_at=clock_timestamp() WHERE schedule_revision_id=sqlc.arg(scheduleRevisionID);

-- name: InsertScheduleRevision :execrows
INSERT INTO refresh.schedule_revision
 (schedule_revision_id,project_id,environment,pipeline_id,schedule_id,semantic_model_id,generation_id,artifact_digest,cron,timezone,starting_deadline,concurrency_policy,schedule_digest,next_run_at)
 VALUES (sqlc.arg(schedule_revision_id),sqlc.arg(project_id),sqlc.arg(environment),sqlc.arg(pipeline_id),sqlc.arg(schedule_id),sqlc.arg(semantic_model_id),sqlc.arg(generation_id),sqlc.arg(artifact_digest),sqlc.arg(cron),sqlc.arg(timezone),sqlc.arg(starting_deadline),sqlc.arg(concurrency_policy),sqlc.arg(schedule_digest),sqlc.arg(next_run_at))
 ON CONFLICT (schedule_revision_id) DO NOTHING;

-- name: GetScheduleRevision :one
SELECT schedule_revision_id, project_id, environment, pipeline_id, schedule_id, semantic_model_id, generation_id, artifact_digest, cron, timezone, starting_deadline, concurrency_policy, schedule_digest, next_run_at, valid_from, COALESCE(closed_at,'epoch'::timestamptz), updated_at, enabled FROM refresh.schedule_revision WHERE schedule_revision_id=sqlc.arg(scheduleRevisionID);

-- name: GetNextScheduleRun :one
SELECT next_run_at FROM refresh.schedule_revision WHERE project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment) AND pipeline_id=sqlc.arg(pipeline_id) AND (sqlc.arg(generation_id)::text='' OR generation_id=sqlc.arg(generation_id)::text) AND enabled AND closed_at IS NULL ORDER BY next_run_at,schedule_revision_id LIMIT 1;

-- name: QueueClaimedOccurrence :execrows
UPDATE refresh.schedule_occurrence SET status='queued',run_id=sqlc.arg(run_id),lease_owner='',lease_expires_at=NULL WHERE occurrence_id=sqlc.arg(occurrence_id) AND project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment) AND generation_id=sqlc.arg(generation_id) AND pipeline_id=sqlc.arg(pipeline_id) AND nominal_time=sqlc.arg(nominal_time) AND status='claimed' AND lease_owner=sqlc.arg(lease_owner) AND fence_generation=sqlc.arg(fence_generation) AND lease_expires_at > clock_timestamp();

-- name: GetRunOccurrence :one
SELECT occurrence_id FROM refresh.run WHERE run_id=sqlc.arg(runID);

-- name: TransitionOccurrence :execrows
UPDATE refresh.schedule_occurrence SET status=sqlc.arg(status),finished_at=CASE WHEN sqlc.arg(status) IN ('succeeded','failed','cancelled','superseded') THEN clock_timestamp() ELSE finished_at END,outcome=CASE WHEN sqlc.arg(outcome)::jsonb='{}'::jsonb THEN outcome ELSE sqlc.arg(outcome)::jsonb END WHERE occurrence_id=sqlc.arg(occurrence_id) AND run_id=sqlc.arg(run_id) AND status IN ('queued','running');

-- name: GetOccurrenceStatus :one
SELECT o.status FROM refresh.schedule_occurrence o JOIN refresh.run r ON r.occurrence_id=o.occurrence_id WHERE r.run_id=sqlc.arg(runID);

-- name: CloseOmittedSchedules :one
SELECT refresh.close_omitted_schedules(sqlc.arg(project_id), sqlc.arg(environment), sqlc.arg(generation_id), sqlc.arg(pipelines)::text[], sqlc.arg(schedule_ids)::text[]) AS closed_count;

-- name: DatabaseClock :one
SELECT clock_timestamp()::timestamptz AS db_now;

-- name: RecoverStaleOccurrences :execrows
WITH stale AS (SELECT o.occurrence_id FROM refresh.schedule_occurrence o WHERE o.project_id=sqlc.arg(project_id) AND o.environment=sqlc.arg(environment) AND (sqlc.arg(generation_id)::text='' OR o.generation_id=sqlc.arg(generation_id)::text) AND o.status='claimed' AND o.lease_expires_at <= clock_timestamp() ORDER BY o.lease_expires_at,o.occurrence_id LIMIT sqlc.arg(page_limit) FOR UPDATE SKIP LOCKED) UPDATE refresh.schedule_occurrence o SET status='pending',lease_owner='',lease_expires_at=NULL WHERE o.occurrence_id IN (SELECT occurrence_id FROM stale);

-- name: ListDueSchedules :many
SELECT schedule_revision_id,project_id,environment,pipeline_id,schedule_id,semantic_model_id,generation_id,artifact_digest,cron,timezone,starting_deadline,concurrency_policy,schedule_digest,next_run_at,valid_from,COALESCE(closed_at,'epoch'::timestamptz),updated_at,enabled FROM refresh.schedule_revision WHERE project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment) AND (sqlc.arg(generation_id)::text='' OR generation_id=sqlc.arg(generation_id)::text) AND enabled AND closed_at IS NULL AND next_run_at <= sqlc.arg(next_run_at) ORDER BY next_run_at,pipeline_id,schedule_id LIMIT sqlc.arg(page_limit) FOR UPDATE SKIP LOCKED;

-- name: AdvanceScheduleCursor :execrows
UPDATE refresh.schedule_revision SET next_run_at=sqlc.arg(next_run_at) WHERE schedule_revision_id=sqlc.arg(schedule_revision_id) AND generation_id=sqlc.arg(generation_id) AND closed_at IS NULL AND enabled;

-- name: InsertPendingOccurrence :execrows
INSERT INTO refresh.schedule_occurrence(occurrence_id,project_id,environment,pipeline_id,nominal_time,schedule_revision_id,matching_schedule_ids,semantic_model_id,generation_id,artifact_digest) VALUES(sqlc.arg(occurrence_id),sqlc.arg(project_id),sqlc.arg(environment),sqlc.arg(pipeline_id),sqlc.arg(nominal_time),sqlc.arg(schedule_revision_id),sqlc.arg(matching_schedule_ids)::jsonb,sqlc.arg(semantic_model_id),sqlc.arg(generation_id),sqlc.arg(artifact_digest)) ON CONFLICT DO NOTHING;

-- name: SkipOccurrence :execrows
UPDATE refresh.schedule_occurrence SET status='skipped',outcome='{"reason":"starting_deadline_exceeded"}'::jsonb,finished_at=clock_timestamp() WHERE occurrence_id=sqlc.arg(occurrenceID) AND status='pending';

-- name: InsertOccurrence :execrows
INSERT INTO refresh.schedule_occurrence(occurrence_id,project_id,environment,pipeline_id,nominal_time,schedule_revision_id,matching_schedule_ids,semantic_model_id,generation_id,artifact_digest) VALUES(sqlc.arg(occurrence_id),sqlc.arg(project_id),sqlc.arg(environment),sqlc.arg(pipeline_id),sqlc.arg(nominal_time),sqlc.arg(schedule_revision_id),sqlc.arg(matching_schedule_ids)::jsonb,sqlc.arg(semantic_model_id),sqlc.arg(generation_id),sqlc.arg(artifact_digest)) ON CONFLICT(project_id,environment,pipeline_id,nominal_time) DO NOTHING;

-- name: ClaimOccurrence :one
UPDATE refresh.schedule_occurrence SET status='claimed',lease_owner=sqlc.arg(lease_owner),lease_expires_at=clock_timestamp()+sqlc.arg(lease)::interval,claimed_at=clock_timestamp(),fence_generation=fence_generation+1 WHERE occurrence_id=sqlc.arg(occurrence_id) AND generation_id=sqlc.arg(generation_id) AND status='pending' RETURNING true;

-- name: GetOccurrenceByNominal :one
SELECT occurrence_id FROM refresh.schedule_occurrence WHERE project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment) AND pipeline_id=sqlc.arg(pipeline_id) AND nominal_time=sqlc.arg(nominal_time);

-- name: GetOccurrence :one
SELECT occurrence_id,project_id,environment,pipeline_id,nominal_time,schedule_revision_id,matching_schedule_ids,semantic_model_id,generation_id,artifact_digest,status,COALESCE(run_id,''),fence_generation,lease_owner,COALESCE(lease_expires_at,'epoch'::timestamptz),COALESCE(claimed_at,'epoch'::timestamptz),COALESCE(finished_at,'epoch'::timestamptz),created_at,outcome FROM refresh.schedule_occurrence WHERE occurrence_id=sqlc.arg(occurrenceID);

-- name: ReleaseOccurrenceClaim :execrows
UPDATE refresh.schedule_occurrence SET status='pending', lease_owner='', lease_expires_at=NULL WHERE occurrence_id=sqlc.arg(occurrence_id) AND status='claimed' AND lease_owner=sqlc.arg(lease_owner) AND fence_generation=sqlc.arg(fence_generation) AND lease_expires_at > clock_timestamp();

-- name: RequeueScheduleRevision :execrows
UPDATE refresh.schedule_revision s SET next_run_at=LEAST(s.next_run_at,sqlc.arg(next_run_at)),updated_at=clock_timestamp() WHERE s.project_id=sqlc.arg(project_id) AND s.environment=sqlc.arg(environment) AND s.pipeline_id=sqlc.arg(pipeline_id) AND s.schedule_revision_id = (SELECT o.schedule_revision_id FROM refresh.schedule_occurrence o WHERE o.occurrence_id=sqlc.arg(occurrence_id)) AND s.closed_at IS NULL AND s.enabled;

-- name: AdvisoryLock :execrows
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(hashtextextended),0));

-- name: ListActiveRuns :many
SELECT run_id,trigger_type,COALESCE(job_id,'') FROM refresh.run WHERE project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment) AND target_type=sqlc.arg(target_type) AND target_id=sqlc.arg(target_id) AND status IN ('queued','running','prepared') AND run_id<>sqlc.arg(run_id) ORDER BY created_at,run_id FOR UPDATE;

-- name: FailSupersededAttempts :execrows
WITH RECURSIVE tree(run_id) AS (
			SELECT r.run_id FROM refresh.run r WHERE r.project_id=sqlc.arg(project_id) AND r.environment=sqlc.arg(environment) AND r.target_type=sqlc.arg(target_type) AND r.target_id=sqlc.arg(target_id) AND r.trigger_type='schedule' AND r.status IN ('queued','running','prepared') AND r.run_id<>sqlc.arg(run_id)
			UNION ALL
			SELECT child.run_id FROM refresh.run child JOIN tree parent ON child.parent_run_id=parent.run_id
		) UPDATE refresh.attempt a SET status='failed',finished_at=clock_timestamp(),error='replaced by newer scheduled invocation',evidence='{"code":"REFRESH_SUPERSEDED","reason":"replace"}'::jsonb WHERE a.status='running' AND a.run_id IN (SELECT tree.run_id FROM tree);

-- name: SupersedeRuns :execrows
WITH RECURSIVE tree(run_id) AS (
			SELECT r.run_id FROM refresh.run r WHERE r.project_id=sqlc.arg(project_id) AND r.environment=sqlc.arg(environment) AND r.target_type=sqlc.arg(target_type) AND r.target_id=sqlc.arg(target_id) AND r.trigger_type='schedule' AND r.status IN ('queued','running','prepared') AND r.run_id<>sqlc.arg(run_id)
			UNION ALL
			SELECT child.run_id FROM refresh.run child JOIN tree parent ON child.parent_run_id=parent.run_id
		) UPDATE refresh.run SET status='superseded',error='replaced by newer scheduled invocation',finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL WHERE run_id IN (SELECT tree.run_id FROM tree) AND status IN ('queued','running','prepared');

-- name: SupersedeOccurrences :execrows
WITH RECURSIVE tree(run_id) AS (
			SELECT r.run_id FROM refresh.run r WHERE r.project_id=sqlc.arg(project_id) AND r.environment=sqlc.arg(environment) AND r.target_type=sqlc.arg(target_type) AND r.target_id=sqlc.arg(target_id) AND r.trigger_type='schedule' AND r.status='superseded' AND r.run_id<>sqlc.arg(run_id)
			UNION ALL
			SELECT child.run_id FROM refresh.run child JOIN tree parent ON child.parent_run_id=parent.run_id
		) UPDATE refresh.schedule_occurrence SET status='superseded',finished_at=clock_timestamp(),outcome='{"code":"REFRESH_SUPERSEDED"}'::jsonb WHERE run_id IN (SELECT tree.run_id FROM tree) AND status IN ('queued','running');

-- name: InsertRun :execrows
INSERT INTO refresh.run(run_id,operation_id,project_id,environment,generation_id,parent_run_id,pipeline_id,semantic_model_id,target_type,target_id,target_revision,trigger_type,invocation_source,trigger_id,concurrency_policy,schedule_revision_id,occurrence_id,nominal_time,plan_digest,artifact_digest,matching_schedule_ids,materialization_scope,principal_id,job_id) VALUES(sqlc.arg(run_id),NULLIF(sqlc.arg(operation_id)::text,''),sqlc.arg(project_id),sqlc.arg(environment),sqlc.arg(generation_id),NULLIF(sqlc.arg(parent_run_id)::text,''),sqlc.arg(pipeline_id),sqlc.arg(semantic_model_id),sqlc.arg(target_type),sqlc.arg(target_id),sqlc.arg(target_revision),sqlc.arg(trigger_type),sqlc.arg(invocation_source),sqlc.arg(trigger_id),sqlc.arg(concurrency_policy),sqlc.arg(schedule_revision_id),sqlc.arg(occurrence_id),NULLIF(sqlc.arg(nominal_time)::timestamptz,'epoch'::timestamptz),sqlc.arg(plan_digest),sqlc.arg(artifact_digest),sqlc.arg(matching_schedule_ids)::jsonb,sqlc.arg(materialization_scope)::jsonb,sqlc.arg(principal_id),NULLIF(sqlc.arg(job_id)::text,'')) ON CONFLICT(run_id) DO NOTHING;

-- name: GetRunJobForUpdate :one
SELECT COALESCE(job_id,'') FROM refresh.run WHERE run_id=sqlc.arg(runID) FOR UPDATE;

-- name: AttachRunJob :execrows
UPDATE refresh.run SET job_id=sqlc.arg(job_id) WHERE run_id=sqlc.arg(run_id) AND job_id IS NULL;

-- name: GetRun :one
SELECT run_id,COALESCE(operation_id,''),project_id,environment,generation_id,COALESCE(parent_run_id,''),pipeline_id,semantic_model_id,target_type,target_id,target_revision,trigger_type,invocation_source,trigger_id,concurrency_policy,schedule_revision_id,occurrence_id,COALESCE(nominal_time,'epoch'::timestamptz),plan_digest,artifact_digest,matching_schedule_ids,materialization_scope,principal_id,COALESCE(job_id,''),status,attempt_count,fence_generation,lease_owner,COALESCE(lease_expires_at,'epoch'::timestamptz),created_at,updated_at,COALESCE(started_at,'epoch'::timestamptz),COALESCE(finished_at,'epoch'::timestamptz),error FROM refresh.run WHERE run_id=sqlc.arg(runID);

-- name: ListRunsPage :many
SELECT r.run_id FROM refresh.run r WHERE r.project_id=sqlc.arg(project_id) AND r.environment=sqlc.arg(environment) AND (sqlc.arg(generation_id)::text='' OR r.generation_id=sqlc.arg(generation_id)::text) AND (sqlc.arg(after)::text='' OR (r.created_at,r.run_id) < (SELECT i.created_at,i.run_id FROM refresh.run i WHERE i.run_id=sqlc.arg(after)::text AND i.project_id=sqlc.arg(project_id) AND i.environment=sqlc.arg(environment) AND (sqlc.arg(generation_id)::text='' OR i.generation_id=sqlc.arg(generation_id)::text))) ORDER BY created_at DESC,run_id DESC LIMIT sqlc.arg(page_limit);

-- name: ListRunsFilteredPage :many
SELECT r.run_id FROM refresh.run r WHERE r.project_id=sqlc.arg(project_id) AND r.environment=sqlc.arg(environment) AND (sqlc.arg(generation_id)::text='' OR r.generation_id=sqlc.arg(generation_id)::text) AND (sqlc.arg(target_type)::text='' OR target_type=sqlc.arg(target_type)::text) AND (sqlc.arg(target_id)::text='' OR target_id=sqlc.arg(target_id)::text) AND (sqlc.arg(semantic_model_id)::text='' OR semantic_model_id=sqlc.arg(semantic_model_id)::text) AND (sqlc.arg(successful)::boolean=false OR status='succeeded') AND (sqlc.arg(after)::text='' OR (r.created_at,r.run_id) < (SELECT i.created_at,i.run_id FROM refresh.run i WHERE i.run_id=sqlc.arg(after)::text AND i.project_id=sqlc.arg(project_id) AND i.environment=sqlc.arg(environment) AND (sqlc.arg(generation_id)::text='' OR i.generation_id=sqlc.arg(generation_id)::text) AND (sqlc.arg(target_type)::text='' OR target_type=sqlc.arg(target_type)::text) AND (sqlc.arg(target_id)::text='' OR target_id=sqlc.arg(target_id)::text) AND (sqlc.arg(semantic_model_id)::text='' OR semantic_model_id=sqlc.arg(semantic_model_id)::text) AND (sqlc.arg(successful)::boolean=false OR status='succeeded'))) ORDER BY created_at DESC,run_id DESC LIMIT sqlc.arg(page_limit);

-- name: ListChildRuns :many
SELECT run_id FROM refresh.run WHERE project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment) AND parent_run_id=sqlc.arg(parent_run_id) ORDER BY created_at,run_id LIMIT sqlc.arg(page_limit);

-- name: GetLiveRunForUpdate :one
SELECT status FROM refresh.run WHERE run_id=sqlc.arg(run_id) AND lease_owner=sqlc.arg(lease_owner) AND fence_generation=sqlc.arg(fence_generation) AND status IN ('running','prepared') AND lease_expires_at > clock_timestamp() FOR UPDATE;

-- name: ListRunTreeJobs :many
WITH RECURSIVE tree(run_id) AS (
		SELECT r.run_id FROM refresh.run r WHERE r.run_id=sqlc.arg(runID)
		UNION ALL
		SELECT child.run_id FROM refresh.run child JOIN tree parent ON child.parent_run_id=parent.run_id
	) SELECT DISTINCT job_id FROM refresh.run WHERE run_id IN (SELECT tree.run_id FROM tree) AND job_id IS NOT NULL;

-- name: FailSupersededTreeAttempts :execrows
WITH RECURSIVE tree(run_id) AS (SELECT r.run_id FROM refresh.run r WHERE r.run_id=sqlc.arg(run_id) UNION ALL SELECT child.run_id FROM refresh.run child JOIN tree parent ON child.parent_run_id=parent.run_id) UPDATE refresh.attempt a SET status='failed',finished_at=clock_timestamp(),error=sqlc.arg(message)::text,evidence=jsonb_build_object('code','REFRESH_SUPERSEDED','message',sqlc.arg(message)::text) FROM tree WHERE a.status='running' AND a.run_id=tree.run_id;

-- name: SupersedeRunTree :execrows
WITH RECURSIVE tree(run_id) AS (SELECT r.run_id FROM refresh.run r WHERE r.run_id=sqlc.arg(run_id) UNION ALL SELECT child.run_id FROM refresh.run child JOIN tree parent ON child.parent_run_id=parent.run_id) UPDATE refresh.run r SET status='superseded',error=sqlc.arg(message)::text,finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL FROM tree WHERE r.run_id=tree.run_id AND r.status IN ('queued','running','prepared');

-- name: FailChildAttempts :execrows
WITH RECURSIVE tree(run_id) AS (
			SELECT r.run_id FROM refresh.run r WHERE r.run_id=sqlc.arg(run_id)
			UNION ALL
			SELECT child.run_id FROM refresh.run child JOIN tree parent ON child.parent_run_id=parent.run_id
		)
		UPDATE refresh.attempt a SET status='failed',evidence=sqlc.arg(evidence)::jsonb,error=sqlc.arg(error),finished_at=clock_timestamp()
		FROM tree, refresh.run child
		WHERE child.run_id=a.run_id AND child.run_id=tree.run_id AND child.run_id<>sqlc.arg(run_id)
		  AND child.status IN ('running','prepared') AND a.status='running' AND child.lease_expires_at > clock_timestamp();

-- name: FailChildRuns :one
SELECT refresh.fail_child_runs(sqlc.arg(run_id), sqlc.arg(error)) AS affected;

-- name: CompleteChildAttempts :execrows
WITH RECURSIVE tree(run_id) AS (
		SELECT r.run_id FROM refresh.run r WHERE r.run_id=sqlc.arg(run_id)
		UNION ALL
		SELECT child.run_id FROM refresh.run child JOIN tree parent ON child.parent_run_id=parent.run_id
	)
	UPDATE refresh.attempt a SET status='succeeded',evidence=sqlc.arg(evidence)::jsonb,finished_at=clock_timestamp()
	FROM tree, refresh.run child
	WHERE child.run_id=a.run_id AND child.run_id=tree.run_id AND child.run_id<>sqlc.arg(run_id) AND child.status IN ('running','prepared') AND a.status='running';

-- name: CompleteChildRuns :one
SELECT refresh.complete_child_runs(sqlc.arg(run_id)) AS affected;

-- name: GetRunPoisonState :one
SELECT status,COALESCE(job_id,'') FROM refresh.run WHERE run_id=sqlc.arg(runID) FOR UPDATE;

-- name: GetRunTreePoisonCounts :one
WITH RECURSIVE tree(run_id) AS (
		SELECT r.run_id FROM refresh.run r WHERE r.run_id=sqlc.arg(run_id)
		UNION ALL
		SELECT child.run_id FROM refresh.run child JOIN tree parent ON child.parent_run_id=parent.run_id
	)
	SELECT count(*)::bigint AS total,
	       count(*) FILTER (WHERE r.status='failed' AND r.finished_at IS NOT NULL AND r.error=sqlc.arg(error))::bigint AS poisoned,
	       count(*) FILTER (WHERE r.status='queued')::bigint AS queued
	FROM refresh.run r WHERE r.run_id IN (SELECT tree.run_id FROM tree);

-- name: FailQueuedTree :execrows
WITH RECURSIVE tree(run_id) AS (
		SELECT r.run_id FROM refresh.run r WHERE r.run_id=sqlc.arg(run_id)
		UNION ALL
		SELECT child.run_id FROM refresh.run child JOIN tree parent ON child.parent_run_id=parent.run_id
	)
	UPDATE refresh.run SET status='failed',error=sqlc.arg(error),finished_at=clock_timestamp()
	WHERE run_id IN (SELECT tree.run_id FROM tree) AND status='queued';

-- name: FailTerminalTreeAttempts :execrows
WITH RECURSIVE tree(run_id) AS (
		SELECT r.run_id FROM refresh.run r WHERE r.run_id=sqlc.arg(run_id)
		UNION ALL
		SELECT child.run_id FROM refresh.run child JOIN tree parent ON child.parent_run_id=parent.run_id
	)
		UPDATE refresh.attempt a SET status=CASE WHEN a.lease_expires_at <= clock_timestamp() THEN 'expired' ELSE 'failed' END,error=sqlc.arg(error),finished_at=clock_timestamp(),evidence=sqlc.arg(evidence)::jsonb
	WHERE a.run_id IN (SELECT tree.run_id FROM tree) AND a.status='running';

-- name: FailTerminalTreeRuns :execrows
WITH RECURSIVE tree(run_id) AS (
		SELECT r.run_id FROM refresh.run r WHERE r.run_id=sqlc.arg(run_id)
		UNION ALL
		SELECT child.run_id FROM refresh.run child JOIN tree parent ON child.parent_run_id=parent.run_id
	)
	UPDATE refresh.run SET status='failed',error=sqlc.arg(error),finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL
	WHERE run_id IN (SELECT tree.run_id FROM tree) AND status IN ('queued','running','prepared');

-- name: GetActiveInvocationTrigger :one
SELECT trigger_type FROM refresh.run WHERE project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment) AND target_type='refresh_pipeline' AND target_id=sqlc.arg(target_id) AND status IN ('queued','running','prepared') ORDER BY created_at,run_id LIMIT 1;

-- name: PrepareRun :execrows
UPDATE refresh.run SET status='prepared' WHERE run_id=sqlc.arg(run_id) AND status='running' AND lease_owner=sqlc.arg(lease_owner) AND fence_generation=sqlc.arg(fence_generation) AND lease_expires_at > clock_timestamp();

-- name: RunLeaseExists :one
SELECT EXISTS (SELECT 1 FROM refresh.run WHERE run_id=sqlc.arg(run_id) AND lease_owner=sqlc.arg(lease_owner) AND fence_generation=sqlc.arg(fence_generation) AND status IN ('running','prepared') AND lease_expires_at > clock_timestamp());

-- name: CancelQueuedRun :execrows
UPDATE refresh.run SET status='cancelled',finished_at=clock_timestamp() WHERE run_id=sqlc.arg(run_id) AND project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment) AND (sqlc.arg(generation_id)::text='' OR generation_id=sqlc.arg(generation_id)::text) AND status='queued';

-- name: FailQueuedRun :execrows
UPDATE refresh.run SET status='failed',error=sqlc.arg(error),finished_at=clock_timestamp() WHERE run_id=sqlc.arg(run_id) AND project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment) AND status='queued';

-- name: ExpireAttempt :execrows
UPDATE refresh.attempt SET status='expired',finished_at=clock_timestamp(),error='lease expired' WHERE run_id=sqlc.arg(runID) AND status='running' AND lease_expires_at <= clock_timestamp();

-- name: ClaimRunAttempt :one
UPDATE refresh.run SET status='running',attempt_count=attempt_count+1,fence_generation=sqlc.arg(fence_generation),lease_owner=sqlc.arg(lease_owner),lease_expires_at=clock_timestamp()+sqlc.arg(lease)::interval,started_at=COALESCE(started_at,clock_timestamp()) WHERE run_id=sqlc.arg(run_id) AND status IN ('queued','running','prepared') AND (status='queued' OR ((status='running' OR status='prepared') AND lease_expires_at <= clock_timestamp() AND sqlc.arg(fence_generation) > fence_generation)) RETURNING attempt_count;

-- name: InsertAttempt :execrows
INSERT INTO refresh.attempt(run_id,attempt_number,fence_generation,owner_id,lease_expires_at) VALUES(sqlc.arg(run_id),sqlc.arg(attempt_number),sqlc.arg(fence_generation),sqlc.arg(owner_id),clock_timestamp()+sqlc.arg(lease)::interval);

-- name: HeartbeatRunLease :execrows
UPDATE refresh.run SET lease_expires_at=clock_timestamp()+sqlc.arg(lease)::interval WHERE run_id=sqlc.arg(run_id) AND status IN ('running','prepared') AND lease_owner=sqlc.arg(lease_owner) AND fence_generation=sqlc.arg(fence_generation) AND lease_expires_at > clock_timestamp();

-- name: HeartbeatAttemptLease :execrows
UPDATE refresh.attempt SET lease_expires_at=clock_timestamp()+sqlc.arg(lease)::interval WHERE run_id=sqlc.arg(run_id) AND status='running' AND owner_id=sqlc.arg(owner_id) AND fence_generation=sqlc.arg(fence_generation);

-- name: FinishAttempt :execrows
UPDATE refresh.attempt SET status=sqlc.arg(status),evidence=sqlc.arg(evidence)::jsonb,error=sqlc.arg(error),finished_at=clock_timestamp() WHERE run_id=sqlc.arg(run_id) AND owner_id=sqlc.arg(owner_id) AND fence_generation=sqlc.arg(fence_generation) AND status='running' AND lease_expires_at > clock_timestamp();

-- name: FinishRun :execrows
UPDATE refresh.run SET status=sqlc.arg(status),error=sqlc.arg(error),finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL WHERE run_id=sqlc.arg(run_id) AND lease_owner=sqlc.arg(lease_owner) AND fence_generation=sqlc.arg(fence_generation) AND status IN ('running','prepared');

-- name: GetPublicationForUpdate :one
SELECT publication_id FROM refresh.publication_link WHERE publication_id=sqlc.arg(publicationID) FOR UPDATE;

-- name: GetRunPublicationFence :one
SELECT generation_id,plan_digest,artifact_digest,lease_owner,fence_generation,status,COALESCE(lease_expires_at,'epoch'::timestamptz) > clock_timestamp() AS live FROM refresh.run WHERE run_id=sqlc.arg(runID);

-- name: GetRunPublicationLink :one
SELECT publication_id FROM refresh.publication_link WHERE run_id=sqlc.arg(runID) AND state IN ('pending','committed');

-- name: InsertPublicationLink :execrows
INSERT INTO refresh.publication_link(publication_id,run_id,base_generation_id,result_generation_id,plan_digest,artifact_digest,physical_pool_id,catalog_id,expected_target_revision,result_target_revision,snapshot_id,fence_generation,owner_id,evidence) VALUES(sqlc.arg(publication_id),sqlc.arg(run_id),sqlc.arg(base_generation_id),sqlc.arg(result_generation_id),sqlc.arg(plan_digest),sqlc.arg(artifact_digest),sqlc.arg(physical_pool_id),sqlc.arg(catalog_id),sqlc.arg(expected_target_revision),sqlc.arg(result_target_revision),CASE WHEN sqlc.arg(snapshot_id)::bigint=0 THEN NULL::bigint ELSE sqlc.arg(snapshot_id)::bigint END,sqlc.arg(fence_generation),sqlc.arg(owner_id),sqlc.arg(evidence)::jsonb) ON CONFLICT(publication_id) DO NOTHING;

-- name: GetPublication :one
SELECT publication_id,run_id,base_generation_id,result_generation_id,plan_digest,artifact_digest,physical_pool_id,catalog_id,expected_target_revision,result_target_revision,COALESCE(snapshot_id,0),fence_generation,owner_id,state,evidence,created_at,COALESCE(committed_at,'epoch'::timestamptz) FROM refresh.publication_link WHERE publication_id=sqlc.arg(publicationID);

-- name: CommitPublication :execrows
UPDATE refresh.publication_link p SET state='committed',snapshot_id=sqlc.arg(snapshot_id),evidence=sqlc.arg(evidence)::jsonb,committed_at=clock_timestamp() WHERE p.publication_id=sqlc.arg(publication_id) AND p.run_id=sqlc.arg(run_id) AND p.owner_id=sqlc.arg(owner_id) AND p.fence_generation=sqlc.arg(fence_generation) AND p.physical_pool_id=sqlc.arg(physical_pool_id) AND p.catalog_id=sqlc.arg(catalog_id) AND p.state='pending' AND EXISTS (SELECT 1 FROM refresh.run r WHERE r.run_id=sqlc.arg(run_id) AND r.fence_generation=sqlc.arg(fence_generation) AND r.lease_owner=sqlc.arg(owner_id) AND r.status IN ('running','prepared'));

-- name: PublicationLinkMarker :one
SELECT count(*) AS link_count FROM refresh.publication_link WHERE run_id=sqlc.arg(run_id) OR result_generation_id=sqlc.arg(result_generation_id);

-- name: RunTreeSucceeded :one
WITH RECURSIVE tree(run_id) AS (SELECT r.run_id FROM refresh.run r WHERE r.run_id=sqlc.arg(runID) UNION ALL SELECT child.run_id FROM refresh.run child JOIN tree parent ON child.parent_run_id=parent.run_id) SELECT EXISTS (SELECT 1 FROM refresh.run root WHERE root.run_id=sqlc.arg(runID) AND root.status='succeeded') AND NOT EXISTS (SELECT 1 FROM refresh.run r WHERE r.run_id IN (SELECT tree.run_id FROM tree) AND r.status <> 'succeeded');

-- name: GetRunStatus :one
SELECT status FROM refresh.run WHERE run_id=sqlc.arg(runID);

-- name: UpsertRecovery :execrows
INSERT INTO refresh.recovery_state(run_id,state,reconciliation_fence,owner_id,lease_expires_at,exact_external_identity,last_error,evidence,next_reconcile_at) VALUES(sqlc.arg(run_id),sqlc.arg(state),sqlc.arg(reconciliation_fence),sqlc.arg(owner_id),clock_timestamp()+sqlc.arg(lease)::interval,sqlc.arg(exact_external_identity),sqlc.arg(last_error),sqlc.arg(evidence)::jsonb,NULLIF(sqlc.arg(next_reconcile_at)::timestamptz,'epoch'::timestamptz)) ON CONFLICT(run_id) DO UPDATE SET state=EXCLUDED.state,reconciliation_fence=EXCLUDED.reconciliation_fence,owner_id=EXCLUDED.owner_id,lease_expires_at=EXCLUDED.lease_expires_at,exact_external_identity=EXCLUDED.exact_external_identity,last_error=EXCLUDED.last_error,evidence=EXCLUDED.evidence,next_reconcile_at=EXCLUDED.next_reconcile_at WHERE refresh.recovery_state.reconciliation_fence < EXCLUDED.reconciliation_fence;

-- name: GetRecovery :one
SELECT run_id,state,reconciliation_fence,owner_id,COALESCE(lease_expires_at,'epoch'::timestamptz),exact_external_identity,last_error,evidence,COALESCE(next_reconcile_at,'epoch'::timestamptz),updated_at FROM refresh.recovery_state WHERE run_id=sqlc.arg(runID);

-- name: PublicationExistsForDataVersion :one
SELECT EXISTS (SELECT 1 FROM refresh.publication_link p JOIN refresh.run r ON r.run_id=p.run_id WHERE p.run_id=sqlc.arg(run_id) AND p.state='committed' AND p.result_generation_id=sqlc.arg(result_generation_id) AND p.physical_pool_id=sqlc.arg(physical_pool_id) AND p.catalog_id=sqlc.arg(catalog_id) AND p.snapshot_id=sqlc.arg(snapshot_id)::bigint AND ( sqlc.arg(source)::text='publish' OR (p.fence_generation=sqlc.arg(fence_generation) AND p.owner_id=sqlc.arg(owner_id)) ) AND r.project_id=sqlc.arg(project_id) AND r.environment=sqlc.arg(environment) AND r.semantic_model_id=sqlc.arg(semantic_model_id));

-- name: RefreshPublicationValid :one
SELECT EXISTS (SELECT 1 FROM refresh.run r JOIN refresh.publication_link p ON p.run_id=r.run_id WHERE r.run_id=sqlc.arg(run_id) AND r.project_id=sqlc.arg(project_id) AND r.environment=sqlc.arg(environment) AND p.base_generation_id=r.generation_id AND p.result_generation_id=sqlc.arg(result_generation_id) AND p.state='committed' AND p.fence_generation=sqlc.arg(fence_generation) AND p.owner_id=sqlc.arg(owner_id) AND r.status IN ('running','prepared','succeeded'));

-- name: GetDataVersionForUpdate :one
SELECT project_id,environment,semantic_model_id,generation_id,snapshot_id,refreshed_at,source,pipeline_id,run_id,target_revision,lease_owner,lease_revision,physical_pool_id,catalog_id FROM refresh.data_version WHERE project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment) AND semantic_model_id=sqlc.arg(semantic_model_id) AND generation_id=sqlc.arg(generation_id) FOR UPDATE;

-- name: UpsertDataVersion :execrows
INSERT INTO refresh.data_version(project_id,environment,semantic_model_id,generation_id,snapshot_id,source,physical_pool_id,catalog_id,pipeline_id,run_id,target_revision,lease_owner,lease_revision) VALUES(sqlc.arg(project_id),sqlc.arg(environment),sqlc.arg(semantic_model_id),sqlc.arg(generation_id),sqlc.arg(snapshot_id),sqlc.arg(source),sqlc.arg(physical_pool_id),sqlc.arg(catalog_id),sqlc.arg(pipeline_id),sqlc.arg(run_id),sqlc.arg(target_revision),sqlc.arg(lease_owner),sqlc.arg(lease_revision)) ON CONFLICT(project_id,environment,semantic_model_id,generation_id) DO UPDATE SET snapshot_id=EXCLUDED.snapshot_id,refreshed_at=clock_timestamp(),source=EXCLUDED.source,physical_pool_id=EXCLUDED.physical_pool_id,catalog_id=EXCLUDED.catalog_id,pipeline_id=EXCLUDED.pipeline_id,run_id=EXCLUDED.run_id,target_revision=EXCLUDED.target_revision,lease_owner=EXCLUDED.lease_owner,lease_revision=EXCLUDED.lease_revision WHERE refresh.data_version.lease_revision < EXCLUDED.lease_revision;

-- name: GetDataVersion :one
SELECT project_id,environment,semantic_model_id,generation_id,snapshot_id,refreshed_at,source,pipeline_id,run_id,target_revision,lease_owner,lease_revision,physical_pool_id,catalog_id FROM refresh.data_version WHERE project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment) AND semantic_model_id=sqlc.arg(semantic_model_id) AND generation_id=sqlc.arg(generation_id);

-- name: RunMaintenance :one
SELECT refresh.maintenance(sqlc.arg(pLimit));

-- name: ListRecoveryRuns :many
SELECT run_id,COALESCE(job_id,''),status,fence_generation,lease_owner,COALESCE(lease_expires_at,'epoch'::timestamptz),environment,created_at FROM refresh.run WHERE environment=sqlc.arg(environment) AND job_id IS NOT NULL AND status IN ('queued','running','prepared') AND (sqlc.arg(after_created)::timestamptz='epoch'::timestamptz OR (created_at,run_id)>(sqlc.arg(after_created)::timestamptz,sqlc.arg(after_id)::text)) ORDER BY created_at,run_id LIMIT sqlc.arg(page_limit);

-- name: ListAttempts :many
SELECT run_id,attempt_number,fence_generation,owner_id,lease_expires_at,status,evidence,error,claimed_at,started_at,COALESCE(finished_at,'epoch'::timestamptz) FROM refresh.attempt WHERE run_id=sqlc.arg(run_id) ORDER BY attempt_number DESC LIMIT sqlc.arg(page_limit);

-- name: ListOccurrences :many
SELECT occurrence_id FROM refresh.schedule_occurrence WHERE project_id=sqlc.arg(project_id) AND environment=sqlc.arg(environment) AND (sqlc.arg(generation_id)::text='' OR generation_id=sqlc.arg(generation_id)::text) ORDER BY nominal_time DESC,occurrence_id DESC LIMIT sqlc.arg(page_limit);
