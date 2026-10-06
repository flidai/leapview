package compiler

import (
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

func TestCanonicalTextNullChecksRequireExplicitOperator(t *testing.T) {
	for _, operator := range []document.DashboardFilterOperator{document.DashboardFilterOperatorIsNull, document.DashboardFilterOperatorIsNotNull} {
		t.Run(string(operator), func(t *testing.T) {
			filter := document.DashboardFilter{ID: "status", Label: "Status", Dimension: "status", Control: document.DashboardFilterControl{Value: &document.TextDashboardFilterControl{Type: "text"}}, Default: &document.DashboardFilterExpression{Value: &document.NullCheckDashboardFilterExpression{Type: "nullCheck", Operator: operator}}}
			doc := document.DashboardDocument{Metadata: document.DashboardMetadata{ID: "sales"}, Spec: document.DashboardSpec{Filters: []document.DashboardFilter{filter}}}
			if _, err := CompileCanonicalDashboardFilterContract(doc, canonicalFilterTestModel()); err == nil {
				t.Fatal("ordinary text control admitted a null predicate")
			}
			filter.Operators = &[]document.DashboardFilterOperator{operator}
			doc.Spec.Filters[0] = filter
			compiled, err := CompileCanonicalDashboardFilterContract(doc, canonicalFilterTestModel())
			if err != nil {
				t.Fatal(err)
			}
			definition := compiled.Definitions["status"]
			if len(definition.Predicates) != 1 || definition.Predicates[0].Kind != dashboardfilter.ExpressionNullCheck || len(definition.Predicates[0].Operators) != 1 || string(definition.Predicates[0].Operators[0]) != string(mustCanonicalOperator(operator)) {
				t.Fatalf("explicit null policy = %#v", definition.Predicates)
			}
			opposite := dashboardfilter.OperatorIsNull
			if operator == document.DashboardFilterOperatorIsNull {
				opposite = dashboardfilter.OperatorIsNotNull
			}
			if definitionAllowsExpression(definition, dashboardfilter.Expression{Kind: dashboardfilter.ExpressionNullCheck, Operator: opposite}) {
				t.Fatal("unrequested null operator admitted")
			}
			if definitionAllowsExpression(definition, dashboardfilter.Expression{Kind: dashboardfilter.ExpressionComparison, Operator: dashboardfilter.OperatorEquals}) {
				t.Fatal("null-only control admitted comparison")
			}
		})
	}
}

func TestCanonicalTextDefaultsRemainComparisonOnly(t *testing.T) {
	policies, err := canonicalPredicates("text", nil, dashboardfilter.ValueString)
	if err != nil {
		t.Fatal(err)
	}
	if len(policies) != 1 || policies[0].Kind != dashboardfilter.ExpressionComparison || len(policies[0].Operators) != 6 {
		t.Fatalf("default text policies changed: %#v", policies)
	}
	operators := []document.DashboardFilterOperator{document.DashboardFilterOperatorEquals, document.DashboardFilterOperatorIsNull}
	policies, err = canonicalPredicates("text", &operators, dashboardfilter.ValueString)
	if err != nil {
		t.Fatal(err)
	}
	definition := dashboardfilter.Definition{Predicates: policies}
	if !definitionAllowsExpression(definition, dashboardfilter.Expression{Kind: dashboardfilter.ExpressionComparison, Operator: dashboardfilter.OperatorEquals}) || !definitionAllowsExpression(definition, dashboardfilter.Expression{Kind: dashboardfilter.ExpressionNullCheck, Operator: dashboardfilter.OperatorIsNull}) {
		t.Fatalf("mixed explicit policies = %#v", policies)
	}
	if definitionAllowsExpression(definition, dashboardfilter.Expression{Kind: dashboardfilter.ExpressionComparison, Operator: dashboardfilter.OperatorIsNull}) {
		t.Fatal("null operator admitted as comparison")
	}
}
