package resultidentity

import (
	"bytes"
	"testing"

	"github.com/flidai/leapview/internal/project/contractprojection"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/semanticvalue"
)

func semanticIdentityFixture() *SemanticAccessIdentity {
	return &SemanticAccessIdentity{
		Lifecycle: SemanticLifecycle{InstanceID: "instance_a", ProjectID: "project-a", AuthoredID: "semantic_sales", ResourceKind: projectgraph.KindSemanticModel,
			Sequence: 4, ActiveBundleID: "bundle-a", PublicationVersion: "1.0.0", ProjectionProfile: contractprojection.Profile,
			PublicationDigest: testDigest("a"), AuthorizationRevision: 3},
		ServingStateID: "generation-a", PrincipalID: "principal-a", PolicyProfile: semanticvalue.Profile,
		RegistryRevision: 7, RegistryDigest: testDigest("b"), ControlRevision: 8, ControlDigest: testDigest("c"),
		Attributes: []SemanticAttributeIdentity{
			{DefinitionID: "region", DefinitionName: "region", DefinitionVersion: 1, Type: "String", Shape: "scalar", Source: "direct", ValueDigest: testDigest("d")},
			{DefinitionID: "tenant", DefinitionName: "tenant", DefinitionVersion: 2, Type: "String", Shape: "list", Source: "group", ValueDigest: testDigest("e")},
		},
	}
}

func TestSemanticDependencyDeterministicAndDetached(t *testing.T) {
	input := validDependencyInput()
	input.SemanticAccess = semanticIdentityFixture()
	first, err := NewDependency(input)
	if err != nil {
		t.Fatal(err)
	}
	input.SemanticAccess.Attributes[0], input.SemanticAccess.Attributes[1] = input.SemanticAccess.Attributes[1], input.SemanticAccess.Attributes[0]
	second, err := NewDependency(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest() != second.Digest() || !bytes.Equal(first.Canonical(), second.Canonical()) {
		t.Fatal("attribute order changed identity")
	}
	input.SemanticAccess.Attributes[0].ValueDigest = testDigest("f")
	input.SemanticAccess.Lifecycle.Sequence++
	if !bytes.Equal(first.Canonical(), second.Canonical()) {
		t.Fatal("input alias mutated retained identity")
	}
	for _, forbidden := range [][]byte{[]byte("canonicalValues"), []byte("rawClaims"), []byte("updatedAt")} {
		if bytes.Contains(first.Canonical(), forbidden) {
			t.Fatalf("identity contains %s", forbidden)
		}
	}
}

func TestSemanticDependencyRotatesForSecurityAndLifecycle(t *testing.T) {
	input := validDependencyInput()
	input.SemanticAccess = semanticIdentityFixture()
	base, err := NewDependency(input)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*SemanticAccessIdentity){
		"instance":      func(s *SemanticAccessIdentity) { s.Lifecycle.InstanceID = "instance_b" },
		"project scope": func(s *SemanticAccessIdentity) { s.Lifecycle.ProjectID = "project-b" },
		"restore":       func(s *SemanticAccessIdentity) { s.Lifecycle.Sequence++ },
		"rollback": func(s *SemanticAccessIdentity) {
			s.Lifecycle.ActiveBundleID = "historical-bundle"
			s.Lifecycle.Sequence++
		},
		"publication": func(s *SemanticAccessIdentity) {
			s.Lifecycle.PublicationVersion = "2.0.0"
			s.Lifecycle.PublicationDigest = testDigest("f")
		},
		"role/grant":               func(s *SemanticAccessIdentity) { s.Lifecycle.AuthorizationRevision++ },
		"activation":               func(s *SemanticAccessIdentity) { s.ServingStateID = "generation-b" },
		"principal":                func(s *SemanticAccessIdentity) { s.PrincipalID = "principal-b" },
		"registry":                 func(s *SemanticAccessIdentity) { s.RegistryRevision++; s.RegistryDigest = testDigest("f") },
		"claim mapping revocation": func(s *SemanticAccessIdentity) { s.ControlRevision++; s.ControlDigest = testDigest("f") },
		"attribute":                func(s *SemanticAccessIdentity) { s.Attributes[0].ValueDigest = testDigest("f") },
		"group source":             func(s *SemanticAccessIdentity) { s.Attributes[0].Source = "group" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := validDependencyInput()
			candidate.SemanticAccess = semanticIdentityFixture()
			mutate(candidate.SemanticAccess)
			changed, err := NewDependency(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if changed.Digest() == base.Digest() {
				t.Fatal("changed authority reused dependency")
			}
		})
	}
}

func TestSemanticDependencyRejectsIncompleteAuthority(t *testing.T) {
	for name, mutate := range map[string]func(*SemanticAccessIdentity){
		"wrong kind":                     func(s *SemanticAccessIdentity) { s.Lifecycle.ResourceKind = projectgraph.KindModel },
		"wrong model":                    func(s *SemanticAccessIdentity) { s.Lifecycle.AuthoredID = "other_model" },
		"missing sequence":               func(s *SemanticAccessIdentity) { s.Lifecycle.Sequence = 0 },
		"missing authorization revision": func(s *SemanticAccessIdentity) { s.Lifecycle.AuthorizationRevision = 0 },
		"missing publication":            func(s *SemanticAccessIdentity) { s.Lifecycle.PublicationDigest = "" },
		"missing profile":                func(s *SemanticAccessIdentity) { s.Lifecycle.ProjectionProfile = "" },
		"missing version":                func(s *SemanticAccessIdentity) { s.Lifecycle.PublicationVersion = "" },
		"bad registry":                   func(s *SemanticAccessIdentity) { s.RegistryDigest = "sha256:registry" },
		"missing control":                func(s *SemanticAccessIdentity) { s.ControlRevision = 0 },
		"unbound claims":                 func(s *SemanticAccessIdentity) { s.Attributes[0].Source = "trusted_claim" },
		"duplicate":                      func(s *SemanticAccessIdentity) { s.Attributes = append(s.Attributes, s.Attributes[0]) },
		"bad type":                       func(s *SemanticAccessIdentity) { s.Attributes[0].Type = "SQL" },
	} {
		t.Run(name, func(t *testing.T) {
			input := validDependencyInput()
			input.SemanticAccess = semanticIdentityFixture()
			mutate(input.SemanticAccess)
			if _, err := NewDependency(input); err == nil {
				t.Fatal("invalid authority accepted")
			}
		})
	}
}
