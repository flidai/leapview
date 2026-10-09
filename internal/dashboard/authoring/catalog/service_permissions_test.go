package catalog

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/accessadapter"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	dashboardcatalog "github.com/flidai/leapview/internal/dashboard/catalog"
	"github.com/flidai/leapview/internal/project/graph"
)

func TestCatalogActionsFollowTypedRolesAndExactGrants(t *testing.T) {
	// READ is granted separately so every row tests action projection for a
	// visible dashboard, including roles with no dashboard read authority.
	for _, test := range []struct {
		name               string
		role               access.PermissionRole
		legacy             access.ProjectRole
		actor              string
		update, delete     bool
		grantTarget        graph.ResourceID
		canEdit, canDelete bool
	}{
		{name: "project admin without dashboard grants", role: access.PermissionRoleProjectAdmin, actor: "owner"},
		{name: "admin with exact actions owns draft", role: access.PermissionRoleProjectAdmin, actor: "owner", update: true, delete: true, canEdit: true, canDelete: true},
		{name: "admin with exact actions other owner", role: access.PermissionRoleProjectAdmin, actor: "admin", update: true, delete: true, canEdit: true, canDelete: true},
		{name: "owner with exact actions", actor: "owner", update: true, delete: true, canEdit: true, canDelete: true},
		{name: "editor owns draft", role: access.PermissionRoleEditor, actor: "owner", canEdit: true},
		{name: "editor other owner", role: access.PermissionRoleEditor, actor: "reader", canEdit: true},
		{name: "viewer owns draft", role: access.PermissionRoleViewer, actor: "owner"},
		{name: "explorer owns draft", role: access.PermissionRoleExplorer, actor: "owner"},
		{name: "delete does not imply update", actor: "owner", delete: true, canDelete: true},
		{name: "unrelated exact actions", actor: "owner", update: true, delete: true, grantTarget: "other"},
		{name: "legacy admin is not typed authority", legacy: access.ProjectRoleAdmin, actor: "owner"},
		{name: "legacy owner is not typed authority", legacy: access.ProjectRoleOwner, actor: "owner"},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity, err := graph.NewServingIdentity("sales", "production", "state-1")
			if err != nil {
				t.Fatal(err)
			}
			project, err := graph.NewProjectGraph([]graph.Resource{
				{ID: "draft", Kind: graph.KindDashboard, Name: "draft"},
				{ID: "managed", Kind: graph.KindDashboard, Name: "managed"},
				{ID: "other", Kind: graph.KindDashboard, Name: "other"},
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: test.actor}
			var bindings []accesssnapshot.RoleBinding
			if test.role != "" {
				binding, err := access.NewTypedRoleBinding("role", "role", subject, test.role, identity.ProjectID)
				if err != nil {
					t.Fatal(err)
				}
				bindings = append(bindings, binding)
			}
			if test.legacy != "" {
				bindings = append(bindings, accesssnapshot.RoleBinding{ID: "legacy", Subject: subject, Role: test.legacy, Capabilities: access.ProjectRoleCapabilities(test.legacy)})
			}
			var pairs []access.PermissionPair
			add := func(action access.Action, id graph.ResourceID) {
				t.Helper()
				resource, err := access.NewResourceRef(id, graph.KindDashboard)
				if err != nil {
					t.Fatal(err)
				}
				pair, err := access.NewExactPermissionPair(action, identity.ProjectID, resource)
				if err != nil {
					t.Fatal(err)
				}
				pairs = append(pairs, pair)
			}
			add(access.ActionDashboardRead, "draft")
			add(access.ActionDashboardRead, "managed")
			target := test.grantTarget
			if target == "" {
				target = "draft"
			}
			if test.update {
				add(access.ActionDashboardUpdate, target)
			}
			if test.delete {
				add(access.ActionDashboardDelete, target)
			}
			grant, err := accesssnapshot.NewTypedGrant("actions", "actions", subject, pairs)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, project, bindings, []accesssnapshot.Grant{grant}, nil)
			if err != nil {
				t.Fatal(err)
			}
			auth, err := accessadapter.New(accessadapter.Options{
				AuthorizeTypedResource: func(_ context.Context, actor string, projectID graph.ResourceID, resource access.ResourceRef, action access.Action) (bool, bool, error) {
					if actor != test.actor || projectID != identity.ProjectID {
						t.Fatalf("wrong authorization identity: %s/%s", actor, projectID)
					}
					pair, err := access.NewExactPermissionPair(action, projectID, resource)
					if err != nil {
						return true, false, err
					}
					allowed, err := snapshot.AllowsTyped(subject, pair)
					return true, allowed, err
				},
				AuthorizeTypedProject: func(context.Context, string, graph.ResourceID, access.Action) (bool, bool, error) {
					t.Fatal("existing dashboard actions must use resource authority")
					return true, false, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			life := catalogLifecycle("draft", authoring.LifecycleStatusDraft, "revision")
			repo := &catalogRepository{lifecycles: []authoring.DashboardLifecycle{life}, revisions: map[string]authoring.Revision{"draft": catalogRevision("draft", "revision", "Sample")}}
			svc := newCatalogService(t, repo, auth, []dashboardcatalog.Dashboard{{ID: "managed", Title: "Managed", SemanticModel: "sales"}})
			for _, includeEditable := range []bool{false, true} {
				listed, err := svc.List(t.Context(), ListRequest{ProjectID: "sales", ActorID: test.actor, IncludeEditableDrafts: includeEditable})
				if err != nil {
					t.Fatal(err)
				}
				wantCount := 2
				if includeEditable && !test.canEdit {
					wantCount = 1
				}
				if listed.Count != wantCount {
					t.Fatalf("include editable %t: visible dashboards = %#v", includeEditable, listed)
				}
				for _, item := range listed.Items {
					if item.Source == SourceProject {
						if item.CanEdit || item.CanDelete {
							t.Fatalf("managed dashboard actions = %#v", item)
						}
					} else if item.CanEdit != test.canEdit || item.CanDelete != test.canDelete {
						t.Fatalf("include editable %t: instance actions edit/delete = %t/%t, want %t/%t", includeEditable, item.CanEdit, item.CanDelete, test.canEdit, test.canDelete)
					}
				}
			}
			got, err := svc.Get(t.Context(), GetRequest{ProjectID: "sales", ActorID: test.actor, DashboardID: "draft"})
			if err != nil || got.CanEdit != test.canEdit || got.CanDelete != test.canDelete {
				t.Fatalf("Get actions = %#v, %v", got, err)
			}
		})
	}
}

func TestCatalogDeleteActionsRequireUnpublishedPrivateDraft(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     authoring.LifecycleStatus
		visibility authoring.Visibility
		published  bool
		canDelete  bool
	}{
		{"private draft", authoring.LifecycleStatusDraft, authoring.VisibilityPrivate, false, true},
		{"organization draft", authoring.LifecycleStatusDraft, authoring.VisibilityOrganization, false, false},
		{"restricted draft", authoring.LifecycleStatusDraft, authoring.VisibilityRestricted, false, false},
		{"published with changes", authoring.LifecycleStatusPublished, authoring.VisibilityPrivate, true, false},
		{"draft with publication", authoring.LifecycleStatusDraft, authoring.VisibilityPrivate, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			life := catalogLifecycle("draft", test.status, "revision")
			life.Visibility = test.visibility
			if test.published {
				life.Published = &authoring.Published{Revision: catalogToken("revision", 1)}
			}
			repo := &catalogRepository{lifecycles: []authoring.DashboardLifecycle{life}, revisions: map[string]authoring.Revision{"draft": catalogRevision("draft", "revision", "Sample")}}
			got, err := newCatalogService(t, repo, &catalogAuthorizer{}, nil).Get(t.Context(), GetRequest{ProjectID: "sales", ActorID: "owner", DashboardID: "draft"})
			if err != nil || !got.CanEdit || got.CanDelete != test.canDelete {
				t.Fatalf("actions = %#v, %v", got, err)
			}
		})
	}
}

