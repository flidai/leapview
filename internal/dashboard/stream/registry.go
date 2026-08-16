package stream

import (
	"context"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"sort"
	"sync"
	"time"
)

// Registry isolates coordinators by the full rendered-page stream ID. The
// stream ID includes streamInstanceId, so browser tabs sharing a client cookie
// cannot supersede one another.
type Registry struct {
	mu           sync.Mutex
	coordinators map[string]*Coordinator
	bindings     map[string]registryBinding
	orphanTTL    time.Duration
}

type registryBinding struct {
	projectID   projectgraph.ResourceID
	environment string
	modelID     string
	refresh     func()
}

func NewRegistry() *Registry {
	return NewRegistryWithTTL(5 * time.Minute)

}

func NewRegistryWithTTL(orphanTTL time.Duration) *Registry {
	if orphanTTL <= 0 {
		orphanTTL = 5 * time.Minute
	}
	return &Registry{coordinators: map[string]*Coordinator{}, bindings: map[string]registryBinding{}, orphanTTL: orphanTTL}
}

func (r *Registry) Open(streamID string, parent context.Context, publish EventPublisher) (*Coordinator, func()) {
	coordinator := NewCoordinator(parent, publish)
	r.mu.Lock()
	if previous := r.coordinators[streamID]; previous != nil {
		previous.CloseWithReason("stream_replaced")
	}
	r.coordinators[streamID] = coordinator
	r.mu.Unlock()
	return coordinator, func() {
		r.mu.Lock()
		if r.coordinators[streamID] == coordinator {
			delete(r.coordinators, streamID)
			delete(r.bindings, streamID)
		}
		r.mu.Unlock()
		coordinator.CloseWithReason("disconnect")
	}
}

func (r *Registry) Bind(streamID string, projectID projectgraph.ResourceID, environment, modelID string, refresh func()) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.coordinators[streamID] != nil {
		r.bindings[streamID] = registryBinding{projectID: projectID, environment: environment, modelID: modelID, refresh: refresh}
	}
	r.mu.Unlock()
}

func (r *Registry) RefreshSemanticModel(projectID projectgraph.ResourceID, environment, modelID string) []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	type target struct {
		streamID string
		refresh  func()
	}
	var targets []target
	for streamID, binding := range r.bindings {
		if binding.projectID == projectID && binding.environment == environment && binding.modelID == modelID {
			targets = append(targets, target{streamID: streamID, refresh: binding.refresh})
		}
	}
	r.mu.Unlock()
	sort.Slice(targets, func(i, j int) bool { return targets[i].streamID < targets[j].streamID })
	streamIDs := make([]string, 0, len(targets))
	for _, target := range targets {
		streamIDs = append(streamIDs, target.streamID)
		if target.refresh != nil {
			target.refresh()
		}
	}
	return streamIDs
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
	if coordinator, ok := r.Get(streamID); ok {
		return coordinator
	}
	coordinator := NewCoordinator(parent, publish)
	r.mu.Lock()
	if existing := r.coordinators[streamID]; existing != nil {
		r.mu.Unlock()
		coordinator.Close()
		return existing
	}
	r.coordinators[streamID] = coordinator
	r.mu.Unlock()
	time.AfterFunc(r.orphanTTL, func() { r.expire(streamID, coordinator) })
	return coordinator
}

func (r *Registry) expire(streamID string, coordinator *Coordinator) {
	r.mu.Lock()
	if r.coordinators[streamID] != coordinator {
		r.mu.Unlock()
		return
	}
	delete(r.coordinators, streamID)
	delete(r.bindings, streamID)
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
	r.mu.Unlock()
	for _, coordinator := range coordinators {
		coordinator.CloseWithReason("shutdown")
	}
}
