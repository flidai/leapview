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
			Source: "source", GrainEntity: "order", Entities: map[string]ModelEntitySpec{"order": {Type: "primary", Fields: []string{"id"}}},
			Columns: map[string]ModelColumn{"id": {Datatype: DataTypeString}},
			Schema:  TableSchema{Columns: []ColumnSchema{{Name: "id", PhysicalType: "BIGINT"}}},
		}},
	}
	if err := model.ValidateDiscoveredSchemas(); err == nil {
		t.Fatal("ValidateDiscoveredSchemas accepted incompatible datatype")
	}
}
