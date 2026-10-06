package module

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestAgentVisualQueryRestrictedCredentialUsesExactSemanticQueryPair(t *testing.T) {
	const projectID = "project:analytics"
	const modelID = "model:sales"

	exactPermissions := visualQueryPermissions(t, access.ActionSemanticQuery, projectID, modelID)
	wrongActionPermissions := append(
		visualQueryPermissions(t, access.ActionSemanticRead, projectID, modelID),
		visualQueryPermissions(t, access.ActionSemanticConsume, projectID, modelID)...,
	)
	wrongTargetPermissions := visualQueryPermissions(t, access.ActionSemanticQuery, projectID, "model:other")

	for _, test := range []struct {
		name        string
		permissions []access.PermissionPair
		wantAllow   bool
	}{
		{name: "exact semantic query pair", permissions: exactPermissions, wantAllow: true},
		{name: "wrong action on exact model", permissions: wrongActionPermissions},
		{name: "semantic query on another model", permissions: wrongTargetPermissions},
	} {
		t.Run(test.name, func(t *testing.T) {
			scope := agenttools.Scope{
				ProjectID:   projectID,
				PrincipalID: "principal-1",
				Credential: agenttools.CredentialScope{
					Restricted:        true,
					Capabilities:      []string{"RESOURCE_USE"},
					PermissionProfile: access.PermissionCatalogProfile,
					Permissions:       test.permissions,
				},
			}
			result, allowed := (&Module{}).authorizeVisualQuery(context.Background(), scope, agenttools.VisualAuthorizationRequest{
				ToolName: agenttools.QueryVisualToolName,
				Model:    modelID,
			})
			if allowed != test.wantAllow {
				t.Fatalf("visual query allowed = %t, result = %#v, want %t", allowed, result, test.wantAllow)
			}
		})
	}
}

func visualQueryPermissions(t *testing.T, action access.Action, projectID, modelID string) []access.PermissionPair {
	t.Helper()

	model, err := access.NewResourceRef(projectgraph.ResourceID(modelID), projectgraph.KindSemanticModel)
	if err != nil {
		t.Fatalf("construct semantic model resource %q: %v", modelID, err)
	}
	query, err := access.NewExactPermissionPair(action, projectgraph.ResourceID(projectID), model)
	if err != nil {
		t.Fatalf("construct exact permission pair for %s on %s: %v", action, modelID, err)
	}
	required, err := access.RequiredPermissionPairs(query)
	if err != nil {
		t.Fatalf("resolve required permission pairs for %s on %s: %v", action, modelID, err)
	}
	return required
}
