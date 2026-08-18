-- Canonical project-generation deployment records.

-- name: GetServingStateForDeployment :one
SELECT project_id, environment, digest, status FROM serving_states WHERE id = ?;

-- name: GetActiveServingState :one
SELECT generation_id FROM project_active_serving_states
WHERE project_id = ? AND environment = ?;

-- Activation state transitions are deliberately named separately from the
-- serving-state reads above.  Each method returns RowsAffected so the
-- repository can preserve the multi-row compare-and-swap fence.

-- name: DrainServingStateForActivation :execresult
UPDATE serving_states
SET status = 'draining', superseded_at = CURRENT_TIMESTAMP
WHERE id = ? AND project_id = ? AND environment = ? AND status = 'active';

-- name: ActivateServingStateForActivation :execresult
UPDATE serving_states
SET status = 'active', activated_at = CURRENT_TIMESTAMP, error = ''
WHERE id = ? AND project_id = ? AND environment = ? AND status IN ('validated', 'inactive');

-- name: InsertActiveServingStateForActivation :execresult
INSERT INTO project_active_serving_states (project_id, environment, generation_id, updated_at)
VALUES (?, ?, ?, CURRENT_TIMESTAMP);

-- name: UpdateActiveServingStateForActivation :execresult
UPDATE project_active_serving_states
SET generation_id = sqlc.arg(candidate_generation_id), updated_at = CURRENT_TIMESTAMP
WHERE project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND generation_id = sqlc.arg(prior_generation_id);

-- name: CreateProjectDeployment :exec
INSERT INTO project_deployments (id, project_id, environment, generation_id, artifact_digest, prior_generation_id, request_digest, status, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?);

-- name: GetProjectDeployment :one
SELECT * FROM project_deployments WHERE id = ?;

-- Deployment approval decisions are immutable in scope and optimistic in
-- transition. A revoked decision remains as audit evidence; a later request
-- receives a new identity.

