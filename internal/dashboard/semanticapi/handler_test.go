package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
	"github.com/flidai/leapview/internal/dashboard/api"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/workload"
)

func TestWorkloadRejectionsMapToStableOverloadProblems(t *testing.T) {
	for _, test := range []struct {
		reason workload.RejectionReason
		status int
		code   string
		retry  string
	}{
		{reason: workload.ClassQueueFull, status: http.StatusServiceUnavailable, code: "WORKLOAD_OVERLOADED", retry: "1"},
		{reason: workload.QueueTimeout, status: http.StatusGatewayTimeout, code: "WORKLOAD_QUEUE_TIMEOUT"},
	} {
		recorder := httptest.NewRecorder()
		err := &workload.Rejection{Reason: test.reason, Class: workload.Interactive, PrincipalID: "principal_1", Operation: "query"}
		writeJSONError(recorder, err, http.StatusBadRequest)
		if recorder.Code != test.status || recorder.Header().Get("Retry-After") != test.retry {
			t.Fatalf("reason %s status=%d retry=%q", test.reason, recorder.Code, recorder.Header().Get("Retry-After"))
		}
		var response map[string]any
		if decodeErr := json.Unmarshal(recorder.Body.Bytes(), &response); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		details := response["details"].(map[string]any)
		if details["problemCode"] != test.code {
			t.Fatalf("reason %s details=%#v", test.reason, details)
		}
	}
}

func TestAnalyticalLimitsMapToStableProblems(t *testing.T) {
	for _, test := range []struct {
		err         error
		status      int
		code, retry string
	}{
		{err: &dataquery.ResultLimitError{Reason: dataquery.ResultRows, Limit: 10, Observed: 11}, status: http.StatusUnprocessableEntity, code: "QUERY_RESULT_ROW_LIMIT"},
		{err: &dataquery.ResultLimitError{Reason: dataquery.ResultBytes, Limit: 10, Observed: 11}, status: http.StatusUnprocessableEntity, code: "QUERY_RESULT_BYTE_LIMIT"},
		{err: &analyticsresource.ResourceExhaustedError{Reason: analyticsresource.ResourceMemory, Err: errors.New("oom")}, status: http.StatusServiceUnavailable, code: "ANALYTICS_RESOURCE_EXHAUSTED", retry: "1"},
	} {
		recorder := httptest.NewRecorder()
		writeJSONError(recorder, test.err, http.StatusInternalServerError)
		if recorder.Code != test.status || recorder.Header().Get("Retry-After") != test.retry {
			t.Fatalf("error=%T status=%d retry=%q", test.err, recorder.Code, recorder.Header().Get("Retry-After"))
		}
		var response map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if got := response["details"].(map[string]any)["problemCode"]; got != test.code {
			t.Fatalf("code=%v", got)
		}
	}
}

func TestSemanticQueryResponseUsesTypedColumnsAndPrecisionSafePositionalRows(t *testing.T) {
	response := semanticQueryResponse([]string{"order_id", "amount", "active"}, reportdef.QueryRows{{
		"order_id": int64(9007199254740993), "amount": 12.5, "active": true,
	}}, 100, 0, "query-1", "state-1")
	if len(response.Columns) != 3 || response.Columns[0].Type != "int64" || response.Columns[1].Type != "float64" || response.Columns[2].Type != "boolean" {
		t.Fatalf("columns = %#v", response.Columns)
	}
	if len(response.Rows) != 1 || response.Rows[0][0] != "9007199254740993" || response.Rows[0][1] != "12.5" || response.Rows[0][2] != "true" {
		t.Fatalf("rows = %#v", response.Rows)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if string(encoded) == "" || response.QueryID != "query-1" || response.ServingSnapshot != "state-1" {
		t.Fatalf("response = %s %#v", encoded, response)
	}
}

func TestSemanticQueryResponsePreservesNullAndEmptyStringAsDistinctCells(t *testing.T) {
	response := semanticQueryResponse([]string{"nullable_value", "empty_value"}, reportdef.QueryRows{{
		"nullable_value": nil,
		"empty_value":    "",
	}}, 100, 0, "query-1", "state-1")
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if !strings.Contains(string(encoded), `"rows":[[null,""]]`) {
		t.Fatalf("null and empty string collapsed in response: %s", encoded)
	}
}

func TestSemanticQueryColumnsUseModelMetadataInsteadOfPageValues(t *testing.T) {
	nullable := false
	model := &semanticmodel.Model{
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Schema: semanticmodel.TableSchema{Columns: []semanticmodel.ColumnSchema{{Name: "created_at", Nullable: &nullable}}},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"created_at": {Label: "Created at", Type: "timestamp"},
				},
			},
		},
		Measures: map[string]semanticmodel.MetricMeasure{
			"order_count": {
				Label: "Orders", Fact: "orders", Aggregation: "count", Empty: "zero",
				Unit: "orders", Format: "integer",
			},
		},
		Metrics: map[string]semanticmodel.Metric{
			"return_rate": {Label: "Return rate", Unit: "percent", Format: "percent_1"},
		},
	}
	columns := semanticQueryColumns(
		"commerce", model,
		[]api.QueryColumn{
			{Name: "created", Type: "string", Nullable: true},
			{Name: "order_count", Type: "string", Nullable: true},
			{Name: "return_rate", Type: "string", Nullable: true},
		},
		[]reportdef.QueryField{{Field: "orders.created_at", Alias: "created"}},
		[]reportdef.QueryField{{Field: "order_count"}, {Field: "return_rate"}},
		nil,
	)
	if columns[0].Type != "timestamp" || columns[0].Nullable ||
		columns[0].FieldRef == nil || columns[0].FieldRef.ID != "commerce.orders.created_at" {
		t.Fatalf("dimension column = %#v", columns[0])
	}
	if columns[1].Type != "int64" || columns[1].Nullable || columns[1].Kind != "measure" ||
		columns[1].Unit != "orders" || columns[1].Format != "integer" {
		t.Fatalf("measure column = %#v", columns[1])
	}
	if columns[2].Type != "decimal" || !columns[2].Nullable || columns[2].Kind != "metric" ||
		columns[2].Unit != "percent" || columns[2].Format != "percent_1" {
		t.Fatalf("metric column = %#v", columns[2])
	}
}

