package deploymentpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	"github.com/flidai/leapview/internal/analytics/gates"
	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/release"
	"github.com/google/uuid"
)

// nativeBuildPlanArtifacts stops immediately after materialization.  The
// coordinator has already reserved the operation and inspected artifacts at
// this point, so returning the input-unavailable sentinel keeps the test
// before any attempt or physical side effects while still proving the
// effective data mode reached the materializer.
type nativeBuildPlanArtifacts struct {
	set            release.CandidateArtifactSet
	materializeErr error
	materializeCnt int
	materialized   release.CandidateArtifactSet
}

func (a *nativeBuildPlanArtifacts) InspectCandidateArtifacts(_ context.Context, request release.CandidateArtifactRequest) (release.CandidateArtifactSet, error) {
	set := a.set
	set.Artifact.SourceDigest = request.ArtifactDigest
	set.Generation.ArtifactDigest = request.ArtifactDigest
	return set, nil
}

func (a *nativeBuildPlanArtifacts) MaterializeCandidateArtifacts(_ context.Context, _ release.CandidateArtifactRequest, inspected release.CandidateArtifactSet) (release.CandidateArtifactSet, error) {
	a.materializeCnt++
	a.materialized = inspected
	return release.CandidateArtifactSet{}, a.materializeErr
}

func (a *nativeBuildPlanArtifacts) HydrateCandidateArtifacts(context.Context, release.CandidateArtifactRequest, release.CandidateArtifactSet, release.CandidateArtifactIdentity) (release.CandidateArtifactSet, error) {
	return release.CandidateArtifactSet{}, errors.New("native build test hydrator is not configured")
}

// nativeBuildPlanOperation is a narrow operation authority test double.  It
// embeds the full interface so the coordinator's unused recovery methods are
// still present, while reservation and deterministic preflight settlement are
// fully value-only and do not mutate the operation schema.
type nativeBuildPlanOperation struct {
	deploymentmodule.NativeBuildOperationAuthority
	operation deploymentmodule.NativeOperationRecord
	lease     deploymentmodule.NativeOperationLease
}

func (a *nativeBuildPlanOperation) AcquireTx(_ context.Context, _ deploymentmodule.NativeOperationTx, input deploymentmodule.NativeOperationAcquireInput) (deploymentmodule.NativeOperationAcquireResult, error) {
	const operationID = "0198f2c0-7c7a-7f00-8a11-000000001910"
	expires := time.Date(2026, 8, 30, 13, 0, 0, 0, time.UTC)
	a.operation = deploymentmodule.NativeOperationRecord{Scope: input.Scope, OperationType: input.OperationType, IdempotencyKey: input.IdempotencyKey, RequestDigest: input.RequestDigest, OwnerID: input.OwnerID, OperationID: operationID, State: deploymentmodule.NativeOperationStatePending, FencingGeneration: 1, LeaseExpiresAt: expires}
	a.lease = deploymentmodule.NativeOperationLease{Scope: input.Scope, IdempotencyKey: input.IdempotencyKey, OperationID: operationID, OwnerID: input.OwnerID, FencingGeneration: 1, LeaseExpiresAt: expires}
	return deploymentmodule.NativeOperationAcquireResult{Status: deploymentmodule.NativeOperationAcquired, Operation: a.operation, Lease: a.lease}, nil
}

func (a *nativeBuildPlanOperation) RenewLeaseTx(_ context.Context, _ deploymentmodule.NativeOperationTx, lease deploymentmodule.NativeOperationLease, duration time.Duration) (deploymentmodule.NativeOperationLease, error) {
	lease.LeaseExpiresAt = lease.LeaseExpiresAt.Add(duration)
	a.lease = lease
	a.operation.LeaseExpiresAt = lease.LeaseExpiresAt
	return lease, nil
}

