package compiler

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	materialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/query/planir"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/flidai/leapview/internal/project/contractprojection"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/semanticvalue"
)

const semanticAccessQualificationDigest = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestProtectedContractRegistryPlannerAndCacheQualification is intentionally
// one cross-package qualification seam. The focused compiler tests above it
// prove generated DTO lowering, while query tests prove policy/planner details;
// this test proves that lowered authored policy reaches the real materialize
// consumer and guarded result cache without introducing another authority.
func TestProtectedContractRegistryPlannerAndCacheQualification(t *testing.T) {
	model := semanticAccessQualificationModel(t)
	dataset := model.Datasets["orders"]
	if len(model.AccessGrants) != 1 || len(dataset.RequiredAccessGrants) != 1 || len(dataset.AccessFilters) != 1 {
		t.Fatalf("lowered protected contract lost grant/filter: grants=%#v dataset=%#v", model.AccessGrants, dataset)
	}
	if dataset.RequiredAccessGrants[0] != "canViewSales" || dataset.AccessFilters[0].Field != "region" || dataset.AccessFilters[0].UserAttribute != "region" {
		t.Fatalf("lowered protected policy = %#v", dataset)
	}

	definition := access.SemanticAttributeDefinition{
		ID: "definition-region", Name: "region", Type: semanticvalue.TypeString,
		Shape: access.SemanticAttributeScalar, Profile: semanticvalue.Profile,
		DefinitionVersion: 1, LifecycleState: access.SemanticAttributeActive, Enabled: true,
	}
	registry := access.SemanticAttributeRegistrySnapshot{
		State:       access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 7, Digest: semanticAccessQualificationDigest},
		Definitions: []access.SemanticAttributeDefinition{definition},
	}
	initial := semanticAccessQualificationResolution(t, registry, definition, "principal-1", "us")
	authority := &semanticAccessQualificationAuthority{registry: registry, resolutions: []access.SemanticAttributeResolution{initial}}

	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatalf("open DuckDB: %v", err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders_model (id BIGINT, region VARCHAR)",
		"INSERT INTO model.orders_model VALUES (1, 'us'), (2, 'eu')",
	} {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("setup DuckDB: %v", err)
		}
	}
	database := &semanticAccessQualificationDatabase{db: db}

	partition, err := resultidentity.NewPartition(resultidentity.PartitionInput{
		Kind: resultidentity.PartitionProduction, ProjectID: "project:test", Environment: "test",
	})
	if err != nil {
		t.Fatalf("result partition: %v", err)
	}
	modelDigest, err := semanticquery.SemanticModelDigest(model)
	if err != nil {
		t.Fatalf("semantic model digest: %v", err)
	}
	evidence, err := resultidentity.NewEvidence(resultidentity.EvidenceInput{
		SemanticModelID: "sales", SemanticModelDigest: modelDigest,
		DatasetRelations: []resultidentity.DatasetRelation{{Dataset: "orders", Relation: resultidentity.RelationRevision{
			RelationID: "model:orders", RevisionDigest: semanticAccessQualificationDigest,
		}}},
		BindingFingerprint: semanticAccessQualificationDigest, RuntimeDigest: semanticAccessQualificationDigest,
		CapabilityDigest: semanticAccessQualificationDigest,
	})
	if err != nil {
		t.Fatalf("dependency evidence: %v", err)
	}
	binding := resultidentity.SemanticLifecycle{
		InstanceID: "instance:test", ProjectID: "project:test", AuthoredID: "sales", ResourceKind: projectgraph.KindSemanticModel,
		Sequence: 7, ActiveBundleID: "bundle:test", PublicationVersion: "1.0.0",
		ProjectionProfile: contractprojection.Profile, PublicationDigest: semanticAccessQualificationDigest,
		AuthorizationRevision: 11,
	}
	runtime, err := materialize.NewRuntimeView(t.Context(), materialize.RuntimeConfig{
		ModelID: "sales", Model: model, Database: database, Sources: semanticAccessQualificationSources{},
		TableRelation: func(table string) (string, error) { return "model." + table, nil },
		SnapshotOnly:  true, ServingStateID: "generation:test", ResultPartition: partition,
		DependencyEvidence: evidence, SemanticAccessAuthority: authority,
		SemanticAccessCompileContext: &semanticquery.SemanticAccessCompileContext{Registry: registry},
		SemanticCache:                &materialize.SemanticCacheConfig{Binding: binding, ReadCurrent: func(context.Context) (resultidentity.SemanticLifecycle, error) { return binding, nil }},
	})
	if err != nil {
		t.Fatalf("open materialize runtime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.CloseView() })

	request := dataquery.Query{
		ProjectID: "project:test", Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardAggregate,
		PrincipalID: "principal-1", RequestID: "qualification-request", ModelID: "sales",
		Kind: dataquery.KindSemanticAggregate, Target: "orders",
		Metrics:                    []dataquery.Field{{Field: "order_count", Alias: "order_count"}},
		EffectivePolicyFingerprint: semanticAccessQualificationDigest,
	}
	first, err := runtime.ExecuteDataQuery(t.Context(), request)
	if err != nil {
		t.Fatalf("protected first query: %v", err)
	}
	if first.CacheOutcome != dataquery.CacheMiss {
		t.Fatalf("first cache outcome = %q, want miss", first.CacheOutcome)
	}
	if got := database.queries.Load(); got != 1 {
		t.Fatalf("first physical query count = %d, want 1", got)
	}
	if len(first.Rows) != 1 || len(first.Columns) != 1 {
		t.Fatalf("restricted result shape = columns=%#v rows=%#v", first.Columns, first.Rows)
	}
	value, ok := first.Rows[0][first.Columns[0].Name].(int64)
	if !ok || value != 1 {
		t.Fatalf("restricted aggregate = %#v, want one authorized row", first.Rows[0])
	}

	plan := database.lastPlan()
	if strings.Count(plan.SQL, "?") != 1 || len(plan.Args) != 1 || plan.Args[0] != "us" {
		t.Fatalf("security filter was not parameterized exactly once: SQL=%q args=%#v", plan.SQL, plan.Args)
	}
	scans, barriers := 0, 0
	for _, node := range plan.IR.Nodes {
		switch node.Kind() {
		case planir.KindScanDataset:
			scans++
		case planir.KindSecurityBarrier:
			barriers++
		}
	}
	if scans == 0 || scans != barriers {
		t.Fatalf("protected plan scans=%d barriers=%d", scans, barriers)
	}

	second, err := runtime.ExecuteDataQuery(t.Context(), request)
	if err != nil {
		t.Fatalf("protected cache hit: %v", err)
	}
	if second.CacheOutcome != dataquery.CacheHit || database.queries.Load() != 1 {
		t.Fatalf("cache reuse outcome=%q physical queries=%d, want hit/1", second.CacheOutcome, database.queries.Load())
	}
	if !reflect.DeepEqual(second.Rows, first.Rows) {
		t.Fatalf("cached rows = %#v, want the same restricted rows as the miss %#v", second.Rows, first.Rows)
	}

	changed := initial
	changed.ControlState.Revision++
	changed.ControlState.Digest = "sha256:" + "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	authority.Set([]access.SemanticAttributeResolution{initial, changed})
	if _, err := runtime.ExecuteDataQuery(t.Context(), request); err == nil {
		t.Fatal("stale authority snapshot was accepted before a physical query")
	}
	if got := database.queries.Load(); got != 1 {
		t.Fatalf("stale authority physical queries = %d, want 1", got)
	}

	denied := semanticAccessQualificationResolution(t, registry, definition, "principal-1", "eu")
	authority.Set([]access.SemanticAttributeResolution{denied})
	if _, err := runtime.ExecuteDataQuery(t.Context(), request); err == nil {
		t.Fatal("principal without the authored allowed region grant was accepted")
	}
	if got := database.queries.Load(); got != 1 {
		t.Fatalf("denied principal physical queries = %d, want 1", got)
	}
}

func semanticAccessQualificationModel(t testing.TB) *semanticmodel.Model {
	t.Helper()
	spec, _, err := decodeSemanticModelResource("semantic-model.yaml", []byte(`apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic-model:sales, name: sales}
spec:
  accessGrants:
    canViewSales: {userAttribute: region, allowedValues: [us]}
  datasets:
    orders:
      model: orders_model
      requiredAccessGrants: [canViewSales]
      accessFilters: [{field: region, userAttribute: region}]
  dimensions:
    region:
      datatype: String
      bindings: {orders: {field: orders.region}}
  metrics:
    order_count:
      type: aggregate
      dataset: orders
      aggregation: count
      input: {field: orders.id}
`))
	if err != nil {
		t.Fatalf("decode protected semantic contract: %v", err)
	}
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{"orders_model": {
			ModelName: "orders_model", Execution: semanticmodel.ExecutionDefinition{SQL: "SELECT id, region FROM model.orders_model"},
			Dimensions: map[string]semanticmodel.MetricDimension{
				"id":     {Name: "id", Type: "integer", Datatype: semanticmodel.DataTypeInteger},
				"region": {Name: "region", Type: "string", Datatype: semanticmodel.DataTypeString},
			},
			Entities: map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"id"}}}, GrainEntity: "order",
		}},
	}
	if err := applySemanticModelSpec(model, spec); err != nil {
		t.Fatalf("lower protected semantic contract: %v", err)
	}
	return model
}

