package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/service"
	dashboardcompiler "github.com/flidai/leapview/internal/dashboard/compiler"
	"github.com/flidai/leapview/internal/dashboard/document"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	"github.com/google/uuid"
)

func TestAppendExplorationCreatesIndependentTileAndChecksSameModel(t *testing.T) {
	app, repo, auth, lease, initial := newExplorationAppendApplication(t, nil)
	target, err := app.ExplorationTarget(t.Context(), application.ExplorationTargetRequest{
		ProjectID: "sales", ActorID: "actor", SourceModelID: "semantic-model:sales", DashboardID: initial.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	dataset := "orders"
	base := explorationAppendRequest(target, initial.ID, exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic-model:sales", DatasetID: &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "status"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}},
		Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
	})
	result, err := app.AppendExploration(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision.Number != 2 || repo.appendCalls != 1 || lease.releases != 1 {
		t.Fatalf("append result/repo/lease = %#v/%d/%d", result.Revision, repo.appendCalls, lease.releases)
	}
	commandID := repo.lastAppend.Evidence.ID.String()
	parsedCommandID, parseErr := uuid.Parse(commandID)
	if parseErr != nil || parsedCommandID.Version() != 7 || commandID != base.IdempotencyKey {
		t.Fatalf("durable append command id = %q, want idempotency UUIDv7 %q (parse error: %v)", commandID, base.IdempotencyKey, parseErr)
	}
	stored := repo.revisions[result.Revision.RevisionID]
	if len(stored.Document.Spec.Visuals) != 1 || len(stored.Document.Spec.Pages[0].Components) != 1 {
		t.Fatalf("appended document visuals/components = %d/%d", len(stored.Document.Spec.Visuals), len(stored.Document.Spec.Pages[0].Components))
	}
	component, err := stored.Document.Spec.Pages[0].Components[0].Base()
	if err != nil || component == nil || component.Placement.Row != 1 || component.Placement.Column != 1 || component.Placement.ColumnSpan != 6 {
		t.Fatalf("appended placement = %#v (%v), want bottom-left half-width tile", component, err)
	}
	visualID := strings.TrimSpace(stored.Document.Spec.Pages[0].Components[0].Value.(*document.VisualDashboardPageComponent).Visual)
	if visualID == "" || stored.Document.Spec.Visuals[visualID].Query.Value == nil {
		t.Fatalf("copied visual %q has no dashboard query", visualID)
	}
	var modelRead, dashboardEdit bool
	for _, call := range auth.calls {
		if call.Target == service.AuthorizationTargetSemanticModel && call.SemanticModel == "semantic-model:sales" && call.Action == authoring.AuthorizationActionView {
			modelRead = true
		}
		if call.Target == service.AuthorizationTargetAuthoredDashboard && call.DashboardID == initial.ID && call.Action == authoring.AuthorizationActionEdit {
			dashboardEdit = true
		}
	}
	if !modelRead || !dashboardEdit {
		t.Fatalf("append authorization lacks model read/dashboard edit: %#v", auth.calls)
	}

	refreshedTarget, err := app.ExplorationTarget(t.Context(), application.ExplorationTargetRequest{
		ProjectID: "sales", ActorID: "actor", SourceModelID: "semantic-model:sales", DashboardID: initial.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrongModel := base
	wrongModel.IdempotencyKey = "0198f2c0-7c7a-7f00-8a11-000000000099"
	wrongModel.RevisionToken = refreshedTarget.RevisionToken
	wrongModel.Spec.ModelID = "semantic-model:other"
	if _, err := app.AppendExploration(t.Context(), wrongModel); !errors.Is(err, authoring.ErrConflict) {
		t.Fatalf("cross-model append error = %v, want conflict", err)
	}
	if repo.appendCalls != 1 {
		t.Fatalf("cross-model append reached repository %d times", repo.appendCalls-1)
	}
}

func TestAppendCategoryRevenueHandoffSpecToEditableSalesDraft(t *testing.T) {
	app, repo, _, lease, initial := newExplorationAppendApplicationForModel(t, nil, explorationAppendSalesOrdersModel())
	target, err := app.ExplorationTarget(t.Context(), application.ExplorationTargetRequest{
		ProjectID: "sales", ActorID: "actor", SourceModelID: "semantic-model:sales", DashboardID: initial.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	category, revenue := "category", "revenue"
	dataset := "sales_orders"
	request := explorationAppendRequest(target, initial.ID, exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic-model:sales", DatasetID: &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "sales_orders.category", Alias: &category}},
		Metrics:    []exploration.ExplorationMetricRef{{Field: "revenue", Alias: &revenue}},
		Filters:    []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{{Field: "revenue", Direction: exploration.ExplorationSortDirectionDesc}}, Limit: 10,
	})
	result, err := app.AppendExploration(t.Context(), request)
	if err != nil {
		t.Fatalf("append category_revenue handoff spec: %v", err)
	}
	if repo.appendCalls != 1 || result.Revision.Number != 2 || lease.releases != 1 {
		t.Fatalf("append/revision/lease = %d/%d/%d, want 1/2/1", repo.appendCalls, result.Revision.Number, lease.releases)
	}
	stored := repo.revisions[result.Revision.RevisionID]
	visual := stored.Document.Spec.Pages[0].Components[0].Value.(*document.VisualDashboardPageComponent).Visual
	query, ok := stored.Document.Spec.Visuals[visual].Query.Value.(*document.AggregateDashboardQuery)
	if !ok {
		t.Fatalf("appended query type = %T, want aggregate", stored.Document.Spec.Visuals[visual].Query.Value)
	}
	dimensionOutput := ""
	if len(query.Dimensions) == 1 {
		selection := query.Dimensions[0]
		if selection.String != nil {
			dimensionOutput = *selection.String
		} else if selection.Reference != nil {
			dimensionOutput = selection.Reference.Dimension
			if selection.Reference.Alias != nil {
				dimensionOutput = *selection.Reference.Alias
			}
		}
	}
	metricOutput := ""
	if len(query.Metrics) == 1 {
		selection := query.Metrics[0]
		if selection.String != nil {
			metricOutput = *selection.String
		} else if selection.Reference != nil {
			metricOutput = selection.Reference.Metric
			if selection.Reference.Alias != nil {
				metricOutput = *selection.Reference.Alias
			}
		}
	}
	if dimensionOutput != "category" || metricOutput != "revenue" || query.Sort == nil || len(*query.Sort) != 1 || (*query.Sort)[0].Field != "revenue" || (*query.Sort)[0].Direction != document.DashboardSortDirectionDesc || query.Limit == nil || *query.Limit != 10 {
		t.Fatalf("category_revenue dashboard query = %#v", stored.Document.Spec.Visuals[visual].Query.Value)
	}
}

func TestAppendExplorationRejectsNonNativeIdempotencyKeyBeforeRepositoryWrite(t *testing.T) {
	app, repo, _, _, initial := newExplorationAppendApplication(t, nil)
	target, err := app.ExplorationTarget(t.Context(), application.ExplorationTargetRequest{
		ProjectID: "sales", ActorID: "actor", SourceModelID: "semantic-model:sales", DashboardID: initial.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	dataset := "orders"
	request := explorationAppendRequest(target, initial.ID, exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic-model:sales", DatasetID: &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "status"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}},
		Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
	})
	for _, idempotencyKey := range []string{
		"explore-not-a-native-id",
		"550e8400-e29b-41d4-a716-446655440000", // UUIDv4 is not a durable authoring identity.
		"0198F2C0-7C7A-7F00-8A11-000000000098", // Canonical identities are lowercase.
	} {
		request.IdempotencyKey = idempotencyKey
		if _, err := app.AppendExploration(t.Context(), request); err == nil || !strings.Contains(err.Error(), "canonical UUIDv7") {
			t.Fatalf("non-native idempotency key %q error = %v, want canonical UUIDv7 validation", idempotencyKey, err)
		}
	}
	if repo.appendCalls != 0 {
		t.Fatalf("invalid idempotency key reached repository %d times", repo.appendCalls)
	}
}

func TestAppendExplorationRequiresMatchingTransactionalAuditIntent(t *testing.T) {
	app, repo, _, _, initial := newExplorationAppendApplication(t, nil)
	repo.requireAudit = true
	target, err := app.ExplorationTarget(t.Context(), application.ExplorationTargetRequest{
		ProjectID: "sales", ActorID: "actor", SourceModelID: "semantic-model:sales", DashboardID: initial.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	dataset := "orders"
	request := explorationAppendRequest(target, initial.ID, exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic-model:sales", DatasetID: &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "status"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}},
		Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
	})
	if _, err := app.AppendExploration(t.Context(), request); err == nil || !strings.Contains(err.Error(), "audit intent is required") {
		t.Fatalf("append without audit intent error = %v, want fail-closed transactional audit requirement", err)
	}
	intent := access.AuditIntent{
		EventID: request.IdempotencyKey, Source: "dashboard.authoring", Operation: "executeDashboardAuthoringCommand",
		ActorID: "actor", PrincipalID: "actor", Action: "dashboard_authoring.command_executed",
		Capability: access.CapabilityResourceEdit, Outcome: "success", RequestID: "0198f2c0-7c7a-7f00-8a11-000000000099",
		CorrelationID: "0198f2c0-7c7a-7f00-8a11-000000000099", MetadataJSON: `{"operationId":"executeDashboardAuthoringCommand"}`,
	}
	result, err := app.AppendExploration(authoring.WithAuditIntent(t.Context(), intent), request)
	if err != nil {
		t.Fatalf("append with transaction-scoped audit intent: %v", err)
	}
	if repo.lastAppend.Evidence.ID.String() != intent.EventID || result.Revision.Number != 2 {
		t.Fatalf("append command/audit identity = %q/%q, revision=%d", repo.lastAppend.Evidence.ID, intent.EventID, result.Revision.Number)
	}
}

func TestAppendExplorationPropagatesConcurrentDraftCASConflict(t *testing.T) {
	app, repo, _, _, initial := newExplorationAppendApplication(t, nil)
	target, err := app.ExplorationTarget(t.Context(), application.ExplorationTargetRequest{
		ProjectID: "sales", ActorID: "actor", SourceModelID: "semantic-model:sales", DashboardID: initial.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	repo.beforeAppend = func() {
		repo.lifecycle.Draft.Revision = authoring.RevisionToken{RevisionID: "revision-race", Number: 2, ContentHash: "sha256:" + strings.Repeat("c", 64)}
	}
	dataset := "orders"
	request := explorationAppendRequest(target, initial.ID, exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic-model:sales", DatasetID: &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "status"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}},
		Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
	})
	if _, err := app.AppendExploration(t.Context(), request); !errors.Is(err, authoring.ErrStaleRevision) {
		t.Fatalf("concurrent append error = %v, want stale revision", err)
	}
	if repo.appendCalls != 1 || repo.lifecycle.Draft.Revision.RevisionID != "revision-race" {
		t.Fatalf("CAS conflict state = calls:%d revision:%s", repo.appendCalls, repo.lifecycle.Draft.Revision.RevisionID)
	}
}

func TestAppendExplorationCompilesDefaultAggregateForLocalSalesDimensions(t *testing.T) {
	for _, field := range []string{"orders.category", "orders.purchase_date"} {
		t.Run(field, func(t *testing.T) {
			model := explorationAppendModel()
			date := model.Dimensions["purchase_date"]
			date.Calendar = "gregorian"
			model.Dimensions["purchase_date"] = date
			app, repo, _, _, initial := newExplorationAppendApplicationForModel(t, nil, model)
			target, err := app.ExplorationTarget(t.Context(), application.ExplorationTargetRequest{
				ProjectID: "sales", ActorID: "actor", SourceModelID: "semantic-model:sales", DashboardID: initial.ID,
			})
			if err != nil {
				t.Fatal(err)
			}
			dataset := "orders"
			request := explorationAppendRequest(target, initial.ID, exploration.ExplorationSpec{
				SchemaVersion: 1, ModelID: "semantic-model:sales", DatasetID: &dataset,
				Dimensions: []exploration.ExplorationDimensionRef{{Field: field}},
				Metrics:    []exploration.ExplorationMetricRef{{Field: "revenue"}},
				Filters:    []exploration.ExplorationFilter{},
				Sort:       []exploration.ExplorationSort{{Field: "revenue", Direction: "desc"}},
				Limit:      30,
			})
			result, err := app.AppendExploration(t.Context(), request)
			if err != nil {
				t.Fatalf("append valid aggregate exploration: %v", err)
			}
			if repo.appendCalls != 1 {
				t.Fatalf("repository append calls = %d, want 1", repo.appendCalls)
			}
			stored := repo.revisions[result.Revision.RevisionID]
			visual := stored.Document.Spec.Pages[0].Components[0].Value.(*document.VisualDashboardPageComponent).Visual
			if got := stored.Document.Spec.Visuals[visual].Type; got != document.DashboardVisualTypeBar {
				t.Fatalf("inferred visual type = %q, want bar for default aggregate presentation", got)
			}
		})
	}
}

func explorationAppendRequest(target application.ExplorationTarget, dashboardID authoring.DashboardID, spec exploration.ExplorationSpec) application.ExplorationAppendRequest {
	return application.ExplorationAppendRequest{
		ProjectID: "sales", ActorID: "actor", DashboardID: dashboardID, PageID: "overview",
		RevisionToken: target.RevisionToken, IdempotencyKey: "0198f2c0-7c7a-7f00-8a11-000000000098",
		PlacementChoice: "half", Spec: spec,
	}
}

func newExplorationAppendApplication(t *testing.T, beforeAppend func()) (*application.Application, *applicationRepository, *applicationAuthorizer, *explorationAppendLease, authoring.DashboardLifecycle) {
	return newExplorationAppendApplicationForModel(t, beforeAppend, explorationAppendModel())
}

func newExplorationAppendApplicationForModel(t *testing.T, beforeAppend func(), model *semanticmodel.Model) (*application.Application, *applicationRepository, *applicationAuthorizer, *explorationAppendLease, authoring.DashboardLifecycle) {
	t.Helper()
	identity, err := projectgraph.NewServingIdentity("sales", "development", "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := semanticquery.CompileModel(model)
	if err != nil {
		t.Fatalf("compile semantic model: %v", err)
	}
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}
	doc := document.DashboardDocument{
		APIVersion: document.DashboardApiVersionLeapviewDevV1, Kind: document.DashboardResourceKindDashboard,
		Metadata: document.DashboardMetadata{ID: "dashboard-sales", Name: "sales"},
		Spec:     document.DashboardSpec{SemanticModel: "semantic-model:sales", Filters: []document.DashboardFilter{}, Visuals: map[string]document.DashboardVisual{}, Pages: []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{}}}},
	}
	revision, err := authoring.NewRevision("revision-1", "dashboard-sales", 1, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), doc, provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{
		ProjectID: "sales", ID: "dashboard-sales", OwnerPrincipalID: "actor", Slug: "sales", Title: "Sales",
		SemanticModel: "semantic-model:sales", Visibility: authoring.VisibilityPrivate,
		Draft: &authoring.Draft{ID: "draft-sales", DashboardID: "dashboard-sales", Revision: revision.Token(), Provenance: provenance},
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := &applicationRepository{
		lifecycle: lifecycle, revisions: map[authoring.RevisionID]authoring.Revision{revision.ID: revision},
		commands: map[authoring.CommandID]authoring.CommandResult{}, fingerprints: map[authoring.CommandID]string{}, beforeAppend: beforeAppend,
	}
	auth := &applicationAuthorizer{}
	compiler := explorationAppendCompiler{identity: identity, model: model}
	serviceApplication, err := service.NewService(service.Options{
		Repository: repo, Authorizer: auth, Compiler: compiler, Now: func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) },
		NewDashboardID: func() (authoring.DashboardID, error) { return "unused-dashboard", nil },
		NewDraftID:     func() (authoring.DraftID, error) { return "unused-draft", nil },
		NewRevisionID:  func() (authoring.RevisionID, error) { return "revision-2", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &explorationAppendRuntime{identity: identity, modelID: "semantic-model:sales", model: model, compiled: compiled}
	lease := &explorationAppendLease{runtime: runtime, identity: identity}
	app, err := application.New(application.Options{
		Authoring: serviceApplication, Repository: repo, Authorizer: auth, Compiler: compiler,
		AcquireRuntime: func(context.Context) (projectruntime.Lease, error) { return lease, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return app, repo, auth, lease, lifecycle
}

func explorationAppendSalesOrdersModel() *semanticmodel.Model {
	fields := map[string]semanticmodel.MetricDimension{
		"id":       {Field: "sales_orders.id", Table: "sales_orders", Name: "id", Type: "number", Datatype: semanticmodel.DataTypeInteger},
		"category": {Field: "sales_orders.category", Table: "sales_orders", Name: "category", Type: "string", Datatype: semanticmodel.DataTypeString},
		"revenue":  {Field: "sales_orders.revenue", Table: "sales_orders", Name: "revenue", Type: "number", Datatype: semanticmodel.DataTypeDecimal},
	}
	columns := make(map[string]semanticmodel.ModelColumn, len(fields))
	for name, field := range fields {
		columns[name] = semanticmodel.ModelColumn{Name: name, SourceField: name, Type: field.Type, Datatype: field.Datatype}
	}
	return &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{"sales_orders": {
			ModelName: "sales_orders_model", GrainEntity: "order",
			Entities:   map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"id"}}},
			Dimensions: fields, Columns: columns,
		}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"sales_orders": {Model: "sales_orders_model"}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"category": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"sales_orders": {Field: "sales_orders.category"}}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"revenue": {Type: "aggregate", Dataset: "sales_orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "sales_orders.revenue"}},
		},
	}
}

func explorationAppendModel() *semanticmodel.Model {
	fields := map[string]semanticmodel.MetricDimension{
		"id":            {Field: "orders.id", Table: "orders", Name: "id", Type: "number", Datatype: semanticmodel.DataTypeInteger},
		"status":        {Field: "orders.status", Table: "orders", Name: "status", Type: "string", Datatype: semanticmodel.DataTypeString},
		"amount":        {Field: "orders.amount", Table: "orders", Name: "amount", Type: "number", Datatype: semanticmodel.DataTypeDecimal},
		"category":      {Field: "orders.category", Table: "orders", Name: "category", Type: "string", Datatype: semanticmodel.DataTypeString},
		"purchase_date": {Field: "orders.purchase_date", Table: "orders", Name: "purchase_date", Type: "date", Datatype: semanticmodel.DataTypeDate},
	}
	columns := map[string]semanticmodel.ModelColumn{}
	for name, field := range fields {
		columns[name] = semanticmodel.ModelColumn{Name: name, SourceField: name, Type: field.Type, Datatype: field.Datatype}
	}
	return &semanticmodel.Model{
		Name: "sales", Tables: map[string]semanticmodel.Table{"orders": {
			ModelName: "orders_model", GrainEntity: "order", Entities: map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"id"}}},
			Dimensions: fields, Columns: columns,
		}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders_model"}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"status":        {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.status"}}},
			"category":      {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.category"}}},
			"purchase_date": {Type: "date", Datatype: semanticmodel.DataTypeDate, NativeGrain: "day", Grains: []string{"day", "week", "month"}, Calendar: "iso8601", Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.purchase_date"}}},
		},
		Metrics: map[string]semanticmodel.Metric{"revenue": {Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.amount"}}},
	}
}

type explorationAppendCompiler struct {
	identity projectgraph.ServingIdentity
	model    *semanticmodel.Model
}

func (c explorationAppendCompiler) Compile(_ context.Context, _ projectgraph.ResourceID, modelID projectgraph.ResourceID, value document.DashboardDocument) (service.Compilation, error) {
	compiled, err := dashboardcompiler.CompileDocument(value, map[string]*semanticmodel.Model{modelID.String(): c.model})
	if err != nil {
		return service.Compilation{}, err
	}
	return service.Compilation{Definition: compiled.Definition, SemanticIdentity: c.identity}, nil
}

type explorationAppendRuntime struct {
	identity projectgraph.ServingIdentity
	modelID  projectgraph.ResourceID
	model    *semanticmodel.Model
	compiled *semanticquery.CompiledModel
}

func (explorationAppendRuntime) Close() error                              { return nil }
func (r *explorationAppendRuntime) Identity() projectgraph.ServingIdentity { return r.identity }
func (r *explorationAppendRuntime) SemanticModelProjection(id projectgraph.ResourceID) (*semanticmodel.Model, bool) {
	return r.model, id == r.modelID
}
func (r *explorationAppendRuntime) CompiledSemanticModel(id string) (*semanticquery.CompiledModel, bool) {
	return r.compiled, id == r.modelID.String()
}

type explorationAppendLease struct {
	runtime  projectruntime.Runtime
	identity projectgraph.ServingIdentity
	releases int
}

func (l *explorationAppendLease) Runtime() projectruntime.Runtime        { return l.runtime }
func (l *explorationAppendLease) Identity() projectgraph.ServingIdentity { return l.identity }
func (l *explorationAppendLease) Release()                               { l.releases++ }

var _ projectruntime.Lease = (*explorationAppendLease)(nil)
