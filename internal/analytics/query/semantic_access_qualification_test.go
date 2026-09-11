package query

import "testing"

func TestSemanticAccessQualificationAbsentRequiredGrantsImposesNoRequirement(t *testing.T) {
	model := semanticAccessTestModel(t)
	dataset := model.AccessPolicy.Datasets["orders"]
	dataset.RequiredAccessGrants = nil
	dataset.AccessFilters = nil
	model.AccessPolicy.Datasets["orders"] = dataset
	model.AccessPolicy.Dimensions = nil
	model.AccessPolicy.Metrics = nil

	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := CompileSemanticAccessPolicy(
		"instance-1",
		"semantic-model:sales",
		"generation-9",
		model,
		compiled,
		semanticAccessRegistry(semanticAccessDefinitions()),
	)
	if err != nil {
		t.Fatal(err)
	}

	snapshot, authority := semanticAccessSnapshot(t, nil)
	decision, err := EvaluateSemanticAccess(policy, snapshot, authority)
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range decision.GrantResults {
		if grant.Allowed {
			t.Fatalf("grant %q unexpectedly matched without attributes", grant.Name)
		}
	}
	orders, ok := decision.Dataset("orders")
	if !ok || !orders.Allowed || len(orders.DeniedGrants) != 0 || orders.Predicate != nil {
		t.Fatalf("dataset with absent required grants gained an authorization requirement: %#v", orders)
	}
}
