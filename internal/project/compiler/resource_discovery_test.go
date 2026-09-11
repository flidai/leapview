package compiler

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDiscoverAuthoredResourcesRecursesDeterministically(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"connections/z.yaml":           discoveryFixture("Connection", "connection:z", "z"),
		"connections/a.yaml":           discoveryFixture("Connection", "connection:a", "a"),
		"models/nested/orders.yml":     discoveryFixture("Model", "model:orders", "orders"),
		"semantic-models/sales.yaml":   discoveryFixture("SemanticModel", "semantic:sales", "sales"),
		"pipelines/refresh.yaml":       discoveryFixture("Pipeline", "pipeline:refresh", "refresh"),
		"dashboards/nested/sales.yaml": discoveryFixture("Dashboard", "dashboard:sales", "sales"),
		"sources/orders.yaml":          discoveryFixture("Source", "source:orders", "orders"),
		"models/ignored.txt":           "not a resource",
		"unrelated/dbt.yml":            "name: warehouse\nmodels: {}\n",
	}
	writeDiscoveryFiles(t, root, files)

	discovered, err := discoverAuthoredResources(root)
	if err != nil {
		t.Fatalf("discoverAuthoredResources() error = %v", err)
	}
	want := []discoveredAuthoredResource{
		{kind: "Connection", path: filepath.Join(root, "connections", "a.yaml")},
		{kind: "Connection", path: filepath.Join(root, "connections", "z.yaml")},
		{kind: "Dashboard", path: filepath.Join(root, "dashboards", "nested", "sales.yaml")},
		{kind: "Model", path: filepath.Join(root, "models", "nested", "orders.yml")},
		{kind: "Pipeline", path: filepath.Join(root, "pipelines", "refresh.yaml")},
		{kind: "SemanticModel", path: filepath.Join(root, "semantic-models", "sales.yaml")},
		{kind: "Source", path: filepath.Join(root, "sources", "orders.yaml")},
	}
	if !reflect.DeepEqual(discovered.resources, want) {
		t.Fatalf("discovered resources = %#v, want %#v", discovered.resources, want)
	}
}

