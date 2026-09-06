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
