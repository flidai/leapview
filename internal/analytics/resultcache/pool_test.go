package resultcache

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/flidai/leapview/internal/analytics/arrowdecode"
)

func TestPoolEnforcesRuntimeAndNodeBudgets(t *testing.T) {
	pool, err := New(Limits{PartitionEntries: 2, PartitionBytes: 1 << 20, NodeEntries: 4, NodeBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	a1 := mustScope(t, pool, ScopeID{RuntimeID: "a1", PartitionID: "a1"})
	a2 := mustScope(t, pool, ScopeID{RuntimeID: "a2", PartitionID: "a2"})
	b1 := mustScope(t, pool, ScopeID{RuntimeID: "b1", PartitionID: "b1"})

	put(t, a1, "a1-1", "one")
	put(t, a1, "a1-2", "two")
	put(t, a1, "a1-3", "three")
	assertMiss(t, a1, "a1-1")

	put(t, a2, "a2-1", "four")
	put(t, a2, "a2-2", "five")
	put(t, b1, "b1-1", "six")
	put(t, b1, "b1-2", "seven")
	if got := pool.Stats().Entries; got != 4 {
		t.Fatalf("node entries = %d, want 4", got)
	}
	if pool.Stats().Evictions[ConstraintPartition] == 0 || pool.Stats().Evictions[ConstraintNode] == 0 {
		t.Fatalf("evictions = %#v", pool.Stats().Evictions)
	}
}

func TestScopeInvalidationPreventsStaleStoreAndClosePreservesPartitionEntries(t *testing.T) {
	pool, _ := New(testLimits())
	scope := mustScope(t, pool, ScopeID{RuntimeID: "one", PartitionID: "one"})
	token := scope.Generation()
	scope.Fence()
	stale := testArrowResult(t, memory.DefaultAllocator, "stale")
	if outcome := scope.StoreArrow("stale", token, stale, Metadata{}); outcome != StoreStale {
		t.Fatalf("outcome = %q", outcome)
	}
	stale.Release()
	put(t, scope, "live", "live")
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	if stats := pool.Stats(); stats.Entries != 1 || stats.Bytes == 0 {
		t.Fatalf("stats after close = %#v", stats)
	}
	closed := testArrowResult(t, memory.DefaultAllocator, "closed")
	if outcome := scope.StoreArrow("closed", scope.Generation(), closed, Metadata{}); outcome != StoreClosed {
		t.Fatalf("outcome = %q", outcome)
	}
	closed.Release()
}

func TestStablePartitionEntriesSurviveOverlappingRuntimeCloseAndFenceWrites(t *testing.T) {
	pool, _ := New(testLimits())
	first := mustScope(t, pool, ScopeID{RuntimeID: "generation-1", PartitionID: "production:project:env"})
	second := mustScope(t, pool, ScopeID{RuntimeID: "generation-2", PartitionID: "production:project:env"})
	put(t, first, "query", "stable")
	entry, _, hit, err := second.LookupArrow("query")
	if err != nil || !hit {
		t.Fatalf("overlapping generation lookup hit=%v err=%v", hit, err)
	}
	entry.Release()
	first.Fence()
	stale := testArrowResult(t, memory.DefaultAllocator, "stale")
	if outcome := first.StoreArrow("stale", 0, stale, Metadata{}); outcome != StoreStale {
		t.Fatalf("invalidated generation accepted stale write: %q", outcome)
	}
	stale.Release()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	entry, _, hit, err = second.LookupArrow("query")
	if err != nil || !hit {
		t.Fatalf("entry disappeared after first generation close hit=%v err=%v", hit, err)
	}
	entry.Release()
	if got := second.Stats().Entries; got != 1 {
		t.Fatalf("stable partition stats entries=%d", got)
	}
	second.Clear()
	entry, _, hit, err = second.LookupArrow("query")
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		entry.Release()
		t.Fatal("partition clear left entry visible to overlapping scope")
	}
	_ = second.Close()
}

func TestOversizedEntryIsSkipped(t *testing.T) {
	pool, _ := New(Limits{PartitionEntries: 2, PartitionBytes: 64, NodeEntries: 2, NodeBytes: 64})
	scope := mustScope(t, pool, ScopeID{RuntimeID: "one", PartitionID: "one"})
	large := testArrowResult(t, memory.DefaultAllocator, string(make([]byte, 256)))
	if outcome := scope.StoreArrow("large", scope.Generation(), large, Metadata{}); outcome != StoreOversized {
		t.Fatalf("outcome = %q", outcome)
	}
	large.Release()
	if pool.Stats().Entries != 0 {
		t.Fatal("oversized entry was retained")
	}
}

func TestByteEntriesAreImmutableAndShareCacheBudgets(t *testing.T) {
	pool, _ := New(testLimits())
	scope := mustScope(t, pool, ScopeID{RuntimeID: "one", PartitionID: "one"})
	original := []byte{1, 2, 3}
	if outcome := scope.StoreBytes("tile", scope.Generation(), original); outcome != StoreStored {
		t.Fatalf("store bytes outcome = %q", outcome)
	}
	original[0] = 9
	first, _, hit, err := scope.LookupBytes("tile")
	if err != nil || !hit || first[0] != 1 {
		t.Fatalf("first lookup = %v hit=%v err=%v", first, hit, err)
	}
	first[1] = 9
	second, _, hit, err := scope.LookupBytes("tile")
	if err != nil || !hit || second[1] != 2 {
		t.Fatalf("second lookup = %v hit=%v err=%v", second, hit, err)
	}
	if stats := scope.Stats(); stats.Entries != 1 || stats.Bytes == 0 {
		t.Fatalf("byte entry stats = %#v", stats)
	}
}

func TestByteEntriesRetainValidEmptyPayloads(t *testing.T) {
	pool, _ := New(testLimits())
	scope := mustScope(t, pool, ScopeID{RuntimeID: "one", PartitionID: "one"})
	if outcome := scope.StoreBytes("empty-tile", scope.Generation(), []byte{}); outcome != StoreStored {
		t.Fatalf("store empty bytes outcome = %q", outcome)
	}
	value, _, hit, err := scope.LookupBytes("empty-tile")
	if err != nil || !hit || len(value) != 0 {
		t.Fatalf("empty lookup = %v hit=%v err=%v", value, hit, err)
	}
}

func BenchmarkWarmTileByteLookup(b *testing.B) {
	pool, err := New(testLimits())
	if err != nil {
		b.Fatal(err)
	}
	scope, err := pool.OpenScope(ScopeID{RuntimeID: "serving-1", PartitionID: "serving-1"})
	if err != nil {
		b.Fatal(err)
	}
	defer scope.Close()
	tile := make([]byte, 128*1024)
	if outcome := scope.StoreBytes("z10/376/512", scope.Generation(), tile); outcome != StoreStored {
		b.Fatalf("store outcome = %q", outcome)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(tile)))
	b.ResetTimer()
	for range b.N {
		value, _, hit, err := scope.LookupBytes("z10/376/512")
		if err != nil || !hit || len(value) != len(tile) {
			b.Fatalf("warm lookup hit=%v bytes=%d err=%v", hit, len(value), err)
		}
	}
}

