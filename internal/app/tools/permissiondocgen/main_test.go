package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestRenderPermissionsMatchesGolden(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "permissions.golden.md"))
	if err != nil {
		t.Fatalf("read permissions golden: %v", err)
	}
	got := renderPermissions(access.PermissionCatalog(), access.PermissionRolePresets())
	if got != string(want) {
		t.Fatalf("rendered permissions differ from golden\n%s", diffPreview(string(want), got))
	}
}

func TestRenderPermissionsIncludesCanonicalActionsAndRoles(t *testing.T) {
	contents := renderPermissions(access.PermissionCatalog(), access.PermissionRolePresets())
	for _, definition := range access.PermissionCatalog() {
		if !strings.Contains(contents, "`"+string(definition.Action)+"`") {
			t.Errorf("generated permissions omitted action %q", definition.Action)
		}
	}
	for _, preset := range access.PermissionRolePresets() {
		if !strings.Contains(contents, "`"+string(preset.Role)+"`") {
			t.Errorf("generated permissions omitted role %q", preset.Role)
		}
	}
	if !strings.Contains(contents, "Catalog profile: `"+access.PermissionCatalogProfile+"`") {
		t.Fatalf("generated permissions omitted catalog profile")
	}
}

func TestCheckGeneratedDetectsDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "permissions.md")
	if err := generate(path); err != nil {
		t.Fatalf("generate permissions: %v", err)
	}
	if err := checkGenerated(path); err != nil {
		t.Fatalf("check fresh permissions: %v", err)
	}
	if err := os.WriteFile(path, []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("write stale permissions: %v", err)
	}
	if err := checkGenerated(path); err == nil {
		t.Fatal("check accepted stale permissions")
	}
}

func TestRenderPermissionsIsDeterministic(t *testing.T) {
	catalog := access.PermissionCatalog()
	presets := access.PermissionRolePresets()
	if first, second := renderPermissions(catalog, presets), renderPermissions(catalog, presets); first != second {
		t.Fatal("renderPermissions is not deterministic")
	}
}

func diffPreview(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	for index := 0; index < len(wantLines) && index < len(gotLines); index++ {
		if wantLines[index] != gotLines[index] {
			return fmt.Sprintf("first differing line %d:\n- %s\n+ %s", index+1, wantLines[index], gotLines[index])
		}
	}
	return "golden line count differs"
}
