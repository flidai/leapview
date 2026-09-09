package project_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	project "github.com/flidai/leapview/internal/project"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	"github.com/flidai/leapview/internal/project/compiler"
	"github.com/flidai/leapview/internal/project/contractprojection"
)

func inventoryBundle(t *testing.T, versioned bool) projectartifact.SourceBundle {
	t.Helper()
	metadata := "{id: 'source:orders', name: orders}"
	if versioned {
		metadata = "{id: 'source:orders', name: orders, contract: {version: 1.0.0, compatibility: backward}}"
	}
	files := map[string]string{
		"connections/warehouse.yaml": "apiVersion: leapview.dev/v1\nkind: Connection\nmetadata: {id: 'connection:warehouse', name: warehouse}\nspec: {type: managed}\n",
		"sources/orders.yaml":        "apiVersion: leapview.dev/v1\nkind: Source\nmetadata: " + metadata + "\nspec: {connection: warehouse, location: {type: path, path: orders.csv, format: csv}}\n",
	}
	return compileInventoryFiles(t, files)
}

func compileInventoryFiles(t *testing.T, files map[string]string) projectartifact.SourceBundle {
	t.Helper()
	root := t.TempDir()
	for name, contents := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := compiler.Compile(root)
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestResourceUIDInventoryModelAndSemanticEvidence(t *testing.T) {
	bundle := compileInventoryFiles(t, map[string]string{
		"connections/warehouse.yaml": "apiVersion: leapview.dev/v1\nkind: Connection\nmetadata: {id: connection:warehouse, name: warehouse}\nspec: {type: managed}\n",
		"sources/orders.yaml": "apiVersion: leapview.dev/v1\nkind: Source\nmetadata: {id: source:orders, name: orders}\nspec: {connection: warehouse, location: {type: path, path: orders.csv, format: csv}}\n",
		"models/orders.yaml": "apiVersion: leapview.dev/v1\nkind: Model\nmetadata: {id: model:orders, name: orders_model, contract: {version: 2.0.0, compatibility: backward}}\nspec: {definition: {type: direct, source: orders}, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}\n",
		"semantic-models/sales.yaml": "apiVersion: leapview.dev/v1\nkind: SemanticModel\nmetadata: {id: semantic:sales, name: sales}\nspec: {datasets: {orders: {model: orders_model}}, metrics: {}}\n",
	})
	inventory, err := project.NewResourceUIDInventory(bundle)
	if err != nil { t.Fatal(err) }
	encoded, _ := inventory.JSON()
	var entries []struct {
		ID string `json:"authored_id"`
		Status string `json:"contract_status"`
		Canonical string `json:"canonical_contract"`
		Digest string `json:"contract_digest"`
	}
	if err := json.Unmarshal(encoded, &entries); err != nil { t.Fatal(err) }
	if len(entries) != 4 { t.Fatalf("inventory: %s", encoded) }
	for _, entry := range entries {
		switch entry.ID {
		case "model:orders":
			digest, err := contractprojection.DigestModelPublication([]byte(entry.Canonical))
			if err != nil || digest != entry.Digest || entry.Status != "canonical" { t.Fatalf("model evidence: %+v, %v", entry, err) }
		case "semantic:sales", "source:orders":
			if entry.Status != "unversioned" || entry.Digest != "" { t.Fatalf("invented version authority: %+v", entry) }
		}
	}
}

func TestResourceUIDInventorySealedCanonicalEvidence(t *testing.T) {
	bundle := inventoryBundle(t, true)
	before := bundle.Canonical()
	inventory, err := project.NewResourceUIDInventory(bundle)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := inventory.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		ID        string `json:"authored_id"`
		Profile   string `json:"contract_profile"`
		Version   string `json:"contract_version"`
		Canonical string `json:"canonical_contract"`
		Digest    string `json:"contract_digest"`
	}
	if err := json.Unmarshal(encoded, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != "connection:warehouse" || entries[1].ID != "source:orders" {
		t.Fatalf("inventory = %s", encoded)
	}
	if entries[0].Profile != "" || entries[0].Digest != "" {
		t.Fatal("invented contract for Connection")
	}
	if entries[1].Profile != contractprojection.Profile {
		t.Fatalf("profile = %q", entries[1].Profile)
	}
	if entries[1].Version != "1.0.0" { t.Fatalf("contract version = %q", entries[1].Version) }
	digest, err := contractprojection.DigestSourcePublication([]byte(entries[1].Canonical))
	if err != nil || digest != entries[1].Digest {
		t.Fatalf("canonical replay: %s, %v", digest, err)
	}
	if inventory.GraphDigest() != bundle.Graph().Digest() || inventory.BundleDigest() != bundle.Digest() {
		t.Fatal("inventory detached from bundle authority")
	}
	if !bytes.Equal(before, bundle.Canonical()) {
		t.Fatal("inventory mutated portable artifact")
	}
	encoded[0] = 'x'
	again, _ := inventory.JSON()
	if again[0] != '[' {
		t.Fatal("inventory leaked mutable bytes")
	}
	other, err := project.NewResourceUIDInventory(inventoryBundle(t, true))
	if err != nil {
		t.Fatal(err)
	}
	otherBytes, _ := other.JSON()
	if !bytes.Equal(again, otherBytes) {
		t.Fatal("source-root path changed registry evidence")
	}
}

func TestResourceUIDInventoryDoesNotInventUnversionedContracts(t *testing.T) {
	inventory, err := project.NewResourceUIDInventory(inventoryBundle(t, false))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := inventory.JSON()
	if strings.Contains(string(encoded), "contract_digest") || strings.Contains(string(encoded), "sha256:") {
		t.Fatalf("invented identity: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"contract_status":"unversioned"`) || !strings.Contains(string(encoded), `"contract_status":"not_contract_bearing"`) {
		t.Fatalf("missing explicit contract status: %s", encoded)
	}
}

func TestResourceUIDInventoryRejectsMissingAndMismatchedAuthority(t *testing.T) {
	if _, err := (project.ResourceUIDInventory{}).JSON(); err == nil {
		t.Fatal("zero inventory accepted")
	}
	if _, err := project.NewResourceUIDInventory(projectartifact.SourceBundle{}); err == nil {
		t.Fatal("zero bundle accepted")
	}
	bundle := inventoryBundle(t, true)
	for _, source := range []string{"", strings.Replace(bundle.Manifest().AuthoredResourceSources["source:orders"], "source:orders", "source:forged", 1)} {
		manifest := bundle.Manifest()
		manifest.AuthoredResourceSources["source:orders"] = source
		changed, err := projectartifact.NewSourceBundle(bundle.Graph(), manifest)
		if err != nil {
			continue
		} // Earlier portable validation is also fail-closed.
		if _, err := project.NewResourceUIDInventory(changed); err == nil {
			t.Fatal("missing/mismatched authored evidence accepted")
		}
	}
}
