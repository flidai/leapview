package query

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/query/planir"
)

func rowPopulationDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders (order_id INTEGER, customer_id INTEGER, status VARCHAR, revenue DECIMAL)",
		"CREATE TABLE model.customers (customer_id INTEGER, state VARCHAR)",
		"INSERT INTO model.orders VALUES (1, 10, 'paid', 10), (2, 20, 'paid', 20), (3, 30, 'paid', 30), (4, 10, 'cancelled', 40)",
		"INSERT INTO model.customers VALUES (10, 'DK'), (20, 'SE')",
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func rowPopulationModel() *semanticmodel.Model {
	model := testModel()
	model.Filters = map[string]semanticmodel.SemanticFilterSpec{
		"danish_customers":       {Field: "customers.state", Operator: "equals", Value: "DK", Path: []string{"orders_customers"}},
		"danish_customers_alias": {Field: "customers.state", Operator: "equals", Value: "DK", Path: []string{"orders_customers"}},
		"swedish_customers":      {Field: "customers.state", Operator: "equals", Value: "SE", Path: []string{"orders_customers"}},
	}
	return model
}

func readRowIDs(t *testing.T, db *sql.DB, plan Plan) []int {
	t.Helper()
	rows, err := db.Query(plan.SQL, plan.Args...)
	if err != nil {
		t.Fatalf("execute row plan: %v\nSQL: %s", err, plan.SQL)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var ids []int
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(values))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		switch value := values[0].(type) {
		case int:
			ids = append(ids, value)
		case int32:
			ids = append(ids, int(value))
		case int64:
			ids = append(ids, int(value))
		default:
			t.Fatalf("row id has unexpected type %T", values[0])
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestPlanRowsAppliesOneMetricNamedPopulation(t *testing.T) {
	db := rowPopulationDB(t)
	model := rowPopulationModel()
	metric := model.Metrics["order_count"]
	metric.Where = []string{"danish_customers"}
	model.Metrics["order_count"] = metric
	planner := mustNewCompiledPlanner(t, model)
	plan, err := planner.PlanRows(RowRequest{Dataset: "orders", Dimensions: []Field{{Field: "orders.order_id"}}, Metrics: []Field{{Field: "order_count"}}})
	if err != nil {
		t.Fatal(err)
	}
	ids := readRowIDs(t, db, plan)
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 4 {
		t.Fatalf("named metric population row ids = %#v, want [1 4]", ids)
	}
	explain, err := plan.Explain()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"source=named name=danish_customers", "phase=relationship", "orders_customers", `"r2"."state" = ?`} {
		if !strings.Contains(explain+plan.SQL, want) {
			t.Fatalf("row population plan missing %q:\n%s\n%s", want, explain, plan.SQL)
		}
	}
}

func TestPlanRowsAcceptsSamePopulationMetrics(t *testing.T) {
	db := rowPopulationDB(t)
	model := rowPopulationModel()
	for _, name := range []string{"order_count", "revenue"} {
		metric := model.Metrics[name]
		metric.Where = []string{"danish_customers"}
		model.Metrics[name] = metric
	}
	planner := mustNewCompiledPlanner(t, model)
	plan, err := planner.PlanRows(RowRequest{Dataset: "orders", Dimensions: []Field{{Field: "orders.order_id"}}, Metrics: []Field{{Field: "order_count"}, {Field: "revenue"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(readRowIDs(t, db, plan)); got != 2 {
		t.Fatalf("same-population row count = %d, want 2", got)
	}
	if strings.Count(plan.SQL, `"r2"."state" = ?`) != 1 {
		t.Fatalf("same population emitted duplicate predicates:\n%s", plan.SQL)
	}
}

func namedPopulationSubgraph(t *testing.T, plan Plan) string {
	t.Helper()
	parts := []string{}
	for _, node := range plan.IR.Nodes {
		filter, ok := node.(planir.FilterRows)
		if !ok || filter.Source != planir.FilterSourceNamed {
			continue
		}
		value, err := json.Marshal(struct {
			Name        string
			Source      planir.FilterSource
			Phase       planir.FilterPhase
			Predicate   planir.Predicate
			FieldRoutes map[string][]planir.RelationshipRoute
		}{filter.Name, filter.Source, filter.FilterPhase, filter.Predicate, filter.FieldRoutes})
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, string(value))
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n")
}

func TestPlanRowsPopulationLoweringIsMetricOrderIndependent(t *testing.T) {
	model := rowPopulationModel()
	for _, name := range []string{"order_count", "revenue"} {
		metric := model.Metrics[name]
		metric.Where = []string{"danish_customers"}
		model.Metrics[name] = metric
	}
	planner := mustNewCompiledPlanner(t, model)
	left, err := planner.PlanRows(RowRequest{Dataset: "orders", Dimensions: []Field{{Field: "orders.order_id"}}, Metrics: []Field{{Field: "order_count"}, {Field: "revenue"}}})
	if err != nil {
		t.Fatal(err)
	}
	right, err := planner.PlanRows(RowRequest{Dataset: "orders", Dimensions: []Field{{Field: "orders.order_id"}}, Metrics: []Field{{Field: "revenue"}, {Field: "order_count"}}})
	if err != nil {
		t.Fatal(err)
	}
	if leftPopulation, rightPopulation := namedPopulationSubgraph(t, left), namedPopulationSubgraph(t, right); leftPopulation != rightPopulation {
		t.Fatalf("metric order changed named population PlanIR subgraph:\nleft=%s\nright=%s", leftPopulation, rightPopulation)
	}
	leftOutput := left.IR.Nodes[left.IR.Output].(planir.SortLimit)
	rightOutput := right.IR.Nodes[right.IR.Output].(planir.SortLimit)
	if got := []string{leftOutput.Projection[0].Source, leftOutput.Projection[1].Source, leftOutput.Projection[2].Source}; !strings.EqualFold(strings.Join(got, ","), "orders.order_id,order_count,revenue") {
		t.Fatalf("left requested projection order changed: %#v", got)
	}
	if got := []string{rightOutput.Projection[0].Source, rightOutput.Projection[1].Source, rightOutput.Projection[2].Source}; !strings.EqualFold(strings.Join(got, ","), "orders.order_id,revenue,order_count") {
		t.Fatalf("right requested projection order changed: %#v", got)
	}
}

func TestPlanRowsRejectsDivergentMetricPopulations(t *testing.T) {
	model := rowPopulationModel()
	orderCount := model.Metrics["order_count"]
	orderCount.Where = []string{"danish_customers"}
	model.Metrics["order_count"] = orderCount
	revenue := model.Metrics["revenue"]
	revenue.Where = []string{"swedish_customers"}
	model.Metrics["revenue"] = revenue
	_, err := mustNewCompiledPlanner(t, model).PlanRows(RowRequest{Dataset: "orders", Metrics: []Field{{Field: "order_count"}, {Field: "revenue"}}})
	if err == nil || !strings.Contains(err.Error(), "divergent populations") {
		t.Fatalf("divergent row populations error = %v", err)
	}
}

func TestPlanRowsRejectsDistinctNamedFiltersWithSamePredicate(t *testing.T) {
	model := rowPopulationModel()
	orderCount := model.Metrics["order_count"]
	orderCount.Where = []string{"danish_customers"}
	model.Metrics["order_count"] = orderCount
	revenue := model.Metrics["revenue"]
	revenue.Where = []string{"danish_customers_alias"}
	model.Metrics["revenue"] = revenue
	_, err := mustNewCompiledPlanner(t, model).PlanRows(RowRequest{Dataset: "orders", Metrics: []Field{{Field: "order_count"}, {Field: "revenue"}}})
	if err == nil || !strings.Contains(err.Error(), "divergent populations") {
		t.Fatalf("same-predicate distinct named populations error = %v", err)
	}
}

func TestPlanRowsKeepsRequestFiltersAndPlanCountRequestOnly(t *testing.T) {
	db := rowPopulationDB(t)
	rowPlanner := mustNewCompiledPlanner(t, rowPopulationModel())
	plan, err := rowPlanner.PlanRows(RowRequest{Dataset: "orders", Dimensions: []Field{{Field: "orders.order_id"}}, Metrics: []Field{{Field: "order_count"}}, Filters: []Filter{{Field: "orders.status", Operator: "equals", Values: []any{"paid"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(readRowIDs(t, db, plan)); got != 3 {
		t.Fatalf("request-filtered row count = %d, want 3", got)
	}
	countModel := rowPopulationModel()
	metric := countModel.Metrics["order_count"]
	metric.Where = []string{"danish_customers"}
	countModel.Metrics["order_count"] = metric
	countPlan, err := mustNewCompiledPlanner(t, countModel).PlanCount(CountRequest{Dataset: "orders"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(countPlan.SQL, countPlan.Args...)
	if err != nil {
		t.Fatalf("execute request-only count: %v\n%s", err, countPlan.SQL)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("request-only count returned no row")
	}
	var count int
	if err := rows.Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("count without metric applied authored population: got %d, want 4", count)
	}
}
