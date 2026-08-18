package model

import (
	"strings"
	"testing"
)

func TestMetricDataTypeTracksFloatReferencesAndCycles(t *testing.T) {
	model := &Model{
		Tables: map[string]Table{
			"orders": {Dimensions: map[string]MetricDimension{
				"amount":      {Datatype: DataTypeDecimal},
				"ratio_input": {Datatype: DataTypeFloat},
			}},
		},
		Metrics: map[string]Metric{
			"amount_metric": {Type: "aggregate", Aggregation: "sum", Input: &MetricInput{Field: "orders.amount"}},
			"float_metric":  {Type: "aggregate", Aggregation: "sum", Input: &MetricInput{Field: "orders.ratio_input"}},
			"ratio":         {Type: "ratio", Numerator: "amount_metric", Denominator: "float_metric"},
			"cycle_a":       {Type: "ratio", Numerator: "cycle_b", Denominator: "amount_metric"},
			"cycle_b":       {Type: "ratio", Numerator: "cycle_a", Denominator: "amount_metric"},
		},
	}
	if got, err := model.MetricDataType("ratio"); err != nil || got != DataTypeFloat {
		t.Fatalf("ratio datatype = %q, err=%v; want Float", got, err)
	}
	if _, err := model.MetricDataType("cycle_a"); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle error = %v, want dependency cycle", err)
	}
}
