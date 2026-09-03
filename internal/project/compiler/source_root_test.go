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

func TestCompileSourceRootPublicAPIsCompileAllAuthoredKindsWithoutManifest(t *testing.T) {
	root := t.TempDir()
	writeAuthoredDiscoveryFiles(t, root, map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {type: managed}
`,
		"sources/orders.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:orders, name: orders}
spec:
  connection: warehouse
  location: {type: path, path: orders.csv, format: csv}
`,
		"models/orders.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders_model}
spec:
  definition: {type: direct, source: orders}
  entities: {order: {type: primary, fields: [order_id]}}
  grain: {entity: order}
  fields: {order_id: {datatype: String}}
`,
		"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:sales, name: sales}
spec:
  datasets: {orders: {model: orders_model}}
  metrics: {order_count: {type: aggregate, dataset: orders, aggregation: count, input: {field: orders.order_id}, empty: zero}}
`,
		"pipelines/sales.yaml": `apiVersion: leapview.dev/v1
kind: Pipeline
metadata: {id: pipeline:sales, name: sales_refresh}
spec: {selection: {semanticModel: sales}}
`,
		"dashboards/sales.yaml": `apiVersion: leapview.dev/v1
kind: Dashboard
metadata: {id: dashboard:sales, name: sales_dashboard, displayName: Sales Dashboard}
spec:
  semanticModel: sales
  filters: []
  visuals:
    order_count:
      type: kpi
      query: {type: aggregate, dimensions: [], metrics: [order_count]}
      presentation: {type: kpi}
  pages: [{id: overview, title: Overview, components: []}]
`,
	})

	graph, err := CompileSourceRootGraph(root)
	if err != nil {
		t.Fatalf("CompileSourceRootGraph() error = %v", err)
	}
	compiled, err := CompileSourceRoot(root)
	if err != nil {
		t.Fatalf("CompileSourceRoot() error = %v", err)
	}
	if compiled.Graph().Digest() != graph.Digest() {
		t.Fatalf("public compile APIs returned different graph digests: artifact=%q graph=%q", compiled.Graph().Digest(), graph.Digest())
	}

	if graph.ProjectID() != syntheticSourceRootID {
		t.Fatalf("project id = %q, want synthetic internal root %q", graph.ProjectID(), syntheticSourceRootID)
	}
	resources := graph.Resources()
	if len(resources) != 7 {
		t.Fatalf("resource count = %d, want synthetic root plus six authored resources", len(resources))
	}
	wantKinds := map[projectgraph.Kind]bool{
		projectgraph.KindProject: true, projectgraph.KindConnection: true, projectgraph.KindSource: true,
		projectgraph.KindModel: true, projectgraph.KindSemanticModel: true, projectgraph.KindPipeline: true,
		projectgraph.KindDashboard: true,
	}
	seenKinds := map[projectgraph.Kind]bool{}
	for _, resource := range resources {
		if !wantKinds[resource.Kind] {
			t.Fatalf("unexpected compiled resource kind %q for %q", resource.Kind, resource.ID)
		}
		seenKinds[resource.Kind] = true
	}
	for kind := range wantKinds {
		if !seenKinds[kind] {
			t.Fatalf("compiled graph omitted authored resource kind %q", kind)
		}
	}

	manifest := compiled.Manifest()
	if _, ok := manifest.AuthoredResourceSources[syntheticSourceRootID.String()]; ok {
		t.Fatal("synthetic source root was exposed as authored resource source")
	}
	if _, ok := manifest.NameIndex.Dashboards[syntheticSourceRootName]; ok {
		t.Fatal("synthetic source root was exposed through the authored dashboard name index")
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

func TestPublicCompileAndPlanRejectLegacyProjectManifest(t *testing.T) {
	root := t.TempDir()
	manifest := filepath.Join(root, "leapview.yaml")
	writeAuthoredDiscoveryFiles(t, root, map[string]string{
		"leapview.yaml": authoredDiscoveryFixture("Project", "project:test", "test"),
	})
	for name, run := range map[string]func() error{
		"compile": func() error { _, err := CompileProject(manifest); return err },
		"plan":    func() error { _, err := PlanProject(manifest); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); err == nil || !strings.Contains(err.Error(), "Project authoring was removed") {
				t.Fatalf("legacy manifest error = %v", err)
			}
		})
	}
}

func sourceRootConnectionFixture(id, name string) string {
	return "apiVersion: leapview.dev/v1\nkind: Connection\nmetadata: {id: " + id + ", name: " + name + "}\nspec: {type: managed}\n"
}
