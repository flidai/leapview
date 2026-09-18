package http

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestAgentCredentialScopeRejectsOmittedTokenCapabilities(t *testing.T) {
	omitted := agentCredentialScope(access.APICredential{Token: access.APIToken{ID: "token-omitted"}})
	if !omitted.Restricted || omitted.Capabilities == nil || len(omitted.Capabilities) != 0 {
		t.Fatalf("omitted token scope = %#v", omitted)
	}

	denyAll := agentCredentialScope(access.APICredential{Token: access.APIToken{ID: "token-deny", Capabilities: []access.Capability{}}})
	if !denyAll.Restricted || denyAll.Capabilities == nil || len(denyAll.Capabilities) != 0 {
		t.Fatalf("deny-all token scope = %#v", denyAll)
	}
}

func TestAgentCredentialScopePreservesAuthoringProjectAndCapabilities(t *testing.T) {
	scope, err := access.NewAuthoringScope(
		"instance-prod", projectgraph.ResourceID("project:analytics"),
		[]access.Capability{access.CapabilityResourcePublish},
	)
	if err != nil {
		t.Fatal(err)
	}
	got := agentCredentialScope(access.APICredential{Authoring: &access.AuthoringSession{Scope: scope}})
	if !got.Restricted || got.ProjectID != "project:analytics" || len(got.Capabilities) != 1 || got.Capabilities[0] != "RESOURCE_PUBLISH" {
		t.Fatalf("authoring credential scope = %#v", got)
	}
}

func TestAgentCredentialScopePreservesTypedTokenPermissionCeiling(t *testing.T) {
	resource, err := access.NewResourceRef("model:sales", projectgraph.KindSemanticModel)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(access.ActionSemanticRead, "project:analytics", resource)
	if err != nil {
		t.Fatal(err)
	}
	credential := access.APICredential{Token: access.APIToken{
		ID: "typed-token", PermissionProfile: access.PermissionCatalogProfile,
		Permissions: []access.PermissionPair{pair}, Capabilities: []access.Capability{access.CapabilityResourceUse},
	}}
	got := agentCredentialScope(credential)
	if got.PermissionProfile != access.PermissionCatalogProfile || len(got.Permissions) != 1 || got.Permissions[0] != pair {
		t.Fatalf("typed credential scope = %#v, want profile and exact permission pair", got)
	}
	if len(got.Capabilities) != 1 || got.Capabilities[0] != string(access.CapabilityResourceUse) {
		t.Fatalf("legacy capability projection = %#v", got.Capabilities)
	}
	credential.Token.Permissions[0].Target.ResourceID = "model:mutated"
	if got.Permissions[0].Target.ResourceID != resource.ID() {
		t.Fatalf("typed permission scope aliases credential input: %#v", got.Permissions)
	}
}
