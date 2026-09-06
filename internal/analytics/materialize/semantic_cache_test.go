package materialize

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/flidai/leapview/internal/project/contractprojection"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/arrowresult"
)

type semanticCacheTestLifecycle struct {
	mu      sync.RWMutex
	current resultidentity.SemanticLifecycle
}

func (l *semanticCacheTestLifecycle) ReadCurrent(context.Context) (resultidentity.SemanticLifecycle, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.current, nil
}

func (l *semanticCacheTestLifecycle) Set(current resultidentity.SemanticLifecycle) {
	l.mu.Lock()
	l.current = current
	l.mu.Unlock()
}

func semanticCacheTestBinding() resultidentity.SemanticLifecycle {
	return resultidentity.SemanticLifecycle{
		InstanceID: "instance:test", ProjectID: "project:test", AuthoredID: "sales", ResourceKind: projectgraph.KindSemanticModel,
		Sequence: 7, ActiveBundleID: "bundle:test", PublicationVersion: "1.0.0",
		ProjectionProfile: contractprojection.Profile, PublicationDigest: materializeTestDigest('1'),
		AuthorizationRevision: 13,
	}
}

func newSemanticCacheTestRuntime(t testing.TB, source string) (*Runtime, *semanticCacheTestLifecycle, *countingCacheRuntimeDatabase) {
	t.Helper()
	resolution := semanticConsumerResolution(t, "principal-1")
	resolution.Registry.State.Digest = materializeTestDigest('2')
	resolution.ControlState.Digest = materializeTestDigest('3')
	if source != "" {
		resolution.Attributes[0].Source = source
	}
	authority := &semanticConsumerAuthority{resolutions: []access.SemanticAttributeResolution{resolution}}
	binding := semanticCacheTestBinding()
	lifecycle := &semanticCacheTestLifecycle{current: binding}
	model := semanticConsumerProtectedModel()
	modelDigest, err := semanticquery.SemanticModelDigest(model)
	if err != nil {
		t.Fatalf("SemanticModelDigest: %v", err)
	}
	evidence, err := resultidentity.NewEvidence(resultidentity.EvidenceInput{
		SemanticModelID: "sales", SemanticModelDigest: modelDigest,
		DatasetRelations: []resultidentity.DatasetRelation{{Dataset: "orders", Relation: resultidentity.RelationRevision{
			RelationID: "model:orders", RevisionDigest: materializeTestDigest('4'),
		}}},
		BindingFingerprint: materializeTestDigest('5'), RuntimeDigest: materializeTestDigest('6'),
		CapabilityDigest: materializeTestDigest('7'),
	})
	if err != nil {
		t.Fatalf("NewEvidence: %v", err)
	}
	database := &countingCacheRuntimeDatabase{}
	runtime, err := NewRuntimeView(t.Context(), RuntimeConfig{
		ModelID: "sales", Model: model, Database: database, Sources: ownershipSources{},
		ResultPartition: materializeTestPartition(t, resultidentity.PartitionProduction, ""), DependencyEvidence: evidence,
		SemanticAccessAuthority:      authority,
		SemanticAccessCompileContext: &semanticquery.SemanticAccessCompileContext{Registry: resolution.Registry},
		ServingStateID:               "serving:test",
		SemanticCache:                &SemanticCacheConfig{Binding: binding, ReadCurrent: lifecycle.ReadCurrent},
	})
	if err != nil {
		t.Fatalf("NewRuntimeView: %v", err)
	}
	t.Cleanup(func() { _ = runtime.CloseView() })
	return runtime, lifecycle, database
}

func semanticCacheTestRequest() dataquery.Query {
	request := semanticConsumerRequest()
	request.EffectivePolicyFingerprint = materializeTestDigest('8')
	return request
}

