package compiler

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	projectartifact "github.com/flidai/leapview/internal/project/artifact"
)

func TestCompileProjectFilesParityWithFilesystemAndNestedFragments(t *testing.T) {
	files := map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {type: managed}
`,
		"sources/orders.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:orders, name: orders}
spec: {connection: warehouse, location: {type: path, path: orders.csv, format: csv}}
`,
		"models/orders.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders_model}
spec: {definition: {type: direct, source: orders}, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}
`,
		"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:sales, name: sales}
spec: {datasets: {orders: {model: orders_model}}, metrics: {order_count: {type: aggregate, dataset: orders, aggregation: count, input: {field: orders.id}, empty: zero}}}
`,
		"pipelines/sales-refresh.yaml": `apiVersion: leapview.dev/v1
kind: Pipeline
metadata: {id: pipeline:sales-refresh, name: sales-refresh}
spec: {selection: {semanticModel: sales}}
`,
		"dashboards/shared.yaml": `visuals:
  order_count:
    type: kpi
    query: {type: aggregate, dimensions: [], metrics: [order_count]}
    presentation: {type: kpi}
`,
		"dashboards/fragments/pages.yaml": `pages:
  - id: overview
    title: Overview
    components: []
`,
		"dashboards/nested/sales.yaml": `apiVersion: leapview.dev/v1
kind: Dashboard
metadata: {id: dashboard:sales, name: sales_dashboard}
spec:
  semanticModel: sales
  filters: []
  includes:
    visuals: [../shared.yaml]
    pages: [../fragments/pages.yaml]
  visuals: {}
  pages: []
`,
	}
	projectYAML := strings.Replace(flatProjectFixtureYAML(), "pipelines: {include: []}", "pipelines: {include: [pipelines/*.yaml]}", 1)
	projectYAML = strings.Replace(projectYAML, "dashboards: {include: []}", "dashboards: {include: [dashboards/nested/*.yaml]}", 1)
	files["leapview.yaml"] = projectYAML
	projectPath := writeFlatProjectFixtureWithProject(t, projectYAML, files)
	filesystemArtifact, err := CompileProject(projectPath)
	if err != nil {
		t.Fatalf("CompileProject: %v", err)
	}
	t.Chdir(filepath.Dir(projectPath))
	relativeArtifact, err := CompileProject("leapview.yaml")
	if err != nil {
		t.Fatalf("CompileProject(relative): %v", err)
	}
	if relativeArtifact.Digest() != filesystemArtifact.Digest() {
		for i := 0; i < len(relativeArtifact.Canonical()) && i < len(filesystemArtifact.Canonical()); i++ {
			if relativeArtifact.Canonical()[i] != filesystemArtifact.Canonical()[i] {
				t.Logf("first artifact difference at %d: relative=%q absolute=%q", i, relativeArtifact.Canonical()[i:i+80], filesystemArtifact.Canonical()[i:i+80])
				break
			}
		}
		t.Fatalf("relative filesystem artifact digest = %s, want %s", relativeArtifact.Digest(), filesystemArtifact.Digest())
	}
	logical := make(map[string][]byte, len(files)+1)
	if err := filepath.WalkDir(filepath.Dir(projectPath), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(filepath.Dir(projectPath), path)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		logical[filepath.ToSlash(relative)] = body
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	inMemoryArtifact, err := CompileProjectFiles(logical, "leapview.yaml")
	if err != nil {
		t.Fatalf("CompileProjectFiles: %v", err)
	}
	if filesystemArtifact.Digest() != inMemoryArtifact.Digest() || !bytes.Equal(filesystemArtifact.Canonical(), inMemoryArtifact.Canonical()) {
		t.Fatalf("filesystem/in-memory artifacts differ: %s / %s", filesystemArtifact.Digest(), inMemoryArtifact.Digest())
	}
	decoded, err := projectartifact.Decode(inMemoryArtifact.Canonical())
	if err != nil {
		t.Fatalf("artifact.Decode: %v", err)
	}
	if decoded.Digest() != inMemoryArtifact.Digest() || decoded.Version() != projectartifact.Version || decoded.ProjectID() != inMemoryArtifact.ProjectID() {
		t.Fatalf("decoded artifact identity/version = %s/%d, want %s/%d", decoded.Digest(), decoded.Version(), inMemoryArtifact.Digest(), projectartifact.Version)
	}
	if strings.Contains(string(inMemoryArtifact.Canonical()), filepath.Dir(projectPath)) {
		t.Fatalf("in-memory artifact contains host project path")
	}
	filesystemPlan, err := PlanProjectAgainstArtifact(projectPath, filesystemArtifact)
	if err != nil {
		t.Fatalf("PlanProjectAgainstArtifact: %v", err)
	}
	inMemoryPlan, err := PlanProjectFilesAgainstArtifact(logical, "leapview.yaml", filesystemArtifact)
	if err != nil {
		t.Fatalf("PlanProjectFilesAgainstArtifact: %v", err)
	}
	if !reflect.DeepEqual(filesystemPlan, inMemoryPlan) {
		t.Fatalf("filesystem/in-memory plans differ: %#v / %#v", filesystemPlan, inMemoryPlan)
	}
}

func TestLoadProjectFilesRejectsUnsafeAndMissingIncludes(t *testing.T) {
	base := flatProjectFixtureYAML()
	base = strings.Replace(base, "connections: {include: [connections/*.yaml]}", "connections: {include: [../outside/*.yaml]}", 1)
	if _, err := LoadProjectFiles(map[string][]byte{"leapview.yaml": []byte(base)}, "leapview.yaml"); err == nil || !strings.Contains(err.Error(), "escapes project boundary") {
		t.Fatalf("unsafe include error = %v", err)
	}
	base = flatProjectFixtureYAML()
	if _, err := LoadProjectFiles(map[string][]byte{"leapview.yaml": []byte(base)}, "leapview.yaml"); err == nil || !strings.Contains(err.Error(), "matched no files") {
		t.Fatalf("missing include error = %v", err)
	}
	if _, err := LoadProjectFiles(map[string][]byte{"leapview.yaml": []byte(base)}, " ./leapview.yaml"); err == nil {
		t.Fatal("noncanonical project path accepted")
	}
	if _, err := LoadProjectFiles(map[string][]byte{"./leapview.yaml": []byte(base)}, "leapview.yaml"); err == nil {
		t.Fatal("noncanonical source path accepted")
	}
}
