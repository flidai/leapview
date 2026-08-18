package runtimefactory

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	"github.com/flidai/leapview/internal/dashboard/report"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

type compiledPlannerDataRuntime struct{ planner consumer.Planner }

func (r compiledPlannerDataRuntime) Query(context.Context, report.AggregateQuery) (report.QueryRows, error) {
	return nil, nil
}
func (r compiledPlannerDataRuntime) Rows(context.Context, report.RowQuery) (report.QueryRows, error) {
	return nil, nil
}
func (r compiledPlannerDataRuntime) Count(context.Context, report.CountQuery) (int, error) {
	return 0, nil
}
func (r compiledPlannerDataRuntime) Histogram(context.Context, report.RawValueQuery, int) ([]report.HistogramBin, error) {
	return nil, nil
}
func (r compiledPlannerDataRuntime) Distribution(context.Context, report.RawValueQuery, []report.QuerySort, int) (report.QueryRows, error) {
	return nil, nil
}
func (r compiledPlannerDataRuntime) ExecuteDataQuery(context.Context, dataquery.Query) (dataquery.Result, error) {
	return dataquery.Result{}, nil
}
func (r compiledPlannerDataRuntime) Refresh(context.Context) error { return nil }
func (r compiledPlannerDataRuntime) Close() error                  { return nil }
func (r compiledPlannerDataRuntime) LastRefresh() time.Time        { return time.Time{} }
func (r compiledPlannerDataRuntime) Planner() consumer.Planner     { return r.planner }

type compiledPlannerDataRuntimeFactory struct{ runtime dashboardruntime.DataRuntime }

func (f compiledPlannerDataRuntimeFactory) OpenDashboardProjectDataRuntimes(_ context.Context, definition dashboardruntime.ProjectDataRuntimeConfig) (map[projectgraph.ResourceID]dashboardruntime.DataRuntime, error) {
	models := definition.Definition.Models()
	result := make(map[projectgraph.ResourceID]dashboardruntime.DataRuntime, len(models))
	for id := range models {
		result[id] = f.runtime
	}
	return result, nil
}

func compiledPlannerTestModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name:     "sales",
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Tables: map[string]semanticmodel.Table{"orders": {
			ModelName: "orders", GrainEntity: "order",
			Entities:   map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"order_id"}}},
			Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Type: "number", Datatype: semanticmodel.DataTypeInteger}},
		}},
		Metrics: map[string]semanticmodel.Metric{
			"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}},
		},
	}
}

func TestProjectManifestReturnsDetachedCompiledDefinition(t *testing.T) {
	runtime := dashboardRuntimeWithGraph{projectManifest: projectmanifest.Project{
		ID: "project:demo",
		Models: map[string]semanticmodel.Table{
			"model:orders": {
				Entities:           map[string]semanticmodel.EntityDefinition{"order_id": {Type: "primary", Fields: []string{"order_id"}}},
				GrainEntity:        "order_id",
				SourceDependencies: []string{"orders"},
			},
		},
	}}

	first := runtime.ProjectManifest()
	model := first.Models["model:orders"]
	model.GrainEntity = "changed"
	model.Entities["order_id"] = semanticmodel.EntityDefinition{Type: "unique", Fields: []string{"changed"}}
	model.SourceDependencies[0] = "changed"
	first.Models["model:orders"] = model

	second := runtime.ProjectManifest()
	model = second.Models["model:orders"]
	if model.GrainEntity != "order_id" || !slices.Equal(model.GrainFields(), []string{"order_id"}) || model.SourceDependencies[0] != "orders" {
		t.Fatalf("project manifest aliases caller mutation: %#v", second.Models["model:orders"])
	}
}

func TestCompiledSemanticModelAdaptsActivationPlannerAndFailsClosed(t *testing.T) {
	model := compiledPlannerTestModel()
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := projectgraph.NewResourceID("project:demo")
	if err != nil {
		t.Fatal(err)
	}
	modelID, err := projectgraph.NewResourceID("model:sales")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := dashboardruntime.NewProjectDefinition(projectID, "Demo", "", map[projectgraph.ResourceID]*semanticmodel.Model{modelID: model}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := dashboardruntime.NewFromGeneration(context.Background(), "", compiledPlannerDataRuntimeFactory{runtime: compiledPlannerDataRuntime{planner: planner}}, projectgraph.ServingIdentity{ProjectID: projectID, Environment: "dev", GenerationID: "state-1"}, definition)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	runtime := dashboardRuntimeWithGraph{Service: service}
	compiled, ok := runtime.CompiledSemanticModel(modelID.String())
	if !ok || compiled == nil || compiled != planner.CompiledModel() {
		t.Fatalf("compiled semantic model = %p/%v, want activation planner %p/true", compiled, ok, planner.CompiledModel())
	}
	if _, ok := runtime.CompiledSemanticModel("model:missing"); ok {
		t.Fatal("unknown semantic model unexpectedly resolved")
	}
	if _, ok := (dashboardRuntimeWithGraph{}).CompiledSemanticModel(modelID.String()); ok {
		t.Fatal("runtime without dashboard service unexpectedly resolved a planner")
	}
}

func TestAuthoredDashboardSourcesRetainsDescriptiveDomain(t *testing.T) {
	projectID, err := projectgraph.NewResourceID("project:demo")
	if err != nil {
		t.Fatal(err)
	}
	dashboardID, err := projectgraph.NewResourceID("dashboard:sales")
	if err != nil {
		t.Fatal(err)
	}
	sources, err := authoredDashboardSources(projectmanifest.Project{
		DashboardSources: map[string]projectmanifest.DashboardSource{
			string(dashboardID): {
				Document: dashboardauthoring.Dashboard{ID: dashboardID},
				Metadata: projectmanifest.DashboardSourceMetadata{
					Name: "sales_dashboard", Domain: "revenue", Tags: []string{"finance"},
				},
			},
		},
	}, projectID)
	if err != nil {
		t.Fatal(err)
	}
	source, ok := sources[string(dashboardID)]
	if !ok {
		t.Fatal("authored dashboard source was not projected")
	}
	if source.Metadata.Domain != "revenue" {
		t.Fatalf("projected authored dashboard domain = %q, want revenue", source.Metadata.Domain)
	}
	if source.Metadata.Project != projectID {
		t.Fatalf("projected authored dashboard project = %q, want %q", source.Metadata.Project, projectID)
	}
}