-- name: CreateDeploymentApproval :exec
INSERT INTO deployment_approvals (
  id, project_id, deployment_id, environment, request_digest, release_id,
  status, requested_by, request_credential_class, request_credential_id,
  requested_at, approved_by, approval_credential_class,
  approval_credential_id, approval_credential_expires_at, approved_at, revoked_by, revoked_at,
  expires_at, revision
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetCurrentDeploymentApproval :one
SELECT *
FROM deployment_approvals
WHERE deployment_id = ?
ORDER BY requested_at DESC, id DESC
LIMIT 1;

-- name: UpdateDeploymentApproval :execrows
UPDATE deployment_approvals
SET status = ?,
    approved_by = ?,
    approval_credential_class = ?,
    approval_credential_id = ?,
    approval_credential_expires_at = ?,
    approved_at = ?,
    revoked_by = ?,
    revoked_at = ?,
    expires_at = ?,
    revision = ?
WHERE id = ?
  AND deployment_id = ?
  AND revision = ?;

-- name: ActivateProjectDeployment :execresult
UPDATE project_deployments
SET status = 'active',
    activated_at = CURRENT_TIMESTAMP,
    activation_principal = ?,
    verification_digest = ?,
    verified_at = CURRENT_TIMESTAMP,
    error = ''
WHERE id = ? AND status = 'pending';

-- name: SupersedeOtherProjectDeployments :exec
UPDATE project_deployments
SET status = 'superseded'
WHERE project_id = ? AND environment = ? AND id <> ? AND status = 'active';

-- name: FailProjectDeployment :execresult
UPDATE project_deployments
SET status = 'failed', error = ?
WHERE id = ? AND status = 'pending';

-- name: CancelProjectDeployment :execresult
UPDATE project_deployments
SET status = 'cancelled'
WHERE id = ? AND status = 'pending';

-- Deployment-owned validation projections over managed-data records.

-- Durable private project candidate sessions.

-- name: InsertInstanceProjectClaim :execresult
INSERT INTO instance_project_claim (singleton_id, project_id, environment, claimed_by, claimed_at)
VALUES (1, ?, ?, ?, ?)
ON CONFLICT(singleton_id) DO NOTHING;

-- name: GetInstanceProjectClaim :one
SELECT project_id, environment, claimed_by, claimed_at
FROM instance_project_claim
WHERE singleton_id = 1;

-- Permanent one-shot binding for first protected activation.

-- name: InsertBootstrapActivationPolicy :execresult
INSERT INTO bootstrap_activation_policies (
  deployment_id, project_id, environment, request_digest, actor_id,
  credential_id, credential_expires_at, armed_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(deployment_id) DO NOTHING;

-- name: GetBootstrapActivationPolicy :one
SELECT deployment_id, project_id, environment, request_digest, actor_id,
       credential_id, credential_expires_at, armed_at
FROM bootstrap_activation_policies
WHERE deployment_id = ?;

-- name: GetBootstrapActivationPolicyByScope :one
SELECT deployment_id, project_id, environment, request_digest, actor_id,
       credential_id, credential_expires_at, armed_at
FROM bootstrap_activation_policies
WHERE project_id = ? AND environment = ?;

-- name: CreateProjectCandidate :exec
INSERT INTO project_candidates (
  id, project_id, target_id, environment, owner_principal_id, candidate_key,
  base_generation, artifact_digest, provenance_digest, status, failure_reason,
  expires_at, created_at, updated_at, ready_at, cancelled_at,
  expired_at, revision
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetProjectCandidate :one
SELECT *
FROM project_candidates
WHERE id = ?;

-- name: GetActiveProjectCandidateSession :one
SELECT *
FROM project_candidates
WHERE target_id = ?
  AND project_id = ?
  AND owner_principal_id = ?
  AND candidate_key = ?
  AND status IN ('preparing', 'ready', 'failed')
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: GetActiveProjectCandidateByKey :one
SELECT *
FROM project_candidates
WHERE target_id = ?
  AND project_id = ?
  AND owner_principal_id = ?
  AND candidate_key = ?
  AND status IN ('preparing', 'ready', 'failed')
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: CountActiveProjectCandidatesForOwner :one
SELECT count(*)
FROM project_candidates
WHERE owner_principal_id = ?
  AND status IN ('preparing', 'ready', 'failed');

-- name: GetActiveProjectCandidateBaseGeneration :one
SELECT id
FROM project_deployments
WHERE project_id = ?
  AND environment = ?
  AND status = 'active'
ORDER BY activated_at DESC, created_at DESC, id DESC
LIMIT 1;

-- name: UpdateProjectCandidate :execrows
UPDATE project_candidates
SET artifact_digest = ?,
    provenance_digest = ?,
    status = ?,
    failure_reason = ?,
    expires_at = ?,
    updated_at = ?,
    ready_at = ?,
    cancelled_at = ?,
    expired_at = ?,
    revision = ?
WHERE id = ?
  AND revision = ?;

-- name: ExpireProjectCandidates :execrows
UPDATE project_candidates
SET status = 'expired',
    provenance_digest = '',
    failure_reason = '',
    ready_at = NULL,
    expired_at = ?,
    updated_at = ?,
    revision = revision + 1
WHERE target_id = ?
  AND status IN ('preparing', 'ready', 'failed')
  AND expires_at <= ?;

-- name: CancelSupersededProjectCandidate :execrows
UPDATE project_candidates
SET status = 'cancelled', failure_reason = 'candidate base generation superseded',
    cancelled_at = sqlc.arg(cancelled_at), updated_at = sqlc.arg(updated_at),
    revision = revision + 1
WHERE id = sqlc.arg(id) AND status IN ('preparing', 'ready', 'failed');

-- name: GetManagedDataCollection :one
SELECT * FROM managed_data_collections WHERE id = ?;

-- name: GetManagedDataRevision :one
SELECT * FROM managed_data_revisions WHERE id = ?;

-- Operator delivery projections. These queries keep scope filtering and
-- persistence SQL in the deployment-owned generated adapter; the reader must
-- not construct ad-hoc SQL strings for operator status.

-- name: GetDeliveryOperatorTarget :one
SELECT target_id, target_revision, COALESCE(active_generation_id, '') AS active_generation_id
FROM delivery_target_revisions WHERE project_id = ? AND environment = ?;

-- name: ListDeliveryOperatorPhysicalPoolAdmissions :many
SELECT p.id, p.identity_digest, a.compatibility_digest, a.evidence_digest,
       a.conformance_version,
       json_extract(a.compatibility_json, '$.duckdb_runtime') AS duckdb_runtime,
       json_extract(a.compatibility_json, '$.ducklake_extension') AS ducklake_extension,
       json_extract(a.compatibility_json, '$.catalog_format') AS catalog_format,
       json_extract(a.compatibility_json, '$.storage_implementation') AS storage_implementation,
       json_extract(a.compatibility_json, '$.object_naming_contract') AS object_naming_contract,
       a.admitted_at
FROM physical_pools p JOIN physical_pool_admissions a ON a.pool_id = p.id
WHERE p.id IN (
  SELECT c.physical_pool_id FROM delivery_candidates c WHERE c.project_id = ? AND c.environment = ?
  UNION SELECT g.physical_pool_id FROM delivery_generations g WHERE g.project_id = ? AND g.environment = ?
  UNION SELECT a2.physical_pool_id FROM delivery_build_attempts a2 JOIN delivery_plans p2 ON p2.id = a2.plan_id WHERE p2.project_id = ? AND p2.environment = ?
)
ORDER BY p.id, a.admitted_at;

-- name: ListDeliveryOperatorRoots :many
SELECT r.physical_pool_id, r.root_kind, r.source_id,
       COALESCE(r.candidate_id,''), COALESCE(r.generation_id,''), COALESCE(r.lease_id,''),
       r.catalog_digest, r.status, r.created_at, COALESCE(r.expires_at,'')
FROM delivery_root_registry r
WHERE r.status = 'active' AND (
  EXISTS (SELECT 1 FROM delivery_candidates c WHERE c.id = r.candidate_id AND c.project_id = ? AND c.environment = ?)
  OR EXISTS (SELECT 1 FROM delivery_generations g WHERE g.id = r.generation_id AND g.project_id = ? AND g.environment = ?)
  OR EXISTS (SELECT 1 FROM delivery_build_attempts b JOIN delivery_plans p ON p.id = b.plan_id WHERE b.id = r.source_id AND p.project_id = ? AND p.environment = ?)
  OR EXISTS (SELECT 1 FROM delivery_query_leases l JOIN delivery_candidates c ON c.id = l.candidate_id WHERE l.id = r.lease_id AND c.project_id = ? AND c.environment = ?)
  OR EXISTS (SELECT 1 FROM delivery_query_leases l JOIN delivery_generations g ON g.id = l.generation_id WHERE l.id = r.lease_id AND g.project_id = ? AND g.environment = ?)
)
ORDER BY r.physical_pool_id, r.root_kind, r.source_id;

-- name: ListDeliveryOperatorQueryLeases :many
SELECT l.id, l.holder_id, COALESCE(l.candidate_id,''), COALESCE(l.generation_id,''),
       l.physical_pool_id, l.catalog_digest, l.status, l.created_at, l.expires_at
FROM delivery_query_leases l
WHERE EXISTS (SELECT 1 FROM delivery_candidates c WHERE c.id = l.candidate_id AND c.project_id = ? AND c.environment = ?)
   OR EXISTS (SELECT 1 FROM delivery_generations g WHERE g.id = l.generation_id AND g.project_id = ? AND g.environment = ?)
ORDER BY l.created_at DESC;

-- name: ListDeliveryOperatorWriterLeases :many
SELECT l.id, l.attempt_id, l.physical_pool_id, l.owner_id, l.epoch, l.status,
       l.created_at, l.expires_at, COALESCE(l.released_at,'')
FROM delivery_writer_leases l JOIN delivery_build_attempts a ON a.id = l.attempt_id
JOIN delivery_plans p ON p.id = a.plan_id
WHERE p.project_id = ? AND p.environment = ? ORDER BY l.created_at DESC;

-- name: ListDeliveryOperatorGCCycles :many
SELECT g.id, g.physical_pool_id, g.epoch, g.root_revision, COALESCE(g.mark_digest,''),
       g.status, g.created_at, COALESCE(g.completed_at,''), COALESCE(g.abort_reason,'')
FROM delivery_gc_cycles g WHERE g.physical_pool_id IN (
  SELECT c2.physical_pool_id FROM delivery_candidates c2 WHERE c2.project_id = ? AND c2.environment = ?
  UNION SELECT g2.physical_pool_id FROM delivery_generations g2 WHERE g2.project_id = ? AND g2.environment = ?
  UNION SELECT a.physical_pool_id FROM delivery_build_attempts a JOIN delivery_plans p ON p.id = a.plan_id WHERE p.project_id = ? AND p.environment = ?
)
ORDER BY g.created_at DESC;

-- name: ListDeliveryOperatorGCDeleteIntents :many
SELECT i.id, i.cycle_id, i.physical_pool_id, i.object_digest, COALESCE(i.object_version,''),
       i.status, i.created_at, COALESCE(i.completed_at,'')
FROM delivery_gc_delete_intents i WHERE i.physical_pool_id IN (
  SELECT c3.physical_pool_id FROM delivery_candidates c3 WHERE c3.project_id = ? AND c3.environment = ?
  UNION SELECT g3.physical_pool_id FROM delivery_generations g3 WHERE g3.project_id = ? AND g3.environment = ?
  UNION SELECT a.physical_pool_id FROM delivery_build_attempts a JOIN delivery_plans p ON p.id = a.plan_id WHERE p.project_id = ? AND p.environment = ?
)
ORDER BY i.created_at DESC;

-- name: CountDeliveryOperatorActiveGCLeases :one
SELECT count(*) FROM delivery_gc_leases WHERE physical_pool_id IN (
  SELECT c4.physical_pool_id FROM delivery_candidates c4 WHERE c4.project_id = ? AND c4.environment = ?
  UNION SELECT g4.physical_pool_id FROM delivery_generations g4 WHERE g4.project_id = ? AND g4.environment = ?
  UNION SELECT a.physical_pool_id FROM delivery_build_attempts a JOIN delivery_plans p ON p.id = a.plan_id WHERE p.project_id = ? AND p.environment = ?
) AND status = 'active';

-- Plan-driven delivery control state. These named queries are deliberately
-- kept in the deployment-owned sqlc package so SQLite adapters cannot embed
-- persistence SQL in capability code. RowsAffected from CAS statements is
-- retained by :execresult/:execrows callers.

-- name: EnsureDeliveryTargetRevision :exec
INSERT INTO delivery_target_revisions
  (target_id, project_id, environment, target_revision, active_generation_id, created_at, updated_at)
VALUES (?, ?, ?, 0, NULL, ?, ?)
ON CONFLICT(target_id) DO NOTHING;

-- name: GetDeliveryTargetRevision :one
SELECT project_id, environment, target_revision, COALESCE(active_generation_id, '') AS active_generation_id
FROM delivery_target_revisions WHERE target_id = ?;

-- name: GetDeliveryPublicationRequestActor :one
SELECT actor_id FROM delivery_events
WHERE object_kind = 'publication' AND object_id = ? AND event_kind = 'publish_requested'
ORDER BY created_at ASC, rowid ASC LIMIT 1;

-- name: GetServingStateIDForArtifact :one
SELECT serving_state_id FROM serving_state_artifacts WHERE id = ?;

-- name: HasIndeterminateDeliveryPublication :one
SELECT EXISTS(
  SELECT 1 FROM delivery_publications
  WHERE target_id = ? AND status = 'indeterminate'
);

-- name: GetDeliveryTargetScope :one
SELECT target_id, project_id, environment
FROM delivery_target_revisions WHERE project_id = ? AND environment = ?;

-- name: GetDeliveryTargetScopeByPoolCandidate :one
SELECT target_id, project_id, environment, plan_digest
FROM delivery_candidates WHERE physical_pool_id = ?
ORDER BY created_at ASC, id ASC LIMIT 1;

-- name: GetDeliveryTargetScopeByPoolGeneration :one
SELECT target_id, project_id, environment, plan_digest
FROM delivery_generations WHERE physical_pool_id = ?
ORDER BY created_at ASC, id ASC LIMIT 1;

-- name: CreateDeliveryPlan :exec
INSERT INTO delivery_plans
 (id, target_id, project_id, environment, operation_kind, source_digest,
  base_generation_id, base_target_revision, execution_digest, execution_inputs_json,
  provenance_digest, governance_digest, provenance_json, governance_json, evidence_json, evidence_digest,
  plan_digest, status, expires_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetDeliveryPlan :one
SELECT id,target_id,project_id,environment,operation_kind,source_digest,base_generation_id,
       base_target_revision,execution_digest,execution_inputs_json,provenance_digest,
       governance_digest,plan_digest,status,expires_at,created_at,provenance_json,governance_json,evidence_json,evidence_digest
FROM delivery_plans WHERE id = ?;

-- name: GetDeliveryPlanIDByTargetDigest :one
SELECT id FROM delivery_plans WHERE target_id = ? AND plan_digest = ?;

-- name: ExpireDeliveryPlan :execresult
UPDATE delivery_plans SET status = 'expired' WHERE id = ? AND status = 'planned';

-- name: ExpireDeliveryWriterLeasesForPool :exec
UPDATE delivery_writer_leases SET status='expired', released_at=?
WHERE physical_pool_id=? AND status='active' AND julianday(expires_at) <= julianday(?);

-- name: AdvanceDeliveryWriterEpoch :execresult
UPDATE delivery_pool_fences SET writer_epoch=writer_epoch+1, updated_at=?
WHERE physical_pool_id=? AND (gc_lease_id IS NULL OR julianday(gc_expires_at) <= julianday(?));

-- name: GetDeliveryWriterEpoch :one
SELECT writer_epoch FROM delivery_pool_fences WHERE physical_pool_id = ?;

-- name: CreateDeliveryWriterLease :exec
INSERT INTO delivery_writer_leases
 (id,attempt_id,physical_pool_id,owner_id,epoch,status,expires_at,created_at,released_at)
VALUES (?,?,?,?,?,'active',?,?,NULL);

-- name: CreateDeliveryBuildAttempt :exec
INSERT INTO delivery_build_attempts
 (id,plan_id,idempotency_key,plan_digest,source_digest,execution_digest,base_generation_id,base_catalog_digest,
  base_physical_pool_id,physical_pool_id,writer_lease_id,status,seal_id,candidate_id,failure_code,
  revision,created_at,updated_at,terminal_at)
VALUES (?,?,?, ?,?,?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),?,?,'building',NULL,NULL,'',1,?,?,NULL);

-- name: GetDeliveryWriterLease :one
SELECT id,attempt_id,physical_pool_id,owner_id,epoch,status,expires_at,created_at,released_at
FROM delivery_writer_leases WHERE id = ?;

-- name: UpdateDeliveryWriterLeaseStatus :execresult
UPDATE delivery_writer_leases SET status=?, released_at=? WHERE id=? AND status=?;

-- name: RenewDeliveryWriterLease :execresult
UPDATE delivery_writer_leases SET expires_at=? WHERE id=? AND status='active' AND expires_at=?;

-- name: UpdateDeliveryBuildAttempt :execresult
UPDATE delivery_build_attempts
SET status=?, seal_id=NULLIF(?,''), candidate_id=NULLIF(?,''), failure_code=?, revision=?,
    updated_at=?, terminal_at=? WHERE id=? AND revision=?;

-- name: GetDeliveryBuildAttempt :one
SELECT id,plan_id,idempotency_key,plan_digest,source_digest,execution_digest,base_generation_id,base_catalog_digest,
       base_physical_pool_id,physical_pool_id,writer_lease_id,status,seal_id,candidate_id,
       failure_code,revision,created_at,updated_at,terminal_at
FROM delivery_build_attempts WHERE id = ?;

-- name: CreateDeliveryBuildArtifactBinding :exec
INSERT INTO delivery_build_artifact_bindings
 (attempt_id, serving_artifact_id, serving_artifact_digest, serving_state_id, created_at)
VALUES (?,?,?,?,?);

-- name: GetDeliveryBuildArtifactBinding :one
SELECT attempt_id, serving_artifact_id, serving_artifact_digest, serving_state_id, created_at
FROM delivery_build_artifact_bindings WHERE attempt_id = ?;

-- name: CreateDeliveryCatalogSeal :exec
INSERT INTO delivery_catalog_seals
 (id,attempt_id,plan_id,plan_digest,execution_digest,physical_pool_id,catalog_digest,
  base_catalog_digest,base_physical_pool_id,compatibility_digest,object_key,object_size,
  closure_digest,qualification_digest,status,failure_code,serving_artifact_id,
  serving_artifact_digest,serving_state_id,created_at,verified_at)
VALUES (?,?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),?,?,?,NULL,NULL,'preparing','',?,?,?,?,NULL);

-- name: GetDeliveryCatalogSeal :one
SELECT id,attempt_id,plan_id,plan_digest,execution_digest,physical_pool_id,catalog_digest,
       base_catalog_digest,base_physical_pool_id,compatibility_digest,object_key,object_size,
       closure_digest,qualification_digest,status,failure_code,serving_artifact_id,
       serving_artifact_digest,serving_state_id,created_at,verified_at
FROM delivery_catalog_seals WHERE id = ?;

-- name: GetDeliveryCatalogSealWithIdentity :one
SELECT id,attempt_id,plan_id,plan_digest,execution_digest,physical_pool_id,catalog_digest,
       base_catalog_digest,base_physical_pool_id,compatibility_digest,object_key,object_size,
       closure_digest,qualification_digest,status,failure_code,created_at,verified_at,
       identity_candidate_id,identity_closure_digest,identity_qualification_digest,
       serving_artifact_id,serving_artifact_digest,serving_state_id
FROM delivery_catalog_seals WHERE id = ?;

-- name: GetDeliveryCatalogSealIDByAttempt :one
SELECT id FROM delivery_catalog_seals WHERE attempt_id = ?;

-- name: GetDeliveryCatalogSealIDByObjectKey :one
SELECT id FROM delivery_catalog_seals WHERE object_key = ?;

-- name: CreateDeliveryCatalogSealWithIdentity :exec
INSERT INTO delivery_catalog_seals
 (id,attempt_id,plan_id,plan_digest,execution_digest,physical_pool_id,catalog_digest,
 base_catalog_digest,base_physical_pool_id,compatibility_digest,object_key,object_size,
  closure_digest,qualification_digest,status,failure_code,created_at,verified_at,
  identity_candidate_id,identity_closure_digest,identity_qualification_digest,
  serving_artifact_id,serving_artifact_digest,serving_state_id)
VALUES (?,?,?,?,?, ?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),?,?,?,NULL,NULL,
        'preparing','',?,NULL,?,?,?,?,?,?);

-- name: ReleaseDeliveryWriterLeaseExact :execresult
UPDATE delivery_writer_leases SET status='released',released_at=?
WHERE id=? AND attempt_id=? AND physical_pool_id=? AND owner_id=? AND epoch=?
  AND status='active' AND julianday(expires_at)>julianday(?);

-- name: MarkDeliveryCatalogSealUploaded :execresult
UPDATE delivery_catalog_seals SET status='uploaded' WHERE id=? AND status='preparing';

-- name: MarkDeliveryCatalogSealVerified :execresult
UPDATE delivery_catalog_seals SET status='verified',closure_digest=?,qualification_digest=?,verified_at=?
WHERE id=? AND status='uploaded';

-- name: FailDeliveryCatalogSeal :execresult
UPDATE delivery_catalog_seals SET status='failed',failure_code=?
WHERE id=? AND status IN ('preparing','uploaded');

-- name: GetDeliveryCandidate :one
SELECT id,plan_id,plan_digest,target_id,project_id,environment,source_digest,execution_digest,
       base_generation_id,base_target_revision,seal_id,catalog_digest,base_catalog_digest,
       base_physical_pool_id,compatibility_digest,catalog_object_key,physical_pool_id,
       serving_artifact_id,serving_artifact_digest,serving_state_id,qualification_digest,resolved_inputs_json,resolved_inputs_digest,status,failure_code,
       created_at,ready_at,retired_at
FROM delivery_candidates WHERE id = ?;

-- name: MarkDeliveryCandidateReady :execresult
UPDATE delivery_candidates SET qualification_digest=?,status='ready',ready_at=?
WHERE id=? AND status='preparing';

-- name: SealDeliveryBuildAttempt :execresult
UPDATE delivery_build_attempts SET status='sealed',seal_id=?,candidate_id=?,revision=?,updated_at=?,terminal_at=?
WHERE id=? AND status='sealing' AND revision=?;

-- name: CreateDeliveryCandidateReady :exec
INSERT INTO delivery_candidates
 (id,plan_id,plan_digest,target_id,project_id,environment,source_digest,execution_digest,
 base_generation_id,base_target_revision,seal_id,catalog_digest,base_catalog_digest,
 base_physical_pool_id,compatibility_digest,catalog_object_key,physical_pool_id,
 serving_artifact_id,serving_artifact_digest,serving_state_id,qualification_digest,resolved_inputs_json,resolved_inputs_digest,status,failure_code,created_at,ready_at,retired_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?,''), ?, ?, ?, NULLIF(?,''), NULLIF(?,''), ?, ?, ?, ?, ?, ?, ?, ?, ?, 'ready','', ?, ?, NULL);

