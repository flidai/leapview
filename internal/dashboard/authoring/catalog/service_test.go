package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	dashboardcatalog "github.com/flidai/leapview/internal/dashboard/catalog"
	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

func TestListAuthorizesBeforeRevisionDisclosureAndExcludesArchived(t *testing.T) {
	good := catalogLifecycle("good", authoring.LifecycleStatusDraft, "rev-good")
	denied := catalogLifecycle("denied", authoring.LifecycleStatusDraft, "rev-denied")
	archived := catalogLifecycle("archived", authoring.LifecycleStatusArchived, "rev-archived")
	repo := &catalogRepository{lifecycles: []authoring.DashboardLifecycle{good, denied, archived}, revisions: map[string]authoring.Revision{
		"good":   catalogRevision("good", "rev-good", "Good description"),
		"denied": {DashboardID: "denied"},
	}}
	featured := true
	goodRevision := repo.revisions["good"]
	goodRevision.Document.Metadata.Featured = &featured
	repo.revisions["good"] = goodRevision
	auth := &catalogAuthorizer{deny: map[string]bool{"denied": true}}
	svc := newCatalogService(t, repo, auth, nil)
	result, err := svc.List(t.Context(), ListRequest{ProjectID: "sales", ActorID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 || result.Items[0].ID.String() != "good" || len(repo.revisionReads) != 1 || repo.revisionReads[0] != "good" {
		t.Fatalf("result/revision reads = %#v/%#v", result, repo.revisionReads)
	}
	if !result.Items[0].Featured {
		t.Fatal("instance featured metadata was not projected from the current revision")
	}
	if result.Items[0].FirstPageID != "overview" {
		t.Fatalf("first page = %q, want overview", result.Items[0].FirstPageID)
	}
	if len(auth.requests) != 2 || !auth.requested("good") || !auth.requested("denied") {
		t.Fatalf("authorization requests = %#v", auth.requests)
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
	featured := true
	runtime := catalogSourceRuntime{source: authoring.AuthoredDashboardSource{
		Document: document.DashboardDocument{
			Metadata: document.DashboardMetadata{ID: "dashboard:executive-sales", Name: "executive-sales", Featured: &featured},
			Spec:     document.DashboardSpec{SemanticModel: "semantic-model:sales"},
		},
		Metadata: authoring.AuthoredDashboardMetadata{Project: "project:sales", Name: "executive-sales"},
	}}
	item := Dashboard{ID: "dashboard:executive-sales", ProjectID: "project:sales", SemanticModel: "semantic-model:sales", Source: SourceProject}
	if err := enrichProjectItem(runtime, &item); err != nil {
		t.Fatalf("canonical id with symbolic authored name was rejected: %v", err)
	}
	if !item.Featured {
		t.Fatal("featured dashboard metadata was not projected")
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

func newCatalogService(t *testing.T, repo *catalogRepository, auth *catalogAuthorizer, dashboards []dashboardcatalog.Dashboard) *Service {
	return newCatalogServiceWithProvider(t, repo, auth, &catalogProvider{runtime: catalogRuntime{catalog: dashboardcatalog.Catalog{Project: dashboardcatalog.Project{ID: "sales"}, Dashboards: dashboards}}})
}

func newCatalogServiceWithProvider(t *testing.T, repo *catalogRepository, auth *catalogAuthorizer, provider *catalogProvider) *Service {
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
	requests []authoringservice.AuthorizationRequest
}

func (a *catalogAuthorizer) Authorize(_ context.Context, request authoringservice.AuthorizationRequest) error {
	a.requests = append(a.requests, request)
	if a.deny[request.DashboardID.String()] {
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
