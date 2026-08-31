// Package resultcache owns node-wide retained analytical results and
// generation-scoped execution coalescing through separate lifetimes.
package resultcache

import (
	"container/list"
	"crypto/sha256"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/flidai/leapview/pkg/arrowresult"
)

type Constraint string

const (
	ConstraintRuntime Constraint = "runtime"
	ConstraintNode    Constraint = "node"
)

type StoreOutcome string

const (
	StoreStored    StoreOutcome = "stored"
	StoreOversized StoreOutcome = "oversized"
	StoreStale     StoreOutcome = "stale"
	StoreClosed    StoreOutcome = "closed"
)

// QueryFamily is an opaque, process-local projection used only to classify an
// exact-key miss as a canonical-query mismatch. It never participates in the
// serialized cache key and is never exported as telemetry.
type QueryFamily [sha256.Size]byte

type LookupMissReason string

const (
	LookupMissColdStart     LookupMissReason = "cold_start"
	LookupMissAbsentEntry   LookupMissReason = "absent_entry"
	LookupMissQueryMismatch LookupMissReason = "query_mismatch"
	LookupMissInvalidated   LookupMissReason = "invalidated"
	LookupMissEvicted       LookupMissReason = "evicted"
)

type HitSource string

const (
	HitCurrentGeneration HitSource = "current_generation"
	HitSharedGeneration  HitSource = "shared_generation"
	HitCutoverRetained   HitSource = "cutover_retained"
)

type LookupObservation struct {
	MissReason LookupMissReason
	HitSource  HitSource
}

type CacheClass string

const (
	CacheClassStableResult   CacheClass = "stable_result"
	CacheClassGenerationByte CacheClass = "generation_byte"
)

type ScopeTransition string

const (
	ScopeTransitionCreated     ScopeTransition = "created"
	ScopeTransitionDormant     ScopeTransition = "dormant"
	ScopeTransitionReactivated ScopeTransition = "reactivated"
	ScopeTransitionRemoved     ScopeTransition = "removed"
)

type Limits struct {
	RuntimeEntries int
	RuntimeBytes   int64
	NodeEntries    int
	NodeBytes      int64
}

func (l Limits) Validate() error {
	if l.RuntimeEntries <= 0 || l.NodeEntries <= 0 || l.RuntimeBytes <= 0 || l.NodeBytes <= 0 {
		return fmt.Errorf("query cache limits must be positive")
	}
	if l.RuntimeEntries > l.NodeEntries {
		return fmt.Errorf("query cache entry limits must satisfy runtime <= node")
	}
	if l.RuntimeBytes > l.NodeBytes {
		return fmt.Errorf("query cache byte limits must satisfy runtime <= node")
	}
	return nil
}

type ScopeID struct{ RuntimeID string }
type Token uint64

// ScopeProvider is the cache capability required by runtime consumers.
type ScopeProvider interface {
	OpenScope(ScopeID) (*Scope, error)
}

type Pool struct {
	mu                  sync.Mutex
	limits              Limits
	closed              bool
	entries             map[string]*list.Element
	lru                 *list.List
	scopes              map[string]*scopeState
	bytes               int64
	evictions           map[Constraint]uint64
	stores              map[StoreOutcome]uint64
	invalidations       map[CacheClass]uint64
	invalidatedEntries  map[CacheClass]uint64
	classEvictions      map[CacheClass]map[Constraint]uint64
	scopeTransitions    map[ScopeTransition]uint64
	removalHistory      map[[sha256.Size]byte]*list.Element
	removalOrder        *list.List
	removalHistoryLimit int
	nextHandleID        uint64
}

type Scope struct {
	pool       *Pool
	key        string
	handleID   uint64
	activation uint64
	closed     atomic.Bool
}
type scopeState struct {
	id              ScopeID
	generation      Token
	closed          bool
	shared          bool
	references      int
	entries         map[string]struct{}
	queryFamilies   map[QueryFamily]int
	everStoredArrow bool
	activation      uint64
	usage           usage
}
type usage struct {
	entries int
	bytes   int64
}
type entry struct {
	composite, key, scope string
	arrowResult           *arrowresult.Result
	arrowHold             *arrowresult.Lease
	byteValue             []byte
	metadata              Metadata
	bytes                 int64
	family                QueryFamily
	writerHandle          uint64
	writerActivation      uint64
}

