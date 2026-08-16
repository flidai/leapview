package sourceadapter_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

var sourceTestTime = time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

type fakeRepository struct {
	lifecycle       authoring.DashboardLifecycle
	revisions       map[authoring.RevisionID]authoring.Revision
	getRevisionCall int
	created         []authoring.CreateInput
	operations      map[string]authoring.CreateOperationResult
	getIDs          []authoring.DashboardID
	getRevisionIDs  []authoring.RevisionID
}

func (r *fakeRepository) Get(_ context.Context, _ projectgraph.ResourceID, dashboardID authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	r.getIDs = append(r.getIDs, dashboardID)
	return r.lifecycle, nil
}

func (r *fakeRepository) GetRevision(_ context.Context, _ projectgraph.ResourceID, _ authoring.DashboardID, id authoring.RevisionID) (authoring.Revision, error) {
	r.getRevisionCall++
	r.getRevisionIDs = append(r.getRevisionIDs, id)
	revision, ok := r.revisions[id]
	if !ok {
		return authoring.Revision{}, authoring.ErrNotFound
	}
	return revision, nil
}

func (r *fakeRepository) Create(_ context.Context, input authoring.CreateInput) (authoring.DashboardLifecycle, error) {
	if input.Operation.Enabled() {
		if existing, ok := r.operations[operationKey(input.Operation)]; ok {
			if existing.Fingerprint != input.Operation.Fingerprint {
				return authoring.DashboardLifecycle{}, authoring.ErrCommandReuse
			}
			return r.lifecycle, nil
		}
	}
	r.created = append(r.created, input)
	r.lifecycle = input.Lifecycle
	if r.revisions == nil {
		r.revisions = map[authoring.RevisionID]authoring.Revision{}
	}
	r.revisions[input.Revision.ID] = input.Revision
	if input.Operation.Enabled() {
		if r.operations == nil {
			r.operations = map[string]authoring.CreateOperationResult{}
		}
		r.operations[operationKey(input.Operation)] = authoring.CreateOperationResult{DashboardID: input.Lifecycle.ID, Revision: input.Revision.Token(), Fingerprint: input.Operation.Fingerprint}
	}
	return input.Lifecycle, nil
}

func (r *fakeRepository) LookupCreateOperation(_ context.Context, operation authoring.CreateOperation) (authoring.CreateOperationResult, bool, error) {
	result, ok := r.operations[operationKey(operation)]
	return result, ok, nil
}

func operationKey(operation authoring.CreateOperation) string {
	return operation.ProjectID.String() + "|" + operation.ActorID + "|" + operation.Kind + "|" + operation.IdempotencyKey
}

func (r *fakeRepository) List(context.Context, projectgraph.ResourceID) ([]authoring.DashboardLifecycle, error) {
	return []authoring.DashboardLifecycle{r.lifecycle}, nil
}
func (r *fakeRepository) CountBySemanticModel(context.Context, projectgraph.ResourceID) ([]authoring.SemanticModelUsage, error) {
	return nil, nil
}
func (r *fakeRepository) LookupCommandResult(context.Context, projectgraph.ResourceID, authoring.DashboardID, authoring.CommandEvidence) (authoring.CommandResult, bool, error) {
	return authoring.CommandResult{}, false, nil
}
func (r *fakeRepository) AppendDraft(context.Context, authoring.AppendDraftInput) (authoring.Revision, error) {
	return authoring.Revision{}, errors.New("unexpected append")
}
func (r *fakeRepository) Publish(context.Context, authoring.PublishInput) (authoring.DashboardLifecycle, error) {
	return authoring.DashboardLifecycle{}, errors.New("unexpected publish")
}
func (r *fakeRepository) Archive(context.Context, authoring.ArchiveInput) (authoring.DashboardLifecycle, error) {
	return authoring.DashboardLifecycle{}, errors.New("unexpected archive")
}
func (r *fakeRepository) GetPublishedCompilation(context.Context, projectgraph.ResourceID, authoring.DashboardID) (authoring.CompiledRevision, error) {
	return authoring.CompiledRevision{}, authoring.ErrNotFound
}

type fakeAuthorizer struct {
	requests []service.AuthorizationRequest
	err      error
}

func (a *fakeAuthorizer) Authorize(_ context.Context, request service.AuthorizationRequest) error {
	a.requests = append(a.requests, request)
	return a.err
}

type fakeRuntime struct {
	source authoring.AuthoredDashboardSource
	called bool
	fail   bool
}

