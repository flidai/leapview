package compiler

import (
	"fmt"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/semanticnumeric"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

func TestCFOVarianceLegendMeasuresPreserveSignedAmounts(t *testing.T) {
	project, err := LoadSourceRoot("../../../dashboards/experiments/cfo-demo")
	if err != nil {
		t.Fatal(err)
	}
	finance := project.SemanticModels["finance"]
	for _, tc := range []struct {
		input        any
		above, below any
	}{
		{"21457.29", "21457.29", nil}, {"-826392.56", nil, "-826392.56"},
		{"0.01", "0.01", nil}, {"-0.01", nil, "-0.01"}, {"0", nil, nil}, {nil, nil, nil},
	} {
		for name, want := range map[string]any{"revenue_above_budget": tc.above, "revenue_below_budget": tc.below} {
			metric, ok := (*finance.Metrics)[name]
			if !ok {
				t.Fatalf("missing legend measure %s", name)
			}
			expression, err := semanticmodel.ParseExpression(metric.Value.(*projectcontracts.SemanticMetricDerivedVariant).Expression)
			if err != nil {
				t.Fatal(err)
			}
			got, err := expression.Evaluate(func(ref string) (any, error) {
				if ref != "revenue_variance" {
					return nil, fmt.Errorf("unexpected reference %s", ref)
				}
				return tc.input, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if want == nil {
				if got != nil {
					t.Fatalf("%s(%v) = %v, want no bar", name, tc.input, got)
				}
				continue
			}
			actual, err := semanticnumeric.FromValue(got)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := semanticnumeric.FromValue(want)
			if err != nil {
				t.Fatal(err)
			}
			comparison, err := actual.Cmp(expected)
			if err != nil || comparison != 0 {
				t.Fatalf("%s(%v) = %v, want %v: %v", name, tc.input, got, want, err)
			}
		}
	}
}
