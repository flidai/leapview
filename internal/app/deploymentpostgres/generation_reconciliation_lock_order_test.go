package deploymentpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	lineagepostgres "github.com/flidai/leapview/internal/lineage/postgres"
	servingnative "github.com/flidai/leapview/internal/servingstate/postgres"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestGenerationAdmissionAndReconciliationUsePhysicalThenDeliveryLockOrder
// proves that generation admission acquires the physical fence/quarantine
// scope before the target lease and canonical delivery attempt, then queues on
// the gated attempt. Once the gate is released, admission commits first and
// reconciliation returns an exact committed replay; no second lifecycle
// ledger participates in the ordering. The committed seal also materializes
// one live retention row through the application wiring.
func TestGenerationAdmissionAndReconciliationUsePhysicalThenDeliveryLockOrder(t *testing.T) {
	p := generationAdmissionDB(t)
	delivery := deploymentnative.New(p)
	physical := ducklakepostgres.New(p)
	serving := servingnative.New(p)
	// The physical authority contributes only the seal-derived retention gate;
	// no physical attempt or generation lifecycle row is used by this test.
	schemaTx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ducklakepostgres.ApplySchema(t.Context(), schemaTx); err != nil {
		_ = schemaTx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := schemaTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	input := validGenerationAdmissionInput(t)
	if _, err := ducklakepostgres.RegisterCatalog(t.Context(), p, ducklakepostgres.CatalogIdentity{
		PhysicalPoolID:  input.Seal.PhysicalPoolID,
		CatalogDatabase: input.Seal.CatalogDatabase,
		CatalogID:       input.Seal.CatalogID,
		CatalogUUID:     input.Seal.CatalogUUID,
		MetadataSchema:  "main",
	}); err != nil {
		t.Fatal(err)
	}
	admission, err := NewGenerationAdmission(delivery, serving, lineagepostgres.New(p), physical, &testManagedDataBindingAdmission{}, &testCandidateProvenanceAdmission{})
	if err != nil {
		t.Fatal(err)
	}
	reconciliation, err := NewAttemptTermination(delivery)
	if err != nil {
		t.Fatal(err)
	}
	seedGenerationAdmission(t, delivery, input)

	runCtx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Hold the canonical delivery attempt so admission has to queue after its
	// lease lock. The mutation is rolled back, leaving the fixture running for
	// the real completion and exact reconciliation replay.
	gateTx, err := p.Begin(runCtx)
	if err != nil {
		t.Fatal(err)
	}
	gateOpen := true
	defer func() {
		if gateOpen {
			_ = gateTx.Rollback(context.Background())
		}
	}()
	if _, err := delivery.MarkAttemptIndeterminateTx(runCtx, gateTx, deploymentnative.TerminateAttemptInput{
		AttemptID:    input.Commit.AttemptID,
		OwnerID:      input.Commit.OwnerID,
		FencingEpoch: input.Commit.FencingEpoch,
		Evidence:     json.RawMessage(`{"gate":"canonical-delivery-attempt"}`),
	}); err != nil {
		t.Fatal(err)
	}

	type admissionOutcome struct {
		result GenerationAdmissionResult
		err    error
	}
	admissionDone := make(chan admissionOutcome, 1)
	go func() {
		result, runErr := admission.CompleteBuildAndAdmit(runCtx, input)
		admissionDone <- admissionOutcome{result: result, err: runErr}
	}()

	// A lease-lock probe must time out once admission has acquired the lease
	// and is waiting on the gated attempt. Successful probes are rolled back
	// and retried; the channel below is the synchronization point, not a raw
	// sleep.
	leaseLocked := false
	probeDeadline := time.NewTimer(5 * time.Second)
	defer probeDeadline.Stop()
	probeTicker := time.NewTicker(10 * time.Millisecond)
	defer probeTicker.Stop()
	for !leaseLocked {
		probeCtx, probeCancel := context.WithTimeout(runCtx, 100*time.Millisecond)
		probeTx, beginErr := p.Begin(probeCtx)
		if beginErr != nil {
			probeCancel()
			t.Fatal(beginErr)
		}
		_, probeErr := delivery.LockLeaseTx(probeCtx, probeTx, input.Fence.LeaseID)
		probeCancel()
		_ = probeTx.Rollback(context.Background())
		if isLockWaitTimeout(probeErr) {
			leaseLocked = true
			break
		}
		if probeErr != nil {
			t.Fatalf("target lease lock probe: %v", probeErr)
		}
		select {
		case outcome := <-admissionDone:
			t.Fatalf("admission finished before exposing lease-before-attempt ordering: %#v, %v", outcome.result, outcome.err)
		case <-probeDeadline.C:
			t.Fatal("admission did not acquire the target lease while waiting on the delivery attempt")
		case <-probeTicker.C:
		}
	}

	type reconciliationOutcome struct {
		result AttemptTerminationResult
		err    error
	}
	reconciliationDone := make(chan reconciliationOutcome, 1)
	go func() {
		result, runErr := reconciliation.ReconcileAttempt(runCtx, AttemptReconciliationInput{
			AttemptID:      input.Commit.AttemptID,
			OwnerID:        input.Commit.OwnerID,
			FencingEpoch:   input.Commit.FencingEpoch,
			PhysicalPoolID: input.Seal.PhysicalPoolID,
			SnapshotID:     input.Commit.SnapshotID,
			CommitMarker:   input.Commit.CommitMarker,
			State:          deploymentnative.AttemptCommitted,
		})
		reconciliationDone <- reconciliationOutcome{result: result, err: runErr}
	}()

	// Both callers are now waiting on the same canonical attempt lock. The
	// admission request arrived first, so releasing the gate deterministically
	// lets it commit before reconciliation obtains its exact replay lock.
	if err := gateTx.Rollback(runCtx); err != nil {
		t.Fatal(err)
	}
	gateOpen = false

	select {
	case outcome := <-admissionDone:
		if outcome.err != nil {
			t.Fatalf("generation admission after delivery gate release: %v", outcome.err)
		}
		if outcome.result.AttemptID != input.Commit.AttemptID || outcome.result.Generation.GenerationID != input.Generation.GenerationID {
			t.Fatalf("generation admission result = %#v", outcome.result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("generation admission remained blocked after delivery gate release")
	}
	retention, err := physical.LoadSnapshotRetention(t.Context(), ducklakepostgres.SnapshotRef{
		PhysicalPoolID: input.Seal.PhysicalPoolID,
		CatalogID:      input.Seal.CatalogID,
		SnapshotID:     input.Seal.DuckLakeSnapshotID,
	})
	if err != nil {
		t.Fatalf("load seal-derived snapshot retention: %v", err)
	}
	if retention.PhysicalPoolID != input.Seal.PhysicalPoolID || retention.CatalogID != input.Seal.CatalogID || retention.SnapshotID != input.Seal.DuckLakeSnapshotID || retention.State != ducklakepostgres.RetentionLive {
		t.Fatalf("seal-derived snapshot retention = %+v, want live %s/%s/%d", retention, input.Seal.PhysicalPoolID, input.Seal.CatalogID, input.Seal.DuckLakeSnapshotID)
	}

	select {
	case outcome := <-reconciliationDone:
		if outcome.err != nil {
			t.Fatalf("delivery reconciliation after admission: %v", outcome.err)
		}
		if outcome.result.DeliveryAttempt.AttemptID != input.Commit.AttemptID || outcome.result.DeliveryAttempt.State != deploymentnative.AttemptCommitted {
			t.Fatalf("delivery reconciliation result = %#v", outcome.result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("delivery reconciliation remained blocked after admission committed")
	}
}

func isLockWaitTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "57014"
}
