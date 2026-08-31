package resultcache

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/flidai/leapview/internal/analytics/arrowdecode"
)

func TestSharedResultScopeCutoverQualification(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer allocator.AssertSize(t, 0)
	limits := Limits{RuntimeEntries: 2, RuntimeBytes: 1 << 20, NodeEntries: 3, NodeBytes: 2 << 20}
	pool, err := New(limits)
	if err != nil {
		t.Fatal(err)
	}
	partition := ScopeID{RuntimeID: "qualification-production"}
	generationA, err := pool.OpenSharedScope(partition)
	if err != nil {
		t.Fatal(err)
	}
	warm := testArrowResult(t, allocator, "warm-a")
	if outcome := generationA.StoreArrow("warm", generationA.Generation(), warm, Metadata{}); outcome != StoreStored {
		warm.Release()
		t.Fatalf("warm store = %q, want %q", outcome, StoreStored)
	}
	warm.Release()

	generationB, err := pool.OpenSharedScope(partition)
	if err != nil {
		t.Fatal(err)
	}
	activeLease, _, hit, observation, err := generationB.LookupArrowObserved("warm", QueryFamily{1})
	if err != nil || !hit || observation.HitSource != HitSharedGeneration {
		t.Fatalf("overlapping generation lookup hit=%v observation=%#v err=%v", hit, observation, err)
	}
	if err := generationA.Close(); err != nil {
		t.Fatal(err)
	}
	assertQualificationLeaseValue(t, activeLease, "warm-a")
	if entry, _, ok, err := generationB.LookupArrow("warm"); err != nil || !ok {
		t.Fatalf("generation B warm lookup hit=%v err=%v", ok, err)
	} else {
		entry.Release()
	}
	if err := generationB.Close(); err != nil {
		t.Fatal(err)
	}
	if stats := pool.Stats(); stats.Stable.ActiveScopes != 0 || stats.Stable.DormantScopes != 1 || stats.Stable.Entries != 1 {
		t.Fatalf("zero-reference stable state = %#v", stats.Stable)
	}

	generationC, err := pool.OpenSharedScope(partition)
	if err != nil {
		t.Fatal(err)
	}
	retained, token, hit, observation, err := generationC.LookupArrowObserved("warm", QueryFamily{1})
	if err != nil || !hit || observation.HitSource != HitCutoverRetained {
		t.Fatalf("reactivated generation lookup hit=%v observation=%#v err=%v", hit, observation, err)
	}
	assertQualificationLeaseValue(t, retained, "warm-a")

	generationC.Invalidate()
	stale := testArrowResult(t, allocator, "stale")
	if outcome := generationC.StoreArrow("warm", token, stale, Metadata{}); outcome != StoreStale {
		stale.Release()
		t.Fatalf("stale store = %q, want %q", outcome, StoreStale)
	}
	stale.Release()
	if _, _, ok, observation, err := generationC.LookupArrowObserved("warm", QueryFamily{1}); err != nil || ok || observation.MissReason != LookupMissInvalidated {
		t.Fatalf("post-invalidation lookup hit=%v observation=%#v err=%v", ok, observation, err)
	}
	assertQualificationLeaseValue(t, activeLease, "warm-a")
	assertQualificationLeaseValue(t, retained, "warm-a")
	retained.Release()

	first := testArrowResult(t, allocator, "evict-one")
	if outcome := generationC.StoreArrow("one", generationC.Generation(), first, Metadata{}); outcome != StoreStored {
		first.Release()
		t.Fatalf("first eviction store = %q", outcome)
	}
	first.Release()
	evictionLease, _, hit, err := generationC.LookupArrow("one")
	if err != nil || !hit {
		t.Fatalf("eviction lease lookup hit=%v err=%v", hit, err)
	}
	for index, value := range []string{"evict-two", "evict-three"} {
		result := testArrowResult(t, allocator, value)
		if outcome := generationC.StoreArrow(fmt.Sprintf("entry-%d", index), generationC.Generation(), result, Metadata{}); outcome != StoreStored {
			result.Release()
			t.Fatalf("pressure store %d = %q", index, outcome)
		}
		result.Release()
	}
	if _, _, ok, err := generationC.LookupArrow("one"); err != nil || ok {
		t.Fatalf("evicted cache entry hit=%v err=%v", ok, err)
	}
	assertQualificationLeaseValue(t, evictionLease, "evict-one")
	stats := pool.Stats()
	if stats.Entries > limits.NodeEntries || stats.Bytes > limits.NodeBytes || stats.Stable.Entries > limits.NodeEntries || stats.Stable.Bytes > limits.NodeBytes {
		t.Fatalf("cache accounting exceeded limits: %#v", stats)
	}
	if stats.Stable.ArrowHolds != stats.Stable.Entries {
		t.Fatalf("stable Arrow holds = %d, entries = %d", stats.Stable.ArrowHolds, stats.Stable.Entries)
	}
	if stats.Stores[StoreStale] != 1 || stats.ClassEvictions[CacheClassStableResult][ConstraintRuntime] == 0 {
		t.Fatalf("qualification outcomes = stores %#v evictions %#v", stats.Stores, stats.ClassEvictions)
	}

	if err := generationC.Close(); err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	assertQualificationLeaseValue(t, activeLease, "warm-a")
	assertQualificationLeaseValue(t, evictionLease, "evict-one")
	activeLease.Release()
	evictionLease.Release()
}

