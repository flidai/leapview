package materialize

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
)

var runtimeCacheCutoverBenchmarkValue int64

// BenchmarkCacheCutoverRuntime measures the governed runtime boundary around
// stable cache lookups and deterministic cold execution. It deliberately uses
// the production planner, dependency derivation, key generation, Arrow cache,
// execution coalescer, and decode path with a test-only physical database.
func BenchmarkCacheCutoverRuntime(b *testing.B) {
	b.Run("warm_hit/shared_generation", benchmarkRuntimeCacheCutoverSharedHit)
	b.Run("warm_hit/cutover_retained", benchmarkRuntimeCacheCutoverRetainedHit)
	b.Run("cold_miss/plan_execute_store", benchmarkRuntimeCacheCutoverColdMiss)
}

func benchmarkRuntimeCacheCutoverSharedHit(b *testing.B) {
	pool := newRuntimeCacheCutoverBenchmarkPool(b)
	partition := materializeTestPartition(b, resultidentity.PartitionProduction, "")
	evidence := cutoverQualificationEvidence(b, '1')
	request := cutoverQualificationRequest()
	databaseA := &cutoverQualificationDatabase{value: 701}
	databaseB := &cutoverQualificationDatabase{value: 702}
	generationA := newCutoverQualificationRuntime(b, pool, "benchmark-shared-a", partition, evidence, databaseA)
	generationB := newCutoverQualificationRuntime(b, pool, "benchmark-shared-b", partition, evidence, databaseB)
	b.Cleanup(func() {
		if err := generationB.Close(); err != nil {
			b.Errorf("close shared generation B: %v", err)
		}
	})
	b.Cleanup(func() {
		if err := generationA.Close(); err != nil {
			b.Errorf("close shared generation A: %v", err)
		}
	})
	assertRuntimeCacheCutoverOutcome(b, generationA, request, dataquery.CacheMiss, 701)
	assertRuntimeCacheCutoverOutcome(b, generationB, request, dataquery.CacheHit, 701)
	if got := databaseB.queries.Load(); got != 0 {
		b.Fatalf("shared-generation warmup physical queries = %d, want 0", got)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		result, err := generationB.ExecuteDataQuery(context.Background(), request)
		if err != nil || result.CacheOutcome != dataquery.CacheHit {
			b.Fatalf("shared-generation warm result outcome=%q err=%v", result.CacheOutcome, err)
		}
		runtimeCacheCutoverBenchmarkValue = runtimeCacheCutoverBenchmarkResultValue(b, result)
	}
	b.StopTimer()
	b.ReportMetric(float64(databaseB.queries.Load())/float64(b.N), "physical-queries/op")
	reportRuntimeCacheCutoverAccounting(b, pool)
}

func benchmarkRuntimeCacheCutoverRetainedHit(b *testing.B) {
	pool := newRuntimeCacheCutoverBenchmarkPool(b)
	partition := materializeTestPartition(b, resultidentity.PartitionProduction, "")
	evidence := cutoverQualificationEvidence(b, '1')
	request := cutoverQualificationRequest()
	databaseA := &cutoverQualificationDatabase{value: 801}
	generationA := newCutoverQualificationRuntime(b, pool, "benchmark-retained-a", partition, evidence, databaseA)
	assertRuntimeCacheCutoverOutcome(b, generationA, request, dataquery.CacheMiss, 801)
	if err := generationA.Close(); err != nil {
		b.Fatal(err)
	}
	stats := pool.Stats()
	if stats.Stable.ActiveScopes != 0 || stats.Stable.DormantScopes != 1 {
		b.Fatalf("pre-reactivation stable scopes = %#v", stats.Stable)
	}

	databaseC := &cutoverQualificationDatabase{value: 802}
	generationC := newCutoverQualificationRuntime(b, pool, "benchmark-retained-c", partition, evidence, databaseC)
	b.Cleanup(func() {
		if err := generationC.Close(); err != nil {
			b.Errorf("close retained generation C: %v", err)
		}
	})
	assertRuntimeCacheCutoverOutcome(b, generationC, request, dataquery.CacheHit, 801)
	if got := databaseC.queries.Load(); got != 0 {
		b.Fatalf("retained warmup physical queries = %d, want 0", got)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		result, err := generationC.ExecuteDataQuery(context.Background(), request)
		if err != nil || result.CacheOutcome != dataquery.CacheHit {
			b.Fatalf("cutover-retained warm result outcome=%q err=%v", result.CacheOutcome, err)
		}
		runtimeCacheCutoverBenchmarkValue = runtimeCacheCutoverBenchmarkResultValue(b, result)
	}
	b.StopTimer()
	b.ReportMetric(float64(databaseC.queries.Load())/float64(b.N), "physical-queries/op")
	reportRuntimeCacheCutoverAccounting(b, pool)
}

