package materialize

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/flidai/leapview/internal/analytics/arrowresult"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/resultcache"
)

var localCacheID atomic.Uint64

// queryResultCache retains the materialization-specific key contract while the
// resultcache scope owns retention, hierarchy, invalidation, and coalescing.
type queryResultCache struct {
	mu           sync.Mutex
	namespace    string
	pool         *resultcache.Pool
	scope        *resultcache.Scope
	owned        bool
	scopeOwned   bool
	capacity     int
	maxBytes     int64
	currentBytes int64
	generation   uint64
}

type arrowQueryExecution struct {
	data     *arrowresult.Result
	metadata resultcache.Metadata
	summary  dataquery.Result
}

func (c *queryResultCache) executeArrow(ctx context.Context, request dataquery.Query, execute func() (arrowQueryExecution, error)) (dataquery.Result, error) {
	key, generation, err := c.cacheKey(request)
	if err != nil {
		return dataquery.Result{}, err
	}
	if cached, ok, err := c.getArrow(ctx, request, key); err != nil || ok {
		return cached, err
	}
	var ownerSummary dataquery.Result
	flight, status, err := c.scope.CoalesceArrow(ctx, fmt.Sprintf("arrow-query:%d:%s", generation, key), func() (resultcache.ArrowFlightValue, error) {
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
		execution, executeErr := execute()
		ownerSummary = execution.summary
		if execution.data != nil {
			defer execution.data.Release()
		}
		if ownerErr := ctx.Err(); ownerErr != nil {
			return resultcache.ArrowFlightValue{}, canceledQueryCacheFlightError{err: ownerErr}
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
		c.scope.StoreArrow(key, resultcache.Token(generation), execution.data, execution.metadata)
		c.syncStats()
		return resultcache.ArrowFlightValue{Data: base, Metadata: execution.metadata}, nil
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
	result, err := decodeArrowQueryResult(request, flight.Data(), flight.Metadata(), ownerSummary)
	if err != nil {
		return dataquery.Result{}, err
	}
	result.CacheOutcome = outcome
	return result, nil
}

func (c *queryResultCache) getArrow(ctx context.Context, request dataquery.Query, key string) (dataquery.Result, bool, error) {
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
	result, err := decodeArrowQueryResult(request, entry.Data(), entry.Metadata(), dataquery.Result{CacheOutcome: dataquery.CacheHit})
	if err != nil {
		return dataquery.Result{}, false, err
	}
	result.CacheOutcome = dataquery.CacheHit
	c.syncStats()
	return result, true, nil
}

func newQueryResultCache(capacity int, namespace string) *queryResultCache {
	return newQueryResultCacheWithLimits(capacity, 64<<20, namespace)
}

func newQueryResultCacheWithLimits(capacity int, maxBytes int64, namespace string) *queryResultCache {
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
	return &queryResultCache{namespace: namespace, pool: pool, scope: scope, owned: true, capacity: capacity, maxBytes: maxBytes}
}

func newQueryResultCacheWithScope(scope *resultcache.Scope, namespace string) *queryResultCache {
	return &queryResultCache{namespace: namespace, scope: scope}
}

func (c *queryResultCache) ownScope() {
	if c != nil && !c.owned {
		c.scopeOwned = true
	}
}

func (c *queryResultCache) coalesce(ctx context.Context, key string, execute func() (any, error)) (any, bool, error) {
	return c.scope.Coalesce(ctx, "bundle:"+key, func() (any, error) {
		result, err := execute()
		if ownerErr := ctx.Err(); ownerErr != nil {
			return nil, canceledQueryCacheFlightError{err: ownerErr}
		}
		return result, err
	})
}

func (c *queryResultCache) lookupBytes(key string) ([]byte, bool, error) {
	if c == nil || c.scope == nil {
		return nil, false, fmt.Errorf("result cache scope is required")
	}
	value, _, ok, err := c.scope.LookupBytes(key)
	if err == nil {
		c.syncStats()
	}
	return value, ok, err
}

func (c *queryResultCache) storeBytes(key string, value []byte) resultcache.StoreOutcome {
	if c == nil || c.scope == nil {
		return resultcache.StoreClosed
	}
	_, token, _, err := c.scope.LookupBytes(key)
	if err != nil {
		return resultcache.StoreClosed
	}
	outcome := c.scope.StoreBytes(key, token, value)
	c.syncStats()
	return outcome
}

func (c *queryResultCache) coalesceBytes(ctx context.Context, key string, execute func() error) (bool, error) {
	if c == nil || c.scope == nil {
		return false, fmt.Errorf("result cache scope is required")
	}
	_, shared, err := c.scope.Coalesce(ctx, "immutable-bytes:"+key, func() (any, error) {
		return struct{}{}, execute()
	})
	return shared, err
}

func (c *queryResultCache) lookupArrow(ctx context.Context, request dataquery.Query) (dataquery.Result, string, uint64, bool, error) {
	key, generation, err := c.cacheKey(request)
	if err != nil {
		return dataquery.Result{}, "", 0, false, err
	}
	result, hit, err := c.getArrow(ctx, request, key)
	return result, key, generation, hit, err
}

func (c *queryResultCache) cacheKey(request dataquery.Query) (string, uint64, error) {
	spatialTileGenerationVersion := 0
	if request.SpatialTile != nil || request.SpatialTileBudget != nil {
		// Bump whenever MVT encoding or promoted feature identity changes so an
		// active cache can never serve bytes from an older tile contract.
		spatialTileGenerationVersion = 5
	}
	keyBytes, err := json.Marshal(queryResultCacheKey{
		Namespace:                    c.namespace,
		CandidateID:                  request.CandidateID,
		EffectivePolicyFingerprint:   request.EffectivePolicyFingerprint,
		Operation:                    request.Operation,
		ModelID:                      request.ModelID,
		Kind:                         request.Kind,
		Target:                       request.Target,
		Fields:                       request.Fields,
		Metrics:                      request.Metrics,
		AuthorizationFields:          request.AuthorizationFields,
		Value:                        request.Value,
		Time:                         request.Time,
		Filters:                      request.Filters,
		Sort:                         request.Sort,
		ColumnMasks:                  request.ColumnMasks,
		Offset:                       request.Offset,
		Limit:                        request.Limit,
		BinCount:                     request.BinCount,
		IncludeTotal:                 request.IncludeTotal,
		SpatialTile:                  request.SpatialTile,
		SpatialTileBudget:            request.SpatialTileBudget,
		SpatialTileGenerationVersion: spatialTileGenerationVersion,
		SpatialMetadata:              request.SpatialMetadata,
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

type canceledQueryCacheFlightError struct{ err error }

func (e canceledQueryCacheFlightError) Error() string { return e.err.Error() }
func (e canceledQueryCacheFlightError) Unwrap() error { return e.err }

type queryResultCacheKey struct {
	Namespace                    string
	CandidateID                  string
	EffectivePolicyFingerprint   string
	Operation                    string
	ModelID                      string
	Kind                         dataquery.Kind
	Target                       string
	Fields                       []dataquery.Field
	Metrics                      []dataquery.Field
	AuthorizationFields          []dataquery.Field
	Value                        dataquery.Field
	Time                         dataquery.Time
	Filters                      []dataquery.Filter
	Sort                         []dataquery.Sort
	ColumnMasks                  []dataquery.ColumnMask
	Offset                       int
	Limit                        int
	BinCount                     int
	IncludeTotal                 bool
	SpatialTile                  *dataquery.SpatialTile
	SpatialTileBudget            *dataquery.SpatialTileBudget
	SpatialTileGenerationVersion int
	SpatialMetadata              *dataquery.SpatialMetadata
}

func (c *queryResultCache) clear() {
	c.scope.Invalidate()
	c.mu.Lock()
	c.generation = uint64(c.scope.Generation())
	c.mu.Unlock()
	c.syncStats()
}

func (c *queryResultCache) close() error {
	if c == nil {
		return nil
	}
	if c.scopeOwned {
		return c.scope.Close()
	}
	if !c.owned {
		return nil
	}
	return errors.Join(c.scope.Close(), c.pool.Close())
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
