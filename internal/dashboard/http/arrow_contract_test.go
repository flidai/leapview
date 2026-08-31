package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	httptransport "github.com/flidai/leapview/internal/platform/http/transport"
)

const (
	dashboardNativeArrowContract      = "native-v1"
	dashboardNativeArrowQueryID       = "query-contract-1"
	dashboardNativeArrowSnapshot      = "snapshot-contract-1"
	dashboardNativeArrowSchemaVersion = "7"
	dashboardNativeArrowSpecRevision  = "spec-revision-1"
	dashboardNativeArrowDataRevision  = "11"
)

var dashboardNativeArrowFieldNames = []string{
	"is_active",
	"tiny_signed",
	"small_signed",
	"order_count",
	"order_id",
	"tiny_unsigned",
	"small_unsigned",
	"medium_unsigned",
	"large_unsigned",
	"ratio_32",
	"ratio_64",
	"amount",
	"business_date",
	"event_time_ms",
	"occurred_at",
	"occurred_at_local",
	"customer_alias",
	"payload",
	"status",
}

var dashboardNativeArrowSchemaMetadataAllowlist = map[string]struct{}{
	"leapview.arrow_contract":               {},
	"leapview.query_id":                     {},
	"leapview.serving_snapshot":             {},
	"leapview.visualization_schema_version": {},
	"leapview.visualization_spec_revision":  {},
	"leapview.visualization_data_revision":  {},
}

var dashboardNativeArrowFieldMetadataAllowlist = map[string]struct{}{
	"display.label":         {},
	"leapview.logical_type": {},
}

var dashboardNativeArrowFieldProducerMetadataAllowlist = map[string]struct{}{
	"display.label": {},
}

var dashboardNativeArrowLogicalTypes = map[string]string{
	"is_active":         "boolean",
	"order_id":          "integer",
	"amount":            "decimal",
	"business_date":     "date",
	"occurred_at":       "timestamp",
	"occurred_at_local": "timestamp",
	"customer_alias":    "string",
	"payload":           "binary",
	"status":            "category",
}

// TestDashboardNativeArrowContractPreservesValues is the reusable correctness
// oracle for a future native dashboard implementation. It intentionally uses a
// test-only response writer; the current dashboard serving path is unchanged.
func TestDashboardNativeArrowContractPreservesValues(t *testing.T) {
	response := dashboardNativeArrowContractResponse(t, 3, 0, "scope-a", false)
	defer response.Body.Close()
	assertDashboardNativeArrowHeaders(t, response)

	reader, err := ipc.NewReader(response.Body)
	if err != nil {
		t.Fatalf("open Arrow IPC: %v", err)
	}
	defer reader.Release()
	assertDashboardNativeArrowSchema(t, reader.Schema())
	if !reader.Next() {
		t.Fatalf("read Arrow record: %v", reader.Err())
	}
	assertDashboardNativeArrowValues(t, reader.Record())
	next := reader.Next()
	if next || reader.Err() != nil {
		t.Fatalf("unexpected second record or reader error: next=%v err=%v", next, reader.Err())
	}
	if got := response.Trailer.Get("X-Next-Cursor"); got != "" {
		t.Fatalf("final-page cursor = %q, want empty", got)
	}
}

func TestDashboardNativeArrowContractMetadataAllowlist(t *testing.T) {
	allowedSchema := arrow.MetadataFrom(map[string]string{
		"leapview.arrow_contract":               dashboardNativeArrowContract,
		"leapview.query_id":                     dashboardNativeArrowQueryID,
		"leapview.serving_snapshot":             dashboardNativeArrowSnapshot,
		"leapview.visualization_schema_version": dashboardNativeArrowSchemaVersion,
		"leapview.visualization_spec_revision":  dashboardNativeArrowSpecRevision,
		"leapview.visualization_data_revision":  dashboardNativeArrowDataRevision,
	})
	if err := validateDashboardNativeArrowMetadata(allowedSchema, dashboardNativeArrowSchemaMetadataAllowlist); err != nil {
		t.Fatalf("allowed schema metadata rejected: %v", err)
	}
	allowedField := arrow.MetadataFrom(map[string]string{
		"display.label":         "Customer",
		"leapview.logical_type": "string",
	})
	if err := validateDashboardNativeArrowMetadata(allowedField, dashboardNativeArrowFieldMetadataAllowlist); err != nil {
		t.Fatalf("allowed field metadata rejected: %v", err)
	}

	for _, key := range []string{
		"leapview.unknown",
		"duckdb.query_sql",
		"source.connection",
		"producer.unapproved",
	} {
		t.Run("schema rejects "+key, func(t *testing.T) {
			metadata := arrow.MetadataFrom(map[string]string{key: "secret"})
			if err := validateDashboardNativeArrowMetadata(metadata, dashboardNativeArrowSchemaMetadataAllowlist); err == nil {
				t.Fatalf("unsafe schema metadata %q was accepted", key)
			}
		})
		t.Run("field rejects "+key, func(t *testing.T) {
			metadata := arrow.MetadataFrom(map[string]string{key: "secret"})
			if err := validateDashboardNativeArrowMetadata(metadata, dashboardNativeArrowFieldMetadataAllowlist); err == nil {
				t.Fatalf("unsafe field metadata %q was accepted", key)
			}
		})
	}
}

