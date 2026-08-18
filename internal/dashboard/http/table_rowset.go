package http

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	stdhttp "net/http"
	"strconv"
	"strings"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/flidai/leapview/internal/dashboard/api"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

const dashboardArrowMediaType = "application/vnd.apache.arrow.stream"

func dashboardVisualizationRowset(envelope visualizationir.VisualizationEnvelope, block string, start, limit int, scope, snapshot string) (api.DashboardTableQueryResponse, error) {
	state, ok := envelope.DataState.Value.(*visualizationir.WindowedVisualizationDataState)
	if !ok {
		return api.DashboardTableQueryResponse{}, fmt.Errorf("visualization %q is not windowed", envelope.VisualID)
	}
	window, ok := state.Blocks[block]
	if !ok {
		return api.DashboardTableQueryResponse{}, fmt.Errorf("visualization %q omitted requested block %q", envelope.VisualID, block)
	}
	rows := window.Rows
	if len(rows) > limit {
		rows = rows[:limit]
	}
	columns := make([]api.QueryColumn, len(state.Schema.Fields))
	encodedRows := make([][]string, 0, len(rows))
	for index, field := range state.Schema.Fields {
		columns[index] = api.QueryColumn{Name: field.ID, Type: visualizationFieldType(field.DataType), Nullable: field.Nullable}
	}
	for _, row := range rows {
		values := make([]string, len(state.Schema.Fields))
		for index := range state.Schema.Fields {
			if index < len(row) {
				values[index] = dashboardCellString(row[index])
			}
		}
		encodedRows = append(encodedRows, values)
	}
	next := ""
	if int64(start+len(encodedRows)) < state.AvailableRows {
		next = encodeIndexCursor(start+len(encodedRows), scope, snapshot)
	}
	base, err := visualizationir.SpecificationBase(envelope.Spec)
	if err != nil {
		return api.DashboardTableQueryResponse{}, err
	}
	queryDigest := sha256String(scope)
	return api.DashboardTableQueryResponse{
		ID: envelope.VisualID, Type: base.Kind,
		QueryID: "query_" + queryDigest[:24], ServingSnapshot: snapshot, Title: base.Title,
		Columns: columns, Rows: encodedRows, AvailableRows: int(state.AvailableRows), Page: api.PageInfo{NextCursor: next},
	}, nil
}

func visualizationFieldType(value visualizationir.VisualizationDataType) string {
	switch value {
	case visualizationir.VisualizationDataTypeBoolean:
		return "boolean"
	case visualizationir.VisualizationDataTypeInteger:
		return "int64"
	case visualizationir.VisualizationDataTypeDecimal:
		return "decimal"
	case visualizationir.VisualizationDataTypeFloat:
		return "float64"
	case visualizationir.VisualizationDataTypeDate, visualizationir.VisualizationDataTypeTemporal:
		return "timestamp"
	default:
		return "string"
	}
}

func writeDashboardTableRowset(w stdhttp.ResponseWriter, r *stdhttp.Request, response api.DashboardTableQueryResponse, envelope visualizationir.VisualizationEnvelope) {
	if requestID := strings.TrimSpace(r.Header.Get("X-Request-ID")); requestID != "" {
		response.QueryID = requestID
	}
	w.Header().Set("Cache-Control", "no-store")
	if !acceptsDashboardMediaType(r.Header.Get("Accept"), dashboardArrowMediaType) {
		writeJSON(w, stdhttp.StatusOK, response)
		return
	}
	payload, err := encodeDashboardTableArrow(response)
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", dashboardArrowMediaType)
	w.Header().Set("X-Query-ID", response.QueryID)
	w.Header().Set("X-Serving-Snapshot", response.ServingSnapshot)
	w.Header().Set("X-Visualization-Schema-Version", strconv.FormatInt(int64(envelope.SchemaVersion), 10))
	w.Header().Set("X-Visualization-Spec-Revision", envelope.SpecRevision)
	w.Header().Set("X-Visualization-Data-Revision", strconv.FormatInt(envelope.DataRevision, 10))
	if response.Page.NextCursor != "" {
		w.Header().Set("X-Next-Cursor", response.Page.NextCursor)
	}
	w.WriteHeader(stdhttp.StatusOK)
	_, _ = w.Write(payload)
}

func acceptsDashboardMediaType(header, mediaType string) bool {
	for _, item := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(strings.SplitN(item, ";", 2)[0]), mediaType) {
			return true
		}
	}
	return false
}

func encodeDashboardTableArrow(response api.DashboardTableQueryResponse) ([]byte, error) {
	metadata := arrow.NewMetadata(
		[]string{"leapview.query_id", "leapview.serving_snapshot", "leapview.next_cursor"},
		[]string{response.QueryID, response.ServingSnapshot, response.Page.NextCursor},
	)
	fields := make([]arrow.Field, len(response.Columns))
	for index, column := range response.Columns {
		fields[index] = arrow.Field{Name: column.Name, Type: arrow.BinaryTypes.String, Nullable: column.Nullable, Metadata: arrow.NewMetadata([]string{"leapview.logical_type"}, []string{column.Type})}
	}
	schema := arrow.NewSchema(fields, &metadata)
	allocator := memory.NewGoAllocator()
	arrays := make([]arrow.Array, len(fields))
	for columnIndex := range fields {
		builder := array.NewStringBuilder(allocator)
		for _, row := range response.Rows {
			if columnIndex >= len(row) {
				builder.AppendNull()
				continue
			}
			builder.Append(row[columnIndex])
		}
		arrays[columnIndex] = builder.NewArray()
		builder.Release()
	}
	defer func() {
		for _, values := range arrays {
			values.Release()
		}
	}()
	record := array.NewRecord(schema, arrays, int64(len(response.Rows)))
	defer record.Release()
	var output bytes.Buffer
	writer := ipc.NewWriter(&output, ipc.WithSchema(schema))
	if err := writer.Write(record); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func dashboardCellString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.FormatInt(int64(typed), 10)
	case int8:
		return strconv.FormatInt(int64(typed), 10)
	case int16:
		return strconv.FormatInt(int64(typed), 10)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint:
		return strconv.FormatUint(uint64(typed), 10)
	case uint8:
		return strconv.FormatUint(uint64(typed), 10)
	case uint16:
		return strconv.FormatUint(uint64(typed), 10)
	case uint32:
		return strconv.FormatUint(uint64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case float32:
		return strconv.FormatFloat(float64(typed), 'g', -1, 32)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano)
	default:
		encoded, err := json.Marshal(typed)
		if err == nil && (len(encoded) == 0 || encoded[0] == '{' || encoded[0] == '[') {
			return string(encoded)
		}
		return fmt.Sprint(typed)
	}
}

func sha256String(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", digest[:])
}
