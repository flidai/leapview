package materialize

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/pkg/arrowresult"
)

var localCacheID atomic.Uint64

// queryResultCache retains the materialization-specific key contract while the
// stable result scope owns retention and invalidation, the byte scope owns
// generation-bound immutable values, and the execution scope owns flights.
type queryResultCache struct {
	mu             sync.Mutex
	pool           *resultcache.Pool
	scope          *resultcache.Scope
	byteScope      *resultcache.Scope
	execution      *resultcache.ExecutionScope
	owned          bool
	scopeOwned     bool
	executionOwned bool
	capacity       int
	maxBytes       int64
	currentBytes   int64
	generation     uint64
}

type arrowQueryExecution struct {
	data     *arrowresult.Result
	metadata resultcache.Metadata
	summary  dataquery.Result
}

func (c *queryResultCache) executeArrow(ctx context.Context, request dataquery.Query, partition resultidentity.Partition, dependency resultidentity.Dependency, diagnosticsSQL string, execute func(context.Context) (arrowQueryExecution, error)) (dataquery.Result, error) {
	key, generation, err := c.cacheKey(request, partition, dependency)
	if err != nil {
		return dataquery.Result{}, err
	}
	if cached, ok, err := c.getArrow(ctx, request, key, diagnosticsSQL); err != nil || ok {
		return cached, err
	}
	var ownerSummary dataquery.Result
	flight, status, err := c.execution.CoalesceArrow(ctx, fmt.Sprintf("arrow-query:%d:%s", generation, key), func(flightCtx context.Context) (resultcache.ArrowFlightValue, error) {
		if entry, _, ok, lookupErr := c.scope.LookupArrow(key); lookupErr != nil {
			return resultcache.ArrowFlightValue{}, lookupErr
		} else if ok {
			base, acquireErr := entry.Data().Acquire()
			metadata := entry.Metadata()
			entry.Release()
			if acquireErr != nil {
				return resultcache.ArrowFlightValue{}, acquireErr
			}
			return resultcache.ArrowFlightValue{Data: base, Metadata: metadata, Cached: true}, nil
		}
		execution, executeErr := execute(flightCtx)
		ownerSummary = execution.summary
		if execution.data != nil {
			defer execution.data.Release()
		}
		if ownerErr := flightCtx.Err(); ownerErr != nil {
			return resultcache.ArrowFlightValue{}, ownerErr
		}
		if executeErr != nil {
			return resultcache.ArrowFlightValue{}, executeErr
		}
		if execution.data == nil {
			return resultcache.ArrowFlightValue{}, fmt.Errorf("Arrow query execution returned no data")
		}
		base, acquireErr := execution.data.Acquire()
		if acquireErr != nil {
			return resultcache.ArrowFlightValue{}, acquireErr
		}
		// Physical SQL is generation-bound diagnostic state. Preserve it on the
		// live execution response, but never retain it in the stable partition
		// cache or its coalesced value.
		cacheMetadata := execution.metadata
		cacheMetadata.SQL = ""
		c.scope.StoreArrow(key, resultcache.Token(generation), execution.data, cacheMetadata)
		c.syncStats()
		return resultcache.ArrowFlightValue{Data: base, Metadata: cacheMetadata}, nil
	})
	if err != nil {
		return dataquery.Result{}, err
	}
	defer flight.Release()
	outcome := dataquery.CacheMiss
	if flight.Cached() {
		outcome = dataquery.CacheHit
	} else if !status.Owner {
		outcome = dataquery.CacheCoalesced
	}
	if flight.Cached() || !status.Owner {
		if budget, found := dataquery.ResultBudgetFromContext(ctx); found {
			if err := budget.ConsumeSize(int(flight.Data().Rows()), flight.Data().Bytes()); err != nil {
				return dataquery.Result{}, err
			}
		}
	}
	metadata := flight.Metadata()
	metadata.SQL = diagnosticsSQL
	result, err := decodeArrowQueryResult(request, flight.Data(), metadata, ownerSummary)
	if err != nil {
		return dataquery.Result{}, err
	}
	result.CacheOutcome = outcome
	return result, nil
}