func TestDashboardNativeArrowContractEmptyResultKeepsSchema(t *testing.T) {
	response := dashboardNativeArrowContractResponse(t, 3, 0, "scope-a", true)
	defer response.Body.Close()
	assertDashboardNativeArrowHeaders(t, response)
	reader, err := ipc.NewReader(response.Body)
	if err != nil {
		t.Fatalf("open empty Arrow IPC: %v", err)
	}
	defer reader.Release()
	assertDashboardNativeArrowSchema(t, reader.Schema())
	next := reader.Next()
	if next || reader.Err() != nil {
		t.Fatalf("empty result contained a record or failed: next=%v err=%v", next, reader.Err())
	}
	if got := response.Trailer.Get("X-Next-Cursor"); got != "" {
		t.Fatalf("empty-result cursor = %q, want empty", got)
	}
}

func TestDashboardNativeArrowContractPaginationUsesCompletionTrailer(t *testing.T) {
	response := dashboardNativeArrowContractResponse(t, 2, 20, "scope-a", false)
	defer response.Body.Close()
	assertDashboardNativeArrowHeaders(t, response)
	if got := response.Header.Get("X-Next-Cursor"); got != "" {
		t.Fatalf("next cursor was exposed as an initial header: %q", got)
	}
	reader, err := ipc.NewReader(response.Body)
	if err != nil {
		t.Fatalf("open paged Arrow IPC: %v", err)
	}
	defer reader.Release()
	var rows int64
	for reader.Next() {
		rows += reader.Record().NumRows()
	}
	if err := reader.Err(); err != nil {
		t.Fatalf("read paged Arrow IPC: %v", err)
	}
	if rows != 2 {
		t.Fatalf("emitted rows = %d, want two rows from a limit+1 fixture", rows)
	}
	cursor := response.Trailer.Get("X-Next-Cursor")
	if cursor == "" {
		t.Fatal("limit+1 probe did not produce X-Next-Cursor trailer")
	}
	if _, err := strconv.Atoi(cursor); err == nil {
		t.Fatalf("cursor %q is a client-visible numeric offset", cursor)
	}
	if offset, err := decodeIndexCursor(cursor, "scope-a", dashboardNativeArrowSnapshot); err != nil || offset != 22 {
		t.Fatalf("decode next cursor = (%d, %v), want offset 22", offset, err)
	}
	if _, err := decodeIndexCursor(cursor, "scope-b", dashboardNativeArrowSnapshot); err == nil {
		t.Fatal("cursor was accepted for a different normalized query scope")
	}
	if _, err := decodeIndexCursor(cursor, "scope-a", "snapshot-other"); !errors.Is(err, errDashboardCursorSnapshot) {
		t.Fatalf("snapshot-mismatched cursor error = %v", err)
	}
}

