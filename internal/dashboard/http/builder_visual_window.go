package http

import (
	"errors"
	"fmt"
	nethttp "net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/flidai/leapview/pkg/pagestream"
)

type builderVisualWindowSignals struct {
	Builder             uisignals.DashboardBuilderSignal           `json:"builder"`
	Runtime             uisignals.RouteRuntimeSignal               `json:"runtime"`
	BuilderFilterState  uisignals.DashboardFilterState             `json:"builderFilterState"`
	VisualWindowCommand visualizationir.VisualizationWindowRequest `json:"visualWindowCommand"`
}

// DashboardBuilderVisualWindow queries one window of one visual from the
// exact authored draft revision. Filter state is loaded from the builder's
// ephemeral session, while definition compilation and query execution stay in
// the read-only preview boundary. The published dashboard command path is
// deliberately not reachable from this handler.
func (h Handler) DashboardBuilderVisualWindow(w nethttp.ResponseWriter, r *nethttp.Request) {
	var signals builderVisualWindowSignals
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, "dashboard builder visual window payload is invalid", nethttp.StatusBadRequest)
		return
	}
	if strings.TrimSpace(signals.VisualWindowCommand.VisualID) == "" {
		nethttp.Error(w, "dashboard builder visual window requires a visual ID", nethttp.StatusBadRequest)
		return
	}
	request, err := h.builderFilterRequest(r, builderFilterSignals{Builder: signals.Builder, Runtime: signals.Runtime})
	if err != nil {
		writeBuilderVisualWindowError(w, err)
		return
	}
	if h.SessionStore == nil {
		nethttp.Error(w, "dashboard builder filter session is unavailable", nethttp.StatusServiceUnavailable)
		return
	}

	compiled, err := h.Authoring.Compile(h.analyticalContext(r.Context()), preview.CompileRequest{
		ProjectID: request.ProjectID, ActorID: request.ActorID,
		DashboardID: authoring.DashboardID(request.DashboardID), DraftID: authoring.DraftID(request.Builder.DraftID),
		ExpectedRevision: request.Revision,
	})
	if err != nil {
		writeBuilderVisualWindowError(w, err)
		return
	}
	generation := strings.TrimSpace(compiled.SemanticEvidence.Identity.GenerationID)
	if generation == "" {
		writeBuilderVisualWindowError(w, errors.New("dashboard builder visual window requires an active serving generation"))
		return
	}
	request.Key.ServingStateID = builderServingStateIDForGeneration(request.Builder, generation)
	if request.Key.ServingStateID == "" {
		writeBuilderVisualWindowError(w, fmt.Errorf("%w: complete builder draft revision is required", authoring.ErrInvalidPayload))
		return
	}
	if supplied := strings.TrimSpace(optionalRuntimeValue(signals.Runtime.ServingStateID)); supplied != "" && supplied != request.Key.ServingStateID {
		writeBuilderVisualWindowError(w, authoring.ErrStaleRevision)
		return
	}
	if err := validateBuilderVisualWindow(compiled.Definition, request.PageID, signals.VisualWindowCommand); err != nil {
		writeBuilderVisualWindowError(w, err)
		return
	}
	record, err := h.ensureBuilderFilterSession(r.Context(), request.Key, request.PageID, compiled.Definition)
	if err != nil {
		writeBuilderVisualWindowError(w, err)
		return
	}
	state := record.State.Filters.State
	if err := validateBuilderFilterRevision(signals.BuilderFilterState.Revision, state.Revision); err != nil {
		writeBuilderVisualWindowError(w, err)
		return
	}
	filters := dashboard.Filters{CompiledState: &state, ActivePageID: request.PageID, ServingStateID: request.Key.ServingStateID}
	previewResult, err := h.Authoring.Preview(h.analyticalContext(r.Context()), preview.PreviewRequest{
		ProjectID: request.ProjectID, ActorID: request.ActorID,
		DashboardID: authoring.DashboardID(request.DashboardID), DraftID: authoring.DraftID(request.Builder.DraftID),
		ExpectedRevision: request.Revision, PageID: request.PageID, Filters: filters,
		Window: &signals.VisualWindowCommand, BestEffortVisuals: true,
	})
	if err != nil {
		writeBuilderVisualWindowError(w, err)
		return
	}
	if strings.TrimSpace(previewResult.SemanticEvidence.Identity.GenerationID) != generation {
		writeBuilderVisualWindowError(w, fmt.Errorf("%w: active serving generation changed during builder visual window preview", authoring.ErrStaleRevision))
		return
	}
	// Filter commands can complete while the window query is running. Re-read
	// the session before publishing the result so a response cannot carry rows
	// for a filter revision that is no longer current.
	latest, err := h.SessionStore.Load(r.Context(), request.Key)
	if err != nil {
		writeBuilderVisualWindowError(w, err)
		return
	}
	if err := validateBuilderFilterRevision(signals.BuilderFilterState.Revision, latest.State.Filters.State.Revision); err != nil {
		writeBuilderVisualWindowError(w, err)
		return
	}
	visualID := strings.TrimSpace(signals.VisualWindowCommand.VisualID)
	visuals := dashboardBuilderPreviewVisuals(request.Builder, previewResult)
	envelope, ok := visuals[visualID]
	if !ok {
		writeBuilderVisualWindowError(w, fmt.Errorf("dashboard builder visual window response omitted visual %q", visualID))
		return
	}
	// Keep window results separate from the base preview. Otherwise an old
	// response can erase a new preview before the browser has rendered it.
	// Full preview replacement clears these bounded, per-context window slots.
	windowKey := fmt.Sprintf("window:%s:%s:%d:%s", request.Key.ServingStateID, request.PageID, state.Revision, visualID)
	writeJSON(w, nethttp.StatusOK, map[string]any{"builderVisuals": map[string]uisignals.DashboardVisualizationSignal{windowKey: envelope}})
}

