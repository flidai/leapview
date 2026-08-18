package module

import (
	"context"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

type projectDefinitionRuntimeStub struct {
	definition projectmanifest.Project
	compiled   map[string]*semanticquery.CompiledModel
}

func (r projectDefinitionRuntimeStub) Close() error { return nil }
func (r projectDefinitionRuntimeStub) ProjectManifest() projectmanifest.Project {
	return r.definition
}
func (r projectDefinitionRuntimeStub) CompiledSemanticModel(modelID string) (*semanticquery.CompiledModel, bool) {
	compiled, ok := r.compiled[modelID]
	return compiled, ok && compiled != nil
}

type projectDefinitionProviderStub struct {
	lease        *projectDefinitionLeaseStub
	compiled     map[string]*semanticquery.CompiledModel
	definition   projectmanifest.Project
	acquisitions int
}

func (p *projectDefinitionProviderStub) Acquire(context.Context) (projectruntime.Lease, error) {
	p.acquisitions++
	definition := p.definition
	if definition.ID == "" {
		definition = projectmanifest.Project{ID: "project:demo", Title: "Demo"}
	}
	p.lease = &projectDefinitionLeaseStub{runtime: projectDefinitionRuntimeStub{
		definition: definition, compiled: p.compiled,
	}}
	return p.lease, nil
}

type projectDefinitionLeaseStub struct {
	runtime  projectruntime.Runtime
	released bool
}

func (l *projectDefinitionLeaseStub) Runtime() projectruntime.Runtime { return l.runtime }
func (l *projectDefinitionLeaseStub) Identity() projectgraph.ServingIdentity {
	return projectgraph.ServingIdentity{}
}
func (l *projectDefinitionLeaseStub) Release() { l.released = true }

func TestActiveProjectDefinitionReaderPinsAndReleasesRuntime(t *testing.T) {
	provider := &projectDefinitionProviderStub{}
	reader := NewActiveProjectDefinitionReader(provider)
	definition, compiled, err := reader.ProjectDefinitionSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if definition.ID != "project:demo" || definition.Title != "Demo" {
		t.Fatalf("definition = %#v", definition)
	}
	if len(compiled) != 0 {
		t.Fatalf("compiled models = %#v, want empty", compiled)
	}
	if provider.lease == nil || !provider.lease.released {
		t.Fatal("active runtime lease was not released")
	}
	if provider.acquisitions != 1 {
		t.Fatalf("acquisitions = %d, want one", provider.acquisitions)
	}
}

func TestActiveProjectDefinitionReaderSnapshotReadsCompiledModelPort(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {Model: "orders"},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName:   "orders",
				GrainEntity: "order",
				Entities: map[string]semanticmodel.ModelEntitySpec{
					"order": {Type: "primary", Fields: []string{"order_id"}},
				},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id": {Datatype: semanticmodel.DataTypeInteger},
				},
			},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count": {
				Type: "aggregate", Dataset: "orders", Aggregation: "count",
				Input: &semanticmodel.MetricInput{Field: "orders.order_id"},
			},
		},
	}
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	if !planner.CompiledModel().MatchesModel(model) {
		t.Fatalf("planner fingerprint mismatch: got %q want %q", planner.CompiledModel().SourceFingerprint(), semanticquery.SemanticModelFingerprint(model))
	}
	provider := &projectDefinitionProviderStub{compiled: map[string]*semanticquery.CompiledModel{"sales": planner.CompiledModel()}, definition: projectmanifest.Project{ID: "project:demo", SemanticModels: map[string]*semanticmodel.Model{"sales": model}}}
	reader := NewActiveProjectDefinitionReader(provider)
	definition, compiled, err := reader.ProjectDefinitionSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if definition.ID != "project:demo" {
		t.Fatalf("definition = %#v", definition)
	}
	if compiled["sales"] == nil {
		t.Fatalf("compiled model = %#v", compiled["sales"])
	}
	if provider.lease == nil || !provider.lease.released {
		t.Fatal("active runtime lease was not released")
	}
	if provider.acquisitions != 1 {
		t.Fatalf("acquisitions = %d, want one", provider.acquisitions)
	}
}

func TestActiveProjectDefinitionReaderSnapshotRejectsMismatchedPlanner(t *testing.T) {
	model := &semanticmodel.Model{
		Name:     "sales",
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Tables: map[string]semanticmodel.Table{"orders": {
			ModelName: "orders", GrainEntity: "order",
			Entities:   map[string]semanticmodel.ModelEntitySpec{"order": {Type: "primary", Fields: []string{"order_id"}}},
			Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Datatype: semanticmodel.DataTypeInteger}},
		}},
		Metrics: map[string]semanticmodel.Metric{"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}}},
	}
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	other := model.ExecutionSnapshot()
	other.Metrics["order_count"] = semanticmodel.Metric{Type: "aggregate", Dataset: "orders", Aggregation: "count_distinct", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}}
	provider := &projectDefinitionProviderStub{
		compiled:   map[string]*semanticquery.CompiledModel{"sales": planner.CompiledModel(), "marketing": planner.CompiledModel()},
		definition: projectmanifest.Project{ID: "project:demo", SemanticModels: map[string]*semanticmodel.Model{"sales": model, "marketing": other}},
	}
	reader := NewActiveProjectDefinitionReader(provider)
	if _, _, err := reader.ProjectDefinitionSnapshot(t.Context()); err == nil {
		t.Fatal("snapshot succeeded with planner compiled from a different semantic model")
	}
	if provider.acquisitions != 1 || provider.lease == nil || !provider.lease.released {
		t.Fatalf("lease lifecycle = acquisitions:%d lease:%#v, want one acquire/release", provider.acquisitions, provider.lease)
	}
}
