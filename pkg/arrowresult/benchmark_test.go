package arrowresult

import (
	"bytes"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
	arrowutil "github.com/apache/arrow-go/v18/arrow/util"
)

var arrowBenchmarkBytes []byte

type arrowBenchmarkShape struct {
	name    string
	columns int
}

var arrowBenchmarkShapes = []arrowBenchmarkShape{
	{name: "narrow", columns: 8},
	{name: "wide", columns: 32},
}

var arrowBenchmarkRows = []int{1, 50, 1_000, 10_000}

func BenchmarkArrowCaptureCopy(b *testing.B) {
	for _, shape := range arrowBenchmarkShapes {
		for _, rows := range arrowBenchmarkRows {
			b.Run(shape.name+"/rows_"+strconv.Itoa(rows), func(b *testing.B) {
				record := newArrowBenchmarkRecord(b, rows, shape.columns)
				defer record.Release()
				b.ReportAllocs()
				b.SetBytes(arrowutil.TotalRecordSize(record))
				b.ResetTimer()
				for range b.N {
					builder := NewBuilder()
					if err := builder.WriteSchema(record.Schema()); err != nil {
						b.Fatal(err)
					}
					if err := builder.WriteRecord(record); err != nil {
						b.Fatal(err)
					}
					result, err := builder.Finish()
					if err != nil {
						b.Fatal(err)
					}
					result.Release()
				}
				b.ReportMetric(float64(rows), "rows/op")
				b.ReportMetric(float64(shape.columns), "columns/op")
			})
		}
	}
	for _, rows := range arrowBenchmarkRows {
		b.Run("dictionary/rows_"+strconv.Itoa(rows), func(b *testing.B) {
			record := newArrowBenchmarkDictionaryRecord(b, rows)
			defer record.Release()
			b.ReportAllocs()
			b.SetBytes(arrowutil.TotalRecordSize(record))
			b.ResetTimer()
			for range b.N {
				builder := NewBuilder()
				if err := builder.WriteSchema(record.Schema()); err != nil {
					b.Fatal(err)
				}
				if err := builder.WriteRecord(record); err != nil {
					b.Fatal(err)
				}
				result, err := builder.Finish()
				if err != nil {
					b.Fatal(err)
				}
				result.Release()
			}
			b.ReportMetric(1, "columns/op")
			b.ReportMetric(float64(rows), "rows/op")
		})
	}
}

func BenchmarkArrowIPCExistingDashboardString(b *testing.B) {
	for _, shape := range arrowBenchmarkShapes {
		for _, rows := range arrowBenchmarkRows {
			b.Run(shape.name+"/rows_"+strconv.Itoa(rows), func(b *testing.B) {
				record := newArrowBenchmarkRecord(b, rows, shape.columns)
				defer record.Release()
				stringRows := arrowBenchmarkStringRows(record)
				payload, err := encodeArrowBenchmarkStringIPC(record.Schema(), stringRows)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.SetBytes(int64(len(payload)))
				b.ResetTimer()
				for range b.N {
					payload, err = encodeArrowBenchmarkStringIPC(record.Schema(), stringRows)
					if err != nil {
						b.Fatal(err)
					}
				}
				arrowBenchmarkBytes = payload
				b.ReportMetric(float64(len(payload)), "ipc-bytes/op")
				b.ReportMetric(float64(rows), "rows/op")
			})
		}
	}
}

func BenchmarkArrowIPCNativeReference(b *testing.B) {
	for _, shape := range arrowBenchmarkShapes {
		for _, rows := range arrowBenchmarkRows {
			b.Run(shape.name+"/rows_"+strconv.Itoa(rows), func(b *testing.B) {
				record := newArrowBenchmarkRecord(b, rows, shape.columns)
				defer record.Release()
				payload, err := encodeArrowBenchmarkNativeIPC(record)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.SetBytes(int64(len(payload)))
				b.ResetTimer()
				for range b.N {
					payload, err = encodeArrowBenchmarkNativeIPC(record)
					if err != nil {
						b.Fatal(err)
					}
				}
				arrowBenchmarkBytes = payload
				b.ReportMetric(float64(len(payload)), "ipc-bytes/op")
				b.ReportMetric(float64(rows), "rows/op")
			})
		}
	}
}

func TestArrowBenchmarkFixtureIsDeterministic(t *testing.T) {
	record := newArrowBenchmarkRecord(t, 50, 8)
	defer record.Release()
	first, err := encodeArrowBenchmarkNativeIPC(record)
	if err != nil {
		t.Fatal(err)
	}
	second, err := encodeArrowBenchmarkNativeIPC(record)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("identical Arrow evidence fixtures produced different IPC streams")
	}
}

