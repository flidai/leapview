package materialize

import (
	"context"
	"crypto/sha256"
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
	"github.com/flidai/leapview/internal/analytics/arrowdecode"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/workload"
	"github.com/flidai/leapview/pkg/arrowresult"
	"github.com/stretchr/testify/require"
)

// activatedCacheRuntime upgrades lightweight cache fixtures to the same
// immutable semantic planner contract used by serving runtimes. Test doubles
// intentionally remain explicit about their dataset/model and grain metadata.
func activatedCacheRuntime(t testing.TB, runtime *Runtime) *Runtime {
	t.Helper()
	if runtime == nil || runtime.model == nil {
		return runtime
	}
	if runtime.resultPartition.Version() == 0 {
		runtime.resultPartition = materializeTestPartition(t, resultidentity.PartitionProduction, "")
	}
	for alias, spec := range runtime.model.Datasets {
		table, ok := runtime.model.Tables[alias]
		if !ok {
			continue
		}
		table.ModelName = spec.Model
		if table.Dimensions == nil {
			table.Dimensions = map[string]semanticmodel.MetricDimension{}
		}
		for name, column := range table.Columns {
			if _, exists := table.Dimensions[name]; !exists {
				table.Dimensions[name] = semanticmodel.MetricDimension{Name: name, Type: column.Type, Datatype: column.Datatype}
			}
		}
		if len(table.Dimensions) == 0 {
			table.Dimensions["__row"] = semanticmodel.MetricDimension{Name: "__row", Type: "integer", Datatype: semanticmodel.DataTypeInteger}
		}
		if len(table.Entities) == 0 {
			field := ""
			for name := range table.Dimensions {
				field = name
				break
			}
			table.Entities = map[string]semanticmodel.EntityDefinition{"row": {Type: "primary", Fields: []string{field}}}
			table.GrainEntity = "row"
		} else if table.GrainEntity == "" {
			for name, entity := range table.Entities {
				if entity.Type == "primary" || entity.Type == "unique" {
					table.GrainEntity = name
					break
				}
			}
		}
		runtime.model.Tables[alias] = table
	}
	planner, err := semanticquery.NewCompiledPlanner(runtime.model, semanticquery.WithTableRelation(func(table string) (string, error) {
		return "model." + table, nil
	}))
	if err != nil {
		t.Fatalf("activate cache fixture: %v", err)
	}
	runtime.planner = planner
	if !runtime.dependencyEvidence.Available() {
		relations := make([]resultidentity.DatasetRelation, 0, len(runtime.model.Datasets))
		for dataset := range runtime.model.Datasets {
			relations = append(relations, resultidentity.DatasetRelation{
				Dataset: dataset,
				Relation: resultidentity.RelationRevision{
					RelationID: "model:fixture", RevisionDigest: materializeTestDigest('b'),
				},
			})
		}
		modelID := projectgraph.ResourceID(runtime.modelID)
		if modelID.Validate() != nil {
			modelID = "semantic:fixture"
		}
		evidence, evidenceErr := resultidentity.NewEvidence(resultidentity.EvidenceInput{
			SemanticModelID: modelID, SemanticModelDigest: materializeTestDigest('a'),
			DatasetRelations: relations, BindingFingerprint: materializeTestDigest('c'),
			RuntimeDigest: materializeTestDigest('d'), CapabilityDigest: materializeTestDigest('e'),
		})
		if evidenceErr != nil {
			t.Fatalf("activate dependency evidence: %v", evidenceErr)
		}
		runtime.dependencyEvidence = evidence
	}
	return runtime
}

func materializeTestDigest(value byte) string {
	return "sha256:" + strings.Repeat(string(value), 64)
}

func materializeTestPartition(t testing.TB, kind resultidentity.PartitionKind, candidateID string) resultidentity.Partition {
	t.Helper()
	partition, err := resultidentity.NewPartition(resultidentity.PartitionInput{
		Kind: kind, ProjectID: "project:test", Environment: "test", CandidateID: candidateID,
	})
	require.NoError(t, err)
	return partition
}

func materializeTestDependency(t testing.TB, revision byte) resultidentity.Dependency {
	t.Helper()
	dependency, err := resultidentity.NewDependency(resultidentity.DependencyInput{
		SemanticModelID: "semantic:test", SemanticModelDigest: materializeTestDigest('a'),
		Relations: []resultidentity.RelationRevision{{
			RelationID: "model:test", RevisionDigest: materializeTestDigest(revision),
		}},
		BindingFingerprint: materializeTestDigest('c'),
		Execution: resultidentity.ExecutionIdentity{
			PlannerDigest: materializeTestDigest('d'), RuntimeDigest: materializeTestDigest('e'),
			CapabilityDigest: materializeTestDigest('f'), SettingsDigest: materializeTestDigest('1'),
		},
		ResultFormat: resultidentity.ResultFormat{Name: "arrow-result", Version: 1},
	})
	require.NoError(t, err)
	return dependency
}

