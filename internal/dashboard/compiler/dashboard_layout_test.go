package compiler

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

func TestValidateWidgetPlacementsRejectsUndersizedDateRangeSlicer(t *testing.T) {
	authored := &dashboardauthoring.Dashboard{
		Pages: []dashboard.Page{{
			ID: "overview", Title: "Overview",
			Canvas: dashboard.PageCanvas{Width: 1366, Height: 912},
			Grid:   dashboard.PageGrid{Columns: 12, RowHeight: 48, Gap: 16, Padding: 16},
			Visuals: []dashboard.PageVisual{{
				ID: "period", Kind: "slicer",
				Presentation: dashboardfilter.Presentation{Style: dashboardfilter.PresentationDateRange},
				Placement:    dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 3, RowSpan: 1},
			}},
		}},
	}

	err := validateWidgetPlacements(authored)
	if err == nil {
		t.Fatal("expected undersized slicer error")
	}
	for _, fragment := range []string{`page "overview"`, `slicer "period"`, "provides 322x48", "inline requires 288x94", "stacked requires 192x154"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("error %q does not contain %q", err, fragment)
		}
	}

	authored.Pages[0].Visuals[0].Placement.RowSpan = 2
	if err := validateWidgetPlacements(authored); err != nil {
		t.Fatalf("valid date-range placement: %v", err)
	}
}

func TestValidateWidgetPlacementsDerivesKPIRequirementsFromExplicitFeatures(t *testing.T) {
	authored := &dashboardauthoring.Dashboard{
		Visuals: dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{
			"revenue": {
				Type: "kpi",
				KPI: dashboardauthoring.VisualKPI{
					Comparison: &dashboardauthoring.VisualKPIValueBinding{},
					Trend:      &dashboardauthoring.VisualKPITrendBinding{},
				},
			},
		}),
		Pages: []dashboard.Page{{
			ID: "overview", Title: "Overview",
			Canvas: dashboard.PageCanvas{Width: 192, Height: 123},
			Grid:   dashboard.PageGrid{Columns: 1, RowHeight: 123, Gap: 1},
			Visuals: []dashboard.PageVisual{{
				ID: "revenue-card", Kind: "visual", Visual: "revenue",
				Placement: dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 1, RowSpan: 1},
			}},
		}},
	}

	err := validateWidgetPlacements(authored)
	if err == nil || !strings.Contains(err.Error(), "stacked requires 192x124") {
		t.Fatalf("boundary-minus-one KPI error = %v", err)
	}

	authored.Pages[0].Grid.RowHeight = 124
	if err := validateWidgetPlacements(authored); err != nil {
		t.Fatalf("valid stacked KPI placement: %v", err)
	}
}
