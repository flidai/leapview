package model

import (
	"strings"
	"testing"
)

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
		AuthoredFields: map[string]ModelFieldDeclaration{"customer_id": {Label: "Customer ID"}},
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

func TestResolveDiscoveredModelFieldsRebuildsInferredFieldsFromAuthoredOverlay(t *testing.T) {
	model := &Model{Tables: map[string]Table{"orders": {
		AuthoredFields: map[string]ModelFieldDeclaration{
			"order_id": {Datatype: DataTypeString, Label: "Order ID"},
		},
		Schema: TableSchema{Columns: []ColumnSchema{
			{Name: "order_id", PhysicalType: "VARCHAR"},
			{Name: "legacy_status", PhysicalType: "VARCHAR"},
		}},
	}}}

	if err := model.ResolveDiscoveredModelFields(); err != nil {
		t.Fatal(err)
	}
	table := model.Tables["orders"]
	table.Schema = TableSchema{Columns: []ColumnSchema{
		{Name: "order_id", PhysicalType: "VARCHAR"},
		{Name: "current_status", PhysicalType: "VARCHAR"},
	}}
	model.Tables["orders"] = table
	if err := model.ResolveDiscoveredModelFields(); err != nil {
		t.Fatalf("rediscovery treated an inferred field as authored: %v", err)
	}
	table = model.Tables["orders"]
	if _, ok := table.Columns["legacy_status"]; ok {
		t.Fatal("rediscovery retained removed inferred field")
	}
	if _, ok := table.Columns["current_status"]; !ok {
		t.Fatal("rediscovery omitted new inferred field")
	}
	if got := table.Dimensions["order_id"]; got.Label != "Order ID" || got.Datatype != DataTypeString {
		t.Fatalf("authored overlay after rediscovery = %#v", got)
	}
}

func TestResolveDiscoveredModelFieldsRejectsMissingAuthoredField(t *testing.T) {
	model := &Model{Tables: map[string]Table{"orders": {
		AuthoredFields: map[string]ModelFieldDeclaration{"revenue": {}},
		Schema:         TableSchema{Columns: []ColumnSchema{{Name: "order_id", PhysicalType: "VARCHAR"}}},
	}}}

	if err := model.ResolveDiscoveredModelFields(); err == nil || !strings.Contains(err.Error(), `authored field "revenue" is not in discovered output`) {
		t.Fatalf("missing authored field error = %v", err)
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
