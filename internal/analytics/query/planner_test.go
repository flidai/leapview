package query

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestPlannerScalarMultiDatasetAggregatesDatasetsIndependently(t *testing.T) {
	plan, err := mustNewCompiledPlanner(t, testModel()).Plan(Request{Metrics: []Field{
		{Field: "revenue", Alias: "revenue"},
		{Field: "tag_count", Alias: "tags"},
		{Field: "tags_per_order", Alias: "ratio"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	explain, err := plan.Explain()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[AggregateMetrics]", "[StitchAggregates]", "[ComputeDerived]"} {
		if !strings.Contains(explain, want) {
			t.Fatalf("PlanIR missing %q:\n%s", want, explain)
		}
	}
	if strings.Count(explain, "[AggregateMetrics]") != 2 {
		t.Fatalf("scalar multi-root PlanIR did not aggregate each root independently:\n%s", explain)
	}
	if plan.Mode != "multi_dataset" || strings.Join(plan.Datasets, ",") != "orders,tags" {
		t.Fatalf("plan mode/datasets = %q/%v", plan.Mode, plan.Datasets)
	}
}

func TestPlannerAggregatePaginationAlwaysHasTotalOrdering(t *testing.T) {
	plan, err := mustNewCompiledPlanner(t, testModel()).Plan(Request{
		Dimensions: []Field{{Field: "customer_state", Alias: "label"}},
		Metrics:    []Field{{Field: "order_count", Alias: "value"}},
		Limit:      1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.EffectiveOrdering) != 2 || plan.EffectiveOrdering[0].Field != "label" || plan.EffectiveOrdering[1].Field != "value" {
		t.Fatalf("effective ordering = %#v", plan.EffectiveOrdering)
	}
}

func TestPlannerExplicitSortRemainsPrimaryAndGetsTieBreaker(t *testing.T) {
	plan, err := mustNewCompiledPlanner(t, testModel()).Plan(Request{
		Dimensions: []Field{{Field: "customer_state", Alias: "label"}},
		Metrics:    []Field{{Field: "order_count", Alias: "value"}},
		Sort:       []Sort{{Field: "value", Direction: "desc"}},
		Limit:      1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.EffectiveOrdering) != 2 || plan.EffectiveOrdering[0].Field != "value" || plan.EffectiveOrdering[0].Direction != "desc" || plan.EffectiveOrdering[1].Field != "label" {
		t.Fatalf("effective ordering = %#v", plan.EffectiveOrdering)
	}
}

func TestPlannerGroupedMultiDatasetUsesFullOuterStitch(t *testing.T) {
	plan, err := mustNewCompiledPlanner(t, testModel()).Plan(Request{
		Dimensions: []Field{{Field: "customer_state", Alias: "state"}},
		Metrics:    []Field{{Field: "order_count"}, {Field: "tag_count"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	explain, err := plan.Explain()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(explain, "[AggregateMetrics]") != 2 || !strings.Contains(explain, "[StitchAggregates]") || !strings.Contains(explain, "keys=[customer_state]") {
		t.Fatalf("grouped multi-root PlanIR:\n%s", explain)
	}
	if strings.Join(plan.StitchDimensions, ",") != "customer_state" {
		t.Fatalf("stitch dimensions = %v", plan.StitchDimensions)
	}
	if strings.Join(plan.RelationshipPaths, ",") != "orders:orders_customers,tags:tags_customers" {
		t.Fatalf("relationship paths = %v", plan.RelationshipPaths)
	}
}

func TestPlannerConformedFilterPropagatesToEveryDataset(t *testing.T) {
	plan, err := mustNewCompiledPlanner(t, testModel()).Plan(Request{
		Metrics: []Field{{Field: "order_count"}, {Field: "tag_count"}},
		Filters: []Filter{{Field: "customer_state", Operator: "equals", Values: []any{"DK"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Args) != 2 || plan.Args[0] != "DK" || plan.Args[1] != "DK" {
		t.Fatalf("args = %#v", plan.Args)
	}
	explain, err := plan.Explain()
	if err != nil || strings.Count(explain, "[FilterRows]") != 2 {
		t.Fatalf("conformed filter PlanIR = %q, error=%v", explain, err)
	}
}

func TestPlannerDimensionOnlyQueryUsesDatasetsCompatibleWithConformedFilters(t *testing.T) {
	model := testModel()
	model.Dimensions["order_status"] = semanticmodel.SemanticDimension{Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{
		"orders": {Field: "orders.status"},
	}}

	plan, err := mustNewCompiledPlanner(t, model).Plan(Request{
		Dimensions: []Field{{Field: "customer_state", Alias: "value"}},
		Filters:    []Filter{{Field: "order_status", Operator: "in", Values: []any{"canceled", "created"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != "single_dataset" || strings.Join(plan.Datasets, ",") != "orders" {
		t.Fatalf("plan mode/datasets = %q/%v, want single_dataset/[orders]", plan.Mode, plan.Datasets)
	}
	if len(plan.Args) != 2 || plan.Args[0] != "canceled" || plan.Args[1] != "created" {
		t.Fatalf("status filter args = %#v", plan.Args)
	}
}

func TestPlannerDimensionOnlyModelWithoutMetrics(t *testing.T) {
	model := testModel()
	model.Metrics = map[string]semanticmodel.Metric{}
	plan, err := mustNewCompiledPlanner(t, model).Plan(Request{
		Dimensions: []Field{{Field: "customer_state", Alias: "state"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != "multi_dataset" || strings.Join(plan.Datasets, ",") != "orders,tags" {
		t.Fatalf("plan mode/datasets = %q/%v, want multi_dataset/[orders tags]", plan.Mode, plan.Datasets)
	}
	if plan.IR == nil {
		t.Fatal("dimension-only metricless plan omitted PlanIR")
	}
}

func TestPlannerConformedSelectionEntriesPropagateToEveryDataset(t *testing.T) {
	plan, err := mustNewCompiledPlanner(t, testModel()).Plan(Request{
		Metrics: []Field{{Field: "order_count"}, {Field: "tag_count"}},
		Filters: []Filter{{Groups: []FilterGroup{
			{Filters: []Filter{{Field: "customer_state", Operator: "equals", Values: []any{"DK"}}}},
			{Filters: []Filter{{Field: "customer_state", Operator: "equals", Values: []any{"SE"}}}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []any{"DK", "SE", "DK", "SE"}
	if fmt.Sprint(plan.Args) != fmt.Sprint(wantArgs) {
		t.Fatalf("args = %#v, want %#v", plan.Args, wantArgs)
	}
}

func TestPlannerDatasetLocalSelectionFiltersOnlyNamedDataset(t *testing.T) {
	plan, err := mustNewCompiledPlanner(t, testModel()).Plan(Request{
		Metrics: []Field{{Field: "order_count"}, {Field: "tag_count"}},
		Filters: []Filter{{Field: "orders.status", Dataset: "orders", Operator: "equals", Values: []any{"paid"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Args) != 1 || plan.Args[0] != "paid" {
		t.Fatalf("args = %#v, want [paid]", plan.Args)
	}
}

func TestPlannerRequiresDatasetForLocalMultiDatasetFilter(t *testing.T) {
	_, err := mustNewCompiledPlanner(t, testModel()).Plan(Request{
		Metrics: []Field{{Field: "order_count"}, {Field: "tag_count"}},
		Filters: []Filter{{Field: "orders.status", Operator: "equals", Values: []any{"paid"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "requires dataset") {
		t.Fatalf("error = %v", err)
	}
}

func TestPlannerRejectsMismatchedDatasetOnSingleDatasetFilter(t *testing.T) {
	_, err := mustNewCompiledPlanner(t, testModel()).PlanRows(RowRequest{
		Dataset:    "orders",
		Dimensions: []Field{{Field: "orders.order_id"}},
		Filters:    []Filter{{Field: "customer_state", Dataset: "tags", Operator: "equals", Values: []any{"DK"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "does not match query dataset") {
		t.Fatalf("error = %v", err)
	}
}

func TestPlannerTableScopeRejectsOtherDatasetDependencies(t *testing.T) {
	_, err := mustNewCompiledPlanner(t, testModel()).Plan(Request{
		Dataset: "orders",
		Metrics: []Field{{Field: "tags_per_order"}},
	})
	if err == nil || !strings.Contains(err.Error(), "selects dependency from dataset") {
		t.Fatalf("error = %v", err)
	}
}

func TestPlannerRejectsLocalDimensionInMultiDatasetQuery(t *testing.T) {
	_, err := mustNewCompiledPlanner(t, testModel()).Plan(Request{
		Dimensions: []Field{{Field: "orders.status"}},
		Metrics:    []Field{{Field: "order_count"}, {Field: "tag_count"}},
	})
	if err == nil || !strings.Contains(err.Error(), "qualified local dimension") {
		t.Fatalf("error = %v", err)
	}
}

func TestPlannerUsesExplicitBindingPathInAmbiguousGraph(t *testing.T) {
	model := testModel()
	orders := model.Tables["orders"]
	orders.Dimensions["billing_customer_id"] = semanticmodel.MetricDimension{Datatype: semanticmodel.DataTypeInteger}
	model.Tables["orders"] = orders
	model.Relationships = append(model.Relationships, semanticmodel.Relationship{
		ID: "orders_billing_customers", FromDataset: "orders", FromFields: []string{"billing_customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one",
	})
	state := model.Dimensions["customer_state"]
	state.Bindings["orders"] = semanticmodel.DimensionBinding{Field: "customers.state", Path: []string{"orders_customers"}}
	model.Dimensions["customer_state"] = state

	plan, err := mustNewCompiledPlanner(t, model).Plan(Request{
		Dimensions: []Field{{Field: "customer_state"}},
		Metrics:    []Field{{Field: "order_count"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(plan.RelationshipPaths, ",") != "orders:orders_customers" {
		t.Fatalf("explicit relationship paths = %v", plan.RelationshipPaths)
	}
}

func TestPlannerUsesDistinctAliasesForRolePlayingDimensionPaths(t *testing.T) {
	plan, err := mustNewCompiledPlanner(t, rolePlayingDateModel()).Plan(Request{
		Dimensions: []Field{{Field: "order_date", Alias: "order_date"}, {Field: "ship_date", Alias: "ship_date"}},
		Metrics:    []Field{{Field: "order_count"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(plan.RelationshipPaths, ",") != "orders:orders_order_date,orders:orders_ship_date" {
		t.Fatalf("relationship paths = %v", plan.RelationshipPaths)
	}

	reversed, err := mustNewCompiledPlanner(t, rolePlayingDateModel()).Plan(Request{
		Dimensions: []Field{{Field: "ship_date"}, {Field: "order_date"}},
		Metrics:    []Field{{Field: "order_count"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(reversed.RelationshipPaths, ",") != "orders:orders_order_date,orders:orders_ship_date" {
		t.Fatalf("reversed relationship paths = %v", reversed.RelationshipPaths)
	}
}

func TestPlannerKeepsRolePlayingFilterPathsDistinct(t *testing.T) {
	plan, err := mustNewCompiledPlanner(t, rolePlayingDateModel()).Plan(Request{
		Metrics: []Field{{Field: "order_count"}},
		Filters: []Filter{
			{Field: "order_date", Operator: "equals", Values: []any{"2026-07-01"}},
			{Field: "ship_date", Operator: "equals", Values: []any{"2026-07-02"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Args) != 2 || plan.Args[0] != "2026-07-01" || plan.Args[1] != "2026-07-02" {
		t.Fatalf("args = %#v", plan.Args)
	}
	if strings.Join(plan.RelationshipPaths, ",") != "orders:orders_order_date,orders:orders_ship_date" {
		t.Fatalf("filter relationship paths = %v", plan.RelationshipPaths)
	}
}

func TestPlannerRowsKeepsRolePlayingDimensionPathsDistinct(t *testing.T) {
	plan, err := mustNewCompiledPlanner(t, rolePlayingDateModel()).PlanRows(RowRequest{
		Dataset:    "orders",
		Dimensions: []Field{{Field: "order_date"}, {Field: "ship_date"}},
		Metrics:    []Field{{Field: "order_count"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	explain, err := plan.Explain()
	if err != nil || !strings.Contains(explain, "path=orders_order_date") || !strings.Contains(explain, "path=orders_ship_date") {
		t.Fatalf("role-playing row PlanIR = %q, error=%v", explain, err)
	}
}

func TestPlannerRawValuesKeepsRolePlayingDimensionPathsDistinct(t *testing.T) {
	plan, err := mustNewCompiledPlanner(t, rolePlayingDateModel()).PlanRawValues(RawValueRequest{
		Dataset:    "orders",
		Dimensions: []Field{{Field: "order_date", Alias: "order_date"}, {Field: "ship_date", Alias: "ship_date"}},
		Metric:     Field{Field: "order_count"},
	})
	if err != nil {
		t.Fatal(err)
	}
	explain, err := plan.Explain()
	if err != nil || !strings.Contains(explain, "path=orders_order_date") || !strings.Contains(explain, "path=orders_ship_date") {
		t.Fatalf("role-playing raw PlanIR = %q, error=%v", explain, err)
	}
}

func TestPlannerRowsAppliesPhysicalMaskToPathResolvedDimension(t *testing.T) {
	plan, err := mustNewCompiledPlanner(t, rolePlayingDateModel()).PlanRows(RowRequest{
		Dataset:     "orders",
		Dimensions:  []Field{{Field: "order_date", Alias: "order_date"}},
		ColumnMasks: []ColumnMask{{Field: "dates.date_value", Mask: "null"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, `NULL AS "order_date"`) {
		t.Fatalf("physical dimension mask was not applied:\n%s", plan.SQL)
	}
}

func TestPlannerCountKeepsRolePlayingFilterPathsDistinct(t *testing.T) {
	plan, err := mustNewCompiledPlanner(t, rolePlayingDateModel()).PlanCount(CountRequest{
		Dataset: "orders",
		Filters: []Filter{
			{Field: "order_date", Operator: "equals", Values: []any{"2026-08-01"}},
			{Field: "ship_date", Operator: "equals", Values: []any{"2026-08-02"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	explain, err := plan.Explain()
	if err != nil || !strings.Contains(explain, "path=orders_order_date") || !strings.Contains(explain, "path=orders_ship_date") {
		t.Fatalf("role-playing count PlanIR = %q, error=%v", explain, err)
	}
}

func TestPlannerResolvesDatasetDefaultTimeDimensionForAggregateMetric(t *testing.T) {
	model := testModel()
	model.Datasets = map[string]semanticmodel.SemanticDatasetSpec{
		"orders": {Model: "orders", DefaultTimeDimension: "activity_date"}, "tags": {Model: "tags"}, "customers": {Model: "customers"},
	}
	model.Dimensions["activity_date"] = semanticmodel.SemanticDimension{
		Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ, NativeGrain: "day", Grains: []string{"day"},
		Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.ordered_at"}},
	}
	planner := mustNewCompiledPlanner(t, model)
	resolved, err := planner.resolvedAggregateMetric("revenue")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.TimeDimension != "activity_date" {
		t.Fatalf("effective metric time dimension = %q, want activity_date", resolved.TimeDimension)
	}

	metric := model.Metrics["revenue"]
	metric.TimeDimension = "activity_date_override"
	model.Dimensions["activity_date_override"] = model.Dimensions["activity_date"]
	model.Metrics["revenue"] = metric
	resolved, err = mustNewCompiledPlanner(t, model).resolvedAggregateMetric("revenue")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.TimeDimension != "activity_date_override" {
		t.Fatalf("explicit metric time dimension = %q, want activity_date_override", resolved.TimeDimension)
	}
}

func TestPlannerRejectsRelationshipKeyLogicalDatatypeMismatch(t *testing.T) {
	model := testModel()
	orders := model.Tables["orders"]
	orders.Dimensions["customer_id"] = semanticmodel.MetricDimension{Type: "number", Datatype: semanticmodel.DataTypeInteger}
	model.Tables["orders"] = orders
	customers := model.Tables["customers"]
	customers.Dimensions["customer_id"] = semanticmodel.MetricDimension{Type: "number", Datatype: semanticmodel.DataTypeDecimal}
	model.Tables["customers"] = customers
	_, err := NewCompiledPlanner(model)
	if err == nil {
		_, err = mustNewCompiledPlanner(t, model).Plan(Request{
			Dimensions: []Field{{Field: "customer_state"}},
			Metrics:    []Field{{Field: "order_count"}},
		})
	}
	if err == nil || !strings.Contains(err.Error(), "incompatible") && !strings.Contains(err.Error(), "relationship tuple field") {
		t.Fatalf("relationship key datatype mismatch accepted: %v", err)
	}
}

func TestPlannerRowAndRawQueriesStaySingleDataset(t *testing.T) {
	planner := mustNewCompiledPlanner(t, testModel())
	row, err := planner.PlanRows(RowRequest{
		Dataset: "orders", Dimensions: []Field{{Field: "orders.order_id"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	explain, err := row.Explain()
	if err != nil || !strings.Contains(explain, "[ScanDataset]") || strings.Contains(explain, "[StitchAggregates]") {
		t.Fatalf("row PlanIR = %q, error=%v", explain, err)
	}
	_, err = planner.PlanRawValues(RawValueRequest{Dataset: "orders", Metric: Field{Field: "order_count"}})
	if err != nil {
		t.Fatalf("raw canonical count error = %v", err)
	}
}

func TestPlannerRawValuesApplyNamedMetricPopulation(t *testing.T) {
	model := testModel()
	model.Filters = map[string]semanticmodel.SemanticFilterSpec{
		"danish_customers": {Field: "customers.state", Operator: "equals", Value: "DK", Path: []string{"orders_customers"}},
	}
	revenue := model.Metrics["revenue"]
	revenue.Where = []string{"danish_customers"}
	model.Metrics["revenue"] = revenue

	plan, err := mustNewCompiledPlanner(t, model).PlanRawValues(RawValueRequest{Dataset: "orders", Metric: Field{Field: "revenue"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"LEFT JOIN model.customers",
		`"r2"."customer_id" IS NOT NULL`,
		`"r2"."state" = ?`,
	} {
		if !strings.Contains(plan.SQL, want) {
			t.Fatalf("raw metric population SQL missing %q:\n%s", want, plan.SQL)
		}
	}
	if len(plan.Args) != 1 || plan.Args[0] != "DK" {
		t.Fatalf("raw metric population args = %#v, want [DK]", plan.Args)
	}
}

func TestPlannerExecutesTimezoneAndSundayWeekSemantics(t *testing.T) {
	model := testModel()
	model.Dimensions["local_week"] = semanticmodel.SemanticDimension{
		Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ, NativeGrain: "week", Grains: []string{"week"}, Calendar: "gregorian", Timezone: "America/Los_Angeles", WeekStart: "sunday",
		Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.ordered_at"}},
	}
	plan, err := mustNewCompiledPlanner(t, model).Plan(Request{Metrics: []Field{{Field: "order_count", Alias: "orders"}}, Time: Time{Field: "local_week", Grain: "week", Alias: "week"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "timezone('America/Los_Angeles'") || !strings.Contains(plan.SQL, "INTERVAL 1 DAY") {
		t.Fatalf("timezone/week SQL = %s", plan.SQL)
	}
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders(order_id INTEGER, ordered_at TIMESTAMPTZ)",
		"INSERT INTO model.orders VALUES (1, TIMESTAMPTZ '2026-01-04 07:30:00+00'), (2, TIMESTAMPTZ '2026-01-04 23:30:00+00'), (3, TIMESTAMPTZ '2026-01-05 08:30:00+00')",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.Query(plan.SQL, plan.Args...)
	if err != nil {
		t.Fatalf("execute timezone/week plan: %v\n%s", err, plan.SQL)
	}
	defer rows.Close()
	counts := []int{}
	for rows.Next() {
		var week time.Time
		var count int
		if err := rows.Scan(&week, &count); err != nil {
			t.Fatal(err)
		}
		counts = append(counts, count)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(counts, []int{1, 2}) {
		t.Fatalf("timezone/week counts = %#v, want [1 2]", counts)
	}
}

func TestPlannerAppliesSpatialInteractionPredicateBeforeAggregation(t *testing.T) {
	plan, err := mustNewCompiledPlanner(t, testModel()).Plan(Request{
		Dataset: "orders", Metrics: []Field{{Field: "order_count"}},
		Filters: []Filter{{Spatial: &SpatialFilter{
			Kind: "radius", LatitudeField: "orders.latitude", LongitudeField: "orders.longitude", Dataset: "orders",
			Center: SpatialPoint{Longitude: -46.63, Latitude: -23.55}, RadiusMeters: 25_000,
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ASIN", `RADIANS("orders"."latitude" - ?)`, `RADIANS("orders"."longitude" - ?)`} {
		if !strings.Contains(plan.SQL, want) {
			t.Fatalf("spatial interaction SQL missing %q:\n%s", want, plan.SQL)
		}
	}
	if len(plan.Args) != 4 {
		t.Fatalf("spatial interaction args = %#v", plan.Args)
	}
}

func testModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: "commerce",
		Tables: map[string]semanticmodel.Table{
			"orders": {GrainEntity: "order", Entities: map[string]semanticmodel.ModelEntitySpec{"order": {Type: "primary", Fields: []string{"order_id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{
				"order_id": {Datatype: semanticmodel.DataTypeInteger}, "customer_id": {Datatype: semanticmodel.DataTypeInteger},
				"ordered_at": {Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ}, "revenue": {Type: "number", Datatype: semanticmodel.DataTypeDecimal},
				"status": {Type: "string", Datatype: semanticmodel.DataTypeString}, "latitude": {Type: "number", Datatype: semanticmodel.DataTypeFloat}, "longitude": {Type: "number", Datatype: semanticmodel.DataTypeFloat},
			}},
			"tags": {GrainEntity: "tag", Entities: map[string]semanticmodel.ModelEntitySpec{"tag": {Type: "primary", Fields: []string{"tag_id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{
				"tag_id": {Datatype: semanticmodel.DataTypeInteger}, "customer_id": {Datatype: semanticmodel.DataTypeInteger},
				"tagged_at": {Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ},
			}},
			"customers": {GrainEntity: "customer", Entities: map[string]semanticmodel.ModelEntitySpec{"customer": {Type: "primary", Fields: []string{"customer_id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{
				"customer_id": {Datatype: semanticmodel.DataTypeInteger}, "state": {Type: "string", Datatype: semanticmodel.DataTypeString},
			}},
		},
		Relationships: []semanticmodel.Relationship{
			{ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"},
			{ID: "tags_customers", FromDataset: "tags", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {Model: "orders"}, "tags": {Model: "tags"}, "customers": {Model: "customers"},
		},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"activity_date": {Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ, NativeGrain: "day", Grains: []string{"day", "month"}, Bindings: map[string]semanticmodel.DimensionBinding{
				"orders": {Field: "orders.ordered_at"}, "tags": {Field: "tags.tagged_at"},
			}},
			"customer_state": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{
				"orders": {Field: "customers.state", Path: []string{"orders_customers"}},
				"tags":   {Field: "customers.state", Path: []string{"tags_customers"}},
			}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count":    {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, Empty: "zero"},
			"revenue":        {Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}, Empty: "zero"},
			"tag_count":      {Type: "aggregate", Dataset: "tags", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "tags.tag_id"}, Empty: "zero"},
			"tags_per_order": {Type: "derived", Expression: "safe_divide(${tag_count}, ${order_count})"},
		},
	}
}

func rolePlayingDateModel() *semanticmodel.Model {
	model := testModel()
	orders := model.Tables["orders"]
	orders.Dimensions["ordered_date_id"] = semanticmodel.MetricDimension{Datatype: semanticmodel.DataTypeInteger}
	orders.Dimensions["shipped_date_id"] = semanticmodel.MetricDimension{Datatype: semanticmodel.DataTypeInteger}
	model.Tables["orders"] = orders
	model.Datasets["dates"] = semanticmodel.SemanticDatasetSpec{Model: "dates"}
	model.Tables["dates"] = semanticmodel.Table{GrainEntity: "date", Entities: map[string]semanticmodel.ModelEntitySpec{"date": {Type: "primary", Fields: []string{"date_id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{
		"date_id":    {Datatype: semanticmodel.DataTypeInteger},
		"date_value": {Type: "date", Datatype: semanticmodel.DataTypeDate},
	}}
	model.Relationships = append(model.Relationships,
		semanticmodel.Relationship{ID: "orders_order_date", FromDataset: "orders", FromFields: []string{"ordered_date_id"}, ToDataset: "dates", ToFields: []string{"date_id"}, Cardinality: "many_to_one"},
		semanticmodel.Relationship{ID: "orders_ship_date", FromDataset: "orders", FromFields: []string{"shipped_date_id"}, ToDataset: "dates", ToFields: []string{"date_id"}, Cardinality: "many_to_one"},
	)
	model.Dimensions["order_date"] = semanticmodel.SemanticDimension{Type: "date", Datatype: semanticmodel.DataTypeDate, Bindings: map[string]semanticmodel.DimensionBinding{
		"orders": {Field: "dates.date_value", Path: []string{"orders_order_date"}},
	}}
	model.Dimensions["ship_date"] = semanticmodel.SemanticDimension{Type: "date", Datatype: semanticmodel.DataTypeDate, Bindings: map[string]semanticmodel.DimensionBinding{
		"orders": {Field: "dates.date_value", Path: []string{"orders_ship_date"}},
	}}
	return model
}
