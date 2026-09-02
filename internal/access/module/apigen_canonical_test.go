package module

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/go-chi/chi/v5"
)

type mutableAPIGenLease struct {
	identity projectgraph.ServingIdentity
	current  func() accesssnapshot.AuthorizationSnapshot
}

func (l mutableAPIGenLease) Runtime() projectruntime.Runtime        { return nil }
func (l mutableAPIGenLease) Identity() projectgraph.ServingIdentity { return l.identity }
func (l mutableAPIGenLease) Release()                               {}
func (l mutableAPIGenLease) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return l.current()
}

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

func TestAPIGenPublicOperationAcceptsGeneratedNoneMetadata(t *testing.T) {
	module := browserGuardModule(nil, Principal{}, false)
	contract := APIGenOperationContract{
		OperationID: "getInstance", Method: http.MethodGet, Path: "/api/v1/instance",
		Protected: false, AuthzMode: "none",
		Extensions: map[string]any{"x-authz": map[string]any{"mode": "none"}},
	}
	authorizer, err := module.APIGenAuthorizer(
		apigenRuntimeFake{project: "project_demo"},
		map[string]APIGenOperationContract{"getInstance": contract},
		APIGenResourceResolvers{},
	)
	if err != nil {
		t.Fatal(err)
	}
	handler, ok := authorizer.Protect("getInstance", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if !ok || handler == nil {
		t.Fatal("public generated operation was not accepted")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/instance", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func apigenResourceAuthorizer(t *testing.T, principal Principal, groups []string, resourceID projectgraph.ResourceID, resourceKind projectgraph.Kind, snapshot accesssnapshot.AuthorizationSnapshot, runtimeErr error, lease runtimehost.Lease) *APIGenAuthorizer {
	t.Helper()
	repo := browserGuardRepository{groups: groups}
	module := browserGuardModule(repo, principal, true)
	module.SetCurrentEffectiveCapabilities(func(context.Context, string) ([]access.Capability, error) {
		subjects := []access.SubjectRef{}
		if principal.ID != "" {
			subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, principal.ID)
			if err != nil {
				return nil, err
			}
			subjects = append(subjects, subject)
		}
		for _, groupID := range groups {
			subject, err := access.NewSubjectRef(access.SubjectKindGroup, groupID)
			if err != nil {
				return nil, err
			}
			subjects = append(subjects, subject)
		}
		return snapshot.EffectiveCapabilities(subjects)
	})
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

func TestAPIGenServerBoundResourceRouteUsesActiveProject(t *testing.T) {
	const principalID = "principal_alice"
	resourceID := projectgraph.ResourceID("dashboard_sales")
	identity, snapshot := apigenSnapshot(t, principalID, "", resourceID, projectgraph.KindDashboard, true, false)
	module := browserGuardModule(browserGuardRepository{}, Principal{ID: principalID}, true)
	module.SetCurrentEffectiveCapabilities(func(context.Context, string) ([]access.Capability, error) {
		subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, principalID)
		if err != nil {
			return nil, err
		}
		return snapshot.EffectiveCapabilities([]access.SubjectRef{subject})
	})
	contract := APIGenOperationContract{
		OperationID: "readDashboard", Method: http.MethodGet,
		Path: "/api/v1/dashboards/{dashboard}", Protected: true, AuthzMode: "privilege",
		Extensions: map[string]any{
			apiGenObjectScopeExtension: "dashboard",
			"x-authz":                  map[string]any{"mode": "privilege", "privilege": "RESOURCE_READ"},
		},
	}
	authorizer, err := module.APIGenAuthorizer(
		apigenRuntimeFake{project: identity.ProjectID, lease: apigenLeaseFake{identity: identity, snapshot: snapshot}},
		map[string]APIGenOperationContract{"readDashboard": contract},
		APIGenResourceResolvers{Dashboard: apigenResolver("dashboard", projectgraph.KindDashboard)},
	)
	if err != nil {
		t.Fatal(err)
	}
	handler, ok := authorizer.Protect("readDashboard", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if !ok {
		t.Fatal("server-bound resource operation was not protected")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, apigenRequest(http.MethodGet, "/api/v1/dashboards/dashboard_sales", map[string]string{"dashboard": resourceID.String()}))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestAPIGenResourceAuthorizationAttenuatesAndRevokesBearerTokens(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository := accesssqlite.NewRepository(store.SQLDB())
	principal, err := repository.UpsertPrincipal(t.Context(), access.PrincipalInput{ID: "principal_alice", Email: "alice@example.test", DisplayName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	resourceID := projectgraph.ResourceID("dashboard_sales")
	identity, snapshot := apigenSnapshot(t, principal.ID, "", resourceID, projectgraph.KindDashboard, true, false)
	module, err := newSurface(surfaceConfig{
		Repository: func() (access.Repository, error) { return repository, nil },
		Auth:       NewAuth(repository, AuthConfig{}),
		CurrentEffectiveCapabilities: func(context.Context, string) ([]access.Capability, error) {
			subject, subjectErr := access.NewSubjectRef(access.SubjectKindPrincipal, principal.ID)
			if subjectErr != nil {
				return nil, subjectErr
			}
			return snapshot.EffectiveCapabilities([]access.SubjectRef{subject})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	contract := APIGenOperationContract{
		OperationID: "readResource", Method: http.MethodGet,
		Path: "/api/v1/projects/{project}/dashboard/{dashboard}", Protected: true, AuthzMode: "privilege",
		Command:    &APIGenCommandContract{AuthzMode: "privilege", Privilege: "RESOURCE_READ"},
		Extensions: map[string]any{apiGenObjectScopeExtension: "dashboard"},
	}
	authorizer, err := module.APIGenAuthorizer(
		apigenRuntimeFake{project: "project_demo", lease: apigenLeaseFake{identity: identity, snapshot: snapshot}},
		map[string]APIGenOperationContract{"readResource": contract},
		APIGenResourceResolvers{Dashboard: apigenResolver("dashboard", projectgraph.KindDashboard)},
	)
	if err != nil {
		t.Fatal(err)
	}
	handler, ok := authorizer.Protect("readResource", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	if !ok {
		t.Fatal("resource operation was not protected")
	}
	call := func(secret string) int {
		request := apigenRequest(http.MethodGet, "/api/v1/projects/project_demo/dashboard/dashboard_sales", map[string]string{"project": "project_demo", "dashboard": "dashboard_sales"})
		request.Header.Set("Authorization", "Bearer "+secret)
		request.Header.Set("X-Request-ID", "request_resource_denial")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Code
	}
	dynamicSecret, dynamicToken, err := repository.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{PrincipalID: principal.ID, Name: "dynamic", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if got := call(dynamicSecret); got != http.StatusNoContent {
		t.Fatalf("dynamic token status = %d, want 204", got)
	}
	denySecret, _, err := repository.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{PrincipalID: principal.ID, Name: "deny-all", Capabilities: []access.Capability{}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if got := call(denySecret); got != http.StatusForbidden {
		t.Fatalf("deny-all token status = %d, want 403", got)
	}
	events, err := repository.ListAuditEvents(t.Context(), access.AuditEventFilter{Action: "authorization.denied"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].PrincipalID != principal.ID || events[0].ResourceKind != string(projectgraph.KindDashboard) || events[0].ResourceID != resourceID.String() || events[0].Capability != access.CapabilityResourceRead || events[0].Status != "denied" || events[0].RequestID != "request_resource_denial" {
		t.Fatalf("authorization denial audit = %#v", events)
	}
	if err := repository.RevokeAPIToken(t.Context(), dynamicToken.ID); err != nil {
		t.Fatal(err)
	}
	if got := call(dynamicSecret); got != http.StatusUnauthorized {
		t.Fatalf("revoked token status = %d, want 401", got)
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
	generated := accessgen.GetAPIGenOperationContracts()
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

func TestDeliveryObjectIDMapsCanonicalPublicationApprovalOperations(t *testing.T) {
	request := apigenRequest(http.MethodPost, "/", map[string]string{"publication": "publication_demo"})
	for _, operationID := range []string{
		"requestDeliveryPublicationApproval",
		"getDeliveryPublicationApproval",
		"approveDeliveryPublicationApproval",
		"denyDeliveryPublicationApproval",
		"revokeDeliveryPublicationApproval",
	} {
		if got := deliveryObjectID(operationID, request); got != "publication_demo" {
			t.Errorf("deliveryObjectID(%q) = %q, want publication_demo", operationID, got)
		}
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

func TestAPIGenPublicationReplayRechecksRevokedResourcePublishGrant(t *testing.T) {
	identity, err := projectgraph.NewServingIdentity("project_demo", "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project_demo", Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "dashboard_website", Kind: projectgraph.KindDashboard, Name: "website"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, "principal_alice")
	if err != nil {
		t.Fatal(err)
	}
	allowedSnapshot, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, graph, []accesssnapshot.RoleBinding{{
		ID: "binding_deployer", Subject: subject, Role: access.ProjectRoleDeployer,
		Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleDeployer),
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	revokedSnapshot, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, graph, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	current := allowedSnapshot
	module := browserGuardModule(browserGuardRepository{}, Principal{ID: subject.ID}, true)
	module.SetCurrentEffectiveCapabilities(func(context.Context, string) ([]access.Capability, error) {
		return current.EffectiveCapabilities([]access.SubjectRef{subject})
	})
	lease := mutableAPIGenLease{identity: identity, current: func() accesssnapshot.AuthorizationSnapshot { return current }}
	authorizer, err := module.APIGenAuthorizer(apigenRuntimeFake{project: identity.ProjectID, lease: lease}, map[string]APIGenOperationContract{
		"suspendDashboardPublication": {
			OperationID: "suspendDashboardPublication", Method: http.MethodPost,
			Path: "/api/v1/projects/{project}/dashboard-publications/{publication}/suspend", Protected: true, AuthzMode: "privilege",
			Command:    &APIGenCommandContract{AuthzMode: "privilege", Privilege: "RESOURCE_PUBLISH", Target: &APIGenCommandTarget{Parameter: "project", Type: "project"}},
			Extensions: map[string]any{apiGenObjectScopeExtension: "project"},
		},
	}, APIGenResourceResolvers{Project: apigenResolver("project", projectgraph.KindProject)})
	if err != nil {
		t.Fatal(err)
	}
	request := apigenRequest(http.MethodPost, "/api/v1/projects/project_demo/dashboard-publications/website/suspend", map[string]string{"project": "project_demo", "publication": "website"})
	handler, ok := authorizer.Protect("suspendDashboardPublication", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	if !ok || handler == nil {
		t.Fatal("publication operation was not protected")
	}
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, request)
	if first.Code != http.StatusNoContent {
		t.Fatalf("initial publication mutation authorization = %d, want %d", first.Code, http.StatusNoContent)
	}
	current = revokedSnapshot
	if authorizer.AuthorizeReplay(request) {
		t.Fatal("publication replay bypassed revoked RESOURCE_PUBLISH grant")
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

func TestAPIGenPlatformScopeDoesNotRequireActiveRuntime(t *testing.T) {
	module := browserGuardModule(browserGuardRepository{admin: true}, Principal{ID: "platform-admin"}, true)
	contract := APIGenOperationContract{
		OperationID: "platformStatus", Method: http.MethodGet, Path: "/api/v1/platform/status",
		Protected: true, AuthzMode: "authenticated",
		Extensions: map[string]any{apiGenObjectScopeExtension: "platform"},
	}
	authorizer, err := module.APIGenAuthorizer(nil, map[string]APIGenOperationContract{"platformStatus": contract}, APIGenResourceResolvers{})
	if err != nil {
		t.Fatalf("platform authorizer without runtime: %v", err)
	}
	protected, ok := authorizer.Protect("platformStatus", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if !ok || protected == nil {
		t.Fatal("platform operation was not protected without runtime")
	}
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, contract.Path, nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestAPIGenProjectPrivilegeRequiresRuntime(t *testing.T) {
	module := browserGuardModule(browserGuardRepository{admin: true}, Principal{ID: "platform-admin"}, true)
	contract := APIGenOperationContract{
		OperationID: "readDashboard", Method: http.MethodGet, Path: "/api/v1/dashboards/{dashboard}",
		Protected: true, AuthzMode: "privilege",
		Command:    &APIGenCommandContract{AuthzMode: "privilege", Privilege: "RESOURCE_READ"},
		Extensions: map[string]any{apiGenObjectScopeExtension: "dashboard"},
	}
	_, err := module.APIGenAuthorizer(nil, map[string]APIGenOperationContract{"readDashboard": contract}, APIGenResourceResolvers{
		Dashboard: apigenResolver("dashboard", projectgraph.KindDashboard),
	})
	if err == nil {
		t.Fatal("project resource authorizer unexpectedly accepted nil runtime")
	}
}

func TestAPIGenCandidateActivePathKeepsNormalSessionSnapshotAuthorization(t *testing.T) {
	identity, snapshot := apigenSnapshot(t, "principal", "", "project_demo", projectgraph.KindProject, true, false)
	module := browserGuardModule(browserGuardRepository{admin: true}, Principal{ID: "principal"}, true)
	module.SetCurrentEffectiveCapabilities(func(context.Context, string) ([]access.Capability, error) {
		return snapshot.EffectiveCapabilities([]access.SubjectRef{{Kind: access.SubjectKindPrincipal, ID: "principal"}})
	})
	contract := APIGenOperationContract{
		OperationID: "publishProjectCandidate", Method: http.MethodPost,
		Path: "/api/v1/projects/{project}/candidates/{candidate}/publish", Protected: true, AuthzMode: "privilege",
		Command:    &APIGenCommandContract{AuthzMode: "privilege", Privilege: "PROJECT_ADMIN", Target: &APIGenCommandTarget{Parameter: "project", Type: "project"}},
		Extensions: map[string]any{apiGenObjectScopeExtension: "project"},
	}
	authorizer, err := module.APIGenAuthorizer(apigenRuntimeFake{project: "project_demo", lease: apigenLeaseFake{identity: identity, snapshot: snapshot}}, map[string]APIGenOperationContract{"publishProjectCandidate": contract}, APIGenResourceResolvers{Project: apigenResolver("project", projectgraph.KindProject)})
	if err != nil {
		t.Fatal(err)
	}
	authorizer.SetBootstrapAuthorizer(func(context.Context, *http.Request, string, projectgraph.ResourceID, access.Capability) (APIGenBootstrapDecision, error) {
		return APIGenBootstrapDecision{Handled: false}, nil
	})
	protected, ok := authorizer.Protect("publishProjectCandidate", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	if !ok || protected == nil {
		t.Fatal("candidate authorizer was not created")
	}
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, apigenRequest(http.MethodPost, "/api/v1/projects/project_demo/candidates/candidate/publish", map[string]string{"project": "project_demo"}))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("active candidate path status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestAPIGenDeliveryAuthorizerUsesTargetOwnedRoleDecision(t *testing.T) {
	identity, snapshot := apigenSnapshot(t, "principal", "", "project_demo", projectgraph.KindProject, true, false)
	module := browserGuardModule(nil, Principal{ID: "principal"}, true)
	contract := APIGenOperationContract{
		OperationID: "createDeliveryPlan", Method: http.MethodPost,
		Path: "/api/v1/projects/{project}/delivery", Protected: true, AuthzMode: "privilege",
		Command:    &APIGenCommandContract{AuthzMode: "privilege", Privilege: "RESOURCE_READ", Target: &APIGenCommandTarget{Parameter: "project", Type: "project"}},
		Extensions: map[string]any{apiGenObjectScopeExtension: "project"},
	}
	var calls int
	authorizer, err := module.APIGenAuthorizer(
		apigenRuntimeFake{project: "project_demo", lease: apigenLeaseFake{identity: identity, snapshot: snapshot}},
		map[string]APIGenOperationContract{"createDeliveryPlan": contract},
		APIGenResourceResolvers{Delivery: func(_ context.Context, _ *http.Request, operationID, objectID string, projectID projectgraph.ResourceID, capability access.Capability) (bool, error) {
			calls++
			if operationID != "createDeliveryPlan" || objectID != "" || projectID.String() != "project_demo" || capability != access.CapabilityResourceRead {
				t.Fatalf("delivery authorization request = %q/%q/%s/%s", operationID, objectID, projectID, capability)
			}
			return calls == 1, nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	serve := func() int {
		protected, ok := authorizer.Protect("createDeliveryPlan", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		if !ok || protected == nil {
			t.Fatal("delivery authorizer was not created")
		}
		recorder := httptest.NewRecorder()
		protected.ServeHTTP(recorder, apigenRequest(http.MethodPost, "/api/v1/projects/project_demo/delivery", map[string]string{"project": "project_demo"}))
		return recorder.Code
	}
	if status := serve(); status != http.StatusNoContent {
		t.Fatalf("target role allowed status = %d, want %d", status, http.StatusNoContent)
	}
	if status := serve(); status != http.StatusForbidden {
		t.Fatalf("target role denied status = %d, want %d", status, http.StatusForbidden)
	}
}

func TestAPIGenDeliveryActivePathValidatesTargetForConfiguredDevelopmentBypass(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	module := browserGuardModule(nil, LocalDeveloperPrincipal(), true)
	module.auth = NewAuth(nil, AuthConfig{DevBypass: true, DevAPIToken: "dev"})
	contract := APIGenOperationContract{
		OperationID: "getDeliveryCandidateStatus", Method: http.MethodGet,
		Path: "/api/v1/projects/{project}/delivery/candidates/{candidate}", Protected: true, AuthzMode: "privilege",
		Command:    &APIGenCommandContract{AuthzMode: "privilege", Privilege: "RESOURCE_READ", Target: &APIGenCommandTarget{Parameter: "project", Type: "project"}},
		Extensions: map[string]any{apiGenObjectScopeExtension: "project"},
	}
	var targetAuthorizationCalls int
	authorizer, err := module.APIGenAuthorizer(
		apigenRuntimeFake{project: projectID},
		map[string]APIGenOperationContract{"getDeliveryCandidateStatus": contract},
		APIGenResourceResolvers{Delivery: func(context.Context, *http.Request, string, string, projectgraph.ResourceID, access.Capability) (bool, error) {
			targetAuthorizationCalls++
			return true, nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	authorizer.SetBootstrapAuthorizer(func(context.Context, *http.Request, string, projectgraph.ResourceID, access.Capability) (APIGenBootstrapDecision, error) {
		return APIGenBootstrapDecision{Handled: false}, nil
	})
	protected, ok := authorizer.Protect("getDeliveryCandidateStatus", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if !ok || protected == nil {
		t.Fatal("delivery authorizer was not created")
	}
	request := apigenRequest(http.MethodGet, "/api/v1/projects/project_demo/delivery/candidates/candidate_1", map[string]string{
		"project": "project_demo", "candidate": "candidate_1",
	})
	request.Header.Set("Authorization", "Bearer dev")
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("development delivery status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	if targetAuthorizationCalls != 1 {
		t.Fatalf("target authorization calls = %d, want one target validation for configured development bypass", targetAuthorizationCalls)
	}
}

func TestAPIGenCreateDeliveryPlanUsesBootstrapBeforeFirstGeneration(t *testing.T) {
	module := browserGuardModule(browserGuardRepository{admin: true}, Principal{ID: "admin"}, true)
	contract := APIGenOperationContract{
		OperationID: "createDeliveryPlan", Method: http.MethodPost,
		Path: "/api/v1/projects/{project}/delivery", Protected: true, AuthzMode: "privilege",
		Command:    &APIGenCommandContract{AuthzMode: "privilege", Privilege: "RESOURCE_READ", Target: &APIGenCommandTarget{Parameter: "project", Type: "project"}},
		Extensions: map[string]any{apiGenObjectScopeExtension: "project"},
	}
	projectID, err := projectgraph.NewResourceID("project_demo")
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := module.APIGenAuthorizer(
		apigenRuntimeFake{project: projectID, err: errors.New("no active serving generation")},
		map[string]APIGenOperationContract{"createDeliveryPlan": contract},
		APIGenResourceResolvers{Delivery: func(context.Context, *http.Request, string, string, projectgraph.ResourceID, access.Capability) (bool, error) {
			t.Fatal("target-owned delivery authorization should not run before first generation")
			return false, nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	authorizer.SetBootstrapAuthorizer(func(_ context.Context, _ *http.Request, operation string, project projectgraph.ResourceID, _ access.Capability) (APIGenBootstrapDecision, error) {
		if operation != "createDeliveryPlan" || project != projectID {
			t.Fatalf("bootstrap decision identity = %q/%s", operation, project)
		}
		return APIGenBootstrapDecision{Handled: true, Allowed: true}, nil
	})
	protected, ok := authorizer.Protect("createDeliveryPlan", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	if !ok || protected == nil {
		t.Fatal("delivery authorizer was not created")
	}
	credential := access.APICredential{Principal: access.Principal{ID: "admin"}, Token: access.APIToken{ID: "token_1", PrincipalID: "admin", Capabilities: []access.Capability{access.CapabilityResourceRead}}}
	r := apigenRequest(http.MethodPost, "/api/v1/projects/project_demo/delivery", map[string]string{"project": "project_demo"})
	r.Header.Set("Authorization", "Bearer test-token")
	r = r.WithContext(WithAPICredential(r.Context(), credential))
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, r)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("bootstrap create plan status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestAPIGenDeliveryAuthoringBootstrapCredentialScope(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	newContract := func(operationID, path string, capability access.Capability) APIGenOperationContract {
		return APIGenOperationContract{
			OperationID: operationID, Method: http.MethodPost,
			Path: path, Protected: true, AuthzMode: "privilege",
			Command:    &APIGenCommandContract{AuthzMode: "privilege", Privilege: string(capability), Target: &APIGenCommandTarget{Parameter: "project", Type: "project"}},
			Extensions: map[string]any{apiGenObjectScopeExtension: "project"},
		}
	}
	serve := func(t *testing.T, contract APIGenOperationContract, requestPath string, parameters map[string]string, admin bool, scopeProject projectgraph.ResourceID, capabilities []access.Capability, wantStatus int, wantMarker bool) {
		t.Helper()
		module := browserGuardModule(browserGuardRepository{admin: admin}, Principal{}, false)
		module.auth = &Auth{}
		module.authoringProjectID = func(context.Context) (projectgraph.ResourceID, error) {
			return scopeProject, nil
		}
		authorizer, err := module.APIGenAuthorizer(
			apigenRuntimeFake{project: projectID, err: errors.New("no active serving generation")},
			map[string]APIGenOperationContract{contract.OperationID: contract},
			APIGenResourceResolvers{Delivery: func(context.Context, *http.Request, string, string, projectgraph.ResourceID, access.Capability) (bool, error) {
				t.Fatal("target-owned delivery authorization should not run before first generation")
				return false, nil
			}},
		)
		if err != nil {
			t.Fatal(err)
		}
		authorizer.SetBootstrapAuthorizer(func(_ context.Context, _ *http.Request, operation string, project projectgraph.ResourceID, capability access.Capability) (APIGenBootstrapDecision, error) {
			if operation != contract.OperationID || project != projectID || capability != access.Capability(contract.Command.Privilege) {
				t.Fatalf("bootstrap decision identity = %q/%s/%s", operation, project, capability)
			}
			return APIGenBootstrapDecision{Handled: true, Allowed: true}, nil
		})
		protected, ok := authorizer.Protect(contract.OperationID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			marker, marked := BootstrapAuthorizationFromContext(r.Context())
			if marked != wantMarker || (marked && (marker.ProjectID != projectID || marker.PrincipalID != "authoring-user" || marker.Capability != access.Capability(contract.Command.Privilege))) {
				t.Fatalf("authoring delivery bootstrap marker = %#v, marked=%t", marker, marked)
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		if !ok || protected == nil {
			t.Fatal("delivery authorizer was not created")
		}
		scope, err := access.NewAuthoringScope("instance-prod", scopeProject, capabilities)
		if err != nil {
			t.Fatal(err)
		}
		credential := access.APICredential{
			Principal: access.Principal{ID: "authoring-user", Kind: access.PrincipalKindUser},
			Authoring: &access.AuthoringSession{
				ID: "authoring-1", Kind: access.AuthoringSessionHumanCLI,
				ClientID: access.AuthoringCLIClientID, PrincipalID: "authoring-user", Scope: scope,
			},
		}
		r := apigenRequest(http.MethodPost, requestPath, parameters)
		r.Header.Set("Authorization", "Bearer authoring-token")
		r = r.WithContext(WithPrincipal(r.Context(), Principal{ID: "authoring-user", Kind: access.PrincipalKindUser}))
		r = r.WithContext(WithAPICredential(r.Context(), credential))
		recorder := httptest.NewRecorder()
		protected.ServeHTTP(recorder, r)
		if recorder.Code != wantStatus {
			t.Fatalf("authoring delivery bootstrap status = %d body=%q, want %d", recorder.Code, recorder.Body.String(), wantStatus)
		}
	}

	for _, test := range []struct {
		name, operationID, path, requestPath string
		capability                           access.Capability
		parameters                           map[string]string
	}{
		{name: "createDeliveryPlan", operationID: "createDeliveryPlan", path: "/api/v1/projects/{project}/delivery", requestPath: "/api/v1/projects/project_demo/delivery", capability: access.CapabilityResourceRead, parameters: map[string]string{"project": projectID.String()}},
		{name: "buildDeliveryPlan", operationID: "buildDeliveryPlan", path: "/api/v1/projects/{project}/delivery/plans/{plan}/build", requestPath: "/api/v1/projects/project_demo/delivery/plans/plan_1/build", capability: access.CapabilityResourceUse, parameters: map[string]string{"project": projectID.String(), "plan": "plan_1"}},
		{name: "publishDeliveryCandidate", operationID: "publishDeliveryCandidate", path: "/api/v1/projects/{project}/delivery/candidates/{candidate}/publish", requestPath: "/api/v1/projects/project_demo/delivery/candidates/candidate_1/publish", capability: access.CapabilityResourcePublish, parameters: map[string]string{"project": projectID.String(), "candidate": "candidate_1"}},
		{name: "requestDeliveryPublicationApproval", operationID: "requestDeliveryPublicationApproval", path: "/api/v1/projects/{project}/delivery/publications/{publication}/approval-requests", requestPath: "/api/v1/projects/project_demo/delivery/publications/publication_1/approval-requests", capability: access.CapabilityResourcePublish, parameters: map[string]string{"project": projectID.String(), "publication": "publication_1"}},
	} {
		t.Run(test.name+"/exact scope allowed", func(t *testing.T) {
			contract := newContract(test.operationID, test.path, test.capability)
			serve(t, contract, test.requestPath, test.parameters, true, projectID, []access.Capability{test.capability}, http.StatusNoContent, true)
		})
	}
	if !isBootstrapDeliveryAPIGenOperation("approveDeliveryPublicationApproval") {
		t.Fatal("reviewer approval operation is not bootstrap-authorized")
	}
	if isAuthoringDeliveryBootstrapOperation("approveDeliveryPublicationApproval") {
		t.Fatal("reviewer approval operation is authoring-authorized")
	}
	for _, operationID := range []string{"getDeliveryPublicationEvidence", "getDeliveryPublicationApproval", "denyDeliveryPublicationApproval", "revokeDeliveryPublicationApproval"} {
		if isBootstrapDeliveryAPIGenOperation(operationID) {
			t.Errorf("non-authoring publication operation %q unexpectedly admitted to bootstrap", operationID)
		}
	}
	approveContract := newContract("approveDeliveryPublicationApproval", "/api/v1/projects/{project}/delivery/publications/{publication}/approval-requests/{approval}/approve", access.CapabilityProjectAdmin)
	t.Run("approveDeliveryPublicationApproval/authoring credential denied", func(t *testing.T) {
		serve(t, approveContract, "/api/v1/projects/project_demo/delivery/publications/publication_1/approval-requests/approval_1/approve", map[string]string{"project": projectID.String(), "publication": "publication_1", "approval": "approval_1"}, true, projectID, []access.Capability{access.CapabilityProjectAdmin}, http.StatusForbidden, false)
	})
	contract := newContract("createDeliveryPlan", "/api/v1/projects/{project}/delivery", access.CapabilityResourceRead)
	for _, test := range []struct {
		name         string
		admin        bool
		scopeProject projectgraph.ResourceID
		capabilities []access.Capability
	}{
		{name: "project scope mismatch denied", admin: true, scopeProject: "project_other", capabilities: []access.Capability{access.CapabilityResourceRead}},
		{name: "capability mismatch denied", admin: true, scopeProject: projectID, capabilities: []access.Capability{access.CapabilityResourceUse}},
		{name: "platform admin required", admin: false, scopeProject: projectID, capabilities: []access.Capability{access.CapabilityResourceRead}},
	} {
		t.Run(test.name, func(t *testing.T) {
			serve(t, contract, "/api/v1/projects/project_demo/delivery", map[string]string{"project": projectID.String()}, test.admin, test.scopeProject, test.capabilities, http.StatusForbidden, false)
		})
	}
}

func TestAPIGenPublicationApprovalBootstrapUsesReviewerToken(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	module := browserGuardModule(browserGuardRepository{admin: false}, Principal{ID: "reviewer", Kind: access.PrincipalKindUser}, true)
	contract := APIGenOperationContract{
		OperationID: "approveDeliveryPublicationApproval", Method: http.MethodPost,
		Path:      "/api/v1/projects/{project}/delivery/publications/{publication}/approval-requests/{approval}/approve",
		Protected: true, AuthzMode: "privilege",
		Command: &APIGenCommandContract{
			AuthzMode: "privilege", Privilege: "PROJECT_ADMIN",
			Target: &APIGenCommandTarget{Parameter: "project", Type: "project"},
		},
		Extensions: map[string]any{apiGenObjectScopeExtension: "project"},
	}
	authorizer, err := module.APIGenAuthorizer(
		apigenRuntimeFake{project: projectID, err: errors.New("no active serving generation")},
		map[string]APIGenOperationContract{contract.OperationID: contract},
		APIGenResourceResolvers{Delivery: func(context.Context, *http.Request, string, string, projectgraph.ResourceID, access.Capability) (bool, error) {
			t.Fatal("target-owned delivery authorization should not run before first generation")
			return false, nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	authorizer.SetBootstrapAuthorizer(func(_ context.Context, _ *http.Request, operation string, project projectgraph.ResourceID, capability access.Capability) (APIGenBootstrapDecision, error) {
		if operation != contract.OperationID || project != projectID || capability != access.CapabilityProjectAdmin {
			t.Fatalf("bootstrap decision identity = %q/%s/%s", operation, project, capability)
		}
		return APIGenBootstrapDecision{Handled: true, Allowed: true}, nil
	})
	protected, ok := authorizer.Protect(contract.OperationID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		marker, marked := PublicationApprovalBootstrapAuthorizationFromContext(r.Context())
		if !marked || marker.ProjectID != projectID || marker.PrincipalID != "reviewer" || marker.Capability != access.CapabilityProjectAdmin {
			t.Fatalf("reviewer bootstrap marker = %#v, marked=%t", marker, marked)
		}
		if _, genericMarked := BootstrapAuthorizationFromContext(r.Context()); genericMarked {
			t.Fatal("reviewer approval unexpectedly received generic bootstrap marker")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	if !ok || protected == nil {
		t.Fatal("delivery approval authorizer was not created")
	}
	credential := access.APICredential{
		Principal: access.Principal{ID: "reviewer", Kind: access.PrincipalKindUser},
		Token:     access.APIToken{ID: "reviewer-token", PrincipalID: "reviewer", Capabilities: []access.Capability{access.CapabilityProjectAdmin}},
	}
	request := apigenRequest(http.MethodPost, "/api/v1/projects/project_demo/delivery/publications/publication_1/approval-requests/approval_1/approve", map[string]string{
		"project": projectID.String(), "publication": "publication_1", "approval": "approval_1",
	})
	request.Header.Set("Authorization", "Bearer reviewer-token")
	request = request.WithContext(WithAPICredential(request.Context(), credential))
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("reviewer approval bootstrap status = %d body=%q, want %d", recorder.Code, recorder.Body.String(), http.StatusNoContent)
	}

	for _, test := range []struct {
		name       string
		credential access.APICredential
	}{
		{name: "malformed authoring credential", credential: access.APICredential{
			Principal: access.Principal{ID: "reviewer", Kind: access.PrincipalKindUser},
			Authoring: &access.AuthoringSession{ID: "authoring-1", PrincipalID: "reviewer"},
		}},
		{name: "principal mismatch", credential: access.APICredential{
			Principal: access.Principal{ID: "other-reviewer", Kind: access.PrincipalKindUser},
			Token:     access.APIToken{ID: "other-token", PrincipalID: "other-reviewer", Capabilities: []access.Capability{access.CapabilityProjectAdmin}},
		}},
		{name: "capability mismatch", credential: access.APICredential{
			Principal: access.Principal{ID: "reviewer", Kind: access.PrincipalKindUser},
			Token:     access.APIToken{ID: "reviewer-token", PrincipalID: "reviewer", Capabilities: []access.Capability{access.CapabilityResourcePublish}},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := apigenRequest(http.MethodPost, "/api/v1/projects/project_demo/delivery/publications/publication_1/approval-requests/approval_1/approve", map[string]string{
				"project": projectID.String(), "publication": "publication_1", "approval": "approval_1",
			})
			request.Header.Set("Authorization", "Bearer reviewer-token")
			request = request.WithContext(WithAPICredential(request.Context(), test.credential))
			recorder := httptest.NewRecorder()
			protected.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("reviewer approval bootstrap status = %d body=%q, want %d", recorder.Code, recorder.Body.String(), http.StatusForbidden)
			}
		})
	}

	t.Run("wrong operation", func(t *testing.T) {
		wrongContract := contract
		wrongContract.OperationID = "requestDeliveryPublicationApproval"
		wrongContract.Path = "/api/v1/projects/{project}/delivery/publications/{publication}/approval-requests"
		wrongAuthorizer, err := module.APIGenAuthorizer(
			apigenRuntimeFake{project: projectID, err: errors.New("no active serving generation")},
			map[string]APIGenOperationContract{wrongContract.OperationID: wrongContract},
			APIGenResourceResolvers{Delivery: func(context.Context, *http.Request, string, string, projectgraph.ResourceID, access.Capability) (bool, error) {
				t.Fatal("target-owned delivery authorization should not run before first generation")
				return false, nil
			}},
		)
		if err != nil {
			t.Fatal(err)
		}
		wrongAuthorizer.SetBootstrapAuthorizer(func(_ context.Context, _ *http.Request, operation string, project projectgraph.ResourceID, capability access.Capability) (APIGenBootstrapDecision, error) {
			if operation != wrongContract.OperationID || project != projectID || capability != access.CapabilityProjectAdmin {
				t.Fatalf("bootstrap decision identity = %q/%s/%s", operation, project, capability)
			}
			return APIGenBootstrapDecision{Handled: true, Allowed: true}, nil
		})
		wrongProtected, ok := wrongAuthorizer.Protect(wrongContract.OperationID, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("wrong operation unexpectedly reached handler")
		}))
		if !ok || wrongProtected == nil {
			t.Fatal("wrong-operation authorizer was not created")
		}
		request := apigenRequest(http.MethodPost, "/api/v1/projects/project_demo/delivery/publications/publication_1/approval-requests", map[string]string{
			"project": projectID.String(), "publication": "publication_1",
		})
		request.Header.Set("Authorization", "Bearer reviewer-token")
		request = request.WithContext(WithAPICredential(request.Context(), credential))
		recorder := httptest.NewRecorder()
		wrongProtected.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("wrong-operation bootstrap status = %d body=%q, want %d", recorder.Code, recorder.Body.String(), http.StatusForbidden)
		}
	})
}

func TestAPIGenBuildDeliveryPlanUsesBootstrapBeforeFirstGeneration(t *testing.T) {
	module := browserGuardModule(browserGuardRepository{admin: true}, Principal{ID: "admin"}, true)
	contract := APIGenOperationContract{
		OperationID: "buildDeliveryPlan", Method: http.MethodPost,
		Path: "/api/v1/projects/{project}/delivery/plans/{plan}/build", Protected: true, AuthzMode: "privilege",
		Command:    &APIGenCommandContract{AuthzMode: "privilege", Privilege: "RESOURCE_USE", Target: &APIGenCommandTarget{Parameter: "project", Type: "project"}},
		Extensions: map[string]any{apiGenObjectScopeExtension: "project"},
	}
	projectID, err := projectgraph.NewResourceID("project_demo")
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := module.APIGenAuthorizer(
		apigenRuntimeFake{project: projectID, err: errors.New("no active serving generation")},
		map[string]APIGenOperationContract{"buildDeliveryPlan": contract},
		APIGenResourceResolvers{Delivery: func(context.Context, *http.Request, string, string, projectgraph.ResourceID, access.Capability) (bool, error) {
			t.Fatal("target-owned delivery authorization should not run before first generation")
			return false, nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	authorizer.SetBootstrapAuthorizer(func(_ context.Context, _ *http.Request, operation string, project projectgraph.ResourceID, capability access.Capability) (APIGenBootstrapDecision, error) {
		if operation != "buildDeliveryPlan" || project != projectID || capability != access.CapabilityResourceUse {
			t.Fatalf("bootstrap decision identity = %q/%s/%s", operation, project, capability)
		}
		return APIGenBootstrapDecision{Handled: true, Allowed: true}, nil
	})
	protected, ok := authorizer.Protect("buildDeliveryPlan", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	if !ok || protected == nil {
		t.Fatal("delivery authorizer was not created")
	}
	credential := access.APICredential{Principal: access.Principal{ID: "admin"}, Token: access.APIToken{ID: "token_1", PrincipalID: "admin", Capabilities: []access.Capability{access.CapabilityResourceUse}}}
	r := apigenRequest(http.MethodPost, "/api/v1/projects/project_demo/delivery/plans/plan_1/build", map[string]string{"project": "project_demo", "plan": "plan_1"})
	r.Header.Set("Authorization", "Bearer test-token")
	r = r.WithContext(WithAPICredential(r.Context(), credential))
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, r)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("bootstrap build status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestAPIGenDeliveryPlanResolutionReadsUseBootstrapBeforeFirstGeneration(t *testing.T) {
	projectID, err := projectgraph.NewResourceID("project_demo")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		operationID string
		path        string
		requestPath string
		parameters  map[string]string
	}{
		{
			operationID: "getDeliveryCandidateStatus",
			path:        "/api/v1/projects/{project}/delivery/candidates/{candidate}",
			requestPath: "/api/v1/projects/project_demo/delivery/candidates/candidate_1",
			parameters:  map[string]string{"project": "project_demo", "candidate": "candidate_1"},
		},
		{
			operationID: "getDeliveryPlanPreview",
			path:        "/api/v1/projects/{project}/delivery/plans/{plan}",
			requestPath: "/api/v1/projects/project_demo/delivery/plans/plan_1",
			parameters:  map[string]string{"project": "project_demo", "plan": "plan_1"},
		},
	}
	for _, test := range tests {
		t.Run(test.operationID, func(t *testing.T) {
			module := browserGuardModule(browserGuardRepository{admin: true}, Principal{ID: "admin"}, true)
			contract := APIGenOperationContract{
				OperationID: test.operationID, Method: http.MethodGet,
				Path: test.path, Protected: true, AuthzMode: "privilege",
				Command:    &APIGenCommandContract{AuthzMode: "privilege", Privilege: "RESOURCE_READ", Target: &APIGenCommandTarget{Parameter: "project", Type: "project"}},
				Extensions: map[string]any{apiGenObjectScopeExtension: "project"},
			}
			authorizer, err := module.APIGenAuthorizer(
				apigenRuntimeFake{project: projectID, err: errors.New("no active serving generation")},
				map[string]APIGenOperationContract{test.operationID: contract},
				APIGenResourceResolvers{Delivery: func(context.Context, *http.Request, string, string, projectgraph.ResourceID, access.Capability) (bool, error) {
					t.Fatal("target-owned delivery authorization should not run before first generation")
					return false, nil
				}},
			)
			if err != nil {
				t.Fatal(err)
			}
			authorizer.SetBootstrapAuthorizer(func(_ context.Context, _ *http.Request, operation string, project projectgraph.ResourceID, capability access.Capability) (APIGenBootstrapDecision, error) {
				if operation != test.operationID || project != projectID || capability != access.CapabilityResourceRead {
					t.Fatalf("bootstrap decision identity = %q/%s/%s", operation, project, capability)
				}
				return APIGenBootstrapDecision{Handled: true, Allowed: true}, nil
			})
			protected, ok := authorizer.Protect(test.operationID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				marker, marked := BootstrapAuthorizationFromContext(r.Context())
				if !marked || marker.ProjectID != projectID || marker.PrincipalID != "admin" || marker.Capability != access.CapabilityResourceRead {
					t.Fatalf("delivery read bootstrap marker = %#v, marked=%t", marker, marked)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			if !ok || protected == nil {
				t.Fatal("delivery authorizer was not created")
			}
			credential := access.APICredential{Principal: access.Principal{ID: "admin"}, Token: access.APIToken{ID: "token_1", PrincipalID: "admin", Capabilities: []access.Capability{access.CapabilityResourceRead}}}
			r := apigenRequest(http.MethodGet, test.requestPath, test.parameters)
			r.Header.Set("Authorization", "Bearer test-token")
			r = r.WithContext(WithAPICredential(r.Context(), credential))
			recorder := httptest.NewRecorder()
			protected.ServeHTTP(recorder, r)
			if recorder.Code != http.StatusNoContent {
				t.Fatalf("bootstrap delivery read status = %d, want %d", recorder.Code, http.StatusNoContent)
			}
		})
	}
}

func TestAPIGenDeliveryReadPreservesTargetOwnedAuthorizationForLocalDeveloper(t *testing.T) {
	projectID, err := projectgraph.NewResourceID("project_demo")
	if err != nil {
		t.Fatal(err)
	}
	module := browserGuardModule(nil, Principal{ID: "dev", DevBypass: true}, true)
	contract := APIGenOperationContract{
		OperationID: "getDeliveryCandidateStatus", Method: http.MethodGet,
		Path: "/api/v1/projects/{project}/delivery/candidates/{candidate}", Protected: true, AuthzMode: "privilege",
		Command:    &APIGenCommandContract{AuthzMode: "privilege", Privilege: "RESOURCE_READ", Target: &APIGenCommandTarget{Parameter: "project", Type: "project"}},
		Extensions: map[string]any{apiGenObjectScopeExtension: "project"},
	}
	deliveryCalled := false
	authorizer, err := module.APIGenAuthorizer(
		apigenRuntimeFake{project: projectID},
		map[string]APIGenOperationContract{"getDeliveryCandidateStatus": contract},
		APIGenResourceResolvers{Delivery: func(_ context.Context, _ *http.Request, operationID, objectID string, project projectgraph.ResourceID, capability access.Capability) (bool, error) {
			deliveryCalled = true
			if operationID != "getDeliveryCandidateStatus" || objectID != "candidate_1" || project != projectID || capability != access.CapabilityResourceRead {
				t.Fatalf("delivery authorization request = %q/%q/%s/%s", operationID, objectID, project, capability)
			}
			return true, nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	authorizer.SetBootstrapAuthorizer(func(context.Context, *http.Request, string, projectgraph.ResourceID, access.Capability) (APIGenBootstrapDecision, error) {
		return APIGenBootstrapDecision{}, nil
	})
	protected, ok := authorizer.Protect("getDeliveryCandidateStatus", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if !ok || protected == nil {
		t.Fatal("delivery authorizer was not created")
	}
	request := apigenRequest(http.MethodGet, "/api/v1/projects/project_demo/delivery/candidates/candidate_1", map[string]string{"project": "project_demo", "candidate": "candidate_1"})
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("dev delivery read status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	if !deliveryCalled {
		t.Fatal("local developer bypass skipped target-owned delivery validation")
	}
}

func TestAPIGenPublishDeliveryCandidateUsesBootstrapMarker(t *testing.T) {
	module := browserGuardModule(browserGuardRepository{admin: true}, Principal{ID: "admin"}, true)
	contract := APIGenOperationContract{
		OperationID: "publishDeliveryCandidate", Method: http.MethodPost,
		Path: "/api/v1/projects/{project}/delivery/candidates/{candidate}/publish", Protected: true, AuthzMode: "privilege",
		Command:    &APIGenCommandContract{AuthzMode: "privilege", Privilege: "RESOURCE_PUBLISH", Target: &APIGenCommandTarget{Parameter: "project", Type: "project"}},
		Extensions: map[string]any{apiGenObjectScopeExtension: "project"},
	}
	projectID, err := projectgraph.NewResourceID("project_demo")
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := module.APIGenAuthorizer(
		apigenRuntimeFake{project: projectID, err: errors.New("no active serving generation")},
		map[string]APIGenOperationContract{"publishDeliveryCandidate": contract},
		APIGenResourceResolvers{Delivery: func(_ context.Context, _ *http.Request, operationID, objectID string, project projectgraph.ResourceID, capability access.Capability) (bool, error) {
			t.Fatalf("target-owned delivery authorization should not run before first generation: %q/%q/%s/%s", operationID, objectID, project, capability)
			return false, nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	authorizer.SetBootstrapAuthorizer(func(_ context.Context, _ *http.Request, operation string, project projectgraph.ResourceID, capability access.Capability) (APIGenBootstrapDecision, error) {
		if operation != "publishDeliveryCandidate" || project != projectID || capability != access.CapabilityResourcePublish {
			t.Fatalf("bootstrap decision identity = %q/%s/%s", operation, project, capability)
		}
		return APIGenBootstrapDecision{Handled: true, Allowed: true}, nil
	})
	protected, ok := authorizer.Protect("publishDeliveryCandidate", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		marker, marked := BootstrapAuthorizationFromContext(r.Context())
		if !marked || marker.ProjectID != projectID || marker.PrincipalID != "admin" || marker.Capability != access.CapabilityResourcePublish {
			t.Fatalf("publish bootstrap marker = %#v, marked=%t", marker, marked)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	if !ok || protected == nil {
		t.Fatal("delivery authorizer was not created")
	}
	credential := access.APICredential{Principal: access.Principal{ID: "admin"}, Token: access.APIToken{ID: "token_1", PrincipalID: "admin", Capabilities: []access.Capability{access.CapabilityResourcePublish}}}
	r := apigenRequest(http.MethodPost, "/api/v1/projects/project_demo/delivery/candidates/candidate_1/publish", map[string]string{"project": "project_demo", "candidate": "candidate_1"})
	r.Header.Set("Authorization", "Bearer test-token")
	r = r.WithContext(WithAPICredential(r.Context(), credential))
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, r)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("bootstrap publish status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestAPIGenCandidatePreActivationRequiresExplicitRESTTokenPlatformAdmin(t *testing.T) {
	contract := APIGenOperationContract{
		OperationID: "startProjectCandidate", Method: http.MethodPost,
		Path: "/api/v1/projects/{project}/candidates", Protected: true, AuthzMode: "privilege",
		Command:    &APIGenCommandContract{AuthzMode: "privilege", Privilege: "RESOURCE_EDIT", Target: &APIGenCommandTarget{Parameter: "project", Type: "project"}},
		Extensions: map[string]any{apiGenObjectScopeExtension: "project"},
	}
	newAuthorizer := func(admin bool) *APIGenAuthorizer {
		module := browserGuardModule(browserGuardRepository{admin: admin}, Principal{ID: "admin"}, true)
		authorizer, err := module.APIGenAuthorizer(nil, map[string]APIGenOperationContract{"startProjectCandidate": contract}, APIGenResourceResolvers{Project: apigenResolver("project", projectgraph.KindProject)})
		if err != nil {
			t.Fatal(err)
		}
		authorizer.SetBootstrapAuthorizer(func(_ context.Context, _ *http.Request, operation string, project projectgraph.ResourceID, _ access.Capability) (APIGenBootstrapDecision, error) {
			if operation != "startProjectCandidate" || project != "project_demo" {
				t.Fatalf("bootstrap decision identity = %q/%q", operation, project)
			}
			return APIGenBootstrapDecision{Handled: true, Allowed: true}, nil
		})
		return authorizer
	}
	serve := func(authorizer *APIGenAuthorizer, credential *access.APICredential) (int, string) {
		protected, ok := authorizer.Protect("startProjectCandidate", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		if !ok {
			t.Fatal("candidate authorizer was not created")
		}
		r := apigenRequest(http.MethodPost, "/api/v1/projects/project_demo/candidates", map[string]string{"project": "project_demo"})
		if credential != nil {
			r.Header.Set("Authorization", "Bearer test-token")
			r = r.WithContext(WithAPICredential(r.Context(), *credential))
		}
		recorder := httptest.NewRecorder()
		protected.ServeHTTP(recorder, r)
		return recorder.Code, recorder.Body.String()
	}
	explicit := &access.APICredential{Principal: access.Principal{ID: "admin"}, Token: access.APIToken{ID: "token_1", PrincipalID: "admin", Capabilities: []access.Capability{access.CapabilityResourceEdit}}}
	if status, body := serve(newAuthorizer(true), explicit); status != http.StatusNoContent {
		t.Fatalf("explicit token status = %d body=%q, want %d", status, body, http.StatusNoContent)
	}
	if status, _ := serve(newAuthorizer(true), nil); status != http.StatusUnauthorized {
		t.Fatalf("session status = %d, want %d", status, http.StatusUnauthorized)
	}
	empty := *explicit
	empty.Token.Capabilities = []access.Capability{}
	if status, _ := serve(newAuthorizer(true), &empty); status != http.StatusForbidden {
		t.Fatalf("empty token status = %d, want %d", status, http.StatusForbidden)
	}
	mismatchedPrincipal := *explicit
	mismatchedPrincipal.Token.PrincipalID = "other-principal"
	if status, _ := serve(newAuthorizer(true), &mismatchedPrincipal); status != http.StatusForbidden {
		t.Fatalf("mismatched token principal status = %d, want %d", status, http.StatusForbidden)
	}
	authoring := *explicit
	authoring.Authoring = &access.AuthoringSession{ID: "authoring_1", PrincipalID: "admin"}
	if status, _ := serve(newAuthorizer(true), &authoring); status != http.StatusForbidden {
		t.Fatalf("authoring credential status = %d, want %d", status, http.StatusForbidden)
	}
	if status, _ := serve(newAuthorizer(false), explicit); status != http.StatusForbidden {
		t.Fatalf("non-admin status = %d, want %d", status, http.StatusForbidden)
	}
	adminError := browserGuardModule(browserGuardRepository{admin: true, err: errors.New("role store unavailable")}, Principal{ID: "admin"}, true)
	errorAuthorizer, err := adminError.APIGenAuthorizer(nil, map[string]APIGenOperationContract{"startProjectCandidate": contract}, APIGenResourceResolvers{Project: apigenResolver("project", projectgraph.KindProject)})
	if err != nil {
		t.Fatal(err)
	}
	errorAuthorizer.SetBootstrapAuthorizer(func(context.Context, *http.Request, string, projectgraph.ResourceID, access.Capability) (APIGenBootstrapDecision, error) {
		return APIGenBootstrapDecision{Handled: true, Allowed: true}, nil
	})
	if status, _ := serve(errorAuthorizer, explicit); status != http.StatusServiceUnavailable {
		t.Fatalf("admin role error status = %d, want %d", status, http.StatusServiceUnavailable)
	}
}

func TestAPIGenActiveProjectScopedResourceCapabilityUsesRoleBinding(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: projectID, Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "model_orders", Kind: projectgraph.KindModel, Name: "orders"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, "admin")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, graph, []accesssnapshot.RoleBinding{{
		ID: "binding_admin", Subject: subject, Role: access.ProjectRoleAdmin,
		Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleAdmin),
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	module := browserGuardModule(browserGuardRepository{}, Principal{ID: "admin"}, true)
	module.SetCurrentEffectiveCapabilities(func(context.Context, string) ([]access.Capability, error) {
		return snapshot.EffectiveCapabilities([]access.SubjectRef{subject})
	})
	contract := APIGenOperationContract{
		OperationID: "getProjectCandidate", Method: http.MethodGet,
		Path: "/api/v1/projects/{project}/candidates/{candidate}", Protected: true, AuthzMode: "privilege",
		Extensions: map[string]any{
			apiGenObjectScopeExtension: "project",
			"x-authz":                  map[string]any{"mode": "privilege", "privilege": "RESOURCE_EDIT"},
		},
	}
	authorizer, err := module.APIGenAuthorizer(
		apigenRuntimeFake{project: projectID, lease: apigenLeaseFake{identity: identity, snapshot: snapshot}},
		map[string]APIGenOperationContract{"getProjectCandidate": contract},
		APIGenResourceResolvers{Project: apigenResolver("project", projectgraph.KindProject)},
	)
	if err != nil {
		t.Fatal(err)
	}
	protected, ok := authorizer.Protect("getProjectCandidate", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	if !ok || protected == nil {
		t.Fatal("active candidate authorizer was not created")
	}
	request := apigenRequest(http.MethodGet, "/api/v1/projects/project_demo/candidates/candidate_1", map[string]string{"project": projectID.String(), "candidate": "candidate_1"})
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("active project-scoped role status = %d body=%q, want %d", recorder.Code, recorder.Body.String(), http.StatusNoContent)
	}
}

func TestAPIGenDeploymentStatusUsesActiveSnapshotForSessionAndProjectToken(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: projectID, Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "model_orders", Kind: projectgraph.KindModel, Name: "orders"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, "admin")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, graph, []accesssnapshot.RoleBinding{{
		ID: "binding_admin", Subject: subject, Role: access.ProjectRoleAdmin,
		Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleAdmin),
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	module := browserGuardModule(browserGuardRepository{}, Principal{ID: "admin"}, true)
	module.SetCurrentEffectiveCapabilities(func(context.Context, string) ([]access.Capability, error) {
		return snapshot.EffectiveCapabilities([]access.SubjectRef{subject})
	})
	runtime := apigenRuntimeFake{project: projectID, lease: apigenLeaseFake{identity: identity, snapshot: snapshot}}
	operations := map[string]APIGenOperationContract{
		"listDeployments": {
			OperationID: "listDeployments", Method: http.MethodGet, Path: "/api/v1/projects/{project}/deployments",
			Protected: true, AuthzMode: "privilege",
			Extensions: map[string]any{apiGenObjectScopeExtension: "project", "x-authz": map[string]any{"mode": "privilege", "privilege": "RESOURCE_READ"}},
		},
		"getDeployment": {
			OperationID: "getDeployment", Method: http.MethodGet, Path: "/api/v1/projects/{project}/deployments/{deployment}",
			Protected: true, AuthzMode: "privilege",
			Extensions: map[string]any{apiGenObjectScopeExtension: "project", "x-authz": map[string]any{"mode": "privilege", "privilege": "RESOURCE_READ"}},
		},
		"listDeploymentEvents": {
			OperationID: "listDeploymentEvents", Method: http.MethodGet, Path: "/api/v1/projects/{project}/deployments/{deployment}/events",
			Protected: true, AuthzMode: "privilege",
			Extensions: map[string]any{apiGenObjectScopeExtension: "project", "x-authz": map[string]any{"mode": "privilege", "privilege": "RESOURCE_READ"}},
		},
	}
	authorizer, err := module.APIGenAuthorizer(runtime, operations, APIGenResourceResolvers{Project: apigenResolver("project", projectgraph.KindProject)})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapCalls := 0
	authorizer.SetBootstrapAuthorizer(func(context.Context, *http.Request, string, projectgraph.ResourceID, access.Capability) (APIGenBootstrapDecision, error) {
		bootstrapCalls++
		return APIGenBootstrapDecision{Handled: false}, nil
	})
	for operationID, contract := range operations {
		t.Run(operationID+"/session", func(t *testing.T) {
			protected, ok := authorizer.Protect(operationID, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
			if !ok || protected == nil {
				t.Fatal("deployment status authorizer was not created")
			}
			request := apigenRequest(http.MethodGet, contract.Path, map[string]string{"project": projectID.String(), "deployment": "deployment_1"})
			recorder := httptest.NewRecorder()
			protected.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNoContent {
				t.Fatalf("active session status = %d body=%q, want %d", recorder.Code, recorder.Body.String(), http.StatusNoContent)
			}
		})
		t.Run(operationID+"/project-token", func(t *testing.T) {
			protected, ok := authorizer.Protect(operationID, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
			if !ok || protected == nil {
				t.Fatal("deployment status authorizer was not created")
			}
			credential := access.APICredential{Principal: access.Principal{ID: "admin"}, Token: access.APIToken{ID: "token_project_read", PrincipalID: "admin", Capabilities: []access.Capability{access.CapabilityResourceRead}}}
			request := apigenRequest(http.MethodGet, contract.Path, map[string]string{"project": projectID.String(), "deployment": "deployment_1"})
			request = request.WithContext(WithAPICredential(request.Context(), credential))
			recorder := httptest.NewRecorder()
			protected.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNoContent {
				t.Fatalf("active project token status = %d body=%q, want %d", recorder.Code, recorder.Body.String(), http.StatusNoContent)
			}
		})
	}
	if bootstrapCalls != len(operations)*2 {
		t.Fatalf("bootstrap decision calls = %d, want one active-runtime decision per request (%d)", bootstrapCalls, len(operations)*2)
	}
}

func TestAPIGenDeploymentStatusBootstrapRemainsFailClosed(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	module := browserGuardModule(browserGuardRepository{admin: false}, Principal{ID: "admin"}, true)
	contract := APIGenOperationContract{
		OperationID: "getDeployment", Method: http.MethodGet, Path: "/api/v1/projects/{project}/deployments/{deployment}",
		Protected: true, AuthzMode: "privilege",
		Extensions: map[string]any{apiGenObjectScopeExtension: "project", "x-authz": map[string]any{"mode": "privilege", "privilege": "RESOURCE_READ"}},
	}
	authorizer, err := module.APIGenAuthorizer(apigenRuntimeFake{project: projectID, err: errors.New("runtime warm-up")}, map[string]APIGenOperationContract{"getDeployment": contract}, APIGenResourceResolvers{Project: apigenResolver("project", projectgraph.KindProject)})
	if err != nil {
		t.Fatal(err)
	}
	authorizer.SetBootstrapAuthorizer(func(context.Context, *http.Request, string, projectgraph.ResourceID, access.Capability) (APIGenBootstrapDecision, error) {
		return APIGenBootstrapDecision{Handled: true, Allowed: true}, nil
	})
	protected, ok := authorizer.Protect("getDeployment", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	if !ok || protected == nil {
		t.Fatal("deployment status authorizer was not created")
	}
	request := apigenRequest(http.MethodGet, contract.Path, map[string]string{"project": projectID.String(), "deployment": "deployment_1"})
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("bootstrap session status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	credential := access.APICredential{Principal: access.Principal{ID: "admin"}, Token: access.APIToken{ID: "token_project_read", PrincipalID: "admin", Capabilities: []access.Capability{access.CapabilityResourceRead}}}
	request = request.WithContext(WithAPICredential(request.Context(), credential))
	request.Header.Set("Authorization", "Bearer test-token")
	recorder = httptest.NewRecorder()
	protected.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("bootstrap non-admin token status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

func TestAPIGenBootstrapAllowlistIncludesCandidateSourceAndManagedDataStaging(t *testing.T) {
	if !isBootstrapAPIGenOperation("retainProjectCandidateSource") {
		t.Fatal("source retention operation is not bootstrap-authorized")
	}
	for _, operation := range []string{
		"createManagedDataUploadSession", "getManagedDataUploadSession", "cancelManagedDataUploadSession", "finalizeManagedDataUploadSession",
		"createManagedDataS3MultipartUpload", "signManagedDataS3MultipartPart", "completeManagedDataS3MultipartUpload", "abortManagedDataS3MultipartUpload",
	} {
		if !isBootstrapAPIGenOperation(operation) {
			t.Errorf("managed-data operation %q is not bootstrap-authorized", operation)
		}
	}
	for _, operation := range []string{"listManagedDataRevisions", "getManagedDataRevision", "getActiveManagedDataRevision", "listManagedDataUploadSessions", "listManagedDataUploadSessionEvents", "getDashboard"} {
		if isBootstrapAPIGenOperation(operation) {
			t.Errorf("unrelated operation %q is bootstrap-authorized", operation)
		}
	}
	for _, operation := range []string{"listDeployments", "getDeployment", "listDeploymentEvents"} {
		if !isBootstrapAPIGenOperation(operation) {
			t.Errorf("deployment status operation %q is not bootstrap-authorized", operation)
		}
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
