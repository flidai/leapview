package materialize

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/stretchr/testify/require"
)

func TestRuntimeCachesOwnedArrowAndRebuildsRequestTimingOnHit(t *testing.T) {
	database := &arrowCountingRuntimeDatabase{}
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}}},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}},
		db:         database,
		queryCache: newQueryResultCache(256),
	})
	request := dataquery.Query{Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardRows, EffectivePolicyFingerprint: materializeTestDigest('9'), ModelID: "sales", Kind: dataquery.KindSemanticRows, Target: "orders", Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 1}
	first, err := runtime.ExecuteDataQuery(context.Background(), request)
	require.NoError(t, err)
	planned, err := runtime.planOwnedArrowQuery(request)
	require.NoError(t, err)
	dependency, reusable := runtime.dependencyForPlan(planned.plan)
	require.True(t, reusable)
	queryDigest, err := planned.plan.ResultEquivalenceDigest()
	require.NoError(t, err)
	address, err := runtime.queryCache.cacheAddressWithDigest(request, runtime.resultPartition, dependency, queryDigest)
	require.NoError(t, err)
	entry, _, found, err := runtime.queryCache.scope.LookupArrow(address.key)
	require.NoError(t, err)
	require.True(t, found)
	defer entry.Release()
	require.Empty(t, entry.Metadata().SQL)
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

