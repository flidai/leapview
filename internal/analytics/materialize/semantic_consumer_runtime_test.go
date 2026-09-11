package materialize

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/query/planir"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/semanticvalue"
)

func protectedMaterializeRuntime(t *testing.T) *Runtime {
	t.Helper()
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName:   "orders",
				Execution:   semanticmodel.ExecutionDefinition{SQL: "SELECT 1 AS id"},
				GrainEntity: "order",
				Entities:    map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"id"}}},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"id": {Name: "id", Type: "integer", Datatype: semanticmodel.DataTypeInteger},
				},
			},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		AccessPolicy: semanticmodel.SemanticAccessPolicy{
			Datasets: map[string]semanticmodel.SemanticDatasetAccessSpec{"orders": {}},
		},
	}
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	return &Runtime{
		modelID:    "sales",
		model:      model,
		planner:    planner,
		db:         cacheRuntimeDatabase{},
		queryCache: newQueryResultCache(8),
	}
}

func TestProtectedRuntimeRequiresRequestConsumerBeforeExecution(t *testing.T) {
	runtime := protectedMaterializeRuntime(t)
	request := dataquery.Query{
		ModelID: "sales", Kind: dataquery.KindSemanticRows, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 1,
	}
	if _, err := runtime.ExecuteDataQuery(context.Background(), request); err == nil {
		t.Fatal("protected execution without a request consumer succeeded")
	}
	if _, err := runtime.planOwnedArrowQuery(request); err == nil {
		t.Fatal("protected planning without a request consumer succeeded")
	}
}

