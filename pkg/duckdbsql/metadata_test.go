package duckdbsql

import (
	"strings"
	"testing"
)

func TestUpstreamSourceIdentityAndInventorySchemas(t *testing.T) {
	identity, err := UpstreamSourceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if identity.DuckDBVersion != DuckDBVersion || identity.DuckDBGitCommit != DuckDBSourceCommit {
		t.Fatalf("identity = %#v", identity)
	}
	if len(identity.DescriptorFileSHA) != 6 {
		t.Fatalf("descriptor count = %d", len(identity.DescriptorFileSHA))
	}
	for _, table := range []string{"duckdb_functions", "duckdb_keywords", "duckdb_types"} {
		columns, err := InventoryColumns(table)
		if err != nil {
			t.Fatal(err)
		}
		if len(columns) == 0 {
			t.Fatalf("empty schema for %s", table)
		}
	}
	if _, err := InventoryColumns("not_a_catalog_table"); err == nil {
		t.Fatal("unknown inventory table accepted")
	}
}

func TestSortInventoryIsCanonical(t *testing.T) {
	inventory := MetadataInventory{
		Functions: []FunctionMetadata{
			{SchemaName: "z", FunctionName: "same", FunctionType: "scalar", ParameterTypes: []string{"VARCHAR"}},
			{SchemaName: "a", FunctionName: "same", FunctionType: "scalar", ParameterTypes: []string{"INTEGER"}},
		},
		Keywords: []KeywordMetadata{{Name: "select", Category: "reserved"}, {Name: "from", Category: "reserved"}},
		Types:    []TypeMetadata{{SchemaName: "z", TypeName: "Z"}, {SchemaName: "a", TypeName: "A"}},
	}
	SortInventory(&inventory)
	if inventory.Functions[0].SchemaName != "a" || inventory.Keywords[0].Name != "from" || inventory.Types[0].TypeName != "A" {
		t.Fatalf("inventory was not sorted: %#v", inventory)
	}
}

func TestCloneMetadataInventoryDeepCopiesDocumentation(t *testing.T) {
	source := MetadataInventory{
		Functions: []FunctionMetadata{{
			Description: "description",
			Comment:     "comment",
			Tags:        map[string]string{"owner": "duckdb"},
			Examples:    []string{"SELECT 1"},
		}},
		Types: []TypeMetadata{{
			TypeSize: 8,
			Comment:  "type comment",
			Tags:     map[string]string{"kind": "numeric"},
		}},
	}
	clone := cloneMetadataInventory(source)
	if clone.Functions[0].Description != source.Functions[0].Description || clone.Types[0].TypeSize != source.Types[0].TypeSize {
		t.Fatalf("documentation fields were not copied: %#v", clone)
	}
	clone.Functions[0].Tags["owner"] = "changed"
	clone.Functions[0].Examples[0] = "SELECT 2"
	clone.Types[0].Tags["kind"] = "changed"
	if source.Functions[0].Tags["owner"] != "duckdb" || source.Functions[0].Examples[0] != "SELECT 1" || source.Types[0].Tags["kind"] != "numeric" {
		t.Fatalf("clone shares mutable documentation data: %#v", source)
	}
}

func TestGeneratedDescriptorManifestMatchesLock(t *testing.T) {
	identity, err := UpstreamSourceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	manifest := GeneratedDescriptorManifestSnapshot()
	if len(manifest) != len(identity.DescriptorFileSHA) {
		t.Fatalf("manifest length = %d", len(manifest))
	}
	for _, descriptor := range manifest {
		name := descriptor.Path[strings.LastIndexByte(descriptor.Path, '/')+1:]
		if identity.DescriptorFileSHA[name] != descriptor.SHA256 {
			t.Fatalf("descriptor %s does not match lock", descriptor.Path)
		}
	}
	inventory := GeneratedInventorySnapshot()
	if len(inventory.Functions) != 2950 || len(inventory.Keywords) != 489 || len(inventory.Types) != 244 {
		t.Fatalf("generated inventory counts changed: functions=%d keywords=%d types=%d", len(inventory.Functions), len(inventory.Keywords), len(inventory.Types))
	}
	functionKinds := make(map[string]bool)
	for _, function := range inventory.Functions {
		functionKinds[function.FunctionType] = true
	}
	for _, kind := range []string{"aggregate", "macro", "pragma", "scalar", "table", "table_macro"} {
		if !functionKinds[kind] {
			t.Errorf("generated inventory is missing %s functions", kind)
		}
	}
}

func TestGeneratedInventoryPreservesRuntimeDocumentationFields(t *testing.T) {
	inventory := GeneratedInventorySnapshot()
	var hasDescription, hasExamples, hasTypeSize bool
	for _, function := range inventory.Functions {
		hasDescription = hasDescription || function.Description != ""
		hasExamples = hasExamples || len(function.Examples) > 0
	}
	for _, typ := range inventory.Types {
		hasTypeSize = hasTypeSize || typ.TypeSize != 0
	}
	if !hasDescription || !hasExamples || !hasTypeSize {
		t.Fatalf("generated documentation fields missing: description=%t examples=%t type_size=%t", hasDescription, hasExamples, hasTypeSize)
	}
}

func TestGeneratedEnumManifestMatchesLock(t *testing.T) {
	identity, err := UpstreamSourceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	manifest := GeneratedEnumManifestSnapshot()
	if len(manifest) != len(identity.EnumFileSHA) {
		t.Fatalf("enum manifest length = %d, lock has %d", len(manifest), len(identity.EnumFileSHA))
	}
	for _, source := range manifest {
		if identity.EnumFileSHA[source.Path] != source.SHA256 {
			t.Fatalf("enum source %s does not match lock", source.Path)
		}
	}
}

func TestValidateSourceCheckoutRejectsUnpinnedSource(t *testing.T) {
	if err := ValidateSourceCheckout("."); err == nil || !strings.Contains(err.Error(), "commit mismatch") {
		t.Fatalf("ValidateSourceCheckout(.) error = %v", err)
	}
}
