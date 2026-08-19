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
	if len(inventory.Functions) == 0 || len(inventory.Keywords) == 0 || len(inventory.Types) == 0 {
		t.Fatalf("generated inventory is empty: functions=%d keywords=%d types=%d", len(inventory.Functions), len(inventory.Keywords), len(inventory.Types))
	}
}

func TestValidateSourceCheckoutRejectsUnpinnedSource(t *testing.T) {
	if err := ValidateSourceCheckout("."); err == nil || !strings.Contains(err.Error(), "commit mismatch") {
		t.Fatalf("ValidateSourceCheckout(.) error = %v", err)
	}
}