func (c *queryResultCache) getArrow(ctx context.Context, request dataquery.Query, key, diagnosticsSQL string) (dataquery.Result, bool, error) {
	entry, _, ok, err := c.scope.LookupArrow(key)
	if err != nil || !ok {
		return dataquery.Result{}, false, err
	}
	defer entry.Release()
	if budget, found := dataquery.ResultBudgetFromContext(ctx); found {
		if err := budget.ConsumeSize(int(entry.Data().Rows()), entry.Data().Bytes()); err != nil {
			return dataquery.Result{}, false, err
		}
	}
	metadata := entry.Metadata()
	metadata.SQL = diagnosticsSQL
	result, err := decodeArrowQueryResult(request, entry.Data(), metadata, dataquery.Result{CacheOutcome: dataquery.CacheHit})
	if err != nil {
		return dataquery.Result{}, false, err
	}
	result.CacheOutcome = dataquery.CacheHit
	c.syncStats()
	return result, true, nil
}

func newQueryResultCache(capacity int) *queryResultCache {
	return newQueryResultCacheWithLimits(capacity, 64<<20)
}

func newQueryResultCacheWithLimits(capacity int, maxBytes int64) *queryResultCache {
	if capacity <= 0 {
		capacity = 1
	}
	if maxBytes <= 0 {
		maxBytes = 1
	}
	pool, err := resultcache.New(resultcache.Limits{RuntimeEntries: capacity, RuntimeBytes: maxBytes, NodeEntries: capacity, NodeBytes: maxBytes})
	if err != nil {
		panic(err)
	}
	id := fmt.Sprintf("local-%d", localCacheID.Add(1))
	scope, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: id})
	if err != nil {
		panic(err)
	}
	return &queryResultCache{pool: pool, scope: scope, byteScope: scope, execution: resultcache.NewExecutionScope(), owned: true, executionOwned: true, capacity: capacity, maxBytes: maxBytes}
}

func newQueryResultCacheWithScopes(scope, byteScope *resultcache.Scope) *queryResultCache {
	return newQueryResultCacheWithExecutionScope(scope, byteScope, resultcache.NewExecutionScope(), true)
}

func newQueryResultCacheWithExecutionScope(scope, byteScope *resultcache.Scope, execution *resultcache.ExecutionScope, executionOwned bool) *queryResultCache {
	if byteScope == nil {
		byteScope = scope
	}
	return &queryResultCache{scope: scope, byteScope: byteScope, execution: execution, executionOwned: executionOwned}
}

func (c *queryResultCache) ownScope() {
	if c != nil && !c.owned {
		c.scopeOwned = true
	}
}

func (c *queryResultCache) coalesce(ctx context.Context, key string, execute func(context.Context) (any, error)) (any, bool, error) {
	return c.execution.Coalesce(ctx, "bundle:"+key, execute)
}

func (c *queryResultCache) lookupBytes(key string) ([]byte, bool, error) {
	if c == nil || c.byteScope == nil {
		return nil, false, fmt.Errorf("result cache scope is required")
	}
	value, _, ok, err := c.byteScope.LookupBytes(key)
	if err == nil {
		c.syncStats()
	}
	return value, ok, err
}

func (c *queryResultCache) storeBytes(key string, value []byte) resultcache.StoreOutcome {
	if c == nil || c.byteScope == nil {
		return resultcache.StoreClosed
	}
	_, token, _, err := c.byteScope.LookupBytes(key)
	if err != nil {
		return resultcache.StoreClosed
	}
	outcome := c.byteScope.StoreBytes(key, token, value)
	c.syncStats()
	return outcome
}

