package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesspolicy "github.com/flidai/leapview/internal/access/policy"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	canonical "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/testing/ssetest"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projecthttp "github.com/flidai/leapview/internal/project/http"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

// TestSavedExplorationMountedMonthlyWorkflow exercises the authenticated
// browser composition rather than calling the saved service directly. The
// shell is intentionally query-free; canonical /updates performs one initial
// owner execution, then the durable save is reopened and exported as viewer.
// Append/Explore-back remains the separate FAI661 workflow dependency.
func TestSavedExplorationMountedMonthlyWorkflow(t *testing.T) {
	fixture := newSavedMonthlyExportFixture(t)
	addMountedMonthlyViewerMask(t, fixture)
	model := savedMonthlyExportModel()
	compiled, err := semanticquery.CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	store := testStore(t)
	ownerToken, viewerToken := seedMountedMonthlyPrincipals(t, store)
	audit := analyticsmodule.BuildQueryAuditSurface(newTestQueryAuditRepository())
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	analytics := analyticsmodule.NewSurface(nil, nil)
	server, err := assembleRuntimeChecked(t.Context(), fakeMetrics{}, testStoreOptions(store, assemblyConfig{
		Auth: auth, AnalyticsModule: analytics,
	}))
	if err != nil {
		t.Fatalf("assemble production browser runtime: %v", err)
	}
	queryAuditRecorder := audit.Recorder()
	governanceAudit := &savedAdapterAudit{}
	queries := &mountedMonthlyQueryExecutor{
		fixture:  fixture,
		executor: mustMountedMonthlyExecutor(t, fixture, governanceAudit),
	}
	server.routes.projectBrowser.Graph = mountedMonthlyGraph{}
	server.routes.projectBrowser.ProjectDefinitionReader = mountedMonthlyDefinitions{model: model, compiled: compiled}
	server.routes.projectBrowser.Catalog = mountedMonthlyCatalog{}
	server.routes.projectBrowser.ResolveProjectID = func(context.Context) (projectgraph.ResourceID, error) { return savedAdapterProject, nil }
	server.routes.projectBrowser.Environment = "production"
	server.routes.projectBrowser.SavedExplorations = fixture.service
	bindings := analytics.SavedExplorationUICommandBindings()
	server.routes.projectBrowser.SavedExplorationCommands = projecthttp.SavedExplorationCommandBindings{
		Create: bindings.Create, Update: bindings.Update, Duplicate: bindings.Duplicate, Archive: bindings.Archive,
	}
	server.routes.projectBrowser.BeginSavedExplorationCommand = func(ctx context.Context, invocation projecthttp.SavedExplorationCommandInvocation) (context.Context, error) {
		return analytics.BeginSavedExplorationUICommand(ctx, analyticsmodule.SavedExplorationUICommandInvocation{
			Action: invocation.Action, Project: invocation.Project, Resource: invocation.Resource,
			IdempotencyKey: invocation.IdempotencyKey, RequestID: invocation.RequestID,
			CorrelationID: invocation.CorrelationID, Revision: invocation.Revision, ConcurrencyRevision: invocation.ConcurrencyRevision,
		})
	}
	server.routes.projectBrowser.ExecuteSavedExplorationCommand = func(ctx context.Context, invocation projecthttp.SavedExplorationCommandInvocation, transaction func(context.Context) error) error {
		return analytics.ExecuteSavedExplorationUICommand(ctx, analyticsmodule.SavedExplorationUICommandInvocation{
			Action: invocation.Action, Project: invocation.Project, Resource: invocation.Resource,
			IdempotencyKey: invocation.IdempotencyKey, RequestID: invocation.RequestID,
			CorrelationID: invocation.CorrelationID, Revision: invocation.Revision, ConcurrencyRevision: invocation.ConcurrencyRevision,
		}, transaction)
	}
	server.routes.projectBrowser.QueryExecutor = queries
	server.routes.projectBrowser.ExplorationExportAuditRecorder = queryAuditRecorder
	handler := server.Routes()

	stateJSON, err := json.Marshal(savedMonthlyExportSpec())
	if err != nil {
		t.Fatal(err)
	}

	shell := mountedMonthlyRequest(t, handler, http.MethodGet, "/explore?v=2&mode=explore&state="+url.QueryEscape(string(stateJSON)), ownerToken, nil)
	if shell.Code != http.StatusOK || !strings.Contains(shell.Body.String(), "Data Explorer") {
		t.Fatalf("explore shell = %d, want 200 with Data Explorer: %s", shell.Code, shell.Body.String())
	}
	if fixture.ownerMetrics.calls != 0 || len(queries.queries) != 0 {
		t.Fatalf("shell executed owner query calls=%d captured=%d, want zero", fixture.ownerMetrics.calls, len(queries.queries))
	}

	updatesPath := "/updates?route=data&surface=explore&v=2&mode=explore&state=" + url.QueryEscape(string(stateJSON))
	updates := mountedMonthlyUpdates(t, handler, updatesPath, ownerToken)
	if updates.Code != http.StatusOK {
		t.Fatalf("explore updates = %d, want 200: %s", updates.Code, updates.Body.String())
	}
	if len(queries.queries) != 1 || fixture.ownerMetrics.calls != 1 {
		t.Fatalf("initial owner execution queries=%d runtime calls=%d, want exactly one", len(queries.queries), fixture.ownerMetrics.calls)
	}
	assertMountedMonthlyQuery(t, queries.queries[0])
	assertMountedMonthlyGovernance(t, fixture.ownerMetrics.query, false)
	initialPatch := ssetest.RequirePatchSignal(t, updates.Body.String(), func(patch map[string]any) bool {
		return patch["dataExplorer"] != nil
	})
	if result := mountedSignalMap(initialPatch, "dataExplorer", "explore", "result"); result == nil {
		t.Fatalf("initial update patch omitted explorer result: %#v", initialPatch)
	}

	savedID, revision := mountedMonthlySave(t, handler, ownerToken, savedMonthlyExportSpec())
	if fixture.repository.createCalls != 1 || savedID == "" || revision.RevisionID == "" {
		t.Fatalf("save result id=%q revision=%#v createCalls=%d", savedID, revision, fixture.repository.createCalls)
	}

	reopened := mountedMonthlyRequest(t, handler, http.MethodGet, "/explore/saved/"+url.PathEscape(savedID), viewerToken, nil)
	if reopened.Code != http.StatusOK {
		t.Fatalf("viewer reopen = %d, want 200: %s", reopened.Code, reopened.Body.String())
	}
	if fixture.viewerMetrics.calls != 1 {
		t.Fatalf("viewer reopen runtime calls=%d, want one governed execution", fixture.viewerMetrics.calls)
	}
	assertMountedMonthlyGovernance(t, fixture.viewerMetrics.query, true)
	if fixture.ownerMetrics.query.EffectivePolicyFingerprint == fixture.viewerMetrics.query.EffectivePolicyFingerprint {
		t.Fatalf("owner and viewer policy fingerprints match: %q", fixture.viewerMetrics.query.EffectivePolicyFingerprint)
	}
	reopenPatch := ssetest.RequirePatchSignal(t, reopened.Body.String(), func(patch map[string]any) bool {
		return patch["dataExplorer"] != nil && patch["savedExplorations"] != nil
	})
	assertMountedMonthlyReopenedState(t, reopenPatch, savedID, revision, savedMonthlyExportSpec())
	assertMountedMonthlyViewerRows(t, reopenPatch)

	exportPath := "/explore/export?v=2&mode=explore&state=" + url.QueryEscape(string(stateJSON)) + "&format="
	csvResponse := mountedMonthlyRequest(t, handler, http.MethodGet, exportPath+"csv", viewerToken, nil)
	if csvResponse.Code != http.StatusOK || !strings.HasPrefix(csvResponse.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("viewer CSV export = %d content-type=%q body=%s", csvResponse.Code, csvResponse.Header().Get("Content-Type"), csvResponse.Body.String())
	}
	wantCSV := "month,state,revenue\n2026-01-01T00:00:00Z,REDACTED,50.00\n2026-02-01T00:00:00Z,REDACTED,25.00\n"
	if csvResponse.Body.String() != wantCSV {
		t.Fatalf("viewer CSV = %q, want %q", csvResponse.Body.String(), wantCSV)
	}
	parquetResponse := mountedMonthlyRequest(t, handler, http.MethodGet, exportPath+"parquet", viewerToken, nil)
	if parquetResponse.Code != http.StatusOK || parquetResponse.Header().Get("Content-Type") != "application/vnd.apache.parquet" {
		t.Fatalf("viewer Parquet export = %d content-type=%q", parquetResponse.Code, parquetResponse.Header().Get("Content-Type"))
	}
	assertSavedMonthlyParquetValues(t, parquetResponse.Body.Bytes(), "REDACTED")

	if fixture.viewerMetrics.calls != 3 {
		t.Fatalf("viewer runtime calls after reopen and two exports=%d, want 3", fixture.viewerMetrics.calls)
	}
	if len(governanceAudit.events) != 2 || governanceAudit.events[0].PrincipalID != "owner" || governanceAudit.events[1].PrincipalID != "viewer" || governanceAudit.events[0].Capability != access.CapabilityResourceUse || governanceAudit.events[1].Capability != access.CapabilityResourceUse {
		t.Fatalf("governed query audit events = %#v, want owner and viewer use events", governanceAudit.events)
	}
	events, err := auditQueryEvents(t, audit, savedAdapterProject, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("viewer export audit events=%d, want one per format: %#v", len(events), events)
	}
	formats := map[string]bool{}
	for _, event := range events {
		if event.Operation != "saved_exploration_export_preparation" || event.Status != "success" || event.PrincipalID != "viewer" {
			t.Fatalf("viewer export audit event = %#v", event)
		}
		var metadata map[string]string
		if err := json.Unmarshal([]byte(event.QueryJSON), &metadata); err != nil {
			t.Fatalf("decode export audit metadata: %v", err)
		}
		formats[metadata["exportFormat"]] = true
	}
	if !formats["csv"] || !formats["parquet"] {
		t.Fatalf("export audit formats = %#v, want csv and parquet", formats)
	}
	if stats := fixture.admission.Stats(); stats.Running != 0 || stats.Queued != 0 {
		t.Fatalf("workload admission remained active after mounted workflow: %#v", stats)
	}
	if queries.releases != len(queries.queries) {
		t.Fatalf("mounted query lease releases=%d executions=%d", queries.releases, len(queries.queries))
	}
}

