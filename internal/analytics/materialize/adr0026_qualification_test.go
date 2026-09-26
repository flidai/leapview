package materialize

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/semanticvalue"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	adr0026AccessActor     = "50000000-0000-0000-0000-000000000001"
	adr0026AccessPrincipal = "50000000-0000-0000-0000-000000000002"
)

// TestADR0026AF03PostgreSQLCommittedAttributeMutationInvalidatesCache proves
// that a committed Access control change cannot leave a protected cached Arrow
// result usable. The guard reads the same durable control state that a live
// semantic consumer uses; it is intentionally not a test-only revision flag.
func TestADR0026AF03PostgreSQLCommittedAttributeMutationInvalidatesCache(t *testing.T) {
	fixture := newADR0026PostgresAccessFixture(t)
	cache := newQueryResultCache(4)
	t.Cleanup(func() { _ = cache.close() })
	partition := materializeTestPartition(t, resultidentity.PartitionProduction, "")
	dependency := protectedCacheTestDependency(t, semanticCacheTestIdentity())
	request := semanticCacheTestRequest()
	initial := fixture.controlState(t)
	guard := fixture.controlGuard(initial)

	first, err := cache.executeArrow(context.Background(), request, partition, dependency, "SELECT 1", nowForCacheTest(), func(context.Context) (arrowQueryExecution, error) {
		return newProtectedCacheArrowExecution()
	}, guard)
	if err != nil {
		t.Fatalf("initial protected cache execution: %v", err)
	}
	if first.CacheOutcome != dataquery.CacheMiss || len(first.Rows) != 1 {
		t.Fatalf("initial cache result = outcome %q rows %d, want miss/one row", first.CacheOutcome, len(first.Rows))
	}

	fixture.narrowToEMEA(t)
	var physicalExecutions atomic.Int32
	result, err := cache.executeArrow(context.Background(), request, partition, dependency, "SELECT 1", nowForCacheTest(), func(context.Context) (arrowQueryExecution, error) {
		physicalExecutions.Add(1)
		return newProtectedCacheArrowExecution()
	}, guard)
	if err == nil {
		t.Fatal("cache reused a result after committed attribute narrowing")
	}
	if !errors.Is(err, errADR0026AuthorityChanged) {
		t.Fatalf("post-commit cache error = %v, want authority-change denial", err)
	}
	if result.Rows != nil {
		t.Fatalf("stale cached rows were delivered: %d", len(result.Rows))
	}
	if physicalExecutions.Load() != 0 {
		t.Fatalf("post-commit cache lookup executed physical query %d times", physicalExecutions.Load())
	}
}

