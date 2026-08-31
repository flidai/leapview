package materialize

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	resultcacheidentity "github.com/flidai/leapview/internal/analytics/cache"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/pkg/arrowresult"
)

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

type queryCacheAddress struct {
	key        string
	family     resultcache.QueryFamily
	generation uint64
}

func (c *queryResultCache) executeArrow(ctx context.Context, request dataquery.Query, partition resultidentity.Partition, dependency resultidentity.Dependency, diagnosticsSQL string, observationStarted time.Time, execute func(context.Context) (arrowQueryExecution, error)) (dataquery.Result, error) {
	if observationStarted.IsZero() {
		observationStarted = time.Now()
	}
	address, err := c.cacheAddress(request, partition, dependency)
	if err != nil {
		observeTypedCacheFinal(ctx, dataquery.CacheObservationError, time.Since(observationStarted))
		return dataquery.Result{}, err
	}
	lookupStarted := time.Now()
	cached, ok, lookup, err := c.getArrowObserved(ctx, request, address, diagnosticsSQL)
	if err != nil || ok {
		observeTypedCacheLookup(ctx, lookup, time.Since(lookupStarted))
		if err != nil {
			observeTypedCacheFinal(ctx, dataquery.CacheObservationError, time.Since(observationStarted))
		} else {
			observeTypedCacheFinalWithSource(ctx, dataquery.CacheObservationHit, lookup.HitSource, time.Since(observationStarted))
		}
		return cached, err
	}
	firstLookupDuration := time.Since(lookupStarted)
	observeTypedCacheLookup(ctx, lookup, firstLookupDuration)
	var ownerSummary dataquery.Result
	flight, status, err := c.execution.CoalesceArrow(ctx, fmt.Sprintf("arrow-query:%d:%s", address.generation, address.key), func(flightCtx context.Context) (resultcache.ArrowFlightValue, error) {
		if entry, _, ok, observed, lookupErr := c.scope.LookupArrowObserved(address.key, address.family); lookupErr != nil {
			return resultcache.ArrowFlightValue{}, lookupErr
		} else if ok {
			base, acquireErr := entry.Data().Acquire()
			metadata := entry.Metadata()
			entry.Release()
			if acquireErr != nil {
				return resultcache.ArrowFlightValue{}, acquireErr
			}
			return resultcache.ArrowFlightValue{Data: base, Metadata: metadata, Cached: true, HitSource: observed.HitSource}, nil
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
		storeStarted := time.Now()
		storeOutcome := c.scope.StoreArrowObserved(address.key, address.family, resultcache.Token(address.generation), execution.data, cacheMetadata)
		dataquery.ObserveCache(ctx, dataquery.CacheObservation{Phase: dataquery.CacheObservationStore, StoreOutcome: dataquery.CacheStoreOutcome(storeOutcome), Duration: time.Since(storeStarted)})
		c.syncStats()
		return resultcache.ArrowFlightValue{Data: base, Metadata: cacheMetadata}, nil
	})
	if err != nil {
		observeTypedCacheFinal(ctx, dataquery.CacheObservationError, time.Since(observationStarted))
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
				observeTypedCacheFinal(ctx, dataquery.CacheObservationError, time.Since(observationStarted))
				return dataquery.Result{}, err
			}
		}
	}
	metadata := flight.Metadata()
	metadata.SQL = diagnosticsSQL
	result, err := decodeArrowQueryResult(request, flight.Data(), metadata, ownerSummary)
	if err != nil {
		observeTypedCacheFinal(ctx, dataquery.CacheObservationError, time.Since(observationStarted))
		return dataquery.Result{}, err
	}
	result.CacheOutcome = outcome
	typedOutcome := dataquery.CacheObservationOutcome(outcome)
	if flight.Cached() {
		observeTypedCacheFinalWithSource(ctx, typedOutcome, flight.HitSource(), time.Since(observationStarted))
	} else {
		observeTypedCacheFinal(ctx, typedOutcome, time.Since(observationStarted))
	}
	return result, nil
}

func (c *queryResultCache) getArrow(ctx context.Context, request dataquery.Query, key, diagnosticsSQL string) (dataquery.Result, bool, error) {
	result, hit, _, err := c.getArrowObserved(ctx, request, queryCacheAddress{key: key}, diagnosticsSQL)
	return result, hit, err
}

func (c *queryResultCache) getArrowObserved(ctx context.Context, request dataquery.Query, address queryCacheAddress, diagnosticsSQL string) (dataquery.Result, bool, resultcache.LookupObservation, error) {
	entry, _, ok, observation, err := c.scope.LookupArrowObserved(address.key, address.family)
	if err != nil || !ok {
		return dataquery.Result{}, false, observation, err
	}
	defer entry.Release()
	if budget, found := dataquery.ResultBudgetFromContext(ctx); found {
		if err := budget.ConsumeSize(int(entry.Data().Rows()), entry.Data().Bytes()); err != nil {
			return dataquery.Result{}, false, observation, err
		}
	}
	metadata := entry.Metadata()
	metadata.SQL = diagnosticsSQL
	result, err := decodeArrowQueryResult(request, entry.Data(), metadata, dataquery.Result{CacheOutcome: dataquery.CacheHit})
	if err != nil {
		return dataquery.Result{}, false, observation, err
	}
	result.CacheOutcome = dataquery.CacheHit
	c.syncStats()
	return result, true, observation, nil
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
	pool, err := resultcache.New(resultcache.Limits{PartitionEntries: capacity, PartitionBytes: maxBytes, NodeEntries: capacity, NodeBytes: maxBytes})
	if err != nil {
		panic(err)
	}
	id := fmt.Sprintf("cache-%p", pool)
	scope, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: id, PartitionID: "local:" + id})
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

