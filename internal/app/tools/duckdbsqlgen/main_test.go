package main

import "testing"

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
