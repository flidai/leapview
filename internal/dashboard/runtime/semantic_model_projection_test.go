package runtime

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestSemanticModelProjectionIsDetachedFromBaseRuntime(t *testing.T) {
	base := &semanticmodel.Model{
		Name: "sales_model",
		Tables: map[string]semanticmodel.Table{
			"orders": {Dimensions: map[string]semanticmodel.MetricDimension{
				"status": {Field: "orders.status", Type: "string"},
			}},
		},
	}
	workspace := &dashboarddefinition.Project{
		Catalog: dashboard.Catalog{Project: dashboard.CatalogProject{ID: "workspace"}},
		Models:  map[projectgraph.ResourceID]*semanticmodel.Model{"sales_model": base},
	}
	service := &Service{reports: &ReportService{projectID: "workspace", models: workspace.Models, dashboards: workspace.Dashboards, catalog: workspace.Catalog}}
	projection, ok := service.SemanticModelProjection("sales_model")
	if !ok || projection == nil {
		t.Fatal("semantic model projection unavailable")
	}
	projection.Tables["orders"].Dimensions["status"] = semanticmodel.MetricDimension{Field: "mutated"}
	projection.Tables["new"] = semanticmodel.Table{}
	second, ok := service.SemanticModelProjection("sales_model")
	if !ok || second.Tables["orders"].Dimensions["status"].Field != "orders.status" {
		t.Fatalf("base nested model changed: %#v", second)
	}
	if _, exists := second.Tables["new"]; exists {
		t.Fatal("base model map changed through projection")
	}
}