func TestDiscoverAuthoredResourcesRejectsLegacyAndRemovedEnvelopesAnywhere(t *testing.T) {
	tests := map[string]struct {
		path string
		kind string
		want string
	}{
		"root project":   {path: "project.yaml", kind: "Project", want: "Project authoring was removed"},
		"nested project": {path: "legacy/nested.yml", kind: "Project", want: "Project authoring was removed"},
		"group":          {path: "access/group.yaml", kind: "Group", want: "was removed from analytics source"},
		"role binding":   {path: "access/role-binding.yaml", kind: "RoleBinding", want: "was removed from analytics source"},
		"grant":          {path: "access/grant.yaml", kind: "Grant", want: "was removed from analytics source"},
		"publication":    {path: "publications/dashboard.yaml", kind: "DashboardPublication", want: "was removed from analytics source"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeDiscoveryFiles(t, root, map[string]string{test.path: discoveryFixture(test.kind, "resource:test", "test")})
			_, err := discoverAuthoredResources(root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("discoverAuthoredResources() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDiscoverAuthoredResourcesRejectsWrongDirectoryKind(t *testing.T) {
	root := t.TempDir()
	writeDiscoveryFiles(t, root, map[string]string{
		"models/not-model.yaml": discoveryFixture("Source", "source:orders", "orders"),
	})
	_, err := discoverAuthoredResources(root)
	if err == nil || !strings.Contains(err.Error(), `authored kind "Source" belongs in sources/, not models/`) {
		t.Fatalf("discoverAuthoredResources() error = %v, want wrong-directory diagnostic", err)
	}
}

func TestDiscoverAuthoredResourcesRejectsWrongAPIVersionButAllowsFragments(t *testing.T) {
	for _, content := range []string{
		"apiVersion: example.dev/v9\nkind: Connection\nmetadata: {name: warehouse}\nspec: {}\n",
		"apiVersion: example.dev/v9\nkind: NotAResource\nspec: {}\n",
		"apiVersion: example.dev/v9\n",
	} {
		root := t.TempDir()
		writeDiscoveryFiles(t, root, map[string]string{"connections/source.yaml": content})
		_, err := discoverAuthoredResources(root)
		if err == nil || !strings.Contains(err.Error(), "apiVersion") {
			t.Fatalf("discoverAuthoredResources() error = %v, want wrong apiVersion diagnostic", err)
		}
	}

	root := t.TempDir()
	writeDiscoveryFiles(t, root, map[string]string{
		"dashboards/fragments/visuals.yaml": "visuals:\n  revenue: {}\n",
		"dashboards/fragments/pages.yaml":   "pages: []\n",
	})
	discovered, err := discoverAuthoredResources(root)
	if err != nil {
		t.Fatalf("discoverAuthoredResources() rejected dashboard fragments: %v", err)
	}
	if len(discovered.resources) != 0 {
		t.Fatalf("discovered dashboard fragments as resources: %#v", discovered.resources)
	}
}

func TestDiscoverAuthoredResourcesRejectsEscapingSymlink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "connections"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "warehouse.yaml")
	if err := os.WriteFile(target, []byte(discoveryFixture("Connection", "connection:warehouse", "warehouse")), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "connections", "warehouse.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := discoverAuthoredResources(root)
	if err == nil || !strings.Contains(err.Error(), "resolves outside source root") {
		t.Fatalf("discoverAuthoredResources() error = %v, want source-root boundary diagnostic", err)
	}
	if strings.Contains(err.Error(), outside) {
		t.Fatalf("boundary diagnostic leaked outside path: %v", err)
	}
}

func TestDiscoverAuthoredResourcesRejectsInternalFileSymlink(t *testing.T) {
	root := t.TempDir()
	writeDiscoveryFiles(t, root, map[string]string{
		"shared/warehouse.yaml": discoveryFixture("Connection", "connection:warehouse", "warehouse"),
	})
	if err := os.MkdirAll(filepath.Join(root, "connections"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "shared", "warehouse.yaml"), filepath.Join(root, "connections", "warehouse.yaml")); err != nil {
		t.Fatal(err)
	}
	_, err := discoverAuthoredResources(root)
	if err == nil || !strings.Contains(err.Error(), "symlink") || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("discoverAuthoredResources() error = %v, want explicit file-symlink rejection", err)
	}
}

func TestCleanSourceRelativePathRejectsUnsafeInput(t *testing.T) {
	outside := t.TempDir()
	for _, path := range []string{
		filepath.Join("..", filepath.Base(outside)),
		outside,
	} {
		t.Run(path, func(t *testing.T) {
			_, err := cleanSourceRelativePath(path)
			if err == nil || !strings.Contains(err.Error(), "escapes source root") {
				t.Fatalf("cleanSourceRelativePath(%q) error = %v, want source-root containment diagnostic", path, err)
			}
			if strings.Contains(err.Error(), outside) {
				t.Fatalf("containment diagnostic leaked outside path: %v", err)
			}
		})
	}
}

func TestDiscoverySnapshotCannotBeRedirectedAfterValidation(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	original := discoveryFixture("Connection", "connection:warehouse", "warehouse")
	replacement := discoveryFixture("Connection", "connection:outside", "outside")
	writeDiscoveryFiles(t, root, map[string]string{"connections/warehouse.yaml": original})
	outsidePath := filepath.Join(outside, "warehouse.yaml")
	if err := os.WriteFile(outsidePath, []byte(replacement), 0o644); err != nil {
		t.Fatal(err)
	}

	discovered, err := discoverAuthoredResources(root)
	if err != nil {
		t.Fatal(err)
	}
	resourcePath := filepath.Join(root, "connections", "warehouse.yaml")
	if err := os.Remove(resourcePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, resourcePath); err != nil {
		t.Fatal(err)
	}
	content, err := discovered.reader.ReadFile(resourcePath)
	if err != nil {
		t.Fatalf("read validated snapshot: %v", err)
	}
	if string(content) != original {
		t.Fatalf("snapshot content changed after symlink swap: %q", content)
	}
}

func TestSnapshotReaderAllowsBoundedParentIncludeAndRejectsNonYAMLMatch(t *testing.T) {
	root := t.TempDir()
	reader := &snapshotSourceReader{
		root: root,
		files: map[string][]byte{
			"dashboards/shared.yaml":       []byte("visuals: {}\n"),
			"dashboards/nested/sales.yaml": []byte(discoveryFixture("Dashboard", "dashboard:sales", "sales")),
		},
		paths: map[string]struct{}{
			"dashboards/shared.yaml":       {},
			"dashboards/shared.txt":        {},
			"dashboards/nested/sales.yaml": {},
		},
	}
	matches, err := reader.ResolveIncludePaths(root, filepath.Join(root, "dashboards", "nested"), "../shared.yaml")
	if err != nil {
		t.Fatalf("bounded parent include was rejected: %v", err)
	}
	want := []string{filepath.Join(root, "dashboards", "shared.yaml")}
	if !reflect.DeepEqual(matches, want) {
		t.Fatalf("parent include matches = %v, want %v", matches, want)
	}
	if _, err := reader.ResolveIncludePaths(root, filepath.Join(root, "dashboards"), "shared.*"); err == nil || !strings.Contains(err.Error(), "non-YAML") {
		t.Fatalf("broad non-YAML include error = %v, want non-YAML diagnostic", err)
	}
}

func TestDiscoverAuthoredResourcesRejectsOversizedYAML(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models", "oversized.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxAuthoredSourceFileBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = discoverAuthoredResources(root)
	if err == nil || !strings.Contains(err.Error(), "file limit") {
		t.Fatalf("discoverAuthoredResources() error = %v, want file-size limit", err)
	}
}

func discoveryFixture(kind, id, name string) string {
	return "apiVersion: leapview.dev/v1\nkind: " + kind + "\nmetadata: {id: " + id + ", name: " + name + "}\nspec: {}\n"
}

func writeDiscoveryFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
