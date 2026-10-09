package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/accessadapter"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	uitransport "github.com/flidai/leapview/internal/platform/web/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

// dashboardDeleteSnapshotAuthorizer supplies the durable private-dashboard
// context while leaving every permission decision to the production adapter.
type dashboardDeleteSnapshotAuthorizer struct {
	adapter *accessadapter.Adapter
	owner   string
	actions []authoring.AuthorizationAction
}

func (a *dashboardDeleteSnapshotAuthorizer) AuthorizeDashboardEdit(ctx context.Context, project projectgraph.ResourceID, actor string, dashboard authoring.DashboardID) error {
	return a.authorize(ctx, project, actor, dashboard, authoring.AuthorizationActionEdit)
}

func (a *dashboardDeleteSnapshotAuthorizer) AuthorizeDashboardManage(ctx context.Context, project projectgraph.ResourceID, actor string, dashboard authoring.DashboardID) error {
	return a.authorize(ctx, project, actor, dashboard, authoring.AuthorizationActionArchive)
}

func (a *dashboardDeleteSnapshotAuthorizer) authorize(ctx context.Context, project projectgraph.ResourceID, actor string, dashboard authoring.DashboardID, action authoring.AuthorizationAction) error {
	a.actions = append(a.actions, action)
	return a.adapter.Authorize(ctx, authoringservice.AuthorizationRequest{
		ActorID: actor, ProjectID: project, DashboardID: dashboard,
		OwnerPrincipalID: a.owner, Target: authoringservice.AuthorizationTargetAuthoredDashboard,
		Visibility: authoring.VisibilityPrivate, Action: action,
	})
}

