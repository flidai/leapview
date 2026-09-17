package http

import (
	"encoding/json"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
)

func TestDashboardComponentDTOEmitsSlicerDiscriminatorAndField(t *testing.T) {
	component := dashboard.PageVisual{ID: "state-slicer", Kind: "slicer", Binding: dashboardfilter.BindingRef{Scope: dashboardfilter.ScopePage, ID: "state"}}
	page := dashboard.Page{ID: "overview", FilterBindings: map[string]dashboardfilter.Binding{"state": {ID: "state", Filter: "state"}}}
	report := dashboarddefinition.Definition{FilterDefinitions: map[string]dashboardfilter.Definition{"state": {Label: "State"}}}
	encoded, err := json.Marshal(dashboardComponentDTO(component, report, page))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	if body["kind"] != "slicer" || body["filterId"] != "state" {
		t.Fatalf("component = %s", encoded)
	}
}

func TestDashboardQueryFiltersDecodesVersionedAppliedStateAndIndependentSelections(t *testing.T) {
	key := dashboardfilter.BindingKey("dashboard", dashboardfilter.ScopePage, "overview", "state")
	definition := dashboarddefinition.Definition{
		FilterDefinitions: map[string]dashboardfilter.Definition{
			"state": {
				ValueKind: dashboardfilter.ValueString,
				Predicates: []dashboardfilter.PredicatePolicy{{
					Kind: dashboardfilter.ExpressionSet, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorIn},
				}},
			},
		},
		Pages: []dashboard.Page{{ID: "overview", Visuals: []dashboard.PageVisual{{Visual: "orders"}}, FilterBindings: map[string]dashboardfilter.Binding{
			"state": {
				Key: key, ID: "state", Filter: "state", Scope: dashboardfilter.ScopePage, PageID: "overview",
				Default: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionUnfiltered},
			},
		}}}, Visualizations: map[string]visualizationdefinition.Definition{"orders": {ID: "orders"}},
	}
	filters, err := dashboardQueryFilters(definition, "overview", map[string]any{
		"version": "typed_v1",
		"controls": map[string]any{key: map[string]any{
			"kind": "set", "operator": "in",
			"values": []any{map[string]any{"kind": "string", "value": "SP"}},
		}},
	}, []map[string]any{{"sourceKind": "visual", "sourceId": "orders", "interactionKind": "selection"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if filters.CompiledState == nil || filters.CompiledState.AppliedControls[key].Expression.Values[0].Value != "SP" {
		t.Fatalf("compiled state = %#v", filters.CompiledState)
	}
	if len(filters.Selections) != 1 || filters.Selections[0].SourceID != "orders" {
		t.Fatalf("selections = %#v", filters.Selections)
	}
}

func TestDashboardQueryFiltersRejectsForgedInteractionSources(t *testing.T) {
	definition := dashboarddefinition.Definition{
		Pages: []dashboard.Page{
			{ID: "overview", Visuals: []dashboard.PageVisual{{Visual: "orders"}}},
			{ID: "hidden", Visuals: []dashboard.PageVisual{{Visual: "secret"}}},
		},
		Visualizations: map[string]visualizationdefinition.Definition{
			"orders": {ID: "orders"}, "secret": {ID: "secret"},
		},
	}
	for name, selection := range map[string]map[string]any{
		"unknown visual":     {"sourceKind": "visual", "sourceId": "missing", "interactionKind": "selection"},
		"off-page visual":    {"sourceKind": "visual", "sourceId": "secret", "interactionKind": "selection"},
		"forged source kind": {"sourceKind": "semanticModel", "sourceId": "orders", "interactionKind": "selection"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := dashboardQueryFilters(definition, "overview", nil, []map[string]any{selection}, nil); err == nil {
				t.Fatal("forged interaction source was accepted")
			}
		})
	}
}

func TestDashboardQueryFiltersRejectsForgedSpatialSources(t *testing.T) {
	definition := dashboarddefinition.Definition{
		Pages: []dashboard.Page{
			{ID: "overview", Visuals: []dashboard.PageVisual{{Visual: "orders"}}},
			{ID: "hidden", Visuals: []dashboard.PageVisual{{Visual: "secret-map"}}},
		},
		Visualizations: map[string]visualizationdefinition.Definition{
			"orders": {ID: "orders"}, "secret-map": {ID: "secret-map"},
		},
	}
	if _, err := dashboardQueryFilters(definition, "overview", nil, nil, []map[string]any{{"visualID": "secret-map", "interactionID": "spatial"}}); err == nil {
		t.Fatal("off-page spatial source was accepted")
	}
}

func TestDashboardQueryFiltersRejectsLegacyWholeMapState(t *testing.T) {
	if _, err := dashboardQueryFilters(dashboarddefinition.Definition{}, "overview", map[string]any{
		"controls": map[string]any{},
	}, nil, nil); err == nil {
		t.Fatal("legacy unversioned filter state was accepted")
	}
}

func TestDashboardQueryFiltersRejectsForgedFilterBinding(t *testing.T) {
	definition := dashboarddefinition.Definition{
		FilterDefinitions: map[string]dashboardfilter.Definition{
			"status": {ValueKind: dashboardfilter.ValueString},
		},
		FilterBindings: map[string]dashboardfilter.Binding{
			"status": {Key: "status", ID: "status", Filter: "status", Scope: dashboardfilter.ScopeReport},
		},
		Pages: []dashboard.Page{{ID: "overview"}},
	}
	_, err := dashboardQueryFilters(definition, "overview", map[string]any{
		"version": "typed_v1",
		"controls": map[string]any{"forged": map[string]any{
			"kind": "set", "operator": "in",
			"values": []any{map[string]any{"kind": "string", "value": "secret"}},
		}},
	}, nil, nil)
	if err == nil {
		t.Fatal("forged filter binding was accepted")
	}
}