// TestADR0026AF03PostgreSQLCommittedAttributeMutationStopsCoalescedFlight
// holds one physical execution open while a second caller joins its flight.
// The committed Access mutation wins before the owner can store; both callers
// therefore receive a denial and no stale in-flight value is published.
func TestADR0026AF03PostgreSQLCommittedAttributeMutationStopsCoalescedFlight(t *testing.T) {
	fixture := newADR0026PostgresAccessFixture(t)
	cache := newQueryResultCache(4)
	t.Cleanup(func() { _ = cache.close() })
	partition := materializeTestPartition(t, resultidentity.PartitionProduction, "")
	dependency := protectedCacheTestDependency(t, semanticCacheTestIdentity())
	request := semanticCacheTestRequest()
	initial := fixture.controlState(t)

	executionStarted := make(chan struct{})
	releaseExecution := make(chan struct{})
	ownerResult := make(chan dataquery.Result, 1)
	ownerError := make(chan error, 1)
	// The third guard call is the waiter's pre-flight check: the owner's first
	// check, flight check, and blocked physical execution account for the first
	// two calls. Holding here makes the join deterministic before mutation.
	waiterEntered := make(chan struct{})
	allowWaiter := make(chan struct{})
	var guardCalls atomic.Int32
	coalescedGuard := func(ctx context.Context) error {
		call := guardCalls.Add(1)
		if call == 3 {
			close(waiterEntered)
			<-allowWaiter
		}
		return fixture.controlGuard(initial)(ctx)
	}
	go func() {
		result, err := cache.executeArrow(context.Background(), request, partition, dependency, "SELECT 1", nowForCacheTest(), func(context.Context) (arrowQueryExecution, error) {
			close(executionStarted)
			<-releaseExecution
			return newProtectedCacheArrowExecution()
		}, coalescedGuard)
		ownerResult <- result
		ownerError <- err
	}()
	<-executionStarted

	waiterResult := make(chan dataquery.Result, 1)
	waiterError := make(chan error, 1)
	var waiterPhysicalExecutions atomic.Int32
	go func() {
		result, err := cache.executeArrow(context.Background(), request, partition, dependency, "SELECT 1", nowForCacheTest(), func(context.Context) (arrowQueryExecution, error) {
			waiterPhysicalExecutions.Add(1)
			return arrowQueryExecution{}, errors.New("coalesced waiter became a second physical execution")
		}, coalescedGuard)
		waiterResult <- result
		waiterError <- err
	}()
	<-waiterEntered
	fixture.narrowToEMEA(t)
	close(allowWaiter)
	close(releaseExecution)

	if err := <-ownerError; !errors.Is(err, errADR0026AuthorityChanged) {
		t.Fatalf("owner coalesced result error = %v, want authority-change denial", err)
	}
	if err := <-waiterError; !errors.Is(err, errADR0026AuthorityChanged) {
		t.Fatalf("waiter coalesced result error = %v, want authority-change denial", err)
	}
	owner := <-ownerResult
	waiter := <-waiterResult
	if owner.Rows != nil || waiter.Rows != nil {
		t.Fatalf("coalesced stale rows delivered: owner=%d waiter=%d", len(owner.Rows), len(waiter.Rows))
	}
	if waiterPhysicalExecutions.Load() != 0 {
		t.Fatalf("coalesced waiter executed a second physical query %d times", waiterPhysicalExecutions.Load())
	}

	// A fresh valid authority must execute again; the failed flight did not
	// repopulate the cache with the old all-regions result.
	fixture.restoreAllRegions(t)
	fresh, err := cache.executeArrow(context.Background(), request, partition, dependency, "SELECT 1", nowForCacheTest(), func(context.Context) (arrowQueryExecution, error) {
		return newProtectedCacheArrowExecution()
	}, fixture.controlGuard(fixture.controlState(t)))
	if err != nil {
		t.Fatalf("fresh protected execution after coalesced denial: %v", err)
	}
	if fresh.CacheOutcome != dataquery.CacheMiss || len(fresh.Rows) != 1 {
		t.Fatalf("fresh protected execution = outcome %q rows %d, want miss/one row", fresh.CacheOutcome, len(fresh.Rows))
	}
}

