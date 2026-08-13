package materialize

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/arrowresult"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/workload"
	"github.com/stretchr/testify/require"
)

// The row-shaped helpers below exist only to preserve cache-policy tests while
// production cache storage is Arrow-only.
func (c *queryResultCache) execute(ctx context.Context, request dataquery.Query, execute func() (dataquery.Result, error)) (dataquery.Result, error) {
	key, generation, err := c.cacheKey(request)
	if err != nil {
		return dataquery.Result{}, err
	}
	if cached, ok := c.get(key); ok {
		return cached, nil
	}
	value, shared, err := c.scope.Coalesce(ctx, fmt.Sprintf("test-query:%d:%s", generation, key), func() (any, error) {
		if cached, ok := c.get(key); ok {
			return cached, nil
		}
		result, executeErr := execute()
		if executeErr != nil {
			return result, executeErr
		}
		result.CacheOutcome = dataquery.CacheMiss
		c.store(key, generation, result)
		return cloneDataQueryResult(result), nil
	})
	if err != nil {
		return dataquery.Result{}, err
	}
	result := cloneDataQueryResult(value.(dataquery.Result))
	if shared {
		result.CacheOutcome = dataquery.CacheCoalesced
	}
	return result, nil
}

func (c *queryResultCache) lookup(request dataquery.Query) (dataquery.Result, string, uint64, bool, error) {
	key, generation, err := c.cacheKey(request)
	if err != nil {
		return dataquery.Result{}, "", 0, false, err
	}
	result, ok := c.get(key)
	return result, key, generation, ok, nil
}

func (c *queryResultCache) store(key string, generation uint64, result dataquery.Result) {
	columns := make([]string, len(result.Columns))
	for index := range result.Columns {
		columns[index] = result.Columns[index].Name
	}
	if len(columns) == 0 && len(result.Rows) > 0 {
		for column := range result.Rows[0] {
			columns = append(columns, column)
		}
		sort.Strings(columns)
	}
	rows := make(semanticquery.Rows, len(result.Rows))
	for index := range result.Rows {
		rows[index] = semanticquery.Row(result.Rows[index])
	}
	collector := arrowresult.NewBuilder()
	if err := writeTestRowsArrow(context.Background(), semanticquery.Plan{Columns: columns}, rows, collector); err != nil {
		panic(err)
	}
	owned, err := collector.Finish()
	if err != nil {
		panic(err)
	}
	c.scope.StoreArrow(key, resultcache.Token(generation), owned, resultcache.Metadata{SQL: result.SQL, TotalRows: result.TotalRows, TotalRowsKnown: result.TotalRowsKnown, Warnings: result.Warnings})
	owned.Release()
	c.syncStats()
}

func (c *queryResultCache) get(key string) (dataquery.Result, bool) {
	entry, _, ok, err := c.scope.LookupArrow(key)
	if err != nil || !ok {
		return dataquery.Result{}, false
	}
	defer entry.Release()
	rows, err := arrowresult.DecodeRows(entry.Data())
	if err != nil {
		return dataquery.Result{}, false
	}
	result := dataquery.Result{SQL: entry.Metadata().SQL, TotalRows: entry.Metadata().TotalRows, TotalRowsKnown: entry.Metadata().TotalRowsKnown, Warnings: entry.Metadata().Warnings, CacheOutcome: dataquery.CacheHit}
	if schema := entry.Data().Schema(); schema != nil {
		columns := make([]string, len(schema.Fields()))
		for index, field := range schema.Fields() {
			columns[index] = field.Name
		}
		result.Columns = dataquery.ColumnsFromNames(columns)
	}
	result.Rows = make([]dataquery.Row, len(rows))
	for index := range rows {
		result.Rows[index] = dataquery.Row(rows[index])
	}
	c.syncStats()
	return result, true
}

func TestRuntimeCachesOwnedArrowAndRebuildsRequestTimingOnHit(t *testing.T) {
	database := &arrowCountingRuntimeDatabase{}
	runtime := &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Name: "id"}}},
		}},
		db:         database,
		queryCache: newQueryResultCache(256, ""),
	}
	request := dataquery.Query{Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardRows, ModelID: "sales", Kind: dataquery.KindModelTableRows, Target: "orders", Fields: []dataquery.Field{{Field: "id"}}, Limit: 1}
	first, err := runtime.ExecuteDataQuery(context.Background(), request)
	require.NoError(t, err)
	second, err := runtime.ExecuteDataQuery(context.Background(), request)
	require.NoError(t, err)
	if first.CacheOutcome != dataquery.CacheMiss || second.CacheOutcome != dataquery.CacheHit {
		t.Fatalf("outcomes = (%q, %q)", first.CacheOutcome, second.CacheOutcome)
	}
	if second.DatabaseMS != 0 || second.ConnectionWaitMS != 0 || second.PlanningMS != 0 {
		t.Fatalf("cache hit retained request timing: %#v", second)
	}
	if got := database.queries.Load(); got != 1 {
		t.Fatalf("Arrow executions = %d, want 1", got)
	}
	if got := second.Rows[0]["id"]; got != int64(1) {
		t.Fatalf("cached id = %#v", got)
	}
}

func TestRuntimeExecutesSpatialQueriesThroughOwnedArrowResults(t *testing.T) {
	database := &arrowCountingRuntimeDatabase{}
	runtime := &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{
			Name: "sales",
			Tables: map[string]semanticmodel.Table{
				"orders": {Dimensions: map[string]semanticmodel.MetricDimension{
					"latitude":  {Expr: "latitude", Type: "number"},
					"longitude": {Expr: "longitude", Type: "number"},
				}},
			},
			Measures: map[string]semanticmodel.MetricMeasure{
				"order_count": {Fact: "orders", Aggregation: "count", Empty: "zero"},
			},
		},
		db:         database,
		queryCache: newQueryResultCache(256, ""),
	}
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardSpatial,
		ModelID: "sales", Kind: dataquery.KindSemanticSpatial, Target: "orders",
		Fields:   []dataquery.Field{{Field: "orders.latitude", Alias: "latitude"}, {Field: "orders.longitude", Alias: "longitude"}},
		Measures: []dataquery.Field{{Field: "order_count", Alias: "value"}},
		Spatial: &dataquery.SpatialWindow{
			Latitude: dataquery.Field{Field: "orders.latitude", Alias: "latitude"}, Longitude: dataquery.Field{Field: "orders.longitude", Alias: "longitude"},
			West: -180, South: -85, East: 180, North: 85, Width: 800, Height: 600, FeatureCap: 5000,
			Precision: dataquery.SpatialPrecisionAggregated,
		},
	}

	first, err := runtime.ExecuteDataQuery(context.Background(), request)
	require.NoError(t, err)
	second, err := runtime.ExecuteDataQuery(context.Background(), request)
	require.NoError(t, err)
	if first.CacheOutcome != dataquery.CacheMiss || second.CacheOutcome != dataquery.CacheHit {
		t.Fatalf("cache outcomes = (%q, %q), want (miss, hit)", first.CacheOutcome, second.CacheOutcome)
	}
	if got := database.queries.Load(); got != 1 {
		t.Fatalf("Arrow executions = %d, want 1", got)
	}
	if !first.TotalRowsKnown || first.TotalRows != 1 {
		t.Fatalf("spatial total = (%d, %t), want (1, true)", first.TotalRows, first.TotalRowsKnown)
	}
	for _, result := range []dataquery.Result{first, second} {
		for _, column := range result.Columns {
			if column.Name == semanticquery.SpatialTotalColumn {
				t.Fatalf("spatial transport column leaked through result schema: %#v", result.Columns)
			}
		}
		if _, ok := result.Rows[0][semanticquery.SpatialTotalColumn]; ok {
			t.Fatalf("spatial transport column leaked through result row: %#v", result.Rows[0])
		}
	}
}

