package compiler

import (
	"testing"

	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

func assertShowcasePurchasePeriod(t *testing.T, showcase dashboarddefinition.Definition) {
	t.Helper()
	period, ok := showcase.FilterDefinitions["purchase_time"]
	if !ok || period.Label != "Purchase period" || period.Field != "purchase_timestamp" || period.ValueKind != dashboardfilter.ValueTimestamp {
		t.Fatalf("shared showcase purchase period = %#v", period)
	}
	if len(period.Predicates) != 1 || period.Predicates[0].Kind != dashboardfilter.ExpressionRange {
		t.Fatalf("purchase period must compile to an absolute range: %#v", period.Predicates)
	}
	binding, ok := showcase.FilterBindings["purchase_time"]
	if !ok || binding.Default.Kind != dashboardfilter.ExpressionUnfiltered {
		t.Fatalf("purchase period must preserve its identity and unfiltered default: %#v", binding)
	}
	for _, page := range showcase.Pages {
		for _, component := range page.Visuals {
			if page.ID == "filters" && component.ID == "relative-period-filter" {
				if component.Kind != "slicer" || component.Presentation.Style != dashboardfilter.PresentationDateRange {
					t.Fatalf("purchase period canvas control must use Start/End dates: %#v", component)
				}
				return
			}
		}
	}
	t.Fatal("shared showcase omitted the existing purchase-period component")
}
