package stream

import (
	"context"
	"errors"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"sort"
	"sync"
	"time"
)

const DefaultRegistryMaxEntries = 1024

// ErrRegistryCapacity is returned by EnsureWithError when only active stream
// entries remain and the registry cannot admit another orphan command entry.
var ErrRegistryCapacity = errors.New("dashboard stream registry capacity reached")

// Registry isolates coordinators by the full rendered-page stream ID. The
// stream ID includes streamInstanceId, so browser tabs sharing a client cookie
// cannot supersede one another.
type Registry struct {
	mu           sync.Mutex
	coordinators map[string]*Coordinator
	bindings     map[string]registryBinding
	orphanTTL    time.Duration
	maxEntries   int
	order        map[string]uint64
	sequence     uint64
	orphan       map[string]bool
}

type registryBinding struct {
	projectID   projectgraph.ResourceID
	environment string
	modelID     string
	publication string
	refresh     func()
}

// SemanticModelRefreshTarget identifies the stream and publication scope that
// must receive a semantic-model refresh status patch.
type SemanticModelRefreshTarget struct {
	StreamID    string
	Publication string
}

func NewRegistry() *Registry {
	return NewRegistryWithTTL(5 * time.Minute)

}

func NewRegistryWithTTL(orphanTTL time.Duration) *Registry {
	return NewRegistryWithLimits(orphanTTL, DefaultRegistryMaxEntries)
}

// NewRegistryWithLimits creates a registry with a bounded number of stream
// entries. Ensure evicts the oldest orphan deterministically before rejecting
// a request when all retained entries are active.
func NewRegistryWithLimits(orphanTTL time.Duration, maxEntries int) *Registry {
	if orphanTTL <= 0 {
		orphanTTL = 5 * time.Minute
	}
	if maxEntries <= 0 {
		maxEntries = DefaultRegistryMaxEntries
	}
	return &Registry{
		coordinators: map[string]*Coordinator{}, bindings: map[string]registryBinding{},
		orphanTTL: orphanTTL, maxEntries: maxEntries, order: map[string]uint64{}, orphan: map[string]bool{},
	}
}

func (r *Registry) Open(streamID string, parent context.Context, publish EventPublisher) (*Coordinator, func()) {
	coordinator, closeCoordinator, _ := r.OpenWithError(streamID, parent, publish)
	return coordinator, closeCoordinator
}

// OpenWithError admits a live Updates stream only when capacity is available.
// It evicts the oldest orphan deterministically; active streams are never
// evicted to make room for another live stream.
func (r *Registry) OpenWithError(streamID string, parent context.Context, publish EventPublisher) (*Coordinator, func(), error) {
	coordinator := NewCoordinator(parent, publish)
	var previous, evicted *Coordinator
	r.mu.Lock()
	previous = r.coordinators[streamID]
	if previous == nil && len(r.coordinators) >= r.maxEntries {
		evictedID, candidate := r.oldestLocked(true)
		if candidate == nil && len(r.coordinators) >= r.maxEntries {
			r.mu.Unlock()
			coordinator.CloseWithReason("registry_capacity")
			return nil, func() {}, ErrRegistryCapacity
		}
		if candidate != nil {
			evicted = candidate
			delete(r.coordinators, evictedID)
			delete(r.bindings, evictedID)
			delete(r.order, evictedID)
			delete(r.orphan, evictedID)
		}
	}
	r.coordinators[streamID] = coordinator
	r.sequence++
	r.order[streamID] = r.sequence
	r.orphan[streamID] = false
	r.mu.Unlock()
	if previous != nil {
		previous.CloseWithReason("stream_replaced")
	}
	if evicted != nil {
		evicted.CloseWithReason("registry_capacity")
	}
	return coordinator, func() {
		r.mu.Lock()
		if r.coordinators[streamID] == coordinator {
			delete(r.coordinators, streamID)
			delete(r.bindings, streamID)
			delete(r.order, streamID)
			delete(r.orphan, streamID)
		}
		r.mu.Unlock()
		coordinator.CloseWithReason("disconnect")
	}, nil
}

func (r *Registry) Bind(streamID string, projectID projectgraph.ResourceID, environment, modelID string, refresh func()) {
	r.BindForPublication(streamID, projectID, environment, modelID, "", refresh)
}

// BindForPublication records the immutable public publication scope for a
// public stream. An empty scope is the private dashboard broker.
func (r *Registry) BindForPublication(streamID string, projectID projectgraph.ResourceID, environment, modelID, publication string, refresh func()) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.coordinators[streamID] != nil {
		r.bindings[streamID] = registryBinding{projectID: projectID, environment: environment, modelID: modelID, publication: publication, refresh: refresh}
	}
	r.mu.Unlock()
}

