package http

import (
	"errors"
	stdhttp "net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

func (h Handler) ListAuditEvents(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	requestedProject := strings.TrimSpace(chi.URLParam(r, "project"))
	if requestedProject == "" {
		requestedProject = strings.TrimSpace(r.URL.Query().Get("project"))
	}
	h.listAuditEvents(w, r, requestedProject, false)
}

func (h Handler) ListAuditEventsForProject(w stdhttp.ResponseWriter, r *stdhttp.Request, requestedProject string) {
	h.listAuditEvents(w, r, requestedProject, false)
}

func (h Handler) ListPlatformAuditEvents(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.listAuditEvents(w, r, strings.TrimSpace(r.URL.Query().Get("project")), true)
}

func (h Handler) listAuditEvents(w stdhttp.ResponseWriter, r *stdhttp.Request, requestedProject string, includeUnscoped bool) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	projectID, ok := h.auditProjectScope(w, r, requestedProject)
	if !ok {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	limit, err := parseAPILimitQuery(r.URL.Query().Get("limit"))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	pageToken := strings.TrimSpace(r.URL.Query().Get("pageToken"))
	if pageToken != "" && !validAuditPageToken(pageToken) {
		writeJSONError(w, errors.New("pageToken is invalid"), stdhttp.StatusBadRequest)
		return
	}
	rows, err := repo.ListAuditEvents(r.Context(), access.AuditEventFilter{
		ProjectID:       projectID.String(),
		IncludeUnscoped: includeUnscoped,
		PrincipalID:     r.URL.Query().Get("principalId"),
		Action:          r.URL.Query().Get("action"),
		ResourceKind:    r.URL.Query().Get("resourceKind"),
		ResourceID:      r.URL.Query().Get("resourceId"),
		Capability:      access.Capability(r.URL.Query().Get("capability")),
		From:            r.URL.Query().Get("from"),
		To:              r.URL.Query().Get("to"),
		PageToken:       pageToken,
		Limit:           limit + 1,
	})
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	next := ""
	if len(rows) > limit {
		last := rows[limit-1]
		next = encodeAuditPageToken(last.CreatedAt, last.ID)
		rows = rows[:limit]
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, auditEventDTO(row))
	}
	writeJSON(w, stdhttp.StatusOK, map[string]any{"items": items, "page": map[string]any{"nextCursor": next}})
}

func (h Handler) auditProjectScope(w stdhttp.ResponseWriter, r *stdhttp.Request, requested string) (projectgraph.ResourceID, bool) {
	if h.CurrentProjectID == nil {
		writeJSONError(w, errors.New("active Project identity is unavailable"), stdhttp.StatusServiceUnavailable)
		return "", false
	}
	projectID, err := h.CurrentProjectID(r.Context())
	if err != nil || projectID.Validate() != nil {
		writeJSONError(w, errors.New("active Project identity is unavailable"), stdhttp.StatusServiceUnavailable)
		return "", false
	}
	requested = strings.TrimSpace(requested)
	if requested != "" && requested != projectID.String() {
		writeJSONError(w, errors.New("audit events not found"), stdhttp.StatusNotFound)
		return "", false
	}
	return projectID, true
}
