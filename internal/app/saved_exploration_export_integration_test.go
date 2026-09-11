package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

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

// This fixture keeps physical execution deterministic so it can isolate the
// application wiring contract. The DuckDB-backed test below covers the same
// path with a real materialization runtime and physical rows.
func TestSavedExplorationURLExportWiringUsesViewerPoliciesAndAtomicEncoding(t *testing.T) {
	fixture := newSavedExportFixture(t)
	spec := savedExportSpec()
	execute := func(actor string) saved.ExecuteResult {
		t.Helper()
		result, err := fixture.service.ExecuteSpec(savedAdapterContext(actor), saved.ExecuteSpecRequest{
			ProjectID: savedAdapterProject, ActorID: actor, Spec: spec, Operation: "saved_exploration_url_export",
		})
		if err != nil {
			t.Fatalf("ExecuteSpec(%s): %v", actor, err)
		}
		return result
	}

	owner := execute("owner")
	viewer := execute("viewer")
	if fixture.ownerMetrics.query.EffectivePolicyFingerprint == "" || fixture.ownerMetrics.query.EffectivePolicyFingerprint == fixture.viewerMetrics.query.EffectivePolicyFingerprint {
		t.Fatal("viewer policy identity was not isolated from owner policy identity")
	}

	limits := explorationexport.Limits{MaxRows: 10, MaxBytes: 1 << 20}
	ownerCSV, err := explorationexport.Encode(t.Context(), owner.Result, explorationexport.CSV, limits)
	if err != nil {
		t.Fatalf("owner CSV: %v", err)
	}
	viewerCSV, err := explorationexport.Encode(t.Context(), viewer.Result, explorationexport.CSV, limits)
	if err != nil {
		t.Fatalf("viewer CSV: %v", err)
	}
	if string(ownerCSV) != "order_id,order_count\n1,1\n" || string(viewerCSV) != "order_id,order_count\n2,1\n" {
		t.Fatalf("CSV bytes leaked or merged identities: owner=%q viewer=%q", ownerCSV, viewerCSV)
	}

	ownerParquet, err := explorationexport.Encode(t.Context(), owner.Result, explorationexport.Parquet, limits)
	if err != nil {
		t.Fatalf("owner Parquet: %v", err)
	}
	viewerParquet, err := explorationexport.Encode(t.Context(), viewer.Result, explorationexport.Parquet, limits)
	if err != nil {
		t.Fatalf("viewer Parquet: %v", err)
	}
	if bytes.Equal(ownerParquet, viewerParquet) {
		t.Fatal("Parquet bytes did not isolate viewer identities")
	}
	if got := savedExportParquetOrderID(t, ownerParquet); got != 1 {
		t.Fatalf("owner Parquet order id = %d, want 1", got)
	}
	if got := savedExportParquetOrderID(t, viewerParquet); got != 2 {
		t.Fatalf("viewer Parquet order id = %d, want 2", got)
	}
	if fixture.repository.calls != 0 {
		t.Fatalf("URL export read durable saved-exploration repository %d times", fixture.repository.calls)
	}

	canceled, cancel := context.WithCancel(savedAdapterContext("owner"))
	cancel()
	if _, err := fixture.service.ExecuteSpec(canceled, saved.ExecuteSpecRequest{ProjectID: savedAdapterProject, ActorID: "owner", Spec: spec}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ExecuteSpec error = %v, want context.Canceled", err)
	}
	if fixture.admission.acquired != 3 || fixture.admission.released != 3 {
		t.Fatalf("admission acquire/release = %d/%d, want 3/3", fixture.admission.acquired, fixture.admission.released)
	}
}

