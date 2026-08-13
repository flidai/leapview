package query

import (
	"database/sql"
	"fmt"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
)

func TestSpatialAggregationExecutesAcrossEveryGovernedRowBeforeFeatureCap(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders(order_id VARCHAR, customer_id VARCHAR, ordered_at TIMESTAMP, revenue DOUBLE, status VARCHAR, latitude DOUBLE, longitude DOUBLE)",
		"INSERT INTO model.orders SELECT 'order-' || i, 'customer-' || i, TIMESTAMP '2026-01-01', 1, 'paid', -30 + (i % 60), -120 + (i % 240) FROM range(10000) t(i)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := NewPlanner(testModel()).PlanSpatial(SpatialRequest{
		Table:      "orders",
		Dimensions: []Field{{Field: "orders.order_id", Alias: "order_id"}, {Field: "orders.latitude", Alias: "latitude"}, {Field: "orders.longitude", Alias: "longitude"}},
		Measures:   []Field{{Field: "revenue", Alias: "revenue"}},
		Latitude:   Field{Field: "orders.latitude", Alias: "latitude"}, Longitude: Field{Field: "orders.longitude", Alias: "longitude"},
		West: -180, South: -85, East: 180, North: 85, Width: 320, Height: 240, FeatureCap: 10, Precision: SpatialPrecisionAggregated,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := db.Query(plan.SQL, plan.Args...)
	if err != nil {
		t.Fatalf("execute spatial plan: %v\n%s", err, plan.SQL)
	}
	defer result.Close()
	var rows, total int
	var revenue float64
	for result.Next() {
		var orderID string
		var latitude, longitude, cellRevenue float64
		var cellTotal int
		if err := result.Scan(&orderID, &latitude, &longitude, &cellRevenue, &cellTotal); err != nil {
			t.Fatal(err)
		}
		_ = fmt.Sprintf("%s/%f/%f", orderID, latitude, longitude)
		rows++
		revenue += cellRevenue
		total = cellTotal
	}
	if err := result.Err(); err != nil {
		t.Fatal(err)
	}
	if rows == 0 || rows > 10 {
		t.Fatalf("aggregated rows = %d, want 1..10", rows)
	}
	if total != 10_000 || revenue != 10_000 {
		t.Fatalf("aggregated total/revenue = %d/%v, want 10000/10000", total, revenue)
	}
}

