package main

import (
	"strings"
	"testing"

	"github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/pkg/duckdbsql"
)

func TestPinnedSchemaSnapshotFamilies(t *testing.T) {
	schemas, err := loadSchemaSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"statement", len(schemas.Statements), 4},
		{"relation", len(schemas.Relations), 9},
		{"expression", len(schemas.Expressions), 19},
		{"modifier", len(schemas.Modifiers), 4},
	} {
		if tc.got != tc.want {
			t.Errorf("%s schema count = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
	for family, variants := range map[string]map[string]schemaDescriptor{
		"statement": schemas.Statements, "relation": schemas.Relations,
		"expression": schemas.Expressions, "modifier": schemas.Modifiers,
	} {
		for discriminator, schema := range variants {
			if discriminator != schema.Discriminator {
				t.Errorf("%s discriminator key %q = %q", family, discriminator, schema.Discriminator)
			}
			if len(schema.AllowedFields) == 0 || len(schema.RequiredFields) == 0 {
				t.Errorf("%s %q has incomplete field contract", family, discriminator)
			}
		}
	}
}

func TestSchemaRenderingIsDeterministic(t *testing.T) {
	schemas, err := loadSchemaSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	first, err := renderSchema(schemas)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderSchema(schemas)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("schema rendering is not deterministic")
	}
}

func TestInventoryRenderingPreservesDescriptiveFields(t *testing.T) {
	tags := duckdb.OrderedMap{}
	tags.Set("zeta", "last")
	tags.Set("alpha", "first")
	inventory := duckdbsql.MetadataInventory{
		Functions: []duckdbsql.FunctionMetadata{{
			FunctionName: "documented_fn",
			Description:  "A descriptive function.",
			Comment:      "A runtime comment.",
			Tags:         stringMap(tags),
			Examples:     []string{"SELECT documented_fn(1)"},
		}},
		Types: []duckdbsql.TypeMetadata{{
			TypeName: "documented_type",
			TypeSize: 16,
			Comment:  "A runtime type comment.",
			Tags:     map[string]string{"zeta": "last", "alpha": "first"},
		}},
	}
	first, err := render(inventory)
	if err != nil {
		t.Fatal(err)
	}
	second, err := render(inventory)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("inventory rendering is not deterministic")
	}
	for _, want := range []string{
		`Description: "A descriptive function."`,
		`Comment: "A runtime comment."`,
		`Tags: map[string]string{"alpha": "first", "zeta": "last"}`,
		`Examples: []string{"SELECT documented_fn(1)"}`,
		`TypeSize: 16`,
		`Comment: "A runtime type comment."`,
	} {
		if !strings.Contains(first, want) {
			t.Errorf("rendered inventory missing %q", want)
		}
	}
}

func TestInventoryValueConversions(t *testing.T) {
	ordered := duckdb.OrderedMap{}
	ordered.Set("name", "value")
	ordered.Set("count", int64(3))
	got := stringMap(ordered)
	if got["name"] != "value" || got["count"] != "3" {
		t.Fatalf("stringMap = %#v", got)
	}
	if got := int64Value(int32(7)); got != 7 {
		t.Fatalf("int64Value = %d, want 7", got)
	}
}

func TestInventoryQueriesInspectCompleteRuntimeSchemas(t *testing.T) {
	for table, query := range map[string]string{
		"duckdb_functions": functionsSQL,
		"duckdb_keywords":  keywordsSQL,
		"duckdb_types":     typesSQL,
	} {
		want := "SELECT * FROM " + table + "()"
		if strings.TrimSpace(query) != want {
			t.Fatalf("%s query = %q, want %q", table, query, want)
		}
	}
}
