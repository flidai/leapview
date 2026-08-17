package query

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/query/planir"
)

// mustNewCompiledPlanner is test-only shorthand for the activation API. The
// production package intentionally has no fallible-constructor-swallowing
// NewPlanner helper.
func mustNewCompiledPlanner(t testing.TB, model *semanticmodel.Model, options ...PlannerOption) *Planner {
	t.Helper()
	populateFixtureTableModelNames(model)
	planner, err := NewCompiledPlanner(model, options...)
	if err != nil {
		t.Fatalf("NewCompiledPlanner() error = %v", err)
	}
	return planner
}

// populateFixtureTableModelNames mirrors the authored dataset-to-model
// lowering used by test fixtures. Runtime validation intentionally requires
// this binding to be explicit; production code must never infer it.
func populateFixtureTableModelNames(model *semanticmodel.Model) {
	if model == nil {
		return
	}
	for alias, dataset := range model.Datasets {
		modelName := dataset.Model
		if modelName == "" {
			modelName = alias
		}
		table, ok := model.Tables[modelName]
		if !ok {
			continue
		}
		table.ModelName = dataset.Model
		model.Tables[modelName] = table
	}
}

func TestCompileModelBuildsReusableMetricDependencyMetadata(t *testing.T) {
	model := testModel()
	model.Metrics["nested_ratio"] = semanticmodel.Metric{Type: "derived", Expression: "${tags_per_order} * 100"}
	model.Metrics["net_revenue"] = semanticmodel.Metric{
		Type: "aggregate", Dataset: "orders",
		Aggregation: "sum",
		Input:       &semanticmodel.MetricInput{Field: "orders.revenue"},
		Empty:       "zero",
	}
	orders := model.Tables["orders"]
	orders.Dimensions["discount"] = semanticmodel.MetricDimension{Type: "number", Datatype: semanticmodel.DataTypeDecimal}
	model.Tables["orders"] = orders
	populateFixtureTableModelNames(model)

	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	nested, ok := compiled.metric("nested_ratio")
	if !ok || !reflect.DeepEqual(nested.RootDatasets, []string{"orders", "tags"}) {
		t.Fatalf("nested metric roots = %#v", nested.RootDatasets)
	}
	tagsPerOrder, ok := compiled.metric("tags_per_order")
	if !ok || tagsPerOrder.Derived == nil || len(tagsPerOrder.Derived.Expression.References()) == 0 {
		t.Fatal("metric expression was not compiled")
	}
	if tagsPerOrder.Aggregate != nil || tagsPerOrder.Ratio != nil {
		t.Fatal("derived metric has more than one typed payload")
	}
}

func TestCompiledDatasetTableGetterReturnsDetachedMetadata(t *testing.T) {
	model := testModel()
	populateFixtureTableModelNames(model)
	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	dataset, ok := compiled.Dataset("orders")
	if !ok {
		t.Fatal("orders dataset missing")
	}
	detached := dataset.Table()
	detached.Dimensions["order_id"] = semanticmodel.MetricDimension{Label: "mutated"}
	detached.Entities["order"] = semanticmodel.ModelEntitySpec{Type: "unique", Fields: []string{"status"}}
	detached.Description = "mutated"
	current := dataset.Table()
	if current.Dimensions["order_id"].Label == "mutated" || current.Entities["order"].Type != "primary" || current.Description == "mutated" {
		t.Fatal("CompiledDataset.Table() exposed mutable serving metadata")
	}
}

func TestCompileModelRetainsAggregateMetricDatasetMetadata(t *testing.T) {
	model := testModel()
	model.Metrics["gross_revenue"] = semanticmodel.Metric{
		Type: "aggregate", Dataset: "orders", Aggregation: "sum",
		Input: &semanticmodel.MetricInput{Field: "orders.revenue"},
	}
	populateFixtureTableModelNames(model)
	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	metric, ok := compiled.metric("gross_revenue")
	if !ok || !reflect.DeepEqual(metric.RootDatasets, []string{"orders"}) {
		t.Fatalf("aggregate metric roots = %#v, want [orders]", metric.RootDatasets)
	}
	if metric.Aggregate == nil || metric.Derived != nil || metric.Ratio != nil {
		t.Fatal("aggregate metric payload is not closed")
	}
}

func TestCompileModelRetainsRatioPayloadWithoutExpression(t *testing.T) {
	model := testModel()
	model.Metrics["conversion"] = semanticmodel.Metric{Type: "ratio", Numerator: "tags_per_order", Denominator: "order_count"}
	populateFixtureTableModelNames(model)
	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	metric, ok := compiled.metric("conversion")
	if !ok || metric.Ratio == nil || metric.Ratio.Numerator != "tags_per_order" || metric.Ratio.Denominator != "order_count" {
		t.Fatalf("ratio payload = %#v", metric.Ratio)
	}
	if metric.Aggregate != nil || metric.Derived != nil {
		t.Fatal("ratio metric has more than one typed payload")
	}
}

func TestCompiledMetricRejectsMultipleTypedPayloads(t *testing.T) {
	node := CompiledMetric{
		Name: "invalid", Type: "ratio",
		Derived: &CompiledDerivedMetric{}, Ratio: &CompiledRatioMetric{Numerator: "a", Denominator: "b"},
	}
	if err := node.validatePayload(); err == nil || !strings.Contains(err.Error(), "exactly one typed payload") {
		t.Fatalf("invalid payload error = %v", err)
	}
}

