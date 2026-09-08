package query

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/query/planir"
)

func TestAggregatePlanIRAppliesDimensionMasksToFinalProjection(t *testing.T) {
	dimensions := []struct {
		name      string
		selection Field
		maskField string
		source    string
	}{
		{name: "physical alias", selection: Field{Field: "orders.status", Alias: "status_label"}, maskField: "orders.status", source: "orders.status"},
		{name: "physical leaf", selection: Field{Field: "orders.status", Alias: "status_label"}, maskField: "status", source: "orders.status"},
		{name: "semantic alias via physical binding", selection: Field{Field: "customer_state", Alias: "state_label"}, maskField: "customers.state", source: "customer_state"},
	}
	masks := []struct {
		name        string
		value       string
		sql         string
		nullability OutputNullability
	}{
		{name: "null", value: "null", sql: "NULL", nullability: OutputNullable},
		{name: "redact", value: "redact", sql: "'REDACTED'", nullability: OutputDefinitelyNonNull},
		{name: "zero", value: "zero", sql: "0", nullability: OutputDefinitelyNonNull},
	}
	for _, dimension := range dimensions {
		for _, mask := range masks {
			t.Run(dimension.name+"/"+mask.name, func(t *testing.T) {
				planner := mustNewCompiledPlanner(t, testModel())
				plan, err := planner.Plan(Request{
					Dimensions:  []Field{dimension.selection},
					Metrics:     []Field{{Field: "order_count"}},
					ColumnMasks: []ColumnMask{{Field: dimension.maskField, Mask: mask.value}},
				})
				if err != nil {
					t.Fatal(err)
				}
				output, ok := plan.IR.Nodes[plan.IR.Output].(planir.SortLimit)
				if !ok {
					t.Fatalf("output node = %T, want SortLimit", plan.IR.Nodes[plan.IR.Output])
				}
				if len(output.Projection) < 2 {
					t.Fatalf("final projection = %#v, want dimension and metric", output.Projection)
				}
				projection := output.Projection[0]
				if projection.Name != dimension.selection.Alias || projection.Source != dimension.source || projection.Mask != mask.value {
					t.Fatalf("masked dimension projection = %#v, want alias=%q source=%q mask=%q", projection, dimension.selection.Alias, dimension.source, mask.value)
				}
				if !strings.Contains(plan.SQL, mask.sql+` AS "`+dimension.selection.Alias+`"`) {
					t.Fatalf("rendered SQL omitted %q mask for %q:\n%s", mask.value, dimension.selection.Alias, plan.SQL)
				}
				descriptor, err := planner.DescribeOutputSchema(plan)
				if err != nil {
					t.Fatal(err)
				}
				if descriptor.Fields[0].Alias != dimension.selection.Alias || descriptor.Fields[0].Nullability != mask.nullability {
					t.Fatalf("masked output schema field = %#v, want alias=%q nullability=%q", descriptor.Fields[0], dimension.selection.Alias, mask.nullability)
				}
			})
		}
	}
}

