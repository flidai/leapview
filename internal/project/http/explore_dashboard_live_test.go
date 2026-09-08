package http

import (
	"context"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	analyticsgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringapplication "github.com/flidai/leapview/internal/dashboard/authoring/application"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	authoringsqlite "github.com/flidai/leapview/internal/dashboard/authoring/sqlite"
	dashboardcatalog "github.com/flidai/leapview/internal/dashboard/catalog"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/platform"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	"github.com/go-chi/chi/v5"
)

// TestExploreFromDashboardMountedWorkflow exercises the mounted browser
// boundary against the real repository, authoring service, source adapter,
// and reverse adapter. The runtime/compiler ports are intentionally small
// serving-state fixtures; append success is never mocked at the transport.
func TestExploreFromDashboardMountedWorkflow(t *testing.T) {
	ctx := t.Context()
	identity, err := graphIdentityForLiveHandoff()
	if err != nil {
		t.Fatal(err)
	}
	modelValue, compiled := liveHandoffModel(t)
	repository, closeStore := liveHandoffRepository(t, ctx)
	t.Cleanup(closeStore)
	authorizer := &liveHandoffAuthorizer{denyProjectView: true}
	runtime := &liveHandoffRuntime{identity: identity, model: modelValue, compiled: compiled}
	compiler := liveHandoffCompiler{identity: identity}
	serviceValue, err := authoringservice.NewService(authoringservice.Options{
		Repository: repository, Authorizer: authorizer, Compiler: compiler,
		Now:            func() time.Time { return time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC) },
		NewDashboardID: func() (authoring.DashboardID, error) { return "unused-dashboard", nil },
		NewDraftID:     func() (authoring.DraftID, error) { return "draft:sales", nil },
		NewRevisionID:  liveHandoffRevisionIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	app, err := authoringapplication.New(authoringapplication.Options{
		Authoring: serviceValue, Repository: repository, Authorizer: authorizer,
		Compiler: compiler, AcquireRuntime: func(context.Context) (projectruntime.Lease, error) {
			return &liveHandoffLease{runtime: runtime, identity: identity}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := app.Create(ctx, authoringservice.CreateRequest{
		ProjectID: "project:sales", ActorID: "principal:alice", OwnerPrincipalID: "principal:alice",
		DashboardID: "dashboard:sales", Title: "Sales", Slug: "sales", SemanticModel: "semantic:sales",
		Visibility: authoring.VisibilityPrivate, Origin: authoring.OriginUI, IdempotencyKey: "create-live-sales",
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	if created.Lifecycle.Draft == nil || created.Lifecycle.Status != authoring.LifecycleStatusDraft {
		t.Fatalf("create lifecycle = %#v", created.Lifecycle)
	}
	target, err := app.ExplorationTarget(ctx, authoringapplication.ExplorationTargetRequest{ProjectID: "project:sales", ActorID: "principal:alice", DashboardID: "dashboard:sales"})
	if err != nil {
		t.Fatalf("load target: %v", err)
	}
	if target.RevisionToken == "" || len(target.Pages) != 1 || target.Pages[0].ID != "overview" {
		t.Fatalf("target = %#v", target)
	}

	spec := liveHandoffSpec()
	command := addExplorationToDashboardCommand{
		DashboardID: "dashboard:sales", RevisionToken: target.RevisionToken, PageID: "overview",
		PlacementChoice: "half", Spec: spec,
	}
	router := chi.NewRouter()
	(&BrowserHandler{
		DashboardAuthoring:        app,
		DashboardAuthoringCommand: analyticsgen.GenUIActionExecuteDashboardAuthoringCommand(),
		ProjectDefinitionReader:   &liveHandoffDefinitionReader{model: modelValue, compiled: compiled},
		ResolveProjectID:          func(context.Context) (projectgraph.ResourceID, error) { return "project:sales", nil },
		CurrentUser:               func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "principal:alice"}, true },
		MutationMiddleware:        func(next stdhttp.Handler) stdhttp.Handler { return next },
	}).MountAuthenticated(router)

	appendResponse := httptest.NewRecorder()
	router.ServeHTTP(appendResponse, addDashboardRequest(t, command))
	if appendResponse.Code != stdhttp.StatusOK {
		t.Fatalf("mounted append status=%d body=%q", appendResponse.Code, appendResponse.Body.String())
	}

	lifecycle, err := repository.Get(ctx, "project:sales", "dashboard:sales")
	if err != nil || lifecycle.Draft == nil {
		t.Fatalf("draft after append = %#v (%v)", lifecycle, err)
	}
	appendedRevision, err := repository.GetRevision(ctx, "project:sales", "dashboard:sales", lifecycle.Draft.Revision.RevisionID)
	if err != nil {
		t.Fatal(err)
	}
	visualID, componentID := liveHandoffAppendedVisual(t, appendedRevision.Document)
	if len(appendedRevision.Document.Spec.Visuals) != 1 || visualID == "" || componentID == "" {
		t.Fatalf("appended visual/component = %q/%q document=%#v", visualID, componentID, appendedRevision.Document)
	}
	if filters := appendedRevision.Document.Spec.Filters; len(filters) != 1 || filters[0].Targets == nil || !reflect.DeepEqual(*filters[0].Targets, []string{visualID}) {
		t.Fatalf("scoped appended filters = %#v", filters)
	}

	// A retry with the same transport request identity must replay the durable
	// command, while a different request identity carrying the old CAS token
	// must be rejected without creating another visual.
	replay, err := app.AppendExploration(ctx, authoringapplication.ExplorationAppendRequest{
		ProjectID: "project:sales", ActorID: "principal:alice", DashboardID: "dashboard:sales", PageID: "overview",
		RevisionToken: target.RevisionToken, RequestID: "add-exploration-test-1", PlacementChoice: "half", Spec: spec,
	})
	if err != nil || replay.Revision != lifecycle.Draft.Revision {
		t.Fatalf("append replay = %#v (%v), want revision %#v", replay, err, lifecycle.Draft.Revision)
	}
	_, err = app.AppendExploration(ctx, authoringapplication.ExplorationAppendRequest{
		ProjectID: "project:sales", ActorID: "principal:alice", DashboardID: "dashboard:sales", PageID: "overview",
		RevisionToken: target.RevisionToken, RequestID: "stale-exploration-request", PlacementChoice: "half", Spec: spec,
	})
	if !errors.Is(err, authoring.ErrStaleRevision) {
		t.Fatalf("stale append error=%v, want ErrStaleRevision", err)
	}
	unchanged, err := repository.Get(ctx, "project:sales", "dashboard:sales")
	if err != nil || unchanged.Draft == nil {
		t.Fatal(err)
	}
	unchangedRevision, err := repository.GetRevision(ctx, "project:sales", "dashboard:sales", unchanged.Draft.Revision.RevisionID)
	if err != nil || len(unchangedRevision.Document.Spec.Visuals) != 1 {
		t.Fatalf("stale/replay changed draft = %#v (%v)", unchangedRevision.Document.Spec.Visuals, err)
	}

	publishCommand := authoring.Command{
		ID: "publish-live-sales", DashboardID: "dashboard:sales", DraftID: unchanged.Draft.ID,
		ExpectedRevision: unchanged.Draft.Revision,
		Provenance:       authoring.Provenance{Origin: authoring.OriginUI, ActorID: "principal:alice"},
		Publish:          &authoring.PublishPayload{},
	}
	published, err := app.Execute(ctx, "project:sales", publishCommand)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if published.Lifecycle.Status != authoring.LifecycleStatusPublished || published.Lifecycle.Published == nil || published.Lifecycle.Published.Revision != unchanged.Draft.Revision {
		t.Fatalf("published lifecycle = %#v", published.Lifecycle)
	}

	// Advance only the unpublished draft metadata. The source adapter used by
	// the handoff must continue to read the exact publication pointer.
	draftTitle := "Unpublished local title"
	current, err := repository.Get(ctx, "project:sales", "dashboard:sales")
	if err != nil || current.Draft == nil {
		t.Fatal(err)
	}
	if _, err := app.Execute(ctx, "project:sales", authoring.Command{
		ID: "edit-unpublished-title", DashboardID: current.ID, DraftID: current.Draft.ID,
		ExpectedRevision: current.Draft.Revision, Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "principal:alice"},
		Metadata: &authoring.MetadataPatch{Title: &draftTitle},
	}); err != nil {
		t.Fatalf("advance unpublished draft: %v", err)
	}
	handoffResponse := httptest.NewRecorder()
	router.ServeHTTP(handoffResponse, httptest.NewRequest(stdhttp.MethodGet, "/dashboards/dashboard:sales/pages/overview/components/"+componentID+"/explore", nil))
	if handoffResponse.Code != stdhttp.StatusSeeOther {
		t.Fatalf("mounted handoff status=%d body=%q", handoffResponse.Code, handoffResponse.Body.String())
	}
	publishedSource, err := app.LoadPublishedDashboardSource(ctx, "project:sales", "dashboard:sales", "principal:alice")
	if err != nil {
		t.Fatalf("published source: %v", err)
	}
	if publishedSource.Provenance.Instance == nil || publishedSource.Provenance.Instance.DraftRevision != nil || publishedSource.Provenance.Instance.PublishedRevision != unchanged.Draft.Revision || publishedSource.Document.Metadata.DisplayName == nil || *publishedSource.Document.Metadata.DisplayName != "Sales" {
		t.Fatalf("published source provenance/document = %#v/%#v", publishedSource.Provenance, publishedSource.Document.Metadata)
	}
	location, err := url.Parse(handoffResponse.Header().Get("Location"))
	if err != nil || location.Path != "/explore" {
		t.Fatalf("handoff location=%q err=%v", handoffResponse.Header().Get("Location"), err)
	}
	query := location.Query()
	if query.Get("returnSurface") != string(ExploreReturnDashboard) || query.Get("returnDashboard") != "dashboard:sales" || query.Get("returnPage") != "overview" {
		t.Fatalf("safe return context=%#v", query)
	}
	handoffCommand, err := dataExploreCommandFromQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	if handoffCommand.Spec.ModelID != spec.ModelID || handoffCommand.Spec.DatasetID == nil || *handoffCommand.Spec.DatasetID != "orders" {
		t.Fatalf("canonical identity = %#v", handoffCommand.Spec)
	}
	if handoffCommand.Spec.Time == nil || spec.Time == nil || handoffCommand.Spec.Time.Field != spec.Time.Field || handoffCommand.Spec.Time.Grain != spec.Time.Grain || !reflect.DeepEqual(handoffCommand.Spec.Time.Range, spec.Time.Range) || !reflect.DeepEqual(handoffCommand.Spec.Sort, spec.Sort) || !reflect.DeepEqual(handoffCommand.Spec.Filters, spec.Filters) {
		t.Fatalf("canonical analytical state changed: got=%#v want=%#v", handoffCommand.Spec, spec)
	}
	if len(handoffCommand.Spec.Dimensions) != len(spec.Dimensions) || len(handoffCommand.Spec.Metrics) != len(spec.Metrics) {
		t.Fatalf("canonical selections changed: got=%#v want=%#v", handoffCommand.Spec, spec)
	}
	for i := range spec.Dimensions {
		gotDimension, wantDimension := handoffCommand.Spec.Dimensions[i], spec.Dimensions[i]
		if gotDimension.Field != wantDimension.Field || (gotDimension.Grain == nil) != (wantDimension.Grain == nil) || (gotDimension.Grain != nil && *gotDimension.Grain != *wantDimension.Grain) {
			t.Fatalf("canonical dimension %d = %#v want %#v", i, handoffCommand.Spec.Dimensions[i], spec.Dimensions[i])
		}
	}
	if handoffCommand.Spec.Metrics[0].Field != spec.Metrics[0].Field {
		t.Fatalf("canonical metric = %#v want %#v", handoffCommand.Spec.Metrics, spec.Metrics)
	}

	if len(authorizer.calls) == 0 || authorizer.calls[len(authorizer.calls)-1].Target != authoringservice.AuthorizationTargetAuthoredDashboard || authorizer.calls[len(authorizer.calls)-1].Action != authoring.AuthorizationActionView || authorizer.calls[len(authorizer.calls)-1].OwnerPrincipalID != "principal:alice" || authorizer.calls[len(authorizer.calls)-1].Visibility != authoring.VisibilityPrivate {
		t.Fatalf("published source authorization calls = %#v", authorizer.calls)
	}
	if !authorizer.sawAction(authoringservice.AuthorizationTargetSemanticModel, authoring.AuthorizationActionUse) {
		t.Fatalf("semantic model USE authorization was not exercised: %#v", authorizer.calls)
	}
}

func liveHandoffSpec() exploration.ExplorationSpec {
	dataset := "orders"
	grain := exploration.ExplorationTimeGrainMonth
	return exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}, {Field: "orders.created_at", Grain: &grain}},
		Metrics:    []exploration.ExplorationMetricRef{{Field: "revenue"}},
		Filters:    []exploration.ExplorationFilter{{Field: "orders.status", DatasetID: &dataset, Expression: exploration.ExplorationFilterExpression{Value: &exploration.ComparisonExplorationFilterExpression{Kind: "comparison", Operator: "equals", Value: exploration.ExplorationFilterValue{Value: &exploration.StringExplorationFilterValue{Kind: "string", Value: "paid"}}}}}},
		Time:       &exploration.ExplorationTimeSelection{Field: "orders.created_at", Grain: grain},
		Sort:       []exploration.ExplorationSort{{Field: "orders.status", Direction: exploration.ExplorationSortDirectionAsc}}, Limit: 100,
		Visualization: &exploration.ExplorationVisualizationConfig{Value: &exploration.CartesianExplorationVisualization{
			ExplorationVisualizationConfigBase: exploration.ExplorationVisualizationConfigBase{Kind: "cartesian"}, Kind: "cartesian",
			Mark: exploration.ExplorationVisualizationCartesianMarkLine,
			X:    &exploration.ExplorationVisualizationFieldRef{Field: "orders.status"},
			Y:    &[]exploration.ExplorationVisualizationFieldRef{{Field: "revenue"}},
		}},
	}
}

