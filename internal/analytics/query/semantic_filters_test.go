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

func TestNamedMetricWhereAppliesPerFactBeforeMultiFactStitch(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders (customer_id INTEGER, segment VARCHAR)",
		"CREATE TABLE model.tags (customer_id INTEGER, segment VARCHAR)",
		"INSERT INTO model.orders VALUES (1, 'consumer'), (2, 'consumer'), (3, 'business')",
		"INSERT INTO model.tags VALUES (1, 'consumer'), (2, 'business'), (3, 'business'), (4, 'business')",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	model := executableMultiFactModel()
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
		t.Fatalf("execute multi-fact named filter plan: %v\nSQL: %s", err, plan.SQL)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("multi-fact named filter query returned no row")
	}
	var orders, tags int
	if err := rows.Scan(&orders, &tags); err != nil {
		t.Fatal(err)
	}
	if orders != 2 || tags != 1 {
		t.Fatalf("per-fact filtered counts = (%d, %d), want (2, 1)", orders, tags)
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
	planner, err := NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(Request{Metrics: []Field{{Field: "order_count"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "t0.status = ?") || !strings.Contains(plan.SQL, " OR ") || !strings.Contains(plan.SQL, "t1.customer_id IS NOT NULL") {
		t.Fatalf("OR filter did not preserve local/joined semantics: %s", plan.SQL)
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
	if !strings.Contains(plan.SQL, "t1.customer_id IS NOT NULL") || !strings.Contains(plan.SQL, "AND NOT") {
		t.Fatalf("NOT filter did not guard joined match: %s", plan.SQL)
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
	planner, err := NewCompiledPlanner(model, WithTableRelation(func(table string) (string, error) { return "model." + table, nil }))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(Request{Metrics: []Field{{Field: "order_count"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "LEFT JOIN model.customers t1 ON t0.customer_id = t1.customer_id") {
		t.Fatalf("joined named filter omitted relationship join: %s", plan.SQL)
	}
	for _, fragment := range []string{
		"t1.state = ?",
		"t0.status IS NOT NULL",
		"NOT (t0.status = ?)",
		"t0.status IN (?, ?)",
		"t0.status IS NULL",
		"AND NOT (t0.status = ?)",
	} {
		if !strings.Contains(plan.SQL, fragment) {
			t.Fatalf("named filter SQL missing %q: %s", fragment, plan.SQL)
		}
	}
	if got, want := plan.Args, []any{"DK", "canceled", "paid", "shipped", "canceled"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("named filter args = %#v, want %#v", got, want)
	}
}

func TestNamedMetricWhereCoercesIntegerLiteralsAndRejectsUnknownFilter(t *testing.T) {
	model := testModel()
	orders := model.Tables["orders"]
	orders.Dimensions["order_id"] = semanticmodel.MetricDimension{Expr: "order_id", Type: "number", Datatype: semanticmodel.DataTypeInteger}
	model.Tables["orders"] = orders
	model.Filters = map[string]semanticmodel.SemanticFilterSpec{
		"ids": {Field: "orders.order_id", Operator: "in", Value: []any{1, 2}},
	}
	metric := model.Metrics["order_count"]
	metric.Where = []string{"ids"}
	model.Metrics["order_count"] = metric
	planner, err := NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(Request{Metrics: []Field{{Field: "order_count"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "t0.order_id IN (?, ?)") {
		t.Fatalf("integer filter SQL = %s", plan.SQL)
	}
	if got, want := plan.Args, []any{int64(1), int64(2)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("integer filter args = %#v, want %#v", got, want)
	}
	metric.Where = []string{"missing"}
	model.Metrics["order_count"] = metric
	planner, err = NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Plan(Request{Metrics: []Field{{Field: "order_count"}}}); err == nil || !strings.Contains(err.Error(), `unknown semantic filter "missing"`) {
		t.Fatalf("unknown named filter error = %v", err)
	}
}

func TestNamedFilterRequiresExplicitPathWhenJoinedDatasetIsAmbiguous(t *testing.T) {
	model := testModel()
	orders := model.Tables["orders"]
	orders.Dimensions["alt_customer_id"] = semanticmodel.MetricDimension{Expr: "alt_customer_id"}
	model.Tables["orders"] = orders
	model.Relationships = append(model.Relationships, semanticmodel.Relationship{
		ID: "orders_customers_alt", From: "orders.alt_customer_id", To: "customers.customer_id", Cardinality: "many_to_one",
	})
	model.Filters = map[string]semanticmodel.SemanticFilterSpec{
		"ambiguous": {Field: "customers.state", Operator: "equals", Value: "DK"},
	}
	metric := model.Metrics["order_count"]
	metric.Where = []string{"ambiguous"}
	model.Metrics["order_count"] = metric
	planner, err := NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Plan(Request{Metrics: []Field{{Field: "order_count"}}}); err == nil || !strings.Contains(err.Error(), "ambiguous relationship path") {
		t.Fatalf("ambiguous named filter error = %v", err)
	}
	filter := model.Filters["ambiguous"]
	filter.Path = []string{"orders_customers"}
	model.Filters["ambiguous"] = filter
	planner, err = NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planner.Plan(Request{Metrics: []Field{{Field: "order_count"}}}); err != nil {
		t.Fatalf("explicit named filter path rejected: %v", err)
	}
}
