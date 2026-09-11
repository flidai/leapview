package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	nethttp "net/http"
	"sort"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
	"github.com/flidai/leapview/internal/dashboard/api"
	httpmodel "github.com/flidai/leapview/internal/platform/http/model"
	"github.com/flidai/leapview/internal/platform/http/pagination"
	httptransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/internal/workload"
)

func (h Handler) requestQueryMetadata(r *nethttp.Request, surface, operation, objectType, objectID string) dataquery.Metadata {
	if surface == dataquery.SurfaceAPI && r.Header.Get("X-LeapView-Client") == dataquery.SurfaceCLI {
		surface = dataquery.SurfaceCLI
	}
	metadata := dataquery.Metadata{
		Surface:       surface,
		Operation:     requestQueryOperation(operation, objectType),
		ObjectType:    objectType,
		ObjectID:      objectID,
		RequestID:     r.Header.Get("X-Request-ID"),
		CorrelationID: r.Header.Get("X-Correlation-ID"),
	}
	if h.CurrentPrincipalID != nil {
		metadata.PrincipalID = h.CurrentPrincipalID(r)
	}
	existing := dataquery.MetadataFromContext(r.Context())
	if existing.Surface != "" {
		metadata.Surface = existing.Surface
	}
	if existing.Operation != "" {
		metadata.Operation = existing.Operation
	}
	if existing.PrincipalID != "" {
		metadata.PrincipalID = existing.PrincipalID
	}
	if existing.RequestID != "" {
		metadata.RequestID = existing.RequestID
	}
	if existing.ObjectType != "" {
		metadata.ObjectType = existing.ObjectType
	}
	if existing.ObjectID != "" {
		metadata.ObjectID = existing.ObjectID
	}
	if existing.CorrelationID != "" {
		metadata.CorrelationID = existing.CorrelationID
	}
	return metadata
}

func requestQueryOperation(operation, objectType string) string {
	if operation != dataquery.OperationAPIQuery {
		return operation
	}
	switch objectType {
	case "dashboard_page", "dashboard_visual", "dashboard_filter":
		return ""
	default:
		return operation
	}
}

func sortedMapKeys[T any](items map[string]T) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func pageSliceForRequest[T any](w nethttp.ResponseWriter, r *nethttp.Request, items []T) ([]T, string, bool) {
	limit, ok := apiLimitForRequest(w, r)
	if !ok {
		return nil, "", false
	}
	snapshot, snapshotErr := servingSnapshotForRequest(r)
	if snapshotErr != nil {
		writeJSONError(w, snapshotErr, nethttp.StatusServiceUnavailable)
		return nil, "", false
	}
	scope := requestCursorScope(r, nil)
	lastKey, err := decodeListKeysetCursor(r.URL.Query().Get("pageToken"), scope, snapshot)
	if err != nil {
		writeJSONError(w, err, statusForCursorError(err))
		return nil, "", false
	}
	start := 0
	if lastKey != "" {
		start = -1
		for index, item := range items {
			if listPageItemKey(item) == lastKey {
				start = index + 1
				break
			}
		}
		if start < 0 {
			writeJSONError(w, errCursorSnapshotUnavailable, nethttp.StatusConflict)
			return nil, "", false
		}
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	nextCursor := ""
	if end < len(items) {
		nextCursor = encodeListKeysetCursor(listPageItemKey(items[end-1]), scope, snapshot)
	}
	return append(make([]T, 0, end-start), items[start:end]...), nextCursor, true
}

func listPageItemKey(value any) string {
	return pagination.PageItemKey(value)
}

func encodeListKeysetCursor(key, scope, snapshot string) string {
	token, _ := pagination.EncodeKeyset(pagination.QueryKeysetDomain, pagination.KeysetCursor{Key: key, Scope: scope, Snapshot: snapshot, Expires: time.Now().Add(indexCursorLifetime).Unix()})
	return token
}

func decodeListKeysetCursor(token, scope, snapshot string) (string, error) {
	return pagination.DecodeKeyset(pagination.QueryKeysetDomain, token, scope, snapshot, time.Now())
}

const (
	defaultAPILimit   = 50
	maxAPILimit       = 200
	defaultQueryLimit = 100
	maxQueryLimit     = 1000
)

func apiLimitForRequest(w nethttp.ResponseWriter, r *nethttp.Request) (int, bool) {
	limit, err := parseAPILimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return 0, false
	}
	return limit, true
}

