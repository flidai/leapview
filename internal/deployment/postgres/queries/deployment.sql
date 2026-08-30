-- Static PostgreSQL query leaves for deployment authority.

-- name: EnsureTargetRevision :exec
INSERT INTO delivery.delivery_target_revision(target_id)
VALUES(sqlc.arg(target_id))
ON CONFLICT(target_id) DO NOTHING;

-- name: LockTargetRevision :one
SELECT target_id,next_plan_revision,next_candidate_revision,next_generation_revision
FROM delivery.delivery_target_revision
WHERE target_id=sqlc.arg(target_id)
FOR UPDATE;

-- name: NextPlanRevision :one
UPDATE delivery.delivery_target_revision
SET next_plan_revision=next_plan_revision+1
WHERE target_id=sqlc.arg(target_id)
RETURNING (next_plan_revision-1)::bigint AS revision;

-- name: NextCandidateRevision :one
UPDATE delivery.delivery_target_revision
SET next_candidate_revision=next_candidate_revision+1
WHERE target_id=sqlc.arg(target_id)
RETURNING (next_candidate_revision-1)::bigint AS revision;

-- name: NextGenerationRevision :one
UPDATE delivery.delivery_target_revision
SET next_generation_revision=next_generation_revision+1
WHERE target_id=sqlc.arg(target_id)
RETURNING (next_generation_revision-1)::bigint AS revision;

-- name: GetApproval :one
SELECT approval_id::text AS approval_id,candidate_id::text AS candidate_id,COALESCE(principal_id::text,'')::text AS principal_id,decision,evidence,decided_at
FROM delivery.delivery_approval WHERE approval_id=sqlc.arg(approval_id)::uuid;

-- name: InsertApproval :exec
INSERT INTO delivery.delivery_approval(approval_id,candidate_id,principal_id,decision,evidence)
VALUES(sqlc.arg(approval_id)::uuid,sqlc.arg(candidate_id)::uuid,sqlc.narg(principal_id)::uuid,sqlc.arg(decision),sqlc.arg(evidence)::jsonb)
ON CONFLICT(approval_id) DO NOTHING;

-- name: GetRetentionRoot :one
SELECT root_id::text AS root_id,target_id,COALESCE(candidate_id::text,'')::text AS candidate_id,COALESCE(generation_id::text,'')::text AS generation_id,COALESCE(snapshot_seal_id::text,'')::text AS snapshot_seal_id,root_kind,state,expires_at,evidence,created_at,retired_at,expired_at
FROM delivery.delivery_retention_root WHERE root_id=sqlc.arg(root_id)::uuid;

-- name: DatabaseClock :one
SELECT clock_timestamp()::timestamptz;

-- name: InsertTarget :exec
INSERT INTO delivery.delivery_target(target_id,project_id,environment,target_revision)
VALUES(sqlc.arg(target_id),sqlc.arg(project_id),sqlc.arg(environment),sqlc.arg(target_revision))
ON CONFLICT(target_id) DO NOTHING;

-- name: InsertTargetFence :exec
INSERT INTO delivery.delivery_target_fence(target_id,next_fencing_epoch)
VALUES(sqlc.arg(target_id),1) ON CONFLICT(target_id) DO NOTHING;

-- name: GetTarget :one
SELECT t.target_id,t.project_id,t.environment,t.target_revision,
COALESCE((SELECT generation_id::text FROM delivery.delivery_active_pointer p WHERE p.target_id=t.target_id),'')::text AS active_generation_id,
COALESCE((SELECT publication_id::text FROM delivery.delivery_active_pointer p WHERE p.target_id=t.target_id),'')::text AS active_publication_id,
t.created_at,t.updated_at FROM delivery.delivery_target t WHERE t.target_id=sqlc.arg(target_id);

-- name: InsertPlan :exec
INSERT INTO delivery.delivery_plan(plan_id,target_id,plan_revision,plan_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,artifact_digest,qualification_digest,qualification_required,plan_document,evidence)
VALUES(sqlc.arg(plan_id)::uuid,sqlc.arg(target_id),sqlc.arg(plan_revision),sqlc.arg(plan_digest),sqlc.arg(compiled_graph_digest),sqlc.arg(compiled_config_digest),sqlc.arg(security_domain_fingerprint),sqlc.arg(artifact_digest),sqlc.arg(qualification_digest),sqlc.arg(qualification_required),sqlc.arg(plan_document)::jsonb,sqlc.arg(evidence)::jsonb)
ON CONFLICT(plan_id) DO NOTHING;