func TestQueryResultCacheUsesGovernedRequestAndReturnsDeepCopies(t *testing.T) {
	cache := newQueryResultCache(256, "")
	request := dataquery.Query{
		ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders",
		Operation:                  dataquery.OperationDashboardFilterOptions,
		EffectivePolicyFingerprint: "sha256:policy-one",
		Fields:                     []dataquery.Field{{Field: "orders.state", Alias: "value"}},
		ColumnMasks:                []dataquery.ColumnMask{{Field: "orders.state", Mask: "redact"}},
	}
	var calls atomic.Int32
	execute := func() (dataquery.Result, error) {
		calls.Add(1)
		return dataquery.Result{Rows: []dataquery.Row{{"value": "SP"}}}, nil
	}
	first, err := cache.execute(context.Background(), request, execute)
	require.NoError(t, err)
	if first.CacheOutcome != dataquery.CacheMiss {
		t.Fatalf("first cache outcome = %q, want miss", first.CacheOutcome)
	}
	first.Rows[0]["value"] = "mutated"
	request.PrincipalID = "another-user"
	request.RequestID = "request-2"
	request.CorrelationID = "refresh-2"
	second, err := cache.execute(context.Background(), request, execute)
	require.NoError(t, err)
	if calls.Load() != 1 {
		t.Fatalf("physical executions = %d, want 1", calls.Load())
	}
	if second.CacheOutcome != dataquery.CacheHit {
		t.Fatalf("second cache outcome = %q, want hit", second.CacheOutcome)
	}
	if second.Rows[0]["value"] != "SP" {
		t.Fatalf("cached result was aliased: %#v", second.Rows)
	}

	request.EffectivePolicyFingerprint = "sha256:policy-two"
	if _, err := cache.execute(context.Background(), request, execute); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("different effective policy executions = %d, want 2", calls.Load())
	}

	request.ColumnMasks[0].Mask = "null"
	if _, err := cache.execute(context.Background(), request, execute); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("different governed request executions = %d, want 3", calls.Load())
	}
}

func TestQueryResultCacheEnforcesByteBudgetAndRejectsOversizedEntries(t *testing.T) {
	cache := newQueryResultCacheWithLimits(10, 1200, "bytes")
	first := dataquery.Query{ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Measures: []dataquery.Field{{Field: "revenue"}}}
	second := first
	second.Measures = []dataquery.Field{{Field: "orders"}}
	large := first
	large.Measures = []dataquery.Field{{Field: "large"}}

	_, firstKey, generation, _, err := cache.lookup(first)
	require.NoError(t, err)
	cache.store(firstKey, generation, dataquery.Result{Rows: []dataquery.Row{{"value": strings.Repeat("a", 80)}}})
	_, secondKey, generation, _, err := cache.lookup(second)
	require.NoError(t, err)
	cache.store(secondKey, generation, dataquery.Result{Rows: []dataquery.Row{{"value": strings.Repeat("b", 80)}}})
	if cache.currentBytes > cache.maxBytes {
		t.Fatalf("cache bytes = %d, budget = %d", cache.currentBytes, cache.maxBytes)
	}
	if entries := cache.scope.Stats().Entries; entries != 1 {
		t.Fatalf("entries = %d, want byte-budget eviction", entries)
	}

	_, largeKey, generation, _, err := cache.lookup(large)
	require.NoError(t, err)
	cache.store(largeKey, generation, dataquery.Result{Rows: []dataquery.Row{{"value": strings.Repeat("x", 5000)}}})
	if _, ok := cache.get(largeKey); ok {
		t.Fatal("oversized result was cached")
	}
}

func TestQueryResultCacheKeyIncludesRawValueField(t *testing.T) {
	cache := newQueryResultCache(256, "")
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardHistogram,
		ModelID: "sales", Kind: dataquery.KindSemanticHistogram, Target: "orders",
		Value: dataquery.Field{Field: "order_total", Alias: "value"}, BinCount: 20,
	}
	var calls atomic.Int32
	execute := func() (dataquery.Result, error) {
		calls.Add(1)
		return dataquery.Result{Rows: []dataquery.Row{{"bucket": 0}}}, nil
	}
	if _, err := cache.execute(context.Background(), request, execute); err != nil {
		t.Fatal(err)
	}
	request.Value = dataquery.Field{Field: "shipping_total", Alias: "value"}
	if _, err := cache.execute(context.Background(), request, execute); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("physical executions = %d, want distinct entries for raw value fields", calls.Load())
	}
}

func TestQueryResultCacheKeyIncludesAuthorizationProjection(t *testing.T) {
	cache := newQueryResultCache(256, "")
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardCount,
		ModelID: "sales", Kind: dataquery.KindSemanticRows, Target: "orders", IncludeTotal: true,
		AuthorizationFields: []dataquery.Field{{Field: "orders.customer_email"}},
	}
	first, _, err := cache.cacheKey(request)
	require.NoError(t, err)
	request.AuthorizationFields = []dataquery.Field{{Field: "orders.customer_id"}}
	second, _, err := cache.cacheKey(request)
	require.NoError(t, err)
	if first == second {
		t.Fatal("count cache key ignored its authorization projection")
	}
}

func TestQueryResultCacheKeyIncludesRuntimeNamespace(t *testing.T) {
	request := dataquery.Query{ModelID: "sales", Kind: dataquery.KindSemanticAggregate}
	first, _, err := newQueryResultCache(256, "snapshot=1;source=old").cacheKey(request)
	require.NoError(t, err)
	second, _, err := newQueryResultCache(256, "snapshot=2;source=new").cacheKey(request)
	require.NoError(t, err)
	if first == second {
		t.Fatal("cache keys matched across snapshot/source namespaces")
	}
}

