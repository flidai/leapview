package runtime

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
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
	workspace := &dashboarddefinition.Workspace{
		Catalog: dashboard.Catalog{Workspace: dashboard.CatalogWorkspace{ID: "workspace"}},
		Models:  map[string]*semanticmodel.Model{"sales_model": base},
	}
	service := &Service{reports: &ReportService{workspace: workspace}}
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
