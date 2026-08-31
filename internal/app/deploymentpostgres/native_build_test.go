package deploymentpostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/release"
	"github.com/google/uuid"
)

func TestNewNativeBuildCoordinatorFailsClosedWithoutRepository(t *testing.T) {
	_, err := NewNativeBuildCoordinator(NativeBuildConfig{})
	if err == nil {
		t.Fatal("constructor accepted an unconfigured native build")
	}
	// The constructor intentionally uses a descriptive error rather than a
	// package sentinel; assert the stable fail-closed message without
	// coupling this test to an implementation error value.
	if err.Error() != "native build requires a configured transaction-capable PostgreSQL repository" {
		t.Fatalf("unexpected constructor error: %v", err)
	}
}

func TestNativeBuildProjectionUsesAttemptAndPersistedArtifactIdentity(t *testing.T) {
	operationID := "0198f2c0-7c7a-7f00-8a11-000000001100"
	ids := make(map[string]string)
	for _, role := range []string{"candidate", "attempt", "lease", "generation", "seal", "event", "audit"} {
		id, err := nativeBuildConsequenceID(operationID, role)
		if err != nil {
			t.Fatal(err)
		}
		ids[role] = id
	}
	created := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	finished := created.Add(2 * time.Minute)
	attempt := deploymentnative.DeliveryBuildAttempt{AttemptID: ids["attempt"], PlanID: "0198f2c0-7c7a-7f00-8a11-000000001101", CandidateID: ids["candidate"], PhysicalPoolID: "pool-native", CreatedAt: created, UpdatedAt: finished, FinishedAt: finished}
	lease := deploymentnative.DeliveryLease{LeaseID: ids["lease"]}
	outcome := nativeBuildOutcome{OperationID: operationID, OperationOwnerID: "0198f2c0-7c7a-7f00-8a11-000000001103", AttemptID: ids["attempt"], PlanID: attempt.PlanID, CandidateID: ids["candidate"], LeaseID: ids["lease"], GenerationID: ids["generation"], SealID: ids["seal"], EventID: ids["event"], AuditID: ids["audit"], ServingArtifactID: "artifact-planned-identity", ServingArtifactDigest: "sha256:" + "a" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ActorID: "principal", IdempotencyKey: "key", RequestDigest: "sha256:" + "b" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", PlanDigest: "sha256:" + "c" + "ccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", SourceDigest: "sha256:" + "d" + "ddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", ExecutionDigest: "sha256:" + "e" + "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", QualificationDigest: "sha256:" + "f" + "fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}
	projected, err := nativeBuildProjection(outcome, "0198f2c0-7c7a-7f00-8a11-000000001102", attempt, lease, GenerationEvidence{GenerationRevision: 7})
	if err != nil {
		t.Fatal(err)
	}
	if projected.ID.String() != ids["attempt"] {
		t.Fatalf("build projection id = %s, want attempt %s", projected.ID, ids["attempt"])
	}
	if projected.ServingArtifactID != "artifact-planned-identity" {
		t.Fatalf("serving artifact id = %q, want persisted identity", projected.ServingArtifactID)
	}
	if projected.OperationOwnerID != "0198f2c0-7c7a-7f00-8a11-000000001103" {
		t.Fatalf("operation owner = %q, want durable owner", projected.OperationOwnerID)
	}
	if projected.BaseGenerationID == uuid.Nil {
		t.Fatal("base generation identity was dropped")
	}
	if !projected.CreatedAt.Equal(created) || !projected.UpdatedAt.Equal(finished) || !projected.TerminalAt.Equal(finished) {
		t.Fatalf("projection lifecycle timestamps = %v/%v/%v", projected.CreatedAt, projected.UpdatedAt, projected.TerminalAt)
	}
	if projected.Status != "sealed" || projected.Revision != 7 {
		t.Fatalf("projection terminal evidence = status %q revision %d", projected.Status, projected.Revision)
	}
}

func TestNativeBuildDeterministicFailureSettlesEveryLedgerAndRejectsCandidate(t *testing.T) {
	f := newNativeHeartbeatFixture(t)
	coordinator, admission := nativeBuildSettlementFixture(t, f)
	buildErr := errors.New("deterministic physical validation failure")
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	got := coordinator.settleNativeBuildFailure(canceled, f.Lease, admission, buildErr, NativePhysicalFailureDeterministic, NativePhysicalBuildPhaseValidation, nil)
	if !errors.Is(got, buildErr) {
		t.Fatalf("settlement error = %v, want original build error", got)
	}
	nativeBuildAssertSettlement(t, f, admission, deploymentmodule.NativeOperationStateFailed, deploymentnative.AttemptAborted, ducklakepostgres.AttemptAborted, "rejected")
	attempt, err := f.Delivery.BuildAttempt(t.Context(), f.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(attempt.TerminationEvidence), buildErr.Error()) {
		t.Fatalf("termination evidence leaked raw error text: %s", attempt.TerminationEvidence)
	}
}

