package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/catalog"
	previewservice "github.com/flidai/leapview/internal/dashboard/authoring/preview"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	dashboardcatalog "github.com/flidai/leapview/internal/dashboard/catalog"
	dashboardcompiler "github.com/flidai/leapview/internal/dashboard/compiler"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
)

var applicationTestTime = time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

func TestNewRequiresCompositionPorts(t *testing.T) {
	valid := application.Options{
		Authoring:       newAuthoringService(t, newRepository(), &fakeAuthorizer{}),
		Repository:      newRepository(),
		Authorizer:      &fakeAuthorizer{},
		AcquireRuntime:  func(context.Context) (runtimehost.Lease, error) { return nil, nil },
		ExportDashboard: projectcompiler.ExportDashboard,
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
	app := newApplication(t, repository, authorizer, func(context.Context) (runtimehost.Lease, error) {
		return nil, errors.New("runtime should not be acquired")
	})

	created, err := app.Create(context.Background(), authoringservice.CreateRequest{
		ProjectID: "project", ActorID: "actor", OwnerPrincipalID: "owner",
		Title: "Orders", Slug: "orders", SemanticModel: "sales", Origin: authoring.OriginUI,
	})
	if err != nil {
		t.Fatal(err)
	}
	if repository.createCalls != 1 || created.Lifecycle.ProjectID != "project" {
		t.Fatalf("create delegation = calls %d lifecycle %#v", repository.createCalls, created.Lifecycle)
	}

	title := "Orders v2"
	command := authoring.Command{
		ID: "edit-1", DashboardID: created.Lifecycle.ID, DraftID: created.Lifecycle.Draft.ID,
		ExpectedRevision: created.Revision, Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"},
		Metadata: &authoring.MetadataPatch{Title: &title},
	}
	updated, err := app.Execute(context.Background(), "project", command)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Lifecycle.Title != title || repository.appendCalls != 1 {
		t.Fatalf("execute delegation = lifecycle %#v appendCalls=%d", updated.Lifecycle, repository.appendCalls)
	}
}

func TestCatalogOperationsUseRequestedProjectAndOneLeaseEach(t *testing.T) {
	repository := newRepository()
	authorizer := &fakeAuthorizer{}
	var acquired []string
	var leases []*fakeLease
	projects := []string{"sales", "finance"}
	app := newApplication(t, repository, authorizer, func(_ context.Context) (runtimehost.Lease, error) {
		if len(projects) == 0 {
			return nil, errors.New("unexpected runtime acquisition")
		}
		project := projects[0]
		projects = projects[1:]
		acquired = append(acquired, project)
		lease := newFakeLease(t, projectgraph.ResourceID(project), &catalogRuntime{catalog: dashboardcatalog.Catalog{
			Dashboards: []dashboardcatalog.Dashboard{{ID: projectgraph.ResourceID(project + "-sales"), Title: project + " sales", SemanticModel: "sales"}},
		}})
		leases = append(leases, lease)
		return lease, nil
	})

	listed, err := app.List(context.Background(), catalog.ListRequest{ProjectID: "sales", ActorID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	if listed.Count != 1 || listed.Items[0].ProjectID != "sales" {
		t.Fatalf("catalog list = %#v", listed)
	}
	got, err := app.Get(context.Background(), catalog.GetRequest{ProjectID: "finance", ActorID: "actor", DashboardID: "finance-sales"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectID != "finance" || got.ID != "finance-sales" {
		t.Fatalf("catalog get = %#v", got)
	}
	if strings.Join(acquired, ",") != "sales,finance" {
		t.Fatalf("runtime projects = %#v", acquired)
	}
	if len(leases) != 2 || leases[0].releaseCalls != 1 || leases[1].releaseCalls != 1 {
		t.Fatalf("lease lifecycle = %#v", leases)
	}
}

func TestPreviewUsesRequestedProjectAndOneLease(t *testing.T) {
	repository, lifecycle, revision := previewRepository(t)
	authorizer := &fakeAuthorizer{}
	var acquired string
	lease := newFakeLease(t, "project", &previewRuntime{model: previewModel()})
	app := newApplication(t, repository, authorizer, func(_ context.Context) (runtimehost.Lease, error) {
		project := "project"
		acquired = project
		return lease, nil
	})

	result, err := app.Preview(context.Background(), previewservice.PreviewRequest{
		ProjectID: "project", ActorID: "actor", DashboardID: lifecycle.ID,
		DraftID: "preview-draft", ExpectedRevision: revision.Token(), PageID: "overview",
	})
	if err != nil {
		t.Fatal(err)
	}
	if acquired != "project" || lease.releaseCalls != 1 || result.Revision != revision.Token() {
		t.Fatalf("preview scope/result = project %q releases %d result %#v", acquired, lease.releaseCalls, result)
	}
}

func TestProjectExportNormalizesSourceProjectAndDoesNotCrossProject(t *testing.T) {
	repository := newRepository()
	authorizer := &fakeAuthorizer{}
	sources := map[string]authoring.AuthoredDashboardSource{
		"sales": {
			Document: exportDocument("sales-dashboard", "Sales"),
			Metadata: authoring.AuthoredDashboardMetadata{Project: "sales", Name: "sales-dashboard", Title: "Sales"},
		},
		"finance": {
			Document: exportDocument("finance-dashboard", "Finance"),
			Metadata: authoring.AuthoredDashboardMetadata{Project: "finance", Name: "finance-dashboard", Title: "Finance"},
		},
	}
	var acquired []string
	var leases []*fakeLease
	projects := []string{"sales", "finance"}
	app := newApplication(t, repository, authorizer, func(_ context.Context) (runtimehost.Lease, error) {
		if len(projects) == 0 {
			return nil, errors.New("unexpected runtime acquisition")
		}
		project := projects[0]
		projects = projects[1:]
		acquired = append(acquired, project)
		lease := newFakeLease(t, projectgraph.ResourceID(project), &sourceRuntime{source: sources[project]})
		leases = append(leases, lease)
		return lease, nil
	})

	sales, err := app.ExportYAML(context.Background(), sourceadapter.ExportRequest{
		Source: sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, ProjectID: "sales", DashboardID: "sales-dashboard"}, ActorID: "actor",
	})
	if err != nil {
		t.Fatal(err)
	}
	finance, err := app.ExportYAML(context.Background(), sourceadapter.ExportRequest{
		Source: sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, ProjectID: "finance", DashboardID: "finance-dashboard"}, ActorID: "actor",
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

func TestDraftExportNormalizesProjectAndUsesCurrentLifecycleDraft(t *testing.T) {
	repository, lifecycle, revision := previewRepository(t)
	draft, err := authoring.NewRevision(revision.ID, lifecycle.ID, revision.Number, revision.CreatedAt, exportDocument(string(lifecycle.ID), lifecycle.Title), revision.Provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.Draft.Revision = draft.Token()
	repository.revisions[draft.ID] = draft
	repository.lifecycles[lifecycle.ID] = lifecycle
	authorizer := &fakeAuthorizer{}
	app := newApplication(t, repository, authorizer, func(context.Context) (runtimehost.Lease, error) {
		return nil, errors.New("runtime should not be acquired")
	})

	exported, err := app.ExportDraftYAML(context.Background(), sourceadapter.ExportRequest{
		Source: sourceadapter.SourceRef{Kind: sourceadapter.SourceInstance, ProjectID: "project", DashboardID: lifecycle.ID}, ActorID: "actor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(exported), "title: Sales") || !strings.Contains(string(exported), "name: sales") {
		t.Fatalf("draft export = %s", exported)
	}
}

func TestProjectForkRejectsCrossProjectTarget(t *testing.T) {
	repository := newRepository()
	authorizer := &fakeAuthorizer{}
	app := newApplication(t, repository, authorizer, func(context.Context) (runtimehost.Lease, error) {
		return nil, errors.New("runtime should not be acquired")
	})
	_, err := app.Fork(context.Background(), sourceadapter.ForkRequest{
		Source:          sourceadapter.SourceRef{Kind: sourceadapter.SourceInstance, ProjectID: "project", DashboardID: "sales"},
		TargetProjectID: "other", ActorID: "actor",
	})
	if err == nil || !strings.Contains(err.Error(), "must remain in the source project") {
		t.Fatalf("cross-project fork error = %v", err)
	}
}

func TestExecuteIntentRejectsGenericPayloadAndKeepsNonFieldIntentsLeaseFree(t *testing.T) {
	repository, lifecycle, revision := previewRepository(t)
	authorizer := &recordingAuthorizer{}
	acquired := 0
	app := newApplication(t, repository, authorizer, func(context.Context) (runtimehost.Lease, error) {
		acquired++
		return nil, errors.New("unexpected runtime acquisition")
	})
	command := intentCommandForApplication(revision, lifecycle.Draft.ID, "intent-page", &authoring.AddPagePayload{})
	if _, err := app.ExecuteIntent(context.Background(), application.IntentRequest{ProjectID: "project", ActorID: "actor", Command: command}); err != nil {
		t.Fatal(err)
	}
	if acquired != 0 || repository.appendCalls != 1 {
		t.Fatalf("non-field intent acquired runtime=%d append=%d", acquired, repository.appendCalls)
	}
	title := "unsafe"
	generic := intentCommandForApplication(revision, lifecycle.Draft.ID, "intent-generic", &authoring.MetadataPatch{Title: &title})
	if _, err := app.ExecuteIntent(context.Background(), application.IntentRequest{ProjectID: "project", ActorID: "actor", Command: generic}); !errors.Is(err, authoring.ErrInvalidPayload) {
		t.Fatalf("generic payload error = %v", err)
	}
}

func TestExecuteIntentAuthorizesBeforeDraftRevisionAndRuntimeReads(t *testing.T) {
	repository, lifecycle, revision := previewRepository(t)
	authorizer := &recordingAuthorizer{err: access.ErrForbidden}
	acquired := 0
	app := newApplication(t, repository, authorizer, func(context.Context) (runtimehost.Lease, error) {
		acquired++
		return nil, errors.New("runtime should not be acquired")
	})
	command := intentCommandForApplication(revision, lifecycle.Draft.ID, "intent-field", &authoring.AssignFieldPayload{PageID: "overview", VisualID: "orders", FieldID: "order_count", Role: authoring.FieldRoleMeasure})
	if _, err := app.ExecuteIntent(context.Background(), application.IntentRequest{ProjectID: "project", ActorID: "actor", Command: command}); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("denied intent error = %v", err)
	}
	if repository.getRevisionCalls != 0 || acquired != 0 {
		t.Fatalf("denied intent leaked reads: getRevision=%d runtime=%d", repository.getRevisionCalls, acquired)
	}
}

func TestExecuteIntentAssignFieldUsesOneLeaseAndGovernedRole(t *testing.T) {
	repository, lifecycle, revision := previewRepository(t)
	authorizer := &recordingAuthorizer{}
	lease := newFakeLease(t, "project", &previewRuntime{model: previewModel()})
	app := newApplication(t, repository, authorizer, func(context.Context) (runtimehost.Lease, error) { return lease, nil })
	command := intentCommandForApplication(revision, lifecycle.Draft.ID, "intent-field", &authoring.AssignFieldPayload{PageID: "overview", VisualID: "orders", FieldID: "order_count", Role: authoring.FieldRoleMeasure})
	if _, err := app.ExecuteIntent(context.Background(), application.IntentRequest{ProjectID: "project", ActorID: "actor", Command: command}); err != nil {
		t.Fatal(err)
	}
	if lease.releaseCalls != 1 || repository.appendCalls != 1 {
		t.Fatalf("field intent lease/revision = release=%d append=%d", lease.releaseCalls, repository.appendCalls)
	}
	// A role mismatch is rejected against the governed model before the
	// transactional service can append a partial revision.
	current := repository.lifecycles[lifecycle.ID].Draft.Revision
	bad := intentCommandForApplication(repository.revisions[current.RevisionID], lifecycle.Draft.ID, "intent-field-bad", &authoring.AssignFieldPayload{PageID: "overview", VisualID: "orders", FieldID: "orders.status", Role: authoring.FieldRoleMeasure})
	lease.releaseCalls = 0
	if _, err := app.ExecuteIntent(context.Background(), application.IntentRequest{ProjectID: "project", ActorID: "actor", Command: bad}); !errors.Is(err, authoring.ErrInvalidPayload) {
		t.Fatalf("spoofed role error = %v", err)
	}
	if lease.releaseCalls != 1 || repository.appendCalls != 1 {
		t.Fatalf("spoofed role mutated state: release=%d append=%d", lease.releaseCalls, repository.appendCalls)
	}
	// A durable command retry replays before field/runtime validation even
	// though the draft pointer has advanced.
	if _, err := app.ExecuteIntent(context.Background(), application.IntentRequest{ProjectID: "project", ActorID: "actor", Command: command}); err != nil {
		t.Fatalf("idempotent field retry error = %v", err)
	}
	if lease.releaseCalls != 1 || repository.appendCalls != 1 {
		t.Fatalf("idempotent field retry reacquired or appended: release=%d append=%d", lease.releaseCalls, repository.appendCalls)
	}
}

func TestExecuteIntentAssignFieldInfersTableAndCompilesTableVisual(t *testing.T) {
	repository, lifecycle, current := previewRepository(t)
	document, err := current.Document.Clone()
	if err != nil {
		t.Fatal(err)
	}
	document.Visuals["orders"] = authoring.TabularVisualization("table", authoring.TableVisual{Title: "Customers"})
	current, err = authoring.NewRevision("table-revision", current.DashboardID, current.Number, current.CreatedAt, document, current.Provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.Draft.Revision = current.Token()
	repository.lifecycles[lifecycle.ID] = lifecycle
	repository.revisions[current.ID] = current

	model := previewModel()
	model.Tables["customers"] = semanticmodel.Table{Dimensions: map[string]semanticmodel.MetricDimension{
		"customer_id": {Field: "customers.customer_id", Table: "customers", Name: "customer_id", Type: "string"},
	}}
	lease := newFakeLease(t, "project", &previewRuntime{model: model})
	app := newApplication(t, repository, &recordingAuthorizer{}, func(context.Context) (runtimehost.Lease, error) { return lease, nil })
	command := intentCommandForApplication(current, lifecycle.Draft.ID, "intent-customers-field", &authoring.AssignFieldPayload{
		PageID: "overview", VisualID: "orders", FieldID: "customers.customer_id", Role: authoring.FieldRoleDimension,
	})
	result, err := app.ExecuteIntent(context.Background(), application.IntentRequest{ProjectID: "project", ActorID: "actor", Command: command})
	if err != nil {
		t.Fatal(err)
	}
	updated := repository.revisions[result.Revision.RevisionID].Document
	visual := updated.Visuals["orders"]
	if visual.Tabular == nil || visual.Tabular.Query.Table != "customers" || len(visual.Tabular.Query.Columns) != 1 || visual.Tabular.Query.Columns[0].Field != "customers.customer_id" {
		t.Fatalf("assigned table visual = %#v", visual)
	}
	if _, err := dashboardcompiler.Compile(updated, map[string]*semanticmodel.Model{"sales": model}); err != nil {
		t.Fatalf("compiled builder table visual: %v", err)
	}
	appendCalls, releaseCalls := repository.appendCalls, lease.releaseCalls
	if _, err := app.ExecuteIntent(context.Background(), application.IntentRequest{ProjectID: "project", ActorID: "actor", Command: command}); err != nil {
		t.Fatalf("idempotent table-field retry: %v", err)
	}
	if repository.appendCalls != appendCalls || lease.releaseCalls != releaseCalls {
		t.Fatalf("idempotent table-field retry mutated or reacquired: append=%d/%d release=%d/%d", repository.appendCalls, appendCalls, lease.releaseCalls, releaseCalls)
	}
}

func TestBuilderIntentServerIDsExportCanonicalDraft(t *testing.T) {
	repository, lifecycle, current := previewRepository(t)
	// The preview fixture intentionally exercises the builder's short measure
	// aliases. Export's strict contract requires qualified table.field refs, so
	// make the pre-existing visual canonical before testing the newly added one.
	document, err := current.Document.Clone()
	if err != nil {
		t.Fatal(err)
	}
	document.Visuals["orders"].Tabular.Query.Fields[1] = "orders.order_count"
	current, err = authoring.NewRevision("canonical-base", current.DashboardID, current.Number, current.CreatedAt, document, current.Provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.Draft.Revision = current.Token()
	repository.lifecycles[lifecycle.ID] = lifecycle
	repository.revisions[current.ID] = current
	model := previewModel()
	model.Tables["customers"] = semanticmodel.Table{Dimensions: map[string]semanticmodel.MetricDimension{
		"customer_id": {Field: "customers.customer_id", Table: "customers", Name: "customer_id", Type: "string"},
	}}
	lease := newFakeLease(t, "project", &previewRuntime{model: model})
	app := newApplication(t, repository, &recordingAuthorizer{}, func(context.Context) (runtimehost.Lease, error) { return lease, nil })

	pageResult, err := app.ExecuteIntent(context.Background(), application.IntentRequest{
		ProjectID: "project", ActorID: "actor",
		Command: intentCommandForApplication(current, lifecycle.Draft.ID, "intent-add-page", &authoring.AddPagePayload{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	current = repository.revisions[pageResult.Revision.RevisionID]
	pageID := current.Document.Pages[len(current.Document.Pages)-1].ID
	if pageID != "page-2" {
		t.Fatalf("server page ID = %q", pageID)
	}

	visualResult, err := app.ExecuteIntent(context.Background(), application.IntentRequest{
		ProjectID: "project", ActorID: "actor",
		Command: intentCommandForApplication(current, lifecycle.Draft.ID, "intent-add-table", &authoring.AddVisualPayload{PageID: pageID, Type: "table"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	current = repository.revisions[visualResult.Revision.RevisionID]
	page := current.Document.Pages[len(current.Document.Pages)-1]
	component := page.Visuals[len(page.Visuals)-1]
	if component.Visual != "visual_2" || component.ID != "visual_2_tile" {
		t.Fatalf("server visual/component IDs = visual %q component %q", component.Visual, component.ID)
	}

	fieldResult, err := app.ExecuteIntent(context.Background(), application.IntentRequest{
		ProjectID: "project", ActorID: "actor",
		Command: intentCommandForApplication(current, lifecycle.Draft.ID, "intent-assign-customer", &authoring.AssignFieldPayload{
			PageID: pageID, VisualID: component.ID, FieldID: "customers.customer_id", Role: authoring.FieldRoleDimension,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.ExportDraftYAML(context.Background(), sourceadapter.ExportRequest{
		Source: sourceadapter.SourceRef{Kind: sourceadapter.SourceInstance, ProjectID: "project", DashboardID: lifecycle.ID}, ActorID: "actor",
	}); err != nil {
		t.Fatalf("export server-generated builder IDs: %v", err)
	}
	if fieldResult.Lifecycle.Draft == nil || fieldResult.Lifecycle.Draft.Revision != repository.lifecycles[lifecycle.ID].Draft.Revision {
		t.Fatal("field intent did not return current lifecycle")
	}
}

func TestExecuteIntentAssignFieldDoesNotRewriteExistingFactForAnotherGovernedTable(t *testing.T) {
	repository, lifecycle, current := previewRepository(t)
	model := previewModel()
	model.Tables["customers"] = semanticmodel.Table{Dimensions: map[string]semanticmodel.MetricDimension{
		"customer_id": {Field: "customers.customer_id", Table: "customers", Name: "customer_id", Type: "string"},
	}}
	lease := newFakeLease(t, "project", &previewRuntime{model: model})
	app := newApplication(t, repository, &recordingAuthorizer{}, func(context.Context) (runtimehost.Lease, error) { return lease, nil })
	command := intentCommandForApplication(current, lifecycle.Draft.ID, "intent-cross-table-field", &authoring.AssignFieldPayload{
		PageID: "overview", VisualID: "orders", FieldID: "customers.customer_id", Role: authoring.FieldRoleDimension,
	})
	result, err := app.ExecuteIntent(context.Background(), application.IntentRequest{ProjectID: "project", ActorID: "actor", Command: command})
	if err != nil {
		t.Fatal(err)
	}
	visual := repository.revisions[result.Revision.RevisionID].Document.Visuals["orders"]
	if visual.Tabular == nil || visual.Tabular.Query.Table != "orders" || len(visual.Tabular.Query.Columns) != 1 || visual.Tabular.Query.Columns[0].Field != "customers.customer_id" {
		t.Fatalf("cross-table assignment rewrote fact: %#v", visual)
	}
}

func intentCommandForApplication(revision authoring.Revision, draftID authoring.DraftID, id string, payload authoringPayloadForApplication) authoring.Command {
	command := authoring.Command{ID: authoring.CommandID(id), DashboardID: revision.DashboardID, DraftID: draftID, ExpectedRevision: revision.Token(), Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}}
	switch value := payload.(type) {
	case *authoring.AddPagePayload:
		command.AddPage = value
	case *authoring.AddVisualPayload:
		command.AddVisual = value
	case *authoring.AssignFieldPayload:
		command.AssignField = value
	case *authoring.MetadataPatch:
		command.Metadata = value
	}
	return command
}

type authoringPayloadForApplication interface{}

type recordingAuthorizer struct{ err error }

func (a *recordingAuthorizer) Authorize(context.Context, authoringservice.AuthorizationRequest) error {
	return a.err
}

func newApplication(t *testing.T, repository *fakeRepository, authorizer authoringservice.Authorizer, acquire sourceadapter.AcquireRuntime) *application.Application {
	t.Helper()
	authoringSvc := newAuthoringService(t, repository, authorizer)
	app, err := application.New(application.Options{Authoring: authoringSvc, Repository: repository, Authorizer: authorizer, AcquireRuntime: acquire, ExportDashboard: projectcompiler.ExportDashboard})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func newAuthoringService(t *testing.T, repository *fakeRepository, authorizer authoringservice.Authorizer) *authoringservice.Service {
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

func (fakeCompiler) Compile(_ context.Context, _ projectgraph.ResourceID, _ projectgraph.ResourceID, document authoring.Dashboard) (authoringservice.Compilation, error) {
	return authoringservice.Compilation{Definition: dashboarddefinition.Definition{ID: document.ID.String(), Title: document.Title, SemanticModel: document.SemanticModel.String()}}, nil
}

type fakeAuthorizer struct{ err error }

func (a *fakeAuthorizer) Authorize(_ context.Context, _ authoringservice.AuthorizationRequest) error {
	return a.err
}

type fakeRepository struct {
	lifecycles       map[authoring.DashboardID]authoring.DashboardLifecycle
	revisions        map[authoring.RevisionID]authoring.Revision
	commands         map[authoring.CommandID]authoring.CommandResult
	createCalls      int
	appendCalls      int
	created          []authoring.CreateInput
	getRevisionCalls int
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
func (r *fakeRepository) Get(_ context.Context, _ projectgraph.ResourceID, id authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	lifecycle, ok := r.lifecycles[id]
	if !ok {
		return authoring.DashboardLifecycle{}, authoring.ErrNotFound
	}
	return lifecycle, nil
}
func (r *fakeRepository) List(_ context.Context, project projectgraph.ResourceID) ([]authoring.DashboardLifecycle, error) {
	items := make([]authoring.DashboardLifecycle, 0, len(r.lifecycles))
	for _, lifecycle := range r.lifecycles {
		if lifecycle.ProjectID == project {
			items = append(items, lifecycle)
		}
	}
	return items, nil
}
func (r *fakeRepository) CountBySemanticModel(context.Context, projectgraph.ResourceID) ([]authoring.SemanticModelUsage, error) {
	return nil, nil
}
func (r *fakeRepository) GetRevision(_ context.Context, _ projectgraph.ResourceID, _ authoring.DashboardID, id authoring.RevisionID) (authoring.Revision, error) {
	r.getRevisionCalls++
	revision, ok := r.revisions[id]
	if !ok {
		return authoring.Revision{}, authoring.ErrNotFound
	}
	return revision, nil
}
func (r *fakeRepository) LookupCommandResult(_ context.Context, _ projectgraph.ResourceID, _ authoring.DashboardID, evidence authoring.CommandEvidence) (authoring.CommandResult, bool, error) {
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
func (r *fakeRepository) GetPublishedCompilation(context.Context, projectgraph.ResourceID, authoring.DashboardID) (authoring.CompiledRevision, error) {
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
		ProjectID: "project", ID: "sales", OwnerPrincipalID: "owner", Slug: "sales", Title: "Sales", SemanticModel: "sales", Visibility: authoring.VisibilityPrivate,
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
		ID: projectgraph.ResourceID(id), Title: title, SemanticModel: "sales",
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
	identity     projectgraph.ServingIdentity
	releaseCalls int
}

func (l *fakeLease) Runtime() runtimehost.Runtime           { return l.runtime }
func (l *fakeLease) Identity() projectgraph.ServingIdentity { return l.identity }
func (l *fakeLease) Release()                               { l.releaseCalls++ }

func newFakeLease(t *testing.T, projectID projectgraph.ResourceID, runtime runtimehost.Runtime) *fakeLease {
	t.Helper()
	identity, err := projectgraph.NewServingIdentity(projectID, "production", "state-"+projectID.String())
	if err != nil {
		t.Fatal(err)
	}
	return &fakeLease{runtime: runtime, identity: identity}
}

type catalogRuntime struct{ catalog dashboardcatalog.Catalog }

func (r *catalogRuntime) Close() error                      { return nil }
func (r *catalogRuntime) Catalog() dashboardcatalog.Catalog { return r.catalog }

type sourceRuntime struct {
	source authoring.AuthoredDashboardSource
}

func (r *sourceRuntime) Close() error { return nil }
func (r *sourceRuntime) AuthoredDashboardSource(id string) (authoring.AuthoredDashboardSource, bool) {
	if r.source.Document.ID.String() != id {
		return authoring.AuthoredDashboardSource{}, false
	}
	return r.source, true
}

type previewRuntime struct{ model *semanticmodel.Model }

func (r *previewRuntime) Close() error { return nil }
func (r *previewRuntime) SemanticModelProjection(id projectgraph.ResourceID) (*semanticmodel.Model, bool) {
	if r.model == nil || r.model.Name != id.String() {
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
