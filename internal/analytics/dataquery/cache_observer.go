package dataquery

import (
	"context"
	"strings"
	"sync/atomic"
	"time"
)

// CacheOutcomeObserver receives low-cardinality cache outcomes at the query
// boundary. The observer is request scoped so runtimes remain independent of
// HTTP and metrics packages.
type CacheOutcomeObserver func(outcome string)

// CacheObservationPhase identifies one bounded step in the logical cache
// decision. A query emits at most one lookup observation; an internal
// execution-flight second-chance lookup is deliberately not a second logical
// lookup.
type CacheObservationPhase string

const (
	CacheObservationAdmission CacheObservationPhase = "admission"
	CacheObservationLookup    CacheObservationPhase = "lookup"
	CacheObservationFinal     CacheObservationPhase = "final"
	CacheObservationStore     CacheObservationPhase = "store"
)

type CacheAdmissionDecision string

const (
	CacheAdmissionEligible CacheAdmissionDecision = "eligible"
	CacheAdmissionBypassed CacheAdmissionDecision = "bypassed"
	CacheAdmissionRejected CacheAdmissionDecision = "rejected"
)

type CacheAdmissionReason string

const (
	CacheAdmissionReasonEligible              CacheAdmissionReason = "eligible"
	CacheAdmissionReasonQueryNotCacheable     CacheAdmissionReason = "query_not_cacheable"
	CacheAdmissionReasonPlanningFailed        CacheAdmissionReason = "planning_failed"
	CacheAdmissionReasonCanceled              CacheAdmissionReason = "canceled"
	CacheAdmissionReasonDependencyUnavailable CacheAdmissionReason = "dependency_unavailable"
	CacheAdmissionReasonDependencyInvalid     CacheAdmissionReason = "dependency_invalid"
	CacheAdmissionReasonPolicyInvalid         CacheAdmissionReason = "policy_invalid"
	CacheAdmissionReasonPartitionInvalid      CacheAdmissionReason = "partition_invalid"
	// NonDeterministic covers volatile plans and plans without positive
	// determinism evidence. Both bypass lookup, store, and execution
	// coalescing so a transient result cannot be reused accidentally.
	CacheAdmissionReasonNonDeterministic CacheAdmissionReason = "non_deterministic"
)

type CacheLookupMissReason string

const (
	CacheLookupMissColdStart     CacheLookupMissReason = "cold_start"
	CacheLookupMissAbsentEntry   CacheLookupMissReason = "absent_entry"
	CacheLookupMissQueryMismatch CacheLookupMissReason = "query_mismatch"
	CacheLookupMissInvalidated   CacheLookupMissReason = "invalidated"
	CacheLookupMissEvicted       CacheLookupMissReason = "evicted"
)

type CacheHitSource string

const (
	CacheHitCurrentGeneration CacheHitSource = "current_generation"
	CacheHitSharedGeneration  CacheHitSource = "shared_generation"
	CacheHitCutoverRetained   CacheHitSource = "cutover_retained"
)

type CacheObservationOutcome string

const (
	CacheObservationHit       CacheObservationOutcome = "hit"
	CacheObservationMiss      CacheObservationOutcome = "miss"
	CacheObservationCoalesced CacheObservationOutcome = "coalesced"
	CacheObservationError     CacheObservationOutcome = "error"
)

type CacheStoreOutcome string

const (
	CacheStoreStored    CacheStoreOutcome = "stored"
	CacheStoreOversized CacheStoreOutcome = "oversized"
	CacheStoreStale     CacheStoreOutcome = "stale"
	CacheStoreClosed    CacheStoreOutcome = "closed"
)

// CacheObservation contains only fixed-cardinality classifications and
// durations. Query text, identities, principals, and resource labels are not
// part of this contract.
type CacheObservation struct {
	Phase           CacheObservationPhase
	Decision        CacheAdmissionDecision
	AdmissionReason CacheAdmissionReason
	MissReason      CacheLookupMissReason
	HitSource       CacheHitSource
	Outcome         CacheObservationOutcome
	StoreOutcome    CacheStoreOutcome
	Duration        time.Duration
}