func TestNativeBuildIndeterminateFailureSettlesEveryLedgerWithoutRejectingCandidate(t *testing.T) {
	f := newNativeHeartbeatFixture(t)
	coordinator, admission := nativeBuildSettlementFixture(t, f)
	buildErr := errors.New("physical commit outcome unknown")
	got := coordinator.settleNativeBuildFailure(t.Context(), f.Lease, admission, buildErr, NativePhysicalFailureIndeterminate, NativePhysicalBuildPhaseEvidence, nil)
	if !errors.Is(got, buildErr) {
		t.Fatalf("settlement error = %v, want original build error", got)
	}
	nativeBuildAssertSettlement(t, f, admission, deploymentmodule.NativeOperationStateIndeterminate, deploymentnative.AttemptIndeterminate, ducklakepostgres.AttemptIndeterminate, "building")
}

func TestNativeBuildPreflightFailureTerminalizesReservationWithoutRawError(t *testing.T) {
	f := newNativeHeartbeatFixture(t)
	request := f.Request
	request.IdempotencyKey = "preflight-failure"
	digest, err := nativeBuildRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := ReserveNativeBuildOperation(t.Context(), f.Delivery, f.Operation, NativeBuildOperationReservationInput{
		Request: request, RequestDigest: digest, OwnerID: "0198f2c0-7c7a-7f00-8a11-000000000912", LeaseDuration: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &NativeBuildCoordinator{repository: f.Delivery, operations: f.Operation}
	buildErr := errors.New("secret preflight failure detail")
	got := coordinator.settleNativeBuildPreflightFailure(t.Context(), reserved.Lease, digest, admissionDigest('d'), buildErr)
	if !errors.Is(got, buildErr) {
		t.Fatalf("preflight settlement error = %v, want original error", got)
	}
	operation, found, err := f.Operation.Lookup(t.Context(), deploymentmodule.NativeOperationAcquireInput{
		Scope: request.TargetID, OperationType: nativeBuildOperationType, IdempotencyKey: request.IdempotencyKey,
		RequestDigest: digest, OwnerID: reserved.Lease.OwnerID,
	})
	if err != nil || !found || operation.State != deploymentmodule.NativeOperationStateFailed {
		t.Fatalf("preflight operation = %+v found=%v err=%v, want failed", operation, found, err)
	}
	if strings.Contains(string(operation.Outcome), buildErr.Error()) {
		t.Fatalf("preflight outcome leaked raw error text: %s", operation.Outcome)
	}
	if err := replayFailedNativeBuild(operation, digest); !errors.Is(err, deploymentdomain.ErrDeliveryConflict) {
		t.Fatalf("preflight replay error = %v, want terminal conflict", err)
	}
}

func TestNativeBuildPreflightFailureClassification(t *testing.T) {
	for _, err := range []error{
		deploymentdomain.ErrDeliveryInvalid,
		deploymentdomain.ErrDeliveryConflict,
		deploymentdomain.ErrDeliveryPlanExpired,
		deploymentnative.ErrInvalid,
		deploymentnative.ErrConflict,
		release.ErrCandidateArtifactInvalid,
		deploymentmodule.ErrDeliveryInputUnavailable,
	} {
		if !nativeBuildPreflightFailureIsDeterministic(fmt.Errorf("preflight: %w", err)) {
			t.Fatalf("error %v was not classified deterministic", err)
		}
	}
	if nativeBuildPreflightFailureIsDeterministic(errors.New("temporary artifact store outage")) {
		t.Fatal("transient preflight error was classified deterministic")
	}
}

func TestNativeBuildExpiredLeasesSettleEveryLedger(t *testing.T) {
	tests := []struct {
		name           string
		classification NativePhysicalFailureClassification
		operationState deploymentmodule.NativeOperationState
		deliveryState  deploymentnative.BuildAttemptState
		duckState      ducklakepostgres.AttemptState
		candidateState string
		takeover       bool
	}{
		{name: "deterministic", classification: NativePhysicalFailureDeterministic, operationState: deploymentmodule.NativeOperationStateFailed, deliveryState: deploymentnative.AttemptAborted, duckState: ducklakepostgres.AttemptAborted, candidateState: "rejected"},
		{name: "indeterminate", classification: NativePhysicalFailureIndeterminate, operationState: deploymentmodule.NativeOperationStateIndeterminate, deliveryState: deploymentnative.AttemptIndeterminate, duckState: ducklakepostgres.AttemptIndeterminate, candidateState: "building"},
		{name: "takeover-deterministic", classification: NativePhysicalFailureDeterministic, operationState: deploymentmodule.NativeOperationStateFailed, deliveryState: deploymentnative.AttemptAborted, duckState: ducklakepostgres.AttemptAborted, candidateState: "rejected", takeover: true},
		{name: "takeover-indeterminate", classification: NativePhysicalFailureIndeterminate, operationState: deploymentmodule.NativeOperationStateIndeterminate, deliveryState: deploymentnative.AttemptIndeterminate, duckState: ducklakepostgres.AttemptIndeterminate, candidateState: "building", takeover: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newNativeHeartbeatFixtureWithLeaseDuration(t, time.Second)
			coordinator, admission := nativeBuildSettlementFixture(t, f)
			if wait := time.Until(f.Lease.LeaseExpiresAt) + 10*time.Millisecond; wait > 0 {
				time.Sleep(wait)
			}
			if tt.takeover {
				taken, err := ReserveNativeBuildOperation(t.Context(), f.Delivery, f.Operation, NativeBuildOperationReservationInput{
					Request: f.Request, RequestDigest: f.Digest, OwnerID: "0198f2c0-7c7a-7f00-8a11-000000000913", LeaseDuration: time.Second,
				})
				if err != nil {
					t.Fatalf("expiry takeover: %v", err)
				}
				if taken.Disposition != deploymentmodule.NativeOperationIndeterminate || taken.Operation.AttemptID != f.AttemptID {
					t.Fatalf("expiry takeover = %+v, want exact indeterminate attempt", taken)
				}
			}
			buildErr := errors.New("expired native build failure")
			phase := NativePhysicalBuildPhaseEvidence
			if tt.classification == NativePhysicalFailureDeterministic {
				phase = NativePhysicalBuildPhaseValidation
			}
			got := coordinator.settleNativeBuildFailure(t.Context(), f.Lease, admission, buildErr, tt.classification, phase, nil)
			if !errors.Is(got, buildErr) {
				t.Fatalf("settlement error = %v, want original build error", got)
			}
			nativeBuildAssertSettlement(t, f, admission, tt.operationState, tt.deliveryState, tt.duckState, tt.candidateState)
		})
	}
}

func nativeBuildSettlementFixture(t *testing.T, f nativeHeartbeatFixture) (*NativeBuildCoordinator, CandidateBuildAttemptAdmissionResult) {
	t.Helper()
	termination, err := NewAttemptTermination(f.Delivery, f.DuckLake)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := f.Delivery.BuildAttempt(t.Context(), f.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.Delivery.Lease(t.Context(), f.Target.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	duckAttempt, err := f.DuckLake.LoadAttempt(t.Context(), f.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	return &NativeBuildCoordinator{repository: f.Delivery, attemptTermination: termination, operations: f.Operation}, CandidateBuildAttemptAdmissionResult{Lease: lease, Attempt: attempt, DuckLakeAttempt: duckAttempt}
}

func nativeBuildAssertSettlement(t *testing.T, f nativeHeartbeatFixture, admission CandidateBuildAttemptAdmissionResult, operationState deploymentmodule.NativeOperationState, deliveryState deploymentnative.BuildAttemptState, duckState ducklakepostgres.AttemptState, candidateState string) {
	t.Helper()
	operation, found, err := f.Operation.Lookup(t.Context(), deploymentmodule.NativeOperationAcquireInput{Scope: f.Request.TargetID, OperationType: nativeBuildOperationType, IdempotencyKey: f.Request.IdempotencyKey, RequestDigest: f.Digest, OwnerID: f.Lease.OwnerID})
	if err != nil || !found || operation.State != operationState {
		t.Fatalf("operation settlement = %+v found=%v err=%v, want %s", operation, found, err, operationState)
	}
	attempt, err := f.Delivery.BuildAttempt(t.Context(), f.AttemptID)
	if err != nil || attempt.State != deliveryState || len(attempt.TerminationEvidence) == 0 {
		t.Fatalf("delivery attempt settlement = %+v err=%v, want %s", attempt, err, deliveryState)
	}
	duckAttempt, err := f.DuckLake.LoadAttempt(t.Context(), f.AttemptID)
	if err != nil || duckAttempt.State != duckState || len(duckAttempt.TerminationEvidence) == 0 {
		t.Fatalf("DuckLake attempt settlement = %+v err=%v, want %s", duckAttempt, err, duckState)
	}
	if operationState == deploymentmodule.NativeOperationStateIndeterminate && (!sameTerminationEvidence(operation.AttemptEvidence, attempt.TerminationEvidence) || !sameTerminationEvidence(operation.AttemptEvidence, duckAttempt.TerminationEvidence)) {
		t.Fatal("indeterminate operation and attempt ledgers retained different recovery evidence")
	}
	candidate, err := f.Delivery.Candidate(t.Context(), admission.Attempt.CandidateID)
	if err != nil || candidate.Status != candidateState {
		t.Fatalf("candidate settlement = %+v err=%v, want %s", candidate, err, candidateState)
	}
	lease, err := f.Delivery.Lease(t.Context(), admission.Lease.LeaseID)
	if err != nil || lease.State != "released" {
		t.Fatalf("target lease settlement = %+v err=%v, want released", lease, err)
	}
}
