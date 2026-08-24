package http

import (
	"errors"
	stdhttp "net/http"
	"strings"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/access"
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	uitransport "github.com/flidai/leapview/internal/platform/web/transport"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	"github.com/go-chi/chi/v5"
)

func (h *BrowserHandler) DashboardAppearanceCommand(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if err := uicommand.VerifyClaim(uicommand.OperationClaims(r), dashboardappearance.UpdateCommandBinding().OperationID()); err != nil {
		stdhttp.Error(w, "invalid dashboard appearance command", stdhttp.StatusBadRequest)
		return
	}
	if h.DashboardAppearances == nil || h.AuthorizeDashboard == nil || h.CurrentUser == nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return
	}
	dashboardID, err := projectgraph.NewResourceID(strings.TrimSpace(chi.URLParam(r, "asset")))
	if err != nil || !strings.HasPrefix(dashboardID.String(), "dashboard:") {
		stdhttp.NotFound(w, r)
		return
	}
	principal, ok := h.CurrentUser(r)
	if !ok {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusUnauthorized), stdhttp.StatusUnauthorized)
		return
	}
	if !principal.DevBypass {
		allowed, err := h.AuthorizeDashboard(r, dashboardID.String(), access.CapabilityResourceManage)
		if err != nil {
			stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
			return
		}
		if !allowed {
			uitransport.WriteBrowserAuthorizationError(w, r, stdhttp.StatusForbidden)
			return
		}
	}
	var signals struct {
		Command struct {
			Icon  *string `json:"icon"`
			Color *string `json:"color"`
		} `json:"dashboardAppearanceCommand"`
	}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		stdhttp.Error(w, "dashboard appearance signals are required", stdhttp.StatusBadRequest)
		return
	}
	projectID, err := h.boundProject(r.Context())
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return
	}
	record, err := h.DashboardAppearances.ApplyPatch(r.Context(), dashboardappearance.Key{ProjectID: projectID, DashboardID: dashboardID}, principal.ID, dashboardappearance.Patch{Icon: signals.Command.Icon, Color: signals.Command.Color})
	if err != nil {
		if errors.Is(err, dashboardappearance.ErrInvalid) || errors.Is(err, dashboardappearance.ErrEmptyPatch) {
			stdhttp.Error(w, "the selected dashboard appearance is invalid", stdhttp.StatusUnprocessableEntity)
			return
		}
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusInternalServerError), stdhttp.StatusInternalServerError)
		return
	}
	resolved := dashboardappearance.Resolve(record.Value)
	patch := map[string]any{"page": map[string]any{"dashboardAppearance": projectsignals.DashboardAppearanceSignal{Icon: resolved.Icon, Color: resolved.Color, Revision: record.Revision}}}
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch(patch))
}