func TestRuntimeCacheUsesPlannerEquivalentOperatorAndSortSyntax(t *testing.T) {
	database := &arrowCountingRuntimeDatabase{}
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}}},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}},
		db:         database,
		queryCache: newQueryResultCache(256),
	})
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardRows,
		EffectivePolicyFingerprint: materializeTestDigest('9'), ModelID: "sales",
		Kind: dataquery.KindSemanticRows, Target: "orders",
		Fields:  []dataquery.Field{{Field: "orders.id", Alias: "id"}},
		Filters: []dataquery.Filter{{Field: "orders.id", Operator: "EQUALS", Values: []any{int64(1)}}},
		Sort:    []dataquery.Sort{{Field: "id", Direction: "ASC"}}, Limit: 1,
	}
	first, err := runtime.ExecuteDataQuery(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Filters[0].Operator = "equals"
	request.Sort[0].Direction = "asc"
	second, err := runtime.ExecuteDataQuery(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.CacheOutcome != dataquery.CacheMiss || second.CacheOutcome != dataquery.CacheHit {
		t.Fatalf("cache outcomes = (%q, %q)", first.CacheOutcome, second.CacheOutcome)
	}
	if got := database.queries.Load(); got != 1 {
		t.Fatalf("Arrow executions = %d, want 1", got)
	}
}

func TestRuntimeSharedPartitionCacheReusesDataWithoutStaleSQLOrByteLifetime(t *testing.T) {
	pool, err := resultcache.New(resultcache.Limits{
		PartitionEntries: 16, PartitionBytes: 1 << 20, NodeEntries: 32, NodeBytes: 2 << 20,
	})
	require.NoError(t, err)
	defer pool.Close()
	firstResults, err := pool.OpenSharedScope(resultcache.ScopeID{RuntimeID: "partition-production"})
	require.NoError(t, err)
	firstBytes, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "serving-one"})
	require.NoError(t, err)

	model := func() *semanticmodel.Model {
		return &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}}},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}}
	}
	firstDB, secondDB := &countingCacheRuntimeDatabase{}, &countingCacheRuntimeDatabase{}
	first := activatedCacheRuntime(t, &Runtime{
		modelID: "sales", model: model(), db: firstDB,
		queryCache: newQueryResultCacheWithScopes(firstResults, firstBytes),
	})
	bindPlanner := func(runtime *Runtime, snapshot string) {
		planner, plannerErr := semanticquery.NewCompiledPlanner(runtime.model, semanticquery.WithTableRelation(func(table string) (string, error) {
			return snapshot + "." + table, nil
		}))
		require.NoError(t, plannerErr)
		runtime.planner = planner
	}
	bindPlanner(first, "snapshot_one")
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardRows,
		EffectivePolicyFingerprint: materializeTestDigest('9'), ModelID: "sales",
		Kind: dataquery.KindSemanticRows, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 1,
	}
	firstResult, err := first.ExecuteDataQuery(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, dataquery.CacheMiss, firstResult.CacheOutcome)
	require.Contains(t, firstResult.SQL, "snapshot_one")
	firstPlan, err := first.planOwnedArrowQuery(request)
	require.NoError(t, err)
	firstDependency, reusable := first.dependencyForPlan(firstPlan.plan)
	require.True(t, reusable)
	queryDigest, err := firstPlan.plan.ResultEquivalenceDigest()
	require.NoError(t, err)
	address, err := first.queryCache.cacheAddressWithDigest(request, first.resultPartition, firstDependency, queryDigest)
	require.NoError(t, err)
	first.queryCache.store(address.key, address.generation, dataquery.Result{
		SQL:  "select * from snapshot_one.stale_physical_target",
		Rows: []dataquery.Row{{"id": int64(1)}}, Columns: dataquery.ColumnsFromNames([]string{"id"}),
	})
	require.True(t, first.StoreImmutableBytes("tile", []byte("generation-one")))

	require.NoError(t, first.queryCache.close())
	require.NoError(t, firstResults.Close())
	require.NoError(t, firstBytes.Close())
	secondResults, err := pool.OpenSharedScope(resultcache.ScopeID{RuntimeID: "partition-production"})
	require.NoError(t, err)
	defer secondResults.Close()
	secondBytes, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "serving-two"})
	require.NoError(t, err)
	defer secondBytes.Close()
	second := activatedCacheRuntime(t, &Runtime{
		modelID: "sales", model: model(), db: secondDB,
		queryCache: newQueryResultCacheWithScopes(secondResults, secondBytes),
	})
	defer second.queryCache.close()
	bindPlanner(second, "snapshot_two")
	cutoverObservations := []dataquery.CacheObservation{}
	cutoverCtx := dataquery.WithCacheObserver(context.Background(), func(observation dataquery.CacheObservation) {
		cutoverObservations = append(cutoverObservations, observation)
	})
	secondResult, err := second.ExecuteDataQuery(cutoverCtx, request)
	require.NoError(t, err)
	require.Equal(t, dataquery.CacheHit, secondResult.CacheOutcome)
	require.Contains(t, secondResult.SQL, "snapshot_two")
	require.NotContains(t, secondResult.SQL, "snapshot_one")
	require.Equal(t, int32(1), firstDB.queries.Load())
	require.Zero(t, secondDB.queries.Load())
	require.Equal(t, 1, countCacheObservations(cutoverObservations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationLookup && observation.HitSource == dataquery.CacheHitCutoverRetained
	}))
	changedEvidence, err := resultidentity.NewEvidence(resultidentity.EvidenceInput{
		SemanticModelID: "sales", SemanticModelDigest: materializeTestDigest('a'),
		DatasetRelations: []resultidentity.DatasetRelation{{
			Dataset: "orders", Relation: resultidentity.RelationRevision{
				RelationID: "model:fixture", RevisionDigest: materializeTestDigest('8'),
			},
		}},
		BindingFingerprint: materializeTestDigest('c'), RuntimeDigest: materializeTestDigest('d'),
		CapabilityDigest: materializeTestDigest('e'),
	})
	require.NoError(t, err)
	second.dependencyEvidence = changedEvidence
	changedResult, err := second.ExecuteDataQuery(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, dataquery.CacheMiss, changedResult.CacheOutcome)
	require.Equal(t, int32(1), secondDB.queries.Load())
	_, found, err := second.LookupImmutableBytes("tile")
	require.NoError(t, err)
	require.False(t, found)
}

