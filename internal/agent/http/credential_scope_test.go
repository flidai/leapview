package http

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestAgentCredentialScopePreservesTokenAttenuation(t *testing.T) {
	dynamic := agentCredentialScope(access.APICredential{Token: access.APIToken{ID: "token-dynamic"}})
	if !dynamic.Restricted || dynamic.Capabilities != nil {
		t.Fatalf("dynamic token scope = %#v", dynamic)
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
