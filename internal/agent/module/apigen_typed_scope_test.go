package module

import (
	"context"
	"net/http"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

func TestAgentAPIGenTypedCredentialUsesExactOperationPair(t *testing.T) {
	operation := agenttools.APIGenOperation{Contract: agenttools.OperationContract{
		OperationID: "querySemanticModel", Protected: true, AuthzMode: "privilege", Action: string(access.ActionSemanticQuery),
		Resolver:   string(access.TypedOperationResolverSemanticModel),
		Extensions: map[string]any{"x-authz": map[string]any{"mode": "privilege", "privilege": "RESOURCE_USE"}},
	}}
	model, err := access.NewResourceRef("model:sales", projectgraph.KindSemanticModel)
	if err != nil {
		t.Fatal(err)
	}
	query, err := access.NewExactPermissionPair(access.ActionSemanticQuery, "project:analytics", model)
	if err != nil {
		t.Fatal(err)
	}
	consume, err := access.NewExactPermissionPair(access.ActionSemanticConsume, "project:analytics", model)
	if err != nil {
		t.Fatal(err)
	}
	otherModel, err := access.NewResourceRef("model:other", projectgraph.KindSemanticModel)
	if err != nil {
		t.Fatal(err)
	}
	otherTarget, err := access.NewExactPermissionPair(access.ActionSemanticQuery, "project:analytics", otherModel)
	if err != nil {
		t.Fatal(err)
	}
	read, err := access.NewExactPermissionPair(access.ActionSemanticRead, "project:analytics", model)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name        string
		permissions []access.PermissionPair
		wantAllow   bool
	}{
		{name: "exact typed pair", permissions: []access.PermissionPair{query, consume}, wantAllow: true},
		{name: "capability-only fallback is denied", permissions: []access.PermissionPair{consume}, wantAllow: false},
		{name: "wrong exact target is denied", permissions: []access.PermissionPair{otherTarget, consume}, wantAllow: false},
		{name: "wrong exact action is denied", permissions: []access.PermissionPair{read, consume}, wantAllow: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			scope := agent.Scope{ProjectID: "project:analytics", PrincipalID: "principal", Credential: agent.CredentialScope{
				Restricted: true, Capabilities: []string{"RESOURCE_USE"}, PermissionProfile: access.PermissionCatalogProfile,
				Permissions: test.permissions,
			}}
			request := typedAgentRouteRequest(operation.Contract.Path, map[string]string{"project": "project:analytics", "model": "model:sales"})
			result, allowed := (&Module{}).authorizeAPIGenOperation(context.Background(), scope, operation, request)
			if allowed != test.wantAllow {
				t.Fatalf("allowed = %t, result = %#v, want %t", allowed, result, test.wantAllow)
			}
		})
	}
}

func typedAgentRouteRequest(path string, params map[string]string) *http.Request {
	request, _ := http.NewRequest(http.MethodPost, path, nil)
	route := chi.NewRouteContext()
	for name, value := range params {
		route.URLParams.Add(name, value)
	}
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
}
