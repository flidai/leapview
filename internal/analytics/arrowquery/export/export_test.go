package export

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"github.com/flidai/leapview/internal/analytics/dataquery"
)

func testLimits() Limits {
	return Limits{MaxRows: 10, MaxBytes: 1 << 20}
}

func successfulResult(rows ...dataquery.Row) dataquery.Result {
	return dataquery.Result{
		Columns:        []dataquery.Column{{Name: "name"}, {Name: "amount"}},
		Rows:           rows,
		TotalRows:      len(rows),
		TotalRowsKnown: true,
		Status:         dataquery.StatusSuccess,
		ExecutionState: dataquery.ExecutionSucceeded,
	}
}

func TestEncodeCSVProtectsFormulaPrefixesInHeadersAndValues(t *testing.T) {
	result := dataquery.Result{
		Columns:        []dataquery.Column{{Name: "\t=column"}, {Name: "value"}},
		Rows:           []dataquery.Row{{"\t=column": "\n+SUM(A1)", "value": "safe"}},
		TotalRows:      1,
		TotalRowsKnown: true,
		Status:         dataquery.StatusSuccess,
		ExecutionState: dataquery.ExecutionSucceeded,
	}
	body, err := Encode(context.Background(), result, CSV, testLimits())
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "'\t=column") || !strings.Contains(text, "'\n+SUM(A1)") {
		t.Fatalf("CSV did not protect formula prefixes: %q", text)
	}
}

func TestEncodeCSVRoundTripsQuotedNewlineAndTypedScalars(t *testing.T) {
	result := dataquery.Result{
		Columns:        []dataquery.Column{{Name: "note"}, {Name: "amount"}, {Name: "enabled"}},
		Rows:           []dataquery.Row{{"note": "quoted, value\nnext", "amount": json.Number("123.4500"), "enabled": true}},
		TotalRows:      1,
		TotalRowsKnown: true,
		Status:         dataquery.StatusSuccess,
		ExecutionState: dataquery.ExecutionSucceeded,
	}
	body, err := Encode(context.Background(), result, CSV, testLimits())
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	rows, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	if len(rows) != 2 || rows[1][0] != "quoted, value\nnext" || rows[1][1] != "123.4500" || rows[1][2] != "true" {
		t.Fatalf("CSV rows = %#v", rows)
	}
}

func TestEncodeRejectsPartialAndIndependentBounds(t *testing.T) {
	result := successfulResult(dataquery.Row{"name": "one", "amount": int64(1)})
	result.TotalRows = 2
	if _, err := Encode(context.Background(), result, CSV, testLimits()); !errors.Is(err, ErrPartial) {
		t.Fatalf("partial result error = %v, want ErrPartial", err)
	}

	result = successfulResult(dataquery.Row{"name": strings.Repeat("x", 32), "amount": int64(1)})
	_, err := Encode(context.Background(), result, CSV, Limits{MaxRows: 10, MaxBytes: 8})
	var limit *dataquery.ResultLimitError
	if !errors.As(err, &limit) || limit.Reason != dataquery.ResultBytes {
		t.Fatalf("byte bound error = %v, want bytes limit", err)
	}
}

