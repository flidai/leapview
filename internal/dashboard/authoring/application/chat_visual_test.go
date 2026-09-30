package application

import (
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestAppendChatVisualFiltersScopesImportedStandaloneFilterToNewVisual(t *testing.T) {
	filter := testChatVisualFilter("region", "orders.region", nil)
	dashboard := document.DashboardDocument{
		Spec: document.DashboardSpec{
			Visuals: map[string]document.DashboardVisual{"existing-orders": {}},
			Filters: []document.DashboardFilter{},
		},
	}

	if err := appendChatVisualFilters(&dashboard, []document.DashboardFilter{filter}, "chat-artifact", "imported-chat-visual"); err != nil {
		t.Fatalf("appendChatVisualFilters() error = %v", err)
	}
	if len(dashboard.Spec.Filters) != 1 || dashboard.Spec.Filters[0].Targets == nil {
		t.Fatalf("imported filter targets = %#v, want an explicit target", dashboard.Spec.Filters)
	}
	targets := *dashboard.Spec.Filters[0].Targets
	if len(targets) != 1 || targets[0] != "imported-chat-visual" {
		t.Fatalf("imported filter targets = %v, want only imported-chat-visual", targets)
	}
	if chatFilterAppliesToVisual(dashboard.Spec.Filters[0], "existing-orders") {
		t.Fatal("imported source filter unexpectedly applies to a preexisting visual")
	}
	if !chatFilterAppliesToVisual(dashboard.Spec.Filters[0], "imported-chat-visual") {
		t.Fatal("imported source filter does not apply to its imported visual")
	}
}

func TestAppendChatVisualFiltersRemapsOnlyApplicableExplicitTarget(t *testing.T) {
	applicableTargets := []string{"chat-artifact"}
	inapplicableTargets := []string{"existing-orders"}
	filters := []document.DashboardFilter{
		testChatVisualFilter("applicable", "orders.region", &applicableTargets),
		testChatVisualFilter("inapplicable", "orders.status", &inapplicableTargets),
	}
	dashboard := document.DashboardDocument{Spec: document.DashboardSpec{Filters: []document.DashboardFilter{}}}

	if err := appendChatVisualFilters(&dashboard, filters, "chat-artifact", "imported-chat-visual"); err != nil {
		t.Fatalf("appendChatVisualFilters() error = %v", err)
	}
	if len(dashboard.Spec.Filters) != 1 {
		t.Fatalf("imported filters = %#v, want only the applicable source filter", dashboard.Spec.Filters)
	}
	if targets := *dashboard.Spec.Filters[0].Targets; len(targets) != 1 || targets[0] != "imported-chat-visual" {
		t.Fatalf("remapped targets = %v, want [imported-chat-visual]", targets)
	}
}

func testChatVisualFilter(id, dimension string, targets *[]string) document.DashboardFilter {
	return document.DashboardFilter{
		ID: id, Label: id, Dimension: dimension, Targets: targets,
		Control: document.DashboardFilterControl{Value: &document.MultiSelectDashboardFilterControl{Type: "multiSelect"}},
	}
}

func chatFilterAppliesToVisual(filter document.DashboardFilter, visualID string) bool {
	if filter.Targets == nil {
		return true
	}
	for _, target := range *filter.Targets {
		if target == visualID {
			return true
		}
	}
	return false
}
