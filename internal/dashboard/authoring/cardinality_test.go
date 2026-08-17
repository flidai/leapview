package authoring

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
)

func TestDashboardRejectsUnknownTableCardinalityPolicy(t *testing.T) {
	dashboardDefinition := Dashboard{
		ID:            "commerce",
		Title:         "Commerce",
		SemanticModel: "commerce",
		Visuals: map[string]AuthoringVisualization{
			"orders": TabularVisualization("table", TableVisual{Title: "Orders", Cardinality: "automatic", Query: TableQuery{Dataset: "orders", Fields: []string{"order_id"}}}),
		},
		Pages: []dashboard.Page{{ID: "overview", Title: "Overview", Visuals: []dashboard.PageVisual{{Kind: "visual", Visual: "orders"}}}},
	}
	err := dashboardDefinition.ValidateContract()
	if err == nil || !strings.Contains(err.Error(), "unsupported cardinality") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestTableCardinalityDefaultsToBounded(t *testing.T) {
	if got := (TableVisual{}).CardinalityOrDefault(); got != TableCardinalityBounded {
		t.Fatalf("default cardinality = %q", got)
	}
}

func TestDashboardRejectsLegacyPageComponentKinds(t *testing.T) {
	for _, kind := range []string{"table", "kpi_card", "bar_chart", "filter_card"} {
		t.Run(kind, func(t *testing.T) {
			dashboardDefinition := Dashboard{
				ID:            "commerce",
				Title:         "Commerce",
				SemanticModel: "commerce",
				Visuals: map[string]AuthoringVisualization{
					"orders": TabularVisualization("table", TableVisual{Title: "Orders", Query: TableQuery{Dataset: "orders", Fields: []string{"order_id"}}}),
				},
				Pages: []dashboard.Page{{
					ID: "overview", Title: "Overview",
					Visuals: []dashboard.PageVisual{{
						ID: "orders", Kind: kind, Visual: "orders",
						Placement: dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 1, RowSpan: 1},
					}},
				}},
			}
			err := dashboardDefinition.ValidateContract()
			if err == nil || !strings.Contains(err.Error(), "unsupported kind") {
				t.Fatalf("validation error = %v, want legacy kind rejection", err)
			}
		})
	}
}
