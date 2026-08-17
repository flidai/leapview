package compiler

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
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
			"total": {Type: "kpi", Query: dashboardauthoring.VisualQuery{Metrics: []dashboardauthoring.FieldRef{{Field: "order_count", Alias: "value"}}}},
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

func TestDashboardDomainRoundTripsCompiledManifestAndExport(t *testing.T) {
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
spec: {source: orders, entities: {order: {type: primary, fields: [order_id]}}, grain: {entity: order}, fields: {order_id: {datatype: String}}}
`,
		"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:sales, name: sales}
spec:
  datasets: {orders: {model: orders_model}}
  metrics: {order_count: {type: aggregate, dataset: orders, aggregation: count, input: {field: orders.order_id}, empty: zero}}
`,
		"dashboards/sales.yaml": `apiVersion: leapview.dev/v1
kind: Dashboard
metadata: {id: dashboard:sales, name: sales_dashboard, displayName: Sales Dashboard, domain: revenue}
spec:
  title: Sales Dashboard
  semanticModel: sales
  visuals:
    order_count:
      type: kpi
      query: {metrics: {order_count: null}}
  pages: [{id: overview, title: Overview, components: []}]
`,
	}
	project := flatProjectFixtureYAMLForFiles(files)
	project = strings.Replace(project, "dashboards: {include: []}", "dashboards: {include: [dashboards/*.yaml]}", 1)
	projectPath := writeFlatProjectFixtureWithProject(t, project, files)
	compiled, err := LoadProject(projectPath)
	if err != nil {
		t.Fatalf("LoadProject() error = %v", err)
	}
	source, ok := compiled.Manifest.DashboardSources["dashboard:sales"]
	if !ok {
		t.Fatal("compiled manifest omitted dashboard source")
	}
	if source.Metadata.Domain != "revenue" {
		t.Fatalf("compiled dashboard source domain = %q, want revenue", source.Metadata.Domain)
	}
	encoded, err := ExportDashboard(source.Document, dashboardauthoring.DashboardExportMetadata{
		Name: source.Metadata.Name, Title: source.Metadata.Title, Description: source.Metadata.Description,
		Owner: source.Metadata.Owner, Domain: source.Metadata.Domain, Tags: source.Metadata.Tags,
	})
	if err != nil {
		t.Fatalf("ExportDashboard() error = %v", err)
	}
	if !strings.Contains(string(encoded), "domain: revenue") {
		t.Fatalf("canonical dashboard export omitted authored domain: %s", encoded)
	}
}

