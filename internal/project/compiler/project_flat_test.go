package compiler

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardcompiler "github.com/flidai/leapview/internal/dashboard/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/manifest"
	configschema "github.com/flidai/leapview/internal/project/schema"
)

func TestExportDashboardConvertsCanonicalResourceIDs(t *testing.T) {
	document := dashboardauthoring.Dashboard{
		ID: "dashboard_sales", Title: "Sales", SemanticModel: "semantic_sales",
		Visuals: dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{
			"total": {Type: "kpi", Query: dashboardauthoring.VisualQuery{Measures: []dashboardauthoring.FieldRef{{Field: "order_count", Alias: "value"}}}},
		}),
		Pages: []dashboard.Page{{ID: "overview", Title: "Overview"}},
	}
	encoded, err := ExportDashboard(document, dashboardauthoring.DashboardExportMetadata{Name: "sales_dashboard"})
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if !strings.Contains(text, "id: dashboard_sales") || !strings.Contains(text, "name: sales_dashboard") || !strings.Contains(text, "semanticModel: semantic_sales") {
		t.Fatalf("canonical dashboard omitted ResourceID strings: %s", text)
	}
}

func TestResourceResolverRejectsAmbiguousNames(t *testing.T) {
	_, err := newResourceResolver([]projectgraph.Resource{
		{ID: "source:orders", Kind: projectgraph.KindSource, Name: "orders"},
		{ID: "model:orders", Kind: projectgraph.KindModel, Name: "orders"},
	})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("newResourceResolver() error = %v, want ambiguous name", err)
	}
}

func TestProjectGraphRejectsDependencyCycle(t *testing.T) {
	resources := []projectgraph.Resource{
		{ID: "project:test", Kind: projectgraph.KindProject, Name: "test"},
		{ID: "model:a", Kind: projectgraph.KindModel, Name: "a"},
		{ID: "model:b", Kind: projectgraph.KindModel, Name: "b"},
	}
	_, err := projectgraph.NewProjectGraph(resources, []projectgraph.Edge{{From: "model:a", To: "model:b", Relation: "uses_model"}, {From: "model:b", To: "model:a", Relation: "uses_model"}})
	if !errors.Is(err, projectgraph.ErrCycle) {
		t.Fatalf("NewProjectGraph() error = %v, want cycle", err)
	}
}

func TestProjectGraphCanonicalBytesStableAcrossTraversalOrder(t *testing.T) {
	resources := []projectgraph.Resource{{ID: "project:test", Kind: projectgraph.KindProject, Name: "test"}, {ID: "source:z", Kind: projectgraph.KindSource, Name: "z"}, {ID: "connection:a", Kind: projectgraph.KindConnection, Name: "a"}}
	edges := []projectgraph.Edge{{From: "source:z", To: "connection:a", Relation: "uses_connection"}}
	first, err := projectgraph.NewProjectGraph(resources, edges)
	if err != nil {
		t.Fatal(err)
	}
	second, err := projectgraph.NewProjectGraph([]projectgraph.Resource{resources[2], resources[0], resources[1]}, []projectgraph.Edge{edges[0]})
	if err != nil {
		t.Fatal(err)
	}
	if string(first.CanonicalBytes()) != string(second.CanonicalBytes()) || first.Digest() != second.Digest() {
		t.Fatal("graph canonical bytes changed with traversal order")
	}
}

func TestSemanticModelScannerCapturesSourceAndModelDependencies(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales", Connections: map[string]semanticmodel.Connection{"warehouse": {Kind: "managed"}},
		Sources: map[string]semanticmodel.Source{"orders": {Connection: "warehouse", Format: "csv", Path: "orders.csv"}},
		Tables: map[string]semanticmodel.Table{
			"orders": {Source: "orders", PrimaryKey: "id"},
			"daily":  {Sources: []string{"orders"}, Transform: semanticmodel.Transform{SQL: "-- source.orders\nWITH q AS (SELECT * FROM source.orders) SELECT * FROM q JOIN model.orders ON q.id = model.orders.id"}, PrimaryKey: "id"},
		},
	}
	if err := model.ValidateAuthored(); err != nil {
		t.Fatalf("ValidateAuthored() error = %v", err)
	}
	if got := model.Tables["daily"].SourceDependencies; len(got) != 1 || got[0] != "orders" {
		t.Fatalf("source dependencies = %#v, want [orders]", got)
	}
	if got := model.Tables["daily"].ModelDependencies; len(got) != 1 || got[0] != "orders" {
		t.Fatalf("model dependencies = %#v, want [orders]", got)
	}
}

