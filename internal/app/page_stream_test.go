package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	adminmodule "github.com/flidai/leapview/internal/admin/module"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestPageStreamRouteInventoryIsProjectOwned(t *testing.T) {
	got := map[string]struct{}{
		routeLogin: {}, routeDashboard: {}, routeDashboardBuilder: {},
		routeChat: {}, routeAdmin: {},
	}
	want := []string{"login", "dashboard", "dashboard_builder", "chat", "admin"}
	for _, route := range want {
		if _, ok := got[route]; !ok {
			t.Fatalf("route %q is missing from the page-stream inventory", route)
		}
	}
	for _, legacy := range []string{
		"catalog", "pipelines", "workspace", "workspace_asset", "connections",
		"connection_asset", "data",
	} {
		if _, ok := got[legacy]; ok {
			t.Fatalf("legacy workspace page-stream route %q remains registered", legacy)
		}
	}
}

func TestDashboardBuilderPageStreamValidatesSelectorAndUsesHandlerAuthorization(t *testing.T) {
	auth, err := accessmodule.NewAuth(nil, accessmodule.AuthConfig{
		DevBypass: true, DevAPIToken: "builder-stream-test", CSRFKey: "0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	accessSurface, err := accessmodule.Build(context.Background(), accessmodule.Config{ExistingAuth: auth})
	if err != nil {
		t.Fatal(err)
	}
	routes := &capabilityRoutes{
		accessModule:    accessSurface,
		dashboardModule: &dashboardmodule.Module{},
		agentModule:     &agentmodule.Module{},
		adminModule:     &adminmodule.Module{},
	}
	runtime := &runtimeServices{}
	configurePageStream(routes, runtime, nil, nil)

	for _, test := range []struct {
		name       string
		url        string
		wantStatus int
	}{
		{name: "exact dashboard", url: "/updates?route=dashboard_builder&dashboard=dashboard_owned", wantStatus: http.StatusInternalServerError},
		{name: "missing dashboard", url: "/updates?route=dashboard_builder", wantStatus: http.StatusNotFound},
		{name: "duplicate dashboard", url: "/updates?route=dashboard_builder&dashboard=dashboard_owned&dashboard=dashboard_other", wantStatus: http.StatusNotFound},
		{name: "invalid dashboard", url: "/updates?route=dashboard_builder&dashboard=not%20a%20dashboard", wantStatus: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, test.url, nil)
			request.Header.Set("Authorization", "Bearer builder-stream-test")
			runtime.pageStreams.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("dashboard builder stream status = %d, want %d", recorder.Code, test.wantStatus)
			}
		})
	}
}

func TestDashboardPageStreamResourceRequiresExactDashboardID(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want projectgraph.ResourceID
	}{
		{name: "direct resource", url: "/updates?route=dashboard&dashboard=dashboard_sales", want: "dashboard_sales"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			resources := dashboardPageStreamResource(req, projectgraph.ResourceID("project_demo"))
			if len(resources) != 1 {
				t.Fatalf("resource count = %d, want 1", len(resources))
			}
			if got := resources[0].ID(); got != tt.want {
				t.Fatalf("resource id = %q, want %q", got, tt.want)
			}
			if got := resources[0].Kind(); got != projectgraph.KindDashboard {
				t.Fatalf("resource kind = %q, want %q", got, projectgraph.KindDashboard)
			}
			if err := resources[0].Validate(); err != nil {
				t.Fatalf("resource is not canonical: %v", err)
			}
		})
	}
}