-- name: CreateDeliveryGeneration :exec
INSERT INTO delivery_generations
 (id,candidate_id,plan_id,plan_digest,target_id,project_id,environment,catalog_digest,
  catalog_object_key,physical_pool_id,serving_artifact_id,serving_artifact_digest,
  rollback_class,rollback_external_effects_json,serving_state_id,compatibility_digest,status,created_at,activated_at,retired_at,rollback_until)
VALUES (?,?,?,?,?,?,?,?,?,?,?, ?,?,?,?,?,'prepared',?,?,NULL,?);

-- name: CreateDeliveryPublication :exec
INSERT INTO delivery_publications
 (id,request_digest,target_id,project_id,environment,plan_id,plan_digest,candidate_id,generation_id,
  expected_base_generation_id,expected_target_revision,result_target_revision,status,reason,created_at,completed_at)
VALUES (?,?,?,?,?,?,?,?,?,NULLIF(?,''),?,0,'pending','',?,NULL);

-- name: GetDeliveryPublicationIDByTargetDigest :one
SELECT id FROM delivery_publications WHERE target_id=? AND request_digest=?;

-- name: GetDeliveryPublication :one
SELECT id,request_digest,target_id,project_id,environment,plan_id,plan_digest,candidate_id,generation_id,
       expected_base_generation_id,expected_target_revision,result_target_revision,status,reason,created_at,completed_at
