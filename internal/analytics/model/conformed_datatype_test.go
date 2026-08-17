package model

import (
	"strings"
	"testing"
)

func TestConformedBindingRequiresExactLogicalDatatype(t *testing.T) {
	table := Table{Dimensions: map[string]MetricDimension{
		"event_date":    {Type: "date", Datatype: DataTypeDate},
		"event_time":    {Type: "timestamp", Datatype: DataTypeDateTime},
		"event_instant": {Type: "timestamp", Datatype: DataTypeDateTimeTZ},
	}}
	base := func(datatype LogicalDataType, field string) *Model {
		return &Model{
			Name:   "exact_types",
			Tables: map[string]Table{"orders": table},
			Dimensions: map[string]SemanticDimension{
				"when": {Type: "timestamp", Datatype: datatype, Bindings: map[string]DimensionBinding{"orders": {Field: "orders." + field}}},
			},
			Metrics: map[string]Metric{
				"rows": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &MetricInput{Field: "orders." + field}},
			},
		}
	}

	if err := base(DataTypeDate, "event_time").validateSemanticDefinitions(); err == nil || !strings.Contains(err.Error(), "logical datatype") {
		t.Fatalf("Date/DateTime mismatch error = %v", err)
	}
	if err := base(DataTypeDateTime, "event_instant").validateSemanticDefinitions(); err == nil || !strings.Contains(err.Error(), "logical datatype") {
		t.Fatalf("DateTime/DateTimeTz mismatch error = %v", err)
	}
	if err := base(DataTypeDateTime, "event_time").validateSemanticDefinitions(); err != nil {
		t.Fatalf("matching DateTime binding rejected: %v", err)
	}
}