func TestDormantSharedResultInvalidationBeforeCompatibleGenerationPreventsReuse(t *testing.T) {
	pool, err := resultcache.New(resultcache.Limits{
		RuntimeEntries: 16, RuntimeBytes: 1 << 20, NodeEntries: 32, NodeBytes: 2 << 20,
	})
	require.NoError(t, err)
	defer pool.Close()
	partitionID := resultcache.ScopeID{RuntimeID: "partition-production"}
	firstScope, err := pool.OpenSharedScope(partitionID)
	require.NoError(t, err)
	firstBytes, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "serving-one"})
	require.NoError(t, err)
	first := newQueryResultCacheWithScopes(firstScope, firstBytes)
	request := dataquery.Query{
		ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders",
		Operation: dataquery.OperationDashboardFilterOptions,
	}
	var executions atomic.Int32
	execute := func() (dataquery.Result, error) {
		value := executions.Add(1)
		return dataquery.Result{Rows: []dataquery.Row{{"value": value}}}, nil
	}
	result, err := first.execute(context.Background(), request, execute)
	require.NoError(t, err)
	require.Equal(t, dataquery.CacheMiss, result.CacheOutcome)
	require.NoError(t, first.close())
	require.NoError(t, firstBytes.Close())
	require.NoError(t, firstScope.Close())

	invalidator, err := pool.OpenSharedScope(partitionID)
	require.NoError(t, err)
	invalidator.Invalidate()
	require.NoError(t, invalidator.Close())

	secondScope, err := pool.OpenSharedScope(partitionID)
	require.NoError(t, err)
	defer secondScope.Close()
	secondBytes, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "serving-two"})
	require.NoError(t, err)
	defer secondBytes.Close()
	second := newQueryResultCacheWithScopes(secondScope, secondBytes)
	defer second.close()
	result, err = second.execute(context.Background(), request, execute)
	require.NoError(t, err)
	require.Equal(t, dataquery.CacheMiss, result.CacheOutcome)
	require.Equal(t, int32(2), executions.Load())
}

func TestGenerationExecutionScopesDoNotCoalesceColdMissesButReuseCompletedEntries(t *testing.T) {
	pool, err := resultcache.New(resultcache.Limits{RuntimeEntries: 16, RuntimeBytes: 1 << 20, NodeEntries: 32, NodeBytes: 2 << 20})
	require.NoError(t, err)
	defer pool.Close()
	firstScope, err := pool.OpenSharedScope(resultcache.ScopeID{RuntimeID: "partition-production"})
	require.NoError(t, err)
	secondScope, err := pool.OpenSharedScope(resultcache.ScopeID{RuntimeID: "partition-production"})
	require.NoError(t, err)
	firstBytes, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "generation-one"})
	require.NoError(t, err)
	secondBytes, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "generation-two"})
	require.NoError(t, err)
	first := newQueryResultCacheWithScopes(firstScope, firstBytes)
	second := newQueryResultCacheWithScopes(secondScope, secondBytes)
	defer first.close()
	defer second.close()
	request := dataquery.Query{ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders", Operation: dataquery.OperationDashboardFilterOptions}

	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var executions atomic.Int32
	execute := func() (dataquery.Result, error) {
		executions.Add(1)
		started <- struct{}{}
		<-release
		return dataquery.Result{Rows: []dataquery.Row{{"value": int64(1)}}}, nil
	}
	type response struct {
		result dataquery.Result
		err    error
	}
	responses := make(chan response, 2)
	go func() {
		result, err := first.execute(context.Background(), request, execute)
		responses <- response{result, err}
	}()
	go func() {
		result, err := second.execute(context.Background(), request, execute)
		responses <- response{result, err}
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("different generation cold misses shared one execution")
		}
	}
	close(release)
	for range 2 {
		response := <-responses
		require.NoError(t, response.err)
		require.Equal(t, dataquery.CacheMiss, response.result.CacheOutcome)
	}
	require.Equal(t, int32(2), executions.Load())

	cached, err := second.execute(context.Background(), request, func() (dataquery.Result, error) {
		executions.Add(1)
		return dataquery.Result{}, nil
	})
	require.NoError(t, err)
	require.Equal(t, dataquery.CacheHit, cached.CacheOutcome)
	require.Equal(t, int32(2), executions.Load())
}

func TestGenerationExecutionScopeStillCoalescesSameGenerationMisses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := newQueryResultCache(16)
		defer cache.close()
		request := dataquery.Query{ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders", Operation: dataquery.OperationDashboardFilterOptions}
		started := make(chan struct{})
		release := make(chan struct{})
		var calls atomic.Int32
		execute := func() (dataquery.Result, error) {
			if calls.Add(1) == 1 {
				close(started)
			}
			<-release
			return dataquery.Result{Rows: []dataquery.Row{{"value": int64(1)}}}, nil
		}
		results := make(chan dataquery.Result, 2)
		errs := make(chan error, 2)
		go func() {
			result, err := cache.execute(context.Background(), request, execute)
			results <- result
			errs <- err
		}()
		<-started
		go func() {
			result, err := cache.execute(context.Background(), request, execute)
			results <- result
			errs <- err
		}()
		synctest.Wait()
		require.Equal(t, int32(1), calls.Load())
		close(release)
		for range 2 {
			require.NoError(t, <-errs)
			<-results
		}
		require.Equal(t, int32(1), calls.Load())
	})
}

