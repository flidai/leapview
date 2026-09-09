package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	analyticsduckdb "github.com/flidai/leapview/internal/analytics/duckdb"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/app/testing/extensionfixture"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardcompiler "github.com/flidai/leapview/internal/dashboard/compiler"
	dashboarddocument "github.com/flidai/leapview/internal/dashboard/document"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	configschema "github.com/flidai/leapview/internal/project/schema"
	"github.com/flidai/leapview/internal/workload"
	"github.com/google/go-cmp/cmp"
)

func TestExportDashboardConvertsCanonicalResourceIDs(t *testing.T) {
	metric := "order_count"
	document := dashboarddocument.DashboardDocument{
		APIVersion: dashboarddocument.DashboardApiVersionLeapviewDevV1,
		Kind:       dashboarddocument.DashboardResourceKindDashboard,
		Metadata:   dashboarddocument.DashboardMetadata{ID: "dashboard_sales", Name: "sales_dashboard"},
		Spec: dashboarddocument.DashboardSpec{
			SemanticModel: "semantic_sales", Filters: []dashboarddocument.DashboardFilter{},
			Visuals: map[string]dashboarddocument.DashboardVisual{"total": {
				Type:         dashboarddocument.DashboardVisualTypeKpi,
				Query:        dashboarddocument.DashboardQuery{Value: &dashboarddocument.AggregateDashboardQuery{Type: "aggregate", Dimensions: []dashboarddocument.DashboardDimensionSelection{}, Metrics: []dashboarddocument.DashboardMetricSelection{{String: &metric}}}},
				Presentation: dashboarddocument.DashboardPresentation{Value: &dashboarddocument.KPIDashboardPresentation{Type: "kpi"}},
			}},
			Pages: []dashboarddocument.DashboardPage{{ID: "overview", Title: "Overview", Components: []dashboarddocument.DashboardPageComponent{}}},
		},
	}
	encoded, err := ExportDashboard(document)
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
spec: {definition: {type: direct, source: orders}, entities: {order: {type: primary, fields: [order_id]}}, grain: {entity: order}, fields: {order_id: {datatype: String}}}
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
  semanticModel: sales
  filters: []
  visuals:
    order_count:
      type: kpi
      query: {type: aggregate, dimensions: [], metrics: [order_count]}
      presentation: {type: kpi}
  pages: [{id: overview, title: Overview, components: []}]
`,
	}
	projectPath := writeSourceFixture(t, files)
	compiled, err := LoadSourceRoot(projectPath)
	if err != nil {
		t.Fatalf("LoadSourceRoot() error = %v", err)
	}
	source, ok := compiled.Manifest.DashboardSources["dashboard:sales"]
	if !ok {
		t.Fatal("compiled manifest omitted dashboard source")
	}
	if source.Metadata.Domain != "revenue" {
		t.Fatalf("compiled dashboard source domain = %q, want revenue", source.Metadata.Domain)
	}
	configuration := compiled.Manifest.AuthoredResourceSources["dashboard:sales"]
	if json.Valid([]byte(configuration)) || !strings.Contains(configuration, "kind: Dashboard") || !strings.Contains(configuration, "semanticModel: sales") {
		t.Fatalf("manifest canonical dashboard source = %q, want expanded authored Dashboard YAML", configuration)
	}
	displayName := source.Metadata.Title
	domain := source.Metadata.Domain
	encoded, err := ExportDashboard(dashboarddocument.DashboardDocument{
		APIVersion: dashboarddocument.DashboardApiVersionLeapviewDevV1,
		Kind:       dashboarddocument.DashboardResourceKindDashboard,
		Metadata: dashboarddocument.DashboardMetadata{
			ID: "dashboard:sales", Name: source.Metadata.Name, DisplayName: &displayName, Domain: &domain,
		},
		Spec: dashboarddocument.DashboardSpec{SemanticModel: "sales", Filters: []dashboarddocument.DashboardFilter{}, Visuals: map[string]dashboarddocument.DashboardVisual{}, Pages: []dashboarddocument.DashboardPage{{ID: "overview", Title: "Overview", Components: []dashboarddocument.DashboardPageComponent{}}}},
	})
	if err != nil {
		t.Fatalf("ExportDashboard() error = %v", err)
	}
	if !strings.Contains(string(encoded), "domain: revenue") {
		t.Fatalf("canonical dashboard export omitted authored domain: %s", encoded)
	}
}

func TestAIContextIsPreservedWithoutChangingExecutableSemantics(t *testing.T) {
	load := func(t *testing.T, withContext bool) *sourceAssembly {
		t.Helper()
		modelContext := ""
		semanticContext := ""
		if withContext {
			modelContext = "aiContext:\n  instructions: Use order line identity when answering questions.\n  synonyms: [order line]\n  examples: [show revenue by line]\n"
			semanticContext = "aiContext:\n  instructions: Prefer the sales grain.\n  synonyms: [sales]\n  examples: [compare revenue]\n"
		}
		files := map[string]string{
			"orders.csv": "order_id,line_number,revenue,activity_date\nsample,1,10.50,2026-01-03\nsample,2,5.25,2026-01-14\nother,1,99.00,2026-01-20\n",
			"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {type: managed}
`,
			"sources/orders_source.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:orders_source, name: orders_source}
spec: {connection: warehouse, location: {type: path, path: orders.csv, format: csv, options: {header: true}}}
`,
			"models/orders.yaml": "apiVersion: leapview.dev/v1\nkind: Model\nmetadata: {id: model:orders, name: orders}\n" + modelContext + `spec:
  definition: {type: direct, source: orders_source}
  entities:
    order_line: {type: primary, fields: [order_id, line_number]}
  grain: {entity: order_line}
  fields:
    order_id: {datatype: String}
    line_number: {datatype: Integer}
    revenue: {datatype: Float}
    activity_date: {datatype: Date}
`,
			"models/customers.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:customers, name: customers}
spec:
  definition: {type: direct, source: orders_source}
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
		projectPath := writeSourceFixture(t, files)
		project := mustLoadSourceAssembly(t, projectPath)
		// Managed source paths are relative to the active revision root. Keep
		// both projects pointed at their own identical fixture data.
		for name, model := range project.Manifest.SemanticModels {
			connection := model.Connections["warehouse"]
			connection.Root = projectPath
			model.Connections["warehouse"] = connection
			project.Manifest.SemanticModels[name] = model
		}
		return project
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
		copy.Connections = maps.Clone(model.Connections)
		for name, connection := range copy.Connections {
			// Each fixture is compiled in a distinct temporary directory. The
			// managed connection root is runtime location, not executable
			// semantic meaning, so exclude it from the AI-context equivalence.
			connection.Root = ""
			copy.Connections[name] = connection
		}
		copy.Relationships = append([]semanticmodel.Relationship(nil), model.Relationships...)
		for index := range copy.Relationships {
			copy.Relationships[index].AIContext = nil
		}
		copy.Tables = make(map[string]semanticmodel.Table, len(model.Tables))
		for name, original := range model.Tables {
			table := original
			table.AIContext = nil
			table.AuthoredFields = maps.Clone(original.AuthoredFields)
			for field, declaration := range table.AuthoredFields {
				declaration.AIContext = nil
				table.AuthoredFields[field] = declaration
			}
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

	// Exercise the complete governed path against identical fixture data. The
	// result comparison intentionally ignores timing and cache metadata; rows,
	// columns, status, and the generated SQL are the executable contract.
	execute := func(model *semanticmodel.Model) dataquery.Result {
		ctx := context.Background()
		dir := t.TempDir()
		admission := extensionfixture.New(t, "ducklake")
		environment, err := analyticsducklake.Open(ctx, analyticsducklake.Config{RootDir: filepath.Join(dir, "ducklake"), MaxConnections: 2, ExtensionAdmission: admission.Admission})
		if err != nil {
			t.Fatalf("open DuckLake fixture environment: %v", err)
		}
		controller, err := workload.New(workload.DefaultConfig())
		if err != nil {
			_ = environment.Close()
			t.Fatalf("open fixture workload controller: %v", err)
		}
		lease, err := controller.Acquire(ctx, workload.Request{Class: workload.Refresh, PrincipalID: "alice", Operation: "ai-context-qualification", EstimatedMemoryBytes: 1})
		if err != nil {
			controller.Close()
			_ = environment.Close()
			t.Fatalf("admit fixture refresh: %v", err)
		}
		runtime, err := analyticsduckdb.OpenProjectMaterializeRuntime(lease.Context(), analyticsduckdb.ProjectRuntimeConfig{
			ProjectID: "project:test", Models: map[string]*semanticmodel.Model{"semantic:sales": model}, Database: environment,
			ExtensionAdmission: admission.Admission,
		})
		if err != nil {
			lease.Release()
			controller.Close()
			_ = environment.Close()
			t.Fatalf("open governed fixture runtime: %v", err)
		}
		t.Cleanup(func() {
			_ = runtime.Close()
			lease.Release()
			controller.Close()
			_ = environment.Close()
		})
		request := dataquery.SemanticAggregate("semantic:sales", "orders", []dataquery.Field{{Field: "activity_date"}}, []dataquery.Field{{Field: "revenue"}}, nil, nil, 0, 0)
		request.ProjectID = "project:test"
		request.Surface = dataquery.SurfaceDashboard
		request.Operation = dataquery.OperationDashboardAggregate
		request.PrincipalID = "alice"
		request.ObjectType = "semantic_model"
		request.ObjectID = "semantic:sales"
		result, err := runtime.ExecuteDataQuery(dataquery.WithGovernor(lease.Context(), aiContextQualificationGovernor{}), request)
		if err != nil {
			t.Fatalf("execute governed fixture query: %v", err)
		}
		return result
	}
	withoutResult := execute(withoutModel)
	withResult := execute(withModel)
	if !reflect.DeepEqual(withoutResult.Columns, withResult.Columns) || !reflect.DeepEqual(withoutResult.Rows, withResult.Rows) || withoutResult.TotalRows != withResult.TotalRows || withoutResult.TotalRowsKnown != withResult.TotalRowsKnown || withoutResult.SQL != withResult.SQL || withoutResult.Status != withResult.Status || withoutResult.ExecutionState != withResult.ExecutionState {
		t.Fatalf("AI context changed governed query result:\n%s", cmp.Diff(withoutResult, withResult))
	}
}

type aiContextQualificationGovernor struct{}

func (aiContextQualificationGovernor) GovernDataQuery(_ context.Context, request dataquery.Query) (dataquery.Query, dataquery.ResultTransformer, error) {
	request.EffectivePolicyFingerprint = "fixture-policy"
	return request, nil, nil
}

func mustLoadSourceAssembly(t *testing.T, path string) *sourceAssembly {
	t.Helper()
	project, err := LoadSourceRoot(path)
	if err != nil {
		t.Fatalf("LoadSourceRoot(%q): %v", path, err)
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
		{ID: "model:a", Kind: projectgraph.KindModel, Name: "a"},
		{ID: "model:b", Kind: projectgraph.KindModel, Name: "b"},
	}
	_, err := projectgraph.NewProjectGraph(resources, []projectgraph.Edge{{From: "model:a", To: "model:b", Relation: "uses_model"}, {From: "model:b", To: "model:a", Relation: "uses_model"}})
	if !errors.Is(err, projectgraph.ErrCycle) {
		t.Fatalf("NewProjectGraph() error = %v, want cycle", err)
	}
}

func TestProjectGraphCanonicalBytesStableAcrossTraversalOrder(t *testing.T) {
	resources := []projectgraph.Resource{{ID: "source:z", Kind: projectgraph.KindSource, Name: "z"}, {ID: "connection:a", Kind: projectgraph.KindConnection, Name: "a"}}
	edges := []projectgraph.Edge{{From: "source:z", To: "connection:a", Relation: "uses_connection"}}
	first, err := projectgraph.NewProjectGraph(resources, edges)
	if err != nil {
		t.Fatal(err)
	}
	second, err := projectgraph.NewProjectGraph([]projectgraph.Resource{resources[1], resources[0]}, []projectgraph.Edge{edges[0]})
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
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {Model: "orders"},
			"daily":  {Model: "daily"},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {Execution: semanticmodel.ExecutionDefinition{Source: "orders"}, Entities: map[string]semanticmodel.EntityDefinition{"id": {Type: "primary", Fields: []string{"id"}}}, GrainEntity: "id", Dimensions: map[string]semanticmodel.MetricDimension{"id": {Datatype: semanticmodel.DataTypeString, Type: "string"}}},
			"daily":  {Execution: semanticmodel.ExecutionDefinition{SQL: "-- source.orders\nWITH q AS (SELECT * FROM source.orders) SELECT * FROM q JOIN model.orders ON q.id = model.orders.id"}, Entities: map[string]semanticmodel.EntityDefinition{"id": {Type: "primary", Fields: []string{"id"}}}, GrainEntity: "id", Dimensions: map[string]semanticmodel.MetricDimension{"id": {Datatype: semanticmodel.DataTypeString, Type: "string"}}},
		},
	}
	if err := deriveModelSQLDependencies(model); err != nil {
		t.Fatalf("deriveModelSQLDependencies() error = %v", err)
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

func TestSemanticModelAliasesPreservePhysicalTransformDependencies(t *testing.T) {
	newModel := func(source string) semanticmodel.Table {
		return semanticmodel.Table{
			Execution:   semanticmodel.ExecutionDefinition{Source: source},
			Entities:    map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"order_id"}}},
			GrainEntity: "order",
			Dimensions:  map[string]semanticmodel.MetricDimension{"order_id": {Datatype: semanticmodel.DataTypeString}},
		}
	}
	base := newModel("orders")
	derived := newModel("")
	derived.Execution.SQL = "SELECT base.order_id FROM model.base_model AS base JOIN source.orders AS raw ON raw.order_id = base.order_id"
	project := sourceAssembly{
		Connections:   map[string]semanticmodel.Connection{"warehouse": {Kind: "managed"}},
		ConnectionIDs: map[string]string{"warehouse": "connection:warehouse"},
		Sources:       map[string]semanticmodel.Source{"orders": {Connection: "warehouse", Format: "csv", Path: "orders.csv"}},
		SourceIDs:     map[string]string{"orders": "source:orders"},
		Models:        map[string]semanticmodel.Table{"base_model": base, "derived_model": derived},
		ModelIDs:      map[string]string{"base_model": "model:base", "derived_model": "model:derived"},
		SemanticModels: map[string]projectcontracts.SemanticModelSpec{"sales": {Datasets: map[string]projectcontracts.SemanticDataset{
			"base_alias": {Model: "base_model"}, "derived_alias": {Model: "derived_model"},
		}}},
		SemanticModelIDs: map[string]string{"sales": "semantic:sales"},
	}
	manifest, err := buildResourceManifest(project)
	if err != nil {
		t.Fatalf("buildResourceManifest() error = %v", err)
	}
	semanticModel := manifest.SemanticModels["semantic:sales"]
	if semanticModel == nil {
		t.Fatal("compiled semantic model is missing")
	}
	if got, want := semanticModel.Tables["derived_alias"].ModelDependencies, []string{"base_model"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("derived alias dependencies = %#v, want %#v", got, want)
	}
	invalid := project
	invalid.Models = map[string]semanticmodel.Table{}
	for name, table := range project.Models {
		invalid.Models[name] = table
	}
	derived = invalid.Models["derived_model"]
	derived.Execution.SQL = strings.Replace(derived.Execution.SQL, "model.base_model", "model.base_alias", 1)
	invalid.Models["derived_model"] = derived
	if _, err := buildResourceManifest(invalid); err == nil || !strings.Contains(err.Error(), `unknown Model "base_alias"`) {
		t.Fatalf("dataset alias in transform dependency error = %v, want unknown physical model", err)
	}
}

func TestSourceRootAllowsModelOnlyTransform(t *testing.T) {
	projectPath := writeSourceFixture(t, map[string]string{
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
spec:
  definition: {type: sql, sql: SELECT order_id FROM source.orders}
  entities: {order: {type: primary, fields: [order_id]}}
  grain: {entity: order}
  fields: {order_id: {datatype: String}}
`,
		"models/order_labels.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:order_labels, name: order_labels}
spec:
  definition:
    type: sql
    sql: SELECT order_id FROM model.orders_model
  entities: {order: {type: primary, fields: [order_id]}}
  grain: {entity: order}
  fields: {order_id: {datatype: String}}
`,
	})
	project, err := LoadSourceRoot(projectPath)
	if err != nil {
		t.Fatalf("LoadSourceRoot() model-only transform: %v", err)
	}
	derived, ok := project.Models["order_labels"]
	if !ok {
		t.Fatal("model-only table is missing")
	}
	if len(derived.SourceDependencies) != 0 {
		t.Fatalf("model-only source dependencies = %#v, want none", derived.SourceDependencies)
	}
	if !reflect.DeepEqual(derived.ModelDependencies, []string{"orders_model"}) {
		t.Fatalf("model-only model dependencies = %#v, want [orders_model]", derived.ModelDependencies)
	}
	foundEdge := false
	for _, edge := range project.Graph.Edges() {
		if edge.From == "model:order_labels" && edge.To == "model:orders" && edge.Relation == "uses_model" {
			foundEdge = true
			break
		}
	}
	if !foundEdge {
		t.Fatalf("project graph edges = %#v, want model-only uses_model edge", project.Graph.Edges())
	}
}

func TestSourceRootRejectsTopLevelModelSQLAlias(t *testing.T) {
	projectPath := writeSourceFixture(t, map[string]string{
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
metadata: {id: model:orders, name: orders}
spec:
  definition: {type: sql, sql: SELECT order_id FROM source.orders}
  legacySql: SELECT order_id FROM source.orders
  entities: {order: {type: primary, fields: [order_id]}}
  grain: {entity: order}
  fields: {order_id: {datatype: String}}
`,
	})
	if _, err := LoadSourceRoot(projectPath); err == nil || !strings.Contains(err.Error(), "legacySql") {
		t.Fatalf("LoadSourceRoot() accepted removed top-level Model sql alias: %v", err)
	}
}

