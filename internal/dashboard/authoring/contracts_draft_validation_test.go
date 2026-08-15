package authoring

import (
	"strings"
	"testing"

	dashboardmodel "github.com/flidai/leapview/internal/dashboard"
)

func TestValidateDraftStructureDiagnosticIsDeterministic(t *testing.T) {
	document := Dashboard{
		ID:            "sales",
		Title:         "Sales",
		SemanticModel: "sales_model",
		Visuals: map[string]AuthoringVisualization{
			"revenue": ChartVisualization(Visual{
				Type: "line",
				Interaction: Interaction{
					PointSelection: SelectionInteraction{Targets: []string{"missing-point"}},
					RowSelection:   SelectionInteraction{Targets: []string{"missing-row"}},
				},
			}),
		},
		Pages: []dashboardmodel.Page{{ID: "overview"}},
	}
	firstErr := document.ValidateDraftStructure()
	secondErr := document.ValidateDraftStructure()
	if firstErr == nil || secondErr == nil {
		t.Fatalf("invalid draft unexpectedly validated: first=%v second=%v", firstErr, secondErr)
	}
	if firstErr.Error() != secondErr.Error() {
		t.Fatalf("draft diagnostics changed across repeated validation: %q != %q", firstErr, secondErr)
	}
	if !strings.Contains(firstErr.Error(), "point_selection") {
		t.Fatalf("draft diagnostic did not use the stable point-selection order: %v", firstErr)
	}
}