type CacheObserver func(observation CacheObservation)

type PhysicalQueryObservation struct {
	Count  int
	Result Result
}

// PhysicalQueryObserver receives the physical statement count and aggregate
// stage timings for one cache-miss execution. Cache hits and coalesced callers
// never invoke it.
type PhysicalQueryObserver func(observation PhysicalQueryObservation)

// ConnectionWaitObserver receives time spent waiting for a database/sql pool
// connection. Executors report exactly once for each public query operation.
type ConnectionWaitObserver func(wait time.Duration)

// ConnectionWaitCounter accumulates connection acquisition time across one
// logical data query, which may execute more than one physical operation.
type ConnectionWaitCounter struct{ nanoseconds atomic.Int64 }

type cacheOutcomeObserverContextKey struct{}
type cacheObserverContextKey struct{}
type physicalQueryObserverContextKey struct{}
type connectionWaitObserverContextKey struct{}

func WithCacheOutcomeObserver(ctx context.Context, observer CacheOutcomeObserver) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, cacheOutcomeObserverContextKey{}, observer)
}

func ObserveCacheOutcome(ctx context.Context, outcome string) {
	if ctx == nil || strings.TrimSpace(outcome) == "" {
		return
	}
	observer, ok := ctx.Value(cacheOutcomeObserverContextKey{}).(CacheOutcomeObserver)
	if ok && observer != nil {
		observer(outcome)
	}
}

func WithCacheObserver(ctx context.Context, observer CacheObserver) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, cacheObserverContextKey{}, observer)
}

func ObserveCache(ctx context.Context, observation CacheObservation) {
	if ctx == nil || observation.Phase == "" {
		return
	}
	observer, ok := ctx.Value(cacheObserverContextKey{}).(CacheObserver)
	if ok && observer != nil {
		observer(observation)
	}
}

func WithPhysicalQueryObserver(ctx context.Context, observer PhysicalQueryObserver) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, physicalQueryObserverContextKey{}, observer)
}

func ObservePhysicalQuery(ctx context.Context, observation PhysicalQueryObservation) {
	if ctx == nil {
		return
	}
	observer, ok := ctx.Value(physicalQueryObserverContextKey{}).(PhysicalQueryObserver)
	if ok && observer != nil {
		observer(observation)
	}
}

func WithConnectionWaitObserver(ctx context.Context, observer ConnectionWaitObserver) context.Context {
	if observer == nil {
		return ctx
	}
	if existing, ok := ctx.Value(connectionWaitObserverContextKey{}).(ConnectionWaitObserver); ok && existing != nil {
		return context.WithValue(ctx, connectionWaitObserverContextKey{}, ConnectionWaitObserver(func(wait time.Duration) {
			existing(wait)
			observer(wait)
		}))
	}
	return context.WithValue(ctx, connectionWaitObserverContextKey{}, observer)
}

func ObserveConnectionWait(ctx context.Context, wait time.Duration) {
	if ctx == nil || wait < 0 {
		return
	}
	observer, ok := ctx.Value(connectionWaitObserverContextKey{}).(ConnectionWaitObserver)
	if ok && observer != nil {
		observer(wait)
	}
}

func WithConnectionWaitCounter(ctx context.Context) (context.Context, *ConnectionWaitCounter) {
	counter := &ConnectionWaitCounter{}
	return WithConnectionWaitObserver(ctx, counter.Add), counter
}

func (c *ConnectionWaitCounter) Add(wait time.Duration) {
	if c != nil && wait > 0 {
		c.nanoseconds.Add(int64(wait))
	}
}

func (c *ConnectionWaitCounter) Duration() time.Duration {
	if c == nil {
		return 0
	}
	return time.Duration(c.nanoseconds.Load())
}
