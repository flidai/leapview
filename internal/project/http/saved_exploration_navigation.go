package http

import (
	stdhttp "net/http"
	"net/url"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	saved "github.com/flidai/leapview/internal/analytics/exploration/saved"
	projectui "github.com/flidai/leapview/internal/project/ui"
	"github.com/go-chi/chi/v5"
)

// savedExplorationNavigation resolves the authorized current saved revision
// before redirecting to its canonical explorer URL. A named ID by itself is
// only a selector: possession of a link never grants access or supplies an
// unpublished working copy to the destination.
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
	href, err := projectui.CanonicalDataExplorerHref(opened.Spec)
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusUnprocessableEntity), stdhttp.StatusUnprocessableEntity)
		return
	}
	parsed, err := url.Parse(href)
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusUnprocessableEntity), stdhttp.StatusUnprocessableEntity)
		return
	}
	values := parsed.Query()
	values.Set("saved", explorationID.String())
	if savedExplorationIncludeArchived(r) {
		values.Set("includeArchived", "true")
	}
	parsed.RawQuery = values.Encode()
	stdhttp.Redirect(w, r, parsed.String(), stdhttp.StatusSeeOther)
}