func TestExecutionScopesCutoverQualification(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const coldKeys = 4
		const generations = 2
		const callersPerFlight = 2
		generationA := NewExecutionScope()
		generationB := NewExecutionScope()
		release := make(chan struct{})
		var executions atomic.Int32
		type response struct {
			value  any
			shared bool
			err    error
		}
		responses := make(chan response, coldKeys*generations*callersPerFlight)

		execute := func(ctx context.Context) (any, error) {
			executions.Add(1)
			select {
			case <-release:
				return "qualified", nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		launch := func(scope *ExecutionScope, key string) {
			go func() {
				value, shared, err := scope.Coalesce(context.Background(), key, execute)
				responses <- response{value: value, shared: shared, err: err}
			}()
		}

		for index := range coldKeys {
			key := fmt.Sprintf("cold-%d", index)
			launch(generationA, key)
			launch(generationB, key)
		}
		synctest.Wait()
		if got, want := executions.Load(), int32(coldKeys*generations); got != want {
			t.Errorf("initial physical executions = %d, want %d (one owner per key and generation); a generation owner is missing or joined across generations", got, want)
		}

		for index := range coldKeys {
			key := fmt.Sprintf("cold-%d", index)
			launch(generationA, key)
			launch(generationB, key)
		}
		synctest.Wait()
		if got, want := executions.Load(), int32(coldKeys*generations); got != want {
			t.Errorf("physical executions after same-generation joins = %d, want %d; same-generation callers did not coalesce", got, want)
		}

		close(release)
		synctest.Wait()
		if got, want := len(responses), coldKeys*generations*callersPerFlight; got != want {
			t.Fatalf("completed coalesced responses = %d, want %d after releasing owners", got, want)
		}
		for range coldKeys * generations * callersPerFlight {
			response := <-responses
			if response.err != nil || response.value != "qualified" || !response.shared {
				t.Fatalf("coalesced response = %#v", response)
			}
		}
		if err := generationA.Close(); err != nil {
			t.Fatal(err)
		}
		if err := generationB.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func assertQualificationLeaseValue(t *testing.T, entry *EntryLease, want string) {
	t.Helper()
	if entry == nil || entry.Data() == nil {
		t.Fatal("qualification lease is unavailable")
	}
	rows, err := arrowdecode.DecodeRows(entry.Data())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["value"] != want {
		t.Fatalf("qualification lease rows = %#v, want value %q", rows, want)
	}
}