func TestSourceRootPreservesStableIDForPunctuatedSourceName(t *testing.T) {
	projectPath := writeSourceFixture(t, map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {type: managed}
`,
		"sources/foo-bar.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:foo-bar, name: foo-bar}
spec: {connection: warehouse, location: {type: path, path: foo-bar.csv, format: csv}}
`,
		"models/orders.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders}
spec: {definition: {type: direct, source: foo-bar}, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}
`,
	})
	project, err := LoadSourceRoot(projectPath)
	if err != nil {
		t.Fatalf("LoadSourceRoot() error = %v", err)
	}
	resource, ok := project.Graph.Resource("source:foo-bar")
	if !ok || resource.Kind != projectgraph.KindSource || resource.Name != "foo-bar" {
		t.Fatalf("source graph resource = %#v, present=%v; want source:foo-bar/foo-bar", resource, ok)
	}
	if got := project.Manifest.Models["model:orders"].Execution.Source; got != "source:foo-bar" {
		t.Fatalf("manifest model source = %q, want stable source ID", got)
	}
}

func TestSourceRootRejectsCollidingSourceAliases(t *testing.T) {
	projectPath := writeSourceFixture(t, map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {type: managed}
`,
		"sources/foo-bar.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:foo-bar, name: foo-bar}
spec: {connection: warehouse, location: {type: path, path: foo-bar.csv, format: csv}}
`,
		"sources/foo_bar.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:foo_bar, name: foo_bar}
spec: {connection: warehouse, location: {type: path, path: foo_bar.csv, format: csv}}
`,
		"models/orders.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders}
spec: {definition: {type: direct, source: foo-bar}, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}
`,
	})
	_, err := LoadSourceRoot(projectPath)
	if err == nil {
		t.Fatal("LoadSourceRoot() accepted colliding source aliases")
	}
	for _, want := range []string{"foo-bar", "foo_bar", "source:foo-bar", "source:foo_bar", "runtime source alias"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("LoadSourceRoot() error = %v, want %q", err, want)
		}
	}
}

func TestCompileGraphResolvesStableIDsAndProvenance(t *testing.T) {
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
	write("connections/warehouse.yaml", `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: conn:warehouse, name: warehouse}
spec: {type: managed}
`)
	write("sources/orders.yaml", `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:orders, name: orders}