func TestDashboardResultCacheEligibility(t *testing.T) {
	for _, operation := range []string{
		dataquery.OperationDashboardAggregate,
		dataquery.OperationDashboardRows,
		dataquery.OperationDashboardCount,
		dataquery.OperationDashboardHistogram,
		dataquery.OperationDashboardDistribution,
		dataquery.OperationDashboardFilterOptions,
		dataquery.OperationDashboardSpatial,
		dataquery.OperationDashboardSpatialTile,
		dataquery.OperationDashboardSpatialTileBudget,
		dataquery.OperationDashboardSpatialMetadata,
	} {
		request := dataquery.Query{Surface: dataquery.SurfaceDashboard, Operation: operation}
		if !dashboardQueryResultCacheable(request) {
			t.Errorf("operation %q was not cacheable", operation)
		}
	}
	for _, request := range []dataquery.Query{
		{Surface: dataquery.SurfaceAPI, Operation: dataquery.OperationDashboardAggregate},
		{Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationAPIQuery},
		{Operation: dataquery.OperationDashboardAggregate},
	} {
		if dashboardQueryResultCacheable(request) {
			t.Errorf("non-dashboard request was cacheable: %#v", request)
		}
	}
}

func TestQueryResultCacheKeysSpatialTileBudgetZoom(t *testing.T) {
	cache := newQueryResultCache(256, "snapshot=1")
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardSpatialTileBudget,
		ModelID: "sales", Kind: dataquery.KindSemanticSpatialTileBudget, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.latitude", Alias: "latitude"}, {Field: "orders.longitude", Alias: "longitude"}},
		SpatialTileBudget: &dataquery.SpatialTileBudget{
			Latitude: dataquery.Field{Field: "orders.latitude", Alias: "latitude"}, Longitude: dataquery.Field{Field: "orders.longitude", Alias: "longitude"},
			Zoom: 10, Buffer: 768, FeatureCap: 5_000, MaximumBytes: 512 * 1024,
		},
	}
	baseline, _, err := cache.cacheKey(request)
	require.NoError(t, err)
	if !strings.Contains(baseline, `"SpatialTileGenerationVersion":5`) {
		t.Fatalf("spatial tile budget cache key has no generation version: %s", baseline)
	}
	variant := request
	budget := *request.SpatialTileBudget
	budget.Zoom++
	variant.SpatialTileBudget = &budget
	key, _, err := cache.cacheKey(variant)
	require.NoError(t, err)
	if key == baseline {
		t.Fatal("spatial tile budget zoom reused the baseline key")
	}
}

func TestQueryResultCacheKeysEverySpatialTileCoordinateAndPrecision(t *testing.T) {
	cache := newQueryResultCache(256, "snapshot=1")
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardSpatialTile,
		ModelID: "sales", Kind: dataquery.KindSemanticSpatialTile, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.latitude", Alias: "latitude"}, {Field: "orders.longitude", Alias: "longitude"}},
		SpatialTile: &dataquery.SpatialTile{
			Latitude: dataquery.Field{Field: "orders.latitude", Alias: "latitude"}, Longitude: dataquery.Field{Field: "orders.longitude", Alias: "longitude"},
			Zoom: 10, MetatileX: 376, MetatileY: 512, MetatileSize: 4, CellPixels: 48, Buffer: 768, FeatureCap: 5000, Precision: dataquery.SpatialTilePrecisionRaw,
		},
	}
	baseline, _, err := cache.cacheKey(request)
	require.NoError(t, err)
	if !strings.Contains(baseline, `"SpatialTileGenerationVersion":5`) {
		t.Fatalf("spatial tile cache key has no generation version: %s", baseline)
	}
	variants := []func(*dataquery.SpatialTile){
		func(tile *dataquery.SpatialTile) { tile.Zoom++ },
		func(tile *dataquery.SpatialTile) { tile.TargetZoom++ },
		func(tile *dataquery.SpatialTile) { tile.MetatileX += 4 },
		func(tile *dataquery.SpatialTile) { tile.MetatileY += 4 },
		func(tile *dataquery.SpatialTile) { tile.CellPixels = 64 },
		func(tile *dataquery.SpatialTile) { tile.Precision = dataquery.SpatialTilePrecisionAggregated },
	}
	for index, mutate := range variants {
		variant := request
		tile := *request.SpatialTile
		variant.SpatialTile = &tile
		mutate(variant.SpatialTile)
		key, _, err := cache.cacheKey(variant)
		require.NoError(t, err)
		if key == baseline {
			t.Fatalf("spatial tile cache variant %d reused the baseline key", index)
		}
	}
}

func TestQueryResultCacheKeysEverySpatialViewportCoordinate(t *testing.T) {
	cache := newQueryResultCache(256, "snapshot=1")
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardSpatial,
		ModelID: "sales", Kind: dataquery.KindSemanticSpatial, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.latitude", Alias: "latitude"}, {Field: "orders.longitude", Alias: "longitude"}},
		Spatial: &dataquery.SpatialWindow{
			Latitude: dataquery.Field{Field: "orders.latitude", Alias: "latitude"}, Longitude: dataquery.Field{Field: "orders.longitude", Alias: "longitude"},
			West: 170, South: -20, East: -170, North: 25, Width: 960, Height: 540, FeatureCap: 5000, Precision: dataquery.SpatialPrecisionAggregated,
		},
	}
	baseline, _, err := cache.cacheKey(request)
	require.NoError(t, err)
	identical, _, err := cache.cacheKey(request)
	if err != nil || identical != baseline {
		t.Fatalf("identical spatial cache key = %q, %v; want %q", identical, err, baseline)
	}

	variants := []func(*dataquery.SpatialWindow){
		func(window *dataquery.SpatialWindow) { window.West = 160 },
		func(window *dataquery.SpatialWindow) { window.South = -30 },
		func(window *dataquery.SpatialWindow) { window.East = -160 },
		func(window *dataquery.SpatialWindow) { window.North = 35 },
		func(window *dataquery.SpatialWindow) { window.Width = 1200 },
		func(window *dataquery.SpatialWindow) { window.Height = 800 },
		func(window *dataquery.SpatialWindow) { window.FeatureCap = 1000 },
		func(window *dataquery.SpatialWindow) { window.Precision = dataquery.SpatialPrecisionRaw },
	}
	for index, mutate := range variants {
		variant := request
		window := *request.Spatial
		variant.Spatial = &window
		mutate(variant.Spatial)
		key, _, err := cache.cacheKey(variant)
		require.NoError(t, err)
		if key == baseline {
			t.Fatalf("spatial cache variant %d reused the baseline key", index)
		}
	}
}

func TestQueryResultCacheDoesNotCacheErrorsAndInvalidatesGeneration(t *testing.T) {
	cache := newQueryResultCache(1, "")
	request := dataquery.Query{ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders"}
	var calls atomic.Int32
	execute := func() (dataquery.Result, error) {
		if calls.Add(1) == 1 {
			return dataquery.Result{}, errors.New("temporary")
		}
		return dataquery.Result{Rows: []dataquery.Row{{"value": "SP"}}}, nil
	}
	if _, err := cache.execute(context.Background(), request, execute); err == nil {
		t.Fatal("first cache execution error = nil")
	}
	if _, err := cache.execute(context.Background(), request, execute); err != nil {
		t.Fatal(err)
	}
	cache.clear()
	if _, err := cache.execute(context.Background(), request, execute); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("physical executions after error and clear = %d, want 3", calls.Load())
	}
}

