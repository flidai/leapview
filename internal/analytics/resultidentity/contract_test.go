package resultidentity

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestDependencyCanonicalSerializationAndDigest(t *testing.T) {
	if DependencyVersion != 1 || PartitionVersion != 1 || CacheKeyFormatVersion != 1 {
		t.Fatalf("contract versions = dependency %d, partition %d, cache key %d; want all 1", DependencyVersion, PartitionVersion, CacheKeyFormatVersion)
	}
	if DependencyDigestDomain != "flid.resultidentity.dependency.v1" {
		t.Fatalf("DependencyDigestDomain = %q", DependencyDigestDomain)
	}
	dependency, err := NewDependency(validDependencyInput())
	if err != nil {
		t.Fatalf("NewDependency() error = %v", err)
	}

	wantCanonical := `{"version":1,"semanticModel":{"id":"semantic_sales","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"relations":[{"id":"orders","revisionDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},{"id":"returns","revisionDigest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}],"bindingFingerprint":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","execution":{"plannerDigest":"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","runtimeDigest":"sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff","capabilityDigest":"sha256:1111111111111111111111111111111111111111111111111111111111111111","settingsDigest":"sha256:2222222222222222222222222222222222222222222222222222222222222222"},"resultFormat":{"name":"arrow-ipc","version":1}}`
	if got := string(dependency.Canonical()); got != wantCanonical {
		t.Fatalf("Canonical() = %s, want %s", got, wantCanonical)
	}

	preimage := append([]byte("flid.resultidentity.dependency.v1"), 0)
	preimage = append(preimage, wantCanonical...)
	wantDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(preimage))
	if got := dependency.Digest(); got != wantDigest {
		t.Fatalf("Digest() = %q, want %q", got, wantDigest)
	}
	if dependency.Version() != DependencyVersion {
		t.Fatalf("Version() = %d, want %d", dependency.Version(), DependencyVersion)
	}
}

func TestDependencyCanonicalizesRelationsAndDefensivelyCopies(t *testing.T) {
	input := validDependencyInput()
	input.Relations[0], input.Relations[1] = input.Relations[1], input.Relations[0]
	dependency, err := NewDependency(input)
	if err != nil {
		t.Fatalf("NewDependency() error = %v", err)
	}
	originalCanonical := dependency.Canonical()
	originalDigest := dependency.Digest()
	ordered, err := NewDependency(validDependencyInput())
	if err != nil {
		t.Fatalf("NewDependency(ordered) error = %v", err)
	}
	if string(ordered.Canonical()) != string(originalCanonical) || ordered.Digest() != originalDigest {
		t.Fatal("relation input order changed canonical dependency identity")
	}

	input.Relations[0].RelationID = "mutated"
	input.Relations[0].RevisionDigest = testDigest("9")
	returned := dependency.Canonical()
	returned[0] = 'x'

	if got := dependency.Canonical(); string(got) != string(originalCanonical) {
		t.Fatalf("Canonical() changed through an input or output alias: %s", got)
	}
	if got := dependency.Digest(); got != originalDigest {
		t.Fatalf("Digest() changed through an input alias: %q", got)
	}
	if strings.Index(string(originalCanonical), `"id":"orders"`) > strings.Index(string(originalCanonical), `"id":"returns"`) {
		t.Fatalf("relations are not ordered by canonical resource ID: %s", originalCanonical)
	}
}