spec:
  connection: warehouse
  location: {type: path, path: orders.csv, format: csv}
`)
	write("models/orders.yaml", `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders_model}
spec:
  definition: {type: direct, source: orders}
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
spec: {selection: {semanticModel: sales}}
`)
	write("dashboards/sales.yaml", `apiVersion: leapview.dev/v1
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
`)

	compiled, err := CompileGraph(root)
	if err != nil {
		t.Fatal(err)
	}
	resources := compiled.Resources()
	if len(resources) != 6 {
		t.Fatalf("resource count = %d, want 6", len(resources))
	}
	for _, resource := range resources {
		if resource.Provenance.Path == "" || filepath.IsAbs(resource.Provenance.Path) {
			t.Fatalf("resource %q provenance = %#v", resource.ID, resource.Provenance)
		}
		if resource.Provenance.Origin != "source" {
			t.Fatalf("resource %q provenance origin = %q, want source", resource.ID, resource.Provenance.Origin)
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

func TestCompileGraphShowcase(t *testing.T) {
	// The repository also keeps the independent MovieLens example below
	// dashboards/experiments. Compile the showcase's six owned directories as
	// one source root so that sibling examples are not treated as resources.
	fixtureRoot := filepath.Join("..", "..", "..", "dashboards")
	sourceRoot := t.TempDir()
	for _, directory := range []string{"connections", "sources", "models", "semantic-models", "pipelines", "dashboards"} {
		from := filepath.Join(fixtureRoot, directory)
		to := filepath.Join(sourceRoot, directory)
		err := filepath.WalkDir(from, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(from, path)
			if err != nil {
				return err
			}
			target := filepath.Join(to, relative)
			if entry.IsDir() {
				return os.MkdirAll(target, 0o755)
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.WriteFile(target, content, 0o644)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	project, err := LoadSourceRoot(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	graph := project.Graph
	if len(graph.Resources()) < 10 {
		t.Fatalf("resource count = %d", len(graph.Resources()))
	}
	wantKinds := map[projectgraph.Kind]bool{
		projectgraph.KindConnection: true, projectgraph.KindSource: true,
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
	showcase := project.Manifest.DashboardDefinitions["dashboard:visual-showcase"]
	wantHeatmapTargets := []string{
		"chart-heatmap/category-status-heatmap",
		"chart-heatmap/category-status-heatmap-labels",
		"chart-heatmap/state-status-heatmap",
	}
	for _, filterID := range []string{"purchase_date", "state"} {
		binding, ok := showcase.FilterBindings[filterID]
		if !ok {
			t.Fatalf("compiled showcase omitted %s filter binding", filterID)
		}
		got := make([]string, 0, len(wantHeatmapTargets))
		for _, target := range binding.Targets {
			if strings.HasPrefix(target, "chart-heatmap/") {
				got = append(got, target)
			}
		}
		if !reflect.DeepEqual(got, wantHeatmapTargets) {
			t.Fatalf("compiled %s heatmap targets = %#v, want %#v", filterID, got, wantHeatmapTargets)
		}
	}
	categories, ok := showcase.Visualizations["categories"]
	if !ok {
		t.Fatal("compiled showcase omitted categories visual")
	}
	base, err := visualizationir.SpecificationBase(categories.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := base.Datasets[0].Fields[0].Role; got != visualizationir.VisualizationFieldRoleIdentity {
		t.Fatalf("compiled categories selection source role = %q, want identity", got)
	}
	envelope, err := visualizationruntime.EnvelopeFromFrame(categories, visualizationruntime.Frame{
		Columns: []string{"category", "revenue"}, Rows: [][]any{{"books", "10"}},
	}, []dashboard.InteractionSelectionEntry{{Mappings: []dashboard.InteractionSelectionMapping{{Field: "category", Value: "books"}}}}, 1, 1)
	if err != nil {
		t.Fatalf("project selected categories envelope: %v", err)
	}
	if len(envelope.Selection) != 1 || envelope.Selection[0].Datum.Identity["category"] != "books" {
		t.Fatalf("project selected categories envelope selection = %#v", envelope.Selection)
	}
	formattedMatrix, ok := showcase.Visualizations["state_status_matrix_formatted"]
	if !ok {
		t.Fatal("compiled showcase omitted formatted matrix visual")
	}
	formattedMatrixBase, err := visualizationir.SpecificationBase(formattedMatrix.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := formattedMatrixBase.DataBudget.MaxRows; got != 5000 {
		t.Fatalf("compiled formatted matrix maxRows = %d, want 5000", got)
	}
}

func TestCompileProjectGraphExecutiveSalesFilterControls(t *testing.T) {
	project, err := LoadSourceRoot(filepath.Join("..", "..", "..", "dashboards"))
	if err != nil {
		t.Fatal(err)
	}
	dashboard, ok := project.Manifest.DashboardDefinitions["dashboard:executive-sales"]
	if !ok {
		t.Fatal("compiled project omitted Executive Sales dashboard")
	}
	definition, ok := dashboard.FilterDefinitions["state"]
	if !ok {
		t.Fatal("compiled Executive Sales omitted state filter definition")
	}
	if definition.Field != "state" || definition.Dataset != "sales_orders" {
		t.Fatalf("compiled state filter field/dataset = %q/%q, want state/sales_orders", definition.Field, definition.Dataset)
	}
	if definition.Options.Kind != dashboardfilter.OptionSourceDistinct || definition.Options.Limit != 50 {
		t.Fatalf("compiled state filter options = %#v, want distinct limit 50", definition.Options)
	}
	binding, ok := dashboard.FilterBindings["state"]
	if !ok {
		t.Fatal("compiled Executive Sales omitted state filter binding")
	}
	if binding.Selection.Mode != dashboardfilter.SelectionMultiple || binding.Selection.MaxSelectedValues != 50 {
		t.Fatalf("compiled state filter selection = %#v, want multi-select limit 50", binding.Selection)
	}
	purchaseDate, ok := dashboard.FilterDefinitions["purchase_date"]
	if !ok {
		t.Fatal("compiled Executive Sales omitted purchase_date filter definition")
	}
	if purchaseDate.Field != "purchase_date" || purchaseDate.ValueKind != dashboardfilter.ValueDate {
		t.Fatalf("compiled purchase_date field/kind = %q/%q, want purchase_date/date", purchaseDate.Field, purchaseDate.ValueKind)
	}
	if len(purchaseDate.Predicates) != 1 || purchaseDate.Predicates[0].Kind != dashboardfilter.ExpressionRange {
		t.Fatalf("compiled purchase_date predicates = %#v, want one range predicate", purchaseDate.Predicates)
	}
	purchaseBinding, ok := dashboard.FilterBindings["purchase_date"]
	if !ok || purchaseBinding.Default.Kind != dashboardfilter.ExpressionUnfiltered {
		t.Fatalf("compiled purchase_date default = %#v, want unfiltered", purchaseBinding.Default)
	}
	dateRangeSlicer := false
	for _, page := range dashboard.Pages {
		for _, visual := range page.Visuals {
			if visual.ID == "purchase-date-filter" && visual.Kind == "slicer" && visual.Presentation.Style == dashboardfilter.PresentationDateRange {
				dateRangeSlicer = true
			}
		}
	}
	if !dateRangeSlicer {
		t.Fatal("compiled Executive Sales purchase_date slicer is not a date range")
	}
}

func TestCompileGraphAcceptsCanonicalReferenceIDs(t *testing.T) {
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
	write("connections/c.yaml", "apiVersion: leapview.dev/v1\nkind: Connection\nmetadata: {id: connection:id, name: warehouse}\nspec: {type: managed}\n")
	write("sources/s.yaml", "apiVersion: leapview.dev/v1\nkind: Source\nmetadata: {id: source:id, name: orders}\nspec: {connection: warehouse, location: {type: path, path: orders.csv, format: csv}}\n")
	write("models/m.yaml", "apiVersion: leapview.dev/v1\nkind: Model\nmetadata: {id: model:id, name: orders_model}\nspec: {definition: {type: direct, source: source:id}, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}\n")
	write("semantic-models/s.yaml", "apiVersion: leapview.dev/v1\nkind: SemanticModel\nmetadata: {id: semantic-model:id, name: sales}\nspec: {datasets: {orders: {model: orders_model}}, metrics: {count: {type: aggregate, dataset: orders, aggregation: count, input: {field: orders.id}, empty: zero}}}\n")
	graph, err := CompileGraph(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Edges()) != 3 {
		t.Fatalf("edges = %d, want 3", len(graph.Edges()))
	}
}

func TestSourceRootAllowsTwoSemanticConsumersOfOneModel(t *testing.T) {
	projectPath := writeSourceFixture(t, map[string]string{
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
spec:
  definition: {type: direct, source: orders}
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
	project, err := LoadSourceRoot(projectPath)
	if err != nil {
		t.Fatalf("LoadSourceRoot() error = %v", err)
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

func TestSourceRootRejectsDuplicateStableIDsAcrossKinds(t *testing.T) {
	projectPath := writeSourceFixture(t, map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: resource:duplicate, name: warehouse}
spec: {type: managed}
`,
		"models/orders.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: resource:duplicate, name: orders}
