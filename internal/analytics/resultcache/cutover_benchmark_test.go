package resultcache

import "testing"

var cacheCutoverBenchmarkRows int64

// BenchmarkCacheCutoverLifecycle measures the retained-result operations that
// serving-generation cutover adds around the existing Arrow lookup path. The
// fixtures are bounded and deterministic; physical query planning/execution is
// measured separately by the materialize benchmark.
func BenchmarkCacheCutoverLifecycle(b *testing.B) {
	b.Run("warm_hit/shared_generation", benchmarkCacheCutoverSharedHit)
	b.Run("warm_hit/cutover_retained", benchmarkCacheCutoverRetainedHit)
	b.Run("dormant_scope/reactivate_lookup_close", benchmarkCacheCutoverReactivation)
	b.Run("overlap/open_lookup_close", benchmarkCacheCutoverOverlap)
	b.Run("arrow_lease/retained_after_invalidation", benchmarkCacheCutoverRetainedLease)
}

func benchmarkCacheCutoverSharedHit(b *testing.B) {
	pool, _, stableBytes := newCacheCutoverBenchmarkEntry(b, "shared-hit")
	reader, err := pool.OpenSharedScope(ScopeID{RuntimeID: "shared-hit"})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := reader.Close(); err != nil {
			b.Errorf("close shared reader: %v", err)
		}
	})
	assertCacheCutoverBenchmarkHit(b, reader, HitSharedGeneration)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		lease, _, hit, observation, err := reader.LookupArrowObserved("query", QueryFamily{1})
		if err != nil || !hit || observation.HitSource != HitSharedGeneration {
			b.Fatalf("shared-generation lookup hit=%v observation=%#v err=%v", hit, observation, err)
		}
		cacheCutoverBenchmarkRows = lease.Data().Rows()
		lease.Release()
	}
	b.StopTimer()
	b.ReportMetric(float64(stableBytes), "stable-B")
	assertCacheCutoverBenchmarkAccounting(b, pool, 1, stableBytes)
}

func benchmarkCacheCutoverRetainedHit(b *testing.B) {
	pool, writer, stableBytes := newCacheCutoverBenchmarkEntry(b, "retained-hit")
	if err := writer.Close(); err != nil {
		b.Fatal(err)
	}
	reader, err := pool.OpenSharedScope(ScopeID{RuntimeID: "retained-hit"})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := reader.Close(); err != nil {
			b.Errorf("close retained reader: %v", err)
		}
	})
	assertCacheCutoverBenchmarkHit(b, reader, HitCutoverRetained)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		lease, _, hit, observation, err := reader.LookupArrowObserved("query", QueryFamily{1})
		if err != nil || !hit || observation.HitSource != HitCutoverRetained {
			b.Fatalf("cutover-retained lookup hit=%v observation=%#v err=%v", hit, observation, err)
		}
		cacheCutoverBenchmarkRows = lease.Data().Rows()
		lease.Release()
	}
	b.StopTimer()
	b.ReportMetric(float64(stableBytes), "stable-B")
	assertCacheCutoverBenchmarkAccounting(b, pool, 1, stableBytes)
}

func benchmarkCacheCutoverReactivation(b *testing.B) {
	pool, writer, stableBytes := newCacheCutoverBenchmarkEntry(b, "reactivation")
	if err := writer.Close(); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		reader, err := pool.OpenSharedScope(ScopeID{RuntimeID: "reactivation"})
		if err != nil {
			b.Fatal(err)
		}
		lease, _, hit, observation, err := reader.LookupArrowObserved("query", QueryFamily{1})
		if err != nil || !hit || observation.HitSource != HitCutoverRetained {
			b.Fatalf("reactivation lookup hit=%v observation=%#v err=%v", hit, observation, err)
		}
		cacheCutoverBenchmarkRows = lease.Data().Rows()
		lease.Release()
		if err := reader.Close(); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(stableBytes), "stable-B")
	stats := pool.Stats()
	if stats.Stable.ActiveScopes != 0 || stats.Stable.DormantScopes != 1 {
		b.Fatalf("post-reactivation lifecycle = %#v", stats.Stable)
	}
	b.ReportMetric(float64(stats.ScopeTransitions[ScopeTransitionReactivated])/float64(b.N), "reactivations/op")
	assertCacheCutoverBenchmarkAccounting(b, pool, 1, stableBytes)
}

