package runtime

import (
	"testing"

	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

func TestSemanticFiltersForExpressionTransformsEqualsWithMultipleValues(t *testing.T) {
	definition := dashboardfilter.Definition{Field: "orders.status", Dataset: "orders"}

	// 1. equals + ["created", "delivered"] -> in
	multiEquals := dashboardfilter.Expression{
		Kind:     dashboardfilter.ExpressionSet,
		Operator: "equals",
		Values: []dashboardfilter.Value{
			{Kind: dashboardfilter.ValueString, Value: "created"},
			{Kind: dashboardfilter.ValueString, Value: "delivered"},
		},
	}
	filters, err := semanticFiltersForExpression(definition, multiEquals)
	if err != nil || len(filters) != 1 {
		t.Fatalf("expected 1 filter, got %d, err: %v", len(filters), err)
	}
	if filters[0].Operator != "in" {
		t.Fatalf("expected 'in' operator, got %q", filters[0].Operator)
	}
	if len(filters[0].Values) != 2 || filters[0].Values[0] != "created" || filters[0].Values[1] != "delivered" {
		t.Fatalf("expected 2 values, got %v", filters[0].Values)
	}

	// 2. not_equals + ["created", "delivered"] -> not_in
	multiNotEquals := dashboardfilter.Expression{
		Kind:     dashboardfilter.ExpressionSet,
		Operator: "not_equals",
		Values: []dashboardfilter.Value{
			{Kind: dashboardfilter.ValueString, Value: "created"},
			{Kind: dashboardfilter.ValueString, Value: "delivered"},
		},
	}
	filters2, err := semanticFiltersForExpression(definition, multiNotEquals)
	if err != nil || len(filters2) != 1 {
		t.Fatalf("expected 1 filter, got %d, err: %v", len(filters2), err)
	}
	if filters2[0].Operator != "not_in" {
		t.Fatalf("expected 'not_in' operator, got %q", filters2[0].Operator)
	}

	// 3. equals + ["created"] -> equals
	singleEquals := dashboardfilter.Expression{
		Kind:     dashboardfilter.ExpressionSet,
		Operator: "equals",
		Values: []dashboardfilter.Value{
			{Kind: dashboardfilter.ValueString, Value: "created"},
		},
	}
	filters3, err := semanticFiltersForExpression(definition, singleEquals)
	if err != nil || len(filters3) != 1 {
		t.Fatalf("expected 1 filter, got %d, err: %v", len(filters3), err)
	}
	if filters3[0].Operator != "equals" {
		t.Fatalf("expected 'equals' operator, got %q", filters3[0].Operator)
	}

	// 4. clearing filter -> empty values (still equals)
	emptyEquals := dashboardfilter.Expression{
		Kind:     dashboardfilter.ExpressionSet,
		Operator: "equals",
		Values:   []dashboardfilter.Value{},
	}
	filters4, err := semanticFiltersForExpression(definition, emptyEquals)
	if err != nil || len(filters4) != 1 {
		t.Fatalf("expected 1 filter, got %d, err: %v", len(filters4), err)
	}
	if filters4[0].Operator != "equals" {
		t.Fatalf("expected 'equals' operator, got %q", filters4[0].Operator)
	}
	if len(filters4[0].Values) != 0 {
		t.Fatalf("expected 0 values, got %v", filters4[0].Values)
	}
}
