package deployment

import (
	"regexp"
	"testing"
)

func TestDeriveRelationNamespaceDeterministicAndSQLSafe(t *testing.T) {
	input := RelationNamespaceInput{
		CandidateID:  "0198f2c0-7c7a-7f00-8a11-000000000304",
		AttemptID:    "0198f2c0-7c7a-7f00-8a11-000000000303",
		FencingEpoch: 7,
	}
	one, err := DeriveRelationNamespace(input)
	if err != nil {
		t.Fatal(err)
	}
	two, err := DeriveRelationNamespace(input)
	if err != nil {
		t.Fatal(err)
	}
	if one != two {
		t.Fatalf("derivation is not deterministic: %q != %q", one, two)
	}
	if len(one) != MaxRelationNamespaceBytes {
		t.Fatalf("namespace length=%d, want %d", len(one), MaxRelationNamespaceBytes)
	}
	if !regexp.MustCompile(`^_[a-z0-9]+$`).MatchString(one) {
		t.Fatalf("namespace is not a lowercase SQL identifier: %q", one)
	}
}

func TestDeriveRelationNamespaceRejectsInvalidOrNonCanonicalInput(t *testing.T) {
	base := RelationNamespaceInput{
		CandidateID:  "0198f2c0-7c7a-7f00-8a11-000000000304",
		AttemptID:    "0198f2c0-7c7a-7f00-8a11-000000000303",
		FencingEpoch: 1,
	}
	for name, mutate := range map[string]func(*RelationNamespaceInput){
		"missing candidate":   func(in *RelationNamespaceInput) { in.CandidateID = "" },
		"uppercase candidate": func(in *RelationNamespaceInput) { in.CandidateID = "0198F2C0-7C7A-7F00-8A11-000000000304" },
		"whitespace attempt":  func(in *RelationNamespaceInput) { in.AttemptID += " " },
		"zero fence":          func(in *RelationNamespaceInput) { in.FencingEpoch = 0 },
		"negative fence":      func(in *RelationNamespaceInput) { in.FencingEpoch = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			mutate(&input)
			if _, err := DeriveRelationNamespace(input); err == nil {
				t.Fatal("invalid input unexpectedly derived a namespace")
			}
		})
	}
}

func TestDeriveRelationNamespaceSuccessorAttemptsAndFencesAreDisjoint(t *testing.T) {
	base := RelationNamespaceInput{
		CandidateID:  "0198f2c0-7c7a-7f00-8a11-000000000304",
		AttemptID:    "0198f2c0-7c7a-7f00-8a11-000000000303",
		FencingEpoch: 1,
	}
	baseNamespace, err := DeriveRelationNamespace(base)
	if err != nil {
		t.Fatal(err)
	}
	for name, successor := range map[string]RelationNamespaceInput{
		"successor attempt": {CandidateID: base.CandidateID, AttemptID: "0198f2c0-7c7a-7f00-8a11-000000000305", FencingEpoch: base.FencingEpoch},
		"successor fence":   {CandidateID: base.CandidateID, AttemptID: base.AttemptID, FencingEpoch: base.FencingEpoch + 1},
	} {
		t.Run(name, func(t *testing.T) {
			successorNamespace, err := DeriveRelationNamespace(successor)
			if err != nil {
				t.Fatal(err)
			}
			if successorNamespace == baseNamespace {
				t.Fatalf("successor reused predecessor namespace %q", baseNamespace)
			}
		})
	}
}