// TestADR0026AF10PostgreSQLCommittedAttributeMutationStopsNextArrowBatch
// qualifies the output release boundary with two bounded batches. One batch
// already released before revocation is allowed to finish; after the durable
// mutation commits, the next batch is rejected before reaching downstream.
func TestADR0026AF10PostgreSQLCommittedAttributeMutationStopsNextArrowBatch(t *testing.T) {
	fixture := newADR0026PostgresAccessFixture(t)
	model := adr0026ProtectedRegionModel(t)
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := fixture.repo.ResolveSemanticAttributes(t.Context(), access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: adr0026AccessPrincipal})
	if err != nil {
		t.Fatalf("resolve initial semantic attributes: %v", err)
	}
	pinned, _, err := semanticquery.SemanticAccessResolutionSnapshot("instance-1", adr0026AccessPrincipal, resolved)
	if err != nil {
		t.Fatalf("build initial semantic snapshot: %v", err)
	}
	consumer, err := semanticquery.NewSemanticAccessConsumer(planner, semanticquery.SemanticAccessConsumerConfig{
		InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", ModelID: "sales", Generation: "generation-1", PrincipalID: adr0026AccessPrincipal,
		Authority: func() (semanticquery.SemanticAccessAttributeSnapshot, semanticquery.SemanticAccessAuthority, error) {
			current, resolveErr := fixture.repo.ResolveSemanticAttributes(t.Context(), access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: adr0026AccessPrincipal})
			if resolveErr != nil {
				return semanticquery.SemanticAccessAttributeSnapshot{}, semanticquery.SemanticAccessAuthority{}, resolveErr
			}
			_, currentAuthority, snapshotErr := semanticquery.SemanticAccessResolutionSnapshot("instance-1", adr0026AccessPrincipal, current)
			if snapshotErr != nil {
				return semanticquery.SemanticAccessAttributeSnapshot{}, semanticquery.SemanticAccessAuthority{}, snapshotErr
			}
			// The consumer pins the effective values at admission. Returning the
			// current authority with that pinned snapshot models the production
			// request-bound consumer contract and lets ValidatePlan detect the
			// committed control/effective-value change.
			return pinned, currentAuthority, nil
		},
	})
	if err != nil {
		t.Fatalf("create protected semantic consumer: %v", err)
	}
	plan, err := consumer.Planner().PlanRows(semanticquery.RowRequest{Dataset: "orders", Dimensions: []semanticquery.Field{{Field: "orders.id", Alias: "id"}}})
	if err != nil {
		t.Fatalf("plan governed region query: %v", err)
	}

	downstream := &adr0026ArrowCaptureSink{}
	guarded := semanticConsumerSink{consumer: consumer, plan: plan, sink: downstream}
	schema := arrow.NewSchema([]arrow.Field{{Name: "id", Type: arrow.PrimitiveTypes.Int64, Nullable: false}}, nil)
	if err := guarded.WriteSchema(schema); err != nil {
		t.Fatalf("release first Arrow schema: %v", err)
	}
	first := adr0026ArrowRecord(t, schema, 1)
	if err := guarded.WriteRecord(first); err != nil {
		first.Release()
		t.Fatalf("release first Arrow batch: %v", err)
	}
	first.Release()
	if downstream.records != 1 {
		t.Fatalf("first Arrow batch records = %d, want 1", downstream.records)
	}

	fixture.narrowToEMEA(t)
	second := adr0026ArrowRecord(t, schema, 2)
	err = guarded.WriteRecord(second)
	second.Release()
	if err == nil {
		t.Fatal("Arrow output released a batch after committed attribute narrowing")
	}
	if downstream.records != 1 {
		t.Fatalf("post-commit downstream records = %d, want only first bounded batch", downstream.records)
	}
}

var errADR0026AuthorityChanged = errors.New("semantic authority changed during ADR-0026 qualification")

type adr0026PostgresAccessFixture struct {
	repo       *accesspostgres.Repository
	assignment access.SemanticAttributeAssignment
}

func newADR0026PostgresAccessFixture(t *testing.T) *adr0026PostgresAccessFixture {
	t.Helper()
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator"})
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: uuid.NewString(), Login: true})
	h.GrantRole(t, owner, migrator)
	database := h.NewDatabase(t, "")
	h.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	conn, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `SET ROLE leapview_control_migrator`); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		t.Fatal(err)
	}
	if err := accesspostgres.ApplySchema(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		conn.Release()
		t.Fatalf("apply access schema: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()
	runtimeDB, err := pgxpool.New(ctx, database.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	for _, id := range []string{adr0026AccessActor, adr0026AccessPrincipal} {
		if _, err := admin.Exec(ctx, `INSERT INTO access.principal (id, principal_type, status) VALUES ($1::uuid, 'user', 'active')`, id); err != nil {
			t.Fatalf("insert qualification principal %s: %v", id, err)
		}
	}
	repo, err := accesspostgres.NewAccess(runtimeDB, accesspostgres.FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	mutation := access.SemanticAttributeMutationContext{ActorPrincipalID: adr0026AccessActor}
	definition, err := repo.RegisterSemanticAttribute(ctx, access.RegisterSemanticAttributeInput{
		Name: "regions", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeList,
		Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}}, Mutation: mutation,
	})
	if err != nil {
		t.Fatalf("register qualification attribute: %v", err)
	}
	assignment, err := repo.SetSemanticAttributeAssignment(ctx, access.SemanticAttributeAssignmentInput{
		DefinitionID: definition.ID, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: adr0026AccessPrincipal}, Values: []string{"AMER", "EMEA"}, Mutation: mutation,
	})
	if err != nil {
		t.Fatalf("set all-regions qualification attribute: %v", err)
	}
	return &adr0026PostgresAccessFixture{repo: repo, assignment: assignment}
}