func materializeCacheTestIdentity() (resultidentity.Partition, resultidentity.Dependency) {
	partition, err := resultidentity.NewPartition(resultidentity.PartitionInput{
		Kind: resultidentity.PartitionProduction, ProjectID: "project:test", Environment: "test",
	})
	if err != nil {
		panic(err)
	}
	dependency, err := resultidentity.NewDependency(resultidentity.DependencyInput{
		SemanticModelID: "semantic:test", SemanticModelDigest: materializeTestDigest('a'),
		Relations:          []resultidentity.RelationRevision{{RelationID: "model:test", RevisionDigest: materializeTestDigest('b')}},
		BindingFingerprint: materializeTestDigest('c'),
		Execution: resultidentity.ExecutionIdentity{
			PlannerDigest: materializeTestDigest('d'), RuntimeDigest: materializeTestDigest('e'),
			CapabilityDigest: materializeTestDigest('f'), SettingsDigest: materializeTestDigest('1'),
		},
		ResultFormat: resultidentity.ResultFormat{Name: "arrow-result", Version: 1},
	})
	if err != nil {
		panic(err)
	}
	return partition, dependency
}

func canonicalTestPolicy(request dataquery.Query) dataquery.Query {
	if request.EffectivePolicyFingerprint == "" || !strings.HasPrefix(request.EffectivePolicyFingerprint, "sha256:") || len(request.EffectivePolicyFingerprint) != 71 {
		sum := sha256.Sum256([]byte(request.EffectivePolicyFingerprint))
		request.EffectivePolicyFingerprint = fmt.Sprintf("sha256:%x", sum)
	}
	return request
}

func (c *queryResultCache) cacheKeyForTest(request dataquery.Query) (string, uint64, error) {
	partition, dependency := materializeCacheTestIdentity()
	return c.cacheKey(canonicalTestPolicy(request), partition, dependency)
}

func TestQueryResultCacheKeyUsesStableCompositeIdentity(t *testing.T) {
	cache := newQueryResultCache(16)
	request := dataquery.Query{
		Operation: dataquery.OperationDashboardRows, ModelID: "sales",
		Kind: dataquery.KindSemanticRows, Target: "orders",
		Fields:                     []dataquery.Field{{Field: "orders.id", Alias: "id"}},
		EffectivePolicyFingerprint: materializeTestDigest('9'),
	}
	production := materializeTestPartition(t, resultidentity.PartitionProduction, "")
	dependency := materializeTestDependency(t, '2')
	first, _, err := cache.cacheKey(request, production, dependency)
	require.NoError(t, err)
	require.Contains(t, first, `"version":1`)
	require.Contains(t, first, `"dependencyDigest":"`+dependency.Digest()+`"`)
	secondCache := newQueryResultCache(16)
	second, _, err := secondCache.cacheKey(request, production, dependency)
	require.NoError(t, err)
	require.Equal(t, first, second, "snapshot and serving generation must not enter the key")
	emptyCollections := request
	emptyCollections.Filters = []dataquery.Filter{}
	emptyCollections.Sort = []dataquery.Sort{}
	emptyCollections.ColumnMasks = []dataquery.ColumnMask{}
	normalized, _, err := cache.cacheKey(emptyCollections, production, dependency)
	require.NoError(t, err)
	require.Equal(t, first, normalized)

	changedDependency, _, err := cache.cacheKey(request, production, materializeTestDependency(t, '3'))
	require.NoError(t, err)
	require.NotEqual(t, first, changedDependency)

	candidate := materializeTestPartition(t, resultidentity.PartitionCandidate, "candidate-one")
	candidateRequest := request
	candidateRequest.CandidateID = "candidate-one"
	candidateKey, _, err := cache.cacheKey(candidateRequest, candidate, dependency)
	require.NoError(t, err)
	require.NotEqual(t, first, candidateKey)
}