-- name: GetPlan :one
SELECT plan_id::text,target_id,plan_revision,plan_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,artifact_digest,qualification_digest,qualification_required,plan_document,evidence,created_at
FROM delivery.delivery_plan WHERE plan_id=sqlc.arg(plan_id)::uuid;

-- name: GetCandidatePlan :one
SELECT plan_id::text FROM delivery.delivery_candidate WHERE candidate_id=sqlc.arg(candidate_id)::uuid;

-- name: InsertBuildAttempt :exec
INSERT INTO delivery.delivery_build_attempt(attempt_id,plan_id,candidate_id,owner_id,physical_pool_id,fencing_epoch,request_digest,plan_digest,state,namespace,lease_expires_at,session_identity)
VALUES(sqlc.arg(attempt_id)::uuid,sqlc.arg(plan_id)::uuid,sqlc.narg(candidate_id)::uuid,sqlc.arg(owner_id),sqlc.arg(physical_pool_id),sqlc.arg(fencing_epoch),sqlc.arg(request_digest),sqlc.arg(plan_digest),'running',sqlc.arg(namespace),sqlc.arg(lease_expires_at),sqlc.arg(session_identity))
ON CONFLICT(attempt_id) DO NOTHING;

-- name: GetBuildAttempt :one
SELECT attempt_id::text,plan_id::text,COALESCE(candidate_id::text,'')::text AS candidate_id,owner_id,physical_pool_id,fencing_epoch,request_digest,plan_digest,state,namespace,lease_expires_at,session_identity,COALESCE(snapshot_id,0)::bigint AS snapshot_id,commit_marker,termination_evidence,created_at,updated_at,finished_at
FROM delivery.delivery_build_attempt WHERE attempt_id=sqlc.arg(attempt_id)::uuid;

-- name: LockBuildAttempt :one
SELECT attempt_id::text FROM delivery.delivery_build_attempt WHERE attempt_id=sqlc.arg(attempt_id)::uuid FOR UPDATE;

-- name: BuildAttemptLeaseActive :one
SELECT lease_expires_at > clock_timestamp() FROM delivery.delivery_build_attempt WHERE attempt_id=sqlc.arg(attempt_id)::uuid;

-- name: InsertBuildArtifactBinding :exec
INSERT INTO delivery.delivery_build_artifact_binding(attempt_id,serving_artifact_id,serving_artifact_digest,serving_state_id)
VALUES(sqlc.arg(attempt_id)::uuid,sqlc.arg(serving_artifact_id),sqlc.arg(serving_artifact_digest),sqlc.arg(serving_state_id))
ON CONFLICT(attempt_id) DO NOTHING;

-- name: GetBuildArtifactBinding :one
SELECT attempt_id::text,serving_artifact_id,serving_artifact_digest,serving_state_id,bound_at
FROM delivery.delivery_build_artifact_binding
WHERE attempt_id=sqlc.arg(attempt_id)::uuid;

-- name: CommitBuildAttempt :execrows
UPDATE delivery.delivery_build_attempt SET state='committed',snapshot_id=sqlc.arg(snapshot_id),commit_marker=sqlc.arg(commit_marker)::jsonb,updated_at=clock_timestamp(),finished_at=clock_timestamp()
WHERE attempt_id=sqlc.arg(attempt_id)::uuid AND state='running' AND owner_id=sqlc.arg(owner_id) AND fencing_epoch=sqlc.arg(fencing_epoch);

-- name: TerminateBuildAttempt :execrows
UPDATE delivery.delivery_build_attempt SET state=sqlc.arg(state),termination_evidence=sqlc.arg(evidence)::jsonb,updated_at=clock_timestamp(),finished_at=clock_timestamp()
WHERE attempt_id=sqlc.arg(attempt_id)::uuid AND state='running' AND owner_id=sqlc.arg(owner_id) AND fencing_epoch=sqlc.arg(fencing_epoch);