// TestSavedExplorationMonthlyPresentationIRProjection covers the canonical
// monthly result through table, chart, and pivot projection modes. These are
// projection-only IR assertions for a mounted result shape; browser rendering
// is covered by the existing Data Explorer rendering suites.
func TestSavedExplorationMonthlyPresentationIRProjection(t *testing.T) {
	result := projectsignals.DataExploreResultSignal{
		Columns: []projectsignals.DataPreviewColumnSignal{
			{Key: "month", Label: "Activity date", Type: projectsignals.Pointer("timestamp")},
			{Key: "state", Label: "Customer state", Type: projectsignals.Pointer("string")},
			{Key: "revenue", Label: "Revenue", Type: projectsignals.Pointer("decimal")},
		},
		Rows: []map[string]any{
			{"month": "2026-01-01T00:00:00Z", "state": "US", "revenue": "50.00"},
			{"month": "2026-02-01T00:00:00Z", "state": "US", "revenue": "25.00"},
		},
		RequestSeq: 1,
	}
	fields := []projectsignals.DataExploreFieldSignal{
		{ID: "activity_date", Kind: "dimension", Label: "Activity date", Type: projectsignals.Pointer("timestamp"), DatasetID: "orders", Compatible: true},
		{ID: "customer_state", Kind: "dimension", Label: "Customer state", Type: projectsignals.Pointer("string"), DatasetID: "customers", Compatible: true},
		{ID: "revenue", Kind: "metric", Label: "Revenue", Type: projectsignals.Pointer("decimal"), DatasetID: "orders", Compatible: true},
	}
	base := savedMonthlyExportSpec()
	compiled, err := semanticquery.CompileModel(savedMonthlyExportModel())
	if err != nil {
		t.Fatalf("compile monthly presentation model: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*canonical.ExplorationSpec)
		view   string
		check  func(t *testing.T, projection projecthttp.DataExplorerVisualizationProjection)
	}{
		{name: "table", view: "table", mutate: func(spec *canonical.ExplorationSpec) {
			spec.Visualization = &canonical.ExplorationVisualizationConfig{Value: &canonical.TableExplorationVisualization{Kind: "table"}}
		}, check: func(t *testing.T, projection projecthttp.DataExplorerVisualizationProjection) {
			t.Helper()
			if _, ok := projection.Views["table"].Spec.Value.(*visualizationir.TableVisualizationSpec); !ok {
				t.Fatalf("table projection = %#v, want table IR", projection.Views["table"].Spec.Value)
			}
		}},
		{name: "chart", view: "line", mutate: func(spec *canonical.ExplorationSpec) {
			spec.Visualization = &canonical.ExplorationVisualizationConfig{Value: &canonical.CartesianExplorationVisualization{
				Kind: "cartesian", Mark: canonical.ExplorationVisualizationCartesianMarkLine,
				X: &canonical.ExplorationVisualizationFieldRef{Field: "activity_date"},
				Y: &[]canonical.ExplorationVisualizationFieldRef{{Field: "revenue"}},
			}}
		}, check: func(t *testing.T, projection projecthttp.DataExplorerVisualizationProjection) {
			t.Helper()
			chart, ok := projection.Views["line"]
			if !ok {
				t.Fatalf("chart projection views = %#v, want line IR", projection.Views)
			}
			value, ok := chart.Spec.Value.(*visualizationir.CartesianVisualizationSpec)
			if !ok || value.Mark != visualizationir.VisualizationCartesianMarkLine || value.X.Field != "month" || len(value.Y) != 1 || value.Y[0].Field != "revenue" {
				t.Fatalf("chart projection = %#v, want monthly line over revenue", chart.Spec.Value)
			}
		}},
		{name: "pivot", view: "pivot", mutate: func(spec *canonical.ExplorationSpec) {
			month := canonical.ExplorationTimeGrainMonth
			monthAlias := "month"
			stateAlias := "state"
			spec.Pivot = &canonical.ExplorationPivotConfig{
				Rows:    []canonical.ExplorationDimensionRef{{Field: "activity_date", Alias: &monthAlias, Grain: &month}},
				Columns: []canonical.ExplorationDimensionRef{{Field: "customer_state", Alias: &stateAlias}},
				Metrics: []canonical.ExplorationMetricRef{{Field: "revenue"}},
			}
		}, check: func(t *testing.T, projection projecthttp.DataExplorerVisualizationProjection) {
			t.Helper()
			pivot, ok := projection.Views["pivot"]
			if !ok {
				t.Fatalf("pivot projection views = %#v, want pivot IR", projection.Views)
			}
			value, ok := pivot.Spec.Value.(*visualizationir.PivotVisualizationSpec)
			if !ok || len(value.Rows) != 1 || value.Rows[0].Field != "month" || len(value.Columns) != 1 || value.Columns[0].Field != "state" || len(value.Metrics) != 1 || value.Metrics[0].Field != "revenue" {
				t.Fatalf("pivot projection = %#v, want month/state/revenue output bindings", pivot.Spec.Value)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := base
			if test.mutate != nil {
				test.mutate(&spec)
			}
			if err := canonical.ValidateShape(&spec); err != nil {
				t.Fatalf("invalid presentation spec shape: %v", err)
			}
			if err := canonical.ValidateAgainstModel(savedMonthlyExportModel(), &spec); err != nil {
				t.Fatalf("invalid presentation spec model references: %v", err)
			}
			if spec.ModelID != "semantic:sales" || spec.DatasetID == nil || *spec.DatasetID != "orders" {
				t.Fatalf("presentation spec target = %#v, want semantic:sales/orders", spec)
			}
			for _, dimension := range spec.Dimensions {
				if _, ok := compiled.SemanticDimension(dimension.Field); !ok {
					t.Fatalf("presentation dimension %q is not in compiled model", dimension.Field)
				}
				if _, ok := compiled.DimensionBinding(dimension.Field, "orders"); !ok {
					t.Fatalf("presentation dimension %q has no orders binding", dimension.Field)
				}
			}
			for _, metric := range spec.Metrics {
				if _, ok := compiled.Metric(metric.Field); !ok {
					t.Fatalf("presentation metric %q is not in compiled model", metric.Field)
				}
			}
			if spec.Pivot != nil {
				for _, dimension := range append(append([]canonical.ExplorationDimensionRef{}, spec.Pivot.Rows...), spec.Pivot.Columns...) {
					if _, ok := compiled.SemanticDimension(dimension.Field); !ok {
						t.Fatalf("pivot dimension %q is not in compiled model", dimension.Field)
					}
					if _, ok := compiled.DimensionBinding(dimension.Field, "orders"); !ok {
						t.Fatalf("pivot dimension %q has no orders binding", dimension.Field)
					}
				}
				for _, metric := range spec.Pivot.Metrics {
					if _, ok := compiled.Metric(metric.Field); !ok {
						t.Fatalf("pivot metric %q is not in compiled model", metric.Field)
					}
				}
			}
			projection := projecthttp.ProjectDataExplorerViews(spec, result, fields)
			if len(projection.Warnings) != 0 {
				t.Fatalf("projection warnings = %#v", projection.Warnings)
			}
			if projection.RecommendedView != test.view {
				t.Fatalf("recommended view = %q, want %q", projection.RecommendedView, test.view)
			}
			test.check(t, projection)
		})
	}
}

func seedMountedMonthlyPrincipals(t *testing.T, store *platform.Store) (string, string) {
	t.Helper()
	repo := testAccessRepository(store)
	for _, input := range []access.PrincipalInput{{ID: "owner", Kind: access.PrincipalKindUser, Email: "owner@monthly.test", DisplayName: "Monthly Owner"}, {ID: "viewer", Kind: access.PrincipalKindUser, Email: "viewer@monthly.test", DisplayName: "Monthly Viewer"}} {
		if _, err := repo.UpsertPrincipal(t.Context(), input); err != nil {
			t.Fatalf("seed %s principal: %v", input.ID, err)
		}
	}
	capabilities := []access.Capability{access.CapabilityResourceRead, access.CapabilityResourceUse, access.CapabilityResourceEdit, access.CapabilityResourceManage}
	ownerToken, _, err := repo.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{PrincipalID: "owner", Name: "monthly-owner", Capabilities: capabilities})
	if err != nil {
		t.Fatalf("create owner token: %v", err)
	}
	viewerToken, _, err := repo.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{PrincipalID: "viewer", Name: "monthly-viewer", Capabilities: []access.Capability{access.CapabilityResourceRead, access.CapabilityResourceUse}})
	if err != nil {
		t.Fatalf("create viewer token: %v", err)
	}
	return ownerToken, viewerToken
}

