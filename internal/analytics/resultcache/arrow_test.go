package resultcache

import (
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/flidai/leapview/pkg/arrowresult"
)

func TestArrowLookupLeaseSurvivesEviction(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer allocator.AssertSize(t, 0)
	pool, err := New(Limits{PartitionEntries: 1, PartitionBytes: 1 << 20, NodeEntries: 1, NodeBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := pool.OpenScope(ScopeID{RuntimeID: "r", PartitionID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	first := testArrowResult(t, allocator, "first")
	second := testArrowResult(t, allocator, "second")
	if got := scope.StoreArrow("first", 0, first, Metadata{SQL: "select 1"}); got != StoreStored {
		t.Fatalf("store = %q", got)
	}
	first.Release()
	lease, _, hit, err := scope.LookupArrow("first")
	if err != nil || !hit {
		t.Fatalf("lookup hit=%v err=%v", hit, err)
	}
	if got := scope.StoreArrow("second", 0, second, Metadata{}); got != StoreStored {
		t.Fatalf("second store = %q", got)
	}
	second.Release()
	if _, _, hit, err := scope.LookupArrow("first"); err != nil || hit {
		t.Fatalf("evicted lookup hit=%v err=%v", hit, err)
	}
	if lease.Data().Rows() != 1 {
		t.Fatalf("leased rows = %d", lease.Data().Rows())
	}
	lease.Release()
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestArrowLookupLeaseSurvivesDormantScopePurge(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer allocator.AssertSize(t, 0)
	pool, err := New(testLimits())
	if err != nil {
		t.Fatal(err)
	}
	scope, err := pool.OpenSharedScope(ScopeID{RuntimeID: "partition-production"})
	if err != nil {
		t.Fatal(err)
	}
	result := testArrowResult(t, allocator, "retained")
	if got := scope.StoreArrow("query", scope.Generation(), result, Metadata{}); got != StoreStored {
		t.Fatalf("store = %q", got)
	}
	result.Release()
	lease, _, hit, err := scope.LookupArrow("query")
	if err != nil || !hit {
		t.Fatalf("lookup hit=%v err=%v", hit, err)
	}
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	if lease.Data().Rows() != 1 {
		t.Fatalf("leased rows after purge = %d", lease.Data().Rows())
	}
	lease.Release()
}

func TestPoolShutdownReleasesDormantArrowHold(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer allocator.AssertSize(t, 0)
	pool, err := New(testLimits())
	if err != nil {
		t.Fatal(err)
	}
	scope, err := pool.OpenSharedScope(ScopeID{RuntimeID: "partition-production"})
	if err != nil {
		t.Fatal(err)
	}
	result := testArrowResult(t, allocator, "retained")
	if got := scope.StoreArrow("query", scope.Generation(), result, Metadata{}); got != StoreStored {
		t.Fatalf("store = %q", got)
	}
	result.Release()
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	if allocator.CurrentAlloc() == 0 {
		t.Fatal("final shared handle close released dormant Arrow hold")
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	if got := allocator.CurrentAlloc(); got != 0 {
		t.Fatalf("allocator bytes after pool shutdown = %d, want 0", got)
	}
}

func testArrowResult(t *testing.T, allocator memory.Allocator, value string) *arrowresult.Result {
	t.Helper()
	builder := array.NewStringBuilder(allocator)
	builder.Append(value)
	values := builder.NewArray()
	builder.Release()
	record := array.NewRecordBatch(arrow.NewSchema([]arrow.Field{{Name: "value", Type: arrow.BinaryTypes.String}}, nil), []arrow.Array{values}, 1)
	values.Release()
	collector := arrowresult.NewBuilderWithAllocator(allocator)
	if err := collector.WriteSchema(record.Schema()); err != nil {
		t.Fatal(err)
	}
	if err := collector.WriteRecord(record); err != nil {
		t.Fatal(err)
	}
	record.Release()
	result, err := collector.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return result
}
