package resultcache

import (
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/flidai/leapview/internal/analytics/arrowresult"
)

func TestArrowLookupLeaseSurvivesEviction(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer allocator.AssertSize(t, 0)
	pool, err := New(Limits{RuntimeEntries: 1, RuntimeBytes: 1 << 20, NodeEntries: 1, NodeBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := pool.OpenScope(ScopeID{RuntimeID: "r"})
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