func (r *fakeRuntime) Close() error { return nil }
func (r *fakeRuntime) AuthoredDashboardSource(id string) (authoring.AuthoredDashboardSource, bool) {
	r.called = true
	if r.fail {
		return authoring.AuthoredDashboardSource{}, false
	}
	if projectgraph.ResourceID(id) != r.source.Document.ID {
		return authoring.AuthoredDashboardSource{}, false
	}
	return r.source, true
}

type fakeLease struct {
	runtime  projectruntime.Runtime
	identity projectgraph.ServingIdentity
	released *int
}

func (l *fakeLease) Runtime() projectruntime.Runtime        { return l.runtime }
func (l *fakeLease) Identity() projectgraph.ServingIdentity { return l.identity }
func (l *fakeLease) Release()                               { *l.released++ }

func TestLoadInstanceUsesExactPublishedRevisionAndAuthorizesBeforeContent(t *testing.T) {
	published, newer, lifecycle := publishedFixture(t)
	repository := &fakeRepository{lifecycle: lifecycle, revisions: map[authoring.RevisionID]authoring.Revision{
		published.ID: published, newer.ID: newer,
	}}
	authorizer := &fakeAuthorizer{}
	adapter := newAdapter(t, repository, authorizer, nil)

	source, err := adapter.Load(t.Context(), sourceadapter.SourceRef{Kind: sourceadapter.SourceInstance, ProjectID: "project", DashboardID: "sales"}, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if source.Document.Title != "Published title" {
		t.Fatalf("loaded title = %q, want exact published title", source.Document.Title)
	}
	if source.Provenance.Kind != sourceadapter.SourceInstance || source.Provenance.Instance == nil || source.Provenance.Instance.PublishedRevision != published.Token() {
		t.Fatalf("project provenance = %#v", source.Provenance)
	}
	if source.Lifecycle == nil || source.Lifecycle.Status != authoring.LifecycleStatusPublished {
		t.Fatalf("loaded lifecycle = %#v", source.Lifecycle)
	}
	if len(authorizer.requests) != 1 || authorizer.requests[0].Action != authoring.AuthorizationActionView {
		t.Fatalf("authorization requests = %#v", authorizer.requests)
	}

	deniedRepo := &fakeRepository{lifecycle: lifecycle, revisions: repository.revisions}
	denied := &fakeAuthorizer{err: errors.New("view denied")}
	deniedAdapter := newAdapter(t, deniedRepo, denied, nil)
	if _, err := deniedAdapter.Load(t.Context(), sourceadapter.SourceRef{Kind: sourceadapter.SourceInstance, ProjectID: "project", DashboardID: "sales"}, "actor"); err == nil {
		t.Fatal("Load succeeded despite VIEW denial")
	}
	if deniedRepo.getRevisionCall != 0 {
		t.Fatalf("revision reads after VIEW denial = %d", deniedRepo.getRevisionCall)
	}
}

func TestExportDraftUsesLifecycleDraftRevisionByDashboardID(t *testing.T) {
	published, newer, lifecycle := publishedFixture(t)
	repository := &fakeRepository{lifecycle: lifecycle, revisions: map[authoring.RevisionID]authoring.Revision{
		published.ID: published, newer.ID: newer,
	}}
	authorizer := &fakeAuthorizer{}
	authoringService := newAuthoringService(t, repository, authorizer)
	adapter, err := sourceadapter.New(sourceadapter.Options{
		Repository: repository, Authorizer: authorizer, Authoring: authoringService,
		ExportDashboard: func(document authoring.Dashboard, _ authoring.DashboardExportMetadata) ([]byte, error) {
			return []byte(document.Title), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	exported, err := adapter.ExportDraft(t.Context(), sourceadapter.ExportRequest{
		Source: sourceadapter.SourceRef{Kind: sourceadapter.SourceInstance, ProjectID: "project", DashboardID: lifecycle.ID}, ActorID: "actor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(exported) != "Newer draft" {
		t.Fatalf("draft export document = %q", exported)
	}
	// The authored title is deliberately changed only in the draft revision;
	// its document content must still be the current lifecycle draft, not the
	// published source selected by Adapter.Export.
	source, err := adapter.LoadDraft(t.Context(), sourceadapter.SourceRef{Kind: sourceadapter.SourceInstance, ProjectID: "project", DashboardID: lifecycle.ID}, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if source.Document.Title != "Newer draft" || source.Provenance.Instance == nil || source.Provenance.Instance.DraftRevision == nil || *source.Provenance.Instance.DraftRevision != newer.Token() {
		t.Fatalf("draft source = title %q provenance %#v", source.Document.Title, source.Provenance.Instance)
	}
	if source.Provenance.Instance.PublishedRevision != (authoring.RevisionToken{}) {
		t.Fatalf("draft source fabricated published provenance: %#v", source.Provenance.Instance)
	}
}

func TestProjectForkReplaySkipsSourceReads(t *testing.T) {
	published, newer, lifecycle := publishedFixture(t)
	repository := &fakeRepository{lifecycle: lifecycle, revisions: map[authoring.RevisionID]authoring.Revision{published.ID: published, newer.ID: newer}}
	authorizer := &fakeAuthorizer{}
	authoringService := newAuthoringService(t, repository, authorizer)
	adapter := newAdapterWithService(t, repository, authorizer, authoringService, nil)
	request := sourceadapter.ForkRequest{Source: sourceadapter.SourceRef{Kind: sourceadapter.SourceInstance, ProjectID: "project", DashboardID: "sales"}, ActorID: "actor", Title: "Forked", Origin: authoring.OriginAgent, ConversationID: "conversation", ToolCallID: "tool", IdempotencyKey: "retry-project"}
	first, err := adapter.Fork(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	repository.getIDs = nil
	repository.getRevisionIDs = nil
	second, err := adapter.Fork(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range repository.getIDs {
		if id == "sales" {
			t.Fatalf("replay read source lifecycle: %v", repository.getIDs)
		}
	}
	if len(repository.getRevisionIDs) != 0 || second.Revision != first.Revision || second.Lifecycle.ID != first.Lifecycle.ID {
		t.Fatalf("project replay = %#v first=%#v source revisions=%v", second, first, repository.getRevisionIDs)
	}
}

func TestLoadProjectUsesOneActiveLeaseAndHasNoFakeRevision(t *testing.T) {
	_, _, _ = publishedFixture(t)
	document := authoredDocument("project-sales", "Project title")
	runtime := &fakeRuntime{source: authoring.AuthoredDashboardSource{
		Document: document,
		Metadata: authoring.AuthoredDashboardMetadata{Project: "project", Name: "project-sales", Title: document.Title},
		Path:     "projects/project/dashboards/project-sales.yaml",
	}}
	released := 0
	acquires := 0
	authorizer := &fakeAuthorizer{}
	identity, _ := projectgraph.NewServingIdentity("project", "production", "serving-project-1")
	adapter := newAdapter(t, &fakeRepository{}, authorizer, func(_ context.Context) (projectruntime.Lease, error) {
		acquires++
		return &fakeLease{runtime: runtime, identity: identity, released: &released}, nil
	})

	source, err := adapter.Load(t.Context(), sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, ProjectID: "project", DashboardID: "project-sales"}, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if acquires != 1 || released != 1 || !runtime.called {
		t.Fatalf("lease use = acquires %d released %d runtimeCalled %v", acquires, released, runtime.called)
	}
	if source.Provenance.Kind != sourceadapter.SourceProject || source.Provenance.Project == nil {
		t.Fatalf("project provenance = %#v", source.Provenance)
	}
	if source.Provenance.Project.Identity != identity || source.Provenance.Project.Path != runtime.source.Path {
		t.Fatalf("project evidence = %#v", source.Provenance.Project)
	}
	if source.Provenance.Project.Identity.GenerationID == "" {
		t.Fatal("project provenance omitted serving state")
	}
}

func TestMissingProjectSourceIsTypedUnavailableAndAuthPrecedesDisclosure(t *testing.T) {
	runtime := &fakeRuntime{source: authoring.AuthoredDashboardSource{Document: authoredDocument("other", "Other")}}
	released := 0
	authorizer := &fakeAuthorizer{}
	identity, _ := projectgraph.NewServingIdentity("project", "production", "missing-state")
	adapter := newAdapter(t, &fakeRepository{}, authorizer, func(context.Context) (projectruntime.Lease, error) {
		return &fakeLease{runtime: runtime, identity: identity, released: &released}, nil
	})
	_, err := adapter.Load(t.Context(), sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, ProjectID: "project", DashboardID: "missing"}, "actor")
	if !errors.Is(err, sourceadapter.ErrSourceUnavailable) || !errors.As(err, new(*sourceadapter.SourceUnavailableError)) {
		t.Fatalf("error = %v, want typed unavailable", err)
	}
	if len(authorizer.requests) != 1 || authorizer.requests[0].Action != authoring.AuthorizationActionView {
		t.Fatalf("authorization requests = %#v", authorizer.requests)
	}
	if released != 1 {
		t.Fatalf("lease releases = %d, want 1", released)
	}
}

func TestExportAndProjectForkPreserveAuthoredDocumentWithoutPublishOrDeploy(t *testing.T) {
	document := authoredDocument("project-sales", "Project title")
	runtime := &fakeRuntime{source: authoring.AuthoredDashboardSource{
		Document: document,
		Metadata: authoring.AuthoredDashboardMetadata{Project: "project", Name: "project-sales", Title: document.Title, Owner: "project-owner"},
		Path:     "dashboards/project-sales.yaml",
	}}
	repository := &fakeRepository{}
	authorizer := &fakeAuthorizer{}
	authoringService := newAuthoringService(t, repository, authorizer)
	released := 0
	identity, _ := projectgraph.NewServingIdentity("project", "production", "project-state")
	adapter := newAdapterWithService(t, repository, authorizer, authoringService, func(context.Context) (projectruntime.Lease, error) {
		return &fakeLease{runtime: runtime, identity: identity, released: &released}, nil
	})

	exported, err := adapter.Export(t.Context(), sourceadapter.ExportRequest{Source: sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, ProjectID: "project", DashboardID: "project-sales"}, ActorID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(exported), "semanticModel: sales") || !strings.Contains(string(exported), "title: Project title") {
		t.Fatalf("exported YAML = %s", exported)
	}
	result, err := adapter.Fork(t.Context(), sourceadapter.ForkRequest{Source: sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, ProjectID: "project", DashboardID: "project-sales"}, ActorID: "actor", Title: "Forked project", IdempotencyKey: "project-fork"})
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.created) != 1 || result.Lifecycle.Visibility != authoring.VisibilityPrivate || result.Lifecycle.SemanticModel != "sales" {
		t.Fatalf("fork result = %#v creates=%d", result.Lifecycle, len(repository.created))
	}
	created := repository.created[0].Revision
	if created.Document.Title != "Forked project" || created.Document.ID == document.ID || created.ID == "" || result.Lifecycle.Draft.ID == "" {
		t.Fatalf("fork identities/document = %#v", created)
	}
	if created.Provenance.ForkedFrom == nil || created.Provenance.ForkedFrom.Kind != authoring.ForkSourceProject || created.Provenance.ForkedFrom.Project == nil || created.Provenance.ForkedFrom.Project.SourceDashboardID != "project-sales" || created.Provenance.ForkedFrom.Project.Identity != identity || created.Provenance.Source == nil || created.Provenance.Source.Path != runtime.source.Path || created.Provenance.BaseSemanticIdentity != identity {
		t.Fatalf("project fork provenance = %#v", created.Provenance)
	}
	if released != 2 { // export + fork load; each lease is released exactly once.
		t.Fatalf("lease releases = %d, want 2", released)
	}
	if authorizer.requests[len(authorizer.requests)-1].Action != authoring.AuthorizationActionEdit {
		t.Fatalf("last authorization = %#v", authorizer.requests[len(authorizer.requests)-1])
	}
}

func TestProjectForkReplaySkipsUnavailableSourceLoad(t *testing.T) {
	document := authoredDocument("project-sales", "Project title")
	runtime := &fakeRuntime{source: authoring.AuthoredDashboardSource{Document: document, Metadata: authoring.AuthoredDashboardMetadata{Project: "project", Name: "project-sales", Title: document.Title, Owner: "project-owner"}, Path: "dashboards/project-sales.yaml"}}
	repository := &fakeRepository{}
	authorizer := &fakeAuthorizer{}
	authoringService := newAuthoringService(t, repository, authorizer)
	released := 0
	identity, _ := projectgraph.NewServingIdentity("project", "production", "project-state")
	adapter := newAdapterWithService(t, repository, authorizer, authoringService, func(context.Context) (projectruntime.Lease, error) {
		return &fakeLease{runtime: runtime, identity: identity, released: &released}, nil
	})
	request := sourceadapter.ForkRequest{Source: sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, ProjectID: "project", DashboardID: "project-sales"}, ActorID: "actor", Title: "Forked project", Origin: authoring.OriginAgent, ConversationID: "conversation", ToolCallID: "tool", IdempotencyKey: "retry-project"}
	first, err := adapter.Fork(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	loads := runtime.called
	runtime.fail = true
	second, err := adapter.Fork(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != first.Revision || second.Lifecycle.ID != first.Lifecycle.ID || runtime.called != loads || released != 1 {
		t.Fatalf("project replay = %#v first=%#v runtimeCalled=%v/%v released=%d", second, first, loads, runtime.called, released)
	}
	authorizer.err = errors.New("target revoked")
	changed := request
	changed.Title = "Different"
	if _, err := adapter.Fork(t.Context(), changed); err == nil || !strings.Contains(err.Error(), "target revoked") || runtime.called != loads {
		t.Fatalf("revoked changed replay err=%v runtimeCalled=%v/%v", err, runtime.called, loads)
	}
}

func newAdapter(t *testing.T, repository *fakeRepository, authorizer *fakeAuthorizer, acquire sourceadapter.AcquireRuntime) *sourceadapter.Adapter {
	t.Helper()
	return newAdapterWithService(t, repository, authorizer, newAuthoringService(t, repository, authorizer), acquire)
}

func newAdapterWithService(t *testing.T, repository *fakeRepository, authorizer *fakeAuthorizer, authoringService *service.Service, acquire sourceadapter.AcquireRuntime) *sourceadapter.Adapter {
	t.Helper()
	adapter, err := sourceadapter.New(sourceadapter.Options{Repository: repository, Authorizer: authorizer, AcquireRuntime: acquire, Authoring: authoringService, ExportDashboard: projectcompiler.ExportDashboard})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func newAuthoringService(t *testing.T, repository *fakeRepository, authorizer *fakeAuthorizer) *service.Service {
	t.Helper()
	service, err := service.NewService(service.Options{
		Repository: repository, Authorizer: authorizer, Compiler: fakeCompiler{}, Now: func() time.Time { return sourceTestTime },
		NewDashboardID: func() (authoring.DashboardID, error) { return "forked-project", nil },
		NewDraftID:     func() (authoring.DraftID, error) { return "forked-draft", nil },
		NewRevisionID:  func() (authoring.RevisionID, error) { return "forked-revision", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type fakeCompiler struct{}

func (fakeCompiler) Compile(context.Context, projectgraph.ResourceID, projectgraph.ResourceID, authoring.Dashboard) (service.Compilation, error) {
	identity, _ := projectgraph.NewServingIdentity("project", "production", "unused")
	return service.Compilation{Definition: dashboarddefinition.Definition{ID: "unused", SemanticModel: "sales"}, SemanticIdentity: identity}, nil
}

func authoredDocument(id, title string) authoring.Dashboard {
	page := dashboard.Page{ID: "overview", Title: "Overview", Canvas: dashboard.PageCanvas{Width: 1366, Height: 940}, Grid: dashboard.PageGrid{Columns: 12, RowHeight: 48, Gap: 16}}.WithDefaults()
	page.Visuals = []dashboard.PageVisual{{ID: "revenue-tile", Kind: "visual", Visual: "revenue", Placement: dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 6, RowSpan: 4}}}
	return authoring.Dashboard{
		ID: projectgraph.ResourceID(id), Title: title, SemanticModel: "sales",
		Visuals: map[string]authoring.AuthoringVisualization{
			"revenue": authoring.ChartVisualization(authoring.Visual{Title: "Revenue", Type: "line", Query: authoring.VisualQuery{
				Dimensions: []authoring.FieldRef{{Field: "month", Alias: "month"}},
				Measures:   []authoring.FieldRef{{Field: "revenue", Alias: "revenue"}},
			}}),
		}, Pages: []dashboard.Page{page},
	}
}

func publishedFixture(t *testing.T) (authoring.Revision, authoring.Revision, authoring.DashboardLifecycle) {
	t.Helper()
	document := authoredDocument("sales", "Published title")
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "publisher"}
	published, err := authoring.NewRevision("published-revision", "sales", 3, sourceTestTime, document, provenance)
	if err != nil {
		t.Fatal(err)
	}
	newerDocument := authoredDocument("sales", "Newer draft")
	newer, err := authoring.NewRevision("newer-revision", "sales", 4, sourceTestTime.Add(time.Minute), newerDocument, provenance)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project", "production", "serving-state")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := authoring.NewCompiledRevision("project", "sales", published.Token(), dashboarddefinition.Definition{ID: "sales", Title: document.Title, SemanticModel: "sales"}, identity, sourceTestTime)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := authoring.DashboardLifecycle{ProjectID: "project", ID: "sales", OwnerPrincipalID: "owner", Slug: "sales", Title: document.Title, SemanticModel: "sales", Visibility: authoring.VisibilityOrganization, Status: authoring.LifecycleStatusPublished, Published: &authoring.Published{Revision: published.Token(), Compilation: compiled.Token(), PublishedAt: sourceTestTime, Provenance: provenance}, Draft: &authoring.Draft{ID: "draft", DashboardID: "sales", Revision: newer.Token(), Provenance: provenance}}
	if err := lifecycle.Validate(); err != nil {
		t.Fatal(err)
	}
	return published, newer, lifecycle
}
