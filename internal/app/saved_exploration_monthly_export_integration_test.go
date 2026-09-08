package app

import (
	"bytes"
	"context"
	"encoding/csv"
	"reflect"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesspolicy "github.com/flidai/leapview/internal/access/policy"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	explorationexport "github.com/flidai/leapview/internal/analytics/arrowquery/export"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	canonical "github.com/flidai/leapview/internal/analytics/exploration"
	saved "github.com/flidai/leapview/internal/analytics/exploration/saved"
	savedapplication "github.com/flidai/leapview/internal/analytics/exploration/saved/application"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	"github.com/flidai/leapview/internal/workload"
)

func TestSavedExplorationMonthlyExportPersistsReloadsAndExecutesForViewer(t *testing.T) {
	fixture := newSavedMonthlyExportFixture(t)
	spec := savedMonthlyExportSpec()
	payload, err := saved.NewExplorationSpecPayload(spec)
	if err != nil {
		t.Fatal(err)
	}
	create := saved.CreateRequest{
		ProjectID: savedAdapterProject, ID: "monthly-revenue", ActorID: "owner",
		Title: "Monthly Revenue", Slug: "monthly-revenue", Visibility: saved.VisibilityOrganization,
		Payload: payload,
	}
	fingerprint, err := savedapplication.FingerprintCreate(create)
	if err != nil {
		t.Fatal(err)
	}
	create.Evidence, err = saved.NewMutationEvidence("owner", saved.MutationActionCreate, "monthly-create", fingerprint, "request-monthly-create", "correlation-monthly-create", fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	created, err := fixture.service.Create(savedAdapterContext("owner"), create)
	if err != nil {
		t.Fatalf("save canonical monthly exploration: %v", err)
	}
	if created.Revision == nil || created.Lifecycle.CurrentRevision.Token() != created.Revision.Token() {
		t.Fatalf("saved result = %#v, want exact first revision", created)
	}

	reloaded, err := fixture.service.Read(savedAdapterContext("viewer"), saved.ReadRequest{ProjectID: savedAdapterProject, ID: "monthly-revenue", ActorID: "viewer"})
	if err != nil {
		t.Fatalf("reload saved monthly exploration: %v", err)
	}
	reloadedSpec, err := reloaded.Revision.Payload.Spec()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloadedSpec, spec) || !bytes.Equal(reloaded.Revision.Payload.Canonical(), payload.Canonical()) {
		t.Fatalf("reloaded spec/payload differs from saved canonical state: got=%#v want=%#v", reloadedSpec, spec)
	}

	executed, err := fixture.service.Execute(savedAdapterContext("viewer"), saved.ExecuteRequest{
		ProjectID: savedAdapterProject, ID: "monthly-revenue", ActorID: "viewer", Operation: "saved_exploration_url_export",
		ExpectedRevision: reloaded.Revision.Token(),
	})
	if err != nil {
		t.Fatalf("execute reloaded monthly exploration for viewer: %v", err)
	}
	if executed.Evidence.ActorID != "viewer" || executed.Evidence.ServingIdentity != fixture.identity {
		t.Fatalf("execution evidence = %#v, want viewer on active serving identity", executed.Evidence)
	}
	governedQuery := fixture.viewerMetrics.query
	if governedQuery.PrincipalID != "viewer" || governedQuery.EffectivePolicyFingerprint == "" {
		t.Fatalf("viewer governed query metadata = %#v, want viewer policy identity", governedQuery)
	}
	if fixture.ownerMetrics.calls != 0 || fixture.viewerMetrics.calls != 1 {
		t.Fatalf("runtime executions owner/viewer = %d/%d, want 0/1 (no creator result reuse)", fixture.ownerMetrics.calls, fixture.viewerMetrics.calls)
	}
	if len(governedQuery.Filters) != 1 || governedQuery.Filters[0].Field != "orders.customer_id" {
		t.Fatalf("viewer governed filters = %#v, want customer RLS filter", governedQuery.Filters)
	}
	if len(executed.Result.Columns) != 3 {
		t.Fatalf("monthly result columns = %#v, want month/state/revenue", executed.Result.Columns)
	}
	if monthType := executed.Result.Columns[0].Type; monthType.Kind != dataquery.ColumnTypeTimestamp || monthType.Unit == "" {
		t.Fatalf("monthly result month metadata = %#v, want retained timestamp type", monthType)
	}
	if revenueType := executed.Result.Columns[2].Type; revenueType.Kind != dataquery.ColumnTypeDecimal || revenueType.Precision != 38 || revenueType.Scale != 2 {
		t.Fatalf("monthly result revenue metadata = %#v, want aggregate decimal(38,2)", revenueType)
	}

	limits := explorationexport.Limits{MaxRows: 10, MaxBytes: 1 << 20}
	csvBody, err := explorationexport.Encode(t.Context(), executed.Result, explorationexport.CSV, limits)
	if err != nil {
		t.Fatalf("monthly viewer CSV: %v", err)
	}
	assertSavedMonthlyCSV(t, csvBody)
	parquetBody, err := explorationexport.Encode(t.Context(), executed.Result, explorationexport.Parquet, limits)
	if err != nil {
		t.Fatalf("monthly viewer Parquet: %v", err)
	}
	assertSavedMonthlyParquet(t, parquetBody)

	if fixture.repository.createCalls != 1 || fixture.repository.lookupCalls != 1 || fixture.repository.lifecycleReads != 2 || fixture.repository.revisionReads != 2 {
		t.Fatalf("repository save/reload/execute calls = create:%d lookup:%d lifecycle:%d revision:%d, want 1/1/2/2", fixture.repository.createCalls, fixture.repository.lookupCalls, fixture.repository.lifecycleReads, fixture.repository.revisionReads)
	}
	if stats := fixture.admission.Stats(); stats.Running != 0 || stats.Queued != 0 {
		t.Fatalf("workload admission remained active: %#v", stats)
	}
	if fixture.runtime.acquires["owner"] != 1 || fixture.runtime.acquires["viewer"] != 2 {
		t.Fatalf("runtime lease acquisitions = %#v, want owner:1 viewer:2", fixture.runtime.acquires)
	}
}

