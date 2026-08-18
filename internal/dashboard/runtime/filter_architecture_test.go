package runtime

import (
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

func TestSemanticBindingFiltersUsesResolvedExpressionAndCompiledConsumerTargets(t *testing.T) {
	definition := &dashboarddefinition.Definition{
		FilterDefinitions: map[string]dashboardfilter.Definition{
			"amount": {Field: "orders.amount", Dataset: "orders", ValueKind: dashboardfilter.ValueDecimal},
		},
		FilterBindings: map[string]dashboardfilter.Binding{
			"amount": {
				Key: "amount-key", Filter: "amount", Scope: dashboardfilter.ScopeReport,
				Targets: []string{"overview/revenue"},
			},
		},
	}
	state := dashboardfilter.State{
		Revision: 2,
		AppliedControls: map[string]dashboardfilter.AppliedState{
			"amount-key": {
				Expression: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionRelativePeriod},
				ResolvedExpression: dashboardfilter.Expression{
					Kind: dashboardfilter.ExpressionRange,
					Lower: &dashboardfilter.Bound{
						Value: dashboardfilter.Value{Kind: dashboardfilter.ValueDecimal, Value: "10.5"}, Inclusive: false,
					},
					Upper: &dashboardfilter.Bound{
						Value: dashboardfilter.Value{Kind: dashboardfilter.ValueDecimal, Value: "20"}, Inclusive: true,
					},
				},
			},
		},
	}

	filters, err := semanticBindingFilters(definition, state, "overview/revenue")
	if err != nil {
		t.Fatal(err)
	}
	if len(filters) != 2 {
		t.Fatalf("semantic filters = %#v", filters)
	}
	if filters[0].Operator != "greater_than" || filters[1].Operator != "less_than_or_equal" {
		t.Fatalf("range operators = %q, %q", filters[0].Operator, filters[1].Operator)
	}
	if filters[0].Values[0] != "10.5" || filters[1].Values[0] != "20" {
		t.Fatalf("range values = %#v, %#v", filters[0].Values, filters[1].Values)
	}

	unrelated, err := semanticBindingFilters(definition, state, "overview/orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(unrelated) != 0 {
		t.Fatalf("unrelated consumer filters = %#v", unrelated)
	}
}

func TestSemanticBindingFiltersForTargetUsesExactActivePageComponentIdentity(t *testing.T) {
	definition := &dashboarddefinition.Definition{
		FilterDefinitions: map[string]dashboardfilter.Definition{
			"state": {Field: "state", ValueKind: dashboardfilter.ValueString},
		},
		FilterBindings: map[string]dashboardfilter.Binding{
			"state": {
				Key: "state-key", Filter: "state", Scope: dashboardfilter.ScopeReport,
				Targets: []string{"overview/revenue-card"},
			},
		},
		Pages: []dashboard.Page{
			{ID: "overview", Visuals: []dashboard.PageVisual{{ID: "revenue-card", Kind: "visual", Visual: "revenue"}}},
			{ID: "details", Visuals: []dashboard.PageVisual{{ID: "revenue-card", Kind: "visual", Visual: "revenue"}}},
		},
	}
	state := dashboardfilter.State{AppliedControls: map[string]dashboardfilter.AppliedState{
		"state-key": {ResolvedExpression: dashboardfilter.Expression{
			Kind: dashboardfilter.ExpressionSet, Operator: dashboardfilter.OperatorIn,
			Values: []dashboardfilter.Value{{Kind: dashboardfilter.ValueString, Value: "CA"}},
		}},
	}}
	overview, err := semanticBindingFiltersForTarget(definition, state, "overview", "visual", "revenue")
	if err != nil || len(overview) != 1 {
		t.Fatalf("overview filters = %#v, error = %v", overview, err)
	}
	details, err := semanticBindingFiltersForTarget(definition, state, "details", "visual", "revenue")
	if err != nil {
		t.Fatal(err)
	}
	if len(details) != 0 {
		t.Fatalf("details filters = %#v, want exact-page exclusion", details)
	}
}

func TestSemanticBindingFiltersPreservesSetAndNullPredicateGrouping(t *testing.T) {
	definition := &dashboarddefinition.Definition{
		FilterDefinitions: map[string]dashboardfilter.Definition{
			"state":   {Field: "customers.state", ValueKind: dashboardfilter.ValueString},
			"missing": {Field: "customers.state", ValueKind: dashboardfilter.ValueString},
		},
		FilterBindings: map[string]dashboardfilter.Binding{
			"state":   {Key: "state-key", Filter: "state", Scope: dashboardfilter.ScopeReport, Targets: []string{"overview/orders"}},
			"missing": {Key: "null-key", Filter: "missing", Scope: dashboardfilter.ScopeReport, Targets: []string{"overview/orders"}},
		},
	}
	state := dashboardfilter.State{AppliedControls: map[string]dashboardfilter.AppliedState{
		"state-key": {ResolvedExpression: dashboardfilter.Expression{
			Kind: dashboardfilter.ExpressionSet, Operator: dashboardfilter.OperatorNotIn,
			Values: []dashboardfilter.Value{{Kind: dashboardfilter.ValueString, Value: "CA"}, {Kind: dashboardfilter.ValueString, Value: "WA"}},
		}},
		"null-key": {ResolvedExpression: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionNullCheck, Operator: dashboardfilter.OperatorIsNotNull}},
	}}

	filters, err := semanticBindingFilters(definition, state, "overview/orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(filters) != 2 || filters[0].Operator != "is_not_null" || filters[1].Operator != "not_in" {
		t.Fatalf("semantic filters = %#v", filters)
	}
}