func TestQueryResultCacheLiveWaiterRetriesCanceledFlightAndCachesResult(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := newQueryResultCache(256, "")
		request := dataquery.Query{
			ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders",
			Operation: dataquery.OperationDashboardFilterOptions,
		}

		key, generation, err := cache.cacheKey(request)
		require.NoError(t, err)
		flightStarted := make(chan struct{})
		releaseCanceledFlight := make(chan struct{})
		ownerContext, cancelOwner := context.WithCancel(context.Background())
		go func() {
			_, _, _ = cache.scope.Coalesce(ownerContext, fmt.Sprintf("query:%d:%s", generation, key), func() (any, error) {
				close(flightStarted)
				<-releaseCanceledFlight
				return dataquery.Result{}, resultcache.OwnerCanceled(ownerContext.Err())
			})
		}()
		<-flightStarted
		cancelOwner()

		var physicalExecutions atomic.Int32
		secondResult := make(chan dataquery.Result, 1)
		secondError := make(chan error, 1)
		go func() {
			result, executeErr := cache.execute(context.Background(), request, func() (dataquery.Result, error) {
				physicalExecutions.Add(1)
				return dataquery.Result{Rows: []dataquery.Row{{"value": "SP"}}}, nil
			})
			secondResult <- result
			secondError <- executeErr
		}()
		synctest.Wait()

		close(releaseCanceledFlight)
		if err := <-secondError; err != nil {
			t.Fatalf("live waiter inherited canceled flight: %v", err)
		}
		result := <-secondResult
		if result.CacheOutcome != dataquery.CacheMiss {
			t.Fatalf("live waiter cache outcome = %q, want miss", result.CacheOutcome)
		}
		if physicalExecutions.Load() != 1 {
			t.Fatalf("live waiter physical executions = %d, want 1", physicalExecutions.Load())
		}

		cached, err := cache.execute(context.Background(), request, func() (dataquery.Result, error) {
			physicalExecutions.Add(1)
			return dataquery.Result{}, nil
		})
		require.NoError(t, err)
		if cached.CacheOutcome != dataquery.CacheHit {
			t.Fatalf("follow-up cache outcome = %q, want hit", cached.CacheOutcome)
		}
		if physicalExecutions.Load() != 1 {
			t.Fatalf("physical executions after cache hit = %d, want 1", physicalExecutions.Load())
		}
		if generation != cache.generation {
			t.Fatalf("cache generation changed during flight: got %d, want %d", cache.generation, generation)
		}
	})
}

func TestObserveQueryCacheOutcomeReportsSuccessAndError(t *testing.T) {
	observed := []string{}
	ctx := dataquery.WithCacheOutcomeObserver(context.Background(), func(outcome string) {
		observed = append(observed, outcome)
	})

	for _, outcome := range []string{dataquery.CacheHit, dataquery.CacheMiss, dataquery.CacheCoalesced} {
		observeQueryCacheOutcome(ctx, dataquery.Result{CacheOutcome: outcome}, nil)
	}
	observeQueryCacheOutcome(ctx, dataquery.Result{}, errors.New("temporary"))

	want := []string{dataquery.CacheHit, dataquery.CacheMiss, dataquery.CacheCoalesced, dataquery.CacheError}
	if len(observed) != len(want) {
		t.Fatalf("observed cache outcomes = %#v, want %#v", observed, want)
	}
	for index := range want {
		if observed[index] != want[index] {
			t.Fatalf("observed cache outcomes = %#v, want %#v", observed, want)
		}
	}
}

func TestRuntimeCountsFilterOptionCacheMissAsPhysicalAndHitAsZero(t *testing.T) {
	runtime := &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Name: "id"}}},
		}},
		db:         cacheRuntimeDatabase{},
		queryCache: newQueryResultCache(256, ""),
	}
	physicalQueries := 0
	cacheOutcomes := []string{}
	ctx := dataquery.WithPhysicalQueryObserver(context.Background(), func(observation dataquery.PhysicalQueryObservation) {
		physicalQueries += observation.Count
	})
	ctx = dataquery.WithCacheOutcomeObserver(ctx, func(outcome string) { cacheOutcomes = append(cacheOutcomes, outcome) })
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard,
		ModelID: "sales", Kind: dataquery.KindModelTableRows, Target: "orders",
		Operation: dataquery.OperationDashboardFilterOptions,
		Fields:    []dataquery.Field{{Field: "id"}},
		Limit:     50,
	}

	if _, err := runtime.ExecuteDataQuery(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ExecuteDataQuery(ctx, request); err != nil {
		t.Fatal(err)
	}

	if physicalQueries != 1 {
		t.Fatalf("physical queries = %d, want 1 miss and zero for hit", physicalQueries)
	}
	wantOutcomes := []string{dataquery.CacheMiss, dataquery.CacheHit}
	if len(cacheOutcomes) != len(wantOutcomes) || cacheOutcomes[0] != wantOutcomes[0] || cacheOutcomes[1] != wantOutcomes[1] {
		t.Fatalf("cache outcomes = %#v, want %#v", cacheOutcomes, wantOutcomes)
	}
}

func TestRuntimeCachesGovernedDashboardQueriesAndToggleBackExecutesZeroSQL(t *testing.T) {
	database := &countingCacheRuntimeDatabase{}
	runtime := &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {
				Columns:    map[string]semanticmodel.ModelColumn{"id": {Name: "id"}},
				Dimensions: map[string]semanticmodel.MetricDimension{"id": {Label: "ID"}},
			},
		}},
		db:         database,
		queryCache: newQueryResultCache(256, ""),
	}
	base := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardAggregate,
		ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 50,
	}
	selected := base
	selected.Filters = []dataquery.Filter{{Field: "orders.id", Operator: "equals", Values: []any{42}}}

	for _, request := range []dataquery.Query{selected, base, selected} {
		if _, err := runtime.ExecuteDataQuery(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	if got := database.queries.Load(); got != 2 {
		t.Fatalf("physical executions = %d, want selection miss + clear miss + toggle-back hit", got)
	}

	selected.Filters[0].Values[0] = 43
	if _, err := runtime.ExecuteDataQuery(context.Background(), selected); err != nil {
		t.Fatal(err)
	}
	if got := database.queries.Load(); got != 3 {
		t.Fatalf("physical executions after governed filter change = %d, want 3", got)
	}

	runtime.ClearQueryCache()
	selected.Filters[0].Values[0] = 42
	if _, err := runtime.ExecuteDataQuery(context.Background(), selected); err != nil {
		t.Fatal(err)
	}
	if got := database.queries.Load(); got != 4 {
		t.Fatalf("physical executions after snapshot generation invalidation = %d, want 4", got)
	}
}

func TestRuntimeReauthorizesBeforeCacheLookupAndRejectsRevocation(t *testing.T) {
	database := &countingCacheRuntimeDatabase{}
	runtime := &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {
				Columns:    map[string]semanticmodel.ModelColumn{"id": {Name: "id"}},
				Dimensions: map[string]semanticmodel.MetricDimension{"id": {Label: "ID"}},
			},
		}},
		db:         database,
		queryCache: newQueryResultCache(256, ""),
	}
	governor := &revocableCacheGovernor{fingerprint: "sha256:policy-one"}
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardAggregate,
		ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 50,
	}
	execute := func() error {
		_, err := runtime.ExecuteDataQuery(dataquery.WithGovernor(t.Context(), governor), request)
		return err
	}

	if err := execute(); err != nil {
		t.Fatal(err)
	}
	if err := execute(); err != nil {
		t.Fatal(err)
	}
	if governor.calls.Load() != 2 || database.queries.Load() != 1 {
		t.Fatalf("governance calls = %d, physical queries = %d; want 2, 1", governor.calls.Load(), database.queries.Load())
	}

	governor.revoked.Store(true)
	if err := execute(); err == nil {
		t.Fatal("revoked query reused an authorized cached result")
	}
	if governor.calls.Load() != 3 || database.queries.Load() != 1 {
		t.Fatalf("post-revocation governance calls = %d, physical queries = %d; want 3, 1", governor.calls.Load(), database.queries.Load())
	}
}