func TestQueryCacheIdentityReasonClassifiesValidatedEvidence(t *testing.T) {
	partition := materializeTestPartition(t, resultidentity.PartitionProduction, "")
	dependency := materializeTestDependency(t, '2')
	request := dataquery.Query{ProjectID: "project:test", EffectivePolicyFingerprint: materializeTestDigest('9')}
	if got := queryCacheIdentityReason(request, partition, dependency); got != dataquery.CacheAdmissionReasonEligible {
		t.Fatalf("valid identity reason = %q", got)
	}
	invalidPolicy := request
	invalidPolicy.EffectivePolicyFingerprint = "malformed"
	if got := queryCacheIdentityReason(invalidPolicy, partition, dependency); got != dataquery.CacheAdmissionReasonPolicyInvalid {
		t.Fatalf("invalid policy reason = %q", got)
	}
	if got := queryCacheIdentityReason(request, resultidentity.Partition{}, dependency); got != dataquery.CacheAdmissionReasonPartitionInvalid {
		t.Fatalf("invalid partition reason = %q", got)
	}
	if got := queryCacheIdentityReason(request, partition, resultidentity.Dependency{}); got != dataquery.CacheAdmissionReasonDependencyInvalid {
		t.Fatalf("invalid dependency reason = %q", got)
	}
}

// The row-shaped helpers below exist only to preserve cache-policy tests while
// production cache storage is Arrow-only.
func (c *queryResultCache) execute(ctx context.Context, request dataquery.Query, execute func() (dataquery.Result, error)) (dataquery.Result, error) {
	key, generation, err := c.cacheKeyForTest(request)
	if err != nil {
		return dataquery.Result{}, err
	}
	if cached, ok := c.get(key); ok {
		return cached, nil
	}
	value, shared, err := c.execution.Coalesce(ctx, fmt.Sprintf("test-query:%d:%s", generation, key), func(context.Context) (any, error) {
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
	key, generation, err := c.cacheKeyForTest(request)
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
	rows, err := arrowdecode.DecodeRows(entry.Data())
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
	key, _, err := runtime.queryCache.cacheKey(request, runtime.resultPartition, dependency)
	require.NoError(t, err)
	entry, _, found, err := runtime.queryCache.scope.LookupArrow(key)
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

func TestRuntimeSharedPartitionCacheReusesDataWithoutStaleSQLOrByteLifetime(t *testing.T) {
	pool, err := resultcache.New(resultcache.Limits{
		RuntimeEntries: 16, RuntimeBytes: 1 << 20, NodeEntries: 32, NodeBytes: 2 << 20,
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
	key, generation, err := first.queryCache.cacheKey(request, first.resultPartition, firstDependency)
	require.NoError(t, err)
	first.queryCache.store(key, generation, dataquery.Result{
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
		Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 1,
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

func TestRuntimePlanningFailureRetainsExecutionFailureClassification(t *testing.T) {
	database := &countingCacheRuntimeDatabase{}
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}}},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}},
		db: database, queryCache: newQueryResultCache(256),
	})
	admission, err := workload.New(workload.Config{
		MaxRunning: 1,
		Classes: map[workload.Class]workload.Policy{
			workload.Interactive: {MaximumRunning: 1},
		},
	})
	require.NoError(t, err)
	observations := []dataquery.CacheObservation{}
	ctx := workload.WithAdmitter(context.Background(), admission)
	ctx = dataquery.WithCacheObserver(ctx, func(observation dataquery.CacheObservation) { observations = append(observations, observation) })
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardRows,
		ModelID: "sales", Kind: dataquery.KindSemanticRows, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.missing", Alias: "missing"}}, Limit: 1,
	}
	key, generation, err := runtime.queryCache.cacheKeyForTest(request)
	require.NoError(t, err)
	runtime.queryCache.store(key, generation, dataquery.Result{
		Columns: dataquery.ColumnsFromNames([]string{"missing"}),
		Rows:    []dataquery.Row{{"missing": "stale"}},
	})
	result, err := runtime.ExecuteDataQuery(ctx, request)
	require.Error(t, err)
	require.Equal(t, dataquery.ExecutionFailed, result.ExecutionState)
	require.Empty(t, result.Status)
	require.Empty(t, result.Error)
	require.Equal(t, int32(0), database.queries.Load())
	require.Equal(t, 1, runtime.queryCache.scope.Stats().Entries)
	require.Equal(t, 1, countCacheObservations(observations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationAdmission && observation.Decision == dataquery.CacheAdmissionRejected && observation.AdmissionReason == dataquery.CacheAdmissionReasonPlanningFailed
	}))
	require.Zero(t, countCacheObservations(observations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationLookup
	}))
}

type rejectingQueryAdmitter struct{ calls int }