func TestDashboardNativeArrowContractErrorsRespectCommitBoundary(t *testing.T) {
	beforeCommit := []struct {
		name   string
		status int
		code   string
	}{
		{name: "authentication", status: stdhttp.StatusUnauthorized, code: "AUTHENTICATION_REQUIRED"},
		{name: "authorization", status: stdhttp.StatusForbidden, code: "FORBIDDEN"},
		{name: "authorization concealed", status: stdhttp.StatusNotFound, code: "RESOURCE_NOT_FOUND"},
		{name: "invalid cursor", status: stdhttp.StatusBadRequest, code: "INVALID_CURSOR"},
		{name: "snapshot mismatch", status: stdhttp.StatusConflict, code: "CURSOR_SNAPSHOT_MISMATCH"},
		{name: "result budget", status: stdhttp.StatusUnprocessableEntity, code: "QUERY_RESULT_LIMIT"},
		{name: "admission", status: stdhttp.StatusServiceUnavailable, code: "WORKLOAD_OVERLOADED"},
		{name: "timeout", status: stdhttp.StatusGatewayTimeout, code: "WORKLOAD_EXECUTION_TIMEOUT"},
		{name: "internal", status: stdhttp.StatusInternalServerError, code: "INTERNAL"},
	}
	for _, test := range beforeCommit {
		t.Run("before commit/"+test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/dashboards/sales/pages/main/visuals/orders/query", nil)
			httptransport.WriteProblem(recorder, request, test.status, test.code, test.name, nil)
			response := recorder.Result()
			defer response.Body.Close()
			if response.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.status)
			}
			if got := response.Header.Get("Content-Type"); got != "application/problem+json" {
				t.Fatalf("content type = %q", got)
			}
			if got := response.Header.Get("X-LeapView-Arrow-Contract"); got != "" {
				t.Fatalf("problem response claimed Arrow contract %q", got)
			}
			var problem httptransport.ProblemDetails
			if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
				t.Fatalf("decode problem response: %v", err)
			}
			if problem.Status != int32(test.status) || problem.Code != test.code {
				t.Fatalf("problem = %#v", problem)
			}
		})
	}

	t.Run("after commit", func(t *testing.T) {
		complete := dashboardNativeArrowContractBody(t, false, 3)
		recorder := httptest.NewRecorder()
		recorder.Header().Set("Content-Type", "application/vnd.apache.arrow.stream")
		recorder.Header().Set("X-LeapView-Arrow-Contract", dashboardNativeArrowContract)
		recorder.Header().Set("Trailer", "X-Next-Cursor")
		recorder.WriteHeader(stdhttp.StatusOK)
		_, _ = recorder.Write(complete[:len(complete)/2])
		response := recorder.Result()
		defer response.Body.Close()
		if err := consumeDashboardNativeArrow(response.Body); err == nil {
			t.Fatal("truncated committed Arrow stream was accepted")
		}
		if got := response.Trailer.Get("X-Next-Cursor"); got != "" {
			t.Fatalf("failed stream exposed success cursor %q", got)
		}
	})
}

func TestDashboardNativeArrowContractChargesSchemaAndPaginationProbeToBudgets(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	schema, record := newDashboardNativeArrowContractFixture(t, allocator)
	defer allocator.AssertSize(t, 0)
	defer record.Release()

	rowContext := dataquery.WithResultBudget(context.Background(), dataquery.ResultLimits{MaxRows: 2, MaxBytes: 1 << 20})
	if err := arrowquery.ConsumeSchemaBudget(rowContext, schema); err != nil {
		t.Fatalf("consume schema budget: %v", err)
	}
	err := arrowquery.ConsumeResultBudget(rowContext, record)
	var limit *dataquery.ResultLimitError
	if !errors.As(err, &limit) || limit.Reason != dataquery.ResultRows || limit.Observed != 3 {
		t.Fatalf("limit+1 probe budget error = %v, want observed row count 3", err)
	}

	byteContext := dataquery.WithResultBudget(context.Background(), dataquery.ResultLimits{MaxRows: 3, MaxBytes: 1})
	err = arrowquery.ConsumeSchemaBudget(byteContext, schema)
	if !errors.As(err, &limit) || limit.Reason != dataquery.ResultBytes || limit.Observed <= 1 {
		t.Fatalf("schema byte budget error = %v", err)
	}
}

func dashboardNativeArrowContractResponse(t testing.TB, limit, offset int, scope string, empty bool) *stdhttp.Response {
	t.Helper()
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "application/vnd.apache.arrow.stream")
	recorder.Header().Set("Cache-Control", "no-store")
	recorder.Header().Set("X-Query-ID", dashboardNativeArrowQueryID)
	recorder.Header().Set("X-Serving-Snapshot", dashboardNativeArrowSnapshot)
	recorder.Header().Set("X-LeapView-Arrow-Contract", dashboardNativeArrowContract)
	recorder.Header().Set("Trailer", "X-Next-Cursor")
	recorder.WriteHeader(stdhttp.StatusOK)
	body := dashboardNativeArrowContractBody(t, empty, limit)
	_, _ = recorder.Write(body)
	if !empty && limit < 3 {
		recorder.Header().Set("X-Next-Cursor", encodeIndexCursor(offset+limit, scope, dashboardNativeArrowSnapshot))
	}
	return recorder.Result()
}

