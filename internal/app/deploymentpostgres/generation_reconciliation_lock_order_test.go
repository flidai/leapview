package deploymentpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	servingnative "github.com/flidai/leapview/internal/servingstate/postgres"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestGenerationAdmissionAndReconciliationUseDeliveryFirstLockOrder(t *testing.T) {
	p := generationAdmissionDB(t)
	delivery := deploymentnative.New(p)
	ducklake := ducklakepostgres.New(p)
	serving := servingnative.New(p)
	admission, err := NewGenerationAdmission(delivery, serving, ducklake)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := NewAttemptTermination(delivery, ducklake)
	if err != nil {
		t.Fatal(err)
	}
	input := validGenerationAdmissionInput(t)
	seedGenerationAdmission(t, delivery, ducklake, input)

	// Hold only the DuckLake attempt row. Normal admission must acquire and
	// retain the delivery attempt row before it waits for this gate.
	gateTx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	gateOpen := true
	defer func() {
		if gateOpen {
			_ = gateTx.Rollback(context.Background())
		}
	}()
	if _, err := ducklake.MarkAttemptIndeterminateTx(t.Context(), gateTx, ducklakepostgres.TerminateAttemptInput{
		AttemptID: input.Commit.AttemptID, OwnerID: input.Commit.OwnerID, FencingEpoch: input.Commit.FencingEpoch,
		Evidence: json.RawMessage(`{"gate":"lock-order"}`),
	}); err != nil {
		t.Fatal(err)
	}

	type admissionOutcome struct {
		result GenerationAdmissionResult
		err    error
	}
	admissionDone := make(chan admissionOutcome, 1)
	go func() {
		result, runErr := admission.CompleteBuildAndAdmit(t.Context(), input)
		admissionDone <- admissionOutcome{result: result, err: runErr}
	}()

	// Observe the delivery row lock without raw SQL. A probe transition is
	// always rolled back; a deadline proves admission already owns the row
	// while it is blocked on the gated DuckLake row.
	locked := false
	lockDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(lockDeadline) && !locked {
		probeTx, beginErr := p.Begin(t.Context())
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		probeCtx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		_, probeErr := delivery.MarkAttemptIndeterminateTx(probeCtx, probeTx, deploymentnative.TerminateAttemptInput{
			AttemptID: input.Commit.AttemptID, OwnerID: input.Commit.OwnerID, FencingEpoch: input.Commit.FencingEpoch,
			Evidence: json.RawMessage(`{"probe":"delivery-lock"}`),
		})
		cancel()
		_ = probeTx.Rollback(context.Background())
		var pgErr *pgconn.PgError
		if errors.Is(probeErr, context.DeadlineExceeded) || (errors.As(probeErr, &pgErr) && pgErr.Code == "57014") {
			locked = true
		} else if probeErr != nil {
			t.Fatalf("delivery lock probe: %v", probeErr)
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !locked {
		t.Fatal("admission did not acquire the delivery attempt before waiting on DuckLake")
	}

	type reconciliationOutcome struct {
		result AttemptTerminationResult
		err    error
	}
	reconciliationDone := make(chan reconciliationOutcome, 1)
	go func() {
		result, runErr := recovery.ReconcileAttempt(t.Context(), AttemptReconciliationInput{
			AttemptID: input.Commit.AttemptID, OwnerID: input.Commit.OwnerID, FencingEpoch: input.Commit.FencingEpoch,
			PhysicalPoolID: input.Seal.PhysicalPoolID, CatalogID: input.Seal.CatalogID, SnapshotID: input.Commit.SnapshotID,
			CommitMarker: input.Commit.CommitMarker, State: deploymentnative.AttemptCommitted,
		})
		reconciliationDone <- reconciliationOutcome{result: result, err: runErr}
	}()
	select {
	case outcome := <-reconciliationDone:
		t.Fatalf("reconciliation bypassed the delivery-first lock: %#v, %v", outcome.result, outcome.err)
	case <-time.After(200 * time.Millisecond):
	}

	if err := gateTx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	gateOpen = false
	select {
	case outcome := <-admissionDone:
		if outcome.err != nil || outcome.result.AttemptID != input.Commit.AttemptID {
			t.Fatalf("admission after DuckLake gate release = %#v, %v", outcome.result, outcome.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("admission remained blocked after DuckLake gate release")
	}
	select {
	case outcome := <-reconciliationDone:
		if outcome.err != nil || outcome.result.DeliveryAttempt.State != deploymentnative.AttemptCommitted || outcome.result.DuckLakeAttempt.State != ducklakepostgres.AttemptCommitted {
			t.Fatalf("reconciliation after admission = %#v, %v", outcome.result, outcome.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reconciliation remained blocked after admission committed")
	}
}