func (r *Registry) RefreshSemanticModel(projectID projectgraph.ResourceID, environment, modelID string) []string {
	targets := r.RefreshSemanticModelTargets(projectID, environment, modelID)
	streamIDs := make([]string, 0, len(targets))
	for _, target := range targets {
		streamIDs = append(streamIDs, target.StreamID)
	}
	return streamIDs
}

func (r *Registry) RefreshSemanticModelTargets(projectID projectgraph.ResourceID, environment, modelID string) []SemanticModelRefreshTarget {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	type target struct {
		streamID    string
		publication string
		refresh     func()
	}
	var targets []target
	for streamID, binding := range r.bindings {
		if binding.projectID == projectID && binding.environment == environment && binding.modelID == modelID {
			targets = append(targets, target{streamID: streamID, publication: binding.publication, refresh: binding.refresh})
		}
	}
	r.mu.Unlock()
	sort.Slice(targets, func(i, j int) bool { return targets[i].streamID < targets[j].streamID })
	refreshTargets := make([]SemanticModelRefreshTarget, 0, len(targets))
	for _, target := range targets {
		refreshTargets = append(refreshTargets, SemanticModelRefreshTarget{StreamID: target.streamID, Publication: target.publication})
		if target.refresh != nil {
			target.refresh()
		}
	}
	return refreshTargets
}

func (r *Registry) Get(streamID string) (*Coordinator, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	coordinator, ok := r.coordinators[streamID]
	return coordinator, ok
}

func (r *Registry) Ensure(streamID string, parent context.Context, publish EventPublisher) *Coordinator {
	coordinator, _ := r.EnsureWithError(streamID, parent, publish)
	return coordinator
}

// EnsureWithError admits an orphan coordinator only while capacity is
// available, evicting the oldest orphan first. Callers that accept untrusted
// stream IDs should use this result to fail closed on capacity exhaustion.
func (r *Registry) EnsureWithError(streamID string, parent context.Context, publish EventPublisher) (*Coordinator, error) {
	if coordinator, ok := r.Get(streamID); ok {
		return coordinator, nil
	}
	coordinator := NewCoordinator(parent, publish)
	var evicted *Coordinator
	var evictedID string
	r.mu.Lock()
	if existing := r.coordinators[streamID]; existing != nil {
		r.mu.Unlock()
		coordinator.Close()
		return existing, nil
	}
	if len(r.coordinators) >= r.maxEntries {
		evictedID, evicted = r.oldestLocked(true)
		if evicted == nil {
			r.mu.Unlock()
			coordinator.Close()
			return nil, ErrRegistryCapacity
		}
		delete(r.coordinators, evictedID)
		delete(r.bindings, evictedID)
		delete(r.order, evictedID)
		delete(r.orphan, evictedID)
	}
	r.coordinators[streamID] = coordinator
	r.sequence++
	r.order[streamID] = r.sequence
	r.orphan[streamID] = true
	r.mu.Unlock()
	if evicted != nil {
		evicted.CloseWithReason("registry_capacity")
	}
	time.AfterFunc(r.orphanTTL, func() { r.expire(streamID, coordinator) })
	return coordinator, nil
}

func (r *Registry) expire(streamID string, coordinator *Coordinator) {
	r.mu.Lock()
	if r.coordinators[streamID] != coordinator {
		r.mu.Unlock()
		return
	}
	delete(r.coordinators, streamID)
	delete(r.bindings, streamID)
	delete(r.order, streamID)
	delete(r.orphan, streamID)
	r.mu.Unlock()
	coordinator.CloseWithReason("ttl_expired")
}

func (r *Registry) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	coordinators := make([]*Coordinator, 0, len(r.coordinators))
	for _, coordinator := range r.coordinators {
		coordinators = append(coordinators, coordinator)
	}
	r.coordinators = map[string]*Coordinator{}
	r.bindings = map[string]registryBinding{}
	r.order = map[string]uint64{}
	r.orphan = map[string]bool{}
	r.mu.Unlock()
	for _, coordinator := range coordinators {
		coordinator.CloseWithReason("shutdown")
	}
}

// Len reports the number of retained stream coordinators.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.coordinators)
}

func (r *Registry) oldestLocked(orphanOnly bool) (string, *Coordinator) {
	var selected string
	var sequence uint64
	for streamID := range r.coordinators {
		if orphanOnly && !r.orphan[streamID] {
			continue
		}
		candidateSequence := r.order[streamID]
		if selected == "" || candidateSequence < sequence || candidateSequence == sequence && streamID < selected {
			selected, sequence = streamID, candidateSequence
		}
	}
	if selected == "" {
		return "", nil
	}
	return selected, r.coordinators[selected]
}