func dashboardNativeArrowContractBody(t testing.TB, empty bool, limit int) []byte {
	t.Helper()
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	schema, record := newDashboardNativeArrowContractFixture(t, allocator)
	var output bytes.Buffer
	writer := ipc.NewWriter(&output, ipc.WithSchema(schema), ipc.WithAllocator(allocator))
	if !empty {
		emitted := record
		sliced := false
		if int64(limit) < record.NumRows() {
			emitted = record.NewSlice(0, int64(limit))
			sliced = true
		}
		err := writer.Write(emitted)
		if sliced {
			emitted.Release()
		}
		if err != nil {
			record.Release()
			_ = writer.Close()
			allocator.AssertSize(t, 0)
			t.Fatalf("write Arrow fixture: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		record.Release()
		allocator.AssertSize(t, 0)
		t.Fatalf("close Arrow fixture: %v", err)
	}
	record.Release()
	allocator.AssertSize(t, 0)
	return output.Bytes()
}

func newDashboardNativeArrowContractFixture(t testing.TB, allocator memory.Allocator) (*arrow.Schema, arrow.RecordBatch) {
	t.Helper()
	decimalType := &arrow.Decimal128Type{Precision: 20, Scale: 4}
	timestampUTCType := &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "UTC"}
	timestampLocalType := &arrow.TimestampType{Unit: arrow.Nanosecond, TimeZone: ""}
	dictionaryType := &arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int8, ValueType: arrow.BinaryTypes.String}
	fields := []arrow.Field{
		{Name: "is_active", Type: arrow.FixedWidthTypes.Boolean, Nullable: true},
		{Name: "tiny_signed", Type: arrow.PrimitiveTypes.Int8, Nullable: true},
		{Name: "small_signed", Type: arrow.PrimitiveTypes.Int16, Nullable: true},
		{Name: "order_count", Type: arrow.PrimitiveTypes.Int32, Nullable: true},
		{Name: "order_id", Type: arrow.PrimitiveTypes.Int64, Nullable: false},
		{Name: "tiny_unsigned", Type: arrow.PrimitiveTypes.Uint8, Nullable: true},
		{Name: "small_unsigned", Type: arrow.PrimitiveTypes.Uint16, Nullable: true},
		{Name: "medium_unsigned", Type: arrow.PrimitiveTypes.Uint32, Nullable: true},
		{Name: "large_unsigned", Type: arrow.PrimitiveTypes.Uint64, Nullable: true},
		{Name: "ratio_32", Type: arrow.PrimitiveTypes.Float32, Nullable: true},
		{Name: "ratio_64", Type: arrow.PrimitiveTypes.Float64, Nullable: true},
		{Name: "amount", Type: decimalType, Nullable: true},
		{Name: "business_date", Type: arrow.FixedWidthTypes.Date32, Nullable: true},
		{Name: "event_time_ms", Type: arrow.FixedWidthTypes.Date64, Nullable: true},
		{Name: "occurred_at", Type: timestampUTCType, Nullable: true},
		{Name: "occurred_at_local", Type: timestampLocalType, Nullable: true},
		{Name: "customer_alias", Type: arrow.BinaryTypes.String, Nullable: true, Metadata: arrow.MetadataFrom(map[string]string{
			"display.label":         "Customer",
			"duckdb.query_sql":      "select customer_name from orders",
			"leapview.logical_type": "spoofed",
			"leapview.source_field": "orders.customer_name",
			"source.connection":     "warehouse-primary",
		})},
		{Name: "payload", Type: arrow.BinaryTypes.Binary, Nullable: true},
		{Name: "status", Type: dictionaryType, Nullable: true},
	}
	upstream := arrow.NewSchema(fields, &arrow.Metadata{})
	upstreamMetadata := arrow.MetadataFrom(map[string]string{
		"duckdb.query_sql":        "select secret",
		"leapview.arrow_contract": "spoofed",
		"leapview.query_id":       "spoofed",
		"producer.unapproved":     "must not survive",
		"source.connection":       "warehouse-primary",
	})
	upstream = arrow.NewSchema(fields, &upstreamMetadata)
	schema := dashboardNativeArrowContractSchema(upstream, dashboardNativeArrowLogicalTypes, dashboardNativeArrowQueryID, dashboardNativeArrowSnapshot)

	validity := []bool{true, false, true}
	builders := []array.Builder{
		array.NewBooleanBuilder(allocator),
		array.NewInt8Builder(allocator),
		array.NewInt16Builder(allocator),
		array.NewInt32Builder(allocator),
		array.NewInt64Builder(allocator),
		array.NewUint8Builder(allocator),
		array.NewUint16Builder(allocator),
		array.NewUint32Builder(allocator),
		array.NewUint64Builder(allocator),
		array.NewFloat32Builder(allocator),
		array.NewFloat64Builder(allocator),
		array.NewDecimal128Builder(allocator, decimalType),
		array.NewDate32Builder(allocator),
		array.NewDate64Builder(allocator),
		array.NewTimestampBuilder(allocator, timestampUTCType),
		array.NewTimestampBuilder(allocator, timestampLocalType),
		array.NewStringBuilder(allocator),
		array.NewBinaryBuilder(allocator, arrow.BinaryTypes.Binary),
		array.NewDictionaryBuilder(allocator, dictionaryType),
	}
	builders[0].(*array.BooleanBuilder).AppendValues([]bool{true, false, false}, validity)
	builders[1].(*array.Int8Builder).AppendValues([]int8{-8, 0, 7}, validity)
	builders[2].(*array.Int16Builder).AppendValues([]int16{-32000, 0, 32000}, validity)
	builders[3].(*array.Int32Builder).AppendValues([]int32{-2_000_000_000, 0, 2_000_000_000}, validity)
	builders[4].(*array.Int64Builder).AppendValues([]int64{-9_007_199_254_740_993, 0, 9_007_199_254_740_993}, nil)
	builders[5].(*array.Uint8Builder).AppendValues([]uint8{1, 0, 255}, validity)
	builders[6].(*array.Uint16Builder).AppendValues([]uint16{1, 0, 65_535}, validity)
	builders[7].(*array.Uint32Builder).AppendValues([]uint32{1, 0, 4_000_000_000}, validity)
	builders[8].(*array.Uint64Builder).AppendValues([]uint64{1, 0, 18_000_000_000_000_000_000}, validity)
	builders[9].(*array.Float32Builder).AppendValues([]float32{1.25, 0, -2.5}, validity)
	builders[10].(*array.Float64Builder).AppendValues([]float64{1.0 / 3.0, 0, -9.5}, validity)
	firstDecimal, err := decimal128.FromString("123456789012.3456", decimalType.Precision, decimalType.Scale)
	if err != nil {
		t.Fatal(err)
	}
	secondDecimal, err := decimal128.FromString("-42.5000", decimalType.Precision, decimalType.Scale)
	if err != nil {
		t.Fatal(err)
	}
	builders[11].(*array.Decimal128Builder).AppendValues([]decimal128.Num{firstDecimal, {}, secondDecimal}, validity)
	builders[12].(*array.Date32Builder).AppendValues([]arrow.Date32{19_723, 0, 19_724}, validity)
	builders[13].(*array.Date64Builder).AppendValues([]arrow.Date64{1_704_067_200_000, 0, 1_704_153_600_000}, validity)
	builders[14].(*array.TimestampBuilder).AppendValues([]arrow.Timestamp{1_704_067_200_123_456, 0, 1_704_153_600_654_321}, validity)
	builders[15].(*array.TimestampBuilder).AppendValues([]arrow.Timestamp{1_704_067_200_123_456_789, 0, 1_704_153_600_987_654_321}, validity)
	builders[16].(*array.StringBuilder).AppendValues([]string{"alpha", "must-not-survive", ""}, validity)
	builders[17].(*array.BinaryBuilder).AppendValues([][]byte{{0x00, 0xff, 0x7f}, {0x99}, {}}, validity)
	dictionary := builders[18].(*array.BinaryDictionaryBuilder)
	if err := dictionary.AppendString("new"); err != nil {
		t.Fatal(err)
	}
	dictionary.AppendNull()
	if err := dictionary.AppendString("complete"); err != nil {
		t.Fatal(err)
	}

	columns := make([]arrow.Array, len(builders))
	for index, builder := range builders {
		columns[index] = builder.NewArray()
		builder.Release()
	}
	record := array.NewRecordBatch(schema, columns, 3)
	for _, column := range columns {
		column.Release()
	}
	return schema, record
}