func benchmarkRuntimeCacheCutoverColdMiss(b *testing.B) {
	pool := newRuntimeCacheCutoverBenchmarkPool(b)
	database := &cutoverQualificationDatabase{value: 901}
	runtime := newCutoverQualificationRuntime(
		b,
		pool,
		"benchmark-cold",
		materializeTestPartition(b, resultidentity.PartitionProduction, ""),
		cutoverQualificationEvidence(b, '1'),
		database,
	)
	b.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			b.Errorf("close cold benchmark runtime: %v", err)
		}
	})
	request := cutoverQualificationRequest()
	iteration := 1

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		request.Offset = iteration
		iteration++
		result, err := runtime.ExecuteDataQuery(context.Background(), request)
		if err != nil || result.CacheOutcome != dataquery.CacheMiss {
			b.Fatalf("cold result outcome=%q err=%v", result.CacheOutcome, err)
		}
		runtimeCacheCutoverBenchmarkValue = runtimeCacheCutoverBenchmarkResultValue(b, result)
	}
	b.StopTimer()
	if got := int64(database.queries.Load()); got != int64(b.N) {
		b.Fatalf("cold physical queries = %d, want %d", got, b.N)
	}
	b.ReportMetric(float64(database.queries.Load())/float64(b.N), "physical-queries/op")
	reportRuntimeCacheCutoverAccounting(b, pool)
}

func newRuntimeCacheCutoverBenchmarkPool(b *testing.B) *resultcache.Pool {
	b.Helper()
	pool, err := resultcache.New(resultcache.Limits{
		RuntimeEntries: 16,
		RuntimeBytes:   16 << 20,
		NodeEntries:    32,
		NodeBytes:      32 << 20,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := pool.Close(); err != nil {
			b.Errorf("close runtime cutover benchmark pool: %v", err)
		}
	})
	return pool
}

func assertRuntimeCacheCutoverOutcome(b *testing.B, runtime *Runtime, request dataquery.Query, wantOutcome string, wantValue int64) {
	b.Helper()
	result, err := runtime.ExecuteDataQuery(context.Background(), request)
	if err != nil {
		b.Fatal(err)
	}
	if result.CacheOutcome != wantOutcome {
		b.Fatalf("cache outcome = %q, want %q", result.CacheOutcome, wantOutcome)
	}
	if len(result.Rows) != 1 || result.Rows[0]["id"] != wantValue {
		b.Fatalf("cache rows = %#v, want id %d", result.Rows, wantValue)
	}
}

func reportRuntimeCacheCutoverAccounting(b *testing.B, pool *resultcache.Pool) {
	b.Helper()
	stats := pool.Stats()
	if stats.Entries > 32 || stats.Bytes > 32<<20 || stats.Stable.Entries > 32 || stats.Stable.Bytes > 32<<20 {
		b.Fatalf("runtime cache benchmark exceeded node limits: %#v", stats)
	}
	if stats.Stable.ArrowHolds != stats.Stable.Entries {
		b.Fatalf("runtime cache benchmark Arrow holds = %d, entries = %d", stats.Stable.ArrowHolds, stats.Stable.Entries)
	}
	for name, scope := range stats.Scopes {
		if scope.Entries > 16 || scope.Bytes > 16<<20 {
			b.Fatalf("runtime cache benchmark scope %q exceeded runtime limits: %#v", name, scope)
		}
	}
	b.ReportMetric(float64(stats.Stable.Bytes), "stable-B")
	b.ReportMetric(float64(stats.Stable.Entries), "stable-entries")
}

func runtimeCacheCutoverBenchmarkResultValue(b *testing.B, result dataquery.Result) int64 {
	b.Helper()
	if len(result.Rows) != 1 {
		b.Fatalf("cache benchmark rows = %d, want 1", len(result.Rows))
	}
	value, ok := result.Rows[0]["id"].(int64)
	if !ok {
		b.Fatalf("cache benchmark id = %#v, want int64", result.Rows[0]["id"])
	}
	return value
}