func TestCatalogPlatformAdministrationDoesNotGrantProjectActions(t *testing.T) {
	// A platform role is not a project role bundle, even when the actor owns
	// this dashboard. The canonical adapter must still require project access.
	auth, err := accessadapter.New(accessadapter.Options{
		AuthorizeTypedResource: func(context.Context, string, graph.ResourceID, access.ResourceRef, access.Action) (bool, bool, error) {
			return true, false, nil
		},
		AuthorizeTypedProject: func(context.Context, string, graph.ResourceID, access.Action) (bool, bool, error) {
			return true, false, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := &catalogRepository{lifecycles: []authoring.DashboardLifecycle{catalogLifecycle("draft", authoring.LifecycleStatusDraft, "revision")}}
	svc := newCatalogService(t, repo, auth, nil)
	listed, err := svc.List(t.Context(), ListRequest{ProjectID: "sales", ActorID: "owner"})
	if err != nil || listed.Count != 0 {
		t.Fatalf("unbound project list = %#v, %v", listed, err)
	}
	if _, err := svc.Get(t.Context(), GetRequest{ProjectID: "sales", ActorID: "owner", DashboardID: "draft"}); !errors.Is(err, ErrNotFound) || len(repo.revisionReads) != 0 {
		t.Fatalf("unbound project get = %v, revision reads=%v", err, repo.revisionReads)
	}
}

func TestCatalogActionAuthorizationPreservesErrorsAndRequestContext(t *testing.T) {
	for _, action := range []authoring.AuthorizationAction{authoring.AuthorizationActionEdit, authoring.AuthorizationActionDelete} {
		for _, denied := range []bool{true, false} {
			t.Run(string(action)+map[bool]string{true: "/forbidden", false: "/unavailable"}[denied], func(t *testing.T) {
				want := errors.New("authorization unavailable")
				if denied {
					want = access.ErrForbidden
				}
				auth := catalogActionAuthorizer(func(_ context.Context, request authoringservice.AuthorizationRequest) error {
					if request.ActorID != "actor" || request.ProjectID != "sales" || request.DashboardID != "draft" || request.OwnerPrincipalID != "owner" || request.SemanticModel != "sales" || request.Visibility != authoring.VisibilityPrivate || request.Target != authoringservice.AuthorizationTargetAuthoredDashboard {
						t.Fatalf("authorization context = %#v", request)
					}
					if request.Action == action {
						return want
					}
					return nil
				})
				repo := &catalogRepository{lifecycles: []authoring.DashboardLifecycle{catalogLifecycle("draft", authoring.LifecycleStatusDraft, "revision")}, revisions: map[string]authoring.Revision{"draft": catalogRevision("draft", "revision", "Sample")}}
				provider := &catalogProvider{runtime: catalogRuntime{}}
				svc := newCatalogServiceWithProvider(t, repo, auth, provider)
				got, err := svc.Get(t.Context(), GetRequest{ProjectID: "sales", ActorID: "actor", DashboardID: "draft"})
				if denied {
					if err != nil || (action == authoring.AuthorizationActionEdit && got.CanEdit) || (action == authoring.AuthorizationActionDelete && got.CanDelete) {
						t.Fatalf("denied actions = %#v, %v", got, err)
					}
				} else if !errors.Is(err, want) || len(repo.revisionReads) != 0 {
					t.Fatalf("authorization failure = %v, revision reads=%v", err, repo.revisionReads)
				}
				if provider.lease.releases != 1 {
					t.Fatalf("lease releases = %d", provider.lease.releases)
				}
			})
		}
	}
}

type catalogActionAuthorizer func(context.Context, authoringservice.AuthorizationRequest) error

func (authorize catalogActionAuthorizer) Authorize(ctx context.Context, request authoringservice.AuthorizationRequest) error {
	return authorize(ctx, request)
}