func semanticAccessQualificationResolution(t testing.TB, registry access.SemanticAttributeRegistrySnapshot, definition access.SemanticAttributeDefinition, principal, value string) access.SemanticAttributeResolution {
	t.Helper()
	values, digest, err := access.CanonicalSemanticAttributeValues(definition, value)
	if err != nil {
		t.Fatalf("canonical semantic attribute: %v", err)
	}
	return access.SemanticAttributeResolution{
		Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principal}, Registry: registry,
		ControlState: access.SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 11, Digest: semanticAccessQualificationDigest},
		Attributes: []access.EffectiveSemanticAttribute{{
			DefinitionID: definition.ID, DefinitionName: definition.Name, DefinitionVersion: definition.DefinitionVersion,
			Type: definition.Type, Shape: definition.Shape, CanonicalValues: values, ValueDigest: digest, Source: "direct",
		}},
	}
}

type semanticAccessQualificationAuthority struct {
	mu           sync.Mutex
	registry     access.SemanticAttributeRegistrySnapshot
	resolutions  []access.SemanticAttributeResolution
	resolutionAt int
}

func (a *semanticAccessQualificationAuthority) SemanticAttributeRegistry(context.Context) (access.SemanticAttributeRegistrySnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.registry, nil
}

func (a *semanticAccessQualificationAuthority) ResolveSemanticAttributes(context.Context) (access.SemanticAttributeResolution, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.resolutions) == 0 {
		return access.SemanticAttributeResolution{}, fmt.Errorf("semantic resolution unavailable")
	}
	index := a.resolutionAt
	if index >= len(a.resolutions) {
		index = len(a.resolutions) - 1
	}
	a.resolutionAt++
	return a.resolutions[index], nil
}

