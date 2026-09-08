package architecture

import (
	"strings"
	"testing"
)

// This guards composition ownership and ordering, not production qualification.
// Executable admission tests must still exercise the retained evidence checks.
func TestSemanticActivationReadinessPreservesAuthorities(t *testing.T) {
	root := repoRoot(t)
	composition := readArchitectureFixture(t, root, "internal/app/composition.go")
	start := strings.Index(composition, "sealedControlCoordinator.ApprovalVerifier =")
	if start < 0 {
		t.Fatal("sealed approval composition is missing")
	}
	approval := composition[start:]
	validation := strings.Index(approval, "validateSealedPublicationPlanBinding(")
	bootstrap := strings.Index(approval, "if binding.Bootstrap")
	exemption := strings.Index(approval, "if !plan.Governance.RequiresApproval")
	if validation < 0 || bootstrap < 0 || exemption < 0 || validation > bootstrap || validation > exemption {
		t.Fatal("retained plan validation must precede bootstrap and approval exemptions")
	}
	for _, path := range []string{
		"internal/app/sealed_approval.go",
		"internal/app/runtimefactory/semantic_activation_readiness.go",
	} {
		body := readArchitectureFixture(t, root, path)
		for _, forbidden := range []string{`"crypto/`, `"database/sql"`, "type Approval struct", "func matchesGrant", "CanonicalSemanticAttributeValues("} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s introduces another authority: %s", path, forbidden)
			}
		}
	}
	if !strings.Contains(readArchitectureFixture(t, root, "internal/app/sealed_approval.go"), "plan.Validate()") {
		t.Fatal("readiness must reuse deployment plan canonical validation")
	}
	planning := readArchitectureFixture(t, root, "internal/app/runtimefactory/delivery_plan.go")
	gate := strings.Index(planning, "validateSemanticActivationReadiness(artifacts)")
	digest := strings.Index(planning, "materializationIdentity(artifacts)")
	if gate < 0 || digest < 0 || gate > digest {
		t.Fatal("protected readiness rejection must precede candidate plan construction")
	}
}
