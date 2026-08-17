package http

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/flidai/leapview/internal/analytics/queryaudit"
	api "github.com/flidai/leapview/internal/platform/http/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

type ReaderProvider func() (queryaudit.Reader, error)
type ProjectIDNormalizer func(string) projectgraph.ResourceID

type Handler struct {
	Reader    ReaderProvider
	ProjectID ProjectIDNormalizer
}

func (h Handler) ListQueryEvents(w http.ResponseWriter, r *http.Request) {
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, http.StatusInternalServerError)
		return
	}
	if repo == nil {
		writeJSON(w, http.StatusOK, pagedResponse(nil, ""))
		return
	}
	limit, ok := limitForRequest(w, r)
	if !ok {
		return
	}
	cursorTime, cursorID := decodeCursor(r.URL.Query().Get("pageToken"))
	rows, err := repo.ListQueryEvents(r.Context(), queryaudit.Filter{
		ProjectID:    h.projectID(chi.URLParam(r, "project")),
		PrincipalID:  r.URL.Query().Get("principal"),
		PrincipalIDs: cleanValues(r.URL.Query()["principal"]),
		Surface:      r.URL.Query().Get("surface"),
		Surfaces:     cleanValues(r.URL.Query()["surface"]),
		Operation:    r.URL.Query().Get("operation"),
		QueryKind:    r.URL.Query().Get("kind"),
		QueryKinds:   cleanValues(r.URL.Query()["kind"]),
		ModelID:      firstNonEmpty(r.URL.Query().Get("modelId"), r.URL.Query().Get("model")),
		Target:       r.URL.Query().Get("target"),
		Status:       r.URL.Query().Get("status"),
		Statuses:     cleanValues(r.URL.Query()["status"]),
		Search:       r.URL.Query().Get("search"),
		From:         r.URL.Query().Get("from"),
		To:           r.URL.Query().Get("to"),
		CursorTime:   cursorTime,
		CursorID:     cursorID,
		Limit:        limit + 1,
	})
	if err != nil {
		writeJSONError(w, err, http.StatusInternalServerError)
		return
	}
	nextCursor := ""
	if len(rows) > limit {
		last := rows[limit-1]
		nextCursor = encodeCursor(last.CreatedAt, last.ID)
		rows = rows[:limit]
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, eventDTO(row))
	}
	writeJSON(w, http.StatusOK, pagedResponse(items, nextCursor))
}

func (h Handler) repository() (queryaudit.Reader, error) {
	if h.Reader == nil {
		return nil, nil
	}
	return h.Reader()
}

func (h Handler) projectID(value string) projectgraph.ResourceID {
	if h.ProjectID == nil {
		return projectgraph.ResourceID(strings.TrimSpace(value))
	}
	return h.ProjectID(value)
}

func eventDTO(row queryaudit.Event) map[string]any {
	var query map[string]any
	if strings.TrimSpace(row.QueryJSON) != "" {
		_ = json.Unmarshal([]byte(row.QueryJSON), &query)
	}
	if query == nil {
		query = map[string]any{}
	}
	return map[string]any{
		"id": row.ID, "projectId": row.ProjectID.String(), "principalId": emptyToNil(row.PrincipalID),
		"surface": row.Surface, "operation": row.Operation, "queryKind": row.QueryKind,
		"modelId": row.ModelID, "target": row.Target, "status": row.Status,
		"objectType": row.ObjectType, "objectId": row.ObjectID, "requestId": row.RequestID,
		"correlationId": row.CorrelationID, "durationMs": row.DurationMS,
		"rowsReturned": row.RowsReturned, "bytesEstimate": row.BytesEstimate,
		"error": emptyToNil(row.Error), "sql": emptyToNil(row.SQL),
		"planText": emptyToNil(row.PlanText), "query": query, "createdAt": row.CreatedAt,
	}
}

func emptyToNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func limitForRequest(w http.ResponseWriter, r *http.Request) (int, bool) {
	value := strings.TrimSpace(r.URL.Query().Get("limit"))
	if value == "" {
		return 50, true
	}
	limit, err := strconv.Atoi(value)
	if err != nil {
		writeJSONError(w, &requestError{"limit must be an integer"}, http.StatusBadRequest)
		return 0, false
	}
	if limit <= 0 {
		writeJSONError(w, &requestError{"limit must be at least 1"}, http.StatusBadRequest)
		return 0, false
	}
	if limit > 200 {
		writeJSONError(w, &requestError{"limit must not exceed 200"}, http.StatusBadRequest)
		return 0, false
	}
	return limit, true
}

type requestError struct{ message string }

func (e *requestError) Error() string { return e.message }

func cleanValues(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func encodeCursor(createdAt, id string) string {
	if createdAt == "" || id == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt + "\x00" + id))
}

func decodeCursor(value string) (string, string) {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return "", ""
	}
	parts := strings.SplitN(string(decoded), "\x00", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func pagedResponse(items any, next string) map[string]any {
	return map[string]any{"items": items, "page": map[string]any{"nextCursor": next}}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeJSONError(w http.ResponseWriter, err error, status int) {
	writeJSON(w, status, api.ErrorResponse{Code: status, Message: err.Error(), Details: map[string]any{}, RequestID: ""})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