func addMountedMonthlyViewerMask(t *testing.T, fixture savedMonthlyExportFixture) {
	t.Helper()
	lease := fixture.runtime.leases["viewer"]
	subject := mustSavedAdapterSubject(t, access.SubjectKindPrincipal, "viewer")
	expression := `{"field":"customers.state","mask":"redact"}`
	compiled, err := accesspolicy.Compile("viewer-monthly-state-mask", accesspolicy.TypeColumnMask, expression)
	if err != nil {
		t.Fatal(err)
	}
	resource := mustSavedExportResource(t, "model:orders", projectgraph.KindModel)
	policies := append(lease.snapshot.DataPolicies(), accesssnapshot.DataPolicy{ID: "viewer-monthly-state-mask", Resource: resource, Subject: &subject, PolicyType: accesspolicy.TypeColumnMask, ExpressionJSON: expression, Compiled: compiled})
	updated, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(lease.snapshot.Identity(), lease.snapshot.Project(), lease.snapshot.RoleBindings(), lease.snapshot.Grants(), policies)
	if err != nil {
		t.Fatalf("add viewer monthly mask: %v", err)
	}
	lease.snapshot = updated
}

func mustMountedMonthlyExecutor(t *testing.T, fixture savedMonthlyExportFixture, audit *savedAdapterAudit) *SavedExplorationExecutor {
	t.Helper()
	executor, err := NewSavedExplorationExecutor(SavedExplorationExecutorOptions{AccessModule: savedAdapterAccessStub{}, Admitter: fixture.admission, AuditRecorder: audit})
	if err != nil {
		t.Fatalf("build mounted monthly executor: %v", err)
	}
	return executor
}

