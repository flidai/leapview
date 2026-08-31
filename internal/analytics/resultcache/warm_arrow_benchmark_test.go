package resultcache

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/flidai/leapview/pkg/arrowresult"
)

var warmArrowCacheLookupRows int64

// BenchmarkWarmArrowCacheLookupLease isolates the public retained-result
// lookup and independent lease acquisition/release cost from Arrow decoding.
// The opaque fixture key is local to this benchmark; no materialization cache
// identity or lifecycle state is inspected.
func BenchmarkWarmArrowCacheLookupLease(b *testing.B) {
	for _, shape := range []struct {
		name    string
		rows    int
		columns int
	}{
		{name: "narrow/rows_50", rows: 50, columns: 8},
		{name: "narrow/rows_1000", rows: 1_000, columns: 8},
		{name: "wide/rows_50", rows: 50, columns: 32},
		{name: "wide/rows_1000", rows: 1_000, columns: 32},
	} {
		b.Run(shape.name, func(b *testing.B) {
			pool, err := New(Limits{RuntimeEntries: 4, RuntimeBytes: 64 << 20, NodeEntries: 4, NodeBytes: 64 << 20})
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if err := pool.Close(); err != nil {
					b.Errorf("close benchmark cache: %v", err)
				}
			})
			scope, err := pool.OpenScope(ScopeID{RuntimeID: "fai-542-warm-cache-lookup"})
			if err != nil {
				b.Fatal(err)
			}
			result := newWarmArrowCacheLookupResult(b, shape.rows, shape.columns)
			if outcome := scope.StoreArrow("fixture-entry", scope.Generation(), result, Metadata{}); outcome != StoreStored {
				result.Release()
				b.Fatalf("store benchmark Arrow result = %q, want %q", outcome, StoreStored)
			}
			result.Release()

			b.ReportAllocs()
			b.ReportMetric(float64(shape.rows), "rows/op")
			b.ReportMetric(float64(shape.columns), "columns/op")
			b.ResetTimer()
			for range b.N {
				entry, _, hit, err := scope.LookupArrow("fixture-entry")
				if err != nil || !hit || entry == nil {
					b.Fatalf("warm Arrow lookup hit=%v err=%v", hit, err)
				}
				warmArrowCacheLookupRows = entry.Data().Rows()
				entry.Release()
			}
		})
	}
}

func newWarmArrowCacheLookupResult(tb testing.TB, rows, columns int) *arrowresult.Result {
	tb.Helper()
	fields := make([]arrow.Field, columns)
	for column := range fields {
		if column%2 == 0 {
			fields[column] = arrow.Field{Name: fmt.Sprintf("field_%02d", column), Type: arrow.PrimitiveTypes.Int64, Nullable: true}
		} else {
			fields[column] = arrow.Field{Name: fmt.Sprintf("field_%02d", column), Type: arrow.BinaryTypes.String, Nullable: true}
		}
	}
	builder := array.NewRecordBuilder(memory.DefaultAllocator, arrow.NewSchema(fields, nil))
	for row := 0; row < rows; row++ {
		for column := range fields {
			if (row+column)%13 == 0 {
				builder.Field(column).AppendNull()
				continue
			}
			switch values := builder.Field(column).(type) {
			case *array.Int64Builder:
				values.Append(int64(row*37 + column))
			case *array.StringBuilder:
				values.Append("value-" + strconv.Itoa((row+column)%997))
			}
		}
	}
	record := builder.NewRecordBatch()
	builder.Release()
	collector := arrowresult.NewBuilder()
	if err := collector.WriteSchema(record.Schema()); err != nil {
		record.Release()
		tb.Fatal(err)
	}
	if err := collector.WriteRecord(record); err != nil {
		record.Release()
		tb.Fatal(err)
	}
	record.Release()
	result, err := collector.Finish()
	if err != nil {
		tb.Fatal(err)
	}
	return result
}