func (c *queryResultCache) coalesceBytes(ctx context.Context, key string, execute func(context.Context) error) (bool, error) {
	if c == nil || c.byteScope == nil {
		return false, fmt.Errorf("result cache scope is required")
	}
	_, shared, err := c.execution.Coalesce(ctx, "immutable-bytes:"+key, func(executionCtx context.Context) (any, error) {
		return struct{}{}, execute(executionCtx)
	})
	return shared, err
}

func (c *queryResultCache) lookupArrow(ctx context.Context, request dataquery.Query, partition resultidentity.Partition, dependency resultidentity.Dependency, diagnosticsSQL string) (dataquery.Result, string, uint64, bool, error) {
	key, generation, err := c.cacheKey(request, partition, dependency)
	if err != nil {
		return dataquery.Result{}, "", 0, false, err
	}
	result, hit, err := c.getArrow(ctx, request, key, diagnosticsSQL)
	return result, key, generation, hit, err
}

func (c *queryResultCache) cacheKey(request dataquery.Query, partition resultidentity.Partition, dependency resultidentity.Dependency) (string, uint64, error) {
	if !queryCacheIdentityAvailable(request, partition, dependency) {
		return "", 0, fmt.Errorf("complete query result cache identity is required")
	}
	spatialTileGenerationVersion := 0
	if request.SpatialTile != nil || request.SpatialTileBudget != nil {
		// Bump whenever MVT encoding or promoted feature identity changes so an
		// active cache can never serve bytes from an older tile contract.
		spatialTileGenerationVersion = 5
	}
	partitionCanonical := partition.Canonical()
	keyBytes, err := json.Marshal(queryResultCacheKey{
		Version:                    resultidentity.CacheKeyFormatVersion,
		Partition:                  json.RawMessage(partitionCanonical),
		DependencyDigest:           dependency.Digest(),
		EffectivePolicyFingerprint: request.EffectivePolicyFingerprint,
		Query: governedQueryCacheKey{
			Operation: request.Operation, ModelID: request.ModelID, Kind: request.Kind, Target: request.Target,
			Fields: request.Fields, Metrics: request.Metrics, AuthorizationFields: request.AuthorizationFields,
			Value: request.Value, Time: request.Time, Filters: request.Filters, Sort: request.Sort,
			ColumnMasks: request.ColumnMasks, Offset: request.Offset, Limit: request.Limit,
			BinCount: request.BinCount, Histogram: request.Histogram, Distribution: request.Distribution,
			IncludeTotal: request.IncludeTotal, SpatialTile: request.SpatialTile,
			SpatialTileBudget:            request.SpatialTileBudget,
			SpatialTileGenerationVersion: spatialTileGenerationVersion, SpatialMetadata: request.SpatialMetadata,
		},
	})
	if err != nil {
		return "", 0, fmt.Errorf("encode governed query cache key: %w", err)
	}
	generation := uint64(c.scope.Generation())
	c.mu.Lock()
	c.generation = generation
	c.mu.Unlock()
	return string(keyBytes), generation, nil
}

func queryCacheIdentityAvailable(request dataquery.Query, partition resultidentity.Partition, dependency resultidentity.Dependency) bool {
	if partition.Version() != resultidentity.PartitionVersion || dependency.Version() != resultidentity.DependencyVersion {
		return false
	}
	if platformdigest.ValidateSHA256Identity(dependency.Digest()) != nil ||
		platformdigest.ValidateSHA256Identity(request.EffectivePolicyFingerprint) != nil {
		return false
	}
	if partition.ProjectID() != request.ProjectID && request.ProjectID != "" {
		return false
	}
	switch partition.Kind() {
	case resultidentity.PartitionProduction:
		return request.CandidateID == ""
	case resultidentity.PartitionCandidate:
		return request.CandidateID == partition.CandidateID()
	default:
		return false
	}
}

type queryResultCacheKey struct {
	Version                    int                   `json:"version"`
	Partition                  json.RawMessage       `json:"partition"`
	DependencyDigest           string                `json:"dependencyDigest"`
	EffectivePolicyFingerprint string                `json:"effectivePolicyFingerprint"`
	Query                      governedQueryCacheKey `json:"query"`
}