func TestImmutableByteFlightUsesExecutionScopeCancellationAndDrainsBeforeCacheRelease(t *testing.T) {
	pool, err := resultcache.New(resultcache.Limits{RuntimeEntries: 4, RuntimeBytes: 1 << 20, NodeEntries: 8, NodeBytes: 2 << 20})
	require.NoError(t, err)
	defer pool.Close()
	stable, err := pool.OpenSharedScope(resultcache.ScopeID{RuntimeID: "partition-production"})
	require.NoError(t, err)
	bytes, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "generation-one"})
	require.NoError(t, err)
	execution := resultcache.NewExecutionScope()
	cache := newQueryResultCacheWithExecutionScope(stable, bytes, execution, true)
	cache.ownScope()

	started := make(chan struct{})
	scopesOpen := make(chan error, 1)
	flightDone := make(chan error, 1)
	go func() {
		_, flightErr := cache.coalesceBytes(context.Background(), "spatial-metatile", func(executionCtx context.Context) error {
			close(started)
			<-executionCtx.Done()
			if _, _, _, lookupErr := stable.LookupArrow("missing"); lookupErr != nil {
				scopesOpen <- lookupErr
				return executionCtx.Err()
			}
			if _, _, _, lookupErr := bytes.LookupBytes("missing"); lookupErr != nil {
				scopesOpen <- lookupErr
				return executionCtx.Err()
			}
			scopesOpen <- nil
			return executionCtx.Err()
		})
		flightDone <- flightErr
	}()
	<-started

	closed := make(chan error, 1)
	go func() { closed <- cache.close() }()
	select {
	case closeErr := <-closed:
		require.NoError(t, closeErr)
	case <-time.After(2 * time.Second):
		t.Fatal("cache close did not cancel and drain immutable-byte owner")
	}
	require.NoError(t, <-scopesOpen, "cache scopes closed before immutable-byte owner drained")
	require.ErrorIs(t, <-flightDone, resultcache.ErrExecutionScopeClosed)
	_, _, _, err = stable.LookupArrow("missing")
	require.Error(t, err, "stable cache handle remained open after generation close")
	_, _, _, err = bytes.LookupBytes("missing")
	require.Error(t, err, "immutable byte cache remained open after generation close")
}

func TestInvalidationDuringGenerationFlightStillRejectsStaleStore(t *testing.T) {
	cache := newQueryResultCache(16)
	defer cache.close()
	request := dataquery.Query{ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders", Operation: dataquery.OperationDashboardFilterOptions}
	started := make(chan struct{})
	release := make(chan struct{})
	var executions atomic.Int32
	done := make(chan error, 1)
	go func() {
		_, err := cache.execute(context.Background(), request, func() (dataquery.Result, error) {
			executions.Add(1)
			close(started)
			<-release
			return dataquery.Result{Rows: []dataquery.Row{{"value": int64(1)}}}, nil
		})
		done <- err
	}()
	<-started
	cache.clear()
	close(release)
	require.NoError(t, <-done)
	_, _, _, hit, err := cache.lookup(request)
	require.NoError(t, err)
	require.False(t, hit)

	_, err = cache.execute(context.Background(), request, func() (dataquery.Result, error) {
		executions.Add(1)
		return dataquery.Result{Rows: []dataquery.Row{{"value": int64(2)}}}, nil
	})
	require.NoError(t, err)
	require.Equal(t, int32(2), executions.Load())
}

func TestBundleFlightsAreIsolatedByGenerationExecutionScope(t *testing.T) {
	pool, err := resultcache.New(resultcache.Limits{RuntimeEntries: 16, RuntimeBytes: 1 << 20, NodeEntries: 32, NodeBytes: 2 << 20})
	require.NoError(t, err)
	defer pool.Close()
	firstScope, err := pool.OpenSharedScope(resultcache.ScopeID{RuntimeID: "partition-production"})
	require.NoError(t, err)
	secondScope, err := pool.OpenSharedScope(resultcache.ScopeID{RuntimeID: "partition-production"})
	require.NoError(t, err)
	firstBytes, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "bundle-generation-one"})
	require.NoError(t, err)
	secondBytes, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "bundle-generation-two"})
	require.NoError(t, err)
	first := newQueryResultCacheWithScopes(firstScope, firstBytes)
	second := newQueryResultCacheWithScopes(secondScope, secondBytes)
	defer first.close()
	defer second.close()
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var executions atomic.Int32
	execute := func(context.Context) (any, error) {
		executions.Add(1)
		started <- struct{}{}
		<-release
		return "bundle", nil
	}
	done := make(chan error, 2)
	go func() { _, _, err := first.coalesce(context.Background(), "same-bundle", execute); done <- err }()
	go func() { _, _, err := second.coalesce(context.Background(), "same-bundle", execute); done <- err }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("different generation bundle flights shared one execution")
		}
	}
	close(release)
	require.NoError(t, <-done)
	require.NoError(t, <-done)
	require.Equal(t, int32(2), executions.Load())
}

