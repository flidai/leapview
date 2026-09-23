package exploration

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestValidateAgainstModelRejectsNonCanonicalTypedValues(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value ExplorationFilterValue
	}{
		{name: "integer", field: "orders.quantity", value: ExplorationFilterValue{Value: &IntegerExplorationFilterValue{Kind: "integer", Value: "abc"}}},
		{name: "integer leading zero", field: "orders.quantity", value: ExplorationFilterValue{Value: &IntegerExplorationFilterValue{Kind: "integer", Value: "01"}}},
		{name: "decimal", field: "orders.amount", value: ExplorationFilterValue{Value: &DecimalExplorationFilterValue{Kind: "decimal", Value: "abc"}}},
		{name: "decimal exponent", field: "orders.amount", value: ExplorationFilterValue{Value: &DecimalExplorationFilterValue{Kind: "decimal", Value: "1e2"}}},
		{name: "decimal fraction", field: "orders.amount", value: ExplorationFilterValue{Value: &DecimalExplorationFilterValue{Kind: "decimal", Value: "0.5"}}},
		{name: "decimal negative fraction", field: "orders.amount", value: ExplorationFilterValue{Value: &DecimalExplorationFilterValue{Kind: "decimal", Value: "-0.5"}}},
		{name: "date", field: "orders.order_date", value: ExplorationFilterValue{Value: &DateExplorationFilterValue{Kind: "date", Value: "2024-02-30"}}},
		{name: "timestamp", field: "orders.created_at", value: ExplorationFilterValue{Value: &TimestampExplorationFilterValue{Kind: "timestamp", Value: "not-a-timestamp"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := validExplorationSpec()
			spec.Dimensions = []ExplorationDimensionRef{{Field: test.field}}
			spec.Filters = []ExplorationFilter{{
				Field: test.field,
				Expression: ExplorationFilterExpression{Value: &ComparisonExplorationFilterExpression{
					Kind: "comparison", Operator: "equals", Value: test.value,
				}},
			}}
			if err := ValidateAgainstModel(explorationValidationModel(), spec); test.name == "decimal fraction" || test.name == "decimal negative fraction" {
				if err != nil {
					t.Fatalf("rejected canonical decimal: %v", err)
				}
			} else if err == nil {
				t.Fatalf("accepted malformed %s literal", test.name)
			}
		})
	}
}

func TestValidateAgainstModelRequiresTemporalTimeFields(t *testing.T) {
	grain := ExplorationTimeGrainDay
	for _, test := range []struct {
		name string
		spec func() *ExplorationSpec
	}{
		{name: "dimension grain", spec: func() *ExplorationSpec {
			spec := validExplorationSpec()
			spec.Dimensions = []ExplorationDimensionRef{{Field: "orders.status", Grain: &grain}}
			return spec
		}},
		{name: "time selection", spec: func() *ExplorationSpec {
			spec := validExplorationSpec()
			spec.Time = &ExplorationTimeSelection{Field: "orders.status", Grain: grain}
			return spec
		}},
		{name: "time range", spec: func() *ExplorationSpec {
			spec := validExplorationSpec()
			spec.Time = &ExplorationTimeSelection{
				Field: "orders.status", Grain: grain,
				Range: &ExplorationTimeRange{Value: &AbsoluteExplorationTimeRange{
					Kind: "absolute", Lower: &ExplorationTimeBound{Inclusive: true, Value: ExplorationTemporalValue{Value: &DateExplorationTemporalValue{Kind: "date", Value: "2024-01-01"}}},
				}},
			}
			return spec
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateAgainstModel(explorationValidationModel(), test.spec()); err == nil {
				t.Fatalf("accepted non-temporal %s", test.name)
			}
		})
	}
}

func TestValidateShapeRejectsInvalidCanonicalReferences(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ExplorationSpec)
	}{
		{name: "model id", mutate: func(spec *ExplorationSpec) { spec.ModelID = "model id" }},
		{name: "field id", mutate: func(spec *ExplorationSpec) { spec.Dimensions[0].Field = "orders.status!" }},
		{name: "alias", mutate: func(spec *ExplorationSpec) { spec.Dimensions[0].Alias = stringPointer("bad-alias") }},
		{name: "decimal leading zero", mutate: func(spec *ExplorationSpec) {
			spec.Filters = []ExplorationFilter{{Field: "orders.amount", Expression: ExplorationFilterExpression{Value: &ComparisonExplorationFilterExpression{Kind: "comparison", Operator: "equals", Value: ExplorationFilterValue{Value: &DecimalExplorationFilterValue{Kind: "decimal", Value: "01.2"}}}}}}
		}},
		{name: "decimal plus", mutate: func(spec *ExplorationSpec) {
			spec.Filters = []ExplorationFilter{{Field: "orders.amount", Expression: ExplorationFilterExpression{Value: &ComparisonExplorationFilterExpression{Kind: "comparison", Operator: "equals", Value: ExplorationFilterValue{Value: &DecimalExplorationFilterValue{Kind: "decimal", Value: "+1"}}}}}}
		}},
		{name: "mismatched filter discriminator", mutate: func(spec *ExplorationSpec) {
			spec.Filters = []ExplorationFilter{{Field: "orders.status", Expression: ExplorationFilterExpression{Value: &ComparisonExplorationFilterExpression{Kind: "set", Operator: "equals", Value: ExplorationFilterValue{Value: &StringExplorationFilterValue{Kind: "string", Value: "ready"}}}}}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := validExplorationSpec()
			test.mutate(spec)
			if err := ValidateShape(spec); err == nil {
				t.Fatalf("accepted invalid %s", test.name)
			}
		})
	}
}

