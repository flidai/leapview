package compiler

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestLoadSourceRootSynthesizesInternalRoot(t *testing.T) {
	root := t.TempDir()
	writeAuthoredDiscoveryFiles(t, root, map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {type: managed}
`,
	})

	project, err := LoadSourceRoot(root)
	if err != nil {
		t.Fatalf("LoadSourceRoot() error = %v", err)
	}
	if project.ID != syntheticSourceRootID || project.Name != syntheticSourceRootName {
		t.Fatalf("internal root = %q/%q, want %q/%q", project.ID, project.Name, syntheticSourceRootID, syntheticSourceRootName)
	}
	rootResource, ok := project.Graph.Resource(syntheticSourceRootID)
	if !ok || rootResource.Kind != projectgraph.KindProject {
		t.Fatalf("synthesized graph root = %#v, found=%v", rootResource, ok)
	}
	if _, ok := project.Graph.Resource("connection:warehouse"); !ok {
		t.Fatal("compiled graph omitted discovered Connection")
	}
	if project.Manifest.ResourceFiles[syntheticSourceRootID.String()] != "." {
		t.Fatalf("internal root provenance = %q, want source-root marker", project.Manifest.ResourceFiles[syntheticSourceRootID.String()])
	}
}

func TestSourceRootFilesAreStableAndExcludeSyntheticRoot(t *testing.T) {
	root := t.TempDir()
	writeAuthoredDiscoveryFiles(t, root, map[string]string{
		"connections/z.yaml": sourceRootConnectionFixture("connection:z", "z"),
		"connections/a.yaml": sourceRootConnectionFixture("connection:a", "a"),
	})

	files, err := SourceRootFiles(root)
	if err != nil {
		t.Fatalf("SourceRootFiles() error = %v", err)
	}
	want := []string{filepath.Join(root, "connections", "a.yaml"), filepath.Join(root, "connections", "z.yaml")}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("source-root files = %#v, want %#v", files, want)
	}
}

func TestLoadSourceRootPreservesDuplicateIDValidation(t *testing.T) {
	root := t.TempDir()
	writeAuthoredDiscoveryFiles(t, root, map[string]string{
		"connections/a.yaml": sourceRootConnectionFixture("connection:duplicate", "a"),
		"connections/b.yaml": sourceRootConnectionFixture("connection:duplicate", "b"),
	})

	_, err := LoadSourceRoot(root)
	if err == nil || !strings.Contains(err.Error(), `duplicate resource id "connection:duplicate" already used by connection:a`) {
		t.Fatalf("LoadSourceRoot() duplicate error = %v", err)
	}
}

func sourceRootConnectionFixture(id, name string) string {
	return "apiVersion: leapview.dev/v1\nkind: Connection\nmetadata: {id: " + id + ", name: " + name + "}\nspec: {type: managed}\n"
}
