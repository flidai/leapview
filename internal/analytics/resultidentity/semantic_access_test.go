package resultidentity

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/semanticvalue"
)

func TestSemanticAccessDependencyIsDeterministicAndDetached(t *testing.T) {
	input := validDependencyInput()
	input.SemanticAccess = validSemanticAccessIdentity()
	first, err := NewDependency(input)
	if err != nil {
		t.Fatalf("NewDependency() error = %v", err)
	}
	second, err := NewDependency(input)
	if err != nil {
		t.Fatalf("NewDependency() second error = %v", err)
	}
	if string(first.Canonical()) != string(second.Canonical()) || first.Digest() != second.Digest() {
		t.Fatal("equivalent semantic access evidence changed dependency identity")
	}
	if !strings.Contains(string(first.Canonical()), `"semanticAccess"`) {
		t.Fatalf("protected dependency omitted semantic access evidence: %s", first.Canonical())
	}
	if !first.HasSemanticAccess() || first.SemanticAccess() == nil {
		t.Fatal("protected dependency did not retain typed semantic access evidence")
	}
	public, err := NewDependency(validDependencyInput())
	if err != nil {
		t.Fatalf("NewDependency(public) error = %v", err)
	}
	if public.HasSemanticAccess() || public.SemanticAccess() != nil {
		t.Fatal("public dependency unexpectedly retained semantic access evidence")
	}

	canonical := first.Canonical()
	input.SemanticAccess.PolicyDigest = testDigest("9")
	if got := string(first.Canonical()); got != string(canonical) {
		t.Fatalf("dependency retained a mutable semantic access input: %s", got)
	}
	returned := first.Canonical()
	returned[0] = 'x'
	if got := string(first.Canonical()); got != string(canonical) {
		t.Fatalf("dependency canonical bytes are mutable through accessor: %s", got)
	}
}

func TestSemanticAccessIdentityFieldPartitioning(t *testing.T) {
	base := validDependencyInput()
	base.SemanticAccess = validSemanticAccessIdentity()
	original, err := NewDependency(base)
	if err != nil {
		t.Fatalf("NewDependency() error = %v", err)
	}
	tests := []struct {
		name   string
		change func(*SemanticAccessIdentity)
	}{
		{name: "project", change: func(value *SemanticAccessIdentity) { value.ProjectID = "project:other" }},
		{name: "environment", change: func(value *SemanticAccessIdentity) { value.Environment = "staging" }},
		{name: "instance", change: func(value *SemanticAccessIdentity) {
			value.InstanceID = "instance-2"
			value.PublicationPolicy.Candidate.InstanceID = "instance-2"
		}},
		{name: "generation", change: func(value *SemanticAccessIdentity) { value.Generation = "generation-2" }},
		{name: "principal", change: func(value *SemanticAccessIdentity) { value.PrincipalID = "principal-2" }},
		{name: "actor", change: func(value *SemanticAccessIdentity) { value.ActorID = "actor-2" }},
		{name: "registry", change: func(value *SemanticAccessIdentity) { value.RegistryDigest = testDigest("0") }},
		{name: "control", change: func(value *SemanticAccessIdentity) { value.ControlRevision++ }},
		{name: "effective attributes", change: func(value *SemanticAccessIdentity) { value.EffectiveAttributeDigest = testDigest("4") }},
		{name: "policy", change: func(value *SemanticAccessIdentity) { value.PolicyDigest = testDigest("5") }},
		{name: "decision", change: func(value *SemanticAccessIdentity) { value.DecisionDigest = testDigest("6") }},
		{name: "direct evidence", change: func(value *SemanticAccessIdentity) { value.DirectAssignmentEvidenceDigest = testDigest("7") }},
		{name: "trusted evidence", change: func(value *SemanticAccessIdentity) { value.TrustedClaimEvidenceDigest = testDigest("8") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := cloneDependencyInput(base)
			input.SemanticAccess = validSemanticAccessIdentity()
			test.change(input.SemanticAccess)
			changed, err := NewDependency(input)
			if err != nil {
				t.Fatalf("NewDependency() error = %v", err)
			}
			if changed.Digest() == original.Digest() {
				t.Fatalf("semantic access %s did not rotate dependency digest", test.name)
			}
		})
	}
}

func TestSemanticAccessIdentityRejectsInvalidEvidenceAndModelMismatch(t *testing.T) {
	tests := []struct {
		name   string
		change func(*SemanticAccessIdentity)
	}{
		{name: "missing project", change: func(value *SemanticAccessIdentity) { value.ProjectID = "" }},
		{name: "missing profile", change: func(value *SemanticAccessIdentity) { value.Profile = "other" }},
		{name: "zero registry revision", change: func(value *SemanticAccessIdentity) { value.RegistryRevision = 0 }},
		{name: "zero control revision", change: func(value *SemanticAccessIdentity) { value.ControlRevision = 0 }},
		{name: "invalid registry digest", change: func(value *SemanticAccessIdentity) { value.RegistryDigest = "md5:nope" }},
		{name: "invalid effective digest", change: func(value *SemanticAccessIdentity) { value.EffectiveAttributeDigest = "" }},
		{name: "invalid optional evidence", change: func(value *SemanticAccessIdentity) { value.TrustedClaimEvidenceDigest = "sha256:bad" }},
		{name: "non-semantic publication", change: func(value *SemanticAccessIdentity) { value.PublicationPolicy.Candidate.ResourceKind = "source" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validDependencyInput()
			input.SemanticAccess = validSemanticAccessIdentity()
			test.change(input.SemanticAccess)
			if _, err := NewDependency(input); err == nil {
				t.Fatal("NewDependency() accepted invalid semantic access identity")
			}
		})
	}
	input := validDependencyInput()
	identity := validSemanticAccessIdentity()
	identity.ModelID = "semantic_other"
	input.SemanticAccess = identity
	if _, err := NewDependency(input); err == nil || !strings.Contains(err.Error(), "does not match dependency semantic model ID") {
		t.Fatalf("NewDependency() model mismatch error = %v", err)
	}
}

func validSemanticAccessIdentity() *SemanticAccessIdentity {
	return &SemanticAccessIdentity{
		ProjectID: "project:test", Environment: "prod", InstanceID: "instance-1",
		ModelID: "semantic_sales", Generation: "generation-1", PrincipalID: "principal-1", ActorID: "actor-1",
		Profile: semanticvalue.Profile, RegistryProfile: semanticvalue.Profile, RegistryRevision: 7, RegistryDigest: testDigest("3"),
		ControlProfile: semanticvalue.Profile, ControlRevision: 11, ControlDigest: testDigest("4"),
		EffectiveAttributeDigest: testDigest("5"), PolicyDigest: testDigest("6"), DecisionDigest: testDigest("7"),
		DirectAssignmentEvidenceDigest: testDigest("8"), TrustedClaimEvidenceDigest: testDigest("9"),
		PublicationPolicy: PublicationPolicyIdentity{
			Candidate: PublicationIdentity{InstanceID: "instance-1", AuthoredID: "semantic_sales", ResourceKind: "semantic_model", Version: "1.0.0", VersionBaseline: "1.0.0", ProjectionProfile: "leapview.contract/v1", Digest: testDigest("a")},
			Policy:    PolicyIdentity{BaselineKind: "genesis", Class: "compatible", Compatibility: "additive", StructuralCompatibility: "additive", SemanticCompatibility: "additive", SecurityImpact: "none", PolicyEvidenceDigest: testDigest("b")},
		},
	}
}
