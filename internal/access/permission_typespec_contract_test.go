package access

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPermissionCatalogActionsAreRepresentedInTypeSpec(t *testing.T) {
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourcePath), "..", ".."))
	contents, err := os.ReadFile(filepath.Join(root, "api", "typespec", "common.tsp"))
	if err != nil {
		t.Fatalf("read common TypeSpec: %v", err)
	}
	source := string(contents)
	actionBody := declarationBody(t, source, "enum PermissionAction")
	for _, definition := range PermissionCatalog() {
		if !strings.Contains(actionBody, fmt.Sprintf(`"%s"`, definition.Action)) {
			t.Errorf("TypeSpec PermissionAction omits Go catalog action %q", definition.Action)
		}
	}

	if !strings.Contains(source, `enum PermissionCatalogProfile`) || !strings.Contains(source, `"leapview.permissions/v1"`) {
		t.Error("TypeSpec PermissionCatalogProfile does not represent the current catalog profile")
	}
	if !strings.Contains(source, `enum PermissionScope`) {
		t.Error("TypeSpec PermissionScope declaration is missing")
	}
	for _, declaration := range []string{
		"model PermissionTarget",
		"model PermissionPair",
		"scope: PermissionScope;",
		"instanceId?: string;",
		"projectId?: ResourceId;",
		"resourceKind?: ResourceKind;",
		"resourceId?: ResourceId;",
		"includeFuture?: boolean;",
		"action: PermissionAction;",
		"target: PermissionTarget;",
		"profile: PermissionCatalogProfile;",
	} {
		if !strings.Contains(source, declaration) {
			t.Errorf("TypeSpec typed permission contract omits %q", declaration)
		}
	}
}

func declarationBody(t *testing.T, source, declaration string) string {
	t.Helper()
	start := strings.Index(source, declaration)
	if start < 0 {
		t.Fatalf("TypeSpec declaration %q is missing", declaration)
	}
	open := strings.Index(source[start:], "{")
	if open < 0 {
		t.Fatalf("TypeSpec declaration %q has no body", declaration)
	}
	open += start
	close := strings.Index(source[open:], "}")
	if close < 0 {
		t.Fatalf("TypeSpec declaration %q has no closing brace", declaration)
	}
	return source[open : open+close]
}