func parseAPILimit(value string) (int, error) {
	return pagination.ParseLimit(value, pagination.LimitPolicy{Default: defaultAPILimit, Maximum: maxAPILimit})
}

func apiCursorOffsetForRequest(w nethttp.ResponseWriter, r *nethttp.Request, scopes ...string) (int, bool) {
	offset, err := decodeIndexCursor(r.URL.Query().Get("pageToken"), scopes...)
	if err != nil {
		writeJSONError(w, err, statusForCursorError(err))
		return 0, false
	}
	return offset, true
}

const indexCursorLifetime = 15 * time.Minute

type indexCursor = pagination.IndexCursor

var errCursorSnapshotUnavailable = pagination.ErrSnapshotMismatch

func decodeIndexCursor(token string, scopes ...string) (int, error) {
	expectedScope, expectedSnapshot := cursorScopeParts(scopes...)
	return pagination.DecodeIndex(pagination.QueryIndexDomain, token, expectedScope, expectedSnapshot, time.Now())
}

func encodeIndexCursor(offset int, scopes ...string) string {
	scope, snapshot := cursorScopeParts(scopes...)
	return encodeIndexCursorValue(indexCursor{Offset: offset, Scope: scope, Snapshot: snapshot, Expires: time.Now().Add(indexCursorLifetime).Unix()})
}

func encodeIndexCursorValue(cursor indexCursor) string {
	token, _ := pagination.EncodeIndex(pagination.QueryIndexDomain, pagination.IndexCursor(cursor))
	return token
}

func cursorScopeParts(scopes ...string) (string, string) {
	return pagination.ScopeParts(scopes...)
}

func statusForCursorError(err error) int {
	if errors.Is(err, errCursorSnapshotUnavailable) {
		return nethttp.StatusConflict
	}
	return nethttp.StatusBadRequest
}

func semanticAggregateCursorScope(r *nethttp.Request, input api.SemanticQueryRequest) string {
	input.PageToken = ""
	return requestCursorScope(r, input)
}

func semanticPreviewCursorScope(r *nethttp.Request, input api.SemanticPreviewRequest) string {
	input.PageToken = ""
	return requestCursorScope(r, input)
}

func requestCursorScope(r *nethttp.Request, payload any) string {
	return pagination.RequestScope(r, payload)
}

func writeJSON(w nethttp.ResponseWriter, status int, value any) {
	httptransport.WriteJSON(w, status, value)
}

func writeJSONError(w nethttp.ResponseWriter, err error, status int) {
	details := map[string]any{}
	if reason, ok := workload.ReasonOf(err); ok {
		if reason == workload.QueueTimeout {
			status = nethttp.StatusGatewayTimeout
			details["problemCode"] = "WORKLOAD_QUEUE_TIMEOUT"
		} else {
			status = nethttp.StatusServiceUnavailable
			w.Header().Set("Retry-After", "1")
			details["problemCode"] = "WORKLOAD_OVERLOADED"
		}
	} else if reason, ok := dataquery.ResultLimitReasonOf(err); ok {
		status = nethttp.StatusUnprocessableEntity
		if reason == dataquery.ResultRows {
			details["problemCode"] = "QUERY_RESULT_ROW_LIMIT"
		} else {
			details["problemCode"] = "QUERY_RESULT_BYTE_LIMIT"
		}
	} else if _, ok := analyticsresource.ResourceExhaustedReasonOf(err); ok {
		status = nethttp.StatusServiceUnavailable
		w.Header().Set("Retry-After", "1")
		details["problemCode"] = "ANALYTICS_RESOURCE_EXHAUSTED"
	} else if errors.Is(err, context.DeadlineExceeded) {
		status = nethttp.StatusGatewayTimeout
		details["problemCode"] = "WORKLOAD_EXECUTION_TIMEOUT"
	}
	writeJSON(w, status, httpmodel.ErrorResponse{
		Code:      status,
		Message:   err.Error(),
		Details:   details,
		RequestID: "",
	})
}

func decodeOptionalJSONBody(r *nethttp.Request, dst any) error {
	if r.Body == nil || r.Body == nethttp.NoBody {
		return nil
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("malformed JSON: %w", err)
	}
	var extra struct{}
	if err := decoder.Decode(&extra); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("malformed JSON: %w", err)
	}
	return fmt.Errorf("malformed JSON: multiple JSON values")
}
