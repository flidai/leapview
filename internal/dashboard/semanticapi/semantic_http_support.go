package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	nethttp "net/http"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
	"github.com/flidai/leapview/internal/dashboard/api"
	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
	httpmodel "github.com/flidai/leapview/internal/platform/http/model"
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

type listKeysetCursor struct {
	Key      string `json:"key"`
	Scope    string `json:"scope"`
	Snapshot string `json:"snapshot,omitempty"`
	Expires  int64  `json:"expires"`
}

func listPageItemKey(value any) string {
	payload, _ := json.Marshal(value)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func encodeListKeysetCursor(key, scope, snapshot string) string {
	payload, _ := json.Marshal(listKeysetCursor{Key: key, Scope: scope, Snapshot: snapshot, Expires: time.Now().Add(indexCursorLifetime).Unix()})
	return cursorsigning.Sign("q2", payload)
}

func decodeListKeysetCursor(token, scope, snapshot string) (string, error) {
	if token == "" {
		return "", nil
	}
	if !strings.HasPrefix(token, "q2.") {
		return "", fmt.Errorf("invalid page token")
	}
	payload, err := cursorsigning.Verify("q2", token)
	if err != nil {
		return "", fmt.Errorf("invalid page token")
	}
	var cursor listKeysetCursor
	if json.Unmarshal(payload, &cursor) != nil || cursor.Key == "" || cursor.Expires < time.Now().Unix() || cursor.Scope != scope {
		return "", fmt.Errorf("invalid page token")
	}
	if cursor.Snapshot != snapshot {
		return "", errCursorSnapshotUnavailable
	}
	return cursor.Key, nil
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
	if value == "" {
		return defaultAPILimit, nil
	}
	var limit int
	if _, err := fmt.Sscanf(value, "%d", &limit); err != nil {
		return 0, fmt.Errorf("limit must be an integer")
	}
	if limit < 1 {
		return 0, fmt.Errorf("limit must be at least 1")
	}
	if limit > maxAPILimit {
		return 0, fmt.Errorf("limit must not exceed %d", maxAPILimit)
	}
	return limit, nil
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

type indexCursor struct {
	Offset   int    `json:"offset"`
	Scope    string `json:"scope"`
	Snapshot string `json:"snapshot,omitempty"`
	Expires  int64  `json:"expires"`
}

var errCursorSnapshotUnavailable = errors.New("cursor serving snapshot is unavailable")

func decodeIndexCursor(token string, scopes ...string) (int, error) {
	if token == "" {
		return 0, nil
	}
	if !strings.HasPrefix(token, "q1.") {
		return 0, fmt.Errorf("invalid page token")
	}
	payload, err := cursorsigning.Verify("q1", token)
	if err != nil {
		return 0, fmt.Errorf("invalid page token")
	}
	var cursor indexCursor
	if json.Unmarshal(payload, &cursor) != nil || cursor.Offset < 0 || cursor.Expires < time.Now().Unix() {
		return 0, fmt.Errorf("invalid page token")
	}
	expectedScope, expectedSnapshot := cursorScopeParts(scopes...)
	if cursor.Snapshot != expectedSnapshot {
		return 0, errCursorSnapshotUnavailable
	}
	if cursor.Scope != expectedScope {
		return 0, fmt.Errorf("invalid page token")
	}
	return cursor.Offset, nil
}

func encodeIndexCursor(offset int, scopes ...string) string {
	scope, snapshot := cursorScopeParts(scopes...)
	return encodeIndexCursorValue(indexCursor{Offset: offset, Scope: scope, Snapshot: snapshot, Expires: time.Now().Add(indexCursorLifetime).Unix()})
}

func encodeIndexCursorValue(cursor indexCursor) string {
	payload, _ := json.Marshal(cursor)
	return cursorsigning.Sign("q1", payload)
}

func cursorScopeParts(scopes ...string) (string, string) {
	if len(scopes) == 0 || strings.TrimSpace(scopes[0]) == "" {
		return "list", ""
	}
	snapshot := ""
	if len(scopes) > 1 {
		snapshot = scopes[1]
	}
	return scopes[0], snapshot
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
	query := r.URL.Query()
	query.Del("pageToken")
	body, _ := json.Marshal(payload)
	digest := sha256.Sum256([]byte(r.Method + "\n" + r.URL.Path + "\n" + query.Encode() + "\n" + string(body)))
	return hex.EncodeToString(digest[:])
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