func TestDependencyDigestRotatesForEveryResultAffectingInput(t *testing.T) {
	base := validDependencyInput()
	baseline, err := NewDependency(base)
	if err != nil {
		t.Fatalf("NewDependency() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*DependencyInput)
	}{
		{name: "semantic model ID", mutate: func(input *DependencyInput) { input.SemanticModelID = "semantic_other" }},
		{name: "semantic model digest", mutate: func(input *DependencyInput) { input.SemanticModelDigest = testDigest("3") }},
		{name: "relation set", mutate: func(input *DependencyInput) {
			input.Relations = append(input.Relations, RelationRevision{RelationID: "customers", RevisionDigest: testDigest("0")})
		}},
		{name: "relation revision", mutate: func(input *DependencyInput) { input.Relations[0].RevisionDigest = testDigest("4") }},
		{name: "binding fingerprint", mutate: func(input *DependencyInput) { input.BindingFingerprint = testDigest("5") }},
		{name: "planner", mutate: func(input *DependencyInput) { input.Execution.PlannerDigest = testDigest("6") }},
		{name: "runtime", mutate: func(input *DependencyInput) { input.Execution.RuntimeDigest = testDigest("7") }},
		{name: "capability", mutate: func(input *DependencyInput) { input.Execution.CapabilityDigest = testDigest("8") }},
		{name: "settings", mutate: func(input *DependencyInput) { input.Execution.SettingsDigest = testDigest("9") }},
		{name: "result format name", mutate: func(input *DependencyInput) { input.ResultFormat.Name = "arrow-stream" }},
		{name: "result format version", mutate: func(input *DependencyInput) { input.ResultFormat.Version++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := cloneDependencyInput(base)
			test.mutate(&input)
			changed, err := NewDependency(input)
			if err != nil {
				t.Fatalf("NewDependency() error = %v", err)
			}
			if changed.Digest() == baseline.Digest() {
				t.Fatalf("Digest() did not rotate from %q", baseline.Digest())
			}
		})
	}
}

func TestPartitionCanonicalSerializationAndIsolation(t *testing.T) {
	production, err := NewPartition(PartitionInput{
		Kind: PartitionProduction, ProjectID: "project_sales", Environment: "prod",
	})
	if err != nil {
		t.Fatalf("NewPartition(production) error = %v", err)
	}
	candidate, err := NewPartition(PartitionInput{
		Kind: PartitionCandidate, ProjectID: "project_sales", Environment: "prod", CandidateID: "candidate_1",
	})
	if err != nil {
		t.Fatalf("NewPartition(candidate) error = %v", err)
	}
	otherCandidate, err := NewPartition(PartitionInput{
		Kind: PartitionCandidate, ProjectID: "project_sales", Environment: "prod", CandidateID: "candidate_2",
	})
	if err != nil {
		t.Fatalf("NewPartition(other candidate) error = %v", err)
	}

	if got, want := string(production.Canonical()), `{"version":1,"kind":"production","projectId":"project_sales","environment":"prod"}`; got != want {
		t.Fatalf("production Canonical() = %s, want %s", got, want)
	}
	if got, want := string(candidate.Canonical()), `{"version":1,"kind":"candidate","projectId":"project_sales","environment":"prod","candidateId":"candidate_1"}`; got != want {
		t.Fatalf("candidate Canonical() = %s, want %s", got, want)
	}
	if string(production.Canonical()) == string(candidate.Canonical()) || string(candidate.Canonical()) == string(otherCandidate.Canonical()) {
		t.Fatal("production and candidate partitions are not isolated")
	}
	if candidate.Kind() != PartitionCandidate || candidate.ProjectID() != "project_sales" || candidate.Environment() != "prod" || candidate.CandidateID() != "candidate_1" {
		t.Fatalf("candidate accessors returned unexpected values")
	}
	if production.Version() != PartitionVersion || candidate.Version() != PartitionVersion {
		t.Fatalf("partition versions = production %d, candidate %d; want %d", production.Version(), candidate.Version(), PartitionVersion)
	}

	returned := candidate.Canonical()
	returned[0] = 'x'
	if got := string(candidate.Canonical()); got[0] != '{' {
		t.Fatalf("Canonical() exposes mutable storage: %s", got)
	}
}

func TestContractsRejectInvalidInputs(t *testing.T) {
	t.Run("dependency", func(t *testing.T) {
		tests := []struct {
			name   string
			mutate func(*DependencyInput)
		}{
			{name: "invalid semantic model ID", mutate: func(input *DependencyInput) { input.SemanticModelID = " semantic_sales" }},
			{name: "invalid semantic digest", mutate: func(input *DependencyInput) { input.SemanticModelDigest = "sha256:ABC" }},
			{name: "invalid relation ID", mutate: func(input *DependencyInput) { input.Relations[0].RelationID = "orders table" }},
			{name: "invalid revision digest", mutate: func(input *DependencyInput) { input.Relations[0].RevisionDigest = "md5:abcd" }},
			{name: "duplicate relation", mutate: func(input *DependencyInput) { input.Relations[1].RelationID = input.Relations[0].RelationID }},
			{name: "invalid binding fingerprint", mutate: func(input *DependencyInput) { input.BindingFingerprint = "" }},
			{name: "invalid planner digest", mutate: func(input *DependencyInput) { input.Execution.PlannerDigest = testDigest("A") }},
			{name: "invalid runtime digest", mutate: func(input *DependencyInput) { input.Execution.RuntimeDigest = "" }},
			{name: "invalid capability digest", mutate: func(input *DependencyInput) { input.Execution.CapabilityDigest = "sha512:" + strings.Repeat("1", 128) }},
			{name: "invalid settings digest", mutate: func(input *DependencyInput) { input.Execution.SettingsDigest = "sha256:" + strings.Repeat("1", 63) }},
			{name: "empty result format", mutate: func(input *DependencyInput) { input.ResultFormat.Name = "" }},
			{name: "noncanonical result format", mutate: func(input *DependencyInput) { input.ResultFormat.Name = " arrow-ipc" }},
			{name: "zero result format version", mutate: func(input *DependencyInput) { input.ResultFormat.Version = 0 }},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				input := cloneDependencyInput(validDependencyInput())
				test.mutate(&input)
				if _, err := NewDependency(input); err == nil {
					t.Fatal("NewDependency() error = nil")
				}
			})
		}
	})

	t.Run("partition", func(t *testing.T) {
		tests := []PartitionInput{
			{},
			{Kind: "preview", ProjectID: "project_sales", Environment: "prod"},
			{Kind: PartitionProduction, ProjectID: "project sales", Environment: "prod"},
			{Kind: PartitionProduction, ProjectID: "project_sales", Environment: " prod"},
			{Kind: PartitionProduction, ProjectID: "project_sales", Environment: "prod", CandidateID: "candidate_1"},
			{Kind: PartitionCandidate, ProjectID: "project_sales", Environment: "prod"},
			{Kind: PartitionCandidate, ProjectID: "project_sales", Environment: "prod", CandidateID: " candidate_1"},
		}
		for _, input := range tests {
			if _, err := NewPartition(input); err == nil {
				t.Fatalf("NewPartition(%#v) error = nil", input)
			}
		}
	})
}