func TestProtectedSemanticCacheReusesOnlyUnchangedLifecycle(t *testing.T) {
	runtime, lifecycle, database := newSemanticCacheTestRuntime(t, "")
	request := semanticCacheTestRequest()
	first, err := runtime.ExecuteDataQuery(t.Context(), request)
	if err != nil {
		t.Fatalf("first protected query: %v", err)
	}
	if first.CacheOutcome != dataquery.CacheMiss {
		t.Fatalf("first cache outcome = %q, want miss", first.CacheOutcome)
	}
	second, err := runtime.ExecuteDataQuery(t.Context(), request)
	if err != nil {
		t.Fatalf("unchanged protected cache hit: %v", err)
	}
	if second.CacheOutcome != dataquery.CacheHit {
		t.Fatalf("second cache outcome = %q, want hit", second.CacheOutcome)
	}
	if got := database.queries.Load(); got != 1 {
		t.Fatalf("database executions = %d, want 1", got)
	}

	stale := semanticCacheTestBinding()
	stale.AuthorizationRevision++
	lifecycle.Set(stale)
	if _, err := runtime.ExecuteDataQuery(t.Context(), request); err == nil {
		t.Fatal("stale authorization lifecycle unexpectedly reused cached result")
	}
	if got := database.queries.Load(); got != 1 {
		t.Fatalf("stale lifecycle database executions = %d, want 1", got)
	}
}

func TestProtectedSemanticCacheBypassesUnsupportedAttributeSource(t *testing.T) {
	runtime, _, database := newSemanticCacheTestRuntime(t, "trusted_claim")
	request := semanticCacheTestRequest()
	for range 2 {
		if _, err := runtime.ExecuteDataQuery(t.Context(), request); err != nil {
			t.Fatalf("unsupported-source protected query: %v", err)
		}
	}
	if got := database.queries.Load(); got != 2 {
		t.Fatalf("unsupported-source database executions = %d, want 2", got)
	}
	if got := runtime.queryCache.scope.Stats().Entries; got != 0 {
		t.Fatalf("unsupported-source cache entries = %d, want 0", got)
	}
}

func TestProtectedSemanticCacheRejectsStaleAuthoritySnapshots(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*access.SemanticAttributeResolution) error
	}{
		{name: "registry revision", change: func(resolution *access.SemanticAttributeResolution) error {
			resolution.Registry.State.Revision++
			resolution.Registry.State.Digest = materializeTestDigest('4')
			return nil
		}},
		{name: "control revision", change: func(resolution *access.SemanticAttributeResolution) error {
			resolution.ControlState.Revision++
			resolution.ControlState.Digest = materializeTestDigest('5')
			return nil
		}},
		{name: "attribute value", change: func(resolution *access.SemanticAttributeResolution) error {
			values, digest, err := access.CanonicalSemanticAttributeValues(resolution.Registry.Definitions[0], "eu")
			if err != nil {
				return err
			}
			resolution.Attributes[0].CanonicalValues = values
			resolution.Attributes[0].ValueDigest = digest
			return nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime, _, database := newSemanticCacheTestRuntime(t, "")
			request := semanticCacheTestRequest()
			if _, err := runtime.ExecuteDataQuery(t.Context(), request); err != nil {
				t.Fatalf("populate protected cache: %v", err)
			}
			authority, ok := runtime.semanticAccessAuthority.(*semanticConsumerAuthority)
			if !ok {
				t.Fatal("test runtime authority has unexpected type")
			}
			authority.mu.Lock()
			changed := authority.resolutions[0]
			authority.mu.Unlock()
			if err := test.change(&changed); err != nil {
				t.Fatalf("change authority snapshot: %v", err)
			}
			authority.mu.Lock()
			authority.resolutions = []access.SemanticAttributeResolution{authority.resolutions[0], changed}
			authority.calls = 0
			authority.mu.Unlock()
			if _, err := runtime.ExecuteDataQuery(t.Context(), request); err == nil {
				t.Fatal("stale semantic authority unexpectedly reused cached result")
			}
			if got := database.queries.Load(); got != 1 {
				t.Fatalf("stale authority database executions = %d, want 1", got)
			}
		})
	}
}

