package query

import (
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"strings"
	"testing"
)

func TestPlanResultCountCountsGroupsBeforePagination(t *testing.T) {
	planner := mustNewCompiledPlanner(t, rolePlayingDateModel())
	plan, err := planner.PlanResultCount(Request{Dataset: "orders", Dimensions: []Field{{Field: "orders.order_id", Alias: "id"}}, Limit: 10, Offset: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "COUNT(*)") || strings.Contains(plan.SQL, "LIMIT 10") || strings.Contains(plan.SQL, "OFFSET 1000") {
		t.Fatalf("incorrect grouped count plan: %s", plan.SQL)
	}
	canonical, err := plan.IR.Canonical()
	if err != nil || !strings.Contains(string(canonical), `"count_only":true`) {
		t.Fatalf("count mode missing from canonical plan: %s (%v)", canonical, err)
	}
	if len(plan.Columns) != 1 || plan.Columns[0] != "value" {
		t.Fatalf("count columns: %v", plan.Columns)
	}
}

func TestPlanResultCountExecutesGroupedPopulation(t *testing.T) {
	model := semanticAccessTestModel(t)
	policy := model.AccessPolicy.Datasets["orders"]
	policy.AccessFilters = []semanticmodel.SemanticAccessFilterSpec{{Field: "region", UserAttribute: "regions"}}
	model.AccessPolicy.Datasets["orders"] = policy
	planner, err := NewCompiledPlanner(model, WithTableRelation(func(table string) (string, error) { return "model." + table, nil }))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	consumer, err := NewSemanticAccessConsumer(planner, SemanticAccessConsumerConfig{
		InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", ModelID: "semantic-model:test", Generation: "generation-1", PrincipalID: snapshot.PrincipalID,
		Authority: func() (SemanticAccessAttributeSnapshot, SemanticAccessAuthority, error) {
			return snapshot, authority, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	db := openSemanticAccessPlannerDB(t, "CREATE SCHEMA model", "CREATE TABLE model.orders_model(id BIGINT, region VARCHAR, account_id BIGINT, amount DECIMAL(20,3), approved BOOLEAN, order_date DATE, occurred_at TIMESTAMPTZ)", "INSERT INTO model.orders_model(id, region, amount, approved) VALUES (1,'west',10,true),(2,'west',20,true),(3,'east',30,true),(4,'north',40,false)")
	plan, err := consumer.Planner().PlanResultCount(Request{Dataset: "orders", Dimensions: []Field{{Field: "region"}}, Metrics: []Field{{Field: "revenue"}}, Filters: []Filter{{Field: "approved", Operator: "equals", Values: []any{true}}}, Limit: 1, Offset: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if err := consumer.ValidatePlan(plan); err != nil {
		t.Fatalf("count lost protected admission: %v", err)
	}
	var total int
	if err := db.QueryRow(plan.SQL, plan.Args...).Scan(&total); err != nil {
		t.Fatalf("count SQL: %v\n%s", err, plan.SQL)
	}
	if total != 2 {
		t.Fatalf("count = %d, want two groups rather than three source records", total)
	}
}
