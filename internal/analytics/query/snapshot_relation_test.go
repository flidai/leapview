package query

import (
	"fmt"
	"strings"
	"testing"
)

func TestPlannerQualifiesEveryPhysicalRelationWithSnapshot(t *testing.T) {
	const snapshotID = int64(42)
	planner, err := NewCompiledPlanner(testModel(), WithTableRelation(func(table string) (string, error) {
		return fmt.Sprintf("(FROM lake.model.%s AT (VERSION => %d))", table, snapshotID), nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	plan, err := planner.Plan(Request{
		Dimensions: []Field{{Field: "customer_state", Alias: "state"}},
		Metrics:    []Field{{Field: "order_count"}, {Field: "tag_count"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"orders", "tags", "customers"} {
		want := fmt.Sprintf("lake.model.%s AT (VERSION => %d)", table, snapshotID)
		if !strings.Contains(plan.SQL, want) {
			t.Fatalf("plan does not snapshot-qualify %q with %q:\n%s", table, want, plan.SQL)
		}
	}
	if strings.Contains(plan.SQL, "FROM model.") || strings.Contains(plan.SQL, "JOIN model.") {
		t.Fatalf("plan contains an unqualified physical relation:\n%s", plan.SQL)
	}
}

func TestBundlePlannerQualifiesEveryPhysicalRelationWithSnapshot(t *testing.T) {
	planner, err := NewCompiledPlanner(executableMultiFactModel(), WithTableRelation(func(table string) (string, error) {
		return "(FROM lake.model." + table + " AT (VERSION => 7))", nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	bundle, err := planner.PlanBundle([]BundleRequest{
		{ID: "orders", Request: Request{Metrics: []Field{{Field: "order_count"}}}},
		{ID: "ratio", Request: Request{Metrics: []Field{{Field: "tags_per_order"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"orders", "tags"} {
		want := "lake.model." + table + " AT (VERSION => 7)"
		if !strings.Contains(bundle.Plan.SQL, want) {
			t.Fatalf("bundle does not snapshot-qualify %q:\n%s", table, bundle.Plan.SQL)
		}
	}
	if strings.Contains(bundle.Plan.SQL, "FROM model.") || strings.Contains(bundle.Plan.SQL, "JOIN model.") {
		t.Fatalf("bundle contains an unqualified physical relation:\n%s", bundle.Plan.SQL)
	}
}
