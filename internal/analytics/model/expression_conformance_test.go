package model

import (
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/analytics/semanticnumeric"
)

// This corpus exercises only the public expression contract. A future parser
// or evaluator implementation must pass the same cases without exposing its
// internal node types.
func TestExpressionLanguageConformanceCorpus(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		values     map[string]any
		references []string
		want       any
		wantError  bool
	}{
		{name: "exact decimal", source: "${amount} + 0.001", values: map[string]any{"amount": "9007199254740993.125"}, references: []string{"amount"}, want: semanticnumeric.Decimal("9007199254740993.126")},
		{name: "safe divide", source: "safe_divide(${part}, ${whole})", values: map[string]any{"part": int64(3), "whole": int64(8)}, references: []string{"part", "whole"}, want: semanticnumeric.Decimal("0.375")},
		{name: "safe divide zero", source: "safe_divide(${part}, ${whole})", values: map[string]any{"part": int64(3), "whole": int64(0)}, references: []string{"part", "whole"}, want: nil},
		{name: "exact divide zero", source: "${part} / ${whole}", values: map[string]any{"part": int64(3), "whole": int64(0)}, references: []string{"part", "whole"}, want: nil},
		{name: "exact divide null", source: "${part} / ${whole}", values: map[string]any{"part": int64(3), "whole": nil}, references: []string{"part", "whole"}, want: nil},
		{name: "null propagation", source: "${amount} + 1", values: map[string]any{"amount": nil}, references: []string{"amount"}, want: nil},
		{name: "coalesce", source: "coalesce(${amount}, 2.50)", values: map[string]any{"amount": nil}, references: []string{"amount"}, want: semanticnumeric.Decimal("2.5")},
		{name: "unsupported aggregate SQL", source: "SUM(${amount})", wantError: true},
		{name: "bare identifier", source: "amount + 1", wantError: true},
		{name: "malformed number", source: "1.2.3", wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expression, err := ParseExpression(test.source)
			if test.wantError {
				if err == nil {
					t.Fatalf("ParseExpression(%q) succeeded", test.source)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := expression.References(); !reflect.DeepEqual(got, test.references) {
				t.Fatalf("references = %#v, want %#v", got, test.references)
			}
			value, err := expression.Evaluate(func(reference string) (any, error) {
				return test.values[reference], nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(value, test.want) {
				t.Fatalf("value = %#v (%T), want %#v (%T)", value, value, test.want, test.want)
			}
		})
	}
}

func TestExpressionEvaluationPreservesExactResultKinds(t *testing.T) {
	tests := []struct {
		name   string
		source string
		value  map[string]any
		want   any
	}{
		{name: "integer literals remain integer", source: "1 + 1", want: int64(2)},
		{name: "Decimal literal integral result remains Decimal", source: "1.0 + 1", want: semanticnumeric.Decimal("2")},
		{name: "runtime Decimal integral result remains Decimal", source: "${amount} + 1", value: map[string]any{"amount": "1.0"}, want: semanticnumeric.Decimal("2")},
		{name: "Decimal rounding remains Decimal", source: "round(${amount}, 0)", value: map[string]any{"amount": "1.2"}, want: semanticnumeric.Decimal("1")},
		{name: "Float remains Float", source: "${amount} + 1", value: map[string]any{"amount": float64(1.5)}, want: float64(2.5)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expression, err := ParseExpression(test.source)
			if err != nil {
				t.Fatal(err)
			}
			got, err := expression.Evaluate(func(reference string) (any, error) {
				return test.value[reference], nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("value = %#v (%T), want %#v (%T)", got, got, test.want, test.want)
			}
		})
	}
}

func TestExpressionFloatDivisionByZeroRejectsNonFiniteResult(t *testing.T) {
	expression, err := ParseExpression("${a} / ${b}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := expression.Evaluate(func(reference string) (any, error) {
		return map[string]any{"a": float64(1), "b": float64(0)}[reference], nil
	}); err == nil {
		t.Fatal("Float division by zero was accepted")
	}
}
