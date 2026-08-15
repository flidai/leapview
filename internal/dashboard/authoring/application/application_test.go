package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/catalog"
	previewservice "github.com/flidai/leapview/internal/dashboard/authoring/preview"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	dashboardcatalog "github.com/flidai/leapview/internal/dashboard/catalog"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

var applicationTestTime = time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

func TestNewRequiresCompositionPorts(t *testing.T) {
	valid := application.Options{
		Authoring:      newAuthoringService(t, newRepository(), &fakeAuthorizer{}),
		Repository:     newRepository(),
		Authorizer:     &fakeAuthorizer{},
		AcquireRuntime: func(context.Context, string) (runtimehost.Lease, error) { return nil, nil },
	}
	tests := []struct {
		name string
		edit func(*application.Options)
	}{
		{name: "authoring", edit: func(options *application.Options) { options.Authoring = nil }},
		{name: "repository", edit: func(options *application.Options) { options.Repository = nil }},
		{name: "authorizer", edit: func(options *application.Options) { options.Authorizer = nil }},
		{name: "runtime", edit: func(options *application.Options) { options.AcquireRuntime = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := valid
			test.edit(&options)
			if _, err := application.New(options); err == nil {
				t.Fatal("New succeeded with missing composition port")
			}
		})
	}
}

func TestCreateAndExecuteDelegateToTransactionalService(t *testing.T) {
	repository := newRepository()
	authorizer := &fakeAuthorizer{}
	app := newApplication(t, repository, authorizer, func(context.Context, string) (runtimehost.Lease, error) {
		return nil, errors.New("runtime should not be acquired")
	})

	created, err := app.Create(context.Background(), authoringservice.CreateRequest{
		WorkspaceID: " workspace ", ActorID: "actor", OwnerPrincipalID: "owner",
		Title: "Orders", Slug: "orders", SemanticModel: "sales", Origin: authoring.OriginUI,
	})
	if err != nil {
		t.Fatal(err)
	}
	if repository.createCalls != 1 || created.Lifecycle.WorkspaceID != "workspace" {
		t.Fatalf("create delegation = calls %d lifecycle %#v", repository.createCalls, created.Lifecycle)
	}

	title := "Orders v2"
	command := authoring.Command{
		ID: "edit-1", DashboardID: created.Lifecycle.ID, DraftID: created.Lifecycle.Draft.ID,
		ExpectedRevision: created.Revision, Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"},
		Metadata: &authoring.MetadataPatch{Title: &title},
	}
	updated, err := app.Execute(context.Background(), " workspace ", command)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Lifecycle.Title != title || repository.appendCalls != 1 {
		t.Fatalf("execute delegation = lifecycle %#v appendCalls=%d", updated.Lifecycle, repository.appendCalls)
	}
}