func TestEncodeParquetPreservesUnsignedAndDecimalTypes(t *testing.T) {
	result := dataquery.Result{
		Columns: []dataquery.Column{{Name: "unsigned"}, {Name: "decimal"}, {Name: "empty"}},
		Rows: []dataquery.Row{{
			"unsigned": uint64(^uint64(0)),
			"decimal":  json.Number("12345678901234567890.1234567890"),
			"empty":    nil,
		}},
		TotalRows:      1,
		TotalRowsKnown: true,
		Status:         dataquery.StatusSuccess,
		ExecutionState: dataquery.ExecutionSucceeded,
	}
	body, err := Encode(context.Background(), result, Parquet, testLimits())
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if len(body) < 8 || string(body[:4]) != "PAR1" || string(body[len(body)-4:]) != "PAR1" {
		t.Fatalf("invalid Parquet framing: %x", body[:min(len(body), 8)])
	}
	table, err := pqarrow.ReadTable(context.Background(), bytes.NewReader(body), nil, pqarrow.ArrowReadProperties{}, memory.DefaultAllocator)
	if err != nil {
		t.Fatalf("read Parquet: %v", err)
	}
	defer table.Release()
	if table.NumRows() != 1 || table.NumCols() != 3 {
		t.Fatalf("Parquet shape = %d rows x %d cols", table.NumRows(), table.NumCols())
	}
	unsigned, ok := table.Column(0).Data().Chunk(0).(*array.Uint64)
	if !ok || unsigned.Value(0) != ^uint64(0) {
		t.Fatalf("unsigned roundtrip = %#v, want MaxUint64", table.Column(0).Data().Chunk(0))
	}
	decimalValues, ok := table.Column(1).Data().Chunk(0).(*array.Decimal128)
	decimalString := ""
	if ok {
		decimalString = decimalValues.Value(0).ToString(decimalValues.DataType().(*arrow.Decimal128Type).Scale)
	}
	if !ok || decimalString != "12345678901234567890.1234567890" {
		t.Fatalf("decimal roundtrip = %q %#v, want exact value", decimalString, table.Column(1).Data().Chunk(0))
	}
	if !table.Column(2).Data().Chunk(0).IsNull(0) {
		t.Fatal("null value did not roundtrip as null")
	}
	types, err := parquetTypes(result)
	if err != nil {
		t.Fatalf("parquetTypes() error = %v", err)
	}
	if types[0].ID() != arrow.UINT64 {
		t.Fatalf("unsigned type = %s, want uint64", types[0])
	}
	decimalType, ok := types[1].(*arrow.Decimal128Type)
	if !ok || decimalType.Precision < 30 || decimalType.Scale != 10 {
		t.Fatalf("decimal type = %#v, want exact decimal128", types[1])
	}
	if types[2].ID() != arrow.STRING {
		t.Fatalf("all-null type = %s, want conservative string", types[2])
	}
}

func TestEncodeParquetRejectsUnsupportedValuesAndCancellation(t *testing.T) {
	result := successfulResult(dataquery.Row{"name": map[string]any{"secret": "value"}, "amount": int64(1)})
	if _, err := Encode(context.Background(), result, Parquet, testLimits()); err == nil || !strings.Contains(err.Error(), "unsupported value type") {
		t.Fatalf("unsupported Parquet value error = %v", err)
	}
	result = successfulResult(dataquery.Row{"name": []byte{0xff}, "amount": int64(1)})
	if _, err := Encode(context.Background(), result, Parquet, testLimits()); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid UTF-8 Parquet value error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Encode(ctx, successfulResult(dataquery.Row{"name": "one", "amount": int64(1)}), CSV, testLimits()); !errors.Is(err, ErrCanceled) {
		t.Fatalf("canceled export error = %v, want ErrCanceled", err)
	}
}

func TestEncodeParquetEmptyResultUsesNullableTextSchema(t *testing.T) {
	result := dataquery.Result{
		Columns:        []dataquery.Column{{Name: "empty"}},
		Rows:           []dataquery.Row{},
		TotalRows:      0,
		TotalRowsKnown: true,
		Status:         dataquery.StatusSuccess,
		ExecutionState: dataquery.ExecutionSucceeded,
	}
	body, err := Encode(context.Background(), result, Parquet, testLimits())
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	table, err := pqarrow.ReadTable(context.Background(), bytes.NewReader(body), nil, pqarrow.ArrowReadProperties{}, memory.DefaultAllocator)
	if err != nil {
		t.Fatalf("read empty Parquet: %v", err)
	}
	defer table.Release()
	if table.NumRows() != 0 || table.NumCols() != 1 || table.Column(0).DataType().ID() != arrow.STRING {
		t.Fatalf("empty Parquet shape/type = %d x %d %s", table.NumRows(), table.NumCols(), table.Column(0).DataType())
	}
}