type revocableCacheGovernor struct {
	calls       atomic.Int32
	revoked     atomic.Bool
	fingerprint string
}

func (governor *revocableCacheGovernor) GovernDataQuery(
	_ context.Context,
	request dataquery.Query,
) (dataquery.Query, dataquery.ResultTransformer, error) {
	governor.calls.Add(1)
	if governor.revoked.Load() {
		return request, nil, errors.New("authorization revoked")
	}
	request.EffectivePolicyFingerprint = governor.fingerprint
	return request, nil, nil
}

func TestRuntimeBundleCacheAllHitExecutesZeroAdditionalSQL(t *testing.T) {
	database := &bundleCountingDatabase{}
	runtime := bundleCacheRuntime(database)
	requests := bundleCacheRequests()
	if _, err := runtime.ExecuteDataQueryBundle(context.Background(), requests); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ExecuteDataQueryBundle(context.Background(), requests); err != nil {
		t.Fatal(err)
	}
	if got := database.queries.Load(); got != 1 {
		t.Fatalf("physical executions = %d, want one bundle miss and zero for all-hit", got)
	}
}

func TestRuntimeBundleChargesLogicalRowsOnceOnCacheMiss(t *testing.T) {
	database := &budgetConsumingBundleDatabase{}
	runtime := bundleCacheRuntime(database)
	runtime.resultLimits = dataquery.ResultLimits{MaxRows: 2, MaxBytes: 1 << 20}

	result, err := runtime.ExecuteDataQueryBundle(context.Background(), bundleCacheRequests())
	if err != nil {
		t.Fatalf("bundle within logical row limit: %v", err)
	}
	if got := result.Results["orders"].RowsReturned + result.Results["events"].RowsReturned; got != 2 {
		t.Fatalf("logical rows = %d, want 2", got)
	}
}

type budgetConsumingBundleDatabase struct{ bundleCountingDatabase }

func (d *budgetConsumingBundleDatabase) Query(ctx context.Context, plan semanticquery.Plan) (semanticquery.Rows, error) {
	rows, err := d.bundleCountingDatabase.Query(ctx, plan)
	if err != nil {
		return nil, err
	}
	if budget, ok := dataquery.ResultBudgetFromContext(ctx); ok {
		for _, row := range rows {
			if err := budget.ConsumeRow(row); err != nil {
				return nil, err
			}
		}
	}
	return rows, nil
}

func TestRuntimeBundleRejectsNonDashboardBranchesBeforeFlightCoalescing(t *testing.T) {
	database := &bundleCountingDatabase{}
	runtime := bundleCacheRuntime(database)
	requests := bundleCacheRequests()
	for i := range requests {
		requests[i].Query.Surface = dataquery.SurfaceAPI
		requests[i].Query.Operation = dataquery.OperationAPIQuery
	}
	_, err := runtime.ExecuteDataQueryBundle(context.Background(), requests)
	if err == nil || !dataquery.IsBundleIncompatible(err) {
		t.Fatalf("error = %v, want incompatible non-dashboard bundle", err)
	}
	if database.queries.Load() != 0 {
		t.Fatalf("physical executions = %d, want fail before flight", database.queries.Load())
	}
}

func TestRuntimeBundleCacheMixedHitExecutesOnlyLoneMiss(t *testing.T) {
	database := &bundleCountingDatabase{}
	runtime := bundleCacheRuntime(database)
	requests := bundleCacheRequests()
	if _, err := runtime.ExecuteDataQuery(context.Background(), requests[0].Query); err != nil {
		t.Fatal(err)
	}
	result, err := runtime.ExecuteDataQueryBundle(context.Background(), requests)
	require.NoError(t, err)
	if got := database.queries.Load(); got != 2 {
		t.Fatalf("physical executions = %d, want prime plus lone miss", got)
	}
	if result.Results[requests[0].ID].CacheOutcome != dataquery.CacheHit {
		t.Fatalf("first branch outcome = %q", result.Results[requests[0].ID].CacheOutcome)
	}
}

func TestRuntimeBundleCanceledExecutionDoesNotCacheOrAuditSuccess(t *testing.T) {
	database := &cancelIgnoringBundleDatabase{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	runtime := bundleCacheRuntime(database)
	governor := &bundleAuditGovernor{}
	ctx, cancel := context.WithCancel(dataquery.WithGovernor(context.Background(), governor))
	done := make(chan error, 1)
	go func() {
		_, err := runtime.ExecuteDataQueryBundle(ctx, bundleCacheRequests())
		done <- err
	}()
	<-database.started
	cancel()
	close(database.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled bundle error = %v", err)
	}
	if governor.successes.Load() != 0 {
		t.Fatalf("canceled bundle recorded %d successful branches", governor.successes.Load())
	}
	if _, err := runtime.ExecuteDataQueryBundle(dataquery.WithGovernor(context.Background(), governor), bundleCacheRequests()); err != nil {
		t.Fatal(err)
	}
	if got := database.queries.Load(); got != 2 {
		t.Fatalf("physical executions = %d, want canceled miss plus uncached retry", got)
	}
}

func TestQueryResultCacheCoalescesExactBundleFlightsAndRetriesCanceledOwner(t *testing.T) {
	cache := newQueryResultCache(256, "bundle")
	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	var executions atomic.Int32
	var startedOnce sync.Once
	execute := func(ctx context.Context) (any, error) {
		executions.Add(1)
		startedOnce.Do(func() { close(started) })
		<-release
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return "fresh", nil
	}
	ownerDone := make(chan error, 1)
	go func() {
		_, _, err := cache.coalesce(ownerCtx, "exact-bundle", func() (any, error) { return execute(ownerCtx) })
		ownerDone <- err
	}()
	<-started
	waiterDone := make(chan error, 1)
	go func() {
		result, _, err := cache.coalesce(context.Background(), "exact-bundle", func() (any, error) { return execute(context.Background()) })
		if err == nil && result != "fresh" {
			err = fmt.Errorf("coalesced result = %v", result)
		}
		waiterDone <- err
	}()
	cancelOwner()
	close(release)
	if err := <-ownerDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("owner error = %v", err)
	}
	if err := <-waiterDone; err != nil {
		t.Fatalf("live waiter inherited canceled owner: %v", err)
	}
	if got := executions.Load(); got != 2 {
		t.Fatalf("executions = %d, want canceled owner plus one live replacement", got)
	}
}