func (a *rejectingQueryAdmitter) Acquire(_ context.Context, request workload.Request) (workload.Lease, error) {
	a.calls++
	return nil, &workload.Rejection{
		Reason: workload.InstanceMemoryLimit, Class: request.Class,
		PrincipalID: request.PrincipalID, Operation: request.Operation,
	}
}

func TestRuntimeNonCacheableQueryIsAdmittedBeforePlanning(t *testing.T) {
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}}},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}},
		db: &countingCacheRuntimeDatabase{}, queryCache: newQueryResultCache(256),
	})
	admitter := &rejectingQueryAdmitter{}
	request := dataquery.Query{
		Surface: dataquery.SurfaceAPI, Operation: dataquery.OperationDashboardRows,
		ModelID: "sales", Kind: dataquery.KindSemanticRows, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.missing", Alias: "missing"}}, Limit: 1,
	}
	result, err := runtime.ExecuteDataQuery(workload.WithAdmitter(context.Background(), admitter), request)
	require.Error(t, err)
	require.Equal(t, 1, admitter.calls)
	require.Equal(t, dataquery.ExecutionRejected, result.ExecutionState)
	reason, found := workload.ReasonOf(err)
	require.True(t, found)
	require.Equal(t, workload.InstanceMemoryLimit, reason)
}

func TestRuntimeCacheableQueryPlansBeforeAdmissionAndCacheLookup(t *testing.T) {
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}}},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}},
		db: &countingCacheRuntimeDatabase{}, queryCache: newQueryResultCache(256),
	})
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardRows,
		ModelID: "sales", Kind: dataquery.KindSemanticRows, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.missing", Alias: "missing"}}, Limit: 1,
	}
	key, generation, err := runtime.queryCache.cacheKeyForTest(request)
	require.NoError(t, err)
	runtime.queryCache.store(key, generation, dataquery.Result{Rows: []dataquery.Row{{"missing": "stale"}}})
	admitter := &rejectingQueryAdmitter{}
	result, err := runtime.ExecuteDataQuery(workload.WithAdmitter(context.Background(), admitter), request)
	require.Error(t, err)
	require.Zero(t, admitter.calls)
	require.Equal(t, dataquery.ExecutionFailed, result.ExecutionState)
	require.Equal(t, 1, runtime.queryCache.scope.Stats().Entries)
}

func TestRuntimeDerivesDependencyFromExecutedPlanProjection(t *testing.T) {
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}}},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}},
		db: cacheRuntimeDatabase{}, queryCache: newQueryResultCache(256),
	})
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardRows,
		ModelID: "sales", Kind: dataquery.KindSemanticRows, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 1,
	}
	planned, err := runtime.planOwnedArrowQuery(request)
	require.NoError(t, err)
	first, ok := runtime.dependencyForPlan(planned.plan)
	require.True(t, ok)

	changed, err := resultidentity.NewEvidence(resultidentity.EvidenceInput{
		SemanticModelID: "sales", SemanticModelDigest: materializeTestDigest('a'),
		DatasetRelations: []resultidentity.DatasetRelation{{
			Dataset: "orders", Relation: resultidentity.RelationRevision{
				RelationID: "model:fixture", RevisionDigest: materializeTestDigest('9'),
			},
		}},
		BindingFingerprint: materializeTestDigest('c'), RuntimeDigest: materializeTestDigest('d'),
		CapabilityDigest: materializeTestDigest('e'),
	})
	require.NoError(t, err)
	runtime.dependencyEvidence = changed
	second, ok := runtime.dependencyForPlan(planned.plan)
	require.True(t, ok)
	require.NotEqual(t, first.Digest(), second.Digest())
	runtime.resultLimits = dataquery.ResultLimits{MaxRows: 7, MaxBytes: 4096}
	settingsChanged, ok := runtime.dependencyForPlan(planned.plan)
	require.True(t, ok)
	require.NotEqual(t, second.Digest(), settingsChanged.Digest())
}

func TestRuntimeBundleMissingBranchDependencyEvidenceBypassesResultReuse(t *testing.T) {
	database := &bundleCountingDatabase{}
	runtime := bundleCacheRuntime(t, database)
	runtime.dependencyEvidence = resultidentity.Evidence{}
	for range 2 {
		result, err := runtime.ExecuteDataQueryBundle(context.Background(), bundleCacheRequests())
		require.NoError(t, err)
		require.Equal(t, dataquery.CacheMiss, result.Results["orders"].CacheOutcome)
		require.Equal(t, dataquery.CacheMiss, result.Results["events"].CacheOutcome)
	}
	require.Equal(t, int32(2), database.queries.Load())
}

