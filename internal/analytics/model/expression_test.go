package model

import (
	"reflect"
	"testing"
)

func TestExpressionParsesReferencesAndSafeDivide(t *testing.T) {
	expression, err := ParseExpression("safe_divide(${refunds}, ${revenue}) * 100")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := expression.References(), []string{"refunds", "revenue"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("references = %#v, want %#v", got, want)
	}
	sql, err := expression.SQL(func(ref string) (string, error) { return "m_" + ref, nil })
	if err != nil {
		t.Fatal(err)
	}
	if sql != "((m_refunds / NULLIF(m_revenue, 0)) * 100)" {
		t.Fatalf("SQL = %q", sql)
	}
}

func TestExpressionRejectsAggregateSQLAndBareIdentifiers(t *testing.T) {
	for _, input := range []string{"SUM(${orders.revenue})", "orders.revenue", "${}"} {
		if _, err := ParseExpression(input); err == nil {
			t.Fatalf("ParseExpression(%q) succeeded", input)
		}
	}
}

func TestExpressionReportsAllowlistedFunctions(t *testing.T) {
	expression, err := ParseExpression("round(abs(${ratings.score}), 2) + safe_divide(1, 2)")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := expression.Functions(), []string{"round", "abs", "safe_divide"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("functions = %#v, want %#v", got, want)
	}
}

func TestExpressionTreePreservesTypedOperatorsAndNumberTokens(t *testing.T) {
	expression, err := ParseExpression("-safe_divide(${tags}, 9007199254740993.125)")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := expression.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if tree.Kind != ScalarExpressionUnary || tree.Operator != "-" || len(tree.Children) != 1 {
		t.Fatalf("tree root = %#v", tree)
	}
	call := tree.Children[0]
	if call.Kind != ScalarExpressionCall || call.Function != "safe_divide" || len(call.Children) != 2 {
		t.Fatalf("tree call = %#v", call)
	}
	if got := call.Children[1].Number; got != "9007199254740993.125" {
		t.Fatalf("number token = %q", got)
	}
	// Tree returns detached slices, so callers cannot mutate the parsed
	// expression through the exported view.
	tree.Children[0].Children[0].Metric = "mutated"
	again, err := expression.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if again.Children[0].Children[0].Metric != "tags" {
		t.Fatalf("Tree() did not return a detached AST: %#v", again)
	}
}

func TestExpressionEvaluateMetricArithmeticAndNullSemantics(t *testing.T) {
	expression, err := ParseExpression("round(safe_divide(${tags}, ${ratings}), 3)")
	if err != nil {
		t.Fatal(err)
	}
	value, err := expression.Evaluate(func(ref string) (any, error) {
		return map[string]any{"tags": int64(3), "ratings": int64(8)}[ref], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if value != 0.375 {
		t.Fatalf("value = %#v, want 0.375", value)
	}

	nullValue, err := expression.Evaluate(func(ref string) (any, error) {
		return map[string]any{"tags": int64(3), "ratings": int64(0)}[ref], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if nullValue != nil {
		t.Fatalf("division by zero = %#v, want nil", nullValue)
	}
}
