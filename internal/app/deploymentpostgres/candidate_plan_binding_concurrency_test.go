package deploymentpostgres

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/google/uuid"
)

func TestCandidatePlanBindingConcurrentTargetAdvancePreservesLockOrder(t *testing.T) {
	pool := candidateAdmissionDB(t)
	delivery := deploymentnative.New(pool)
	admission, err := NewCandidateBuildAttemptAdmission(delivery, candidatePhysicalAdmissionStub{})
	if err != nil {
		t.Fatal(err)
	}
	fixture := candidateAdmissionFixtureInput(t)
	plan := candidateAdmissionRichPlan(t, fixture, 1)
	persistCandidateAdmissionRichPlan(t, &fixture, plan)
	fixture.Input.Plan = &plan
	if _, err := delivery.CreateTarget(t.Context(), fixture.Target); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.CreatePlan(t.Context(), fixture.Plan); err != nil {
		t.Fatal(err)
	}
	oldLease, err := delivery.AcquireLease(t.Context(), deploymentnative.LeaseInput{
		LeaseID: uuid.Must(uuid.NewV7()).String(), TargetID: fixture.Target.TargetID,
		OwnerID: "activation-owner", ExpiresAt: fixture.Input.Lease.ExpiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	var originalNextRevision int64
	if err := pool.QueryRow(t.Context(), `SELECT next_candidate_revision FROM delivery.delivery_target_revision WHERE target_id=$1`, fixture.Target.TargetID).Scan(&originalNextRevision); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	activationTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer activationTx.Rollback(context.Background())
	var activationPID int32
	if err := activationTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&activationPID); err != nil {
		t.Fatal(err)
	}
	// Activation owns its lease before it locks the target. Hold that first
	// lock while candidate admission reaches lease acquisition on another
	// connection; no scheduler timing assumption establishes this ordering.
	if _, err := activationTx.Exec(ctx, `SELECT lease_id FROM delivery.delivery_lease WHERE lease_id=$1::uuid FOR UPDATE`, oldLease.LeaseID); err != nil {
		t.Fatal(err)
	}
	admissionTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var admissionPID int32
	if err := admissionTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&admissionPID); err != nil {
		_ = admissionTx.Rollback(context.Background())
		t.Fatal(err)
	}
	result := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, admitErr := admission.AdmitCandidateBuildAttemptTx(ctx, admissionTx, fixture.Input)
		rollbackErr := admissionTx.Rollback(context.Background())
		result <- errors.Join(admitErr, rollbackErr)
	}()
	defer func() {
		cancel()
		_ = activationTx.Rollback(context.Background())
		<-finished
	}()
	for {
		var blocked bool
		if err := pool.QueryRow(ctx, `SELECT $1::int = ANY(pg_blocking_pids($2::int))`, activationPID, admissionPID).Scan(&blocked); err != nil {
			t.Fatalf("wait for candidate admission to reach the held lease: %v", err)
		}
		if blocked {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("admission returned before reaching the held lease: %v", err)
		default:
			runtime.Gosched()
		}
	}
	// NOWAIT is the regression assertion: target-before-lease admission would
	// already own this row and deadlock with activation's lease-before-target
	// order. The current admission must not have locked the target yet.
	if _, err := activationTx.Exec(ctx, `SELECT target_id FROM delivery.delivery_target WHERE target_id=$1 FOR UPDATE NOWAIT`, fixture.Target.TargetID); err != nil {
		t.Fatalf("candidate admission inverted activation's lease/target order: %v", err)
	}
	if _, err := activationTx.Exec(ctx, `UPDATE delivery.delivery_target SET target_revision=target_revision+1 WHERE target_id=$1`, fixture.Target.TargetID); err != nil {
		t.Fatal(err)
	}
	if err := activationTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, deploymentdomain.ErrDeliveryStale) {
			t.Fatalf("admission after target advance = %v, want stale plan", err)
		}
	case <-ctx.Done():
		t.Fatalf("candidate admission did not finish: %v", ctx.Err())
	}
	for label, id := range map[string]string{"candidate": fixture.Input.Attempt.CandidateID, "lease": fixture.Input.Lease.LeaseID, "attempt": fixture.Input.Attempt.AttemptID} {
		var err error
		switch label {
		case "candidate":
			_, err = delivery.Candidate(t.Context(), id)
		case "lease":
			_, err = delivery.Lease(t.Context(), id)
		case "attempt":
			_, err = delivery.BuildAttempt(t.Context(), id)
		}
		if !errors.Is(err, deploymentnative.ErrNotFound) {
			t.Fatalf("stale admission retained %s: %v", label, err)
		}
	}
	retained, err := delivery.Lease(t.Context(), oldLease.LeaseID)
	if err != nil || retained.State != "active" || retained.FencingEpoch != oldLease.FencingEpoch {
		t.Fatalf("stale admission changed the previous lease: %+v, %v", retained, err)
	}
	var nextRevision int64
	if err := pool.QueryRow(t.Context(), `SELECT next_candidate_revision FROM delivery.delivery_target_revision WHERE target_id=$1`, fixture.Target.TargetID).Scan(&nextRevision); err != nil {
		t.Fatal(err)
	}
	if nextRevision != originalNextRevision {
		t.Fatalf("stale admission consumed candidate revision: %d -> %d", originalNextRevision, nextRevision)
	}
}