type bundleAuditGovernor struct {
	successes atomic.Int32
	failures  atomic.Int32
}

func (g *bundleAuditGovernor) GovernDataQuery(_ context.Context, request dataquery.Query) (dataquery.Query, dataquery.ResultTransformer, error) {
	return request, func(_ *dataquery.Result, err error) error {
		if err == nil {
			g.successes.Add(1)
		} else {
			g.failures.Add(1)
		}
		return nil
	}, nil
}

type cancelIgnoringBundleDatabase struct {
	bundleCountingDatabase
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (d *cancelIgnoringBundleDatabase) Query(ctx context.Context, plan semanticquery.Plan) (semanticquery.Rows, error) {
	d.once.Do(func() {
		close(d.started)
		<-d.release
	})
	return d.bundleCountingDatabase.Query(ctx, plan)
}

func (d *cancelIgnoringBundleDatabase) QueryArrow(ctx context.Context, plan semanticquery.Plan, sink arrowquery.Sink) error {
	rows, err := d.Query(ctx, plan)
	if err != nil {
		return err
	}
	return writeTestRowsArrow(ctx, plan, rows, sink)
}

func TestRuntimeBundleGovernsEveryBranchAndFailsClosedOnMask(t *testing.T) {
	database := &bundleCountingDatabase{}
	runtime := bundleCacheRuntime(database)
	governor := &bundleMaskGovernor{}
	_, err := runtime.ExecuteDataQueryBundle(dataquery.WithGovernor(context.Background(), governor), bundleCacheRequests())
	if err == nil || !dataquery.IsBundleIncompatible(err) {
		t.Fatalf("error = %v, want incompatible masked bundle", err)
	}
	if governor.calls.Load() != 2 {
		t.Fatalf("governance calls = %d, want every branch", governor.calls.Load())
	}
	if database.queries.Load() != 0 {
		t.Fatalf("physical executions = %d, want fail before SQL", database.queries.Load())
	}
}

type bundleMaskGovernor struct{ calls atomic.Int32 }

func (g *bundleMaskGovernor) GovernDataQuery(_ context.Context, request dataquery.Query) (dataquery.Query, dataquery.ResultTransformer, error) {
	if g.calls.Add(1) == 2 {
		request.ColumnMasks = []dataquery.ColumnMask{{Field: "orders.secret", Mask: "redact"}}
	}
	return request, nil, nil
}

func bundleCacheRuntime(database Database) *Runtime {
	return &Runtime{modelID: "sales", model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{"orders": {}}, Measures: map[string]semanticmodel.MetricMeasure{
		"order_count": {Fact: "orders", Aggregation: "count", Empty: "zero"},
		"event_count": {Fact: "orders", Aggregation: "count", Empty: "zero"},
	}}, db: database, queryCache: newQueryResultCache(256, "bundle-test")}
}

func bundleCacheRequests() []dataquery.BundleRequest {
	base := dataquery.Query{Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardAggregate, ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders"}
	first := base
	first.Measures = []dataquery.Field{{Field: "order_count", Alias: "value"}}
	second := base
	second.Measures = []dataquery.Field{{Field: "event_count", Alias: "value"}}
	return []dataquery.BundleRequest{{ID: "orders", Query: first}, {ID: "events", Query: second}}
}

type bundleCountingDatabase struct {
	cacheRuntimeDatabase
	queries atomic.Int32
}

func (d *bundleCountingDatabase) Query(_ context.Context, plan semanticquery.Plan) (semanticquery.Rows, error) {
	d.queries.Add(1)
	if len(plan.Columns) > 0 && plan.Columns[0] == semanticquery.BundleBranchColumn {
		rows := semanticquery.Rows{}
		for ordinal := int64(0); ordinal < 2; ordinal++ {
			row := semanticquery.Row{}
			for _, column := range plan.Columns {
				row[column] = int64(1)
			}
			row[semanticquery.BundleBranchColumn] = ordinal
			rows = append(rows, row)
		}
		return rows, nil
	}
	row := semanticquery.Row{}
	for _, column := range plan.Columns {
		row[column] = int64(1)
	}
	return semanticquery.Rows{row}, nil
}

func (d *bundleCountingDatabase) QueryArrow(ctx context.Context, plan semanticquery.Plan, sink arrowquery.Sink) error {
	rows, err := d.Query(ctx, plan)
	if err != nil {
		return err
	}
	return writeTestRowsArrow(ctx, plan, rows, sink)
}

func TestRuntimeDoesNotCacheNonDashboardQueries(t *testing.T) {
	database := &countingCacheRuntimeDatabase{}
	runtime := &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {
				Columns:    map[string]semanticmodel.ModelColumn{"id": {Name: "id"}},
				Dimensions: map[string]semanticmodel.MetricDimension{"id": {Label: "ID"}},
			},
		}},
		db:         database,
		queryCache: newQueryResultCache(256, ""),
	}
	request := dataquery.Query{
		Surface: dataquery.SurfaceAPI, Operation: dataquery.OperationAPIQuery,
		ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 50,
	}
	for range 2 {
		if _, err := runtime.ExecuteDataQuery(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	if got := database.queries.Load(); got != 2 {
		t.Fatalf("non-dashboard physical executions = %d, want 2", got)
	}
}

func TestRuntimeCountFailsClosedForMaskedAuthorizationProjection(t *testing.T) {
	runtime := &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {Dimensions: map[string]semanticmodel.MetricDimension{"email": {Type: "string"}}},
		}},
		db: cacheRuntimeDatabase{}, queryCache: newQueryResultCache(256, ""),
	}
	_, err := runtime.ExecuteDataQuery(context.Background(), dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardCount,
		ModelID: "sales", Kind: dataquery.KindSemanticRows, Target: "orders", IncludeTotal: true,
		AuthorizationFields: []dataquery.Field{{Field: "orders.email"}},
		ColumnMasks:         []dataquery.ColumnMask{{Field: "orders.email", Mask: "null"}},
	})
	if err == nil || !strings.Contains(err.Error(), "masked fields") {
		t.Fatalf("count error = %v, want masked authorization projection rejection", err)
	}
}

