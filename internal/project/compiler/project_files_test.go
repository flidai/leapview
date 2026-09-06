package compiler

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCompileSourceRootAndNestedFragmentsAreDeterministic(t *testing.T) {
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
	firstRoot := writeSourceFixture(t, files)
	secondRoot := writeSourceFixture(t, files)

	first, err := Compile(firstRoot)
	if err != nil {
		t.Fatalf("Compile(source root): %v", err)
	}
	second, err := Compile(secondRoot)
	if err != nil {
		t.Fatalf("Compile(second source root): %v", err)
	}
	if first.Digest() != second.Digest() || string(first.Canonical()) != string(second.Canonical()) {
		t.Fatalf("source-root bundles differ: %s / %s", first.Digest(), second.Digest())
	}

	plan, err := PlanSourceRootAgainstBundle(firstRoot, first)
	if err != nil {
		t.Fatalf("PlanSourceRootAgainstBundle: %v", err)
	}
	if !reflect.DeepEqual(plan, BundlePlan{
		Connections:    []string{"connection:warehouse"},
		Sources:        []string{"source:orders"},
		Models:         []string{"model:orders"},
		SemanticModels: []string{"semantic:sales"},
		Pipelines:      []string{"pipeline:sales-refresh"},
		Dashboards:     []string{"dashboard:sales"},
		Deterministic:  true,
	}) {
		t.Fatalf("unchanged source-root plan = %#v", plan)
	}
}

func TestSourceRootRejectsLegacyProjectManifestWithMigrationGuidance(t *testing.T) {
	root := writeSourceFixture(t, map[string]string{
		"connections/warehouse.yaml": `apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec: {type: managed}
`,
	})
	if err := os.WriteFile(filepath.Join(root, "leapview.yaml"), []byte(`apiVersion: leapview.dev/v1
kind: Project
metadata: {id: project:legacy, name: legacy}
spec: {}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Compile(root); err == nil {
		t.Fatal("Compile(source root) accepted legacy Project manifest")
	} else if message := err.Error(); !strings.Contains(message, "Project authoring was removed") || !strings.Contains(message, "delete") {
		t.Fatalf("Compile(source root) error = %q, want removal and migration guidance", message)
	}
}
