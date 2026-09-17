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