func TestRuntimeSemanticRowsIncludeTotalCountsFilteredPopulationBeforePagination(t *testing.T) {
	database := &totalRowsRuntimeDatabase{}
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {Columns: map[string]semanticmodel.ModelColumn{
				"id":     {Name: "id", Type: "integer", Datatype: semanticmodel.DataTypeInteger},
				"status": {Name: "status", Type: "string", Datatype: semanticmodel.DataTypeString},
			}},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}},
		db:         database,
		queryCache: newQueryResultCache(256),
	})
	result, err := runtime.ExecuteDataQuery(context.Background(), dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardRows,
		ModelID: "sales", Kind: dataquery.KindSemanticRows, Target: "orders",
		Fields:  []dataquery.Field{{Field: "orders.id", Alias: "id"}},
		Filters: []dataquery.Filter{{Field: "orders.status", Operator: "equals", Values: []any{"paid"}}},
		Sort:    []dataquery.Sort{{Field: "orders.id", Direction: "asc"}}, Limit: 1, Offset: 1, IncludeTotal: true,
	})
	require.NoError(t, err)
	require.True(t, result.TotalRowsKnown)
	require.Equal(t, 3, result.TotalRows)
	require.Equal(t, int64(2), result.Rows[0]["id"])
	require.Equal(t, []dataquery.Column{{Name: "id"}}, result.Columns)
	if got := database.queries.Load(); got != 1 {
		t.Fatalf("physical executions = %d, want one data query with an inline total", got)
	}
	for _, want := range []string{
		`COUNT(*) OVER () AS "__leapview_total_rows"`,
		`WHERE "orders"."status" = ?`,
		`ORDER BY "id" ASC`,
		`LIMIT 1 OFFSET 1`,
	} {
		if !strings.Contains(database.plan.SQL, want) {
			t.Fatalf("rendered total rows SQL missing %q:\n%s", want, database.plan.SQL)
		}
	}
	if strings.Index(database.plan.SQL, `COUNT(*) OVER ()`) > strings.Index(database.plan.SQL, `LIMIT 1`) {
		t.Fatalf("total window occurs after pagination:\n%s", database.plan.SQL)
	}
}

func TestQueryResultCacheUsesGovernedRequestAndReturnsDeepCopies(t *testing.T) {
	cache := newQueryResultCache(256)
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
	cache := newQueryResultCacheWithLimits(10, 1200)
	first := dataquery.Query{ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Metrics: []dataquery.Field{{Field: "revenue"}}}
	second := first
	second.Metrics = []dataquery.Field{{Field: "orders"}}
	large := first
	large.Metrics = []dataquery.Field{{Field: "large"}}

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
	cache := newQueryResultCache(256)
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
	cache := newQueryResultCache(256)
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardCount,
		ModelID: "sales", Kind: dataquery.KindSemanticRows, Target: "orders", IncludeTotal: true,
		AuthorizationFields: []dataquery.Field{{Field: "orders.customer_email"}},
	}
	first, _, err := cache.cacheKeyForTest(request)
	require.NoError(t, err)
	request.AuthorizationFields = []dataquery.Field{{Field: "orders.customer_id"}}
	second, _, err := cache.cacheKeyForTest(request)
	require.NoError(t, err)
	if first == second {
		t.Fatal("count cache key ignored its authorization projection")
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
	cache := newQueryResultCache(256)
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardSpatialTileBudget,
		ModelID: "sales", Kind: dataquery.KindSemanticSpatialTileBudget, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.latitude", Alias: "latitude"}, {Field: "orders.longitude", Alias: "longitude"}},
		SpatialTileBudget: &dataquery.SpatialTileBudget{
			Latitude: dataquery.Field{Field: "orders.latitude", Alias: "latitude"}, Longitude: dataquery.Field{Field: "orders.longitude", Alias: "longitude"},
			Zoom: 10, Buffer: 768, FeatureCap: 5_000, MaximumBytes: 512 * 1024,
		},
	}
	baseline, _, err := cache.cacheKeyForTest(request)
	require.NoError(t, err)
	if !strings.Contains(baseline, `"spatialTileGenerationVersion":5`) {
		t.Fatalf("spatial tile budget cache key has no generation version: %s", baseline)
	}
	variant := request
	budget := *request.SpatialTileBudget
	budget.Zoom++
	variant.SpatialTileBudget = &budget
	key, _, err := cache.cacheKeyForTest(variant)
	require.NoError(t, err)
	if key == baseline {
		t.Fatal("spatial tile budget zoom reused the baseline key")
	}
}

