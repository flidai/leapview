package query

import (
	"database/sql"
	"fmt"
	"strconv"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
)

func TestPlanHistogramExecutesExplicitDomainNullAndOverflowContract(t *testing.T) {
	db := analyticalTestDB(t, "INSERT INTO model.orders VALUES (1, 1.0), (2, 2.0), (3, 4.0), (4, 5.0), (5, NULL)")
	plan, err := mustNewCompiledPlanner(t, testModel()).PlanHistogram(RawValueRequest{Dataset: "orders", Metric: Field{Field: "revenue", Alias: "value"}}, 2, HistogramOptions{
		Domain: &HistogramDomain{Minimum: 2, Maximum: 4}, NullPolicy: "include", Approximation: "exact",
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := executeAnalyticalRows(t, db, plan)
	t.Logf("histogram rows=%#v sql=%s", rows, plan.SQL)
	if len(rows) != 5 {
		t.Fatalf("histogram rows = %#v, want null/underflow/two bins/overflow", rows)
	}
	wantCounts := map[int]int64{-2: 1, -1: 1, 0: 1, 1: 1, 2: 1}
	for _, row := range rows {
		bucket := int(row[0].(int32))
		count := row[1].(int64)
		if wantCounts[bucket] != count {
			t.Fatalf("bucket %d count = %d, want %d", bucket, count, wantCounts[bucket])
		}
	}
}

func TestPlanHistogramExecutesNullOnlyAndDegeneratePopulations(t *testing.T) {
	db := analyticalTestDB(t, "INSERT INTO model.orders VALUES (1, NULL), (2, NULL)")
	plan, err := mustNewCompiledPlanner(t, testModel()).PlanHistogram(RawValueRequest{Dataset: "orders", Metric: Field{Field: "revenue"}}, 3, HistogramOptions{NullPolicy: "include", Approximation: "exact"})
	if err != nil {
		t.Fatal(err)
	}
	rows := executeAnalyticalRows(t, db, plan)
	if len(rows) != 1 || int(rows[0][0].(int32)) != -2 || rows[0][1].(int64) != 2 {
		t.Fatalf("null-only histogram = %#v", rows)
	}
	if _, err := db.Exec("DELETE FROM model.orders; INSERT INTO model.orders VALUES (1, 3.0), (2, 3.0)"); err != nil {
		t.Fatal(err)
	}
	plan, err = mustNewCompiledPlanner(t, testModel()).PlanHistogram(RawValueRequest{Dataset: "orders", Metric: Field{Field: "revenue"}}, 3, HistogramOptions{NullPolicy: "omit", Approximation: "exact"})
	if err != nil {
		t.Fatal(err)
	}
	rows = executeAnalyticalRows(t, db, plan)
	if len(rows) != 1 || int(rows[0][0].(int32)) != 0 || rows[0][1].(int64) != 2 {
		t.Fatalf("degenerate histogram = %#v", rows)
	}
}

func TestPlanAnalyticalQueriesReturnEmptyFrameForEmptyPopulation(t *testing.T) {
	db := analyticalTestDB(t, "")
	planner := mustNewCompiledPlanner(t, testModel())
	histogram, err := planner.PlanHistogram(RawValueRequest{Dataset: "orders", Metric: Field{Field: "revenue"}}, 4, HistogramOptions{NullPolicy: "omit", Approximation: "exact"})
	if err != nil {
		t.Fatal(err)
	}
	if rows := executeAnalyticalRows(t, db, histogram); len(rows) != 0 {
		t.Fatalf("empty histogram = %#v, want no rows", rows)
	}
	distribution, err := planner.PlanDistribution(RawValueRequest{Dataset: "orders", Metric: Field{Field: "revenue"}}, nil, 0, DistributionOptions{Quantiles: []float64{0.25, 0.5, 0.75}, Outliers: "include", Approximation: "exact"})
	if err != nil {
		t.Fatal(err)
	}
	if rows := executeAnalyticalRows(t, db, distribution); len(rows) != 0 {
		t.Fatalf("empty distribution = %#v, want no rows", rows)
	}
}

func TestPlanDistributionExecutesQuantilesAndWhiskerOutlierPolicy(t *testing.T) {
	db := analyticalTestDB(t, "INSERT INTO model.orders VALUES (1, 1.0), (2, 2.0), (3, 3.0), (4, 4.0), (5, 5.0)")
	plan, err := mustNewCompiledPlanner(t, testModel()).PlanDistribution(RawValueRequest{Dataset: "orders", Metric: Field{Field: "revenue", Alias: "value"}}, nil, 0, DistributionOptions{Quantiles: []float64{0.25, 0.3}, Outliers: "include", Approximation: "exact"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Columns) != 5 || plan.Columns[2] != "q0" || plan.Columns[3] != "q1" {
		t.Fatalf("arbitrary quantile columns = %#v", plan.Columns)
	}
	rows := executeAnalyticalRows(t, db, plan)
	if len(rows) != 1 || rows[0][0].(string) != "all" {
		t.Fatalf("distribution rows = %#v", rows)
	}
	plan, err = mustNewCompiledPlanner(t, testModel()).PlanDistribution(RawValueRequest{Dataset: "orders", Metric: Field{Field: "revenue", Alias: "value"}}, nil, 0, DistributionOptions{Quantiles: []float64{0.25, 0.5, 0.75}, Whiskers: &DistributionWhiskers{Lower: 0.25, Upper: 0.75}, Outliers: "omit", Approximation: "exact"})
	if err != nil {
		t.Fatal(err)
	}
	rows = executeAnalyticalRows(t, db, plan)
	if len(rows) != 1 || analyticalNumber(t, rows[0][1]) != 2 || analyticalNumber(t, rows[0][5]) != 4 {
		t.Fatalf("whisker-filtered distribution = %#v", rows)
	}
}

func TestPlanHistogramPreservesGovernedFiltersAndMasks(t *testing.T) {
	db := analyticalTestDB(t, "INSERT INTO model.orders VALUES (1, 1.0), (2, 2.0), (3, 3.0), (4, 4.0)")
	planner := mustNewCompiledPlanner(t, testModel())
	plan, err := planner.PlanHistogram(RawValueRequest{Dataset: "orders", Metric: Field{Field: "revenue"}, Filters: []Filter{{Field: "orders.order_id", Operator: "greater_than_or_equal", Values: []any{int64(3)}}}}, 2, HistogramOptions{NullPolicy: "omit", Approximation: "exact"})
	if err != nil {
		t.Fatal(err)
	}
	rows := executeAnalyticalRows(t, db, plan)
	count := int64(0)
	for _, row := range rows {
		count += row[1].(int64)
	}
	if count != 2 {
		t.Fatalf("policy-filtered histogram count = %d, want 2", count)
	}
	if _, err := planner.PlanHistogram(RawValueRequest{Dataset: "orders", Metric: Field{Field: "revenue"}, ColumnMasks: []ColumnMask{{Field: "orders.revenue", Mask: "redact"}}}, 2, HistogramOptions{NullPolicy: "omit", Approximation: "exact"}); err == nil {
		t.Fatal("masked histogram metric was accepted")
	}
}

func analyticalNumber(t *testing.T, value any) float64 {
	t.Helper()
	switch typed := value.(type) {
	case float64:
		return typed
	case int64:
		return float64(typed)
	default:
		parsed, err := strconv.ParseFloat(fmt.Sprint(value), 64)
		if err != nil {
			t.Fatalf("numeric result %T(%v): %v", value, value, err)
		}
		return parsed
	}
}

func analyticalTestDB(t *testing.T, insert string) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{"CREATE SCHEMA model", "CREATE TABLE model.orders(order_id INTEGER, revenue DECIMAL(10,2))", insert} {
		if statement == "" {
			continue
		}
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func executeAnalyticalRows(t *testing.T, db *sql.DB, plan Plan) [][]any {
	t.Helper()
	result, err := db.Query(plan.SQL, plan.Args...)
	if err != nil {
		t.Fatalf("execute analytical plan: %v\n%s", err, plan.SQL)
	}
	defer result.Close()
	rows := [][]any{}
	for result.Next() {
		values := make([]any, len(plan.Columns))
		scans := make([]any, len(values))
		for index := range values {
			scans[index] = &values[index]
		}
		if err := result.Scan(scans...); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, values)
	}
	if err := result.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}