-- name: GetCandidateIdentity :one
SELECT target_id,plan_id::text,status,artifact_digest FROM delivery.delivery_candidate WHERE candidate_id=sqlc.arg(candidate_id)::uuid;

-- name: GetPlanDigests :one
SELECT plan_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,artifact_digest,qualification_digest FROM delivery.delivery_plan WHERE plan_id=sqlc.arg(plan_id)::uuid;

-- name: InsertSnapshotSeal :exec
INSERT INTO delivery.delivery_snapshot_seal(seal_id,attempt_id,candidate_id,physical_pool_id,tenant_domain,region,encryption_domain,object_namespace,catalog_database,catalog_id,catalog_uuid,catalog_version,ducklake_snapshot_id,relation_namespace,relation_manifest_digest,closure_digest,object_root,object_root_digest,artifact_root,artifact_root_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,request_digest,plan_digest,compatibility_digest,serving_artifact_id,serving_artifact_digest,duckdb_version,runtime_version,ducklake_extension_version,ducklake_spec_version,catalog_schema_version,qualification_evidence)
VALUES(sqlc.arg(seal_id)::uuid,sqlc.arg(attempt_id)::uuid,sqlc.arg(candidate_id)::uuid,sqlc.arg(physical_pool_id),sqlc.arg(tenant_domain),sqlc.arg(region),sqlc.arg(encryption_domain),sqlc.arg(object_namespace),sqlc.arg(catalog_database),sqlc.arg(catalog_id),sqlc.arg(catalog_uuid),sqlc.arg(catalog_version),sqlc.arg(ducklake_snapshot_id),sqlc.arg(relation_namespace),sqlc.arg(relation_manifest_digest),sqlc.arg(closure_digest),sqlc.arg(object_root),sqlc.arg(object_root_digest),sqlc.arg(artifact_root),sqlc.arg(artifact_root_digest),sqlc.arg(compiled_graph_digest),sqlc.arg(compiled_config_digest),sqlc.arg(security_domain_fingerprint),sqlc.arg(request_digest),sqlc.arg(plan_digest),sqlc.arg(compatibility_digest),sqlc.arg(serving_artifact_id),sqlc.arg(serving_artifact_digest),sqlc.arg(duckdb_version),sqlc.arg(runtime_version),sqlc.arg(ducklake_extension_version),sqlc.arg(ducklake_spec_version),sqlc.arg(catalog_schema_version),sqlc.arg(qualification_evidence)::jsonb)
ON CONFLICT(seal_id) DO NOTHING;

-- name: GetSnapshotSeal :one
SELECT seal_id::text,attempt_id::text,COALESCE(candidate_id::text,'')::text AS candidate_id,physical_pool_id,tenant_domain,region,encryption_domain,object_namespace,catalog_database,catalog_id,catalog_uuid,catalog_version,ducklake_snapshot_id,relation_namespace,relation_manifest_digest,closure_digest,object_root,object_root_digest,artifact_root,artifact_root_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,request_digest,plan_digest,compatibility_digest,serving_artifact_id,serving_artifact_digest,duckdb_version,runtime_version,ducklake_extension_version,ducklake_spec_version,catalog_schema_version,qualification_evidence,qualified_at
FROM delivery.delivery_snapshot_seal WHERE seal_id=sqlc.arg(seal_id)::uuid;

-- name: GetPlanTarget :one
SELECT target_id FROM delivery.delivery_plan WHERE plan_id=sqlc.arg(plan_id)::uuid;

-- name: InsertCandidate :exec
INSERT INTO delivery.delivery_candidate(candidate_id,target_id,plan_id,snapshot_seal_id,status,candidate_revision,artifact_digest,qualification_digest)
VALUES(sqlc.arg(candidate_id)::uuid,sqlc.arg(target_id),sqlc.arg(plan_id)::uuid,sqlc.narg(snapshot_seal_id)::uuid,sqlc.arg(status),sqlc.arg(candidate_revision),sqlc.arg(artifact_digest),sqlc.narg(qualification_digest))
ON CONFLICT(candidate_id) DO NOTHING;

