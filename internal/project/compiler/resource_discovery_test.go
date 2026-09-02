package compiler

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDiscoverAuthoredResourcesUsesSixDeterministicDirectories(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"connections/z.yaml":         authoredDiscoveryFixture("Connection", "connection:z", "z"),
		"connections/a.yaml":         authoredDiscoveryFixture("Connection", "connection:a", "a"),
		"sources/orders.yaml":        authoredDiscoveryFixture("Source", "source:orders", "orders"),
		"models/orders.yaml":         authoredDiscoveryFixture("Model", "model:orders", "orders"),
		"semantic-models/sales.yaml": authoredDiscoveryFixture("SemanticModel", "semantic:sales", "sales"),
		"pipelines/refresh.yaml":     authoredDiscoveryFixture("Pipeline", "pipeline:refresh", "refresh"),
		"dashboards/sales.yaml":      authoredDiscoveryFixture("Dashboard", "dashboard:sales", "sales"),
	}
	writeAuthoredDiscoveryFiles(t, root, files)

	discovered, err := discoverAuthoredResources(root)
	if err != nil {
		t.Fatalf("discoverAuthoredResources() error = %v", err)
	}
	if got, want := len(authoredResourceDirectories), 6; got != want {
		t.Fatalf("authored resource directory count = %d, want %d", got, want)
	}
	want := []discoveredAuthoredResource{
		{kind: "Connection", path: filepath.Join(root, "connections", "a.yaml")},
		{kind: "Connection", path: filepath.Join(root, "connections", "z.yaml")},
		{kind: "Dashboard", path: filepath.Join(root, "dashboards", "sales.yaml")},
		{kind: "Model", path: filepath.Join(root, "models", "orders.yaml")},
		{kind: "Pipeline", path: filepath.Join(root, "pipelines", "refresh.yaml")},
		{kind: "SemanticModel", path: filepath.Join(root, "semantic-models", "sales.yaml")},
		{kind: "Source", path: filepath.Join(root, "sources", "orders.yaml")},
	}
	if !reflect.DeepEqual(discovered.resources, want) {
		t.Fatalf("discovered resources = %#v, want %#v", discovered.resources, want)
	}
}

func TestDiscoverAuthoredResourcesRetainsOnlyTransitionalDataPolicy(t *testing.T) {
	root := t.TempDir()
	writeAuthoredDiscoveryFiles(t, root, map[string]string{
		"access/orders.yaml": authoredDiscoveryFixture("DataPolicy", "policy:orders", "orders"),
	})

	discovered, err := discoverAuthoredResources(root)
	if err != nil {
		t.Fatalf("discoverAuthoredResources() error = %v", err)
	}
	want := []discoveredAuthoredResource{{kind: "DataPolicy", path: filepath.Join(root, "access", "orders.yaml")}}
	if !reflect.DeepEqual(discovered.transitionalDataPolicies, want) {
		t.Fatalf("transitional data policies = %#v, want %#v", discovered.transitionalDataPolicies, want)
	}
}

func TestDiscoverAuthoredResourcesRejectsRemovedAuthoringSurfaces(t *testing.T) {
	tests := map[string]struct {
		files map[string]string
		want  string
	}{
		"project manifest": {
			files: map[string]string{"leapview.yaml": authoredDiscoveryFixture("Project", "project:test", "test")},
			want:  "Project authoring was removed; pass the source root directory",
		},
		"group": {
			files: map[string]string{"access/group.yaml": authoredDiscoveryFixture("Group", "group:test", "test")},
			want:  `authored kind "Group" was removed from analytics source`,
		},
		"role binding": {
			files: map[string]string{"access/binding.yaml": authoredDiscoveryFixture("RoleBinding", "binding:test", "test")},
			want:  `authored kind "RoleBinding" was removed from analytics source`,
		},
		"grant": {
			files: map[string]string{"access/grant.yaml": authoredDiscoveryFixture("Grant", "grant:test", "test")},
			want:  `authored kind "Grant" was removed from analytics source`,
		},
		"publication": {
			files: map[string]string{"publications/dashboard.yaml": authoredDiscoveryFixture("DashboardPublication", "publication:test", "test")},
			want:  `authored kind "DashboardPublication" was removed from analytics source`,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeAuthoredDiscoveryFiles(t, root, test.files)
			_, err := discoverAuthoredResources(root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("discoverAuthoredResources() error = %v, want %q", err, test.want)
			}
		})
	}
}

func authoredDiscoveryFixture(kind, id, name string) string {
	return "apiVersion: leapview.dev/v1\nkind: " + kind + "\nmetadata: {id: " + id + ", name: " + name + "}\nspec: {}\n"
}

func writeAuthoredDiscoveryFiles(t *testing.T, root string, files map[string]string) {
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
