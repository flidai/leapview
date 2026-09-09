package identityledger

import (
	"errors"
	"testing"

	ocidigest "github.com/opencontainers/go-digest"
)

func TestPolicyActivationReferenceBindsExactPublicationGraphAndPolicy(t *testing.T) {
	publication := policyTestExistingPublication(t)
	graphDigest := ocidigest.FromString("graph-a").String()
	reference, err := NewPolicyActivationReference(publication, graphDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err := reference.Matches(publication, graphDigest); err != nil {
		t.Fatalf("matching publication rejected: %v", err)
	}
	advanced := reference
	advanced.LifecycleSequence++
	advanced.ActiveBundleID = "generation-successor"
	if err := advanced.Matches(publication, graphDigest); err != nil {
		t.Fatalf("same immutable publication with a newer active base rejected: %v", err)
	}

	tests := map[string]func(*PolicyActivationReference){
		"publication digest": func(value *PolicyActivationReference) {
			value.Publication.Digest = ocidigest.FromString("other-publication").String()
		},
		"graph digest":       func(value *PolicyActivationReference) { value.GraphDigest = ocidigest.FromString("graph-b").String() },
		"projection profile": func(value *PolicyActivationReference) { value.Publication.ProjectionProfile = "other/v1" },
		"policy digest": func(value *PolicyActivationReference) {
			value.PolicyEvidenceDigest = ocidigest.FromString("other-policy").String()
		},
		"baseline": func(value *PolicyActivationReference) {
			value.Baseline.Digest = ocidigest.FromString("other-baseline").String()
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := reference
			if reference.Baseline != nil {
				baseline := *reference.Baseline
				changed.Baseline = &baseline
			}
			mutate(&changed)
			if err := changed.Matches(publication, graphDigest); !errors.Is(err, ErrPolicyEvidenceConflict) {
				t.Fatalf("mismatch error = %v, want ErrPolicyEvidenceConflict", err)
			}
		})
	}
	unsupported := reference
	unsupported.PolicyEvidenceVersion = RegistryPolicyEvidenceVersion + 1
	if err := unsupported.Validate(); !errors.Is(err, ErrPolicyEvidenceInvalid) {
		t.Fatalf("unsupported evidence version error=%v, want ErrPolicyEvidenceInvalid", err)
	}
}