spec: {definition: {type: direct, source: orders}, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}
`,
	})
	_, err := LoadSourceRoot(projectPath)
	if err == nil || !strings.Contains(err.Error(), "duplicates resource") {
		t.Fatalf("LoadSourceRoot() error = %v, want duplicate stable ID", err)
	}
	diagnostics := configschema.Diagnostics(err)
	if len(diagnostics) == 0 || diagnostics[0].ResourceID != "resource:duplicate" || !strings.HasSuffix(filepath.ToSlash(diagnostics[0].File), "models/orders.yaml") {
		t.Fatalf("diagnostics = %#v, want model path and stable ID", diagnostics)
	}
}

func TestSourceRootWrongReferenceReportsResourcePathAndField(t *testing.T) {
	projectPath := writeSourceFixture(t, map[string]string{
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
spec: {datasets: {orders: {model: orders}}, metrics: {}}
`,
	})
	_, err := LoadSourceRoot(projectPath)
	if err == nil || !strings.Contains(err.Error(), `resolves to source, want model`) {
		t.Fatalf("LoadSourceRoot() error = %v, want wrong-kind reference", err)
	}
	diagnostics := configschema.Diagnostics(err)
	if len(diagnostics) == 0 || diagnostics[0].ResourceID != "semantic:sales" || diagnostics[0].FieldPath != "spec.datasets.orders.model" || !strings.HasSuffix(filepath.ToSlash(diagnostics[0].File), "semantic-models/sales.yaml") {
		t.Fatalf("diagnostics = %#v, want semantic model path/id/field", diagnostics)
	}
}

