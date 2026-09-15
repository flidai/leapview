package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	nethttp "net/http"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
	"github.com/flidai/leapview/internal/platform/http/pagination"
	httptransport "github.com/flidai/leapview/internal/platform/http/transport"
)

type pageResponse struct {
	NextCursor string `json:"nextCursor"`
}

func pagedResponseWithCursor(items any, nextCursor string) map[string]any {
	return map[string]any{"items": items, "page": pageResponse{NextCursor: nextCursor}}
}

func pageSliceForRequest[T any](w nethttp.ResponseWriter, r *nethttp.Request, items []T) ([]T, string, bool) {
	limit, ok := apiLimitForRequest(w, r)
	if !ok {
		return nil, "", false
	}
	snapshot, snapshotErr := dashboardServingSnapshot(r)
	if snapshotErr != nil {
		writeJSONError(w, snapshotErr, nethttp.StatusServiceUnavailable)
		return nil, "", false
	}
	scope := dashboardRequestCursorScope(r, nil)
	lastKey, err := decodeDashboardKeysetCursor(r.URL.Query().Get("pageToken"), scope, snapshot)
	if err != nil {
		status := nethttp.StatusBadRequest
		if errors.Is(err, errDashboardCursorSnapshot) {
			status = nethttp.StatusConflict
		}
		writeJSONError(w, err, status)
		return nil, "", false
	}
	start := 0
	if lastKey != "" {
		start = -1
		for index, item := range items {
			if apiPageItemKey(item) == lastKey {
				start = index + 1
				break
			}
		}
		if start < 0 {
			writeJSONError(w, errDashboardCursorSnapshot, nethttp.StatusConflict)
			return nil, "", false
		}
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	nextCursor := ""
	if end < len(items) {
		nextCursor = encodeDashboardKeysetCursor(apiPageItemKey(items[end-1]), scope, snapshot)
	}
	return append(make([]T, 0, end-start), items[start:end]...), nextCursor, true
}

func apiPageItemKey(value any) string {
	return pagination.PageItemKey(value)
}

func encodeDashboardKeysetCursor(key, scope, snapshot string) string {
	token, _ := pagination.EncodeKeyset(pagination.DashboardKeysetDomain, pagination.KeysetCursor{Key: key, Scope: scope, Snapshot: snapshot, Expires: time.Now().Add(dashboardCursorLifetime).Unix()})
	return token
}

func decodeDashboardKeysetCursor(token, scope, snapshot string) (string, error) {
	return pagination.DecodeKeyset(pagination.DashboardKeysetDomain, token, scope, snapshot, time.Now())
}

const (
	defaultAPILimit = 50
	maxAPILimit     = 200
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
		status := nethttp.StatusBadRequest
		if errors.Is(err, errDashboardCursorSnapshot) {
			status = nethttp.StatusConflict
		}
		writeJSONError(w, err, status)
		return 0, false
	}
	return offset, true
}

const dashboardCursorLifetime = 15 * time.Minute

var errDashboardCursorSnapshot = pagination.ErrSnapshotMismatch

func decodeIndexCursor(token string, scopes ...string) (int, error) {
	scope, snapshot := dashboardCursorScopeParts(scopes...)
	return pagination.DecodeIndex(pagination.DashboardIndexDomain, token, scope, snapshot, time.Now())
}

func encodeIndexCursor(offset int, scopes ...string) string {
	scope, snapshot := dashboardCursorScopeParts(scopes...)
	return encodeIndexCursorValue(pagination.IndexCursor{Offset: offset, Scope: scope, Snapshot: snapshot, Expires: time.Now().Add(dashboardCursorLifetime).Unix()})
}

func encodeIndexCursorValue(cursor pagination.IndexCursor) string {
	token, _ := pagination.EncodeIndex(pagination.DashboardIndexDomain, cursor)
	return token
}

func dashboardCursorScopeParts(scopes ...string) (string, string) {
	return pagination.ScopeParts(scopes...)
}

func dashboardRequestCursorScope(r *nethttp.Request, payload any) string {
	return pagination.RequestScope(r, payload)
}

func dashboardServingSnapshot(r *nethttp.Request) (string, error) {
	if value := strings.TrimSpace(r.Header.Get("X-Serving-Snapshot")); value != "" {
		return value, nil
	}
	return "", errors.New("serving snapshot is unavailable")
}

func writeJSON(w nethttp.ResponseWriter, status int, value any) {
	httptransport.WriteJSON(w, status, value)
}

func writeJSONError(w nethttp.ResponseWriter, err error, status int) {
	details := map[string]any{}
	var rejection interface{ WorkloadRejectionReason() string }
	if errors.As(err, &rejection) {
		if rejection.WorkloadRejectionReason() == "queue_timeout" {
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
	writeJSON(w, status, map[string]any{
		"code":      status,
		"message":   err.Error(),
		"details":   details,
		"requestId": "",
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
