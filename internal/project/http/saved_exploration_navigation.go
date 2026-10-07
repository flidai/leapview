package http

import (
	"encoding/json"
	stdhttp "net/http"
	"net/url"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	saved "github.com/flidai/leapview/internal/analytics/exploration/saved"
	"github.com/go-chi/chi/v5"
)

// savedExplorationNavigation resolves an authorized saved revision before
// redirecting to canonical explorer state. Possession of an ID never grants
// access to the saved resource.
func (h *BrowserHandler) savedExplorationNavigation(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		stdhttp.NotFound(w, r)
		return
	}
	projectID, err := h.boundProject(r.Context())
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return
	}
	explorationID := saved.ExplorationID(strings.TrimSpace(chi.URLParam(r, "exploration")))
	if err := explorationID.Validate(); err != nil {
		stdhttp.NotFound(w, r)
		return
	}
	opened, err := h.SavedExplorations.Reopen(r.Context(), saved.ReopenRequest{ProjectID: projectID, ID: explorationID, ActorID: principal.ID})
	if err != nil {
		writeSavedExplorationReadError(w, r, err)
		return
	}
	if opened.Lifecycle.ID != explorationID || opened.Lifecycle.ProjectID != projectID {
		stdhttp.NotFound(w, r)
		return
	}
	if err := exploration.ValidateShape(&opened.Spec); err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusUnprocessableEntity), stdhttp.StatusUnprocessableEntity)
		return
	}
	state, err := json.Marshal(opened.Spec)
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusUnprocessableEntity), stdhttp.StatusUnprocessableEntity)
		return
	}
	values := url.Values{"v": {"2"}, "mode": {"explore"}, "state": {string(state)}, "saved": {explorationID.String()}}
	if savedExplorationIncludeArchived(r) {
		values.Set("includeArchived", "true")
	}
	stdhttp.Redirect(w, r, "/explore?"+values.Encode(), stdhttp.StatusSeeOther)
}
