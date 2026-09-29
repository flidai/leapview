package export

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"strings"
	"testing"

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
