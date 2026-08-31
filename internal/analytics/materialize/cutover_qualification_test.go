package materialize

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/flidai/leapview/internal/analytics/arrowdecode"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/stretchr/testify/require"
)

func TestRuntimeCacheCutoverQualification(t *testing.T) {
	const nodeEntries = 32
	const nodeBytes = int64(8 << 20)
	pool, err := resultcache.New(resultcache.Limits{
		RuntimeEntries: 16, RuntimeBytes: 4 << 20, NodeEntries: nodeEntries, NodeBytes: nodeBytes,
	})
	require.NoError(t, err)
	production := materializeTestPartition(t, resultidentity.PartitionProduction, "")
	evidence := cutoverQualificationEvidence(t, '1')
	request := cutoverQualificationRequest()
	databaseA := &cutoverQualificationDatabase{value: 101}
	databaseB := &cutoverQualificationDatabase{value: 202}
	generationA := newCutoverQualificationRuntime(t, pool, "generation-a", production, evidence, databaseA)
	generationB := newCutoverQualificationRuntime(t, pool, "generation-b", production, evidence, databaseB)

	first := executeCutoverQualificationQuery(t, generationA, request)
	require.Equal(t, dataquery.CacheMiss, first.CacheOutcome)
	require.Equal(t, int64(101), cutoverQualificationValue(t, first))
	overlapping := executeCutoverQualificationQuery(t, generationB, request)
	require.Equal(t, dataquery.CacheHit, overlapping.CacheOutcome)
	require.Equal(t, int64(101), cutoverQualificationValue(t, overlapping))
	require.Equal(t, int32(1), databaseA.queries.Load())
	require.Zero(t, databaseB.queries.Load())

	planned, err := generationB.planOwnedArrowQuery(request)
	require.NoError(t, err)
	dependency, reusable := generationB.dependencyForPlan(planned.plan)
	require.True(t, reusable)
	key, _, err := generationB.queryCache.cacheKey(request, generationB.resultPartition, dependency)
	require.NoError(t, err)
	consumer, _, hit, err := generationB.queryCache.scope.LookupArrow(key)
	require.NoError(t, err)
	require.True(t, hit)

	require.NoError(t, generationA.Close())
	stillServing := executeCutoverQualificationQuery(t, generationB, request)
	require.Equal(t, dataquery.CacheHit, stillServing.CacheOutcome)
	require.Equal(t, int64(101), cutoverQualificationValue(t, stillServing))
	require.NoError(t, generationB.Close())
	stats := pool.Stats()
	require.Zero(t, stats.Stable.ActiveScopes)
	require.Equal(t, 1, stats.Stable.DormantScopes)
	assertCutoverQualificationLease(t, consumer, 101)

	databaseC := &cutoverQualificationDatabase{value: 999}
	generationC := newCutoverQualificationRuntime(t, pool, "generation-c", production, evidence, databaseC)
	retained := executeCutoverQualificationQuery(t, generationC, request)
	require.Equal(t, dataquery.CacheHit, retained.CacheOutcome)
	require.Equal(t, int64(101), cutoverQualificationValue(t, retained))
	require.Zero(t, databaseC.queries.Load())
	consumer.Release()

	changedQuery := request
	changedQuery.Limit = 7
	changedPolicy := request
	changedPolicy.EffectivePolicyFingerprint = materializeTestDigest('8')
	for index, test := range []struct {
		name      string
		partition resultidentity.Partition
		evidence  resultidentity.Evidence
		request   dataquery.Query
		value     int64
	}{
		{name: "dependency", partition: production, evidence: cutoverQualificationEvidence(t, '2'), request: request, value: 401},
		{name: "policy", partition: production, evidence: evidence, request: changedPolicy, value: 402},
		{name: "query", partition: production, evidence: evidence, request: changedQuery, value: 403},
		{name: "candidate", partition: materializeTestPartition(t, resultidentity.PartitionCandidate, "candidate-one"), evidence: evidence, request: cutoverCandidateRequest(request, "candidate-one"), value: 404},
		{name: "candidate-id", partition: materializeTestPartition(t, resultidentity.PartitionCandidate, "candidate-two"), evidence: evidence, request: cutoverCandidateRequest(request, "candidate-two"), value: 405},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := &cutoverQualificationDatabase{value: test.value}
			runtime := newCutoverQualificationRuntime(t, pool, fmt.Sprintf("isolation-%d", index), test.partition, test.evidence, database)
			miss := executeCutoverQualificationQuery(t, runtime, test.request)
			require.Equal(t, dataquery.CacheMiss, miss.CacheOutcome)
			require.Equal(t, test.value, cutoverQualificationValue(t, miss), "isolated identity reused another sentinel")
			hit := executeCutoverQualificationQuery(t, runtime, test.request)
			require.Equal(t, dataquery.CacheHit, hit.CacheOutcome)
			require.Equal(t, test.value, cutoverQualificationValue(t, hit))
			require.Equal(t, int32(1), database.queries.Load())
			require.NoError(t, runtime.Close())
			assertCutoverQualificationAccounting(t, pool, nodeEntries, nodeBytes)
		})
	}

	// FAI-534 projects unsupported external lineage to unavailable activation
	// evidence. Materialization must execute normally without consulting the
	// compatible managed cache entry or storing a fallback identity.
	externalDatabase := &cutoverQualificationDatabase{value: 501}
	external := newCutoverQualificationRuntime(t, pool, "external-bypass", production, evidence, externalDatabase)
	external.dependencyEvidence = resultidentity.Evidence{}
	for range 2 {
		result := executeCutoverQualificationQuery(t, external, request)
		require.Equal(t, dataquery.CacheMiss, result.CacheOutcome)
		require.Equal(t, int64(501), cutoverQualificationValue(t, result))
	}
	require.Equal(t, int32(2), externalDatabase.queries.Load())
	require.NoError(t, external.Close())
	require.NoError(t, generationC.Close())
	assertCutoverQualificationAccounting(t, pool, nodeEntries, nodeBytes)
	require.NoError(t, pool.Close())
}

