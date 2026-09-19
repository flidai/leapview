package module

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
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

func TestAPIGenManagedDataStagingAllowsBoundAuthoringCredentialWithActivePredecessor(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	connectionID := projectgraph.ResourceID("connection:new")
	contract := APIGenOperationContract{
		OperationID: "createManagedDataUploadSession", Method: http.MethodPost,
		Path: "/api/v1/projects/{project}/connections/{connection}/upload-sessions", Protected: true, AuthzMode: "privilege",
		Command: &APIGenCommandContract{
			Owner: "LeapViewAPI.ManagedData", AuthzMode: "privilege", Privilege: "RESOURCE_EDIT", Idempotency: "required",
			Target: &APIGenCommandTarget{Parameter: "connection", Type: "connection"},
		},
		Extensions: map[string]any{
			apiGenObjectScopeExtension: "connection",
			"x-authz":                  map[string]any{"mode": "privilege", "privilege": "RESOURCE_EDIT"},
		},
	}
	module := browserGuardModule(browserGuardRepository{admin: false}, Principal{}, false)
	module.auth = &Auth{}
	module.authoringProjectID = func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil }
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, "publisher")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, graph, []accesssnapshot.RoleBinding{{
		ID: "binding:publisher", Subject: subject, Role: access.ProjectRoleDataDeployer,
		Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleDataDeployer),
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fenceHeld := false
	authorizer, err := module.APIGenAuthorizer(
		apigenRuntimeFake{project: projectID, lease: apigenLeaseFake{identity: identity, snapshot: snapshot}, fenceHeld: &fenceHeld},
		map[string]APIGenOperationContract{"createManagedDataUploadSession": contract},
		APIGenResourceResolvers{Connection: apigenResolver("connection", projectgraph.KindConnection)},
	)
	if err != nil {
		t.Fatal(err)
	}
	authorizer.SetBootstrapAuthorizer(func(_ context.Context, _ *http.Request, operation string, project projectgraph.ResourceID, capability access.Capability) (APIGenBootstrapDecision, error) {
		if operation != "createManagedDataUploadSession" || project != projectID || capability != access.CapabilityResourceEdit {
			t.Fatalf("bootstrap decision identity = %q/%s/%s", operation, project, capability)
		}
		return APIGenBootstrapDecision{Handled: false, AllowMissingResource: true}, nil
	})
	scope, err := access.NewAuthoringScope("instance-prod", projectID, []access.Capability{access.CapabilityResourceEdit})
	if err != nil {
		t.Fatal(err)
	}
	credential := access.APICredential{
		Principal: access.Principal{ID: "publisher", Kind: access.PrincipalKindServicePrincipal},
		Authoring: &access.AuthoringSession{
			ID: "authoring-1", Kind: access.AuthoringSessionWorkload,
			ClientID: "publisher", PrincipalID: "publisher", Scope: scope,
		},
	}
	protected, ok := authorizer.Protect("createManagedDataUploadSession", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !fenceHeld {
			t.Fatal("managed-data handler ran outside the cutover fence")
		}
		marker, marked := ManagedDataStagingAuthorizationFromContext(r.Context())
		if !marked || marker.ProjectID != projectID || marker.ConnectionID != connectionID || marker.PrincipalID != "publisher" || marker.Capability != access.CapabilityResourceEdit {
			t.Fatalf("managed-data staging marker = %#v, marked=%t", marker, marked)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	if !ok || protected == nil {
		t.Fatal("managed-data staging authorizer was not created")
	}
	request := apigenRequest(contract.Method, "/api/v1/projects/project_demo/connections/connection:new/upload-sessions", map[string]string{
		"project": projectID.String(), "connection": connectionID.String(),
	})
	request.Header.Set("Authorization", "Bearer authoring-token")
	request = request.WithContext(WithPrincipal(request.Context(), Principal{ID: "publisher", Kind: access.PrincipalKindServicePrincipal}))
	request = request.WithContext(WithAPICredential(request.Context(), credential))
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("managed-data authoring status = %d body=%q, want %d", recorder.Code, recorder.Body.String(), http.StatusNoContent)
	}
	if fenceHeld {
		t.Fatal("managed-data cutover fence was not released after dispatch")
	}

	bootstrapModule := browserGuardModule(browserGuardRepository{admin: true}, Principal{}, false)
	bootstrapModule.auth = &Auth{}
	bootstrapModule.authoringProjectID = func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil }
	bootstrapAuthorizer, err := bootstrapModule.APIGenAuthorizer(
		apigenRuntimeFake{project: projectID, fenceHeld: &fenceHeld},
		map[string]APIGenOperationContract{"createManagedDataUploadSession": contract},
		APIGenResourceResolvers{Connection: apigenResolver("connection", projectgraph.KindConnection)},
	)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapAuthorizer.SetBootstrapAuthorizer(func(context.Context, *http.Request, string, projectgraph.ResourceID, access.Capability) (APIGenBootstrapDecision, error) {
		return APIGenBootstrapDecision{Handled: true, Allowed: true}, nil
	})
	bootstrapProtected, ok := bootstrapAuthorizer.Protect("createManagedDataUploadSession", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !fenceHeld {
			t.Fatal("managed-data bootstrap handler ran outside the cutover fence")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	if !ok || bootstrapProtected == nil {
		t.Fatal("managed-data bootstrap authorizer was not created")
	}
	bootstrapResponse := httptest.NewRecorder()
	bootstrapProtected.ServeHTTP(bootstrapResponse, request)
	if bootstrapResponse.Code != http.StatusNoContent {
		t.Fatalf("managed-data bootstrap status = %d body=%q, want %d", bootstrapResponse.Code, bootstrapResponse.Body.String(), http.StatusNoContent)
	}
	if fenceHeld {
		t.Fatal("managed-data bootstrap cutover fence was not released after dispatch")
	}
}

func TestAPIGenManagedDataStagingDoesNotBypassExistingResourceOrLeaseFailures(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	connectionID := projectgraph.ResourceID("connection:existing")
	contract := APIGenOperationContract{
		OperationID: "createManagedDataUploadSession", Method: http.MethodPost,
		Path: "/api/v1/projects/{project}/connections/{connection}/upload-sessions", Protected: true, AuthzMode: "privilege",
		Command:    &APIGenCommandContract{Owner: "LeapViewAPI.ManagedData", AuthzMode: "privilege", Privilege: "RESOURCE_EDIT", Idempotency: "required", Target: &APIGenCommandTarget{Parameter: "connection", Type: "connection"}},
		Extensions: map[string]any{apiGenObjectScopeExtension: "connection", "x-authz": map[string]any{"mode": "privilege", "privilege": "RESOURCE_EDIT"}},
	}
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: connectionID, Kind: projectgraph.KindConnection, Name: "existing"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	emptyGraph, err := projectgraph.NewProjectGraph(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	missingWithoutRole, err := accesssnapshot.NewAuthorizationSnapshot(identity, emptyGraph, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := access.NewAuthoringScope("instance-prod", projectID, []access.Capability{access.CapabilityResourceEdit})
	if err != nil {
		t.Fatal(err)
	}
	credential := access.APICredential{
		Principal: access.Principal{ID: "publisher", Kind: access.PrincipalKindServicePrincipal},
		Authoring: &access.AuthoringSession{ID: "authoring-1", Kind: access.AuthoringSessionWorkload, ClientID: "publisher", PrincipalID: "publisher", Scope: scope},
	}

	for _, test := range []struct {
		name    string
		runtime apigenRuntimeFake
		want    int
	}{
		{name: "existing connection without snapshot grant", runtime: apigenRuntimeFake{project: projectID, lease: apigenLeaseFake{identity: identity, snapshot: snapshot}}, want: http.StatusForbidden},
		{name: "missing connection without project role", runtime: apigenRuntimeFake{project: projectID, lease: apigenLeaseFake{identity: identity, snapshot: missingWithoutRole}}, want: http.StatusForbidden},
		{name: "active lease unavailable", runtime: apigenRuntimeFake{project: projectID, err: errors.New("lease unavailable")}, want: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			module := browserGuardModule(browserGuardRepository{admin: true}, Principal{}, false)
			module.auth = &Auth{}
			module.authoringProjectID = func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil }
			authorizer, err := module.APIGenAuthorizer(test.runtime, map[string]APIGenOperationContract{contract.OperationID: contract}, APIGenResourceResolvers{Connection: apigenResolver("connection", projectgraph.KindConnection)})
			if err != nil {
				t.Fatal(err)
			}
			authorizer.SetBootstrapAuthorizer(func(context.Context, *http.Request, string, projectgraph.ResourceID, access.Capability) (APIGenBootstrapDecision, error) {
				return APIGenBootstrapDecision{Handled: false, AllowMissingResource: true}, nil
			})
			protected, ok := authorizer.Protect(contract.OperationID, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("request unexpectedly reached handler")
			}))
			if !ok {
				t.Fatal("managed-data authorizer was not created")
			}
			request := apigenRequest(contract.Method, "/api/v1/projects/project_demo/connections/connection:existing/upload-sessions", map[string]string{"project": projectID.String(), "connection": connectionID.String()})
			request.Header.Set("Authorization", "Bearer authoring-token")
			request = request.WithContext(WithPrincipal(request.Context(), Principal{ID: "publisher", Kind: access.PrincipalKindServicePrincipal}))
			request = request.WithContext(WithAPICredential(request.Context(), credential))
			response := httptest.NewRecorder()
			protected.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d body=%q, want %d", response.Code, response.Body.String(), test.want)
			}
		})
	}
}