type semanticCacheBlockingLifecycle struct {
	mu          sync.RWMutex
	current     resultidentity.SemanticLifecycle
	blockNext   atomic.Bool
	entered     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func (l *semanticCacheBlockingLifecycle) ReadCurrent(ctx context.Context) (resultidentity.SemanticLifecycle, error) {
	if l.blockNext.CompareAndSwap(true, false) {
		close(l.entered)
		select {
		case <-l.release:
		case <-ctx.Done():
			return resultidentity.SemanticLifecycle{}, ctx.Err()
		}
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.current, nil
}

func (l *semanticCacheBlockingLifecycle) Set(current resultidentity.SemanticLifecycle) {
	l.mu.Lock()
	l.current = current
	l.mu.Unlock()
}

func (l *semanticCacheBlockingLifecycle) Release() {
	l.releaseOnce.Do(func() { close(l.release) })
}

type semanticCacheBlockingDatabase struct {
	cacheRuntimeDatabase
	started     chan struct{}
	release     chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
	queries     atomic.Int32
}

func (d *semanticCacheBlockingDatabase) Query(ctx context.Context, _ semanticquery.Plan) (semanticquery.Rows, error) {
	d.queries.Add(1)
	d.startOnce.Do(func() { close(d.started) })
	select {
	case <-d.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return semanticquery.Rows{{"id": int64(1)}}, nil
}

func (d *semanticCacheBlockingDatabase) Release() {
	d.releaseOnce.Do(func() { close(d.release) })
}

func (d *semanticCacheBlockingDatabase) QueryArrow(ctx context.Context, plan semanticquery.Plan, sink arrowquery.Sink) error {
	rows, err := d.Query(ctx, plan)
	if err != nil {
		return err
	}
	return writeTestRowsArrow(ctx, plan, rows, sink)
}

func TestProtectedSemanticCacheRejectsControlUpdateRacingCachedRead(t *testing.T) {
	runtime, _, database := newSemanticCacheTestRuntime(t, "")
	request := semanticCacheTestRequest()
	if _, err := runtime.ExecuteDataQuery(t.Context(), request); err != nil {
		t.Fatalf("populate protected cache: %v", err)
	}
	lifecycle := &semanticCacheBlockingLifecycle{
		current: semanticCacheTestBinding(), entered: make(chan struct{}), release: make(chan struct{}),
	}
	t.Cleanup(lifecycle.Release)
	runtime.semanticCache.ReadCurrent = lifecycle.ReadCurrent
	lifecycle.blockNext.Store(true)
	type runtimeResult struct {
		result dataquery.Result
		err    error
	}
	done := make(chan runtimeResult, 1)
	go func() {
		result, err := runtime.ExecuteDataQuery(context.Background(), request)
		done <- runtimeResult{result: result, err: err}
	}()
	select {
	case <-lifecycle.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("cached read did not reach live lifecycle reader")
	}
	stale := semanticCacheTestBinding()
	// The lifecycle revision is the monotonic role/grant control boundary.
	stale.AuthorizationRevision++
	lifecycle.Set(stale)
	lifecycle.Release()
	outcome := <-done
	if outcome.err == nil {
		t.Fatal("control update racing cached read unexpectedly returned a result")
	}
	if len(outcome.result.Rows) != 0 {
		t.Fatal("control update racing cached read returned old rows")
	}
	if got := database.queries.Load(); got != 1 {
		t.Fatalf("racing cached read database executions = %d, want 1", got)
	}
}

func TestProtectedSemanticCacheRejectsLifecycleTransitionDuringMiss(t *testing.T) {
	runtime, lifecycle, _ := newSemanticCacheTestRuntime(t, "")
	database := &semanticCacheBlockingDatabase{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(database.Release)
	runtime.db = database
	type runtimeResult struct {
		result dataquery.Result
		err    error
	}
	done := make(chan runtimeResult, 1)
	go func() {
		result, err := runtime.ExecuteDataQuery(context.Background(), semanticCacheTestRequest())
		done <- runtimeResult{result: result, err: err}
	}()
	select {
	case <-database.started:
	case <-time.After(2 * time.Second):
		t.Fatal("protected miss did not reach physical execution")
	}
	stale := semanticCacheTestBinding()
	stale.AuthorizationRevision++
	lifecycle.Set(stale)
	database.Release()
	outcome := <-done
	if outcome.err == nil {
		t.Fatal("lifecycle transition during miss unexpectedly returned a result")
	}
	if len(outcome.result.Rows) != 0 {
		t.Fatal("lifecycle transition during miss returned old rows")
	}
	if got := database.queries.Load(); got != 1 {
		t.Fatalf("transition miss database executions = %d, want 1", got)
	}
	if got := runtime.queryCache.scope.Stats().Entries; got != 0 {
		t.Fatalf("transition miss cache entries = %d, want 0", got)
	}
}

func TestProtectedSemanticCacheUsesNewBindingAfterRestoreOrRollback(t *testing.T) {
	for _, test := range []struct {
		name string
	}{
		{name: "restore"},
		{name: "rollback"},
	} {
		t.Run(test.name, func(t *testing.T) {
			oldRuntime, lifecycle, database := newSemanticCacheTestRuntime(t, "")
			request := semanticCacheTestRequest()
			if _, err := oldRuntime.ExecuteDataQuery(t.Context(), request); err != nil {
				t.Fatalf("populate protected cache: %v", err)
			}
			binding := oldRuntime.semanticCache.Binding
			binding.Sequence++
			lifecycle.Set(binding)
			oldResult, oldErr := oldRuntime.ExecuteDataQuery(t.Context(), request)
			if oldErr == nil {
				t.Fatal("old activation unexpectedly served after lifecycle sequence changed")
			}
			if len(oldResult.Rows) != 0 {
				t.Fatal("old activation returned rows after lifecycle sequence changed")
			}

			authority, ok := oldRuntime.semanticAccessAuthority.(*semanticConsumerAuthority)
			if !ok {
				t.Fatal("test runtime authority has unexpected type")
			}
			authority.mu.Lock()
			resolution := authority.resolutions[0]
			authority.mu.Unlock()
			newRuntime, err := NewRuntimeView(t.Context(), RuntimeConfig{
				ModelID: oldRuntime.modelID, Model: oldRuntime.model, Database: oldRuntime.db, Sources: oldRuntime.sources,
				ResultPartition: oldRuntime.resultPartition, QueryResultCache: oldRuntime.queryCache.scope,
				ImmutableByteCache: oldRuntime.queryCache.byteScope, DependencyEvidence: oldRuntime.dependencyEvidence,
				SemanticAccessAuthority:      authority,
				SemanticAccessCompileContext: &semanticquery.SemanticAccessCompileContext{Registry: resolution.Registry},
				ServingStateID:               oldRuntime.servingStateID,
				SemanticCache:                &SemanticCacheConfig{Binding: binding, ReadCurrent: lifecycle.ReadCurrent},
			})
			if err != nil {
				t.Fatalf("new activation runtime: %v", err)
			}
			t.Cleanup(func() { _ = newRuntime.CloseView() })
			result, err := newRuntime.ExecuteDataQuery(t.Context(), request)
			if err != nil {
				t.Fatalf("new activation miss: %v", err)
			}
			if result.CacheOutcome != dataquery.CacheMiss {
				t.Fatalf("new activation cache outcome = %q, want miss", result.CacheOutcome)
			}
			result, err = newRuntime.ExecuteDataQuery(t.Context(), request)
			if err != nil {
				t.Fatalf("new activation hit: %v", err)
			}
			if result.CacheOutcome != dataquery.CacheHit {
				t.Fatalf("new activation second cache outcome = %q, want hit", result.CacheOutcome)
			}
			if got := database.queries.Load(); got != 2 {
				t.Fatalf("new activation database executions = %d, want 2", got)
			}
		})
	}
}

func TestProtectedSemanticCacheClaimRevocationBypassesThenDenies(t *testing.T) {
	runtime, _, database := newSemanticCacheTestRuntime(t, "trusted_claim")
	request := semanticCacheTestRequest()
	if _, err := runtime.ExecuteDataQuery(t.Context(), request); err != nil {
		t.Fatalf("unsupported-source query: %v", err)
	}
	authority, ok := runtime.semanticAccessAuthority.(*semanticConsumerAuthority)
	if !ok {
		t.Fatal("test runtime authority has unexpected type")
	}
	authority.mu.Lock()
	authority.resolveErr = errors.New("revoked")
	authority.mu.Unlock()
	if _, err := runtime.ExecuteDataQuery(t.Context(), request); err == nil {
		t.Fatal("revoked claim unexpectedly returned a result")
	}
	if got := database.queries.Load(); got != 1 {
		t.Fatalf("revoked claim database executions = %d, want 1", got)
	}
}

func TestProtectedSemanticCacheRejectsLifecycleTransitions(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*resultidentity.SemanticLifecycle)
	}{
		{name: "tombstone", change: func(lifecycle *resultidentity.SemanticLifecycle) {
			lifecycle.Sequence++
			lifecycle.ActiveBundleID = "bundle:tombstone"
		}},
		{name: "restore", change: func(lifecycle *resultidentity.SemanticLifecycle) {
			lifecycle.Sequence++
			lifecycle.ActiveBundleID = "bundle:restore"
		}},
		{name: "rollback", change: func(lifecycle *resultidentity.SemanticLifecycle) {
			lifecycle.Sequence++
			lifecycle.ActiveBundleID = "bundle:old"
		}},
		{name: "new activation", change: func(lifecycle *resultidentity.SemanticLifecycle) {
			lifecycle.Sequence++
			lifecycle.ActiveBundleID = "bundle:new"
		}},
		{name: "cross instance", change: func(lifecycle *resultidentity.SemanticLifecycle) {
			lifecycle.InstanceID = "instance:other"
		}},
		{name: "role or grant revision", change: func(lifecycle *resultidentity.SemanticLifecycle) {
			lifecycle.AuthorizationRevision++
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime, lifecycle, database := newSemanticCacheTestRuntime(t, "")
			request := semanticCacheTestRequest()
			if _, err := runtime.ExecuteDataQuery(t.Context(), request); err != nil {
				t.Fatalf("populate protected cache: %v", err)
			}
			current := semanticCacheTestBinding()
			test.change(&current)
			lifecycle.Set(current)
			if _, err := runtime.ExecuteDataQuery(t.Context(), request); err == nil {
				t.Fatal("lifecycle transition unexpectedly reused cached result")
			}
			if got := database.queries.Load(); got != 1 {
				t.Fatalf("transition database executions = %d, want 1", got)
			}
		})
	}
}

func TestProtectedSemanticCacheRejectsMismatchedActivationModelEvidence(t *testing.T) {
	runtime, _, database := newSemanticCacheTestRuntime(t, "")
	evidence, err := resultidentity.NewEvidence(resultidentity.EvidenceInput{
		SemanticModelID: "sales", SemanticModelDigest: materializeTestDigest('9'),
		DatasetRelations: []resultidentity.DatasetRelation{{Dataset: "orders", Relation: resultidentity.RelationRevision{
			RelationID: "model:orders", RevisionDigest: materializeTestDigest('4'),
		}}},
		BindingFingerprint: materializeTestDigest('5'), RuntimeDigest: materializeTestDigest('6'),
		CapabilityDigest: materializeTestDigest('7'),
	})
	if err != nil {
		t.Fatalf("NewEvidence: %v", err)
	}
	runtime.dependencyEvidence = evidence
	if _, err := runtime.ExecuteDataQuery(t.Context(), semanticCacheTestRequest()); err == nil {
		t.Fatal("mismatched activation model evidence unexpectedly enabled protected cache execution")
	}
	if got := database.queries.Load(); got != 0 {
		t.Fatalf("mismatched evidence database executions = %d, want 0", got)
	}
}

func TestProtectedSemanticCacheRejectsCrossProjectBinding(t *testing.T) {
	runtime, _, database := newSemanticCacheTestRuntime(t, "")
	runtime.semanticCache.Binding.ProjectID = "project:other"
	if _, err := runtime.ExecuteDataQuery(t.Context(), semanticCacheTestRequest()); err == nil {
		t.Fatal("cross-project semantic cache binding unexpectedly reused a result")
	}
	if got := database.queries.Load(); got != 0 {
		t.Fatalf("cross-project binding database executions = %d, want 0", got)
	}
}

func TestProtectedSemanticCacheRejectsMalformedBinding(t *testing.T) {
	resolution := semanticConsumerResolution(t, "principal-1")
	_, err := NewRuntimeView(t.Context(), RuntimeConfig{
		ModelID: "sales", Model: semanticConsumerProtectedModel(), Database: cacheRuntimeDatabase{}, Sources: ownershipSources{},
		SemanticAccessAuthority:      &semanticConsumerAuthority{resolutions: []access.SemanticAttributeResolution{resolution}},
		SemanticAccessCompileContext: &semanticquery.SemanticAccessCompileContext{Registry: resolution.Registry},
		ServingStateID:               "serving:test", SemanticCache: &SemanticCacheConfig{Binding: resultidentity.SemanticLifecycle{}}})
	if err == nil {
		t.Fatal("malformed semantic cache binding unexpectedly accepted")
	}
}

func TestQueryCacheGuardDeletesEntryWhenLifecycleChangesAfterStore(t *testing.T) {
	cache := newQueryResultCache(4)
	t.Cleanup(func() { _ = cache.close() })
	request := semanticCacheTestRequest()
	partition := materializeTestPartition(t, resultidentity.PartitionProduction, "")
	dependency := materializeTestDependency(t, 'a')
	var checks atomic.Int32
	execute := func(ctx context.Context) (arrowQueryExecution, error) {
		builder := arrowresult.NewBuilder()
		if err := writeTestRowsArrow(ctx, semanticquery.Plan{Columns: []string{"id"}}, semanticquery.Rows{{"id": int64(1)}}, builder); err != nil {
			return arrowQueryExecution{}, err
		}
		data, err := builder.Finish()
		if err != nil {
			return arrowQueryExecution{}, err
		}
		return arrowQueryExecution{data: data}, nil
	}
	guard := func(context.Context) error {
		if checks.Add(1) == 4 {
			return errSemanticCacheMismatch
		}
		return nil
	}
	if _, err := cache.executeArrow(t.Context(), request, partition, dependency, "SELECT 1", time.Time{}, execute, guard); err == nil {
		t.Fatal("post-store lifecycle mismatch unexpectedly returned a result")
	}
	address, err := cache.cacheAddress(request, partition, dependency)
	if err != nil {
		t.Fatal(err)
	}
	entry, _, ok, err := cache.scope.LookupArrow(address.key)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		entry.Release()
		t.Fatal("post-store lifecycle mismatch retained cache entry")
	}
}

func semanticCacheArrowExecution(ctx context.Context, value int64) (arrowQueryExecution, error) {
	builder := arrowresult.NewBuilder()
	if err := writeTestRowsArrow(ctx, semanticquery.Plan{Columns: []string{"id"}}, semanticquery.Rows{{"id": value}}, builder); err != nil {
		return arrowQueryExecution{}, err
	}
	data, err := builder.Finish()
	if err != nil {
		return arrowQueryExecution{}, err
	}
	return arrowQueryExecution{data: data}, nil
}

func TestQueryCacheInvalidationRacingMissDoesNotPublishOldResult(t *testing.T) {
	cache := newQueryResultCache(4)
	t.Cleanup(func() { _ = cache.close() })
	request := semanticCacheTestRequest()
	partition := materializeTestPartition(t, resultidentity.PartitionProduction, "")
	dependency := materializeTestDependency(t, 'a')
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var executions atomic.Int32
	var admitted atomic.Bool
	admitted.Store(true)
	execute := func(ctx context.Context) (arrowQueryExecution, error) {
		value := executions.Add(1)
		if value == 1 {
			close(started)
			<-release
		}
		return semanticCacheArrowExecution(ctx, int64(value))
	}
	guard := func(context.Context) error {
		if !admitted.Load() {
			return errSemanticCacheMismatch
		}
		return nil
	}
	type cacheResult struct {
		result dataquery.Result
		err    error
	}
	done := make(chan cacheResult, 1)
	go func() {
		result, err := cache.executeArrow(context.Background(), request, partition, dependency, "SELECT 1", time.Time{}, execute, guard)
		done <- cacheResult{result: result, err: err}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("cache miss did not reach execution")
	}
	cache.scope.Invalidate()
	admitted.Store(false)
	releaseOnce.Do(func() { close(release) })
	first := <-done
	if first.err == nil {
		t.Fatal("invalidated in-flight result unexpectedly succeeded")
	}
	if len(first.result.Rows) != 0 {
		t.Fatal("invalidated in-flight result returned old rows")
	}
	admitted.Store(true)
	second, err := cache.executeArrow(context.Background(), request, partition, dependency, "SELECT 1", time.Time{}, execute, guard)
	if err != nil {
		t.Fatalf("post-invalidation query: %v", err)
	}
	if second.CacheOutcome != dataquery.CacheMiss {
		t.Fatalf("post-invalidation cache outcome = %q, want miss", second.CacheOutcome)
	}
	if len(second.Rows) != 1 || second.Rows[0]["id"] != int64(2) {
		t.Fatalf("post-invalidation rows = %#v, want new execution", second.Rows)
	}
	if got := executions.Load(); got != 2 {
		t.Fatalf("post-invalidation executions = %d, want 2", got)
	}
}
