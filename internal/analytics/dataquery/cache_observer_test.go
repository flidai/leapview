package dataquery

import (
	"context"
	"testing"
	"time"
)

func TestCacheOutcomeObserverUsesRequestContext(t *testing.T) {
	observed := []string{}
	ctx := WithCacheOutcomeObserver(context.Background(), func(outcome string) {
		observed = append(observed, outcome)
	})

	ObserveCacheOutcome(ctx, CacheHit)
	ObserveCacheOutcome(ctx, "")
	ObserveCacheOutcome(context.Background(), CacheMiss)

	if len(observed) != 1 || observed[0] != CacheHit {
		t.Fatalf("observed cache outcomes = %#v, want [hit]", observed)
	}
}

func TestTypedCacheObserverUsesFixedContract(t *testing.T) {
	observed := []CacheObservation{}
	ctx := WithCacheObserver(context.Background(), func(observation CacheObservation) {
		observed = append(observed, observation)
	})
	input := CacheObservation{
		Phase: CacheObservationAdmission, Decision: CacheAdmissionBypassed,
		AdmissionReason: CacheAdmissionReasonDependencyUnavailable,
	}
	ObserveCache(ctx, input)
	ObserveCache(ctx, CacheObservation{})
	ObserveCache(context.Background(), input)
	if len(observed) != 1 || observed[0] != input {
		t.Fatalf("typed cache observations = %#v, want %#v", observed, []CacheObservation{input})
	}

	assertUniqueCacheLabels(t, []string{
		string(CacheAdmissionReasonEligible), string(CacheAdmissionReasonQueryNotCacheable),
		string(CacheAdmissionReasonPlanningFailed), string(CacheAdmissionReasonCanceled),
		string(CacheAdmissionReasonDependencyUnavailable),
		string(CacheAdmissionReasonDependencyInvalid), string(CacheAdmissionReasonPolicyInvalid),
		string(CacheAdmissionReasonPartitionInvalid), string(CacheAdmissionReasonNonDeterministic),
	})
	assertUniqueCacheLabels(t, []string{
		string(CacheLookupMissColdStart), string(CacheLookupMissAbsentEntry), string(CacheLookupMissQueryMismatch),
		string(CacheLookupMissInvalidated), string(CacheLookupMissEvicted),
	})
}

func assertUniqueCacheLabels(t *testing.T, values []string) {
	t.Helper()
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			t.Fatal("cache observation label must not be empty")
		}
		if _, ok := seen[value]; ok {
			t.Fatalf("duplicate cache observation label %q", value)
		}
		seen[value] = struct{}{}
	}
}

func TestConnectionWaitCounterAccumulatesAndPreservesOuterObserver(t *testing.T) {
	var observed time.Duration
	ctx := WithConnectionWaitObserver(context.Background(), func(wait time.Duration) { observed += wait })
	ctx, counter := WithConnectionWaitCounter(ctx)

	ObserveConnectionWait(ctx, 12*time.Millisecond)
	ObserveConnectionWait(ctx, 8*time.Millisecond)

	if got := counter.Duration(); got != 20*time.Millisecond {
		t.Fatalf("counter duration = %s, want 20ms", got)
	}
	if observed != 20*time.Millisecond {
		t.Fatalf("outer observer duration = %s, want 20ms", observed)
	}
}

func TestPhysicalQueryObserverUsesRequestContext(t *testing.T) {
	observed := []PhysicalQueryObservation{}
	ctx := WithPhysicalQueryObserver(context.Background(), func(observation PhysicalQueryObservation) {
		observed = append(observed, observation)
	})

	ObservePhysicalQuery(ctx, PhysicalQueryObservation{Count: 2, Result: Result{PlanningMS: 3, DatabaseMS: 7}})
	ObservePhysicalQuery(context.Background(), PhysicalQueryObservation{})

	if len(observed) != 1 || observed[0].Count != 2 || observed[0].Result.PlanningMS != 3 || observed[0].Result.DatabaseMS != 7 {
		t.Fatalf("observed physical queries = %#v, want one result with stage timings", observed)
	}
}
