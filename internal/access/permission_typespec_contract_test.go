package access

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestPermissionCatalogGeneratedTypeSpecIsExact(t *testing.T) {
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourcePath), "..", ".."))
	contents, err := os.ReadFile(filepath.Join(root, "api", "typespec", "generated", "permissions.tsp"))
	if err != nil {
		t.Fatalf("read generated permission TypeSpec: %v", err)
	}
	source := string(contents)
	actionBody := declarationBody(t, source, "enum PermissionAction")
	var gotActions []string
	for _, line := range strings.Split(actionBody, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		separator := strings.IndexByte(line, ':')
		if separator < 0 {
			t.Fatalf("malformed PermissionAction member %q", line)
		}
		value, err := strconv.Unquote(strings.TrimSuffix(strings.TrimSpace(line[separator+1:]), ";"))
		if err != nil {
			t.Fatalf("decode PermissionAction member %q: %v", line, err)
		}
		gotActions = append(gotActions, value)
	}
	var wantActions []string
	for _, definition := range PermissionCatalog() {
		wantActions = append(wantActions, string(definition.Action))
	}
	if !reflect.DeepEqual(gotActions, wantActions) {
		t.Fatalf("TypeSpec PermissionAction values = %v, want exact catalog order %v", gotActions, wantActions)
	}

	profileBody := declarationBody(t, source, "enum PermissionCatalogProfile")
	if !strings.Contains(profileBody, fmt.Sprintf("%q", PermissionCatalogProfile)) {
		t.Error("generated PermissionCatalogProfile does not represent the current catalog profile")
	}
	scopeBody := declarationBody(t, source, "enum PermissionScope")
	if got := enumMembers(scopeBody); !reflect.DeepEqual(got, []string{"instance", "project", "resource"}) {
		t.Fatalf("generated PermissionScope values = %v", got)
	}
	if got := enumValues(declarationBody(t, source, "enum ResourceKind")); !reflect.DeepEqual(got, generatedPermissionKinds()) {
		t.Fatalf("generated ResourceKind values = %v, want exact catalog kinds %v", got, generatedPermissionKinds())
	}
	for _, declaration := range []string{
		"model PermissionTarget",
		"model PermissionPair",
		"scope: PermissionScope;",
		"instanceId?: string;",
		"projectId?: PermissionResourceId;",
		"resourceKind?: ResourceKind;",
		"resourceId?: PermissionResourceId;",
		"includeFuture?: boolean;",
		"action: PermissionAction;",
		"target: PermissionTarget;",
		"profile: PermissionCatalogProfile;",
	} {
		if !strings.Contains(source, declaration) {
			t.Errorf("generated typed permission contract omits %q", declaration)
		}
	}
	common, err := os.ReadFile(filepath.Join(root, "api", "typespec", "common.tsp"))
	if err != nil {
		t.Fatalf("read common TypeSpec: %v", err)
	}
	if !strings.Contains(string(common), "import \"./generated/permissions.tsp\";") {
		t.Fatal("common TypeSpec does not import generated permission contract")
	}
}

func enumValues(body string) []string {
	var values []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		separator := strings.IndexByte(line, ':')
		if separator < 0 {
			continue
		}
		value, err := strconv.Unquote(strings.TrimSuffix(strings.TrimSpace(line[separator+1:]), ";"))
		if err == nil {
			values = append(values, value)
		}
	}
	return values
}

func enumMembers(body string) []string {
	var members []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ";"))
		if line != "" {
			members = append(members, line)
		}
	}
	return members
}

func generatedPermissionKinds() []string {
	seen := make(map[string]struct{})
	for _, definition := range PermissionCatalog() {
		for _, kind := range append(append([]projectgraph.Kind(nil), definition.ResourceKinds...), definition.CheckKinds...) {
			seen[string(kind)] = struct{}{}
		}
	}
	preferred := []string{"project", "connection", "source", "model", "semantic_model", "pipeline", "dashboard"}
	result := make([]string, 0, len(seen))
	for _, kind := range preferred {
		if _, ok := seen[kind]; ok {
			result = append(result, kind)
			delete(seen, kind)
		}
	}
	var extra []string
	for kind := range seen {
		extra = append(extra, kind)
	}
	sort.Strings(extra)
	return append(result, extra...)
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
	return source[open+1 : open+close]
}
