package sourceadapter_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	"github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

type sourceFixture struct {
	repository *sourceRepository
	authorizer *sourceAuthorizer
	runtime    *sourceRuntime
	lease      *sourceLease
	adapter    *sourceadapter.Adapter
	published  authoring.Revision
	draft      authoring.Revision
	lifecycle  authoring.DashboardLifecycle
}

func newSourceFixture(t *testing.T) sourceFixture {
	t.Helper()
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}
	published, err := authoring.NewRevision("revision-published", "sales", 1, time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC), sourceDocument("Published title"), provenance)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := authoring.NewRevision("revision-draft", "sales", 2, time.Date(2026, 8, 18, 1, 0, 0, 0, time.UTC), sourceDocument("Newer draft"), provenance)
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := graph.NewServingIdentity("project", "production", "serving-1")
	compiled := authoring.CompiledRevisionToken{AuthoredRevision: published.Token(), DefinitionHash: "sha256:" + strings.Repeat("a", 64), SemanticIdentity: identity}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{ProjectID: "project", ID: "sales", OwnerPrincipalID: "owner", Slug: "sales", Title: "Sales", SemanticModel: "sales_model", Visibility: authoring.VisibilityPrivate, Draft: &authoring.Draft{ID: "draft-1", DashboardID: "sales", Revision: draft.Token(), Provenance: provenance}})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.Status = authoring.LifecycleStatusPublished
	lifecycle.Published = &authoring.Published{Revision: published.Token(), Compilation: compiled, PublishedAt: time.Date(2026, 8, 18, 2, 0, 0, 0, time.UTC), Provenance: provenance}
	repo := &sourceRepository{lifecycle: lifecycle, revisions: map[authoring.RevisionID]authoring.Revision{published.ID: published, draft.ID: draft}}
	auth := &sourceAuthorizer{}
	projectDoc := sourceDocumentWithID("project-sales", "Project title")
	runtime := &sourceRuntime{source: authoring.AuthoredDashboardSource{Document: projectDoc, Metadata: authoring.AuthoredDashboardMetadata{Project: "project", Name: "project-sales", Title: "Project title", Domain: "revenue"}, Path: "dashboards/project-sales.yaml"}}
	lease := &sourceLease{runtime: runtime, identity: identity}
	authSvc, err := service.NewService(service.Options{Repository: repo, Authorizer: auth, Compiler: sourceCompiler{}, Now: func() time.Time { return time.Date(2026, 8, 18, 3, 0, 0, 0, time.UTC) }, NewDashboardID: func() (authoring.DashboardID, error) { return "forked", nil }, NewDraftID: func() (authoring.DraftID, error) { return "forked-draft", nil }, NewRevisionID: func() (authoring.RevisionID, error) { return "forked-revision", nil }})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := sourceadapter.New(sourceadapter.Options{Repository: repo, Authorizer: auth, Authoring: authSvc, AcquireRuntime: func(context.Context) (projectruntime.Lease, error) { return lease, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return sourceFixture{repository: repo, authorizer: auth, runtime: runtime, lease: lease, adapter: adapter, published: published, draft: draft, lifecycle: lifecycle}
}

func sourceDocument(title string) document.DashboardDocument {
	return sourceDocumentWithID("sales", title)
}
func sourceDocumentWithID(id, title string) document.DashboardDocument {
	return document.DashboardDocument{APIVersion: document.DashboardApiVersionLeapviewDevV1, Kind: document.DashboardResourceKindDashboard, Metadata: document.DashboardMetadata{ID: id, Name: id, DisplayName: sourceStringPtr(title)}, Spec: document.DashboardSpec{SemanticModel: "sales_model", Filters: []document.DashboardFilter{}, Visuals: map[string]document.DashboardVisual{}, Pages: []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{}}}}}
}
func sourceStringPtr(value string) *string { return &value }