type governedQueryCacheKey struct {
	Operation                    string                         `json:"operation"`
	ModelID                      string                         `json:"modelId"`
	Kind                         dataquery.Kind                 `json:"kind"`
	Target                       string                         `json:"target"`
	Fields                       []dataquery.Field              `json:"fields,omitempty"`
	Metrics                      []dataquery.Field              `json:"metrics,omitempty"`
	AuthorizationFields          []dataquery.Field              `json:"authorizationFields,omitempty"`
	Value                        dataquery.Field                `json:"value"`
	Time                         dataquery.Time                 `json:"time"`
	Filters                      []dataquery.Filter             `json:"filters,omitempty"`
	Sort                         []dataquery.Sort               `json:"sort,omitempty"`
	ColumnMasks                  []dataquery.ColumnMask         `json:"columnMasks,omitempty"`
	Offset                       int                            `json:"offset,omitempty"`
	Limit                        int                            `json:"limit,omitempty"`
	BinCount                     int                            `json:"binCount,omitempty"`
	Histogram                    *dataquery.HistogramOptions    `json:"histogram,omitempty"`
	Distribution                 *dataquery.DistributionOptions `json:"distribution,omitempty"`
	IncludeTotal                 bool                           `json:"includeTotal,omitempty"`
	SpatialTile                  *dataquery.SpatialTile         `json:"spatialTile,omitempty"`
	SpatialTileBudget            *dataquery.SpatialTileBudget   `json:"spatialTileBudget,omitempty"`
	SpatialTileGenerationVersion int                            `json:"spatialTileGenerationVersion,omitempty"`
	SpatialMetadata              *dataquery.SpatialMetadata     `json:"spatialMetadata,omitempty"`
}

func (c *queryResultCache) clear() {
	c.scope.Invalidate()
	if c.byteScope != nil && c.byteScope != c.scope {
		c.byteScope.Invalidate()
	}
	c.mu.Lock()
	c.generation = uint64(c.scope.Generation())
	c.mu.Unlock()
	c.syncStats()
}

func (c *queryResultCache) close() error {
	if c == nil {
		return nil
	}
	var executionErr error
	if c.executionOwned && c.execution != nil {
		executionErr = c.execution.Close()
	}
	if c.scopeOwned {
		var byteErr error
		if c.byteScope != nil && c.byteScope != c.scope {
			byteErr = c.byteScope.Close()
		}
		return errors.Join(executionErr, byteErr, c.scope.Close())
	}
	if !c.owned {
		return executionErr
	}
	return errors.Join(executionErr, c.scope.Close(), c.pool.Close())
}

func (c *queryResultCache) syncStats() {
	stats := c.scope.Stats()
	c.mu.Lock()
	c.currentBytes = stats.Bytes
	c.mu.Unlock()
}

func cloneDataQueryResult(result dataquery.Result) dataquery.Result {
	clone := result
	clone.Columns = append([]dataquery.Column{}, result.Columns...)
	clone.Rows = make([]dataquery.Row, len(result.Rows))
	for index, row := range result.Rows {
		clone.Rows[index] = make(dataquery.Row, len(row))
		for key, value := range row {
			clone.Rows[index][key] = cloneDataQueryValue(value)
		}
	}
	clone.Warnings = append([]string{}, result.Warnings...)
	return clone
}

func cloneDataQueryValue(value any) any {
	switch value := value.(type) {
	case []byte:
		return append([]byte{}, value...)
	case []any:
		clone := make([]any, len(value))
		for index := range value {
			clone[index] = cloneDataQueryValue(value[index])
		}
		return clone
	case map[string]any:
		clone := make(map[string]any, len(value))
		for key, item := range value {
			clone[key] = cloneDataQueryValue(item)
		}
		return clone
	default:
		return value
	}
}
