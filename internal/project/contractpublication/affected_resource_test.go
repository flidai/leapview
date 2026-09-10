package contractpublication

import (
	"errors"
	"testing"
	"time"
)

func TestPolicyEvidenceBindsExactDirectAffectedResource(t *testing.T) {
	baseline := semanticPublication(t, "1.0.0", `["east"]`)
	candidate := semanticPublication(t, "1.1.0", `["east","west"]`)
	evidence, err := DeriveUpdatePolicyEvidence(baseline, candidate)
	if err != nil {
		t.Fatal(err)
	}
	want := directAffectedResources(candidate.Identity())
	if !equalAffectedResources(evidence.AffectedResources, want) {
		t.Fatalf("affected resources = %#v, want %#v", evidence.AffectedResources, want)
	}
	detached := evidence.Affected()
	detached[0].AuthoredID = "semantic-model:detached"
	if !equalAffectedResources(evidence.AffectedResources, want) {
		t.Fatalf("affected-resource accessor mutated evidence: %#v", evidence.AffectedResources)
	}

	for name, mutate := range map[string]func(*PolicyEvidence){
		"missing": func(value *PolicyEvidence) { value.AffectedResources = nil },
		"extra": func(value *PolicyEvidence) {
			value.AffectedResources = append(value.AffectedResources, value.AffectedResources[0])
		},
		"different resource": func(value *PolicyEvidence) {
			value.AffectedResources[0].AuthoredID = "semantic-model:other"
		},
		"different scope": func(value *PolicyEvidence) {
			value.AffectedResources[0].Scope = "consumer-graph"
		},
	} {
		t.Run(name, func(t *testing.T) {
			tampered := evidence.Clone()
			mutate(&tampered)
			digest, digestErr := tampered.computeDigest()
			if digestErr != nil {
				t.Fatal(digestErr)
			}
			tampered.EvidenceDigest = digest
			if err := tampered.Validate(); !errors.Is(err, ErrInvalidPolicy) {
				t.Fatalf("tampered affected-resource evidence error = %v", err)
			}
		})
	}
}

func TestWideningApprovalDigestBindsAffectedResourceEvidence(t *testing.T) {
	baseline := semanticPublication(t, "1.0.0", `["east"]`)
	candidate := semanticPublication(t, "1.1.0", `["east","west"]`)
	policy, err := DeriveUpdatePolicyEvidence(baseline, candidate)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	approval, err := PrepareWideningApproval(policy, "principal:reviewer", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	tampered := policy.Clone()
	tampered.AffectedResources[0].Scope = "consumer-graph"
	digest, err := tampered.computeDigest()
	if err != nil {
		t.Fatal(err)
	}
	tampered.EvidenceDigest = digest
	if err := ValidateAdmission(PolicyContext{BaselineKind: BaselineExisting, Existing: &baseline}, candidate, tampered, &approval, now); err == nil {
		t.Fatal("approval accepted altered affected-resource evidence")
	}
}
