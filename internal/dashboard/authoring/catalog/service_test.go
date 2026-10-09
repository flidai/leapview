package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/accessadapter"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	dashboardcatalog "github.com/flidai/leapview/internal/dashboard/catalog"
	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
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

func TestListAuthorizesBeforeRevisionDisclosureAndExcludesArchived(t *testing.T) {
	good := catalogLifecycle("good", authoring.LifecycleStatusDraft, "rev-good")
	denied := catalogLifecycle("denied", authoring.LifecycleStatusDraft, "rev-denied")
	archived := catalogLifecycle("archived", authoring.LifecycleStatusArchived, "rev-archived")
	repo := &catalogRepository{lifecycles: []authoring.DashboardLifecycle{good, denied, archived}, revisions: map[string]authoring.Revision{
		"good":   catalogRevision("good", "rev-good", "Good description"),
		"denied": {DashboardID: "denied"},
	}}
	auth := &catalogAuthorizer{deny: map[string]bool{"denied": true}}
	svc := newCatalogService(t, repo, auth, nil)
	result, err := svc.List(t.Context(), ListRequest{ProjectID: "sales", ActorID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 || result.Items[0].ID.String() != "good" || len(repo.revisionReads) != 1 || repo.revisionReads[0] != "good" {
		t.Fatalf("result/revision reads = %#v/%#v", result, repo.revisionReads)
	}
	if result.Items[0].FirstPageID != "overview" {
		t.Fatalf("first page = %q, want overview", result.Items[0].FirstPageID)
	}
	if len(auth.requests) != 4 || !auth.requested("good") || !auth.requested("denied") {
		t.Fatalf("authorization requests = %#v", auth.requests)
	}
	for _, request := range auth.requests {
		if request.DashboardID == "denied" && request.Action != authoring.AuthorizationActionView {
			t.Fatalf("hidden dashboard action authorization = %#v", request)
		}
	}
}

func TestListOrdersSourcesAndRejectsAuthorizedCollision(t *testing.T) {
	instance := catalogLifecycle("same", authoring.LifecycleStatusDraft, "rev-same")
	repo := &catalogRepository{lifecycles: []authoring.DashboardLifecycle{instance}, revisions: map[string]authoring.Revision{"same": catalogRevision("same", "rev-same", "instance")}}
	projects := []dashboardcatalog.Dashboard{{ID: "same", Title: "Project", SemanticModel: "sales"}}
	svc := newCatalogService(t, repo, &catalogAuthorizer{}, projects)
	if _, err := svc.List(t.Context(), ListRequest{ProjectID: "sales", ActorID: "actor"}); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("collision error = %v", err)
	}
	denied := newCatalogService(t, repo, &catalogAuthorizer{deny: map[string]bool{"same": true}}, projects)
	result, err := denied.List(t.Context(), ListRequest{ProjectID: "sales", ActorID: "actor"})
	if err != nil || result.Count != 0 {
		t.Fatalf("denied collision result=%#v err=%v", result, err)
	}
}

func TestGetHidesUnauthorizedAndArchived(t *testing.T) {
	life := catalogLifecycle("private", authoring.LifecycleStatusDraft, "rev-private")
	repo := &catalogRepository{lifecycles: []authoring.DashboardLifecycle{life}, revisions: map[string]authoring.Revision{"private": catalogRevision("private", "rev-private", "secret")}}
	denied := newCatalogService(t, repo, &catalogAuthorizer{deny: map[string]bool{"private": true}}, nil)
	if _, err := denied.Get(t.Context(), GetRequest{ProjectID: "sales", ActorID: "actor", DashboardID: "private"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unauthorized error = %v", err)
	}
	repo.lifecycles[0] = catalogLifecycle("private", authoring.LifecycleStatusArchived, "rev-private")
	auth := &catalogAuthorizer{}
	archived := newCatalogService(t, repo, auth, nil)
	if _, err := archived.Get(t.Context(), GetRequest{ProjectID: "sales", ActorID: "actor", DashboardID: "private"}); !errors.Is(err, ErrNotFound) || len(auth.requests) != 0 {
		t.Fatalf("archived error=%v auth=%#v", err, auth.requests)
	}
}

func TestProjectDashboardSourceUsesCanonicalIDAndAuthoredNameSeparately(t *testing.T) {
	runtime := catalogSourceRuntime{source: authoring.AuthoredDashboardSource{
		Document: document.DashboardDocument{
			Metadata: document.DashboardMetadata{ID: "dashboard:executive-sales", Name: "executive-sales"},
			Spec:     document.DashboardSpec{SemanticModel: "semantic-model:sales"},
		},
		Metadata: authoring.AuthoredDashboardMetadata{Project: "project:sales", Name: "executive-sales"},
	}}
	item := Dashboard{ID: "dashboard:executive-sales", ProjectID: "project:sales", SemanticModel: "semantic-model:sales", Source: SourceProject}
	if err := enrichProjectItem(runtime, &item); err != nil {
		t.Fatalf("canonical id with symbolic authored name was rejected: %v", err)
	}
	runtime.source.Metadata.Name = "different-name"
	if err := enrichProjectItem(runtime, &item); err == nil {
		t.Fatal("mismatched retained authored name was accepted")
	}
}

func TestBackendErrorsReleaseExactlyOnce(t *testing.T) {
	want := errors.New("repository unavailable")
	repo := &catalogRepository{listErr: want}
	provider := &catalogProvider{runtime: catalogRuntime{catalog: dashboardcatalog.Catalog{Project: dashboardcatalog.Project{ID: "sales"}}}}
	svc := newCatalogServiceWithProvider(t, repo, &catalogAuthorizer{}, provider)
	if _, err := svc.List(t.Context(), ListRequest{ProjectID: "sales", ActorID: "actor"}); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	if provider.acquires != 1 || provider.lease.releases != 1 {
		t.Fatalf("lease acquire/release = %d/%d", provider.acquires, provider.lease.releases)
	}
}

func newCatalogService(t *testing.T, repo *catalogRepository, auth authoringservice.Authorizer, dashboards []dashboardcatalog.Dashboard) *Service {
	return newCatalogServiceWithProvider(t, repo, auth, &catalogProvider{runtime: catalogRuntime{catalog: dashboardcatalog.Catalog{Project: dashboardcatalog.Project{ID: "sales"}, Dashboards: dashboards}}})
}

func newCatalogServiceWithProvider(t *testing.T, repo *catalogRepository, auth authoringservice.Authorizer, provider *catalogProvider) *Service {
	t.Helper()
	svc, err := NewService(Options{Provider: provider, Repository: repo, Authorizer: auth})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

type catalogRuntime struct{ catalog dashboardcatalog.Catalog }

func (r catalogRuntime) Close() error                      { return nil }
func (r catalogRuntime) Catalog() dashboardcatalog.Catalog { return r.catalog }

type catalogSourceRuntime struct {
	catalogRuntime
	source authoring.AuthoredDashboardSource
}

func (r catalogSourceRuntime) AuthoredDashboardSource(id string) (authoring.AuthoredDashboardSource, bool) {
	return r.source, r.source.Document.Metadata.ID == id
}

type catalogProvider struct {
	runtime  projectruntime.Runtime
	acquires int
	lease    *catalogLease
}

func (p *catalogProvider) Acquire(context.Context) (projectruntime.Lease, error) {
	p.acquires++
	identity, _ := graph.NewServingIdentity("sales", "production", "state-1")
	p.lease = &catalogLease{runtime: p.runtime, identity: identity}
	return p.lease, nil
}

type catalogLease struct {
	runtime  projectruntime.Runtime
	identity graph.ServingIdentity
	releases int
}

func (l *catalogLease) Runtime() projectruntime.Runtime { return l.runtime }
func (l *catalogLease) Identity() graph.ServingIdentity { return l.identity }
func (l *catalogLease) Release()                        { l.releases++ }

type catalogAuthorizer struct {
	deny     map[string]bool
	denyView bool
	requests []authoringservice.AuthorizationRequest
}

func (a *catalogAuthorizer) Authorize(_ context.Context, request authoringservice.AuthorizationRequest) error {
	a.requests = append(a.requests, request)
	if a.deny[request.DashboardID.String()] || (a.denyView && request.Action == authoring.AuthorizationActionView) {
		return access.ErrForbidden
	}
	return nil
}
func (a *catalogAuthorizer) requested(id string) bool {
	for _, request := range a.requests {
		if request.DashboardID.String() == id {
			return true
		}
	}
	return false
}

type catalogRepository struct {
	lifecycles    []authoring.DashboardLifecycle
	revisions     map[string]authoring.Revision
	revisionReads []string
	listErr       error
}

func (r *catalogRepository) Create(context.Context, authoring.CreateInput) (authoring.DashboardLifecycle, error) {
	panic("unused")
}
func (r *catalogRepository) Get(_ context.Context, _ graph.ResourceID, id authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	for _, value := range r.lifecycles {
		if value.ID == id {
			return value, nil
		}
	}
	return authoring.DashboardLifecycle{}, authoring.ErrNotFound
}
func (r *catalogRepository) List(_ context.Context, _ graph.ResourceID) ([]authoring.DashboardLifecycle, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return append([]authoring.DashboardLifecycle(nil), r.lifecycles...), nil
}
func (r *catalogRepository) CountBySemanticModel(context.Context, graph.ResourceID) ([]authoring.SemanticModelUsage, error) {
	panic("unused")
}
func (r *catalogRepository) GetRevision(_ context.Context, _ graph.ResourceID, id authoring.DashboardID, revisionID authoring.RevisionID) (authoring.Revision, error) {
	r.revisionReads = append(r.revisionReads, id.String())
	value, ok := r.revisions[id.String()]
	if !ok || value.ID != revisionID {
		return authoring.Revision{}, authoring.ErrNotFound
	}
	return value, nil
}
func (r *catalogRepository) LookupCommandResult(context.Context, graph.ResourceID, authoring.DashboardID, authoring.CommandEvidence) (authoring.CommandResult, bool, error) {
	panic("unused")
}
func (r *catalogRepository) LookupCreateOperation(context.Context, authoring.CreateOperation) (authoring.CreateOperationResult, bool, error) {
	panic("unused")
}
func (r *catalogRepository) AppendDraft(context.Context, authoring.AppendDraftInput) (authoring.Revision, error) {
	panic("unused")
}
func (r *catalogRepository) Publish(context.Context, authoring.PublishInput) (authoring.DashboardLifecycle, error) {
	panic("unused")
}
func (r *catalogRepository) Archive(context.Context, authoring.ArchiveInput) (authoring.DashboardLifecycle, error) {
	panic("unused")
}
func (r *catalogRepository) GetPublishedCompilation(context.Context, graph.ResourceID, authoring.DashboardID) (authoring.CompiledRevision, error) {
	panic("unused")
}

func catalogLifecycle(id string, status authoring.LifecycleStatus, revisionID string) authoring.DashboardLifecycle {
	return authoring.DashboardLifecycle{ProjectID: "sales", ID: authoring.DashboardID(id), OwnerPrincipalID: "owner", Slug: id, Title: id, SemanticModel: "sales", Visibility: authoring.VisibilityPrivate, Status: status, Draft: &authoring.Draft{DashboardID: authoring.DashboardID(id), Revision: catalogToken(revisionID, 1), Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}}}
}
func catalogToken(id string, number uint64) authoring.RevisionToken {
	return authoring.RevisionToken{RevisionID: authoring.RevisionID(id), Number: number, ContentHash: "sha256:" + strings.Repeat("b", 64)}
}
func catalogRevision(id, revisionID, description string) authoring.Revision {
	descriptionPtr := description
	return authoring.Revision{ID: authoring.RevisionID(revisionID), DashboardID: authoring.DashboardID(id), Number: 1, ContentHash: "sha256:" + strings.Repeat("b", 64), Document: document.DashboardDocument{Metadata: document.DashboardMetadata{ID: id, Name: id, Description: &descriptionPtr}, Spec: document.DashboardSpec{Pages: []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{}}}}}}
}

