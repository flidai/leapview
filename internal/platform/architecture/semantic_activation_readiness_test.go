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
		"internal/app/contract_activation.go",
		"internal/app/runtimefactory/semantic_activation_readiness.go",
		"internal/project/identityledger/activation_reference.go",
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
	gate := strings.Index(planning, "validateSemanticActivationReadiness(artifacts, policy.ContractActivations)")
	digest := strings.Index(planning, "materializationIdentity(artifacts)")
	if gate < 0 || digest < 0 || gate > digest {
		t.Fatal("protected readiness rejection must precede candidate plan construction")
	}
	publicationCommit := readArchitectureFixture(t, root, "internal/deployment/sqlite/plan_delivery_publication.go")
	commitStart := strings.Index(publicationCommit, "func (r *Repository) CommitPublication")
	if commitStart < 0 {
		t.Fatal("delivery publication commit boundary is missing")
	}
	publicationCommit = publicationCommit[commitStart:]
	reservation := strings.Index(publicationCommit, "ReserveDeliveryPublicationActivation")
	approvalRead := strings.Index(publicationCommit, "GetCurrentDeploymentApproval")
	evidenceRead := strings.Index(publicationCommit, "approvalEvidenceFromEventTx")
	evidenceFence := strings.Index(publicationCommit, "validateCanonicalApprovalEvidenceTx")
	approvalCheck := strings.Index(publicationCommit, "ValidateApprovalActivation")
	targetCAS := strings.Index(publicationCommit, "ActivateDeliveryGeneration")
	if reservation < 0 || approvalRead < reservation || evidenceRead < approvalRead || evidenceFence < evidenceRead || approvalCheck < evidenceFence || targetCAS < approvalCheck {
		t.Fatal("approval revocation, freshness, and exact plan evidence must be revalidated under the final SQLite write reservation before target activation")
	}
	approvalRepository := readArchitectureFixture(t, root, "internal/deployment/sqlite/approval_repository.go")
	if !strings.Contains(approvalRepository, "PlanDigest: approval.PlanDigest, ResultDigest: approval.EvidenceDigest") {
		t.Fatal("approval decisions must reuse the immutable delivery event ledger for exact plan/evidence references")
	}
	if !strings.Contains(composition, "WithContractActivationFence") {
		t.Fatal("canonical publication must reuse the identity/publication lifecycle fence")
	}
	if strings.Count(composition, "validateUnsupportedContractServingArtifact") < 3 || !strings.Contains(composition, "validateCanonicalContractServingArtifact") {
		t.Fatal("canonical and legacy activation paths must inspect immutable serving artifacts before selecting a contract fence")
	}
	if strings.Count(composition, "validateLegacySealedContractPlan") < 4 {
		t.Fatal("legacy sealed publish and rollback resolvers must reject contract-bearing plans")
	}
	candidateArtifacts := readArchitectureFixture(t, root, "internal/release/module/candidate_artifacts.go")
	if strings.Count(candidateArtifacts, "BaseArtifact: base.artifact") < 2 {
		t.Fatal("both inspected and freshly prepared candidates must retain the exact active base artifact")
	}
	evidence := readArchitectureFixture(t, root, "internal/deployment/plan_delivery_evidence.go")
	if !strings.Contains(evidence, "[]identityledger.PolicyActivationReference") {
		t.Fatal("delivery plan evidence does not retain the existing publication/policy reference")
	}
}