func dashboardNativeArrowContractSchema(schema *arrow.Schema, logicalTypes map[string]string, queryID, snapshot string) *arrow.Schema {
	metadata := publicDashboardNativeArrowMetadata(schema.Metadata(), nil, map[string]string{
		"leapview.arrow_contract":               dashboardNativeArrowContract,
		"leapview.query_id":                     queryID,
		"leapview.serving_snapshot":             snapshot,
		"leapview.visualization_schema_version": dashboardNativeArrowSchemaVersion,
		"leapview.visualization_spec_revision":  dashboardNativeArrowSpecRevision,
		"leapview.visualization_data_revision":  dashboardNativeArrowDataRevision,
	})
	fields := schema.Fields()
	for index := range fields {
		authoritative := map[string]string{}
		if logicalType := logicalTypes[fields[index].Name]; logicalType != "" {
			authoritative["leapview.logical_type"] = logicalType
		}
		fields[index].Metadata = publicDashboardNativeArrowMetadata(fields[index].Metadata, dashboardNativeArrowFieldProducerMetadataAllowlist, authoritative)
	}
	return arrow.NewSchema(fields, &metadata)
}

func publicDashboardNativeArrowMetadata(upstream arrow.Metadata, producerAllowlist map[string]struct{}, authoritative map[string]string) arrow.Metadata {
	values := make(map[string]string, upstream.Len()+len(authoritative))
	for key, value := range upstream.ToMap() {
		if _, allowed := producerAllowlist[key]; allowed {
			values[key] = value
		}
	}
	for key, value := range authoritative {
		values[key] = value
	}
	return arrow.MetadataFrom(values)
}

