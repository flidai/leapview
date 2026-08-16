package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func definitionOverlayService(t *testing.T, modelName string, ready bool) (*Service, dashboarddefinition.Definition) {
	t.Helper()
	model := &semanticmodel.Model{Name: modelName}
	project := dashboarddefinition.Definition{
		ID: "project", Title: "Project", SemanticModel: "sales_model",
		Pages: []dashboard.Page{{ID: "project-page", Title: "Project Page"}},
	}
	workspace := &dashboarddefinition.Project{
		Catalog:    dashboard.Catalog{Project: dashboard.CatalogProject{ID: "workspace"}},
		Models:     map[projectgraph.ResourceID]*semanticmodel.Model{"sales_model": model},
		Dashboards: map[projectgraph.ResourceID]dashboarddefinition.Definition{"project": project},
	}
	baseRuntime := &modelRuntime{model: model, ready: ready}
	if !ready {
		baseRuntime.missing = errors.New("setup required")
	}
	service := &Service{
		runtimes: map[projectgraph.ResourceID]*modelRuntime{"sales_model": baseRuntime},
		tiles:    newSpatialTileRegistry(),
	}
	var err error
	service.catalog, err = NewCatalogService(&service.mu, workspace)
	if err != nil {
		t.Fatal(err)
	}
	service.reports = &ReportService{projectID: "workspace", models: workspace.Models, dashboards: workspace.Dashboards, catalog: workspace.Catalog, defaultID: "project"}
	service.filters = &FilterService{}
	service.visualizations = &VisualizationDataService{mu: &service.mu, reports: service.reports, runtimes: service.runtimes, filters: service.filters, tiles: service.tiles, projectID: "workspace"}
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
	if _, err := service.reports.Resolve("published"); err == nil {
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
