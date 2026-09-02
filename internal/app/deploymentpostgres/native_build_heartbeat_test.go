package deploymentpostgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	deploymentoperation "github.com/flidai/leapview/internal/app/deploymentoperation"
	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type mismatchingHeartbeatOperationAuthority struct {
	deploymentmodule.NativeBuildOperationAuthority
}

func (a mismatchingHeartbeatOperationAuthority) RenewLeaseTx(_ context.Context, _ deploymentmodule.NativeOperationTx, lease deploymentmodule.NativeOperationLease, duration time.Duration) (deploymentmodule.NativeOperationLease, error) {
	lease.Scope += "-other"
	lease.LeaseExpiresAt = lease.LeaseExpiresAt.Add(duration)
	return lease, nil
}

type nativeHeartbeatFixture struct {
	Pool      *pgxpool.Pool
	Delivery  *deploymentnative.Repository
	DuckLake  *ducklakepostgres.Repository
	Operation deploymentmodule.NativeBuildOperationAuthority
	Heartbeat *NativeBuildHeartbeat
	Request   deploymentmodule.NativeDeliveryBuildRequest
	Digest    string
	Lease     deploymentmodule.NativeOperationLease
	Target    deploymentnative.LeaseFence
	AttemptID string
}

func newNativeHeartbeatFixture(t *testing.T) nativeHeartbeatFixture {
	return newNativeHeartbeatFixtureWithLeaseDuration(t, time.Hour)
}

func newNativeHeartbeatFixtureWithLeaseDuration(t *testing.T, leaseDuration time.Duration) nativeHeartbeatFixture {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "native_build_heartbeat")
	p, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), operationpostgres.SchemaSQL()); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := deploymentnative.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := ducklakepostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	delivery := deploymentnative.New(p)
	ducklake := ducklakepostgres.New(p)
	operations := deploymentoperation.New(operationpostgres.NewWithConfig(p, leaseDuration, time.Hour))
	request := deploymentmodule.NativeDeliveryBuildRequest{ProjectID: "project-heartbeat", TargetID: "target-heartbeat", Environment: "prod", PlanID: uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000901"), PrincipalID: "builder-heartbeat", IdempotencyKey: "heartbeat-1"}
	digest, err := nativeBuildRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	plan := nativePlanFixture(t, deploymentnative.PlanInput{PlanID: request.PlanID.String(), TargetID: request.TargetID, PlanRevision: 1, CompiledGraphDigest: admissionDigest('d'), CompiledConfigDigest: admissionDigest('e'), SecurityDomainFingerprint: admissionDigest('f'), ArtifactDigest: admissionDigest('c'), QualificationDigest: admissionDigest('1')}, request.ProjectID.String())
	if _, err := delivery.CreateTarget(t.Context(), deploymentnative.TargetInput{TargetID: request.TargetID, ProjectID: request.ProjectID.String(), Environment: request.Environment}); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.CreatePlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	const catalogID = "catalog-heartbeat"
	if _, err := ducklake.RegisterCatalog(t.Context(), ducklakepostgres.CatalogIdentity{PhysicalPoolID: "pool-candidate-admission", CatalogDatabase: "ducklake", CatalogID: catalogID, CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000000999", MetadataSchema: "main"}); err != nil {
		t.Fatal(err)
	}
	reserved, err := ReserveNativeBuildOperation(t.Context(), delivery, operations, NativeBuildOperationReservationInput{Request: request, RequestDigest: digest, OwnerID: "0198f2c0-7c7a-7f00-8a11-000000000902", LeaseDuration: leaseDuration})
	if err != nil {
		t.Fatalf("reserve native build operation: %v", err)
	}
	attemptID := "0198f2c0-7c7a-7f00-8a11-000000000903"
	tx, err = p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	bound, err := operations.BeginAttemptTx(t.Context(), tx, deploymentmodule.NativeOperationBeginAttemptInput{Lease: reserved.Lease, AttemptID: attemptID, AttemptIdentity: "native-build/heartbeat-1"})
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("bind native build operation attempt: %v", err)
	}
	candidateID := "0198f2c0-7c7a-7f00-8a11-000000000904"
	if _, err := delivery.CreateCandidateAllocatedTx(t.Context(), tx, deploymentnative.CandidateInput{CandidateID: candidateID, TargetID: request.TargetID, PlanID: request.PlanID.String(), ArtifactDigest: plan.ArtifactDigest}); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("allocate native build candidate: %v", err)
	}
	leaseID := "0198f2c0-7c7a-7f00-8a11-000000000905"
	admission, err := NewCandidateBuildAttemptAdmission(delivery, ducklake)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	admitted, err := admission.AdmitCandidateBuildAttemptTx(t.Context(), tx, CandidateBuildAttemptAdmissionInput{
		Lease:    deploymentnative.LeaseInput{LeaseID: leaseID, TargetID: request.TargetID, OwnerID: request.PrincipalID, ExpiresAt: reserved.Lease.LeaseExpiresAt},
		Attempt:  deploymentnative.BuildAttemptInput{AttemptID: attemptID, PlanID: request.PlanID.String(), CandidateID: candidateID, OwnerID: request.PrincipalID, PhysicalPoolID: "pool-candidate-admission", RequestDigest: digest, PlanDigest: plan.PlanDigest, SessionIdentity: "heartbeat-session", LeaseExpiresAt: reserved.Lease.LeaseExpiresAt},
		Artifact: CandidateBuildArtifactInput{ServingArtifactID: "artifact-" + plan.ArtifactDigest[len("sha256:"):], ServingArtifactDigest: plan.ArtifactDigest, ServingStateID: "state-heartbeat"}, CatalogID: catalogID,
	})
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("admit native build attempt: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit native build attempt admission: %v", err)
	}
	heartbeat, err := NewNativeBuildHeartbeat(delivery, ducklake, operations)
	if err != nil {
		t.Fatal(err)
	}
	return nativeHeartbeatFixture{Pool: p, Delivery: delivery, DuckLake: ducklake, Operation: operations, Heartbeat: heartbeat, Request: request, Digest: digest, Lease: bound.Lease, Target: deploymentnative.LeaseFence{LeaseID: admitted.Lease.LeaseID, TargetID: admitted.Lease.TargetID, OwnerID: admitted.Lease.OwnerID, FencingEpoch: admitted.Lease.FencingEpoch}, AttemptID: attemptID}
}

