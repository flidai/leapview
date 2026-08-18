package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
)

func recoveryPublicationFixture(t *testing.T, now time.Time) (*Repository, deployment.DeliveryPublication, deployment.DeliveryCandidate, deployment.DeliveryGeneration) {
	t.Helper()
	store, repo := openDeliveryRepository(t)
	repo.WithDeliveryClock(func() time.Time { return now })
	pool := repoDeliveryDigest('9')
	insertDeliveryPool(t, store, pool)
	plan := repoDeliveryPlan(t, now)
	if _, err := repo.CreatePlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	artifact := createAdapterArtifact(t, repo, plan, pool, now, "attempt-recovery", "writer-recovery", "seal-recovery", "candidate-recovery", 'a', "", "", 0)
	generation := deployment.DeliveryGeneration{
		ID: "generation-recovery", CandidateID: artifact.candidate.ID, PlanID: plan.ID, PlanDigest: plan.Digest,
		TargetID: plan.TargetID, ProjectID: plan.ProjectID, Environment: plan.Environment,
		CatalogDigest: artifact.seal.CatalogDigest, CatalogObjectKey: artifact.seal.ObjectKey,
		PhysicalPoolID: pool, ServingArtifactID: artifact.candidate.ServingArtifactID,
		ServingArtifactDigest: artifact.candidate.ServingArtifactDigest, ServingStateID: artifact.candidate.ServingStateID,
		CompatibilityDigest: artifact.candidate.CompatibilityDigest, RollbackClass: deployment.DeliveryRollbackSafe,
		CreatedAt: now,
	}
	publication := deployment.DeliveryPublication{
		ID: "publication-recovery", RequestDigest: repoDeliveryDigest('1'), TargetID: plan.TargetID,
		ProjectID: plan.ProjectID, Environment: plan.Environment, PlanID: plan.ID, PlanDigest: plan.Digest,
		CandidateID: artifact.candidate.ID, GenerationID: generation.ID, ExpectedTargetRevision: 0,
		CreatedAt: now.Add(6 * time.Minute),
	}
	if _, err := repo.CreatePublication(t.Context(), publication, generation); err != nil {
		t.Fatal(err)
	}
	return repo, publication, artifact.candidate, generation
}