func validateDashboardNativeArrowMetadata(metadata arrow.Metadata, allowlist map[string]struct{}) error {
	for _, key := range metadata.Keys() {
		if _, allowed := allowlist[key]; !allowed {
			return fmt.Errorf("metadata key %q is not response-safe", key)
		}
	}
	return nil
}

func assertDashboardNativeArrowHeaders(t testing.TB, response *stdhttp.Response) {
	t.Helper()
	want := map[string]string{
		"Content-Type":              "application/vnd.apache.arrow.stream",
		"Cache-Control":             "no-store",
		"X-Query-ID":                dashboardNativeArrowQueryID,
		"X-Serving-Snapshot":        dashboardNativeArrowSnapshot,
		"X-LeapView-Arrow-Contract": dashboardNativeArrowContract,
		"Trailer":                   "X-Next-Cursor",
	}
	for header, expected := range want {
		if got := response.Header.Get(header); got != expected {
			t.Fatalf("%s = %q, want %q", header, got, expected)
		}
	}
}

func assertDashboardNativeArrowSchema(t testing.TB, schema *arrow.Schema) {
	t.Helper()
	if schema == nil {
		t.Fatal("native Arrow schema is nil")
	}
	gotNames := make([]string, schema.NumFields())
	for index, field := range schema.Fields() {
		gotNames[index] = field.Name
	}
	if got := gotNames; !equalStrings(got, dashboardNativeArrowFieldNames) {
		t.Fatalf("field order/names = %#v, want %#v", got, dashboardNativeArrowFieldNames)
	}
	wantTypes := []arrow.Type{
		arrow.BOOL, arrow.INT8, arrow.INT16, arrow.INT32, arrow.INT64,
		arrow.UINT8, arrow.UINT16, arrow.UINT32, arrow.UINT64,
		arrow.FLOAT32, arrow.FLOAT64, arrow.DECIMAL128, arrow.DATE32,
		arrow.DATE64, arrow.TIMESTAMP, arrow.TIMESTAMP, arrow.STRING, arrow.BINARY, arrow.DICTIONARY,
	}
	for index, want := range wantTypes {
		field := schema.Field(index)
		wantNullable := index != 4
		if field.Type.ID() != want || field.Nullable != wantNullable {
			t.Fatalf("field %q = (%s, nullable=%v), want type %s and nullable=%v", field.Name, field.Type, field.Nullable, want, wantNullable)
		}
		if err := validateDashboardNativeArrowMetadata(field.Metadata, dashboardNativeArrowFieldMetadataAllowlist); err != nil {
			t.Fatalf("field %q metadata: %v", field.Name, err)
		}
		wantMetadataCount := 0
		if wantLogicalType := dashboardNativeArrowLogicalTypes[field.Name]; wantLogicalType != "" {
			wantMetadataCount++
			if got, ok := field.Metadata.GetValue("leapview.logical_type"); !ok || got != wantLogicalType {
				t.Fatalf("field %q logical metadata = (%q, %v), want %q", field.Name, got, ok, wantLogicalType)
			}
		}
		if field.Name == "customer_alias" {
			wantMetadataCount++
			if got, ok := field.Metadata.GetValue("display.label"); !ok || got != "Customer" {
				t.Fatalf("alias field public metadata = (%q, %v)", got, ok)
			}
		}
		if field.Metadata.Len() != wantMetadataCount {
			t.Fatalf("field %q metadata count = %d, want %d", field.Name, field.Metadata.Len(), wantMetadataCount)
		}
	}
	decimalType, ok := schema.Field(11).Type.(*arrow.Decimal128Type)
	if !ok || decimalType.Precision != 20 || decimalType.Scale != 4 {
		t.Fatalf("decimal type = %#v", schema.Field(11).Type)
	}
	timestampType, ok := schema.Field(14).Type.(*arrow.TimestampType)
	if !ok || timestampType.Unit != arrow.Microsecond || timestampType.TimeZone != "UTC" {
		t.Fatalf("timezone-aware timestamp type = %#v", schema.Field(14).Type)
	}
	timestampLocalType, ok := schema.Field(15).Type.(*arrow.TimestampType)
	if !ok || timestampLocalType.Unit != arrow.Nanosecond || timestampLocalType.TimeZone != "" {
		t.Fatalf("timezone-neutral timestamp type = %#v", schema.Field(15).Type)
	}
	dictionaryType, ok := schema.Field(18).Type.(*arrow.DictionaryType)
	if !ok || dictionaryType.IndexType.ID() != arrow.INT8 || dictionaryType.ValueType.ID() != arrow.STRING || dictionaryType.Ordered {
		t.Fatalf("dictionary type = %#v", schema.Field(18).Type)
	}
	metadata := schema.Metadata()
	if err := validateDashboardNativeArrowMetadata(metadata, dashboardNativeArrowSchemaMetadataAllowlist); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"leapview.arrow_contract":               dashboardNativeArrowContract,
		"leapview.query_id":                     dashboardNativeArrowQueryID,
		"leapview.serving_snapshot":             dashboardNativeArrowSnapshot,
		"leapview.visualization_schema_version": dashboardNativeArrowSchemaVersion,
		"leapview.visualization_spec_revision":  dashboardNativeArrowSpecRevision,
		"leapview.visualization_data_revision":  dashboardNativeArrowDataRevision,
	} {
		if got, ok := metadata.GetValue(key); !ok || got != want {
			t.Fatalf("schema metadata %q = (%q, %v), want %q", key, got, ok, want)
		}
	}
	if metadata.Len() != len(dashboardNativeArrowSchemaMetadataAllowlist) {
		t.Fatalf("schema metadata count = %d, want %d", metadata.Len(), len(dashboardNativeArrowSchemaMetadataAllowlist))
	}
}