func TestQueryResultCacheKeysEverySpatialTileCoordinateAndPrecision(t *testing.T) {
	cache := newQueryResultCache(256)
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardSpatialTile,
		ModelID: "sales", Kind: dataquery.KindSemanticSpatialTile, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.latitude", Alias: "latitude"}, {Field: "orders.longitude", Alias: "longitude"}},
		SpatialTile: &dataquery.SpatialTile{
			Latitude: dataquery.Field{Field: "orders.latitude", Alias: "latitude"}, Longitude: dataquery.Field{Field: "orders.longitude", Alias: "longitude"},
			Zoom: 10, MetatileX: 376, MetatileY: 512, MetatileSize: 4, CellPixels: 48, Buffer: 768, FeatureCap: 5000, Precision: dataquery.SpatialTilePrecisionRaw,
		},
	}
	baseline, _, err := cache.cacheKeyForTest(request)
	require.NoError(t, err)
	if !strings.Contains(baseline, `"spatialTileGenerationVersion":5`) {
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
		key, _, err := cache.cacheKeyForTest(variant)
		require.NoError(t, err)
		if key == baseline {
			t.Fatalf("spatial tile cache variant %d reused the baseline key", index)
		}
	}
}

func TestQueryResultCacheDoesNotCacheErrorsAndInvalidatesGeneration(t *testing.T) {
	cache := newQueryResultCache(1)
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
		cache := newQueryResultCache(256)
		request := dataquery.Query{
			ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders",
			Operation: dataquery.OperationDashboardFilterOptions,
		}

		key, generation, err := cache.cacheKeyForTest(request)
		require.NoError(t, err)
		flightStarted := make(chan struct{})
		releaseCanceledFlight := make(chan struct{})
		ownerContext, cancelOwner := context.WithCancel(context.Background())
		go func() {
			_, _, _ = cache.execution.Coalesce(ownerContext, fmt.Sprintf("test-query:%d:%s", generation, key), func(context.Context) (any, error) {
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
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}}},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}},
		db:         cacheRuntimeDatabase{},
		queryCache: newQueryResultCache(256),
	})
	physicalQueries := 0
	cacheOutcomes := []string{}
	typedObservations := []dataquery.CacheObservation{}
	ctx := dataquery.WithPhysicalQueryObserver(context.Background(), func(observation dataquery.PhysicalQueryObservation) {
		physicalQueries += observation.Count
	})
	ctx = dataquery.WithCacheOutcomeObserver(ctx, func(outcome string) { cacheOutcomes = append(cacheOutcomes, outcome) })
	ctx = dataquery.WithCacheObserver(ctx, func(observation dataquery.CacheObservation) {
		typedObservations = append(typedObservations, observation)
	})
	request := dataquery.Query{
		Surface:                    dataquery.SurfaceDashboard,
		EffectivePolicyFingerprint: materializeTestDigest('9'),
		ModelID:                    "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders",
		Operation: dataquery.OperationDashboardFilterOptions,
		Fields:    []dataquery.Field{{Field: "orders.id", Alias: "id"}},
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
	lookups := make([]dataquery.CacheObservation, 0, 2)
	for _, observation := range typedObservations {
		if observation.Phase == dataquery.CacheObservationLookup {
			lookups = append(lookups, observation)
		}
	}
	require.Len(t, lookups, 2, "execution-flight second chance must not count as a logical lookup")
	require.Equal(t, dataquery.CacheLookupMissColdStart, lookups[0].MissReason)
	require.Equal(t, dataquery.CacheHitCurrentGeneration, lookups[1].HitSource)
}