type removalRecord struct {
	digest [sha256.Size]byte
	reason LookupMissReason
}

// Metadata is stable result information that may be retained across requests.
// Request-specific audit state and timing deliberately live outside the cache.
type Metadata struct {
	SQL            string
	TotalRows      int
	TotalRowsKnown bool
	Warnings       []string
}

type EntryLease struct {
	data     *arrowresult.Lease
	metadata Metadata
}

// ArrowFlightValue is the one reference owned by an in-flight execution. The
// coalescer releases it after every registered caller has either acquired an
// independent sibling lease or canceled.
type ArrowFlightValue struct {
	Data      *arrowresult.Lease
	Metadata  Metadata
	Cached    bool
	HitSource HitSource
}

type ArrowFlightStatus struct {
	Owner  bool
	Shared bool
}

type ArrowFlightLease struct {
	data      *arrowresult.Lease
	metadata  Metadata
	cached    bool
	hitSource HitSource
}

func (l *ArrowFlightLease) Data() *arrowresult.Lease {
	if l == nil {
		return nil
	}
	return l.data
}

func (l *ArrowFlightLease) Metadata() Metadata {
	if l == nil {
		return Metadata{}
	}
	return cloneMetadata(l.metadata)
}

func (l *ArrowFlightLease) Cached() bool {
	return l != nil && l.cached
}

func (l *ArrowFlightLease) HitSource() HitSource {
	if l == nil {
		return ""
	}
	return l.hitSource
}

func (l *ArrowFlightLease) Release() {
	if l == nil || l.data == nil {
		return
	}
	l.data.Release()
	l.data = nil
}

func (l *EntryLease) Data() *arrowresult.Lease {
	if l == nil {
		return nil
	}
	return l.data
}

func (l *EntryLease) Metadata() Metadata {
	if l == nil {
		return Metadata{}
	}
	return cloneMetadata(l.metadata)
}

func (l *EntryLease) Release() {
	if l == nil || l.data == nil {
		return
	}
	l.data.Release()
	l.data = nil
}

type UsageSnapshot struct {
	Entries int
	Bytes   int64
}
type ScopeSnapshot struct {
	ScopeID
	Entries    int
	Bytes      int64
	Generation Token
}

func (s *Scope) Stats() UsageSnapshot {
	if s == nil || s.pool == nil {
		return UsageSnapshot{}
	}
	s.pool.mu.Lock()
	defer s.pool.mu.Unlock()
	if state := s.openStateLocked(); state != nil {
		return UsageSnapshot{Entries: state.usage.entries, Bytes: state.usage.bytes}
	}
	return UsageSnapshot{}
}

type Snapshot struct {
	Entries            int
	Bytes              int64
	Scopes             map[string]ScopeSnapshot
	Evictions          map[Constraint]uint64
	Stores             map[StoreOutcome]uint64
	Stable             StableSnapshot
	Generation         GenerationSnapshot
	Invalidations      map[CacheClass]uint64
	InvalidatedEntries map[CacheClass]uint64
	ClassEvictions     map[CacheClass]map[Constraint]uint64
	ScopeTransitions   map[ScopeTransition]uint64
}

type StableSnapshot struct {
	ActiveScopes  int
	DormantScopes int
	Entries       int
	Bytes         int64
	ArrowHolds    int
}

type GenerationSnapshot struct {
	Scopes      int
	ByteEntries int
	ByteBytes   int64
}

func New(limits Limits) (*Pool, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	removalHistoryLimit := min(limits.NodeEntries, 4096)
	return &Pool{
		limits: limits, entries: map[string]*list.Element{}, lru: list.New(), scopes: map[string]*scopeState{},
		evictions: map[Constraint]uint64{}, stores: map[StoreOutcome]uint64{},
		invalidations: map[CacheClass]uint64{}, invalidatedEntries: map[CacheClass]uint64{},
		classEvictions: map[CacheClass]map[Constraint]uint64{}, scopeTransitions: map[ScopeTransition]uint64{},
		removalHistory: map[[sha256.Size]byte]*list.Element{}, removalOrder: list.New(), removalHistoryLimit: removalHistoryLimit,
	}, nil
}