func TestRuntimeCrossGenerationCutoverQualificationDoesNotCoalesceColdMiss(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool, err := resultcache.New(resultcache.Limits{
			RuntimeEntries: 8, RuntimeBytes: 2 << 20, NodeEntries: 8, NodeBytes: 2 << 20,
		})
		require.NoError(t, err)
		release := make(chan struct{})
		databaseA := &cutoverQualificationDatabase{value: 303, release: release}
		databaseB := &cutoverQualificationDatabase{value: 303, release: release}
		partition := materializeTestPartition(t, resultidentity.PartitionProduction, "")
		evidence := cutoverQualificationEvidence(t, '1')
		generationA := newCutoverQualificationRuntime(t, pool, "generation-a-cold", partition, evidence, databaseA)
		generationB := newCutoverQualificationRuntime(t, pool, "generation-b-cold", partition, evidence, databaseB)
		request := cutoverQualificationRequest()
		request.Limit = 2
		type queryResponse struct {
			result dataquery.Result
			err    error
		}
		responses := make(chan queryResponse, 2)
		go func() {
			result, err := generationA.ExecuteDataQuery(context.Background(), request)
			responses <- queryResponse{result: result, err: err}
		}()
		go func() {
			result, err := generationB.ExecuteDataQuery(context.Background(), request)
			responses <- queryResponse{result: result, err: err}
		}()

		// If either request fails before execution or joins the other generation's
		// flight, synctest still reaches quiescence and this assertion fails directly.
		synctest.Wait()
		if got := databaseA.queries.Load(); got != 1 {
			t.Errorf("generation A physical executions = %d, want 1; generation A did not reach its independent cold execution owner", got)
		}
		if got := databaseB.queries.Load(); got != 1 {
			t.Errorf("generation B physical executions = %d, want 1; generation B joined another generation or failed before physical execution", got)
		}

		close(release)
		synctest.Wait()
		require.Len(t, responses, 2, "both generation owners must complete after the execution barrier is released")
		for range 2 {
			response := <-responses
			require.NoError(t, response.err)
			require.Equal(t, dataquery.CacheMiss, response.result.CacheOutcome)
			require.Equal(t, int64(303), cutoverQualificationValue(t, response.result))
		}
		require.NoError(t, generationA.Close())
		require.NoError(t, generationB.Close())
		require.NoError(t, pool.Close())
	})
}

func TestRuntimeSameGenerationCutoverQualificationCoalescesColdMiss(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool, err := resultcache.New(resultcache.Limits{
			RuntimeEntries: 8, RuntimeBytes: 2 << 20, NodeEntries: 8, NodeBytes: 2 << 20,
		})
		require.NoError(t, err)
		release := make(chan struct{})
		database := &cutoverQualificationDatabase{value: 601, release: release}
		runtime := newCutoverQualificationRuntime(
			t, pool, "same-generation", materializeTestPartition(t, resultidentity.PartitionProduction, ""),
			cutoverQualificationEvidence(t, '1'), database,
		)
		request := cutoverQualificationRequest()
		responses := make(chan dataquery.Result, 2)
		errs := make(chan error, 2)
		go func() {
			result, err := runtime.ExecuteDataQuery(context.Background(), request)
			responses <- result
			errs <- err
		}()
		synctest.Wait()
		if got := database.queries.Load(); got != 1 {
			t.Errorf("initial same-generation physical executions = %d, want 1; the request did not reach its execution owner", got)
		}
		go func() {
			result, err := runtime.ExecuteDataQuery(context.Background(), request)
			responses <- result
			errs <- err
		}()
		synctest.Wait()
		if got := database.queries.Load(); got != 1 {
			t.Errorf("physical executions after same-generation join = %d, want 1; the second request did not coalesce", got)
		}
		close(release)
		synctest.Wait()
		require.Len(t, responses, 2, "both same-generation callers must complete after the owner is released")
		require.Len(t, errs, 2, "both same-generation callers must report completion")
		outcomes := map[string]int{}
		for range 2 {
			require.NoError(t, <-errs)
			result := <-responses
			require.Equal(t, int64(601), cutoverQualificationValue(t, result))
			outcomes[result.CacheOutcome]++
		}
		require.Equal(t, map[string]int{dataquery.CacheMiss: 1, dataquery.CacheCoalesced: 1}, outcomes)
		require.NoError(t, runtime.Close())
		require.NoError(t, pool.Close())
	})
}

