package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	dashboardcatalog "github.com/flidai/leapview/internal/dashboard/catalog"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func TestListFiltersBeforeReadingUnauthorizedRevisionAndExcludesArchived(t *testing.T) {
	good := lifecycle("good", authoring.LifecycleStatusDraft, "rev-good")
	denied := lifecycle("denied", authoring.LifecycleStatusDraft, "rev-denied")
	archived := lifecycle("archived", authoring.LifecycleStatusArchived, "rev-archived")
	repo := &fakeRepository{lifecycles: []authoring.DashboardLifecycle{good, denied, archived}, revisions: map[string]authoring.Revision{
		"good":   revision("good", "rev-good", "Good description"),
		"denied": {DashboardID: "denied"}, // must never be read
	}}
	auth := &fakeAuthorizer{deny: map[string]bool{"denied": true}}
	service := newTestService(t, repo, auth, nil)
	result, err := service.List(t.Context(), ListRequest{ProjectID: "sales", ActorID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 || result.ProjectCount != 0 || result.InstanceCount != 1 || len(result.Items) != 1 || result.Items[0].ID.String() != "good" {
		t.Fatalf("result = %#v", result)
	}
	if len(repo.revisionReads) != 1 || repo.revisionReads[0] != "good" {
		t.Fatalf("revision reads = %#v", repo.revisionReads)
	}
	if len(auth.requests) != 2 || !auth.requested("denied") || !auth.requested("good") {
		t.Fatalf("authorization requests = %#v", auth.requests)
	}
}

func TestListMergesSourcesOrdersAndReturnsMetadata(t *testing.T) {
	runtimeDashboard := dashboardcatalog.Dashboard{ID: "z-project", Title: "Zulu", Description: "Project", SemanticModel: "sales", Tags: []string{"z"}}
	instance := lifecycle("a-project", authoring.LifecycleStatusPublished, "rev-draft")
	instance.Draft = &authoring.Draft{DashboardID: "a-project", Revision: token("rev-draft", 2), Provenance: provenance(authoring.OriginAgent)}
	instance.Published = &authoring.Published{
		Revision: token("rev-published", 1), PublishedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		Compilation: authoring.CompiledRevisionToken{AuthoredRevision: token("rev-published", 1), DefinitionHash: "sha256:" + strings.Repeat("a", 64), SemanticServingStateID: "semantic-1"},
		Provenance:  provenance(authoring.OriginFile),
	}
	repo := &fakeRepository{lifecycles: []authoring.DashboardLifecycle{instance}, revisions: map[string]authoring.Revision{
		"a-project": revisionWithToken("a-project", token("rev-draft", 2), "Draft description"),
	}}
	service := newTestService(t, repo, &fakeAuthorizer{}, []dashboardcatalog.Dashboard{runtimeDashboard})
	result, err := service.List(t.Context(), ListRequest{ProjectID: "sales", ActorID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{result.Items[0].ID.String(), result.Items[1].ID.String()}; got[0] != "a-project" || got[1] != "z-project" {
		t.Fatalf("ordering = %#v", got)
	}
	item := result.Items[0]
	if item.Source != SourceInstance || item.Origin != authoring.OriginAgent || item.Status != authoring.LifecycleStatusPublished || item.Description != "Draft description" || item.Revision == nil || item.Revision.ID != "rev-draft" || item.Publication == nil || item.Publication.Revision.ID != "rev-published" || item.Publication.SemanticServingStateID != "semantic-1" {
		t.Fatalf("project metadata = %#v rev=%#v pub=%#v", item, item.Revision, item.Publication)
	}
	if result.ProjectCount != 1 || result.InstanceCount != 1 || result.Count != 2 {
		t.Fatalf("counts = %#v", result)
	}
	if result.Items[1].Source != SourceProject || result.Items[1].Origin != authoring.OriginFile || result.Items[1].Status != authoring.LifecycleStatusPublished || result.Items[1].Visibility != authoring.VisibilityOrganization {
		t.Fatalf("project metadata = %#v", result.Items[1])
	}
}

func TestListCollisionOnlyAfterBothSourcesAreAuthorized(t *testing.T) {
	project := lifecycle("same", authoring.LifecycleStatusDraft, "rev-same")
	repo := &fakeRepository{lifecycles: []authoring.DashboardLifecycle{project}, revisions: map[string]authoring.Revision{"same": revision("same", "rev-same", "project")}}
	service := newTestService(t, repo, &fakeAuthorizer{}, []dashboardcatalog.Dashboard{{ID: "same", Title: "Project", SemanticModel: "sales"}})
	if _, err := service.List(t.Context(), ListRequest{ProjectID: "sales", ActorID: "actor"}); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("error = %v, want collision", err)
	}

	denied := newTestService(t, repo, &fakeAuthorizer{deny: map[string]bool{"same": true}}, []dashboardcatalog.Dashboard{{ID: "same", Title: "Project", SemanticModel: "sales"}})
	result, err := denied.List(t.Context(), ListRequest{ProjectID: "sales", ActorID: "actor"})
	if err != nil || result.Count != 0 {
		t.Fatalf("denied collision result=%#v err=%v", result, err)
	}
}

func TestGetHidesUnauthorizedAndArchivedDashboards(t *testing.T) {
	life := lifecycle("private", authoring.LifecycleStatusDraft, "rev-private")
	repo := &fakeRepository{lifecycles: []authoring.DashboardLifecycle{life}, revisions: map[string]authoring.Revision{"private": revision("private", "rev-private", "secret")}}
	service := newTestService(t, repo, &fakeAuthorizer{deny: map[string]bool{"private": true}}, nil)
	if _, err := service.Get(t.Context(), GetRequest{ProjectID: "sales", ActorID: "actor", DashboardID: "private"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want not found", err)
	}

	archived := lifecycle("private", authoring.LifecycleStatusArchived, "rev-private")
	repo.lifecycles = []authoring.DashboardLifecycle{archived}
	auth := &fakeAuthorizer{}
	service = newTestService(t, repo, auth, nil)
	if _, err := service.Get(t.Context(), GetRequest{ProjectID: "sales", ActorID: "actor", DashboardID: "private"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("archived error = %v, want not found", err)
	}
	if len(auth.requests) != 0 {
		t.Fatalf("archived dashboard was authorized: %#v", auth.requests)
	}
}

func TestBackendErrorsPropagateAndLeaseIsSingle(t *testing.T) {
	want := errors.New("repository unavailable")
	repo := &fakeRepository{listErr: want}
	provider := &fakeProvider{runtime: fakeRuntime{catalog: dashboardcatalog.Catalog{Workspace: dashboardcatalog.Workspace{ID: "sales"}}}}
	service := newTestServiceWithProvider(t, repo, &fakeAuthorizer{}, provider)
	if _, err := service.List(t.Context(), ListRequest{ProjectID: "sales", ActorID: "actor"}); !errors.Is(err, want) {
		t.Fatalf("error = %v, want backend error", err)
	}
	if provider.acquires != 1 || provider.lease.releases != 1 {
		t.Fatalf("lease counts acquire=%d release=%d", provider.acquires, provider.lease.releases)
	}
}

func newTestService(t *testing.T, repo *fakeRepository, auth *fakeAuthorizer, projects []dashboardcatalog.Dashboard) *Service {
	return newTestServiceWithProvider(t, repo, auth, &fakeProvider{runtime: fakeRuntime{catalog: dashboardcatalog.Catalog{Workspace: dashboardcatalog.Workspace{ID: "sales"}, Dashboards: projects}}})
}

func newTestServiceWithProvider(t *testing.T, repo *fakeRepository, auth *fakeAuthorizer, provider *fakeProvider) *Service {
	t.Helper()
	service, err := NewService(Options{Provider: provider, Repository: repo, Authorizer: auth})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type fakeRuntime struct{ catalog dashboardcatalog.Catalog }

func (fakeRuntime) Close() error                        { return nil }
func (r fakeRuntime) Catalog() dashboardcatalog.Catalog { return r.catalog }

type fakeProvider struct {
	runtime  runtimehost.Runtime
	acquires int
	lease    *fakeLease
}

func (p *fakeProvider) Acquire(context.Context) (runtimehost.Lease, error) {
	p.acquires++
	p.lease = &fakeLease{runtime: p.runtime}
	return p.lease, nil
}

type fakeLease struct {
	runtime  runtimehost.Runtime
	releases int
}

func (l *fakeLease) Runtime() runtimehost.Runtime    { return l.runtime }
func (l *fakeLease) ServingStateID() servingstate.ID { return "state-1" }
func (l *fakeLease) DuckLakeSnapshotID() int64       { return 0 }
func (l *fakeLease) Release()                        { l.releases++ }

type fakeAuthorizer struct {
	deny     map[string]bool
	requests []authoringservice.AuthorizationRequest
}

func (a *fakeAuthorizer) Authorize(_ context.Context, request authoringservice.AuthorizationRequest) error {
	a.requests = append(a.requests, request)
	if a.deny[request.DashboardID.String()] {
		return access.ErrForbidden
	}
	return nil
}

func (a *fakeAuthorizer) requested(id string) bool {
	for _, request := range a.requests {
		if request.DashboardID.String() == id {
			return true
		}
	}
	return false
}

type fakeRepository struct {
	lifecycles    []authoring.DashboardLifecycle
	revisions     map[string]authoring.Revision
	revisionReads []string
	listErr       error
}

func (r *fakeRepository) Create(context.Context, authoring.CreateInput) (authoring.DashboardLifecycle, error) {
	panic("unused")
}
func (r *fakeRepository) Get(_ context.Context, _ graph.ResourceID, id authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	for _, lifecycle := range r.lifecycles {
		if lifecycle.ID == id {
			return lifecycle, nil
		}
	}
	return authoring.DashboardLifecycle{}, authoring.ErrNotFound
}
func (r *fakeRepository) List(context.Context, graph.ResourceID) ([]authoring.DashboardLifecycle, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return append([]authoring.DashboardLifecycle(nil), r.lifecycles...), nil
}
func (r *fakeRepository) CountBySemanticModel(context.Context, graph.ResourceID) ([]authoring.SemanticModelUsage, error) {
	panic("unused")
}
func (r *fakeRepository) GetRevision(_ context.Context, _ graph.ResourceID, id authoring.DashboardID, revisionID authoring.RevisionID) (authoring.Revision, error) {
	r.revisionReads = append(r.revisionReads, id.String())
	revision, ok := r.revisions[id.String()]
	if !ok || revision.ID != revisionID {
		return authoring.Revision{}, authoring.ErrNotFound
	}
	return revision, nil
}
func (r *fakeRepository) LookupCommandResult(context.Context, graph.ResourceID, authoring.DashboardID, authoring.CommandEvidence) (authoring.CommandResult, bool, error) {
	panic("unused")
}
func (r *fakeRepository) AppendDraft(context.Context, authoring.AppendDraftInput) (authoring.Revision, error) {
	panic("unused")
}
func (r *fakeRepository) Publish(context.Context, authoring.PublishInput) (authoring.DashboardLifecycle, error) {
	panic("unused")
}
func (r *fakeRepository) Archive(context.Context, authoring.ArchiveInput) (authoring.DashboardLifecycle, error) {
	panic("unused")
}
func (r *fakeRepository) GetPublishedCompilation(context.Context, graph.ResourceID, authoring.DashboardID) (authoring.CompiledRevision, error) {
	panic("unused")
}

func lifecycle(id string, status authoring.LifecycleStatus, revisionID string) authoring.DashboardLifecycle {
	return authoring.DashboardLifecycle{ProjectID: graph.ResourceID("sales"), ID: authoring.DashboardID(id), OwnerPrincipalID: "owner", Slug: id, Title: id, SemanticModel: graph.ResourceID("sales"), Visibility: authoring.VisibilityPrivate, Status: status, Draft: &authoring.Draft{DashboardID: authoring.DashboardID(id), Revision: token(revisionID, 1), Provenance: provenance(authoring.OriginUI)}}
}

func revision(id, revisionID, description string) authoring.Revision {
	return revisionWithToken(id, token(revisionID, 1), description)
}

func revisionWithToken(id string, revisionToken authoring.RevisionToken, description string) authoring.Revision {
	return authoring.Revision{ID: revisionToken.RevisionID, DashboardID: authoring.DashboardID(id), Number: revisionToken.Number, ContentHash: revisionToken.ContentHash, Document: authoring.Dashboard{ID: graph.ResourceID(id), Description: description}}
}

func token(id string, number uint64) authoring.RevisionToken {
	return authoring.RevisionToken{RevisionID: authoring.RevisionID(id), Number: number, ContentHash: "sha256:" + strings.Repeat("b", 64)}
}

func provenance(origin authoring.Origin) authoring.Provenance {
	return authoring.Provenance{Origin: origin, ActorID: "actor"}
}
