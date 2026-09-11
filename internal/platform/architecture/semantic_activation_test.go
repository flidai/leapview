package architecture

import (
	"strings"
	"testing"
)

func TestSemanticAccessActivationCutoverBoundary(t *testing.T) {
	root := repoRoot(t)
	accessSpec := readArchitectureFixture(t, root, "api/typespec/access.tsp")
	for _, legacy := range []string{"interface DataPolicies", "DataPolicyRequest", "/projects/{project}/data-policies", "data_policy.created"} {
		if strings.Contains(accessSpec, legacy) {
			t.Errorf("public access contract still exposes legacy authoring %q", legacy)
		}
	}

	activation := readArchitectureFixture(t, root, "internal/app/semanticactivation/activation.go")
	for _, boundary := range []string{
		"CompiledPolicyDigest", "ValidateQualifiedPublication", "stableAuthority",
		"SemanticBarrierProfile", "SemanticConsumerProfile", "SemanticCacheProfile", "SemanticAuditProfile",
		"PersistCanonicalAuditEvent", "validateLegacyDataPolicyCutover",
	} {
		if !strings.Contains(activation, boundary) {
			t.Errorf("semantic activation fence omits %q", boundary)
		}
	}

	jobs := readArchitectureFixture(t, root, "internal/deployment/module/jobs.go")
	fence := strings.Index(jobs, "m.jobs.ValidateCutover(ctx")
	commit := strings.Index(jobs, "m.jobs.Coordinator.Activate(ctx")
	if fence < 0 || commit < 0 || fence > commit {
		t.Fatal("semantic cutover validation must execute before the activation commit")
	}

	cutover := readArchitectureFixture(t, root, "adr/specifications/semantic-access-activation-cutover.md")
	for _, statement := range []string{
		"representable row restriction", "manual redesign", "historical generation", "VAL-11 remains **PARTIAL**",
	} {
		if !strings.Contains(cutover, statement) {
			t.Errorf("activation guidance omits %q", statement)
		}
	}
}