FROM delivery_publications WHERE id=?;

-- name: GetDeliveryGeneration :one
SELECT id,candidate_id,plan_id,plan_digest,target_id,project_id,environment,catalog_digest,catalog_object_key,
       physical_pool_id,serving_artifact_id,serving_artifact_digest,serving_state_id,compatibility_digest,rollback_class,status,created_at,
       activated_at,retired_at,rollback_until,rollback_external_effects_json
FROM delivery_generations WHERE id=?;

-- name: CommitDeliveryPublication :execresult
UPDATE delivery_publications SET status='committed',result_target_revision=?,completed_at=?
WHERE id=? AND status IN ('pending','indeterminate');

-- name: CommitIndeterminateDeliveryPublication :execresult
UPDATE delivery_publications SET status='committed',result_target_revision=?,completed_at=?
WHERE id=? AND status='indeterminate';

-- name: RejectDeliveryPublication :execresult
UPDATE delivery_publications SET status='rejected',reason=?,completed_at=?
WHERE id=? AND status IN ('pending','indeterminate');

-- name: MarkDeliveryPublicationIndeterminate :execresult
UPDATE delivery_publications SET status='indeterminate',completed_at=?
WHERE id=? AND status='pending';

-- name: ActivateDeliveryGeneration :execresult
UPDATE delivery_generations SET status='active',activated_at=? WHERE id=? AND status='prepared';