func validateBuilderFilterRevision(posted int64, current uint64) error {
	if posted < 0 || uint64(posted) != current {
		return fmt.Errorf("%w: builder filter revision %d does not match current filter revision %d", dashboardfilter.ErrStaleRevision, posted, current)
	}
	return nil
}

func validateBuilderVisualWindow(definition dashboarddefinition.Definition, pageID string, request visualizationir.VisualizationWindowRequest) error {
	pageID = strings.TrimSpace(pageID)
	visualID := strings.TrimSpace(request.VisualID)
	if pageID == "" || visualID == "" {
		return fmt.Errorf("%w: builder visual window page and visual are required", authoring.ErrInvalidPayload)
	}
	for _, page := range definition.Pages {
		if page.ID != pageID {
			continue
		}
		for _, visual := range page.Visuals {
			if visual.Visual == visualID {
				return nil
			}
		}
		return fmt.Errorf("%w: visual %q is not on page %q", authoring.ErrInvalidPayload, visualID, pageID)
	}
	return fmt.Errorf("%w: page %q is not in the selected draft", authoring.ErrInvalidPayload, pageID)
}

func writeBuilderVisualWindowError(w nethttp.ResponseWriter, err error) {
	if err == nil {
		err = errors.New("dashboard builder visual window request failed")
	}
	status := nethttp.StatusServiceUnavailable
	switch {
	case errors.Is(err, access.ErrForbidden):
		status = nethttp.StatusForbidden
	case errors.Is(err, authoring.ErrNotFound):
		status = nethttp.StatusNotFound
	case errors.Is(err, authoring.ErrStaleRevision), errors.Is(err, dashboardfilter.ErrStaleRevision), errors.Is(err, dashboardsession.ErrConflict), errors.Is(err, dashboardsession.ErrNotFound):
		status = nethttp.StatusConflict
	case errors.Is(err, authoring.ErrInvalidAuthoring), errors.Is(err, authoring.ErrInvalidPayload), errors.Is(err, authoring.ErrInvalidIdentifier), errors.Is(err, authoring.ErrInvalidTransition):
		status = nethttp.StatusBadRequest
	case strings.Contains(err.Error(), "specification revision is stale"):
		status = nethttp.StatusConflict
	case strings.Contains(err.Error(), "visual window requires"), strings.Contains(err.Error(), "invalid visual window"), strings.Contains(err.Error(), "visual window supports"):
		status = nethttp.StatusBadRequest
	}
	nethttp.Error(w, err.Error(), status)
}
