package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContractProjectionCoverageMatchesGeneratedAuthority(t *testing.T) {
	doc, err := loadDocument("../../../../api/gen/data-resources-ir.json")
	if err != nil {
		t.Fatal(err)
	}
	const manifest = "../../contractprojection/exclusions.json"
	if err := verifyContractProjectionCoverage(doc, manifest); err != nil {
		t.Fatal(err)
	}

	sourceSpec := doc.Schemas["SourceSpec"]
	sourceSpec.Properties["unreviewedRuntimeField"] = property{Schema: schemaRef{Type: "string"}}
	doc.Schemas["SourceSpec"] = sourceSpec
	err = verifyContractProjectionCoverage(doc, manifest)
	if err == nil || !strings.Contains(err.Error(), "spec.unreviewedRuntimeField") {
		t.Fatalf("unclassified generated field error = %v", err)
	}
}

func TestContractProjectionCoverageRejectsProjectionOnlyField(t *testing.T) {
	doc, err := loadDocument("../../../../api/gen/data-resources-ir.json")
	if err != nil {
		t.Fatal(err)
	}
	projection := doc.Schemas["SourceContractProjection"]
	projection.Properties["unreviewedProjectionField"] = property{Schema: schemaRef{Type: "string"}}
	doc.Schemas["SourceContractProjection"] = projection
	err = verifyContractProjectionCoverage(doc, "../../contractprojection/exclusions.json")
	if err == nil || !strings.Contains(err.Error(), "projection fields are not authored or explicitly reviewed") || !strings.Contains(err.Error(), "unreviewedProjectionField") {
		t.Fatalf("projection-only field error = %v", err)
	}
}

func TestContractProjectionCoverageRejectsSourceShapeDrift(t *testing.T) {
	doc, err := loadDocument("../../../../api/gen/data-resources-ir.json")
	if err != nil {
		t.Fatal(err)
	}
	schemaField := doc.Schemas["SourceSchemaField"]
	description := schemaField.Properties["description"]
	description.Schema.Type = "integer"
	schemaField.Properties["description"] = description
	doc.Schemas["SourceSchemaField"] = schemaField
	err = verifyContractProjectionCoverage(doc, "../../contractprojection/exclusions.json")
	if err == nil || !strings.Contains(err.Error(), "Source exclusion group") || !strings.Contains(err.Error(), "source shape changed") {
		t.Fatalf("source shape drift error = %v", err)
	}
}

func TestContractProjectionCoverageRejectsParentExclusion(t *testing.T) {
	doc, err := loadDocument("../../../../api/gen/data-resources-ir.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../contractprojection/exclusions.json")
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), `"metadata.provenance.origin"`, `"metadata.provenance"`, 1))
	manifestPath := filepath.Join(t.TempDir(), "exclusions.json")
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	err = verifyContractProjectionCoverage(doc, manifestPath)
	if err == nil || !strings.Contains(err.Error(), "exclusion paths do not match generated authoring fields") || !strings.Contains(err.Error(), "metadata.provenance") {
		t.Fatalf("parent exclusion error = %v", err)
	}
}

func TestContractProjectionCoverageRejectsRenamedAndRemovedFields(t *testing.T) {
	manifest := "../../contractprojection/exclusions.json"
	tests := []struct {
		name string
		edit func(document)
		want string
	}{
		{
			name: "renamed field",
			edit: func(doc document) {
				source := doc.Schemas["SourceSpec"]
				field := source.Properties["connection"]
				delete(source.Properties, "connection")
				source.Properties["endpoint"] = field
				doc.Schemas["SourceSpec"] = source
			},
			want: "spec.connection",
		},
		{
			name: "removed field",
			edit: func(doc document) {
				source := doc.Schemas["SourceSpec"]
				delete(source.Properties, "connection")
				doc.Schemas["SourceSpec"] = source
			},
			want: "spec.connection",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			doc, err := loadDocument("../../../../api/gen/data-resources-ir.json")
			if err != nil {
				t.Fatal(err)
			}
			test.edit(doc)
			err = verifyContractProjectionCoverage(doc, manifest)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("field drift error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestContractProjectionCoverageRejectsBothProjectedAndExcluded(t *testing.T) {
	doc, err := loadDocument("../../../../api/gen/data-resources-ir.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../contractprojection/exclusions.json")
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), `"spec.schema.fields.*.description"`, `"spec.schema.fields.*.datatype"`, 1))
	manifestPath := filepath.Join(t.TempDir(), "exclusions.json")
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	err = verifyContractProjectionCoverage(doc, manifestPath)
	if err == nil || !strings.Contains(err.Error(), "both projected and excluded") || !strings.Contains(err.Error(), "spec.schema.fields.*.datatype") {
		t.Fatalf("overlap error = %v", err)
	}
}

func TestContractProjectionCoverageRejectsUnknownManifestSchema(t *testing.T) {
	doc, err := loadDocument("../../../../api/gen/data-resources-ir.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../contractprojection/exclusions.json")
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object["unreviewed"] = true
	raw, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(t.TempDir(), "exclusions.json")
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	err = verifyContractProjectionCoverage(doc, manifestPath)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown manifest schema error = %v", err)
	}
}
