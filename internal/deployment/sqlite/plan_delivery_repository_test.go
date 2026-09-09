package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/project/graph"
	identityledger "github.com/flidai/leapview/internal/project/identityledger"
	"github.com/flidai/leapview/internal/release"
)

func repoDeliveryDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

func repoDeliveryPlan(t *testing.T, now time.Time) deployment.DeliveryPlan {
	t.Helper()
	d := repoDeliveryDigest
	plan, err := deployment.NewDeliveryPlan(deployment.DeliveryPlan{
		ID: "plan-repo-1", ActorID: "author-repo-1", TargetID: "target-repo-1", ProjectID: graph.ResourceID("project-repo-1"), Environment: "prod",
		Operation: deployment.DeliveryOperationCodeChange, SourceDigest: d('a'), BaseTargetRevision: 0,
		Execution:  deployment.DeliveryExecutionInputs{SourceArtifactDigest: d('a'), CompilerDigest: d('b'), ExecutableDigest: d('c'), DependencyDigest: d('d'), ConfigDigest: d('e'), BindingDigest: d('f'), RuntimeDigest: d('0'), CapabilityDigest: d('1')},
		Provenance: deployment.DeliveryProvenance{Builder: "test"},
		Governance: deployment.DeliveryGovernance{PolicyDigest: d('2'), AuthorizationDigest: d('3'), QualificationDigest: d('4'), ExpiresAt: now.Add(time.Hour), ObservedInputsAllowed: true},
		Evidence: deployment.DeliveryPlanEvidence{
			ImpactStatement: "direct model change with downstream impact", PhysicalWorkStatement: "materialize affected relations", ReuseStatement: "reuse unchanged relations",
			Qualification: deployment.DeliveryQualificationEvidence{Policy: "protected", Steps: []deployment.DeliveryQualificationStep{{ID: "contracts", Kind: "contract", Description: "run graph contracts", Required: true, Blocking: true}}},
			StalePolicy:   deployment.DeliveryStalePolicy{Mode: "reject"}, Rollback: deployment.DeliveryRollbackEvidence{Class: deployment.DeliveryRollbackSafe},
		},
		CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func openDeliveryRepository(t *testing.T) (*platform.Store, *Repository) {
	t.Helper()
	store, err := platform.Open(context.Background(), filepath.Join(t.TempDir(), "delivery.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, NewRepositoryWithHooks(store.SQLDB(), ActivationHooks{})
}

func insertDeliveryPool(t *testing.T, store *platform.Store, id string) {
	t.Helper()
	_, err := store.SQLDB().ExecContext(context.Background(), `INSERT INTO physical_pools (id,identity_digest,storage_location,storage_namespace,storage_implementation,object_naming_contract,isolation_boundary,retention_authority,retention_policy_json) VALUES (?,?, 's3://delivery', 'repo-test', 's3', 'names-v1', 'repo-test', 'gc', '{}')`, id, id)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRefreshPublicationFenceRejectsSupersededRunInCommitTransaction(t *testing.T) {
	store, _ := openDeliveryRepository(t)
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO serving_states (id, project_id, environment, status) VALUES ('generation-refresh', 'project-repo-1', 'prod', 'validated');
INSERT INTO refresh_jobs (
  id, project_id, generation_id, semantic_model_id, pipeline_id, principal_id,
  group_ids_json, estimated_memory_bytes, kind, status, lease_owner, lease_revision, lease_expires_at
) VALUES (
  'job-refresh', 'project-repo-1', 'generation-refresh', 'semantic-sales', 'pipeline-sales',
  'worker-refresh', '[]', 1, 'refresh_pipeline', 'running', 'worker-refresh', 4, datetime('now', '+5 minutes')
);
INSERT INTO refresh_job_runs (
  id, job_id, project_id, environment, target_type, target_id, target_revision,
  trigger_type, invocation_source, status, created_sequence
) VALUES (
  'run-refresh', 'job-refresh', 'project-repo-1', 'prod', 'refresh_pipeline',
  'pipeline-sales', 7, 'schedule', 'schedule', 'prepared', 1
);`); err != nil {
		t.Fatal(err)
	}
	publication := deployment.DeliveryPublication{
		ProjectID: "project-repo-1", Environment: "prod", RefreshRunID: "run-refresh",
		RefreshLeaseOwner: "worker-refresh", RefreshLeaseRevision: 4, RefreshTargetRevision: 7,
	}
	tx, err := store.SQLDB().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := refreshPublicationFenceActive(t.Context(), tx, publication); err != nil {
		t.Fatalf("active refresh publication fence: %v", err)
	}
	_ = tx.Rollback()
	if _, err := store.SQLDB().ExecContext(t.Context(), `
UPDATE refresh_job_runs SET status='superseded' WHERE id='run-refresh';
UPDATE refresh_jobs SET status='superseded', lease_owner='', lease_expires_at=NULL WHERE id='job-refresh';`); err != nil {
		t.Fatal(err)
	}
	tx, err = store.SQLDB().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := refreshPublicationFenceActive(t.Context(), tx, publication); !errors.Is(err, deployment.ErrDeliveryStale) {
		t.Fatalf("superseded refresh publication fence error = %v, want ErrDeliveryStale", err)
	}
}

func TestDeliveryRepositoryAcceptsFreshBuildAgainstActivePlanBase(t *testing.T) {
	store, repo := openDeliveryRepository(t)
	now := time.Now().UTC().Truncate(time.Second)
	plan := repoDeliveryPlan(t, now)
	plan.BaseGenerationID, plan.BaseTargetRevision = "generation-active", 4
	plan, err := deployment.NewDeliveryPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO delivery_target_revisions (target_id,project_id,environment,target_revision,active_generation_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?) ON CONFLICT(target_id) DO UPDATE SET active_generation_id=excluded.active_generation_id,target_revision=excluded.target_revision,updated_at=excluded.updated_at`, plan.TargetID, plan.ProjectID.String(), plan.Environment, plan.BaseTargetRevision, plan.BaseGenerationID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreatePlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	pool := repoDeliveryDigest('9')
	insertDeliveryPool(t, store, pool)
	lease := deployment.DeliveryWriterLease{ID: "writer-fresh-active", AttemptID: "attempt-fresh-active", PhysicalPoolID: pool, OwnerID: "builder", Epoch: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	attempt := deployment.DeliveryBuildAttempt{ID: lease.AttemptID, PlanID: plan.ID, PlanDigest: plan.Digest, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, PhysicalPoolID: pool, WriterLeaseID: lease.ID, CreatedAt: now}
	_, persisted, err := repo.CreateWriterLeaseAndBuildAttempt(t.Context(), lease, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.BaseGenerationID != "" || persisted.BaseCatalogDigest != "" || persisted.BasePhysicalPoolID != "" {
		t.Fatalf("fresh persisted attempt retained base identity: %#v", persisted)
	}
}

func TestDeliveryRepositoryRoundTripsPipelinePlanIdentity(t *testing.T) {
	store, repo := openDeliveryRepository(t)
	now := time.Now().UTC().Truncate(time.Second)
	plan := repoDeliveryPlan(t, now)
	plan.Operation = deployment.DeliveryOperationRestatement
	plan.BaseGenerationID = "generation-active"
	pipelinePlan, err := deployment.NewPipelinePlan(deployment.PipelinePlan{
		ID: "pipeline-plan-repo", PipelineID: "pipeline:sales", ProjectID: plan.ProjectID.String(), Environment: plan.Environment,
		SemanticModelID: "semantic-model:sales", ServingGenerationID: plan.BaseGenerationID, ArtifactDigest: plan.SourceDigest,
		SelectionDigest: repoDeliveryDigest('8'), MaterializationScope: []string{"sales_orders"}, InvocationSource: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	plan.Restore = &deployment.RestoreIntent{AuthoredIDs: []graph.ResourceID{"orders", "customers"}, Reason: "approved recovery"}
	plan.PipelinePlan = &pipelinePlan
	plan, err = deployment.NewDeliveryPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO delivery_target_revisions (target_id,project_id,environment,target_revision,active_generation_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`, plan.TargetID, plan.ProjectID.String(), plan.Environment, plan.BaseTargetRevision, plan.BaseGenerationID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreatePlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	var evidenceJSON string
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT evidence_json FROM delivery_plans WHERE id = ?`, plan.ID).Scan(&evidenceJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(evidenceJSON, `"restore"`) || !strings.Contains(evidenceJSON, "approved recovery") {
		t.Fatalf("persisted plan evidence omitted restore intent: %s", evidenceJSON)
	}
	roundTrip, err := repo.PlanByID(t.Context(), plan.ID)
	if err != nil {
		t.Fatalf("read pipeline delivery plan: %v", err)
	}
	if roundTrip.PipelinePlan == nil || roundTrip.PipelinePlan.Digest != pipelinePlan.Digest || roundTrip.Digest != plan.Digest {
		t.Fatalf("pipeline plan round trip = %#v, want digest %s", roundTrip.PipelinePlan, pipelinePlan.Digest)
	}
	if roundTrip.Restore == nil || !reflect.DeepEqual(roundTrip.Restore.AuthoredIDs, []graph.ResourceID{"customers", "orders"}) || roundTrip.Restore.Reason != "approved recovery" {
		t.Fatalf("restore plan round trip = %#v", roundTrip.Restore)
	}
}

func TestDeliveryRepositoryPlanBuildSealCandidatePublication(t *testing.T) {
	store, repo := openDeliveryRepository(t)
	now := time.Now().UTC().Truncate(time.Second)
	plan := repoDeliveryPlan(t, now)
	plan.Evidence.ContractActivations = []identityledger.PolicyActivationReference{{
		Version:      identityledger.PolicyActivationReferenceVersion,
		Publication:  identityledger.PolicyPublicationIdentity{InstanceID: "instance-repo-1", AuthoredID: "semantic:orders", ResourceKind: graph.KindSemanticModel, Version: "1.0.0", VersionBaseline: "1.0.0", ProjectionProfile: "leapview.contract/v1", Digest: repoDeliveryDigest('5')},
		BaselineKind: identityledger.PolicyBaselineGenesis, LifecycleSequence: 1, ActiveBundleID: "generation-base", GraphDigest: repoDeliveryDigest('6'),
		PolicyEvidenceVersion: identityledger.PolicyEvidenceVersion, PolicyEvidenceDigest: repoDeliveryDigest('7'), ApprovalState: identityledger.PolicyApprovalRequired,
	}}
	plan.Governance.RequiresApproval = true
	plan.EvidenceDigest, plan.GovernanceDigest, plan.Digest = "", "", ""
	plan, err := deployment.NewDeliveryPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := repo.CreatePlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := repo.CreatePlan(t.Context(), plan)
	if err != nil || replayed.Digest != persisted.Digest {
		t.Fatalf("plan replay=%#v err=%v", replayed, err)
	}
	roundTrip, err := repo.DeliveryPlanByID(t.Context(), plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := roundTrip.Validate(); err != nil {
		t.Fatalf("round-trip plan validation: %v", err)
	}
	if roundTrip.ActorID != plan.ActorID {
		t.Fatalf("round-trip plan actor = %q, want %q", roundTrip.ActorID, plan.ActorID)
	}
	conflict := plan
	conflict.Provenance.SourceRevision = "different-source"
	conflict.ProvenanceDigest = ""
	conflict.Digest = ""
	if _, err := deployment.NewDeliveryPlan(conflict); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreatePlan(t.Context(), conflict); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("same-id changed canonical plan err=%v", err)
	}
	pool := repoDeliveryDigest('9')
	insertDeliveryPool(t, store, pool)
	lease := deployment.DeliveryWriterLease{ID: "writer-repo-1", AttemptID: "attempt-repo-1", PhysicalPoolID: pool, OwnerID: "builder", Epoch: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	attempt := deployment.DeliveryBuildAttempt{ID: "attempt-repo-1", PlanID: plan.ID, IdempotencyKey: "build-op-repo-1", PlanDigest: plan.Digest, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, PhysicalPoolID: pool, WriterLeaseID: lease.ID, CreatedAt: now}
	if _, _, err = repo.CreateWriterLeaseAndBuildAttempt(t.Context(), lease, attempt); err != nil {
		t.Fatal(err)
	}
	var buildActor string
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT actor_id FROM delivery_events WHERE event_kind='build_started' AND object_id=?`, attempt.ID).Scan(&buildActor); err != nil {
		t.Fatal(err)
	}
	if buildActor != lease.OwnerID {
		t.Fatalf("build audit actor=%q, want authenticated builder %q", buildActor, lease.OwnerID)
	}
	retryLease := lease
	retryLease.CreatedAt = now.Add(time.Minute)
	retryLease.ExpiresAt = retryLease.CreatedAt.Add(time.Hour)
	retryAttempt := attempt
	retryAttempt.CreatedAt = retryLease.CreatedAt
	if _, replay, err := repo.CreateWriterLeaseAndBuildAttempt(t.Context(), retryLease, retryAttempt); err != nil || replay.IdempotencyKey != attempt.IdempotencyKey {
		t.Fatalf("same build idempotency retry=%#v err=%v", replay, err)
	}
	conflictingAttempt := attempt
	conflictingAttempt.IdempotencyKey = "build-op-repo-conflict"
	if _, _, err := repo.CreateWriterLeaseAndBuildAttempt(t.Context(), lease, conflictingAttempt); !errors.Is(err, deployment.ErrDeliveryIdempotencyDrift) {
		t.Fatalf("conflicting build idempotency err=%v, want ErrDeliveryIdempotencyDrift", err)
	}
	if _, err = repo.TransitionBuildAttempt(t.Context(), attempt.ID, 1, deployment.DeliveryBuildNormalizing, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.TransitionBuildAttempt(t.Context(), attempt.ID, 2, deployment.DeliveryBuildValidating, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.TransitionBuildAttempt(t.Context(), attempt.ID, 3, deployment.DeliveryBuildSealing, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	seal, err := repo.PrepareCatalogSeal(t.Context(), deployment.CatalogSeal{ID: "seal-repo-1", AttemptID: attempt.ID, PlanID: plan.ID, PlanDigest: plan.Digest, ExecutionDigest: plan.ExecutionDigest, PhysicalPoolID: pool, CatalogDigest: repoDeliveryDigest('c'), CompatibilityDigest: repoDeliveryDigest('d'), ServingArtifactID: "artifact-repo-1", ServingArtifactDigest: repoDeliveryDigest('7'), ServingStateID: "state-repo-1", ObjectKey: "catalogs/repo-1", ObjectSize: 1, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateCandidate(t.Context(), deployment.DeliveryCandidate{ID: "candidate-before-seal", PlanID: plan.ID, PlanDigest: plan.Digest, TargetID: plan.TargetID, ProjectID: plan.ProjectID, Environment: plan.Environment, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, SealID: seal.ID, CatalogDigest: seal.CatalogDigest, CompatibilityDigest: seal.CompatibilityDigest, CatalogObjectKey: seal.ObjectKey, PhysicalPoolID: pool, ServingArtifactID: seal.ServingArtifactID, ServingArtifactDigest: seal.ServingArtifactDigest, ServingStateID: "state-repo-1", CreatedAt: now, ResolvedInputs: sqliteResolvedInputs(t, plan, "candidate-before-seal")}); err == nil {
		t.Fatal("candidate creation before verified seal unexpectedly succeeded")
	}
	if seal, err = repo.MarkCatalogSealUploaded(t.Context(), seal.ID); err != nil {
		t.Fatal(err)
	}
	if retry, err := repo.MarkCatalogSealUploaded(t.Context(), seal.ID); err != nil || retry.Status != deployment.CatalogSealUploaded {
		t.Fatalf("seal upload retry=%#v err=%v", retry, err)
	}
	if seal, err = repo.VerifyCatalogSeal(t.Context(), seal.ID, repoDeliveryDigest('e'), repoDeliveryDigest('f'), now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if retry, err := repo.VerifyCatalogSeal(t.Context(), seal.ID, repoDeliveryDigest('e'), repoDeliveryDigest('f'), now.Add(5*time.Minute)); err != nil || retry.Status != deployment.CatalogSealVerified {
		t.Fatalf("seal verify retry=%#v err=%v", retry, err)
	}
	if _, err := repo.VerifyCatalogSeal(t.Context(), seal.ID, repoDeliveryDigest('e'), repoDeliveryDigest('0'), now.Add(5*time.Minute)); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("mismatching seal verification err=%v", err)
	}
	emptyServingState := deployment.DeliveryCandidate{ID: "candidate-empty-serving-state", PlanID: plan.ID, PlanDigest: plan.Digest, TargetID: plan.TargetID, ProjectID: plan.ProjectID, Environment: plan.Environment, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, BaseTargetRevision: 0, SealID: seal.ID, CatalogDigest: seal.CatalogDigest, CompatibilityDigest: seal.CompatibilityDigest, CatalogObjectKey: seal.ObjectKey, PhysicalPoolID: pool, ServingArtifactID: seal.ServingArtifactID, ServingArtifactDigest: seal.ServingArtifactDigest, CreatedAt: now, ResolvedInputs: sqliteResolvedInputs(t, plan, "candidate-empty-serving-state")}
	if _, err := repo.CreateCandidateReady(t.Context(), emptyServingState, seal, now.Add(5*time.Minute)); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("candidate without persisted serving state err=%v, want ErrDeliveryConflict", err)
	}
	candidate, err := repo.CreateCandidateReady(t.Context(), deployment.DeliveryCandidate{ID: "candidate-repo-1", PlanID: plan.ID, PlanDigest: plan.Digest, TargetID: plan.TargetID, ProjectID: plan.ProjectID, Environment: plan.Environment, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, BaseTargetRevision: 0, SealID: seal.ID, CatalogDigest: seal.CatalogDigest, CompatibilityDigest: seal.CompatibilityDigest, CatalogObjectKey: seal.ObjectKey, PhysicalPoolID: pool, ServingArtifactID: seal.ServingArtifactID, ServingArtifactDigest: seal.ServingArtifactDigest, ServingStateID: "state-repo-1", CreatedAt: now, ResolvedInputs: sqliteResolvedInputs(t, plan, "candidate-repo-1")}, seal, now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Status != deployment.DeliveryCandidateReady {
		t.Fatalf("candidate status=%s", candidate.Status)
	}
	publication, err := repo.CreatePublication(t.Context(), deployment.DeliveryPublication{ID: "publication-repo-1", RequestDigest: repoDeliveryDigest('1'), TargetID: plan.TargetID, ProjectID: plan.ProjectID, Environment: plan.Environment, PlanID: plan.ID, PlanDigest: plan.Digest, CandidateID: candidate.ID, GenerationID: "generation-repo-1", ExpectedTargetRevision: 0, CreatedAt: now.Add(5 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `
		INSERT INTO principals (id, email, display_name) VALUES
			('publisher-repo-1', 'publisher-repo-1@example.test', 'Publisher'),
			('reviewer-repo-1', 'reviewer-repo-1@example.test', 'Reviewer')`); err != nil {
		t.Fatal(err)
	}
	approval := deployment.Approval{
		ID: "approval-publication-repo-1", ProjectID: plan.ProjectID.String(), DeploymentID: publication.ID,
		Environment: plan.Environment, RequestDigest: publication.RequestDigest, ReleaseID: candidate.ServingArtifactID,
		PlanDigest: plan.Digest, EvidenceDigest: plan.EvidenceDigest,
		Status: deployment.ApprovalPending, RequestedBy: "publisher-repo-1",
		RequestCredentialClass: deployment.CredentialClassWorkload, RequestCredentialID: "credential-repo-1",
		RequestedAt: now.Add(5 * time.Minute), ExpiresAt: now.Add(6 * time.Minute), Revision: 1,
	}
	unboundApproval := approval
	unboundApproval.ID = "approval-publication-unbound"
	unboundApproval.PlanDigest, unboundApproval.EvidenceDigest = "", ""
	if _, err := repo.CreateApproval(t.Context(), unboundApproval); !errors.Is(err, deployment.ErrApprovalScope) {
		t.Fatalf("unbound canonical approval error=%v, want ErrApprovalScope", err)
	}
	// Simulate a canonical approval requested by the preceding binary, whose
	// immutable event predates explicit plan/evidence fields. It may expire and
	// be replaced, but the repository never reconstructs those missing fields
	// from the current plan.
	historicalApproval := approval
	historicalApproval.PlanDigest, historicalApproval.EvidenceDigest = "", ""
	if _, err := store.SQLDB().ExecContext(t.Context(), `
		INSERT INTO deployment_approvals
		(id,project_id,deployment_id,environment,request_digest,release_id,status,requested_by,request_credential_class,request_credential_id,requested_at,expires_at,revision)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, historicalApproval.ID, historicalApproval.ProjectID, historicalApproval.DeploymentID,
		historicalApproval.Environment, historicalApproval.RequestDigest, historicalApproval.ReleaseID, string(historicalApproval.Status),
		historicalApproval.RequestedBy, string(historicalApproval.RequestCredentialClass), historicalApproval.RequestCredentialID,
		formatApprovalTime(historicalApproval.RequestedAt), formatApprovalTime(historicalApproval.ExpiresAt), historicalApproval.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AppendDeliveryEvent(t.Context(), deployment.DeliveryEvent{
		ID:       deployment.DeliveryEventID(plan.TargetID, historicalApproval.RequestDigest, "approval_requested", "approval", historicalApproval.ID),
		TargetID: plan.TargetID, ProjectID: historicalApproval.ProjectID, Environment: historicalApproval.Environment,
		ActorID: historicalApproval.RequestedBy, EventKind: "approval_requested", ObjectKind: "approval", ObjectID: historicalApproval.ID,
		RequestDigest: historicalApproval.RequestDigest, Outcome: "accepted", Details: map[string]any{"status": string(historicalApproval.Status)}, CreatedAt: historicalApproval.RequestedAt,
	}); err != nil {
		t.Fatal(err)
	}
	persistedApproval, err := repo.ApprovalByDeployment(t.Context(), publication.ID)
	if err != nil || persistedApproval != historicalApproval {
		t.Fatalf("historical canonical approval round trip = %#v, %v", persistedApproval, err)
	}
	legacyNow := persistedApproval.ExpiresAt
	legacyService, err := deployment.NewApprovalService(repo, deployment.ApprovalServiceConfig{
		Now: func() time.Time { return legacyNow }, Lifetime: 4 * time.Hour,
		NewID: func() (string, error) { return "approval-publication-rebound", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	expiredLegacy, err := legacyService.Current(t.Context(), publication.ID)
	if err != nil || expiredLegacy.Status != deployment.ApprovalExpired || expiredLegacy.PlanDigest != "" || expiredLegacy.EvidenceDigest != "" {
		t.Fatalf("historical approval expiry = %#v, %v", expiredLegacy, err)
	}
	reboundApproval, err := legacyService.Request(t.Context(), deployment.ApprovalRequest{
		ProjectID: plan.ProjectID.String(), DeploymentID: publication.ID, Environment: plan.Environment,
		RequestDigest: publication.RequestDigest, ReleaseID: candidate.ServingArtifactID,
		PlanDigest: plan.Digest, EvidenceDigest: plan.EvidenceDigest,
		RequestedBy: deployment.ApprovalActor{PrincipalID: "publisher-repo-1", CredentialClass: deployment.CredentialClassWorkload, CredentialID: "credential-rebound", CredentialExpiresAt: legacyNow.Add(time.Hour)},
	})
	if err != nil {
		t.Fatalf("replace expired historical approval: %v", err)
	}
	var requestedPlanDigest, requestedEvidenceDigest string
	if err := store.SQLDB().QueryRowContext(t.Context(), `
		SELECT plan_digest, result_digest FROM delivery_events
		WHERE event_kind='approval_requested' AND object_id=?`, reboundApproval.ID).Scan(&requestedPlanDigest, &requestedEvidenceDigest); err != nil {
		t.Fatal(err)
	}
	if requestedPlanDigest != plan.Digest || requestedEvidenceDigest != plan.EvidenceDigest {
		t.Fatalf("rebound approval evidence = %s/%s, want %s/%s", requestedPlanDigest, requestedEvidenceDigest, plan.Digest, plan.EvidenceDigest)
	}
	approval = reboundApproval
	persistedApproval = reboundApproval
	// An uncommitted approval transition cannot be observed by activation. If
	// the competing decision rolls back, the publication remains pending and
	// activation fails closed instead of using dirty approval state.
	approvalTx, err := store.SQLDB().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := approvalTx.ExecContext(t.Context(), `
		UPDATE deployment_approvals
		SET status='approved', approved_by='reviewer-repo-1',
		    approval_credential_class='human', approval_credential_id='approval-race',
		    approval_credential_expires_at=?, approved_at=?, revision=2
		WHERE id=? AND revision=1`, now.Add(4*time.Hour).Format(time.RFC3339Nano), now.Add(6*time.Minute+time.Second).Format(time.RFC3339Nano), approval.ID); err != nil {
		_ = approvalTx.Rollback()
		t.Fatal(err)
	}
	approvalRaceStarted := make(chan struct{})
	approvalRaceDone := make(chan error, 1)
	go func() {
		close(approvalRaceStarted)
		_, activationErr := repo.CommitPublication(t.Context(), publication.ID, now.Add(7*time.Minute))
		approvalRaceDone <- activationErr
	}()
	<-approvalRaceStarted
	if err := approvalTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-approvalRaceDone; !errors.Is(err, deployment.ErrApprovalRequired) {
		t.Fatalf("approval/activation rollback race error=%v, want ErrApprovalRequired", err)
	}
	approved := persistedApproval
	approved.Status = deployment.ApprovalApproved
	approved.ApprovedBy = "reviewer-repo-1"
	approved.ApprovalCredentialClass = deployment.CredentialClassHuman
	approved.ApprovalCredentialID = "reviewer-credential-repo-1"
	approved.ApprovalCredentialExpiresAt = now.Add(4 * time.Hour)
	approved.ApprovedAt = now.Add(6*time.Minute + time.Second)
	approved.Revision = 2
	if _, err := repo.SaveApproval(t.Context(), approved, 1); err != nil {
		t.Fatalf("approve canonical publication: %v", err)
	}
	var grantedPlanDigest, grantedEvidenceDigest string
	if err := store.SQLDB().QueryRowContext(t.Context(), `
		SELECT plan_digest, result_digest FROM delivery_events
		WHERE event_kind='approval_granted' AND object_id=?`, approval.ID).Scan(&grantedPlanDigest, &grantedEvidenceDigest); err != nil {
		t.Fatal(err)
	}
	if grantedPlanDigest != plan.Digest || grantedEvidenceDigest != plan.EvidenceDigest {
		t.Fatalf("granted approval evidence = %s/%s, want %s/%s", grantedPlanDigest, grantedEvidenceDigest, plan.Digest, plan.EvidenceDigest)
	}
	// The exact evidence event survives independently of mutable projections.
	// A publication digest mutation therefore cannot reuse this approval.
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE delivery_publications SET request_digest=? WHERE id=?`, repoDeliveryDigest('2'), publication.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CommitPublication(t.Context(), publication.ID, now.Add(7*time.Minute)); !errors.Is(err, deployment.ErrApprovalScope) {
		t.Fatalf("mutated publication approval error=%v, want ErrApprovalScope", err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE delivery_publications SET request_digest=? WHERE id=?`, publication.RequestDigest, publication.ID); err != nil {
		t.Fatal(err)
	}
	// The plan validator and the approval event both bind policy/graph evidence.
	// Drift in the persisted evidence digest fails before activation commits.
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE delivery_plans SET evidence_digest=? WHERE id=?`, repoDeliveryDigest('3'), plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CommitPublication(t.Context(), publication.ID, now.Add(7*time.Minute)); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("policy evidence drift error=%v, want ErrDeliveryConflict", err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE delivery_plans SET evidence_digest=? WHERE id=?`, plan.EvidenceDigest, plan.ID); err != nil {
		t.Fatal(err)
	}
	// An approval for artifact A is not reusable after the candidate is changed
	// to artifact B, even if the publication identity itself is retained.
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE delivery_candidates SET serving_artifact_id='artifact-repo-2' WHERE id=?`, candidate.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CommitPublication(t.Context(), publication.ID, now.Add(7*time.Minute)); !errors.Is(err, deployment.ErrApprovalScope) {
		t.Fatalf("replacement artifact approval error=%v, want ErrApprovalScope", err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE delivery_candidates SET serving_artifact_id=? WHERE id=?`, candidate.ServingArtifactID, candidate.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CommitPublication(t.Context(), publication.ID, now.Add(2*time.Hour)); !errors.Is(err, deployment.ErrDeliveryPlanExpired) {
		t.Fatalf("expired pending publication err=%v, want ErrDeliveryPlanExpired", err)
	}
	var activeBefore string
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT COALESCE(active_generation_id,'') FROM delivery_target_revisions WHERE target_id=?`, plan.TargetID).Scan(&activeBefore); err != nil {
		t.Fatal(err)
	}
	if activeBefore != "" {
		t.Fatalf("expired publication changed active pointer to %q", activeBefore)
	}
	repo.WithDeliveryClock(func() time.Time { return now.Add(5 * time.Hour) })
	if _, err := repo.CommitPublication(t.Context(), publication.ID, now.Add(7*time.Minute)); !errors.Is(err, deployment.ErrApprovalExpired) {
		t.Fatalf("commit-time stale approval error=%v, want ErrApprovalExpired", err)
	}
	repo.WithDeliveryClock(func() time.Time { return now.Add(7 * time.Minute) })
	// Hold the approval revocation transaction open while activation starts.
	// Once revocation wins the SQLite write order, activation must re-read the
	// revoked decision in its commit transaction and fail closed.
	revocationTx, err := store.SQLDB().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := revocationTx.ExecContext(t.Context(), `
		UPDATE deployment_approvals
		SET status='revoked', revoked_by='reviewer-repo-1', revoked_at=?, revision=3
		WHERE id=? AND revision=2`, now.Add(6*time.Minute+2*time.Second).Format(time.RFC3339Nano), approved.ID); err != nil {
		_ = revocationTx.Rollback()
		t.Fatal(err)
	}
	activationStarted := make(chan struct{})
	activationDone := make(chan error, 1)
	go func() {
		close(activationStarted)
		_, activationErr := repo.CommitPublication(t.Context(), publication.ID, now.Add(7*time.Minute))
		activationDone <- activationErr
	}()
	<-activationStarted
	if err := revocationTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-activationDone; !errors.Is(err, deployment.ErrApprovalRequired) {
		t.Fatalf("revocation/activation race error=%v, want ErrApprovalRequired", err)
	}
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT COALESCE(active_generation_id,'') FROM delivery_target_revisions WHERE target_id=?`, plan.TargetID).Scan(&activeBefore); err != nil {
		t.Fatal(err)
	}
	if activeBefore != "" {
		t.Fatalf("revoked activation changed active pointer to %q", activeBefore)
	}
	replacement := approval
	replacement.ID = "approval-publication-repo-2"
	replacement.RequestedAt = now.Add(6*time.Minute + 3*time.Second)
	if _, err := repo.CreateApproval(t.Context(), replacement); err != nil {
		t.Fatalf("replacement approval: %v", err)
	}
	replacement.Status = deployment.ApprovalApproved
	replacement.ApprovedBy = "reviewer-repo-1"
	replacement.ApprovalCredentialClass = deployment.CredentialClassHuman
	replacement.ApprovalCredentialID = "reviewer-credential-repo-2"
	replacement.ApprovalCredentialExpiresAt = now.Add(4 * time.Hour)
	replacement.ApprovedAt = now.Add(6*time.Minute + 4*time.Second)
	replacement.Revision = 2
	if _, err := repo.SaveApproval(t.Context(), replacement, 1); err != nil {
		t.Fatalf("approve replacement publication: %v", err)
	}
	committed, err := repo.CommitPublication(t.Context(), publication.ID, now.Add(7*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if committed.Status != deployment.DeliveryPublicationCommitted || committed.ResultTargetRevision != 1 {
		t.Fatalf("publication=%#v", committed)
	}
	if retry, err := repo.CommitPublication(t.Context(), publication.ID, now.Add(8*time.Minute)); err != nil || retry != committed {
		t.Fatalf("publication lost-response retry=%#v err=%v", retry, err)
	}
	var active string
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT active_generation_id FROM delivery_target_revisions WHERE target_id=?`, plan.TargetID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != "generation-repo-1" {
		t.Fatalf("active generation=%q", active)
	}
	// Retire the active generation and leave the target with no active pointer;
	// rollback below only swaps SQLite lifecycle state. No DuckLake or
	// object-store call is involved in this setup or in Rollback.
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE delivery_generations SET status='retired',retired_at=?,rollback_until=? WHERE id=?`, deliveryTime(now.Add(8*time.Minute)), deliveryTime(now.Add(time.Hour)), "generation-repo-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE delivery_target_revisions SET active_generation_id=NULL,target_revision=1 WHERE target_id=?`, plan.TargetID); err != nil {
		t.Fatal(err)
	}
	rollbackRequest := deployment.RollbackRequest{
		ID: "rollback-repo-1", RequestDigest: repoDeliveryDigest('7'), TargetID: plan.TargetID, ProjectID: plan.ProjectID, Environment: plan.Environment,
		GenerationID: "generation-repo-1", CandidateID: candidate.ID, ExpectedBaseGenerationID: "", ExpectedTargetRevision: 1,
		VerifiedSeal: deployment.VerifiedSeal{SealID: seal.ID, CatalogDigest: seal.CatalogDigest, CatalogObjectKey: seal.ObjectKey, ObjectSize: seal.ObjectSize, PhysicalPoolID: seal.PhysicalPoolID, CompatibilityDigest: seal.CompatibilityDigest, ClosureDigest: repoDeliveryDigest('e'), QualificationDigest: repoDeliveryDigest('f'), ServingArtifactID: seal.ServingArtifactID, ServingArtifactDigest: seal.ServingArtifactDigest},
		CreatedAt:    now.Add(9 * time.Minute),
	}
	wrongEvidence := rollbackRequest
	wrongEvidence.ID, wrongEvidence.RequestDigest = "rollback-repo-evidence", repoDeliveryDigest('6')
	wrongEvidence.VerifiedSeal.ClosureDigest = repoDeliveryDigest('0')
	if _, err := repo.Rollback(t.Context(), wrongEvidence); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("wrong seal evidence rollback=%v, want ErrDeliveryConflict", err)
	}
	stale := rollbackRequest
	stale.ID, stale.RequestDigest, stale.ExpectedTargetRevision = "rollback-repo-stale", repoDeliveryDigest('8'), 0
	if _, err := repo.Rollback(t.Context(), stale); !errors.Is(err, deployment.ErrDeliveryStale) {
		t.Fatalf("stale rollback=%v, want ErrDeliveryStale", err)
	}
	expired := rollbackRequest
	expired.ID, expired.RequestDigest, expired.CreatedAt = "rollback-repo-expired", repoDeliveryDigest('5'), now.Add(2*time.Hour)
	if _, err := repo.Rollback(t.Context(), expired); !errors.Is(err, deployment.ErrDeliveryPlanExpired) {
		t.Fatalf("expired rollback=%v, want ErrDeliveryPlanExpired", err)
	}
	rolledBack, err := repo.Rollback(t.Context(), rollbackRequest)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Status != string(deployment.DeliveryPublicationCommitted) || rolledBack.TargetRevision != 2 || rolledBack.GenerationID != "generation-repo-1" {
		t.Fatalf("rollback result=%#v", rolledBack)
	}
	if retry, err := repo.Rollback(t.Context(), rollbackRequest); err != nil || retry != rolledBack {
		t.Fatalf("rollback retry=%#v err=%v", retry, err)
	}
}

func TestDeliveryRepositoryBuildAttemptAllowsFullRefreshBaseGeneration(t *testing.T) {
	store, repo := openDeliveryRepository(t)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	if _, err := store.SQLDB().ExecContext(t.Context(), `
		INSERT INTO delivery_target_revisions (target_id, project_id, environment, active_generation_id, created_at, updated_at)
		VALUES ('target-repo-1', 'project-repo-1', 'prod', 'generation-full-refresh', ?, ?)`, deliveryTime(now), deliveryTime(now)); err != nil {
		t.Fatal(err)
	}
	plan := repoDeliveryPlan(t, now)
	plan.BaseGenerationID = "generation-full-refresh"
	plan.Digest = ""
	plan, err := deployment.NewDeliveryPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreatePlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	pool := repoDeliveryDigest('9')
	insertDeliveryPool(t, store, pool)
	lease := deployment.DeliveryWriterLease{
		ID: "writer-full-refresh", AttemptID: "attempt-full-refresh", PhysicalPoolID: pool,
		OwnerID: "builder", Epoch: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	attempt := deployment.DeliveryBuildAttempt{
		ID: "attempt-full-refresh", PlanID: plan.ID, PlanDigest: plan.Digest,
		SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest,
		BaseGenerationID: plan.BaseGenerationID, PhysicalPoolID: pool,
		WriterLeaseID: lease.ID, CreatedAt: now,
	}
	persistedLease, persistedAttempt, err := repo.CreateWriterLeaseAndBuildAttempt(t.Context(), lease, attempt)
	if err != nil {
		t.Fatalf("full-refresh build attempt: %v", err)
	}
	if persistedLease.AttemptID != attempt.ID || persistedAttempt.BaseGenerationID != plan.BaseGenerationID || persistedAttempt.BaseCatalogDigest != "" || persistedAttempt.BasePhysicalPoolID != "" {
		t.Fatalf("persisted full-refresh identities = lease:%#v attempt:%#v", persistedLease, persistedAttempt)
	}
	roundTrip, err := repo.DeliveryBuildAttemptByID(t.Context(), attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := roundTrip.Validate(); err != nil {
		t.Fatalf("round-trip full-refresh attempt validation: %v", err)
	}

	invalid := attempt
	invalid.ID = "attempt-partial-retained"
	invalid.WriterLeaseID = "writer-partial-retained"
	invalid.BaseCatalogDigest = repoDeliveryDigest('a')
	if _, _, err := repo.CreateWriterLeaseAndBuildAttempt(t.Context(), deployment.DeliveryWriterLease{
		ID: invalid.WriterLeaseID, AttemptID: invalid.ID, PhysicalPoolID: pool,
		OwnerID: "builder", Epoch: 2, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}, invalid); !errors.Is(err, deployment.ErrDeliveryInvalid) {
		t.Fatalf("partial retained-base pair err=%v, want ErrDeliveryInvalid", err)
	}
}

func TestFailedGateEvidenceRoundTripIsImmutable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "failed-gate.db")
	store, err := platform.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewRepositoryWithHooks(store.SQLDB(), ActivationHooks{})
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC().Truncate(time.Second)
	plan := repoDeliveryPlan(t, now)
	if _, err := repo.CreatePlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	pool := repoDeliveryDigest('9')
	insertDeliveryPool(t, store, pool)
	lease := deployment.DeliveryWriterLease{ID: "writer-failed-gate", AttemptID: "attempt-failed-gate", PhysicalPoolID: pool, OwnerID: "builder", Epoch: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	attempt := deployment.DeliveryBuildAttempt{ID: lease.AttemptID, PlanID: plan.ID, IdempotencyKey: "build-op-failed-gate", PlanDigest: plan.Digest, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, PhysicalPoolID: pool, WriterLeaseID: lease.ID, CreatedAt: now}
	if _, _, err := repo.CreateWriterLeaseAndBuildAttempt(t.Context(), lease, attempt); err != nil {
		t.Fatal(err)
	}
	evidence, err := (release.GateEvidence{Version: 1, CandidateID: "candidate-failed-gate", SourceDigest: plan.SourceDigest, BindingGeneration: release.BindingFingerprint(nil), RuntimeVersion: "runtime:test", DuckDBVersion: "duckdb:test", Outcome: release.GateUnavailable, EvaluatedAt: now, Bounds: release.GateBounds{MaxRows: 10, MaxQueries: 2, MaxMillis: 100}, Sources: []release.GateSourceEvidence{{ID: "source-1", Mode: "inferred", SourceDigest: plan.SourceDigest, SchemaOutcome: release.GateUnavailable, ObservedSchema: []semanticmodel.ColumnSchema{}}}}).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordFailedBuildGateEvidence(t.Context(), attempt.ID, &evidence); err != nil {
		t.Fatal(err)
	}
	got, err := repo.FailedBuildGateEvidence(t.Context(), attempt.ID)
	if err != nil || got == nil || got.Digest != evidence.Digest {
		t.Fatalf("failed gate evidence readback=%#v err=%v", got, err)
	}
	var payload string
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT evidence_json FROM delivery_failed_gate_evidence WHERE attempt_id=?`, attempt.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(payload), "secret") || strings.Contains(strings.ToLower(payload), "password") {
		t.Fatalf("failed gate evidence persisted a secret-looking value: %s", payload)
	}
	conflict := evidence
	conflict.CandidateID = "candidate-other"
	conflict, err = conflict.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordFailedBuildGateEvidence(t.Context(), attempt.ID, &conflict); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("changed failed evidence err=%v, want conflict", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := platform.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if restarted, err := NewRepositoryWithHooks(reopened.SQLDB(), ActivationHooks{}).FailedBuildGateEvidence(t.Context(), attempt.ID); err != nil || restarted == nil || restarted.Digest != evidence.Digest {
		t.Fatalf("restarted failed gate evidence=%#v err=%v", restarted, err)
	}
}

func TestDeliveryRepositoryCreatePlanConcurrentIdenticalRequestsConverge(t *testing.T) {
	_, repo := openDeliveryRepository(t)
	plan := repoDeliveryPlan(t, time.Now().UTC().Truncate(time.Second))
	start := make(chan struct{})
	results := make(chan deployment.DeliveryPlan, 2)
	errs := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, err := repo.CreatePlan(t.Context(), plan)
			results <- result
			errs <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errs)
	var first deployment.DeliveryPlan
	for result := range results {
		if first.ID == "" {
			first = result
		}
		if result.ID != plan.ID || result.Digest != plan.Digest {
			t.Fatalf("concurrent plan result = %#v, want id/digest %s/%s", result, plan.ID, plan.Digest)
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent identical CreatePlan error = %v", err)
		}
	}
	if first.ID == "" {
		t.Fatal("concurrent CreatePlan returned no result")
	}
}

func TestDeliveryRepositoryCreatePlanConcurrentTimestampDriftConverges(t *testing.T) {
	_, repo := openDeliveryRepository(t)
	base := repoDeliveryPlan(t, time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC))
	drift := base
	drift.CreatedAt = base.CreatedAt.Add(2 * time.Minute)
	drift.Governance.ExpiresAt = base.Governance.ExpiresAt.Add(2 * time.Minute)
	drift.GovernanceDigest, drift.Digest = "", ""
	drift, err := deployment.NewDeliveryPlan(drift)
	if err != nil {
		t.Fatal(err)
	}
	if drift.Digest == base.Digest {
		t.Fatal("planner timestamp drift unexpectedly preserved the complete plan digest")
	}

	start := make(chan struct{})
	results := make(chan deployment.DeliveryPlan, 2)
	errs := make(chan error, 2)
	var group sync.WaitGroup
	for _, candidate := range []deployment.DeliveryPlan{base, drift} {
		group.Add(1)
		go func(plan deployment.DeliveryPlan) {
			defer group.Done()
			<-start
			result, createErr := repo.CreatePlan(t.Context(), plan)
			results <- result
			errs <- createErr
		}(candidate)
	}
	close(start)
	group.Wait()
	close(results)
	close(errs)
	var durable deployment.DeliveryPlan
	for result := range results {
		if durable.ID == "" {
			durable, err = repo.PlanByID(t.Context(), result.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
		if result.ID != base.ID || result.Digest != durable.Digest {
			t.Fatalf("timestamp-drift result=%#v durable=%#v", result, durable)
		}
	}
	for createErr := range errs {
		if createErr != nil {
			t.Fatalf("concurrent timestamp-drift CreatePlan error=%v", createErr)
		}
	}
	if durable.ID == "" {
		t.Fatal("timestamp-drift CreatePlan returned no durable plan")
	}
}

func TestDeliveryRepositoryBuildTransitionCASAllowsOneConcurrentWinner(t *testing.T) {
	store, repo := openDeliveryRepository(t)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	plan := repoDeliveryPlan(t, now)
	if _, err := repo.CreatePlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	pool := repoDeliveryDigest('9')
	insertDeliveryPool(t, store, pool)
	lease := deployment.DeliveryWriterLease{ID: "writer-cas-1", AttemptID: "attempt-cas-1", PhysicalPoolID: pool, OwnerID: "builder", Epoch: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	attempt := deployment.DeliveryBuildAttempt{ID: "attempt-cas-1", PlanID: plan.ID, PlanDigest: plan.Digest, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, PhysicalPoolID: pool, WriterLeaseID: lease.ID, CreatedAt: now}
	if _, _, err := repo.CreateWriterLeaseAndBuildAttempt(t.Context(), lease, attempt); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.TransitionBuildAttempt(t.Context(), attempt.ID, 1, deployment.DeliveryBuildNormalizing, now.Add(time.Minute))
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var wins, conflicts int
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, deployment.ErrDeliveryConflict) {
			conflicts++
		} else {
			t.Errorf("unexpected transition error: %v", err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("concurrent CAS wins=%d conflicts=%d", wins, conflicts)
	}
}

func TestDeliveryRepositoryRejectsTamperedPlanEvidence(t *testing.T) {
	store, repo := openDeliveryRepository(t)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	plan := repoDeliveryPlan(t, now)
	if _, err := repo.CreatePlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE delivery_plans SET execution_inputs_json='{}' WHERE id=?`, plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DeliveryPlanByID(t.Context(), plan.ID); err == nil {
		t.Fatal("tampered plan evidence unexpectedly decoded")
	}
}
