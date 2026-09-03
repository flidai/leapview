package materialize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	resultcacheidentity "github.com/flidai/leapview/internal/analytics/cache"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/flidai/leapview/internal/analytics/resulttier"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/pkg/arrowresult"
)

// queryResultCache retains the materialization-specific key contract while the
// stable result scope owns retention and invalidation, the byte scope owns
// generation-bound immutable values, and the execution scope owns flights.
type queryResultCache struct {
	mu             sync.Mutex
	tierMu         sync.Mutex
	pool           *resultcache.Pool
	scope          *resultcache.Scope
	byteScope      *resultcache.Scope
	execution      *resultcache.ExecutionScope
	tier           resulttier.Tier
	tierQueue      chan tierWritebackJob
	tierCancel     context.CancelFunc
	tierWG         sync.WaitGroup
	tierStarted    bool
	tierClosed     bool
	owned          bool
	scopeOwned     bool
	executionOwned bool
	capacity       int
	maxBytes       int64
	currentBytes   int64
	generation     uint64
}

const (
	// Write-back is disposable acceleration. Keep only a small burst behind the
	// active upload so a slow object store cannot pin a large per-model Arrow
	// backlog; saturation drops new writes without affecting query correctness.
	tierWritebackQueueCapacity = 4
	tierRejectTimeout          = 5 * time.Second
)

// tierWritebackJob owns one Arrow lease while it is queued or being written.
// The Result pointer is safe to borrow through that lease, even after the
// physical execution releases its creator reference.
type tierWritebackJob struct {
	key      resultcacheidentity.Key
	result   *arrowresult.Result
	lease    *arrowresult.Lease
	metadata resultcache.Metadata
}

type arrowQueryExecution struct {
	data     *arrowresult.Result
	metadata resultcache.Metadata
	summary  dataquery.Result
}

type queryCacheAddress struct {
	key string
	// cacheKey retains the typed identity used by persistent tiers. key is the
	// digest projection required by the generation-local L1 scope.
	cacheKey   resultcacheidentity.Key
	family     resultcache.QueryFamily
	generation uint64
}

// tierArrowLookup carries the tier-owned Arrow result together with a decoded
// value used by callers that need the logical row representation (bundle
// cache resolution). The result reference remains owned by the caller and
// must be released exactly once.
type tierArrowLookup struct {
	data     *arrowresult.Result
	metadata resultcache.Metadata
	decoded  dataquery.Result
}

// executeArrow is retained for cache-unit tests and low-level callers that
// intentionally provide an authored-query identity. Runtime execution uses
// executeArrowWithPlan so the key is always derived from normalized PlanIR.
func (c *queryResultCache) executeArrow(ctx context.Context, request dataquery.Query, partition resultidentity.Partition, dependency resultidentity.Dependency, diagnosticsSQL string, observationStarted time.Time, execute func(context.Context) (arrowQueryExecution, error)) (dataquery.Result, error) {
	if observationStarted.IsZero() {
		observationStarted = time.Now()
	}
	queryDigest, err := resultcacheidentity.CanonicalQueryDigest(request)
	if err != nil {
		observeTypedCacheFinal(ctx, dataquery.CacheObservationError, time.Since(observationStarted))
		return dataquery.Result{}, err
	}
	return c.executeArrowWithDigest(ctx, request, partition, dependency, diagnosticsSQL, observationStarted, queryDigest, execute)
}

func (c *queryResultCache) executeArrowWithPlan(ctx context.Context, request dataquery.Query, partition resultidentity.Partition, dependency resultidentity.Dependency, diagnosticsSQL string, observationStarted time.Time, plan semanticquery.Plan, execute func(context.Context) (arrowQueryExecution, error)) (dataquery.Result, error) {
	if observationStarted.IsZero() {
		observationStarted = time.Now()
	}
	baseDigest, err := plan.ResultEquivalenceDigest()
	if err != nil {
		observeTypedCacheFinal(ctx, dataquery.CacheObservationError, time.Since(observationStarted))
		return dataquery.Result{}, err
	}
	queryDigest := materializeResultEquivalenceDigest(baseDigest, request)
	return c.executeArrowWithDigest(ctx, request, partition, dependency, diagnosticsSQL, observationStarted, queryDigest, execute)
}

const authorizationProjectionDomain = "flid.resultidentity.authorization-projection.v1"