func TestEncodeParquetHonorsDeclaredTypesForAllNullAndTemporalValues(t *testing.T) {
	instant := time.Date(2026, time.January, 5, 12, 34, 56, 123456000, time.UTC)
	result := dataquery.Result{
		Columns: []dataquery.Column{
			{Name: "empty_decimal", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeDecimal, Precision: 10, Scale: 2}},
			{Name: "empty_timestamp", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeTimestamp, Unit: "microsecond"}},
			{Name: "decimal", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeDecimal, Precision: 10, Scale: 2}},
			{Name: "timestamp", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeTimestamp, Unit: "microsecond"}},
			{Name: "numeric_string", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeString}},
		},
		Rows: []dataquery.Row{{
			"empty_decimal":   nil,
			"empty_timestamp": nil,
			"decimal":         "50.00",
			"timestamp":       instant,
			"numeric_string":  "123.45",
		}},
		TotalRows:      1,
		TotalRowsKnown: true,
		Status:         dataquery.StatusSuccess,
		ExecutionState: dataquery.ExecutionSucceeded,
	}
	body, err := Encode(context.Background(), result, Parquet, testLimits())
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	table, err := pqarrow.ReadTable(context.Background(), bytes.NewReader(body), nil, pqarrow.ArrowReadProperties{}, memory.DefaultAllocator)
	if err != nil {
		t.Fatalf("read Parquet: %v", err)
	}
	defer table.Release()
	if got := table.Schema().Field(0).Type.ID(); got != arrow.DECIMAL128 {
		t.Fatalf("all-null decimal type = %s, want decimal128", got)
	}
	if got := table.Schema().Field(1).Type.ID(); got != arrow.TIMESTAMP {
		t.Fatalf("all-null timestamp type = %s, want timestamp", got)
	}
	decimal, ok := table.Column(2).Data().Chunk(0).(*array.Decimal128)
	if !ok {
		t.Fatalf("declared decimal = %#v, want Decimal128 50.00", table.Column(2).Data().Chunk(0))
	}
	if decimal.Value(0).ToString(2) != "50.00" {
		t.Fatalf("declared decimal = %q %#v, want Decimal128 50.00", decimal.Value(0).ToString(2), table.Column(2).Data().Chunk(0))
	}
	timestamp, ok := table.Column(3).Data().Chunk(0).(*array.Timestamp)
	if !ok || !timestamp.Value(0).ToTime(arrow.Microsecond).Equal(instant) {
		t.Fatalf("declared timestamp = %#v, want %s", table.Column(3).Data().Chunk(0), instant)
	}
	if got := table.Schema().Field(4).Type.ID(); got != arrow.STRING {
		t.Fatalf("numeric-looking string type = %s, want string", got)
	}
}

func TestEncodeParquetPreservesDeclaredDatesAndTimestampMetadata(t *testing.T) {
	preEpoch := time.Date(1969, time.December, 31, 0, 0, 0, 0, time.UTC)
	instant := time.Date(2026, time.January, 5, 12, 34, 56, 123456789, time.FixedZone("CET", 3600))
	result := dataquery.Result{
		Columns: []dataquery.Column{
			{Name: "date_day", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeDate, Unit: "day"}},
			{Name: "date_millisecond", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeDate, Unit: "millisecond"}},
			{Name: "timestamp", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeTimestamp, Unit: "nanosecond", TimeZone: "America/New_York"}},
		},
		Rows: []dataquery.Row{{
			"date_day":         preEpoch,
			"date_millisecond": preEpoch,
			"timestamp":        instant,
		}},
		TotalRows:      1,
		TotalRowsKnown: true,
		Status:         dataquery.StatusSuccess,
		ExecutionState: dataquery.ExecutionSucceeded,
	}
	body, err := Encode(context.Background(), result, Parquet, testLimits())
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	table, err := pqarrow.ReadTable(context.Background(), bytes.NewReader(body), nil, pqarrow.ArrowReadProperties{}, memory.DefaultAllocator)
	if err != nil {
		t.Fatalf("read Parquet: %v", err)
	}
	defer table.Release()
	dateDay, ok := table.Column(0).Data().Chunk(0).(*array.Date32)
	if !ok {
		t.Fatalf("date_day = %#v, want Date32", table.Column(0).Data().Chunk(0))
	}
	if got := dateDay.Value(0); got != arrow.Date32(-1) {
		t.Fatalf("date_day = %d, want -1", got)
	}
	// Parquet's DATE logical type is stored as days, so a Date64 input is
	// normalized to the equivalent Date32 representation on read.
	dateMillisecond, ok := table.Column(1).Data().Chunk(0).(*array.Date32)
	if !ok {
		t.Fatalf("date_millisecond = %#v, want Date32 DATE value", table.Column(1).Data().Chunk(0))
	}
	if got := dateMillisecond.Value(0); got != arrow.Date32(-1) {
		t.Fatalf("date_millisecond = %d, want pre-epoch day -1", got)
	}
	timestamp, ok := table.Column(2).Data().Chunk(0).(*array.Timestamp)
	if !ok {
		t.Fatalf("timestamp = %#v, want Timestamp", table.Column(2).Data().Chunk(0))
	}
	timestampType, ok := table.Schema().Field(2).Type.(*arrow.TimestampType)
	if !ok || timestampType.Unit != arrow.Nanosecond || timestampType.TimeZone != "America/New_York" {
		t.Fatalf("timestamp type = %#v, want nanosecond America/New_York", table.Schema().Field(2).Type)
	}
	if got := timestamp.Value(0).ToTime(arrow.Nanosecond); !got.Equal(instant.UTC()) {
		t.Fatalf("timestamp = %s, want %s", got, instant.UTC())
	}
}

