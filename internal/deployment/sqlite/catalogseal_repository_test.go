package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/catalogseal"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/release"
)

type catalogSealFixture struct {
	store    *platform.Store
	repo     *Repository
	identity catalogseal.SealIdentity
	plan     deployment.DeliveryPlan
	pool     string
	now      time.Time
	clock    *time.Time
}

func newCatalogSealFixture(t *testing.T) catalogSealFixture {
	t.Helper()
	store, repo := openDeliveryRepository(t)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	plan := repoDeliveryPlan(t, now)
	if _, err := repo.CreatePlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	pool := repoDeliveryDigest('9')
	insertDeliveryPool(t, store, pool)
	lease := deployment.DeliveryWriterLease{ID: "writer-catalogseal-1", AttemptID: "attempt-catalogseal-1", PhysicalPoolID: pool, OwnerID: "builder", Epoch: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	attempt := deployment.DeliveryBuildAttempt{ID: lease.AttemptID, PlanID: plan.ID, PlanDigest: plan.Digest, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, PhysicalPoolID: pool, WriterLeaseID: lease.ID, CreatedAt: now}
	if _, _, err := repo.CreateWriterLeaseAndBuildAttempt(t.Context(), lease, attempt); err != nil {
		t.Fatal(err)
	}
	for i, next := range []deployment.DeliveryBuildAttemptStatus{deployment.DeliveryBuildNormalizing, deployment.DeliveryBuildValidating, deployment.DeliveryBuildSealing} {
		if _, err := repo.TransitionBuildAttempt(t.Context(), attempt.ID, int64(i+1), next, now.Add(time.Duration(i+1)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	clock := now.Add(10 * time.Minute)
	repo.WithCatalogSealClock(func() time.Time { return clock })
	catalogDigest := repoDeliveryDigest('c')
	identity := catalogseal.SealIdentity{
		SealID:        "seal-catalogseal-1",
		Attempt:       catalogseal.AttemptIdentity{ID: attempt.ID, WriterLeaseID: lease.ID},
		Plan:          catalogseal.PlanIdentity{ID: plan.ID, Digest: plan.Digest, ExecutionDigest: plan.ExecutionDigest},
		Pool:          catalogseal.PoolIdentity{ID: pool, CompatibilityDigest: repoDeliveryDigest('d')},
		Qualification: catalogseal.QualificationIdentity{Digest: repoDeliveryDigest('e')},
		Closure:       catalogseal.ClosureIdentity{Digest: repoDeliveryDigest('f')},
		Candidate:     catalogseal.CandidateIdentity{ID: "candidate-catalogseal-1", ServingArtifactID: "artifact-catalogseal-1", ServingArtifactDigest: repoDeliveryDigest('7'), ServingStateID: "state-catalogseal-1"},
		CatalogDigest: catalogDigest, ObjectKey: catalogseal.CanonicalObjectKey(catalogDigest), ObjectSize: 42,
	}
	return catalogSealFixture{store: store, repo: repo, identity: identity, plan: plan, pool: pool, now: now, clock: &clock}
}

func (f catalogSealFixture) completionInput() catalogseal.CompleteInput {
	binding := release.BindingFingerprint(nil)
	evidence, err := (release.GateEvidence{Version: 1, CandidateID: f.identity.Candidate.ID, SourceDigest: f.plan.SourceDigest, BindingGeneration: binding, RuntimeVersion: "runtime:test", DuckDBVersion: "duckdb:test", Outcome: release.GateSuccess, EvaluatedAt: f.now, Bounds: release.GateBounds{MaxRows: 100, MaxQueries: 10, MaxMillis: 1000}}).Canonical()
	if err != nil {
		panic(err)
	}
	resolved, err := deployment.NewDeliveryResolvedBuildInputs(deployment.DeliveryResolvedBuildInputs{PolicyDigest: f.plan.Governance.PolicyDigest, GateEvidence: &evidence})
	if err != nil {
		panic(err)
	}
	encoded, err := json.Marshal(resolved)
	if err != nil {
		panic(err)
	}
	return catalogseal.CompleteInput{Seal: f.identity, SealID: f.identity.SealID, CandidateID: f.identity.Candidate.ID, ClosureDigest: f.identity.Closure.Digest, QualificationDigest: f.identity.Qualification.Digest, ResolvedInputsJSON: string(encoded), ResolvedInputsDigest: resolved.EvidenceDigest}
}

func TestCatalogSealRepositoryRestartAndIdempotentCompletion(t *testing.T) {
	f := newCatalogSealFixture(t)
	prepared, err := f.repo.Prepare(t.Context(), f.identity)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status != catalogseal.SealPreparing || prepared.Identity != f.identity {
		t.Fatalf("prepared = %#v", prepared)
	}
	if _, err := f.repo.MarkUploaded(t.Context(), f.identity.SealID); err != nil {
		t.Fatal(err)
	}
	completed, err := f.repo.CompleteVerified(t.Context(), f.completionInput())
	if err != nil {
		t.Fatal(err)
	}
	if completed.CandidateID != f.identity.Candidate.ID || !completed.LeaseReleased || completed.Seal.Status != catalogseal.SealVerified {
		t.Fatalf("completion = %#v", completed)
	}
	candidate, err := f.repo.DeliveryCandidateByID(t.Context(), f.identity.Candidate.ID)
	if err != nil || candidate.Status != deployment.DeliveryCandidateReady {
		t.Fatalf("candidate = %#v, err=%v", candidate, err)
	}
	lease, err := f.repo.DeliveryWriterLeaseByID(t.Context(), f.identity.Attempt.WriterLeaseID)
	if err != nil || lease.Status != deployment.DeliveryLeaseReleased {
		t.Fatalf("lease = %#v, err=%v", lease, err)
	}
	attempt, err := f.repo.DeliveryBuildAttemptByID(t.Context(), f.identity.Attempt.ID)
	if err != nil || attempt.Status != deployment.DeliveryBuildSealed || attempt.CandidateID != f.identity.Candidate.ID {
		t.Fatalf("attempt = %#v, err=%v", attempt, err)
	}
	// The seal completion releases the writer lease. An exact retry must return
	// the durable sealed attempt instead of trying to allocate a new epoch.
	retryLease := deployment.DeliveryWriterLease{ID: lease.ID, AttemptID: lease.AttemptID, PhysicalPoolID: lease.PhysicalPoolID, OwnerID: lease.OwnerID, CreatedAt: f.now.Add(20 * time.Minute), ExpiresAt: f.now.Add(21 * time.Minute)}
	retryAttempt := attempt
	retryAttempt.CreatedAt = retryLease.CreatedAt
	replayedLease, replayedAttempt, err := f.repo.CreateWriterLeaseAndBuildAttempt(t.Context(), retryLease, retryAttempt)
	if err != nil || replayedLease.Status != deployment.DeliveryLeaseReleased || replayedAttempt.Status != deployment.DeliveryBuildSealed {
		t.Fatalf("released sealed retry lease=%#v attempt=%#v err=%v", replayedLease, replayedAttempt, err)
	}
	driftedAttempt := retryAttempt
	driftedAttempt.IdempotencyKey = "drifted-sealed-retry"
	if _, _, err := f.repo.CreateWriterLeaseAndBuildAttempt(t.Context(), retryLease, driftedAttempt); !errors.Is(err, deployment.ErrDeliveryIdempotencyDrift) {
		t.Fatalf("released sealed identity drift err=%v, want ErrDeliveryIdempotencyDrift", err)
	}

	// A fresh adapter over the same database models process restart. No local
	// state is needed to replay Lookup or the atomic completion.
	restarted := NewRepositoryWithHooks(f.store.SQLDB(), ActivationHooks{}).WithCatalogSealClock(func() time.Time { return *f.clock })
	lookedUp, err := restarted.Lookup(t.Context(), f.identity.SealID)
	if err != nil || lookedUp.Status != catalogseal.SealVerified || lookedUp.Identity != f.identity {
		t.Fatalf("restart lookup = %#v, err=%v", lookedUp, err)
	}
	completion, err := restarted.CompletedDelivery(t.Context(), f.identity.Attempt.ID, f.identity.Candidate.ID)
	if err != nil || completion.CandidateID != f.identity.Candidate.ID || !completion.LeaseReleased || completion.Seal.Status != catalogseal.SealVerified {
		t.Fatalf("restart completed delivery = %#v, err=%v", completion, err)
	}
	if _, err := restarted.CompletedDelivery(t.Context(), f.identity.Attempt.ID, "candidate-other"); !errors.Is(err, catalogseal.ErrIdentityConflict) {
		t.Fatalf("completion candidate mismatch err=%v, want identity conflict", err)
	}
	retry, err := restarted.CompleteVerified(t.Context(), f.completionInput())
	if err != nil || !retry.LeaseReleased || retry.CandidateID != f.identity.Candidate.ID {
		t.Fatalf("completion retry = %#v, err=%v", retry, err)
	}
}

func TestCreateWriterLeaseAndBuildAttemptRejectsReleasedNonsealedReplay(t *testing.T) {
	f := newCatalogSealFixture(t)
	lease, err := f.repo.DeliveryWriterLeaseByID(t.Context(), f.identity.Attempt.WriterLeaseID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.TransitionWriterLease(t.Context(), lease.ID, deployment.DeliveryLeaseActive, deployment.DeliveryLeaseReleased, f.now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	attempt, err := f.repo.DeliveryBuildAttemptByID(t.Context(), f.identity.Attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	retryLease := deployment.DeliveryWriterLease{ID: lease.ID, AttemptID: lease.AttemptID, PhysicalPoolID: lease.PhysicalPoolID, OwnerID: lease.OwnerID, CreatedAt: f.now.Add(20 * time.Minute), ExpiresAt: f.now.Add(21 * time.Minute)}
	retryAttempt := attempt
	retryAttempt.CreatedAt = retryLease.CreatedAt
	if _, _, err := f.repo.CreateWriterLeaseAndBuildAttempt(t.Context(), retryLease, retryAttempt); !errors.Is(err, deployment.ErrDeliveryIdempotencyDrift) {
		t.Fatalf("released nonsealed replay err=%v, want ErrDeliveryIdempotencyDrift", err)
	}
}

func TestCatalogSealRepositoryRejectsIdentityDrift(t *testing.T) {
	f := newCatalogSealFixture(t)
	if _, err := f.repo.Prepare(t.Context(), f.identity); err != nil {
		t.Fatal(err)
	}
	otherSeal := f.identity
	otherSeal.SealID = "seal-catalogseal-other"
	if _, err := f.repo.Prepare(t.Context(), otherSeal); !errors.Is(err, catalogseal.ErrIdentityConflict) {
		t.Fatalf("same-attempt identity drift error = %v, want conflict", err)
	}
	changed := f.identity
	changed.Closure.Digest = repoDeliveryDigest('0')
	if _, err := f.repo.Prepare(t.Context(), changed); !errors.Is(err, catalogseal.ErrIdentityConflict) {
		t.Fatalf("identity drift error = %v, want conflict", err)
	}
	if _, err := f.store.SQLDB().ExecContext(t.Context(), `UPDATE delivery_catalog_seals SET identity_closure_digest=? WHERE id=?`, repoDeliveryDigest('0'), f.identity.SealID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.CompleteVerified(t.Context(), f.completionInput()); !errors.Is(err, catalogseal.ErrIdentityConflict) {
		t.Fatalf("persisted identity drift completion = %v, want conflict", err)
	}
}

func TestCatalogSealRepositoryCompletionRollsBackOnCandidateConflict(t *testing.T) {
	f := newCatalogSealFixture(t)
	if _, err := f.repo.Prepare(t.Context(), f.identity); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.MarkUploaded(t.Context(), f.identity.SealID); err != nil {
		t.Fatal(err)
	}
	// This row has the exact seal and plan bindings but a different project
	// scope. Completion must reject it after attempting the seal transition,
	// and SQLite must roll that transition back with no ready/released split.
	_, err := f.store.SQLDB().ExecContext(t.Context(), `
		INSERT INTO delivery_candidates
		 (id,plan_id,plan_digest,target_id,project_id,environment,source_digest,execution_digest,
		  base_target_revision,seal_id,catalog_digest,compatibility_digest,catalog_object_key,
		  physical_pool_id,status,failure_code,created_at)
		VALUES (?,?,?,?,?,?,?, ?,0,?,?,?, ?,?,'preparing','',?)`,
		f.identity.Candidate.ID, f.plan.ID, f.plan.Digest, f.plan.TargetID, "different-project", f.plan.Environment,
		f.plan.SourceDigest, f.plan.ExecutionDigest, f.identity.SealID, f.identity.CatalogDigest,
		f.identity.Pool.CompatibilityDigest, f.identity.ObjectKey, f.pool, deliveryTime(f.now))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.CompleteVerified(t.Context(), f.completionInput()); !errors.Is(err, catalogseal.ErrIdentityConflict) {
		t.Fatalf("conflicting candidate completion error = %v, want conflict", err)
	}
	var sealStatus, leaseStatus, candidateStatus, attemptStatus string
	if err := f.store.SQLDB().QueryRowContext(t.Context(), `SELECT status FROM delivery_catalog_seals WHERE id=?`, f.identity.SealID).Scan(&sealStatus); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SQLDB().QueryRowContext(t.Context(), `SELECT status FROM delivery_writer_leases WHERE id=?`, f.identity.Attempt.WriterLeaseID).Scan(&leaseStatus); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SQLDB().QueryRowContext(t.Context(), `SELECT status FROM delivery_candidates WHERE id=?`, f.identity.Candidate.ID).Scan(&candidateStatus); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SQLDB().QueryRowContext(t.Context(), `SELECT status FROM delivery_build_attempts WHERE id=?`, f.identity.Attempt.ID).Scan(&attemptStatus); err != nil {
		t.Fatal(err)
	}
	if sealStatus != "uploaded" || leaseStatus != "active" || candidateStatus != "preparing" || attemptStatus != "sealing" {
		t.Fatalf("rollback statuses seal=%s lease=%s candidate=%s attempt=%s", sealStatus, leaseStatus, candidateStatus, attemptStatus)
	}
}

func TestCatalogSealRepositoryRejectsExpiredLease(t *testing.T) {
	f := newCatalogSealFixture(t)
	*f.clock = f.now.Add(2 * time.Hour)
	if _, err := f.repo.Prepare(t.Context(), f.identity); !errors.Is(err, catalogseal.ErrRepositoryTransition) {
		t.Fatalf("expired prepare error = %v, want transition", err)
	}
	var seals int
	if err := f.store.SQLDB().QueryRowContext(t.Context(), `SELECT count(*) FROM delivery_catalog_seals WHERE id=?`, f.identity.SealID).Scan(&seals); err != nil {
		t.Fatal(err)
	}
	if seals != 0 {
		t.Fatalf("expired lease persisted %d seal rows", seals)
	}
}

func TestCatalogSealRepositoryLostLocalRecoveryUsesDurableIdentity(t *testing.T) {
	f := newCatalogSealFixture(t)
	if _, err := f.repo.Prepare(t.Context(), f.identity); err != nil {
		t.Fatal(err)
	}
	lookup, err := NewRepositoryWithHooks(f.store.SQLDB(), ActivationHooks{}).Lookup(t.Context(), f.identity.SealID)
	if err != nil {
		t.Fatal(err)
	}
	if lookup.Identity.ObjectKey != catalogseal.CanonicalObjectKey(lookup.Identity.CatalogDigest) || lookup.Identity.ObjectSize != 42 {
		t.Fatalf("durable identity = %#v", lookup.Identity)
	}
	if _, err := f.repo.MarkUploaded(t.Context(), f.identity.SealID); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogSealRepositoryMissingSealIsNotFound(t *testing.T) {
	f := newCatalogSealFixture(t)
	if _, err := f.repo.Lookup(t.Context(), "missing-seal"); !errors.Is(err, catalogseal.ErrSealNotFound) {
		t.Fatalf("missing lookup error = %v, want not found", err)
	}
	if _, err := f.repo.MarkUploaded(t.Context(), "missing-seal"); !errors.Is(err, catalogseal.ErrSealNotFound) {
		t.Fatalf("missing mark error = %v, want not found", err)
	}
}

func TestCatalogSealRepositoryObjectKeyIdentityIsCanonical(t *testing.T) {
	f := newCatalogSealFixture(t)
	f.identity.ObjectKey = strings.Replace(f.identity.ObjectKey, ".ducklake", ".sqlite", 1)
	if _, err := f.repo.Prepare(context.Background(), f.identity); !errors.Is(err, catalogseal.ErrInvalidRequest) {
		t.Fatalf("noncanonical object key error = %v, want invalid request", err)
	}
}

var _ catalogseal.SealRepository = (*Repository)(nil)