func (p *Pool) OpenScope(id ScopeID) (*Scope, error) {
	if p == nil {
		return nil, fmt.Errorf("result cache pool is required")
	}
	if id.RuntimeID == "" {
		return nil, fmt.Errorf("result cache runtime ID is required")
	}
	key := id.RuntimeID
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, fmt.Errorf("result cache pool is closed")
	}
	if existing := p.scopes[key]; existing != nil && !existing.closed {
		return nil, fmt.Errorf("result cache scope already exists")
	}
	p.nextHandleID++
	p.scopes[key] = &scopeState{id: id, references: 1, entries: map[string]struct{}{}, queryFamilies: map[QueryFamily]int{}, activation: 1}
	return &Scope{pool: p, key: key, handleID: p.nextHandleID, activation: 1}, nil
}

// OpenSharedScope acquires one handle to a stable cache scope. All live
// handles with the same identity share entries and invalidation generation.
// Closing the final handle leaves retained state dormant so a compatible
// serving generation can reactivate it. Empty dormant scopes are discarded.
func (p *Pool) OpenSharedScope(id ScopeID) (*Scope, error) {
	if p == nil {
		return nil, fmt.Errorf("result cache pool is required")
	}
	if id.RuntimeID == "" {
		return nil, fmt.Errorf("result cache runtime ID is required")
	}
	key := id.RuntimeID
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, fmt.Errorf("result cache pool is closed")
	}
	if existing := p.scopes[key]; existing != nil && !existing.closed {
		if !existing.shared {
			return nil, fmt.Errorf("result cache scope already exists")
		}
		cutover := existing.references == 0
		if cutover {
			existing.activation++
		}
		existing.references++
		p.nextHandleID++
		if cutover {
			p.scopeTransitions[ScopeTransitionReactivated]++
		}
		return &Scope{pool: p, key: key, handleID: p.nextHandleID, activation: existing.activation}, nil
	}
	p.nextHandleID++
	p.scopes[key] = &scopeState{id: id, shared: true, references: 1, entries: map[string]struct{}{}, queryFamilies: map[QueryFamily]int{}, activation: 1}
	p.scopeTransitions[ScopeTransitionCreated]++
	return &Scope{pool: p, key: key, handleID: p.nextHandleID, activation: 1}, nil
}

func (s *Scope) openStateLocked() *scopeState {
	if s == nil || s.pool == nil || s.closed.Load() || s.pool.closed {
		return nil
	}
	state := s.pool.scopes[s.key]
	if state == nil || state.closed {
		return nil
	}
	return state
}

func (s *Scope) Generation() Token {
	if s == nil || s.pool == nil {
		return 0
	}
	s.pool.mu.Lock()
	defer s.pool.mu.Unlock()
	if state := s.openStateLocked(); state != nil {
		return state.generation
	}
	return 0
}

// LookupArrow returns an independently retained lease. Eviction, invalidation,
// or scope closure can remove the cache's reference without invalidating it.
func (s *Scope) LookupArrow(key string) (*EntryLease, Token, bool, error) {
	lease, token, hit, _, err := s.LookupArrowObserved(key, QueryFamily{})
	return lease, token, hit, err
}

// LookupArrowObserved returns the cache lookup classification without exposing
// the key or family through telemetry. The family is only compared while the
// pool mutex is held.
func (s *Scope) LookupArrowObserved(key string, family QueryFamily) (*EntryLease, Token, bool, LookupObservation, error) {
	if s == nil || s.pool == nil {
		return nil, 0, false, LookupObservation{}, fmt.Errorf("result cache scope is required")
	}
	p := s.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	state := s.openStateLocked()
	if state == nil {
		return nil, 0, false, LookupObservation{}, fmt.Errorf("result cache scope is closed")
	}
	composite := s.key + "\x00" + key
	element := p.entries[composite]
	if element == nil {
		return nil, state.generation, false, LookupObservation{MissReason: p.lookupMissReasonLocked(state, composite, family)}, nil
	}
	e := element.Value.(entry)
	if e.arrowResult == nil {
		return nil, state.generation, false, LookupObservation{MissReason: p.lookupMissReasonLocked(state, composite, family)}, nil
	}
	lease, err := e.arrowResult.Acquire()
	if err != nil {
		return nil, state.generation, false, LookupObservation{}, err
	}
	p.lru.MoveToFront(element)
	source := HitCurrentGeneration
	if e.writerHandle != s.handleID {
		source = HitSharedGeneration
		if e.writerActivation < s.activation {
			source = HitCutoverRetained
		}
	}
	return &EntryLease{data: lease, metadata: cloneMetadata(e.metadata)}, state.generation, true, LookupObservation{HitSource: source}, nil
}

