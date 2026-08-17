package query

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

// mustNewCompiledPlanner is test-only shorthand for the activation API. The
// production package intentionally has no fallible-constructor-swallowing
// NewPlanner helper.
func mustNewCompiledPlanner(t testing.TB, model *semanticmodel.Model, options ...PlannerOption) *Planner {
	t.Helper()
	planner, err := NewCompiledPlanner(model, options...)
	if err != nil {
		t.Fatalf("NewCompiledPlanner() error = %v", err)
	}
	return planner
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

	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	nested, ok := compiled.metric("nested_ratio")
	if !ok || !reflect.DeepEqual(nested.RootDatasets, []string{"orders", "tags"}) {
		t.Fatalf("nested metric roots = %#v", nested.RootDatasets)
	}
	tagsPerOrder, ok := compiled.metric("tags_per_order")
	if !ok || len(tagsPerOrder.Expression.References()) == 0 {
		t.Fatal("metric expression was not compiled")
	}
}

func TestCompileModelRetainsAggregateMetricFactMetadata(t *testing.T) {
	model := testModel()
	model.Metrics["gross_revenue"] = semanticmodel.Metric{
		Type: "aggregate", Dataset: "orders", Aggregation: "sum",
		Input: &semanticmodel.MetricInput{Field: "orders.revenue"},
	}
	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	metric, ok := compiled.metric("gross_revenue")
	if !ok || !reflect.DeepEqual(metric.RootDatasets, []string{"orders"}) {
		t.Fatalf("aggregate metric roots = %#v, want [orders]", metric.RootDatasets)
	}
	if len(metric.Expression.References()) != 0 {
		t.Fatal("aggregate metric was incorrectly converted to an expression")
	}
}

func TestPlannerLowersCanonicalAggregateMetricsWithoutMetricsDualWrite(t *testing.T) {
	model := testModel()
	model.Metrics["order_count"] = semanticmodel.Metric{Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, Empty: "zero"}
	model.Metrics["revenue"] = semanticmodel.Metric{Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}, Empty: "zero"}
	planner, err := NewCompiledPlanner(model)
	if err != nil {
		t.Fatalf("NewCompiledPlanner() error = %v", err)
	}
	plan, err := planner.Plan(Request{Metrics: []Field{{Field: "order_count"}, {Field: "tags_per_order"}}})
	if err != nil {
		t.Fatalf("canonical aggregate plan error = %v", err)
	}
	if !strings.Contains(plan.SQL, "COUNT(t0.order_id)") || !strings.Contains(plan.SQL, "safe_divide") && !strings.Contains(plan.SQL, "NULLIF") {
		t.Fatalf("canonical aggregate SQL = %s", plan.SQL)
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
	planner, err := NewCompiledPlanner(model, WithTableRelation(func(table string) (string, error) { return "model." + table, nil }))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(Request{Dimensions: []Field{{Field: "customers.state"}}, Metrics: []Field{{Field: "order_count"}}})
	if err != nil {
		t.Fatalf("composite relationship planner error = %v", err)
	}
	if !strings.Contains(plan.SQL, "t0.customer_id = t1.customer_id AND t0.order_id = t1.order_id") {
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
