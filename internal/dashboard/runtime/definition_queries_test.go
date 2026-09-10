package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func definitionOverlayService(t *testing.T, modelName string, ready bool) (*Service, dashboarddefinition.Definition) {
	t.Helper()
	model := &semanticmodel.Model{Name: modelName}
	project := dashboarddefinition.Definition{
		ID: "project", Title: "Project", SemanticModel: "sales_model",
		Pages: []dashboard.Page{{ID: "project-page", Title: "Project Page"}},
	}
	projectID := projectgraph.ResourceID("project_1")
	definition, err := NewProjectDefinition(projectID, "Project", "", map[projectgraph.ResourceID]*semanticmodel.Model{"sales_model": model}, map[projectgraph.ResourceID]dashboarddefinition.Definition{"project": project})
	if err != nil {
		t.Fatal(err)
	}
	baseRuntime := &modelRuntime{model: model, ready: ready}
	if !ready {
		baseRuntime.missing = errors.New("setup required")
	}
	service := &Service{
		runtimes: map[projectgraph.ResourceID]*modelRuntime{"sales_model": baseRuntime},
		tiles:    newSpatialTileRegistry(),
	}
	service.catalog, err = NewCatalogService(&service.mu, definition)
	if err != nil {
		t.Fatal(err)
	}
	service.reports = &ReportService{projectID: projectID, models: definition.Models(), dashboards: definition.Dashboards(), catalog: service.catalog.catalog, defaultID: "project"}
	service.filters = &FilterService{}
	service.visualizations = &VisualizationDataService{mu: &service.mu, reports: service.reports, runtimes: service.runtimes, filters: service.filters, tiles: service.tiles}
	service.snapshots = &SnapshotService{mu: &service.mu, reports: service.reports, runtimes: service.runtimes, filters: service.filters, visualizations: service.visualizations}
	service.queries = &QueryService{snapshots: service.snapshots, visualizations: service.visualizations}

	published := dashboarddefinition.Definition{
		ID: "published", Title: "Published", SemanticModel: "sales_model",
		Pages: []dashboard.Page{{ID: "published-page", Title: "Published Page"}},
	}
	return service, published
}

func TestDefinitionServiceOverlayPreservesBaseWorkspaceAndExecutesArbitraryPage(t *testing.T) {
	service, published := definitionOverlayService(t, "sales_model", true)
	patch, err := service.QueryDashboardPageForDefinition(context.Background(), published, "published-page", dashboard.Filters{})
	if err != nil {
		t.Fatal(err)
	}
	if patch.Status.Error != "" {
		t.Fatalf("patch status error = %q", patch.Status.Error)
	}
	if _, err := service.reports.Resolve(projectgraph.ResourceID("published")); err == nil {
		t.Fatal("published overlay leaked into base workspace")
	}
	pages := service.Pages("project")
	if len(pages) != 1 || pages[0].ID != "project-page" {
		t.Fatalf("base pages changed: %#v", pages)
	}
}

func TestDefinitionServiceOverlayMetadataWorksBeforeDataReady(t *testing.T) {
	service, published := definitionOverlayService(t, "sales_model", false)
	if got := service.PagesForDefinition(published); len(got) != 1 || got[0].ID != "published-page" {
		t.Fatalf("pages = %#v", got)
	}
	if got := service.ModelIDForDashboardDefinition(published); got != "sales_model" {
		t.Fatalf("model ID = %q", got)
	}
	if got := service.DefaultFiltersForDefinition(published); got.CompiledState == nil {
		t.Fatal("default filters did not compile before data readiness")
	}
}

