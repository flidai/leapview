package postgres

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

type completeBuildFixture struct {
	TargetID, PlanID, CandidateID, AttemptID, SealID, LeaseID string
	PlanDigest, RequestDigest, ArtifactDigest                 string
	Lease                                                     DeliveryLease
	Commit                                                    CommitAttemptInput
	Seal                                                      SnapshotSealInput
}

func newCompleteBuildFixture(t *testing.T, r *Repository) completeBuildFixture {
	return newCompleteBuildFixtureWithSuffix(t, r, "1")
}

func newCompleteBuildFixtureWithSuffix(t *testing.T, r *Repository, suffix string) completeBuildFixture {
	t.Helper()
	ctx := t.Context()
	f := completeBuildFixture{
		TargetID:       "target_complete_build_" + suffix,
		PlanID:         fmt.Sprintf("0198f2c0-7c7a-7f00-0000-00000000%s001", suffix),
		CandidateID:    fmt.Sprintf("0198f2c0-7c7a-7f00-0000-00000000%s002", suffix),
		AttemptID:      fmt.Sprintf("0198f2c0-7c7a-7f00-0000-00000000%s003", suffix),
		SealID:         fmt.Sprintf("0198f2c0-7c7a-7f00-0000-00000000%s004", suffix),
		LeaseID:        fmt.Sprintf("0198f2c0-7c7a-7f00-0000-00000000%s005", suffix),
		PlanDigest:     testDigest('a'),
		RequestDigest:  testDigest('f'),
		ArtifactDigest: testDigest('e'),
	}
	if _, err := r.CreateTarget(ctx, TargetInput{TargetID: f.TargetID, ProjectID: "project_complete_build_" + suffix, Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreatePlan(ctx, PlanInput{PlanID: f.PlanID, TargetID: f.TargetID, PlanRevision: 1, PlanDigest: f.PlanDigest, CompiledGraphDigest: testDigest('b'), CompiledConfigDigest: testDigest('c'), SecurityDomainFingerprint: testDigest('d'), ArtifactDigest: f.ArtifactDigest}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateCandidate(ctx, CandidateInput{CandidateID: f.CandidateID, TargetID: f.TargetID, PlanID: f.PlanID, CandidateRevision: 1, ArtifactDigest: f.ArtifactDigest}); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().UTC().Add(time.Hour)
	tx, err := r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.Lease, _, err = r.AcquireLeaseAndBeginBuildAttemptTx(ctx, tx,
		LeaseInput{LeaseID: f.LeaseID, TargetID: f.TargetID, OwnerID: "builder-complete", ExpiresAt: expires},
		BuildAttemptInput{AttemptID: f.AttemptID, PlanID: f.PlanID, CandidateID: f.CandidateID, OwnerID: "builder-complete", PhysicalPoolID: "pool-complete", RequestDigest: f.RequestDigest, PlanDigest: f.PlanDigest, Namespace: "candidate/complete", SessionIdentity: "session-complete"},
	)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	f.Commit = CommitAttemptInput{AttemptID: f.AttemptID, OwnerID: f.Lease.OwnerID, FencingEpoch: f.Lease.FencingEpoch, SnapshotID: 42, CommitMarker: testCommitMarker(f.AttemptID, "pool-complete", f.RequestDigest, f.PlanDigest)}
	f.Seal = SnapshotSealInput{
		SealID: f.SealID, AttemptID: f.AttemptID, CandidateID: f.CandidateID, PhysicalPoolID: "pool-complete", TenantDomain: "tenant-complete", Region: "us-east", EncryptionDomain: "enc-complete", ObjectNamespace: "objects/complete", CatalogDatabase: "ducklake", CatalogID: "catalog-complete", CatalogUUID: fmt.Sprintf("0198f2c0-7c7a-7f00-0000-00000000%s006", suffix), CatalogVersion: 1, DuckLakeSnapshotID: 42, RelationNamespace: "candidate/complete", RelationManifestDigest: testDigest('1'), ClosureDigest: testDigest('8'), ObjectRoot: "objects/complete/42", ObjectRootDigest: testDigest('6'), ArtifactRoot: "artifacts/complete", ArtifactRootDigest: testDigest('7'), CompiledGraphDigest: testDigest('b'), CompiledConfigDigest: testDigest('c'), SecurityDomainFingerprint: testDigest('d'), RequestDigest: f.RequestDigest, PlanDigest: f.PlanDigest, CompatibilityDigest: testDigest('2'), ServingArtifactID: "artifact-complete", ServingArtifactDigest: f.ArtifactDigest, DuckDBVersion: "1", RuntimeVersion: "runtime-v1", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1", QualificationEvidence: []byte(`{"checks":["schema"]}`),
	}
	return f
}

func (f completeBuildFixture) fence() LeaseFence {
	return LeaseFence{LeaseID: f.Lease.LeaseID, TargetID: f.Lease.TargetID, OwnerID: f.Lease.OwnerID, FencingEpoch: f.Lease.FencingEpoch}
}

func TestPostgresCompleteBuildTxSuccessAndExactReplay(t *testing.T) {
	r := New(deliveryTestDB(t))
	f := newCompleteBuildFixture(t, r)
	ctx := t.Context()
	qualificationDigest := testDigest('3')
	// Renewing the lease changes its expiry without changing the attempt's
	// original lease deadline. Completion must trust the active exact fence,
	// not require those independent timestamps to remain equal.
	if err := r.RenewLease(ctx, f.fence(), time.Now().UTC().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	tx, err := r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.CompleteBuildTx(ctx, tx, f.Commit, f.Seal, qualificationDigest, f.fence())
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if first.Attempt.State != AttemptCommitted || first.Candidate.Status != "qualified" || first.Lease.State != "released" {
		_ = tx.Rollback(ctx)
		t.Fatalf("completion result = %#v", first)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	tx, err = r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := r.CompleteBuildTx(ctx, tx, f.Commit, f.Seal, qualificationDigest, f.fence())
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("exact completion replay: %v", err)
	}
	if replayed.Attempt.State != AttemptCommitted || replayed.Candidate.Status != "qualified" || replayed.Lease.State != "released" {
		t.Fatalf("replayed completion result = %#v", replayed)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresCompleteBuildTxConflictingReplayAndStaleLease(t *testing.T) {
	r := New(deliveryTestDB(t))
	f := newCompleteBuildFixture(t, r)
	stale := newCompleteBuildFixtureWithSuffix(t, r, "2")
	ctx := t.Context()
	if _, err := r.CompleteBuild(ctx, f.Commit, f.Seal, testDigest('3'), f.fence()); err != nil {
		t.Fatal(err)
	}
	tx, err := r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CompleteBuildTx(ctx, tx, f.Commit, f.Seal, testDigest('4'), f.fence()); !errors.Is(err, ErrConflict) {
		_ = tx.Rollback(ctx)
		t.Fatalf("conflicting replay = %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	// A newer lease fences the original exact fence before completion starts.
	newLease, err := r.AcquireLease(ctx, LeaseInput{LeaseID: "0198f2c0-7c7a-7f00-0000-000000002007", TargetID: stale.TargetID, OwnerID: "builder-new", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if newLease.FencingEpoch == stale.Lease.FencingEpoch {
		t.Fatalf("new lease did not advance fencing epoch: %#v", newLease)
	}
	// The original lease was expired by acquisition of the newer epoch.
	tx, err = r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CompleteBuildTx(ctx, tx, stale.Commit, stale.Seal, testDigest('3'), stale.fence()); !errors.Is(err, ErrStaleFence) {
		_ = tx.Rollback(ctx)
		t.Fatalf("stale completion = %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresCompleteBuildTxRollbackLeavesBuildOpen(t *testing.T) {
	r := New(deliveryTestDB(t))
	f := newCompleteBuildFixture(t, r)
	ctx := t.Context()
	invalidSeal := f.Seal
	invalidSeal.QualificationEvidence = []byte(`[]`)
	tx, err := r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CompleteBuildTx(ctx, tx, f.Commit, invalidSeal, testDigest('3'), f.fence()); err == nil {
		_ = tx.Rollback(ctx)
		t.Fatal("invalid seal unexpectedly completed")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	attempt, err := r.BuildAttempt(ctx, f.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != AttemptRunning {
		t.Fatalf("rolled-back attempt state = %s", attempt.State)
	}
	candidate, err := r.Candidate(ctx, f.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Status != "building" || candidate.SnapshotSealID != "" {
		t.Fatalf("rolled-back candidate = %#v", candidate)
	}
	if _, err := r.SnapshotSeal(ctx, f.SealID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rolled-back seal lookup = %v", err)
	}
	lease, err := r.Lease(ctx, f.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	if lease.State != "active" {
		t.Fatalf("rolled-back lease state = %s", lease.State)
	}
}