// materializeResultEquivalenceDigest binds the authorization-only projection
// to a planner-owned result identity. Authorization aliases and declaration
// order are presentation details; the sorted, deduplicated field-name set is
// the complete authorization semantic that can affect the result.
func materializeResultEquivalenceDigest(baseDigest string, request dataquery.Query) string {
	if baseDigest == "" || len(request.AuthorizationFields) == 0 {
		return baseDigest
	}
	fieldSet := make(map[string]struct{}, len(request.AuthorizationFields))
	for _, field := range request.AuthorizationFields {
		name := strings.TrimSpace(field.Field)
		if name != "" {
			fieldSet[name] = struct{}{}
		}
	}
	if len(fieldSet) == 0 {
		return baseDigest
	}
	fields := make([]string, 0, len(fieldSet))
	for field := range fieldSet {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	canonical, err := json.Marshal(struct {
		Base   string   `json:"base"`
		Fields []string `json:"fields"`
	}{Base: baseDigest, Fields: fields})
	if err != nil {
		// The input consists only of strings, so encoding cannot fail. Keep this
		// fail-open if that ever changes: an unbound digest is still preferable
		// to making an otherwise valid query unavailable.
		return baseDigest
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(authorizationProjectionDomain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(canonical)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func (c *queryResultCache) executeArrowWithDigest(ctx context.Context, request dataquery.Query, partition resultidentity.Partition, dependency resultidentity.Dependency, diagnosticsSQL string, observationStarted time.Time, queryDigest string, execute func(context.Context) (arrowQueryExecution, error)) (dataquery.Result, error) {
	if observationStarted.IsZero() {
		observationStarted = time.Now()
	}
	address, err := c.cacheAddressWithDigest(request, partition, dependency, queryDigest)
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
		// Consult the optional reusable tier only after entering the existing
		// process coalescing flight. This prevents concurrent cold misses from
		// stampeding durable storage while retaining the L1 recheck above.
		if tierHit, ok := c.lookupTierArrow(flightCtx, request, address); ok {
			// lookupTierArrow validates/decode-checks the value before promoting
			// it. Lookup transfers one owner reference; hand an independent lease
			// to the flight and release that owner reference.
			base, acquireErr := tierHit.data.Acquire()
			tierHit.data.Release()
			if acquireErr == nil {
				return resultcache.ArrowFlightValue{Data: base, Metadata: tierHit.metadata, Cached: true, HitSource: resultcache.HitSharedGeneration}, nil
			}
			// A value can be concurrently retired by a tier implementation after
			// lookup. Treat an acquire failure as a fail-open miss.
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
		c.storeArrowTier(address, execution.data, cacheMetadata)
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
	queryDigest, err := resultcacheidentity.CanonicalQueryDigest(request)
	if err != nil {
		return dataquery.Result{}, queryCacheAddress{}, false, resultcache.LookupObservation{}, err
	}
	return c.lookupArrowWithDigest(ctx, request, partition, dependency, diagnosticsSQL, queryDigest)
}

func (c *queryResultCache) lookupArrowWithDigest(ctx context.Context, request dataquery.Query, partition resultidentity.Partition, dependency resultidentity.Dependency, diagnosticsSQL, canonicalDigest string) (dataquery.Result, queryCacheAddress, bool, resultcache.LookupObservation, error) {
	address, err := c.cacheAddressWithDigest(request, partition, dependency, canonicalDigest)
	if err != nil {
		return dataquery.Result{}, queryCacheAddress{}, false, resultcache.LookupObservation{}, err
	}
	result, hit, observation, err := c.getArrowObserved(ctx, request, address, diagnosticsSQL)
	if err != nil || hit {
		return result, address, hit, observation, err
	}
	// A generation-local miss can still be reusable from the optional
	// persistent tier. Tier failures and malformed/inconsistent values are
	// deliberately fail-open misses so physical execution remains authoritative.
	tierHit, tierFound := c.lookupTierArrow(ctx, request, address)
	if !tierFound {
		return result, address, false, observation, nil
	}
	if budget, found := dataquery.ResultBudgetFromContext(ctx); found {
		if err := budget.ConsumeSize(int(tierHit.data.Rows()), tierHit.data.Bytes()); err != nil {
			tierHit.data.Release()
			return dataquery.Result{}, address, false, resultcache.LookupObservation{HitSource: resultcache.HitSharedGeneration}, err
		}
	}
	result = tierHit.decoded
	result.SQL = diagnosticsSQL
	result.CacheOutcome = dataquery.CacheHit
	tierHit.data.Release()
	return result, address, true, resultcache.LookupObservation{HitSource: resultcache.HitSharedGeneration}, nil
}

// lookupTierArrow performs one best-effort persistent-tier lookup. The tier
// result is decode-validated before it is promoted into L1; malformed or
// inconsistent responses are treated as misses rather than user-visible
// errors. A successful lookup transfers one owner reference to the caller.
func (c *queryResultCache) lookupTierArrow(ctx context.Context, request dataquery.Query, address queryCacheAddress) (tierArrowLookup, bool) {
	if c == nil || c.tier == nil || address.cacheKey.Version() == 0 {
		return tierArrowLookup{}, false
	}
	tierResult, tierMetadata, admission, tierFound, tierErr := c.tier.Lookup(ctx, address.cacheKey)
	if tierResult != nil && (tierErr != nil || !tierFound) {
		tierResult.Release()
		tierResult = nil
	}
	if tierErr != nil || !tierFound || tierResult == nil {
		return tierArrowLookup{}, false
	}
	if tierMetadata.TotalRows < 0 || (!tierMetadata.TotalRowsKnown && tierMetadata.TotalRows != 0) || (tierMetadata.TotalRowsKnown && int64(tierMetadata.TotalRows) < tierResult.Rows()) {
		tierResult.Release()
		rejectTierAdmission(ctx, admission, "result-tier metadata is inconsistent with materialize result")
		return tierArrowLookup{}, false
	}
	lease, err := tierResult.Acquire()
	if err != nil {
		tierResult.Release()
		return tierArrowLookup{}, false
	}
	tierMetadata.SQL = ""
	if schemaErr := validateTierArrowSchema(request, lease); schemaErr != nil {
		lease.Release()
		tierResult.Release()
		rejectTierAdmission(ctx, admission, "result-tier Arrow schema is inconsistent with query")
		return tierArrowLookup{}, false
	}
	decoded, decodeErr := decodeArrowQueryResult(request, lease, tierMetadata, dataquery.Result{CacheOutcome: dataquery.CacheHit})
	lease.Release()
	if decodeErr != nil {
		tierResult.Release()
		rejectTierAdmission(ctx, admission, "result-tier Arrow result is semantically invalid")
		return tierArrowLookup{}, false
	}
	// Store borrows tierResult, so a tier failure cannot alter the live owner.
	_ = c.scope.StoreArrowObserved(address.key, address.family, resultcache.Token(address.generation), tierResult, tierMetadata)
	c.syncStats()
	decoded.CacheOutcome = dataquery.CacheHit
	return tierArrowLookup{data: tierResult, metadata: tierMetadata, decoded: decoded}, true
}

func rejectTierAdmission(ctx context.Context, admission resulttier.Admission, reason string) {
	if admission != nil {
		// Tier rejection is best effort. The durable result is never allowed to
		// turn a semantic cache miss into a user-visible query failure, and the
		// admission capability itself guarantees this cannot retire a replacement
		// manifest published after Lookup.
		if ctx == nil {
			ctx = context.Background()
		}
		rejectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tierRejectTimeout)
		defer cancel()
		_ = admission.Reject(rejectCtx, reason)
	}
}

// validateTierArrowSchema checks the persistent result's final output shape
// against the governed query projection before promoting it into L1. Durable
// tiers are request-agnostic, so this boundary is where a valid Arrow stream
// with the wrong columns is rejected and retired. Specialized analytical and
// spatial envelopes have renderer-owned schemas and remain validated by their
// semantic decoders instead.
func validateTierArrowSchema(request dataquery.Query, lease *arrowresult.Lease) error {
	expected := tierExpectedColumns(request)
	if len(expected) == 0 {
		return nil
	}
	if lease == nil || lease.Schema() == nil {
		return fmt.Errorf("result-tier Arrow schema is missing")
	}
	actual := lease.Schema().Fields()
	actualNames := make([]string, 0, len(actual))
	for _, field := range actual {
		if request.IncludeTotal && request.Kind == dataquery.KindSemanticRows && field.Name == totalRowsColumn {
			continue
		}
		actualNames = append(actualNames, field.Name)
	}
	if len(actualNames) != len(expected) {
		return fmt.Errorf("result-tier Arrow schema columns = %d, want %d", len(actualNames), len(expected))
	}
	for index, name := range actualNames {
		if name != expected[index] {
			return fmt.Errorf("result-tier Arrow schema column %d = %q, want %q", index, name, expected[index])
		}
	}
	return nil
}

func tierExpectedColumns(request dataquery.Query) []string {
	switch request.Kind {
	case dataquery.KindSemanticAggregate, dataquery.KindSemanticRows, dataquery.KindModelTableRows:
	default:
		return nil
	}
	fields := make([]dataquery.Field, 0, len(request.Fields)+len(request.Metrics)+1)
	fields = append(fields, request.Fields...)
	if request.Kind == dataquery.KindSemanticAggregate && request.Time.Field != "" {
		fields = append(fields, dataquery.Field{Field: request.Time.Field, Alias: request.Time.Alias})
	}
	fields = append(fields, request.Metrics...)
	if len(fields) == 0 {
		if request.Kind == dataquery.KindSemanticRows && request.IncludeTotal {
			return []string{"value"}
		}
		return nil
	}
	expected := make([]string, 0, len(fields))
	for _, field := range fields {
		name := ""
		if request.Kind != dataquery.KindModelTableRows {
			name = strings.TrimSpace(field.Alias)
		}
		if name == "" {
			name = field.Field
			if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
				name = name[dot+1:]
			}
		}
		if name != "" {
			expected = append(expected, name)
		}
	}
	return expected
}

func (c *queryResultCache) storeArrowTier(address queryCacheAddress, result *arrowresult.Result, metadata resultcache.Metadata) {
	if c == nil || address.cacheKey.Version() == 0 || result == nil {
		return
	}
	metadata.SQL = ""
	metadata.Warnings = append([]string(nil), metadata.Warnings...)
	c.tierMu.Lock()
	defer c.tierMu.Unlock()
	if c.tierClosed || c.tier == nil {
		return
	}
	if !c.tierStarted {
		c.startTierWriterLocked()
	}
	lease, err := result.Acquire()
	if err != nil {
		return
	}
	job := tierWritebackJob{key: address.cacheKey, result: result, lease: lease, metadata: metadata}
	select {
	case c.tierQueue <- job:
		// The queue now owns lease and releases it after Store (or while
		// dropping a canceled/saturated job).
	default:
		lease.Release()
	}
}

// startTierWriterLocked starts the one bounded write-back worker. The caller
// must hold tierMu. Keeping Add and the closed transition under the same lock
// ensures close cannot race a WaitGroup Add.
func (c *queryResultCache) startTierWriterLocked() {
	if c.tierStarted || c.tierClosed {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.tierQueue = make(chan tierWritebackJob, tierWritebackQueueCapacity)
	c.tierCancel = cancel
	c.tierStarted = true
	c.tierWG.Add(1)
	go c.runTierWriter(ctx, c.tier, c.tierQueue)
}

func (c *queryResultCache) runTierWriter(ctx context.Context, tier resulttier.Tier, queue <-chan tierWritebackJob) {
	defer c.tierWG.Done()
	for {
		if ctx.Err() != nil {
			drainTierWritebackQueue(queue)
			return
		}
		select {
		case <-ctx.Done():
			drainTierWritebackQueue(queue)
			return
		case job := <-queue:
			if ctx.Err() == nil {
				// Store borrows job.result; the lease keeps its buffers alive for
				// the complete call. Tier errors are deliberately fail-open.
				_ = tier.Store(ctx, job.key, job.result, job.metadata)
			}
			job.lease.Release()
		}
	}
}

func drainTierWritebackQueue(queue <-chan tierWritebackJob) {
	for {
		select {
		case job := <-queue:
			job.lease.Release()
		default:
			return
		}
	}
}

// closeTierWriter cancels queued and in-flight write-back. tierMu protects the
// closed transition and the sole WaitGroup Add site; after it is set, no future
// write-back can add work, so Wait is safe.
func (c *queryResultCache) closeTierWriter() {
	if c == nil {
		return
	}
	c.tierMu.Lock()
	c.tierClosed = true
	started, cancel := c.tierStarted, c.tierCancel
	c.tierMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if started {
		c.tierWG.Wait()
	}
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
	return c.cacheAddressWithDigest(request, partition, dependency, queryDigest)
}

func (c *queryResultCache) cacheAddressWithDigest(request dataquery.Query, partition resultidentity.Partition, dependency resultidentity.Dependency, queryDigest string) (queryCacheAddress, error) {
	if !queryCacheIdentityAvailable(request, partition, dependency) {
		return queryCacheAddress{}, fmt.Errorf("complete query result cache identity is required")
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
	return queryCacheAddress{key: key.Digest(), cacheKey: key, family: resultcache.QueryFamily(family), generation: generation}, nil
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
	c.closeTierWriter()
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