func TestNativeBuildHeartbeatRenewsAllLedgersAtomically(t *testing.T) {
	f := newNativeHeartbeatFixture(t)
	beforeAttempt, err := f.Delivery.BuildAttempt(t.Context(), f.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.Heartbeat.Renew(t.Context(), NativeBuildHeartbeatInput{OperationLease: f.Lease, TargetLease: f.Target, AttemptID: f.AttemptID, AttemptOwnerID: f.Target.OwnerID, AttemptFencingEpoch: f.Target.FencingEpoch, Duration: 2 * time.Hour})
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if !result.OperationLease.LeaseExpiresAt.Equal(result.TargetLease.ExpiresAt) || !result.TargetLease.ExpiresAt.Equal(result.DeliveryAttempt.LeaseExpiresAt) || !result.DeliveryAttempt.LeaseExpiresAt.Equal(result.DuckLakeAttempt.LeaseExpiresAt) || !result.DeliveryAttempt.LeaseExpiresAt.After(beforeAttempt.LeaseExpiresAt) {
		t.Fatalf("heartbeat expiries diverged: %+v", result)
	}
	if result.DeliveryAttempt.AttemptID != f.AttemptID || result.DuckLakeAttempt.AttemptID != f.AttemptID || result.DeliveryAttempt.State != deploymentnative.AttemptRunning || result.DuckLakeAttempt.State != ducklakepostgres.AttemptRunning {
		t.Fatalf("heartbeat identity/state = %+v", result)
	}
}

func TestNativeBuildHeartbeatRollsBackOnTargetFenceMismatch(t *testing.T) {
	f := newNativeHeartbeatFixture(t)
	before, _, err := f.Operation.Lookup(t.Context(), deploymentmodule.NativeOperationAcquireInput{Scope: f.Request.TargetID, OperationType: nativeBuildOperationType, IdempotencyKey: f.Request.IdempotencyKey, RequestDigest: f.Digest, OwnerID: f.Lease.OwnerID})
	if err != nil {
		t.Fatal(err)
	}
	badTarget := f.Target
	badTarget.TargetID = "target-heartbeat-other"
	_, err = f.Heartbeat.Renew(t.Context(), NativeBuildHeartbeatInput{OperationLease: f.Lease, TargetLease: badTarget, AttemptID: f.AttemptID, AttemptOwnerID: f.Target.OwnerID, AttemptFencingEpoch: f.Target.FencingEpoch, Duration: time.Hour})
	if !errors.Is(err, deploymentnative.ErrStaleFence) {
		t.Fatalf("mismatched target fence err=%v, want stale fence", err)
	}
	after, found, err := f.Operation.Lookup(t.Context(), deploymentmodule.NativeOperationAcquireInput{Scope: f.Request.TargetID, OperationType: nativeBuildOperationType, IdempotencyKey: f.Request.IdempotencyKey, RequestDigest: f.Digest, OwnerID: f.Lease.OwnerID})
	if err != nil || !found || !after.LeaseExpiresAt.Equal(before.LeaseExpiresAt) {
		t.Fatalf("operation changed despite heartbeat rollback: before=%+v after=%+v found=%v err=%v", before, after, found, err)
	}
}

func TestNativeBuildHeartbeatRejectsMismatchedRenewedOperationIdentity(t *testing.T) {
	f := newNativeHeartbeatFixture(t)
	heartbeat, err := NewNativeBuildHeartbeat(f.Delivery, f.DuckLake, mismatchingHeartbeatOperationAuthority{NativeBuildOperationAuthority: f.Operation})
	if err != nil {
		t.Fatal(err)
	}
	_, err = heartbeat.Renew(t.Context(), NativeBuildHeartbeatInput{OperationLease: f.Lease, TargetLease: f.Target, AttemptID: f.AttemptID, AttemptOwnerID: f.Target.OwnerID, AttemptFencingEpoch: f.Target.FencingEpoch, Duration: 2 * time.Hour})
	if !errors.Is(err, deploymentdomain.ErrDeliveryConflict) {
		t.Fatalf("mismatched operation renewal error = %v, want conflict", err)
	}
}

func TestNativeBuildHeartbeatConcurrentRenewalsRemainConsistent(t *testing.T) {
	f := newNativeHeartbeatFixture(t)
	input := NativeBuildHeartbeatInput{OperationLease: f.Lease, TargetLease: f.Target, AttemptID: f.AttemptID, AttemptOwnerID: f.Target.OwnerID, AttemptFencingEpoch: f.Target.FencingEpoch, Duration: 2 * time.Hour}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.Heartbeat.Renew(t.Context(), input)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent heartbeat: %v", err)
		}
	}
	operation, found, err := f.Operation.Lookup(t.Context(), deploymentmodule.NativeOperationAcquireInput{Scope: f.Request.TargetID, OperationType: nativeBuildOperationType, IdempotencyKey: f.Request.IdempotencyKey, RequestDigest: f.Digest, OwnerID: f.Lease.OwnerID})
	if err != nil || !found {
		t.Fatalf("lookup after concurrent heartbeat: %+v found=%v err=%v", operation, found, err)
	}
	attempt, err := f.Delivery.BuildAttempt(t.Context(), f.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	duckAttempt, err := f.DuckLake.LoadAttempt(t.Context(), f.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.Delivery.Lease(t.Context(), f.Target.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	if !operation.LeaseExpiresAt.Equal(attempt.LeaseExpiresAt) || !attempt.LeaseExpiresAt.Equal(duckAttempt.LeaseExpiresAt) || !duckAttempt.LeaseExpiresAt.Equal(lease.ExpiresAt) {
		t.Fatalf("concurrent heartbeat expiries diverged: operation=%v delivery=%v ducklake=%v target=%v", operation.LeaseExpiresAt, attempt.LeaseExpiresAt, duckAttempt.LeaseExpiresAt, lease.ExpiresAt)
	}
}