func TestEncodeParquetPreservesSignedDecimalSubunits(t *testing.T) {
	result := dataquery.Result{
		Columns: []dataquery.Column{{Name: "amount", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeDecimal, Precision: 6, Scale: 2}}},
		Rows: []dataquery.Row{
			{"amount": "-0.50"},
			{"amount": "-0.01"},
			{"amount": "0"},
			{"amount": "-12.34"},
		},
		TotalRows:      4,
		TotalRowsKnown: true,
		Status:         dataquery.StatusSuccess,
		ExecutionState: dataquery.ExecutionSucceeded,
	}
	body, err := Encode(context.Background(), result, Parquet, testLimits())
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	table, err := pqarrow.ReadTable(context.Background(), bytes.NewReader(body), nil, pqarrow.ArrowReadProperties{}, memory.DefaultAllocator)
	if err != nil {
		t.Fatalf("read Parquet: %v", err)
	}
	defer table.Release()
	decimal, ok := table.Column(0).Data().Chunk(0).(*array.Decimal128)
	if !ok {
		t.Fatalf("decimal column = %#v, want Decimal128", table.Column(0).Data().Chunk(0))
	}
	want := []string{"-0.50", "-0.01", "0.00", "-12.34"}
	for index, wantValue := range want {
		if got := decimal.Value(index).ToString(2); got != wantValue {
			t.Errorf("decimal row %d = %q, want %q", index, got, wantValue)
		}
	}
}

func TestDecimalValueTextCanonicalizesSignsAndScale(t *testing.T) {
	tests := []struct {
		name      string
		value     any
		scale     int32
		precision int32
		want      string
	}{
		{name: "negative subunit", value: "-0.50", scale: 2, precision: 3, want: "-0.50"},
		{name: "negative one cent", value: "-0.01", scale: 2, precision: 2, want: "-0.01"},
		{name: "zero", value: "0", scale: 2, precision: 3, want: "0.00"},
		{name: "negative whole", value: "-12", scale: 2, precision: 4, want: "-12.00"},
		{name: "leading zeroes", value: "+001.2", scale: 2, precision: 3, want: "1.20"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decimalValueText(tt.value, tt.scale, tt.precision)
			if err != nil {
				t.Fatalf("decimalValueText() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("decimalValueText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDecimalValueTextCountsScalePaddingForPrecision(t *testing.T) {
	if _, err := decimalValueText("12", 2, 3); err == nil || !strings.Contains(err.Error(), "exceeds precision 3") {
		t.Fatalf("decimalValueText() error = %v, want scale-padded precision rejection", err)
	}
}

func TestEncodeParquetHonorsDeclaredPrimitiveTypesForAllNullValues(t *testing.T) {
	result := dataquery.Result{
		Columns: []dataquery.Column{
			{Name: "integer", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeInteger, BitWidth: 32}},
			{Name: "unsigned", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeUnsigned, BitWidth: 16}},
			{Name: "fraction", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeFloat, BitWidth: 32}},
			{Name: "enabled", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeBoolean}},
			{Name: "label", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeString}},
		},
		Rows:           []dataquery.Row{{"integer": nil, "unsigned": nil, "fraction": nil, "enabled": nil, "label": nil}},
		TotalRows:      1,
		TotalRowsKnown: true,
		Status:         dataquery.StatusSuccess,
		ExecutionState: dataquery.ExecutionSucceeded,
	}
	body, err := Encode(context.Background(), result, Parquet, testLimits())
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	table, err := pqarrow.ReadTable(context.Background(), bytes.NewReader(body), nil, pqarrow.ArrowReadProperties{}, memory.DefaultAllocator)
	if err != nil {
		t.Fatalf("read Parquet: %v", err)
	}
	defer table.Release()
	want := []arrow.Type{arrow.INT32, arrow.UINT16, arrow.FLOAT32, arrow.BOOL, arrow.STRING}
	for index, wantType := range want {
		if got := table.Schema().Field(index).Type.ID(); got != wantType {
			t.Fatalf("all-null column %q type = %s, want %s", result.Columns[index].Name, got, wantType)
		}
		if !table.Column(index).Data().Chunk(0).IsNull(0) {
			t.Fatalf("all-null column %q did not retain null", result.Columns[index].Name)
		}
	}
}