func TestRuntimeCachesGovernedDashboardQueriesAndToggleBackExecutesZeroSQL(t *testing.T) {
	database := &countingCacheRuntimeDatabase{}
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {
				Columns:    map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}},
				Dimensions: map[string]semanticmodel.MetricDimension{"id": {Label: "ID", Datatype: semanticmodel.DataTypeInteger}},
			},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}},
		db:         database,
		queryCache: newQueryResultCache(256),
	})
	base := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardAggregate,
		EffectivePolicyFingerprint: materializeTestDigest('9'),
		ModelID:                    "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders",
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
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {
				Columns:    map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}},
				Dimensions: map[string]semanticmodel.MetricDimension{"id": {Label: "ID", Datatype: semanticmodel.DataTypeInteger}},
			},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}},
		db:         database,
		queryCache: newQueryResultCache(256),
	})
	governor := &revocableCacheGovernor{fingerprint: materializeTestDigest('9')}
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardAggregate,
		EffectivePolicyFingerprint: materializeTestDigest('9'),
		ModelID:                    "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders",
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
	runtime := bundleCacheRuntime(t, database)
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
	runtime := bundleCacheRuntime(t, database)
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
	runtime := bundleCacheRuntime(t, database)
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
	runtime := bundleCacheRuntime(t, database)
	requests := bundleCacheRequests()
	if _, err := runtime.ExecuteDataQuery(context.Background(), requests[0].Query); err != nil {
		t.Fatal(err)
	}
	observations := []dataquery.CacheObservation{}
	ctx := dataquery.WithCacheObserver(context.Background(), func(observation dataquery.CacheObservation) { observations = append(observations, observation) })
	result, err := runtime.ExecuteDataQueryBundle(ctx, requests)
	require.NoError(t, err)
	if got := database.queries.Load(); got != 2 {
		t.Fatalf("physical executions = %d, want prime plus lone miss", got)
	}
	if result.Results[requests[0].ID].CacheOutcome != dataquery.CacheHit {
		t.Fatalf("first branch outcome = %q", result.Results[requests[0].ID].CacheOutcome)
	}
	require.Equal(t, 2, countCacheObservations(observations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationAdmission
	}))
	require.Equal(t, 2, countCacheObservations(observations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationLookup
	}))
	require.Equal(t, 2, countCacheObservations(observations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationFinal
	}))
}

func TestRuntimeBundleLookupErrorFlushesEarlierBranchObservations(t *testing.T) {
	runtime := bundleCacheRuntime(t, &bundleCountingDatabase{})
	observations := []dataquery.CacheObservation{}
	closed := false
	ctx := dataquery.WithCacheObserver(context.Background(), func(observation dataquery.CacheObservation) {
		observations = append(observations, observation)
		if !closed && observation.Phase == dataquery.CacheObservationLookup && observation.MissReason != "" {
			closed = true
			require.NoError(t, runtime.queryCache.scope.Close())
		}
	})

	_, err := runtime.ExecuteDataQueryBundle(ctx, bundleCacheRequests())
	require.Error(t, err)
	require.True(t, closed)
	require.Equal(t, len(bundleCacheRequests()), countCacheObservations(observations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationAdmission
	}))
	require.Equal(t, len(bundleCacheRequests()), countCacheObservations(observations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationLookup
	}))
	require.Equal(t, len(bundleCacheRequests()), countCacheObservations(observations, func(observation dataquery.CacheObservation) bool {
		return observation.Phase == dataquery.CacheObservationFinal && observation.Outcome == dataquery.CacheObservationError
	}))
}

func TestRuntimeDegenerateBundleObservesFirstLogicalLookupReason(t *testing.T) {
	database := &bundleCountingDatabase{}
	runtime := bundleCacheRuntime(t, database)
	requests := bundleCacheRequests()
	for _, request := range requests {
		require.NoError(t, primeBundleBranch(runtime, request))
	}

	planned, err := runtime.planOwnedArrowQuery(requests[1].Query)
	require.NoError(t, err)
	dependency, reusable := runtime.dependencyForPlan(planned.plan)
	require.True(t, reusable)
	address, err := runtime.queryCache.cacheAddress(requests[1].Query, runtime.resultPartition, dependency)
	require.NoError(t, err)
	runtime.queryCache.scope.Delete(address.key)

	observations := []dataquery.CacheObservation{}
	restored := false
	ctx := dataquery.WithCacheObserver(context.Background(), func(observation dataquery.CacheObservation) {
		observations = append(observations, observation)
		if !restored && observation.Phase == dataquery.CacheObservationLookup && observation.MissReason != "" {
			restored = true
			runtime.queryCache.store(address.key, address.generation, dataquery.Result{
				Columns: dataquery.ColumnsFromNames([]string{"value"}),
				Rows:    []dataquery.Row{{"value": int64(1)}},
			})
		}
	})
	result, err := runtime.ExecuteDataQueryBundle(ctx, requests)
	require.NoError(t, err)
	require.True(t, restored)
	require.Equal(t, int32(2), database.queries.Load(), "the internal safety lookup should see the restored entry")
	require.Equal(t, dataquery.CacheHit, result.Results[requests[1].ID].CacheOutcome)

	lookups := make([]dataquery.CacheObservation, 0, len(requests))
	for _, observation := range observations {
		if observation.Phase == dataquery.CacheObservationLookup {
			lookups = append(lookups, observation)
		}
	}
	require.Len(t, lookups, len(requests), "each bundle branch must emit one logical lookup")
	require.Equal(t, dataquery.CacheHitCurrentGeneration, lookups[0].HitSource)
	require.Equal(t, dataquery.CacheLookupMissAbsentEntry, lookups[1].MissReason, "the recorded miss must come from the first bundle lookup")
}