// LookupBytes returns a copy of an immutable byte entry. Callers can mutate
// the returned slice without changing cached tiles or other callers' results.
func (s *Scope) LookupBytes(key string) ([]byte, Token, bool, error) {
	if s == nil || s.pool == nil {
		return nil, 0, false, fmt.Errorf("result cache scope is required")
	}
	p := s.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	state := s.openStateLocked()
	if state == nil {
		return nil, 0, false, fmt.Errorf("result cache scope is closed")
	}
	element := p.entries[s.key+"\x00"+key]
	if element == nil {
		return nil, state.generation, false, nil
	}
	e := element.Value.(entry)
	if e.byteValue == nil {
		return nil, state.generation, false, nil
	}
	p.lru.MoveToFront(element)
	return append([]byte(nil), e.byteValue...), state.generation, true, nil
}

// StoreArrow retains one cache-owned reference when the value fits every
// applicable budget. The caller retains ownership of its original reference.
func (s *Scope) StoreArrow(key string, token Token, result *arrowresult.Result, metadata Metadata) StoreOutcome {
	return s.StoreArrowObserved(key, QueryFamily{}, token, result, metadata)
}

func (s *Scope) StoreArrowObserved(key string, family QueryFamily, token Token, result *arrowresult.Result, metadata Metadata) StoreOutcome {
	if s == nil || s.pool == nil || result == nil {
		return StoreClosed
	}
	p := s.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	state := s.openStateLocked()
	if state == nil {
		p.stores[StoreClosed]++
		return StoreClosed
	}
	if token != state.generation {
		p.stores[StoreStale]++
		return StoreStale
	}
	bytes := int64(len(key)) + result.Bytes() + metadataBytes(metadata)
	if bytes > p.limits.RuntimeBytes || bytes > p.limits.NodeBytes {
		p.stores[StoreOversized]++
		return StoreOversized
	}
	hold, err := result.Acquire()
	if err != nil {
		p.stores[StoreClosed]++
		return StoreClosed
	}
	composite := s.key + "\x00" + key
	if old := p.entries[composite]; old != nil {
		p.removeLocked(old, "", "")
	}
	p.forgetRemovalLocked(composite)
	e := entry{composite: composite, key: key, scope: s.key, arrowResult: result, arrowHold: hold, metadata: cloneMetadata(metadata), bytes: bytes, family: family, writerHandle: s.handleID, writerActivation: s.activation}
	element := p.lru.PushFront(e)
	p.entries[composite] = element
	state.entries[composite] = struct{}{}
	state.queryFamilies[family]++
	state.everStoredArrow = true
	state.usage.entries++
	state.usage.bytes += bytes
	p.bytes += bytes
	p.enforceLocked(state)
	p.stores[StoreStored]++
	return StoreStored
}

// StoreBytes copies and retains one immutable byte entry under the same
// runtime/node budgets as Arrow results.
func (s *Scope) StoreBytes(key string, token Token, value []byte) StoreOutcome {
	if s == nil || s.pool == nil || value == nil {
		return StoreClosed
	}
	p := s.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	state := s.openStateLocked()
	if state == nil {
		p.stores[StoreClosed]++
		return StoreClosed
	}
	if token != state.generation {
		p.stores[StoreStale]++
		return StoreStale
	}
	bytes := int64(len(key) + len(value))
	if bytes > p.limits.RuntimeBytes || bytes > p.limits.NodeBytes {
		p.stores[StoreOversized]++
		return StoreOversized
	}
	composite := s.key + "\x00" + key
	if old := p.entries[composite]; old != nil {
		p.removeLocked(old, "", "")
	}
	stored := make([]byte, len(value))
	copy(stored, value)
	e := entry{composite: composite, key: key, scope: s.key, byteValue: stored, bytes: bytes}
	element := p.lru.PushFront(e)
	p.entries[composite] = element
	state.entries[composite] = struct{}{}
	state.usage.entries++
	state.usage.bytes += bytes
	p.bytes += bytes
	p.enforceLocked(state)
	p.stores[StoreStored]++
	return StoreStored
}