func TestAggregatePlanIRDoesNotMatchOutputAliasAsPhysicalMask(t *testing.T) {
	planner := mustNewCompiledPlanner(t, testModel())
	plan, err := planner.Plan(Request{
		Dimensions:  []Field{{Field: "orders.status", Alias: "email"}},
		Metrics:     []Field{{Field: "order_count"}},
		ColumnMasks: []ColumnMask{{Field: "email", Mask: "redact"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	output, ok := plan.IR.Nodes[plan.IR.Output].(planir.SortLimit)
	if !ok {
		t.Fatalf("output node = %T, want SortLimit", plan.IR.Nodes[plan.IR.Output])
	}
	if got := output.Projection[0].Mask; got != "" {
		t.Fatalf("output alias selected a physical mask = %q, want no mask", got)
	}
}

func TestAggregatePlanIRUsesOnlySelectedSemanticBindingsForMasks(t *testing.T) {
	planner := mustNewCompiledPlanner(t, testModel())
	tests := []struct {
		name      string
		maskField string
		wantMask  string
	}{
		{name: "selected binding", maskField: "customers.state", wantMask: "redact"},
		{name: "unrelated physical field", maskField: "orders.status", wantMask: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := planner.Plan(Request{
				Dimensions:  []Field{{Field: "customer_state", Alias: "state_label"}},
				Metrics:     []Field{{Field: "order_count"}, {Field: "tag_count"}},
				ColumnMasks: []ColumnMask{{Field: test.maskField, Mask: "redact"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			output, ok := plan.IR.Nodes[plan.IR.Output].(planir.SortLimit)
			if !ok {
				t.Fatalf("output node = %T, want SortLimit", plan.IR.Nodes[plan.IR.Output])
			}
			if got := output.Projection[0].Mask; got != test.wantMask {
				t.Fatalf("semantic projection mask = %q, want %q", got, test.wantMask)
			}
		})
	}
}

func TestDescribeOutputSchemaUsesRenderedMaskLogicalTypes(t *testing.T) {
	tests := []struct {
		name            string
		dimension       Field
		maskField       string
		mask            string
		wantType        string
		wantNullability OutputNullability
	}{
		{name: "numeric redact", dimension: Field{Field: "orders.revenue", Alias: "revenue"}, maskField: "orders.revenue", mask: "redact", wantType: "string", wantNullability: OutputDefinitelyNonNull},
		{name: "date redact", dimension: Field{Field: "activity_date", Alias: "activity"}, maskField: "orders.ordered_at", mask: "redact", wantType: "string", wantNullability: OutputDefinitelyNonNull},
		{name: "text zero", dimension: Field{Field: "orders.status", Alias: "status"}, maskField: "orders.status", mask: "zero", wantType: "integer", wantNullability: OutputDefinitelyNonNull},
		{name: "numeric null preserves source type", dimension: Field{Field: "orders.revenue", Alias: "revenue"}, maskField: "orders.revenue", mask: "null", wantType: "number", wantNullability: OutputNullable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			planner := mustNewCompiledPlanner(t, testModel())
			plan, err := planner.Plan(Request{
				Dimensions:  []Field{test.dimension},
				Metrics:     []Field{{Field: "order_count"}},
				ColumnMasks: []ColumnMask{{Field: test.maskField, Mask: test.mask}},
			})
			if err != nil {
				t.Fatal(err)
			}
			descriptor, err := planner.DescribeOutputSchema(plan)
			if err != nil {
				t.Fatal(err)
			}
			if len(descriptor.Fields) == 0 {
				t.Fatal("output descriptor has no fields")
			}
			field := descriptor.Fields[0]
			if field.LogicalType != test.wantType || field.Nullability != test.wantNullability {
				t.Fatalf("masked descriptor = %#v, want type=%q nullability=%q", field, test.wantType, test.wantNullability)
			}
		})
	}
}

func TestAggregatePlanIRRejectsMaskedMetricDependencies(t *testing.T) {
	for _, field := range []string{"orders.order_id", "order_id"} {
		t.Run(field, func(t *testing.T) {
			planner := mustNewCompiledPlanner(t, testModel())
			_, err := planner.Plan(Request{
				Metrics:     []Field{{Field: "order_count"}},
				ColumnMasks: []ColumnMask{{Field: field, Mask: "null"}},
			})
			if err == nil || !strings.Contains(err.Error(), `metric "order_count" depends on a masked field`) {
				t.Fatalf("masked metric dependency error = %v, want dependency rejection", err)
			}
		})
	}
}

func TestAggregatePlanIRRejectsConflictingSelectedSemanticMasks(t *testing.T) {
	planner := mustNewCompiledPlanner(t, testModel())
	_, err := planner.Plan(Request{
		Dimensions: []Field{{Field: "activity_date"}},
		Metrics:    []Field{{Field: "order_count"}, {Field: "tag_count"}},
		ColumnMasks: []ColumnMask{
			{Field: "orders.ordered_at", Mask: "redact"},
			{Field: "tags.tagged_at", Mask: "zero"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), `conflicting masks for dimension "activity_date"`) {
		t.Fatalf("conflicting semantic masks error = %v, want fail-closed rejection", err)
	}
}
