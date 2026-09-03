package postgres

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
)

func successorResolutionEvidence(attemptID, poolID, catalogID, requestDigest, planDigest string) []byte {
	b, _ := json.Marshal(BuildAttemptMarkerResolutionEvidence{
		SchemaVersion: 1, PhysicalPoolID: poolID, CatalogID: catalogID, AttemptID: attemptID,
		RequestDigest: requestDigest, PlanDigest: planDigest, MarkerAbsent: true,
		ResolvedAt: time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC),
	})
	return b
}

func TestPostgresAdmitSuccessorBuildAttemptRequiresMarkerResolutionAndFencesPredecessor(t *testing.T) {
	r := New(deliveryTestDB(t))
	f := newCompleteBuildFixtureWithSuffixBindingAndLifetime(t, r, "9", false, time.Hour)
	ctx := t.Context()
	successorAttemptID := "0198f2c0-7c7a-7f00-0000-00000000a003"
	successorLeaseID := "0198f2c0-7c7a-7f00-0000-00000000a005"
	successorExpires := time.Now().UTC().Add(time.Hour)
	input := BuildAttemptSuccessorInput{
		Predecessor:          f.fence(),
		PredecessorAttemptID: f.AttemptID,
		CatalogID:            "catalog-complete",
		ResolutionEvidence:   successorResolutionEvidence(f.AttemptID, "pool-complete", "catalog-complete", f.RequestDigest, f.PlanDigest),
		SuccessorLease:       LeaseInput{LeaseID: successorLeaseID, TargetID: f.TargetID, OwnerID: "builder-successor", ExpiresAt: successorExpires},
		SuccessorAttempt: BuildAttemptInput{
			AttemptID: successorAttemptID, PlanID: f.PlanID, CandidateID: f.CandidateID,
			OwnerID: "builder-successor", PhysicalPoolID: "pool-complete", CatalogID: "catalog-complete",
			RequestDigest: f.RequestDigest, PlanDigest: f.PlanDigest, SessionIdentity: "session-successor",
		},
	}

	// A lease timeout or arbitrary evidence cannot authorize a successor.
	bad := input
	bad.ResolutionEvidence = []byte(`{"reason":"lease expired"}`)
	tx, err := r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AdmitSuccessorBuildAttemptTx(ctx, tx, bad); !errors.Is(err, ErrConflict) && !errors.Is(err, ErrInvalid) {
		_ = tx.Rollback(ctx)
		t.Fatalf("timeout-only successor error = %v", err)
	}
	_ = tx.Rollback(ctx)

	// Marker absence does not itself transition a running predecessor to
	// indeterminate. The owner/reconciler must make that decision first.
	runningTx, err := r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AdmitSuccessorBuildAttemptTx(ctx, runningTx, input); !errors.Is(err, ErrConflict) {
		_ = runningTx.Rollback(ctx)
		t.Fatalf("running predecessor successor admission = %v, want conflict", err)
	}
	_ = runningTx.Rollback(ctx)
	predecessorEvidence := []byte(`{"reason":"session outcome unavailable"}`)
	if _, err := r.MarkAttemptIndeterminate(ctx, TerminateAttemptInput{AttemptID: f.AttemptID, OwnerID: f.Lease.OwnerID, FencingEpoch: f.Lease.FencingEpoch, Evidence: predecessorEvidence}); err != nil {
		t.Fatalf("mark predecessor indeterminate: %v", err)
	}
	// Resolution evidence is typed, exact, and bound to the predecessor's
	// physical pool/catalog/request/plan identities. Unknown fields are not a
	// metadata escape hatch.
	unknown := input
	unknown.ResolutionEvidence = json.RawMessage(string(input.ResolutionEvidence[:len(input.ResolutionEvidence)-1]) + `,"unknown":true}`)
	unknownTx, err := r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AdmitSuccessorBuildAttemptTx(ctx, unknownTx, unknown); !errors.Is(err, ErrInvalid) {
		_ = unknownTx.Rollback(ctx)
		t.Fatalf("unknown resolution field = %v, want invalid", err)
	}
	_ = unknownTx.Rollback(ctx)
	wrongCatalog := input
	wrongCatalog.CatalogID = "catalog-other"
	wrongCatalogTx, err := r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AdmitSuccessorBuildAttemptTx(ctx, wrongCatalogTx, wrongCatalog); !errors.Is(err, ErrConflict) {
		_ = wrongCatalogTx.Rollback(ctx)
		t.Fatalf("wrong catalog resolution identity = %v, want conflict", err)
	}
	_ = wrongCatalogTx.Rollback(ctx)

	tx, err = r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.AdmitSuccessorBuildAttemptTx(ctx, tx, input)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("successor admission: %v", err)
	}
	if first.Predecessor.State != AttemptIndeterminate || first.Predecessor.CatalogID != input.CatalogID || first.Successor.State != AttemptRunning || !sameCanonical(first.Predecessor.TerminationEvidence, predecessorEvidence) {
		t.Fatalf("successor states predecessor=%s successor=%s", first.Predecessor.State, first.Successor.State)
	}
	if first.Successor.AttemptID != successorAttemptID || first.Successor.CatalogID != input.CatalogID || first.SuccessorLease.LeaseID != successorLeaseID || first.Successor.FencingEpoch <= f.Lease.FencingEpoch || first.Successor.Namespace == first.Predecessor.Namespace || first.Successor.SessionIdentity == first.Predecessor.SessionIdentity {
		t.Fatalf("successor identity predecessor=%#v successor=%#v lease=%#v", first.Predecessor, first.Successor, first.SuccessorLease)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// A late predecessor commit is fenced by its terminal state and the
	// immutable predecessor-successor edge. Recovery reconciliation is fenced
	// by the same edge, so a late marker can only be handled by orphan cleanup.
	marker := testCommitMarker(f.AttemptID, "pool-complete", f.RequestDigest, f.PlanDigest)
	if _, err := r.CommitBuildAttempt(ctx, CommitAttemptInput{AttemptID: f.AttemptID, OwnerID: f.Lease.OwnerID, FencingEpoch: f.Lease.FencingEpoch, SnapshotID: 7, CommitMarker: marker}); !errors.Is(err, ErrConflict) {
		t.Fatalf("late predecessor commit = %v, want conflict", err)
	}
	if _, err := r.ReconcileBuildAttempt(ctx, ReconcileBuildAttemptInput{AttemptID: f.AttemptID, OwnerID: f.Lease.OwnerID, FencingEpoch: f.Lease.FencingEpoch, SnapshotID: 7, CommitMarker: marker, State: AttemptCommitted}); !errors.Is(err, ErrConflict) {
		t.Fatalf("late predecessor reconcile = %v, want conflict", err)
	}
	if _, err := r.db.Exec(ctx, `UPDATE delivery.delivery_build_attempt_successor SET resolution_evidence='{}'::jsonb WHERE predecessor_attempt_id=$1::uuid`, f.AttemptID); err == nil {
		t.Fatal("successor link update succeeded")
	}
	if _, err := r.db.Exec(ctx, `DELETE FROM delivery.delivery_build_attempt_successor WHERE predecessor_attempt_id=$1::uuid`, f.AttemptID); err == nil {
		t.Fatal("successor link delete succeeded")
	}

	// Replaying the exact transaction returns the same successor identities;
	// changing any immutable successor input conflicts without mutation.
	replayTx, err := r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := r.AdmitSuccessorBuildAttemptTx(ctx, replayTx, input)
	if err != nil {
		_ = replayTx.Rollback(ctx)
		t.Fatalf("successor replay: %v", err)
	}
	if replayed.Successor.AttemptID != first.Successor.AttemptID || replayed.Successor.CatalogID != first.Successor.CatalogID || replayed.Successor.FencingEpoch != first.Successor.FencingEpoch || replayed.Successor.Namespace != first.Successor.Namespace || replayed.SuccessorLease.LeaseID != first.SuccessorLease.LeaseID {
		t.Fatalf("successor replay drifted: first=%#v replay=%#v", first, replayed)
	}
	if err := replayTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	changed := input
	changed.SuccessorAttempt.AttemptID = "0198f2c0-7c7a-7f00-0000-00000000a004"
	conflictTx, err := r.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AdmitSuccessorBuildAttemptTx(ctx, conflictTx, changed); !errors.Is(err, ErrConflict) {
		_ = conflictTx.Rollback(ctx)
		t.Fatalf("changed successor replay = %v, want conflict", err)
	}
	_ = conflictTx.Rollback(ctx)
}

// Keep the deployment namespace derivation in the test package's import set;
// this assertion also guards against an accidental candidate-only namespace.
func TestSuccessorNamespaceDerivationIsAttemptAndFenceQualified(t *testing.T) {
	a, err := deployment.DeriveRelationNamespace(deployment.RelationNamespaceInput{CandidateID: "0198f2c0-7c7a-7f00-0000-00000000a002", AttemptID: "0198f2c0-7c7a-7f00-0000-00000000a003", FencingEpoch: 2})
	if err != nil {
		t.Fatal(err)
	}
	b, err := deployment.DeriveRelationNamespace(deployment.RelationNamespaceInput{CandidateID: "0198f2c0-7c7a-7f00-0000-00000000a002", AttemptID: "0198f2c0-7c7a-7f00-0000-00000000a004", FencingEpoch: 2})
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("successor namespace reused predecessor namespace %q", a)
	}
}
