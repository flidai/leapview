package query

import (
	"strings"
	"testing"
)

func TestSemanticAccessPlannerRejectsMissingOrInconsistentPolicy(t *testing.T) {
	for _, mode := range []string{"missing", "invalid digest", "foreign instance", "different model policy"} {
		t.Run(mode, func(t *testing.T) {
			model := semanticAccessTestModel(t)
			policy := compileSemanticAccessPlannerPolicy(t, model)
			snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
			var planner *Planner
			switch mode {
			case "missing":
				var err error
				planner, err = NewCompiledPlanner(model)
				if err != nil {
					t.Fatal(err)
				}
			case "invalid digest":
				policy.digest = "sha256:" + strings.Repeat("0", 64)
			case "foreign instance":
				policy.targetInstanceID = "foreign-instance"
			case "different model policy":
				delete(model.AccessPolicy.Datasets, "orders")
			}
			if planner == nil {
				planner = semanticAccessPlannerForTest(t, model, policy, snapshot, authority, nil)
			}
			if plan, err := planner.Plan(Request{Dataset: "orders", Metrics: []Field{{Field: "revenue"}}}); err == nil {
				t.Fatalf("invalid admission produced executable plan: %s", plan.SQL)
			}
		})
	}
}

func TestSemanticAccessPlannerCanonicalIdentityIsStable(t *testing.T) {
	model := semanticAccessTestModel(t)
	policy := compileSemanticAccessPlannerPolicy(t, model)
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	planner := semanticAccessPlannerForTest(t, model, policy, snapshot, authority, nil)
	var previous string
	for iteration := 0; iteration < 10; iteration++ {
		plan, err := planner.Plan(Request{Dataset: "orders", Metrics: []Field{{Field: "revenue"}}})
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := plan.IR.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		if iteration > 0 && previous != string(canonical) {
			t.Fatal("identical authority produced different canonical plans")
		}
		previous = string(canonical)
	}
}