func TestFlatProjectPreservesStableIDForPunctuatedSourceName(t *testing.T) {
	projectPath := writeFlatProjectFixture(t, map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {kind: managed}
`,
		"sources/foo-bar.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:foo-bar, name: foo-bar}
spec: {connection: warehouse, format: csv, path: foo-bar.csv}
`,
		"models/orders.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders}
spec: {source: foo-bar, primaryKey: id}
`,
	})
	project, err := LoadProject(projectPath)
	if err != nil {
		t.Fatalf("LoadProject() error = %v", err)
	}
	resource, ok := project.Graph.Resource("source:foo-bar")
	if !ok || resource.Kind != projectgraph.KindSource || resource.Name != "foo-bar" {
		t.Fatalf("source graph resource = %#v, present=%v; want source:foo-bar/foo-bar", resource, ok)
	}
	if got := project.Manifest.Models["model:orders"].Source; got != "source:foo-bar" {
		t.Fatalf("manifest model source = %q, want stable source ID", got)
	}
}

func TestFlatProjectRejectsCollidingSourceAliases(t *testing.T) {
	projectPath := writeFlatProjectFixture(t, map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {kind: managed}
`,
		"sources/foo-bar.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:foo-bar, name: foo-bar}
spec: {connection: warehouse, format: csv, path: foo-bar.csv}
`,
		"sources/foo_bar.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:foo_bar, name: foo_bar}
spec: {connection: warehouse, format: csv, path: foo_bar.csv}
`,
		"models/orders.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders}
spec: {source: foo-bar, primaryKey: id}
`,
	})
	_, err := LoadProject(projectPath)
	if err == nil {
		t.Fatal("LoadProject() accepted colliding source aliases")
	}
	for _, want := range []string{"foo-bar", "foo_bar", "source:foo-bar", "source:foo_bar", "runtime source alias"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("LoadProject() error = %v, want %q", err, want)
		}
	}
}

func TestFlatAccessRejectsWrongKindAndCapability(t *testing.T) {
	project := Project{
		Access:      manifest.AccessPolicy{Groups: map[string]manifest.Group{}, RoleBindings: map[string]manifest.RoleBinding{}, Grants: map[string]manifest.Grant{"bad": {ID: "grant:bad", Name: "bad", Object: manifest.SecurableRef{Kind: "source", ID: "dashboard:one"}, Subject: manifest.Subject{Kind: "principal", Email: "user@example.test"}, Capability: "NOT_A_CAPABILITY"}}, DataPolicies: map[string]manifest.DataPolicy{}},
		AccessPaths: map[string]string{"bad": "access/bad.yaml"}, ResourceIDs: map[string]string{"grant:bad": "grant:bad"},
	}
	resolver, err := newResourceResolver([]projectgraph.Resource{{ID: "project:test", Kind: projectgraph.KindProject, Name: "test"}, {ID: "dashboard:one", Kind: projectgraph.KindDashboard, Name: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateFlatAccess(project, resolver); err == nil || !strings.Contains(err.Error(), "unsupported capability") {
		t.Fatalf("validateFlatAccess() error = %v, want capability diagnostic", err)
	}
	project.Access.Grants["bad"] = manifest.Grant{ID: "grant:bad", Name: "bad", Object: manifest.SecurableRef{Kind: "source", ID: "dashboard:one"}, Subject: manifest.Subject{Kind: "principal", Email: "user@example.test"}, Capability: "RESOURCE_READ"}
	if err := validateFlatAccess(project, resolver); err == nil || !strings.Contains(err.Error(), "want source") {
		t.Fatalf("validateFlatAccess() error = %v, want wrong-kind diagnostic", err)
	}
	project.Access.Grants["bad"] = manifest.Grant{ID: "grant:bad", Name: "bad", Object: manifest.SecurableRef{Kind: "source", ID: "source:one"}, Subject: manifest.Subject{Kind: "principal", Email: "user@example.test"}, Capability: "RESOURCE_SHARE"}
	resolver, err = newResourceResolver([]projectgraph.Resource{{ID: "project:test", Kind: projectgraph.KindProject, Name: "test"}, {ID: "source:one", Kind: projectgraph.KindSource, Name: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateFlatAccess(project, resolver); err == nil || !strings.Contains(err.Error(), "unsupported capability") {
		t.Fatalf("validateFlatAccess() accepted RESOURCE_SHARE on source")
	}
}

func TestCompileProjectGraphResolvesStableIDsAndProvenance(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("leapview.yaml", `apiVersion: leapview.dev/v1
kind: Project
metadata: {id: project:demo, name: demo, displayName: Demo}
spec:
  connections: {include: [connections/*.yaml]}
  sources: {include: [sources/*.yaml]}
  models: {include: [models/*.yaml]}
  semanticModels: {include: [semantic-models/*.yaml]}
  pipelines: {include: [pipelines/*.yaml]}
  dashboards: {include: [dashboards/*.yaml]}
  access: {include: []}
`)
	write("connections/warehouse.yaml", `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: conn:warehouse, name: warehouse}
spec: {kind: managed}
`)
	write("sources/orders.yaml", `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:orders, name: orders}
spec:
  connection: warehouse
  format: csv
  path: orders.csv
`)
	write("models/orders.yaml", `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders_model}
spec:
  source: orders
  primaryKey: order_id
`)
	write("semantic-models/sales.yaml", `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:sales, name: sales}
spec:
  tables: [orders_model]
  measures:
    order_count: {fact: orders_model, aggregation: count, empty: zero}
`)
	write("pipelines/sales.yaml", `apiVersion: leapview.dev/v1
kind: Pipeline
metadata: {id: pipeline:sales, name: sales_refresh}
spec: {semanticModel: sales}
`)
	write("dashboards/sales.yaml", `apiVersion: leapview.dev/v1
kind: Dashboard
metadata: {id: dashboard:sales, name: sales_dashboard, displayName: Sales Dashboard}
spec:
  title: Sales Dashboard
  semanticModel: sales
  visuals:
    order_count:
      type: kpi
      query: {measures: {order_count: null}}
  pages: [{id: overview, title: Overview, components: []}]
`)

	compiled, err := CompileProjectGraph(filepath.Join(root, "leapview.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if compiled.ProjectID() != "project:demo" {
		t.Fatalf("project id = %q", compiled.ProjectID())
	}
	resources := compiled.Resources()
	if len(resources) != 7 {
		t.Fatalf("resource count = %d, want 7", len(resources))
	}
	for _, resource := range resources {
		if resource.Kind == projectgraph.KindProject {
			continue
		}
		if resource.Provenance.Path == "" || filepath.IsAbs(resource.Provenance.Path) {
			t.Fatalf("resource %q provenance = %#v", resource.ID, resource.Provenance)
		}
	}
	resolver, err := newResourceResolver(compiled.Resources())
	if err != nil {
		t.Fatal(err)
	}
	if id, err := resolver.resolve("orders_model", projectgraph.KindModel); err != nil || id != "model:orders" {
		t.Fatalf("resolve model = %q, %v", id, err)
	}
	if _, err := resolver.resolve("orders_model", projectgraph.KindSource); err == nil {
		t.Fatal("resolved model as source")
	}
	if _, err := resolver.resolve("missing", projectgraph.KindModel); err == nil {
		t.Fatal("resolved missing reference")
	}
	if len(compiled.Edges()) != 5 {
		t.Fatalf("edge count = %d, want 5", len(compiled.Edges()))
	}
}

func TestCompileProjectGraphShowcase(t *testing.T) {
	graph, err := CompileProjectGraph(filepath.Join("..", "..", "..", "dashboards", "leapview.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if graph.ProjectID() != "project:leapview-showcase" {
		t.Fatalf("project id = %q", graph.ProjectID())
	}
	if len(graph.Resources()) < 10 {
		t.Fatalf("resource count = %d", len(graph.Resources()))
	}
	wantKinds := map[projectgraph.Kind]bool{
		projectgraph.KindProject: true, projectgraph.KindConnection: true, projectgraph.KindSource: true,
		projectgraph.KindModel: true, projectgraph.KindSemanticModel: true, projectgraph.KindPipeline: true,
		projectgraph.KindDashboard: true,
	}
	seenKinds := map[projectgraph.Kind]bool{}
	for _, resource := range graph.Resources() {
		if !wantKinds[resource.Kind] {
			t.Fatalf("project graph contains non-project resource kind %q", resource.Kind)
		}
		seenKinds[resource.Kind] = true
	}
	for kind := range wantKinds {
		if !seenKinds[kind] {
			t.Fatalf("project graph omitted project resource kind %q", kind)
		}
	}
}

func TestCompileProjectGraphAcceptsCanonicalReferenceIDs(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("leapview.yaml", `apiVersion: leapview.dev/v1
kind: Project
metadata: {id: project:id-refs, name: id-refs}
spec:
  connections: {include: [connections/*.yaml]}
  sources: {include: [sources/*.yaml]}
  models: {include: [models/*.yaml]}
  semanticModels: {include: [semantic-models/*.yaml]}
  pipelines: {include: []}
  dashboards: {include: []}
  access: {include: []}
`)
	write("connections/c.yaml", "apiVersion: leapview.dev/v1\nkind: Connection\nmetadata: {id: connection:id, name: warehouse}\nspec: {kind: managed}\n")
	write("sources/s.yaml", "apiVersion: leapview.dev/v1\nkind: Source\nmetadata: {id: source:id, name: orders}\nspec: {connection: warehouse, format: csv, path: orders.csv}\n")
	write("models/m.yaml", "apiVersion: leapview.dev/v1\nkind: Model\nmetadata: {id: model:id, name: orders_model}\nspec: {source: source:id, primaryKey: id}\n")
	write("semantic-models/s.yaml", "apiVersion: leapview.dev/v1\nkind: SemanticModel\nmetadata: {id: semantic-model:id, name: sales}\nspec: {tables: [model:id], measures: {count: {fact: orders_model, aggregation: count, empty: zero}}}\n")
	graph, err := CompileProjectGraph(filepath.Join(root, "leapview.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Edges()) != 3 {
		t.Fatalf("edges = %d, want 3", len(graph.Edges()))
	}
}

func TestFlatProjectAllowsTwoSemanticConsumersOfOneModel(t *testing.T) {
	projectPath := writeFlatProjectFixture(t, map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {kind: managed}
`,
		"sources/orders.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:orders, name: orders}
spec: {connection: warehouse, format: csv, path: orders.csv}
`,
		"models/orders.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders_model}
spec:
  source: orders
  primaryKey: id
  fields: {id: {type: string}}
`,
		"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:sales, name: sales}
spec:
  tables: [orders_model]
  dimensions:
    shared_id:
      type: string
      bindings: {orders_model: {field: orders_model.id}}
  measures: {row_count: {fact: orders_model, aggregation: count, empty: zero}}
`,
		"semantic-models/operations.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:operations, name: operations}
spec:
  tables: [orders_model]
  dimensions:
    shared_id:
      type: string
      bindings: {orders_model: {field: orders_model.id}}
  measures: {row_count: {fact: orders_model, aggregation: count, empty: zero}}
`,
	})
	project, err := LoadProject(projectPath)
	if err != nil {
		t.Fatalf("LoadProject() error = %v", err)
	}
	graph := project.Graph
	var consumers int
	for _, edge := range graph.Edges() {
		if edge.Relation == "uses_model" && edge.To == "model:orders" {
			consumers++
		}
	}
	if consumers != 2 {
		t.Fatalf("semantic consumers of model:orders = %d, want 2", consumers)
	}
	for _, id := range []string{"semantic:sales", "semantic:operations"} {
		model := project.Manifest.SemanticModels[id]
		if model == nil || model.Dimensions["shared_id"].Name != "shared_id" {
			t.Fatalf("semantic model %s lost shared dimension: %#v", id, model)
		}
	}
}

func TestFlatProjectRejectsDuplicateStableIDsAcrossKinds(t *testing.T) {
	projectPath := writeFlatProjectFixture(t, map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: resource:duplicate, name: warehouse}
spec: {kind: managed}
`,
		"models/orders.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: resource:duplicate, name: orders}
spec: {source: orders, primaryKey: id}
`,
	})
	_, err := LoadProject(projectPath)
	if err == nil || !strings.Contains(err.Error(), "duplicates resource") {
		t.Fatalf("LoadProject() error = %v, want duplicate stable ID", err)
	}
	diagnostics := configschema.Diagnostics(err)
	if len(diagnostics) == 0 || diagnostics[0].ResourceID != "resource:duplicate" || !strings.HasSuffix(filepath.ToSlash(diagnostics[0].File), "models/orders.yaml") {
		t.Fatalf("diagnostics = %#v, want model path and stable ID", diagnostics)
	}
}

func TestFlatProjectWrongReferenceReportsResourcePathAndField(t *testing.T) {
	projectPath := writeFlatProjectFixture(t, map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {kind: managed}
`,
		"sources/orders.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:orders, name: orders}
spec: {connection: warehouse, format: csv, path: orders.csv}
`,
		"models/orders.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders_model}
spec: {source: orders, primaryKey: id}
`,
		"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:sales, name: sales}
spec: {tables: [orders], measures: {}}
`,
	})
	_, err := LoadProject(projectPath)
	if err == nil || !strings.Contains(err.Error(), `resolves to source, want model`) {
		t.Fatalf("LoadProject() error = %v, want wrong-kind reference", err)
	}
	diagnostics := configschema.Diagnostics(err)
	if len(diagnostics) == 0 || diagnostics[0].ResourceID != "semantic:sales" || diagnostics[0].FieldPath != "spec.tables" || !strings.HasSuffix(filepath.ToSlash(diagnostics[0].File), "semantic-models/sales.yaml") {
		t.Fatalf("diagnostics = %#v, want semantic model path/id/field", diagnostics)
	}
}

func TestFlatProjectManifestIsCheckoutIndependentAndCanonical(t *testing.T) {
	files := map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {kind: managed}
`,
		"sources/orders.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:orders, name: orders}
spec: {connection: warehouse, format: csv, path: orders.csv}
`,
		"models/orders.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders_model}
spec: {source: orders, primaryKey: id}
`,
		"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:sales, name: sales}
spec: {tables: [orders_model], measures: {}}
`,
	}
	first, err := LoadProject(writeFlatProjectFixture(t, files))
	if err != nil {
		t.Fatalf("LoadProject(first) error = %v", err)
	}
	second, err := LoadProject(writeFlatProjectFixture(t, files))
	if err != nil {
		t.Fatalf("LoadProject(second) error = %v", err)
	}
	firstManifest, err := json.Marshal(first.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	secondManifest, err := json.Marshal(second.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstManifest) != string(secondManifest) || first.Graph.Digest() != second.Graph.Digest() {
		t.Fatal("manifest or graph changed with checkout root")
	}
	if got := first.Manifest.Sources["source:orders"].Connection; got != "connection:warehouse" {
		t.Fatalf("manifest source connection = %q, want canonical ID", got)
	}
	if got := first.Manifest.ResourceFiles["model:orders"]; got != "models/orders.yaml" {
		t.Fatalf("manifest model resource path = %q, want relative path", got)
	}
	if got := first.Manifest.NameIndex.Models["orders_model"]; got != "model:orders" {
		t.Fatalf("manifest model name index = %q, want stable ID", got)
	}
}

func TestLoadProjectRejectsSymlinkEscapingInclude(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideConnection := filepath.Join(outside, "warehouse.yaml")
	if err := os.WriteFile(outsideConnection, []byte(`apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {kind: managed}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "connections"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "connections", "warehouse.yaml")
	if err := os.Symlink(outsideConnection, link); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(root, "leapview.yaml")
	if err := os.WriteFile(projectPath, []byte(`apiVersion: leapview.dev/v1
kind: Project
metadata: {id: project:symlink, name: symlink}
spec:
  connections: {include: [connections/*.yaml]}
  sources: {include: []}
  models: {include: []}
  semanticModels: {include: []}
  pipelines: {include: []}
  dashboards: {include: []}
  access: {include: []}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadProject(projectPath)
	if err == nil || !strings.Contains(err.Error(), "resolves outside project boundary") || !strings.Contains(err.Error(), "connections/warehouse.yaml") {
		t.Fatalf("LoadProject() error = %v, want project-relative symlink diagnostic", err)
	}
	if strings.Contains(err.Error(), root) || strings.Contains(err.Error(), outside) {
		t.Fatalf("symlink diagnostic leaked absolute checkout path: %v", err)
	}
}

func TestExpandIncludesRejectsNoMatch(t *testing.T) {
	_, err := expandIncludes(t.TempDir(), []string{"connections/*.yaml"})
	if err == nil || !strings.Contains(err.Error(), "matched no files") {
		t.Fatalf("expandIncludes() error = %v, want no-match diagnostic", err)
	}
}

func TestLoadProjectDeduplicatesOverlappingIncludes(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "connections"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "connections", "warehouse.yaml"), []byte(`apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {kind: managed}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(root, "leapview.yaml")
	if err := os.WriteFile(projectPath, []byte(`apiVersion: leapview.dev/v1
kind: Project
metadata: {id: project:overlap, name: overlap}
spec:
  connections: {include: [connections/*.yaml, connections/warehouse.yaml]}
  sources: {include: []}
  models: {include: []}
  semanticModels: {include: []}
  pipelines: {include: []}
  dashboards: {include: []}
  access: {include: []}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	project, err := LoadProject(projectPath)
	if err != nil {
		t.Fatalf("LoadProject() error = %v", err)
	}
	if len(project.Connections) != 1 || len(project.ConnectionPaths) != 1 {
		t.Fatalf("connections = %#v paths = %#v, want one deduplicated resource", project.Connections, project.ConnectionPaths)
	}
}

func TestProjectRelativePathNeverFallsBackToAbsolute(t *testing.T) {
	project := Project{BaseDir: t.TempDir()}
	path := filepath.Join(t.TempDir(), "outside.yaml")
	if got := projectRelativePath(&project, path); filepath.IsAbs(got) {
		t.Fatalf("projectRelativePath() returned absolute fallback %q", got)
	}
}

func TestFlatProjectRejectsTargetOwnedConnectionCredentials(t *testing.T) {
	projectPath := writeFlatProjectFixture(t, map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec:
  kind: managed
  credentials: {provider: env, secret: LEAPVIEW_WAREHOUSE_CREDENTIALS}
`,
	})
	_, err := LoadProject(projectPath)
	if err == nil || !strings.Contains(err.Error(), "target-owned") {
		t.Fatalf("LoadProject() error = %v, want target-owned credential diagnostic", err)
	}
	diagnostics := configschema.Diagnostics(err)
	if len(diagnostics) == 0 || diagnostics[0].ResourceID != "connection:warehouse" || diagnostics[0].FieldPath != "spec" {
		t.Fatalf("diagnostics = %#v, want connection resource and spec", diagnostics)
	}
}

func TestFlatProjectRejectsHiddenSQLImportsAndUnsafeIncludes(t *testing.T) {
	projectPath := writeFlatProjectFixture(t, map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {kind: managed}
`,
		"sources/orders.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:orders, name: orders}
spec: {connection: warehouse, format: csv, path: orders.csv}
`,
		"models/orders.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders_model}
spec:
  sources: [orders]
  transform: {sql: 'SELECT * FROM raw.orders'}
  primaryKey: id
`,
	})
	if _, err := LoadProject(projectPath); err == nil || !strings.Contains(err.Error(), "raw.<name> is internal") {
		t.Fatalf("LoadProject() accepted hidden raw import: %v", err)
	}
	projectBytes, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	projectBytes = []byte(strings.Replace(string(projectBytes), "connections/*.yaml", "../*.yaml", 1))
	if err := os.WriteFile(projectPath, projectBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProject(projectPath); err == nil || !strings.Contains(err.Error(), "escapes project boundary") {
		t.Fatalf("LoadProject() accepted escaping include: %v", err)
	}
}

func TestFlatProjectRefreshPipelinesValidateAndNormalize(t *testing.T) {
	projectYAML := strings.Replace(flatProjectFixtureYAML(), "pipelines: {include: []}", "pipelines: {include: [pipelines/*.yaml]}", 1)
	base := map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {kind: managed}
`,
		"sources/orders.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:orders, name: orders}
spec: {connection: warehouse, format: csv, path: orders.csv}
`,
		"models/orders.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders_model}
spec: {source: orders, primaryKey: id}
`,
		"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:sales, name: sales}
spec: {tables: [orders_model], measures: {}}
`,
	}
	validFiles := cloneFixtureFiles(base)
	validFiles["pipelines/sales-refresh.yaml"] = `apiVersion: leapview.dev/v1
kind: Pipeline
metadata: {id: pipeline:sales-refresh, name: sales_refresh}
spec:
  semanticModel: sales
  on: {schedule: [{cron: "0 6 * * *", timezone: Europe/Copenhagen}]}
`
	projectPath := writeFlatProjectFixtureWithProject(t, projectYAML, validFiles)
	project, err := LoadProject(projectPath)
	if err != nil {
		t.Fatalf("LoadProject(valid pipeline) error = %v", err)
	}
	pipeline := project.RefreshPipelines["sales_refresh"]
	if pipeline.ID != "pipeline:sales-refresh" || pipeline.SemanticModelID != "sales" || len(pipeline.Schedules) != 1 || pipeline.Schedules[0].Timezone != "Europe/Copenhagen" {
		t.Fatalf("pipeline = %#v, want normalized schedule", pipeline)
	}
	manualFiles := cloneFixtureFiles(base)
	manualFiles["pipelines/manual.yaml"] = `apiVersion: leapview.dev/v1
kind: Pipeline
metadata: {id: pipeline:manual, name: manual}
spec: {semanticModel: sales}
`
	manual, err := LoadProject(writeFlatProjectFixtureWithProject(t, projectYAML, manualFiles))
	if err != nil {
		t.Fatalf("LoadProject(manual pipeline) error = %v", err)
	}
	if got := len(manual.RefreshPipelines["manual"].Schedules); got != 0 {
		t.Fatalf("manual pipeline schedules = %d, want 0", got)
	}
	invalidFiles := cloneFixtureFiles(base)
	invalidFiles["pipelines/bad.yaml"] = `apiVersion: leapview.dev/v1
kind: Pipeline
metadata: {id: pipeline:bad, name: bad}
spec: {semanticModel: missing}
`
	_, err = LoadProject(writeFlatProjectFixtureWithProject(t, projectYAML, invalidFiles))
	if err == nil || !strings.Contains(err.Error(), `reference "missing" is missing`) {
		t.Fatalf("LoadProject(invalid pipeline) error = %v, want missing semantic model", err)
	}
}

func TestFlatProjectRejectsInlineConnectionAuthAndSourceIdentity(t *testing.T) {
	for name, tc := range map[string]struct {
		spec string
		want string
	}{
		"auth": {spec: `spec: {kind: managed, auth: {token: secret}}
`, want: "field not allowed"},
		"source identity": {spec: `spec: {kind: postgres, username: privileged_runtime}
`, want: "target-owned"},
	} {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{"connections/warehouse.yaml": "apiVersion: leapview.dev/v1\nkind: Connection\nmetadata: {id: connection:warehouse, name: warehouse}\n" + tc.spec}
			_, err := LoadProject(writeFlatProjectFixture(t, files))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadProject() error = %v, want schema rejection", err)
			}
			diagnostics := configschema.Diagnostics(err)
			if len(diagnostics) == 0 || diagnostics[0].ResourceID != "connection:warehouse" {
				t.Fatalf("diagnostics = %#v, want connection resource", diagnostics)
			}
		})
	}
}

func TestFlatProjectRejectsSQLSourceMismatchAndModelCycles(t *testing.T) {
	base := map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {kind: managed}
`,
		"sources/orders.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:orders, name: orders}
spec: {connection: warehouse, format: csv, path: orders.csv}
`,
		"sources/customers.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:customers, name: customers}
spec: {connection: warehouse, format: csv, path: customers.csv}
`,
	}
	t.Run("source mismatch", func(t *testing.T) {
		files := cloneFixtureFiles(base)
		files["models/orders.yaml"] = `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders_model}
spec: {sources: [orders], transform: {sql: 'SELECT * FROM source.customers'}, primaryKey: id}
`
		_, err := LoadProject(writeFlatProjectFixture(t, files))
		if err == nil || !strings.Contains(err.Error(), "SQL source references") {
			t.Fatalf("LoadProject() error = %v, want SQL source mismatch", err)
		}
	})
	t.Run("model cycle", func(t *testing.T) {
		files := cloneFixtureFiles(base)
		files["models/orders.yaml"] = `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders_model}
spec: {sources: [orders], transform: {sql: 'SELECT * FROM source.orders JOIN model.customers_model USING (id)'}, primaryKey: id}
`
		files["models/customers.yaml"] = `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:customers, name: customers_model}
spec: {sources: [customers], transform: {sql: 'SELECT * FROM source.customers JOIN model.orders_model USING (id)'}, primaryKey: id}
`
		_, err := LoadProject(writeFlatProjectFixture(t, files))
		if err == nil || !strings.Contains(err.Error(), "cycle") {
			t.Fatalf("LoadProject() error = %v, want model cycle", err)
		}
	})
}

func TestFlatProjectDashboardAdapterMatchesDirectCompilation(t *testing.T) {
	projectYAML := strings.Replace(flatProjectFixtureYAML(), "dashboards: {include: []}", "dashboards: {include: [dashboards/*.yaml]}", 1)
	files := map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {kind: managed}
`,
		"sources/orders.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:orders, name: orders}
spec: {connection: warehouse, format: csv, path: orders.csv}
`,
		"models/orders.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders_model}
spec: {source: orders, primaryKey: id}
`,
		"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:sales, name: sales}
spec: {tables: [orders_model], measures: {order_count: {fact: orders_model, aggregation: count, empty: zero}}}
`,
		"dashboards/sales.yaml": `apiVersion: leapview.dev/v1
kind: Dashboard
metadata: {id: dashboard:sales, name: sales_dashboard, displayName: Sales}
spec:
  title: Sales
  semanticModel: sales
  visuals: {order_count: {type: kpi, query: {measures: {order_count: null}}}}
  pages: [{id: overview, title: Overview, components: []}]
`,
	}
	project, err := LoadProject(writeFlatProjectFixtureWithProject(t, projectYAML, files))
	if err != nil {
		t.Fatalf("LoadProject() error = %v", err)
	}
	authored := *project.Dashboards["sales_dashboard"]
	model := project.Manifest.SemanticModels["semantic:sales"]
	authored.SemanticModel = "semantic:sales"
	direct, err := dashboardcompiler.Compile(authored, map[string]*semanticmodel.Model{"semantic:sales": model})
	if err != nil {
		t.Fatalf("direct dashboard compilation error = %v", err)
	}
	if got := project.Manifest.DashboardDefinitions["dashboard:sales"]; !reflect.DeepEqual(got, direct.Definition) {
		t.Fatalf("project dashboard definition differs from direct compilation:\nproject=%#v\ndirect=%#v", got, direct.Definition)
	}
	if got := project.Manifest.DashboardDefinitions["dashboard:sales"].SemanticModel; got != "semantic:sales" {
		t.Fatalf("dashboard definition semantic model = %q, want canonical stable ID", got)
	}
}

func TestFlatProjectPublicationValidationAndCanonicalization(t *testing.T) {
	projectYAML := strings.NewReplacer(
		"dashboards: {include: []}", "dashboards: {include: [dashboards/*.yaml]}",
		"publications: {include: []}", "publications: {include: [publications/*.yaml]}",
	).Replace(flatProjectFixtureYAML())
	files := map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {kind: managed}
`,
		"sources/orders.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:orders, name: orders}
spec: {connection: warehouse, format: csv, path: orders.csv}
`,
		"models/orders.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders_model}
spec: {source: orders, primaryKey: id}
`,
		"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:sales, name: sales}
spec: {tables: [orders_model], measures: {order_count: {fact: orders_model, aggregation: count, empty: zero}}}
`,
		"dashboards/sales.yaml": `apiVersion: leapview.dev/v1
kind: Dashboard
metadata: {id: dashboard:sales, name: sales_dashboard, displayName: Sales}
spec:
  title: Sales
  semanticModel: sales
  visuals: {order_count: {type: kpi, query: {measures: {order_count: null}}}}
  pages: [{id: overview, title: Overview, components: []}]
`,
		"publications/website.yaml": `apiVersion: leapview.dev/v1
kind: DashboardPublication
metadata: {id: publication:website, name: website}
spec:
  dashboard: sales_dashboard
  defaultPage: overview
  embedding: {allowedOrigins: [https://z.example, https://a.example]}
`,
	}
	project, err := LoadProject(writeFlatProjectFixtureWithProject(t, projectYAML, files))
	if err != nil {
		t.Fatalf("LoadProject() error = %v", err)
	}
	publication := project.Manifest.Publications["publication:website"]
	if publication.Dashboard != "dashboard:sales" || !reflect.DeepEqual(publication.AllowedOrigins, []string{"https://a.example", "https://z.example"}) {
		t.Fatalf("publication = %#v, want canonical dashboard and sorted origins", publication)
	}
	for _, want := range []string{"dashboard:sales", "semantic:sales", "model:orders", "source:orders", "connection:warehouse"} {
		if !containsString(publication.DependencyAssetIDs, want) {
			t.Fatalf("publication dependency closure = %v, missing %q", publication.DependencyAssetIDs, want)
		}
	}
	if publication.ConfigurationDigest == "" {
		t.Fatal("publication configuration digest is empty")
	}
	invalid := cloneFixtureFiles(files)
	invalid["publications/website.yaml"] = strings.Replace(files["publications/website.yaml"], "https://z.example, https://a.example", "http://example.com", 1)
	if _, err := LoadProject(writeFlatProjectFixtureWithProject(t, projectYAML, invalid)); err == nil || !strings.Contains(err.Error(), "must use https") {
		t.Fatalf("LoadProject() accepted invalid publication origin: %v", err)
	}
}

func TestFlatAccessDataPolicyValidatesExpressionAndSubject(t *testing.T) {
	project := Project{
		Access: manifest.AccessPolicy{
			Groups:       map[string]manifest.Group{"analysts": {ID: "group:analysts", Name: "analysts"}},
			RoleBindings: map[string]manifest.RoleBinding{},
			Grants:       map[string]manifest.Grant{},
			DataPolicies: map[string]manifest.DataPolicy{"policy": {ID: "policy:bad", Name: "policy", Object: manifest.SecurableRef{Kind: "source", ID: "source:orders"}, PolicyType: "row_filter", ExpressionJSON: `{}`}},
		},
		AccessPaths: map[string]string{"policy": "access/policy.yaml"}, ResourceIDs: map[string]string{"datapolicy:policy": "policy:bad"},
	}
	resolver, err := newResourceResolver([]projectgraph.Resource{{ID: "source:orders", Kind: projectgraph.KindSource, Name: "orders"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateFlatAccess(project, resolver); err == nil || !strings.Contains(err.Error(), "requires field or filters") {
		t.Fatalf("validateFlatAccess() accepted invalid policy expression: %v", err)
	}
	project.Access.DataPolicies["policy"] = manifest.DataPolicy{ID: "policy:bad", Name: "policy", Object: manifest.SecurableRef{Kind: "source", ID: "source:orders"}, PolicyType: "row_filter", ExpressionJSON: `{"allowAll":true}`, Subject: manifest.Subject{Kind: "group", Group: "missing"}}
	if err := validateFlatAccess(project, resolver); err == nil || !strings.Contains(err.Error(), "unknown Group") {
		t.Fatalf("validateFlatAccess() accepted unknown policy subject: %v", err)
	}
}

func TestProjectPlanDiffIsDeterministicAndAggregatesImpact(t *testing.T) {
	resources := []projectgraph.Resource{
		{ID: "project:test", Kind: projectgraph.KindProject, Name: "test"},
		{ID: "source:orders", Kind: projectgraph.KindSource, Name: "orders"},
		{ID: "model:orders", Kind: projectgraph.KindModel, Name: "orders_model"},
	}
	authored, err := projectgraph.NewProjectGraph(resources, []projectgraph.Edge{{From: "model:orders", To: "source:orders", Relation: "reads_source"}})
	if err != nil {
		t.Fatal(err)
	}
	activeResources := append([]projectgraph.Resource(nil), resources...)
	activeResources[1].Metadata.Description = "changed"
	activeResources[1].Metadata.Domain = "sales"
	activeResources[1].Provenance.Path = "renamed/orders.yaml"
	active, err := projectgraph.NewProjectGraph(activeResources, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstChanges, firstDeps, firstSummary := diffProjectGraphs(authored, active)
	secondChanges, secondDeps, secondSummary := diffProjectGraphs(authored, active)
	if !reflect.DeepEqual(firstChanges, secondChanges) || !reflect.DeepEqual(firstDeps, secondDeps) || firstSummary != secondSummary {
		t.Fatal("project graph diff is not deterministic")
	}
	if firstSummary.Changed != 1 || firstSummary.DependencyChanges != 1 || firstSummary.Breaking || !firstSummary.MaterializationImpact {
		t.Fatalf("summary = %#v, want metadata-only change plus dependency materialization impact", firstSummary)
	}
	removedResources := []projectgraph.Resource{resources[0], resources[2]}
	removed, err := projectgraph.NewProjectGraph(removedResources, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The reduced graph is the authored candidate; the complete graph is the
	// active baseline, so the source is correctly reported as removed.
	_, _, removedSummary := diffProjectGraphs(removed, authored)
	if !removedSummary.Breaking || !removedSummary.MaterializationImpact {
		t.Fatalf("removed source summary = %#v, want breaking materialization impact", removedSummary)
	}
	kindChangedResources := append([]projectgraph.Resource(nil), resources...)
	kindChangedResources[1].Kind = projectgraph.KindModel
	kindChanged, err := projectgraph.NewProjectGraph(kindChangedResources, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, kindChangedSummary := diffProjectGraphs(authored, kindChanged)
	if !kindChangedSummary.Breaking {
		t.Fatalf("kind change summary = %#v, want breaking", kindChangedSummary)
	}
}

func cloneFixtureFiles(files map[string]string) map[string]string {
	clone := make(map[string]string, len(files))
	for name, body := range files {
		clone[name] = body
	}
	return clone
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func writeFlatProjectFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	return writeFlatProjectFixtureWithProject(t, flatProjectFixtureYAMLForFiles(files), files)
}

func writeFlatProjectFixtureWithProject(t *testing.T, project string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files["leapview.yaml"] = project
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, "leapview.yaml")
}

func flatProjectFixtureYAML() string {
	return `apiVersion: leapview.dev/v1
kind: Project
metadata: {id: project:test, name: test}
spec:
  connections: {include: [connections/*.yaml]}
  sources: {include: [sources/*.yaml]}
  models: {include: [models/*.yaml]}
  semanticModels: {include: [semantic-models/*.yaml]}
  pipelines: {include: []}
  dashboards: {include: []}
  access: {include: []}
  publications: {include: []}
`
}

func flatProjectFixtureYAMLForFiles(files map[string]string) string {
	include := func(directory, pattern string) string {
		prefix := directory + "/"
		for name := range files {
			if strings.HasPrefix(name, prefix) {
				return "[" + pattern + "]"
			}
		}
		return "[]"
	}
	project := flatProjectFixtureYAML()
	project = strings.Replace(project, "[connections/*.yaml]", include("connections", "connections/*.yaml"), 1)
	project = strings.Replace(project, "[sources/*.yaml]", include("sources", "sources/*.yaml"), 1)
	project = strings.Replace(project, "[models/*.yaml]", include("models", "models/*.yaml"), 1)
	project = strings.Replace(project, "[semantic-models/*.yaml]", include("semantic-models", "semantic-models/*.yaml"), 1)
	return project
}