func (s *Scope) Delete(key string) {
	if s == nil || s.pool == nil {
		return
	}
	p := s.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	state := s.openStateLocked()
	if state == nil {
		return
	}
	p.removeLocked(p.entries[s.key+"\x00"+key], "", "")
}

func (p *Pool) enforceLocked(state *scopeState) {
	for state.usage.entries > p.limits.RuntimeEntries || state.usage.bytes > p.limits.RuntimeBytes {
		p.removeLocked(p.oldestLocked(func(e entry) bool { return e.scope == scopeKey(state.id) }), ConstraintRuntime, LookupMissEvicted)
	}
	for len(p.entries) > p.limits.NodeEntries || p.bytes > p.limits.NodeBytes {
		p.removeLocked(p.lru.Back(), ConstraintNode, LookupMissEvicted)
	}
}

func (p *Pool) oldestLocked(match func(entry) bool) *list.Element {
	for e := p.lru.Back(); e != nil; e = e.Prev() {
		if match(e.Value.(entry)) {
			return e
		}
	}
	return nil
}
func (p *Pool) removeLocked(element *list.Element, constraint Constraint, reason LookupMissReason) {
	if element == nil {
		return
	}
	e := element.Value.(entry)
	if e.arrowHold != nil {
		e.arrowHold.Release()
	}
	state := p.scopes[e.scope]
	delete(p.entries, e.composite)
	p.lru.Remove(element)
	p.bytes -= e.bytes
	if state != nil {
		delete(state.entries, e.composite)
		if e.arrowResult != nil {
			state.queryFamilies[e.family]--
			if state.queryFamilies[e.family] == 0 {
				delete(state.queryFamilies, e.family)
			}
		}
		state.usage.entries--
		state.usage.bytes -= e.bytes
		p.removeEmptyDormantScopeLocked(state)
	}
	if constraint != "" {
		p.evictions[constraint]++
		class := cacheClassOf(state, e)
		if p.classEvictions[class] == nil {
			p.classEvictions[class] = map[Constraint]uint64{}
		}
		p.classEvictions[class][constraint]++
	}
	if e.arrowResult != nil && (reason == LookupMissEvicted || reason == LookupMissInvalidated) {
		p.rememberRemovalLocked(e.composite, reason)
	}
}

func (p *Pool) removeEmptyDormantScopeLocked(state *scopeState) {
	if state == nil || !state.shared || state.references != 0 || len(state.entries) != 0 {
		return
	}
	state.closed = true
	delete(p.scopes, scopeKey(state.id))
	p.scopeTransitions[ScopeTransitionRemoved]++
}

func (p *Pool) lookupMissReasonLocked(state *scopeState, composite string, family QueryFamily) LookupMissReason {
	digest := sha256.Sum256([]byte(composite))
	if element := p.removalHistory[digest]; element != nil {
		p.removalOrder.MoveToFront(element)
		return element.Value.(removalRecord).reason
	}
	if !state.everStoredArrow {
		return LookupMissColdStart
	}
	if state.queryFamilies[family] > 0 {
		return LookupMissQueryMismatch
	}
	return LookupMissAbsentEntry
}

func (p *Pool) rememberRemovalLocked(composite string, reason LookupMissReason) {
	if p.removalHistoryLimit <= 0 {
		return
	}
	digest := sha256.Sum256([]byte(composite))
	if existing := p.removalHistory[digest]; existing != nil {
		existing.Value = removalRecord{digest: digest, reason: reason}
		p.removalOrder.MoveToFront(existing)
		return
	}
	element := p.removalOrder.PushFront(removalRecord{digest: digest, reason: reason})
	p.removalHistory[digest] = element
	for p.removalOrder.Len() > p.removalHistoryLimit {
		oldest := p.removalOrder.Back()
		record := oldest.Value.(removalRecord)
		delete(p.removalHistory, record.digest)
		p.removalOrder.Remove(oldest)
	}
}

func (p *Pool) forgetRemovalLocked(composite string) {
	digest := sha256.Sum256([]byte(composite))
	if element := p.removalHistory[digest]; element != nil {
		delete(p.removalHistory, digest)
		p.removalOrder.Remove(element)
	}
}

func cacheClassOf(state *scopeState, e entry) CacheClass {
	if e.arrowResult != nil {
		return CacheClassStableResult
	}
	return CacheClassGenerationByte
}

