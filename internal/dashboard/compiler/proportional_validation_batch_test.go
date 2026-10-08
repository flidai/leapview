package compiler

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestPieReportsAllUnsupportedPresentationSettingsTogether(t *testing.T) {
	orientation := document.DashboardOrientation("vertical")
	align := document.DashboardProportionalAlignment("center")
	sort := visualizationir.VisualizationSortDirection("descending")
	err := validateCanonicalPresentationApplicability(document.DashboardPresentation{Value: &document.ProportionalDashboardPresentation{Type: "proportional", Orientation: &orientation, Align: &align, Sort: &sort}}, document.DashboardVisualTypePie)
	if err == nil {
		t.Fatal("unsupported pie settings accepted")
	}
	for _, field := range []string{"orientation", "align", "sort"} {
		if !strings.Contains(err.Error(), "presentation."+field+" is not supported for pie visuals") {
			t.Fatalf("missing %s in: %v", field, err)
		}
	}
}
