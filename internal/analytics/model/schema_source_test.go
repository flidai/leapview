package model

import "testing"

func TestValidateDiscoveredSourceSchemaModesAndNullability(t *testing.T) {
	nullable := true
	nonNull := false
	base := func(mode string, fields map[string]SourceField) *Model {
		return &Model{Sources: map[string]Source{"orders": {
			SchemaMode: mode, Fields: fields,
			Schema: TableSchema{Columns: []ColumnSchema{{Name: "id", PhysicalType: "INTEGER", Nullable: &nullable}, {Name: "extra", PhysicalType: "VARCHAR", Nullable: &nonNull}}},
		}}}
	}
	if err := base("inferred", nil).ValidateDiscoveredSourceSchemas(); err != nil {
		t.Fatalf("inferred mode error = %v", err)
	}
	if err := base("compatible", map[string]SourceField{"id": {Datatype: DataTypeInteger, Nullable: &nullable}}).ValidateDiscoveredSourceSchemas(); err != nil {
		t.Fatalf("compatible mode error = %v", err)
	}
	if err := base("compatible", map[string]SourceField{"id": {Datatype: DataTypeString}}).ValidateDiscoveredSourceSchemas(); err == nil {
		t.Fatal("compatible mode accepted incompatible logical datatype")
	}
	if err := base("compatible", map[string]SourceField{"id": {Datatype: DataTypeInteger, Nullable: &nonNull}}).ValidateDiscoveredSourceSchemas(); err == nil {
		t.Fatal("compatible mode accepted nullable physical column for non-null declaration")
	}
	if err := base("strict", map[string]SourceField{"id": {Datatype: DataTypeInteger, Nullable: &nullable}}).ValidateDiscoveredSourceSchemas(); err == nil {
		t.Fatal("strict mode accepted undeclared physical field")
	}
}
