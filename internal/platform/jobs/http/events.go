// Package http owns the HTTP representation of durable job events.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	stdhttp "net/http"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/pkg/jobs"
)

const (
	cursorLifetime              = 15 * time.Minute
	heartbeatInterval           = 15 * time.Second
	eventPollInterval           = time.Second
	streamAuthorizationLifetime = 5 * time.Minute
)

type EventReader interface {
	ListEvents(ctx context.Context, kind, id string, after int64, limit int) ([]jobs.Event, error)
}

type eventListResponse struct {
	Items []eventResponse `json:"items"`
	Page  pageInfo        `json:"page"`
}

type eventResponse struct {
	CreatedAt    string           `json:"createdAt"`
	Data         map[string]any   `json:"data"`
	Error        *structuredError `json:"error,omitempty"`
	Event        string           `json:"event"`
	ID           string           `json:"id"`
	Progress     *progress        `json:"progress,omitempty"`
	ResourceID   string           `json:"resourceId"`
	ResourceType string           `json:"resourceType"`
}

type progress struct {
	Current *int64   `json:"current,omitempty"`
	Message *string  `json:"message,omitempty"`
	Percent *float64 `json:"percent,omitempty"`
	Total   *int64   `json:"total,omitempty"`
}

type structuredError struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type pageInfo struct {
	NextCursor *string `json:"nextCursor,omitempty"`
}

func WriteEventPage(w stdhttp.ResponseWriter, r *stdhttp.Request, repo EventReader, resourceKind, resourceID string, limit *int32, token *string, scope string) {
	if acceptsEventStream(r.Header.Get("Accept")) {
		writeEventStream(w, r, repo, resourceKind, resourceID)
		return
	}
	pageLimit := 50
	if limit != nil {
		pageLimit = int(*limit)
	}
	if pageLimit < 1 || pageLimit > 200 {
		apitransport.WriteProblem(w, r, stdhttp.StatusBadRequest, "INVALID_PAGE_LIMIT", "limit must be between 1 and 200", nil)
		return
	}
	pageToken := ""
	if token != nil {
		pageToken = strings.TrimSpace(*token)
	}
	after, err := eventCursorAfter(pageToken, scope)
	if err != nil {
		apitransport.WriteProblem(w, r, stdhttp.StatusBadRequest, "INVALID_CURSOR", "The event cursor is invalid or expired", nil)
		return
	}
	rows, err := repo.ListEvents(r.Context(), resourceKind, resourceID, after, pageLimit)
	if err != nil {
		apitransport.WriteProblem(w, r, stdhttp.StatusInternalServerError, "ASYNC_EVENT_READ_FAILED", "Events could not be loaded", nil)
		return
	}
	items, err := eventResponses(rows)
	if err != nil {
		apitransport.WriteProblem(w, r, stdhttp.StatusInternalServerError, "ASYNC_EVENT_READ_FAILED", "Events could not be decoded", nil)
		return
	}
	next := ""
	if len(rows) == pageLimit {
		probe, probeErr := repo.ListEvents(r.Context(), resourceKind, resourceID, rows[len(rows)-1].ID, 1)
		if probeErr != nil {
			apitransport.WriteProblem(w, r, stdhttp.StatusInternalServerError, "ASYNC_EVENT_READ_FAILED", "Events could not be loaded", nil)
			return
		}
		if len(probe) != 0 {
			next = encodeCursor(eventCursor{Scope: scope, LastID: fmt.Sprintf("%020d", rows[len(rows)-1].ID), Expires: time.Now().Add(cursorLifetime).Unix()})
		}
	}
	page := pageInfo{}
	if next != "" {
		page.NextCursor = &next
	}
	apitransport.WriteJSON(w, stdhttp.StatusOK, eventListResponse{Items: items, Page: page})
}

func eventCursorAfter(token, scope string) (int64, error) {
	if token == "" {
		return 0, nil
	}
	cursor, err := decodeCursor(token)
	if err != nil || cursor.Scope != scope || cursor.Expires < time.Now().Unix() || cursor.LastID == "" {
		return 0, fmt.Errorf("invalid cursor")
	}
	after, err := strconv.ParseInt(cursor.LastID, 10, 64)
	if err != nil || after < 1 {
		return 0, fmt.Errorf("invalid cursor")
	}
	return after, nil
}

