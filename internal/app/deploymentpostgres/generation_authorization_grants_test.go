package deploymentpostgres

import (
	"encoding/json"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/release"
)

func TestGenerationAdmissionRequiresExactTargetGrants(t *testing.T) {
	input := validGenerationAdmissionInput(t)
	resource, _ := access.NewResourceRef("connection-admission", graph.KindConnection)
	grant := access.AuthorizationGrant{ID: "read-connection", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: admissionPolicySubjectID}, Resource: resource, Capability: access.CapabilityResourceRead}
	policy := access.AuthorizationPolicy{Scope: access.AuthorizationPolicyScope{TargetID: "target-demo", ProjectID: string(input.Bundle.ProjectID), Environment: string(input.Bundle.Environment)}, RoleBindings: []access.RoleBinding{admissionAuthorizationBinding()}, Grants: []access.AuthorizationGrant{grant}}
	doc, err := manifest.AccessPolicyFromAuthorizationPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	input.Bundle.AccessPolicyJSON = string(encoded)
	snapshot, err := manifest.CompileAuthorizationSnapshot(graph.ServingIdentity{ProjectID: input.Bundle.ProjectID, Environment: string(input.Bundle.Environment), GenerationID: release.CandidatePolicyGenerationID}, input.Graph, doc)
	if err != nil {
		t.Fatal(err)
	}
	input.Generation.SecurityDomainFingerprint, err = snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateGenerationAuthorizationSnapshot(policy, input); err != nil {
		t.Fatal(err)
	}
	policy.Grants = nil
	if err := validateGenerationAuthorizationSnapshot(policy, input); err == nil {
		t.Fatal("admitted policy with unowned grants")
	}
	policy.Grants = []access.AuthorizationGrant{grant}
	policy.Grants[0].Capability = access.CapabilityResourceUse
	if err := validateGenerationAuthorizationSnapshot(policy, input); err == nil {
		t.Fatal("admitted changed grant")
	}
}
