package query

import (
	"encoding/json"
	"reflect"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestDescribeOutputSchemaDerivesGovernedRowNullability(t *testing.T) {
	planner := mustNewCompiledPlanner(t, outputSchemaTestModel())
	plan, err := planner.PlanRows(RowRequest{
		Dataset: "orders",
		Dimensions: []Field{
			{Field: "orders.order_id", Alias: "id"},
			{Field: "orders.status", Alias: "order_status"},
			{Field: "customer_state", Alias: "customer_state"},
			{Field: "orders.unclassified", Alias: "unclassified"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	descriptor, err := planner.DescribeOutputSchema(plan)
	if err != nil {
		t.Fatal(err)
	}
	wantAliases := []string{"id", "order_status", "customer_state", "unclassified"}
	wantTypes := []string{"integer", "string", "string", "string"}
	wantNullability := []OutputNullability{
		OutputDefinitelyNonNull,
		OutputNullable,
		OutputNullable,
		OutputNullabilityUnknown,
	}
	if len(descriptor.Fields) != len(wantAliases) {
		t.Fatalf("descriptor fields = %d, want %d", len(descriptor.Fields), len(wantAliases))
	}
	for index, field := range descriptor.Fields {
		if field.Alias != wantAliases[index] || field.LogicalType != wantTypes[index] || field.Nullability != wantNullability[index] {
			t.Fatalf("field %d = %#v, want alias=%q type=%q nullability=%q", index, field, wantAliases[index], wantTypes[index], wantNullability[index])
		}
	}
	if !descriptor.Fields[2].Provenance.relationshipNullable || len(descriptor.Fields[2].Provenance.relationshipPath) != 1 {
		t.Fatalf("joined field provenance = %#v", descriptor.Fields[2].Provenance)
	}
	if descriptor.Fields[0].ArrowNullable() {
		t.Fatal("proved local NOT NULL field serialized as nullable")
	}
	if !descriptor.Fields[3].ArrowNullable() {
		t.Fatal("unknown source nullability serialized as non-null")
	}
}

func TestDescribeOutputSchemaAppliesMaskNullability(t *testing.T) {
	tests := []struct {
		name string
		mask string
		want OutputNullability
	}{
		{name: "null introduces null", mask: "null", want: OutputNullable},
		{name: "redaction replaces with value", mask: "redact", want: OutputDefinitelyNonNull},
		{name: "zero replaces with value", mask: "zero", want: OutputDefinitelyNonNull},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			planner := mustNewCompiledPlanner(t, outputSchemaTestModel())
			plan, err := planner.PlanRows(RowRequest{
				Dataset:     "orders",
				Dimensions:  []Field{{Field: "orders.status", Alias: "status"}},
				ColumnMasks: []ColumnMask{{Field: "orders.status", Mask: test.mask}},
			})
			if err != nil {
				t.Fatal(err)
			}
			descriptor, err := planner.DescribeOutputSchema(plan)
			if err != nil {
				t.Fatal(err)
			}
			if got := descriptor.Fields[0].Nullability; got != test.want {
				t.Fatalf("mask %q nullability = %q, want %q", test.mask, got, test.want)
			}
		})
	}
}

func TestDescribeOutputSchemaDerivesMetricAndCalculationNullability(t *testing.T) {
	planner := mustNewCompiledPlanner(t, outputSchemaTestModel())
	plan, err := planner.Plan(Request{Metrics: []Field{
		{Field: "order_count", Alias: "count"},
		{Field: "nullable_revenue", Alias: "nullable_total"},
		{Field: "revenue_or_zero", Alias: "filled_total"},
		{Field: "revenue_ratio", Alias: "ratio"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := planner.DescribeOutputSchema(plan)
	if err != nil {
		t.Fatal(err)
	}
	want := []OutputNullability{
		OutputDefinitelyNonNull,
		OutputNullable,
		OutputDefinitelyNonNull,
		OutputNullable,
	}
	for index, field := range descriptor.Fields {
		if field.Nullability != want[index] {
			t.Fatalf("metric field %q nullability = %q, want %q", field.Alias, field.Nullability, want[index])
		}
	}
}

func TestDescribeOutputSchemaRoundRequiresNonNullOptionalDigits(t *testing.T) {
	model := outputSchemaTestModel()
	model.Metrics["nullable_digits"] = semanticmodel.Metric{
		Type: "derived", Expression: "nullif(${order_count}, ${order_count})",
	}
	model.Metrics["rounded"] = semanticmodel.Metric{
		Type: "derived", Expression: "round(${order_count}, ${nullable_digits})",
	}
	planner := mustNewCompiledPlanner(t, model)
	plan, err := planner.Plan(Request{Metrics: []Field{{Field: "rounded"}}})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := planner.DescribeOutputSchema(plan)
	if err != nil {
		t.Fatal(err)
	}
	if got := descriptor.Fields[0].Nullability; got != OutputNullable {
		t.Fatalf("round with nullable digits nullability = %q, want %q", got, OutputNullable)
	}
}

func TestDescribeOutputSchemaDoesNotInferFromPaginationOrExposeProvenance(t *testing.T) {
	planner := mustNewCompiledPlanner(t, outputSchemaTestModel())
	request := RowRequest{Dataset: "orders", Dimensions: []Field{{Field: "orders.order_id", Alias: "id"}, {Field: "orders.status", Alias: "status"}}}
	ordinary, err := planner.PlanRows(request)
	if err != nil {
		t.Fatal(err)
	}
	emptyPage, err := planner.PlanRows(RowRequest{Dataset: request.Dataset, Dimensions: request.Dimensions, Limit: 1, Offset: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	ordinaryDescriptor, err := planner.DescribeOutputSchema(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	emptyDescriptor, err := planner.DescribeOutputSchema(emptyPage)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ordinaryDescriptor, emptyDescriptor) {
		t.Fatalf("empty-page descriptor changed:\nordinary=%#v\nempty=%#v", ordinaryDescriptor, emptyDescriptor)
	}

	payload, err := json.Marshal(ordinaryDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != `{"fields":[{"alias":"id","logical_type":"integer","nullability":"definitely_non_null"},{"alias":"status","logical_type":"string","nullability":"nullable"}]}` {
		t.Fatalf("serialized descriptor exposed internal provenance: %s", payload)
	}
}

func outputSchemaTestModel() *semanticmodel.Model {
	model := testModel()
	nonNull := false
	nullable := true
	model.Tables["orders"] = outputSchemaTable(model.Tables["orders"], []semanticmodel.ColumnSchema{
		{Name: "order_id", Nullable: &nonNull},
		{Name: "customer_id", Nullable: &nonNull},
		{Name: "status", Nullable: &nullable},
		{Name: "revenue", Nullable: &nullable},
		{Name: "unclassified"},
	})
	model.Tables["customers"] = outputSchemaTable(model.Tables["customers"], []semanticmodel.ColumnSchema{
		{Name: "customer_id", Nullable: &nonNull},
		{Name: "state", Nullable: &nonNull},
	})
	model.Tables["tags"] = outputSchemaTable(model.Tables["tags"], []semanticmodel.ColumnSchema{
		{Name: "tag_id", Nullable: &nonNull},
		{Name: "customer_id", Nullable: &nonNull},
	})
	orders := model.Tables["orders"]
	orders.Dimensions["unclassified"] = semanticmodel.MetricDimension{Type: "string", Datatype: semanticmodel.DataTypeString}
	model.Tables["orders"] = orders
	model.Metrics["nullable_revenue"] = semanticmodel.Metric{
		Type: "aggregate", Dataset: "orders", Aggregation: "sum",
		Input: &semanticmodel.MetricInput{Field: "orders.revenue"}, Empty: "null",
	}
	model.Metrics["revenue_or_zero"] = semanticmodel.Metric{Type: "derived", Expression: "coalesce(${nullable_revenue}, 0)"}
	model.Metrics["revenue_ratio"] = semanticmodel.Metric{Type: "derived", Expression: "safe_divide(${nullable_revenue}, ${order_count})"}
	return model
}

func outputSchemaTable(table semanticmodel.Table, columns []semanticmodel.ColumnSchema) semanticmodel.Table {
	table.Schema = semanticmodel.TableSchema{Columns: columns}
	return table
}