-- name: GetCandidate :one
SELECT c.candidate_id::text AS candidate_id,c.target_id,c.plan_id::text AS plan_id,
COALESCE((SELECT s.attempt_id::text FROM delivery.delivery_snapshot_seal s WHERE s.seal_id=c.snapshot_seal_id),'')::text AS attempt_id,
COALESCE(c.snapshot_seal_id::text,'')::text AS snapshot_seal_id,c.status,c.candidate_revision,c.artifact_digest,COALESCE(c.qualification_digest,'')::text AS qualification_digest,c.created_at,c.qualified_at,c.retired_at
FROM delivery.delivery_candidate c WHERE c.candidate_id=sqlc.arg(candidate_id)::uuid;

-- name: QualifyCandidate :exec
UPDATE delivery.delivery_candidate SET status='qualified',snapshot_seal_id=sqlc.arg(snapshot_seal_id)::uuid,qualification_digest=sqlc.arg(qualification_digest),qualified_at=clock_timestamp()
WHERE candidate_id=sqlc.arg(candidate_id)::uuid AND status IN ('building','ready');

-- name: GetCandidateStatus :one
SELECT status,target_id,plan_id::text,COALESCE(snapshot_seal_id::text,'')::text AS snapshot_seal_id FROM delivery.delivery_candidate WHERE candidate_id=sqlc.arg(candidate_id)::uuid;

-- name: InsertGeneration :exec
INSERT INTO delivery.delivery_generation(generation_id,target_id,candidate_id,snapshot_seal_id,plan_id,plan_digest,artifact_root,artifact_root_digest,serving_artifact_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,generation_revision)
VALUES(sqlc.arg(generation_id)::uuid,sqlc.arg(target_id),sqlc.arg(candidate_id)::uuid,sqlc.arg(snapshot_seal_id)::uuid,sqlc.arg(plan_id)::uuid,sqlc.arg(plan_digest),sqlc.arg(artifact_root),sqlc.arg(artifact_root_digest),sqlc.arg(serving_artifact_digest),sqlc.arg(compiled_graph_digest),sqlc.arg(compiled_config_digest),sqlc.arg(security_domain_fingerprint),sqlc.arg(generation_revision))
ON CONFLICT(generation_id) DO NOTHING;

-- name: GetGeneration :one
SELECT generation_id::text,target_id,candidate_id::text,snapshot_seal_id::text,plan_id::text,plan_digest,artifact_root,artifact_root_digest,serving_artifact_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,generation_revision,created_at
FROM delivery.delivery_generation WHERE generation_id=sqlc.arg(generation_id)::uuid;

-- name: LockTargetForShare :one
SELECT t.target_id,t.project_id,t.environment,t.target_revision,
COALESCE((SELECT generation_id::text FROM delivery.delivery_active_pointer p WHERE p.target_id=t.target_id),'')::text AS active_generation_id,
COALESCE((SELECT publication_id::text FROM delivery.delivery_active_pointer p WHERE p.target_id=t.target_id),'')::text AS active_publication_id,t.created_at,t.updated_at
FROM delivery.delivery_target t WHERE t.target_id=sqlc.arg(target_id) FOR SHARE;

-- name: GetGenerationLinks :one
SELECT target_id,candidate_id::text,snapshot_seal_id::text FROM delivery.delivery_generation WHERE generation_id=sqlc.arg(generation_id)::uuid;

-- name: GetActiveGeneration :one
SELECT COALESCE(generation_id::text,'')::text AS active_generation_id FROM delivery.delivery_active_pointer WHERE target_id=sqlc.arg(target_id);

-- name: InsertPublication :exec
INSERT INTO delivery.delivery_publication(publication_id,target_id,generation_id,expected_base_generation_id,candidate_id,snapshot_seal_id,expected_target_revision,actor_id,state,request_digest)
VALUES(sqlc.arg(publication_id)::uuid,sqlc.arg(target_id),sqlc.arg(generation_id)::uuid,sqlc.narg(expected_base_generation_id)::uuid,sqlc.arg(candidate_id)::uuid,sqlc.arg(snapshot_seal_id)::uuid,sqlc.arg(expected_target_revision),sqlc.arg(actor_id),'pending',sqlc.arg(request_digest))
ON CONFLICT(publication_id) DO NOTHING;

