package query

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestNamedJoinedIsNullFilterRequiresMatchedRow(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders (order_id INTEGER, customer_id INTEGER)",
		"CREATE TABLE model.customers (customer_id INTEGER, state VARCHAR)",
		"INSERT INTO model.orders VALUES (1, 10), (2, 20), (3, 30)",
		"INSERT INTO model.customers VALUES (10, NULL), (20, 'DK')",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	model := testModel()
	model.Filters = map[string]semanticmodel.SemanticFilterSpec{
		"customer_state_null": {Field: "customers.state", Operator: "is_null"},
	}
	metric := model.Metrics["order_count"]
	metric.Where = []string{"customer_state_null"}
	model.Metrics["order_count"] = metric
	populateFixtureTableModelNames(model)
	planner, err := NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(Request{Metrics: []Field{{Field: "order_count"}}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(plan.SQL, plan.Args...)
	if err != nil {
		t.Fatalf("execute named filter plan: %v\nSQL: %s", err, plan.SQL)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("named filter query returned no row")
	}
	var count int
	if err := rows.Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("matched-null count = %d, want 1", count)
	}
}

func TestNamedMetricWhereAppliesPerDatasetBeforeMultiDatasetStitch(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders (order_id INTEGER, customer_id INTEGER, segment VARCHAR)",
		"CREATE TABLE model.tags (tag_id INTEGER, customer_id INTEGER, segment VARCHAR)",
		"INSERT INTO model.orders VALUES (1, 1, 'consumer'), (2, 2, 'consumer'), (3, 3, 'business')",
		"INSERT INTO model.tags VALUES (1, 1, 'consumer'), (2, 2, 'business'), (3, 3, 'business'), (4, 4, 'business')",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	model := executableMultiDatasetModel()
	model.Filters = map[string]semanticmodel.SemanticFilterSpec{
		"consumer_orders": {Field: "orders.segment", Operator: "equals", Value: "consumer"},
		"consumer_tags":   {Field: "tags.segment", Operator: "equals", Value: "consumer"},
	}
	orderCount := model.Metrics["order_count"]
	orderCount.Where = []string{"consumer_orders"}
	model.Metrics["order_count"] = orderCount
	tagCount := model.Metrics["tag_count"]
	tagCount.Where = []string{"consumer_tags"}
	model.Metrics["tag_count"] = tagCount
	populateFixtureTableModelNames(model)
	planner, err := NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(Request{Metrics: []Field{{Field: "order_count"}, {Field: "tag_count"}}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(plan.SQL, plan.Args...)
	if err != nil {
		t.Fatalf("execute multi-dataset named filter plan: %v\nSQL: %s", err, plan.SQL)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("multi-dataset named filter query returned no row")
	}
	var orders, tags int
	if err := rows.Scan(&orders, &tags); err != nil {
		t.Fatal(err)
	}
	if orders != 2 || tags != 1 {
		t.Fatalf("per-dataset filtered counts = (%d, %d), want (2, 1)", orders, tags)
	}
}

func TestNamedFilterMatchGuardsPreserveBooleanAndNotSemantics(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders (order_id INTEGER, customer_id INTEGER, status VARCHAR)",
		"CREATE TABLE model.customers (customer_id INTEGER, state VARCHAR)",
		"INSERT INTO model.orders VALUES (1, 10, 'always'), (2, 30, 'never'), (3, 20, 'never'), (4, 10, 'never')",
		"INSERT INTO model.customers VALUES (10, 'SE'), (20, 'DK')",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	model := testModel()
	model.Filters = map[string]semanticmodel.SemanticFilterSpec{
		"or_filter": {Any: []semanticmodel.SemanticFilterSpec{
			{Field: "orders.status", Operator: "equals", Value: "always"},
			{Field: "customers.state", Operator: "equals", Value: "DK"},
		}},
		"not_filter": {Not: &semanticmodel.SemanticFilterSpec{Field: "customers.state", Operator: "equals", Value: "DK"}},
	}
	metric := model.Metrics["order_count"]
	metric.Where = []string{"or_filter"}
	model.Metrics["order_count"] = metric
	populateFixtureTableModelNames(model)
	planner, err := NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(Request{Metrics: []Field{{Field: "order_count"}}})
	if err != nil {
		t.Fatal(err)
	}
	explain, err := plan.Explain()
	if err != nil || !strings.Contains(explain, "filter=named/or_filter:") {
		t.Fatalf("OR filter PlanIR = %q, error=%v", explain, err)
	}
	if got := executeSingleCount(t, db, plan); got != 2 {
		t.Fatalf("OR filter count = %d, want 2", got)
	}

	metric.Where = []string{"not_filter"}
	model.Metrics["order_count"] = metric
	planner, err = NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = planner.Plan(Request{Metrics: []Field{{Field: "order_count"}}})
	if err != nil {
		t.Fatal(err)
	}
	explain, err = plan.Explain()
	if err != nil || !strings.Contains(explain, "filter=named/not_filter:") {
		t.Fatalf("NOT filter PlanIR = %q, error=%v", explain, err)
	}
	if got := executeSingleCount(t, db, plan); got != 2 {
		t.Fatalf("NOT filter count = %d, want 2 matched non-DK rows", got)
	}
}

func executeSingleCount(t *testing.T, db *sql.DB, plan Plan) int {
	t.Helper()
	rows, err := db.Query(plan.SQL, plan.Args...)
	if err != nil {
		t.Fatalf("execute filter plan: %v\nSQL: %s", err, plan.SQL)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("filter query returned no row")
	}
	var count int
	if err := rows.Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestNamedMetricWhereCompilesJoinedBooleanFilterTree(t *testing.T) {
	model := testModel()
	model.Filters = map[string]semanticmodel.SemanticFilterSpec{
		"joined_state":    {Field: "customers.state", Operator: "equals", Value: "DK", Path: []string{"orders_customers"}},
		"not_null_status": {Field: "orders.status", Operator: "is_not_null"},
		"not_cancelled":   {Not: &semanticmodel.SemanticFilterSpec{Field: "orders.status", Operator: "equals", Value: "canceled"}},
		"complex": {All: []semanticmodel.SemanticFilterSpec{
			{Any: []semanticmodel.SemanticFilterSpec{
				{Field: "orders.status", Operator: "in", Value: []any{"paid", "shipped"}},
				{Field: "orders.status", Operator: "is_null"},
			}},
			{Not: &semanticmodel.SemanticFilterSpec{Field: "orders.status", Operator: "equals", Value: "canceled"}},
		}},
	}
	metric := model.Metrics["order_count"]
	metric.Where = []string{"joined_state", "not_null_status", "not_cancelled", "complex"}
	model.Metrics["order_count"] = metric
	populateFixtureTableModelNames(model)
	planner, err := NewCompiledPlanner(model, WithTableRelation(func(table string) (string, error) { return "model." + table, nil }))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(Request{Metrics: []Field{{Field: "order_count"}}})
	if err != nil {
		t.Fatal(err)
	}
	explain, err := plan.Explain()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"joined_state", "not_null_status", "not_cancelled", "complex"} {
		if !strings.Contains(explain, "filter=named/"+name+":") {
			t.Fatalf("named filter PlanIR missing %q: %s", name, explain)
		}
	}
	if strings.Join(plan.RelationshipPaths, ",") != "orders:orders_customers" {
		t.Fatalf("named filter relationship paths = %v", plan.RelationshipPaths)
	}
	if got, want := plan.Args, []any{"DK", "canceled", "paid", "shipped", "canceled"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("named filter args = %#v, want %#v", got, want)
	}
}

func TestNamedMetricWhereCoercesIntegerLiteralsAndRejectsUnknownFilter(t *testing.T) {
	model := testModel()
	orders := model.Tables["orders"]
	orders.Dimensions["order_id"] = semanticmodel.MetricDimension{Type: "number", Datatype: semanticmodel.DataTypeInteger}
	model.Tables["orders"] = orders
	model.Filters = map[string]semanticmodel.SemanticFilterSpec{
		"ids": {Field: "orders.order_id", Operator: "in", Value: []any{1, 2}},
	}
	metric := model.Metrics["order_count"]
	metric.Where = []string{"ids"}
	model.Metrics["order_count"] = metric
	populateFixtureTableModelNames(model)
	planner, err := NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(Request{Metrics: []Field{{Field: "order_count"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := plan.Args, []any{int64(1), int64(2)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("integer filter args = %#v, want %#v", got, want)
	}
	explain, err := plan.Explain()
	if err != nil || !strings.Contains(explain, "filter=named/ids:") {
		t.Fatalf("integer filter PlanIR = %q, error=%v", explain, err)
	}
	metric.Where = []string{"missing"}
	model.Metrics["order_count"] = metric
	if _, err := NewCompiledPlanner(model); err == nil || !strings.Contains(err.Error(), `unknown semantic filter "missing"`) {
		t.Fatalf("unknown named filter error = %v", err)
	}
}

func TestNamedFilterRequiresExplicitPathWhenJoinedDatasetIsAmbiguous(t *testing.T) {
	model := testModel()
	orders := model.Tables["orders"]
	orders.Dimensions["alt_customer_id"] = semanticmodel.MetricDimension{Datatype: semanticmodel.DataTypeInteger}
	model.Tables["orders"] = orders
	model.Relationships = append(model.Relationships, semanticmodel.Relationship{
		ID: "orders_customers_alt", FromDataset: "orders", FromFields: []string{"alt_customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one",
	})
	model.Filters = map[string]semanticmodel.SemanticFilterSpec{
		"ambiguous": {Field: "customers.state", Operator: "equals", Value: "DK"},
	}
	metric := model.Metrics["order_count"]
	metric.Where = []string{"ambiguous"}
	model.Metrics["order_count"] = metric
	populateFixtureTableModelNames(model)
	if _, err := NewCompiledPlanner(model); err == nil || !strings.Contains(err.Error(), "ambiguous relationship path") {
		t.Fatalf("ambiguous named filter error = %v", err)
	}
	filter := model.Filters["ambiguous"]
	filter.Path = []string{"orders_customers"}
	model.Filters["ambiguous"] = filter
	planner, err := NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Plan(Request{Metrics: []Field{{Field: "order_count"}}}); err != nil {
		t.Fatalf("explicit named filter path rejected: %v", err)
	}
}
