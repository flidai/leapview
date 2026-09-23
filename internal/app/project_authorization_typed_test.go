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

func TestTypedPermissionDecisionForSnapshotRequiresExactViewerPair(t *testing.T) {
	identity, err := projectgraph.NewServingIdentity("project_1", "prod", "generation_typed_viewer")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "dashboard_a", Kind: projectgraph.KindDashboard, Name: "dashboard_a"},
		{ID: "dashboard_b", Kind: projectgraph.KindDashboard, Name: "dashboard_b"},
		{ID: "semantic_a", Kind: projectgraph.KindSemanticModel, Name: "semantic_a"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
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
	readA, err := access.NewExactPermissionPair(access.ActionDashboardRead, identity.ProjectID, dashboardA)
	if err != nil {
		t.Fatal(err)
	}
	semanticRead, err := access.NewExactPermissionPair(access.ActionSemanticRead, identity.ProjectID, semantic)
	if err != nil {
		t.Fatal(err)
	}
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal_1"}
	grant, err := accesssnapshot.NewTypedGrant("dashboard-a-read", "dashboard A read", subject, []access.PermissionPair{readA})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, []accesssnapshot.Grant{grant}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name        string
		resource    access.ResourceRef
		action      access.Action
		permissions []access.PermissionPair
		withToken   bool
		wantAllow   bool
	}{
		{name: "exact dashboard read", resource: dashboardA, action: access.ActionDashboardRead, permissions: []access.PermissionPair{readA}, withToken: true, wantAllow: true},
		{name: "dashboard A cannot authorize B", resource: dashboardB, action: access.ActionDashboardRead, permissions: []access.PermissionPair{readA}, withToken: true},
		{name: "semantic-only token", resource: dashboardA, action: access.ActionDashboardRead, permissions: []access.PermissionPair{semanticRead}, withToken: true},
		{name: "read does not authorize mutation", resource: dashboardA, action: access.ActionDashboardUpdate, permissions: []access.PermissionPair{readA}, withToken: true},
		{name: "session uses typed assignment", resource: dashboardA, action: access.ActionDashboardRead, wantAllow: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			if test.withToken {
				ctx = accessmodule.WithAPICredential(ctx, access.APICredential{
					Principal: access.Principal{ID: subject.ID},
					Token: access.APIToken{
						ID: "typed", PrincipalID: subject.ID, PermissionProfile: access.PermissionCatalogProfile,
						Permissions: test.permissions,
					},
				})
			}
			typed, allowed, decisionErr := typedPermissionDecisionForSnapshot(ctx, subject.ID, identity.ProjectID, []access.ResourceRef{test.resource}, func(access.ResourceRef) (access.Action, bool) {
				return test.action, true
			}, snapshot, []access.SubjectRef{subject})
			if decisionErr != nil || !typed || allowed != test.wantAllow {
				t.Fatalf("decision = typed:%t allowed:%t err:%v, want typed:true allowed:%t", typed, allowed, decisionErr, test.wantAllow)
			}
		})
	}
}

func TestTypedDashboardAuthoringActionsDoNotCrossAuthorize(t *testing.T) {
	projectID := projectgraph.ResourceID("project_1")
	identity, err := projectgraph.NewServingIdentity("project_1", "prod", "generation_typed_actions")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "dashboard_a", Kind: projectgraph.KindDashboard, Name: "dashboard_a"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dashboard, err := access.NewResourceRef("dashboard_a", projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	update, err := access.NewExactPermissionPair(access.ActionDashboardUpdate, projectID, dashboard)
	if err != nil {
		t.Fatal(err)
	}
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal_1"}
	grant, err := accesssnapshot.NewTypedGrant("dashboard-a-update", "dashboard A update", subject, []access.PermissionPair{update})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, []accesssnapshot.Grant{grant}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		action access.Action
		want   bool
	}{
		{name: "edit does not authorize publish", action: access.ActionDashboardPublish},
		{name: "edit does not authorize read", action: access.ActionDashboardRead},
		{name: "exact update allows edit", action: access.ActionDashboardUpdate, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			credential := access.APICredential{
				Principal: access.Principal{ID: subject.ID},
				Token:     access.APIToken{ID: "typed", PrincipalID: subject.ID, PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{update}},
			}
			ctx := accessmodule.WithAPICredential(context.Background(), credential)
			typed, allowed, decisionErr := typedPermissionDecisionForSnapshot(ctx, subject.ID, identity.ProjectID, []access.ResourceRef{dashboard}, func(access.ResourceRef) (access.Action, bool) {
				return test.action, true
			}, snapshot, []access.SubjectRef{subject})
			if decisionErr != nil || !typed || allowed != test.want {
				t.Fatalf("decision = typed:%t allowed:%t err:%v, want typed:true allowed:%t", typed, allowed, decisionErr, test.want)
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
		_, allowed, authorizeErr := authorizeProjectResourcesWithTypedAction(ctx, accessModule, runtime, principal.ID, identity.ProjectID, []access.ResourceRef{resources[index]}, requested.action)
		if authorizeErr != nil {
			t.Fatal(authorizeErr)
		}
		if allowed {
			t.Fatalf("read-only credential authorized %s", requested.action)
		}
	}
}

func TestTypedPermissionDecisionForSnapshotRejectsCredentialAttenuationMismatch(t *testing.T) {
	identity, err := projectgraph.NewServingIdentity("project_1", "prod", "generation_typed_attenuation")
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
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal_1"}
	grant, err := accesssnapshot.NewTypedGrant("dashboard-read", "dashboard read", subject, []access.PermissionPair{pair})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, []accesssnapshot.Grant{grant}, nil)
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
			ctx := accessmodule.WithAPICredential(context.Background(), test.credential)
			typed, allowed, decisionErr := typedPermissionDecisionForSnapshot(ctx, subject.ID, identity.ProjectID, []access.ResourceRef{resource}, func(access.ResourceRef) (access.Action, bool) {
				return access.ActionDashboardRead, true
			}, snapshot, []access.SubjectRef{subject})
			if decisionErr != nil || !typed || allowed {
				t.Fatalf("decision = typed:%t allowed:%t err:%v, want typed:true allowed:false", typed, allowed, decisionErr)
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
	update, err := access.NewExactPermissionPair(access.ActionDashboardUpdate, "project_demo", dashboard)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project_demo", "prod", "generation_authoring_guard")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "dashboard_a", Kind: projectgraph.KindDashboard, Name: "dashboard_a"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := accesssnapshot.NewTypedGrant("typed-dashboard-update", "typed dashboard update", access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "owner"}, []access.PermissionPair{update})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, []accesssnapshot.Grant{grant}, nil)
	if err != nil {
		t.Fatal(err)
	}
	authorizer := &repositoryDashboardAuthorizerFake{}
	guarded := protectProjectAuthoringResourceWithTypedAction(
		typedAuthoringAccess{
			tusAccess:  tusAccess{principal: accessmodule.Principal{ID: "owner"}, ok: true, subjects: []access.SubjectRef{{Kind: access.SubjectKindPrincipal, ID: "owner"}}},
			credential: access.APICredential{Principal: access.Principal{ID: "owner"}, Token: access.APIToken{ID: "typed", PrincipalID: "owner", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{read}}},
		},
		tusRuntime{project: "project_demo", lease: tusLease{identity: identity, snapshot: snapshot}}, authorizer, access.ActionDashboardUpdate,
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