func liveHandoffModel(t *testing.T) (*semanticmodel.Model, *semanticquery.CompiledModel) {
	t.Helper()
	modelValue, _ := exploreFromModel(t)
	table := modelValue.Tables["orders"]
	entity := table.Entities["order"]
	entity.Fields = append(entity.Fields, "created_at")
	table.Entities["order"] = entity
	table.Dimensions["created_at"] = semanticmodel.MetricDimension{Type: "date", Datatype: semanticmodel.DataTypeDate}
	modelValue.Tables["orders"] = table
	modelValue.Dimensions["created_at"] = semanticmodel.SemanticDimension{Type: "date", Datatype: semanticmodel.DataTypeDate, NativeGrain: "day", Grains: []string{"day", "month"}, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.created_at"}}}
	compiled, err := semanticquery.CompileModel(modelValue)
	if err != nil {
		t.Fatal(err)
	}
	return modelValue, compiled
}

func graphIdentityForLiveHandoff() (projectgraph.ServingIdentity, error) {
	return projectgraph.NewServingIdentity("project:sales", "production", "generation:live-handoff")
}

func liveHandoffRepository(t *testing.T, ctx context.Context) (*authoringsqlite.Repository, func()) {
	t.Helper()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "authoring.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES ('principal:alice', 'alice@example.test', 'Alice')`); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	return authoringsqlite.NewRepository(store.SQLDB()), func() { _ = store.Close() }
}

func liveHandoffRevisionIDs() func() (authoring.RevisionID, error) {
	ids := []authoring.RevisionID{"revision-1", "revision-2", "revision-3"}
	return func() (authoring.RevisionID, error) {
		if len(ids) == 0 {
			return "", errors.New("revision id generator exhausted")
		}
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
}

func liveHandoffAppendedVisual(t *testing.T, value document.DashboardDocument) (string, string) {
	t.Helper()
	if len(value.Spec.Pages) != 1 || len(value.Spec.Pages[0].Components) != 1 {
		t.Fatalf("appended page/components = %#v", value.Spec.Pages)
	}
	component, ok := value.Spec.Pages[0].Components[0].Value.(*document.VisualDashboardPageComponent)
	if !ok || component == nil {
		t.Fatalf("appended component = %#v", value.Spec.Pages[0].Components[0])
	}
	if _, ok := value.Spec.Visuals[component.Visual]; !ok {
		t.Fatalf("component visual %q missing from definitions", component.Visual)
	}
	return component.Visual, component.ID
}

type liveHandoffAuthorizer struct {
	calls           []authoringservice.AuthorizationRequest
	denyProjectView bool
}

func (a *liveHandoffAuthorizer) Authorize(_ context.Context, request authoringservice.AuthorizationRequest) error {
	a.calls = append(a.calls, request)
	if a.denyProjectView && request.Target == authoringservice.AuthorizationTargetProjectDashboard && request.DashboardID == "dashboard:sales" {
		return access.ErrForbidden
	}
	return nil
}

func (a *liveHandoffAuthorizer) sawAction(target authoringservice.AuthorizationTarget, action authoring.AuthorizationAction) bool {
	for _, request := range a.calls {
		if request.Target == target && request.Action == action {
			return true
		}
	}
	return false
}

type liveHandoffCompiler struct{ identity projectgraph.ServingIdentity }

func (c liveHandoffCompiler) Compile(_ context.Context, _ projectgraph.ResourceID, _ projectgraph.ResourceID, value document.DashboardDocument) (authoringservice.Compilation, error) {
	title := value.Metadata.Name
	if value.Metadata.DisplayName != nil {
		title = *value.Metadata.DisplayName
	}
	return authoringservice.Compilation{Definition: dashboarddefinition.Definition{ID: value.Metadata.ID, Title: title, SemanticModel: value.Spec.SemanticModel}, SemanticIdentity: c.identity}, nil
}

type liveHandoffRuntime struct {
	identity projectgraph.ServingIdentity
	model    *semanticmodel.Model
	compiled *semanticquery.CompiledModel
}

func (r *liveHandoffRuntime) Close() error                           { return nil }
func (r *liveHandoffRuntime) Identity() projectgraph.ServingIdentity { return r.identity }
func (r *liveHandoffRuntime) Catalog() dashboardcatalog.Catalog      { return dashboardcatalog.Catalog{} }
func (r *liveHandoffRuntime) SemanticModelProjection(id projectgraph.ResourceID) (*semanticmodel.Model, bool) {
	if id != "semantic:sales" || r.model == nil {
		return nil, false
	}
	return r.model, true
}
func (r *liveHandoffRuntime) CompiledSemanticModel(id string) (*semanticquery.CompiledModel, bool) {
	if id != "semantic:sales" || r.compiled == nil {
		return nil, false
	}
	return r.compiled, true
}

type liveHandoffLease struct {
	runtime  projectruntime.Runtime
	identity projectgraph.ServingIdentity
}

func (l *liveHandoffLease) Runtime() projectruntime.Runtime        { return l.runtime }
func (l *liveHandoffLease) Identity() projectgraph.ServingIdentity { return l.identity }
func (l *liveHandoffLease) Release()                               {}

type liveHandoffDefinitionReader struct {
	model    *semanticmodel.Model
	compiled *semanticquery.CompiledModel
}

func (r *liveHandoffDefinitionReader) ProjectDefinitionSnapshot(context.Context) (projectmanifest.Project, map[string]*semanticquery.CompiledModel, error) {
	return projectmanifest.Project{ID: "project:sales", SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": r.model}}, map[string]*semanticquery.CompiledModel{"semantic:sales": r.compiled}, nil
}

func (r *liveHandoffDefinitionReader) AuthorizedExploreModel(_ context.Context, projectID projectgraph.ResourceID, _ string, modelID string) (*semanticmodel.Model, *semanticquery.CompiledModel, projectgraph.ServingIdentity, error) {
	if projectID != "project:sales" || modelID != "semantic:sales" || r.model == nil || r.compiled == nil {
		return nil, nil, projectgraph.ServingIdentity{}, errors.New("semantic model unavailable")
	}
	identity, err := graphIdentityForLiveHandoff()
	if err != nil {
		return nil, nil, projectgraph.ServingIdentity{}, err
	}
	return r.model, r.compiled, identity, nil
}

var _ projectruntime.Runtime = (*liveHandoffRuntime)(nil)
var _ projectruntime.Lease = (*liveHandoffLease)(nil)
var _ ProjectDefinitionReader = (*liveHandoffDefinitionReader)(nil)
var _ authoringservice.Compiler = liveHandoffCompiler{}
var _ authoringservice.Authorizer = (*liveHandoffAuthorizer)(nil)