-- name: GetPublication :one
SELECT publication_id::text,target_id,generation_id::text,COALESCE(expected_base_generation_id::text,'')::text AS expected_base_generation_id,candidate_id::text,snapshot_seal_id::text,expected_target_revision,COALESCE(result_target_revision,0)::bigint AS result_target_revision,actor_id,state,request_digest,created_at,committed_at
FROM delivery.delivery_publication WHERE publication_id=sqlc.arg(publication_id)::uuid;

-- name: FindCommittedPublication :one
SELECT p.publication_id::text FROM delivery.delivery_publication p
JOIN delivery.delivery_active_pointer ap ON ap.target_id=p.target_id AND ap.generation_id=p.generation_id AND ap.publication_id=p.publication_id
JOIN delivery.delivery_target t ON t.target_id=p.target_id
WHERE p.generation_id=sqlc.arg(generation_id)::uuid AND p.state='committed' AND p.result_target_revision=t.target_revision
ORDER BY p.committed_at DESC,p.publication_id DESC LIMIT 1;

-- name: FindHistoricalCommittedPublication :one
-- Unlike FindCommittedPublication, this lookup intentionally does not join
-- the active pointer. A completed generation remains replayable after a
-- successor is activated; the immutable committed publication is the proof.
SELECT p.publication_id::text FROM delivery.delivery_publication p
WHERE p.generation_id=sqlc.arg(generation_id)::uuid AND p.state='committed'
ORDER BY p.committed_at DESC,p.publication_id DESC LIMIT 1;

-- name: EnsureTargetFence :exec
INSERT INTO delivery.delivery_target_fence(target_id,next_fencing_epoch)
SELECT t.target_id,1 FROM delivery.delivery_target t WHERE t.target_id=sqlc.arg(target_id)
ON CONFLICT(target_id) DO NOTHING;

-- name: LockTargetFence :one
SELECT next_fencing_epoch FROM delivery.delivery_target_fence WHERE target_id=sqlc.arg(target_id) FOR UPDATE;

-- name: LockLease :one
SELECT lease_id::text,target_id,owner_id,fencing_epoch,state,expires_at,acquired_at,released_at
FROM delivery.delivery_lease WHERE lease_id=sqlc.arg(lease_id)::uuid FOR UPDATE;

-- name: ExpireLeases :exec
UPDATE delivery.delivery_lease SET state='expired',released_at=clock_timestamp() WHERE target_id=sqlc.arg(target_id) AND state='active';

-- name: AdvanceTargetFence :exec
UPDATE delivery.delivery_target_fence SET next_fencing_epoch=sqlc.arg(next_fencing_epoch) WHERE target_id=sqlc.arg(target_id);

-- name: InsertLease :exec
INSERT INTO delivery.delivery_lease(lease_id,target_id,owner_id,fencing_epoch,state,expires_at,acquired_at)
VALUES(sqlc.arg(lease_id)::uuid,sqlc.arg(target_id),sqlc.arg(owner_id),sqlc.arg(fencing_epoch),'active',sqlc.arg(expires_at),sqlc.arg(acquired_at))
ON CONFLICT(lease_id) DO NOTHING;

-- name: GetLease :one
SELECT lease_id::text,target_id,owner_id,fencing_epoch,state,expires_at,acquired_at,released_at
FROM delivery.delivery_lease WHERE lease_id=sqlc.arg(lease_id)::uuid;

-- name: ReleaseLease :one
UPDATE delivery.delivery_lease SET state='released',released_at=clock_timestamp()
WHERE lease_id=sqlc.arg(lease_id)::uuid AND target_id=sqlc.arg(target_id) AND owner_id=sqlc.arg(owner_id) AND fencing_epoch=sqlc.arg(fencing_epoch) AND state='active' AND expires_at>clock_timestamp() RETURNING true;

-- name: RenewLease :one
UPDATE delivery.delivery_lease SET expires_at=sqlc.arg(expires_at)
WHERE lease_id=sqlc.arg(lease_id)::uuid AND target_id=sqlc.arg(target_id) AND owner_id=sqlc.arg(owner_id) AND fencing_epoch=sqlc.arg(fencing_epoch) AND state='active' AND expires_at>clock_timestamp() RETURNING true;