-- name: ActivateRetiredDeliveryGeneration :execresult
UPDATE delivery_generations SET status='active',activated_at=?,retired_at=NULL WHERE id=? AND status='retired';

-- name: RetireDeliveryGeneration :execresult
UPDATE delivery_generations SET status='retired',retired_at=? WHERE id=? AND status='active';

-- name: AdvanceDeliveryTargetRevision :execresult
UPDATE delivery_target_revisions SET active_generation_id=?,target_revision=target_revision+1,updated_at=?
WHERE target_id=? AND target_revision=? AND (active_generation_id IS ? OR active_generation_id=?);

-- name: BumpDeliveryTargetRevision :execresult
UPDATE delivery_target_revisions SET target_revision=target_revision+1,updated_at=?
WHERE target_id=?;

-- name: CreateDeliveryTargetRevisionComponent :exec
INSERT INTO delivery_target_revision_components
 (target_id,target_revision,project_id,environment,component_kind,component_id,component_digest,operation,changed_at)
VALUES (?,?,?,?,?,?,?,'cas',?);

-- name: CreateDeliveryQueryLease :exec
INSERT INTO delivery_query_leases
 (id,holder_id,candidate_id,generation_id,catalog_digest,physical_pool_id,status,expires_at,created_at,released_at)
VALUES (?,?,NULLIF(?,''),NULLIF(?,''),?,?, 'active',?,?,NULL);

-- name: GetDeliveryQueryLease :one
SELECT id,holder_id,candidate_id,generation_id,catalog_digest,physical_pool_id,status,expires_at,created_at,released_at
FROM delivery_query_leases WHERE id=?;

-- name: RenewDeliveryQueryLease :execresult
UPDATE delivery_query_leases SET expires_at=? WHERE id=? AND status='active' AND expires_at=?;

-- name: ReleaseDeliveryQueryLease :execresult
UPDATE delivery_query_leases SET status='released',released_at=? WHERE id=? AND status='active';

-- name: ExpireDeliveryQueryLease :execresult
UPDATE delivery_query_leases SET status='expired',released_at=? WHERE id=? AND status='active';

-- name: CreateDeliveryRetentionException :exec
INSERT INTO delivery_retention_exceptions
 (id,physical_pool_id,candidate_id,generation_id,catalog_digest,reason,expires_at,created_at,released_at,status)
VALUES (?, ?,NULLIF(?,''),NULLIF(?,''),?,?,?, ?,NULL,'active');

-- name: GetDeliveryRetentionException :one
SELECT id,physical_pool_id,candidate_id,generation_id,catalog_digest,reason,expires_at,created_at,released_at,status
FROM delivery_retention_exceptions WHERE id=?;

-- name: ReleaseDeliveryRetentionException :execresult
UPDATE delivery_retention_exceptions SET status='released',released_at=? WHERE id=? AND status='active';

