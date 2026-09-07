package query

import (
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestSemanticAccessConsumerRowsCountMatchesNamedMetricPopulation(t *testing.T) {
	model := semanticAccessTestModel(t)
	policy := model.AccessPolicy.Datasets["orders"]
	policy.AccessFilters = []semanticmodel.SemanticAccessFilterSpec{{Field: "account", UserAttribute: "accountIds"}}
	model.AccessPolicy.Datasets["orders"] = policy
	model.Filters = map[string]semanticmodel.SemanticFilterSpec{
		"west_only": {Field: "orders.region", Operator: "equals", Value: "west"},
	}
	metric := model.Metrics["revenue"]
	metric.Where = []string{"west_only"}
	model.Metrics["revenue"] = metric
	planner, err := NewCompiledPlanner(model, WithTableRelation(func(table string) (string, error) { return "model." + table, nil }))
	if err != nil {
		t.Fatalf("NewCompiledPlanner: %v", err)
	}
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	consumer, err := NewSemanticAccessConsumer(planner, SemanticAccessConsumerConfig{
		InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", ModelID: "semantic-model:test", Generation: "generation-1", PrincipalID: snapshot.PrincipalID,
		Authority: func() (SemanticAccessAttributeSnapshot, SemanticAccessAuthority, error) {
			return snapshot, authority, nil
		},
	})
	if err != nil {
		t.Fatalf("NewSemanticAccessConsumer: %v", err)
	}
	db := openSemanticAccessPlannerDB(t,
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders_model(id BIGINT, region VARCHAR, account_id BIGINT, amount DECIMAL(20,3), approved BOOLEAN, order_date DATE, occurred_at TIMESTAMPTZ)",
		"INSERT INTO model.orders_model VALUES (1, 'west', 9007199254740993, 10, true, DATE '2026-09-06', TIMESTAMPTZ '2026-09-06 03:30:00+00'), (2, 'east', 7, 20, true, DATE '2026-09-06', TIMESTAMPTZ '2026-09-06 03:30:00+00'), (3, 'west', 7, 30, false, DATE '2026-09-06', TIMESTAMPTZ '2026-09-06 03:30:00+00'), (4, 'north', 7, 40, true, DATE '2026-09-06', TIMESTAMPTZ '2026-09-06 03:30:00+00'), (5, 'west', 7, 50, true, DATE '2026-09-06', TIMESTAMPTZ '2026-09-06 03:30:00+00')",
	)
	request := RowRequest{
		Dataset: "orders", Dimensions: []Field{{Field: "orders.id"}}, Metrics: []Field{{Field: "revenue"}},
		Filters: []Filter{{Field: "approved", Operator: "equals", Values: []any{true}}}, Sort: []Sort{{Field: "id", Direction: "asc"}}, Limit: 1, Offset: 1,
	}
	rowPlan, err := consumer.Planner().PlanRows(request)
	if err != nil {
		t.Fatalf("PlanRows: %v", err)
	}
	countPlan, err := consumer.PlanRowsCount(request)
	if err != nil {
		t.Fatalf("PlanRowsCount: %v", err)
	}
	if err := consumer.ValidatePlan(countPlan); err != nil {
		t.Fatalf("ValidatePlan(count): %v", err)
	}
	assertSecurityBarriers(t, countPlan.IR, 1, true)
	explain, err := countPlan.Explain()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "source=named name=west_only") {
		t.Fatalf("count PlanIR omitted named metric population:\n%s\n%s", explain, countPlan.SQL)
	}
	rows, err := db.Query(rowPlan.SQL, rowPlan.Args...)
	if err != nil {
		t.Fatalf("execute rows: %v\n%s", err, rowPlan.SQL)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("row population returned no row")
	}
	var rowID int64
	if err := rows.Scan(&rowID, new(any)); err != nil {
		t.Fatal(err)
	}
	if rowID != 5 {
		t.Fatalf("row population id = %d, want 5", rowID)
	}
	var total int64
	if err := db.QueryRow(countPlan.SQL, countPlan.Args...).Scan(&total); err != nil {
		t.Fatalf("execute count: %v\n%s", err, countPlan.SQL)
	}
	if total != 2 {
		t.Fatalf("named/request population count = %d, want 2", total)
	}

	divergent := request
	divergent.Metrics = []Field{{Field: "revenue"}, {Field: "orderCount"}}
	if _, err := consumer.PlanRowsCount(divergent); err == nil || !strings.Contains(err.Error(), "divergent populations") {
		t.Fatalf("divergent protected count error = %v", err)
	}
}

func TestSemanticAccessConsumerRowsCountRetainsSelectedRelationship(t *testing.T) {
	model := rowPopulationModel()
	consumer, err := NewSemanticAccessConsumer(mustNewCompiledPlanner(t, model), SemanticAccessConsumerConfig{})
	if err != nil {
		t.Fatalf("NewSemanticAccessConsumer: %v", err)
	}
	plan, err := consumer.PlanRowsCount(RowRequest{
		Dataset: "orders", Dimensions: []Field{{Field: "customers.state"}}, Metrics: []Field{{Field: "order_count"}},
	})
	if err != nil {
		t.Fatalf("PlanRowsCount: %v", err)
	}
	if err := consumer.ValidatePlan(plan); err != nil {
		t.Fatalf("ValidatePlan(count): %v", err)
	}
	explain, err := plan.Explain()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "orders_customers") {
		t.Fatalf("count PlanIR omitted selected relationship route:\n%s\n%s", explain, plan.SQL)
	}
	db := rowPopulationDB(t)
	if _, err := db.Exec("INSERT INTO model.customers VALUES (10, 'duplicate')"); err != nil {
		t.Fatal(err)
	}
	var total int
	if err := db.QueryRow(plan.SQL, plan.Args...).Scan(&total); err != nil {
		t.Fatalf("execute relationship count: %v\n%s", err, plan.SQL)
	}
	if total != 6 {
		t.Fatalf("relationship population count = %d, want 6 (including the unmatched left-join row)", total)
	}
}