func savedMonthlyExportSpec() canonical.ExplorationSpec {
	month := canonical.ExplorationTimeGrainMonth
	monthAlias := "month"
	stateAlias := "state"
	revenueAlias := "revenue"
	dataset := "orders"
	return canonical.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset,
		Dimensions: []canonical.ExplorationDimensionRef{
			{Field: "activity_date", Alias: &monthAlias, Grain: &month},
			{Field: "customer_state", Alias: &stateAlias},
		},
		Metrics: []canonical.ExplorationMetricRef{{Field: "revenue", Alias: &revenueAlias}},
		Filters: []canonical.ExplorationFilter{}, Sort: []canonical.ExplorationSort{{Field: "activity_date", Direction: canonical.ExplorationSortDirectionAsc}}, Limit: 100,
	}
}

func assertSavedMonthlyCSV(t *testing.T, body []byte) {
	t.Helper()
	rows, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("decode monthly CSV: %v", err)
	}
	want := [][]string{
		{"month", "state", "revenue"},
		{"2026-01-01T00:00:00Z", "US", "50.00"},
		{"2026-02-01T00:00:00Z", "US", "25.00"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("monthly CSV rows = %#v, want %#v", rows, want)
	}
}

func assertSavedMonthlyParquet(t *testing.T, body []byte) {
	t.Helper()
	table, err := pqarrow.ReadTable(t.Context(), bytes.NewReader(body), nil, pqarrow.ArrowReadProperties{}, memory.DefaultAllocator)
	if err != nil {
		t.Fatalf("decode monthly Parquet: %v", err)
	}
	defer table.Release()
	if table.NumRows() != 2 || table.NumCols() != 3 {
		t.Fatalf("monthly Parquet shape = %d rows x %d cols, want 2 x 3", table.NumRows(), table.NumCols())
	}
	if got := table.Schema().Field(0).Name; got != "month" {
		t.Fatalf("monthly Parquet first column = %q, want month", got)
	}
	months, ok := table.Column(0).Data().Chunk(0).(*array.Timestamp)
	monthType, monthTypeOK := table.Schema().Field(0).Type.(*arrow.TimestampType)
	if !ok || !monthTypeOK || months.Len() != 2 || months.Value(0).ToTime(monthType.Unit).UTC().Format(time.RFC3339) != "2026-01-01T00:00:00Z" || months.Value(1).ToTime(monthType.Unit).UTC().Format(time.RFC3339) != "2026-02-01T00:00:00Z" {
		t.Fatalf("monthly Parquet month values = %#v, want January/February timestamp values", table.Column(0).Data().Chunk(0))
	}
	states, ok := table.Column(1).Data().Chunk(0).(*array.String)
	if !ok || states.Value(0) != "US" || states.Value(1) != "US" {
		t.Fatalf("monthly Parquet state values = %#v, want US/US", table.Column(1).Data().Chunk(0))
	}
	revenue, ok := table.Column(2).Data().Chunk(0).(*array.Decimal128)
	revenueType, revenueTypeOK := table.Schema().Field(2).Type.(*arrow.Decimal128Type)
	if !ok || !revenueTypeOK || revenueType.Precision != 38 || revenueType.Scale != 2 {
		t.Fatalf("monthly Parquet revenue type = %v, want Decimal128(38, 2)", table.Schema().Field(2).Type)
	}
	if revenue.Value(0).ToString(revenueType.Scale) != "50.00" || revenue.Value(1).ToString(revenueType.Scale) != "25.00" {
		t.Fatalf("monthly Parquet revenue values = %q/%q %#v, want Decimal128 50.00/25.00", revenue.Value(0).ToString(revenueType.Scale), revenue.Value(1).ToString(revenueType.Scale), table.Column(2).Data().Chunk(0))
	}
}