func TestCoalesceCancellationDoesNotPoisonLiveWaiter(t *testing.T) {
	scope := NewExecutionScope()
	defer scope.Close()
	owner, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	go func() {
		_, _, _ = scope.Coalesce(owner, "key", func(context.Context) (any, error) {
			calls.Add(1)
			close(started)
			<-release
			return "owner", owner.Err()
		})
	}()
	<-started
	cancel()
	close(release)
	value, _, err := scope.Coalesce(context.Background(), "key", func(context.Context) (any, error) { calls.Add(1); return "live", nil })
	if err != nil || value != "live" {
		t.Fatalf("value=%v err=%v", value, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestCoalesceArrowReturnsIndependentLeasesAndReleasesFlightHold(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer allocator.AssertSize(t, 0)
	pool, _ := New(testLimits())
	defer pool.Close()
	scope := NewExecutionScope()
	defer scope.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	result := testArrowResult(t, allocator, "shared")
	base, err := result.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	result.Release()
	var calls atomic.Int32
	type response struct {
		lease  *ArrowFlightLease
		status ArrowFlightStatus
		err    error
	}
	responses := make(chan response, 2)
	execute := func(context.Context) (ArrowFlightValue, error) {
		calls.Add(1)
		close(started)
		<-release
		return ArrowFlightValue{Data: base, Metadata: Metadata{SQL: "metadata"}}, nil
	}
	go func() {
		lease, status, executeErr := scope.CoalesceArrow(context.Background(), "key", execute)
		responses <- response{lease: lease, status: status, err: executeErr}
	}()
	<-started
	go func() {
		lease, status, executeErr := scope.CoalesceArrow(context.Background(), "key", execute)
		responses <- response{lease: lease, status: status, err: executeErr}
	}()
	waitForArrowFlightWaiters(t, scope, "key", 2)
	close(release)
	first, second := <-responses, <-responses
	if first.err != nil || second.err != nil {
		t.Fatalf("coalesced errors = (%v, %v)", first.err, second.err)
	}
	if calls.Load() != 1 {
		t.Fatalf("executions = %d, want 1", calls.Load())
	}
	if !first.status.Shared || !second.status.Shared {
		t.Fatalf("statuses = (%#v, %#v), want shared", first.status, second.status)
	}
	if first.lease == second.lease || first.lease.Data() == second.lease.Data() {
		t.Fatal("coalesced callers received the same lease")
	}
	if first.lease.Metadata().SQL != "metadata" || second.lease.Metadata().SQL != "metadata" {
		t.Fatalf("metadata = (%v, %v)", first.lease.Metadata(), second.lease.Metadata())
	}
	first.lease.Release()
	rows, err := arrowdecode.DecodeRows(second.lease.Data())
	if err != nil {
		t.Fatal(err)
	}
	if got := rows[0]["value"]; got != "shared" {
		t.Fatalf("second leased value = %#v", got)
	}
	second.lease.Release()
}

func TestCoalesceArrowCanceledWaiterDoesNotLeakOrCancelLiveWaiter(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer allocator.AssertSize(t, 0)
	pool, _ := New(testLimits())
	defer pool.Close()
	scope := NewExecutionScope()
	defer scope.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	owner, cancel := context.WithCancel(context.Background())
	ownerDone := make(chan error, 1)
	go func() {
		_, _, err := scope.CoalesceArrow(owner, "key", func(context.Context) (ArrowFlightValue, error) {
			close(started)
			<-release
			result := testArrowResult(t, allocator, "live")
			base, acquireErr := result.Acquire()
			result.Release()
			return ArrowFlightValue{Data: base}, acquireErr
		})
		ownerDone <- err
	}()
	<-started
	liveDone := make(chan *ArrowFlightLease, 1)
	liveErr := make(chan error, 1)
	go func() {
		lease, _, err := scope.CoalesceArrow(context.Background(), "key", func(context.Context) (ArrowFlightValue, error) {
			result := testArrowResult(t, allocator, "replacement")
			base, acquireErr := result.Acquire()
			result.Release()
			return ArrowFlightValue{Data: base}, acquireErr
		})
		liveDone <- lease
		liveErr <- err
	}()
	waitForArrowFlightWaiters(t, scope, "key", 2)
	cancel()
	close(release)
	if err := <-ownerDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("owner error = %v, want cancellation", err)
	}
	if err := <-liveErr; err != nil {
		t.Fatal(err)
	}
	lease := <-liveDone
	if lease == nil || lease.Data().Rows() != 1 {
		t.Fatalf("live lease = %#v", lease)
	}
	lease.Release()
}

func waitForArrowFlightWaiters(t *testing.T, scope *ExecutionScope, key string, want int) {
	t.Helper()
	for {
		scope.mu.Lock()
		flight := scope.arrowFlights[key]
		ready := flight != nil && flight.waiters >= want
		scope.mu.Unlock()
		if ready {
			return
		}
		runtime.Gosched()
	}
}

func TestPoolConcurrentStatsInvalidateAndClose(t *testing.T) {
	pool, _ := New(testLimits())
	scope := mustScope(t, pool, ScopeID{RuntimeID: "one", PartitionID: "one"})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = pool.Stats()
			scope.Fence()
			value := testArrowResult(t, memory.DefaultAllocator, "value")
			scope.StoreArrow("key", scope.Generation(), value, Metadata{})
			value.Release()
		}()
	}
	wg.Wait()
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
}

func testLimits() Limits {
	return Limits{PartitionEntries: 8, PartitionBytes: 1 << 20, NodeEntries: 32, NodeBytes: 4 << 20}
}
func mustScope(t *testing.T, p *Pool, id ScopeID) *Scope {
	t.Helper()
	s, err := p.OpenScope(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func put(t *testing.T, s *Scope, key, value string) {
	t.Helper()
	result := testArrowResult(t, memory.DefaultAllocator, value)
	defer result.Release()
	if got := s.StoreArrow(key, s.Generation(), result, Metadata{}); got != StoreStored {
		t.Fatalf("store %q = %q", key, got)
	}
}
func assertMiss(t *testing.T, s *Scope, key string) {
	t.Helper()
	if _, _, ok, err := s.LookupArrow(key); err != nil || ok {
		t.Fatalf("lookup %q ok=%v err=%v", key, ok, err)
	}
}
