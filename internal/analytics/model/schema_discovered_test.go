package model

import "testing"

func TestLogicalDataTypeFromPhysicalType(t *testing.T) {
	tests := map[string]LogicalDataType{
		"VARCHAR":                     DataTypeString,
		"DECIMAL(18, 2)":              DataTypeDecimal,
		"BIGINT":                      DataTypeInteger,
		"DOUBLE":                      DataTypeFloat,
		"TIMESTAMP WITH TIME ZONE":    DataTypeDateTimeTZ,
		"TIMESTAMP(6) WITH TIME ZONE": DataTypeDateTimeTZ,
		"TIMESTAMP WITHOUT TIME ZONE": DataTypeDateTime,
		"BLOB":                        DataTypeOpaque,
	}
	for physical, want := range tests {
		if got := LogicalDataTypeFromPhysicalType(physical); got != want {
			t.Fatalf("LogicalDataTypeFromPhysicalType(%q) = %q, want %q", physical, got, want)
		}
	}
}

func TestValidateDiscoveredSchemasRejectsIncompatibleAuthoredDatatype(t *testing.T) {
	model := &Model{
		Sources: map[string]Source{"source": {Schema: TableSchema{Columns: []ColumnSchema{{Name: "id", PhysicalType: "BIGINT"}}}}},
		Tables: map[string]Table{"orders": {
			Execution: ExecutionDefinition{Source: "source"}, GrainEntity: "order", Entities: map[string]EntityDefinition{"order": {Type: "primary", Fields: []string{"id"}}},
			Columns: map[string]ModelColumn{"id": {Datatype: DataTypeString}},
			Schema:  TableSchema{Columns: []ColumnSchema{{Name: "id", PhysicalType: "BIGINT"}}},
		}},
	}
	if err := model.ValidateDiscoveredSchemas(); err == nil {
		t.Fatal("ValidateDiscoveredSchemas accepted incompatible datatype")
	}
}

func TestResolveDiscoveredModelFieldsDerivesUndeclaredAndUntypedFields(t *testing.T) {
	nullable := true
	model := &Model{Tables: map[string]Table{"customers": {
		Dimensions: map[string]MetricDimension{"customer_id": {Label: "Customer ID"}},
		Schema: TableSchema{Columns: []ColumnSchema{
			{Name: "customer_id", PhysicalType: "VARCHAR", Nullable: &nullable},
			{Name: "lifetime_value", PhysicalType: "DECIMAL(18,2)", Nullable: &nullable},
		}},
	}}}

	if err := model.ResolveDiscoveredModelFields(); err != nil {
		t.Fatal(err)
	}
	table := model.Tables["customers"]
	if got := table.Dimensions["customer_id"]; got.Datatype != DataTypeString || got.Label != "Customer ID" {
		t.Fatalf("documented field = %#v", got)
	}
	if got := table.Dimensions["lifetime_value"]; got.Datatype != DataTypeDecimal || got.Label != "Lifetime value" {
		t.Fatalf("inferred field = %#v", got)
	}
	if got := table.Columns["lifetime_value"]; got.Datatype != DataTypeDecimal || got.SourceField != "lifetime_value" {
		t.Fatalf("inferred column = %#v", got)
	}
}

func TestResolveDiscoveredModelFieldsRejectsMissingDocumentedField(t *testing.T) {
	model := &Model{Tables: map[string]Table{"customers": {
		Dimensions: map[string]MetricDimension{"missing": {Label: "Missing"}},
		Schema:     TableSchema{Columns: []ColumnSchema{{Name: "customer_id", PhysicalType: "VARCHAR"}}},
	}}}

	if err := model.ResolveDiscoveredModelFields(); err == nil {
		t.Fatal("ResolveDiscoveredModelFields accepted documented field missing from DuckLake schema")
	}
}