func TestCatalogOperationsUseRequestedWorkspaceAndOneLeaseEach(t *testing.T) {
	repository := newRepository()
	authorizer := &fakeAuthorizer{}
	var acquired []string
	var leases []*fakeLease
	app := newApplication(t, repository, authorizer, func(_ context.Context, workspace string) (runtimehost.Lease, error) {
		acquired = append(acquired, workspace)
		lease := &fakeLease{runtime: &catalogRuntime{catalog: dashboardcatalog.Catalog{
			Workspace:  dashboardcatalog.Workspace{ID: workspace},
			Dashboards: []dashboardcatalog.Dashboard{{ID: workspace + "-sales", Title: workspace + " sales", SemanticModel: "sales"}},
		}}, servingState: servingstate.ID("state-" + workspace)}
		leases = append(leases, lease)
		return lease, nil
	})

	listed, err := app.List(context.Background(), catalog.ListRequest{WorkspaceID: " sales ", ActorID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	if listed.Count != 1 || listed.Items[0].WorkspaceID != "sales" {
		t.Fatalf("catalog list = %#v", listed)
	}
	got, err := app.Get(context.Background(), catalog.GetRequest{WorkspaceID: " finance ", ActorID: "actor", DashboardID: "finance-sales"})
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspaceID != "finance" || got.ID != "finance-sales" {
		t.Fatalf("catalog get = %#v", got)
	}
	if strings.Join(acquired, ",") != "sales,finance" {
		t.Fatalf("runtime workspaces = %#v", acquired)
	}
	if len(leases) != 2 || leases[0].releaseCalls != 1 || leases[1].releaseCalls != 1 {
		t.Fatalf("lease lifecycle = %#v", leases)
	}
}

func TestPreviewUsesRequestedWorkspaceAndOneLease(t *testing.T) {
	repository, lifecycle, revision := previewRepository(t)
	authorizer := &fakeAuthorizer{}
	var acquired string
	lease := &fakeLease{runtime: &previewRuntime{model: previewModel()}, servingState: "serving-sales"}
	app := newApplication(t, repository, authorizer, func(_ context.Context, workspace string) (runtimehost.Lease, error) {
		acquired = workspace
		return lease, nil
	})

	result, err := app.Preview(context.Background(), previewservice.PreviewRequest{
		WorkspaceID: " workspace ", ActorID: "actor", DashboardID: lifecycle.ID,
		ExpectedRevision: revision.Token(), PageID: "overview",
	})
	if err != nil {
		t.Fatal(err)
	}
	if acquired != "workspace" || lease.releaseCalls != 1 || result.Revision != revision.Token() {
		t.Fatalf("preview scope/result = workspace %q releases %d result %#v", acquired, lease.releaseCalls, result)
	}
}

func TestProjectExportNormalizesSourceWorkspaceAndDoesNotCrossWorkspace(t *testing.T) {
	repository := newRepository()
	authorizer := &fakeAuthorizer{}
	sources := map[string]projectartifact.AuthoredDashboardSource{
		"sales": {
			Document: exportDocument("sales-dashboard", "Sales"),
			Metadata: projectartifact.AuthoredDashboardMetadata{Workspace: "sales", Name: "sales-dashboard", Title: "Sales"},
		},
		"finance": {
			Document: exportDocument("finance-dashboard", "Finance"),
			Metadata: projectartifact.AuthoredDashboardMetadata{Workspace: "finance", Name: "finance-dashboard", Title: "Finance"},
		},
	}
	var acquired []string
	var leases []*fakeLease
	app := newApplication(t, repository, authorizer, func(_ context.Context, workspace string) (runtimehost.Lease, error) {
		acquired = append(acquired, workspace)
		lease := &fakeLease{runtime: &sourceRuntime{source: sources[workspace]}, servingState: servingstate.ID("state-" + workspace)}
		leases = append(leases, lease)
		return lease, nil
	})

	sales, err := app.ExportYAML(context.Background(), sourceadapter.ExportRequest{
		Source: sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, WorkspaceID: " sales ", DashboardID: "sales-dashboard"}, ActorID: "actor",
	})
	if err != nil {
		t.Fatal(err)
	}
	finance, err := app.ExportYAML(context.Background(), sourceadapter.ExportRequest{
		Source: sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, WorkspaceID: " finance ", DashboardID: "finance-dashboard"}, ActorID: "actor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sales), "title: Sales") || !strings.Contains(string(finance), "title: Finance") {
		t.Fatalf("exports crossed sources: sales=%s finance=%s", sales, finance)
	}
	if strings.Join(acquired, ",") != "sales,finance" || leases[0].releaseCalls != 1 || leases[1].releaseCalls != 1 {
		t.Fatalf("source lease scope = acquired %#v leases %#v", acquired, leases)
	}
}

func TestWorkspaceForkRejectsCrossWorkspaceTarget(t *testing.T) {
	repository := newRepository()
	authorizer := &fakeAuthorizer{}
	app := newApplication(t, repository, authorizer, func(context.Context, string) (runtimehost.Lease, error) {
		return nil, errors.New("runtime should not be acquired")
	})
	_, err := app.Fork(context.Background(), sourceadapter.ForkRequest{
		Source:            sourceadapter.SourceRef{Kind: sourceadapter.SourceWorkspace, WorkspaceID: " workspace ", DashboardID: "sales"},
		TargetWorkspaceID: "other", ActorID: "actor",
	})
	if err == nil || !strings.Contains(err.Error(), "must remain in the source workspace") {
		t.Fatalf("cross-workspace fork error = %v", err)
	}
}

func newApplication(t *testing.T, repository *fakeRepository, authorizer *fakeAuthorizer, acquire sourceadapter.AcquireRuntime) *application.Application {
	t.Helper()
	authoringSvc := newAuthoringService(t, repository, authorizer)
	app, err := application.New(application.Options{Authoring: authoringSvc, Repository: repository, Authorizer: authorizer, AcquireRuntime: acquire})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func newAuthoringService(t *testing.T, repository *fakeRepository, authorizer *fakeAuthorizer) *authoringservice.Service {
	t.Helper()
	ids := []string{"dashboard-1", "draft-1", "revision-1", "revision-2"}
	next := func() string {
		value := ids[0]
		ids = ids[1:]
		return value
	}
	service, err := authoringservice.NewService(authoringservice.Options{
		Repository: repository, Authorizer: authorizer, Compiler: fakeCompiler{}, Now: func() time.Time { return applicationTestTime },
		NewDashboardID: func() (authoring.DashboardID, error) { return authoring.DashboardID(next()), nil },
		NewDraftID:     func() (authoring.DraftID, error) { return authoring.DraftID(next()), nil },
		NewRevisionID:  func() (authoring.RevisionID, error) { return authoring.RevisionID(next()), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type fakeCompiler struct{}

func (fakeCompiler) Compile(_ context.Context, _ string, _ string, document authoring.Dashboard) (authoringservice.Compilation, error) {
	return authoringservice.Compilation{Definition: dashboarddefinition.Definition{ID: document.ID, Title: document.Title, SemanticModel: document.SemanticModel}}, nil
}

type fakeAuthorizer struct{ err error }

func (a *fakeAuthorizer) Authorize(_ context.Context, _ authoringservice.AuthorizationRequest) error {
	return a.err
}

type fakeRepository struct {
	lifecycles  map[authoring.DashboardID]authoring.DashboardLifecycle
	revisions   map[authoring.RevisionID]authoring.Revision
	commands    map[authoring.CommandID]authoring.CommandResult
	createCalls int
	appendCalls int
	created     []authoring.CreateInput
}

func newRepository() *fakeRepository {
	return &fakeRepository{lifecycles: map[authoring.DashboardID]authoring.DashboardLifecycle{}, revisions: map[authoring.RevisionID]authoring.Revision{}, commands: map[authoring.CommandID]authoring.CommandResult{}}
}

func (r *fakeRepository) Create(_ context.Context, input authoring.CreateInput) (authoring.DashboardLifecycle, error) {
	r.createCalls++
	r.created = append(r.created, input)
	r.lifecycles[input.Lifecycle.ID] = input.Lifecycle
	r.revisions[input.Revision.ID] = input.Revision
	return input.Lifecycle, nil
}
func (r *fakeRepository) Get(_ context.Context, _ string, id authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	lifecycle, ok := r.lifecycles[id]
	if !ok {
		return authoring.DashboardLifecycle{}, authoring.ErrNotFound
	}
	return lifecycle, nil
}
func (r *fakeRepository) List(_ context.Context, workspace string) ([]authoring.DashboardLifecycle, error) {
	items := make([]authoring.DashboardLifecycle, 0, len(r.lifecycles))
	for _, lifecycle := range r.lifecycles {
		if lifecycle.WorkspaceID == workspace {
			items = append(items, lifecycle)
		}
	}
	return items, nil
}
func (r *fakeRepository) CountBySemanticModel(context.Context, string) ([]authoring.SemanticModelUsage, error) {
	return nil, nil
}
func (r *fakeRepository) GetRevision(_ context.Context, _ string, _ authoring.DashboardID, id authoring.RevisionID) (authoring.Revision, error) {
	revision, ok := r.revisions[id]
	if !ok {
		return authoring.Revision{}, authoring.ErrNotFound
	}
	return revision, nil
}
func (r *fakeRepository) LookupCommandResult(_ context.Context, _ string, _ authoring.DashboardID, evidence authoring.CommandEvidence) (authoring.CommandResult, bool, error) {
	result, ok := r.commands[evidence.ID]
	return result, ok, nil
}
func (r *fakeRepository) AppendDraft(_ context.Context, input authoring.AppendDraftInput) (authoring.Revision, error) {
	r.appendCalls++
	lifecycle := r.lifecycles[input.DashboardID]
	if lifecycle.Draft == nil || lifecycle.Draft.Revision != input.ExpectedDraftRevision {
		return authoring.Revision{}, authoring.ErrStaleRevision
	}
	r.lifecycles[input.DashboardID] = input.Next
	r.revisions[input.Revision.ID] = input.Revision
	r.commands[input.Evidence.ID] = authoring.CommandResult{Revision: input.Revision.Token()}
	return input.Revision, nil
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

func previewRepository(t *testing.T) (*fakeRepository, authoring.DashboardLifecycle, authoring.Revision) {
	t.Helper()
	document := authoredDocument("sales", "Sales")
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}
	revision, err := authoring.NewRevision("preview-revision", "sales", 1, applicationTestTime, document, provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{
		WorkspaceID: "workspace", ID: "sales", OwnerPrincipalID: "owner", Slug: "sales", Title: "Sales", SemanticModel: "sales", Visibility: authoring.VisibilityPrivate,
		Draft: &authoring.Draft{ID: "preview-draft", DashboardID: "sales", Revision: revision.Token(), Provenance: provenance},
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := newRepository()
	repository.lifecycles[lifecycle.ID] = lifecycle
	repository.revisions[revision.ID] = revision
	return repository, lifecycle, revision
}

func authoredDocument(id, title string) authoring.Dashboard {
	return authoredDocumentWithField(id, title, "order_count")
}

func exportDocument(id, title string) authoring.Dashboard {
	return authoredDocumentWithField(id, title, "orders.order_count")
}

func authoredDocumentWithField(id, title, measureField string) authoring.Dashboard {
	page := dashboard.Page{ID: "overview", Title: "Overview", Canvas: dashboard.PageCanvas{Width: 1366, Height: 940}, Grid: dashboard.PageGrid{Columns: 12, RowHeight: 48, Gap: 16}}.WithDefaults()
	page.Visuals = []dashboard.PageVisual{{ID: "orders", Kind: "visual", Visual: "orders", Placement: dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 4, RowSpan: 4}}}
	return authoring.Dashboard{
		ID: id, Title: title, SemanticModel: "sales",
		Visuals: authoring.TabularVisualizations("table", map[string]authoring.TableVisual{
			"orders": {Title: "Orders", Query: authoring.TableQuery{Table: "orders", Fields: []string{"orders.status", measureField}}},
		}),
		Pages: []dashboard.Page{page},
	}
}

func previewModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {Dimensions: map[string]semanticmodel.MetricDimension{"status": {Field: "orders.status", Type: "string"}}},
		},
		Measures: map[string]semanticmodel.MetricMeasure{"order_count": {Fact: "orders", Aggregation: "count", Input: semanticmodel.MeasureInput{Field: "orders.status"}, Empty: "zero"}},
	}
}

type fakeLease struct {
	runtime      runtimehost.Runtime
	servingState servingstate.ID
	releaseCalls int
}

func (l *fakeLease) Runtime() runtimehost.Runtime    { return l.runtime }
func (l *fakeLease) ServingStateID() servingstate.ID { return l.servingState }
func (l *fakeLease) DuckLakeSnapshotID() int64       { return 42 }
func (l *fakeLease) Release()                        { l.releaseCalls++ }

type catalogRuntime struct{ catalog dashboardcatalog.Catalog }

func (r *catalogRuntime) Close() error                      { return nil }
func (r *catalogRuntime) Catalog() dashboardcatalog.Catalog { return r.catalog }

type sourceRuntime struct {
	source projectartifact.AuthoredDashboardSource
}

func (r *sourceRuntime) Close() error { return nil }
func (r *sourceRuntime) AuthoredDashboardSource(id string) (projectartifact.AuthoredDashboardSource, bool) {
	if r.source.Document.ID != id {
		return projectartifact.AuthoredDashboardSource{}, false
	}
	return r.source, true
}

type previewRuntime struct{ model *semanticmodel.Model }

func (r *previewRuntime) Close() error { return nil }
func (r *previewRuntime) SemanticModelProjection(id string) (*semanticmodel.Model, bool) {
	if r.model == nil || r.model.Name != id {
		return nil, false
	}
	copy := *r.model
	return &copy, true
}
func (r *previewRuntime) QueryDashboardPageForDefinition(context.Context, dashboarddefinition.Definition, string, dashboard.Filters) (dashboard.Patch, error) {
	return dashboard.EmptyPatch(dashboard.Filters{}, nil), nil
}

var _ authoringservice.Compiler = fakeCompiler{}
var _ authoring.Repository = (*fakeRepository)(nil)
var _ catalog.RuntimeCatalog = (*catalogRuntime)(nil)
var _ previewservice.Runtime = (*previewRuntime)(nil)
