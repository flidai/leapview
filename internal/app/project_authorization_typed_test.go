package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
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

func TestTypedBrowserDecisionUsesPrincipalAndGroupAssignments(t *testing.T) {
	identity, err := projectgraph.NewServingIdentity("project_1", "prod", "generation_typed_browser")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "dashboard_a", Kind: projectgraph.KindDashboard, Name: "dashboard_a"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := access.NewResourceRef("dashboard_a", projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(access.ActionDashboardRead, identity.ProjectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	groupGrant, err := accesssnapshot.NewTypedGrant("group-dashboard-read", "group dashboard read", access.SubjectRef{Kind: access.SubjectKindGroup, ID: "group_analysts"}, []access.PermissionPair{pair})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, []accesssnapshot.Grant{groupGrant}, nil)
	if err != nil {
		t.Fatal(err)
	}
	typed, allowed, err := typedPermissionDecisionForSnapshot(context.Background(), "principal_1", identity.ProjectID, []access.ResourceRef{resource}, func(access.ResourceRef) (access.Action, bool) {
		return access.ActionDashboardRead, true
	}, snapshot, []access.SubjectRef{
		{Kind: access.SubjectKindPrincipal, ID: "principal_1"},
		{Kind: access.SubjectKindGroup, ID: "group_analysts"},
	})
	if err != nil || !typed || !allowed {
		t.Fatalf("group typed browser decision = typed:%t allowed:%t err:%v, want typed:true allowed:true", typed, allowed, err)
	}
}

func TestTypedBrowserDecisionRejectsTypedTokenAgainstLegacyOnlyAuthority(t *testing.T) {
	identity, err := projectgraph.NewServingIdentity("project_1", "prod", "generation_legacy_browser")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "dashboard_a", Kind: projectgraph.KindDashboard, Name: "dashboard_a"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := access.NewResourceRef("dashboard_a", projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	legacySubject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal_1"}
	legacyGrant, err := access.NewCanonicalGrant(graph, legacySubject, resource, access.CapabilityResourceRead)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, []accesssnapshot.Grant{{ID: "legacy-read", Canonical: legacyGrant}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(access.ActionDashboardRead, identity.ProjectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	ctx := accessmodule.WithAPICredential(context.Background(), access.APICredential{
		Principal: access.Principal{ID: "principal_1"},
		Token:     access.APIToken{ID: "typed-token", PrincipalID: "principal_1", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{pair}},
	})
	typed, allowed, err := typedPermissionDecisionForSnapshot(ctx, "principal_1", identity.ProjectID, []access.ResourceRef{resource}, func(access.ResourceRef) (access.Action, bool) {
		return access.ActionDashboardRead, true
	}, snapshot, []access.SubjectRef{legacySubject})
	if err != nil {
		t.Fatal(err)
	}
	if !typed || allowed {
		t.Fatalf("typed token over legacy authority = typed:%t allowed:%t, want typed:true allowed:false", typed, allowed)
	}
}

func TestTypedBrowserMutationRejectsCredentialWithDifferentAction(t *testing.T) {
	identity, err := projectgraph.NewServingIdentity("project_1", "prod", "generation_typed_browser_mutation")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "dashboard_a", Kind: projectgraph.KindDashboard, Name: "dashboard_a"},
		{ID: "pipeline_a", Kind: projectgraph.KindPipeline, Name: "pipeline_a"},
		{ID: "connection_a", Kind: projectgraph.KindConnection, Name: "connection_a"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal_1"}
	var authority []access.PermissionPair
	var resources []access.ResourceRef
	for _, requested := range []struct {
		action access.Action
		id     projectgraph.ResourceID
		kind   projectgraph.Kind
	}{
		{access.ActionDashboardUpdate, "dashboard_a", projectgraph.KindDashboard},
		{access.ActionPipelineRun, "pipeline_a", projectgraph.KindPipeline},
		{access.ActionConnectionManage, "connection_a", projectgraph.KindConnection},
	} {
		resource, resourceErr := access.NewResourceRef(requested.id, requested.kind)
		if resourceErr != nil {
			t.Fatal(resourceErr)
		}
		pair, pairErr := access.NewExactPermissionPair(requested.action, identity.ProjectID, resource)
		if pairErr != nil {
			t.Fatal(pairErr)
		}
		authority = append(authority, pair)
		resources = append(resources, resource)
	}
	grant, err := accesssnapshot.NewTypedGrant("typed-browser-mutations", "typed browser mutations", principal, authority)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, []accesssnapshot.Grant{grant}, nil)
	if err != nil {
		t.Fatal(err)
	}
	readResource, err := access.NewResourceRef("dashboard_a", projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := access.NewExactPermissionPair(access.ActionDashboardRead, identity.ProjectID, readResource)
	if err != nil {
		t.Fatal(err)
	}
	ctx := accessmodule.WithAPICredential(context.Background(), access.APICredential{
		Principal: access.Principal{ID: principal.ID},
		Token: access.APIToken{
			ID: "read-only-token", PrincipalID: principal.ID,
			PermissionProfile: access.PermissionCatalogProfile,
			Permissions:       []access.PermissionPair{readOnly},
		},
	})
	runtime := tusRuntime{project: identity.ProjectID, lease: tusLease{identity: identity, snapshot: snapshot}}
	accessModule := tusAccess{principal: accessmodule.Principal{ID: principal.ID}, ok: true, subjects: []access.SubjectRef{principal}}
	for index, requested := range []struct {
		action     access.Action
		capability access.Capability
	}{
		{access.ActionDashboardUpdate, access.CapabilityResourceManage},
		{access.ActionPipelineRun, access.CapabilityResourceUse},
		{access.ActionConnectionManage, access.CapabilityResourceManage},
	} {
		allowed, authorizeErr := authorizeProjectResourcesWithTypedAction(ctx, accessModule, runtime, principal.ID, identity.ProjectID, []access.ResourceRef{resources[index]}, requested.capability, requested.action)
		if authorizeErr != nil {
			t.Fatal(authorizeErr)
		}
		if allowed {
			t.Fatalf("read-only credential authorized %s", requested.action)
		}
	}
}

func TestTypedPermissionDecisionRejectsCredentialAttenuationMismatch(t *testing.T) {
	resource, err := access.NewResourceRef("dashboard_a", projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(access.ActionDashboardRead, "project_1", resource)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		credential access.APICredential
	}{
		{name: "wrong principal", credential: access.APICredential{Principal: access.Principal{ID: "principal_2"}, Token: access.APIToken{ID: "typed", PrincipalID: "principal_1", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{pair}}}},
		{name: "wrong profile", credential: access.APICredential{Principal: access.Principal{ID: "principal_1"}, Token: access.APIToken{ID: "typed", PrincipalID: "principal_1", PermissionProfile: "legacy", Permissions: []access.PermissionPair{pair}}}},
		{name: "invalid pair set", credential: access.APICredential{Principal: access.Principal{ID: "principal_1"}, Token: access.APIToken{ID: "typed", PrincipalID: "principal_1", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{{Action: access.ActionDashboardRead}}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: "principal_1"})
			ctx = accessmodule.WithAPICredential(ctx, test.credential)
			typed, allowed := typedPermissionDecision(ctx, "project_1", []access.ResourceRef{resource}, func(access.ResourceRef) (access.Action, bool) {
				return access.ActionDashboardRead, true
			})
			if !typed || allowed {
				t.Fatalf("decision = typed:%t allowed:%t, want typed:true allowed:false", typed, allowed)
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