func TestRuntimeInvalidDependencyEvidenceBypassesResultReuseAndRetainsCurrentPlanSQL(t *testing.T) {
	database := &countingCacheRuntimeDatabase{}
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}}},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}},
		db: database, queryCache: newQueryResultCache(256),
	})
	runtime.dependencyEvidence = resultidentity.Evidence{}
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardRows,
		EffectivePolicyFingerprint: materializeTestDigest('9'),
		ModelID:                    "sales", Kind: dataquery.KindSemanticRows, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 1,
	}
	observations := []dataquery.CacheObservation{}
	ctx := dataquery.WithCacheObserver(context.Background(), func(observation dataquery.CacheObservation) { observations = append(observations, observation) })
	for range 2 {
		result, err := runtime.ExecuteDataQuery(ctx, request)
		require.NoError(t, err)
		require.Equal(t, dataquery.CacheMiss, result.CacheOutcome)
		require.NotEmpty(t, result.SQL)
		require.Contains(t, result.SQL, "orders")
	}
	require.Equal(t, int32(2), database.queries.Load())
	require.Zero(t, runtime.queryCache.scope.Stats().Entries)
	require.Equal(t, 2, countCacheObservations(observations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationAdmission && observation.Decision == dataquery.CacheAdmissionBypassed && observation.AdmissionReason == dataquery.CacheAdmissionReasonDependencyUnavailable
	}))
	require.Zero(t, countCacheObservations(observations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationLookup
	}))
}

func TestRuntimeNonCacheableQueryRetainsCurrentPlanSQL(t *testing.T) {
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}}},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}},
		db: &countingCacheRuntimeDatabase{}, queryCache: newQueryResultCache(256),
	})
	request := dataquery.Query{
		Surface: dataquery.SurfaceAPI, Operation: dataquery.OperationDashboardRows,
		ModelID: "sales", Kind: dataquery.KindSemanticRows, Target: "orders",
		EffectivePolicyFingerprint: materializeTestDigest('p'),
		Fields:                     []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 1,
	}
	observations := []dataquery.CacheObservation{}
	ctx := dataquery.WithCacheObserver(context.Background(), func(observation dataquery.CacheObservation) { observations = append(observations, observation) })
	result, err := runtime.ExecuteDataQuery(ctx, request)
	require.NoError(t, err)
	require.NotEmpty(t, result.SQL)
	require.Contains(t, result.SQL, "orders")
	require.Equal(t, 1, countCacheObservations(observations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationAdmission && observation.AdmissionReason == dataquery.CacheAdmissionReasonQueryNotCacheable
	}))
	require.Zero(t, countCacheObservations(observations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationLookup
	}))
}

func TestRuntimeModelTableRowsBypassesLookupStoreAndCoalescing(t *testing.T) {
	database := &countingCacheRuntimeDatabase{}
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}}},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}},
		db: database, queryCache: newQueryResultCache(256),
	})
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardRows,
		EffectivePolicyFingerprint: materializeTestDigest('9'), ModelID: "sales",
		Kind: dataquery.KindModelTableRows, Target: "orders",
		Fields: []dataquery.Field{{Field: "id", Alias: "id"}}, Limit: 1,
	}
	observations := []dataquery.CacheObservation{}
	ctx := dataquery.WithCacheObserver(context.Background(), func(observation dataquery.CacheObservation) {
		observations = append(observations, observation)
	})
	for range 2 {
		result, err := runtime.ExecuteDataQuery(ctx, request)
		require.NoError(t, err)
		require.Empty(t, result.CacheOutcome)
	}
	require.Equal(t, int32(2), database.queries.Load())
	require.Zero(t, runtime.queryCache.scope.Stats().Entries)
	require.Equal(t, 2, countCacheObservations(observations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationAdmission &&
			observation.Decision == dataquery.CacheAdmissionBypassed &&
			observation.AdmissionReason == dataquery.CacheAdmissionReasonQueryNotCacheable
	}))
	require.Zero(t, countCacheObservations(observations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationLookup || observation.Phase == dataquery.CacheObservationStore
	}))
}

