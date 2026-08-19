package compiler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExportDashboardSourceModesPreserveCanonicalSemantics(t *testing.T) {
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
		"dashboards/fragments/visuals.yaml": `visuals:
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
		"dashboards/sales.yaml": `apiVersion: leapview.dev/v1
kind: Dashboard
metadata: {id: dashboard:sales, name: sales_dashboard}
spec:
  semanticModel: sales
  filters: []
  includes:
    visuals: [fragments/visuals.yaml]
    pages: [fragments/pages.yaml]
  visuals: {}
  pages: []
`,
	}
	projectYAML := strings.Replace(flatProjectFixtureYAML(), "dashboards: {include: []}", "dashboards: {include: [dashboards/*.yaml]}", 1)
	projectPath := writeFlatProjectFixtureWithProject(t, projectYAML, files)
	root := filepath.Dir(projectPath)

	expanded, err := ExportDashboardSource(filepath.Join(root, "dashboards/sales.yaml"), root, DashboardExportExpanded)
	if err != nil {
		t.Fatalf("expanded export: %v", err)
	}
	fragmented, err := ExportDashboardSource(filepath.Join(root, "dashboards/sales.yaml"), root, DashboardExportFragmented)
	if err != nil {
		t.Fatalf("fragmented export: %v", err)
	}
	if expanded.MainPath != "dashboards/sales.yaml" || fragmented.MainPath != expanded.MainPath {
		t.Fatalf("main paths = %q / %q", expanded.MainPath, fragmented.MainPath)
	}
	if len(expanded.Files) != 1 || len(fragmented.Files) != 3 {
		t.Fatalf("export files = %d / %d, want 1 / 3", len(expanded.Files), len(fragmented.Files))
	}
	if !reflect.DeepEqual(expanded.Document, fragmented.Document) {
		t.Fatalf("expanded and fragmented semantic DTOs differ")
	}
	var visualSource string
	for _, file := range fragmented.Files {
		if file.Path == "dashboards/fragments/visuals.yaml" {
			visualSource = string(file.Content)
		}
	}
	if !strings.Contains(visualSource, "order_count") {
		t.Fatalf("fragmented export did not preserve source bytes: %s", visualSource)
	}
	badFragment := filepath.Join(root, "dashboards/fragments/visuals.yaml")
	if err := os.WriteFile(badFragment, []byte("visuals:\n  order_count:\n    type: unsupported\n    query: {type: aggregate, dimensions: [], metrics: [order_count]}\n    presentation: {type: kpi}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportDashboardSource(filepath.Join(root, "dashboards/sales.yaml"), root, DashboardExportFragmented); err == nil || !strings.Contains(err.Error(), "validate canonical dashboard") {
		t.Fatalf("fragmented export accepted invalid expanded document: %v", err)
	}
	if _, err := LoadProject(projectPath); err == nil || !strings.Contains(err.Error(), "dashboards/fragments/visuals.yaml") || !strings.Contains(err.Error(), "validate canonical dashboard") {
		t.Fatalf("project compilation accepted invalid expanded fragment: %v", err)
	}
	if err := os.WriteFile(badFragment, []byte(visualSource), 0o644); err != nil {
		t.Fatal(err)
	}

	// Recompile the original fragmented checkout and two exported checkouts.
	// The artifact digest includes graph, manifest, normalized Dashboard DTO,
	// and relative source provenance, so equality is the semantic equivalence
	// contract rather than a layout/text comparison.
	originalArtifact, err := CompileProject(projectPath)
	if err != nil {
		t.Fatalf("compile original fragmented project: %v", err)
	}
	expandedRoot := copyProjectWithoutDashboard(t, root, files)
	writeExportFiles(t, expandedRoot, expanded.Files)
	expandedArtifact, err := CompileProject(filepath.Join(expandedRoot, "leapview.yaml"))
	if err != nil {
		t.Fatalf("compile expanded export: %v", err)
	}
	fragmentedRoot := copyProjectWithoutDashboard(t, root, files)
	writeExportFiles(t, fragmentedRoot, fragmented.Files)
	fragmentedArtifact, err := CompileProject(filepath.Join(fragmentedRoot, "leapview.yaml"))
	if err != nil {
		t.Fatalf("compile fragmented export: %v", err)
	}
	if originalArtifact.Digest() != expandedArtifact.Digest() || originalArtifact.Digest() != fragmentedArtifact.Digest() {
		t.Fatalf("artifact digests differ: original=%s expanded=%s fragmented=%s", originalArtifact.Digest(), expandedArtifact.Digest(), fragmentedArtifact.Digest())
	}
	if got, want := expandedArtifact.DashboardDefinitions(), fragmentedArtifact.DashboardDefinitions(); !reflect.DeepEqual(got, want) {
		left, _ := json.Marshal(got)
		right, _ := json.Marshal(want)
		t.Fatalf("compiled dashboard definitions differ:\n%s\n%s", left, right)
	}
}

func copyProjectWithoutDashboard(t *testing.T, root string, files map[string]string) string {
	t.Helper()
	target := t.TempDir()
	for name, body := range files {
		if name == "dashboards/sales.yaml" || strings.HasPrefix(name, "dashboards/fragments/") {
			continue
		}
		path := filepath.Join(target, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return target
}

func writeExportFiles(t *testing.T, root string, files []DashboardSourceFile) {
	t.Helper()
	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, file.Content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