func TestValidateAgainstModelRejectsIncompatibleOperatorsAndBounds(t *testing.T) {
	spec := validExplorationSpec()
	spec.Filters = []ExplorationFilter{{Field: "orders.status", Expression: ExplorationFilterExpression{Value: &ComparisonExplorationFilterExpression{Kind: "comparison", Operator: "greater_than", Value: ExplorationFilterValue{Value: &StringExplorationFilterValue{Kind: "string", Value: "z"}}}}}}
	if err := ValidateAgainstModel(explorationValidationModel(), spec); err == nil {
		t.Fatal("accepted ordered comparison on text field")
	}

	spec = validExplorationSpec()
	spec.Dimensions = []ExplorationDimensionRef{{Field: "orders.quantity"}}
	spec.Filters = []ExplorationFilter{{Field: "orders.quantity", Expression: ExplorationFilterExpression{Value: &RangeExplorationFilterExpression{Kind: "range", Lower: &ExplorationFilterBound{Inclusive: true, Value: ExplorationFilterValue{Value: &IntegerExplorationFilterValue{Kind: "integer", Value: "10"}}}, Upper: &ExplorationFilterBound{Inclusive: true, Value: ExplorationFilterValue{Value: &IntegerExplorationFilterValue{Kind: "integer", Value: "2"}}}}}}}
	if err := ValidateAgainstModel(explorationValidationModel(), spec); err == nil {
		t.Fatal("accepted reversed numeric range")
	}

	spec = validExplorationSpec()
	spec.Dimensions = []ExplorationDimensionRef{{Field: "orders.amount"}}
	spec.Filters = []ExplorationFilter{{Field: "orders.amount", Expression: ExplorationFilterExpression{Value: &RangeExplorationFilterExpression{Kind: "range", Lower: &ExplorationFilterBound{Inclusive: true, Value: ExplorationFilterValue{Value: &DecimalExplorationFilterValue{Kind: "decimal", Value: "1.2"}}}, Upper: &ExplorationFilterBound{Inclusive: true, Value: ExplorationFilterValue{Value: &IntegerExplorationFilterValue{Kind: "integer", Value: "2"}}}}}}}
	if err := ValidateAgainstModel(explorationValidationModel(), spec); err == nil {
		t.Fatal("accepted range bounds with incompatible kinds")
	}

	spec = validExplorationSpec()
	spec.Dimensions = []ExplorationDimensionRef{{Field: "orders.order_date"}}
	spec.Filters = []ExplorationFilter{{Field: "orders.order_date", Expression: ExplorationFilterExpression{Value: &RangeExplorationFilterExpression{Kind: "range", Lower: &ExplorationFilterBound{Inclusive: true, Value: ExplorationFilterValue{Value: &DateExplorationFilterValue{Kind: "date", Value: "2024-02-01"}}}, Upper: &ExplorationFilterBound{Inclusive: true, Value: ExplorationFilterValue{Value: &DateExplorationFilterValue{Kind: "date", Value: "2024-01-01"}}}}}}}
	if err := ValidateAgainstModel(explorationValidationModel(), spec); err == nil {
		t.Fatal("accepted reversed date range")
	}
}

func TestValidateAgainstModelRejectsIncompatibleTemporalRanges(t *testing.T) {
	spec := validExplorationSpec()
	spec.Time = &ExplorationTimeSelection{Field: "orders.order_date", Grain: ExplorationTimeGrainDay, Range: &ExplorationTimeRange{Value: &RelativeExplorationTimeRange{Kind: "relative", Direction: ExplorationRelativeDirectionPrevious, Count: 1, Unit: ExplorationRelativeUnitHour, IncludeCurrent: false, Anchor: ExplorationRelativeAnchorCurrentTime}}}
	if err := ValidateAgainstModel(explorationValidationModel(), spec); err == nil {
		t.Fatal("accepted hour relative period for date-only field")
	}

	spec = validExplorationSpec()
	spec.Time = &ExplorationTimeSelection{Field: "orders.created_at", Grain: ExplorationTimeGrainDay, Range: &ExplorationTimeRange{Value: &AbsoluteExplorationTimeRange{Kind: "absolute", Lower: &ExplorationTimeBound{Inclusive: true, Value: ExplorationTemporalValue{Value: &DateExplorationTemporalValue{Kind: "date", Value: "2024-01-01"}}}}}}
	if err := ValidateAgainstModel(explorationValidationModel(), spec); err == nil {
		t.Fatal("accepted date bound for timestamp field")
	}

	spec = validExplorationSpec()
	spec.Time = &ExplorationTimeSelection{Field: "orders.created_at", Grain: ExplorationTimeGrainDay, Range: &ExplorationTimeRange{Value: &AbsoluteExplorationTimeRange{Kind: "absolute", Lower: &ExplorationTimeBound{Inclusive: true, Value: ExplorationTemporalValue{Value: &TimestampExplorationTemporalValue{Kind: "timestamp", Value: "2024-02-01T00:00:00Z"}}}, Upper: &ExplorationTimeBound{Inclusive: true, Value: ExplorationTemporalValue{Value: &TimestampExplorationTemporalValue{Kind: "timestamp", Value: "2024-01-01T00:00:00Z"}}}}}}
	if err := ValidateAgainstModel(explorationValidationModel(), spec); err == nil {
		t.Fatal("accepted reversed timestamp range")
	}
}

