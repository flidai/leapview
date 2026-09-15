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
		"PolicyDefinitionDigest", "ValidateQualifiedPublication", "ValidateHistoricalPublication", "SemanticAttributeActivationAuthorityTx",
		"SemanticBarrierProfile", "SemanticConsumerProfile", "SemanticCacheProfile", "SemanticAuditProfile",
		"PersistCanonicalAuditEvent", "validateLegacyDataPolicyCutover",
	} {
		if !strings.Contains(activation, boundary) {
			t.Errorf("semantic activation fence omits %q", boundary)
		}
	}

	composition := readArchitectureFixture(t, root, "internal/app/postgres_build.go")
	if !strings.Contains(composition, "BeforeNativeActivationCommit") || !strings.Contains(composition, "semanticActivation.ValidatePublication(ctx, tx, publication)") {
		t.Fatal("semantic cutover validation must execute inside the native activation transaction")
	}
	if !strings.Contains(composition, "nativeRefreshFinalizer.BeforeActivationCommit = semanticActivation.ValidatePublication") {
		t.Fatal("refresh generation activation must share the semantic cutover fence")
	}
	jobs := readArchitectureFixture(t, root, "internal/deployment/module/jobs.go")
	if strings.Contains(jobs, "ValidateCutover") {
		t.Fatal("semantic cutover validation must not run before the coordinator opens its activation transaction")
	}
	authority := readArchitectureFixture(t, root, "internal/access/postgres/semantic_activation_authority.go")
	for _, lock := range []string{"LockSemanticAttributeRegistry", "lockSemanticAttributeControlState"} {
		if !strings.Contains(authority, lock) {
			t.Errorf("semantic activation authority omits transaction lock %q", lock)
		}
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