func TestSourceRootManifestIsCheckoutIndependentAndCanonical(t *testing.T) {
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
spec: {datasets: {orders: {model: orders_model}}, metrics: {}}
`,
	}
	first, err := LoadSourceRoot(writeSourceFixture(t, files))
	if err != nil {
		t.Fatalf("LoadSourceRoot(first) error = %v", err)
	}
	second, err := LoadSourceRoot(writeSourceFixture(t, files))
	if err != nil {
		t.Fatalf("LoadSourceRoot(second) error = %v", err)
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
	if got := first.Manifest.AuthoredModelSources["model:orders"]; got != files["models/orders.yaml"] {
		t.Fatalf("manifest authored model source = %q, want exact YAML", got)
	}
	for id, path := range map[string]string{
		"source:orders":  "sources/orders.yaml",
		"model:orders":   "models/orders.yaml",
		"semantic:sales": "semantic-models/sales.yaml",
	} {
		if got := first.Manifest.AuthoredResourceSources[id]; got != files[path] {
			t.Fatalf("manifest authored resource source %s = %q, want exact YAML from %s", id, got, path)
		}
	}
	if _, ok := first.Manifest.AuthoredResourceSources["connection:warehouse"]; ok {
		t.Fatal("manifest retained raw connection YAML; connection definitions must be projected through the redacted read model")
	}
	if got := first.Manifest.NameIndex.Models["orders_model"]; got != "model:orders" {
		t.Fatalf("manifest model name index = %q, want stable ID", got)
	}
}

func TestLoadSourceRootRejectsSymlinkEscapingResource(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideConnection := filepath.Join(outside, "warehouse.yaml")
	if err := os.WriteFile(outsideConnection, []byte(`apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {type: managed}
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
	_, err := LoadSourceRoot(root)
	if err == nil || !strings.Contains(err.Error(), "resolves outside source root") || !strings.Contains(err.Error(), "connections/warehouse.yaml") {
		t.Fatalf("LoadSourceRoot() error = %v, want source-relative symlink diagnostic", err)
	}
	if strings.Contains(err.Error(), root) || strings.Contains(err.Error(), outside) {
		t.Fatalf("symlink diagnostic leaked absolute checkout path: %v", err)
	}
}

