package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testRepository(t *testing.T) (*Repository, *pgxpool.Pool) {
	t.Helper()
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "operation_test")
	p, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return NewWithConfig(p, time.Second, time.Hour), p
}

func acquireInput(owner string) AcquireInput {
	return AcquireInput{Scope: "tenant-a", OperationType: "write", IdempotencyKey: "key-1", Request: []byte(`{"b":2,"a":1}`), OwnerID: owner}
}

func TestPostgreSQL18OperationMaintenanceRoleBoundary(t *testing.T) {
	h := postgrestest.Start(t)
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Login: true, Password: "runtime-secret"})
	maintenance := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Login: true, Password: "maintenance-secret"})
	database := h.NewDatabase(t, "operation_retention_roles")
	admin, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(t.Context(), SchemaSQL()); err != nil {
		t.Fatal(err)
	}

	runtimeDB, err := pgxpool.New(t.Context(), database.URL(runtime))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	var runtimeExecute bool
	if err := runtimeDB.QueryRow(t.Context(), `SELECT has_function_privilege(current_user, 'platform.prune_operations(timestamptz, integer)', 'EXECUTE')`).Scan(&runtimeExecute); err != nil {
		t.Fatal(err)
	}
	if runtimeExecute {
		t.Fatal("runtime role has operation prune EXECUTE privilege")
	}
	if _, err := runtimeDB.Exec(t.Context(), `SELECT platform.prune_operations(clock_timestamp(), 1)`); err == nil {
		t.Fatal("runtime operation retention unexpectedly succeeded")
	}

	// Runtime may still create and complete records; only the maintenance role
	// can remove the expired terminal evidence.
	runtimeRepo := NewWithConfig(runtimeDB, time.Second, time.Microsecond)
	completed, err := runtimeRepo.Acquire(t.Context(), AcquireInput{Scope: "role", IdempotencyKey: "terminal", Request: []byte(`{"v":1}`), OwnerID: "runtime", Retention: time.Microsecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeRepo.Complete(t.Context(), completed.Lease, []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}

	maintenanceDB, err := pgxpool.New(t.Context(), database.URL(maintenance))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(maintenanceDB.Close)
	var maintenanceExecute bool
	if err := maintenanceDB.QueryRow(t.Context(), `SELECT has_function_privilege(current_user, 'platform.prune_operations(timestamptz, integer)', 'EXECUTE')`).Scan(&maintenanceExecute); err != nil {
		t.Fatal(err)
	}
	if !maintenanceExecute {
		t.Fatal("maintenance role is missing operation prune EXECUTE privilege")
	}
	removed, err := NewMaintenance(maintenanceDB).Prune(t.Context(), time.Now().UTC().Add(time.Hour), 1000)
	if err != nil {
		t.Fatalf("maintenance operation retention: %v", err)
	}
	if removed != 1 {
		t.Fatalf("maintenance removed %d operation rows, want 1", removed)
	}
}

func TestCanonicalDigestAndConcurrentAcquire(t *testing.T) {
	r, _ := testRepository(t)
	d1, err := RequestDigest([]byte(`{"a":1,"b":2}`))
	if err != nil {
		t.Fatal(err)
	}
	d2, err := RequestDigest([]byte(" { \"b\": 2, \"a\": 1 } "))
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatalf("canonical digest mismatch: %s != %s", d1, d2)
	}
	if _, err := RequestDigest([]byte(`{"outer":{"x":1,"x":2}}`)); err == nil {
		t.Fatal("nested duplicate request key was accepted")
	}
	if _, err := RequestDigest([]byte(`{"value":"` + strings.Repeat("x", 1<<20) + `"}`)); err == nil {
		t.Fatal("oversized request was accepted")
	}
	const n = 8
	results := make(chan AcquireResult, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := r.Acquire(context.Background(), AcquireInput{Scope: "tenant-a", OperationType: "write", IdempotencyKey: "same", Request: []byte(`{"x":1}`), OwnerID: "owner-" + string(rune('a'+i))})
			results <- got
			errs <- err
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	owners := 0
	busy := 0
	for got := range results {
		if got.Status == StatusAcquired {
			owners++
		}
		if got.Status == StatusBusy {
			busy++
		}
	}
	for err := range errs {
		if err != nil && !errors.Is(err, ErrBusy) {
			t.Fatalf("acquire error: %v", err)
		}
		if errors.Is(err, ErrBusy) {
			continue
		}
	}
	if owners != 1 || busy != n-1 {
		t.Fatalf("owners=%d busy=%d, want one owner and %d busy", owners, busy, n-1)
	}
}

func TestReplayConflictFenceAndRollback(t *testing.T) {
	r, p := testRepository(t)
	first, err := r.Acquire(t.Context(), AcquireInput{Scope: "s", IdempotencyKey: "k", Request: []byte(`{"v":1}`), OwnerID: "one"})
	if err != nil {
		t.Fatal(err)
	}
	operationID, parseErr := uuid.Parse(first.Operation.OperationID)
	if parseErr != nil || operationID.String() != first.Operation.OperationID || operationID.Version() != 7 {
		t.Fatalf("operation ID = %q, want canonical UUIDv7: %v", first.Operation.OperationID, parseErr)
	}
	if err := r.Complete(t.Context(), first.Lease, []byte(` { "ok": true } `)); err != nil {
		t.Fatal(err)
	}
	replay, err := r.Acquire(t.Context(), AcquireInput{Scope: "s", IdempotencyKey: "k", Request: []byte(`{"v":1}`), OwnerID: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replay || replay.Status != StatusReplay || string(replay.Operation.Outcome) != `{"ok":true}` {
		t.Fatalf("unexpected replay: %#v", replay)
	}
	if _, err := r.Acquire(t.Context(), AcquireInput{Scope: "s", IdempotencyKey: "k", Request: []byte(`{"v":2}`), OwnerID: "two"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed digest err=%v", err)
	}
	// A transaction rollback must remove both acquire and any caller mutation.
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AcquireTx(t.Context(), tx, AcquireInput{Scope: "rollback", IdempotencyKey: "k", Request: []byte(`{"v":1}`), OwnerID: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Acquire(t.Context(), AcquireInput{Scope: "rollback", IdempotencyKey: "k", Request: []byte(`{"v":1}`), OwnerID: "two"}); err != nil || got.Status != StatusAcquired {
		t.Fatalf("rollback acquire got=%#v err=%v", got, err)
	}
}

func TestDirectSQLOperationLifecycleGuard(t *testing.T) {
	r, p := testRepository(t)
	if _, err := p.Exec(t.Context(), `
		INSERT INTO platform.operation
		 (operation_id, scope_id, operation_type, idempotency_key, request_digest,
		  state, owner_id, lease_expires_at, fencing_generation, outcome,
		  created_at, updated_at, terminal_at, retention_interval, expires_at)
		VALUES ('00000000-0000-0000-0000-000000000099', 'guard-insert', 'write', 'key',
		        'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
		        'completed', 'owner', clock_timestamp()+interval '1 hour', 1,
		        '{"forged":true}'::jsonb, clock_timestamp(), clock_timestamp(),
		        clock_timestamp(), interval '1 hour', clock_timestamp()+interval '1 hour')`); err == nil {
		t.Fatal("direct terminal operation insert unexpectedly succeeded")
	}
	acquired, err := r.Acquire(t.Context(), AcquireInput{Scope: "guard", IdempotencyKey: "key", Request: []byte(`{"v":1}`), OwnerID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE platform.operation SET scope_id='tampered' WHERE operation_id=$1`, acquired.Lease.OperationID); err == nil {
		t.Fatal("direct identity mutation unexpectedly succeeded")
	}
	if _, err := p.Exec(t.Context(), `UPDATE platform.operation SET fencing_generation=fencing_generation-1 WHERE operation_id=$1`, acquired.Lease.OperationID); err == nil {
		t.Fatal("direct fencing rollback unexpectedly succeeded")
	}
	if _, err := p.Exec(t.Context(), `UPDATE platform.operation SET outcome='{"forged":true}'::jsonb WHERE operation_id=$1`, acquired.Lease.OperationID); err == nil {
		t.Fatal("direct pending outcome mutation unexpectedly succeeded")
	}
	if _, err := p.Exec(t.Context(), `UPDATE platform.operation SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE operation_id=$1`, acquired.Lease.OperationID); err == nil {
		t.Fatal("direct pending lease shortening unexpectedly succeeded")
	}
	if err := r.Complete(t.Context(), acquired.Lease, []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE platform.operation SET state='pending', terminal_at=NULL WHERE operation_id=$1`, acquired.Lease.OperationID); err == nil {
		t.Fatal("terminal operation was reopened by direct SQL")
	}
}

func TestExpiredTakeoverAndIndeterminateReconciliation(t *testing.T) {
	r, _ := testRepository(t)
	now := time.Now().UTC()
	r.clock = func() time.Time { return now }
	old, err := r.Acquire(t.Context(), AcquireInput{Scope: "s", IdempotencyKey: "no-attempt", Request: []byte(`{"v":1}`), OwnerID: "old", Lease: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	next, err := r.Acquire(t.Context(), AcquireInput{Scope: "s", IdempotencyKey: "no-attempt", Request: []byte(`{"v":1}`), OwnerID: "new", Lease: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != StatusAcquired || next.Lease.FencingGeneration != old.Lease.FencingGeneration+1 {
		t.Fatalf("takeover=%#v", next)
	}
	if err := r.Complete(t.Context(), old.Lease, []byte(`{"old":true}`)); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale completion=%v", err)
	}
	withAttempt, err := r.Acquire(t.Context(), AcquireInput{Scope: "s", IdempotencyKey: "external", Request: []byte(`{"v":1}`), OwnerID: "owner", Lease: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	attemptID, _ := newUUID()
	attempt, err := r.BeginAttempt(t.Context(), BeginAttemptInput{Lease: withAttempt.Lease, AttemptID: attemptID, AttemptIdentity: "external-commit-1"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	ind, err := r.Acquire(t.Context(), AcquireInput{Scope: "s", IdempotencyKey: "external", Request: []byte(`{"v":1}`), OwnerID: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if ind.Status != StatusIndeterminate || ind.Operation.State != StateIndeterminate {
		t.Fatalf("indeterminate=%#v", ind)
	}
	if ind.Operation.FencingGeneration != attempt.Lease.FencingGeneration+1 {
		t.Fatalf("indeterminate fence=%d, want %d", ind.Operation.FencingGeneration, attempt.Lease.FencingGeneration+1)
	}
	if err := r.Complete(t.Context(), attempt.Lease, []byte(`{"late":true}`)); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("indeterminate predecessor completion=%v", err)
	}
	reconciliation := ReconcileAttemptInput{Scope: "s", IdempotencyKey: "external", AttemptID: attempt.AttemptID, AttemptIdentity: attempt.AttemptIdentity, State: StateCompleted, Outcome: []byte(`{"committed":true}`), Evidence: []byte(`{"commit_id":"c1"}`)}
	if _, err := r.ReconcileAttempt(t.Context(), ReconcileAttemptInput{Scope: "s", IdempotencyKey: "external", AttemptID: attempt.AttemptID, AttemptIdentity: attempt.AttemptIdentity, State: StateCompleted, Outcome: []byte(`{"committed":true}`), Evidence: []byte(`{}`)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty reconciliation evidence=%v", err)
	}
	if _, err := r.ReconcileAttempt(t.Context(), reconciliation); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReconcileAttempt(t.Context(), reconciliation); err != nil {
		t.Fatalf("exact reconciliation replay: %v", err)
	}
	changed := reconciliation
	changed.Evidence = []byte(`{"commit_id":"different"}`)
	if _, err := r.ReconcileAttempt(t.Context(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed reconciliation evidence=%v", err)
	}
	replay, err := r.Acquire(t.Context(), AcquireInput{Scope: "s", IdempotencyKey: "external", Request: []byte(`{"v":1}`), OwnerID: "other"})
	if err != nil || replay.Status != StatusReplay {
		t.Fatalf("resolved replay=%#v err=%v", replay, err)
	}
}

func TestAttemptEvidenceAndLeaseBounds(t *testing.T) {
	r, _ := testRepository(t)
	acquired, err := r.Acquire(t.Context(), AcquireInput{Scope: "s", IdempotencyKey: "bounded", Request: []byte(`{"v":1}`), OwnerID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RenewLease(t.Context(), acquired.Lease, maxLeaseDuration+time.Second); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized lease renewal=%v", err)
	}
	attempt, err := r.BeginAttempt(t.Context(), BeginAttemptInput{Lease: acquired.Lease, AttemptIdentity: "warehouse-job-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.MarkIndeterminate(t.Context(), attempt.Lease, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing attempt evidence=%v", err)
	}
	if err := r.MarkIndeterminate(t.Context(), attempt.Lease, []byte(`{"warehouse_job":"warehouse-job-1"}`)); err != nil {
		t.Fatal(err)
	}
	if err := r.Fail(t.Context(), attempt.Lease, []byte(`{"late":true}`)); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("indeterminate attempt fence=%v", err)
	}
}

func TestPruneSafety(t *testing.T) {
	r, p := testRepository(t)
	completed, err := r.Acquire(t.Context(), AcquireInput{Scope: "s", IdempotencyKey: "terminal", Request: []byte(`{"v":1}`), OwnerID: "one", Retention: time.Microsecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Complete(t.Context(), completed.Lease, []byte(`{"ok":1}`)); err != nil {
		t.Fatal(err)
	}
	pending, err := r.Acquire(t.Context(), AcquireInput{Scope: "s", IdempotencyKey: "pending", Request: []byte(`{"v":1}`), OwnerID: "one", Retention: time.Microsecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewMaintenance(p).Prune(t.Context(), time.Now().Add(time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM platform.operation WHERE scope_id='s' AND idempotency_key='terminal'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("terminal should prune, count=%d", count)
	}
	if got, err := r.Acquire(t.Context(), AcquireInput{Scope: "s", IdempotencyKey: "pending", Request: []byte(`{"v":1}`), OwnerID: "one"}); err != nil || got.Operation.OperationID != pending.Operation.OperationID {
		t.Fatalf("pending prune got=%#v err=%v", got, err)
	}
}

func TestMaintenancePrunePreservesPendingAndIndeterminate(t *testing.T) {
	r, p := testRepository(t)

	completed, err := r.Acquire(t.Context(), AcquireInput{Scope: "retention", IdempotencyKey: "completed", Request: []byte(`{"v":1}`), OwnerID: "owner", Retention: time.Microsecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Complete(t.Context(), completed.Lease, []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	pending, err := r.Acquire(t.Context(), AcquireInput{Scope: "retention", IdempotencyKey: "pending", Request: []byte(`{"v":1}`), OwnerID: "owner", Retention: time.Microsecond})
	if err != nil {
		t.Fatal(err)
	}
	withAttempt, err := r.Acquire(t.Context(), AcquireInput{Scope: "retention", IdempotencyKey: "indeterminate", Request: []byte(`{"v":1}`), OwnerID: "owner", Lease: 100 * time.Millisecond, Retention: time.Microsecond})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.BeginAttempt(t.Context(), BeginAttemptInput{Lease: withAttempt.Lease, AttemptIdentity: "external-retention-attempt"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	indeterminate, err := r.Acquire(t.Context(), AcquireInput{Scope: "retention", IdempotencyKey: "indeterminate", Request: []byte(`{"v":1}`), OwnerID: "other", Lease: time.Second})
	if err != nil || indeterminate.Status != StatusIndeterminate {
		t.Fatalf("indeterminate acquisition = %#v, %v", indeterminate, err)
	}

	maintenance := NewMaintenance(p)
	removed, err := maintenance.Prune(t.Context(), time.Now().UTC().Add(time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("bounded prune removed %d rows, want 1", removed)
	}
	if removed, err = maintenance.Prune(t.Context(), time.Now().UTC().Add(time.Hour), 1000); err != nil {
		t.Fatal(err)
	} else if removed != 0 {
		t.Fatalf("idempotent prune retry removed %d rows, want 0", removed)
	}
	var pendingState, indeterminateState string
	if err := p.QueryRow(t.Context(), `SELECT state FROM platform.operation WHERE operation_id=$1`, pending.Lease.OperationID).Scan(&pendingState); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(t.Context(), `SELECT state FROM platform.operation WHERE scope_id='retention' AND idempotency_key='indeterminate'`).Scan(&indeterminateState); err != nil {
		t.Fatal(err)
	}
	if pendingState != string(StatePending) || indeterminateState != string(StateIndeterminate) {
		t.Fatalf("retention states pending=%q indeterminate=%q", pendingState, indeterminateState)
	}
}

func TestMaintenancePruneConcurrentBatches(t *testing.T) {
	r, p := testRepository(t)
	const terminalRows = 17
	for i := 0; i < terminalRows; i++ {
		key := "concurrent-" + string(rune('a'+i))
		acquired, err := r.Acquire(t.Context(), AcquireInput{Scope: "concurrent-retention", IdempotencyKey: key, Request: []byte(`{"v":1}`), OwnerID: "owner", Retention: time.Microsecond})
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Complete(t.Context(), acquired.Lease, []byte(`{"ok":true}`)); err != nil {
			t.Fatal(err)
		}
	}

	maintenance := NewMaintenance(p)
	start := make(chan struct{})
	removed := make(chan int64, 4)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var total int64
			for {
				batch, err := maintenance.Prune(t.Context(), time.Now().UTC().Add(time.Hour), 2)
				if err != nil {
					t.Errorf("concurrent operation prune: %v", err)
					return
				}
				if batch == 0 {
					break
				}
				total += batch
			}
			removed <- total
		}()
	}
	close(start)
	wg.Wait()
	close(removed)
	var total int64
	for count := range removed {
		total += count
	}
	if total != terminalRows {
		t.Fatalf("concurrent operation prune removed %d rows, want %d", total, terminalRows)
	}
	var remaining int
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM platform.operation WHERE scope_id='concurrent-retention'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("concurrent operation prune left %d rows", remaining)
	}
}
