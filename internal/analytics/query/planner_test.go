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

func TestPlannerScalarMultiFactAggregatesFactsIndependently(t *testing.T) {
	plan, err := NewPlanner(testModel()).Plan(Request{Metrics: []Field{
		{Field: "revenue", Alias: "revenue"},
		{Field: "tag_count", Alias: "tags"},
		{Field: "tags_per_order", Alias: "ratio"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"fact_0 AS", "SUM(t0.revenue)", "fact_1 AS", "COUNT(*) AS __m2",
		"CROSS JOIN", "NULLIF(COALESCE(s.__m0, 0), 0)",
	} {
		if !strings.Contains(plan.SQL, want) {
			t.Fatalf("SQL missing %q:\n%s", want, plan.SQL)
		}
	}
	if plan.Mode != "multi_fact" || strings.Join(plan.Facts, ",") != "orders,tags" {
		t.Fatalf("plan mode/facts = %q/%v", plan.Mode, plan.Facts)
	}
}

func TestPlannerAggregatePaginationAlwaysHasTotalOrdering(t *testing.T) {
	plan, err := NewPlanner(testModel()).Plan(Request{
		Dimensions: []Field{{Field: "customer_state", Alias: "label"}},
		Metrics:    []Field{{Field: "order_count", Alias: "value"}},
		Limit:      1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "ORDER BY label ASC, value ASC") {
		t.Fatalf("SQL missing deterministic ordering:\n%s", plan.SQL)
	}
	if len(plan.EffectiveOrdering) != 2 || plan.EffectiveOrdering[0].Field != "label" || plan.EffectiveOrdering[1].Field != "value" {
		t.Fatalf("effective ordering = %#v", plan.EffectiveOrdering)
	}
}

func TestPlannerExplicitSortRemainsPrimaryAndGetsTieBreaker(t *testing.T) {
	plan, err := NewPlanner(testModel()).Plan(Request{
		Dimensions: []Field{{Field: "customer_state", Alias: "label"}},
		Metrics:    []Field{{Field: "order_count", Alias: "value"}},
		Sort:       []Sort{{Field: "value", Direction: "desc"}},
		Limit:      1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "ORDER BY value DESC, label ASC") {
		t.Fatalf("SQL did not preserve primary sort:\n%s", plan.SQL)
	}
}

func TestPlannerGroupedMultiFactUsesFullOuterStitch(t *testing.T) {
	plan, err := NewPlanner(testModel()).Plan(Request{
		Dimensions: []Field{{Field: "customer_state", Alias: "state"}},
		Metrics:    []Field{{Field: "order_count"}, {Field: "tag_count"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"LEFT JOIN model.customers", "FULL OUTER JOIN fact_1", "IS NOT DISTINCT FROM",
		"COALESCE(l.__d0, r.__d0) AS __d0", "COALESCE(s.__m0, 0)",
	} {
		if !strings.Contains(plan.SQL, want) {
			t.Fatalf("SQL missing %q:\n%s", want, plan.SQL)
		}
	}
	if strings.Join(plan.StitchDimensions, ",") != "customer_state" {
		t.Fatalf("stitch dimensions = %v", plan.StitchDimensions)
	}
	if strings.Join(plan.RelationshipPaths, ",") != "orders:orders_customers,tags:tags_customers" {
		t.Fatalf("relationship paths = %v", plan.RelationshipPaths)
	}
}

func TestPlannerConformedFilterPropagatesToEveryFact(t *testing.T) {
	plan, err := NewPlanner(testModel()).Plan(Request{
		Metrics: []Field{{Field: "order_count"}, {Field: "tag_count"}},
		Filters: []Filter{{Field: "customer_state", Operator: "equals", Values: []any{"DK"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(plan.SQL, "state = ?"); got != 2 {
		t.Fatalf("conformed filter count = %d, want 2:\n%s", got, plan.SQL)
	}
	if len(plan.Args) != 2 || plan.Args[0] != "DK" || plan.Args[1] != "DK" {
		t.Fatalf("args = %#v", plan.Args)
	}
}

func TestPlannerDimensionOnlyQueryUsesFactsCompatibleWithConformedFilters(t *testing.T) {
	model := testModel()
	model.Dimensions["order_status"] = semanticmodel.SemanticDimension{Type: "string", Bindings: map[string]semanticmodel.DimensionBinding{
		"orders": {Field: "orders.status"},
	}}

	plan, err := NewPlanner(model).Plan(Request{
		Dimensions: []Field{{Field: "customer_state", Alias: "value"}},
		Filters:    []Filter{{Field: "order_status", Operator: "in", Values: []any{"canceled", "created"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != "single_fact" || strings.Join(plan.Facts, ",") != "orders" {
		t.Fatalf("plan mode/facts = %q/%v, want single_fact/[orders]", plan.Mode, plan.Facts)
	}
	if got := strings.Count(plan.SQL, "status IN (?, ?)"); got != 1 {
		t.Fatalf("status filter count = %d, want 1:\n%s", got, plan.SQL)
	}
}

func TestPlannerConformedSelectionEntriesPropagateToEveryFact(t *testing.T) {
	plan, err := NewPlanner(testModel()).Plan(Request{
		Metrics: []Field{{Field: "order_count"}, {Field: "tag_count"}},
		Filters: []Filter{{Groups: []FilterGroup{
			{Filters: []Filter{{Field: "customer_state", Operator: "equals", Values: []any{"DK"}}}},
			{Filters: []Filter{{Field: "customer_state", Operator: "equals", Values: []any{"SE"}}}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(plan.SQL, "state = ?"); got != 4 {
		t.Fatalf("conformed selection predicate count = %d, want 4:\n%s", got, plan.SQL)
	}
	wantArgs := []any{"DK", "SE", "DK", "SE"}
	if fmt.Sprint(plan.Args) != fmt.Sprint(wantArgs) {
		t.Fatalf("args = %#v, want %#v", plan.Args, wantArgs)
	}
}

func TestPlannerFactLocalSelectionFiltersOnlyNamedFact(t *testing.T) {
	plan, err := NewPlanner(testModel()).Plan(Request{
		Metrics: []Field{{Field: "order_count"}, {Field: "tag_count"}},
		Filters: []Filter{{Field: "orders.status", Fact: "orders", Operator: "equals", Values: []any{"paid"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(plan.SQL, "status = ?"); got != 1 {
		t.Fatalf("fact-local predicate count = %d, want 1:\n%s", got, plan.SQL)
	}
	if len(plan.Args) != 1 || plan.Args[0] != "paid" {
		t.Fatalf("args = %#v, want [paid]", plan.Args)
	}
}

func TestPlannerRequiresFactForLocalMultiFactFilter(t *testing.T) {
	_, err := NewPlanner(testModel()).Plan(Request{
		Metrics: []Field{{Field: "order_count"}, {Field: "tag_count"}},
		Filters: []Filter{{Field: "orders.status", Operator: "equals", Values: []any{"paid"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "requires fact") {
		t.Fatalf("error = %v", err)
	}
}

func TestPlannerRejectsMismatchedFactOnSingleFactFilter(t *testing.T) {
	_, err := NewPlanner(testModel()).PlanRows(RowRequest{
		Table:      "orders",
		Dimensions: []Field{{Field: "orders.order_id"}},
		Filters:    []Filter{{Field: "customer_state", Fact: "tags", Operator: "equals", Values: []any{"DK"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "does not match query fact") {
		t.Fatalf("error = %v", err)
	}
}

func TestPlannerTableScopeRejectsOtherFactDependencies(t *testing.T) {
	_, err := NewPlanner(testModel()).Plan(Request{
		Table:   "orders",
		Metrics: []Field{{Field: "tags_per_order"}},
	})
	if err == nil || !strings.Contains(err.Error(), "selects dependency from fact") {
		t.Fatalf("error = %v", err)
	}
}

func TestPlannerRejectsLocalDimensionInMultiFactQuery(t *testing.T) {
	_, err := NewPlanner(testModel()).Plan(Request{
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
	orders.Dimensions["billing_customer_id"] = semanticmodel.MetricDimension{Expr: "billing_customer_id"}
	model.Tables["orders"] = orders
	model.Relationships = append(model.Relationships, semanticmodel.Relationship{
		ID: "orders_billing_customers", From: "orders.billing_customer_id", To: "customers.customer_id", Cardinality: "many_to_one",
	})
	state := model.Dimensions["customer_state"]
	state.Bindings["orders"] = semanticmodel.DimensionBinding{Field: "customers.state", Path: []string{"orders_customers"}}
	model.Dimensions["customer_state"] = state

	plan, err := NewPlanner(model).Plan(Request{
		Dimensions: []Field{{Field: "customer_state"}},
		Metrics:    []Field{{Field: "order_count"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "t0.customer_id = t1.customer_id") || strings.Contains(plan.SQL, "billing_customer_id") {
		t.Fatalf("plan did not use explicit orders_customers path:\n%s", plan.SQL)
	}
}

func TestPlannerUsesDistinctAliasesForRolePlayingDimensionPaths(t *testing.T) {
	plan, err := NewPlanner(rolePlayingDateModel()).Plan(Request{
		Dimensions: []Field{{Field: "order_date"}, {Field: "ship_date"}},
		Metrics:    []Field{{Field: "order_count"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"LEFT JOIN model.dates t1 ON t0.ordered_date_id = t1.date_id",
		"LEFT JOIN model.dates t2 ON t0.shipped_date_id = t2.date_id",
		"CAST(t1.date_value AS DATE) AS __d0",
		"CAST(t2.date_value AS DATE) AS __d1",
	} {
		if !strings.Contains(plan.SQL, want) {
			t.Fatalf("role-playing SQL missing %q:\n%s", want, plan.SQL)
		}
	}
	if strings.Join(plan.RelationshipPaths, ",") != "orders:orders_order_date,orders:orders_ship_date" {
		t.Fatalf("relationship paths = %v", plan.RelationshipPaths)
	}

	reversed, err := NewPlanner(rolePlayingDateModel()).Plan(Request{
		Dimensions: []Field{{Field: "ship_date"}, {Field: "order_date"}},
		Metrics:    []Field{{Field: "order_count"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"LEFT JOIN model.dates t1 ON t0.ordered_date_id = t1.date_id",
		"LEFT JOIN model.dates t2 ON t0.shipped_date_id = t2.date_id",
	} {
		if !strings.Contains(reversed.SQL, want) {
			t.Fatalf("reversed role-playing SQL missing deterministic join %q:\n%s", want, reversed.SQL)
		}
	}
}

func TestPlannerKeepsRolePlayingFilterPathsDistinct(t *testing.T) {
	plan, err := NewPlanner(rolePlayingDateModel()).Plan(Request{
		Metrics: []Field{{Field: "order_count"}},
		Filters: []Filter{
			{Field: "order_date", Operator: "equals", Values: []any{"2026-07-01"}},
			{Field: "ship_date", Operator: "equals", Values: []any{"2026-07-02"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"LEFT JOIN model.dates t1 ON t0.ordered_date_id = t1.date_id",
		"LEFT JOIN model.dates t2 ON t0.shipped_date_id = t2.date_id",
		"t1.date_value = ?",
		"t2.date_value = ?",
	} {
		if !strings.Contains(plan.SQL, want) {
			t.Fatalf("role-playing filter SQL missing %q:\n%s", want, plan.SQL)
		}
	}
	if len(plan.Args) != 2 || plan.Args[0] != "2026-07-01" || plan.Args[1] != "2026-07-02" {
		t.Fatalf("args = %#v", plan.Args)
	}
	if strings.Join(plan.RelationshipPaths, ",") != "orders:orders_order_date,orders:orders_ship_date" {
		t.Fatalf("filter relationship paths = %v", plan.RelationshipPaths)
	}
}

func TestPlannerRowAndRawQueriesStaySingleFact(t *testing.T) {
	planner := NewPlanner(testModel())
	row, err := planner.PlanRows(RowRequest{
		Table: "orders", Dimensions: []Field{{Field: "orders.order_id"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(row.SQL, "t0.order_id AS order_id") {
		t.Fatalf("row SQL:\n%s", row.SQL)
	}
	_, err = planner.PlanRawValues(RawValueRequest{Table: "orders", Metric: Field{Field: "order_count"}})
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

	plan, err := NewPlanner(model).PlanRawValues(RawValueRequest{Table: "orders", Metric: Field{Field: "revenue"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"LEFT JOIN model.customers",
		"t1.customer_id IS NOT NULL",
		"t1.state = ?",
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
		Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ, Grains: []string{"week"}, Calendar: "gregorian", Timezone: "America/Los_Angeles", WeekStart: "sunday",
		Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.ordered_at"}},
	}
	plan, err := NewPlanner(model).Plan(Request{Metrics: []Field{{Field: "order_count", Alias: "orders"}}, Time: Time{Field: "local_week", Grain: "week", Alias: "week"}})
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
	plan, err := NewPlanner(testModel()).Plan(Request{
		Table: "orders", Metrics: []Field{{Field: "order_count"}},
		Filters: []Filter{{Spatial: &SpatialFilter{
			Kind: "radius", LatitudeField: "orders.latitude", LongitudeField: "orders.longitude", Fact: "orders",
			Center: SpatialPoint{Longitude: -46.63, Latitude: -23.55}, RadiusMeters: 25_000,
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ASIN", "RADIANS(t0.latitude - ?)", "RADIANS(t0.longitude - ?)"} {
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
			"orders": {Dimensions: map[string]semanticmodel.MetricDimension{
				"order_id": {Expr: "order_id"}, "customer_id": {Expr: "customer_id"},
				"ordered_at": {Expr: "ordered_at", Type: "timestamp"}, "revenue": {Expr: "revenue", Type: "number"},
				"status": {Expr: "status", Type: "string"}, "latitude": {Expr: "latitude", Type: "number"}, "longitude": {Expr: "longitude", Type: "number"},
			}},
			"tags": {Dimensions: map[string]semanticmodel.MetricDimension{
				"tag_id": {Expr: "tag_id"}, "customer_id": {Expr: "customer_id"},
				"tagged_at": {Expr: "tagged_at", Type: "timestamp"},
			}},
			"customers": {Dimensions: map[string]semanticmodel.MetricDimension{
				"customer_id": {Expr: "customer_id"}, "state": {Expr: "state", Type: "string"},
			}},
		},
		Relationships: []semanticmodel.Relationship{
			{ID: "orders_customers", From: "orders.customer_id", To: "customers.customer_id", Cardinality: "many_to_one"},
			{ID: "tags_customers", From: "tags.customer_id", To: "customers.customer_id", Cardinality: "many_to_one"},
		},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"activity_date": {Type: "timestamp", Grains: []string{"day", "month"}, Bindings: map[string]semanticmodel.DimensionBinding{
				"orders": {Field: "orders.ordered_at"}, "tags": {Field: "tags.tagged_at"},
			}},
			"customer_state": {Type: "string", Bindings: map[string]semanticmodel.DimensionBinding{
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
	orders.Dimensions["ordered_date_id"] = semanticmodel.MetricDimension{Expr: "ordered_date_id"}
	orders.Dimensions["shipped_date_id"] = semanticmodel.MetricDimension{Expr: "shipped_date_id"}
	model.Tables["orders"] = orders
	model.Tables["dates"] = semanticmodel.Table{Dimensions: map[string]semanticmodel.MetricDimension{
		"date_id":    {Expr: "date_id"},
		"date_value": {Expr: "date_value", Type: "date"},
	}}
	model.Relationships = append(model.Relationships,
		semanticmodel.Relationship{ID: "orders_order_date", From: "orders.ordered_date_id", To: "dates.date_id", Cardinality: "many_to_one"},
		semanticmodel.Relationship{ID: "orders_ship_date", From: "orders.shipped_date_id", To: "dates.date_id", Cardinality: "many_to_one"},
	)
	model.Dimensions["order_date"] = semanticmodel.SemanticDimension{Type: "date", Bindings: map[string]semanticmodel.DimensionBinding{
		"orders": {Field: "dates.date_value", Path: []string{"orders_order_date"}},
	}}
	model.Dimensions["ship_date"] = semanticmodel.SemanticDimension{Type: "date", Bindings: map[string]semanticmodel.DimensionBinding{
		"orders": {Field: "dates.date_value", Path: []string{"orders_ship_date"}},
	}}
	return model
}