func TestProjectRelativePathNeverFallsBackToAbsolute(t *testing.T) {
	project := sourceAssembly{BaseDir: t.TempDir()}
	path := filepath.Join(t.TempDir(), "outside.yaml")
	if got := projectRelativePath(&project, path); filepath.IsAbs(got) {
		t.Fatalf("projectRelativePath() returned absolute fallback %q", got)
	}
}

func TestSourceRootRejectsTargetOwnedConnectionCredentials(t *testing.T) {
	projectPath := writeSourceFixture(t, map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec:
  type: managed
  credentials: {provider: env, secret: LEAPVIEW_WAREHOUSE_CREDENTIALS}
`,
	})
	_, err := LoadSourceRoot(projectPath)
	if err == nil || (!strings.Contains(err.Error(), "target-owned") && !strings.Contains(err.Error(), "credentials")) {
		t.Fatalf("LoadSourceRoot() error = %v, want target-owned credential diagnostic", err)
	}
	diagnostics := configschema.Diagnostics(err)
	if len(diagnostics) == 0 || diagnostics[0].ResourceID != "connection:warehouse" || diagnostics[0].FieldPath != "spec.credentials" {
		t.Fatalf("diagnostics = %#v, want connection resource and spec.credentials", diagnostics)
	}
}

func TestSourceRootRejectsHiddenSQLImportsAndUnsafeIncludes(t *testing.T) {
	projectPath := writeSourceFixture(t, map[string]string{
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
spec:
  definition: {type: sql, sql: 'SELECT * FROM raw.orders'}
  entities: {id: {type: primary, fields: [id]}}
  grain: {entity: id}
  fields: {id: {datatype: String}}
`,
	})
	if _, err := LoadSourceRoot(projectPath); err == nil || (!strings.Contains(err.Error(), `relation schema "raw" is not governed`) && !strings.Contains(err.Error(), "raw namespace relations are not allowed") && !strings.Contains(err.Error(), "raw.<name> is internal")) {
		t.Fatalf("LoadSourceRoot() accepted hidden raw import: %v", err)
	}
}