type mountedMonthlyQueryExecutor struct {
	fixture  savedMonthlyExportFixture
	executor *SavedExplorationExecutor
	queries  []dataquery.Query
	releases int
}

func (e *mountedMonthlyQueryExecutor) ExecuteDataQuery(ctx context.Context, query dataquery.Query) (dataquery.Result, error) {
	principal, ok := accessmodule.PrincipalFromContext(ctx)
	if !ok || principal.ID == "" {
		return dataquery.Result{}, access.ErrForbidden
	}
	query.ProjectID = savedAdapterProject
	query.PrincipalID = principal.ID
	e.queries = append(e.queries, query)
	lease, err := e.fixture.runtime.Acquire(ctx)
	if err != nil {
		return dataquery.Result{}, err
	}
	defer func() {
		lease.Release()
		e.releases++
	}()
	governed, err := e.executor.Execute(ctx, lease, principal.ID, query)
	return governed, err
}

type mountedMonthlyGraph struct{}

func (mountedMonthlyGraph) ActiveServingStateGraph(context.Context, projectgraph.ResourceID, string) (servingstate.AssetGraph, bool, error) {
	return servingstate.AssetGraph{Assets: []servingstate.Asset{
		{ID: savedAdapterProject, ProjectID: savedAdapterProject, ServingStateID: "state:monthly", SnapshotID: "snapshot:monthly", Type: "project", Key: "saved", Title: "Saved Monthly"},
		{ID: "model:orders", ProjectID: savedAdapterProject, ServingStateID: "state:monthly", SnapshotID: "snapshot:monthly", Type: "model", Key: "orders", Title: "Orders", PayloadJSON: `{}`},
		{ID: "semantic:sales", ProjectID: savedAdapterProject, ServingStateID: "state:monthly", SnapshotID: "snapshot:monthly", Type: "semantic_model", Key: "sales", Title: "Sales", PayloadJSON: `{}`},
	}}, true, nil
}

