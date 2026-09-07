package query

import (
	"reflect"
	"testing"
)

func TestResultIdentityMatchesIndependentProjections(t *testing.T) {
	planner := mustNewCompiledPlanner(t, testModel())
	plan, err := planner.Plan(Request{
		Dataset:    "orders",
		Dimensions: []Field{{Field: "customer_state", Alias: "state"}},
		Metrics:    []Field{{Field: "order_count", Alias: "count"}},
		Filters:    []Filter{{Field: "orders.status", Operator: "equals", Values: []any{"paid"}}},
		Sort:       []Sort{{Field: "count", Direction: "asc"}},
		Limit:      10,
	})
	if err != nil {
		t.Fatal(err)
	}

	identity, err := plan.ResultIdentity()
	if err != nil {
		t.Fatal(err)
	}
	dependencies, err := plan.ResultDependencies()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := plan.ResultEquivalenceDigest()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(identity.Dependencies, dependencies) {
		t.Fatalf("single-pass dependencies = %#v, want %#v", identity.Dependencies, dependencies)
	}
	if identity.EquivalenceDigest != digest {
		t.Fatalf("single-pass equivalence digest = %q, want %q", identity.EquivalenceDigest, digest)
	}
}

func TestResultEquivalenceDigestUsesPlannerNormalization(t *testing.T) {
	planner := mustNewCompiledPlanner(t, testModel())
	base := Request{
		Dataset:    "orders",
		Dimensions: []Field{{Field: "customer_state", Alias: "state"}},
		Metrics:    []Field{{Field: "order_count", Alias: "count"}},
		Filters:    []Filter{{Field: "orders.status", Operator: "EQUALS", Values: []any{"paid"}}},
		Sort:       []Sort{{Field: "count", Direction: "ASC"}},
		Limit:      10,
	}
	first, err := planner.Plan(base)
	if err != nil {
		t.Fatal(err)
	}
	base.Filters[0].Operator = "equals"
	base.Sort[0].Direction = "asc"
	second, err := planner.Plan(base)
	if err != nil {
		t.Fatal(err)
	}
	firstDigest, err := first.ResultEquivalenceDigest()
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := second.ResultEquivalenceDigest()
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest {
		t.Fatalf("equivalent accepted syntax produced different result identity: %s != %s", firstDigest, secondDigest)
	}
}

func TestResultEquivalenceDigestBindsShapeAndPagination(t *testing.T) {
	planner := mustNewCompiledPlanner(t, testModel())
	base := Request{
		Dataset:    "orders",
		Dimensions: []Field{{Field: "customer_state", Alias: "state"}},
		Metrics:    []Field{{Field: "order_count", Alias: "count"}},
		Filters:    []Filter{{Field: "orders.status", Operator: "equals", Values: []any{"paid"}}},
		Limit:      10,
	}
	first, err := planner.Plan(base)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := first.ResultEquivalenceDigest()
	if err != nil {
		t.Fatal(err)
	}
	changedShape := base
	changedShape.Dimensions = []Field{{Field: "customer_state", Alias: "other_state"}}
	shapePlan, err := planner.Plan(changedShape)
	if err != nil {
		t.Fatal(err)
	}
	shapeDigest, err := shapePlan.ResultEquivalenceDigest()
	if err != nil {
		t.Fatal(err)
	}
	if digest == shapeDigest {
		t.Fatal("output alias changed result identity")
	}
	changedPagination := base
	changedPagination.Offset = 1
	paginationPlan, err := planner.Plan(changedPagination)
	if err != nil {
		t.Fatal(err)
	}
	paginationDigest, err := paginationPlan.ResultEquivalenceDigest()
	if err != nil {
		t.Fatal(err)
	}
	if digest == paginationDigest {
		t.Fatal("pagination changed result identity")
	}
	argsOnly := first
	argsOnly.Args = []any{"renderer-only change"}
	argsDigest, err := argsOnly.ResultEquivalenceDigest()
	if err != nil {
		t.Fatal(err)
	}
	if digest != argsDigest {
		t.Fatal("renderer-only bound argument change rotated semantic identity")
	}
}

