package module

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/go-chi/chi/v5"
)

type apigenRuntimeFake struct {
	project projectgraph.ResourceID
	lease   runtimehost.Lease
	err     error
}

func (r apigenRuntimeFake) ProjectID() projectgraph.ResourceID { return r.project }
func (r apigenRuntimeFake) Acquire(context.Context) (runtimehost.Lease, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.lease, nil
}

type apigenLeaseFake struct {
	identity projectgraph.ServingIdentity
	snapshot accesssnapshot.AuthorizationSnapshot
}

func (l apigenLeaseFake) Runtime() projectruntime.Runtime        { return nil }
func (l apigenLeaseFake) Identity() projectgraph.ServingIdentity { return l.identity }
func (l apigenLeaseFake) Release()                               {}
func (l apigenLeaseFake) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return l.snapshot
}

func apigenRequest(method, path string, params map[string]string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	route := chi.NewRouteContext()
	for name, value := range params {
		route.URLParams.Add(name, value)
	}
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
}

func apigenResolver(parameter string, kind projectgraph.Kind) APIGenResourceResolver {
	return func(r *http.Request, active projectgraph.ResourceID) []access.ResourceRef {
		id, err := projectgraph.NewResourceID(chi.URLParam(r, parameter))
		if err != nil || (kind == projectgraph.KindProject && id != active) {
			return nil
		}
		resource, err := access.NewResourceRef(id, kind)
		if err != nil {
			return nil
		}
		return []access.ResourceRef{resource}
	}
}

func apigenSnapshot(t *testing.T, principalID, groupID string, resourceID projectgraph.ResourceID, resourceKind projectgraph.Kind, direct, group bool) (projectgraph.ServingIdentity, accesssnapshot.AuthorizationSnapshot) {
	t.Helper()
	identity, err := projectgraph.NewServingIdentity("project_demo", "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	resources := []projectgraph.Resource{{ID: "project_demo", Kind: projectgraph.KindProject, Name: "demo"}}
	if resourceID != "project_demo" || resourceKind != projectgraph.KindProject {
		resources = append(resources, projectgraph.Resource{ID: resourceID, Kind: resourceKind, Name: resourceID.String()})
	}
	graph, err := projectgraph.NewProjectGraph(resources, nil)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := access.NewResourceRef(resourceID, resourceKind)
	if err != nil {
		t.Fatal(err)
	}
	capability := access.CapabilityResourceRead
	if resourceKind == projectgraph.KindProject {
		capability = access.CapabilityProjectAdmin
	}
	grants := make([]accesssnapshot.Grant, 0, 2)
	if direct {
		subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, principalID)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := access.NewCanonicalGrant(graph, subject, resource, capability)
		if err != nil {
			t.Fatal(err)
		}
		grants = append(grants, accesssnapshot.Grant{ID: "grant_direct", Canonical: canonical})
	}
	if group {
		subject, err := access.NewSubjectRef(access.SubjectKindGroup, groupID)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := access.NewCanonicalGrant(graph, subject, resource, capability)
		if err != nil {
			t.Fatal(err)
		}
		grants = append(grants, accesssnapshot.Grant{ID: "grant_group", Canonical: canonical})
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, grants, nil)
	if err != nil {
		t.Fatal(err)
	}
	return identity, snapshot
}

func apigenResourceAuthorizer(t *testing.T, principal Principal, groups []string, resourceID projectgraph.ResourceID, resourceKind projectgraph.Kind, snapshot accesssnapshot.AuthorizationSnapshot, runtimeErr error, lease runtimehost.Lease) *APIGenAuthorizer {
	t.Helper()
	repo := browserGuardRepository{groups: groups}
	module := browserGuardModule(repo, principal, true)
	parameter, scope := "dashboard", "dashboard"
	resolvers := APIGenResourceResolvers{Dashboard: apigenResolver("dashboard", projectgraph.KindDashboard), SemanticModel: apigenResolver("model", projectgraph.KindSemanticModel), Connection: apigenResolver("connection", projectgraph.KindConnection), Project: apigenResolver("project", projectgraph.KindProject)}
	switch resourceKind {
	case projectgraph.KindSemanticModel:
		parameter, scope = "model", "semantic-model"
	case projectgraph.KindConnection:
		parameter, scope = "connection", "connection"
	case projectgraph.KindProject:
		parameter, scope = "project", "project"
	}
	contract := APIGenOperationContract{
		OperationID: "readResource", Method: http.MethodGet,
		Path: "/api/v1/projects/{project}/" + scope + "/{" + parameter + "}", Protected: true, AuthzMode: "privilege",
		Command:    &APIGenCommandContract{AuthzMode: "privilege", Privilege: "RESOURCE_READ"},
		Extensions: map[string]any{apiGenObjectScopeExtension: scope},
	}
	if resourceKind == projectgraph.KindProject {
		contract.Command.Privilege = "PROJECT_ADMIN"
	}
	authorizer, err := module.APIGenAuthorizer(apigenRuntimeFake{project: "project_demo", lease: lease, err: runtimeErr}, map[string]APIGenOperationContract{"readResource": contract}, resolvers)
	if err != nil {
		t.Fatal(err)
	}
	return authorizer
}