type mountedMonthlyDefinitions struct {
	model    *semanticmodel.Model
	compiled *semanticquery.CompiledModel
}

func (d mountedMonthlyDefinitions) ProjectDefinitionSnapshot(context.Context) (projectmanifest.ResourceManifest, map[string]*semanticquery.CompiledModel, error) {
	return projectmanifest.ResourceManifest{Title: "Saved Monthly", Models: map[string]semanticmodel.Table{"model:orders": d.model.Tables["orders"], "model:customers": d.model.Tables["customers"]}, SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": d.model}, NameIndex: projectmanifest.NameIndex{Models: map[string]string{"orders": "model:orders", "customers": "model:customers"}, SemanticModels: map[string]string{"sales": "semantic:sales"}}}, map[string]*semanticquery.CompiledModel{"semantic:sales": d.compiled}, nil
}

type mountedMonthlyCatalog struct{}

func (mountedMonthlyCatalog) List(_ context.Context, request projectcatalog.ListRequest) (projectcatalog.Page, error) {
	if request.PrincipalID != "owner" && request.PrincipalID != "viewer" || request.DevAuthBypass {
		return projectcatalog.Page{}, projectcatalog.ErrNotFound
	}
	items := make([]projectcatalog.Result, 0, len(request.Kinds))
	for _, kind := range request.Kinds {
		if item, ok := mountedMonthlyCatalogItem(kind); ok {
			items = append(items, item)
		}
	}
	return projectcatalog.Page{Items: items}, nil
}

func (mountedMonthlyCatalog) Resolve(_ context.Context, principalID string, ref projectcatalog.Ref, _ access.Capability, devAuthBypass bool) (projectcatalog.Result, error) {
	if (principalID != "owner" && principalID != "viewer") || devAuthBypass {
		return projectcatalog.Result{}, projectcatalog.ErrNotFound
	}
	item, ok := mountedMonthlyCatalogItem(ref.Kind)
	if !ok || item.Ref.ID != ref.ID {
		return projectcatalog.Result{}, projectcatalog.ErrNotFound
	}
	return item, nil
}

func mountedMonthlyCatalogItem(kind projectgraph.Kind) (projectcatalog.Result, bool) {
	switch kind {
	case projectgraph.KindProjectNamespace:
		return projectcatalog.Result{Ref: projectcatalog.Ref{ID: savedAdapterProject, Kind: kind}, Name: "saved", DisplayName: "Saved Monthly", Description: "Monthly revenue fixture"}, true
	case projectgraph.KindModel:
		return projectcatalog.Result{Ref: projectcatalog.Ref{ID: "model:orders", Kind: kind}, Name: "orders", DisplayName: "Orders"}, true
	case projectgraph.KindSemanticModel:
		return projectcatalog.Result{Ref: projectcatalog.Ref{ID: "semantic:sales", Kind: kind}, Name: "sales", DisplayName: "Sales"}, true
	default:
		return projectcatalog.Result{}, false
	}
}

func mountedMonthlySave(t *testing.T, handler http.Handler, token string, spec canonical.ExplorationSpec) (string, projectsignals.SavedExplorationRevisionSignal) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"savedExplorations": map[string]any{"command": map[string]any{"action": "create", "title": "Monthly Revenue", "slug": "monthly-revenue", "visibility": "organization", "spec": spec}}})
	if err != nil {
		t.Fatal(err)
	}
	request := mountedMonthlyRequest(t, handler, http.MethodPost, "/explore/saved/command", token, bytes.NewReader(body))
	if request.Code != http.StatusOK {
		t.Fatalf("save command = %d, want 200: %s", request.Code, request.Body.String())
	}
	patch := ssetest.RequirePatchSignal(t, request.Body.String(), func(patch map[string]any) bool { return patch["savedExplorations"] != nil })
	var state projectsignals.SavedExplorationStateSignal
	encoded, err := json.Marshal(patch["savedExplorations"])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &state); err != nil {
		t.Fatalf("decode saved state: %v", err)
	}
	if state.Save.State != "saved" || state.Current == nil {
		t.Fatalf("save state = %#v, message=%q, want saved current state", state, projectsignals.ValueOrZero(state.Save.Message))
	}
	if state.Current.Spec == nil || !reflect.DeepEqual(*state.Current.Spec, spec) {
		t.Fatalf("saved canonical spec = %#v, want %#v", state.Current.Spec, spec)
	}
	return state.Current.ID, state.Current.Revision
}

