package query

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestRolePlayingDimensionPathsExecuteWithIndependentAliases(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders(order_id INTEGER, ordered_date_id INTEGER, shipped_date_id INTEGER)",
		"INSERT INTO model.orders VALUES (1, 1, 2), (2, 1, 3), (3, 2, 3)",
		"CREATE TABLE model.dates(date_id INTEGER, date_value DATE)",
		"INSERT INTO model.dates VALUES (1, DATE '2026-07-01'), (2, DATE '2026-07-02'), (3, DATE '2026-07-03')",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("execute %q: %v", statement, err)
		}
	}

	plan, err := mustNewCompiledPlanner(t, rolePlayingDateModel()).Plan(Request{
		Dimensions: []Field{{Field: "order_date"}, {Field: "ship_date"}},
		Metrics:    []Field{{Field: "order_count"}},
		Sort:       []Sort{{Field: "order_date", Direction: "asc"}, {Field: "ship_date", Direction: "asc"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(plan.SQL, plan.Args...)
	if err != nil {
		t.Fatalf("execute role-playing plan:\n%s\n%v", plan.SQL, err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var orderDate, shipDate time.Time
		var count int
		if err := rows.Scan(&orderDate, &shipDate, &count); err != nil {
			t.Fatal(err)
		}
		got[orderDate.Format("2006-01-02")+"/"+shipDate.Format("2006-01-02")] = count
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"2026-07-01/2026-07-02": 1,
		"2026-07-01/2026-07-03": 1,
		"2026-07-02/2026-07-03": 1,
	}
	if len(got) != len(want) {
		t.Fatalf("role-playing rows = %#v, want %#v", got, want)
	}
	for key, count := range want {
		if got[key] != count {
			t.Fatalf("role-playing row %q = %d, want %d; all rows %#v", key, got[key], count, got)
		}
	}
}

func TestMultiDatasetPlanExecutesWithoutDatasetFanoutAndPreservesOneSidedGroups(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders(order_id VARCHAR, customer_id VARCHAR, segment VARCHAR, amount DOUBLE)",
		"INSERT INTO model.orders VALUES ('o1', 'a', 'consumer', 10), ('o2', 'a', 'consumer', 20), ('o3', 'b', 'business', 30)",
		"CREATE TABLE model.tags(tag_id VARCHAR, customer_id VARCHAR, segment VARCHAR, tag VARCHAR)",
		"INSERT INTO model.tags VALUES ('t1', 'a', 'consumer', 'new'), ('t2', 'c', 'consumer', 'vip'), ('t3', 'c', 'consumer', 'repeat')",
		"CREATE TABLE model.clicks(click_id VARCHAR, customer_id VARCHAR, segment VARCHAR)",
		"INSERT INTO model.clicks VALUES ('c1', 'a', 'consumer'), ('c2', 'd', 'business'), ('c3', 'd', 'business')",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("execute %q: %v", statement, err)
		}
	}

	model := executableMultiDatasetModel()
	planner := mustNewCompiledPlanner(t, model)
	scalar, err := planner.Plan(Request{Metrics: []Field{{Field: "order_count"}, {Field: "tag_count"}, {Field: "click_count"}, {Field: "tags_per_order"}}})
	if err != nil {
		t.Fatal(err)
	}
	var orderCount, tagCount, clickCount int
	var ratio float64
	if err := db.QueryRow(scalar.SQL, scalar.Args...).Scan(&orderCount, &tagCount, &clickCount, &ratio); err != nil {
		t.Fatalf("execute scalar plan:\n%s\n%v", scalar.SQL, err)
	}
	if orderCount != 3 || tagCount != 3 || clickCount != 3 || ratio != 1 {
		t.Fatalf("scalar = orders %d tags %d clicks %d ratio %v", orderCount, tagCount, clickCount, ratio)
	}

	conformed, err := planner.Plan(Request{
		Metrics: []Field{{Field: "order_count"}, {Field: "tag_count"}, {Field: "click_count"}, {Field: "tags_per_order"}},
		Filters: []Filter{{Field: "segment", Operator: "equals", Values: []any{"consumer"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(conformed.SQL, conformed.Args...).Scan(&orderCount, &tagCount, &clickCount, &ratio); err != nil {
		t.Fatalf("execute conformed selection plan:\n%s\n%v", conformed.SQL, err)
	}
	if orderCount != 2 || tagCount != 3 || clickCount != 1 || ratio != 1.5 {
		t.Fatalf("conformed selection = orders %d tags %d clicks %d ratio %v", orderCount, tagCount, clickCount, ratio)
	}

	local, err := planner.Plan(Request{
		Metrics: []Field{{Field: "order_count"}, {Field: "tag_count"}, {Field: "tags_per_order"}},
		Filters: []Filter{{Field: "orders.segment", Dataset: "orders", Operator: "equals", Values: []any{"business"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(local.SQL, local.Args...).Scan(&orderCount, &tagCount, &ratio); err != nil {
		t.Fatalf("execute dataset-local selection plan:\n%s\n%v", local.SQL, err)
	}
	if orderCount != 1 || tagCount != 3 || ratio != 3 {
		t.Fatalf("dataset-local selection = orders %d tags %d ratio %v", orderCount, tagCount, ratio)
	}

	multiSelect, err := planner.Plan(Request{
		Metrics: []Field{{Field: "order_count"}, {Field: "tag_count"}, {Field: "click_count"}},
		Filters: []Filter{{Groups: []FilterGroup{
			{Filters: []Filter{{Field: "customer", Operator: "equals", Values: []any{"a"}}}},
			{Filters: []Filter{{Field: "customer", Operator: "equals", Values: []any{"c"}}}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(multiSelect.SQL, multiSelect.Args...).Scan(&orderCount, &tagCount, &clickCount); err != nil {
		t.Fatalf("execute multi-entry selection plan:\n%s\n%v", multiSelect.SQL, err)
	}
	if orderCount != 2 || tagCount != 3 || clickCount != 1 {
		t.Fatalf("multi-entry selection = orders %d tags %d clicks %d", orderCount, tagCount, clickCount)
	}

	grouped, err := planner.Plan(Request{
		Dimensions: []Field{{Field: "customer", Alias: "customer"}, {Field: "segment", Alias: "segment"}},
		Metrics:    []Field{{Field: "order_count", Alias: "orders"}, {Field: "tag_count", Alias: "tags"}, {Field: "click_count", Alias: "clicks"}},
		Sort:       []Sort{{Field: "customer", Direction: "asc"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(grouped.SQL, grouped.Args...)
	if err != nil {
		t.Fatalf("execute grouped plan:\n%s\n%v", grouped.SQL, err)
	}
	defer rows.Close()
	got := map[string][3]int{}
	for rows.Next() {
		var customer, segment string
		var orders, tags, clicks int
		if err := rows.Scan(&customer, &segment, &orders, &tags, &clicks); err != nil {
			t.Fatal(err)
		}
		got[customer+"/"+segment] = [3]int{orders, tags, clicks}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string][3]int{
		"a/consumer": {2, 1, 1},
		"b/business": {1, 0, 0},
		"c/consumer": {0, 2, 0},
		"d/business": {0, 0, 2},
	}
	if len(got) != len(want) {
		t.Fatalf("grouped rows = %#v", got)
	}
	for customer, counts := range want {
		if got[customer] != counts {
			t.Fatalf("customer %q = %v, want %v; all rows %#v", customer, got[customer], counts, got)
		}
	}
}

func TestMultiDatasetStitchPreservesMetricEmptyPolicyAcrossThreeBranches(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders(order_id VARCHAR, customer_id VARCHAR, segment VARCHAR, amount DOUBLE)",
		"INSERT INTO model.orders VALUES ('o1', 'a', 'consumer', 10), ('o2', 'b', 'business', 20)",
		"CREATE TABLE model.tags(tag_id VARCHAR, customer_id VARCHAR, segment VARCHAR, tag VARCHAR)",
		"INSERT INTO model.tags VALUES ('t1', 'a', 'consumer', 'new'), ('t2', 'c', 'consumer', 'vip'), ('t3', 'c', 'consumer', 'repeat')",
		"CREATE TABLE model.clicks(click_id VARCHAR, customer_id VARCHAR, segment VARCHAR)",
		"INSERT INTO model.clicks VALUES ('c1', 'a', 'consumer'), ('c2', 'd', 'business')",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("execute %q: %v", statement, err)
		}
	}

	model := executableMultiDatasetModel()
	model.Metrics["tag_count_null"] = semanticmodel.Metric{
		Type: "aggregate", Dataset: "tags", Aggregation: "count",
		Input: &semanticmodel.MetricInput{Field: "tags.tag_id"}, Empty: "null",
	}
	plan, err := mustNewCompiledPlanner(t, model).Plan(Request{
		Dimensions: []Field{{Field: "customer"}, {Field: "segment"}},
		Metrics:    []Field{{Field: "order_count"}, {Field: "tag_count_null"}, {Field: "click_count"}},
		Sort:       []Sort{{Field: "customer", Direction: "asc"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(plan.SQL, plan.Args...)
	if err != nil {
		t.Fatalf("execute three-branch empty-policy plan:\n%s\n%v", plan.SQL, err)
	}
	defer rows.Close()
	type counts struct {
		orders, tags, clicks sql.NullInt64
	}
	got := map[string]counts{}
	for rows.Next() {
		var customer, segment string
		var values counts
		if err := rows.Scan(&customer, &segment, &values.orders, &values.tags, &values.clicks); err != nil {
			t.Fatal(err)
		}
		got[customer+"/"+segment] = values
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got["a/consumer"].tags.Valid == false || got["a/consumer"].tags.Int64 != 1 {
		t.Fatalf("present nullable metric = %#v, want 1", got["a/consumer"].tags)
	}
	for _, key := range []string{"b/business", "d/business"} {
		if got[key].tags.Valid {
			t.Fatalf("missing nullable metric for %q = %#v, want SQL NULL", key, got[key].tags)
		}
	}
	if !got["b/business"].orders.Valid || got["b/business"].orders.Int64 != 1 || !got["b/business"].clicks.Valid || got["b/business"].clicks.Int64 != 0 {
		t.Fatalf("zero-policy one-sided row = %#v, want orders=1 clicks=0", got["b/business"])
	}
	if !got["d/business"].clicks.Valid || got["d/business"].clicks.Int64 != 1 || !got["d/business"].orders.Valid || got["d/business"].orders.Int64 != 0 {
		t.Fatalf("zero-policy one-sided row = %#v, want orders=0 clicks=1", got["d/business"])
	}
}

func executableMultiDatasetModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: "executable",
		Tables: map[string]semanticmodel.Table{
			"orders": {GrainEntity: "order_id", Entities: map[string]semanticmodel.ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{
				"order_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "segment": {Type: "string", Datatype: semanticmodel.DataTypeString}, "amount": {Type: "number", Datatype: semanticmodel.DataTypeDecimal},
			}},
			"tags": {GrainEntity: "tag_id", Entities: map[string]semanticmodel.ModelEntitySpec{"tag_id": {Type: "primary", Fields: []string{"tag_id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{
				"tag_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "segment": {Type: "string", Datatype: semanticmodel.DataTypeString}, "tag": {Type: "string", Datatype: semanticmodel.DataTypeString},
			}},
			"clicks": {GrainEntity: "click_id", Entities: map[string]semanticmodel.ModelEntitySpec{"click_id": {Type: "primary", Fields: []string{"click_id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{
				"click_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "segment": {Type: "string", Datatype: semanticmodel.DataTypeString},
			}},
			"customers": {GrainEntity: "customer_id", Entities: map[string]semanticmodel.ModelEntitySpec{"customer_id": {Type: "primary", Fields: []string{"customer_id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{
				"customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "state": {Type: "string", Datatype: semanticmodel.DataTypeString},
			}},
		},
		Relationships: []semanticmodel.Relationship{
			{ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"},
			{ID: "tags_customers", FromDataset: "tags", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"},
			{ID: "clicks_customers", FromDataset: "clicks", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {Model: "orders"}, "tags": {Model: "tags"}, "clicks": {Model: "clicks"}, "customers": {Model: "customers"},
		},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"customer": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{
				"orders": {Field: "orders.customer_id"}, "tags": {Field: "tags.customer_id"}, "clicks": {Field: "clicks.customer_id"},
			}},
			"segment": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{
				"orders": {Field: "orders.segment"}, "tags": {Field: "tags.segment"}, "clicks": {Field: "clicks.segment"},
			}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count":    {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, Empty: "zero"},
			"revenue":        {Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.amount"}, Empty: "zero"},
			"tag_count":      {Type: "aggregate", Dataset: "tags", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "tags.tag_id"}, Empty: "zero"},
			"click_count":    {Type: "aggregate", Dataset: "clicks", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "clicks.click_id"}, Empty: "zero"},
			"tags_per_order": {Type: "derived", Expression: "safe_divide(${tag_count}, ${order_count})"},
		},
	}
}