-- name: CreateDeliveryGCCycle :exec
INSERT INTO delivery_gc_cycles
 (id,physical_pool_id,epoch,root_revision,mark_digest,status,abort_reason,created_at,completed_at,actor_id)
VALUES (?,?,?, ?,NULL,'running','',?,NULL,?);

-- name: GetDeliveryGCCycle :one
SELECT id,physical_pool_id,epoch,root_revision,mark_digest,status,abort_reason,created_at,completed_at,actor_id
FROM delivery_gc_cycles WHERE id=?;

-- name: MarkDeliveryGCCycle :execresult
UPDATE delivery_gc_cycles SET status='marked',mark_digest=? WHERE id=? AND status='running';

-- name: BeginDeliveryGCDelete :execresult
UPDATE delivery_gc_cycles SET status='deleting' WHERE id=? AND status='marked';

-- name: CompleteDeliveryGCCycle :execresult
UPDATE delivery_gc_cycles SET status='complete',completed_at=? WHERE id=? AND status='deleting';

-- name: AbortDeliveryGCCycle :execresult
UPDATE delivery_gc_cycles SET status='aborted',abort_reason=?,completed_at=?
WHERE id=? AND status NOT IN ('complete','aborted');

-- name: CreateDeliveryGCDeleteIntent :exec
INSERT INTO delivery_gc_delete_intents
 (id,cycle_id,physical_pool_id,object_key,object_digest,object_version,status,created_at,completed_at)
VALUES (?,?,?,?,?,?, 'pending',?,NULL);

-- name: GetDeliveryGCDeleteIntent :one
SELECT id,cycle_id,physical_pool_id,object_key,object_digest,object_version,status,created_at,completed_at
FROM delivery_gc_delete_intents WHERE id=?;

-- name: CompleteDeliveryGCDeleteIntent :execresult
UPDATE delivery_gc_delete_intents SET status=?,completed_at=? WHERE id=? AND status='pending';

-- name: ListDeliveryGCDeleteIntents :many
SELECT id,cycle_id,physical_pool_id,object_key,object_digest,object_version,status,created_at,completed_at
FROM delivery_gc_delete_intents WHERE cycle_id=? ORDER BY id;

-- name: UpdateQuarantineRoot :execresult
UPDATE delivery_root_registry SET candidate_id=?,generation_id=?,lease_id=?,catalog_digest=?,object_key=?,status='active',expires_at=NULL,retired_at=NULL
WHERE physical_pool_id=? AND root_kind='quarantined' AND source_id=?;

-- name: FailDeliveryCatalogSealForPool :execresult
UPDATE delivery_catalog_seals
SET status='failed',failure_code=?,closure_digest=NULL,qualification_digest=NULL,verified_at=NULL
WHERE id=? AND physical_pool_id=?;

-- name: FailDeliveryCandidateForPool :execresult
UPDATE delivery_candidates
SET status='failed',failure_code=?,qualification_digest=NULL,ready_at=NULL,retired_at=NULL
WHERE id=? AND physical_pool_id=?;

-- name: GetSQLiteChanges :one
SELECT changes() AS changes;

-- Fencing and root-registry queries.
-- name: GetDeliveryPoolFence :one
SELECT physical_pool_id,writer_epoch,gc_lease_epoch,root_revision,
       COALESCE(gc_lease_id,'') AS gc_lease_id,COALESCE(gc_holder_id,'') AS gc_holder_id,gc_expires_at
FROM delivery_pool_fences WHERE physical_pool_id=?;

-- name: GetDeliveryPoolFencePresence :one
SELECT 1 AS present FROM delivery_pool_fences WHERE physical_pool_id=?;

-- name: EnsureDeliveryPoolFence :exec
INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (?);

-- name: ExpireDeliveryGCLeasesForPool :exec
UPDATE delivery_gc_leases SET status='expired',released_at=?
WHERE physical_pool_id=? AND status='active' AND julianday(expires_at)<=julianday(?);

-- name: ClearExpiredDeliveryGCLeaseFence :exec
UPDATE delivery_pool_fences SET gc_lease_id=NULL,gc_holder_id=NULL,gc_expires_at=NULL,gc_created_at=NULL,gc_root_revision=NULL,updated_at=?
WHERE physical_pool_id=? AND gc_lease_id IS NOT NULL AND julianday(gc_expires_at)<=julianday(?);

-- name: UpdateDeliveryPoolWriterEpoch :execresult
UPDATE delivery_pool_fences SET writer_epoch=writer_epoch+1,updated_at=?
WHERE physical_pool_id=? AND (gc_lease_id IS NULL OR julianday(gc_expires_at)<=julianday(?));

-- name: GetDeliveryPoolWriterEpoch :one
SELECT writer_epoch FROM delivery_pool_fences WHERE physical_pool_id=?;

-- name: GetDeliveryWriterFenceLease :one
SELECT id,attempt_id,physical_pool_id,owner_id,epoch,status,created_at,expires_at
FROM delivery_writer_leases WHERE id=?;

-- name: ExpireDeliveryWriterLeasesExactPool :exec
UPDATE delivery_writer_leases SET status='expired',released_at=?
WHERE physical_pool_id=? AND status='active' AND julianday(expires_at)<=julianday(?);

-- name: AdvanceDeliveryPoolWriterEpoch :execresult
UPDATE delivery_pool_fences SET writer_epoch=writer_epoch+1,updated_at=?
WHERE physical_pool_id=? AND (gc_lease_id IS NULL OR julianday(gc_expires_at)<=julianday(?));

-- name: CreateDeliveryWriterFenceLease :exec
INSERT INTO delivery_writer_leases(id,attempt_id,physical_pool_id,owner_id,epoch,status,expires_at,created_at,released_at)
VALUES (?,?,?,?,?,'active',?,?,NULL);

-- name: RenewDeliveryWriterFenceLease :execresult
UPDATE delivery_writer_leases SET expires_at=?
WHERE physical_pool_id=? AND id=? AND attempt_id=? AND owner_id=? AND epoch=? AND status='active' AND julianday(expires_at)>julianday(?);