func TestAPIGenResourceSelectorsAndSubjectResolution(t *testing.T) {
	const principalID = "principal_alice"
	const groupID = "group_analysts"
	for _, test := range []struct {
		name      string
		id        projectgraph.ResourceID
		kind      projectgraph.Kind
		parameter string
		scope     string
	}{
		{name: "dashboard", id: "dashboard_sales", kind: projectgraph.KindDashboard, parameter: "dashboard", scope: "dashboard"},
		{name: "semantic model", id: "model_sales", kind: projectgraph.KindSemanticModel, parameter: "model", scope: "semantic-model"},
		{name: "connection", id: "connection_sales", kind: projectgraph.KindConnection, parameter: "connection", scope: "connection"},
		{name: "project", id: "project_demo", kind: projectgraph.KindProject, parameter: "project", scope: "project"},
	} {
		t.Run(test.name, func(t *testing.T) {
			resourceID := test.id
			identity, snapshot := apigenSnapshot(t, principalID, groupID, resourceID, test.kind, true, false)
			lease := apigenLeaseFake{identity: identity, snapshot: snapshot}
			authorizer := apigenResourceAuthorizer(t, Principal{ID: principalID}, nil, resourceID, test.kind, snapshot, nil, lease)
			path := "/api/v1/projects/project_demo/" + test.scope + "/" + resourceID.String()
			if test.kind == projectgraph.KindProject {
				path = "/api/v1/projects/" + resourceID.String() + "/project/" + resourceID.String()
			}
			recorder := httptest.NewRecorder()
			authorizerHandler, ok := authorizer.Protect("readResource", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
			if !ok || authorizerHandler == nil {
				t.Fatal("resource operation was not protected")
			}
			authorizerHandler.ServeHTTP(recorder, apigenRequest(http.MethodGet, path, map[string]string{"project": "project_demo", test.parameter: resourceID.String()}))
			if recorder.Code != http.StatusNoContent {
				t.Fatalf("direct subject status = %d, want %d", recorder.Code, http.StatusNoContent)
			}

			groupIdentity, groupSnapshot := apigenSnapshot(t, principalID, groupID, resourceID, test.kind, false, true)
			groupAuthorizer := apigenResourceAuthorizer(t, Principal{ID: principalID}, []string{groupID}, resourceID, test.kind, groupSnapshot, nil, apigenLeaseFake{identity: groupIdentity, snapshot: groupSnapshot})
			recorder = httptest.NewRecorder()
			groupHandler, _ := groupAuthorizer.Protect("readResource", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
			groupHandler.ServeHTTP(recorder, apigenRequest(http.MethodGet, path, map[string]string{"project": "project_demo", test.parameter: resourceID.String()}))
			if recorder.Code != http.StatusNoContent {
				t.Fatalf("group subject status = %d, want %d", recorder.Code, http.StatusNoContent)
			}
		})
	}
}

func TestAPIGenResourceDenialUnknownSelectorAndInfrastructureFailures(t *testing.T) {
	const principalID = "principal_alice"
	resourceID := projectgraph.ResourceID("dashboard_sales")
	identity, snapshot := apigenSnapshot(t, principalID, "group_analysts", resourceID, projectgraph.KindDashboard, false, false)
	lease := apigenLeaseFake{identity: identity, snapshot: snapshot}
	request := func(id string) *http.Request {
		return apigenRequest(http.MethodGet, "/api/v1/projects/project_demo/dashboard/"+id, map[string]string{"project": "project_demo", "dashboard": id})
	}
	newHandler := func(authorizer *APIGenAuthorizer) http.Handler {
		handler, ok := authorizer.Protect("readResource", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		if !ok || handler == nil {
			t.Fatal("resource operation was not protected")
		}
		return handler
	}
	denied := newHandler(apigenResourceAuthorizer(t, Principal{ID: principalID}, nil, resourceID, projectgraph.KindDashboard, snapshot, nil, lease))
	recorder := httptest.NewRecorder()
	denied.ServeHTTP(recorder, request(resourceID.String()))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("deny status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	projectMismatch := newHandler(apigenResourceAuthorizer(t, Principal{ID: principalID}, nil, resourceID, projectgraph.KindDashboard, snapshot, nil, lease))
	recorder = httptest.NewRecorder()
	projectMismatch.ServeHTTP(recorder, apigenRequest(http.MethodGet, "/api/v1/projects/project_other/dashboard/"+resourceID.String(), map[string]string{"project": "project_other", "dashboard": resourceID.String()}))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("project mismatch status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
	unknown := newHandler(apigenResourceAuthorizer(t, Principal{ID: principalID}, nil, resourceID, projectgraph.KindDashboard, snapshot, nil, lease))
	recorder = httptest.NewRecorder()
	unknown.ServeHTTP(recorder, request("dashboard_unknown"))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown selector status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
	acquireFailure := newHandler(apigenResourceAuthorizer(t, Principal{ID: principalID}, nil, resourceID, projectgraph.KindDashboard, snapshot, errors.New("acquire failed"), lease))
	recorder = httptest.NewRecorder()
	acquireFailure.ServeHTTP(recorder, request(resourceID.String()))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("acquire failure status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	subjectFailureAuthorizer := apigenResourceAuthorizer(t, Principal{ID: principalID}, nil, resourceID, projectgraph.KindDashboard, snapshot, nil, lease)
	subjectFailureAuthorizer.module.repository = func() (access.Repository, error) {
		return browserGuardRepository{groupErr: errors.New("subject lookup failed")}, nil
	}
	subjectFailure := newHandler(subjectFailureAuthorizer)
	recorder = httptest.NewRecorder()
	subjectFailure.ServeHTTP(recorder, request(resourceID.String()))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("subject failure status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	invalidSnapshot := newHandler(apigenResourceAuthorizer(t, Principal{ID: principalID}, nil, resourceID, projectgraph.KindDashboard, snapshot, nil, apigenLeaseFake{identity: identity}))
	recorder = httptest.NewRecorder()
	invalidSnapshot.ServeHTTP(recorder, request(resourceID.String()))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("snapshot validation failure status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	nilLease := newHandler(apigenResourceAuthorizer(t, Principal{ID: principalID}, nil, resourceID, projectgraph.KindDashboard, snapshot, nil, nil))
	recorder = httptest.NewRecorder()
	nilLease.ServeHTTP(recorder, request(resourceID.String()))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil lease status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	mismatched, err := projectgraph.NewServingIdentity("project_demo", "prod", "generation_2")
	if err != nil {
		t.Fatal(err)
	}
	snapshotMismatch := newHandler(apigenResourceAuthorizer(t, Principal{ID: principalID}, nil, resourceID, projectgraph.KindDashboard, snapshot, nil, apigenLeaseFake{identity: mismatched, snapshot: snapshot}))
	recorder = httptest.NewRecorder()
	snapshotMismatch.ServeHTTP(recorder, request(resourceID.String()))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("snapshot mismatch status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

func generatedAPIGenContracts() map[string]APIGenOperationContract {
	generated := apiaggregate.GetAPIGenOperationContracts()
	contracts := make(map[string]APIGenOperationContract, len(generated))
	for operationID, contract := range generated {
		var command *APIGenCommandContract
		if contract.Command != nil {
			command = &APIGenCommandContract{
				Owner: contract.Command.Owner, AuthzMode: contract.Command.AuthzMode,
				Privilege: contract.Command.Privilege, Idempotency: contract.Command.Idempotency,
				Concurrency: contract.Command.Concurrency,
			}
			if contract.Command.Target != nil {
				command.Target = &APIGenCommandTarget{Parameter: contract.Command.Target.Parameter, Type: contract.Command.Target.Type}
			}
		}
		contracts[operationID] = APIGenOperationContract{
			OperationID: contract.OperationID, Method: contract.Method, Path: contract.Path,
			Protected: contract.Protected, AuthzMode: contract.AuthzMode, Command: command,
			Extensions: contract.Extensions,
		}
	}
	return contracts
}

func TestAPIGenEveryGeneratedOperationConstructsWithCanonicalResolvers(t *testing.T) {
	contracts := generatedAPIGenContracts()
	if len(contracts) == 0 {
		t.Fatal("generated operation contract registry is empty")
	}
	module := browserGuardModule(nil, Principal{ID: "dev", DevBypass: true}, true)
	_, err := module.APIGenAuthorizer(apigenRuntimeFake{project: "project_demo"}, contracts, APIGenResourceResolvers{
		Dashboard:     apigenResolver("dashboard", projectgraph.KindDashboard),
		SemanticModel: apigenResolver("model", projectgraph.KindSemanticModel),
		Connection:    apigenResolver("connection", projectgraph.KindConnection),
		Project:       apigenResolver("project", projectgraph.KindProject),
	})
	if err != nil {
		t.Fatalf("generated operation contracts are not constructible: %v", err)
	}
}

func TestAPIGenDeviceApprovalRequiresCSRF(t *testing.T) {
	if !apiGenRequiresCSRF("decideDeviceAuthorization") {
		t.Fatal("device approval operation is missing CSRF requirement")
	}
	if apiGenRequiresCSRF("getCurrentPrincipal") {
		t.Fatal("ordinary authenticated operation unexpectedly requires device CSRF")
	}
}

func TestAPIGenReplayReevaluatesCurrentPolicyAndRejectsMethodOrPathMismatch(t *testing.T) {
	module := browserGuardModule(nil, Principal{ID: "dev", DevBypass: true}, true)
	contract := APIGenOperationContract{OperationID: "currentPrincipal", Method: http.MethodGet, Path: "/api/v1/me", Protected: true, AuthzMode: "authenticated"}
	authorizer, err := module.APIGenAuthorizer(apigenRuntimeFake{project: "project_demo"}, map[string]APIGenOperationContract{"currentPrincipal": contract}, APIGenResourceResolvers{})
	if err != nil {
		t.Fatal(err)
	}
	route := chi.NewRouteContext()
	route.RoutePatterns = append(route.RoutePatterns, contract.Path)
	request := httptest.NewRequest(http.MethodGet, contract.Path, nil).WithContext(context.WithValue(context.Background(), chi.RouteCtxKey, route))
	if !authorizer.AuthorizeReplay(request) {
		t.Fatal("replay rejected current authenticated policy")
	}
	request.Method = http.MethodPost
	if authorizer.AuthorizeReplay(request) {
		t.Fatal("replay authorized a method mismatch")
	}
	request.Method = http.MethodGet
	request.URL.Path = "/api/v1/other"
	if authorizer.AuthorizeReplay(request) {
		t.Fatal("replay authorized a path mismatch")
	}
	module.currentPrincipal = func(*http.Request) (Principal, bool) { return Principal{}, false }
	request.URL.Path = contract.Path
	if authorizer.AuthorizeReplay(request) {
		t.Fatal("replay ignored current authentication policy")
	}
}

func TestAPIGenPlatformScopeUsesPlatformRoleEvenWhenAuthenticated(t *testing.T) {
	repo := browserGuardRepository{admin: false}
	module := browserGuardModule(repo, Principal{ID: "principal"}, true)
	authorizer := &APIGenAuthorizer{
		module: module,
		operations: map[string]APIGenOperationContract{
			"platformStatus": {OperationID: "platformStatus", Protected: true, AuthzMode: "authenticated", Extensions: map[string]any{apiGenObjectScopeExtension: "platform"}},
		},
	}
	protected, ok := authorizer.Protect("platformStatus", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	if !ok || protected == nil {
		t.Fatal("platform operation was not protected")
	}
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/platform/status", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

func TestAPIGenAuthenticatedOperationUsesPrincipalAuthentication(t *testing.T) {
	module := browserGuardModule(nil, Principal{ID: "principal"}, true)
	authorizer := &APIGenAuthorizer{
		module: module,
		operations: map[string]APIGenOperationContract{
			"currentPrincipal": {OperationID: "currentPrincipal", Protected: true, AuthzMode: "authenticated"},
		},
	}
	protected, ok := authorizer.Protect("currentPrincipal", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	if !ok || protected == nil {
		t.Fatal("authenticated operation was not protected")
	}
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/current-principal", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestAPIGenResourceResolverRejectsMismatchedCanonicalScope(t *testing.T) {
	resolver := func(*http.Request, projectgraph.ResourceID) []access.ResourceRef { return nil }
	authorizer := &APIGenAuthorizer{
		scopes: map[string]apiGenResourceScope{
			"dashboard": {pathParameter: "dashboard", resolver: resolver, kind: projectgraph.KindDashboard},
		},
	}
	for name, contract := range map[string]APIGenOperationContract{
		"wrong parameter": {
			Path:       "/api/v1/projects/{project}/dashboards/{model}",
			Command:    &APIGenCommandContract{Target: &APIGenCommandTarget{Parameter: "model", Type: "dashboard"}},
			Extensions: map[string]any{apiGenObjectScopeExtension: "dashboard"},
		},
		"legacy scope": {
			Path:       "/api/v1/projects/{project}/dashboards/{dashboard}",
			Extensions: map[string]any{apiGenObjectScopeExtension: "project-environment"},
		},
	} {
		if _, ok := authorizer.resourceResolverForContract(contract); ok {
			t.Errorf("%s accepted a mismatched or legacy scope", name)
		}
	}
}

func TestAPIGenProtectNeverDowngradesScopedCapabilityToAuthentication(t *testing.T) {
	module := browserGuardModule(nil, Principal{ID: "principal"}, true)
	authorizer := &APIGenAuthorizer{
		module: module,
		scopes: map[string]apiGenResourceScope{
			"dashboard": {pathParameter: "dashboard", resolver: func(*http.Request, projectgraph.ResourceID) []access.ResourceRef { return nil }, kind: projectgraph.KindDashboard},
		},
		operations: map[string]APIGenOperationContract{
			"unscopedCapability":   {OperationID: "unscopedCapability", Protected: true, AuthzMode: "privilege", Command: &APIGenCommandContract{AuthzMode: "privilege", Privilege: "RESOURCE_READ"}},
			"selfCapability":       {OperationID: "selfCapability", Protected: true, AuthzMode: "privilege", Command: &APIGenCommandContract{AuthzMode: "privilege", Privilege: "RESOURCE_READ"}, Extensions: map[string]any{apiGenObjectScopeExtension: "principal"}},
			"scopedAuthentication": {OperationID: "scopedAuthentication", Protected: true, AuthzMode: "authenticated", Extensions: map[string]any{apiGenObjectScopeExtension: "dashboard"}},
		},
	}
	for operationID := range authorizer.operations {
		if protected, ok := authorizer.Protect(operationID, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})); ok || protected != nil {
			t.Errorf("%s was accepted without its required canonical resource guard", operationID)
		}
	}
}

func TestAPIGenValidateRejectsInvalidAuthzScopeCombinations(t *testing.T) {
	authorizer := &APIGenAuthorizer{scopes: map[string]apiGenResourceScope{
		"dashboard": {pathParameter: "dashboard", resolver: func(*http.Request, projectgraph.ResourceID) []access.ResourceRef { return nil }, kind: projectgraph.KindDashboard},
	}}
	tests := map[string]APIGenOperationContract{
		"authenticated resource": {
			OperationID: "authenticatedResource", Protected: true, AuthzMode: "authenticated",
			Extensions: map[string]any{apiGenObjectScopeExtension: "dashboard"},
		},
		"privilege principal": {
			OperationID: "privilegePrincipal", Protected: true, AuthzMode: "privilege",
			Command:    &APIGenCommandContract{AuthzMode: "privilege", Privilege: "RESOURCE_READ"},
			Extensions: map[string]any{apiGenObjectScopeExtension: "principal"},
		},
		"privilege unscoped": {
			OperationID: "privilegeUnscoped", Protected: true, AuthzMode: "privilege",
			Command: &APIGenCommandContract{AuthzMode: "privilege", Privilege: "RESOURCE_READ"},
		},
	}
	for name, contract := range tests {
		if err := authorizer.validateOperation(contract.OperationID, contract); err == nil {
			t.Errorf("%s contract unexpectedly validated", name)
		}
	}
	public := APIGenOperationContract{OperationID: "public", Protected: false, AuthzMode: "none", Extensions: map[string]any{apiGenObjectScopeExtension: "platform"}}
	if err := authorizer.validateOperation("public", public); err == nil {
		t.Fatal("unprotected operation with scope metadata unexpectedly validated")
	}
}

func TestAPIGenMetadataRejectsWhitespace(t *testing.T) {
	if scope, ok := apiGenScope(APIGenOperationContract{Extensions: map[string]any{apiGenObjectScopeExtension: " platform"}}); ok || scope != "" {
		t.Fatalf("whitespace-prefixed scope accepted: %q, %t", scope, ok)
	}
	if capability, ok := apiGenOperationCapability(APIGenOperationContract{AuthzMode: "privilege", Extensions: map[string]any{"x-authz": map[string]any{"mode": "privilege", "privilege": "RESOURCE_READ "}}}); ok || capability != "" {
		t.Fatalf("whitespace-suffixed capability accepted: %q, %t", capability, ok)
	}
}
