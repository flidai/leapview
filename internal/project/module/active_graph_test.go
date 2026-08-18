package module

import (
	"context"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type activeGraphProviderStub struct {
	lease *activeGraphLeaseStub
}

func (p *activeGraphProviderStub) Acquire(context.Context) (projectruntime.Lease, error) {
	p.lease = &activeGraphLeaseStub{identity: projectgraph.ServingIdentity{ProjectID: "project:demo", Environment: "dev", GenerationID: "state:active"}}
	return p.lease, nil
}

type activeGraphLeaseStub struct {
	identity projectgraph.ServingIdentity
	released bool
}

func (l *activeGraphLeaseStub) Runtime() projectruntime.Runtime        { return activeGraphRuntimeStub{} }
func (l *activeGraphLeaseStub) Identity() projectgraph.ServingIdentity { return l.identity }
func (l *activeGraphLeaseStub) Release()                               { l.released = true }

type activeGraphRuntimeStub struct{}

func (activeGraphRuntimeStub) Close() error { return nil }

type exactGraphStub struct {
	stateID servingstate.ID
}

func (s *exactGraphStub) ServingStateGraph(_ context.Context, _ projectgraph.ResourceID, _ string, stateID servingstate.ID) (servingstate.AssetGraph, bool, error) {
	s.stateID = stateID
	return servingstate.AssetGraph{Assets: []servingstate.Asset{{ID: "semantic-model:sales", ServingStateID: stateID}}}, true, nil
}

func TestActiveServingStateGraphReaderPinsExactRuntimeGeneration(t *testing.T) {
	provider := &activeGraphProviderStub{}
	graphs := &exactGraphStub{}
	reader := NewActiveServingStateGraphReader(provider, graphs)
	graph, ok, err := reader.ActiveServingStateGraph(t.Context(), "project:demo", "dev")
	if err != nil || !ok {
		t.Fatalf("graph = %#v, ok=%v, err=%v", graph, ok, err)
	}
	if graphs.stateID != "state:active" {
		t.Fatalf("exact state ID = %q, want state:active", graphs.stateID)
	}
	if provider.lease == nil || !provider.lease.released {
		t.Fatal("runtime lease was not released")
	}
}

func TestActiveServingStateGraphReaderRejectsScopeMismatch(t *testing.T) {
	provider := &activeGraphProviderStub{}
	reader := NewActiveServingStateGraphReader(provider, &exactGraphStub{})
	_, ok, err := reader.ActiveServingStateGraph(t.Context(), "project:other", "dev")
	if ok || err == nil {
		t.Fatalf("scope mismatch = ok=%v err=%v", ok, err)
	}
}