func TestPublicationActivationTimeoutPersistsIndeterminateAndReconcilesProvenNonCommit(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	repo, publication, candidate, generation := recoveryPublicationFixture(t, now)
	repo.hooks.CommitPublication = func(context.Context, *sql.Tx) error {
		return errors.New("activation acknowledgement timed out")
	}
	_, err := repo.CommitPublication(t.Context(), publication.ID, now.Add(7*time.Minute))
	if !errors.Is(err, deployment.ErrDeliveryOutcomeUnknown) {
		t.Fatalf("activation timeout err = %v, want ErrDeliveryOutcomeUnknown", err)
	}
	indeterminate, err := repo.DeliveryPublicationByID(t.Context(), publication.ID)
	if err != nil {
		t.Fatal(err)
	}
	if indeterminate.Status != deployment.DeliveryPublicationIndeterminate {
		t.Fatalf("publication status=%s, want indeterminate", indeterminate.Status)
	}
	reconciled, err := repo.ReconcilePublication(t.Context(), publication.ID, now.Add(8*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Status != deployment.DeliveryPublicationRejected || reconciled.Reason != "activation_not_committed" {
		t.Fatalf("reconciled publication=%#v", reconciled)
	}
	target, err := repo.DeliveryTargetRevision(t.Context(), publication.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if target.ActiveGenerationID != "" || target.TargetRevision != 0 {
		t.Fatalf("target changed after proven non-commit: %#v", target)
	}
	candidateAfter, err := repo.DeliveryCandidateByID(t.Context(), candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	generationAfter, err := repo.DeliveryGenerationByID(t.Context(), generation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if candidateAfter.Status != deployment.DeliveryCandidateReady || generationAfter.Status != deployment.DeliveryGenerationPrepared {
		t.Fatalf("reconciliation mutated candidate/generation: %s/%s", candidateAfter.Status, generationAfter.Status)
	}
}

func TestDeliveryGenerationByServingStateIDResolvesPendingGenerationExactly(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	repo, _, _, generation := recoveryPublicationFixture(t, now)
	got, err := repo.DeliveryGenerationByServingStateID(t.Context(), generation.TargetID, generation.ProjectID.String(), generation.Environment, generation.ServingStateID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != generation.ID || got.Status != deployment.DeliveryGenerationPrepared {
		t.Fatalf("resolved generation = %#v, want pending prepared %q", got, generation.ID)
	}
	if _, err := repo.DeliveryGenerationByServingStateID(t.Context(), generation.TargetID, "project-other", generation.Environment, generation.ServingStateID); !errors.Is(err, deployment.ErrNotFound) {
		t.Fatalf("wrong project lookup = %v, want ErrNotFound", err)
	}
}

func TestPublicationActivationLostAcknowledgementConvergesCommitted(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	repo, publication, _, _ := recoveryPublicationFixture(t, now)
	repo.hooks.CommitPublication = func(_ context.Context, tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("lost activation acknowledgement")
	}
	committed, err := repo.CommitPublication(t.Context(), publication.ID, now.Add(7*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if committed.Status != deployment.DeliveryPublicationCommitted || committed.ResultTargetRevision != 1 {
		t.Fatalf("committed publication=%#v", committed)
	}
}

func TestPublicationActivationCanceledContextStillPersistsIndeterminate(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	repo, publication, _, _ := recoveryPublicationFixture(t, now)
	ctx, cancel := context.WithCancel(t.Context())
	repo.hooks.CommitPublication = func(context.Context, *sql.Tx) error {
		cancel()
		return context.DeadlineExceeded
	}
	_, err := repo.CommitPublication(ctx, publication.ID, now.Add(7*time.Minute))
	if !errors.Is(err, deployment.ErrDeliveryOutcomeUnknown) {
		t.Fatalf("canceled activation err = %v, want ErrDeliveryOutcomeUnknown", err)
	}
	indeterminate, err := repo.DeliveryPublicationByID(t.Context(), publication.ID)
	if err != nil {
		t.Fatal(err)
	}
	if indeterminate.Status != deployment.DeliveryPublicationIndeterminate {
		t.Fatalf("publication status=%s, want indeterminate", indeterminate.Status)
	}
}

func TestPublicationReconcileUnknownOutcomeNeverActivatesOrCleansCandidate(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	repo, publication, candidate, generation := recoveryPublicationFixture(t, now)
	repo.hooks.CommitPublication = func(context.Context, *sql.Tx) error {
		return errors.New("activation process crashed")
	}
	if _, err := repo.CommitPublication(t.Context(), publication.ID, now.Add(7*time.Minute)); !errors.Is(err, deployment.ErrDeliveryOutcomeUnknown) {
		t.Fatalf("activation crash err = %v, want ErrDeliveryOutcomeUnknown", err)
	}
	if _, err := repo.db.ExecContext(t.Context(), `UPDATE delivery_target_revisions SET active_generation_id=?,target_revision=? WHERE target_id=?`, "unrelated-generation", 9, publication.TargetID); err != nil {
		t.Fatal(err)
	}
	reconciled, err := repo.ReconcilePublication(t.Context(), publication.ID, now.Add(8*time.Minute))
	if !errors.Is(err, deployment.ErrDeliveryOutcomeUnknown) {
		t.Fatalf("unknown reconciliation err = %v, want ErrDeliveryOutcomeUnknown", err)
	}
	if reconciled.Status != deployment.DeliveryPublicationIndeterminate {
		t.Fatalf("unknown reconciliation status=%s, want indeterminate", reconciled.Status)
	}
	candidateAfter, err := repo.DeliveryCandidateByID(t.Context(), candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	generationAfter, err := repo.DeliveryGenerationByID(t.Context(), generation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if candidateAfter.Status != deployment.DeliveryCandidateReady || generationAfter.Status != deployment.DeliveryGenerationPrepared {
		t.Fatalf("unknown reconciliation mutated candidate/generation: %s/%s", candidateAfter.Status, generationAfter.Status)
	}
}

func TestNonInvalidatingQueryLeaseLifecycleDoesNotBumpTargetRevision(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	repo, publication, _, generation := recoveryPublicationFixture(t, now)
	if _, err := repo.CommitPublication(t.Context(), publication.ID, now.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}
	readRevision := func() int64 {
		t.Helper()
		target, err := repo.DeliveryTargetRevision(t.Context(), publication.TargetID)
		if err != nil {
			t.Fatal(err)
		}
		return target.TargetRevision
	}
	if got := readRevision(); got != 1 {
		t.Fatalf("committed target revision=%d, want 1", got)
	}
	lease := deployment.DeliveryQueryLease{ID: "query-lease-revision", HolderID: "reader", GenerationID: generation.ID, CatalogDigest: generation.CatalogDigest, PhysicalPoolID: generation.PhysicalPoolID, CreatedAt: now.Add(8 * time.Minute), ExpiresAt: now.Add(1 * time.Hour)}
	if _, err := repo.AcquireQueryLease(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	if got := readRevision(); got != 1 {
		t.Fatalf("revision after acquire=%d, want 1", got)
	}
	if _, err := repo.HeartbeatQueryLease(t.Context(), lease.ID, now.Add(9*time.Minute), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := readRevision(); got != 1 {
		t.Fatalf("revision after heartbeat=%d, want 1", got)
	}
	if _, err := repo.ReleaseQueryLease(t.Context(), lease.ID, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := readRevision(); got != 1 {
		t.Fatalf("revision after release=%d, want 1", got)
	}
}
