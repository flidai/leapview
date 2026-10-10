package snapshot

import (
	"fmt"
	"sync"
	"testing"
)

func concurrentAuthorizationBatch(fixture authorizationBenchmarkFixture, readers int) []error {
	start := make(chan struct{})
	errors := make([]error, readers)
	var ready, finished sync.WaitGroup
	ready.Add(readers)
	finished.Add(readers)
	for index := range readers {
		go func() {
			defer finished.Done()
			ready.Done()
			<-start
			for _, decision := range []struct{ want bool }{{true}, {false}} {
				pair := fixture.allowed
				if !decision.want {
					pair = fixture.denied
				}
				allowed, err := fixture.snapshot.AllowsTyped(fixture.subject, pair)
				if err != nil || allowed != decision.want {
					errors[index] = fmt.Errorf("AllowsTyped = (%v, %v), want (%v, nil)", allowed, err, decision.want)
					return
				}
			}
		}()
	}
	ready.Wait()
	close(start)
	finished.Wait()
	return errors
}

func requireConcurrentAuthorizationBatch(tb testing.TB, results []error) {
	tb.Helper()
	for index, err := range results {
		if err != nil {
			tb.Fatalf("reader %d: %v", index, err)
		}
	}
}

// One operation is an exact simultaneous reader batch, each making an allowed
// and denied decision through the unchanged public bound-validation path.
// Setup is excluded; coordination and error checks are measured. This is
// descriptive batch cost, not user p95 or adoption of the rejected helper.
func BenchmarkAuthorizationSnapshotCapacity(b *testing.B) {
	for _, readers := range []int{1, 10, 20, 100} {
		b.Run(fmt.Sprintf("readers=%d", readers), func(b *testing.B) {
			fixture := newAuthorizationBenchmarkFixture(b, authorizationBenchmarkSize{"all=100", 100, 100, 100, 100})
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				requireConcurrentAuthorizationBatch(b, concurrentAuthorizationBatch(fixture, readers))
			}
			b.ReportMetric(float64(readers*2), "decisions/op")
		})
	}
}

func TestConcurrentAuthorizationCapacityPreservesAllowAndDeny(t *testing.T) {
	fixture := newAuthorizationBenchmarkFixture(t, authorizationBenchmarkSize{"all=10", 10, 10, 10, 10})
	before := fixture.snapshot.Project().Digest()
	requireConcurrentAuthorizationBatch(t, concurrentAuthorizationBatch(fixture, 4))
	if fixture.snapshot.Project().Digest() != before {
		t.Fatal("concurrent decisions mutated snapshot graph")
	}
}
