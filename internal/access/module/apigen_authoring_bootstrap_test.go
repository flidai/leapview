package module

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestAPIGenProjectRoleBindingsAllowAuthoringBootstrapCredential(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	contracts := generatedAPIGenContracts()
	operations := map[string]APIGenOperationContract{
		"listProjectRoleBindings":  contracts["listProjectRoleBindings"],
		"createProjectRoleBinding": contracts["createProjectRoleBinding"],
	}
	module := browserGuardModule(browserGuardRepository{admin: true}, Principal{}, false)
	module.auth = &Auth{}
	module.authoringProjectID = func(context.Context) (projectgraph.ResourceID, error) {
		return projectID, nil
	}
	authorizer, err := module.APIGenAuthorizer(
		apigenRuntimeFake{project: projectID, err: errors.New("no active serving generation")},
		operations,
		APIGenResourceResolvers{Project: apigenResolver("project", projectgraph.KindProjectNamespace)},
	)
	if err != nil {
		t.Fatal(err)
	}
	authorizer.SetBootstrapAuthorizer(func(_ context.Context, _ *http.Request, operation string, project projectgraph.ResourceID, capability access.Capability) (APIGenBootstrapDecision, error) {
		if _, ok := operations[operation]; !ok || project != projectID || capability != access.CapabilityProjectAdmin {
			t.Fatalf("bootstrap decision identity = %q/%s/%s", operation, project, capability)
		}
		return APIGenBootstrapDecision{Handled: true, Allowed: true}, nil
	})
	scope, err := access.NewAuthoringScope("instance-prod", projectID, []access.Capability{access.CapabilityProjectAdmin})
	if err != nil {
		t.Fatal(err)
	}
	credential := access.APICredential{
		Principal: access.Principal{ID: "authoring-admin", Kind: access.PrincipalKindUser},
		Authoring: &access.AuthoringSession{
			ID: "authoring-1", Kind: access.AuthoringSessionHumanCLI,
			ClientID: access.AuthoringCLIClientID, PrincipalID: "authoring-admin", Scope: scope,
		},
	}

	for operationID, contract := range operations {
		t.Run(operationID, func(t *testing.T) {
			protected, ok := authorizer.Protect(operationID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				marker, marked := BootstrapAuthorizationFromContext(r.Context())
				if !marked || marker.ProjectID != projectID || marker.PrincipalID != "authoring-admin" || marker.Capability != access.CapabilityProjectAdmin {
					t.Fatalf("authoring bootstrap marker = %#v, marked=%t", marker, marked)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			if !ok || protected == nil {
				t.Fatal("role-binding authorizer was not created")
			}
			request := apigenRequest(contract.Method, "/api/v1/projects/project_demo/role-bindings", map[string]string{"project": projectID.String()})
			request.Header.Set("Authorization", "Bearer authoring-token")
			request = request.WithContext(WithPrincipal(request.Context(), Principal{ID: "authoring-admin", Kind: access.PrincipalKindUser}))
			request = request.WithContext(WithAPICredential(request.Context(), credential))
			recorder := httptest.NewRecorder()
			protected.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNoContent {
				t.Fatalf("authoring role-binding bootstrap status = %d body=%q, want %d", recorder.Code, recorder.Body.String(), http.StatusNoContent)
			}
		})
	}
}