func TestSourceRootRefreshPipelinesValidateAndNormalize(t *testing.T) {
	base := map[string]string{
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
spec: {datasets: {orders: {model: orders_model}}, metrics: {}}
`,
	}
	validFiles := cloneFixtureFiles(base)
	validFiles["pipelines/sales-refresh.yaml"] = `apiVersion: leapview.dev/v1
kind: Pipeline
metadata: {id: pipeline:sales-refresh, name: sales_refresh}
spec:
  selection: {semanticModel: sales}
  schedules: {"weekdays 06:00": "0 6 * * *"}
  timezone: Europe/Copenhagen
  startingDeadlineSeconds: 3600
  concurrencyPolicy: Replace
`
	projectPath := writeSourceFixture(t, validFiles)
	project, err := LoadSourceRoot(projectPath)
	if err != nil {
		t.Fatalf("LoadSourceRoot(valid pipeline) error = %v", err)
	}
	pipeline := project.RefreshPipelines["sales_refresh"]
	if pipeline.ID != "pipeline:sales-refresh" || pipeline.SemanticModelID != "sales" || pipeline.SelectionDigest != authoredPipelineSelectionDigest("sales") || pipeline.Timezone != "Europe/Copenhagen" || pipeline.StartingDeadlineSeconds != 3600 || pipeline.ConcurrencyPolicy != "Replace" || len(pipeline.Schedules) != 1 || pipeline.Schedules[0].ID != "weekdays 06:00" || pipeline.Schedules[0].Expression != "0 6 * * *" {
		t.Fatalf("pipeline = %#v, want normalized schedule", pipeline)
	}
	manualFiles := cloneFixtureFiles(base)
	manualFiles["pipelines/manual.yaml"] = `apiVersion: leapview.dev/v1
kind: Pipeline
metadata: {id: pipeline:manual, name: manual}
spec: {selection: {semanticModel: sales}}
`
	manual, err := LoadSourceRoot(writeSourceFixture(t, manualFiles))
	if err != nil {
		t.Fatalf("LoadSourceRoot(manual pipeline) error = %v", err)
	}
	if got := len(manual.RefreshPipelines["manual"].Schedules); got != 0 {
		t.Fatalf("manual pipeline schedules = %d, want 0", got)
	}
	invalidFiles := cloneFixtureFiles(base)
	invalidFiles["pipelines/bad.yaml"] = `apiVersion: leapview.dev/v1
kind: Pipeline
metadata: {id: pipeline:bad, name: bad}
spec: {selection: {semanticModel: missing}}
`
	_, err = LoadSourceRoot(writeSourceFixture(t, invalidFiles))
	if err == nil || !strings.Contains(err.Error(), `unknown authored SemanticModel name "missing"`) {
		t.Fatalf("LoadSourceRoot(invalid pipeline) error = %v, want missing semantic model", err)
	}
	canonicalIDFiles := cloneFixtureFiles(base)
	canonicalIDFiles["pipelines/canonical-id.yaml"] = `apiVersion: leapview.dev/v1
kind: Pipeline
metadata: {id: pipeline:canonical-id, name: canonical_id}
spec: {selection: {semanticModel: semantic-model:sales}}
`
	_, err = LoadSourceRoot(writeSourceFixture(t, canonicalIDFiles))
	if err == nil || !strings.Contains(err.Error(), "spec.selection.semanticModel") {
		t.Fatalf("LoadSourceRoot(canonical semantic model ID) error = %v, want authored-name validation", err)
	}
}

func TestSourceRootRejectsInlineConnectionAuthAndSourceIdentity(t *testing.T) {
	for name, tc := range map[string]struct {
		spec string
		want string
	}{
		"auth": {spec: `spec: {type: managed, auth: {token: secret}}
`, want: "spec.auth"},
		"source identity": {spec: `spec: {type: postgres, username: privileged_runtime}
`, want: "username"},
	} {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{"connections/warehouse.yaml": "apiVersion: leapview.dev/v1\nkind: Connection\nmetadata: {id: connection:warehouse, name: warehouse}\n" + tc.spec}
			_, err := LoadSourceRoot(writeSourceFixture(t, files))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadSourceRoot() error = %v, want schema rejection", err)
			}
			diagnostics := configschema.Diagnostics(err)
			if len(diagnostics) == 0 || diagnostics[0].ResourceID != "connection:warehouse" {
				t.Fatalf("diagnostics = %#v, want connection resource", diagnostics)
			}
		})
	}
}

func TestSourceRootRejectsSQLSourceMismatchAndModelCycles(t *testing.T) {
	base := map[string]string{
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
		"sources/customers.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:customers, name: customers}
spec: {connection: warehouse, location: {type: path, path: customers.csv, format: csv}}
`,
	}
	t.Run("source mismatch", func(t *testing.T) {
		files := cloneFixtureFiles(base)
		files["models/orders.yaml"] = `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders_model}
spec: {definition: {type: sql, sql: 'SELECT * FROM source.missing'}, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}
`
		_, err := LoadSourceRoot(writeSourceFixture(t, files))
		if err == nil || !strings.Contains(err.Error(), `unknown source "missing"`) {
			t.Fatalf("LoadSourceRoot() error = %v, want unknown SQL source diagnostic", err)
		}
	})
	t.Run("model cycle", func(t *testing.T) {
		files := cloneFixtureFiles(base)
		files["models/orders.yaml"] = `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders_model}
spec: {definition: {type: sql, sql: 'SELECT * FROM source.orders JOIN model.customers_model USING (id)'}, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}
`
		files["models/customers.yaml"] = `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:customers, name: customers_model}
spec: {definition: {type: sql, sql: 'SELECT * FROM source.customers JOIN model.orders_model USING (id)'}, entities: {id: {type: primary, fields: [id]}}, grain: {entity: id}, fields: {id: {datatype: String}}}
`
		_, err := LoadSourceRoot(writeSourceFixture(t, files))
		if err == nil || !strings.Contains(err.Error(), "cycle") {
			t.Fatalf("LoadSourceRoot() error = %v, want model cycle", err)
		}
	})
}