func mountedMonthlyRequest(t *testing.T, handler http.Handler, method, path, token string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, body)
	request.Header.Set("Authorization", "Bearer "+token)
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Request-ID", "01900000-0000-7000-8000-000000000201")
		request.Header.Set("Idempotency-Key", "01900000-0000-7000-8000-000000000202")
		request.Header.Set(uicommand.HeaderOperationID, analyticsgen.GenUIActionCreateSavedExploration().OperationID())
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func mountedMonthlyUpdates(t *testing.T, handler http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	ctx, timeout := context.WithTimeout(t.Context(), 5*time.Second)
	defer timeout()
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := &mountedMonthlyStreamRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: timeout}
	handler.ServeHTTP(response, request)
	return response.ResponseRecorder
}

type mountedMonthlyStreamRecorder struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
	once   sync.Once
}

func (r *mountedMonthlyStreamRecorder) Write(data []byte) (int, error) {
	n, err := r.ResponseRecorder.Write(data)
	r.once.Do(r.cancel)
	return n, err
}

func mountedSignalMap(patch map[string]any, path ...string) map[string]any {
	var current any = patch
	for _, segment := range path {
		values, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = values[segment]
	}
	values, _ := current.(map[string]any)
	return values
}

func assertMountedMonthlyQuery(t *testing.T, query dataquery.Query) {
	t.Helper()
	if query.Kind != dataquery.KindSemanticAggregate || query.ProjectID != savedAdapterProject || query.Surface != dataquery.SurfaceDataExplorer || query.Operation != dataquery.OperationSemanticExplore || query.ModelID != "semantic:sales" || query.Target != "orders" || query.Limit != 101 {
		t.Fatalf("initial canonical query metadata = %#v", query)
	}
	if len(query.Fields) != 2 || query.Fields[0] != (dataquery.Field{Field: "activity_date", Alias: "month", Grain: "month"}) || query.Fields[1] != (dataquery.Field{Field: "customer_state", Alias: "state"}) {
		t.Fatalf("initial canonical dimensions = %#v", query.Fields)
	}
	if len(query.Metrics) != 1 || query.Metrics[0] != (dataquery.Field{Field: "revenue", Alias: "revenue"}) || len(query.Filters) != 0 || len(query.Sort) != 1 || query.Sort[0].Field != "activity_date" || query.Sort[0].Direction != "asc" {
		t.Fatalf("initial canonical metrics/filters/sort = %#v/%#v/%#v", query.Metrics, query.Filters, query.Sort)
	}
}

