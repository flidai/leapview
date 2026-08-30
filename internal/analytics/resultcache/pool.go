// Package resultcache owns node-wide retained analytical results and
// generation-scoped execution coalescing through separate lifetimes.
package resultcache

import (
	"container/list"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/flidai/leapview/pkg/arrowresult"
)

type Constraint string

const (
	ConstraintPartition Constraint = "partition"
	// ConstraintRuntime is retained as a compatibility alias for the former
	// per-runtime budget name.
	ConstraintRuntime Constraint = ConstraintPartition
	ConstraintNode    Constraint = "node"
)

type StoreOutcome string

const (
	StoreStored    StoreOutcome = "stored"
	StoreOversized StoreOutcome = "oversized"
	StoreStale     StoreOutcome = "stale"
	StoreClosed    StoreOutcome = "closed"
)

type Limits struct {
	PartitionEntries int
	PartitionBytes   int64
	NodeEntries      int
	NodeBytes        int64
	// RuntimeEntries/RuntimeBytes are compatibility aliases for the former
	// per-runtime limits. New callers should use PartitionEntries/Bytes.
	RuntimeEntries int
	RuntimeBytes   int64
}

func (l Limits) Validate() error {
	partitionEntries, partitionBytes := l.PartitionEntries, l.PartitionBytes
	if partitionEntries == 0 {
		partitionEntries = l.RuntimeEntries
	}
	if partitionBytes == 0 {
		partitionBytes = l.RuntimeBytes
	}
	if partitionEntries <= 0 || l.NodeEntries <= 0 || partitionBytes <= 0 || l.NodeBytes <= 0 {
		return fmt.Errorf("query cache limits must be positive")
	}
	if partitionEntries > l.NodeEntries {
		return fmt.Errorf("query cache entry limits must satisfy partition <= node")
	}
	if partitionBytes > l.NodeBytes {
		return fmt.Errorf("query cache byte limits must satisfy partition <= node")
	}
	return nil
}

type ScopeID struct {
	RuntimeID   string
	PartitionID string
}
type Token uint64

// ScopeProvider is the cache capability required by runtime consumers.
type ScopeProvider interface {
	OpenScope(ScopeID) (*Scope, error)
}

type Pool struct {
	mu         sync.Mutex
	limits     Limits
	closed     bool
	entries    map[string]*list.Element
	lru        *list.List
	scopes     map[string]*scopeState
	partitions map[string]*usage
	bytes      int64
	evictions  map[Constraint]uint64
	stores     map[StoreOutcome]uint64
}

type Scope struct {
	pool   *Pool
	key    string
	closed atomic.Bool
}
type scopeState struct {
	id         ScopeID
	generation Token
	closed     bool
	shared     bool
	references int
	entries    map[string]struct{}
}
type usage struct {
	entries int
	bytes   int64
}
type entry struct {
	composite, key, scope string
	partition             string
	arrowResult           *arrowresult.Result
	arrowHold             *arrowresult.Lease
	byteValue             []byte
	metadata              Metadata
	bytes                 int64
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
	Data     *arrowresult.Lease
	Metadata Metadata
	Cached   bool
}

type ArrowFlightStatus struct {
	Owner  bool
	Shared bool
}

type ArrowFlightLease struct {
	data     *arrowresult.Lease
	metadata Metadata
	cached   bool
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
		usage := s.pool.partitions[state.id.PartitionID]
		if usage == nil {
			return UsageSnapshot{}
		}
		return UsageSnapshot{Entries: usage.entries, Bytes: usage.bytes}
	}
	return UsageSnapshot{}
}

type Snapshot struct {
	Entries   int
	Bytes     int64
	Scopes    map[string]ScopeSnapshot
	Evictions map[Constraint]uint64
	Stores    map[StoreOutcome]uint64
}

func New(limits Limits) (*Pool, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	if limits.PartitionEntries == 0 {
		limits.PartitionEntries = limits.RuntimeEntries
	}
	if limits.PartitionBytes == 0 {
		limits.PartitionBytes = limits.RuntimeBytes
	}
	return &Pool{limits: limits, entries: map[string]*list.Element{}, lru: list.New(), scopes: map[string]*scopeState{}, partitions: map[string]*usage{}, evictions: map[Constraint]uint64{}, stores: map[StoreOutcome]uint64{}}, nil
}

