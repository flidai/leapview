package compiler

import "testing"

func TestLoadSourceRootAllowsPartialDirectModelFieldUsedByDashboardRatio(t *testing.T) {
	files := map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {type: managed}
`,
		"sources/orders.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:warehouse.orders, name: warehouse.orders}
spec: {connection: warehouse, location: {type: path, path: orders.csv, format: csv}}
`,
		"models/orders.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders}
spec:
  definition: {type: direct, source: warehouse.orders}
  fields: {order_id: {datatype: String}}
  entities: {order: {type: primary, fields: [order_id]}}
  grain: {entity: order}
`,
		"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic-model:sales, name: sales}
spec:
  datasets: {orders: {model: orders}}
  metrics:
    revenue: {type: aggregate, dataset: orders, aggregation: sum, input: {field: orders.revenue}}
    order_count: {type: aggregate, dataset: orders, aggregation: count, input: {field: orders.order_id}}
    revenue_per_order: {type: ratio, numerator: revenue, denominator: order_count}
`,
		"dashboards/sales.yaml": `apiVersion: leapview.dev/v1
kind: Dashboard
metadata: {id: dashboard:sales, name: sales_dashboard}
spec:
  semanticModel: sales
  filters: []
  visuals:
    revenue_per_order:
      type: kpi
      query: {type: aggregate, dimensions: [], metrics: [revenue_per_order]}
      presentation: {type: kpi}
  pages: [{id: overview, title: Overview, components: [{id: revenue-per-order, type: visual, visual: revenue_per_order, placement: {column: 1, row: 1, columnSpan: 12, rowSpan: 6}}]}]
`,
	}

	// Source-root compilation happens before candidate materialization discovers
	// the direct model's physical schema. The omitted revenue field therefore
	// has to survive as a deferred semantic reference so the dashboard can be
	// compiled; snapshot discovery supplies its physical type later.
	project, err := LoadSourceRoot(writeSourceFixture(t, files))
	if err != nil {
		t.Fatalf("LoadSourceRoot(partial direct Model with ratio dashboard): %v", err)
	}
	model := project.Manifest.SemanticModels["semantic-model:sales"]
	if model == nil {
		t.Fatal("compiled semantic model is missing")
	}
	if _, ok := model.Tables["orders"].Dimensions["revenue"]; !ok {
		t.Fatalf("partial direct Model did not retain omitted metric input field: %#v", model.Tables["orders"].Dimensions)
	}
	if _, ok := model.Metrics["revenue_per_order"]; !ok {
		t.Fatalf("compiled ratio metric is missing: %#v", model.Metrics)
	}
	dashboard, ok := project.Manifest.DashboardDefinitions["dashboard:sales"]
	if !ok {
		t.Fatal("dashboard consuming ratio is missing from compiled manifest")
	}
	visual, ok := dashboard.Visualizations["revenue_per_order"]
	if !ok || visual.Query.Aggregate == nil || len(visual.Query.Aggregate.Metrics) != 1 || visual.Query.Aggregate.Metrics[0].FieldID != "revenue_per_order" {
		t.Fatalf("dashboard ratio query binding = %#v", visual.Query)
	}
}

func TestLoadSourceRootAllowsPartialDirectModelFieldUsedByDashboardRecords(t *testing.T) {
	files := map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {type: managed}
`,
		"sources/orders.yaml": `apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:warehouse.orders, name: warehouse.orders}
spec: {connection: warehouse, location: {type: path, path: orders.csv, format: csv}}
`,
		"models/orders.yaml": `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders}
spec:
  definition: {type: direct, source: warehouse.orders}
  fields: {order_id: {datatype: String}}
  entities: {order: {type: primary, fields: [order_id]}}
  grain: {entity: order}
`,
		"semantic-models/sales.yaml": `apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic-model:sales, name: sales}
spec:
  datasets: {orders: {model: orders}}
  metrics: {}
`,
		"dashboards/sales.yaml": `apiVersion: leapview.dev/v1
kind: Dashboard
metadata: {id: dashboard:sales, name: sales_dashboard}
spec:
  semanticModel: sales
  filters: []
  visuals:
    orders_table:
      type: table
      query: {type: records, dataset: orders, fields: [order_id, dashboard_only]}
      presentation: {type: table, rowHeight: 32, showHeader: true, striped: false}
  pages: [{id: overview, title: Overview, components: [{id: orders-table, type: visual, visual: orders_table, placement: {column: 1, row: 1, columnSpan: 12, rowSpan: 6}}]}]
`,
	}

	project, err := LoadSourceRoot(writeSourceFixture(t, files))
	if err != nil {
		t.Fatalf("LoadSourceRoot(partial direct Model with records dashboard): %v", err)
	}
	dashboard, ok := project.Manifest.DashboardDefinitions["dashboard:sales"]
	if !ok {
		t.Fatal("dashboard consuming records field is missing from compiled manifest")
	}
	visual, ok := dashboard.Visualizations["orders_table"]
	if !ok || visual.Query.Detail == nil || len(visual.Query.Detail.Fields) != 2 || visual.Query.Detail.Fields[1].FieldID != "orders.dashboard_only" {
		t.Fatalf("dashboard records query binding = %#v", visual.Query)
	}
}