func TestRuntimeDashboardCacheHitDoesNotConsumeReadPermit(t *testing.T) {
	database := &countingCacheRuntimeDatabase{}
	runtime := &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {
				Columns:    map[string]semanticmodel.ModelColumn{"id": {Name: "id"}},
				Dimensions: map[string]semanticmodel.MetricDimension{"id": {Label: "ID"}},
			},
		}},
		db:         database,
		queryCache: newQueryResultCache(256, ""),
	}
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardAggregate,
		ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 50,
	}
	if _, err := runtime.ExecuteDataQuery(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	admission, err := workload.New(workload.Config{MaxRunning: 1, Classes: map[workload.Class]workload.Policy{workload.Interactive: {MaximumRunning: 1}}})
	require.NoError(t, err)
	occupied := make(chan struct{})
	release := make(chan struct{})
	var occupying sync.WaitGroup
	occupying.Add(1)
	go func() {
		defer occupying.Done()
		lease, acquireErr := admission.Acquire(context.Background(), workload.Request{Class: workload.Interactive, WorkspaceID: "sales", Operation: "occupy"})
		if acquireErr != nil {
			return
		}
		close(occupied)
		<-release
		lease.Release()
	}()
	<-occupied
	ctx := workload.WithAdmitter(context.Background(), admission)
	result, err := runtime.ExecuteDataQuery(ctx, request)
	close(release)
	occupying.Wait()
	if err != nil {
		t.Fatalf("cache hit attempted read admission: %v", err)
	}
	if result.CacheOutcome != dataquery.CacheHit {
		t.Fatalf("cache outcome = %q, want hit", result.CacheOutcome)
	}
	if got := database.queries.Load(); got != 1 {
		t.Fatalf("physical executions = %d, want one initial miss", got)
	}
}

func TestRuntimeRefreshInvalidatesCacheBeforeFailingSchemaDiscovery(t *testing.T) {
	runtime := &Runtime{
		modelID:    "sales",
		model:      &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{}},
		db:         failingDiscoveryRuntimeDatabase{},
		sources:    cacheSourceRegistrar{},
		queryCache: newQueryResultCache(256, "mutable"),
	}
	request := dataquery.Query{ModelID: "sales", Kind: dataquery.KindSemanticAggregate}
	var executions atomic.Int32
	execute := func() (dataquery.Result, error) {
		executions.Add(1)
		return dataquery.Result{Rows: []dataquery.Row{{"value": 1}}}, nil
	}
	if _, err := runtime.queryCache.execute(context.Background(), request, execute); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Refresh(context.Background()); err == nil {
		t.Fatal("refresh error = nil, want schema discovery failure")
	}
	if _, err := runtime.queryCache.execute(context.Background(), request, execute); err != nil {
		t.Fatal(err)
	}
	if got := executions.Load(); got != 2 {
		t.Fatalf("physical executions = %d, want cache invalidated after materialization mutation", got)
	}
}

func TestRuntimeRefreshInvalidatesCacheAfterPartialMaterializationFailure(t *testing.T) {
	for _, test := range []struct {
		name    string
		refresh func(*Runtime) error
	}{
		{name: "full refresh", refresh: func(runtime *Runtime) error {
			return runtime.Refresh(context.Background())
		}},
		{name: "selected tables", refresh: func(runtime *Runtime) error {
			return runtime.RefreshModelTables(context.Background(), []string{"first", "second"})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := &partialFailureCacheRuntimeDatabase{}
			runtime := &Runtime{
				modelID: "sales",
				model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
					"first":  {},
					"second": {},
				}},
				db:         database,
				sources:    partialRefreshSourcePreparer{},
				queryCache: newQueryResultCache(256, "mutable"),
			}
			request := dataquery.Query{ModelID: "sales", Kind: dataquery.KindSemanticAggregate}
			var executions atomic.Int32
			execute := func() (dataquery.Result, error) {
				executions.Add(1)
				return dataquery.Result{Rows: []dataquery.Row{{"value": 1}}}, nil
			}
			if _, err := runtime.queryCache.execute(context.Background(), request, execute); err != nil {
				t.Fatal(err)
			}
			if err := test.refresh(runtime); err == nil || !strings.Contains(err.Error(), "second materialization failed") {
				t.Fatalf("refresh error = %v, want second materialization failure", err)
			}
			if !database.firstMaterializationSucceeded {
				t.Fatal("refresh did not mutate the first materialized table before failing")
			}
			if _, err := runtime.queryCache.execute(context.Background(), request, execute); err != nil {
				t.Fatal(err)
			}
			if got := executions.Load(); got != 2 {
				t.Fatalf("physical executions = %d, want cache invalidated after partial materialization", got)
			}
		})
	}
}

type cacheRuntimeDatabase struct{}

type partialFailureCacheRuntimeDatabase struct {
	cacheRuntimeDatabase
	firstMaterializationSucceeded bool
}

func (d *partialFailureCacheRuntimeDatabase) Exec(_ context.Context, statement string) error {
	switch {
	case strings.Contains(statement, "model.first"):
		d.firstMaterializationSucceeded = true
		return nil
	case strings.Contains(statement, "model.second"):
		return errors.New("second materialization failed")
	default:
		return nil
	}
}

type partialRefreshSourcePreparer struct{}

func (partialRefreshSourcePreparer) Prepare(context.Context, *semanticmodel.Model) (PreparedSources, error) {
	return partialRefreshPreparedSources{}, nil
}

type partialRefreshPreparedSources struct{}

func (partialRefreshPreparedSources) PlanModelTable(_ context.Context, _ *semanticmodel.Model, tableName string, _ semanticmodel.Table) (ModelTablePlan, error) {
	return ModelTablePlan{Mode: PlanModeModelSQL, SQL: "CREATE OR REPLACE TABLE model." + tableName + " AS SELECT 1"}, nil
}

func (partialRefreshPreparedSources) Close() error { return nil }

type countingCacheRuntimeDatabase struct {
	cacheRuntimeDatabase
	queries atomic.Int32
}

type arrowCountingRuntimeDatabase struct {
	cacheRuntimeDatabase
	queries atomic.Int32
}

func (d *arrowCountingRuntimeDatabase) QueryArrow(ctx context.Context, plan semanticquery.Plan, sink arrowquery.Sink) error {
	d.queries.Add(1)
	fields := make([]arrow.Field, len(plan.Columns))
	arrays := make([]arrow.Array, len(plan.Columns))
	for index, column := range plan.Columns {
		fields[index] = arrow.Field{Name: column, Type: arrow.PrimitiveTypes.Int64}
		builder := array.NewInt64Builder(memory.DefaultAllocator)
		builder.Append(1)
		arrays[index] = builder.NewArray()
		builder.Release()
	}
	schema := arrow.NewSchema(fields, nil)
	if err := sink.WriteSchema(schema); err != nil {
		return err
	}
	record := array.NewRecordBatch(schema, arrays, 1)
	for _, values := range arrays {
		values.Release()
	}
	defer record.Release()
	if err := arrowquery.ConsumeResultBudget(ctx, record); err != nil {
		return err
	}
	return sink.WriteRecord(record)
}

