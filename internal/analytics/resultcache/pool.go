// Package resultcache owns node-wide retention and coalescing for governed
// analytical query results.
package resultcache

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/flidai/leapview/internal/analytics/arrowresult"
	"golang.org/x/sync/singleflight"
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
	mu           sync.Mutex
	limits       Limits
	closed       bool
	entries      map[string]*list.Element
	lru          *list.List
	scopes       map[string]*scopeState
	bytes        int64
	evictions    map[Constraint]uint64
	stores       map[StoreOutcome]uint64
	group        singleflight.Group
	arrowFlights map[string]*arrowFlight
}

type Scope struct {
	pool *Pool
	key  string
}
type scopeState struct {
	id         ScopeID
	generation Token
	closed     bool
	entries    map[string]struct{}
	usage      usage
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

type arrowFlight struct {
	done     chan struct{}
	waiters  int
	shared   bool
	complete bool
	value    ArrowFlightValue
	err      error
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
	if state := s.pool.scopes[s.key]; state != nil {
		return UsageSnapshot{Entries: state.usage.entries, Bytes: state.usage.bytes}
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
	return &Pool{limits: limits, entries: map[string]*list.Element{}, lru: list.New(), scopes: map[string]*scopeState{}, evictions: map[Constraint]uint64{}, stores: map[StoreOutcome]uint64{}, arrowFlights: map[string]*arrowFlight{}}, nil
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
	p.scopes[key] = &scopeState{id: id, entries: map[string]struct{}{}}
	return &Scope{pool: p, key: key}, nil
}

func (s *Scope) Generation() Token {
	if s == nil || s.pool == nil {
		return 0
	}
	s.pool.mu.Lock()
	defer s.pool.mu.Unlock()
	if state := s.pool.scopes[s.key]; state != nil {
		return state.generation
	}
	return 0
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
	state := p.scopes[s.key]
	if p.closed || state == nil || state.closed {
		return nil, 0, false, fmt.Errorf("result cache scope is closed")
	}
	element := p.entries[s.key+"\x00"+key]
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
	state := p.scopes[s.key]
	if p.closed || state == nil || state.closed {
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
	if s == nil || s.pool == nil || result == nil {
		return StoreClosed
	}
	p := s.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.scopes[s.key]
	if p.closed || state == nil || state.closed {
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
		p.removeLocked(old, "")
	}
	e := entry{composite: composite, key: key, scope: s.key, arrowResult: result, arrowHold: hold, metadata: cloneMetadata(metadata), bytes: bytes}
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

// StoreBytes copies and retains one immutable byte entry under the same
// runtime/node budgets as Arrow results.
func (s *Scope) StoreBytes(key string, token Token, value []byte) StoreOutcome {
	if s == nil || s.pool == nil || value == nil {
		return StoreClosed
	}
	p := s.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.scopes[s.key]
	if p.closed || state == nil || state.closed {
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
		p.removeLocked(old, "")
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
	state := p.scopes[s.key]
	if state == nil || state.closed {
		return
	}
	p.removeLocked(p.entries[s.key+"\x00"+key], "")
}

func (p *Pool) enforceLocked(state *scopeState) {
	for state.usage.entries > p.limits.RuntimeEntries || state.usage.bytes > p.limits.RuntimeBytes {
		p.removeLocked(p.oldestLocked(func(e entry) bool { return e.scope == scopeKey(state.id) }), ConstraintRuntime)
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
		state.usage.entries--
		state.usage.bytes -= e.bytes
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
func scopeKey(id ScopeID) string { return id.RuntimeID }

func (s *Scope) Invalidate() {
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
	for composite := range state.entries {
		p.removeLocked(p.entries[composite], "")
	}
	state.generation++
}

func (s *Scope) Close() error {
	if s == nil || s.pool == nil {
		return nil
	}
	p := s.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.scopes[s.key]
	if state == nil || state.closed {
		return nil
	}
	for composite := range state.entries {
		p.removeLocked(p.entries[composite], "")
	}
	state.closed = true
	state.generation++
	delete(p.scopes, s.key)
	return nil
}

type canceledFlight struct{ err error }

func (e canceledFlight) Error() string { return e.err.Error() }
func (e canceledFlight) Unwrap() error { return e.err }

// OwnerCanceled marks a coalesced execution whose owning context was canceled,
// allowing a still-live waiter to replace that flight.
func OwnerCanceled(err error) error { return canceledFlight{err: err} }

func (s *Scope) Coalesce(ctx context.Context, key string, execute func() (any, error)) (any, bool, error) {
	if s == nil || s.pool == nil {
		return nil, false, fmt.Errorf("result cache scope is required")
	}
	flightKey := s.key + "\x00" + key
	for {
		ch := s.pool.group.DoChan(flightKey, func() (any, error) {
			value, err := execute()
			if ownerErr := ctx.Err(); ownerErr != nil {
				return nil, canceledFlight{ownerErr}
			}
			return value, err
		})
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case call := <-ch:
			if call.Err != nil {
				var canceled canceledFlight
				if ctx.Err() == nil && errors.As(call.Err, &canceled) {
					continue
				}
				return nil, call.Shared, call.Err
			}
			return call.Val, call.Shared, nil
		}
	}
}

// CoalesceArrow runs one Arrow-producing execution and gives every live caller
// an independently retained lease. Canceled callers are removed without
// releasing buffers still needed by other waiters. If the owning execution was
// canceled, a live waiter starts a replacement flight.
func (s *Scope) CoalesceArrow(ctx context.Context, key string, execute func() (ArrowFlightValue, error)) (*ArrowFlightLease, ArrowFlightStatus, error) {
	if s == nil || s.pool == nil {
		return nil, ArrowFlightStatus{}, fmt.Errorf("result cache scope is required")
	}
	flightKey := s.key + "\x00" + key
	for {
		flight, owner := s.joinArrowFlight(flightKey, ctx, execute)
		select {
		case <-ctx.Done():
			s.leaveArrowFlight(flight)
			return nil, ArrowFlightStatus{}, ctx.Err()
		case <-flight.done:
		}

		s.pool.mu.Lock()
		flightErr := flight.err
		shared := flight.shared
		value := flight.value
		s.pool.mu.Unlock()
		if flightErr != nil {
			s.leaveArrowFlight(flight)
			var canceled canceledFlight
			if ctx.Err() == nil && errors.As(flightErr, &canceled) {
				continue
			}
			return nil, ArrowFlightStatus{Owner: owner, Shared: shared}, flightErr
		}
		if value.Data == nil {
			s.leaveArrowFlight(flight)
			return nil, ArrowFlightStatus{Owner: owner, Shared: shared}, fmt.Errorf("coalesced Arrow execution returned no data")
		}
		lease, err := value.Data.Acquire()
		s.leaveArrowFlight(flight)
		if err != nil {
			return nil, ArrowFlightStatus{Owner: owner, Shared: shared}, err
		}
		return &ArrowFlightLease{data: lease, metadata: cloneMetadata(value.Metadata), cached: value.Cached}, ArrowFlightStatus{Owner: owner, Shared: shared}, nil
	}
}

func (s *Scope) joinArrowFlight(key string, ctx context.Context, execute func() (ArrowFlightValue, error)) (*arrowFlight, bool) {
	p := s.pool
	p.mu.Lock()
	if existing := p.arrowFlights[key]; existing != nil {
		existing.waiters++
		existing.shared = true
		p.mu.Unlock()
		return existing, false
	}
	flight := &arrowFlight{done: make(chan struct{}), waiters: 1}
	p.arrowFlights[key] = flight
	p.mu.Unlock()
	go func() {
		value, err := execute()
		if ownerErr := ctx.Err(); ownerErr != nil {
			err = canceledFlight{ownerErr}
		}
		p.mu.Lock()
		flight.value, flight.err, flight.complete = value, err, true
		delete(p.arrowFlights, key)
		close(flight.done)
		release := flight.waiters == 0 && flight.value.Data != nil
		if release {
			flight.value.Data = nil
		}
		p.mu.Unlock()
		if release {
			value.Data.Release()
		}
	}()
	return flight, true
}

func (s *Scope) leaveArrowFlight(flight *arrowFlight) {
	if flight == nil {
		return
	}
	p := s.pool
	p.mu.Lock()
	flight.waiters--
	release := flight.waiters == 0 && flight.complete && flight.value.Data != nil
	var data *arrowresult.Lease
	if release {
		data = flight.value.Data
		flight.value.Data = nil
	}
	p.mu.Unlock()
	if data != nil {
		data.Release()
	}
}

func (p *Pool) Stats() Snapshot {
	if p == nil {
		return Snapshot{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	result := Snapshot{Entries: len(p.entries), Bytes: p.bytes, Scopes: map[string]ScopeSnapshot{}, Evictions: map[Constraint]uint64{}, Stores: map[StoreOutcome]uint64{}}
	for key, state := range p.scopes {
		result.Scopes[key] = ScopeSnapshot{ScopeID: state.id, Entries: state.usage.entries, Bytes: state.usage.bytes, Generation: state.generation}
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
