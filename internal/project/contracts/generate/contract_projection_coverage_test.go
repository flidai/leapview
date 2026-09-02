package main

import (
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
