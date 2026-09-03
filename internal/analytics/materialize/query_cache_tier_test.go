package materialize

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/cache"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/flidai/leapview/internal/analytics/resulttier"
	"github.com/flidai/leapview/pkg/arrowresult"
)

// testResultTier transfers a result owner on Lookup and borrows values on
// Store, mirroring the public tier ownership contract while keeping tests
// independent of a concrete disk/object implementation.
type testResultTier struct {
	mu           sync.Mutex
	stored       *arrowresult.Result
	metadata     resultcache.Metadata
	admission    resulttier.Admission
	lookupErr    error
	storeErr     error
	storeEnter   chan struct{}
	storeRelease <-chan struct{}
	storeOnce    sync.Once
	lookups      atomic.Int32
	stores       atomic.Int32
	storedMeta   resultcache.Metadata
	storedKey    cache.Key
}

func (t *testResultTier) Lookup(context.Context, cache.Key) (*arrowresult.Result, resultcache.Metadata, resulttier.Admission, bool, error) {
	t.lookups.Add(1)
	if t.lookupErr != nil {
		return nil, resultcache.Metadata{}, nil, false, t.lookupErr
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stored == nil {
		return nil, resultcache.Metadata{}, nil, false, nil
	}
	result := t.stored
	t.stored = nil // transfer the only owner reference to the caller
	return result, t.metadata, t.admission, true, nil
}

func (t *testResultTier) Store(ctx context.Context, key cache.Key, result *arrowresult.Result, metadata resultcache.Metadata) error {
	t.mu.Lock()
	t.storedKey = key
	t.storedMeta = metadata
	t.mu.Unlock()
	t.stores.Add(1)
	t.storeOnce.Do(func() {
		if t.storeEnter != nil {
			close(t.storeEnter)
		}
	})
	if t.storeRelease != nil {
		select {
		case <-t.storeRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return t.storeErr
}

func (t *testResultTier) storedSnapshot() (cache.Key, resultcache.Metadata) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.storedKey, t.storedMeta
}

func tierArrowResult(t *testing.T, value int64) *arrowresult.Result {
	return tierArrowResultWithColumn(t, "id", value)
}

func tierArrowResultWithColumn(t *testing.T, column string, value int64) *arrowresult.Result {
	t.Helper()
	collector := arrowresult.NewBuilder()
	if err := writeTestRowsArrow(context.Background(), query.Plan{Columns: []string{column}}, query.Rows{{column: value}}, collector); err != nil {
		t.Fatal(err)
	}
	result, err := collector.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return result
}

type testTierAdmission struct {
	rejects atomic.Int32
}

func (a *testTierAdmission) Reject(context.Context, string) error {
	a.rejects.Add(1)
	return nil
}

func bundleTierArrowResult(t *testing.T, value int64) *arrowresult.Result {
	t.Helper()
	collector := arrowresult.NewBuilder()
	if err := writeTestRowsArrow(context.Background(), query.Plan{Columns: []string{"value"}}, query.Rows{{"value": value}}, collector); err != nil {
		t.Fatal(err)
	}
	result, err := collector.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func tierQueryInputs(t *testing.T) (dataquery.Query, resultidentity.Partition, resultidentity.Dependency) {
	t.Helper()
	partition, dependency := materializeCacheTestIdentity()
	return dataquery.Query{
		ModelID: "sales", Kind: dataquery.KindSemanticRows, Target: "orders",
		EffectivePolicyFingerprint: materializeTestDigest('9'),
		Fields:                     []dataquery.Field{{Field: "orders.id", Alias: "id"}},
	}, partition, dependency
}

func TestTierExpectedColumnsMatchPlannerProjectionOrder(t *testing.T) {
	tests := []struct {
		name    string
		request dataquery.Query
		want    []string
	}{
		{
			name: "aggregate dimensions time metrics",
			request: dataquery.Query{Kind: dataquery.KindSemanticAggregate,
				Fields:  []dataquery.Field{{Field: "orders.region", Alias: "region_name"}},
				Time:    dataquery.Time{Field: "orders.created_at", Alias: "month"},
				Metrics: []dataquery.Field{{Field: "orders.revenue", Alias: "revenue"}},
			},
			want: []string{"region_name", "month", "revenue"},
		},
		{
			name:    "count only rows",
			request: dataquery.Query{Kind: dataquery.KindSemanticRows, IncludeTotal: true},
			want:    []string{"value"},
		},
		{
			name:    "model table uses physical names",
			request: dataquery.Query{Kind: dataquery.KindModelTableRows, Fields: []dataquery.Field{{Field: "order_id", Alias: "ignored_alias"}}},
			want:    []string{"order_id"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := tierExpectedColumns(test.request); !slices.Equal(got, test.want) {
				t.Fatalf("columns = %v, want %v", got, test.want)
			}
		})
	}
}

func waitForTierStores(t *testing.T, tier *testResultTier, want int32) {
	t.Helper()
	if !waitForAtomicCount(&tier.stores, want, time.Second) {
		t.Fatalf("tier stores = %d, want at least %d", tier.stores.Load(), want)
	}
}

func waitForAtomicCount(value *atomic.Int32, want int32, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for value.Load() < want {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
	return true
}

func TestQueryResultCachePersistentTierHitPromotesIntoL1(t *testing.T) {
	before := arrowresult.Stats()
	tier := &testResultTier{stored: tierArrowResult(t, 7), metadata: resultcache.Metadata{TotalRows: 1, TotalRowsKnown: true, SQL: "must-not-persist"}}
	cache := newQueryResultCache(16)
	cache.tier = tier
	defer cache.close()
	request, partition, dependency := tierQueryInputs(t)
	var physical atomic.Int32
	execute := func(context.Context) (arrowQueryExecution, error) {
		physical.Add(1)
		return arrowQueryExecution{}, errors.New("physical execution should not run")
	}
	result, err := cache.executeArrow(context.Background(), request, partition, dependency, "select current_snapshot.id", time.Time{}, execute)
	if err != nil {
		t.Fatal(err)
	}
	if result.CacheOutcome != dataquery.CacheHit || result.Rows[0]["id"] != int64(7) {
		t.Fatalf("tier result = %#v", result)
	}
	if result.SQL != "select current_snapshot.id" || tier.metadata.SQL != "must-not-persist" {
		t.Fatalf("diagnostic SQL handling = result %q tier %q", result.SQL, tier.metadata.SQL)
	}
	if physical.Load() != 0 || tier.lookups.Load() != 1 {
		t.Fatalf("physical=%d tier lookups=%d", physical.Load(), tier.lookups.Load())
	}
	// The promoted value is now an L1 hit and should not consult the tier.
	second, err := cache.executeArrow(context.Background(), request, partition, dependency, "select next_snapshot.id", time.Time{}, execute)
	if err != nil {
		t.Fatal(err)
	}
	if second.CacheOutcome != dataquery.CacheHit || tier.lookups.Load() != 1 {
		t.Fatalf("promoted second result = %#v, tier lookups=%d", second, tier.lookups.Load())
	}
	if err := cache.close(); err != nil {
		t.Fatal(err)
	}
	if got := arrowresult.Stats(); got.Results != before.Results {
		t.Fatalf("tier hit leaked result ownership: before=%+v after=%+v", before, got)
	}
}

func TestQueryResultCachePersistentTierMissFallsBackAndWritesThrough(t *testing.T) {
	before := arrowresult.Stats()
	tier := &testResultTier{}
	cache := newQueryResultCache(16)
	cache.tier = tier
	defer cache.close()
	request, partition, dependency := tierQueryInputs(t)
	var physical atomic.Int32
	execute := func(context.Context) (arrowQueryExecution, error) {
		physical.Add(1)
		return arrowQueryExecution{data: tierArrowResult(t, 11), metadata: resultcache.Metadata{SQL: "physical sql"}}, nil
	}
	result, err := cache.executeArrow(context.Background(), request, partition, dependency, "select current_snapshot.id", time.Time{}, execute)
	if err != nil {
		t.Fatal(err)
	}
	if result.CacheOutcome != dataquery.CacheMiss || result.Rows[0]["id"] != int64(11) {
		t.Fatalf("fallback result = %#v", result)
	}
	waitForTierStores(t, tier, 1)
	if physical.Load() != 1 || tier.lookups.Load() != 1 || tier.stores.Load() != 1 {
		t.Fatalf("physical=%d tier lookups=%d stores=%d", physical.Load(), tier.lookups.Load(), tier.stores.Load())
	}
	storedKey, storedMeta := tier.storedSnapshot()
	if storedMeta.SQL != "" || storedKey.Version() == 0 {
		t.Fatalf("write-through metadata/key = %#v / %#v", storedMeta, storedKey)
	}
	if err := cache.close(); err != nil {
		t.Fatal(err)
	}
	if got := arrowresult.Stats(); got.Results != before.Results {
		t.Fatalf("tier miss leaked result ownership: before=%+v after=%+v", before, got)
	}
}

func TestQueryResultCachePersistentTierFailureIsFailOpen(t *testing.T) {
	before := arrowresult.Stats()
	tier := &testResultTier{lookupErr: errors.New("tier unavailable"), storeErr: errors.New("tier write failed")}
	cache := newQueryResultCache(16)
	cache.tier = tier
	defer cache.close()
	request, partition, dependency := tierQueryInputs(t)
	var physical atomic.Int32
	execute := func(context.Context) (arrowQueryExecution, error) {
		physical.Add(1)
		return arrowQueryExecution{data: tierArrowResult(t, 13)}, nil
	}
	if _, err := cache.executeArrow(context.Background(), request, partition, dependency, "select id", time.Time{}, execute); err != nil {
		t.Fatal(err)
	}
	waitForTierStores(t, tier, 1)
	if physical.Load() != 1 || tier.lookups.Load() != 1 || tier.stores.Load() != 1 {
		t.Fatalf("physical=%d tier lookups=%d stores=%d", physical.Load(), tier.lookups.Load(), tier.stores.Load())
	}
	if err := cache.close(); err != nil {
		t.Fatal(err)
	}
	if got := arrowresult.Stats(); got.Results != before.Results {
		t.Fatalf("tier failure leaked result ownership: before=%+v after=%+v", before, got)
	}
}

func TestQueryResultCacheRetiresPersistentWrongSchemaEntry(t *testing.T) {
	before := arrowresult.Stats()
	admission := &testTierAdmission{}
	tier := &testResultTier{stored: tierArrowResultWithColumn(t, "wrong", 7), metadata: resultcache.Metadata{TotalRows: 1, TotalRowsKnown: true}, admission: admission}
	cache := newQueryResultCache(16)
	cache.tier = tier
	defer cache.close()
	request, partition, dependency := tierQueryInputs(t)
	var physical atomic.Int32
	result, err := cache.executeArrow(context.Background(), request, partition, dependency, "select id", time.Time{}, func(context.Context) (arrowQueryExecution, error) {
		physical.Add(1)
		return arrowQueryExecution{data: tierArrowResult(t, 11)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CacheOutcome != dataquery.CacheMiss || result.Rows[0]["id"] != int64(11) {
		t.Fatalf("fallback result = %#v", result)
	}
	if physical.Load() != 1 || admission.rejects.Load() != 1 {
		t.Fatalf("physical=%d semantic rejections=%d, want one each", physical.Load(), admission.rejects.Load())
	}
	if err := cache.close(); err != nil {
		t.Fatal(err)
	}
	if got := arrowresult.Stats(); got.Results != before.Results {
		t.Fatalf("wrong-schema tier entry leaked result ownership: before=%+v after=%+v", before, got)
	}
}

func TestRuntimeBundlePersistentTierHitPromotesIntoL1(t *testing.T) {
	database := &bundleCountingDatabase{}
	tier := &testResultTier{stored: bundleTierArrowResult(t, 17), metadata: resultcache.Metadata{TotalRows: 1, TotalRowsKnown: true}}
	runtime := bundleCacheRuntime(t, database)
	runtime.queryCache.tier = tier
	defer runtime.CloseView()

	requests := bundleCacheRequests()
	result, err := runtime.ExecuteDataQueryBundle(context.Background(), requests)
	if err != nil {
		t.Fatal(err)
	}
	if got := database.queries.Load(); got != 1 {
		t.Fatalf("physical executions = %d, want one for the uncached branch", got)
	}
	if got := result.Results["orders"]; got.CacheOutcome != dataquery.CacheHit || got.Rows[0]["value"] != int64(17) {
		t.Fatalf("tier branch result = %#v", got)
	}
	if got := result.Results["events"]; got.CacheOutcome != dataquery.CacheMiss {
		t.Fatalf("physical branch result = %#v", got)
	}
	// The tier lookup occurs for each branch on the first bundle call; the
	// physical degenerate branch also probes the tier through ExecuteDataQuery.
	if got := tier.lookups.Load(); got != 3 {
		t.Fatalf("tier lookups = %d, want three", got)
	}

	if _, err := runtime.ExecuteDataQueryBundle(context.Background(), requests); err != nil {
		t.Fatal(err)
	}
	if got := database.queries.Load(); got != 1 {
		t.Fatalf("second physical executions = %d, want one", got)
	}
	if got := tier.lookups.Load(); got != 3 {
		t.Fatalf("L1 promotion did not suppress tier lookups: %d", got)
	}
}

func TestRuntimeBundlePersistentTierWritesThroughEachPhysicalBranch(t *testing.T) {
	database := &bundleCountingDatabase{}
	tier := &testResultTier{}
	runtime := bundleCacheRuntime(t, database)
	runtime.queryCache.tier = tier
	defer runtime.CloseView()

	result, err := runtime.ExecuteDataQueryBundle(context.Background(), bundleCacheRequests())
	if err != nil {
		t.Fatal(err)
	}
	if got := database.queries.Load(); got != 1 {
		t.Fatalf("physical executions = %d, want one shared bundle query", got)
	}
	if got := tier.lookups.Load(); got != 2 {
		t.Fatalf("tier lookups = %d, want one per branch", got)
	}
	waitForTierStores(t, tier, 2)
	if got := tier.stores.Load(); got != 2 {
		t.Fatalf("tier stores = %d, want one per physical branch", got)
	}
	storedKey, storedMeta := tier.storedSnapshot()
	if storedKey.Version() == 0 || storedMeta.SQL != "" {
		t.Fatalf("tier write-through identity/metadata = %#v / %#v", storedKey, storedMeta)
	}
	if result.Results["orders"].CacheOutcome != dataquery.CacheMiss || result.Results["events"].CacheOutcome != dataquery.CacheMiss {
		t.Fatalf("bundle outcomes = %#v", result.Results)
	}
}

func TestQueryResultCachePersistentTierWritebackDoesNotBlockResponse(t *testing.T) {
	before := arrowresult.Stats()
	storeEnter := make(chan struct{})
	storeRelease := make(chan struct{})
	tier := &testResultTier{storeEnter: storeEnter, storeRelease: storeRelease}
	cache := newQueryResultCache(16)
	cache.tier = tier
	request, partition, dependency := tierQueryInputs(t)
	done := make(chan error, 1)
	go func() {
		_, err := cache.executeArrow(context.Background(), request, partition, dependency, "select id", time.Time{}, func(context.Context) (arrowQueryExecution, error) {
			return arrowQueryExecution{data: tierArrowResult(t, 19)}, nil
		})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("query response waited for persistent-tier Store")
	}
	select {
	case <-storeEnter:
	case <-time.After(time.Second):
		t.Fatal("write-back worker did not start")
	}
	close(storeRelease)
	waitForTierStores(t, tier, 1)
	if err := cache.close(); err != nil {
		t.Fatal(err)
	}
	if got := arrowresult.Stats(); got.Results != before.Results {
		t.Fatalf("asynchronous write-back leaked result ownership: before=%+v after=%+v", before, got)
	}
}

func TestRuntimeBundlePersistentTierWritebackDoesNotBlockResponse(t *testing.T) {
	storeEnter := make(chan struct{})
	storeRelease := make(chan struct{})
	tier := &testResultTier{storeEnter: storeEnter, storeRelease: storeRelease}
	runtime := bundleCacheRuntime(t, &bundleCountingDatabase{})
	runtime.queryCache.tier = tier
	defer runtime.CloseView()
	done := make(chan error, 1)
	go func() {
		_, err := runtime.ExecuteDataQueryBundle(context.Background(), bundleCacheRequests())
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("bundle response waited for persistent-tier Store")
	}
	select {
	case <-storeEnter:
	case <-time.After(time.Second):
		t.Fatal("bundle write-back worker did not start")
	}
	close(storeRelease)
	waitForTierStores(t, tier, 2)
	if err := runtime.CloseView(); err != nil {
		t.Fatal(err)
	}
}

func TestQueryResultCachePersistentTierWritebackSaturatesAndClosesCleanly(t *testing.T) {
	before := arrowresult.Stats()
	storeEnter := make(chan struct{})
	storeRelease := make(chan struct{})
	tier := &testResultTier{storeEnter: storeEnter, storeRelease: storeRelease}
	cache := newQueryResultCache(tierWritebackQueueCapacity + 4)
	cache.tier = tier
	request, partition, dependency := tierQueryInputs(t)
	execute := func(value int64) func(context.Context) (arrowQueryExecution, error) {
		return func(context.Context) (arrowQueryExecution, error) {
			return arrowQueryExecution{data: tierArrowResult(t, value)}, nil
		}
	}
	if _, err := cache.executeArrow(context.Background(), request, partition, dependency, "select id", time.Time{}, execute(0)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-storeEnter:
	case <-time.After(time.Second):
		t.Fatal("write-back worker did not start")
	}
	for index := 1; index <= tierWritebackQueueCapacity+1; index++ {
		request.Target = fmt.Sprintf("orders-%d", index)
		if _, err := cache.executeArrow(context.Background(), request, partition, dependency, "select id", time.Time{}, execute(int64(index))); err != nil {
			t.Fatal(err)
		}
	}
	cache.tierMu.Lock()
	depth := len(cache.tierQueue)
	cache.tierMu.Unlock()
	if depth > tierWritebackQueueCapacity {
		t.Fatalf("write-back queue depth = %d, exceeds capacity %d", depth, tierWritebackQueueCapacity)
	}
	if err := cache.close(); err != nil {
		t.Fatal(err)
	}
	if got := arrowresult.Stats(); got.Results != before.Results {
		t.Fatalf("saturated write-back leaked result ownership: before=%+v after=%+v", before, got)
	}
	if got := tier.stores.Load(); got != 1 {
		t.Fatalf("canceled queued writes reached tier = %d, want only active write", got)
	}
}