func TestDashboardBuilderPageStreamAuthorizesPrivateDraftWithoutPublishedGraphResource(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "generation_draft")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := access.NewTypedRoleBinding("editor", "Editor", access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "owner"}, access.PermissionRoleEditor, projectID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, graph, []accesssnapshot.RoleBinding{binding}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		url        string
		wantStatus int
		wantCalls  int
	}{
		{name: "private draft", url: "/updates?route=dashboard_builder&dashboard=dashboard_owned", wantStatus: http.StatusNoContent, wantCalls: 1},
		{name: "missing dashboard", url: "/updates?route=dashboard_builder", wantStatus: http.StatusNotFound},
		{name: "duplicate dashboard", url: "/updates?route=dashboard_builder&dashboard=dashboard_owned&dashboard=dashboard_other", wantStatus: http.StatusNotFound},
		{name: "invalid dashboard", url: "/updates?route=dashboard_builder&dashboard=not%20a%20dashboard", wantStatus: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			authorizer := &repositoryDashboardAuthorizerFake{}
			guarded := protectProjectAuthoringResourceWithSelector(
				tusAccess{principal: accessmodule.Principal{ID: "owner"}, ok: true, subjects: []access.SubjectRef{{Kind: access.SubjectKindPrincipal, ID: "owner"}}},
				tusRuntime{project: projectID, lease: tusLease{identity: identity, snapshot: snapshot}},
				authorizer,
				access.ActionDashboardUpdate,
				dashboardBuilderPageStreamDashboardID,
				func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
			)
			recorder := httptest.NewRecorder()
			guarded(recorder, httptest.NewRequest(http.MethodGet, test.url, nil))
			if recorder.Code != test.wantStatus || authorizer.editCalls != test.wantCalls {
				t.Errorf("status = %d, edit calls = %d; want %d and %d", recorder.Code, authorizer.editCalls, test.wantStatus, test.wantCalls)
			}
		})
	}
}

func TestDashboardBuilderPageStreamRejectsUnpublishedDraftWithoutTypedAssignment(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "generation_draft")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, graph, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	authorizer := &repositoryDashboardAuthorizerFake{}
	guarded := protectProjectAuthoringResourceWithSelector(
		tusAccess{principal: accessmodule.Principal{ID: "owner"}, ok: true, subjects: []access.SubjectRef{{Kind: access.SubjectKindPrincipal, ID: "owner"}}},
		tusRuntime{project: projectID, lease: tusLease{identity: identity, snapshot: snapshot}},
		authorizer, access.ActionDashboardUpdate, dashboardBuilderPageStreamDashboardID,
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
	)
	recorder := httptest.NewRecorder()
	guarded(recorder, httptest.NewRequest(http.MethodGet, "/updates?route=dashboard_builder&dashboard=dashboard_owned", nil))
	if recorder.Code != http.StatusForbidden || authorizer.editCalls != 0 {
		t.Fatalf("status = %d, repository calls = %d; want forbidden before repository authorization", recorder.Code, authorizer.editCalls)
	}
}

func TestDashboardPageStreamResourceFailsClosedForMissingOrInvalidID(t *testing.T) {
	for _, url := range []string{
		"/updates?route=dashboard",
		"/updates?route=dashboard&dashboard=",
		"/updates?route=dashboard&dashboard=not%20a%20resource",
		"/updates?route=dashboard&dashboard=dashboard%20with%20spaces",
		"/updates?route=dashboard&dashboard=dashboard_sales&dashboard=dashboard_finance",
	} {
		t.Run(url, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, url, nil)
			if got := dashboardPageStreamResource(request, "project_demo"); got != nil {
				t.Fatalf("invalid dashboard selector resolved to %#v", got)
			}
		})
	}
}

func TestDashboardPageStreamResourceUsesDashboardCapabilityContract(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/updates?route=dashboard&dashboard=dashboard_sales", nil)
	resource := dashboardPageStreamResource(request, "project_demo")[0]
	if !access.SupportsCapability(resource.Kind(), access.CapabilityResourceRead) {
		t.Fatalf("dashboard resource does not support RESOURCE_READ")
	}
	if !access.SupportsCapability(resource.Kind(), access.CapabilityResourceEdit) {
		t.Fatalf("dashboard resource does not support RESOURCE_EDIT")
	}
}