func (f *adr0026PostgresAccessFixture) controlState(t *testing.T) access.SemanticAttributeControlSnapshot {
	t.Helper()
	state, err := f.repo.SemanticAttributeControl(t.Context())
	if err != nil {
		t.Fatalf("read semantic control state: %v", err)
	}
	return state
}

func (f *adr0026PostgresAccessFixture) controlGuard(initial access.SemanticAttributeControlSnapshot) func(context.Context) error {
	return func(ctx context.Context) error {
		current, err := f.repo.SemanticAttributeControl(ctx)
		if err != nil {
			return err
		}
		if current.State.Revision != initial.State.Revision || current.State.Digest != initial.State.Digest {
			return errADR0026AuthorityChanged
		}
		return nil
	}
}

func (f *adr0026PostgresAccessFixture) narrowToEMEA(t *testing.T) {
	t.Helper()
	updated, err := f.repo.SetSemanticAttributeAssignment(t.Context(), access.SemanticAttributeAssignmentInput{
		AssignmentID: f.assignment.ID, DefinitionID: f.assignment.DefinitionID, Subject: f.assignment.Subject,
		Values: []string{"EMEA"}, ExpectedVersion: f.assignment.AssignmentVersion,
		Mutation: access.SemanticAttributeMutationContext{ActorPrincipalID: adr0026AccessActor},
	})
	if err != nil {
		t.Fatalf("commit EMEA restriction: %v", err)
	}
	f.assignment = updated
}

func (f *adr0026PostgresAccessFixture) restoreAllRegions(t *testing.T) {
	t.Helper()
	updated, err := f.repo.SetSemanticAttributeAssignment(t.Context(), access.SemanticAttributeAssignmentInput{
		AssignmentID: f.assignment.ID, DefinitionID: f.assignment.DefinitionID, Subject: f.assignment.Subject,
		Values: []string{"AMER", "EMEA"}, ExpectedVersion: f.assignment.AssignmentVersion,
		Mutation: access.SemanticAttributeMutationContext{ActorPrincipalID: adr0026AccessActor},
	})
	if err != nil {
		t.Fatalf("restore all-regions attribute: %v", err)
	}
	f.assignment = updated
}

func adr0026ProtectedRegionModel(t *testing.T) *semanticmodel.Model {
	t.Helper()
	return &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName: "orders", GrainEntity: "order",
				Entities: map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"id"}}},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"id":     {Field: "orders.id", Table: "orders", Name: "id", Type: "number", Datatype: semanticmodel.DataTypeInteger},
					"region": {Field: "orders.region", Table: "orders", Name: "region", Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"id":     {Name: "id", Datatype: semanticmodel.DataTypeInteger, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.id"}}},
			"region": {Name: "region", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.region"}}},
		},
		AccessPolicy: semanticmodel.SemanticAccessPolicy{Datasets: map[string]semanticmodel.SemanticDatasetAccessSpec{
			"orders": {AccessFilters: []semanticmodel.SemanticAccessFilterSpec{{Field: "region", UserAttribute: "regions"}}},
		}},
	}
}

type adr0026ArrowCaptureSink struct{ records int }

func (s *adr0026ArrowCaptureSink) WriteSchema(*arrow.Schema) error { return nil }
func (s *adr0026ArrowCaptureSink) WriteRecord(arrow.RecordBatch) error {
	s.records++
	return nil
}

func adr0026ArrowRecord(t testing.TB, schema *arrow.Schema, value int64) arrow.RecordBatch {
	t.Helper()
	builder := array.NewInt64Builder(memory.DefaultAllocator)
	builder.Append(value)
	column := builder.NewArray()
	builder.Release()
	record := array.NewRecordBatch(schema, []arrow.Array{column}, 1)
	column.Release()
	return record
}

var _ arrowquery.Sink = (*adr0026ArrowCaptureSink)(nil)