func TestIndexCursorIsSignedBoundAndExpiring(t *testing.T) {
	token := encodeIndexCursor(25, "query-a", "snapshot-a")
	if offset, err := decodeIndexCursor(token, "query-a", "snapshot-a"); err != nil || offset != 25 {
		t.Fatalf("decode cursor offset=%d err=%v", offset, err)
	}
	if _, err := decodeIndexCursor(token+"x", "query-a", "snapshot-a"); err == nil {
		t.Fatal("tampered cursor was accepted")
	}
	if _, err := decodeIndexCursor(token, "query-b", "snapshot-a"); err == nil {
		t.Fatal("cursor was accepted for another request")
	}
	if _, err := decodeIndexCursor(token, "query-a", "snapshot-b"); !errors.Is(err, errCursorSnapshotUnavailable) {
		t.Fatalf("snapshot mismatch error = %v", err)
	}
	expired := encodeIndexCursorValue(indexCursor{Offset: 25, Scope: "query-a", Snapshot: "snapshot-a", Expires: time.Now().Add(-time.Second).Unix()})
	if _, err := decodeIndexCursor(expired, "query-a", "snapshot-a"); err == nil {
		t.Fatal("expired cursor was accepted")
	}
}

func TestRequestCursorScopeIgnoresPageTokenButBindsNormalizedRequest(t *testing.T) {
	first := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_1/semantic-models/commerce/query?limit=100&pageToken=one", nil)
	second := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_1/semantic-models/commerce/query?pageToken=two&limit=100", nil)
	if requestCursorScope(first, map[string]any{"field": "revenue"}) != requestCursorScope(second, map[string]any{"field": "revenue"}) {
		t.Fatal("page token changed normalized request scope")
	}
	changed := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_1/semantic-models/commerce/query?limit=200", nil)
	if requestCursorScope(first, map[string]any{"field": "revenue"}) == requestCursorScope(changed, map[string]any{"field": "revenue"}) {
		t.Fatal("query shape did not change request scope")
	}
}

func TestSemanticArrowSinkPreservesNativeTypesNullsAndMetadata(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer allocator.AssertSize(t, 0)
	ids := array.NewInt64Builder(allocator)
	amounts := array.NewFloat64Builder(allocator)
	ids.AppendValues([]int64{9007199254740993, 2}, nil)
	amounts.Append(12.5)
	amounts.AppendNull()
	idArray, amountArray := ids.NewArray(), amounts.NewArray()
	ids.Release()
	amounts.Release()
	defer idArray.Release()
	defer amountArray.Release()
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "id", Type: arrow.PrimitiveTypes.Int64},
		{Name: "amount", Type: arrow.PrimitiveTypes.Float64, Nullable: true},
	}, nil)
	record := array.NewRecord(schema, []arrow.Array{idArray, amountArray}, 2)
	defer record.Release()

	recorder := httptest.NewRecorder()
	sink := newSemanticArrowSink(recorder, "query-a", "state-a", 10)
	if err := sink.WriteSchema(schema); err != nil {
		t.Fatalf("WriteSchema: %v", err)
	}
	if err := sink.WriteRecord(record); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reader, err := ipc.NewReader(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		t.Fatalf("open Arrow: %v", err)
	}
	defer reader.Release()
	metadata := reader.Schema().Metadata()
	if value, ok := metadata.GetValue("leapview.query_id"); !ok || value != "query-a" {
		t.Fatalf("query metadata = %q, %v", value, ok)
	}
	if value, ok := metadata.GetValue("leapview.arrow_contract"); !ok || value != "native-v1" {
		t.Fatalf("contract metadata = %q, %v", value, ok)
	}
	if !reader.Next() {
		t.Fatalf("read Arrow record: %v", reader.Err())
	}
	got := reader.Record()
	if got.NumRows() != 2 || got.NumCols() != 2 {
		t.Fatalf("record shape = %dx%d", got.NumRows(), got.NumCols())
	}
	if got.Schema().Field(0).Type.ID() != arrow.INT64 || got.Schema().Field(1).Type.ID() != arrow.FLOAT64 {
		t.Fatalf("physical types = %s, %s", got.Schema().Field(0).Type, got.Schema().Field(1).Type)
	}
	if got.Column(0).(*array.Int64).Value(0) != int64(9007199254740993) || !got.Column(1).IsNull(1) {
		t.Fatal("native value/null were not preserved")
	}
}

