package duckdbsql

import "testing"

func TestGeneratedASTSchemaFamiliesCoverPinnedVariants(t *testing.T) {
	cases := []struct {
		name     string
		variants []string
		schemas  map[string]serializedNodeSchema
		want     int
	}{
		{"statement", generatedStatementVariants, generatedStatementSchemas, 4},
		{"relation", generatedRelationVariants, generatedRelationSchemas, 9},
		{"expression", generatedExpressionVariants, generatedExpressionSchemas, 19},
		{"modifier", generatedModifierVariants, generatedModifierSchemas, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.variants) != tc.want || len(tc.schemas) != tc.want {
				t.Fatalf("got %d variants and %d schemas, want %d", len(tc.variants), len(tc.schemas), tc.want)
			}
			seen := make(map[string]bool, len(tc.variants))
			for _, variant := range tc.variants {
				if seen[variant] {
					t.Fatalf("duplicate variant %q", variant)
				}
				seen[variant] = true
				schema, ok := tc.schemas[variant]
				if !ok {
					t.Fatalf("variant %q has no schema", variant)
				}
				if schema.Discriminator != variant {
					t.Fatalf("schema discriminator = %q, want %q", schema.Discriminator, variant)
				}
				if len(schema.AllowedFields) == 0 || len(schema.RequiredFields) == 0 {
					t.Fatalf("variant %q has incomplete field contract", variant)
				}
			}
		})
	}
}

func TestGeneratedSupportingSchemasCoverChildBearingClasses(t *testing.T) {
	for _, class := range []string{"CommonTableExpressionInfo", "CommonTableExpressionMap", "OrderByNode", "CaseCheck", "SampleOptions", "PivotColumn", "PivotColumnEntry", "AtClause", "SelectStatement"} {
		schema, ok := generatedSupportingSchemas[class]
		if !ok {
			t.Fatalf("supporting class %q missing", class)
		}
		if len(schema.AllowedFields) == 0 {
			t.Fatalf("supporting class %q has no fields", class)
		}
	}
}
