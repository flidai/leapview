package materialize

import (
	"context"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/flidai/leapview/internal/analytics/arrowdecode"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/pkg/arrowresult"
)

func TestSplitArrowBundleOwnsIndependentProjectedResults(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer allocator.AssertSize(t, 0)

	ordinal := array.NewInt64Builder(allocator)
	ordinal.AppendValues([]int64{0, 1, 0}, nil)
	ordinals := ordinal.NewArray()
	ordinal.Release()
	valuesBuilder := array.NewStringBuilder(allocator)
	valuesBuilder.AppendValues([]string{"a", "b", "c"}, nil)
	values := valuesBuilder.NewArray()
	valuesBuilder.Release()
	schema := arrow.NewSchema([]arrow.Field{
		{Name: semanticquery.BundleBranchColumn, Type: arrow.PrimitiveTypes.Int64},
		{Name: "__o0", Type: arrow.BinaryTypes.String},
	}, nil)
	record := array.NewRecordBatch(schema, []arrow.Array{ordinals, values}, 3)
	ordinals.Release()
	values.Release()

	collector := arrowresult.NewBuilderWithAllocator(allocator)
	if err := collector.WriteSchema(schema); err != nil {
		t.Fatal(err)
	}
	if err := collector.WriteRecord(record); err != nil {
		t.Fatal(err)
	}
	record.Release()
	source, err := collector.Finish()
	if err != nil {
		t.Fatal(err)
	}
	plan := semanticquery.BundlePlan{Branches: []semanticquery.BundleBranch{
		{ID: "first", Ordinal: 0, Columns: []semanticquery.BundleColumn{{Output: "value", Physical: "__o0"}}},
		{ID: "second", Ordinal: 1, Columns: []semanticquery.BundleColumn{{Output: "label", Physical: "__o0"}}},
	}}
	results, err := splitArrowBundle(context.Background(), plan, source)
	if err != nil {
		t.Fatal(err)
	}
	source.Release()
	defer results["first"].Release()
	defer results["second"].Release()

	first, err := results["first"].Acquire()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := arrowdecode.DecodeRows(first)
	first.Release()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["value"] != "a" || rows[1]["value"] != "c" {
		t.Fatalf("first branch rows = %#v", rows)
	}
	second, err := results["second"].Acquire()
	if err != nil {
		t.Fatal(err)
	}
	rows, err = arrowdecode.DecodeRows(second)
	second.Release()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["label"] != "b" {
		t.Fatalf("second branch rows = %#v", rows)
	}
}
