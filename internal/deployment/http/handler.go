package http

import (
	stdhttp "net/http"
	"strings"

	httptransport "github.com/flidai/leapview/internal/platform/http/transport"
)

func (h *Handler) principal(r *stdhttp.Request) (Principal, bool) {
	if h.options.CurrentPrincipal == nil {
		return Principal{}, false
	}
	principal, ok := h.options.CurrentPrincipal(r)
	return principal, ok && strings.TrimSpace(principal.ID) != ""
}
func writeJSON(w stdhttp.ResponseWriter, status int, value any) {
	httptransport.WriteJSON(w, status, value)
}
