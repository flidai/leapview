package app

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmodule "github.com/flidai/leapview/internal/project/module"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

type explorationModelRuntimeStub struct {
	identity      projectgraph.ServingIdentity
	modelID       string
	model         *semanticmodel.Model
	compiled      *semanticquery.CompiledModel
	modelReads    []string
	compiledReads []string
}

func (r *explorationModelRuntimeStub) Close() error                           { return nil }
func (r *explorationModelRuntimeStub) Identity() projectgraph.ServingIdentity { return r.identity }
func (r *explorationModelRuntimeStub) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	r.modelReads = append(r.modelReads, modelID)
	return r.model, modelID == r.modelID && r.model != nil
}
func (r *explorationModelRuntimeStub) CompiledSemanticModel(modelID string) (*semanticquery.CompiledModel, bool) {
	r.compiledReads = append(r.compiledReads, modelID)
	return r.compiled, modelID == r.modelID && r.compiled != nil
}

type explorationModelLeaseStub struct {
	runtime  projectruntime.Runtime
	identity projectgraph.ServingIdentity
	snapshot accesssnapshot.AuthorizationSnapshot
	released bool
}

func (l *explorationModelLeaseStub) Runtime() projectruntime.Runtime        { return l.runtime }
func (l *explorationModelLeaseStub) Identity() projectgraph.ServingIdentity { return l.identity }
func (l *explorationModelLeaseStub) Release()                               { l.released = true }
func (l *explorationModelLeaseStub) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return l.snapshot
}

type explorationModelProviderStub struct {
	lease        *explorationModelLeaseStub
	acquisitions int
}

func (p *explorationModelProviderStub) Acquire(context.Context) (projectruntime.Lease, error) {
	p.acquisitions++
	return p.lease, nil
}

func explorationModelFixture(t *testing.T, allowed bool) (*explorationModelProviderStub, *explorationModelRuntimeStub, projectmodule.AuthorizationSubjects) {
	t.Helper()
	model := &semanticmodel.Model{
		Name:     "sales",
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Tables: map[string]semanticmodel.Table{"orders": {
			ModelName: "orders", GrainEntity: "order",
			Entities:   map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"order_id"}}},
			Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Datatype: semanticmodel.DataTypeInteger}},
		}},
		Metrics: map[string]semanticmodel.Metric{"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}}},
	}
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "semantic:sales", Kind: projectgraph.KindSemanticModel, Name: "sales"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project:demo", "production", "generation:test")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := access.NewSubjectRef(access.SubjectKindPrincipal, "principal:alice")
	if err != nil {
		t.Fatal(err)
	}
	var grants []accesssnapshot.Grant
	if allowed {
		resource, err := access.NewResourceRef("semantic:sales", projectgraph.KindSemanticModel)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := access.NewCanonicalGrant(graph, principal, resource, access.CapabilityResourceUse)
		if err != nil {
			t.Fatal(err)
		}
		grants = []accesssnapshot.Grant{{ID: "grant_use", Canonical: canonical}}
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, grants, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &explorationModelRuntimeStub{identity: identity, modelID: "semantic:sales", model: model, compiled: planner.CompiledModel()}
	lease := &explorationModelLeaseStub{runtime: runtime, identity: identity, snapshot: snapshot}
	provider := &explorationModelProviderStub{lease: lease}
	subjects := func(_ context.Context, actorID string) ([]access.SubjectRef, error) {
		if actorID != "principal:alice" {
			return nil, nil
		}
		return []access.SubjectRef{principal}, nil
	}
	return provider, runtime, subjects
}

func TestExplorationModelProviderDeniesBeforeSelectedModelRead(t *testing.T) {
	provider, runtime, subjects := explorationModelFixture(t, false)
	callback := explorationModelProvider(provider, subjects)
	model, compiled, err := callback(t.Context(), "project:demo", "principal:alice", "semantic:sales", "generation:test")
	if err == nil || model != nil || compiled != nil {
		t.Fatalf("denied callback = model:%#v compiled:%#v err:%v", model, compiled, err)
	}
	if len(runtime.modelReads) != 0 || len(runtime.compiledReads) != 0 || provider.acquisitions != 1 || !provider.lease.released {
		t.Fatalf("denied reads/lifecycle = model:%v compiled:%v acquire:%d released:%v", runtime.modelReads, runtime.compiledReads, provider.acquisitions, provider.lease.released)
	}
}

func TestExplorationModelProviderReturnsSelectedCurrentModelAndPlanner(t *testing.T) {
	provider, runtime, subjects := explorationModelFixture(t, true)
	callback := explorationModelProvider(provider, subjects)
	model, compiled, err := callback(t.Context(), "project:demo", "principal:alice", "semantic:sales", "generation:test")
	if err != nil {
		t.Fatal(err)
	}
	if model == nil || compiled == nil || !compiled.MatchesModel(model) {
		t.Fatalf("authorized callback = model:%#v compiled:%#v", model, compiled)
	}
	if len(runtime.modelReads) != 1 || runtime.modelReads[0] != "semantic:sales" || len(runtime.compiledReads) != 1 || runtime.compiledReads[0] != "semantic:sales" {
		t.Fatalf("selected reads = model:%v compiled:%v; want one selected model read", runtime.modelReads, runtime.compiledReads)
	}
	if provider.acquisitions != 1 || !provider.lease.released {
		t.Fatalf("lease lifecycle = acquire:%d released:%v", provider.acquisitions, provider.lease.released)
	}
}

func TestExplorationModelProviderRejectsServingSnapshotMismatch(t *testing.T) {
	provider, runtime, subjects := explorationModelFixture(t, true)
	callback := explorationModelProvider(provider, subjects)
	model, compiled, err := callback(t.Context(), "project:demo", "principal:alice", "semantic:sales", "generation:stale")
	if err == nil || model != nil || compiled != nil {
		t.Fatalf("stale callback = model:%#v compiled:%#v err:%v", model, compiled, err)
	}
	if len(runtime.modelReads) != 1 || runtime.modelReads[0] != "semantic:sales" || len(runtime.compiledReads) != 1 || runtime.compiledReads[0] != "semantic:sales" {
		t.Fatalf("stale selected reads = model:%v compiled:%v", runtime.modelReads, runtime.compiledReads)
	}
	if provider.acquisitions != 1 || !provider.lease.released {
		t.Fatalf("stale lease lifecycle = acquire:%d released:%v", provider.acquisitions, provider.lease.released)
	}
}