func TestAIContextIsPreservedWithoutChangingExecutableSemantics(t *testing.T) {
	load := func(t *testing.T, withContext bool) *Project {
		t.Helper()
		modelContext := ""
		semanticContext := ""
		if withContext {
			modelContext = "aiContext:\n  instructions: Use order line identity when answering questions.\n  synonyms: [order line]\n  examples: [show revenue by line]\n"
			semanticContext = "aiContext:\n  instructions: Prefer the sales grain.\n  synonyms: [sales]\n  examples: [compare revenue]\n"
		}
		files := map[string]string{
			"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {kind: managed}
`,
			"sources/orders_source.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:orders_source, name: orders_source}
spec: {connection: warehouse, format: csv, path: orders.csv}
`,
			"models/orders.yaml": "apiVersion: leapview.dev/v1\nkind: Model\nmetadata: {id: model:orders, name: orders}\n" + modelContext + `spec:
  source: orders_source
  entities:
    order_line: {type: primary, fields: [order_id, line_number]}
  grain: {entity: order_line}
  fields:
    order_id: {datatype: String}
    line_number: {datatype: Integer}
    revenue: {datatype: Decimal}
    activity_date: {datatype: Date}
`,
			"models/customers.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:customers, name: customers}
spec:
  source: orders_source
  entities:
    customer_line: {type: primary, fields: [order_id, line_number]}
  grain: {entity: customer_line}
  fields:
    order_id: {datatype: String}
    line_number: {datatype: Integer}
`,
			"semantic-models/sales.yaml": "apiVersion: leapview.dev/v1\nkind: SemanticModel\nmetadata: {id: semantic:sales, name: sales}\n" + semanticContext + `spec:
  datasets:
    orders: {model: orders, defaultTimeDimension: activity_date}
    customers: {model: customers}
  relationships:
    orders_customers:
      from: {dataset: orders, fields: [order_id, line_number]}
      to: {dataset: customers, fields: [order_id, line_number]}
  dimensions:
    activity_date:
      datatype: Date
      bindings:
        orders: {field: orders.activity_date}
      time: {nativeGrain: month, grains: [month, quarter, year]}
  metrics:
    revenue:
      type: aggregate
      dataset: orders
      aggregation: sum
      input: {field: orders.revenue}
      empty: 'null'
      timeDimension: activity_date
`,
			"access/sales-read.yaml": `apiVersion: leapview.dev/v1
kind: Grant
metadata: {id: grant:sales-read, name: sales-read}
spec:
  object: {kind: semantic_model, id: semantic:sales}
  subject: {kind: principal, principalId: alice}
  capability: RESOURCE_READ
`,
		}
		if withContext {
			files["models/orders.yaml"] = strings.Replace(files["models/orders.yaml"], "order_line: {type: primary, fields: [order_id, line_number]}", "order_line: {type: primary, fields: [order_id, line_number], aiContext: {instructions: Keep the order-line grain.}}", 1)
			files["models/orders.yaml"] = strings.Replace(files["models/orders.yaml"], "order_id: {datatype: String}", "order_id: {datatype: String, aiContext: {instructions: Use the order identifier.}}", 1)
			files["semantic-models/sales.yaml"] = strings.Replace(files["semantic-models/sales.yaml"], "orders: {model: orders, defaultTimeDimension: activity_date}", "orders: {model: orders, defaultTimeDimension: activity_date, aiContext: {instructions: Use the governed orders dataset.}}", 1)
			files["semantic-models/sales.yaml"] = strings.Replace(files["semantic-models/sales.yaml"], "to: {dataset: customers, fields: [order_id, line_number]}", "to: {dataset: customers, fields: [order_id, line_number]}\n      aiContext: {instructions: Traverse to customers safely.}", 1)
			files["semantic-models/sales.yaml"] = strings.Replace(files["semantic-models/sales.yaml"], "activity_date:\n      datatype: Date", "activity_date:\n      datatype: Date\n      aiContext: {instructions: Use the activity calendar.}", 1)
		}
		filterContext := ""
		if withContext {
			filterContext = "\n      aiContext: {instructions: Keep captured order rows.}"
		}
		files["semantic-models/sales.yaml"] = strings.Replace(files["semantic-models/sales.yaml"], "  metrics:\n", "  filters:\n    captured_orders:\n      field: orders.order_id\n      operator: equals\n      value: sample"+filterContext+"\n  metrics:\n", 1)
		metricContext := ""
		if withContext {
			metricContext = "\n      aiContext: {instructions: Explain governed revenue.}"
		}
		files["semantic-models/sales.yaml"] = strings.Replace(files["semantic-models/sales.yaml"], "      timeDimension: activity_date\n", "      timeDimension: activity_date\n      where: [captured_orders]"+metricContext+"\n", 1)
		projectYAML := flatProjectFixtureYAMLForFiles(files)
		projectYAML = strings.Replace(projectYAML, "access: {include: []}", "access: {include: [access/*.yaml]}", 1)
		return mustLoadProject(t, writeFlatProjectFixtureWithProject(t, projectYAML, files))
	}
	without := load(t, false)
	with := load(t, true)
	withoutModel := without.Manifest.SemanticModels["semantic:sales"]
	withModel := with.Manifest.SemanticModels["semantic:sales"]
	if withoutModel == nil || withModel == nil {
		t.Fatal("compiled semantic model missing")
	}
	if withModel.AIContext == nil || withModel.AIContext.Instructions == "" {
		t.Fatalf("semantic model AI context was not preserved: %#v", withModel.AIContext)
	}
	if with.Models["orders"].AIContext == nil || with.Models["orders"].AIContext.Instructions == "" {
		t.Fatalf("model AI context was not preserved: %#v", with.Models["orders"].AIContext)
	}
	if withModel.Tables["orders"].Entities["order_line"].AIContext == nil || withModel.Tables["orders"].Columns["order_id"].AIContext == nil {
		t.Fatalf("member model AI context was not preserved: table=%#v column=%#v", withModel.Tables["orders"].Entities["order_line"], withModel.Tables["orders"].Columns["order_id"])
	}
	if withModel.Datasets["orders"].AIContext == nil || withModel.StructuredRelationships["orders_customers"].AIContext == nil || withModel.Dimensions["activity_date"].AIContext == nil || withModel.Filters["captured_orders"].AIContext == nil || withModel.Metrics["revenue"].AIContext == nil {
		t.Fatalf("member semantic AI context was not preserved: dataset=%#v relationship=%#v dimension=%#v filter=%#v metric=%#v", withModel.Datasets["orders"], withModel.StructuredRelationships["orders_customers"], withModel.Dimensions["activity_date"], withModel.Filters["captured_orders"], withModel.Metrics["revenue"])
	}
	if len(withModel.Relationships) != 1 || !reflect.DeepEqual(withModel.Relationships[0].FromFields, []string{"order_id", "line_number"}) || !reflect.DeepEqual(withModel.Relationships[0].ToFields, []string{"order_id", "line_number"}) {
		t.Fatalf("composite relationship endpoints were collapsed: %#v", withModel.Relationships)
	}
	activityDate := withModel.Dimensions["activity_date"]
	if activityDate.Datatype != semanticmodel.DataTypeDate || activityDate.NativeGrain != "month" || !reflect.DeepEqual(activityDate.Grains, []string{"month", "quarter", "year"}) {
		t.Fatalf("time semantics were not retained: %#v", activityDate)
	}
	if withModel.Datasets["orders"].DefaultTimeDimension != "activity_date" {
		t.Fatalf("dataset default time dimension = %#v", withModel.Datasets["orders"])
	}
	if withModel.Metrics["revenue"].TimeDimension != "activity_date" {
		t.Fatalf("aggregate metric time dimension = %#v", withModel.Metrics["revenue"])
	}
	strip := func(model *semanticmodel.Model) semanticmodel.Model {
		copy := *model
		copy.AIContext = nil
		copy.Relationships = append([]semanticmodel.Relationship(nil), model.Relationships...)
		for index := range copy.Relationships {
			copy.Relationships[index].AIContext = nil
		}
		copy.Tables = make(map[string]semanticmodel.Table, len(model.Tables))
		for name, original := range model.Tables {
			table := original
			table.AIContext = nil
			table.Columns = maps.Clone(original.Columns)
			for field, column := range table.Columns {
				column.AIContext = nil
				table.Columns[field] = column
			}
			table.Dimensions = maps.Clone(original.Dimensions)
			for field, dimension := range table.Dimensions {
				dimension.AIContext = nil
				table.Dimensions[field] = dimension
			}
			table.Entities = maps.Clone(original.Entities)
			for name, entity := range table.Entities {
				entity.AIContext = nil
				table.Entities[name] = entity
			}
			copy.Tables[name] = table
		}
		copy.Datasets = maps.Clone(model.Datasets)
		for name, dataset := range copy.Datasets {
			dataset.AIContext = nil
			copy.Datasets[name] = dataset
		}
		copy.StructuredRelationships = maps.Clone(model.StructuredRelationships)
		for name, relationship := range copy.StructuredRelationships {
			relationship.AIContext = nil
			copy.StructuredRelationships[name] = relationship
		}
		copy.Dimensions = maps.Clone(model.Dimensions)
		for name, dimension := range copy.Dimensions {
			dimension.AIContext = nil
			copy.Dimensions[name] = dimension
		}
		copy.Filters = maps.Clone(model.Filters)
		for name, filter := range copy.Filters {
			filter.AIContext = nil
			copy.Filters[name] = filter
		}
		copy.Metrics = maps.Clone(model.Metrics)
		for name, metric := range copy.Metrics {
			metric.AIContext = nil
			copy.Metrics[name] = metric
		}
		return copy
	}
	if !reflect.DeepEqual(strip(withoutModel), strip(withModel)) {
		t.Fatalf("top-level AI context changed executable semantic model:\n%s", cmp.Diff(strip(withoutModel), strip(withModel)))
	}
	if !reflect.DeepEqual(without.Manifest.Access, with.Manifest.Access) {
		t.Fatalf("AI context changed compiled authorization artifact:\n%s", cmp.Diff(without.Manifest.Access, with.Manifest.Access))
	}
	withoutPlanner, err := semanticquery.NewCompiledPlanner(withoutModel)
	if err != nil {
		t.Fatalf("compile planner without AI context: %v", err)
	}
	withPlanner, err := semanticquery.NewCompiledPlanner(withModel)
	if err != nil {
		t.Fatalf("compile planner with AI context: %v", err)
	}
	request := semanticquery.Request{Dimensions: []semanticquery.Field{{Field: "activity_date"}}, Metrics: []semanticquery.Field{{Field: "revenue"}}}
	withoutPlan, err := withoutPlanner.Plan(request)
	if err != nil {
		t.Fatalf("plan without AI context: %v", err)
	}
	withPlan, err := withPlanner.Plan(request)
	if err != nil {
		t.Fatalf("plan with AI context: %v", err)
	}
	if withoutPlan.SQL != withPlan.SQL || !reflect.DeepEqual(withoutPlan.Args, withPlan.Args) {
		t.Fatalf("AI context changed executable plan:\n%s", cmp.Diff(withoutPlan, withPlan))
	}
}

func mustLoadProject(t *testing.T, path string) *Project {
	t.Helper()
	project, err := LoadProject(path)
	if err != nil {
		t.Fatalf("LoadProject(%q): %v", path, err)
	}
	return &project
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
			"orders": {Source: "orders", Entities: map[string]semanticmodel.ModelEntitySpec{"id": {Type: "primary", Fields: []string{"id"}}}, GrainEntity: "id"},
			"daily":  {Sources: []string{"orders"}, Transform: semanticmodel.Transform{SQL: "-- source.orders\nWITH q AS (SELECT * FROM source.orders) SELECT * FROM q JOIN model.orders ON q.id = model.orders.id"}, Entities: map[string]semanticmodel.ModelEntitySpec{"id": {Type: "primary", Fields: []string{"id"}}}, GrainEntity: "id"},
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
spec: {source: foo-bar, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}
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
spec: {source: foo-bar, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}
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
	project.Access.Grants["bad"] = manifest.Grant{ID: "grant:bad", Name: "bad", Object: manifest.SecurableRef{Kind: "project", ID: "project:test"}, Subject: manifest.Subject{Kind: "principal", PrincipalID: "principal:test"}, Capability: "RESOURCE_READ"}
	if err := validateFlatAccess(project, resolver); err == nil || !strings.Contains(err.Error(), "unsupported capability") {
		t.Fatalf("validateFlatAccess() accepted RESOURCE_READ as a direct project grant")
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
  entities: {order: {type: primary, fields: [order_id]}}
  grain: {entity: order}
  fields: {order_id: {datatype: String}}
`)
	write("semantic-models/sales.yaml", `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:sales, name: sales}
spec:
  datasets: {orders: {model: orders_model}}
  metrics: {order_count: {type: aggregate, dataset: orders, aggregation: count, input: {field: orders.order_id}, empty: zero}}
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
      query: {metrics: {order_count: null}}
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
	write("models/m.yaml", "apiVersion: leapview.dev/v1\nkind: Model\nmetadata: {id: model:id, name: orders_model}\nspec: {source: source:id, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}\n")
	write("semantic-models/s.yaml", "apiVersion: leapview.dev/v1\nkind: SemanticModel\nmetadata: {id: semantic-model:id, name: sales}\nspec: {datasets: {orders: {model: orders_model}}, metrics: {count: {type: aggregate, dataset: orders, aggregation: count, input: {field: orders.id}, empty: zero}}}\n")
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
  entities: {id: {type: primary, fields: [id]}}
  grain: {entity: id}
  fields: {id: {datatype: String}}
`,
		"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:sales, name: sales}
spec:
  datasets: {orders: {model: orders_model}}
  dimensions:
    shared_id:
      datatype: String
      bindings: {orders: {field: orders.id}}
  metrics: {row_count: {type: aggregate, dataset: orders, aggregation: count, input: {field: orders.id}, empty: zero}}
`,
		"semantic-models/operations.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:operations, name: operations}
spec:
  datasets: {order_rows: {model: orders_model}}
  dimensions:
    shared_id:
      datatype: String
      bindings: {order_rows: {field: order_rows.id}}
  metrics: {row_count: {type: aggregate, dataset: order_rows, aggregation: count, input: {field: order_rows.id}, empty: zero}}
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
	assertFieldOwner := func(modelID, dataset, want string) {
		t.Helper()
		model := project.Manifest.SemanticModels[modelID]
		field := model.Tables[dataset].Dimensions["id"]
		if field.Table != dataset || field.Field != want {
			t.Fatalf("semantic model %s dataset %s field owner = %#v, want %s", modelID, dataset, field, want)
		}
	}
	assertFieldOwner("semantic:sales", "orders", "orders.id")
	assertFieldOwner("semantic:operations", "order_rows", "order_rows.id")
	canonical := project.Manifest.Models["model:orders"].Dimensions["id"]
	if canonical.Table != "orders_model" || canonical.Field != "orders_model.id" {
		t.Fatalf("canonical Model field was mutated by semantic aliases: %#v", canonical)
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
spec: {source: orders, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}
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
spec: {source: orders, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}
`,
		"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:sales, name: sales}
spec: {datasets: {orders: {model: orders}}, metrics: {}}
`,
	})
	_, err := LoadProject(projectPath)
	if err == nil || !strings.Contains(err.Error(), `resolves to source, want model`) {
		t.Fatalf("LoadProject() error = %v, want wrong-kind reference", err)
	}
	diagnostics := configschema.Diagnostics(err)
	if len(diagnostics) == 0 || diagnostics[0].ResourceID != "semantic:sales" || diagnostics[0].FieldPath != "spec.datasets.orders.model" || !strings.HasSuffix(filepath.ToSlash(diagnostics[0].File), "semantic-models/sales.yaml") {
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
spec: {source: orders, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}
`,
		"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:sales, name: sales}
spec: {datasets: {orders: {model: orders_model}}, metrics: {}}
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
  entities: {id: {type: primary, fields: [id]}}
  grain: {entity: id}
  fields: {id: {datatype: String}}
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
spec: {source: orders, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}
`,
		"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:sales, name: sales}
spec: {datasets: {orders: {model: orders_model}}, metrics: {}}
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
spec: {sources: [orders], transform: {sql: 'SELECT * FROM source.customers'}, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}
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
spec: {sources: [orders], transform: {sql: 'SELECT * FROM source.orders JOIN model.customers_model USING (id)'}, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}
`
		files["models/customers.yaml"] = `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:customers, name: customers_model}
spec: {sources: [customers], transform: {sql: 'SELECT * FROM source.customers JOIN model.orders_model USING (id)'}, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}
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
spec: {source: orders, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}
`,
		"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:sales, name: sales}
spec: {datasets: {orders: {model: orders_model}}, metrics: {order_count: {type: aggregate, dataset: orders, aggregation: count, input: {field: orders.id}, empty: zero}}}
`,
		"dashboards/sales.yaml": `apiVersion: leapview.dev/v1
kind: Dashboard
metadata: {id: dashboard:sales, name: sales_dashboard, displayName: Sales}
spec:
  title: Sales
  semanticModel: sales
  visuals: {order_count: {type: kpi, query: {metrics: {order_count: null}}}}
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
spec: {source: orders, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}
`,
		"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic:sales, name: sales}
spec: {datasets: {orders: {model: orders_model}}, metrics: {order_count: {type: aggregate, dataset: orders, aggregation: count, input: {field: orders.id}, empty: zero}}}
`,
		"dashboards/sales.yaml": `apiVersion: leapview.dev/v1
kind: Dashboard
metadata: {id: dashboard:sales, name: sales_dashboard, displayName: Sales}
spec:
  title: Sales
  semanticModel: sales
  visuals: {order_count: {type: kpi, query: {metrics: {order_count: null}}}}
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
