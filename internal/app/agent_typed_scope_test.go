package app

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestWithAgentCredentialPreservesTypedPermissionCeiling(t *testing.T) {
	resource, err := access.NewResourceRef("model:sales", projectgraph.KindSemanticModel)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(access.ActionSemanticQuery, "project:analytics", resource)
	if err != nil {
		t.Fatal(err)
	}
	scope := agentmodule.Scope{ProjectID: "project:analytics", PrincipalID: "principal", Credential: agentmodule.CredentialScope{
		Restricted: true, PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{pair},
	}}
	ctx := withAgentCredential(context.Background(), accessmodule.Principal{ID: "principal"}, scope)
	credential, ok := accessmodule.APICredentialFromContext(ctx)
	if !ok || credential.Token.PermissionProfile != access.PermissionCatalogProfile || len(credential.Token.Permissions) != 1 || credential.Token.Permissions[0] != pair {
		t.Fatalf("credential = %#v, ok=%t", credential, ok)
	}
	credential.Token.Permissions[0] = access.PermissionPair{}
	if scope.Credential.Permissions[0] != pair {
		t.Fatal("credential context aliases agent scope permissions")
	}
}
