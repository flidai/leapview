package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
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
	if _, err := r.Prune(t.Context(), time.Now().Add(time.Hour), 100); err != nil {
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
