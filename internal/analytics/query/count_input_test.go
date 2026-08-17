package query

import (
	"database/sql"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
)

// Count metrics count their declared input expression, so a nullable input is
// excluded even when the fact contains rows. Keep this invariant consistent
// across the single-fact, multi-fact, and bundled planners.
func TestCountInputExcludesNullAcrossPlannerModes(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders(order_id INTEGER, customer_id VARCHAR, segment VARCHAR, amount DOUBLE)",
		"INSERT INTO model.orders VALUES (1, 'a', 'consumer', 10), (NULL, 'a', 'consumer', 20), (3, 'b', 'business', 30)",
		"CREATE TABLE model.tags(tag_id INTEGER, customer_id VARCHAR, segment VARCHAR, tag VARCHAR)",
		"INSERT INTO model.tags VALUES (11, 'a', 'consumer', 'new'), (NULL, 'a', 'consumer', 'missing'), (13, 'b', 'business', 'repeat')",
		"CREATE TABLE model.clicks(click_id INTEGER, customer_id VARCHAR, segment VARCHAR)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	single, err := mustNewCompiledPlanner(t, testModel()).Plan(Request{
		Table: "orders", Metrics: []Field{{Field: "order_count", Alias: "value"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var singleCount int
	if err := db.QueryRow(single.SQL, single.Args...).Scan(&singleCount); err != nil {
		t.Fatalf("single-fact count: %v\n%s", err, single.SQL)
	}
	if singleCount != 2 {
		t.Fatalf("single-fact count = %d, want 2", singleCount)
	}

	multi, err := mustNewCompiledPlanner(t, executableMultiFactModel()).Plan(Request{Metrics: []Field{
		{Field: "order_count", Alias: "orders"},
		{Field: "tag_count", Alias: "tags"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var multiOrders, multiTags int
	if err := db.QueryRow(multi.SQL, multi.Args...).Scan(&multiOrders, &multiTags); err != nil {
		t.Fatalf("multi-fact count: %v\n%s", err, multi.SQL)
	}
	if multiOrders != 2 || multiTags != 2 {
		t.Fatalf("multi-fact counts = (%d, %d), want (2, 2)", multiOrders, multiTags)
	}

	bundle, err := mustNewCompiledPlanner(t, executableMultiFactModel()).PlanBundle([]BundleRequest{
		{ID: "orders", Request: Request{Table: "orders", Metrics: []Field{{Field: "order_count", Alias: "value"}}}},
		{ID: "totals", Request: Request{Metrics: []Field{{Field: "order_count", Alias: "orders"}, {Field: "tag_count", Alias: "tags"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := queryBundlePlan(db, bundle)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := bundle.Decode(rows)
	if err != nil {
		t.Fatal(err)
	}
	if got := decoded["orders"]; len(got) != 1 || got[0]["value"] != int64(2) {
		t.Fatalf("bundled order count = %#v, want 2", got)
	}
	if got := decoded["totals"]; len(got) != 1 || got[0]["orders"] != int64(2) || got[0]["tags"] != int64(2) {
		t.Fatalf("bundled counts = %#v, want (2, 2)", got)
	}
}
