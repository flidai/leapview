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
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
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

func (r *fakeRepository) Get(_ context.Context, _ string, dashboardID authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	r.getIDs = append(r.getIDs, dashboardID)
	return r.lifecycle, nil
}

func (r *fakeRepository) GetRevision(_ context.Context, _ string, _ authoring.DashboardID, id authoring.RevisionID) (authoring.Revision, error) {
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
	return operation.WorkspaceID + "|" + operation.ActorID + "|" + operation.Kind + "|" + operation.IdempotencyKey
}

func (r *fakeRepository) List(context.Context, string) ([]authoring.DashboardLifecycle, error) {
	return []authoring.DashboardLifecycle{r.lifecycle}, nil
}
func (r *fakeRepository) CountBySemanticModel(context.Context, string) ([]authoring.SemanticModelUsage, error) {
	return nil, nil
}
func (r *fakeRepository) LookupCommandResult(context.Context, string, authoring.DashboardID, authoring.CommandEvidence) (authoring.CommandResult, bool, error) {
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
func (r *fakeRepository) GetPublishedCompilation(context.Context, string, authoring.DashboardID) (authoring.CompiledRevision, error) {
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
	source projectartifact.AuthoredDashboardSource
	called bool
	fail   bool
}

func (r *fakeRuntime) Close() error { return nil }
func (r *fakeRuntime) AuthoredDashboardSource(id string) (projectartifact.AuthoredDashboardSource, bool) {
	r.called = true
	if r.fail {
		return projectartifact.AuthoredDashboardSource{}, false
	}
	if id != r.source.Document.ID {
		return projectartifact.AuthoredDashboardSource{}, false
	}
	return r.source, true
}

type fakeLease struct {
	runtime  runtimehost.Runtime
	state    servingstate.ID
	released *int
}

func (l *fakeLease) Runtime() runtimehost.Runtime    { return l.runtime }
func (l *fakeLease) ServingStateID() servingstate.ID { return l.state }
func (l *fakeLease) DuckLakeSnapshotID() int64       { return 42 }
func (l *fakeLease) Release()                        { *l.released++ }

func TestLoadWorkspaceUsesExactPublishedRevisionAndAuthorizesBeforeContent(t *testing.T) {
	published, newer, lifecycle := publishedFixture(t)
	repository := &fakeRepository{lifecycle: lifecycle, revisions: map[authoring.RevisionID]authoring.Revision{
		published.ID: published, newer.ID: newer,
	}}
	authorizer := &fakeAuthorizer{}
	adapter := newAdapter(t, repository, authorizer, nil)

	source, err := adapter.Load(t.Context(), sourceadapter.SourceRef{Kind: sourceadapter.SourceWorkspace, WorkspaceID: "workspace", DashboardID: "sales"}, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if source.Document.Title != "Published title" {
		t.Fatalf("loaded title = %q, want exact published title", source.Document.Title)
	}
	if source.Provenance.Kind != sourceadapter.SourceWorkspace || source.Provenance.Workspace == nil || source.Provenance.Workspace.PublishedRevision != published.Token() {
		t.Fatalf("workspace provenance = %#v", source.Provenance)
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
	if _, err := deniedAdapter.Load(t.Context(), sourceadapter.SourceRef{Kind: sourceadapter.SourceWorkspace, WorkspaceID: "workspace", DashboardID: "sales"}, "actor"); err == nil {
		t.Fatal("Load succeeded despite VIEW denial")
	}
	if deniedRepo.getRevisionCall != 0 {
		t.Fatalf("revision reads after VIEW denial = %d", deniedRepo.getRevisionCall)
	}
}

func TestWorkspaceForkReplaySkipsSourceReads(t *testing.T) {
	published, newer, lifecycle := publishedFixture(t)
	repository := &fakeRepository{lifecycle: lifecycle, revisions: map[authoring.RevisionID]authoring.Revision{published.ID: published, newer.ID: newer}}
	authorizer := &fakeAuthorizer{}
	authoringService := newAuthoringService(t, repository, authorizer)
	adapter := newAdapterWithService(t, repository, authorizer, authoringService, nil)
	request := sourceadapter.ForkRequest{Source: sourceadapter.SourceRef{Kind: sourceadapter.SourceWorkspace, WorkspaceID: "workspace", DashboardID: "sales"}, ActorID: "actor", Title: "Forked", Origin: authoring.OriginAgent, ConversationID: "conversation", ToolCallID: "tool", IdempotencyKey: "retry-workspace"}
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
		t.Fatalf("workspace replay = %#v first=%#v source revisions=%v", second, first, repository.getRevisionIDs)
	}
}

func TestLoadProjectUsesOneActiveLeaseAndHasNoFakeRevision(t *testing.T) {
	_, _, _ = publishedFixture(t)
	document := authoredDocument("project-sales", "Project title")
	runtime := &fakeRuntime{source: projectartifact.AuthoredDashboardSource{
		Document: document,
		Metadata: projectartifact.AuthoredDashboardMetadata{Workspace: "project", Name: "project-sales", Title: document.Title},
		Path:     "workspaces/project/dashboards/project-sales.yaml",
	}}
	released := 0
	acquires := 0
	authorizer := &fakeAuthorizer{}
	adapter := newAdapter(t, &fakeRepository{}, authorizer, func(_ context.Context, workspace string) (runtimehost.Lease, error) {
		acquires++
		if workspace != "project" {
			t.Fatalf("runtime workspace = %q", workspace)
		}
		return &fakeLease{runtime: runtime, state: "serving-project-1", released: &released}, nil
	})

	source, err := adapter.Load(t.Context(), sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, WorkspaceID: "project", DashboardID: "project-sales"}, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if acquires != 1 || released != 1 || !runtime.called {
		t.Fatalf("lease use = acquires %d released %d runtimeCalled %v", acquires, released, runtime.called)
	}
	if source.Provenance.Kind != sourceadapter.SourceProject || source.Provenance.Project == nil {
		t.Fatalf("project provenance = %#v", source.Provenance)
	}
	if source.Provenance.Project.ServingStateID != "serving-project-1" || source.Provenance.Project.Path != runtime.source.Path {
		t.Fatalf("project evidence = %#v", source.Provenance.Project)
	}
	if source.Provenance.Project.ServingStateID == "" {
		t.Fatal("project provenance omitted serving state")
	}
	if source.Provenance.Workspace != nil {
		t.Fatal("project provenance populated workspace revision branch")
	}
}

func TestMissingProjectSourceIsTypedUnavailableAndAuthPrecedesDisclosure(t *testing.T) {
	runtime := &fakeRuntime{source: projectartifact.AuthoredDashboardSource{Document: authoredDocument("other", "Other")}}
	released := 0
	authorizer := &fakeAuthorizer{}
	adapter := newAdapter(t, &fakeRepository{}, authorizer, func(context.Context, string) (runtimehost.Lease, error) {
		return &fakeLease{runtime: runtime, released: &released}, nil
	})
	_, err := adapter.Load(t.Context(), sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, WorkspaceID: "project", DashboardID: "missing"}, "actor")
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
	runtime := &fakeRuntime{source: projectartifact.AuthoredDashboardSource{
		Document: document,
		Metadata: projectartifact.AuthoredDashboardMetadata{Workspace: "project", Name: "project-sales", Title: document.Title, Owner: "project-owner"},
		Path:     "dashboards/project-sales.yaml",
	}}
	repository := &fakeRepository{}
	authorizer := &fakeAuthorizer{}
	authoringService := newAuthoringService(t, repository, authorizer)
	released := 0
	adapter := newAdapterWithService(t, repository, authorizer, authoringService, func(context.Context, string) (runtimehost.Lease, error) {
		return &fakeLease{runtime: runtime, state: "project-state", released: &released}, nil
	})

	exported, err := adapter.Export(t.Context(), sourceadapter.ExportRequest{Source: sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, WorkspaceID: "project", DashboardID: "project-sales"}, ActorID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(exported), "semanticModel: sales") || !strings.Contains(string(exported), "title: Project title") {
		t.Fatalf("exported YAML = %s", exported)
	}
	result, err := adapter.Fork(t.Context(), sourceadapter.ForkRequest{Source: sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, WorkspaceID: "project", DashboardID: "project-sales"}, ActorID: "actor", Title: "Forked project"})
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
	if created.Provenance.ForkedFrom == nil || created.Provenance.ForkedFrom.Kind != authoring.ForkSourceProject || created.Provenance.ForkedFrom.Project == nil || created.Provenance.ForkedFrom.Project.SourceDashboardID != "project-sales" || created.Provenance.ForkedFrom.Project.ServingStateID != "project-state" || created.Provenance.Source == nil || created.Provenance.Source.Path != runtime.source.Path || created.Provenance.BaseSemanticServingStateID != "project-state" {
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
	runtime := &fakeRuntime{source: projectartifact.AuthoredDashboardSource{Document: document, Metadata: projectartifact.AuthoredDashboardMetadata{Workspace: "project", Name: "project-sales", Title: document.Title, Owner: "project-owner"}, Path: "dashboards/project-sales.yaml"}}
	repository := &fakeRepository{}
	authorizer := &fakeAuthorizer{}
	authoringService := newAuthoringService(t, repository, authorizer)
	released := 0
	adapter := newAdapterWithService(t, repository, authorizer, authoringService, func(context.Context, string) (runtimehost.Lease, error) {
		return &fakeLease{runtime: runtime, state: "project-state", released: &released}, nil
	})
	request := sourceadapter.ForkRequest{Source: sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, WorkspaceID: "project", DashboardID: "project-sales"}, ActorID: "actor", Title: "Forked project", Origin: authoring.OriginAgent, ConversationID: "conversation", ToolCallID: "tool", IdempotencyKey: "retry-project"}
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

func (fakeCompiler) Compile(context.Context, string, string, authoring.Dashboard) (service.Compilation, error) {
	return service.Compilation{Definition: dashboarddefinition.Definition{ID: "unused", SemanticModel: "sales"}, SemanticServingStateID: "unused"}, nil
}

func authoredDocument(id, title string) authoring.Dashboard {
	page := dashboard.Page{ID: "overview", Title: "Overview", Canvas: dashboard.PageCanvas{Width: 1366, Height: 940}, Grid: dashboard.PageGrid{Columns: 12, RowHeight: 48, Gap: 16}}.WithDefaults()
	page.Visuals = []dashboard.PageVisual{{ID: "revenue-tile", Kind: "visual", Visual: "revenue", Placement: dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 6, RowSpan: 4}}}
	return authoring.Dashboard{
		ID: id, Title: title, SemanticModel: "sales",
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
	compiled, err := authoring.NewCompiledRevision("workspace", "sales", published.Token(), dashboarddefinition.Definition{ID: "sales", Title: document.Title, SemanticModel: "sales"}, "serving-state", sourceTestTime)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := authoring.DashboardLifecycle{WorkspaceID: "workspace", ID: "sales", OwnerPrincipalID: "owner", Slug: "sales", Title: document.Title, SemanticModel: "sales", Visibility: authoring.VisibilityShared, Status: authoring.LifecycleStatusPublished, Published: &authoring.Published{Revision: published.Token(), Compilation: compiled.Token(), PublishedAt: sourceTestTime, Provenance: provenance}, Draft: &authoring.Draft{ID: "draft", DashboardID: "sales", Revision: newer.Token(), Provenance: provenance}}
	if err := lifecycle.Validate(); err != nil {
		t.Fatal(err)
	}
	return published, newer, lifecycle
}
