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

func TestAPIGenProjectRoleBindingBootstrapUsesExactTypedAuthoringPair(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	contracts := generatedAPIGenContracts()
	contract := contracts["createProjectRoleBinding"]
	manage := bootstrapProjectPair(t, projectID, access.ActionProjectAccessManage)
	read := bootstrapProjectPair(t, projectID, access.ActionProjectAccessRead)
	for _, tc := range []struct {
		name        string
		permissions []access.PermissionPair
		want        int
	}{
		{name: "exact pair", permissions: []access.PermissionPair{manage}, want: http.StatusNoContent},
		{name: "other pair", permissions: []access.PermissionPair{read}, want: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			module := browserGuardModule(browserGuardRepository{admin: true}, Principal{}, false)
			module.auth = &Auth{}
			module.authoringProjectID = func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil }
			authorizer, err := module.APIGenAuthorizer(
				apigenRuntimeFake{project: projectID, err: errors.New("no active serving generation")},
				map[string]APIGenOperationContract{contract.OperationID: contract},
				APIGenResourceResolvers{Project: apigenResolver("project", projectgraph.KindProjectNamespace)},
			)
			if err != nil {
				t.Fatal(err)
			}
			authorizer.SetBootstrapAuthorizer(func(context.Context, *http.Request, string, projectgraph.ResourceID, access.Capability) (APIGenBootstrapDecision, error) {
				return APIGenBootstrapDecision{Handled: true, Allowed: true}, nil
			})
			protected, ok := authorizer.Protect(contract.OperationID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if marker, marked := BootstrapAuthorizationFromContext(r.Context()); !marked || marker.ProjectID != projectID || marker.PrincipalID != "author" {
					t.Fatalf("bootstrap marker = %+v, marked=%t", marker, marked)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			if !ok {
				t.Fatal("typed role-binding bootstrap route unavailable")
			}
			issued := typedAuthoringBootstrapRequest(t, access.AuthoringSessionHumanCLI, "author", projectID, tc.permissions)
			request := apigenRequest(contract.Method, "/api/v1/projects/project_demo/role-bindings", map[string]string{"project": projectID.String()})
			request.Header.Set("Authorization", "Bearer authoring-secret")
			credential, _ := APICredentialFromContext(issued.Context())
			request = request.WithContext(WithPrincipal(request.Context(), Principal{ID: "author", Kind: access.PrincipalKindUser}))
			request = request.WithContext(WithAPICredential(request.Context(), credential))
			response := httptest.NewRecorder()
			protected.ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("status=%d body=%q, want %d", response.Code, response.Body.String(), tc.want)
			}
		})
	}
}

func TestAPIGenManagedDataBootstrapUsesTypedConnectionPairAndFence(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	connectionID := projectgraph.ResourceID("connection_new")
	connection, err := access.NewResourceRef(connectionID, projectgraph.KindConnection)
	if err != nil {
		t.Fatal(err)
	}
	manage, err := access.NewExactPermissionPair(access.ActionConnectionManage, projectID, connection)
	if err != nil {
		t.Fatal(err)
	}
	create := bootstrapProjectPair(t, projectID, access.ActionConnectionCreate)
	read, err := access.NewExactPermissionPair(access.ActionConnectionRead, projectID, connection)
	if err != nil {
		t.Fatal(err)
	}
	contract := APIGenOperationContract{
		OperationID: "createManagedDataUploadSession", Method: http.MethodPost,
		Path: "/api/v1/projects/{project}/connections/{connection}/upload-sessions", Protected: true, AuthzMode: "privilege",
		Action: string(access.ActionConnectionManage), Resolver: string(access.TypedOperationResolverConnection),
		Command:    &APIGenCommandContract{Owner: "LeapViewAPI.ManagedData", AuthzMode: "privilege", Privilege: "RESOURCE_EDIT", Idempotency: "required", Target: &APIGenCommandTarget{Parameter: "connection", Type: "connection"}},
		Extensions: map[string]any{apiGenObjectScopeExtension: "connection", "x-authz": map[string]any{"mode": "privilege", "privilege": "RESOURCE_EDIT"}},
	}
	for _, tc := range []struct {
		name        string
		permissions []access.PermissionPair
		want        int
	}{
		{name: "project create", permissions: []access.PermissionPair{create}, want: http.StatusNoContent},
		{name: "manage without create", permissions: []access.PermissionPair{manage}, want: http.StatusForbidden},
		{name: "read does not manage", permissions: []access.PermissionPair{read}, want: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fenceHeld := false
			module := browserGuardModule(browserGuardRepository{admin: true}, Principal{}, false)
			module.auth = &Auth{}
			module.authoringProjectID = func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil }
			authorizer, err := module.APIGenAuthorizer(
				apigenRuntimeFake{project: projectID, fenceHeld: &fenceHeld},
				map[string]APIGenOperationContract{contract.OperationID: contract},
				APIGenResourceResolvers{Connection: apigenResolver("connection", projectgraph.KindConnection)},
			)
			if err != nil {
				t.Fatal(err)
			}
			authorizer.SetBootstrapAuthorizer(func(context.Context, *http.Request, string, projectgraph.ResourceID, access.Capability) (APIGenBootstrapDecision, error) {
				return APIGenBootstrapDecision{Handled: true, Allowed: true}, nil
			})
			protected, ok := authorizer.Protect(contract.OperationID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !fenceHeld {
					t.Fatal("managed-data handler ran outside cutover fence")
				}
				if marker, marked := ManagedDataStagingAuthorizationFromContext(r.Context()); !marked || marker.ConnectionID != connectionID {
					t.Fatalf("staging marker = %+v, marked=%t", marker, marked)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			if !ok {
				t.Fatal("typed managed-data bootstrap route unavailable")
			}
			issued := typedAuthoringBootstrapRequest(t, access.AuthoringSessionWorkload, "publisher", projectID, tc.permissions)
			request := apigenRequest(contract.Method, "/api/v1/projects/project_demo/connections/connection_new/upload-sessions", map[string]string{"project": projectID.String(), "connection": connectionID.String()})
			request.Header.Set("Authorization", "Bearer authoring-secret")
			credential, _ := APICredentialFromContext(issued.Context())
			request = request.WithContext(WithPrincipal(request.Context(), Principal{ID: "publisher", Kind: access.PrincipalKindServicePrincipal}))
			request = request.WithContext(WithAPICredential(request.Context(), credential))
			response := httptest.NewRecorder()
			protected.ServeHTTP(response, request)
			if response.Code != tc.want || fenceHeld {
				t.Fatalf("status=%d body=%q fenceHeld=%t, want %d and released fence", response.Code, response.Body.String(), fenceHeld, tc.want)
			}
		})
	}
}