func (p *Pool) OpenScope(id ScopeID) (*Scope, error) {
	if p == nil {
		return nil, fmt.Errorf("result cache pool is required")
	}
	if id.RuntimeID == "" {
		return nil, fmt.Errorf("result cache runtime ID is required")
	}
	if id.PartitionID == "" {
		// Compatibility with pre-partition callers. New serving code supplies an
		// explicit stable partition identity.
		id.PartitionID = id.RuntimeID
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
	p.scopes[key] = &scopeState{id: id, references: 1, entries: map[string]struct{}{}}
	return &Scope{pool: p, key: key}, nil
}

// OpenSharedScope acquires one handle to a stable cache partition. It is kept
// for compatibility with callers that explicitly share one partition scope;
// generation runtimes should use OpenScope with distinct RuntimeID values.
func (p *Pool) OpenSharedScope(id ScopeID) (*Scope, error) {
	if p == nil {
		return nil, fmt.Errorf("result cache pool is required")
	}
	if id.RuntimeID == "" {
		return nil, fmt.Errorf("result cache runtime ID is required")
	}
	if id.PartitionID == "" {
		id.PartitionID = id.RuntimeID
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
		existing.references++
		return &Scope{pool: p, key: key}, nil
	}
	p.scopes[key] = &scopeState{id: id, shared: true, references: 1, entries: map[string]struct{}{}}
	return &Scope{pool: p, key: key}, nil
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

func entryCompositeLocked(state *scopeState, key string) string {
	return state.id.PartitionID + "\x00" + key
}

// LookupArrow returns an independently retained lease. Eviction, invalidation,
// or scope closure can remove the cache's reference without invalidating it.
func (s *Scope) LookupArrow(key string) (*EntryLease, Token, bool, error) {
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
	element := p.entries[entryCompositeLocked(state, key)]
	if element == nil {
		return nil, state.generation, false, nil
	}
	e := element.Value.(entry)
	if e.arrowResult == nil {
		return nil, state.generation, false, nil
	}
	lease, err := e.arrowResult.Acquire()
	if err != nil {
		return nil, state.generation, false, err
	}
	p.lru.MoveToFront(element)
	return &EntryLease{data: lease, metadata: cloneMetadata(e.metadata)}, state.generation, true, nil
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
	element := p.entries[entryCompositeLocked(state, key)]
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
	if bytes > p.limits.PartitionBytes || bytes > p.limits.NodeBytes {
		p.stores[StoreOversized]++
		return StoreOversized
	}
	hold, err := result.Acquire()
	if err != nil {
		p.stores[StoreClosed]++
		return StoreClosed
	}
	partition := state.id.PartitionID
	composite := partition + "\x00" + key
	if old := p.entries[composite]; old != nil {
		p.removeLocked(old, "")
	}
	e := entry{composite: composite, key: key, scope: s.key, partition: partition, arrowResult: result, arrowHold: hold, metadata: cloneMetadata(metadata), bytes: bytes}
	element := p.lru.PushFront(e)
	p.entries[composite] = element
	state.entries[composite] = struct{}{}
	usage := p.partitionUsageLocked(partition)
	usage.entries++
	usage.bytes += bytes
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
	if bytes > p.limits.PartitionBytes || bytes > p.limits.NodeBytes {
		p.stores[StoreOversized]++
		return StoreOversized
	}
	partition := state.id.PartitionID
	composite := partition + "\x00" + key
	if old := p.entries[composite]; old != nil {
		p.removeLocked(old, "")
	}
	stored := make([]byte, len(value))
	copy(stored, value)
	e := entry{composite: composite, key: key, scope: s.key, partition: partition, byteValue: stored, bytes: bytes}
	element := p.lru.PushFront(e)
	p.entries[composite] = element
	state.entries[composite] = struct{}{}
	usage := p.partitionUsageLocked(partition)
	usage.entries++
	usage.bytes += bytes
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
	p.removeLocked(p.entries[entryCompositeLocked(state, key)], "")
}

func (p *Pool) enforceLocked(state *scopeState) {
	partition := state.id.PartitionID
	usage := p.partitionUsageLocked(partition)
	for usage.entries > p.limits.PartitionEntries || usage.bytes > p.limits.PartitionBytes {
		p.removeLocked(p.oldestLocked(func(e entry) bool { return e.partition == partition }), ConstraintPartition)
	}
	for len(p.entries) > p.limits.NodeEntries || p.bytes > p.limits.NodeBytes {
		p.removeLocked(p.lru.Back(), ConstraintNode)
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
func (p *Pool) removeLocked(element *list.Element, constraint Constraint) {
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
	}
	if usage := p.partitions[e.partition]; usage != nil {
		usage.entries--
		usage.bytes -= e.bytes
		if usage.entries <= 0 {
			delete(p.partitions, e.partition)
		}
	}
	if constraint != "" {
		p.evictions[constraint]++
	}
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
func (p *Pool) partitionUsageLocked(partition string) *usage {
	if value := p.partitions[partition]; value != nil {
		return value
	}
	value := &usage{}
	p.partitions[partition] = value
	return value
}

// Fence advances this scope's write generation without evicting stable
// partition entries. A subsequent store using the prior token is rejected.
func (s *Scope) Fence() {
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
	state.generation++
}

// Clear explicitly removes every retained entry in this stable partition and
// advances its generation.
func (s *Scope) Clear() {
	if s == nil || s.pool == nil {
		return
	}
	p := s.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.scopes[s.key]
	if state == nil || state.closed {
		return
	}
	partition := state.id.PartitionID
	for element := p.lru.Back(); element != nil; {
		previous := element.Prev()
		if element.Value.(entry).partition == partition {
			p.removeLocked(element, "")
		}
		element = previous
	}
	state.generation++
}

// Invalidate is the destructive legacy spelling of Clear.
func (s *Scope) Invalidate() { s.Clear() }

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
	if state.shared && state.references > 1 {
		state.references--
		return nil
	}
	if state.shared {
		partition := state.id.PartitionID
		for element := p.lru.Back(); element != nil; {
			previous := element.Prev()
			if element.Value.(entry).partition == partition {
				p.removeLocked(element, "")
			}
			element = previous
		}
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
	result := Snapshot{Entries: len(p.entries), Bytes: p.bytes, Scopes: map[string]ScopeSnapshot{}, Evictions: map[Constraint]uint64{}, Stores: map[StoreOutcome]uint64{}}
	for key, state := range p.scopes {
		usage := p.partitions[state.id.PartitionID]
		var entries int
		var bytes int64
		if usage != nil {
			entries, bytes = usage.entries, usage.bytes
		}
		result.Scopes[key] = ScopeSnapshot{ScopeID: state.id, Entries: entries, Bytes: bytes, Generation: state.generation}
	}
	for key, value := range p.evictions {
		result.Evictions[key] = value
	}
	for key, value := range p.stores {
		result.Stores[key] = value
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
		p.removeLocked(e, "")
		e = previous
	}
	for _, state := range p.scopes {
		state.closed = true
		state.generation++
	}
	p.scopes = map[string]*scopeState{}
	return nil
}