func TestSpatialAggregationExecutesAcrossAntimeridianViewport(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders(order_id VARCHAR, customer_id VARCHAR, ordered_at TIMESTAMP, revenue DOUBLE, status VARCHAR, latitude DOUBLE, longitude DOUBLE)",
		"INSERT INTO model.orders VALUES ('east', 'one', TIMESTAMP '2026-01-01', 1, 'paid', 0, 175), ('west', 'two', TIMESTAMP '2026-01-01', 1, 'paid', 0, -175), ('outside', 'three', TIMESTAMP '2026-01-01', 100, 'paid', 0, 0)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := NewPlanner(testModel()).PlanSpatial(SpatialRequest{
		Table: "orders",
		Dimensions: []Field{
			{Field: "orders.order_id", Alias: "order_id"},
			{Field: "orders.latitude", Alias: "latitude"},
			{Field: "orders.longitude", Alias: "longitude"},
		},
		Measures: []Field{{Field: "revenue", Alias: "revenue"}},
		Latitude: Field{Field: "orders.latitude", Alias: "latitude"}, Longitude: Field{Field: "orders.longitude", Alias: "longitude"},
		West: 170, South: -10, East: -170, North: 10, Width: 800, Height: 500, FeatureCap: 10, Precision: SpatialPrecisionAggregated,
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(plan.SQL, plan.Args...)
	if err != nil {
		t.Fatalf("execute antimeridian spatial plan: %v\n%s", err, plan.SQL)
	}
	defer rows.Close()
	var rendered, total int
	var revenue float64
	for rows.Next() {
		var orderID string
		var latitude, longitude, cellRevenue float64
		var cellTotal int
		if err := rows.Scan(&orderID, &latitude, &longitude, &cellRevenue, &cellTotal); err != nil {
			t.Fatal(err)
		}
		rendered++
		revenue += cellRevenue
		total = cellTotal
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if rendered == 0 || rendered > 10 || total != 2 || revenue != 2 {
		t.Fatalf("antimeridian result = rendered %d, total %d, revenue %v; want bounded rows over both matching points", rendered, total, revenue)
	}
}

func TestSpatialAggregationMillionRowsIsCompleteAndBounded(t *testing.T) {
	db := spatialScaleFixture(t, 1_000_000)
	defer db.Close()
	plan := spatialScalePlan(t, 5_000)

	result, err := db.Query(plan.SQL, plan.Args...)
	if err != nil {
		t.Fatalf("execute million-row spatial plan: %v\n%s", err, plan.SQL)
	}
	defer result.Close()
	var rows, total int
	var revenue float64
	for result.Next() {
		var orderID string
		var latitude, longitude, cellRevenue float64
		var cellTotal int
		if err := result.Scan(&orderID, &latitude, &longitude, &cellRevenue, &cellTotal); err != nil {
			t.Fatal(err)
		}
		rows++
		revenue += cellRevenue
		total = cellTotal
	}
	if err := result.Err(); err != nil {
		t.Fatal(err)
	}
	if rows == 0 || rows > 5_000 {
		t.Fatalf("rendered features = %d, want 1..5000", rows)
	}
	if total != 1_000_000 || revenue != 1_000_000 {
		t.Fatalf("complete cardinality/revenue = %d/%v, want 1000000/1000000", total, revenue)
	}
}

// BenchmarkSpatialInitialViewportMillionRows is the migration baseline for
// dynamic vector tiles. Keep the fixture, viewport, feature cap, and consumed
// result shape stable so the tiled implementation can replace the execution
// beneath this benchmark without changing the measured workload.
func BenchmarkSpatialInitialViewportMillionRows(b *testing.B) {
	db := spatialScaleFixture(b, 1_000_000)
	defer db.Close()
	plan := spatialScalePlan(b, 5_000)
	b.ResetTimer()
	for range b.N {
		result, err := db.Query(plan.SQL, plan.Args...)
		if err != nil {
			b.Fatal(err)
		}
		for result.Next() {
			var orderID string
			var latitude, longitude, revenue float64
			var total int
			if err := result.Scan(&orderID, &latitude, &longitude, &revenue, &total); err != nil {
				result.Close()
				b.Fatal(err)
			}
		}
		if err := result.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func spatialScaleFixture(t testing.TB, rows int) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders(order_id VARCHAR, customer_id VARCHAR, ordered_at TIMESTAMP, revenue DOUBLE, status VARCHAR, latitude DOUBLE, longitude DOUBLE)",
		fmt.Sprintf("INSERT INTO model.orders SELECT 'order-' || i, 'customer-' || i, TIMESTAMP '2026-01-01', 1, 'paid', -84 + (i %% 168000) / 1000.0, -179 + (i %% 358000) / 1000.0 FROM range(%d) t(i)", rows),
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	return db
}

func spatialScalePlan(t testing.TB, featureCap int) Plan {
	t.Helper()
	plan, err := NewPlanner(testModel()).PlanSpatial(SpatialRequest{
		Table:      "orders",
		Dimensions: []Field{{Field: "orders.order_id", Alias: "order_id"}, {Field: "orders.latitude", Alias: "latitude"}, {Field: "orders.longitude", Alias: "longitude"}},
		Measures:   []Field{{Field: "revenue", Alias: "revenue"}},
		Latitude:   Field{Field: "orders.latitude", Alias: "latitude"}, Longitude: Field{Field: "orders.longitude", Alias: "longitude"},
		West: -180, South: -85, East: 180, North: 85, Width: 1920, Height: 1080, FeatureCap: featureCap, Precision: SpatialPrecisionAggregated,
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
