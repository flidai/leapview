package arrowdecode

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/flidai/leapview/pkg/arrowresult"
)

var arrowDecodeBenchmarkResult []map[string]any

func BenchmarkArrowDecodeRows(b *testing.B) {
	shapes := []struct {
		name    string
		columns int
	}{
		{name: "narrow", columns: 8},
		{name: "wide", columns: 32},
	}
	for _, shape := range shapes {
		for _, rows := range []int{1, 50, 1_000, 10_000} {
			b.Run(shape.name+"/rows_"+strconv.Itoa(rows), func(b *testing.B) {
				lease := newArrowDecodeBenchmarkLease(b, rows, shape.columns)
				b.Cleanup(lease.Release)
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					decoded, err := DecodeRows(lease)
					if err != nil {
						b.Fatal(err)
					}
					arrowDecodeBenchmarkResult = decoded
				}
				b.ReportMetric(float64(rows), "rows/op")
				b.ReportMetric(float64(shape.columns), "columns/op")
			})
		}
	}
	for _, rows := range []int{1, 50, 1_000, 10_000} {
		b.Run("dictionary/rows_"+strconv.Itoa(rows), func(b *testing.B) {
			lease := newArrowDecodeDictionaryLease(b, rows)
			b.Cleanup(lease.Release)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				decoded, err := DecodeRows(lease)
				if err != nil {
					b.Fatal(err)
				}
				arrowDecodeBenchmarkResult = decoded
			}
			b.ReportMetric(1, "columns/op")
			b.ReportMetric(float64(rows), "rows/op")
		})
	}
}

func newArrowDecodeBenchmarkLease(tb testing.TB, rows, columns int) *arrowresult.Lease {
	tb.Helper()
	fields := make([]arrow.Field, columns)
	for column := range fields {
		fields[column] = arrowDecodeBenchmarkField(column)
	}
	recordBuilder := array.NewRecordBuilder(memory.DefaultAllocator, arrow.NewSchema(fields, nil))
	for row := 0; row < rows; row++ {
		for column := 0; column < columns; column++ {
			appendArrowDecodeBenchmarkValue(tb, recordBuilder.Field(column), row, column, (row+column)%13 != 0)
		}
	}
	record := recordBuilder.NewRecordBatch()
	recordBuilder.Release()
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
	lease, err := result.Acquire()
	result.Release()
	if err != nil {
		tb.Fatal(err)
	}
	return lease
}

func newArrowDecodeDictionaryLease(tb testing.TB, rows int) *arrowresult.Lease {
	tb.Helper()
	dictionaryType := &arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int16, ValueType: arrow.BinaryTypes.String}
	dictionaryBuilder := array.NewDictionaryBuilder(memory.DefaultAllocator, dictionaryType)
	values := dictionaryBuilder.(*array.BinaryDictionaryBuilder)
	for row := 0; row < rows; row++ {
		if row%13 == 0 {
			values.AppendNull()
			continue
		}
		if err := values.AppendString("category-" + strconv.Itoa(row%97)); err != nil {
			dictionaryBuilder.Release()
			tb.Fatal(err)
		}
	}
	dictionary := dictionaryBuilder.NewDictionaryArray()
	dictionaryBuilder.Release()
	record := array.NewRecordBatch(
		arrow.NewSchema([]arrow.Field{{Name: "category", Type: dictionaryType, Nullable: true}}, nil),
		[]arrow.Array{dictionary}, int64(rows),
	)
	dictionary.Release()
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
	lease, err := result.Acquire()
	result.Release()
	if err != nil {
		tb.Fatal(err)
	}
	return lease
}

func arrowDecodeBenchmarkField(column int) arrow.Field {
	field := arrow.Field{Name: fmt.Sprintf("field_%02d", column), Nullable: true}
	switch column % 8 {
	case 0:
		field.Type = arrow.PrimitiveTypes.Int64
	case 1:
		field.Type = arrow.PrimitiveTypes.Float64
	case 2:
		field.Type = arrow.FixedWidthTypes.Boolean
	case 3:
		field.Type = arrow.BinaryTypes.String
	case 4:
		field.Type = arrow.BinaryTypes.Binary
	case 5:
		field.Type = arrow.FixedWidthTypes.Timestamp_us
	case 6:
		field.Type = &arrow.Decimal128Type{Precision: 38, Scale: 3}
	case 7:
		field.Type = arrow.FixedWidthTypes.Date32
	}
	return field
}

func appendArrowDecodeBenchmarkValue(tb testing.TB, builder array.Builder, row, column int, valid bool) {
	tb.Helper()
	if !valid {
		builder.AppendNull()
		return
	}
	value := int64(row*37 + column + 1)
	switch builder := builder.(type) {
	case *array.Int64Builder:
		builder.Append(value)
	case *array.Float64Builder:
		builder.Append(float64(value)/7.0 + 0.125)
	case *array.BooleanBuilder:
		builder.Append((row+column)%2 == 0)
	case *array.StringBuilder:
		builder.Append("value-" + strconv.Itoa((row*17+column)%997))
	case *array.BinaryBuilder:
		builder.Append([]byte("bytes-" + strconv.Itoa((row*19+column)%997)))
	case *array.TimestampBuilder:
		builder.Append(arrow.Timestamp(1_700_000_000_000_000 + value*1_000))
	case *array.Decimal128Builder:
		builder.Append(decimal128.FromI64(value * 1_000))
	case *array.Date32Builder:
		builder.Append(arrow.Date32(19_000 + value%2_000))
	default:
		tb.Fatalf("unsupported Arrow decode benchmark builder %T", builder)
	}
}
