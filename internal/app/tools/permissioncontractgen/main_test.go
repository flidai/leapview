package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestLoadContractProjectsEveryCanonicalField(t *testing.T) {
	value, err := loadContract()
	if err != nil {
		t.Fatal(err)
	}
	definitions := access.PermissionCatalog()
	if value.Profile != access.PermissionCatalogProfile {
		t.Fatalf("profile = %q, want %q", value.Profile, access.PermissionCatalogProfile)
	}
	if len(value.Actions) != len(definitions) {
		t.Fatalf("actions = %d, want %d", len(value.Actions), len(definitions))
	}
	for index, definition := range definitions {
		got := value.Actions[index]
		if got.Action != string(definition.Action) || got.Scope != string(definition.Scope) ||
			got.Family != definition.Family || got.Description != definition.Description ||
			got.Delegable != definition.Delegable || got.UISelectable != definition.UISelectable {
			t.Fatalf("action %d projection lost canonical metadata: %#v / %#v", index, got, definition)
		}
		if strings.Join(got.ResourceKinds, "\x00") != strings.Join(stringKinds(definition.ResourceKinds), "\x00") ||
			strings.Join(got.CheckKinds, "\x00") != strings.Join(stringKinds(definition.CheckKinds), "\x00") ||
			strings.Join(got.Prerequisites, "\x00") != strings.Join(stringActions(definition.Prerequisites), "\x00") {
			t.Fatalf("action %q projection lost structural metadata: %#v", definition.Action, got)
		}
	}
	presets := access.PermissionRolePresets()
	if len(value.Roles) != len(presets) {
		t.Fatalf("roles = %d, want %d", len(value.Roles), len(presets))
	}
	for index, preset := range presets {
		got := value.Roles[index]
		if got.Role != string(preset.Role) || got.Profile != preset.Profile || got.Description != preset.Description ||
			strings.Join(got.Actions, "\x00") != strings.Join(stringActions(preset.Actions), "\x00") {
			t.Fatalf("role %q projection lost canonical metadata: %#v", preset.Role, got)
		}
	}
}

func TestRenderContractsIsDeterministicAndStrict(t *testing.T) {
	value, err := loadContract()
	if err != nil {
		t.Fatal(err)
	}
	if first, second := renderTypeSpec(value), renderTypeSpec(value); first != second {
		t.Fatal("TypeSpec rendering is not deterministic")
	}
	if first, second := string(renderJSON(value)), string(renderJSON(value)); first != second {
		t.Fatal("JSON rendering is not deterministic")
	}
	sql := renderSQL(value)
	for _, required := range []string{
		"CREATE OR REPLACE FUNCTION access.valid_permission_pairs",
		"jsonb_object_keys(item)",
		"jsonb_object_keys(target)",
		"jsonb_typeof(item->'action') <> 'string'",
		"jsonb_typeof(target->'includeFuture') <> 'boolean'",
		"^[A-Za-z0-9][A-Za-z0-9_.:-]*$",
		"jsonb_array_elements(seen_items)",
		"dashboard.read",
		"platform.audit.read",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("generated SQL omits strict/catalog guard %q", required)
		}
	}
	typeSpec := renderTypeSpec(value)
	for _, required := range []string{
		"enum ResourceKind",
		"enum PermissionAction",
		"enum PermissionCatalogProfile",
		"model PermissionTarget",
		"model PermissionPair",
		"scalar PermissionResourceId extends string;",
		`@pattern("^[A-Za-z0-9][A-Za-z0-9_.:-]*$")`,
	} {
		if !strings.Contains(typeSpec, required) {
			t.Errorf("generated TypeSpec omits %q", required)
		}
	}
}

func TestCheckGeneratedDetectsAnyContractDrift(t *testing.T) {
	root := t.TempDir()
	paths := outputPaths{
		typeSpec: filepath.Join(root, "api", "permissions.tsp"),
		json:     filepath.Join(root, "access", "permission_catalog.json"),
		sql:      filepath.Join(root, "postgres", "permission_contract.sql"),
	}
	if err := generate(paths); err != nil {
		t.Fatal(err)
	}
	if err := checkGenerated(paths); err != nil {
		t.Fatalf("check fresh contracts: %v", err)
	}
	contents, err := os.ReadFile(paths.sql)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.sql, append(contents, []byte("-- stale\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkGenerated(paths); err == nil {
		t.Fatal("check accepted stale SQL contract")
	}
}