func TestPlannerLowersRatioPayloadAsComputeRatio(t *testing.T) {
	model := testModel()
	model.Metrics["tag_ratio"] = semanticmodel.Metric{Type: "ratio", Numerator: "tag_count", Denominator: "order_count"}
	planner := mustNewCompiledPlanner(t, model)
	node, ok := planner.compiled.metric("tag_ratio")
	if !ok || node.Ratio == nil || node.Derived != nil || node.Aggregate != nil {
		t.Fatalf("compiled ratio payload = %#v", node)
	}
	plan, err := planner.Plan(Request{Metrics: []Field{{Field: "tag_ratio"}}})
	if err != nil {
		t.Fatal(err)
	}
	var ratio *planir.ComputeRatio
	for _, candidate := range plan.IR.Nodes {
		if value, ok := candidate.(planir.ComputeRatio); ok && value.Output == "tag_ratio" {
			copy := value
			ratio = &copy
			break
		}
	}
	if ratio == nil || ratio.Numerator != "tag_count" || ratio.Denominator != "order_count" {
		t.Fatalf("ratio PlanIR node = %#v", ratio)
	}
}

func TestPlannerLowersCanonicalAggregateMetricsWithoutMetricsDualWrite(t *testing.T) {
	model := testModel()
	model.Metrics["order_count"] = semanticmodel.Metric{Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, Empty: "zero"}
	model.Metrics["revenue"] = semanticmodel.Metric{Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}, Empty: "zero"}
	populateFixtureTableModelNames(model)
	planner, err := NewCompiledPlanner(model)
	if err != nil {
		t.Fatalf("NewCompiledPlanner() error = %v", err)
	}
	plan, err := planner.Plan(Request{Metrics: []Field{{Field: "order_count"}, {Field: "tags_per_order"}}})
	if err != nil {
		t.Fatalf("canonical aggregate plan error = %v", err)
	}
	explain, err := plan.Explain()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "[AggregateMetrics]") || !strings.Contains(explain, "[ComputeDerived]") || strings.Contains(explain, "Measure") {
		t.Fatalf("canonical aggregate PlanIR = %s", explain)
	}
}

func TestPlannerRendersCompositeRelationshipJoinTuple(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders (customer_id INTEGER, order_id INTEGER)",
		"CREATE TABLE model.customers (customer_id INTEGER, order_id INTEGER, state VARCHAR)",
		"INSERT INTO model.orders VALUES (10, 1), (20, 2), (20, 99)",
		"INSERT INTO model.customers VALUES (10, 1, 'DK'), (20, 2, 'SE')",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	model := testModel()
	customers := model.Tables["customers"]
	customers.Dimensions["order_id"] = semanticmodel.MetricDimension{Datatype: semanticmodel.DataTypeInteger}
	customers.Entities["customer_order"] = semanticmodel.ModelEntitySpec{Type: "unique", Fields: []string{"customer_id", "order_id"}}
	model.Tables["customers"] = customers
	model.Relationships[0] = semanticmodel.Relationship{
		ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id", "order_id"},
		ToDataset: "customers", ToFields: []string{"customer_id", "order_id"}, Cardinality: "many_to_one",
	}
	populateFixtureTableModelNames(model)
	planner, err := NewCompiledPlanner(model, WithTableRelation(func(table string) (string, error) { return "model." + table, nil }))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(Request{Dimensions: []Field{{Field: "customers.state"}}, Metrics: []Field{{Field: "order_count"}}})
	if err != nil {
		t.Fatalf("composite relationship planner error = %v", err)
	}
	if !strings.Contains(plan.SQL, `"r0"."customer_id" = "r2"."customer_id" AND "r0"."order_id" = "r2"."order_id"`) {
		t.Fatalf("composite relationship join = %s", plan.SQL)
	}
	rows, err := db.Query(plan.SQL, plan.Args...)
	if err != nil {
		t.Fatalf("execute composite relationship plan: %v\nSQL: %s", err, plan.SQL)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("composite relationship query returned no row")
	}
	var state string
	var count int
	if err := rows.Scan(&state, &count); err != nil {
		t.Fatal(err)
	}
	if state != "DK" || count != 1 {
		t.Fatalf("composite relationship result = (%s, %d), want (DK, 1)", state, count)
	}
}

func TestCompileModelFailsClosedForInvalidMetricDAG(t *testing.T) {
	model := testModel()
	model.Metrics["broken"] = semanticmodel.Metric{Type: "derived", Expression: "${missing_member} + 1"}
	if _, err := CompileModel(model); err == nil || !strings.Contains(err.Error(), "unknown metric") {
		t.Fatalf("unknown dependency error = %v", err)
	}

	model = testModel()
	model.Metrics["cycle_a"] = semanticmodel.Metric{Type: "derived", Expression: "${cycle_b}"}
	model.Metrics["cycle_b"] = semanticmodel.Metric{Type: "derived", Expression: "${cycle_a}"}
	if _, err := CompileModel(model); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle error = %v", err)
	}
}