func TestDashboardDeleteNativePostRequiresTypedDeleteAuthorityWithoutDevBypass(t *testing.T) {
	const (
		actorID     = "principal_delete_actor"
		otherID     = "principal_other_owner"
		projectID   = projectgraph.ResourceID("project_demo")
		dashboardID = authoring.DashboardID("dashboard_private_draft")
	)
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "generation_delete")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name           string
		role           access.PermissionRole
		legacy         access.ProjectRole
		bindingSubject string
		owner          string
		deleteTarget   projectgraph.ResourceID
		unpublished    bool
		allowed        bool
	}{
		{name: "project admin with exact grant own graph-bound dashboard", role: access.PermissionRoleProjectAdmin, bindingSubject: actorID, owner: actorID, deleteTarget: projectgraph.ResourceID(dashboardID), allowed: true},
		{name: "project admin with exact grant another owner's graph-bound dashboard", role: access.PermissionRoleProjectAdmin, bindingSubject: actorID, owner: otherID, deleteTarget: projectgraph.ResourceID(dashboardID), allowed: true},
		{name: "owner with exact grant graph-bound dashboard", bindingSubject: actorID, owner: actorID, deleteTarget: projectgraph.ResourceID(dashboardID), allowed: true},
		{name: "non-owner with exact grant graph-bound dashboard", bindingSubject: actorID, owner: otherID, deleteTarget: projectgraph.ResourceID(dashboardID), allowed: true},
		{name: "project admin cannot delete newly authored draft without action grant", role: access.PermissionRoleProjectAdmin, bindingSubject: actorID, owner: actorID, unpublished: true},
		{name: "editor owner cannot delete newly authored draft", role: access.PermissionRoleEditor, bindingSubject: actorID, owner: actorID, unpublished: true},
		{name: "viewer owner cannot delete newly authored draft", role: access.PermissionRoleViewer, bindingSubject: actorID, owner: actorID, unpublished: true},
		{name: "ownership without assignment cannot delete newly authored draft", owner: actorID, unpublished: true},
		{name: "another principal's delete grant cannot authorize owner", bindingSubject: otherID, owner: actorID, deleteTarget: projectgraph.ResourceID(dashboardID)},
		{name: "unrelated dashboard delete grant cannot authorize owner", bindingSubject: actorID, owner: actorID, deleteTarget: "dashboard_other"},
		{name: "editor update authority cannot authorize graph-bound delete", role: access.PermissionRoleEditor, bindingSubject: actorID, owner: actorID},
		{name: "legacy admin cannot authorize newly authored draft delete", legacy: access.ProjectRoleAdmin, bindingSubject: actorID, owner: actorID, unpublished: true},
		{name: "legacy owner cannot authorize newly authored draft delete", legacy: access.ProjectRoleOwner, bindingSubject: actorID, owner: actorID, unpublished: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Exact grants must bind to an immutable graph resource. Successful cases
			// establish native action enforcement, not delete authority for new draft
			// IDs absent from that graph. The unpublished cases exercise that boundary.
			resources := []projectgraph.Resource{{ID: "dashboard_other", Kind: projectgraph.KindDashboard, Name: "other"}}
			if !test.unpublished {
				resources = append(resources, projectgraph.Resource{ID: projectgraph.ResourceID(dashboardID), Kind: projectgraph.KindDashboard, Name: "private-draft"})
			}
			graph, err := projectgraph.NewProjectGraph(resources, nil)
			if err != nil {
				t.Fatal(err)
			}
			subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: test.bindingSubject}
			var bindings []accesssnapshot.RoleBinding
			if test.role != "" {
				binding, err := access.NewTypedRoleBinding("binding_dashboard_delete", "dashboard role", subject, test.role, identity.ProjectID)
				if err != nil {
					t.Fatal(err)
				}
				bindings = append(bindings, binding)
			}
			if test.legacy != "" {
				bindings = append(bindings, accesssnapshot.RoleBinding{ID: "legacy", Subject: subject, Role: test.legacy, Capabilities: access.ProjectRoleCapabilities(test.legacy)})
			}
			var grants []accesssnapshot.Grant
			if test.deleteTarget != "" {
				resource, err := access.NewResourceRef(test.deleteTarget, projectgraph.KindDashboard)
				if err != nil {
					t.Fatal(err)
				}
				pair, err := access.NewExactPermissionPair(access.ActionDashboardDelete, projectID, resource)
				if err != nil {
					t.Fatal(err)
				}
				grant, err := accesssnapshot.NewTypedPermissionGrant("delete", "dashboard delete", subject, pair)
				if err != nil {
					t.Fatal(err)
				}
				grants = append(grants, grant)
			}
			snapshot, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, graph, bindings, grants, nil)
			if err != nil {
				t.Fatal(err)
			}
			accessModule := tusAccess{
				principal: accessmodule.Principal{ID: actorID, DevBypass: false}, ok: true,
				subjects: []access.SubjectRef{{Kind: access.SubjectKindPrincipal, ID: actorID}},
			}
			runtimeHost := tusRuntime{project: projectID, lease: tusLease{identity: identity, snapshot: snapshot}}
			adapter, err := accessadapter.New(accessadapter.Options{
				AuthorizeTypedResource: func(ctx context.Context, actor string, project projectgraph.ResourceID, resource access.ResourceRef, action access.Action) (bool, bool, error) {
					return authorizeTypedResourceActionWithDraft(ctx, accessModule, runtimeHost, actor, project, []access.ResourceRef{resource}, action, allowsUnpublishedDashboardAuthoringAction(resource, action))
				},
				AuthorizeTypedProject: func(ctx context.Context, actor string, project projectgraph.ResourceID, action access.Action) (bool, bool, error) {
					return authorizeTypedAuthoringProjectAction(ctx, accessModule, runtimeHost, actor, project, action)
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			authorizer := &dashboardDeleteSnapshotAuthorizer{adapter: adapter, owner: test.owner}
			nextCalled, mutated := false, false
			guarded := protectProjectAuthoringResource(accessModule, runtimeHost, authorizer, access.ActionDashboardDelete, func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true
				// The service boundary independently checks DELETE before mutation.
				if err := authorizer.authorize(r.Context(), projectID, actorID, authoring.DashboardID(chi.URLParam(r, "dashboard")), authoring.AuthorizationActionDelete); err != nil {
					uitransport.WriteBrowserAuthorizationError(w, r, http.StatusForbidden)
					return
				}
				mutated = true
				w.WriteHeader(http.StatusNoContent)
			})
			router := chi.NewRouter()
			router.Post("/dashboards/{dashboard}/delete", guarded)
			request := httptest.NewRequest(http.MethodPost, "/dashboards/"+string(dashboardID)+"/delete", strings.NewReader("draft=draft_private"))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Accept", "text/html")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			wantStatus := http.StatusForbidden
			var wantActions []authoring.AuthorizationAction
			if test.allowed {
				wantStatus = http.StatusNoContent
				wantActions = []authoring.AuthorizationAction{authoring.AuthorizationActionArchive, authoring.AuthorizationActionDelete}
			}
			if response.Code != wantStatus || nextCalled != test.allowed || mutated != test.allowed {
				t.Fatalf("status = %d, handler called = %t, mutated = %t; want status %d, handler/mutation %t; body %q", response.Code, nextCalled, mutated, wantStatus, test.allowed, response.Body.String())
			}
			if !reflect.DeepEqual(authorizer.actions, wantActions) {
				t.Fatalf("authorization actions = %v, want %v", authorizer.actions, wantActions)
			}
			if !test.allowed {
				if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
					t.Fatalf("native form denial content type = %q, want HTML recovery", contentType)
				}
				for _, text := range []string{"You don't have access to this dashboard", "No changes were made", "Open your profile", `href="/admin/profile"`} {
					if !strings.Contains(response.Body.String(), text) {
						t.Fatalf("native form denial missing %q: %s", text, response.Body.String())
					}
				}
				if response.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("native form authorization denial can be cached")
				}
			}
		})
	}
}