func assertMountedMonthlyGovernance(t *testing.T, query dataquery.Query, viewer bool) {
	t.Helper()
	if query.EffectivePolicyFingerprint == "" {
		t.Fatalf("governed query has no policy fingerprint: %#v", query)
	}
	if !viewer {
		if len(query.Filters) != 0 || len(query.ColumnMasks) != 0 {
			t.Fatalf("owner query unexpectedly policy-constrained: %#v", query)
		}
		return
	}
	if len(query.Filters) != 1 || query.Filters[0].Field != "orders.customer_id" || len(query.ColumnMasks) != 1 || query.ColumnMasks[0].Field != "customers.state" || query.ColumnMasks[0].Mask != "redact" {
		t.Fatalf("viewer governed RLS/mask = filters:%#v masks:%#v", query.Filters, query.ColumnMasks)
	}
}

func assertMountedMonthlyReopenedState(t *testing.T, patch map[string]any, savedID string, revision projectsignals.SavedExplorationRevisionSignal, want canonical.ExplorationSpec) {
	t.Helper()
	var state projectsignals.SavedExplorationStateSignal
	encoded, err := json.Marshal(patch["savedExplorations"])
	if err != nil {
		t.Fatalf("encode reopened saved state: %v", err)
	}
	if err := json.Unmarshal(encoded, &state); err != nil {
		t.Fatalf("decode reopened saved state: %v", err)
	}
	if state.Current == nil {
		t.Fatalf("reopened saved state omitted current: %#v", state)
	}
	if state.Current.ID != savedID || state.Current.Revision != revision || state.Current.Spec == nil {
		t.Fatalf("reopened saved state identity = %#v, want id=%q revision=%#v with spec", state.Current, savedID, revision)
	}
	if !reflect.DeepEqual(*state.Current.Spec, want) {
		t.Fatalf("reopened canonical spec = %#v, want %#v", *state.Current.Spec, want)
	}
}