func benchmarkCacheCutoverOverlap(b *testing.B) {
	pool, _, stableBytes := newCacheCutoverBenchmarkEntry(b, "overlap")

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		reader, err := pool.OpenSharedScope(ScopeID{RuntimeID: "overlap"})
		if err != nil {
			b.Fatal(err)
		}
		lease, _, hit, observation, err := reader.LookupArrowObserved("query", QueryFamily{1})
		if err != nil || !hit || observation.HitSource != HitSharedGeneration {
			b.Fatalf("overlap lookup hit=%v observation=%#v err=%v", hit, observation, err)
		}
		cacheCutoverBenchmarkRows = lease.Data().Rows()
		lease.Release()
		if err := reader.Close(); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(stableBytes), "stable-B")
	stats := pool.Stats()
	if stats.Stable.ActiveScopes != 1 || stats.Stable.DormantScopes != 0 {
		b.Fatalf("overlapping generation lifecycle = %#v", stats.Stable)
	}
	assertCacheCutoverBenchmarkAccounting(b, pool, 1, stableBytes)
}

func benchmarkCacheCutoverRetainedLease(b *testing.B) {
	pool, scope, _ := newCacheCutoverBenchmarkEntry(b, "retained-lease")
	consumer, _, hit, err := scope.LookupArrow("query")
	if err != nil || !hit {
		b.Fatalf("acquire retained consumer hit=%v err=%v", hit, err)
	}
	b.Cleanup(consumer.Release)
	retainedBytes := consumer.Data().Bytes()
	scope.Invalidate()
	stats := pool.Stats()
	if stats.Stable.Entries != 0 || stats.Stable.ArrowHolds != 0 {
		b.Fatalf("cache hold survived invalidation: %#v", stats.Stable)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		lease, err := consumer.Data().Acquire()
		if err != nil {
			b.Fatal(err)
		}
		cacheCutoverBenchmarkRows = lease.Rows()
		lease.Release()
	}
	b.StopTimer()
	b.ReportMetric(float64(retainedBytes), "retained-arrow-B")
}

func newCacheCutoverBenchmarkEntry(b *testing.B, scopeID string) (*Pool, *Scope, int64) {
	b.Helper()
	pool, err := New(Limits{RuntimeEntries: 4, RuntimeBytes: 64 << 20, NodeEntries: 8, NodeBytes: 128 << 20})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := pool.Close(); err != nil {
			b.Errorf("close cutover benchmark pool: %v", err)
		}
	})
	scope, err := pool.OpenSharedScope(ScopeID{RuntimeID: scopeID})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := scope.Close(); err != nil {
			b.Errorf("close cutover benchmark scope: %v", err)
		}
	})
	result := newWarmArrowCacheLookupResult(b, 1_000, 8)
	stableBytes := int64(len("query")) + result.Bytes() + metadataBytes(Metadata{})
	if outcome := scope.StoreArrowObserved("query", QueryFamily{1}, scope.Generation(), result, Metadata{}); outcome != StoreStored {
		result.Release()
		b.Fatalf("store cutover benchmark result = %q, want %q", outcome, StoreStored)
	}
	result.Release()
	return pool, scope, stableBytes
}

func assertCacheCutoverBenchmarkHit(b *testing.B, scope *Scope, want HitSource) {
	b.Helper()
	lease, _, hit, observation, err := scope.LookupArrowObserved("query", QueryFamily{1})
	if err != nil || !hit || observation.HitSource != want {
		b.Fatalf("cutover benchmark lookup hit=%v observation=%#v err=%v, want source %q", hit, observation, err, want)
	}
	lease.Release()
}

func assertCacheCutoverBenchmarkAccounting(b *testing.B, pool *Pool, wantEntries int, wantBytes int64) {
	b.Helper()
	stats := pool.Stats()
	if stats.Stable.Entries != wantEntries || stats.Stable.ArrowHolds != wantEntries || stats.Stable.Bytes != wantBytes {
		b.Fatalf("stable cutover accounting = %#v, want entries=%d holds=%d bytes=%d", stats.Stable, wantEntries, wantEntries, wantBytes)
	}
	if stats.Entries > 8 || stats.Bytes > 128<<20 {
		b.Fatalf("node cache accounting exceeded benchmark limits: %#v", stats)
	}
}