func TestDependencyRejectsEmptyRelationEvidence(t *testing.T) {
	tests := []struct {
		name      string
		relations []RelationRevision
	}{
		{name: "nil", relations: nil},
		{name: "empty", relations: []RelationRevision{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validDependencyInput()
			input.Relations = test.relations
			_, err := NewDependency(input)
			if !errors.Is(err, ErrInvalidDependency) {
				t.Fatalf("NewDependency() error = %v, want ErrInvalidDependency", err)
			}
			if !strings.Contains(err.Error(), "at least one relation revision is required") {
				t.Fatalf("NewDependency() error = %q, want missing relation evidence detail", err)
			}
		})
	}
}

func validDependencyInput() DependencyInput {
	return DependencyInput{
		SemanticModelID:     projectgraph.ResourceID("semantic_sales"),
		SemanticModelDigest: testDigest("a"),
		Relations: []RelationRevision{
			{RelationID: "orders", RevisionDigest: testDigest("b")},
			{RelationID: "returns", RevisionDigest: testDigest("c")},
		},
		BindingFingerprint: testDigest("d"),
		Execution: ExecutionIdentity{
			PlannerDigest:    testDigest("e"),
			RuntimeDigest:    testDigest("f"),
			CapabilityDigest: testDigest("1"),
			SettingsDigest:   testDigest("2"),
		},
		ResultFormat: ResultFormat{Name: "arrow-ipc", Version: 1},
	}
}

func cloneDependencyInput(input DependencyInput) DependencyInput {
	input.Relations = append([]RelationRevision(nil), input.Relations...)
	return input
}

func testDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}
