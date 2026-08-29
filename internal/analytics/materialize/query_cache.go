package materialize

import (
	"context"
	"errors"
	"fmt"
	"sync"

	resultcacheidentity "github.com/flidai/leapview/internal/analytics/cache"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/flidai/leapview/pkg/arrowresult"
)

// queryResultCache retains the materialization-specific key contract while the
// resultcache scope owns retention, hierarchy, invalidation, and coalescing.
type queryResultCache struct {
	mu           sync.Mutex
	pool         *resultcache.Pool
	scope        *resultcache.Scope
	owned        bool
	scopeOwned   bool
	capacity     int
	maxBytes     int64
	currentBytes int64
	generation   uint64
	partition    resultidentity.Partition
}

type arrowQueryExecution struct {
	data     *arrowresult.Result
	metadata resultcache.Metadata
	summary  dataquery.Result
}

// executeArrowWithDependency is the production path. A query is reusable only
// after planning has supplied immutable dependency evidence; the dependency
// digest is therefore part of both L1 lookup and stampede-coalescing identity.
func (c *queryResultCache) executeArrowWithDependency(ctx context.Context, request dataquery.Query, dependency resultidentity.Dependency, execute func() (arrowQueryExecution, error)) (dataquery.Result, error) {
	key, generation, err := c.cacheKeyWithDependency(request, dependency)
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

func newQueryResultCacheWithPartition(capacity int, maxBytes int64, partition resultidentity.Partition) *queryResultCache {
	if capacity <= 0 {
		capacity = 1
	}
	if maxBytes <= 0 {
		maxBytes = 1
	}
	if partition.Version() == 0 {
		panic("typed query cache partition is required")
	}
	pool, err := resultcache.New(resultcache.Limits{PartitionEntries: capacity, PartitionBytes: maxBytes, NodeEntries: capacity, NodeBytes: maxBytes})
	if err != nil {
		panic(err)
	}
	id := fmt.Sprintf("cache-%p", pool)
	scope, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: id, PartitionID: resultcacheidentity.PartitionIdentity(partition)})
	if err != nil {
		panic(err)
	}
	return &queryResultCache{pool: pool, scope: scope, owned: true, capacity: capacity, maxBytes: maxBytes, partition: partition}
}

func newQueryResultCacheWithScopeAndPartition(scope *resultcache.Scope, partition resultidentity.Partition) *queryResultCache {
	if scope == nil || partition.Version() == 0 {
		panic("typed query cache scope and partition are required")
	}
	return &queryResultCache{scope: scope, partition: partition}
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

func (c *queryResultCache) lookupArrowWithDependency(ctx context.Context, request dataquery.Query, dependency resultidentity.Dependency) (dataquery.Result, string, uint64, bool, error) {
	key, generation, err := c.cacheKeyWithDependency(request, dependency)
	if err != nil {
		return dataquery.Result{}, "", 0, false, err
	}
	result, hit, err := c.getArrow(ctx, request, key)
	return result, key, generation, hit, err
}

func (c *queryResultCache) cacheKeyWithDependency(request dataquery.Query, dependency resultidentity.Dependency) (string, uint64, error) {
	if dependency.Version() == 0 || dependency.Digest() == "" {
		return "", 0, fmt.Errorf("governed query dependency evidence is unavailable")
	}
	queryDigest, err := resultcacheidentity.CanonicalQueryDigest(request)
	if err != nil {
		return "", 0, err
	}
	key, err := resultcacheidentity.NewKey(resultcacheidentity.KeyInput{Partition: c.partition, Dependency: dependency, EffectivePolicyFingerprint: request.EffectivePolicyFingerprint, CanonicalQueryDigest: queryDigest})
	if err != nil {
		return "", 0, err
	}
	generation := uint64(c.scope.Generation())
	c.mu.Lock()
	c.generation = generation
	c.mu.Unlock()
	return key.Digest(), generation, nil
}

type canceledQueryCacheFlightError struct{ err error }

func (e canceledQueryCacheFlightError) Error() string { return e.err.Error() }
func (e canceledQueryCacheFlightError) Unwrap() error { return e.err }

func (c *queryResultCache) clear() {
	c.scope.Clear()
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
