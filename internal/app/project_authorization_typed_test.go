package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

func TestTypedDashboardReadDecisionRequiresExactViewerPair(t *testing.T) {
	dashboardA, err := access.NewResourceRef("dashboard_a", projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	dashboardB, err := access.NewResourceRef("dashboard_b", projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	semantic, err := access.NewResourceRef("semantic_a", projectgraph.KindSemanticModel)
	if err != nil {
		t.Fatal(err)
	}
	readA, err := access.NewExactPermissionPair(access.ActionDashboardRead, "project_1", dashboardA)
	if err != nil {
		t.Fatal(err)
	}
	semanticRead, err := access.NewExactPermissionPair(access.ActionSemanticRead, "project_1", semantic)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name       string
		resource   access.ResourceRef
		capability access.Capability
		credential *access.APICredential
		wantTyped  bool
		wantAllow  bool
	}{
		{
			name:     "exact dashboard read",
			resource: dashboardA, credential: &access.APICredential{Token: access.APIToken{
				ID: "typed", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{readA},
			}}, wantTyped: true, wantAllow: true,
		},
		{
			name:     "dashboard A cannot authorize B",
			resource: dashboardB, credential: &access.APICredential{Token: access.APIToken{
				ID: "typed", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{readA},
			}}, wantTyped: true,
		},
		{
			name:     "semantic-only token",
			resource: dashboardA, credential: &access.APICredential{Token: access.APIToken{
				ID: "typed", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{semanticRead},
			}}, wantTyped: true,
		},
		{
			name:     "read does not authorize mutation",
			resource: dashboardA, capability: access.CapabilityResourceEdit, credential: &access.APICredential{Token: access.APIToken{
				ID: "typed", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{readA},
			}}, wantTyped: true,
		},
		{
			name:     "session retains legacy path",
			resource: dashboardA,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			if test.credential != nil {
				ctx = accessmodule.WithAPICredential(ctx, *test.credential)
			}
			capability := test.capability
			if capability == "" {
				capability = access.CapabilityResourceRead
			}
			typed, allowed := typedDashboardReadDecision(ctx, "project_1", []access.ResourceRef{test.resource}, func(access.ResourceRef) access.Capability {
				return capability
			})
			if typed != test.wantTyped || allowed != test.wantAllow {
				t.Fatalf("decision = typed:%t allowed:%t, want typed:%t allowed:%t", typed, allowed, test.wantTyped, test.wantAllow)
			}
		})
	}
}

func TestTypedDashboardAuthoringActionsDoNotCrossAuthorize(t *testing.T) {
	projectID := projectgraph.ResourceID("project_1")
	dashboard, err := access.NewResourceRef("dashboard_a", projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	read, err := access.NewExactPermissionPair(access.ActionDashboardRead, projectID, dashboard)
	if err != nil {
		t.Fatal(err)
	}
	update, err := access.NewExactPermissionPair(access.ActionDashboardUpdate, projectID, dashboard)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name        string
		permissions []access.PermissionPair
		action      access.Action
		want        bool
	}{
		{name: "read cannot edit", permissions: []access.PermissionPair{read}, action: access.ActionDashboardUpdate},
		{name: "edit cannot publish", permissions: []access.PermissionPair{update}, action: access.ActionDashboardPublish},
		{name: "exact update allows edit", permissions: []access.PermissionPair{update}, action: access.ActionDashboardUpdate, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			credential := access.APICredential{Token: access.APIToken{ID: "typed", PermissionProfile: access.PermissionCatalogProfile, Permissions: test.permissions}}
			ctx := accessmodule.WithAPICredential(context.Background(), credential)
			typed, allowed := typedPermissionDecision(ctx, projectID, []access.ResourceRef{dashboard}, func(access.ResourceRef) (access.Action, bool) {
				return test.action, true
			})
			if !typed || allowed != test.want {
				t.Fatalf("decision = typed:%t allowed:%t, want typed:true allowed:%t", typed, allowed, test.want)
			}
		})
	}
}

type typedAuthoringAccess struct {
	tusAccess
	credential access.APICredential
}

func (a typedAuthoringAccess) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := accessmodule.WithAPICredential(r.Context(), a.credential)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func TestProjectAuthoringGuardRejectsDashboardReadToken(t *testing.T) {
	dashboard, err := access.NewResourceRef("dashboard_a", projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	read, err := access.NewExactPermissionPair(access.ActionDashboardRead, "project_demo", dashboard)
	if err != nil {
		t.Fatal(err)
	}
	authorizer := &repositoryDashboardAuthorizerFake{}
	guarded := protectProjectAuthoringResourceWithTypedAction(
		typedAuthoringAccess{
			tusAccess:  tusAccess{principal: accessmodule.Principal{ID: "owner"}, ok: true},
			credential: access.APICredential{Token: access.APIToken{ID: "typed", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{read}}},
		},
		tusRuntime{project: "project_demo"}, authorizer, access.CapabilityResourceEdit, access.ActionDashboardUpdate,
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
	)
	router := chi.NewRouter()
	router.Get("/dashboards/{dashboard}/edit", guarded)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/dashboards/dashboard_a/edit", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if authorizer.editCalls != 0 || authorizer.manageCalls != 0 {
		t.Fatalf("durable authorizer calls = edit %d, manage %d; typed denial should precede it", authorizer.editCalls, authorizer.manageCalls)
	}
}
