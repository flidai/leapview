package resultidentity

import (
	"errors"
	"strings"
	"testing"
)

func TestEvidenceDerivesExactPlannedRelationSubset(t *testing.T) {
	evidence, err := NewEvidence(validEvidenceInput())
	if err != nil {
		t.Fatalf("NewEvidence() error = %v", err)
	}

	orders, err := evidence.Dependency(validPlanInput("orders"))
	if err != nil {
		t.Fatalf("Dependency(orders) error = %v", err)
	}
	customers, err := evidence.Dependency(validPlanInput("customers"))
	if err != nil {
		t.Fatalf("Dependency(customers) error = %v", err)
	}
	if orders.Digest() == customers.Digest() {
		t.Fatal("different planned relation subsets produced the same dependency")
	}
	if got := string(orders.Canonical()); strings.Contains(got, `"id":"model_customers"`) || !strings.Contains(got, `"id":"model_orders"`) {
		t.Fatalf("orders dependency relations = %s", got)
	}
}

func TestEvidenceCanonicalizesPlanDatasetOrderAndDeduplicatesAliasedRelations(t *testing.T) {
	input := validEvidenceInput()
	input.DatasetRelations = append(input.DatasetRelations, DatasetRelation{
		Dataset: "recent_orders", Relation: input.DatasetRelations[0].Relation,
	})
	evidence, err := NewEvidence(input)
	if err != nil {
		t.Fatalf("NewEvidence() error = %v", err)
	}
	leftInput := validPlanInput("customers", "orders", "recent_orders")
	left, err := evidence.Dependency(leftInput)
	if err != nil {
		t.Fatalf("Dependency(left) error = %v", err)
	}
	rightInput := validPlanInput("recent_orders", "orders", "customers")
	right, err := evidence.Dependency(rightInput)
	if err != nil {
		t.Fatalf("Dependency(right) error = %v", err)
	}
	if left.Digest() != right.Digest() || string(left.Canonical()) != string(right.Canonical()) {
		t.Fatal("planned dataset order changed dependency identity")
	}
	if got := string(left.Canonical()); strings.Count(got, `"id":"model_orders"`) != 1 {
		t.Fatalf("aliased relation was not deduplicated: %s", got)
	}
}

func TestEvidenceFailsClosedForIncompletePlanEvidence(t *testing.T) {
	evidence, err := NewEvidence(validEvidenceInput())
	if err != nil {
		t.Fatalf("NewEvidence() error = %v", err)
	}
	for _, test := range []struct {
		name  string
		input PlanInput
	}{
		{name: "no datasets", input: validPlanInput()},
		{name: "unknown dataset", input: validPlanInput("missing")},
		{name: "duplicate dataset", input: validPlanInput("orders", "orders")},
		{name: "invalid planner digest", input: func() PlanInput { value := validPlanInput("orders"); value.PlannerDigest = ""; return value }()},
		{name: "invalid settings digest", input: func() PlanInput { value := validPlanInput("orders"); value.SettingsDigest = ""; return value }()},
		{name: "invalid result format", input: func() PlanInput { value := validPlanInput("orders"); value.ResultFormat = ResultFormat{}; return value }()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := evidence.Dependency(test.input); !errors.Is(err, ErrInvalidDependency) {
				t.Fatalf("Dependency() error = %v, want ErrInvalidDependency", err)
			}
		})
	}
}

func TestEvidenceOwnsDatasetRelationProjection(t *testing.T) {
	input := validEvidenceInput()
	evidence, err := NewEvidence(input)
	if err != nil {
		t.Fatal(err)
	}
	input.DatasetRelations[0].Dataset = "mutated"
	input.DatasetRelations[0].Relation.RevisionDigest = testDigest("9")
	dependency, err := evidence.Dependency(validPlanInput("orders"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(dependency.Canonical()); !strings.Contains(got, testDigest("b")) || strings.Contains(got, testDigest("9")) {
		t.Fatalf("dependency retained mutable evidence input: %s", got)
	}
}

func TestEvidenceRejectsConflictingAliasRevisions(t *testing.T) {
	input := validEvidenceInput()
	input.DatasetRelations = append(input.DatasetRelations, DatasetRelation{
		Dataset:  "recent_orders",
		Relation: RelationRevision{RelationID: "model_orders", RevisionDigest: testDigest("9")},
	})
	if _, err := NewEvidence(input); !errors.Is(err, ErrInvalidDependency) {
		t.Fatalf("NewEvidence() error = %v, want ErrInvalidDependency", err)
	}
}

func validEvidenceInput() EvidenceInput {
	return EvidenceInput{
		SemanticModelID: "semantic_sales", SemanticModelDigest: testDigest("a"),
		DatasetRelations: []DatasetRelation{
			{Dataset: "orders", Relation: RelationRevision{RelationID: "model_orders", RevisionDigest: testDigest("b")}},
			{Dataset: "customers", Relation: RelationRevision{RelationID: "model_customers", RevisionDigest: testDigest("c")}},
		},
		BindingFingerprint: testDigest("d"), RuntimeDigest: testDigest("e"),
		CapabilityDigest: testDigest("f"),
	}
}

func validPlanInput(datasets ...string) PlanInput {
	return PlanInput{
		Datasets: datasets, PlannerDigest: testDigest("6"), SettingsDigest: testDigest("1"),
		ResultFormat: ResultFormat{Name: "arrow-result", Version: 1},
	}
}