func eventResponses(rows []jobs.Event) ([]eventResponse, error) {
	events := make([]eventResponse, 0, len(rows))
	for _, row := range rows {
		data := map[string]any{}
		if err := json.Unmarshal(row.Data, &data); err != nil {
			return nil, err
		}
		createdAt, err := normalizeTimestamp(row.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("normalize async event timestamp: %w", err)
		}
		if createdAt == "" {
			return nil, fmt.Errorf("normalize async event timestamp: timestamp is required")
		}
		response := eventResponse{
			ID: fmt.Sprintf("%020d", row.ID), Event: row.EventType,
			ResourceType: row.ResourceKind, ResourceID: row.ResourceID,
			Data: data, CreatedAt: createdAt,
		}
		if raw, ok := data["progress"].(map[string]any); ok {
			encoded, _ := json.Marshal(raw)
			var value progress
			if json.Unmarshal(encoded, &value) == nil {
				response.Progress = &value
				delete(data, "progress")
			}
		}
		if raw, ok := data["error"].(map[string]any); ok {
			encoded, _ := json.Marshal(raw)
			var problem structuredError
			if json.Unmarshal(encoded, &problem) == nil && problem.Code != "" && problem.Detail != "" {
				response.Error = &problem
				delete(data, "error")
			}
		}
		events = append(events, response)
	}
	return events, nil
}

func normalizeTimestamp(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC().Format(time.RFC3339Nano), nil
		}
	}
	return "", fmt.Errorf("timestamp %q is not RFC3339 or a supported persisted timestamp", value)
}

func writeEventStream(w stdhttp.ResponseWriter, r *stdhttp.Request, repo EventReader, resourceKind, resourceID string) {
	lastID := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	after := int64(0)
	if lastID != "" {
		parsed, err := strconv.ParseInt(lastID, 10, 64)
		if err != nil || parsed < 1 {
			apitransport.WriteProblem(w, r, stdhttp.StatusBadRequest, "INVALID_LAST_EVENT_ID", "Last-Event-ID does not identify an event in this resource", nil)
			return
		}
		probe, err := repo.ListEvents(r.Context(), resourceKind, resourceID, parsed-1, 1)
		if err != nil || len(probe) != 1 || probe[0].ID != parsed {
			apitransport.WriteProblem(w, r, stdhttp.StatusBadRequest, "INVALID_LAST_EVENT_ID", "Last-Event-ID does not identify an event in this resource", nil)
			return
		}
		after = parsed
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(stdhttp.StatusOK)
	flusher, _ := w.(stdhttp.Flusher)
	heartbeat := time.NewTicker(heartbeatInterval)
	poll := time.NewTicker(eventPollInterval)
	reauthorize := time.NewTimer(streamAuthorizationLifetime)
	defer heartbeat.Stop()
	defer poll.Stop()
	defer reauthorize.Stop()
	for {
		rows, err := repo.ListEvents(r.Context(), resourceKind, resourceID, after, 200)
		if err != nil {
			return
		}
		for _, row := range rows {
			responses, responseErr := eventResponses([]jobs.Event{row})
			if responseErr != nil {
				return
			}
			payload, _ := json.Marshal(responses[0])
			_, _ = fmt.Fprintf(w, "id: %020d\nevent: %s\ndata: %s\n\n", row.ID, row.EventType, payload)
			after = row.ID
			if terminalEvent(row.EventType) {
				if flusher != nil {
					flusher.Flush()
				}
				return
			}
		}
		if flusher != nil && len(rows) != 0 {
			flusher.Flush()
		}
		if len(rows) == 200 {
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-reauthorize.C:
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		case <-poll.C:
		}
	}
}

type eventCursor struct {
	Scope   string `json:"scope"`
	LastID  string `json:"lastId"`
	Expires int64  `json:"expires"`
}

func acceptsEventStream(accept string) bool {
	for _, value := range strings.Split(accept, ",") {
		if strings.EqualFold(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]), "text/event-stream") {
			return true
		}
	}
	return false
}

func terminalEvent(event string) bool {
	suffix := event
	if index := strings.LastIndexByte(event, '.'); index >= 0 {
		suffix = event[index+1:]
	}
	switch suffix {
	case "ready", "failed", "active", "succeeded", "complete", "completed", "cancelled", "canceled", "rolled_back":
		return true
	default:
		return false
	}
}

func encodeCursor(cursor eventCursor) string {
	payload, _ := json.Marshal(cursor)
	return cursorsigning.Sign("e1", payload)
}

func decodeCursor(value string) (eventCursor, error) {
	if !strings.HasPrefix(value, "e1.") {
		return eventCursor{}, fmt.Errorf("invalid cursor")
	}
	payload, err := cursorsigning.Verify("e1", value)
	if err != nil {
		return eventCursor{}, fmt.Errorf("invalid cursor")
	}
	var cursor eventCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return eventCursor{}, err
	}
	return cursor, nil
}