func assertDashboardNativeArrowValues(t testing.TB, record arrow.RecordBatch) {
	t.Helper()
	if record.NumRows() != 3 || record.NumCols() != int64(len(dashboardNativeArrowFieldNames)) {
		t.Fatalf("record shape = %dx%d", record.NumRows(), record.NumCols())
	}
	for index := 0; index < int(record.NumCols()); index++ {
		column := record.Column(index)
		if index == 4 {
			if column.IsNull(0) || column.IsNull(1) || column.IsNull(2) || column.NullN() != 0 {
				t.Fatalf("non-nullable field %q null positions = [%v %v %v] count=%d", record.ColumnName(index), column.IsNull(0), column.IsNull(1), column.IsNull(2), column.NullN())
			}
			continue
		}
		if column.IsNull(0) || !column.IsNull(1) || column.IsNull(2) || column.NullN() != 1 {
			t.Fatalf("field %q null positions = [%v %v %v] count=%d", record.ColumnName(index), column.IsNull(0), column.IsNull(1), column.IsNull(2), column.NullN())
		}
	}
	if values := record.Column(0).(*array.Boolean); !values.Value(0) || values.Value(2) {
		t.Fatalf("boolean values = %v, %v", values.Value(0), values.Value(2))
	}
	if values := record.Column(1).(*array.Int8); values.Value(0) != -8 || values.Value(2) != 7 {
		t.Fatalf("int8 values = %d, %d", values.Value(0), values.Value(2))
	}
	if values := record.Column(2).(*array.Int16); values.Value(0) != -32000 || values.Value(2) != 32000 {
		t.Fatalf("int16 values = %d, %d", values.Value(0), values.Value(2))
	}
	if values := record.Column(3).(*array.Int32); values.Value(0) != -2_000_000_000 || values.Value(2) != 2_000_000_000 {
		t.Fatalf("int32 values = %d, %d", values.Value(0), values.Value(2))
	}
	if values := record.Column(4).(*array.Int64); values.Value(0) != -9_007_199_254_740_993 || values.Value(1) != 0 || values.Value(2) != 9_007_199_254_740_993 {
		t.Fatalf("int64 values = %d, %d, %d", values.Value(0), values.Value(1), values.Value(2))
	}
	if values := record.Column(5).(*array.Uint8); values.Value(0) != 1 || values.Value(2) != 255 {
		t.Fatalf("uint8 values = %d, %d", values.Value(0), values.Value(2))
	}
	if values := record.Column(6).(*array.Uint16); values.Value(0) != 1 || values.Value(2) != 65_535 {
		t.Fatalf("uint16 values = %d, %d", values.Value(0), values.Value(2))
	}
	if values := record.Column(7).(*array.Uint32); values.Value(0) != 1 || values.Value(2) != 4_000_000_000 {
		t.Fatalf("uint32 values = %d, %d", values.Value(0), values.Value(2))
	}
	if values := record.Column(8).(*array.Uint64); values.Value(0) != 1 || values.Value(2) != 18_000_000_000_000_000_000 {
		t.Fatalf("uint64 values = %d, %d", values.Value(0), values.Value(2))
	}
	if values := record.Column(9).(*array.Float32); values.Value(0) != 1.25 || values.Value(2) != -2.5 {
		t.Fatalf("float32 values = %g, %g", values.Value(0), values.Value(2))
	}
	if values := record.Column(10).(*array.Float64); values.Value(0) != 1.0/3.0 || values.Value(2) != -9.5 {
		t.Fatalf("float64 values = %g, %g", values.Value(0), values.Value(2))
	}
	if values := record.Column(11).(*array.Decimal128); values.Value(0).ToString(4) != "123456789012.3456" || values.Value(2).ToString(4) != "-42.5000" {
		t.Fatalf("decimal values = %s, %s", values.Value(0).ToString(4), values.Value(2).ToString(4))
	}
	if values := record.Column(12).(*array.Date32); values.Value(0) != 19_723 || values.Value(2) != 19_724 {
		t.Fatalf("date32 values = %d, %d", values.Value(0), values.Value(2))
	}
	if values := record.Column(13).(*array.Date64); values.Value(0) != 1_704_067_200_000 || values.Value(2) != 1_704_153_600_000 {
		t.Fatalf("date64 values = %d, %d", values.Value(0), values.Value(2))
	}
	if values := record.Column(14).(*array.Timestamp); values.Value(0) != 1_704_067_200_123_456 || values.Value(2) != 1_704_153_600_654_321 || !values.Value(0).ToTime(arrow.Microsecond).Equal(time.Unix(1_704_067_200, 123_456_000).UTC()) {
		t.Fatalf("timezone-aware timestamp values = %d, %d", values.Value(0), values.Value(2))
	}
	if values := record.Column(15).(*array.Timestamp); values.Value(0) != 1_704_067_200_123_456_789 || values.Value(2) != 1_704_153_600_987_654_321 || !values.Value(0).ToTime(arrow.Nanosecond).Equal(time.Unix(1_704_067_200, 123_456_789).UTC()) {
		t.Fatalf("timezone-neutral timestamp values = %d, %d", values.Value(0), values.Value(2))
	}
	if values := record.Column(16).(*array.String); values.Value(0) != "alpha" || values.Value(2) != "" {
		t.Fatalf("string values = %q, %q", values.Value(0), values.Value(2))
	}
	if values := record.Column(17).(*array.Binary); !bytes.Equal(values.Value(0), []byte{0x00, 0xff, 0x7f}) || !bytes.Equal(values.Value(2), []byte{}) {
		t.Fatalf("binary values = %x, %x", values.Value(0), values.Value(2))
	}
	dictionary := record.Column(18).(*array.Dictionary)
	if dictionary.GetValueIndex(0) != 0 || dictionary.GetValueIndex(2) != 1 || dictionary.ValueStr(0) != "new" || dictionary.ValueStr(2) != "complete" {
		t.Fatalf("dictionary mapping = indices [%d %d], values [%q %q]", dictionary.GetValueIndex(0), dictionary.GetValueIndex(2), dictionary.ValueStr(0), dictionary.ValueStr(2))
	}
	dictionaryValues, ok := dictionary.Dictionary().(*array.String)
	if !ok || dictionaryValues.Len() != 2 || dictionaryValues.Value(0) != "new" || dictionaryValues.Value(1) != "complete" {
		t.Fatalf("dictionary values = %v", dictionary.Dictionary())
	}
}

func consumeDashboardNativeArrow(body io.Reader) error {
	reader, err := ipc.NewReader(body)
	if err != nil {
		return err
	}
	defer reader.Release()
	for reader.Next() {
	}
	return reader.Err()
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
