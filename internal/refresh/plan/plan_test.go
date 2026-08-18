package plan

import (
	"reflect"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/refresh/artifact"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

func TestForPipelineOrdersDependenciesBeforeDependents(t *testing.T) {
	definition := &artifact.Definition{
		Models: map[string]*semanticmodel.Model{
			"sales": {
				Tables: map[string]semanticmodel.Table{
					"orders":    {ModelName: "orders", ModelDependencies: []string{"customers"}},
					"customers": {ModelName: "customers"},
				},
				Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}, "customers": {Model: "customers"}},
			},
		},
		Pipelines: map[string]refreshschedule.Definition{
			"daily": {ID: "daily", SemanticModelID: "sales"},
		},
	}

	got, err := ForPipeline(definition, projectgraph.ResourceID("project_acme"), projectgraph.ResourceID("daily"))
	if err != nil {
		t.Fatalf("plan refresh pipeline: %v", err)
	}
	if got.TargetType != "refresh_pipeline" || got.TargetID != "daily" || got.SemanticModelID != "sales" {
		t.Fatalf("unexpected plan identity: %#v", got)
	}
	if want := []string{"customers", "orders"}; !reflect.DeepEqual(got.Tables, want) {
		t.Fatalf("tables = %#v, want %#v", got.Tables, want)
	}
	if !reflect.DeepEqual(got.DependencyTables, got.Tables) {
		t.Fatalf("dependency tables = %#v, want %#v", got.DependencyTables, got.Tables)
	}
}

func TestForPipelineRejectsDependencyCycles(t *testing.T) {
	definition := &artifact.Definition{
		Models: map[string]*semanticmodel.Model{
			"sales": {
				Tables: map[string]semanticmodel.Table{
					"orders":    {ModelDependencies: []string{"customers"}},
					"customers": {ModelDependencies: []string{"orders"}},
				},
			},
		},
		Pipelines: map[string]refreshschedule.Definition{
			"daily": {ID: "daily", SemanticModelID: "sales"},
		},
	}

	if _, err := ForPipeline(definition, projectgraph.ResourceID("project_acme"), projectgraph.ResourceID("daily")); err == nil {
		t.Fatal("expected dependency cycle to be rejected")
	}
}

func TestForPipelineMaterializesDatasetAliasesOnceByModelName(t *testing.T) {
	definition := &artifact.Definition{
		Models: map[string]*semanticmodel.Model{
			"sales": {
				Tables: map[string]semanticmodel.Table{
					"orders":    {ModelName: "sales_orders"},
					"purchases": {ModelName: "sales_orders"},
					"summary":   {ModelName: "sales_summary", ModelDependencies: []string{"sales_orders"}},
				},
				Datasets: map[string]semanticmodel.SemanticDatasetSpec{
					"orders":    {Model: "sales_orders"},
					"purchases": {Model: "sales_orders"},
					"summary":   {Model: "sales_summary"},
				},
			},
		},
		Pipelines: map[string]refreshschedule.Definition{
			"daily": {ID: "daily", SemanticModelID: "sales"},
		},
	}
	got, err := ForPipeline(definition, projectgraph.ResourceID("project_acme"), projectgraph.ResourceID("daily"))
	if err != nil {
		t.Fatalf("plan refresh pipeline: %v", err)
	}
	if want := []string{"sales_orders", "sales_summary"}; !reflect.DeepEqual(got.Tables, want) {
		t.Fatalf("tables = %#v, want %#v", got.Tables, want)
	}
}

func TestForPipelineResolvesDistinctAliasPhysicalDependencies(t *testing.T) {
	definition := &artifact.Definition{
		Models: map[string]*semanticmodel.Model{
			"sales": {
				Tables: map[string]semanticmodel.Table{
					"orders_alias":  {ModelName: "sales_orders"},
					"summary_alias": {ModelName: "sales_summary", ModelDependencies: []string{"sales_orders"}},
				},
				Datasets: map[string]semanticmodel.SemanticDatasetSpec{
					"orders_alias":  {Model: "sales_orders"},
					"summary_alias": {Model: "sales_summary"},
				},
			},
		},
		Pipelines: map[string]refreshschedule.Definition{
			"daily": {ID: "daily", SemanticModelID: "sales"},
		},
	}
	got, err := ForPipeline(definition, projectgraph.ResourceID("project_acme"), projectgraph.ResourceID("daily"))
	if err != nil {
		t.Fatalf("plan refresh pipeline: %v", err)
	}
	if want := []string{"sales_orders", "sales_summary"}; !reflect.DeepEqual(got.Tables, want) {
		t.Fatalf("tables = %#v, want %#v", got.Tables, want)
	}
}

func TestForPipelineRejectsUnknownModelDependency(t *testing.T) {
	definition := &artifact.Definition{
		Models: map[string]*semanticmodel.Model{
			"sales": {
				Tables: map[string]semanticmodel.Table{
					"orders_alias": {ModelName: "sales_orders", ModelDependencies: []string{"missing_model"}},
				},
				Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders_alias": {Model: "sales_orders"}},
			},
		},
		Pipelines: map[string]refreshschedule.Definition{
			"daily": {ID: "daily", SemanticModelID: "sales"},
		},
	}
	if _, err := ForPipeline(definition, projectgraph.ResourceID("project_acme"), projectgraph.ResourceID("daily")); err == nil || !strings.Contains(err.Error(), "unknown model dependency") {
		t.Fatalf("plan refresh pipeline error = %v, want unknown dependency", err)
	}
}