-- name: LockPublication :one
SELECT publication_id FROM delivery.delivery_publication WHERE publication_id=sqlc.arg(publication_id)::uuid FOR UPDATE;

-- name: CancelPublication :execrows
UPDATE delivery.delivery_publication SET state='rejected'
WHERE publication_id=sqlc.arg(publication_id)::uuid AND state='pending';

-- name: LockLeaseForActivation :one
SELECT target_id,owner_id,fencing_epoch,state,(expires_at > clock_timestamp())::boolean AS lease_active FROM delivery.delivery_lease WHERE lease_id=sqlc.arg(lease_id)::uuid FOR UPDATE;

-- name: LockTargetForUpdate :one
SELECT t.target_revision,COALESCE((SELECT generation_id::text FROM delivery.delivery_active_pointer p WHERE p.target_id=t.target_id),'')::text AS active_generation_id
FROM delivery.delivery_target t WHERE t.target_id=sqlc.arg(target_id) FOR UPDATE;

-- name: GetSnapshotSealProof :one
SELECT attempt_id::text,request_digest,plan_digest,ducklake_snapshot_id FROM delivery.delivery_snapshot_seal WHERE seal_id=sqlc.arg(seal_id)::uuid;

-- name: GetPlanQualification :one
SELECT qualification_required FROM delivery.delivery_plan WHERE plan_id=(SELECT plan_id FROM delivery.delivery_generation WHERE generation_id=sqlc.arg(generation_id)::uuid);

-- name: CandidateApproved :one
SELECT COALESCE((SELECT decision='approved' FROM delivery.delivery_approval WHERE candidate_id=sqlc.arg(candidate_id)::uuid ORDER BY decided_at DESC, approval_id DESC LIMIT 1),false)::boolean;

-- name: UpdateTargetRevision :one
UPDATE delivery.delivery_target SET target_revision=sqlc.arg(new_revision),updated_at=clock_timestamp()
WHERE target_id=sqlc.arg(target_id) AND target_revision=sqlc.arg(expected_revision) RETURNING true;

-- name: UpsertActivePointer :exec
INSERT INTO delivery.delivery_active_pointer(target_id,generation_id,publication_id)
VALUES(sqlc.arg(target_id),sqlc.arg(generation_id)::uuid,sqlc.arg(publication_id)::uuid)
ON CONFLICT(target_id) DO UPDATE SET generation_id=EXCLUDED.generation_id,publication_id=EXCLUDED.publication_id,changed_at=clock_timestamp();

-- name: CommitPublication :one
UPDATE delivery.delivery_publication SET state='committed',result_target_revision=sqlc.arg(result_revision),committed_at=clock_timestamp()
WHERE publication_id=sqlc.arg(publication_id)::uuid AND state='pending' RETURNING true;

-- name: LockRetentionRoot :one
SELECT target_id,COALESCE(candidate_id::text,'')::text AS candidate_id,COALESCE(generation_id::text,'')::text AS generation_id,COALESCE(snapshot_seal_id::text,'')::text AS snapshot_seal_id,root_kind,state
FROM delivery.delivery_retention_root WHERE root_id=sqlc.arg(root_id)::uuid FOR UPDATE;

-- name: InsertGenerationRoot :exec
INSERT INTO delivery.delivery_retention_root(root_id,target_id,candidate_id,generation_id,snapshot_seal_id,root_kind,state)
VALUES(sqlc.arg(root_id)::uuid,sqlc.arg(target_id),sqlc.arg(candidate_id)::uuid,sqlc.arg(generation_id)::uuid,sqlc.arg(snapshot_seal_id)::uuid,'generation','live');

-- name: InsertRetentionRoot :exec
INSERT INTO delivery.delivery_retention_root(root_id,target_id,candidate_id,generation_id,snapshot_seal_id,root_kind,state,expires_at,evidence)
VALUES(sqlc.arg(root_id)::uuid,sqlc.arg(target_id),sqlc.narg(candidate_id)::uuid,sqlc.narg(generation_id)::uuid,sqlc.narg(snapshot_seal_id)::uuid,sqlc.arg(root_kind),sqlc.arg(state),sqlc.narg(expires_at),sqlc.arg(evidence)::jsonb)
ON CONFLICT(root_id) DO NOTHING;