func (a *nativeBuildPlanOperation) FailTx(_ context.Context, _ deploymentmodule.NativeOperationTx, lease deploymentmodule.NativeOperationLease, _ json.RawMessage) error {
	a.operation.State = deploymentmodule.NativeOperationStateFailed
	a.operation.LeaseExpiresAt = lease.LeaseExpiresAt
	return nil
}

type nativeBuildPlanContract struct {
	physicalPoolID, compatibilityDigest string
}

func (c nativeBuildPlanContract) Resolve(context.Context, NativeBuildContractRequest) (NativeBuildContract, error) {
	return NativeBuildContract{PhysicalPoolID: c.physicalPoolID, CompatibilityDigest: c.compatibilityDigest, PoolContract: &ducklake.PoolContract{}}, nil
}

// These wrappers intentionally carry nil embedded interfaces.  They are
// non-nil concrete values, satisfying BuildPlan's fail-closed authority
// checks, and are never reached because the materializer returns first.
type nativeBuildPlanArtifactRecovery struct {
	release.CandidateArtifactRecovery
}
type nativeBuildPlanManagedData struct {
	NativeCandidateManagedDataResolver
}
type nativeBuildPlanHeartbeat struct{ NativeBuildHeartbeatRunner }
type nativeBuildPlanAttemptAdmission struct{ CandidateBuildAttemptAdmission }
type nativeBuildPlanAttemptTermination struct{ AttemptTermination }
type nativeBuildPlanGenerationAdmission struct{ GenerationAdmission }
type nativeBuildPlanPhysicalFactory struct {
	NativePhysicalBuildEnvironmentFactory
}
type nativeBuildPlanObservationWriter struct {
	ducklakepostgres.SourceObservationWriter
}
type nativeBuildPlanMarkerFactory struct {
	NativePhysicalMarkerResolverFactory
}
type nativeBuildPlanObservationReader struct{ NativeSourceObservationReader }
type nativeBuildPlanSnapshotFactory struct {
	NativePhysicalSnapshotInspectorFactory
}
type nativeBuildPlanQualificationFactory struct {
	NativeQualificationEnvironmentFactory
}
type nativeBuildPlanEvents struct {
	deploymentmodule.NativeDeliveryEventAppender
}
type nativeBuildPlanAudit struct {
	deploymentmodule.NativeDeliveryAuditAppender
}