type savedMonthlyExportFixture struct {
	service       *savedapplication.Service
	repository    *savedMonthlyRepository
	runtime       *savedMonthlyRuntimeProvider
	identity      projectgraph.ServingIdentity
	ownerMetrics  *savedMonthlyDuckDBMetrics
	viewerMetrics *savedMonthlyDuckDBMetrics
	admission     *workload.Controller
	now           time.Time
}

func newSavedMonthlyExportFixture(t *testing.T) savedMonthlyExportFixture {
	t.Helper()
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	admission, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admission.Close)
	environment, err := analyticsducklake.Open(t.Context(), analyticsducklake.Config{
		RootDir: t.TempDir(), MaxConnections: 2, ExtensionAdmission: newTestExactExtensionAdmission(t, "ducklake"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = environment.Close() })
	setup, err := admission.Acquire(t.Context(), workload.Request{Class: workload.Refresh, PrincipalID: "saved-monthly-fixture", Operation: "create-monthly-fixture", EstimatedMemoryBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := environment.Exec(setup.Context(), "CREATE SCHEMA IF NOT EXISTS model; CREATE OR REPLACE TABLE model.orders (order_id BIGINT, customer_id BIGINT, ordered_at TIMESTAMP, revenue DECIMAL(10,2)); CREATE OR REPLACE TABLE model.customers (customer_id BIGINT, state VARCHAR); INSERT INTO model.orders VALUES (1, 10, '2026-01-05 12:00:00', 100.00), (2, 20, '2026-01-20 12:00:00', 50.00), (3, 10, '2026-02-03 12:00:00', 75.00), (4, 20, '2026-02-20 12:00:00', 25.00); INSERT INTO model.customers VALUES (10, 'DE'), (20, 'US')"); err != nil {
		setup.Release()
		t.Fatalf("create monthly DuckDB fixture: %v", err)
	}
	setup.Release()

	model := savedMonthlyExportModel()
	runtimeView, err := analyticsmaterialize.NewRuntimeView(t.Context(), analyticsmaterialize.RuntimeConfig{
		ModelID: "semantic:sales", Model: model, Database: environment, Sources: savedExportDuckDBSources{}, SnapshotOnly: true,
		TableRelation: func(table string) (string, error) { return "model." + table, nil },
	})
	if err != nil {
		t.Fatalf("compile monthly DuckDB runtime: %v", err)
	}
	t.Cleanup(func() { _ = runtimeView.Close() })
	graph, identity := savedAdapterGraph(t)
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	owner := mustSavedAdapterSubject(t, access.SubjectKindPrincipal, "owner")
	viewer := mustSavedAdapterSubject(t, access.SubjectKindPrincipal, "viewer")
	ownerMetrics := &savedMonthlyDuckDBMetrics{savedExportDuckDBMetrics: &savedExportDuckDBMetrics{savedExportMetrics: &savedExportMetrics{savedAdapterMetrics: &savedAdapterMetrics{model: model, planner: planner, identity: identity}}, runtime: runtimeView}}
	viewerMetrics := &savedMonthlyDuckDBMetrics{savedExportDuckDBMetrics: &savedExportDuckDBMetrics{savedExportMetrics: &savedExportMetrics{savedAdapterMetrics: &savedAdapterMetrics{model: model, planner: planner, identity: identity}}, runtime: runtimeView}}
	ownerSnapshot := savedMonthlySnapshot(t, graph, identity, owner, false)
	viewerSnapshot := savedMonthlySnapshot(t, graph, identity, viewer, true)
	leaseProvider := &savedMonthlyRuntimeProvider{leases: map[string]*savedAdapterLease{
		"owner":  {runtime: ownerMetrics, identity: identity, snapshot: ownerSnapshot},
		"viewer": {runtime: viewerMetrics, identity: identity, snapshot: viewerSnapshot},
	}, acquires: map[string]int{}}
	authorizer, err := NewSavedExplorationAuthorizer(savedAdapterAccessStub{})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewSavedExplorationExecutor(SavedExplorationExecutorOptions{AccessModule: savedAdapterAccessStub{}, Admitter: admission, AuditRecorder: &savedAdapterAudit{}})
	if err != nil {
		t.Fatal(err)
	}
	repository := &savedMonthlyRepository{}
	service, err := savedapplication.NewService(savedapplication.Options{
		Repository: repository, Authorizer: authorizer, Runtime: leaseProvider, Executor: executor,
		Now: func() time.Time { return now }, NewRevisionID: func() (saved.RevisionID, error) { return "revision-monthly", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return savedMonthlyExportFixture{service: service, repository: repository, runtime: leaseProvider, identity: identity, ownerMetrics: ownerMetrics, viewerMetrics: viewerMetrics, admission: admission, now: now}
}

func savedMonthlyExportModel() *semanticmodel.Model {
	model := &semanticmodel.Model{
		Name: "sales", Sources: map[string]semanticmodel.Source{"orders": {}, "customers": {}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}, "customers": {Model: "customers"}},
		Tables: map[string]semanticmodel.Table{
			"orders": {Execution: semanticmodel.ExecutionDefinition{Source: "orders"}, ModelName: "orders", GrainEntity: "order", Entities: map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"order_id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{
				"order_id":    {Field: "orders.order_id", Table: "orders", Name: "order_id", Datatype: semanticmodel.DataTypeInteger},
				"customer_id": {Field: "orders.customer_id", Table: "orders", Name: "customer_id", Datatype: semanticmodel.DataTypeInteger},
				"ordered_at":  {Field: "orders.ordered_at", Table: "orders", Name: "ordered_at", Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ},
				"revenue":     {Field: "orders.revenue", Table: "orders", Name: "revenue", Type: "number", Datatype: semanticmodel.DataTypeDecimal},
			}},
			"customers": {Execution: semanticmodel.ExecutionDefinition{Source: "customers"}, ModelName: "customers", GrainEntity: "customer", Entities: map[string]semanticmodel.EntityDefinition{"customer": {Type: "primary", Fields: []string{"customer_id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{
				"customer_id": {Field: "customers.customer_id", Table: "customers", Name: "customer_id", Datatype: semanticmodel.DataTypeInteger},
				"state":       {Field: "customers.state", Table: "customers", Name: "state", Type: "string", Datatype: semanticmodel.DataTypeString},
			}},
		},
		Relationships: []semanticmodel.Relationship{{ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"activity_date":  {Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ, NativeGrain: "day", Grains: []string{"day", "month"}, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.ordered_at"}}},
			"customer_state": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "customers.state", Path: []string{"orders_customers"}}}},
		},
		Metrics: map[string]semanticmodel.Metric{"revenue": {Type: "aggregate", Label: "Revenue", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}, Empty: "zero"}},
	}
	return model
}

func savedMonthlySnapshot(t *testing.T, graph projectgraph.ProjectGraph, identity projectgraph.ServingIdentity, subject access.SubjectRef, viewerPolicy bool) accesssnapshot.AuthorizationSnapshot {
	t.Helper()
	grants := []accesssnapshot.Grant{
		mustSavedAdapterGrantForResource(t, graph, string(subject.ID)+"-semantic-read", subject, "semantic:sales", access.CapabilityResourceRead),
		mustSavedAdapterGrantForResource(t, graph, string(subject.ID)+"-semantic-use", subject, "semantic:sales", access.CapabilityResourceUse),
		mustSavedAdapterGrantForResource(t, graph, string(subject.ID)+"-model-use", subject, "model:orders", access.CapabilityResourceUse),
	}
	role := access.ProjectRoleEditor
	if subject.ID == "viewer" {
		role = access.ProjectRoleViewer
		grants = append(grants, mustSavedAdapterGrantForResource(t, graph, "viewer-model-read", subject, "model:orders", access.CapabilityResourceRead))
	}
	roles := []accesssnapshot.RoleBinding{savedAdapterRole(t, string(subject.ID)+"-role", subject.ID, role)}
	policies := []accesssnapshot.DataPolicy{}
	if viewerPolicy {
		expression := `{"field":"orders.customer_id","operator":"equals","values":[20]}`
		compiled, err := accesspolicy.Compile("viewer-monthly-rls", accesspolicy.TypeRowFilter, expression)
		if err != nil {
			t.Fatal(err)
		}
		policies = append(policies, accesssnapshot.DataPolicy{ID: "viewer-monthly-rls", Resource: mustSavedExportResource(t, "model:orders", projectgraph.KindModel), Subject: &subject, PolicyType: accesspolicy.TypeRowFilter, ExpressionJSON: expression, Compiled: compiled})
	}
	snapshot := newSavedAdapterSnapshotWithPolicies(t, graph, identity, roles, grants, policies)
	return snapshot
}

type savedMonthlyDuckDBMetrics struct {
	*savedExportDuckDBMetrics
	calls int
}

func (m *savedMonthlyDuckDBMetrics) ExecuteDataQuery(ctx context.Context, query dataquery.Query) (dataquery.Result, error) {
	m.calls++
	return m.savedExportDuckDBMetrics.ExecuteDataQuery(ctx, query)
}

type savedMonthlyRuntimeProvider struct {
	leases   map[string]*savedAdapterLease
	acquires map[string]int
}

func (p *savedMonthlyRuntimeProvider) Acquire(ctx context.Context) (projectruntime.Lease, error) {
	principal, ok := accessmodule.PrincipalFromContext(ctx)
	if !ok || principal.ID == "" {
		return nil, access.ErrForbidden
	}
	lease, ok := p.leases[principal.ID]
	if !ok {
		return nil, saved.ErrNotFound
	}
	p.acquires[principal.ID]++
	return lease, nil
}

type savedMonthlyRepository struct {
	savedExportNoReadRepository
	lifecycle      saved.Lifecycle
	revision       saved.Revision
	createCalls    int
	lookupCalls    int
	lifecycleReads int
	revisionReads  int
}

var _ saved.Repository = (*savedMonthlyRepository)(nil)

func (r *savedMonthlyRepository) LookupMutation(context.Context, saved.MutationLookupInput) (saved.MutationReplayMetadata, bool, error) {
	r.lookupCalls++
	return saved.MutationReplayMetadata{}, false, nil
}

func (r *savedMonthlyRepository) Create(_ context.Context, input saved.CreateInput) (saved.MutationResult, error) {
	r.createCalls++
	record, err := saved.NewSavedExploration(saved.NewInput{ProjectID: input.ProjectID, ID: input.ID, OwnerPrincipalID: input.OwnerPrincipalID, Title: input.Title, Slug: input.Slug, Visibility: input.Visibility, SemanticModelID: input.SemanticModelID, CreatedAt: input.CreatedAt, Revision: input.Revision})
	if err != nil {
		return saved.MutationResult{}, err
	}
	r.lifecycle, r.revision = record.Lifecycle(), input.Revision.Clone()
	return saved.MutationResult{Lifecycle: r.lifecycle, Revision: &r.revision, AppliedRevision: r.revision.Token(), Evidence: input.Evidence}, nil
}

func (r *savedMonthlyRepository) GetLifecycle(_ context.Context, input saved.ReadInput) (saved.Lifecycle, error) {
	r.lifecycleReads++
	if r.lifecycle.ID != input.ID || r.lifecycle.ProjectID != input.ProjectID {
		return saved.Lifecycle{}, saved.ErrNotFound
	}
	return r.lifecycle, nil
}

func (r *savedMonthlyRepository) GetRevision(_ context.Context, input saved.RevisionReadInput) (saved.Revision, error) {
	r.revisionReads++
	if r.revision.Token() != input.Revision || r.revision.Metadata.ID == "" {
		return saved.Revision{}, saved.ErrNotFound
	}
	return r.revision.Clone(), nil
}