-- name: ReleaseDeliveryWriterFenceLease :execresult
UPDATE delivery_writer_leases SET status='released',released_at=?
WHERE physical_pool_id=? AND id=? AND attempt_id=? AND owner_id=? AND epoch=? AND status='active' AND julianday(expires_at)>julianday(?);

-- name: GetDeliveryGCFenceState :one
SELECT COALESCE(gc_lease_id,''),COALESCE(gc_holder_id,''),COALESCE(gc_expires_at,''),COALESCE(gc_created_at,''),COALESCE(gc_last_lease_id,''),gc_root_revision,gc_lease_epoch,root_revision
FROM delivery_pool_fences WHERE physical_pool_id=?;

-- name: GetDeliveryGCLeaseStatus :one
SELECT status FROM delivery_gc_leases WHERE id=?;

-- name: CountActiveDeliveryWriters :one
SELECT count(*) FROM delivery_writer_leases WHERE physical_pool_id=? AND status='active' AND julianday(expires_at)>julianday(?);

-- name: AdvanceDeliveryGCFence :execresult
UPDATE delivery_pool_fences SET gc_lease_epoch=gc_lease_epoch+1,gc_lease_id=?,gc_holder_id=?,gc_expires_at=?,gc_created_at=?,gc_root_revision=?,gc_last_lease_id=?,updated_at=?
WHERE physical_pool_id=? AND (gc_lease_id IS NULL OR julianday(gc_expires_at)<=julianday(?)) AND ?=0;

-- name: GetDeliveryGCFenceEpochRevision :one
SELECT gc_lease_epoch,gc_root_revision FROM delivery_pool_fences WHERE physical_pool_id=?;

-- name: CreateDeliveryGCLease :exec
INSERT INTO delivery_gc_leases(id,physical_pool_id,holder_id,epoch,created_at,expires_at,status,released_at)
VALUES (?,?,?,?,?,?,'active',NULL);

-- name: RenewDeliveryGCFence :execresult
UPDATE delivery_pool_fences SET gc_expires_at=?,updated_at=?
WHERE physical_pool_id=? AND gc_lease_id=? AND gc_holder_id=? AND gc_lease_epoch=? AND julianday(gc_expires_at)>julianday(?);

-- name: RenewDeliveryGCLeaseHistory :execresult
UPDATE delivery_gc_leases SET expires_at=?
WHERE id=? AND physical_pool_id=? AND holder_id=? AND epoch=? AND status='active';

-- name: ReleaseDeliveryGCFence :execresult
UPDATE delivery_pool_fences SET gc_lease_id=NULL,gc_holder_id=NULL,gc_expires_at=NULL,gc_created_at=NULL,gc_root_revision=NULL,updated_at=?
WHERE physical_pool_id=? AND gc_lease_id=? AND gc_holder_id=? AND gc_lease_epoch=? AND julianday(gc_expires_at)>julianday(?);

-- name: ReleaseDeliveryGCLeaseHistory :execresult
UPDATE delivery_gc_leases SET status='released',released_at=?
WHERE id=? AND physical_pool_id=? AND holder_id=? AND epoch=? AND status='active';

-- name: IsCurrentDeliveryGCFence :one
SELECT count(*) FROM delivery_pool_fences WHERE physical_pool_id=? AND gc_lease_id=? AND gc_holder_id=? AND gc_lease_epoch=? AND julianday(gc_expires_at)>julianday(?) AND root_revision=?;

-- name: IsCurrentDeliveryWriterFence :one
SELECT count(*) FROM delivery_writer_leases WHERE physical_pool_id=? AND id=? AND attempt_id=? AND owner_id=? AND epoch=? AND status='active' AND julianday(expires_at)>julianday(?);

-- name: GetDeliveryWriterFence :one
SELECT id,attempt_id,physical_pool_id,owner_id,epoch,created_at,expires_at FROM delivery_writer_leases WHERE id=?;

-- name: CountCandidateQueryLeases :one
SELECT count(*) FROM delivery_query_leases WHERE candidate_id=? AND status='active' AND julianday(expires_at)>julianday(?);

-- name: RetireDeliveryCandidate :execresult
UPDATE delivery_candidates SET status='retired',retired_at=? WHERE id=? AND status='ready';

-- name: CountGenerationQueryLeases :one
SELECT count(*) FROM delivery_query_leases WHERE generation_id=? AND status='active' AND julianday(expires_at)>julianday(?);

-- name: RetireDeliveryGenerationActive :execresult
UPDATE delivery_generations SET status='retired',retired_at=? WHERE id=? AND status='active';

-- name: CreateDeliveryRootRegistry :execresult
INSERT INTO delivery_root_registry(physical_pool_id,root_kind,source_id,candidate_id,generation_id,lease_id,catalog_digest,object_key,status,created_at,expires_at,retired_at)
VALUES (?,?,?,?,?,?,?,?,'active',?,?,NULL);

-- name: GetDeliveryRootRegistry :one
SELECT physical_pool_id,root_kind,source_id,candidate_id,generation_id,lease_id,catalog_digest,object_key,status,created_at,expires_at
FROM delivery_root_registry WHERE physical_pool_id=? AND root_kind=? AND source_id=?;

-- name: CountActiveQueryLeasesForCatalog :one
SELECT count(*) FROM delivery_query_leases WHERE physical_pool_id=? AND catalog_digest=? AND status='active' AND julianday(expires_at)>julianday(?);

-- name: RetireDeliveryRootRegistry :execresult
UPDATE delivery_root_registry SET status='retired',retired_at=? WHERE physical_pool_id=? AND root_kind=? AND source_id=? AND status='active';

-- name: GetCandidateCatalogBinding :one
SELECT catalog_digest,physical_pool_id,status FROM delivery_candidates WHERE id=?;

-- name: GetGenerationCatalogBinding :one
SELECT catalog_digest,physical_pool_id,status FROM delivery_generations WHERE id=?;

-- name: CountQuarantinedDeliveryRoots :one
SELECT count(*) FROM delivery_root_registry WHERE physical_pool_id=? AND catalog_digest=? AND root_kind='quarantined' AND status='active';

-- name: GetDeliveryPoolFenceRootLease :one
SELECT physical_pool_id,writer_epoch,gc_lease_epoch,root_revision,COALESCE(gc_lease_id,''),COALESCE(gc_holder_id,''),gc_expires_at
FROM delivery_pool_fences WHERE physical_pool_id=?;