func TestSavedExplorationURLExportUsesDuckDBRowsForViewerPolicies(t *testing.T) {
	fixture := newSavedExportDuckDBFixture(t)
	spec := savedExportDuckDBSpec()
	execute := func(actor string) saved.ExecuteResult {
		t.Helper()
		result, err := fixture.service.ExecuteSpec(savedAdapterContext(actor), saved.ExecuteSpecRequest{
			ProjectID: savedAdapterProject, ActorID: actor, Spec: spec, Operation: "saved_exploration_url_export",
		})
		if err != nil {
			t.Fatalf("ExecuteSpec(%s): %v", actor, err)
		}
		return result
	}

	owner := execute("owner")
	viewer := execute("viewer")
	assertSavedExportOrderRow(t, owner.Result, 1)
	assertSavedExportOrderRow(t, viewer.Result, 2)
	assertSavedExportCustomerEmail(t, owner.Result, "owner@example.test")
	assertSavedExportCustomerEmail(t, viewer.Result, "REDACTED")
	if fixture.ownerMetrics.query.EffectivePolicyFingerprint == "" || fixture.ownerMetrics.query.EffectivePolicyFingerprint == fixture.viewerMetrics.query.EffectivePolicyFingerprint {
		t.Fatal("DuckDB execution did not preserve per-viewer policy identity")
	}
	for actor, result := range map[string]saved.ExecuteResult{"owner": owner, "viewer": viewer} {
		body, err := explorationexport.Encode(t.Context(), result.Result, explorationexport.CSV, explorationexport.Limits{MaxRows: 10, MaxBytes: 1 << 20})
		if err != nil {
			t.Fatalf("%s CSV: %v", actor, err)
		}
		want := "order_id,customer_email,order_count\n" + map[string]string{"owner": "1,owner@example.test", "viewer": "2,REDACTED"}[actor] + ",1\n"
		if string(body) != want {
			t.Fatalf("%s CSV = %q, want %q", actor, body, want)
		}
	}
	limits := explorationexport.Limits{MaxRows: 10, MaxBytes: 1 << 20}
	ownerParquet, err := explorationexport.Encode(t.Context(), owner.Result, explorationexport.Parquet, limits)
	if err != nil {
		t.Fatalf("owner Parquet: %v", err)
	}
	viewerParquet, err := explorationexport.Encode(t.Context(), viewer.Result, explorationexport.Parquet, limits)
	if err != nil {
		t.Fatalf("viewer Parquet: %v", err)
	}
	if bytes.Equal(ownerParquet, viewerParquet) {
		t.Fatal("DuckDB Parquet bytes did not isolate viewer identities")
	}
	if got := savedExportParquetOrderID(t, ownerParquet); got != 1 {
		t.Fatalf("owner DuckDB Parquet order id = %d, want 1", got)
	}
	if got := savedExportParquetOrderID(t, viewerParquet); got != 2 {
		t.Fatalf("viewer DuckDB Parquet order id = %d, want 2", got)
	}
	if got := savedExportParquetCustomerEmail(t, ownerParquet); got != "owner@example.test" {
		t.Fatalf("owner DuckDB Parquet customer email = %q, want owner@example.test", got)
	}
	if got := savedExportParquetCustomerEmail(t, viewerParquet); got != "REDACTED" {
		t.Fatalf("viewer DuckDB Parquet customer email = %q, want REDACTED", got)
	}
	if fixture.repository.calls != 0 {
		t.Fatalf("URL export read durable saved-exploration repository %d times", fixture.repository.calls)
	}
	if stats := fixture.admission.Stats(); stats.Running != 0 || stats.Queued != 0 {
		t.Fatalf("workload admission remained active after exports: %#v", stats)
	}
}

func assertSavedExportOrderRow(t *testing.T, result dataquery.Result, want int64) {
	t.Helper()
	if result.Status != dataquery.StatusSuccess || len(result.Rows) != 1 {
		t.Fatalf("DuckDB result status/rows = %q/%d, want success/1", result.Status, len(result.Rows))
	}
	value, ok := result.Rows[0]["order_id"].(int64)
	if !ok || value != want {
		t.Fatalf("DuckDB result order_id = %#v, want %d", result.Rows[0]["order_id"], want)
	}
}

func assertSavedExportCustomerEmail(t *testing.T, result dataquery.Result, want string) {
	t.Helper()
	value, ok := result.Rows[0]["customer_email"].(string)
	if !ok || value != want {
		t.Fatalf("DuckDB result customer_email = %#v, want %q", result.Rows[0]["customer_email"], want)
	}
}

