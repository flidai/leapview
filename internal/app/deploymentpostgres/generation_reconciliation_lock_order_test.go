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
// the gated attempt. Reconciliation must join the lock queue behind admission
// before the gate is released. Admission then commits first and
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

	// Holding the lease does not prove admission has reached the attempt lock:
	// it still reads the authoritative plan between those two locks. Observe
	// the database wait queue before letting reconciliation race with it.
	waitCtx, waitCancel := context.WithTimeout(runCtx, 10*time.Second)
	defer waitCancel()
	probeTicker := time.NewTicker(10 * time.Millisecond)
	defer probeTicker.Stop()
	var admissionPID int
	for {
		if err := p.QueryRow(waitCtx, `
			SELECT COALESCE((
				SELECT pid FROM pg_stat_activity
				WHERE datname = current_database()
				  AND $1::integer = ANY(pg_blocking_pids(pid))
			), 0)`, int(gateTx.Conn().PgConn().PID())).Scan(&admissionPID); err != nil {
			t.Fatalf("observe admission waiting on the delivery attempt: %v", err)
		}
		if admissionPID != 0 {
			break
		}
		select {
		case outcome := <-admissionDone:
			t.Fatalf("admission finished before waiting on the delivery attempt: %#v, %v", outcome.result, outcome.err)
		case <-waitCtx.Done():
			t.Fatal("admission did not queue on the gated delivery attempt")
		case <-probeTicker.C:
		}
	}

	// While admission is queued on the attempt, verify it already holds the
	// lease. Only a server lock_timeout proves contention; client deadlines
	// and general query cancellation can also mean a slow runner.
	probeCtx, probeCancel := context.WithTimeout(runCtx, 5*time.Second)
	defer probeCancel()
	probeTx, err := p.Begin(probeCtx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probeTx.Rollback(context.Background()) }()
	if _, err := probeTx.Exec(probeCtx, "SET LOCAL lock_timeout = '100ms'"); err != nil {
		t.Fatal(err)
	}
	_, probeErr := delivery.LockLeaseTx(probeCtx, probeTx, input.Fence.LeaseID)
	_ = probeTx.Rollback(context.Background())
	if !isLockWaitTimeout(probeErr) {
		t.Fatalf("target lease lock probe = %v, want server lock timeout while admission waits on the attempt", probeErr)
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

	// Observe the second waiter before releasing the gate. Merely starting
	// reconciliation does not put it in the lock queue: if its SELECT arrives
	// during gate rollback, it can acquire the unlocked tuple before the
	// already-waiting admission resumes. The test requires admission first,
	// so prove reconciliation is blocked by that exact backend.
	for {
		var queuedBehindAdmission bool
		if err := p.QueryRow(waitCtx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE datname = current_database()
				  AND $1::integer = ANY(pg_blocking_pids(pid))
			)`, admissionPID).Scan(&queuedBehindAdmission); err != nil {
			t.Fatalf("observe reconciliation waiting behind admission: %v", err)
		}
		if queuedBehindAdmission {
			break
		}
		select {
		case outcome := <-reconciliationDone:
			t.Fatalf("reconciliation finished before queueing behind admission: %#v, %v", outcome.result, outcome.err)
		case <-waitCtx.Done():
			t.Fatal("reconciliation did not queue behind admission")
		case <-probeTicker.C:
		}
	}

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
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55P03"
}

func TestIsLockWaitTimeoutRequiresServerLockContention(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "client deadline", err: context.DeadlineExceeded},
		{name: "query cancellation", err: &pgconn.PgError{Code: "57014"}},
		{name: "server lock timeout", err: &pgconn.PgError{Code: "55P03"}, want: true},
		{name: "successful lock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLockWaitTimeout(tc.err); got != tc.want {
				t.Fatalf("lock contention = %t, want %t for %v", got, tc.want, tc.err)
			}
		})
	}
}