func TestProtectedRuntimeDeniesUnqualifiedReuseAndBundles(t *testing.T) {
	runtime := protectedMaterializeRuntime(t)
	if _, _, err := runtime.LookupImmutableBytes("tile"); err == nil {
		t.Fatal("protected immutable-byte lookup succeeded")
	}
	if runtime.StoreImmutableBytes("tile", []byte("bytes")) {
		t.Fatal("protected immutable-byte store succeeded")
	}
	if _, err := runtime.CoalesceImmutableBytes(context.Background(), "tile", func(context.Context) error { return nil }); err == nil {
		t.Fatal("protected immutable-byte coalescing succeeded")
	}
	requests := []dataquery.BundleRequest{
		{ID: "one", Query: dataquery.Query{ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders"}},
		{ID: "two", Query: dataquery.Query{ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders"}},
	}
	if _, err := runtime.ExecuteDataQueryBundle(context.Background(), requests); err == nil {
		t.Fatal("protected bundle execution succeeded")
	}
}

func TestRuntimeFailsClosedWhenSemanticSnapshotIsUnknown(t *testing.T) {
	runtime := &Runtime{
		model:      &semanticmodel.Model{Name: "sales"},
		db:         cacheRuntimeDatabase{},
		queryCache: newQueryResultCache(8),
	}
	if _, _, err := runtime.LookupImmutableBytes("tile"); err == nil {
		t.Fatal("immutable-byte lookup succeeded with an unknown semantic snapshot")
	}
	if runtime.StoreImmutableBytes("tile", []byte("bytes")) {
		t.Fatal("immutable-byte store succeeded with an unknown semantic snapshot")
	}
	executed := false
	if _, err := runtime.CoalesceImmutableBytes(context.Background(), "tile", func(context.Context) error {
		executed = true
		return nil
	}); err == nil {
		t.Fatal("immutable-byte coalescing succeeded with an unknown semantic snapshot")
	}
	if executed {
		t.Fatal("immutable-byte coalescing callback ran with an unknown semantic snapshot")
	}
	requests := []dataquery.BundleRequest{
		{ID: "one", Query: dataquery.Query{ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders"}},
		{ID: "two", Query: dataquery.Query{ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Target: "orders"}},
	}
	if _, err := runtime.ExecuteDataQueryBundle(context.Background(), requests); err == nil {
		t.Fatal("bundle execution succeeded with an unknown semantic snapshot")
	}
}

type semanticConsumerTestGovernor struct {
	consumer *semanticquery.SemanticAccessConsumer
	binding  semanticquery.SemanticAccessConsumerBinding
}

func (g semanticConsumerTestGovernor) GovernDataQuery(_ context.Context, request dataquery.Query) (dataquery.Query, dataquery.ResultTransformer, error) {
	return request, nil, nil
}

func (g semanticConsumerTestGovernor) BindSemanticConsumer(ctx context.Context, modelID string) (context.Context, error) {
	if modelID != g.binding.ModelID {
		return ctx, context.Canceled
	}
	return semanticquery.WithSemanticAccessConsumer(ctx, g.consumer, g.binding), nil
}

type semanticConsumerArrowDatabase struct {
	cacheRuntimeDatabase
	afterSchema func()
	assertPlan  func(semanticquery.Plan)
	queries     atomic.Int32
}

func (d *semanticConsumerArrowDatabase) QueryArrow(ctx context.Context, plan semanticquery.Plan, sink arrowquery.Sink) error {
	d.queries.Add(1)
	if d.assertPlan != nil {
		d.assertPlan(plan)
	}
	fields := make([]arrow.Field, len(plan.Columns))
	arrays := make([]arrow.Array, len(plan.Columns))
	for index, column := range plan.Columns {
		fields[index] = arrow.Field{Name: column, Type: arrow.PrimitiveTypes.Int64}
		builder := array.NewInt64Builder(memory.DefaultAllocator)
		builder.Append(1)
		arrays[index] = builder.NewArray()
		builder.Release()
	}
	schema := arrow.NewSchema(fields, nil)
	if err := sink.WriteSchema(schema); err != nil {
		for _, value := range arrays {
			value.Release()
		}
		return err
	}
	if d.afterSchema != nil {
		d.afterSchema()
	}
	record := array.NewRecordBatch(schema, arrays, 1)
	for _, value := range arrays {
		value.Release()
	}
	defer record.Release()
	if err := arrowquery.ConsumeResultBudget(ctx, record); err != nil {
		return err
	}
	return sink.WriteRecord(record)
}

type semanticConsumerTestSink struct{ rows int }

func (s *semanticConsumerTestSink) WriteSchema(*arrow.Schema) error { return nil }
func (s *semanticConsumerTestSink) WriteRecord(record arrow.RecordBatch) error {
	s.rows += int(record.NumRows())
	return nil
}

func protectedConsumerFixture(t *testing.T) (*Runtime, semanticConsumerTestGovernor, *int64) {
	return protectedConsumerFixtureWithMemberAccess(t, true)
}

func protectedConsumerFixtureWithMemberAccess(t *testing.T, allowMetric bool) (*Runtime, semanticConsumerTestGovernor, *int64) {
	return protectedConsumerFixtureWithModelModifier(t, allowMetric, nil)
}

func protectedConsumerFixtureWithModelModifier(t *testing.T, allowMetric bool, modify func(*semanticmodel.Model)) (*Runtime, semanticConsumerTestGovernor, *int64) {
	t.Helper()
	literal, err := semanticmodel.NewSemanticAccessLiteral(json.Number("1"))
	if err != nil {
		t.Fatal(err)
	}
	otherLiteral, err := semanticmodel.NewSemanticAccessLiteral(json.Number("2"))
	if err != nil {
		t.Fatal(err)
	}
	metricValues, dimensionValues := []semanticmodel.SemanticAccessLiteral{literal}, []semanticmodel.SemanticAccessLiteral{otherLiteral}
	if !allowMetric {
		metricValues, dimensionValues = dimensionValues, metricValues
	}
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName:   "orders",
				Execution:   semanticmodel.ExecutionDefinition{SQL: "SELECT 1 AS id"},
				GrainEntity: "order",
				Entities:    map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"id"}}},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"id":     {Field: "orders.id", Table: "orders", Name: "id", Type: "number", Datatype: semanticmodel.DataTypeInteger},
					"shared": {Field: "orders.shared", Table: "orders", Name: "shared", Type: "number", Datatype: semanticmodel.DataTypeInteger},
				},
			},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"id":     {Name: "id", Datatype: semanticmodel.DataTypeInteger, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.id"}}},
			"shared": {Name: "shared", Datatype: semanticmodel.DataTypeInteger, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.shared"}}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"shared": {Name: "shared", Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.id"}},
		},
		AccessPolicy: semanticmodel.SemanticAccessPolicy{
			AccessGrants: map[string]semanticmodel.SemanticAccessGrantSpec{
				"canViewAccount":   {UserAttribute: "accountIds", AllowedValues: []semanticmodel.SemanticAccessLiteral{literal}},
				"canViewMetric":    {UserAttribute: "accountIds", AllowedValues: metricValues},
				"canViewDimension": {UserAttribute: "accountIds", AllowedValues: dimensionValues},
			},
			Datasets: map[string]semanticmodel.SemanticDatasetAccessSpec{
				"orders": {
					RequiredAccessGrants: []string{"canViewAccount"},
					AccessFilters:        []semanticmodel.SemanticAccessFilterSpec{{Field: "id", UserAttribute: "accountIds"}},
				},
			},
			Dimensions: map[string][]string{"id": {"canViewAccount"}, "shared": {"canViewDimension"}},
			Metrics:    map[string][]string{"shared": {"canViewMetric"}},
		},
	}
	if modify != nil {
		modify(model)
	}
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	definition := access.SemanticAttributeDefinition{ID: "def-account", Name: "accountIds", Type: semanticvalue.TypeInteger, Shape: access.SemanticAttributeList, Profile: semanticvalue.Profile, DefinitionVersion: 1, LifecycleState: access.SemanticAttributeActive, Enabled: true, Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}}}
	registryDigest, err := access.SemanticAttributeRegistryDigest(semanticvalue.Profile, []access.SemanticAttributeDefinition{definition})
	if err != nil {
		t.Fatal(err)
	}
	registry := access.SemanticAttributeRegistrySnapshot{State: access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 1, Digest: registryDigest}, Definitions: []access.SemanticAttributeDefinition{definition}}
	values, valueDigest, err := access.CanonicalSemanticAttributeValues(definition, []int64{1})
	if err != nil {
		t.Fatal(err)
	}
	attribute := access.EffectiveSemanticAttribute{DefinitionID: definition.ID, DefinitionName: definition.Name, DefinitionVersion: definition.DefinitionVersion, Type: definition.Type, Shape: definition.Shape, CanonicalValues: values, ValueDigest: valueDigest, Source: "direct"}
	assignment := access.SemanticAttributeAssignment{ID: "assignment-account", DefinitionID: definition.ID, DefinitionName: definition.Name, DefinitionVersion: definition.DefinitionVersion, Type: definition.Type, Shape: definition.Shape, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "alice"}, CanonicalValues: values, ValueDigest: valueDigest, AssignmentVersion: 1}
	controlDigest, err := access.SemanticAttributeControlDigest([]access.SemanticAttributeAssignment{assignment}, nil)
	if err != nil {
		t.Fatal(err)
	}
	control := access.SemanticAttributeControlSnapshot{State: access.SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 1, Digest: controlDigest}, Assignments: []access.SemanticAttributeAssignment{assignment}}
	directEvidence, err := access.NewSemanticAttributeDirectEvidence("instance-1", "alice", []access.SubjectRef{{Kind: access.SubjectKindPrincipal, ID: "alice"}}, control, []access.EffectiveSemanticAttribute{attribute})
	if err != nil {
		t.Fatal(err)
	}
	attributeDigest, err := semanticquery.EffectiveSemanticAttributeDigest([]access.EffectiveSemanticAttribute{attribute})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := semanticquery.SemanticAccessAttributeSnapshot{InstanceID: "instance-1", PrincipalID: "alice", ActorID: "alice", Registry: registry, Control: control, EffectiveAttributes: []access.EffectiveSemanticAttribute{attribute}, EffectiveAttributeDigest: attributeDigest, DirectAssignmentEvidence: directEvidence}
	authority := semanticquery.SemanticAccessAuthority{InstanceID: "instance-1", Registry: registry, Control: control, ObservedAt: time.Now().UTC()}
	consumer, err := semanticquery.NewSemanticAccessConsumer(planner, semanticquery.SemanticAccessConsumerConfig{
		InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", ModelID: "sales", Generation: "generation-1", PrincipalID: "alice",
		PublicationPolicy: semanticCacheTestPublicationPolicy("instance-1", "sales"),
		Authority: func() (semanticquery.SemanticAccessAttributeSnapshot, semanticquery.SemanticAccessAuthority, error) {
			return snapshot, authority, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := projectgraph.NewResourceID("project:test")
	if err != nil {
		t.Fatal(err)
	}
	partition, err := resultidentity.NewPartition(resultidentity.PartitionInput{Kind: resultidentity.PartitionProduction, TargetID: "instance-1", ProjectID: projectID, Environment: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	controlRevision := &authority.Control.State.Revision
	database := &semanticConsumerArrowDatabase{}
	runtime := &Runtime{modelID: "sales", model: model, planner: planner, servingStateID: "generation-1", resultPartition: partition, db: database, queryCache: newQueryResultCache(8)}
	governor := semanticConsumerTestGovernor{consumer: consumer, binding: semanticquery.SemanticAccessConsumerBinding{InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", Generation: "generation-1", ModelID: "sales", PrincipalID: "alice"}}
	return runtime, governor, controlRevision
}

func TestProtectedRuntimeUsesGovernorConsumerAndReleasesAllowedArrow(t *testing.T) {
	runtime, governor, _ := protectedConsumerFixture(t)
	database := runtime.db.(*semanticConsumerArrowDatabase)
	database.assertPlan = func(plan semanticquery.Plan) {
		barriers, predicates := 0, 0
		for _, node := range plan.IR.Nodes {
			barrier, ok := node.(planir.SecurityBarrier)
			if !ok {
				if pointer, pointerOK := node.(*planir.SecurityBarrier); pointerOK && pointer != nil {
					barrier, ok = *pointer, true
				}
			}
			if !ok {
				continue
			}
			barriers++
			if barrier.Predicate != nil {
				predicates++
			}
		}
		if barriers == 0 || predicates == 0 {
			t.Errorf("protected plan barriers=%d predicates=%d, want a predicate-bearing security barrier; plan=%s", barriers, predicates, plan.SQL)
		}
	}
	sink := &semanticConsumerTestSink{}
	request := dataquery.Query{ModelID: "sales", PrincipalID: "alice", Kind: dataquery.KindSemanticRows, Target: "orders", Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 1}
	result, err := runtime.ExecuteDataQueryArrow(dataquery.WithGovernor(context.Background(), governor), request, sink)
	if err != nil {
		t.Fatal(err)
	}
	if sink.rows != 1 || result.RowsReturned != 1 || database.queries.Load() != 1 {
		t.Fatalf("rows=%d result=%d queries=%d", sink.rows, result.RowsReturned, database.queries.Load())
	}
}

func TestProtectedRuntimeUsesIndependentlyAdmittedTotalPlan(t *testing.T) {
	runtime, governor, _ := protectedConsumerFixture(t)
	database := runtime.db.(*semanticConsumerArrowDatabase)
	request := dataquery.Query{ModelID: "sales", PrincipalID: "alice", Kind: dataquery.KindSemanticRows, Target: "orders", Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, IncludeTotal: true, Limit: 1}
	result, err := runtime.ExecuteDataQuery(dataquery.WithGovernor(context.Background(), governor), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TotalRowsKnown || result.TotalRows != 1 || database.queries.Load() != 2 {
		t.Fatalf("total=%d known=%v queries=%d", result.TotalRows, result.TotalRowsKnown, database.queries.Load())
	}
}

func TestProtectedRuntimeStopsArrowBeforeReleaseAfterAuthorityChange(t *testing.T) {
	runtime, governor, controlRevision := protectedConsumerFixture(t)
	database := runtime.db.(*semanticConsumerArrowDatabase)
	database.afterSchema = func() { atomic.AddInt64(controlRevision, 1) }
	sink := &semanticConsumerTestSink{}
	request := dataquery.Query{ModelID: "sales", PrincipalID: "alice", Kind: dataquery.KindSemanticRows, Target: "orders", Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 1}
	_, err := runtime.ExecuteDataQueryArrow(dataquery.WithGovernor(context.Background(), governor), request, sink)
	if err == nil {
		t.Fatal("authority change during Arrow execution was accepted")
	}
	if sink.rows != 0 {
		t.Fatalf("rows released after authority change = %d", sink.rows)
	}
	if database.queries.Load() != 1 {
		t.Fatalf("queries=%d, want one physical query", database.queries.Load())
	}
}

func TestProtectedRuntimeStopsBufferedArrowBeforeReleaseAfterAuthorityChange(t *testing.T) {
	runtime, governor, controlRevision := protectedConsumerFixture(t)
	database := runtime.db.(*semanticConsumerArrowDatabase)
	database.afterSchema = func() { atomic.AddInt64(controlRevision, 1) }
	request := dataquery.Query{ModelID: "sales", PrincipalID: "alice", Kind: dataquery.KindSemanticRows, Target: "orders", Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 1}
	result, err := runtime.ExecuteDataQuery(dataquery.WithGovernor(context.Background(), governor), request)
	if err == nil {
		t.Fatal("authority change during buffered Arrow execution was accepted")
	}
	if len(result.Rows) != 0 {
		t.Fatalf("buffered rows released after authority change = %d", len(result.Rows))
	}
	if database.queries.Load() != 1 {
		t.Fatalf("queries=%d, want one physical query", database.queries.Load())
	}
}