func cloneMetadata(metadata Metadata) Metadata {
	metadata.Warnings = append([]string{}, metadata.Warnings...)
	return metadata
}

func metadataBytes(metadata Metadata) int64 {
	bytes := int64(len(metadata.SQL) + 16)
	for _, warning := range metadata.Warnings {
		bytes += int64(len(warning) + 16)
	}
	return bytes
}
func scopeKey(id ScopeID) string { return id.RuntimeID }

func (s *Scope) Invalidate() {
	if s == nil || s.pool == nil {
		return
	}
	p := s.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	state := s.openStateLocked()
	if state == nil {
		return
	}
	classes := map[CacheClass]uint64{}
	if len(state.entries) == 0 {
		class := CacheClassGenerationByte
		if state.shared {
			class = CacheClassStableResult
		}
		classes[class] = 0
	}
	for composite := range state.entries {
		element := p.entries[composite]
		if element != nil {
			classes[cacheClassOf(state, element.Value.(entry))]++
		}
		p.removeLocked(element, "", LookupMissInvalidated)
	}
	for class, removed := range classes {
		p.invalidations[class]++
		p.invalidatedEntries[class] += removed
	}
	state.generation++
}

func (s *Scope) Close() error {
	if s == nil || s.pool == nil {
		return nil
	}
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	p := s.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.scopes[s.key]
	if state == nil || state.closed {
		return nil
	}
	if state.shared {
		if state.references > 0 {
			state.references--
		}
		if state.references == 0 && len(state.entries) > 0 {
			p.scopeTransitions[ScopeTransitionDormant]++
		}
		p.removeEmptyDormantScopeLocked(state)
		return nil
	}
	for composite := range state.entries {
		p.removeLocked(p.entries[composite], "", "")
	}
	state.closed = true
	state.generation++
	delete(p.scopes, s.key)
	return nil
}

func (p *Pool) Stats() Snapshot {
	if p == nil {
		return Snapshot{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	result := Snapshot{
		Entries: len(p.entries), Bytes: p.bytes, Scopes: map[string]ScopeSnapshot{},
		Evictions: map[Constraint]uint64{}, Stores: map[StoreOutcome]uint64{},
		Invalidations: map[CacheClass]uint64{}, InvalidatedEntries: map[CacheClass]uint64{},
		ClassEvictions: map[CacheClass]map[Constraint]uint64{}, ScopeTransitions: map[ScopeTransition]uint64{},
	}
	for key, state := range p.scopes {
		result.Scopes[key] = ScopeSnapshot{ScopeID: state.id, Entries: state.usage.entries, Bytes: state.usage.bytes, Generation: state.generation}
		if state.shared {
			if state.references > 0 {
				result.Stable.ActiveScopes++
			} else {
				result.Stable.DormantScopes++
			}
		} else {
			result.Generation.Scopes++
		}
		for composite := range state.entries {
			element := p.entries[composite]
			if element == nil {
				continue
			}
			e := element.Value.(entry)
			if state.shared && e.arrowResult != nil {
				result.Stable.Entries++
				result.Stable.Bytes += e.bytes
				if e.arrowHold != nil {
					result.Stable.ArrowHolds++
				}
			} else if !state.shared && e.byteValue != nil {
				result.Generation.ByteEntries++
				result.Generation.ByteBytes += e.bytes
			}
		}
	}
	for key, value := range p.evictions {
		result.Evictions[key] = value
	}
	for key, value := range p.stores {
		result.Stores[key] = value
	}
	for key, value := range p.invalidations {
		result.Invalidations[key] = value
	}
	for key, value := range p.invalidatedEntries {
		result.InvalidatedEntries[key] = value
	}
	for class, counts := range p.classEvictions {
		result.ClassEvictions[class] = map[Constraint]uint64{}
		for constraint, value := range counts {
			result.ClassEvictions[class][constraint] = value
		}
	}
	for key, value := range p.scopeTransitions {
		result.ScopeTransitions[key] = value
	}
	return result
}

func (p *Pool) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	for e := p.lru.Back(); e != nil; {
		previous := e.Prev()
		p.removeLocked(e, "", "")
		e = previous
	}
	for _, state := range p.scopes {
		state.closed = true
		state.generation++
	}
	p.scopes = map[string]*scopeState{}
	return nil
}