func TestResultEquivalenceDigestUsesPlannerTypedLiteralNormalization(t *testing.T) {
	planner := mustNewCompiledPlanner(t, testModel())
	base := Request{Dataset: "orders", Metrics: []Field{{Field: "order_count"}}, Filters: []Filter{{Field: "orders.order_id", Operator: "equals", Values: []any{int32(1)}}}}
	plan, err := planner.Plan(base)
	if err != nil {
		t.Fatal(err)
	}
	int32Digest, err := plan.ResultEquivalenceDigest()
	if err != nil {
		t.Fatal(err)
	}
	base.Filters[0].Values = []any{int64(1)}
	int64Plan, err := planner.Plan(base)
	if err != nil {
		t.Fatal(err)
	}
	int64Digest, err := int64Plan.ResultEquivalenceDigest()
	if err != nil {
		t.Fatal(err)
	}
	if int32Digest != int64Digest {
		t.Fatal("planner-normalized integer values unexpectedly differ")
	}
	base.Filters[0].Values = []any{float32(1)}
	float32Plan, err := planner.Plan(base)
	if err != nil {
		t.Fatal(err)
	}
	float32Digest, err := float32Plan.ResultEquivalenceDigest()
	if err != nil {
		t.Fatal(err)
	}
	base.Filters[0].Values = []any{float64(1)}
	float64Plan, err := planner.Plan(base)
	if err != nil {
		t.Fatal(err)
	}
	float64Digest, err := float64Plan.ResultEquivalenceDigest()
	if err != nil {
		t.Fatal(err)
	}
	if float32Digest != float64Digest {
		t.Fatal("planner-normalized float values unexpectedly differ")
	}
	base.Filters[0].Values = []any{float64(2)}
	changedPlan, err := planner.Plan(base)
	if err != nil {
		t.Fatal(err)
	}
	changedDigest, err := changedPlan.ResultEquivalenceDigest()
	if err != nil {
		t.Fatal(err)
	}
	if float64Digest == changedDigest {
		t.Fatal("different typed literal value did not rotate identity")
	}
}

func TestResultEquivalenceDigestHonorsPlannerCommutativeNormalization(t *testing.T) {
	planner := mustNewCompiledPlanner(t, testModel())
	group := []Filter{
		{Field: "orders.status", Operator: "equals", Values: []any{"paid"}},
		{Field: "orders.order_id", Operator: "greater_than", Values: []any{int64(1)}},
	}
	base := Request{Dataset: "orders", Metrics: []Field{{Field: "order_count"}}, Filters: []Filter{{Groups: []FilterGroup{{Filters: group}}}}}
	first, err := planner.Plan(base)
	if err != nil {
		t.Fatal(err)
	}
	base.Filters[0].Groups[0].Filters = []Filter{group[1], group[0]}
	second, err := planner.Plan(base)
	if err != nil {
		t.Fatal(err)
	}
	firstDigest, err := first.ResultEquivalenceDigest()
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := second.ResultEquivalenceDigest()
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest {
		t.Fatal("reordered planner-commutative filter group changed identity")
	}

	base.Filters = []Filter{{Field: "orders.status", Operator: "in", Values: []any{"paid", "pending"}}}
	inFirst, err := planner.Plan(base)
	if err != nil {
		t.Fatal(err)
	}
	base.Filters[0].Values = []any{"pending", "paid"}
	inSecond, err := planner.Plan(base)
	if err != nil {
		t.Fatal(err)
	}
	inFirstDigest, err := inFirst.ResultEquivalenceDigest()
	if err != nil {
		t.Fatal(err)
	}
	inSecondDigest, err := inSecond.ResultEquivalenceDigest()
	if err != nil {
		t.Fatal(err)
	}
	if inFirstDigest != inSecondDigest {
		t.Fatal("reordered planner-commutative IN values changed identity")
	}
	base.Filters[0].Values = []any{"paid", "cancelled"}
	inChanged, err := planner.Plan(base)
	if err != nil {
		t.Fatal(err)
	}
	changedDigest, err := inChanged.ResultEquivalenceDigest()
	if err != nil {
		t.Fatal(err)
	}
	if inFirstDigest == changedDigest {
		t.Fatal("changed IN value did not rotate identity")
	}
}
