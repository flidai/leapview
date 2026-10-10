package compiler

import (
	"bytes"
	"fmt"
	"sync"
	"testing"

	projectartifact "github.com/flidai/leapview/internal/project/artifact"
)

type concurrentCompileResult struct {
	bundle projectartifact.SourceBundle
	err    error
}

func concurrentCompileBatch(root string, readers int) []concurrentCompileResult {
	start := make(chan struct{})
	results := make([]concurrentCompileResult, readers)
	var ready, finished sync.WaitGroup
	ready.Add(readers)
	finished.Add(readers)
	for index := range readers {
		go func() {
			defer finished.Done()
			ready.Done()
			<-start
			results[index].bundle, results[index].err = Compile(root)
		}()
	}
	ready.Wait()
	close(start)
	finished.Wait()
	return results
}

func requireConcurrentCompileResults(tb testing.TB, results []concurrentCompileResult, expected projectartifact.SourceBundle) {
	tb.Helper()
	for index, result := range results {
		if result.err != nil {
			tb.Fatalf("reader %d: %v", index, result.err)
		}
		if result.bundle.Digest() != expected.Digest() || !bytes.Equal(result.bundle.Canonical(), expected.Canonical()) {
			tb.Fatalf("reader %d changed canonical compilation output", index)
		}
	}
}

// One operation is a complete concurrent compilation batch, including its
// barrier and goroutine lifecycle. The authored 20-resource primary fixture is
// unchanged. Warm preflight and output assertions are excluded from timing.
// Batch ns/op is not request p95, isolated RSS or a production capacity claim.
func BenchmarkCompileSourceRootCapacity(b *testing.B) {
	tc := compileSourceRootBenchmarkCase{name: "resources/20", files: compileSourceRootResourceScaleFixture(20), wantResources: 20, wantEdges: 19}
	for _, readers := range []int{1, 10, 20, 100} {
		b.Run(fmt.Sprintf("readers=%d", readers), func(b *testing.B) {
			b.StopTimer()
			root := writeCompileSourceRootBenchmarkFixture(b, tc.files)
			expected := preflightCompileSourceRootBenchmarkFixture(b, root, tc)
			b.Logf("fixture_sha256=%s bundle_digest=%s", compileSourceRootBenchmarkFixtureDigest(tc.files), expected.Digest())
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				b.StartTimer()
				results := concurrentCompileBatch(root, readers)
				b.StopTimer()
				requireConcurrentCompileResults(b, results, expected)
			}
			b.ReportMetric(float64(readers), "compiles/op")
		})
	}
}

func TestConcurrentCompilerCapacityPreservesCanonicalResults(t *testing.T) {
	tc := compileSourceRootBenchmarkCase{name: "resources/1", files: compileSourceRootResourceScaleFixture(1), wantResources: 1}
	root := writeCompileSourceRootBenchmarkFixture(t, tc.files)
	expected := preflightCompileSourceRootBenchmarkFixture(t, root, tc)
	requireConcurrentCompileResults(t, concurrentCompileBatch(root, 4), expected)
}
