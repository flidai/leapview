package model

import (
	"strings"
	"testing"
)

func TestValidateDiscoveredSourceSchemaModesAndTypes(t *testing.T) {
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
	if err := base("compatible", map[string]SourceField{"id": {Datatype: DataTypeInteger, Nullable: &nonNull}}).ValidateDiscoveredSourceSchemas(); err != nil {
		t.Fatalf("physical nullability is observed separately from row checks: %v", err)
	}
	if err := base("strict", map[string]SourceField{"id": {Datatype: DataTypeInteger, Nullable: &nullable}}).ValidateDiscoveredSourceSchemas(); err == nil {
		t.Fatal("strict mode accepted undeclared physical field")
	}
}

func TestValidateDiscoveredSourceSchemaFreshnessField(t *testing.T) {
	model := &Model{Sources: map[string]Source{"orders": {
		SchemaMode: "inferred",
		Schema:     TableSchema{Columns: []ColumnSchema{{Name: "id", PhysicalType: "INTEGER"}}},
		Freshness:  &SourceFreshnessSpec{Basis: "field", Field: "updated_at"},
	}}}
	if err := model.ValidateDiscoveredSourceSchemas(); err == nil {
		t.Fatal("freshness field absent from discovered schema was accepted")
	}
}

func TestSourceAndModelAuthoredOpaqueDatatypeShareValidationPolicy(t *testing.T) {
	if err := ValidateDiscoveredDatatype("orders", "payload", DataTypeOpaque, "BLOB"); err == nil {
		t.Fatal("Model validator accepted an unverifiable authored Opaque datatype")
	}
	source := &Model{Sources: map[string]Source{"orders": {
		SchemaMode: "compatible",
		Fields:     map[string]SourceField{"payload": {Datatype: DataTypeOpaque}},
		Schema:     TableSchema{Columns: []ColumnSchema{{Name: "payload", PhysicalType: "BLOB"}}},
	}}}
	if err := source.ValidateDiscoveredSourceSchemas(); err == nil {
		t.Fatal("Source validator accepted an authored Opaque datatype that Model rejects")
	}
}

func TestValidateDiscoveredSourceCheckFields(t *testing.T) {
	model := &Model{Sources: map[string]Source{"orders": {
		SchemaMode: "compatible",
		Schema:     TableSchema{Columns: []ColumnSchema{{Name: "id", PhysicalType: "INTEGER"}}},
		Checks:     []ModelCheck{{ID: "missing", Type: "non_null", Field: "customer_id", Severity: "error"}},
	}}}
	if err := model.ValidateDiscoveredSourceSchemas(); err == nil {
		t.Fatal("source check accepted an undiscovered field")
	}
	model.Sources["orders"] = Source{SchemaMode: "compatible", Schema: TableSchema{Columns: []ColumnSchema{{Name: "id", PhysicalType: "INTEGER"}}}, Checks: []ModelCheck{{ID: "present", Type: "non_null", Field: "id", Severity: "error"}}}
	if err := model.ValidateDiscoveredSourceSchemas(); err != nil {
		t.Fatalf("source check rejected a discovered but undeclared field: %v", err)
	}
}

func TestValidateDiscoveredSourceRelationshipTypesMatchModelPolicy(t *testing.T) {
	model := &Model{Sources: map[string]Source{
		"orders": {
			Schema: TableSchema{Columns: []ColumnSchema{{Name: "customer_id", PhysicalType: "INTEGER"}}},
			Checks: []ModelCheck{{ID: "customer_exists", Type: "relationship", Field: "customer_id", To: "customers.id", Severity: "error"}},
		},
		"customers": {Schema: TableSchema{Columns: []ColumnSchema{{Name: "id", PhysicalType: "VARCHAR"}}}},
	}}
	if err := model.ValidateDiscoveredSourceSchemas(); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("incompatible Source relationship result = %v", err)
	}
	model.Sources["customers"] = Source{Schema: TableSchema{Columns: []ColumnSchema{{Name: "id", PhysicalType: "BIGINT"}}}}
	if err := model.ValidateDiscoveredSourceSchemas(); err != nil {
		t.Fatalf("matching Source relationship result = %v", err)
	}
}

func TestModelCompatibleAndStrictFieldSetsMatchSourcePolicy(t *testing.T) {
	newModel := func(mode string) *Model {
		return &Model{Tables: map[string]Table{"orders": {
			SchemaMode:     mode,
			AuthoredFields: map[string]ModelFieldDeclaration{"id": {Datatype: DataTypeInteger}},
			Schema:         TableSchema{Columns: []ColumnSchema{{Name: "id", PhysicalType: "INTEGER"}, {Name: "extra", PhysicalType: "VARCHAR"}}},
		}}}
	}
	if err := newModel("strict").ResolveDiscoveredModelFields(); err == nil {
		t.Fatal("strict Model accepted an undeclared output field")
	}
	compatible := newModel("compatible")
	if err := compatible.ResolveDiscoveredModelFields(); err != nil {
		t.Fatal(err)
	}
	if compatible.Tables["orders"].Dimensions["extra"].Datatype != DataTypeString {
		t.Fatal("compatible Model failed to discover an extra output field")
	}
}

func TestModelCheckMayReferenceAFieldDiscoveredAfterAuthoring(t *testing.T) {
	table := Table{AuthoredFields: map[string]ModelFieldDeclaration{}, Checks: []ModelCheck{{ID: "new_field_present", Type: "non_null", Field: "new_field", Severity: "error"}}}
	model := &Model{Tables: map[string]Table{"orders": table}}
	if err := validateModelChecks(model, "orders", table, true); err != nil {
		t.Fatalf("pre-discovery check was rejected: %v", err)
	}
	if err := validateModelChecks(model, "orders", table, false); err == nil {
		t.Fatal("unresolved check passed final validation")
	}
	table.Schema.Columns = []ColumnSchema{{Name: "new_field", PhysicalType: "VARCHAR"}}
	model.Tables["orders"] = table
	if err := model.ResolveDiscoveredModelFields(); err != nil {
		t.Fatal(err)
	}
	if err := validateModelChecks(model, "orders", model.Tables["orders"], false); err != nil {
		t.Fatalf("discovered check was rejected: %v", err)
	}
}