func writeTestRowsArrow(ctx context.Context, plan semanticquery.Plan, rows semanticquery.Rows, sink arrowquery.Sink) error {
	fields := make([]arrow.Field, len(plan.Columns))
	arrays := make([]arrow.Array, len(plan.Columns))
	for columnIndex, column := range plan.Columns {
		kind := "int"
		for _, row := range rows {
			switch row[column].(type) {
			case string:
				kind = "string"
			case float32, float64:
				kind = "float"
			case bool:
				kind = "bool"
			}
			if row[column] != nil {
				break
			}
		}
		switch kind {
		case "string":
			fields[columnIndex] = arrow.Field{Name: column, Type: arrow.BinaryTypes.String, Nullable: true}
			builder := array.NewStringBuilder(memory.DefaultAllocator)
			for _, row := range rows {
				value, ok := row[column].(string)
				builder.Append(value)
				if !ok {
					builder.SetNull(builder.Len() - 1)
				}
			}
			arrays[columnIndex] = builder.NewArray()
			builder.Release()
		case "float":
			fields[columnIndex] = arrow.Field{Name: column, Type: arrow.PrimitiveTypes.Float64, Nullable: true}
			builder := array.NewFloat64Builder(memory.DefaultAllocator)
			for _, row := range rows {
				switch value := row[column].(type) {
				case float32:
					builder.Append(float64(value))
				case float64:
					builder.Append(value)
				default:
					builder.AppendNull()
				}
			}
			arrays[columnIndex] = builder.NewArray()
			builder.Release()
		case "bool":
			fields[columnIndex] = arrow.Field{Name: column, Type: arrow.FixedWidthTypes.Boolean, Nullable: true}
			builder := array.NewBooleanBuilder(memory.DefaultAllocator)
			for _, row := range rows {
				value, ok := row[column].(bool)
				if ok {
					builder.Append(value)
				} else {
					builder.AppendNull()
				}
			}
			arrays[columnIndex] = builder.NewArray()
			builder.Release()
		default:
			fields[columnIndex] = arrow.Field{Name: column, Type: arrow.PrimitiveTypes.Int64, Nullable: true}
			builder := array.NewInt64Builder(memory.DefaultAllocator)
			for _, row := range rows {
				switch value := row[column].(type) {
				case int:
					builder.Append(int64(value))
				case int32:
					builder.Append(int64(value))
				case int64:
					builder.Append(value)
				default:
					builder.AppendNull()
				}
			}
			arrays[columnIndex] = builder.NewArray()
			builder.Release()
		}
	}
	schema := arrow.NewSchema(fields, nil)
	if err := sink.WriteSchema(schema); err != nil {
		return err
	}
	record := array.NewRecordBatch(schema, arrays, int64(len(rows)))
	for _, values := range arrays {
		values.Release()
	}
	defer record.Release()
	if err := arrowquery.ConsumeResultBudget(ctx, record); err != nil {
		return err
	}
	return sink.WriteRecord(record)
}

type failingDiscoveryRuntimeDatabase struct{ cacheRuntimeDatabase }

func (failingDiscoveryRuntimeDatabase) DiscoverSchemas(context.Context, *semanticmodel.Model) error {
	return errors.New("discover schemas")
}

type cacheSourceRegistrar struct{}

func (registrar cacheSourceRegistrar) Prepare(context.Context, *semanticmodel.Model) (PreparedSources, error) {
	return cachePreparedSources{cacheSourceRegistrar: registrar}, nil
}

func (cacheSourceRegistrar) PlanModelTable(context.Context, *semanticmodel.Model, string, semanticmodel.Table) (ModelTablePlan, error) {
	return ModelTablePlan{}, errors.New("unexpected model table")
}

type cachePreparedSources struct{ cacheSourceRegistrar }

func (cachePreparedSources) Close() error { return nil }

func (d *countingCacheRuntimeDatabase) Query(ctx context.Context, plan semanticquery.Plan) (semanticquery.Rows, error) {
	d.queries.Add(1)
	return d.cacheRuntimeDatabase.Query(ctx, plan)
}

func (d *countingCacheRuntimeDatabase) QueryArrow(ctx context.Context, plan semanticquery.Plan, sink arrowquery.Sink) error {
	rows, err := d.Query(ctx, plan)
	if err != nil {
		return err
	}
	return writeTestRowsArrow(ctx, plan, rows, sink)
}

func TestRuntimeSeparatesConnectionWaitFromDatabaseExecution(t *testing.T) {
	runtime := &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Name: "id"}}},
		}},
		db: timingRuntimeDatabase{},
	}
	result, err := runtime.ExecuteDataQuery(context.Background(), dataquery.Query{
		ModelID: "sales", Kind: dataquery.KindModelTableRows, Target: "orders",
		Operation: dataquery.OperationDashboardRows,
		Fields:    []dataquery.Field{{Field: "id"}},
		Limit:     1,
	})
	require.NoError(t, err)
	if result.ConnectionWaitMS != 10_000 {
		t.Fatalf("connection wait = %dms, want 10000ms", result.ConnectionWaitMS)
	}
	if result.DatabaseMS != 0 {
		t.Fatalf("database execution = %dms, want observed connection wait excluded", result.DatabaseMS)
	}
}

type timingRuntimeDatabase struct{ cacheRuntimeDatabase }

func (timingRuntimeDatabase) Query(ctx context.Context, _ semanticquery.Plan) (semanticquery.Rows, error) {
	// Use a synthetic wait larger than any execution jitter. The runtime must
	// clamp the remaining database duration at zero instead of underflowing.
	dataquery.ObserveConnectionWait(ctx, 10*time.Second)
	time.Sleep(30 * time.Millisecond)
	return semanticquery.Rows{{"id": 1}}, nil
}

func (d timingRuntimeDatabase) QueryArrow(ctx context.Context, plan semanticquery.Plan, sink arrowquery.Sink) error {
	rows, err := d.Query(ctx, plan)
	if err != nil {
		return err
	}
	return writeTestRowsArrow(ctx, plan, rows, sink)
}

func (cacheRuntimeDatabase) Exec(context.Context, string) error { return nil }
func (cacheRuntimeDatabase) Query(context.Context, semanticquery.Plan) (semanticquery.Rows, error) {
	return semanticquery.Rows{{"id": 1}}, nil
}
func (d cacheRuntimeDatabase) QueryArrow(ctx context.Context, plan semanticquery.Plan, sink arrowquery.Sink) error {
	rows, err := d.Query(ctx, plan)
	if err != nil {
		return err
	}
	return writeTestRowsArrow(ctx, plan, rows, sink)
}
func (cacheRuntimeDatabase) Count(context.Context, semanticquery.Plan) (int, error) { return 1, nil }
func (cacheRuntimeDatabase) FloatBounds(context.Context, semanticquery.Plan, string) (semanticquery.FloatBounds, error) {
	return semanticquery.FloatBounds{}, nil
}
func (cacheRuntimeDatabase) Histogram(context.Context, semanticquery.Plan, semanticquery.HistogramSpec) ([]semanticquery.HistogramBin, error) {
	return nil, nil
}
func (cacheRuntimeDatabase) Distribution(context.Context, semanticquery.Plan, semanticquery.DistributionSpec) (semanticquery.Rows, error) {
	return nil, nil
}
func (cacheRuntimeDatabase) Close() error { return nil }
func (cacheRuntimeDatabase) Path() string { return "" }