var _ projectruntime.Provider = (*catalogProvider)(nil)
var _ authoring.Repository = (*catalogRepository)(nil)

func TestDiscoveryIncludesOnlyEditAuthorizedDrafts(t *testing.T) {
	repo := &catalogRepository{lifecycles: []authoring.DashboardLifecycle{
		catalogLifecycle("mine", authoring.LifecycleStatusDraft, "rev-mine"),
		catalogLifecycle("denied", authoring.LifecycleStatusDraft, "rev-denied"),
		catalogLifecycle("archived", authoring.LifecycleStatusArchived, "rev-archived"),
	}, revisions: map[string]authoring.Revision{"mine": catalogRevision("mine", "rev-mine", "Saved chat visual")}}
	auth := &catalogAuthorizer{denyView: true, deny: map[string]bool{"denied": true}}
	svc := newCatalogService(t, repo, auth, []dashboardcatalog.Dashboard{{ID: "managed", Title: "Managed", SemanticModel: "sales"}})
	result, err := svc.List(t.Context(), ListRequest{ProjectID: "sales", ActorID: "actor", IncludeEditableDrafts: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 || result.InstanceCount != 1 || result.Items[0].ID != "mine" || result.Items[0].FirstPageID != "overview" {
		t.Fatalf("discovery = %#v", result)
	}
	if len(repo.revisionReads) != 1 || repo.revisionReads[0] != "mine" {
		t.Fatalf("revision reads = %v", repo.revisionReads)
	}
	if !result.Items[0].CanEdit || !result.Items[0].CanDelete {
		t.Fatalf("editable draft actions = %#v", result.Items[0])
	}
	wantActions := map[string][]authoring.AuthorizationAction{
		"mine":    {authoring.AuthorizationActionEdit, authoring.AuthorizationActionDelete},
		"denied":  {authoring.AuthorizationActionEdit},
		"managed": {authoring.AuthorizationActionView},
	}
	for _, request := range auth.requests {
		expected := wantActions[request.DashboardID.String()]
		if len(expected) == 0 || request.Action != expected[0] {
			t.Fatalf("unexpected discovery authorization: %#v", request)
		}
		wantActions[request.DashboardID.String()] = expected[1:]
	}
	for id, remaining := range wantActions {
		if len(remaining) != 0 {
			t.Fatalf("missing discovery authorizations for %s: %v", id, remaining)
		}
	}
	auth.requests = nil
	result, err = svc.List(t.Context(), ListRequest{ProjectID: "sales", ActorID: "actor"})
	if err != nil || result.Count != 0 {
		t.Fatalf("VIEW-only list = %#v, %v", result, err)
	}
	for _, request := range auth.requests {
		if request.Action != authoring.AuthorizationActionView {
			t.Fatalf("ordinary list must use VIEW: %#v", request)
		}
	}
	auth.requests = nil
	if _, err := svc.Get(t.Context(), GetRequest{ProjectID: "sales", ActorID: "actor", DashboardID: "mine"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("VIEW-only get = %v", err)
	}
	if len(auth.requests) != 1 || auth.requests[0].Action != authoring.AuthorizationActionView {
		t.Fatalf("ordinary Get authorization = %#v", auth.requests)
	}
	if len(repo.revisionReads) != 1 {
		t.Fatalf("hidden drafts triggered revision reads: %v", repo.revisionReads)
	}
}