func TestEncodeParquetHonorsDeclaredPrimitiveBitWidths(t *testing.T) {
	result := dataquery.Result{
		Columns: []dataquery.Column{
			{Name: "integer", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeInteger, BitWidth: 8}},
			{Name: "unsigned", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeUnsigned, BitWidth: 16}},
			{Name: "fraction", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeFloat, BitWidth: 32}},
			{Name: "enabled", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeBoolean}},
			{Name: "label", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeString}},
		},
		Rows: []dataquery.Row{{
			"integer":  int8(-2),
			"unsigned": uint16(7),
			"fraction": float32(1.5),
			"enabled":  true,
			"label":    "123.45",
		}},
		TotalRows:      1,
		TotalRowsKnown: true,
		Status:         dataquery.StatusSuccess,
		ExecutionState: dataquery.ExecutionSucceeded,
	}
	body, err := Encode(context.Background(), result, Parquet, testLimits())
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	table, err := pqarrow.ReadTable(context.Background(), bytes.NewReader(body), nil, pqarrow.ArrowReadProperties{}, memory.DefaultAllocator)
	if err != nil {
		t.Fatalf("read Parquet: %v", err)
	}
	defer table.Release()
	want := []arrow.Type{arrow.INT8, arrow.UINT16, arrow.FLOAT32, arrow.BOOL, arrow.STRING}
	for index, wantType := range want {
		if got := table.Schema().Field(index).Type.ID(); got != wantType {
			t.Fatalf("declared column %q type = %s, want %s", result.Columns[index].Name, got, wantType)
		}
	}
}

func TestEncodeParquetRejectsInvalidDeclaredTypesAndValues(t *testing.T) {
	tests := []struct {
		name   string
		column dataquery.Column
		value  any
		want   string
	}{
		{
			name:   "unsupported decimal precision",
			column: dataquery.Column{Name: "value", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeDecimal, Precision: 39, Scale: 2}},
			value:  "1.00",
			want:   "invalid decimal precision/scale",
		},
		{
			name:   "decimal scale overflow",
			column: dataquery.Column{Name: "value", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeDecimal, Precision: 10, Scale: 2}},
			value:  "1.234",
			want:   "incompatible with scale",
		},
		{
			name:   "decimal precision after scale padding",
			column: dataquery.Column{Name: "value", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeDecimal, Precision: 3, Scale: 2}},
			value:  "12",
			want:   "exceeds precision 3",
		},
		{
			name:   "malformed decimal",
			column: dataquery.Column{Name: "value", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeDecimal, Precision: 10, Scale: 2}},
			value:  ".",
			want:   "invalid decimal value",
		},
		{
			name:   "malformed temporal unit",
			column: dataquery.Column{Name: "value", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeTimestamp, Unit: "fortnight"}},
			value:  time.Unix(0, 0).UTC(),
			want:   "invalid timestamp unit",
		},
		{
			name:   "incompatible temporal value",
			column: dataquery.Column{Name: "value", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeDate, Unit: "day"}},
			value:  int64(1),
			want:   "incompatible temporal value type",
		},
		{
			name:   "date time-of-day would be truncated",
			column: dataquery.Column{Name: "value", Type: dataquery.ColumnType{Kind: dataquery.ColumnTypeDate, Unit: "millisecond"}},
			value:  time.Date(2026, time.January, 5, 12, 0, 0, 0, time.UTC),
			want:   "contains time-of-day",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := dataquery.Result{
				Columns:        []dataquery.Column{tt.column},
				Rows:           []dataquery.Row{{"value": tt.value}},
				TotalRows:      1,
				TotalRowsKnown: true,
				Status:         dataquery.StatusSuccess,
				ExecutionState: dataquery.ExecutionSucceeded,
			}
			if _, err := Encode(context.Background(), result, Parquet, testLimits()); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Encode() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestEncodeParquetEnforcesBoundBeforeStagingAndFileWrite(t *testing.T) {
	result := successfulResult(dataquery.Row{"name": strings.Repeat("x", 128), "amount": int64(1)})
	_, err := Encode(context.Background(), result, Parquet, Limits{MaxRows: 10, MaxBytes: 16})
	var limit *dataquery.ResultLimitError
	if !errors.As(err, &limit) || limit.Reason != dataquery.ResultBytes {
		t.Fatalf("Parquet byte bound error = %v, want bytes limit", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