func TestRuntimeVolatileModelBypassesResultReuse(t *testing.T) {
	database := &countingCacheRuntimeDatabase{}
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {
				Execution: semanticmodel.ExecutionDefinition{SQL: "SELECT now() AS id"},
				Columns:   map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}},
			},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}},
		db: database, queryCache: newQueryResultCache(256),
	})
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardRows,
		EffectivePolicyFingerprint: materializeTestDigest('9'), ModelID: "sales",
		Kind: dataquery.KindSemanticRows, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 1,
	}
	observations := []dataquery.CacheObservation{}
	ctx := dataquery.WithCacheObserver(context.Background(), func(observation dataquery.CacheObservation) {
		observations = append(observations, observation)
	})
	for range 2 {
		result, err := runtime.ExecuteDataQuery(ctx, request)
		require.NoError(t, err)
		require.Equal(t, dataquery.CacheMiss, result.CacheOutcome)
	}
	require.Equal(t, int32(2), database.queries.Load())
	require.Zero(t, runtime.queryCache.scope.Stats().Entries)
	require.Equal(t, 2, countCacheObservations(observations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationAdmission &&
			observation.Decision == dataquery.CacheAdmissionBypassed &&
			observation.AdmissionReason == dataquery.CacheAdmissionReasonNonDeterministic
	}))
	require.Zero(t, countCacheObservations(observations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationLookup || observation.Phase == dataquery.CacheObservationStore
	}))
}

func TestRuntimeUnrelatedVolatileModelTableDoesNotSuppressCache(t *testing.T) {
	database := &countingCacheRuntimeDatabase{}
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {ModelName: "orders_model", Columns: map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}}},
			"events": {
				ModelName: "events_model",
				Execution: semanticmodel.ExecutionDefinition{SQL: "SELECT now() AS id"},
				Columns:   map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}},
			},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {Model: "orders_model"}, "events": {Model: "events_model"},
		}},
		db: database, queryCache: newQueryResultCache(256),
	})
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardRows,
		EffectivePolicyFingerprint: materializeTestDigest('9'), ModelID: "sales",
		Kind: dataquery.KindSemanticRows, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 1,
	}
	for range 2 {
		result, err := runtime.ExecuteDataQuery(context.Background(), request)
		require.NoError(t, err)
		if result.CacheOutcome != dataquery.CacheMiss && result.CacheOutcome != dataquery.CacheHit {
			t.Fatalf("unexpected cache outcome %q", result.CacheOutcome)
		}
	}
	require.Equal(t, int32(1), database.queries.Load())
	require.Equal(t, 1, runtime.queryCache.scope.Stats().Entries)
}

func TestRuntimeDerivedMetricRemainsCacheable(t *testing.T) {
	database := &countingCacheRuntimeDatabase{}
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{
			Name: "sales", Tables: map[string]semanticmodel.Table{
				"orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}}},
			},
			Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
			Metrics: map[string]semanticmodel.Metric{
				"order_count":  {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.id"}},
				"double_count": {Type: "derived", Expression: "${order_count} * 2"},
			},
		},
		db: database, queryCache: newQueryResultCache(256),
	})
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardAggregate,
		EffectivePolicyFingerprint: materializeTestDigest('9'), ModelID: "sales",
		Kind: dataquery.KindSemanticAggregate, Target: "orders",
		Metrics: []dataquery.Field{{Field: "double_count", Alias: "value"}},
	}
	first, err := runtime.ExecuteDataQuery(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, dataquery.CacheMiss, first.CacheOutcome)
	second, err := runtime.ExecuteDataQuery(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, dataquery.CacheHit, second.CacheOutcome)
	require.Equal(t, int32(1), database.queries.Load())
}

