package materialize

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/semanticvalue"
	"github.com/flidai/leapview/pkg/arrowresult"
)

func TestProtectedDashboardCacheReusesSameConsumerIdentity(t *testing.T) {
	runtime, governor, _ := protectedConsumerFixtureWithModelModifier(t, true, func(model *semanticmodel.Model) {
		// Authored SQL has no portable relation revision in this cache slice. A
		// materialized relation is the eligible protected path under test.
		model.Tables["orders"] = clearExecutionSQL(model.Tables["orders"])
	})
	modelDigest, err := semanticquery.SemanticModelDigest(runtime.model)
	if err != nil {
		t.Fatal(err)
	}
	runtime.dependencyEvidence, err = resultidentity.NewEvidence(resultidentity.EvidenceInput{
		SemanticModelID: modelResourceID(t, runtime.modelID), SemanticModelDigest: modelDigest,
		DatasetRelations: []resultidentity.DatasetRelation{{Dataset: "orders", Relation: resultidentity.RelationRevision{
			RelationID: "model:orders", RevisionDigest: materializeTestDigest('b'),
		}}},
		BindingFingerprint: materializeTestDigest('c'), RuntimeDigest: materializeTestDigest('d'), CapabilityDigest: materializeTestDigest('e'),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := dataquery.Query{
		Surface: dataquery.SurfaceDashboard, Operation: dataquery.OperationDashboardRows,
		ModelID: "sales", PrincipalID: "alice", Kind: dataquery.KindSemanticRows, Target: "orders",
		Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 1,
		EffectivePolicyFingerprint: materializeTestDigest('f'),
	}
	ctx := dataquery.WithGovernor(context.Background(), governor)
	first, err := runtime.ExecuteDataQuery(ctx, request)
	if err != nil {
		t.Fatalf("first protected query: %v", err)
	}
	second, err := runtime.ExecuteDataQuery(ctx, request)
	if err != nil {
		t.Fatalf("second protected query: %v", err)
	}
	if first.CacheOutcome != dataquery.CacheMiss || second.CacheOutcome != dataquery.CacheHit {
		t.Fatalf("cache outcomes = (%q, %q), want miss then hit", first.CacheOutcome, second.CacheOutcome)
	}
	if got := runtime.db.(*semanticConsumerArrowDatabase).queries.Load(); got != 1 {
		t.Fatalf("physical executions = %d, want 1", got)
	}
}

func clearExecutionSQL(table semanticmodel.Table) semanticmodel.Table {
	table.Execution.SQL = ""
	return table
}

func modelResourceID(t *testing.T, value string) projectgraph.ResourceID {
	t.Helper()
	id, err := projectgraph.NewResourceID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestProtectedCacheIdentityMatchesPartitionAndRequest(t *testing.T) {
	partition := materializeTestPartition(t, resultidentity.PartitionProduction, "")
	identity := &resultidentity.SemanticAccessIdentity{
		ProjectID: "project:test", Environment: "test", InstanceID: "target_test", ModelID: "sales", Generation: "generation-1", PrincipalID: "alice",
		Profile: semanticvalue.Profile, RegistryProfile: semanticvalue.Profile, RegistryRevision: 1, RegistryDigest: materializeTestDigest('1'),
		ControlProfile: semanticvalue.Profile, ControlRevision: 1, ControlDigest: materializeTestDigest('2'),
		EffectiveAttributeDigest: materializeTestDigest('3'), PolicyDigest: materializeTestDigest('4'), DecisionDigest: materializeTestDigest('5'),
		PublicationPolicy: semanticCacheTestPublicationPolicy("target_test", "sales"),
	}
	dependency, err := resultidentity.NewDependency(resultidentity.DependencyInput{
		SemanticAccess: identity, SemanticModelID: "sales", SemanticModelDigest: materializeTestDigest('6'),
		Relations:          []resultidentity.RelationRevision{{RelationID: "model:orders", RevisionDigest: materializeTestDigest('7')}},
		BindingFingerprint: materializeTestDigest('8'), Execution: resultidentity.ExecutionIdentity{
			PlannerDigest: materializeTestDigest('9'), RuntimeDigest: materializeTestDigest('a'), CapabilityDigest: materializeTestDigest('b'), SettingsDigest: materializeTestDigest('c'),
		}, ResultFormat: resultidentity.ResultFormat{Name: "arrow-result", Version: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := dataquery.Query{ProjectID: "project:test", ModelID: "sales", PrincipalID: "alice", EffectivePolicyFingerprint: materializeTestDigest('d')}
	if got := queryCacheIdentityReason(request, partition, dependency); got != dataquery.CacheAdmissionReasonEligible {
		t.Fatalf("matching protected identity admission = %q, want eligible", got)
	}

	for _, test := range []struct {
		name   string
		mutate func(*resultidentity.SemanticAccessIdentity)
		want   dataquery.CacheAdmissionReason
	}{
		{name: "partition project", mutate: func(value *resultidentity.SemanticAccessIdentity) { value.ProjectID = "project:other" }, want: dataquery.CacheAdmissionReasonPartitionInvalid},
		{name: "partition environment", mutate: func(value *resultidentity.SemanticAccessIdentity) { value.Environment = "prod" }, want: dataquery.CacheAdmissionReasonPartitionInvalid},
		{name: "instance", mutate: func(value *resultidentity.SemanticAccessIdentity) {
			value.InstanceID = "instance-2"
			value.PublicationPolicy.Candidate.InstanceID = "instance-2"
		}, want: dataquery.CacheAdmissionReasonPartitionInvalid},
		{name: "principal", mutate: func(value *resultidentity.SemanticAccessIdentity) { value.PrincipalID = "bob" }, want: dataquery.CacheAdmissionReasonDependencyInvalid},
		{name: "model", mutate: func(value *resultidentity.SemanticAccessIdentity) {
			value.ModelID = "other"
			value.PublicationPolicy.Candidate.AuthoredID = "other"
		}, want: dataquery.CacheAdmissionReasonDependencyInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := *identity
			test.mutate(&changed)
			semanticModelID := projectgraph.ResourceID("sales")
			if changed.ModelID != identity.ModelID {
				semanticModelID = projectgraph.ResourceID(changed.ModelID)
			}
			candidate, candidateErr := resultidentity.NewDependency(resultidentity.DependencyInput{
				SemanticAccess: &changed, SemanticModelID: semanticModelID, SemanticModelDigest: materializeTestDigest('6'),
				Relations:          []resultidentity.RelationRevision{{RelationID: "model:orders", RevisionDigest: materializeTestDigest('7')}},
				BindingFingerprint: materializeTestDigest('8'), Execution: resultidentity.ExecutionIdentity{
					PlannerDigest: materializeTestDigest('9'), RuntimeDigest: materializeTestDigest('a'), CapabilityDigest: materializeTestDigest('b'), SettingsDigest: materializeTestDigest('c'),
				}, ResultFormat: resultidentity.ResultFormat{Name: "arrow-result", Version: 1},
			})
			if candidateErr != nil {
				t.Fatal(candidateErr)
			}
			if got := queryCacheIdentityReason(request, partition, candidate); got != test.want {
				t.Fatalf("admission = %q, want %q", got, test.want)
			}
		})
	}
}

func TestProtectedCacheLookupRejectsUnguardedDependency(t *testing.T) {
	cache := newQueryResultCache(4)
	partition := materializeTestPartition(t, resultidentity.PartitionProduction, "")
	dependency := protectedCacheTestDependency(t, semanticCacheTestIdentity())
	request := semanticCacheTestRequest()
	if _, _, _, _, err := cache.lookupArrow(context.Background(), request, partition, dependency, ""); err == nil {
		t.Fatal("protected low-level cache lookup without a guard succeeded")
	}
	if _, err := cache.executeArrow(context.Background(), request, partition, dependency, "", nowForCacheTest(), func(context.Context) (arrowQueryExecution, error) {
		t.Fatal("unguarded protected execution invoked its physical callback")
		return arrowQueryExecution{}, nil
	}); err == nil {
		t.Fatal("protected execution without a guard succeeded")
	}
	guard := func(context.Context) error { return nil }
	if _, err := cache.executeArrow(context.Background(), request, partition, dependency, "", nowForCacheTest(), func(context.Context) (arrowQueryExecution, error) {
		t.Fatal("multi-guard protected execution invoked its physical callback")
		return arrowQueryExecution{}, nil
	}, guard, guard); err == nil {
		t.Fatal("protected execution accepted multiple guards")
	}
}

func TestProtectedCacheGuardEvictsEntryWhenRevisionChangesAfterStore(t *testing.T) {
	cache := newQueryResultCache(4)
	partition := materializeTestPartition(t, resultidentity.PartitionProduction, "")
	dependency := protectedCacheTestDependency(t, semanticCacheTestIdentity())
	request := semanticCacheTestRequest()
	var executions atomic.Int32
	var guardCalls atomic.Int32
	stale := errors.New("semantic authority revision changed")
	guard := func(context.Context) error {
		call := guardCalls.Add(1)
		// The first three checks cover pre-lookup, flight start, and the
		// post-execution/pre-store boundary. The fourth check is post-store.
		if call == 3 {
			return nil
		}
		if call >= 4 {
			return stale
		}
		return nil
	}
	result, err := cache.executeArrow(context.Background(), request, partition, dependency, "SELECT 1", nowForCacheTest(), func(context.Context) (arrowQueryExecution, error) {
		executions.Add(1)
		return newProtectedCacheArrowExecution()
	}, guard)
	if err == nil {
		t.Fatal("stale protected result was delivered after store")
	}
	if result.Rows != nil {
		t.Fatalf("stale result rows = %d, want no delivery", len(result.Rows))
	}
	if executions.Load() != 1 {
		t.Fatalf("physical executions = %d, want 1", executions.Load())
	}

	// The stale post-store guard must delete the address. Restore the authority
	// and prove the next request executes rather than hitting that old entry.
	result, err = cache.executeArrow(context.Background(), request, partition, dependency, "SELECT 1", nowForCacheTest(), func(context.Context) (arrowQueryExecution, error) {
		executions.Add(1)
		return newProtectedCacheArrowExecution()
	}, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("fresh protected request: %v", err)
	}
	if result.CacheOutcome != dataquery.CacheMiss {
		t.Fatalf("fresh protected cache outcome = %q, want miss", result.CacheOutcome)
	}
	if executions.Load() != 2 {
		t.Fatalf("physical executions after stale eviction = %d, want 2", executions.Load())
	}
}

func TestProtectedCacheCoalescedWaiterRevalidatesBeforeDelivery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := newQueryResultCache(4)
		partition := materializeTestPartition(t, resultidentity.PartitionProduction, "")
		dependency := protectedCacheTestDependency(t, semanticCacheTestIdentity())
		request := semanticCacheTestRequest()
		var executions atomic.Int32
		stale := atomic.Bool{}
		staleErr := errors.New("semantic authority revision changed")
		executionStarted := make(chan struct{})
		releaseExecution := make(chan struct{})
		ownerStored := make(chan struct{})
		releaseOwnerStoreCheck := make(chan struct{})

		ownerGuardCalls := atomic.Int32{}
		ownerGuard := func(context.Context) error {
			call := ownerGuardCalls.Add(1)
			switch call {
			case 4:
				// Hold after StoreArrowObserved. This lets the waiter remain
				// attached to the same flight while the authority changes.
				close(ownerStored)
				<-releaseOwnerStoreCheck
				return nil
			case 5:
				// The low-level owner has no transform/delivery callback. The
				// waiter-side boundary below is the stale delivery assertion;
				// runtime callers add their final caller validation afterward.
				return nil
			default:
				if stale.Load() {
					return staleErr
				}
				return nil
			}
		}
		ownerResult := make(chan dataquery.Result, 1)
		ownerError := make(chan error, 1)
		go func() {
			result, err := cache.executeArrow(context.Background(), request, partition, dependency, "SELECT 1", nowForCacheTest(), func(context.Context) (arrowQueryExecution, error) {
				executions.Add(1)
				close(executionStarted)
				<-releaseExecution
				return newProtectedCacheArrowExecution()
			}, ownerGuard)
			ownerResult <- result
			ownerError <- err
		}()
		<-executionStarted

		waiterEntered := make(chan struct{})
		allowWaiter := make(chan struct{})
		waiterGuardCalls := atomic.Int32{}
		waiterGuard := func(context.Context) error {
			call := waiterGuardCalls.Add(1)
			if call == 1 {
				close(waiterEntered)
				<-allowWaiter
			}
			if stale.Load() {
				return staleErr
			}
			return nil
		}
		waiterResult := make(chan dataquery.Result, 1)
		waiterError := make(chan error, 1)
		go func() {
			result, err := cache.executeArrow(context.Background(), request, partition, dependency, "SELECT 1", nowForCacheTest(), func(context.Context) (arrowQueryExecution, error) {
				executions.Add(1)
				return newProtectedCacheArrowExecution()
			}, waiterGuard)
			waiterResult <- result
			waiterError <- err
		}()
		<-waiterEntered
		close(allowWaiter)
		// Owner is still blocked in physical execution; once the waiter has
		// joined, both goroutines are quiescent on explicit channels.
		synctest.Wait()
		close(releaseExecution)
		<-ownerStored
		stale.Store(true)
		close(releaseOwnerStoreCheck)
		if err := <-ownerError; err != nil {
			t.Fatalf("owner result unexpectedly failed: %v", err)
		}
		if err := <-waiterError; err == nil {
			t.Fatal("coalesced waiter delivered a result after authority change")
		}
		<-ownerResult
		<-waiterResult
		if executions.Load() != 1 {
			t.Fatalf("coalesced physical executions = %d, want 1", executions.Load())
		}

		// The waiter-side rejection must evict the stored value. A fresh valid
		// guard therefore executes once more instead of reusing stale data.
		stale.Store(false)
		result, err := cache.executeArrow(context.Background(), request, partition, dependency, "SELECT 1", nowForCacheTest(), func(context.Context) (arrowQueryExecution, error) {
			executions.Add(1)
			return newProtectedCacheArrowExecution()
		}, func(context.Context) error { return nil })
		if err != nil {
			t.Fatalf("fresh request after waiter invalidation: %v", err)
		}
		if result.CacheOutcome != dataquery.CacheMiss {
			t.Fatalf("fresh request cache outcome = %q, want miss", result.CacheOutcome)
		}
		if executions.Load() != 2 {
			t.Fatalf("physical executions after waiter invalidation = %d, want 2", executions.Load())
		}
	})
}

func semanticCacheTestIdentity() *resultidentity.SemanticAccessIdentity {
	return &resultidentity.SemanticAccessIdentity{
		ProjectID: "project:test", Environment: "test", InstanceID: "target_test", ModelID: "sales", Generation: "generation-1", PrincipalID: "alice",
		Profile: semanticvalue.Profile, RegistryProfile: semanticvalue.Profile, RegistryRevision: 1, RegistryDigest: materializeTestDigest('1'),
		ControlProfile: semanticvalue.Profile, ControlRevision: 1, ControlDigest: materializeTestDigest('2'),
		EffectiveAttributeDigest: materializeTestDigest('3'), PolicyDigest: materializeTestDigest('4'), DecisionDigest: materializeTestDigest('5'),
		PublicationPolicy: semanticCacheTestPublicationPolicy("target_test", "sales"),
	}
}

func semanticCacheTestPublicationPolicy(instanceID, modelID string) resultidentity.PublicationPolicyIdentity {
	return resultidentity.PublicationPolicyIdentity{
		Candidate: resultidentity.PublicationIdentity{InstanceID: instanceID, AuthoredID: modelID, ResourceKind: "semantic_model", Version: "1.0.0", VersionBaseline: "1.0.0", ProjectionProfile: "leapview.contract/v1", Digest: materializeTestDigest('0')},
		Policy:    resultidentity.PolicyIdentity{BaselineKind: "genesis", Class: "compatible", Compatibility: "additive", StructuralCompatibility: "additive", SemanticCompatibility: "additive", SecurityImpact: "none", PolicyEvidenceDigest: materializeTestDigest('f')},
	}
}

func protectedCacheTestDependency(t *testing.T, identity *resultidentity.SemanticAccessIdentity) resultidentity.Dependency {
	t.Helper()
	dependency, err := resultidentity.NewDependency(resultidentity.DependencyInput{
		SemanticAccess: identity, SemanticModelID: "sales", SemanticModelDigest: materializeTestDigest('6'),
		Relations:          []resultidentity.RelationRevision{{RelationID: "model:orders", RevisionDigest: materializeTestDigest('7')}},
		BindingFingerprint: materializeTestDigest('8'), Execution: resultidentity.ExecutionIdentity{
			PlannerDigest: materializeTestDigest('9'), RuntimeDigest: materializeTestDigest('a'), CapabilityDigest: materializeTestDigest('b'), SettingsDigest: materializeTestDigest('c'),
		}, ResultFormat: resultidentity.ResultFormat{Name: "arrow-result", Version: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	return dependency
}

func semanticCacheTestRequest() dataquery.Query {
	return dataquery.Query{ProjectID: "project:test", ModelID: "sales", PrincipalID: "alice", Kind: dataquery.KindSemanticRows, Target: "orders", Fields: []dataquery.Field{{Field: "orders.id", Alias: "id"}}, Limit: 1, EffectivePolicyFingerprint: materializeTestDigest('d')}
}

func newProtectedCacheArrowExecution() (arrowQueryExecution, error) {
	collector := arrowresult.NewBuilder()
	plan := semanticquery.Plan{SQL: "SELECT 1 AS id", Columns: []string{"id"}}
	if err := writeTestRowsArrow(context.Background(), plan, semanticquery.Rows{{"id": int64(1)}}, collector); err != nil {
		return arrowQueryExecution{}, err
	}
	data, err := collector.Finish()
	if err != nil {
		return arrowQueryExecution{}, err
	}
	return arrowQueryExecution{data: data, metadata: resultcache.Metadata{SQL: plan.SQL}, summary: dataquery.Result{RowsReturned: 1}}, nil
}

func nowForCacheTest() time.Time { return time.Now() }