-- name: GetDeliveryRootRevision :one
SELECT root_revision FROM delivery_pool_fences WHERE physical_pool_id=?;

-- name: EnumerateDeliveryRoots :many
SELECT root_kind,source_id,candidate_id,generation_id,lease_id,catalog_digest,object_key,status,created_at,expires_at
FROM delivery_root_registry WHERE physical_pool_id=? AND status='active'
ORDER BY root_kind,source_id;

-- name: EnumerateDeliveryRootsExpanded :many
SELECT physical_pool_id,'build' AS root_kind,id AS source_id,'' AS candidate_id,'' AS generation_id,'' AS lease_id,catalog_digest,object_key,'active' AS status,created_at,NULL AS expires_at
FROM delivery_catalog_seals WHERE delivery_catalog_seals.physical_pool_id=? AND status IN ('preparing','uploaded','verified') AND NOT EXISTS (SELECT 1 FROM delivery_candidates WHERE seal_id=delivery_catalog_seals.id)
AND EXISTS (SELECT 1 FROM delivery_build_attempts a JOIN delivery_writer_leases l ON l.id=a.writer_lease_id AND l.attempt_id=a.id AND l.physical_pool_id=a.physical_pool_id WHERE a.id=delivery_catalog_seals.attempt_id AND a.physical_pool_id=delivery_catalog_seals.physical_pool_id AND l.status='active' AND julianday(l.expires_at)>julianday(?))
UNION ALL SELECT c.physical_pool_id,'candidate',c.id,c.id,'','',c.catalog_digest,c.catalog_object_key,'active',c.created_at,NULL FROM delivery_candidates c WHERE c.physical_pool_id=? AND c.status IN ('preparing','ready')
AND NOT EXISTS (SELECT 1 FROM delivery_publications p WHERE p.candidate_id=c.id AND p.status='committed')
UNION ALL SELECT g.physical_pool_id,'published',g.id,'',g.id,'',g.catalog_digest,g.catalog_object_key,'active',g.created_at,NULL FROM delivery_generations g WHERE g.physical_pool_id=? AND g.status IN ('prepared','active')
UNION ALL SELECT g.physical_pool_id,'rollback',g.id,'',g.id,'',g.catalog_digest,g.catalog_object_key,'active',g.created_at,g.rollback_until FROM delivery_generations g WHERE g.physical_pool_id=? AND g.status='retired' AND g.rollback_until IS NOT NULL AND julianday(g.rollback_until)>julianday(?)
UNION ALL SELECT c.physical_pool_id,'published',p.id,p.candidate_id,p.generation_id,'',g.catalog_digest,g.catalog_object_key,'active',p.created_at,NULL FROM delivery_publications p JOIN delivery_candidates c ON c.id=p.candidate_id JOIN delivery_generations g ON g.id=p.generation_id WHERE c.physical_pool_id=? AND p.status IN ('pending','indeterminate')
UNION ALL SELECT r.physical_pool_id,'retained',r.id,COALESCE(r.candidate_id,''),COALESCE(r.generation_id,''),'',r.catalog_digest,CASE WHEN r.candidate_id IS NOT NULL THEN (SELECT catalog_object_key FROM delivery_candidates WHERE id=r.candidate_id) ELSE (SELECT catalog_object_key FROM delivery_generations WHERE id=r.generation_id) END,'active',r.created_at,r.expires_at FROM delivery_retention_exceptions r WHERE r.physical_pool_id=? AND r.status='active' AND julianday(r.expires_at)>julianday(?)
UNION ALL SELECT l.physical_pool_id,'lease',l.id,COALESCE(l.candidate_id,''),COALESCE(l.generation_id,''),l.id,l.catalog_digest,CASE WHEN l.candidate_id IS NOT NULL THEN (SELECT catalog_object_key FROM delivery_candidates WHERE id=l.candidate_id) ELSE (SELECT catalog_object_key FROM delivery_generations WHERE id=l.generation_id) END,'active',l.created_at,l.expires_at FROM delivery_query_leases l WHERE l.physical_pool_id=? AND l.status IN ('active','expired') AND julianday(l.expires_at)>julianday(?)
UNION ALL SELECT r.physical_pool_id,r.root_kind,r.source_id,COALESCE(r.candidate_id,''),COALESCE(r.generation_id,''),COALESCE(r.lease_id,''),r.catalog_digest,r.object_key,r.status,r.created_at,r.expires_at FROM delivery_root_registry r WHERE r.physical_pool_id=? AND r.status='active' AND (r.expires_at IS NULL OR julianday(r.expires_at)>julianday(?));

-- Append-only delivery lifecycle evidence. The unique request/object key makes
-- a crash-replayed command resolve to the original event without rewriting it.
-- name: AppendDeliveryEvent :execresult
INSERT INTO delivery_events
 (id, target_id, project_id, environment, actor_id, event_kind, object_kind,
  object_id, request_digest, plan_digest, result_digest, outcome, details_json, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?)
ON CONFLICT(target_id, request_digest, event_kind, object_kind, object_id) DO NOTHING;

-- name: GetDeliveryEventByRequest :one
SELECT id, target_id, project_id, environment, actor_id, event_kind, object_kind,
       object_id, request_digest, plan_digest, result_digest, outcome, details_json, created_at
FROM delivery_events
WHERE target_id = ? AND request_digest = ? AND event_kind = ? AND object_kind = ? AND object_id = ?;

-- name: ListDeliveryEvents :many
SELECT id, target_id, project_id, environment, actor_id, event_kind, object_kind,
       object_id, request_digest, plan_digest, result_digest, outcome, details_json, created_at
FROM delivery_events
WHERE target_id = ? ORDER BY created_at ASC, id ASC;

-- name: ListDeliveryEventsByObject :many
SELECT id, target_id, project_id, environment, actor_id, event_kind, object_kind,
       object_id, request_digest, plan_digest, result_digest, outcome, details_json, created_at
FROM delivery_events
WHERE target_id = ? AND object_kind = ? AND object_id = ?
ORDER BY created_at ASC, id ASC;

-- name: ListDeliveryGenerationIDsByServingState :many
SELECT id
FROM delivery_generations
WHERE target_id = ? AND project_id = ? AND environment = ? AND serving_state_id = ?;