func TestSourceRootDashboardAdapterMatchesDirectCompilation(t *testing.T) {
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
		"dashboards/sales.yaml": `apiVersion: leapview.dev/v1
kind: Dashboard
metadata: {id: dashboard:sales, name: sales_dashboard, displayName: Sales}
spec:
  semanticModel: sales
  filters: []
  visuals: {order_count: {type: kpi, query: {type: aggregate, dimensions: [], metrics: [order_count]}, presentation: {type: kpi}}}
  pages: [{id: overview, title: Overview, components: []}]
`,
	}
	project, err := LoadSourceRoot(writeSourceFixture(t, files))
	if err != nil {
		t.Fatalf("LoadSourceRoot() error = %v", err)
	}
	authored := *project.Dashboards["sales_dashboard"]
	model := project.Manifest.SemanticModels["semantic:sales"]
	authored.Spec.SemanticModel = "semantic:sales"
	direct, err := dashboardcompiler.CompileDocument(authored, map[string]*semanticmodel.Model{"semantic:sales": model})
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

func TestLoadSourceRootAllowsSemanticReferencesOutsidePartialModelFields(t *testing.T) {
	files := map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {type: managed, defaults: {csv: {header: true}}}
`,
		"sources/orders.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:warehouse.orders, name: warehouse.orders}
spec:
  connection: warehouse
  location: {type: path, path: orders.csv, format: csv}
`,
		"models/orders.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders}
spec:
  definition: {type: direct, source: warehouse.orders}
  fields: {order_id: {datatype: String, label: Order ID}}
  entities:
    order: {type: primary, fields: [order_id]}
    customer: {type: foreign, fields: [customer_id]}
  grain: {entity: order}
  checks:
    - {id: status_values, type: accepted_values, field: status, values: [open, closed], severity: error}
`,
		"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic-model:sales, name: sales}
spec:
  datasets: {orders: {model: orders, defaultTimeDimension: order_date}}
  dimensions:
    order_date:
      datatype: Date
      bindings: {orders: {field: orders.order_date}}
      time: {nativeGrain: day, grains: [day], calendar: iso8601}
  metrics:
    revenue: {type: aggregate, dataset: orders, aggregation: sum, input: {field: orders.revenue}}
`,
	}
	project, err := LoadSourceRoot(writeSourceFixture(t, files))
	if err != nil {
		t.Fatalf("LoadSourceRoot(partial Model fields): %v", err)
	}
	model := project.Manifest.SemanticModels["semantic-model:sales"]
	if model == nil {
		t.Fatal("compiled semantic model is missing")
	}
	table := model.Tables["orders"]
	if len(table.AuthoredFields) != 1 || table.AuthoredFields["order_id"].Label != "Order ID" {
		t.Fatalf("authored field overlay = %#v", table.AuthoredFields)
	}
	for _, field := range []string{"order_id", "customer_id", "status", "order_date", "revenue"} {
		if _, ok := table.Dimensions[field]; !ok {
			t.Fatalf("provisional resolved field %q is missing: %#v", field, table.Dimensions)
		}
	}
}

func TestBundlePlanDiffIsDeterministicAndAggregatesImpact(t *testing.T) {
	resources := []projectgraph.Resource{
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
	firstChanges, firstDeps, firstSummary := diffResourceGraphs(authored, active)
	secondChanges, secondDeps, secondSummary := diffResourceGraphs(authored, active)
	if !reflect.DeepEqual(firstChanges, secondChanges) || !reflect.DeepEqual(firstDeps, secondDeps) || firstSummary != secondSummary {
		t.Fatal("project graph diff is not deterministic")
	}
	if firstSummary.Changed != 1 || firstSummary.DependencyChanges != 1 || firstSummary.Breaking || !firstSummary.MaterializationImpact {
		t.Fatalf("summary = %#v, want metadata-only change plus dependency materialization impact", firstSummary)
	}
	if len(firstDeps) != 1 || firstDeps[0].Type != "reads_source" || firstDeps[0].ResourceKind != string(projectgraph.KindSource) {
		t.Fatalf("dependency change = %#v, want reads_source relation targeting a source resource", firstDeps)
	}
	removedResources := []projectgraph.Resource{resources[1]}
	removed, err := projectgraph.NewProjectGraph(removedResources, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The reduced graph is the authored candidate; the complete graph is the
	// active baseline, so the source is correctly reported as removed.
	_, _, removedSummary := diffResourceGraphs(removed, authored)
	if !removedSummary.Breaking || !removedSummary.MaterializationImpact {
		t.Fatalf("removed source summary = %#v, want breaking materialization impact", removedSummary)
	}
	kindChangedResources := append([]projectgraph.Resource(nil), resources...)
	kindChangedResources[0].Kind = projectgraph.KindModel
	kindChanged, err := projectgraph.NewProjectGraph(kindChangedResources, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, kindChangedSummary := diffResourceGraphs(authored, kindChanged)
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

func writeSourceFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