func TestLoadInstanceUsesExactPublishedRevisionAndAuthorizesBeforeRevision(t *testing.T) {
	f := newSourceFixture(t)
	source, err := f.adapter.Load(t.Context(), sourceadapter.SourceRef{Kind: sourceadapter.SourceInstance, ProjectID: "project", DashboardID: "sales"}, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if *source.Document.Metadata.DisplayName != "Published title" || source.Provenance.Instance == nil || source.Provenance.Instance.PublishedRevision != f.published.Token() || f.repository.getRevisionIDs[0] != f.published.ID {
		t.Fatalf("source/provenance = %#v/%#v", source.Document, source.Provenance)
	}
	if len(f.authorizer.requests) != 1 || f.authorizer.requests[0].Action != authoring.AuthorizationActionView {
		t.Fatalf("auth = %#v", f.authorizer.requests)
	}
	f = newSourceFixture(t)
	f.authorizer.err = access.ErrForbidden
	if _, err := f.adapter.Load(t.Context(), sourceadapter.SourceRef{Kind: sourceadapter.SourceInstance, ProjectID: "project", DashboardID: "sales"}, "actor"); !errors.Is(err, access.ErrForbidden) || len(f.repository.getRevisionIDs) != 0 {
		t.Fatalf("denied load err=%v revision reads=%v", err, f.repository.getRevisionIDs)
	}
}

func TestLoadDraftUsesCurrentDraftPointer(t *testing.T) {
	f := newSourceFixture(t)
	source, err := f.adapter.LoadDraft(t.Context(), sourceadapter.SourceRef{Kind: sourceadapter.SourceInstance, ProjectID: "project", DashboardID: "sales"}, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if *source.Document.Metadata.DisplayName != "Newer draft" || source.Provenance.Instance == nil || source.Provenance.Instance.DraftRevision == nil || *source.Provenance.Instance.DraftRevision != f.draft.Token() || source.Provenance.Instance.PublishedRevision != (authoring.RevisionToken{}) {
		t.Fatalf("draft source = %#v", source)
	}
}

func TestLoadProjectUsesOneLeaseAndNoFabricatedRevision(t *testing.T) {
	f := newSourceFixture(t)
	source, err := f.adapter.Load(t.Context(), sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, ProjectID: "project", DashboardID: "project-sales"}, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if f.lease.releases != 1 || !f.runtime.called || source.Provenance.Project == nil || source.Provenance.Project.Identity != f.lease.identity || source.Provenance.Project.Path != f.runtime.source.Path {
		t.Fatalf("lease/source evidence = %d/%#v", f.lease.releases, source.Provenance)
	}
	if source.Provenance.Instance != nil {
		t.Fatal("project source fabricated instance revision evidence")
	}
}

func TestProjectMissingSourceIsUnavailableAndAuthorizationPrecedesLease(t *testing.T) {
	f := newSourceFixture(t)
	f.runtime.source.Metadata.Name = "other"
	f.runtime.source.Document.Metadata.ID = "other"
	if _, err := f.adapter.Load(t.Context(), sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, ProjectID: "project", DashboardID: "missing"}, "actor"); !errors.Is(err, sourceadapter.ErrSourceUnavailable) || f.lease.releases != 1 {
		t.Fatalf("missing source err=%v releases=%d", err, f.lease.releases)
	}
	f = newSourceFixture(t)
	f.authorizer.err = access.ErrForbidden
	if _, err := f.adapter.Load(t.Context(), sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, ProjectID: "project", DashboardID: "project-sales"}, "actor"); !errors.Is(err, access.ErrForbidden) || f.lease.releases != 0 {
		t.Fatalf("denied project load err=%v releases=%d", err, f.lease.releases)
	}
}

type sourceRepository struct {
	lifecycle      authoring.DashboardLifecycle
	revisions      map[authoring.RevisionID]authoring.Revision
	getRevisionIDs []authoring.RevisionID
}

func (r *sourceRepository) Get(context.Context, graph.ResourceID, authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	return r.lifecycle, nil
}
func (r *sourceRepository) GetRevision(_ context.Context, _ graph.ResourceID, _ authoring.DashboardID, id authoring.RevisionID) (authoring.Revision, error) {
	r.getRevisionIDs = append(r.getRevisionIDs, id)
	value, ok := r.revisions[id]
	if !ok {
		return authoring.Revision{}, authoring.ErrNotFound
	}
	return value, nil
}
func (r *sourceRepository) Create(context.Context, authoring.CreateInput) (authoring.DashboardLifecycle, error) {
	panic("unused")
}
func (r *sourceRepository) List(context.Context, graph.ResourceID) ([]authoring.DashboardLifecycle, error) {
	panic("unused")
}
func (r *sourceRepository) CountBySemanticModel(context.Context, graph.ResourceID) ([]authoring.SemanticModelUsage, error) {
	panic("unused")
}
func (r *sourceRepository) LookupCommandResult(context.Context, graph.ResourceID, authoring.DashboardID, authoring.CommandEvidence) (authoring.CommandResult, bool, error) {
	panic("unused")
}
func (r *sourceRepository) LookupCreateOperation(context.Context, authoring.CreateOperation) (authoring.CreateOperationResult, bool, error) {
	return authoring.CreateOperationResult{}, false, nil
}
func (r *sourceRepository) AppendDraft(context.Context, authoring.AppendDraftInput) (authoring.Revision, error) {
	panic("unused")
}
func (r *sourceRepository) Publish(context.Context, authoring.PublishInput) (authoring.DashboardLifecycle, error) {
	panic("unused")
}
func (r *sourceRepository) Archive(context.Context, authoring.ArchiveInput) (authoring.DashboardLifecycle, error) {
	panic("unused")
}
func (r *sourceRepository) GetPublishedCompilation(context.Context, graph.ResourceID, authoring.DashboardID) (authoring.CompiledRevision, error) {
	panic("unused")
}

type sourceAuthorizer struct {
	requests []service.AuthorizationRequest
	err      error
}

func (a *sourceAuthorizer) Authorize(_ context.Context, request service.AuthorizationRequest) error {
	a.requests = append(a.requests, request)
	return a.err
}

type sourceRuntime struct {
	source authoring.AuthoredDashboardSource
	called bool
}

func (r *sourceRuntime) Close() error { return nil }
func (r *sourceRuntime) AuthoredDashboardSource(id string) (authoring.AuthoredDashboardSource, bool) {
	r.called = true
	if r.source.Document.Metadata.ID != id && r.source.Metadata.Name != id {
		return authoring.AuthoredDashboardSource{}, false
	}
	return r.source, true
}

type sourceLease struct {
	runtime  projectruntime.Runtime
	identity graph.ServingIdentity
	releases int
}

func (l *sourceLease) Runtime() projectruntime.Runtime { return l.runtime }
func (l *sourceLease) Identity() graph.ServingIdentity { return l.identity }
func (l *sourceLease) Release()                        { l.releases++ }

type sourceCompiler struct{}

func (sourceCompiler) Compile(context.Context, graph.ResourceID, graph.ResourceID, document.DashboardDocument) (service.Compilation, error) {
	return service.Compilation{Definition: definition.Definition{ID: "sales", SemanticModel: "sales_model"}}, nil
}

var _ projectruntime.Runtime = (*sourceRuntime)(nil)
