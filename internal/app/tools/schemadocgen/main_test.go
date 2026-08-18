package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateWritesACompleteSchemaReference(t *testing.T) {
	root := t.TempDir()
	schemaDir := filepath.Join(root, "schemas")
	exampleDir := filepath.Join(root, "examples")
	outDir := filepath.Join(root, "docs")
	if err := os.MkdirAll(schemaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(exampleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(schemaDir, "project.schema.json"), []byte(`{
  "type": "object",
  "properties": {
    "apiVersion": {"const": "leapview.dev/v1"},
    "kind": {"const": "Project"},
    "metadata": {"$ref": "#/$defs/Metadata"}
  },
  "required": ["apiVersion", "kind", "metadata"],
  "$defs": {
    "Metadata": {"type": "object", "properties": {"name": {"type": "string"}}, "required": ["name"]}
  }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(exampleDir, "leapview.yaml"), []byte("apiVersion: leapview.dev/v1\nkind: Project\nmetadata:\n  name: demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := generate(schemaDir, exampleDir, outDir); err != nil {
		t.Fatalf("generate schema reference: %v", err)
	}

	article, err := os.ReadFile(filepath.Join(outDir, "project.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Project configuration", "## Example", "kind: Project", "## Fields", "`metadata`", "## Nested definitions", "### Metadata", "`name`"} {
		if !strings.Contains(string(article), want) {
			t.Errorf("generated article missing %q:\n%s", want, article)
		}
	}
	catalog, err := os.ReadFile(filepath.Join(outDir, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(catalog), `"slug": "project"`) {
		t.Errorf("generated catalog missing project: %s", catalog)
	}
	if _, err := os.Stat(filepath.Join(outDir, "schemas", "project.schema.json")); err != nil {
		t.Errorf("generated schema download missing: %v", err)
	}
}

func TestExampleFromSchemaBuildsRequiredNestedValues(t *testing.T) {
	t.Parallel()

	root := schema{
		"type": "object",
		"properties": map[string]any{
			"apiVersion": map[string]any{"const": "leapview.dev/v1"},
			"kind":       map[string]any{"const": "Grant"},
			"metadata":   map[string]any{"$ref": "#/$defs/%23Metadata"},
			"spec": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"privilege": map[string]any{"enum": []any{"VIEW_ITEM", "EDIT_ITEM"}},
				},
				"required": []any{"privilege"},
			},
		},
		"required": []any{"apiVersion", "kind", "metadata", "spec"},
		"$defs": map[string]any{
			"#Metadata": map[string]any{
				"type":       "object",
				"properties": map[string]any{"name": map[string]any{"type": "string"}},
				"required":   []any{"name"},
			},
		},
	}

	example, err := exampleFromSchema(root)
	if err != nil {
		t.Fatal(err)
	}
	want := `apiVersion: leapview.dev/v1
kind: Grant
metadata:
  name: example
spec:
  privilege: VIEW_ITEM
`
	if example != want {
		t.Errorf("example mismatch:\nwant:\n%s\ngot:\n%s", want, example)
	}
}

func TestGenerateAcceptsAPIGenSealedContractRoot(t *testing.T) {
	root := t.TempDir()
	schemaDir := filepath.Join(root, "schemas")
	exampleDir := filepath.Join(root, "examples")
	outDir := filepath.Join(root, "docs")
	if err := os.MkdirAll(schemaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(exampleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{
  "$defs": {
    "DashboardDocument": {
      "properties": {"kind": {"enum": ["Dashboard"]}, "spec": {"type": "object", "properties": {"semanticModel": {"type": "string"}}, "required": ["semanticModel"]}},
      "required": ["kind", "spec"],
      "type": "object"
    }
  },
  "anyOf": [{"$ref": "#/$defs/DashboardDocument"}],
  "x-apigen-contracts": [{"kind": "dashboard-document", "schema": {"$ref": "#/$defs/DashboardDocument"}}]
}`
	if err := os.WriteFile(filepath.Join(schemaDir, "dashboard-document.schema.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(exampleDir, "dashboard.yaml"), []byte("kind: Dashboard\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := generate(schemaDir, exampleDir, outDir); err != nil {
		t.Fatalf("generate APIGen schema reference: %v", err)
	}
	article, err := os.ReadFile(filepath.Join(outDir, "dashboard-document.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(article), "# Dashboard configuration") || !strings.Contains(string(article), "kind: Dashboard") {
		t.Fatalf("APIGen schema article missing dashboard root:\n%s", article)
	}
}