func countCacheObservations(observations []dataquery.CacheObservation, match func(dataquery.CacheObservation) bool) int {
	count := 0
	for _, observation := range observations {
		if match(observation) {
			count++
		}
	}
	return count
}

func TestRuntimeBundleCanceledExecutionDoesNotCacheOrAuditSuccess(t *testing.T) {
	database := &cancelIgnoringBundleDatabase{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	runtime := bundleCacheRuntime(t, database)
	governor := &bundleAuditGovernor{}
	ctx, cancel := context.WithCancel(dataquery.WithGovernor(context.Background(), governor))
	done := make(chan error, 1)
	go func() {
		_, err := runtime.ExecuteDataQueryBundle(ctx, bundleCacheRequests())
		done <- err
	}()
	select {
	case <-database.started:
	case err := <-done:
		t.Fatalf("bundle execution exited before database start: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bundle database execution to start")
	}
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
	cache := newQueryResultCache(256)
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
		_, _, err := cache.coalesce(ownerCtx, "exact-bundle", func(executionCtx context.Context) (any, error) { return execute(executionCtx) })
		ownerDone <- err
	}()
	<-started
	waiterDone := make(chan error, 1)
	go func() {
		result, _, err := cache.coalesce(context.Background(), "exact-bundle", func(executionCtx context.Context) (any, error) { return execute(executionCtx) })
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
	runtime := bundleCacheRuntime(t, database)
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

func bundleCacheRuntime(t testing.TB, database Database) *Runtime {
	return activatedCacheRuntime(t, &Runtime{modelID: "sales", model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{"orders": {
		Dimensions: map[string]semanticmodel.MetricDimension{
			"id": {Type: "number", Datatype: semanticmodel.DataTypeInteger},
		},
	}}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}, Metrics: map[string]semanticmodel.Metric{
		"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.id"}, Empty: "zero"},
		"event_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.id"}, Empty: "zero"},
	}}, db: database, queryCache: newQueryResultCache(256)})
}

func bundleCacheRequests() []dataquery.BundleRequest {
	base := dataquery.Query{Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardAggregate, EffectivePolicyFingerprint: materializeTestDigest('9'), ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders"}
	first := base
	first.Metrics = []dataquery.Field{{Field: "order_count", Alias: "value"}}
	second := base
	second.Metrics = []dataquery.Field{{Field: "event_count", Alias: "value"}}
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
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {
				Columns:    map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}},
				Dimensions: map[string]semanticmodel.MetricDimension{"id": {Label: "ID", Datatype: semanticmodel.DataTypeInteger}},
			},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}},
		db:         database,
		queryCache: newQueryResultCache(256),
	})
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
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {Dimensions: map[string]semanticmodel.MetricDimension{"email": {Type: "string", Datatype: semanticmodel.DataTypeString}}},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}},
		db: cacheRuntimeDatabase{}, queryCache: newQueryResultCache(256),
	})
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
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {
				Columns:    map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}},
				Dimensions: map[string]semanticmodel.MetricDimension{"id": {Label: "ID", Datatype: semanticmodel.DataTypeInteger}},
			},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}},
		db:         database,
		queryCache: newQueryResultCache(256),
	})
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardAggregate,
		EffectivePolicyFingerprint: materializeTestDigest('9'),
		ModelID:                    "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders",
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
		lease, acquireErr := admission.Acquire(context.Background(), workload.Request{Class: workload.Interactive, PrincipalID: "sales", Operation: "occupy", EstimatedMemoryBytes: 1})
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
		queryCache: newQueryResultCache(256),
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
				}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"first": {Model: "first"}, "second": {Model: "second"}}},
				db:         database,
				sources:    partialRefreshSourcePreparer{},
				queryCache: newQueryResultCache(256),
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

type totalRowsRuntimeDatabase struct {
	cacheRuntimeDatabase
	queries atomic.Int32
	plan    semanticquery.Plan
}

func (d *totalRowsRuntimeDatabase) QueryArrow(ctx context.Context, plan semanticquery.Plan, sink arrowquery.Sink) error {
	d.queries.Add(1)
	d.plan = plan
	return writeTestRowsArrow(ctx, plan, semanticquery.Rows{{"id": int64(2), totalRowsColumn: int64(3)}}, sink)
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
	runtime := activatedCacheRuntime(t, &Runtime{
		modelID: "sales",
		model: &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
			"orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}}},
		}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}},
		db: timingRuntimeDatabase{},
	})
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