func TestPlanCacheDeterministicFailsClosedForMissingDatasetMapping(t *testing.T) {
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}}},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}},
		db: cacheRuntimeDatabase{}, queryCache: newQueryResultCache(256),
	})
	planned, err := runtime.planOwnedArrowQuery(dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardRows,
		ModelID: "sales", Kind: dataquery.KindSemanticRows, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 1,
	})
	require.NoError(t, err)
	delete(runtime.model.Datasets, "orders")
	require.False(t, planCacheDeterministic(runtime.model, planned.plan))
}

func TestRuntimeInvalidPolicyEvidenceBypassesResultReuse(t *testing.T) {
	database := &countingCacheRuntimeDatabase{}
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}}},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}},
		db: database, queryCache: newQueryResultCache(256),
	})
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardRows,
		EffectivePolicyFingerprint: "sha256:malformed", ModelID: "sales",
		Kind: dataquery.KindSemanticRows, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 1,
	}
	observations := []dataquery.CacheObservation{}
	ctx := dataquery.WithCacheObserver(context.Background(), func(observation dataquery.CacheObservation) { observations = append(observations, observation) })
	for range 2 {
		result, err := runtime.ExecuteDataQuery(ctx, request)
		require.NoError(t, err)
		require.Equal(t, dataquery.CacheMiss, result.CacheOutcome)
	}
	require.Equal(t, int32(2), database.queries.Load())
	require.Zero(t, runtime.queryCache.scope.Stats().Entries)
	require.Equal(t, 2, countCacheObservations(observations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationAdmission && observation.AdmissionReason == dataquery.CacheAdmissionReasonPolicyInvalid
	}))
}

func TestRuntimeCacheFinalLatencyUsesLogicalAttemptOriginForHitMissAndBypass(t *testing.T) {
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}}},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}},
		db: &countingCacheRuntimeDatabase{}, queryCache: newQueryResultCache(256),
	})
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardRows,
		EffectivePolicyFingerprint: materializeTestDigest('9'),
		ModelID:                    "sales", Kind: dataquery.KindSemanticRows, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 1,
	}
	require.NoError(t, primeBundleBranch(runtime, dataquery.BundleRequest{Query: request}))

	observe := func() []dataquery.CacheObservation {
		observations := []dataquery.CacheObservation{}
		ctx := dataquery.WithCacheObserver(context.Background(), func(observation dataquery.CacheObservation) {
			observations = append(observations, observation)
		})
		ctx = withCacheObservationStarted(ctx, time.Now().Add(-2*time.Second))
		_, err := runtime.ExecuteDataQuery(ctx, request)
		require.NoError(t, err)
		return observations
	}

	hit := observe()
	runtime.ClearQueryCache()
	miss := observe()
	runtime.dependencyEvidence = resultidentity.Evidence{}
	bypass := observe()

	hitFinal := cacheObservationByPhase(t, hit, dataquery.CacheObservationFinal)
	missFinal := cacheObservationByPhase(t, miss, dataquery.CacheObservationFinal)
	bypassFinal := cacheObservationByPhase(t, bypass, dataquery.CacheObservationFinal)
	require.Equal(t, dataquery.CacheObservationHit, hitFinal.Outcome)
	require.Equal(t, dataquery.CacheObservationMiss, missFinal.Outcome)
	require.Equal(t, dataquery.CacheObservationMiss, bypassFinal.Outcome)
	finals := []dataquery.CacheObservation{hitFinal, missFinal, bypassFinal}
	for _, final := range finals {
		require.GreaterOrEqual(t, final.Duration, 1500*time.Millisecond)
	}
	minimum, maximum := finals[0].Duration, finals[0].Duration
	for _, final := range finals[1:] {
		if final.Duration < minimum {
			minimum = final.Duration
		}
		if final.Duration > maximum {
			maximum = final.Duration
		}
	}
	require.Less(t, maximum-minimum, time.Second)
	lookup := cacheObservationByPhase(t, hit, dataquery.CacheObservationLookup)
	require.Less(t, lookup.Duration, hitFinal.Duration, "lookup latency must remain narrower than logical cache-attempt latency")
}

func cacheObservationByPhase(t testing.TB, observations []dataquery.CacheObservation, phase dataquery.CacheObservationPhase) dataquery.CacheObservation {
	t.Helper()
	var matches []dataquery.CacheObservation
	for _, observation := range observations {
		if observation.Phase == phase {
			matches = append(matches, observation)
		}
	}
	require.Len(t, matches, 1)
	return matches[0]
}