func (a *semanticAccessQualificationAuthority) Set(resolutions []access.SemanticAttributeResolution) {
	a.mu.Lock()
	a.resolutions = append([]access.SemanticAttributeResolution(nil), resolutions...)
	a.resolutionAt = 0
	a.mu.Unlock()
}

type semanticAccessQualificationSources struct{}

func (semanticAccessQualificationSources) Prepare(context.Context, *semanticmodel.Model) (materialize.PreparedSources, error) {
	return nil, nil
}

type semanticAccessQualificationDatabase struct {
	db      *sql.DB
	queries atomic.Int32
	mu      sync.Mutex
	plans   []semanticquery.Plan
}

func (d *semanticAccessQualificationDatabase) Exec(ctx context.Context, statement string) error {
	_, err := d.db.ExecContext(ctx, statement)
	return err
}

func (d *semanticAccessQualificationDatabase) Close() error { return d.db.Close() }
func (d *semanticAccessQualificationDatabase) Path() string {
	return "duckdb://semantic-access-qualification"
}

func (d *semanticAccessQualificationDatabase) QueryArrow(ctx context.Context, plan semanticquery.Plan, sink arrowquery.Sink) error {
	d.queries.Add(1)
	d.mu.Lock()
	d.plans = append(d.plans, plan)
	d.mu.Unlock()
	rows, err := d.db.QueryContext(ctx, plan.SQL, plan.Args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	values := make([][]any, 0, 1)
	for rows.Next() {
		row := make([]any, len(columns))
		pointers := make([]any, len(row))
		for index := range row {
			pointers[index] = &row[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return err
		}
		values = append(values, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	fields := make([]arrow.Field, len(columns))
	arrays := make([]arrow.Array, len(columns))
	for columnIndex, column := range columns {
		fields[columnIndex] = arrow.Field{Name: column, Type: arrow.PrimitiveTypes.Int64}
		builder := array.NewInt64Builder(memory.DefaultAllocator)
		for _, row := range values {
			if row[columnIndex] == nil {
				builder.AppendNull()
				continue
			}
			value, ok := row[columnIndex].(int64)
			if !ok {
				builder.Release()
				return fmt.Errorf("column %q returned %T, want int64", column, row[columnIndex])
			}
			builder.Append(value)
		}
		arrays[columnIndex] = builder.NewArray()
		builder.Release()
	}
	schema := arrow.NewSchema(fields, nil)
	if err := sink.WriteSchema(schema); err != nil {
		for _, value := range arrays {
			value.Release()
		}
		return err
	}
	record := array.NewRecordBatch(schema, arrays, int64(len(values)))
	for _, value := range arrays {
		value.Release()
	}
	defer record.Release()
	if err := arrowquery.ConsumeResultBudget(ctx, record); err != nil {
		return err
	}
	return sink.WriteRecord(record)
}

func (d *semanticAccessQualificationDatabase) lastPlan() semanticquery.Plan {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.plans) == 0 {
		return semanticquery.Plan{}
	}
	return d.plans[len(d.plans)-1]
}
