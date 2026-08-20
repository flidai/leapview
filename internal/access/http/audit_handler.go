package http

import (
	"errors"
	stdhttp "net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
)

func (h Handler) ListAuditEvents(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
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
		PrincipalID:  r.URL.Query().Get("principalId"),
		Action:       r.URL.Query().Get("action"),
		ResourceKind: r.URL.Query().Get("resourceKind"),
		ResourceID:   r.URL.Query().Get("resourceId"),
		Capability:   access.Capability(r.URL.Query().Get("capability")),
		From:         r.URL.Query().Get("from"),
		To:           r.URL.Query().Get("to"),
		PageToken:    pageToken,
		Limit:        limit + 1,
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
func (h Handler) ListPlatformAuditEvents(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.ListAuditEvents(w, r)
}