func TestWriteSemanticArrowResponseUsesNativeExecutorAndStreamsPaginationProbe(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/query", nil)
	metrics := nativeArrowTestMetrics{t: t}
	writeSemanticArrowResponse(
		recorder,
		request,
		metrics,
		dataquery.Query{ProjectID: projectgraph.ResourceID("project_1"), ModelID: "sales", Kind: dataquery.KindSemanticAggregate, Limit: 3},
		2,
		0,
		"query-a",
		"snapshot-a",
		"scope-a",
	)
	response := recorder.Result()
	defer response.Body.Close()
	if response.Header.Get("X-LeapView-Arrow-Contract") != "native-v1" {
		t.Fatalf("contract header = %q", response.Header.Get("X-LeapView-Arrow-Contract"))
	}
	if response.Header.Get("Trailer") != "X-Next-Cursor" {
		t.Fatalf("declared response trailers = %q", response.Header.Get("Trailer"))
	}
	reader, err := ipc.NewReader(response.Body)
	if err != nil {
		t.Fatalf("open streamed Arrow: %v", err)
	}
	defer reader.Release()
	var rows int64
	for reader.Next() {
		rows += reader.Record().NumRows()
	}
	if err := reader.Err(); err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if rows != 2 {
		t.Fatalf("streamed rows = %d, want page limit 2", rows)
	}
	if response.Trailer.Get("X-Next-Cursor") == "" {
		t.Fatal("pagination probe did not produce next-cursor trailer")
	}
}

type nativeArrowTestMetrics struct {
	Metrics
	t *testing.T
}

func (m nativeArrowTestMetrics) ExecuteDataQueryArrow(_ context.Context, request dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error) {
	m.t.Helper()
	if request.Limit != 3 {
		m.t.Fatalf("physical limit = %d, want page limit plus probe", request.Limit)
	}
	allocator := memory.NewGoAllocator()
	builder := array.NewInt64Builder(allocator)
	builder.AppendValues([]int64{1, 2, 3}, nil)
	values := builder.NewArray()
	builder.Release()
	defer values.Release()
	schema := arrow.NewSchema([]arrow.Field{{Name: "id", Type: arrow.PrimitiveTypes.Int64}}, nil)
	record := array.NewRecord(schema, []arrow.Array{values}, 3)
	defer record.Release()
	if err := sink.WriteSchema(schema); err != nil {
		return dataquery.Result{}, err
	}
	if err := sink.WriteRecord(record); err != nil {
		return dataquery.Result{}, err
	}
	return dataquery.Result{RowsReturned: 3}, nil
}

func TestSemanticRelationshipDTOParsesTypedEndpoints(t *testing.T) {
	got, err := semanticRelationshipDTO(semanticmodel.Relationship{
		ID:          "orders_customers",
		From:        "orders.customer_id",
		To:          "customers.customer_id",
		Cardinality: "many_to_one",
	})
	if err != nil {
		t.Fatalf("semanticRelationshipDTO: %v", err)
	}
	if got.ID != "orders_customers" || got.FromDataset != "orders" || got.FromField != "customer_id" || got.ToDataset != "customers" || got.ToField != "customer_id" || got.Cardinality != "many_to_one" || !got.Active {
		t.Fatalf("unexpected relationship: %#v", got)
	}
}

func TestSemanticRelationshipDTORejectsMalformedEndpoint(t *testing.T) {
	if _, err := semanticRelationshipDTO(semanticmodel.Relationship{ID: "broken", From: "orders", To: "customers.id"}); err == nil {
		t.Fatal("expected malformed endpoint error")
	}
}
