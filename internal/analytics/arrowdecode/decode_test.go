package arrowdecode

import (
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/flidai/leapview/pkg/arrowresult"
)

func TestDecodeDecimalAlwaysUsesCanonicalStringTransport(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer allocator.AssertSize(t, 0)

	for _, test := range []struct {
		name  string
		scale int32
		value string
	}{
		{name: "scale zero", scale: 0, value: "9007199254740993"},
		{name: "negative scale zero", scale: 0, value: "-7"},
		{name: "fractional", scale: 3, value: "9007199254740993.125"},
	} {
		t.Run(test.name, func(t *testing.T) {
			typeInfo := &arrow.Decimal128Type{Precision: 38, Scale: test.scale}
			builder := array.NewDecimal128Builder(allocator, typeInfo)
			value, err := decimal128.FromString(test.value, typeInfo.Precision, typeInfo.Scale)
			if err != nil {
				t.Fatal(err)
			}
			builder.Append(value)
			values := builder.NewDecimal128Array()
			builder.Release()
			defer values.Release()

			decode, err := compileValueDecoder(values)
			if err != nil {
				t.Fatal(err)
			}
			got := decode(0)
			if got != test.value {
				t.Fatalf("decoded Decimal = %#v (%T), want canonical string %q", got, got, test.value)
			}
		})
	}
}

func TestDecodeRowsOwnsVariableWidthValues(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer allocator.AssertSize(t, 0)

	stringsBuilder := array.NewStringBuilder(allocator)
	stringsBuilder.Append("alpha")
	stringsArray := stringsBuilder.NewArray()
	stringsBuilder.Release()
	binaryBuilder := array.NewBinaryBuilder(allocator, arrow.BinaryTypes.Binary)
	binaryBuilder.Append([]byte("bravo"))
	binaryArray := binaryBuilder.NewArray()
	binaryBuilder.Release()
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "text", Type: arrow.BinaryTypes.String},
		{Name: "bytes", Type: arrow.BinaryTypes.Binary},
	}, nil)
	record := array.NewRecordBatch(schema, []arrow.Array{stringsArray, binaryArray}, 1)
	stringsArray.Release()
	binaryArray.Release()

	collector := arrowresult.NewBuilderWithAllocator(allocator)
	if err := collector.WriteSchema(schema); err != nil {
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
	lease, err := result.Acquire()
	result.Release()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := DecodeRows(lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.VisitRecords(func(record arrow.RecordBatch) error {
		record.Column(0).Data().Buffers()[2].Bytes()[0] = 'z'
		record.Column(1).Data().Buffers()[2].Bytes()[0] = 'z'
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	lease.Release()

	if got := rows[0]["text"]; got != "alpha" {
		t.Fatalf("decoded string changed with Arrow buffer: %q", got)
	}
	if got := string(rows[0]["bytes"].([]byte)); got != "bravo" {
		t.Fatalf("decoded binary changed with Arrow buffer: %q", got)
	}
}

func TestDecodeRowsPreservesPhysicalValuesAndNulls(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer allocator.AssertSize(t, 0)
	recordBuilder := array.NewRecordBuilder(allocator, arrow.NewSchema([]arrow.Field{
		{Name: "id", Type: arrow.PrimitiveTypes.Int64},
		{Name: "name", Type: arrow.BinaryTypes.String, Nullable: true},
		{Name: "at", Type: arrow.FixedWidthTypes.Timestamp_us},
	}, nil))
	recordBuilder.Field(0).(*array.Int64Builder).AppendValues([]int64{7, 8}, nil)
	recordBuilder.Field(1).(*array.StringBuilder).AppendValues([]string{"first", ""}, []bool{true, false})
	wantTime := time.Date(2025, 2, 3, 4, 5, 6, 7000, time.UTC)
	timestamp, err := arrow.TimestampFromTime(wantTime, arrow.Microsecond)
	if err != nil {
		t.Fatal(err)
	}
	recordBuilder.Field(2).(*array.TimestampBuilder).AppendValues([]arrow.Timestamp{timestamp, timestamp}, nil)
	record := recordBuilder.NewRecordBatch()
	recordBuilder.Release()

	collector := arrowresult.NewBuilder()
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
	defer result.Release()
	lease, err := result.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	rows, err := DecodeRows(lease)
	if err != nil {
		t.Fatal(err)
	}
	if got := rows[0]["id"]; got != int64(7) {
		t.Fatalf("id = %#v", got)
	}
	if got := rows[1]["name"]; got != nil {
		t.Fatalf("null = %#v", got)
	}
	if got := rows[0]["at"]; !got.(time.Time).Equal(wantTime) {
		t.Fatalf("timestamp = %#v", got)
	}
}
