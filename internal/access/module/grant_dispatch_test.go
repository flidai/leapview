package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

// Embedding the production port keeps this dispatch test focused: only the
// grant methods reached by the module switch are implemented below.
type grantDispatchControlStore struct {
	access.ControlStore
	row      access.ControlGrant
	called   string
	instance string
	project  string
	revision int64
	actor    string
}

func (f *grantDispatchControlStore) ListControlGrants(_ context.Context, instanceID, projectID string) ([]access.ControlGrant, error) {
	f.called, f.instance, f.project = "list", instanceID, projectID
	return []access.ControlGrant{}, nil
}
func (f *grantDispatchControlStore) CreateGrant(_ context.Context, input access.ControlGrantInput, _ projectgraph.ProjectGraph) (access.ControlGrant, error) {
	f.called, f.instance, f.project = "create", input.InstanceID, input.ProjectID
	return f.row, nil
}
func (f *grantDispatchControlStore) ControlGrant(_ context.Context, instanceID, grantID string) (access.ControlGrant, error) {
	f.called, f.instance = "get", instanceID
	return f.row, nil
}
func (f *grantDispatchControlStore) UpdateGrant(_ context.Context, input access.ControlGrantInput, _ projectgraph.ProjectGraph) (access.ControlGrant, error) {
	f.called, f.instance, f.project, f.revision = "update", input.InstanceID, input.ProjectID, input.ExpectedRevision
	return f.row, nil
}
func (f *grantDispatchControlStore) RevokeGrant(_ context.Context, instanceID, _ string, revision int64, actorID string) (access.ControlGrant, error) {
	f.called, f.instance, f.revision, f.actor = "delete", instanceID, revision, actorID
	return f.row, nil
}

func TestDispatchAPIGenGrantOperationsReachControlHandlers(t *testing.T) {
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project_demo", Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "dashboard_main", Kind: projectgraph.KindDashboard, Name: "main"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project_demo", "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := access.NewResourceRef("dashboard_main", projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, "principal-admin")
	if err != nil {
		t.Fatal(err)
	}
	store := &grantDispatchControlStore{row: access.ControlGrant{ID: "grant-reader", InstanceID: "instance-live", ProjectID: "project_demo", Subject: subject, Resource: resource, Capability: access.CapabilityResourceRead, Revision: 3, CreatedAt: "2026-09-04T00:00:00Z"}}
	module, err := newSurface(surfaceConfig{
		InstanceID: "instance-live", Control: store,
		CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: "principal-admin"}, true },
	})
	if err != nil {
		t.Fatal(err)
	}
	module.SetCurrentAuthorizationSnapshot(func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) { return snapshot, nil })

	tests := []struct {
		name      string
		operation string
		method    string
		path      string
		headers   map[string]string
		body      string
		wantCall  string
	}{
		{name: "list", operation: "listGrants", method: http.MethodGet, path: "/api/v1/grants", wantCall: "list"},
		{name: "create", operation: "createGrant", method: http.MethodPost, path: "/api/v1/grants", headers: map[string]string{"Idempotency-Key": "dispatch-create-1"}, body: grantDispatchBody(), wantCall: "create"},
		{name: "get", operation: "getGrant", method: http.MethodGet, path: "/api/v1/grants/grant-reader", wantCall: "get"},
		{name: "update", operation: "updateGrant", method: http.MethodPatch, path: "/api/v1/grants/grant-reader", headers: map[string]string{"If-Match": `"revision-3"`}, body: grantDispatchBody(), wantCall: "update"},
		{name: "delete", operation: "deleteGrant", method: http.MethodDelete, path: "/api/v1/grants/grant-reader", wantCall: "delete"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store.called, store.instance, store.project, store.revision, store.actor = "", "", "", 0, ""
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			for key, value := range test.headers {
				request.Header.Set(key, value)
			}
			if test.name != "list" && test.name != "create" {
				route := chi.NewRouteContext()
				route.URLParams.Add("grant", "grant-reader")
				request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
			}
			response := httptest.NewRecorder()
			if !module.DispatchAPIGenOperation(test.operation, response, request) {
				t.Fatal("grant operation was not dispatched")
			}
			if response.Code < 200 || response.Code >= 300 {
				t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
			}
			if store.called != test.wantCall || store.instance != "instance-live" {
				t.Fatalf("control call = %q instance=%q, want %q and instance-live", store.called, store.instance, test.wantCall)
			}
		})
	}
	if module.DispatchAPIGenOperation("unknownGrantOperation", httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("unknown operation was dispatched")
	}
}

func grantDispatchBody() string {
	return `{"resourceKind":"dashboard","resourceId":"dashboard_main","subjectType":"principal","subjectId":"principal-admin","capability":"RESOURCE_READ"}`
}

var _ access.ControlStore = (*grantDispatchControlStore)(nil)