type cutoverQualificationDatabase struct {
	cacheRuntimeDatabase
	value   int64
	queries atomic.Int32
	release <-chan struct{}
}

func (d *cutoverQualificationDatabase) QueryArrow(ctx context.Context, plan semanticquery.Plan, sink arrowquery.Sink) error {
	d.queries.Add(1)
	if d.release != nil {
		select {
		case <-d.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return writeTestRowsArrow(ctx, plan, semanticquery.Rows{{"id": d.value}}, sink)
}

func newCutoverQualificationRuntime(
	t testing.TB,
	pool *resultcache.Pool,
	generation string,
	partition resultidentity.Partition,
	evidence resultidentity.Evidence,
	database *cutoverQualificationDatabase,
) *Runtime {
	t.Helper()
	stable, err := pool.OpenSharedScope(resultcache.ScopeID{RuntimeID: "cutover:" + string(partition.Canonical())})
	require.NoError(t, err)
	bytes, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "cutover-generation:" + generation})
	if err != nil {
		_ = stable.Close()
		require.NoError(t, err)
	}
	cache := newQueryResultCacheWithScopes(stable, bytes)
	cache.ownScope()
	return activatedCacheRuntime(t, &Runtime{
		modelID: "sales", model: cutoverQualificationModel(), db: database,
		queryCache: cache, resultPartition: partition, dependencyEvidence: evidence,
	})
}

func cutoverQualificationModel() *semanticmodel.Model {
	return &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
		"orders": {Columns: map[string]semanticmodel.ModelColumn{"id": {Name: "id", Datatype: semanticmodel.DataTypeInteger}}},
	}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}}
}

func cutoverQualificationEvidence(t testing.TB, revision byte) resultidentity.Evidence {
	t.Helper()
	evidence, err := resultidentity.NewEvidence(resultidentity.EvidenceInput{
		SemanticModelID: "semantic:sales", SemanticModelDigest: materializeTestDigest('a'),
		DatasetRelations: []resultidentity.DatasetRelation{{
			Dataset: "orders", Relation: resultidentity.RelationRevision{
				RelationID: "model:orders", RevisionDigest: materializeTestDigest(revision),
			},
		}},
		BindingFingerprint: materializeTestDigest('c'), RuntimeDigest: materializeTestDigest('d'),
		CapabilityDigest: materializeTestDigest('e'),
	})
	require.NoError(t, err)
	return evidence
}

func cutoverQualificationRequest() dataquery.Query {
	return dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardRows,
		ProjectID: "project:test", EffectivePolicyFingerprint: materializeTestDigest('9'),
		ModelID: "sales", Kind: dataquery.KindSemanticRows, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 1,
	}
}

func cutoverCandidateRequest(request dataquery.Query, candidateID string) dataquery.Query {
	request.CandidateID = candidateID
	return request
}

func executeCutoverQualificationQuery(t testing.TB, runtime *Runtime, request dataquery.Query) dataquery.Result {
	t.Helper()
	result, err := runtime.ExecuteDataQuery(context.Background(), request)
	require.NoError(t, err)
	return result
}

func cutoverQualificationValue(t testing.TB, result dataquery.Result) int64 {
	t.Helper()
	require.Len(t, result.Rows, 1)
	value, ok := result.Rows[0]["id"].(int64)
	require.True(t, ok, "cutover result value = %#v", result.Rows[0]["id"])
	return value
}

func assertCutoverQualificationLease(t testing.TB, lease *resultcache.EntryLease, want int64) {
	t.Helper()
	rows, err := arrowdecode.DecodeRows(lease.Data())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, want, rows[0]["id"])
}

func assertCutoverQualificationAccounting(t testing.TB, pool *resultcache.Pool, nodeEntries int, nodeBytes int64) {
	t.Helper()
	stats := pool.Stats()
	require.LessOrEqual(t, stats.Entries, nodeEntries)
	require.LessOrEqual(t, stats.Bytes, nodeBytes)
	require.LessOrEqual(t, stats.Stable.Entries, nodeEntries)
	require.LessOrEqual(t, stats.Stable.Bytes, nodeBytes)
	require.Equal(t, stats.Stable.Entries, stats.Stable.ArrowHolds)
}