type savedExportDuckDBFixture struct {
	service       *savedapplication.Service
	repository    *savedExportNoReadRepository
	ownerMetrics  *savedExportDuckDBMetrics
	viewerMetrics *savedExportDuckDBMetrics
	admission     *workload.Controller
}

func newSavedExportDuckDBFixture(t *testing.T) savedExportDuckDBFixture {
	t.Helper()
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
	setup, err := admission.Acquire(t.Context(), workload.Request{Class: workload.Refresh, PrincipalID: "saved-export-fixture", Operation: "create-export-fixture", EstimatedMemoryBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := environment.Exec(setup.Context(), "CREATE SCHEMA IF NOT EXISTS model; CREATE OR REPLACE TABLE model.orders (order_id BIGINT, customer_email VARCHAR); INSERT INTO model.orders VALUES (1, 'owner@example.test'), (2, 'viewer@example.test')"); err != nil {
		setup.Release()
		t.Fatalf("create DuckDB export fixture: %v", err)
	}
	setup.Release()

	model := savedExportDuckDBModel()
	runtime, err := analyticsmaterialize.NewRuntimeView(t.Context(), analyticsmaterialize.RuntimeConfig{
		ModelID: "semantic:sales", Model: model, Database: environment, Sources: savedExportDuckDBSources{}, SnapshotOnly: true,
		TableRelation: func(table string) (string, error) { return "model." + table, nil },
	})
	if err != nil {
		t.Fatalf("compile DuckDB export runtime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	graph, identity := savedAdapterGraph(t)
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	owner := mustSavedAdapterSubject(t, access.SubjectKindPrincipal, "owner")
	viewer := mustSavedAdapterSubject(t, access.SubjectKindPrincipal, "viewer")
	ownerMetrics := &savedExportDuckDBMetrics{savedExportMetrics: &savedExportMetrics{savedAdapterMetrics: &savedAdapterMetrics{model: model, planner: planner, identity: identity}}, runtime: runtime}
	viewerMetrics := &savedExportDuckDBMetrics{savedExportMetrics: &savedExportMetrics{savedAdapterMetrics: &savedAdapterMetrics{model: model, planner: planner, identity: identity}}, runtime: runtime}
	ownerSnapshot := savedExportSnapshot(t, graph, identity, owner, 1)
	viewerSnapshot := savedExportSnapshotWithMask(t, graph, identity, viewer, 2, `{"field":"orders.customer_email","mask":"redact"}`)
	provider := savedExportRuntimeProvider{leases: map[string]*savedAdapterLease{
		"owner":  {runtime: ownerMetrics, identity: identity, snapshot: ownerSnapshot},
		"viewer": {runtime: viewerMetrics, identity: identity, snapshot: viewerSnapshot},
	}}
	authorizer, err := NewSavedExplorationAuthorizer(savedAdapterAccessStub{})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewSavedExplorationExecutor(SavedExplorationExecutorOptions{AccessModule: savedAdapterAccessStub{}, Admitter: admission, AuditRecorder: &savedAdapterAudit{}})
	if err != nil {
		t.Fatal(err)
	}
	repository := &savedExportNoReadRepository{}
	service, err := savedapplication.NewService(savedapplication.Options{
		Repository: repository, Authorizer: authorizer, Runtime: provider, Executor: executor,
		Now:           func() time.Time { return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC) },
		NewRevisionID: func() (saved.RevisionID, error) { return "revision-export", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return savedExportDuckDBFixture{service: service, repository: repository, ownerMetrics: ownerMetrics, viewerMetrics: viewerMetrics, admission: admission}
}

type savedExportDuckDBSources struct{}

func (savedExportDuckDBSources) Prepare(context.Context, *semanticmodel.Model) (analyticsmaterialize.PreparedSources, error) {
	return savedExportDuckDBSources{}, nil
}

func (savedExportDuckDBSources) PlanModelTable(context.Context, *semanticmodel.Model, string, semanticmodel.Table) (analyticsmaterialize.ModelTablePlan, error) {
	return analyticsmaterialize.ModelTablePlan{}, errors.New("DuckDB export fixture does not materialize authored sources")
}

func (savedExportDuckDBSources) Close() error { return nil }

type savedExportDuckDBMetrics struct {
	*savedExportMetrics
	runtime *analyticsmaterialize.Runtime
}

func (m *savedExportDuckDBMetrics) ExecuteDataQuery(ctx context.Context, query dataquery.Query) (dataquery.Result, error) {
	m.query = query
	return m.runtime.ExecuteDataQuery(ctx, query)
}

type savedExportFixture struct {
	service       *savedapplication.Service
	repository    *savedExportNoReadRepository
	admission     *savedAdapterAdmission
	ownerMetrics  *savedExportMetrics
	viewerMetrics *savedExportMetrics
}

func newSavedExportFixture(t *testing.T) savedExportFixture {
	t.Helper()
	graph, identity := savedAdapterGraph(t)
	model := savedAdapterModel()
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	owner := mustSavedAdapterSubject(t, access.SubjectKindPrincipal, "owner")
	viewer := mustSavedAdapterSubject(t, access.SubjectKindPrincipal, "viewer")
	ownerSnapshot := savedExportSnapshot(t, graph, identity, owner, 1)
	viewerSnapshot := savedExportSnapshot(t, graph, identity, viewer, 2)
	ownerMetrics := &savedExportMetrics{savedAdapterMetrics: &savedAdapterMetrics{model: model, planner: planner, identity: identity}}
	viewerMetrics := &savedExportMetrics{savedAdapterMetrics: &savedAdapterMetrics{model: model, planner: planner, identity: identity}}
	provider := savedExportRuntimeProvider{leases: map[string]*savedAdapterLease{
		"owner":  {runtime: ownerMetrics, identity: identity, snapshot: ownerSnapshot},
		"viewer": {runtime: viewerMetrics, identity: identity, snapshot: viewerSnapshot},
	}}
	authorizer, err := NewSavedExplorationAuthorizer(savedAdapterAccessStub{})
	if err != nil {
		t.Fatal(err)
	}
	admission := &savedAdapterAdmission{}
	executor, err := NewSavedExplorationExecutor(SavedExplorationExecutorOptions{AccessModule: savedAdapterAccessStub{}, Admitter: admission, AuditRecorder: &savedAdapterAudit{}})
	if err != nil {
		t.Fatal(err)
	}
	repository := &savedExportNoReadRepository{}
	service, err := savedapplication.NewService(savedapplication.Options{
		Repository: repository, Authorizer: authorizer, Runtime: provider, Executor: executor,
		Now:           func() time.Time { return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC) },
		NewRevisionID: func() (saved.RevisionID, error) { return "revision-export", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return savedExportFixture{service: service, repository: repository, admission: admission, ownerMetrics: ownerMetrics, viewerMetrics: viewerMetrics}
}

func savedExportSnapshot(t *testing.T, graph projectgraph.ProjectGraph, identity projectgraph.ServingIdentity, subject access.SubjectRef, rowID int64) accesssnapshot.AuthorizationSnapshot {
	return savedExportSnapshotWithMask(t, graph, identity, subject, rowID, "")
}

func savedExportSnapshotWithMask(t *testing.T, graph projectgraph.ProjectGraph, identity projectgraph.ServingIdentity, subject access.SubjectRef, rowID int64, maskExpression string) accesssnapshot.AuthorizationSnapshot {
	t.Helper()
	grants := []accesssnapshot.Grant{
		mustSavedAdapterGrantForResource(t, graph, string(subject.ID)+"-semantic-use", subject, "semantic:sales", access.CapabilityResourceUse),
		mustSavedAdapterGrantForResource(t, graph, string(subject.ID)+"-model-use", subject, "model:orders", access.CapabilityResourceUse),
	}
	expression := fmt.Sprintf(`{"field":"orders.order_id","operator":"equals","values":[%d]}`, rowID)
	compiled, err := accesspolicy.Compile(string(subject.ID)+"-row-filter", accesspolicy.TypeRowFilter, expression)
	if err != nil {
		t.Fatal(err)
	}
	policies := []accesssnapshot.DataPolicy{{
		ID: string(subject.ID) + "-row-filter", Resource: mustSavedExportResource(t, "model:orders", projectgraph.KindModel), Subject: &subject,
		PolicyType: accesspolicy.TypeRowFilter, ExpressionJSON: expression, Compiled: compiled,
	}}
	if maskExpression != "" {
		mask, err := accesspolicy.Compile(string(subject.ID)+"-column-mask", accesspolicy.TypeColumnMask, maskExpression)
		if err != nil {
			t.Fatal(err)
		}
		policies = append(policies, accesssnapshot.DataPolicy{
			ID: string(subject.ID) + "-column-mask", Resource: mustSavedExportResource(t, "model:orders", projectgraph.KindModel), Subject: &subject,
			PolicyType: accesspolicy.TypeColumnMask, ExpressionJSON: maskExpression, Compiled: mask,
		})
	}
	return newSavedAdapterSnapshotWithPolicies(t, graph, identity, nil, grants, policies)
}

func mustSavedExportResource(t *testing.T, id projectgraph.ResourceID, kind projectgraph.Kind) access.ResourceRef {
	t.Helper()
	resource, err := access.NewResourceRef(id, kind)
	if err != nil {
		t.Fatal(err)
	}
	return resource
}

func savedExportSpec() canonical.ExplorationSpec {
	dataset := "orders"
	return canonical.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset,
		Dimensions: []canonical.ExplorationDimensionRef{{Field: "orders.order_id"}},
		Metrics:    []canonical.ExplorationMetricRef{{Field: "order_count"}},
		Filters:    []canonical.ExplorationFilter{}, Sort: []canonical.ExplorationSort{}, Limit: 2,
	}
}

func savedExportDuckDBModel() *semanticmodel.Model {
	model := savedAdapterModel()
	table := model.Tables["orders"]
	table.Dimensions = map[string]semanticmodel.MetricDimension{
		"order_id":       table.Dimensions["order_id"],
		"customer_email": {Field: "orders.customer_email", Table: "orders", Name: "customer_email", Datatype: semanticmodel.DataTypeString},
	}
	model.Tables["orders"] = table
	return model
}

func savedExportDuckDBSpec() canonical.ExplorationSpec {
	spec := savedExportSpec()
	spec.Dimensions = append(spec.Dimensions, canonical.ExplorationDimensionRef{Field: "orders.customer_email"})
	return spec
}

type savedExportRuntimeProvider struct {
	leases map[string]*savedAdapterLease
}

func (p savedExportRuntimeProvider) Acquire(ctx context.Context) (projectruntime.Lease, error) {
	principal, ok := accessmodulePrincipal(ctx)
	if !ok {
		return nil, access.ErrForbidden
	}
	lease, ok := p.leases[principal]
	if !ok {
		return nil, saved.ErrNotFound
	}
	return lease, nil
}

func accessmodulePrincipal(ctx context.Context) (string, bool) {
	principal, ok := accessmodule.PrincipalFromContext(ctx)
	return principal.ID, ok && principal.ID != ""
}

type savedExportMetrics struct {
	*savedAdapterMetrics
}

func (m *savedExportMetrics) SemanticModelProjection(id projectgraph.ResourceID) (*semanticmodel.Model, bool) {
	return m.model, m.model != nil && id == "semantic:sales"
}

func (m *savedExportMetrics) ExecuteDataQuery(ctx context.Context, query dataquery.Query) (dataquery.Result, error) {
	m.query = query
	if err := ctx.Err(); err != nil {
		return dataquery.Result{Status: dataquery.StatusError, ExecutionState: dataquery.ExecutionCanceled}, err
	}
	var rowID int64
	for _, filter := range query.Filters {
		if filter.Field != "orders.order_id" || len(filter.Values) != 1 {
			continue
		}
		switch value := filter.Values[0].(type) {
		case int:
			rowID = int64(value)
		case int64:
			rowID = value
		case float64:
			rowID = int64(value)
		case string:
			rowID, _ = strconv.ParseInt(value, 10, 64)
		}
	}
	if rowID == 0 {
		return dataquery.Result{Status: dataquery.StatusError, ExecutionState: dataquery.ExecutionRejected}, errors.New("RLS policy was not applied")
	}
	return dataquery.Result{
		Columns:   []dataquery.Column{{Name: "order_id"}, {Name: "order_count"}},
		Rows:      []dataquery.Row{{"order_id": rowID, "order_count": int64(1)}},
		TotalRows: 1, TotalRowsKnown: true, RowsReturned: 1,
		Status: dataquery.StatusSuccess, ExecutionState: dataquery.ExecutionSucceeded,
	}, nil
}

func savedExportParquetOrderID(t *testing.T, body []byte) int64 {
	t.Helper()
	table, err := pqarrow.ReadTable(t.Context(), bytes.NewReader(body), nil, pqarrow.ArrowReadProperties{}, memory.DefaultAllocator)
	if err != nil {
		t.Fatal(err)
	}
	defer table.Release()
	values, ok := table.Column(0).Data().Chunk(0).(*array.Int64)
	if !ok || values.Len() != 1 {
		t.Fatalf("Parquet order_id column = %#v, want one int64 value", table.Column(0).Data().Chunk(0))
	}
	return values.Value(0)
}

func savedExportParquetCustomerEmail(t *testing.T, body []byte) string {
	t.Helper()
	table, err := pqarrow.ReadTable(t.Context(), bytes.NewReader(body), nil, pqarrow.ArrowReadProperties{}, memory.DefaultAllocator)
	if err != nil {
		t.Fatal(err)
	}
	defer table.Release()
	values, ok := table.Column(1).Data().Chunk(0).(*array.String)
	if !ok || values.Len() != 1 {
		t.Fatalf("Parquet customer_email column = %#v, want one string value", table.Column(1).Data().Chunk(0))
	}
	return values.Value(0)
}

type savedExportNoReadRepository struct{ calls int }

var _ saved.Repository = (*savedExportNoReadRepository)(nil)

func (r *savedExportNoReadRepository) accessed() error {
	r.calls++
	return errors.New("saved export repository must not be read")
}
func (r *savedExportNoReadRepository) Create(context.Context, saved.CreateInput) (saved.MutationResult, error) {
	return saved.MutationResult{}, r.accessed()
}
func (r *savedExportNoReadRepository) LookupMutation(context.Context, saved.MutationLookupInput) (saved.MutationReplayMetadata, bool, error) {
	return saved.MutationReplayMetadata{}, false, r.accessed()
}
func (r *savedExportNoReadRepository) GetLifecycle(context.Context, saved.ReadInput) (saved.Lifecycle, error) {
	return saved.Lifecycle{}, r.accessed()
}
func (r *savedExportNoReadRepository) GetRevision(context.Context, saved.RevisionReadInput) (saved.Revision, error) {
	return saved.Revision{}, r.accessed()
}
func (r *savedExportNoReadRepository) ListPage(context.Context, saved.ListInput) (saved.ListPage, error) {
	return saved.ListPage{}, r.accessed()
}
func (r *savedExportNoReadRepository) UpdateVersion(context.Context, saved.UpdateVersionInput) (saved.MutationResult, error) {
	return saved.MutationResult{}, r.accessed()
}
func (r *savedExportNoReadRepository) Duplicate(context.Context, saved.DuplicateInput) (saved.MutationResult, error) {
	return saved.MutationResult{}, r.accessed()
}
func (r *savedExportNoReadRepository) List(context.Context, saved.ListInput) ([]saved.Lifecycle, error) {
	return nil, r.accessed()
}
func (r *savedExportNoReadRepository) Archive(context.Context, saved.ArchiveInput) (saved.MutationResult, error) {
	return saved.MutationResult{}, r.accessed()
}
