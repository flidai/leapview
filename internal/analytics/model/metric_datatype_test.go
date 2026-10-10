package model

import (
	"strings"
	"testing"
)

func TestMetricDataTypePreservesAggregatePrecision(t *testing.T) {
	model := &Model{Tables: map[string]Table{
		"observations": {Dimensions: map[string]MetricDimension{
			"whole":      {Datatype: DataTypeInteger},
			"fractional": {Datatype: DataTypeFloat},
		}},
	}}
	for _, test := range []struct {
		name, aggregation, field string
		want                     LogicalDataType
	}{
		{"integer sum", "sum", "whole", DataTypeDecimal},
		{"integer average", "avg", "whole", DataTypeDecimal},
		{"integer minimum", "min", "whole", DataTypeInteger},
		{"integer maximum", "max", "whole", DataTypeInteger},
		{"integer count", "count", "whole", DataTypeInteger},
		{"float distinct count", "count_distinct", "fractional", DataTypeInteger},
		{"float sum", "sum", "fractional", DataTypeFloat},
		{"float average", "avg", "fractional", DataTypeFloat},
	} {
		t.Run(test.name, func(t *testing.T) {
			metric := Metric{Type: "aggregate", Aggregation: test.aggregation, Input: &MetricInput{Field: "observations." + test.field}, Format: "integer"}
			model.Metrics = map[string]Metric{"value": metric}
			got, err := model.MetricDataType("value")
			if err != nil || got != test.want {
				t.Fatalf("MetricDataType = %q, %v; want %q despite integer display format", got, err, test.want)
			}
			got, err = model.MetricDataTypeFor(metric)
			if err != nil || got != test.want {
				t.Fatalf("MetricDataTypeFor = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

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
