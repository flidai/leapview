package deploymentpostgres

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
)

func TestAdmitNativeBuildSuccessorReplaysExactIdentity(t *testing.T) {
	f := newNativeRecoveryPreparationFixtureMode(t, false)
	attempt, err := f.Delivery.LoadBuildAttempt(t.Context(), f.Input.Operation.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := f.Delivery.LoadBuildArtifactBinding(t.Context(), attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	leaseID, err := nativeBuildConsequenceID(f.Record.OperationID, "lease")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.Delivery.Lease(t.Context(), leaseID)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := json.Marshal(deploymentnative.BuildAttemptMarkerResolutionEvidence{
		SchemaVersion: 1, PhysicalPoolID: attempt.PhysicalPoolID, CatalogID: f.Input.CatalogID,
		AttemptID: attempt.AttemptID, RequestDigest: attempt.RequestDigest, PlanDigest: attempt.PlanDigest,
		MarkerAbsent: true, ResolvedAt: time.Now().UTC().Truncate(time.Microsecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	duckLake, err := NewCandidateBuildAttemptAdmission(f.Delivery, f.DuckLake)
	if err != nil {
		t.Fatal(err)
	}
	input := NativeBuildSuccessorAdmissionInput{Operation: f.Record, DeliveryAttempt: attempt, DeliveryLease: lease, Artifact: CandidateBuildArtifactInput{ServingArtifactID: artifact.ServingArtifactID, ServingArtifactDigest: artifact.ServingArtifactDigest, ServingStateID: artifact.ServingStateID}, CatalogID: f.Input.CatalogID, Resolution: resolution, DuckLake: duckLake.(CandidateBuildAttemptSuccessorDuckLakeAdmission)}
	first, err := AdmitNativeBuildSuccessor(t.Context(), f.Delivery, f.Operation.(deploymentmodule.NativeBuildOperationSuccessorAuthority), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := AdmitNativeBuildSuccessor(t.Context(), f.Delivery, f.Operation.(deploymentmodule.NativeBuildOperationSuccessorAuthority), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Operation.AttemptID != second.Operation.AttemptID || first.Operation.Lease.FencingGeneration != second.Operation.Lease.FencingGeneration || !first.Operation.Lease.LeaseExpiresAt.Equal(second.Operation.Lease.LeaseExpiresAt) || first.Delivery.Successor.AttemptID != second.Delivery.Successor.AttemptID || first.Delivery.Successor.Namespace != second.Delivery.Successor.Namespace || first.Delivery.Successor.FencingEpoch != second.Delivery.Successor.FencingEpoch {
		t.Fatalf("successor replay drifted: first=%#v second=%#v", first, second)
	}
	if first.Operation.AttemptID == f.Record.AttemptID || first.Delivery.Successor.SessionIdentity == attempt.SessionIdentity || first.Delivery.Successor.Namespace == attempt.Namespace {
		t.Fatalf("successor reused predecessor identity: %#v", first)
	}
}

func TestAdmitNativeBuildSuccessorRejectsNonAbsentResolutionWithoutMutation(t *testing.T) {
	f := newNativeRecoveryPreparationFixtureMode(t, false)
	attempt, err := f.Delivery.LoadBuildAttempt(t.Context(), f.Input.Operation.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	leaseID, err := nativeBuildConsequenceID(f.Record.OperationID, "lease")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.Delivery.Lease(t.Context(), leaseID)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := f.Delivery.LoadBuildArtifactBinding(t.Context(), attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	duckLake, err := NewCandidateBuildAttemptAdmission(f.Delivery, f.DuckLake)
	if err != nil {
		t.Fatal(err)
	}
	input := NativeBuildSuccessorAdmissionInput{Operation: f.Record, DeliveryAttempt: attempt, DeliveryLease: lease, Artifact: CandidateBuildArtifactInput{ServingArtifactID: artifact.ServingArtifactID, ServingArtifactDigest: artifact.ServingArtifactDigest, ServingStateID: artifact.ServingStateID}, CatalogID: f.Input.CatalogID, Resolution: []byte(`{"schema_version":1,"marker_absent":false}`), DuckLake: duckLake.(CandidateBuildAttemptSuccessorDuckLakeAdmission)}
	_, err = AdmitNativeBuildSuccessor(t.Context(), f.Delivery, f.Operation.(deploymentmodule.NativeBuildOperationSuccessorAuthority), input)
	if !errors.Is(err, deploymentnative.ErrInvalid) && !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("invalid marker resolution error = %v, want invalid/conflict", err)
	}
	if _, found, readErr := f.Operation.(deploymentmodule.NativeBuildOperationSuccessorAuthority).CurrentSuccessorAttempt(t.Context(), f.Record.OperationID); readErr != nil || found {
		t.Fatalf("invalid resolution left operation successor: found=%v err=%v", found, readErr)
	}
}

func TestAdmitNativeBuildSuccessorAppendsSecondGenerationAndReplays(t *testing.T) {
	f := newNativeRecoveryPreparationFixtureMode(t, false)
	attempt, err := f.Delivery.LoadBuildAttempt(t.Context(), f.Input.Operation.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := f.Delivery.LoadBuildArtifactBinding(t.Context(), attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	rootLeaseID, err := nativeBuildConsequenceID(f.Record.OperationID, "lease")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.Delivery.Lease(t.Context(), rootLeaseID)
	if err != nil {
		t.Fatal(err)
	}
	duckLake, err := NewCandidateBuildAttemptAdmission(f.Delivery, f.DuckLake)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := json.Marshal(deploymentnative.BuildAttemptMarkerResolutionEvidence{
		SchemaVersion: 1, PhysicalPoolID: attempt.PhysicalPoolID, CatalogID: f.Input.CatalogID,
		AttemptID: attempt.AttemptID, RequestDigest: attempt.RequestDigest, PlanDigest: attempt.PlanDigest,
		MarkerAbsent: true, ResolvedAt: time.Now().UTC().Truncate(time.Microsecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	rootInput := NativeBuildSuccessorAdmissionInput{
		Operation: f.Record, DeliveryAttempt: attempt, DeliveryLease: lease,
		Artifact:  CandidateBuildArtifactInput{ServingArtifactID: artifact.ServingArtifactID, ServingArtifactDigest: artifact.ServingArtifactDigest, ServingStateID: artifact.ServingStateID},
		CatalogID: f.Input.CatalogID, Resolution: resolution,
		DuckLake: duckLake.(CandidateBuildAttemptSuccessorDuckLakeAdmission),
	}
	first, err := AdmitNativeBuildSuccessor(t.Context(), f.Delivery, f.Operation.(deploymentmodule.NativeBuildOperationSuccessorAuthority), rootInput)
	if err != nil {
		t.Fatal(err)
	}
	// An active pending leaf is owned by the in-flight executor. Recovery must
	// report busy and leave the operation, delivery attempt, and target lease
	// untouched rather than marking it indeterminate behind the owner's back.
	firstAttempt, err := f.Delivery.LoadBuildAttempt(t.Context(), first.Delivery.Successor.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	firstLease, err := f.Delivery.Lease(t.Context(), first.Delivery.SuccessorLease.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	termination, err := NewAttemptTermination(f.Delivery, f.DuckLake)
	if err != nil {
		t.Fatal(err)
	}
	leafPrepared := NativeBuildRecoveryPreparationResult{Operation: f.Record, DeliveryAttempt: firstAttempt, Lease: firstLease, AttemptID: firstAttempt.AttemptID, LeaseID: firstLease.LeaseID}
	leafPrepared.Operation.Scope = first.Operation.Lease.Scope
	leafPrepared.Operation.IdempotencyKey = first.Operation.Lease.IdempotencyKey
	leafPrepared.Operation.OperationID = first.Operation.Lease.OperationID
	leafPrepared.Operation.OwnerID = first.Operation.Lease.OwnerID
	leafPrepared.Operation.AttemptID, leafPrepared.Operation.AttemptIdentity = first.Operation.AttemptID, first.Operation.AttemptIdentity
	leafPrepared.Operation.FencingGeneration, leafPrepared.Operation.LeaseExpiresAt = first.Operation.Lease.FencingGeneration, first.Operation.Lease.LeaseExpiresAt
	busyCoordinator := &NativeBuildCoordinator{repository: f.Delivery, operations: f.Operation, attemptTermination: termination, clock: time.Now}
	if err := normalizeSuccessorLeafForRecovery(t.Context(), busyCoordinator, f.Operation.(deploymentmodule.NativeBuildOperationSuccessorLockAuthority), leafPrepared, f.Record, f.Digest); !errors.Is(err, deploymentmodule.ErrNativeOperationBusy) {
		t.Fatalf("active pending successor recovery error = %v, want busy", err)
	}
	currentPending, found, err := f.Operation.(deploymentmodule.NativeBuildOperationSuccessorAuthority).CurrentSuccessorAttempt(t.Context(), f.Record.OperationID)
	if err != nil || !found || currentPending.State != deploymentmodule.NativeOperationStatePending {
		t.Fatalf("active pending successor mutated: current=%#v found=%v err=%v", currentPending, found, err)
	}
	if got, err := f.Delivery.LoadBuildAttempt(t.Context(), firstAttempt.AttemptID); err != nil || got.State != deploymentnative.AttemptRunning {
		t.Fatalf("active pending delivery attempt mutated: got=%#v err=%v", got, err)
	}
	if got, err := f.Delivery.Lease(t.Context(), firstLease.LeaseID); err != nil || got.State != "active" {
		t.Fatalf("active pending target lease mutated: got=%#v err=%v", got, err)
	}

	// Simulate the first successor's external work ending indeterminate, which
	// is the only state from which a second deterministic child may be added.
	evidence, err := json.Marshal(nativeBuildTerminationEvidence{
		SchemaVersion: 1, AttemptID: first.Delivery.Successor.AttemptID,
		OwnerID: first.Delivery.Successor.OwnerID, FencingEpoch: first.Delivery.Successor.FencingEpoch,
		RequestDigest: first.Delivery.Successor.RequestDigest, PlanDigest: first.Delivery.Successor.PlanDigest,
		PhysicalPoolID: first.Delivery.Successor.PhysicalPoolID, Namespace: first.Delivery.Successor.Namespace,
		SessionIdentity: first.Delivery.Successor.SessionIdentity, Phase: NativePhysicalBuildPhaseEvidence,
		Classification: NativePhysicalFailureIndeterminate, ErrorDigest: preparationDigest('z'),
	})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := termination.MarkAttemptIndeterminateTx(t.Context(), tx, AttemptTerminationInput{AttemptID: first.Delivery.Successor.AttemptID, OwnerID: first.Delivery.Successor.OwnerID, FencingEpoch: first.Delivery.Successor.FencingEpoch, Evidence: evidence}); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := f.Operation.MarkIndeterminateTx(t.Context(), tx, first.Operation.Lease, evidence); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	firstLease, err = f.Delivery.Lease(t.Context(), first.Delivery.SuccessorLease.LeaseID)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := f.Delivery.ReleaseLeaseAfterAttemptTerminationTx(t.Context(), tx, deploymentnative.LeaseFence{LeaseID: firstLease.LeaseID, TargetID: firstLease.TargetID, OwnerID: firstLease.OwnerID, FencingEpoch: firstLease.FencingEpoch}); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	predecessorOperation, found, err := f.Operation.(deploymentmodule.NativeBuildOperationSuccessorAuthority).CurrentSuccessorAttempt(t.Context(), f.Record.OperationID)
	if err != nil || !found {
		t.Fatalf("current first successor = %#v found=%v err=%v", predecessorOperation, found, err)
	}
	predecessorAttempt, err := f.Delivery.LoadBuildAttempt(t.Context(), first.Delivery.Successor.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	predecessorLease, err := f.Delivery.Lease(t.Context(), first.Delivery.SuccessorLease.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	secondResolution, err := json.Marshal(deploymentnative.BuildAttemptMarkerResolutionEvidence{
		SchemaVersion: 1, PhysicalPoolID: predecessorAttempt.PhysicalPoolID, CatalogID: f.Input.CatalogID,
		AttemptID: predecessorAttempt.AttemptID, RequestDigest: predecessorAttempt.RequestDigest, PlanDigest: predecessorAttempt.PlanDigest,
		MarkerAbsent: true, ResolvedAt: time.Now().UTC().Truncate(time.Microsecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	predecessorRecord := f.Record
	predecessorRecord.State = predecessorOperation.State
	predecessorRecord.FencingGeneration = predecessorOperation.Lease.FencingGeneration
	predecessorRecord.LeaseExpiresAt = predecessorOperation.Lease.LeaseExpiresAt
	predecessorRecord.AttemptID = predecessorOperation.AttemptID
	predecessorRecord.AttemptIdentity = predecessorOperation.AttemptIdentity
	predecessorRecord.AttemptEvidence = append(json.RawMessage(nil), predecessorOperation.AttemptEvidence...)
	secondInput := NativeBuildSuccessorAdmissionInput{
		Operation: predecessorRecord, DeliveryAttempt: predecessorAttempt, DeliveryLease: predecessorLease,
		Artifact:  CandidateBuildArtifactInput{ServingArtifactID: artifact.ServingArtifactID, ServingArtifactDigest: artifact.ServingArtifactDigest, ServingStateID: artifact.ServingStateID},
		CatalogID: f.Input.CatalogID, Resolution: secondResolution,
		DuckLake: duckLake.(CandidateBuildAttemptSuccessorDuckLakeAdmission),
	}
	second, err := AdmitNativeBuildSuccessor(t.Context(), f.Delivery, f.Operation.(deploymentmodule.NativeBuildOperationSuccessorAuthority), secondInput)
	if err != nil {
		t.Fatal(err)
	}
	expectedAttempt, err := nativeBuildSuccessorID(predecessorAttempt.AttemptID, "attempt")
	if err != nil {
		t.Fatal(err)
	}
	if second.Operation.AttemptID != expectedAttempt || second.Operation.PredecessorID != predecessorAttempt.AttemptID || second.Delivery.Successor.AttemptID != expectedAttempt {
		t.Fatalf("second successor identity = %#v, expected attempt %s", second, expectedAttempt)
	}
	replay, err := AdmitNativeBuildSuccessor(t.Context(), f.Delivery, f.Operation.(deploymentmodule.NativeBuildOperationSuccessorAuthority), secondInput)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Operation.AttemptID != second.Operation.AttemptID || replay.Delivery.Successor.AttemptID != second.Delivery.Successor.AttemptID {
		t.Fatalf("second successor replay appended or drifted: first=%#v replay=%#v", second, replay)
	}
	current, found, err := f.Operation.(deploymentmodule.NativeBuildOperationSuccessorAuthority).CurrentSuccessorAttempt(t.Context(), f.Record.OperationID)
	if err != nil || !found || current.AttemptID != expectedAttempt {
		t.Fatalf("current leaf after replay = %#v found=%v err=%v", current, found, err)
	}
}

func TestValidateNativeBuildSuccessorFenceChainRootsIndependentLedgers(t *testing.T) {
	tests := []struct {
		name                                  string
		rootOperationFence, rootDeliveryFence int64
		predecessorDepth                      int
		leafOperationFence, leafDeliveryFence int64
		wantErr                               bool
	}{
		// A prior unrelated target writer can advance delivery fencing without
		// changing this operation's root generation.
		{name: "prior target leases", rootOperationFence: 1, rootDeliveryFence: 7, predecessorDepth: 0, leafOperationFence: 2, leafDeliveryFence: 8},
		// A direct-mark root has no expiry takeover, so both roots start at one.
		{name: "direct mark root", rootOperationFence: 1, rootDeliveryFence: 1, predecessorDepth: 0, leafOperationFence: 2, leafDeliveryFence: 2},
		// Expiry takeover advances only the public operation root; both ledgers
		// still advance by exactly one for each successor edge.
		{name: "expiry root second child", rootOperationFence: 2, rootDeliveryFence: 1, predecessorDepth: 1, leafOperationFence: 4, leafDeliveryFence: 3},
		{name: "operation drift", rootOperationFence: 1, rootDeliveryFence: 7, predecessorDepth: 0, leafOperationFence: 3, leafDeliveryFence: 8, wantErr: true},
		{name: "delivery drift", rootOperationFence: 1, rootDeliveryFence: 7, predecessorDepth: 0, leafOperationFence: 2, leafDeliveryFence: 9, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateNativeBuildSuccessorFenceChain(tt.rootOperationFence, tt.rootDeliveryFence, tt.predecessorDepth, tt.leafOperationFence, tt.leafDeliveryFence)
			if (err != nil) != tt.wantErr {
				t.Fatalf("fence validation error = %v, wantErr=%v", err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, deploymentdomain.ErrDeliveryConflict) {
				t.Fatalf("fence validation error = %v, want delivery conflict", err)
			}
		})
	}
}