func nativeBuildPlanCoordinatorFixture(t *testing.T, exactReusable bool) (*NativeBuildCoordinator, deploymentmodule.NativeDeliveryBuildRequest, *nativeBuildPlanArtifacts, string) {
	t.Helper()
	db, repository := nativePlanPostgresDB(t)
	const projectID = "project_native_plan"
	const targetID = "target_native_build"
	const sourceDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const attestationDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	request := deploymentmodule.NativeDeliveryBuildRequest{ProjectID: projectID, TargetID: targetID, Environment: "prod", PlanID: uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000001901"), PrincipalID: "principal-native-build", IdempotencyKey: "native-build-plan"}
	source, artifactSet := nativePlanPostgresFixture(t, sourceDigest, attestationDigest)
	planInput := nativePlanFixture(t, deploymentnative.PlanInput{PlanID: request.PlanID.String(), TargetID: targetID, PlanRevision: 1, CompiledGraphDigest: admissionDigest('d'), CompiledConfigDigest: admissionDigest('e'), SecurityDomainFingerprint: admissionDigest('f'), ArtifactDigest: sourceDigest, QualificationDigest: admissionDigest('1')}, projectID)
	var rich deploymentdomain.DeliveryPlan
	if err := json.Unmarshal(planInput.PlanDocument, &rich); err != nil {
		t.Fatalf("decode native build plan fixture: %v", err)
	}
	rich.Operation = deploymentdomain.DeliveryOperationRestatement
	rich.SourceOwnerID = "owner-native-build"
	rich.Provenance.AttestationDigest = attestationDigest
	const operationID = "0198f2c0-7c7a-7f00-8a11-000000001910"
	candidateID, err := nativeBuildConsequenceID(operationID, "candidate")
	if err != nil {
		t.Fatal(err)
	}
	if exactReusable {
		rich.Evidence.Reuse = []deploymentdomain.DeliveryReuseDecision{{ResourceID: candidateID, Reusable: true, Reason: "exact candidate reuse"}}
	} else {
		// A relation-scoped restatement retains the sealed base for unchanged
		// relations, but is not an exact candidate-level reuse decision.
		rich.Evidence.Reuse = []deploymentdomain.DeliveryReuseDecision{{ResourceID: "model:orders", RetainBase: true, Reason: "restatement of affected relation"}}
	}
	rich, err = deploymentdomain.NewDeliveryPlan(rich)
	if err != nil {
		t.Fatalf("rebuild native build plan fixture: %v", err)
	}
	planInput.PlanDigest = rich.Digest
	planInput.PlanDocument, err = json.Marshal(rich)
	if err != nil {
		t.Fatalf("encode native build plan fixture: %v", err)
	}
	if _, err := repository.CreateTarget(t.Context(), deploymentnative.TargetInput{TargetID: targetID, ProjectID: projectID, Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreatePlan(t.Context(), planInput); err != nil {
		t.Fatalf("persist native build plan fixture: %v", err)
	}
	artifactSet.Generation.DataMode = release.GenerationDataReuseBase
	artifactSet.Generation.DataRevision = "base:snapshot-1"
	artifactSet.Generation.BaseGateEvidence = &release.GateEvidence{}
	artifactSet.Generation.ArtifactDigest = sourceDigest
	artifacts := &nativeBuildPlanArtifacts{set: artifactSet, materializeErr: deploymentmodule.ErrDeliveryInputUnavailable}
	operations := &nativeBuildPlanOperation{}
	physicalPoolID := "pool-native-build"
	compatibilityDigest := admissionDigest('a')
	coordinator := &NativeBuildCoordinator{
		repository: repository, sources: &nativePlanSourceReader{snap: source}, artifacts: artifacts,
		artifactRecovery: nativeBuildPlanArtifactRecovery{}, managedData: nativeBuildPlanManagedData{}, contract: nativeBuildPlanContract{physicalPoolID: physicalPoolID, compatibilityDigest: compatibilityDigest},
		physicalPoolID: physicalPoolID, compatibilityDigest: compatibilityDigest, operations: operations, heartbeat: nativeBuildPlanHeartbeat{},
		attemptAdmission: nativeBuildPlanAttemptAdmission{}, attemptTermination: nativeBuildPlanAttemptTermination{}, generationAdmission: nativeBuildPlanGenerationAdmission{},
		physicalFactory: nativeBuildPlanPhysicalFactory{}, observationWriter: nativeBuildPlanObservationWriter{}, markerResolverFactory: nativeBuildPlanMarkerFactory{},
		observationReader: nativeBuildPlanObservationReader{}, snapshotFactory: nativeBuildPlanSnapshotFactory{}, qualificationFactory: nativeBuildPlanQualificationFactory{},
		events: nativeBuildPlanEvents{}, audit: nativeBuildPlanAudit{}, leaseDuration: time.Hour, clock: func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) },
	}
	_ = db // keep the pool alive through repository use; nativePlanPostgresDB registers cleanup.
	return coordinator, request, artifacts, sourceDigest
}

func TestNativeBuildPlanProjectsRestatementReuseToRefreshSourcesBeforeMaterialization(t *testing.T) {
	coordinator, request, artifacts, sourceDigest := nativeBuildPlanCoordinatorFixture(t, false)
	_, err := coordinator.BuildPlan(t.Context(), request)
	if !errors.Is(err, deploymentmodule.ErrDeliveryInputUnavailable) {
		t.Fatalf("BuildPlan error = %v, want materializer sentinel", err)
	}
	if artifacts.materializeCnt != 1 {
		t.Fatalf("materializer calls = %d, want exactly one", artifacts.materializeCnt)
	}
	if artifacts.materialized.Generation.DataMode != release.GenerationDataRefreshSources {
		t.Fatalf("materialized data mode = %q, want refresh_sources", artifacts.materialized.Generation.DataMode)
	}
	wantRevision, err := release.CandidateSourcesDataRevision(sourceDigest, nil)
	if err != nil {
		t.Fatal(err)
	}
	if artifacts.materialized.Generation.DataRevision != wantRevision {
		t.Fatalf("materialized data revision = %q, want %q", artifacts.materialized.Generation.DataRevision, wantRevision)
	}
	if artifacts.materialized.Generation.BaseGateEvidence != nil {
		t.Fatal("partial restatement retained base gate evidence after refresh projection")
	}
}

func TestNativeBuildPlanKeepsExactCandidateReuseFailClosed(t *testing.T) {
	coordinator, request, artifacts, _ := nativeBuildPlanCoordinatorFixture(t, true)
	_, err := coordinator.BuildPlan(t.Context(), request)
	if !errors.Is(err, deploymentmodule.ErrDeliveryInputUnavailable) {
		t.Fatalf("BuildPlan error = %v, want native reuse admission sentinel", err)
	}
	if !strings.Contains(err.Error(), "native base-snapshot reuse admission is not configured") {
		t.Fatalf("BuildPlan error = %v, want reuse admission explanation", err)
	}
	if artifacts.materializeCnt != 0 {
		t.Fatalf("materializer calls = %d, want zero for exact candidate reuse", artifacts.materializeCnt)
	}
}

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

func TestNormalizeNativeBuildBoundsUsesReviewedDefaults(t *testing.T) {
	got := normalizeNativeBuildBounds(gates.Bounds{})
	if got != (gates.Bounds{MaxRows: 10000, MaxQueries: 128, MaxMillis: 5000}) {
		t.Fatalf("native build bounds = %+v", got)
	}
	explicit := gates.Bounds{MaxRows: 7, MaxQueries: 3, MaxMillis: 11}
	if got := normalizeNativeBuildBounds(explicit); got != explicit {
		t.Fatalf("explicit native build bounds = %+v, want %+v", got, explicit)
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
	attempt := deploymentnative.DeliveryBuildAttempt{AttemptID: ids["attempt"], PlanID: "0198f2c0-7c7a-7f00-8a11-000000001101", CandidateID: ids["candidate"], PhysicalPoolID: "pool-native", FencingEpoch: 11, CreatedAt: created, UpdatedAt: finished, FinishedAt: finished}
	lease := deploymentnative.DeliveryLease{LeaseID: ids["lease"]}
	outcome := nativeBuildOutcome{OperationID: operationID, OperationOwnerID: "0198f2c0-7c7a-7f00-8a11-000000001103", AttemptID: ids["attempt"], PlanID: attempt.PlanID, CandidateID: ids["candidate"], LeaseID: ids["lease"], GenerationID: ids["generation"], SealID: ids["seal"], EventID: ids["event"], AuditID: ids["audit"], ServingArtifactID: "artifact-planned-identity", ServingArtifactDigest: "sha256:" + "a" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ActorID: "principal", IdempotencyKey: "key", RequestDigest: "sha256:" + "b" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", PlanDigest: "sha256:" + "c" + "ccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", SourceDigest: "sha256:" + "d" + "ddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", ExecutionDigest: "sha256:" + "e" + "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", QualificationDigest: "sha256:" + "f" + "fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}
	projected, err := nativeBuildProjection(outcome, "0198f2c0-7c7a-7f00-8a11-000000001102", attempt, lease, 13)
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
	if projected.Status != "sealed" || projected.Revision != 11 || projected.CandidateRevision != 13 {
		t.Fatalf("projection terminal evidence = status %q revision %d candidate revision %d", projected.Status, projected.Revision, projected.CandidateRevision)
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