func TestArrowBenchmarkCalibrationDetectsExtraCopy(t *testing.T) {
	record := newArrowBenchmarkRecord(t, 50, 8)
	defer record.Release()
	var encodeErr error
	baseline := testing.AllocsPerRun(10, func() {
		arrowBenchmarkBytes, encodeErr = encodeArrowBenchmarkNativeIPC(record)
	})
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	regressed := testing.AllocsPerRun(10, func() {
		arrowBenchmarkBytes, encodeErr = encodeArrowBenchmarkNativeIPC(record)
		arrowBenchmarkBytes = bytes.Clone(arrowBenchmarkBytes)
	})
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	if regressed <= baseline {
		t.Fatalf("controlled extra copy allocations = %.0f, want more than baseline %.0f", regressed, baseline)
	}
}

func newArrowBenchmarkRecord(tb testing.TB, rows, columns int) arrow.RecordBatch {
	tb.Helper()
	fields := make([]arrow.Field, columns)
	for column := range fields {
		fields[column] = arrowBenchmarkField(column)
	}
	schema := arrow.NewSchema(fields, nil)
	builder := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	defer builder.Release()
	for row := 0; row < rows; row++ {
		for column := 0; column < columns; column++ {
			appendArrowBenchmarkValue(tb, builder.Field(column), row, column, (row+column)%13 != 0)
		}
	}
	return builder.NewRecordBatch()
}

func newArrowBenchmarkDictionaryRecord(tb testing.TB, rows int) arrow.RecordBatch {
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
	return record
}

func arrowBenchmarkField(column int) arrow.Field {
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

func appendArrowBenchmarkValue(tb testing.TB, builder array.Builder, row, column int, valid bool) {
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
		tb.Fatalf("unsupported Arrow evidence builder %T", builder)
	}
}

func arrowBenchmarkStringRows(record arrow.RecordBatch) [][]string {
	rows := make([][]string, int(record.NumRows()))
	for row := range rows {
		rows[row] = make([]string, int(record.NumCols()))
		for column := range rows[row] {
			rows[row][column] = arrowBenchmarkCellString(record.Column(column), row)
		}
	}
	return rows
}

func arrowBenchmarkCellString(values arrow.Array, index int) string {
	if values.IsNull(index) {
		return ""
	}
	switch values := values.(type) {
	case *array.Int64:
		return strconv.FormatInt(values.Value(index), 10)
	case *array.Float64:
		return strconv.FormatFloat(values.Value(index), 'g', -1, 64)
	case *array.Boolean:
		return strconv.FormatBool(values.Value(index))
	case *array.String:
		return values.Value(index)
	case *array.Binary:
		return string(values.Value(index))
	case *array.Timestamp:
		dataType := values.DataType().(*arrow.TimestampType)
		return values.Value(index).ToTime(dataType.Unit).UTC().Format(time.RFC3339Nano)
	case *array.Decimal128:
		dataType := values.DataType().(*arrow.Decimal128Type)
		return values.Value(index).ToString(dataType.Scale)
	case *array.Date32:
		return values.Value(index).ToTime().UTC().Format(time.RFC3339Nano)
	default:
		panic(fmt.Sprintf("unsupported Arrow evidence array %T", values))
	}
}

func encodeArrowBenchmarkStringIPC(nativeSchema *arrow.Schema, rows [][]string) ([]byte, error) {
	fields := make([]arrow.Field, len(nativeSchema.Fields()))
	for index, native := range nativeSchema.Fields() {
		fields[index] = arrow.Field{
			Name: native.Name, Type: arrow.BinaryTypes.String, Nullable: native.Nullable,
			Metadata: arrow.NewMetadata([]string{"leapview.logical_type"}, []string{arrowBenchmarkLogicalType(native.Type)}),
		}
	}
	schema := arrow.NewSchema(fields, nil)
	arrays := make([]arrow.Array, len(fields))
	for column := range fields {
		builder := array.NewStringBuilder(memory.DefaultAllocator)
		for _, row := range rows {
			builder.Append(row[column])
		}
		arrays[column] = builder.NewArray()
		builder.Release()
	}
	defer func() {
		for _, values := range arrays {
			values.Release()
		}
	}()
	record := array.NewRecordBatch(schema, arrays, int64(len(rows)))
	defer record.Release()
	return encodeArrowBenchmarkNativeIPC(record)
}

func arrowBenchmarkLogicalType(dataType arrow.DataType) string {
	switch dataType.ID() {
	case arrow.BOOL:
		return "boolean"
	case arrow.INT64:
		return "int64"
	case arrow.FLOAT64:
		return "float64"
	case arrow.DECIMAL128:
		return "decimal"
	case arrow.TIMESTAMP, arrow.DATE32:
		return "timestamp"
	default:
		return "string"
	}
}

func encodeArrowBenchmarkNativeIPC(record arrow.RecordBatch) ([]byte, error) {
	var output bytes.Buffer
	writer := ipc.NewWriter(&output, ipc.WithSchema(record.Schema()))
	if err := writer.Write(record); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
