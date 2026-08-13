package query

import (
	"database/sql"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestSpatialBucketRunsNonAdditiveMeasuresAtFinalCellGrain(t *testing.T) {
	model := testModel()
	model.Measures["average_revenue"] = semanticmodel.MetricMeasure{Fact: "orders", Aggregation: "avg", Input: semanticmodel.MeasureInput{Field: "orders.revenue"}}
	model.Measures["unique_customers"] = semanticmodel.MetricMeasure{Fact: "orders", Aggregation: "count_distinct", Input: semanticmodel.MeasureInput{Field: "orders.customer_id"}}
	planner := NewPlanner(model)
	plan, err := planner.Plan(Request{
		Table:         "orders",
		Dimensions:    []Field{{Field: "orders.latitude", Alias: "latitude"}, {Field: "orders.longitude", Alias: "longitude"}},
		Measures:      []Field{{Field: "average_revenue", Alias: "average_revenue"}, {Field: "unique_customers", Alias: "unique_customers"}},
		SpatialBucket: &SpatialBucket{Latitude: Field{Field: "orders.latitude", Alias: "latitude"}, Longitude: Field{Field: "orders.longitude", Alias: "longitude"}, Zoom: 4, CellPixels: 64},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"AVG(", "COUNT(DISTINCT", "COUNT(*) AS __spatial_count", "AS __spatial_coordinate_count", "AS __spatial_center_longitude", "AS __spatial_center_latitude", "AS __spatial_west", "AS __spatial_north", "GROUP BY 1, 2"} {
		if !strings.Contains(plan.SQL, want) {
			t.Fatalf("spatial bucket plan missing %q:\n%s", want, plan.SQL)
		}
	}
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders(order_id VARCHAR, customer_id VARCHAR, ordered_at TIMESTAMP, revenue DOUBLE, status VARCHAR, latitude DOUBLE, longitude DOUBLE)",
		"INSERT INTO model.orders VALUES ('one', 'same', TIMESTAMP '2026-01-01', 10, 'paid', -0.01, 0.01), ('two', 'same', TIMESTAMP '2026-01-01', 20, 'paid', -0.02, 0.02)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	var latitudeCell, longitudeCell, count, coordinateCount int
	var average, centerLongitude, centerLatitude, west, south, east, north float64
	var distinct int
	if err := db.QueryRow(plan.SQL, plan.Args...).Scan(&latitudeCell, &longitudeCell, &average, &distinct, &count, &coordinateCount, &centerLongitude, &centerLatitude, &west, &south, &east, &north); err != nil {
		t.Fatalf("execute spatial bucket: %v\n%s", err, plan.SQL)
	}
	if average != 15 || distinct != 1 || count != 2 || coordinateCount != 2 {
		t.Fatalf("cell measures = average %v distinct %d count %d coordinates %d", average, distinct, count, coordinateCount)
	}
	if centerLongitude != 0.015 || centerLatitude != -0.015 || west != 0.01 || east != 0.02 || south != -0.02 || north != -0.01 {
		t.Fatalf("cell occupied geometry = center %v,%v bounds %v,%v,%v,%v", centerLongitude, centerLatitude, west, south, east, north)
	}
}

func TestSpatialMetadataReturnsExactCoordinateGrainAndSemanticTotals(t *testing.T) {
	model := testModel()
	model.Measures["average_revenue"] = semanticmodel.MetricMeasure{Fact: "orders", Aggregation: "avg", Input: semanticmodel.MeasureInput{Field: "orders.revenue"}}
	model.Measures["unique_customers"] = semanticmodel.MetricMeasure{Fact: "orders", Aggregation: "count_distinct", Input: semanticmodel.MeasureInput{Field: "orders.customer_id"}}
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders(order_id VARCHAR, customer_id VARCHAR, ordered_at TIMESTAMP, revenue DOUBLE, status VARCHAR, latitude DOUBLE, longitude DOUBLE)",
		"INSERT INTO model.orders VALUES ('one', 'same', TIMESTAMP '2026-01-01', 10, 'paid', -1, 1), ('two', 'same', TIMESTAMP '2026-01-01', 20, 'paid', -1, 1), ('three', 'other', TIMESTAMP '2026-01-01', 30, 'paid', 2, 3), ('invalid', 'bad', TIMESTAMP '2026-01-01', 1000, 'paid', 89, 0)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := NewPlanner(model).PlanSpatialMetadata(SpatialMetadataRequest{
		Table: "orders", Latitude: Field{Field: "orders.latitude", Alias: "latitude"}, Longitude: Field{Field: "orders.longitude", Alias: "longitude"},
		Measures:   []Field{{Field: "average_revenue", Alias: "average_revenue"}, {Field: "unique_customers", Alias: "unique_customers"}},
		FeatureCap: 1, RawMinimumZoom: 0, MaximumZoom: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	var west, south, east, north float64
	var cardinality int
	var coordinateAverageMin, coordinateAverageMax, totalAverage float64
	var coordinateDistinctMin, coordinateDistinctMax, totalDistinct int
	var rawMinimumZoom int
	if err := db.QueryRow(plan.SQL, plan.Args...).Scan(&west, &south, &east, &north, &cardinality, &coordinateAverageMin, &coordinateAverageMax, &totalAverage, &coordinateDistinctMin, &coordinateDistinctMax, &totalDistinct, &rawMinimumZoom); err != nil {
		t.Fatalf("execute metadata plan: %v\n%s", err, plan.SQL)
	}
	if west != 1 || south != -1 || east != 3 || north != 2 || cardinality != 2 {
		t.Fatalf("metadata extent/cardinality = %v,%v,%v,%v / %d", west, south, east, north, cardinality)
	}
	if coordinateAverageMin != 15 || coordinateAverageMax != 30 || totalAverage != 20 || coordinateDistinctMin != 1 || coordinateDistinctMax != 1 || totalDistinct != 2 {
		t.Fatalf("metadata domains/totals = avg %v..%v total %v, distinct %d..%d total %d", coordinateAverageMin, coordinateAverageMax, totalAverage, coordinateDistinctMin, coordinateDistinctMax, totalDistinct)
	}
	if rawMinimumZoom != 1 {
		t.Fatalf("revision-wide raw minimum zoom = %d, want 1", rawMinimumZoom)
	}
}

func TestPlanSpatialTileAggregateUsesNativeMVTAndAlignedMetatile(t *testing.T) {
	plan, err := NewPlanner(testModel()).PlanSpatialTileAggregate(SpatialTileRequest{
		Table: "orders", Measures: []Field{{Field: "revenue", Alias: "revenue"}},
		Latitude: Field{Field: "orders.latitude", Alias: "latitude"}, Longitude: Field{Field: "orders.longitude", Alias: "longitude"},
		Zoom: 4, TargetZoom: 6, MetatileX: 4, MetatileY: 8, MetatileSize: 4, CellPixels: 48, Buffer: 768,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ST_TileEnvelope(4", "ST_AsMVTGeom", "ST_AsMVT(tile_features, 'primary'", "__lv_aggregate", "6 AS __lv_target_zoom", "COUNT(DISTINCT (", "__lv_coordinate_count_abbreviated", "CAST(__lv_center_longitude AS DOUBLE)", "CAST(__lv_center_latitude AS DOUBLE)", "CAST(__lv_west AS DOUBLE) AS __lv_west", "CONCAT('aggregate:4:'", "__lv_id", "GROUP BY __tile_x, __tile_y"} {
		if !strings.Contains(plan.SQL, want) {
			t.Fatalf("MVT plan missing %q:\n%s", want, plan.SQL)
		}
	}
	if len(plan.Args) != 4 {
		t.Fatalf("metatile bounds args = %#v, want four half-open bounds", plan.Args)
	}
	if strings.Contains(plan.SQL, "<< 58") {
		t.Fatalf("aggregate identity is an unsafe JavaScript integer:\n%s", plan.SQL)
	}
}

func TestSpatialTileAggregateRequiresForwardTargetZoom(t *testing.T) {
	request := SpatialTileRequest{
		Table: "orders", Measures: []Field{{Field: "revenue", Alias: "revenue"}},
		Latitude: Field{Field: "orders.latitude", Alias: "latitude"}, Longitude: Field{Field: "orders.longitude", Alias: "longitude"},
		Zoom: 4, TargetZoom: 4, MetatileX: 4, MetatileY: 8, MetatileSize: 4, CellPixels: 48, Buffer: 768,
	}
	if _, err := NewPlanner(testModel()).PlanSpatialTileAggregate(request); err == nil || !strings.Contains(err.Error(), "target zoom") {
		t.Fatalf("non-forward aggregate target error = %v", err)
	}
	request.TargetZoom = SpatialTileMaximumZoom + 1
	if _, err := NewPlanner(testModel()).PlanSpatialTileAggregate(request); err == nil || !strings.Contains(err.Error(), "target zoom") {
		t.Fatalf("out-of-range aggregate target error = %v", err)
	}
}

func TestSpatialTilePlansCrossFactCoordinatesWithoutTableScope(t *testing.T) {
	model := testModel()
	customers := model.Tables["customers"]
	customers.Dimensions["latitude"] = semanticmodel.MetricDimension{Expr: "latitude", Type: "number"}
	customers.Dimensions["longitude"] = semanticmodel.MetricDimension{Expr: "longitude", Type: "number"}
	model.Tables["customers"] = customers
	plan, err := NewPlanner(model).PlanSpatialTileAggregate(SpatialTileRequest{
		Measures: []Field{{Field: "order_count", Alias: "order_count"}},
		Latitude: Field{Field: "customers.latitude", Alias: "latitude"}, Longitude: Field{Field: "customers.longitude", Alias: "longitude"},
		Zoom: 2, MetatileX: 0, MetatileY: 0, MetatileSize: 4, CellPixels: 48, Buffer: 768,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "LEFT JOIN model.customers") || !strings.Contains(plan.SQL, "COUNT(*)") {
		t.Fatalf("cross-fact spatial plan did not preserve the relationship and measure semantics:\n%s", plan.SQL)
	}
}

func TestSpatialMetatileBoundsClampToMercatorWorld(t *testing.T) {
	west, south, east, north := spatialMetatileBounds(0, 0, 0, 4)
	if west != -180 || east != 180 || south < -mercatorMaximumLatitude-1e-9 || north > mercatorMaximumLatitude+1e-9 {
		t.Fatalf("world bounds = %v,%v,%v,%v", west, south, east, north)
	}
	if _, err := NewPlanner(testModel()).PlanSpatialTileAggregate(SpatialTileRequest{
		Table: "orders", Latitude: Field{Field: "orders.latitude"}, Longitude: Field{Field: "orders.longitude"},
		Zoom: 3, MetatileX: 1, MetatileY: 0, MetatileSize: 4, CellPixels: 64,
	}); err == nil || !strings.Contains(err.Error(), "align") {
		t.Fatalf("unaligned metatile error = %v", err)
	}
}

func TestSpatialTilePlanExecutesNativeMVT(t *testing.T) {
	db := spatialScaleFixture(t, 1_000)
	defer db.Close()
	for _, statement := range []string{"INSTALL spatial FROM core", "LOAD spatial"} {
		if _, err := db.Exec(statement); err != nil {
			t.Skipf("DuckDB spatial extension unavailable: %v", err)
		}
	}
	plan, err := NewPlanner(testModel()).PlanSpatialTileAggregate(SpatialTileRequest{
		Table: "orders", Measures: []Field{{Field: "revenue", Alias: "revenue"}},
		Latitude: Field{Field: "orders.latitude", Alias: "latitude"}, Longitude: Field{Field: "orders.longitude", Alias: "longitude"},
		Zoom: 0, MetatileX: 0, MetatileY: 0, MetatileSize: 4, CellPixels: 48, Buffer: 768,
	})
	if err != nil {
		t.Fatal(err)
	}
	var x, y, features int
	var tile []byte
	if err := db.QueryRow(plan.SQL, plan.Args...).Scan(&x, &y, &features, &tile); err != nil {
		t.Fatalf("execute native MVT plan: %v\n%s", err, plan.SQL)
	}
	if x != 0 || y != 0 || features <= 0 || len(tile) == 0 || len(tile) > 512*1024 {
		t.Fatalf("MVT result = %d/%d, %d features, %d bytes", x, y, features, len(tile))
	}
}

func TestSpatialRawTileFallsBackWithoutTruncating(t *testing.T) {
	db := spatialScaleFixture(t, 1_000)
	defer db.Close()
	for _, statement := range []string{"INSTALL spatial FROM core", "LOAD spatial"} {
		if _, err := db.Exec(statement); err != nil {
			t.Skipf("DuckDB spatial extension unavailable: %v", err)
		}
	}
	request := SpatialTileRawRequest{
		Table:      "orders",
		Dimensions: []Field{{Field: "orders.order_id", Alias: "order_id"}, {Field: "orders.latitude", Alias: "latitude"}, {Field: "orders.longitude", Alias: "longitude"}},
		Measures:   []Field{{Field: "revenue", Alias: "revenue"}}, Identity: []Field{{Field: "orders.order_id", Alias: "order_id"}},
		Latitude: Field{Field: "orders.latitude", Alias: "latitude"}, Longitude: Field{Field: "orders.longitude", Alias: "longitude"},
		Zoom: 0, MetatileX: 0, MetatileY: 0, MetatileSize: 4, FeatureCap: 500, Buffer: 768,
	}
	plan, err := NewPlanner(testModel()).PlanSpatialTileRaw(request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "CONCAT('raw:'") {
		t.Fatalf("raw identity is not encoded as an exact promoted string:\n%s", plan.SQL)
	}
	var x, y, count int
	var tile []byte
	if err := db.QueryRow(plan.SQL, plan.Args...).Scan(&x, &y, &count, &tile); err != nil {
		t.Fatalf("execute raw fallback plan: %v\n%s", err, plan.SQL)
	}
	if count != 1_000 || tile != nil {
		t.Fatalf("overflow raw tile = count %d, %d bytes; want untruncated count and nil MVT", count, len(tile))
	}
	request.FeatureCap = 2_000
	plan, err = NewPlanner(testModel()).PlanSpatialTileRaw(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(plan.SQL, plan.Args...).Scan(&x, &y, &count, &tile); err != nil {
		t.Fatalf("execute raw MVT plan: %v\n%s", err, plan.SQL)
	}
	if count != 1_000 || len(tile) == 0 {
		t.Fatalf("raw tile = count %d, %d bytes", count, len(tile))
	}
}

func TestSpatialTileBudgetMeasuresRevisionWideEncodedBytes(t *testing.T) {
	db := spatialScaleFixture(t, 1_000)
	defer db.Close()
	for _, statement := range []string{"INSTALL spatial FROM core", "LOAD spatial"} {
		if _, err := db.Exec(statement); err != nil {
			t.Skipf("DuckDB spatial extension unavailable: %v", err)
		}
	}
	request := SpatialTileBudgetRequest{
		Table:      "orders",
		Dimensions: []Field{{Field: "orders.order_id", Alias: "order_id"}, {Field: "orders.latitude", Alias: "latitude"}, {Field: "orders.longitude", Alias: "longitude"}},
		Measures:   []Field{{Field: "revenue", Alias: "revenue"}}, Identity: []Field{{Field: "orders.order_id", Alias: "order_id"}},
		Latitude: Field{Field: "orders.latitude", Alias: "latitude"}, Longitude: Field{Field: "orders.longitude", Alias: "longitude"},
		Zoom: 0, FeatureCap: 500, MaximumBytes: 512 * 1024, Buffer: 768,
	}
	plan, err := NewPlanner(testModel()).PlanSpatialTileBudget(request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "MAX(OCTET_LENGTH(e.mvt))") || !strings.Contains(plan.SQL, SpatialTileMaximumBytesColumn) {
		t.Fatalf("budget plan does not measure encoded child bytes:\n%s", plan.SQL)
	}
	var maximumFeatures, maximumBytes int
	if err := db.QueryRow(plan.SQL, plan.Args...).Scan(&maximumFeatures, &maximumBytes); err != nil {
		t.Fatalf("execute feature-overflow budget plan: %v\n%s", err, plan.SQL)
	}
	if maximumFeatures != 1_000 || maximumBytes != 0 {
		t.Fatalf("feature-overflow budget = %d features, %d bytes; want 1000 features and skipped encoding", maximumFeatures, maximumBytes)
	}
	request.FeatureCap = 2_000
	plan, err = NewPlanner(testModel()).PlanSpatialTileBudget(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(plan.SQL, plan.Args...).Scan(&maximumFeatures, &maximumBytes); err != nil {
		t.Fatalf("execute encoded budget plan: %v\n%s", err, plan.SQL)
	}
	if maximumFeatures != 1_000 || maximumBytes <= 0 {
		t.Fatalf("encoded budget = %d features, %d bytes", maximumFeatures, maximumBytes)
	}
	if _, err := db.Exec("UPDATE model.orders SET order_id = repeat('x', 1000) || order_id"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(plan.SQL, plan.Args...).Scan(&maximumFeatures, &maximumBytes); err != nil {
		t.Fatalf("execute encoded byte-overflow plan: %v\n%s", err, plan.SQL)
	}
	if maximumFeatures > request.FeatureCap || maximumBytes <= 512*1024 {
		t.Fatalf("forced byte overflow = %d features, %d bytes; want a feature-safe tile above 512 KiB", maximumFeatures, maximumBytes)
	}
}

func BenchmarkSpatialInitialViewportMillionRowsMVT(b *testing.B) {
	db := spatialMVTScaleFixture(b, 1_000_000)
	defer db.Close()
	plan := spatialAggregateTilePlan(b, 0, 0, 0)
	benchmarkSpatialTilePlan(b, db, plan)
}

func BenchmarkSpatialOneColumnPanMillionRowsMVT(b *testing.B) {
	db := spatialMVTScaleFixture(b, 1_000_000)
	defer db.Close()
	plan := spatialAggregateTilePlan(b, 4, 8, 4)
	benchmarkSpatialTilePlan(b, db, plan)
}

func BenchmarkSpatialRawHighZoomMillionRowsMVT(b *testing.B) {
	db := spatialMVTScaleFixture(b, 1_000_000)
	defer db.Close()
	plan, err := NewPlanner(testModel()).PlanSpatialTileRaw(SpatialTileRawRequest{
		Table:      "orders",
		Dimensions: []Field{{Field: "orders.order_id", Alias: "order_id"}, {Field: "orders.latitude", Alias: "latitude"}, {Field: "orders.longitude", Alias: "longitude"}},
		Measures:   []Field{{Field: "revenue", Alias: "revenue"}}, Identity: []Field{{Field: "orders.order_id", Alias: "order_id"}},
		Latitude: Field{Field: "orders.latitude", Alias: "latitude"}, Longitude: Field{Field: "orders.longitude", Alias: "longitude"},
		Zoom: 10, MetatileX: 240, MetatileY: 512, MetatileSize: 4, FeatureCap: 5_000, Buffer: 768,
	})
	if err != nil {
		b.Fatal(err)
	}
	benchmarkSpatialTilePlan(b, db, plan)
}

func spatialMVTScaleFixture(t testing.TB, rows int) *sql.DB {
	t.Helper()
	db := spatialScaleFixture(t, rows)
	for _, statement := range []string{"INSTALL spatial FROM core", "LOAD spatial"} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Skipf("DuckDB spatial extension unavailable: %v", err)
		}
	}
	return db
}

func spatialAggregateTilePlan(t testing.TB, zoom, x, y int) Plan {
	t.Helper()
	plan, err := NewPlanner(testModel()).PlanSpatialTileAggregate(SpatialTileRequest{
		Table: "orders", Measures: []Field{{Field: "revenue", Alias: "revenue"}},
		Latitude: Field{Field: "orders.latitude", Alias: "latitude"}, Longitude: Field{Field: "orders.longitude", Alias: "longitude"},
		Zoom: zoom, MetatileX: x, MetatileY: y, MetatileSize: 4, CellPixels: 48, Buffer: 768,
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func benchmarkSpatialTilePlan(b *testing.B, db *sql.DB, plan Plan) {
	b.Helper()
	var encodedBytes, features int64
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		rows, err := db.Query(plan.SQL, plan.Args...)
		if err != nil {
			b.Fatal(err)
		}
		for rows.Next() {
			var x, y, count int
			var tile []byte
			if err := rows.Scan(&x, &y, &count, &tile); err != nil {
				rows.Close()
				b.Fatal(err)
			}
			encodedBytes += int64(len(tile))
			features += int64(count)
		}
		if err := rows.Close(); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if b.N > 0 {
		b.ReportMetric(float64(encodedBytes)/float64(b.N), "mvt-bytes/op")
		b.ReportMetric(float64(features)/float64(b.N), "features/op")
	}
}