func TestValidateAgainstModelHonorsSemanticGrainAllowlist(t *testing.T) {
	day := ExplorationTimeGrainDay
	spec := validExplorationSpec()
	spec.Dimensions = []ExplorationDimensionRef{{Field: "order_day", Grain: &day}}
	model := explorationValidationModel()
	model.Dimensions = map[string]semanticmodel.SemanticDimension{
		"order_day": {Type: "date", Datatype: semanticmodel.DataTypeDate, Grains: []string{"month"}, NativeGrain: "month"},
	}
	if err := ValidateAgainstModel(model, spec); err == nil {
		t.Fatal("accepted grain outside semantic dimension allowlist")
	}
}

func TestValidateAgainstModelUsesSemanticDatasetAliases(t *testing.T) {
	spec := validExplorationSpec()
	dataset := "sales_orders"
	spec.DatasetID = &dataset
	model := explorationValidationModel()
	model.Datasets = map[string]semanticmodel.SemanticDatasetSpec{
		"sales_orders": {Model: "warehouse_orders"},
	}
	if err := ValidateAgainstModel(model, spec); err != nil {
		t.Fatalf("rejected semantic dataset alias: %v", err)
	}

	filterDataset := "sales_orders"
	spec.Filters = []ExplorationFilter{{
		Field:      "orders.status",
		DatasetID:  &filterDataset,
		Expression: ExplorationFilterExpression{Value: &UnfilteredExplorationFilterExpression{Kind: "unfiltered"}},
	}}
	if err := ValidateAgainstModel(model, spec); err != nil {
		t.Fatalf("rejected filter semantic dataset alias: %v", err)
	}
}

func TestValidateAgainstModelRejectsNonTemporalPivotGrain(t *testing.T) {
	grain := ExplorationTimeGrainDay
	spec := validExplorationSpec()
	spec.Pivot = &ExplorationPivotConfig{
		Rows:    []ExplorationDimensionRef{{Field: "orders.status", Grain: &grain}},
		Columns: []ExplorationDimensionRef{},
		Metrics: []ExplorationMetricRef{{Field: "order_count"}},
	}
	if err := ValidateAgainstModel(explorationValidationModel(), spec); err == nil {
		t.Fatal("accepted non-temporal pivot grain")
	}
}

func TestValidateAgainstModelAllowsTimeAliasInDurableReferences(t *testing.T) {
	spec := validExplorationSpec()
	spec.Time = &ExplorationTimeSelection{Field: "orders.order_date", Grain: ExplorationTimeGrainDay, Alias: stringPointer("orderDay")}
	spec.Sort = []ExplorationSort{{Field: "orderDay", Direction: ExplorationSortDirectionAsc}}
	if err := ValidateAgainstModel(explorationValidationModel(), spec); err != nil {
		t.Fatalf("rejected time alias sort reference: %v", err)
	}
}

func validExplorationSpec() *ExplorationSpec {
	return &ExplorationSpec{
		SchemaVersion: 1,
		ModelID:       "semantic:sales",
		Dimensions:    []ExplorationDimensionRef{{Field: "orders.status"}},
		Metrics:       []ExplorationMetricRef{{Field: "order_count"}},
		Filters:       []ExplorationFilter{},
		Sort:          []ExplorationSort{},
		Limit:         100,
	}
}

func stringPointer(value string) *string { return &value }

func explorationValidationModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Tables: map[string]semanticmodel.Table{
			"orders": {Dimensions: map[string]semanticmodel.MetricDimension{
				"status":     {Field: "orders.status", Type: "string", Datatype: semanticmodel.DataTypeString},
				"quantity":   {Field: "orders.quantity", Type: "number", Datatype: semanticmodel.DataTypeInteger},
				"amount":     {Field: "orders.amount", Type: "number", Datatype: semanticmodel.DataTypeDecimal},
				"order_date": {Field: "orders.order_date", Type: "date", Datatype: semanticmodel.DataTypeDate},
				"created_at": {Field: "orders.created_at", Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ},
			}},
		},
		Metrics: map[string]semanticmodel.Metric{"order_count": {Type: "aggregate"}},
	}
}
