package postgres

import (
	"errors"
	"fmt"
	"strings"
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
	return newCompleteBuildFixtureWithSuffixBindingAndLifetime(t, r, suffix, true, time.Hour)
}

func newCompleteBuildFixtureWithoutBinding(t *testing.T, r *Repository, suffix string) completeBuildFixture {
	return newCompleteBuildFixtureWithSuffixBindingAndLifetime(t, r, suffix, false, time.Hour)
}

func newCompleteBuildFixtureWithSuffixBindingAndLifetime(t *testing.T, r *Repository, suffix string, bind bool, leaseLifetime time.Duration) completeBuildFixture {
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
	rich, planDocument := richPlanDocumentFixture(t, f.PlanID, f.TargetID, "project_complete_build_"+suffix)
	f.PlanDigest = rich.Digest
	if _, err := r.CreateTarget(ctx, TargetInput{TargetID: f.TargetID, ProjectID: "project_complete_build_" + suffix, Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreatePlan(ctx, PlanInput{PlanID: f.PlanID, TargetID: f.TargetID, PlanRevision: 1, PlanDigest: f.PlanDigest, CompiledGraphDigest: testDigest('b'), CompiledConfigDigest: rich.Execution.ConfigDigest, SecurityDomainFingerprint: testDigest('d'), ArtifactDigest: f.ArtifactDigest, QualificationDigest: rich.Governance.QualificationDigest, ApprovalRequired: rich.Governance.RequiresApproval, ApprovalPolicyRevision: rich.Governance.ApprovalPolicyRevision, PlanDocument: planDocument}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateCandidate(ctx, CandidateInput{CandidateID: f.CandidateID, TargetID: f.TargetID, PlanID: f.PlanID, CandidateRevision: 1, ArtifactDigest: f.ArtifactDigest}); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().UTC().Add(leaseLifetime)
	tx, err := r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.Lease, _, err = r.AcquireLeaseAndBeginBuildAttemptTx(ctx, tx,
		LeaseInput{LeaseID: f.LeaseID, TargetID: f.TargetID, OwnerID: "builder-complete", ExpiresAt: expires},
		BuildAttemptInput{AttemptID: f.AttemptID, PlanID: f.PlanID, CandidateID: f.CandidateID, OwnerID: "builder-complete", PhysicalPoolID: "pool-complete", CatalogID: "catalog-complete", RequestDigest: f.RequestDigest, PlanDigest: f.PlanDigest, Namespace: "candidate/complete", SessionIdentity: "session-complete"},
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
	if bind {
		if _, err := r.BindBuildArtifact(ctx, BuildArtifactBindingInput{AttemptID: f.AttemptID, ServingArtifactID: f.Seal.ServingArtifactID, ServingArtifactDigest: f.Seal.ServingArtifactDigest, ServingStateID: "generation-test", OwnerID: f.Lease.OwnerID, FencingEpoch: f.Lease.FencingEpoch}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f completeBuildFixture) fence() LeaseFence {
	return LeaseFence{LeaseID: f.Lease.LeaseID, TargetID: f.Lease.TargetID, OwnerID: f.Lease.OwnerID, FencingEpoch: f.Lease.FencingEpoch}
}

func TestPostgresRenewLeaseTxCannotShortenActiveLease(t *testing.T) {
	r := New(deliveryTestDB(t))
	f := newCompleteBuildFixture(t, r)
	shorter := f.Lease.ExpiresAt.Add(-time.Minute)
	if !shorter.After(time.Now().UTC()) {
		t.Fatal("fixture lease is too short for monotonic renewal test")
	}
	tx, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if err := r.RenewLeaseTx(t.Context(), tx, f.fence(), shorter); !errors.Is(err, ErrConflict) {
		t.Fatalf("shortening renewal error = %v, want conflict", err)
	}
	lease, err := r.LeaseTx(t.Context(), tx, f.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	if !lease.ExpiresAt.Equal(f.Lease.ExpiresAt) {
		t.Fatalf("lease expiry changed from %v to %v", f.Lease.ExpiresAt, lease.ExpiresAt)
	}
}

func TestPostgresReleaseLeaseAfterAttemptTerminationAcceptsExactExpiredLease(t *testing.T) {
	r := New(deliveryTestDB(t))
	f := newCompleteBuildFixtureWithSuffixBindingAndLifetime(t, r, "8", true, 2*time.Second)
	if wait := time.Until(f.Lease.ExpiresAt) + 10*time.Millisecond; wait > 0 {
		time.Sleep(wait)
	}
	tx, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if err := r.ReleaseLeaseAfterAttemptTerminationTx(t.Context(), tx, f.fence()); err != nil {
		t.Fatalf("release exact expired lease: %v", err)
	}
	lease, err := r.LeaseTx(t.Context(), tx, f.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	if lease.State != "released" || lease.ReleasedAt.IsZero() {
		t.Fatalf("terminated lease = %+v, want released", lease)
	}
	stale := f.fence()
	stale.OwnerID = "other-owner"
	if err := r.ReleaseLeaseAfterAttemptTerminationTx(t.Context(), tx, stale); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale termination release error = %v, want stale fence", err)
	}
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
	if first.Attempt.State != AttemptCommitted || first.Attempt.CatalogID != f.Seal.CatalogID || first.Candidate.Status != "qualified" || first.Lease.State != "released" {
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
	if replayed.Attempt.State != AttemptCommitted || replayed.Attempt.CatalogID != f.Seal.CatalogID || replayed.Candidate.Status != "qualified" || replayed.Lease.State != "released" {
		t.Fatalf("replayed completion result = %#v", replayed)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(ctx, `UPDATE delivery.delivery_build_attempt SET catalog_id='catalog-other' WHERE attempt_id=$1::uuid`, f.AttemptID); err == nil {
		t.Fatal("build attempt catalog identity update succeeded")
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

func TestPostgresBuildArtifactBindingReplayFenceAndTerminalRules(t *testing.T) {
	r := New(deliveryTestDB(t))
	f := newCompleteBuildFixtureWithoutBinding(t, r, "4")
	ctx := t.Context()
	in := BuildArtifactBindingInput{
		AttemptID: f.AttemptID, ServingArtifactID: f.Seal.ServingArtifactID,
		ServingArtifactDigest: f.Seal.ServingArtifactDigest, ServingStateID: "generation-test",
		OwnerID: f.Lease.OwnerID, FencingEpoch: f.Lease.FencingEpoch,
	}
	first, err := r.BindBuildArtifact(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := r.BindBuildArtifact(ctx, in)
	if err != nil || replayed.AttemptID != first.AttemptID || replayed.ServingArtifactID != first.ServingArtifactID || replayed.ServingStateID != first.ServingStateID {
		t.Fatalf("binding replay = %#v, %v", replayed, err)
	}
	conflict := in
	conflict.ServingArtifactID = "artifact-other"
	if _, err := r.BindBuildArtifact(ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign binding identity = %v", err)
	}
	wrongOwner := in
	wrongOwner.OwnerID = "another-builder"
	if _, err := r.BindBuildArtifact(ctx, wrongOwner); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("owner fence validation = %v", err)
	}
	rollbackFixture := newCompleteBuildFixtureWithoutBinding(t, r, "5")
	rollbackInput := in
	rollbackInput.AttemptID = rollbackFixture.AttemptID
	rollbackInput.OwnerID = rollbackFixture.Lease.OwnerID
	rollbackInput.FencingEpoch = rollbackFixture.Lease.FencingEpoch
	tx, err := r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.BindBuildArtifactTx(ctx, tx, rollbackInput); err != nil || got.AttemptID != rollbackInput.AttemptID {
		_ = tx.Rollback(ctx)
		t.Fatalf("caller-owned binding = %#v, %v", got, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BuildArtifactBinding(ctx, rollbackFixture.AttemptID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rolled-back binding = %v", err)
	}

	// Once the attempt is terminal, the exact binding remains replayable while
	// a foreign identity is still rejected.
	if _, err := r.CommitBuildAttempt(ctx, f.Commit); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BindBuildArtifact(ctx, in); err != nil {
		t.Fatalf("terminal exact binding replay = %v", err)
	}
	terminalConflict := in
	terminalConflict.ServingStateID = "generation-other"
	if _, err := r.BindBuildArtifact(ctx, terminalConflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal foreign identity = %v", err)
	}

	terminalUnbound := newCompleteBuildFixtureWithoutBinding(t, r, "6")
	if _, err := r.CommitBuildAttempt(ctx, terminalUnbound.Commit); err != nil {
		t.Fatal(err)
	}
	terminalUnboundInput := in
	terminalUnboundInput.AttemptID = terminalUnbound.AttemptID
	terminalUnboundInput.OwnerID = terminalUnbound.Lease.OwnerID
	terminalUnboundInput.FencingEpoch = terminalUnbound.Lease.FencingEpoch
	if _, err := r.BindBuildArtifact(ctx, terminalUnboundInput); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal first binding = %v", err)
	}

	expired := newCompleteBuildFixtureWithSuffixBindingAndLifetime(t, r, "7", false, 2*time.Second)
	time.Sleep(2100 * time.Millisecond)
	expiredInput := in
	expiredInput.AttemptID = expired.AttemptID
	expiredInput.OwnerID = expired.Lease.OwnerID
	expiredInput.FencingEpoch = expired.Lease.FencingEpoch
	if _, err := r.BindBuildArtifact(ctx, expiredInput); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("expired first binding = %v", err)
	}

	if _, err := r.BindBuildArtifact(ctx, BuildArtifactBindingInput{AttemptID: "0198f2c0-7c7a-7f00-0000-00000000f003", ServingArtifactID: "artifact", ServingArtifactDigest: testDigest('a'), ServingStateID: "state", OwnerID: "owner", FencingEpoch: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing attempt binding = %v", err)
	}
	invalid := in
	invalid.ServingArtifactDigest = "not-a-digest"
	if _, err := r.BindBuildArtifact(ctx, invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid binding digest = %v", err)
	}
	invalid = in
	invalid.ServingStateID = "state with spaces"
	if _, err := r.BindBuildArtifact(ctx, invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid serving-state identity = %v", err)
	}

	if _, err := r.db.Exec(ctx, "UPDATE delivery.delivery_build_artifact_binding SET serving_state_id = 'changed' WHERE attempt_id = $1::uuid", f.AttemptID); err == nil {
		t.Fatal("immutable binding update succeeded")
	}
	if _, err := r.db.Exec(ctx, "DELETE FROM delivery.delivery_build_artifact_binding WHERE attempt_id = $1::uuid", f.AttemptID); err == nil {
		t.Fatal("immutable binding delete succeeded")
	}
}

func recoveredArtifactBindingInput(f completeBuildFixture) RecoveredBuildArtifactBindingInput {
	return RecoveredBuildArtifactBindingInput{
		AttemptID: f.AttemptID, ServingArtifactID: f.Seal.ServingArtifactID,
		ServingArtifactDigest: f.Seal.ServingArtifactDigest, ServingStateID: "generation-test",
		OwnerID: f.Lease.OwnerID, FencingEpoch: f.Lease.FencingEpoch,
		CommitMarker: f.Commit.CommitMarker,
	}
}

func TestPostgresRecoveredBuildArtifactBindingRules(t *testing.T) {
	r := New(deliveryTestDB(t))
	ctx := t.Context()

	// A running attempt cannot bypass the ordinary active-lease binding path.
	running := newCompleteBuildFixtureWithoutBinding(t, r, "a")
	tx, err := r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.BindRecoveredBuildArtifactTx(ctx, tx, recoveredArtifactBindingInput(running)); !errors.Is(err, ErrConflict) {
		_ = tx.Rollback(ctx)
		t.Fatalf("running recovered binding error = %v, want conflict", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BuildArtifactBinding(ctx, running.AttemptID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("running recovery created binding: %v", err)
	}
	runningBound := newCompleteBuildFixtureWithSuffix(t, r, "7")
	tx, err = r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.BindRecoveredBuildArtifactTx(ctx, tx, recoveredArtifactBindingInput(runningBound)); !errors.Is(err, ErrConflict) {
		_ = tx.Rollback(ctx)
		t.Fatalf("running recovered replay error = %v, want conflict", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	// Recovery does not require the build lease to remain active. Move an
	// indeterminate attempt's lease into the past before inserting its binding.
	indeterminate := newCompleteBuildFixtureWithSuffixBindingAndLifetime(t, r, "b", false, 100*time.Millisecond)
	if wait := time.Until(indeterminate.Lease.ExpiresAt) + 20*time.Millisecond; wait > 0 {
		time.Sleep(wait)
	}
	if _, err := r.MarkAttemptIndeterminate(ctx, TerminateAttemptInput{
		AttemptID: indeterminate.AttemptID, OwnerID: indeterminate.Lease.OwnerID,
		FencingEpoch: indeterminate.Lease.FencingEpoch, Evidence: []byte(`{"phase":"unknown"}`),
	}); err != nil {
		t.Fatal(err)
	}
	tx, err = r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.BindRecoveredBuildArtifactTx(ctx, tx, recoveredArtifactBindingInput(indeterminate)); err != nil || got.AttemptID != indeterminate.AttemptID {
		_ = tx.Rollback(ctx)
		t.Fatalf("expired indeterminate recovered binding = %#v, %v", got, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// Exact replay remains valid once the attempt is committed.
	terminal := newCompleteBuildFixtureWithSuffix(t, r, "c")
	if _, err := r.CommitBuildAttempt(ctx, terminal.Commit); err != nil {
		t.Fatal(err)
	}
	tx, err = r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.BindRecoveredBuildArtifactTx(ctx, tx, recoveredArtifactBindingInput(terminal)); err != nil || got.AttemptID != terminal.AttemptID {
		_ = tx.Rollback(ctx)
		t.Fatalf("terminal exact recovered replay = %#v, %v", got, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	terminalMarkerDrift := recoveredArtifactBindingInput(terminal)
	terminalMarkerDrift.CommitMarker = []byte(strings.Replace(string(terminalMarkerDrift.CommitMarker), `"project":"project-test"`, `"project":"project-other"`, 1))
	tx, err = r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.BindRecoveredBuildArtifactTx(ctx, tx, terminalMarkerDrift); !errors.Is(err, ErrConflict) {
		_ = tx.Rollback(ctx)
		t.Fatalf("committed full-marker drift error = %v, want conflict", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	// Marker identity, artifact identity, and owner/fence mismatches are
	// rejected without creating or changing a binding.
	mismatch := newCompleteBuildFixtureWithoutBinding(t, r, "d")
	base := recoveredArtifactBindingInput(mismatch)
	markerMismatch := base
	markerMismatch.CommitMarker = testCommitMarker(mismatch.AttemptID, "pool-other", mismatch.RequestDigest, mismatch.PlanDigest)
	markerMismatch.CommitMarker = []byte(strings.Replace(string(markerMismatch.CommitMarker), `"generation-test"`, `"generation-other"`, 1))
	markerAttempt := base
	markerAttempt.CommitMarker = testCommitMarker("0198f2c0-7c7a-7f00-0000-00000000ffffff", "pool-complete", mismatch.RequestDigest, mismatch.PlanDigest)
	markerPlan := base
	markerPlan.CommitMarker = testCommitMarker(mismatch.AttemptID, "pool-complete", mismatch.RequestDigest, testDigest('c'))
	ownerMismatch := base
	ownerMismatch.OwnerID = "another-builder"
	fenceMismatch := base
	fenceMismatch.FencingEpoch++
	for name, input := range map[string]RecoveredBuildArtifactBindingInput{
		"marker identity": markerMismatch, "marker attempt": markerAttempt,
		"marker plan": markerPlan,
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := r.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.BindRecoveredBuildArtifactTx(ctx, tx, input); !errors.Is(err, ErrConflict) {
				_ = tx.Rollback(ctx)
				t.Fatalf("error = %v, want conflict", err)
			}
			if err := tx.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
	for name, input := range map[string]RecoveredBuildArtifactBindingInput{"owner": ownerMismatch, "fence": fenceMismatch} {
		t.Run(name, func(t *testing.T) {
			tx, err := r.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.BindRecoveredBuildArtifactTx(ctx, tx, input); !errors.Is(err, ErrStaleFence) {
				_ = tx.Rollback(ctx)
				t.Fatalf("error = %v, want stale fence", err)
			}
			if err := tx.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}

	// A terminal attempt without a prior binding cannot be repaired by this
	// seam; rollback leaves the immutable binding absent.
	missing := newCompleteBuildFixtureWithoutBinding(t, r, "e")
	if _, err := r.CommitBuildAttempt(ctx, missing.Commit); err != nil {
		t.Fatal(err)
	}
	tx, err = r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.BindRecoveredBuildArtifactTx(ctx, tx, recoveredArtifactBindingInput(missing)); !errors.Is(err, ErrConflict) {
		_ = tx.Rollback(ctx)
		t.Fatalf("terminal missing binding error = %v, want conflict", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BuildArtifactBinding(ctx, missing.AttemptID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("terminal missing binding after rollback = %v", err)
	}

	// An existing immutable row cannot be replaced by a recovered artifact.
	immutable := newCompleteBuildFixtureWithSuffix(t, r, "f")
	conflict := recoveredArtifactBindingInput(immutable)
	conflict.ServingArtifactDigest = testDigest('d')
	tx, err = r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.BindRecoveredBuildArtifactTx(ctx, tx, conflict); !errors.Is(err, ErrConflict) {
		_ = tx.Rollback(ctx)
		t.Fatalf("immutable exact conflict = %v, want conflict", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresBuildCompletionRequiresArtifactBindingIdentity(t *testing.T) {
	ctx := t.Context()
	r := New(deliveryTestDB(t))

	missing := newCompleteBuildFixtureWithoutBinding(t, r, "3")
	if _, err := r.CommitBuildAttempt(ctx, missing.Commit); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateSnapshotSeal(ctx, missing.Seal); !errors.Is(err, ErrNotQualified) {
		t.Fatalf("missing binding seal = %v", err)
	}

	mismatch := newCompleteBuildFixture(t, r)
	wrongMarker := mismatch.Commit
	wrongMarker.CommitMarker = []byte(strings.Replace(string(wrongMarker.CommitMarker), `"generation-test"`, `"generation-other"`, 1))
	tx, err := r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CompleteBuildTx(ctx, tx, wrongMarker, mismatch.Seal, testDigest('3'), mismatch.fence()); !errors.Is(err, ErrConflict) {
		_ = tx.Rollback(ctx)
		t.Fatalf("mismatched marker generation = %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CommitBuildAttempt(ctx, mismatch.Commit); err != nil {
		t.Fatal(err)
	}
	wrongSeal := mismatch.Seal
	wrongSeal.ServingArtifactID = "artifact-other"
	if _, err := r.CreateSnapshotSeal(ctx, wrongSeal); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched binding artifact = %v", err)
	}
	wrongCatalog := mismatch.Seal
	wrongCatalog.CatalogID = "catalog-other"
	if _, err := r.CreateSnapshotSeal(ctx, wrongCatalog); !errors.Is(err, ErrNotQualified) {
		t.Fatalf("mismatched attempt catalog = %v, want ErrNotQualified", err)
	}
}