func TestDefinitionServiceOverlayRejectsSemanticModelMismatch(t *testing.T) {
	service, published := definitionOverlayService(t, "other_model", true)
	published.SemanticModel = "other_model"
	if got := service.ModelIDForDashboardDefinition(published); got != "" {
		t.Fatalf("model ID = %q, want empty on mismatch", got)
	}
	patch, err := service.QueryDashboardPageForDefinition(context.Background(), published, "published-page", dashboard.Filters{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(patch.Status.Error, "unknown semantic model") && !strings.Contains(patch.Status.Error, "does not match") {
		t.Fatalf("patch error = %q", patch.Status.Error)
	}
}

func TestDefinitionServiceOverlayResolvesDiscoveredMetricTypeWithoutMutatingInput(t *testing.T) {
	service, _ := definitionOverlayService(t, "sales_model", true)
	service.runtimes[projectgraph.ResourceID("sales_model")].model = &semanticmodel.Model{
		Name: "sales_model",
		Tables: map[string]semanticmodel.Table{
			"orders": {Dimensions: map[string]semanticmodel.MetricDimension{
				"amount": {Field: "orders.amount", Datatype: semanticmodel.DataTypeFloat},
			}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Metrics: map[string]semanticmodel.Metric{
			"inferred_float": {
				Type: "aggregate", Dataset: "orders", Aggregation: "sum",
				Input: &semanticmodel.MetricInput{Field: "orders.amount"},
			},
		},
	}
	base := visualizationir.VisualizationSpecBase{
		Kind: "kpi", Title: "Inferred float", Accessibility: visualizationir.VisualizationAccessibility{Title: "Inferred float", Description: "Inferred float"},
		Datasets: []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: []visualizationir.VisualizationField{{
			ID: "value", SourceRef: stringPointer("inferred_float"), Role: visualizationir.VisualizationFieldRoleMetric,
			DataType: visualizationir.VisualizationDataTypeDecimal, Label: "Inferred float",
		}}}},
		DataBudget: visualizationir.VisualizationDataBudget{MaxRows: 1, RequiredCompleteness: visualizationir.VisualizationCompletenessComplete},
	}
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.KPIVisualizationSpec{
		VisualizationSpecBase: base, Kind: "kpi", Value: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "value"},
		Presentation: visualizationir.KPIVisualizationPresentation{Mode: visualizationir.VisualizationKPIModeCompact, Delta: visualizationir.VisualizationKPIDeltaModeAbsolute, FavorableDirection: visualizationir.VisualizationKPIDirectionNeutral, MissingComparison: visualizationir.VisualizationKPIMissingComparisonShowUnavailable},
	}}
	visual, err := visualizationdefinition.New("inferred_float", spec, visualizationdefinition.QueryBinding{
		Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultScalar, ModelID: "sales_model", DatasetID: "primary",
		Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: "orders", Metrics: []visualizationdefinition.FieldBinding{{FieldID: "inferred_float", Alias: "value"}}, Limit: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := dashboarddefinition.Definition{
		ID: "published", Title: "Published", SemanticModel: "sales_model",
		Pages:          []dashboard.Page{{ID: "published-page", Title: "Published Page"}},
		Visualizations: map[string]visualizationdefinition.Definition{"inferred_float": visual},
	}
	before := definition.Visualizations["inferred_float"]
	beforeBase, err := before.Spec.Base()
	if err != nil {
		t.Fatal(err)
	}
	if got := beforeBase.Datasets[0].Fields[0].DataType; got != visualizationir.VisualizationDataTypeDecimal {
		t.Fatalf("provisional metric datatype = %q, want decimal", got)
	}

	view, err := service.definitionService(definition)
	if err != nil {
		t.Fatal(err)
	}
	resolved, ok := view.reports.compiledDashboard("published")
	if !ok {
		t.Fatal("resolved definition was not stored in overlay")
	}
	resolvedVisual := resolved.Visualizations["inferred_float"]
	resolvedBase, err := resolvedVisual.Spec.Base()
	if err != nil {
		t.Fatal(err)
	}
	if got := resolvedBase.Datasets[0].Fields[0].DataType; got != visualizationir.VisualizationDataTypeFloat {
		t.Fatalf("resolved metric datatype = %q, want float", got)
	}
	originalVisual := definition.Visualizations["inferred_float"]
	originalBase, err := originalVisual.Spec.Base()
	if err != nil {
		t.Fatal(err)
	}
	if got := originalBase.Datasets[0].Fields[0].DataType; got != visualizationir.VisualizationDataTypeDecimal {
		t.Fatalf("caller definition was mutated to %q, want decimal", got)
	}
}