func (c *queryResultCache) lookupArrow(ctx context.Context, request dataquery.Query, partition resultidentity.Partition, dependency resultidentity.Dependency, diagnosticsSQL string) (dataquery.Result, queryCacheAddress, bool, resultcache.LookupObservation, error) {
	address, err := c.cacheAddress(request, partition, dependency)
	if err != nil {
		return dataquery.Result{}, queryCacheAddress{}, false, resultcache.LookupObservation{}, err
	}
	result, hit, observation, err := c.getArrowObserved(ctx, request, address, diagnosticsSQL)
	return result, address, hit, observation, err
}

func (c *queryResultCache) cacheKey(request dataquery.Query, partition resultidentity.Partition, dependency resultidentity.Dependency) (string, uint64, error) {
	address, err := c.cacheAddress(request, partition, dependency)
	return address.key, address.generation, err
}

func (c *queryResultCache) cacheAddress(request dataquery.Query, partition resultidentity.Partition, dependency resultidentity.Dependency) (queryCacheAddress, error) {
	if !queryCacheIdentityAvailable(request, partition, dependency) {
		return queryCacheAddress{}, fmt.Errorf("complete query result cache identity is required")
	}
	queryDigest, err := resultcacheidentity.CanonicalQueryDigest(request)
	if err != nil {
		return queryCacheAddress{}, err
	}
	key, err := resultcacheidentity.NewKey(resultcacheidentity.KeyInput{
		Partition: partition, Dependency: dependency,
		EffectivePolicyFingerprint: request.EffectivePolicyFingerprint,
		CanonicalQueryDigest:       queryDigest,
	})
	if err != nil {
		return queryCacheAddress{}, err
	}
	familyBytes, err := json.Marshal(queryResultCacheFamily{
		Version:                    key.Version(),
		Partition:                  json.RawMessage(key.Partition().Canonical()),
		DependencyDigest:           key.DependencyDigest(),
		EffectivePolicyFingerprint: key.PolicyFingerprint(),
	})
	if err != nil {
		return queryCacheAddress{}, fmt.Errorf("encode governed query cache family: %w", err)
	}
	family := sha256.Sum256(familyBytes)
	generation := uint64(c.scope.Generation())
	c.mu.Lock()
	c.generation = generation
	c.mu.Unlock()
	return queryCacheAddress{key: key.Digest(), family: resultcache.QueryFamily(family), generation: generation}, nil
}

func queryCacheIdentityAvailable(request dataquery.Query, partition resultidentity.Partition, dependency resultidentity.Dependency) bool {
	return queryCacheIdentityReason(request, partition, dependency) == dataquery.CacheAdmissionReasonEligible
}

func queryCacheIdentityReason(request dataquery.Query, partition resultidentity.Partition, dependency resultidentity.Dependency) dataquery.CacheAdmissionReason {
	if partition.Version() != resultidentity.PartitionVersion {
		return dataquery.CacheAdmissionReasonPartitionInvalid
	}
	if dependency.Version() != resultidentity.DependencyVersion || platformdigest.ValidateSHA256Identity(dependency.Digest()) != nil {
		return dataquery.CacheAdmissionReasonDependencyInvalid
	}
	if platformdigest.ValidateSHA256Identity(request.EffectivePolicyFingerprint) != nil {
		return dataquery.CacheAdmissionReasonPolicyInvalid
	}
	if partition.ProjectID() != request.ProjectID && request.ProjectID != "" {
		return dataquery.CacheAdmissionReasonPartitionInvalid
	}
	switch partition.Kind() {
	case resultidentity.PartitionProduction:
		if request.CandidateID != "" {
			return dataquery.CacheAdmissionReasonPartitionInvalid
		}
	case resultidentity.PartitionCandidate:
		if request.CandidateID != partition.CandidateID() {
			return dataquery.CacheAdmissionReasonPartitionInvalid
		}
	default:
		return dataquery.CacheAdmissionReasonPartitionInvalid
	}
	return dataquery.CacheAdmissionReasonEligible
}

type queryResultCacheFamily struct {
	Version                    int             `json:"version"`
	Partition                  json.RawMessage `json:"partition"`
	DependencyDigest           string          `json:"dependencyDigest"`
	EffectivePolicyFingerprint string          `json:"effectivePolicyFingerprint"`
}

func observeTypedCacheLookup(ctx context.Context, observation resultcache.LookupObservation, duration time.Duration) {
	control, _ := ctx.Value(cacheObservationContextKey{}).(cacheObservationControl)
	if control.suppressLookup {
		return
	}
	dataquery.ObserveCache(ctx, dataquery.CacheObservation{
		Phase:      dataquery.CacheObservationLookup,
		MissReason: dataquery.CacheLookupMissReason(observation.MissReason),
		HitSource:  dataquery.CacheHitSource(observation.HitSource),
		Duration:   duration,
	})
}

func observeTypedCacheFinal(ctx context.Context, outcome dataquery.CacheObservationOutcome, duration time.Duration) {
	dataquery.ObserveCache(ctx, dataquery.CacheObservation{Phase: dataquery.CacheObservationFinal, Outcome: outcome, Duration: duration})
}

func observeTypedCacheFinalWithSource(ctx context.Context, outcome dataquery.CacheObservationOutcome, source resultcache.HitSource, duration time.Duration) {
	dataquery.ObserveCache(ctx, dataquery.CacheObservation{Phase: dataquery.CacheObservationFinal, Outcome: outcome, HitSource: dataquery.CacheHitSource(source), Duration: duration})
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
