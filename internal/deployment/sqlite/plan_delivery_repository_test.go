package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/project/graph"
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

func TestDeliveryRepositoryPlanBuildSealCandidatePublication(t *testing.T) {
	store, repo := openDeliveryRepository(t)
	now := time.Now().UTC().Truncate(time.Second)
	plan := repoDeliveryPlan(t, now)
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
	if _, err := repo.CreateCandidate(t.Context(), deployment.DeliveryCandidate{ID: "candidate-before-seal", PlanID: plan.ID, PlanDigest: plan.Digest, TargetID: plan.TargetID, ProjectID: plan.ProjectID, Environment: plan.Environment, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, SealID: seal.ID, CatalogDigest: seal.CatalogDigest, CompatibilityDigest: seal.CompatibilityDigest, CatalogObjectKey: seal.ObjectKey, PhysicalPoolID: pool, ServingArtifactID: seal.ServingArtifactID, ServingArtifactDigest: seal.ServingArtifactDigest, ServingStateID: "state-repo-1", CreatedAt: now}); err == nil {
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
	emptyServingState := deployment.DeliveryCandidate{ID: "candidate-empty-serving-state", PlanID: plan.ID, PlanDigest: plan.Digest, TargetID: plan.TargetID, ProjectID: plan.ProjectID, Environment: plan.Environment, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, BaseTargetRevision: 0, SealID: seal.ID, CatalogDigest: seal.CatalogDigest, CompatibilityDigest: seal.CompatibilityDigest, CatalogObjectKey: seal.ObjectKey, PhysicalPoolID: pool, ServingArtifactID: seal.ServingArtifactID, ServingArtifactDigest: seal.ServingArtifactDigest, CreatedAt: now}
	if _, err := repo.CreateCandidateReady(t.Context(), emptyServingState, seal, now.Add(5*time.Minute)); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("candidate without persisted serving state err=%v, want ErrDeliveryConflict", err)
	}
	candidate, err := repo.CreateCandidateReady(t.Context(), deployment.DeliveryCandidate{ID: "candidate-repo-1", PlanID: plan.ID, PlanDigest: plan.Digest, TargetID: plan.TargetID, ProjectID: plan.ProjectID, Environment: plan.Environment, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, BaseTargetRevision: 0, SealID: seal.ID, CatalogDigest: seal.CatalogDigest, CompatibilityDigest: seal.CompatibilityDigest, CatalogObjectKey: seal.ObjectKey, PhysicalPoolID: pool, ServingArtifactID: seal.ServingArtifactID, ServingArtifactDigest: seal.ServingArtifactDigest, ServingStateID: "state-repo-1", CreatedAt: now}, seal, now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Status != deployment.DeliveryCandidateReady {
		t.Fatalf("candidate status=%s", candidate.Status)
	}
	publication, err := repo.CreatePublication(t.Context(), deployment.DeliveryPublication{ID: "publication-repo-1", RequestDigest: repoDeliveryDigest('1'), TargetID: plan.TargetID, ProjectID: plan.ProjectID, Environment: plan.Environment, PlanID: plan.ID, PlanDigest: plan.Digest, CandidateID: candidate.ID, GenerationID: "generation-repo-1", ExpectedTargetRevision: 0, CreatedAt: now.Add(6 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO principals (id, email, display_name) VALUES ('publisher-repo-1', 'publisher-repo-1@example.test', 'Publisher')`); err != nil {
		t.Fatal(err)
	}
	approval := deployment.Approval{
		ID: "approval-publication-repo-1", ProjectID: plan.ProjectID.String(), DeploymentID: publication.ID,
		Environment: plan.Environment, RequestDigest: publication.RequestDigest, ReleaseID: candidate.ServingArtifactID,
		Status: deployment.ApprovalPending, RequestedBy: "publisher-repo-1",
		RequestCredentialClass: deployment.CredentialClassWorkload, RequestCredentialID: "credential-repo-1",
		RequestedAt: now.Add(6 * time.Minute), ExpiresAt: now.Add(time.Hour), Revision: 1,
	}
	persistedApproval, err := repo.CreateApproval(t.Context(), approval)
	if err != nil {
		t.Fatalf("canonical publication approval: %v", err)
	}
	if persistedApproval.DeploymentID != publication.ID {
		t.Fatalf("canonical publication approval parent = %q, want %q", persistedApproval.DeploymentID, publication.ID)
	}
	loadedApproval, err := repo.ApprovalByDeployment(t.Context(), publication.ID)
	if err != nil || loadedApproval != persistedApproval {
		t.Fatalf("canonical publication approval round trip = %#v, %v", loadedApproval, err)
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