func assertMountedMonthlyViewerRows(t *testing.T, patch map[string]any) {
	t.Helper()
	result := mountedSignalMap(patch, "dataExplorer", "explore", "result")
	if result == nil {
		t.Fatalf("viewer update omitted structured result: %#v", patch)
	}
	rows, ok := result["rows"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("viewer result rows = %#v, want two structured rows", result["rows"])
	}
	want := []map[string]any{
		{"month": "2026-01-01T00:00:00Z", "state": "REDACTED", "revenue": "50.00"},
		{"month": "2026-02-01T00:00:00Z", "state": "REDACTED", "revenue": "25.00"},
	}
	for index, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok || !reflect.DeepEqual(row, want[index]) {
			t.Fatalf("viewer result row %d = %#v, want %#v", index, raw, want[index])
		}
	}
}

func auditQueryEvents(t *testing.T, surface *analyticsmodule.QueryAuditSurface, projectID projectgraph.ResourceID, principalID string) ([]queryaudit.Event, error) {
	t.Helper()
	reader, err := surface.Provider()()
	if err != nil {
		return nil, err
	}
	return reader.ListQueryEvents(t.Context(), queryaudit.Filter{ProjectID: projectID, PrincipalID: principalID, Surface: "saved_exploration", Limit: 10})
}
