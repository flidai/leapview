package http

import (
	"context"
	"errors"
	stdhttp "net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringapplication "github.com/flidai/leapview/internal/dashboard/authoring/application"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	httpmiddleware "github.com/flidai/leapview/internal/platform/http/middleware"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectui "github.com/flidai/leapview/internal/project/ui"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	"github.com/flidai/leapview/pkg/pagestream"
	"github.com/go-chi/chi/v5"
)

// DashboardAuthoringApplication is the only dashboard mutation capability
// exposed to the project browser. The browser receives a target picker
// projection and a closed append operation, never a repository/compiler or
// trusted semantic binding map.
type DashboardAuthoringApplication interface {
	ExplorationTargets(context.Context, authoringapplication.ExplorationTargetsRequest) ([]authoringapplication.ExplorationTarget, error)
	ExplorationTarget(context.Context, authoringapplication.ExplorationTargetRequest) (authoringapplication.ExplorationTarget, error)
	AppendExploration(context.Context, authoringapplication.ExplorationAppendRequest) (authoringservice.Result, error)
}

type addExplorationToDashboardSignal = projectsignals.DataExplorerDashboardAppendEnvelope

// addExplorationToDashboardCommand is generated from the closed UI signal
// contract. Keep this alias only for package-local route tests.
type addExplorationToDashboardCommand = projectsignals.DataExplorerDashboardAppendCommandSignal

const maxDashboardForkTargets = 128

func (h *BrowserHandler) dashboardAuthoringCommandBinding() uicommand.Binding {
	if h == nil {
		return uicommand.Binding{}
	}
	return h.DashboardAuthoringCommand
}

func (h *BrowserHandler) dashboardBootstrap(r *stdhttp.Request, sourceModelID string) projectui.DataExplorerDashboardBootstrap {
	binding := h.dashboardAuthoringCommandBinding()
	if h == nil || h.DashboardAuthoring == nil || !binding.Valid() {
		return projectui.DataExplorerDashboardBootstrap{}
	}
	principal, ok := h.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return projectui.DataExplorerDashboardBootstrap{State: "error", Message: "Sign in to add an exploration to a dashboard."}
	}
	project, err := h.boundProject(r.Context())
	if err != nil {
		return projectui.DataExplorerDashboardBootstrap{State: "error", Message: "The active project is unavailable."}
	}
	targets, err := h.DashboardAuthoring.ExplorationTargets(r.Context(), authoringapplication.ExplorationTargetsRequest{ProjectID: project, ActorID: principal.ID, SourceModelID: strings.TrimSpace(sourceModelID)})
	if err != nil {
		return projectui.DataExplorerDashboardBootstrap{State: "error", Message: "Authorized authored dashboards are unavailable."}
	}
	projected := make([]projectsignals.DataExplorerDashboardTargetSignal, 0, len(targets))
	for _, target := range targets {
		projected = append(projected, dashboardTargetSignal(target))
	}
	forkTargets, forkOverflow := h.dashboardForkTargets(r, targets)
	message := ""
	if forkOverflow {
		message = "There are more editable project dashboards than can be shown; use Refresh targets after choosing a narrower dashboard scope."
	}
	return projectui.DataExplorerDashboardBootstrap{Enabled: true, Targets: projected, ForkTargets: forkTargets, Command: binding, Path: "/explore/add-to-dashboard", Message: message}
}

// dashboardForkTargets projects only read-authorized project/YAML dashboards
// are intentionally separate from appendable authored targets: the fork href
// is the existing guarded flow and opening it must not mutate or replace the
// exploration page. Catalog identity is enough to expose the copy affordance;
// the selected fork is re-authorized and model-checked by the normal target
// and append paths after it becomes an authored draft.
func (h *BrowserHandler) dashboardForkTargets(r *stdhttp.Request, authored []authoringapplication.ExplorationTarget) ([]projectsignals.DataExplorerDashboardForkTargetSignal, bool) {
	if h == nil || r == nil || h.Catalog == nil || h.CurrentUser == nil || h.AuthorizeDashboard == nil {
		return nil, false
	}
	principal, ok := h.CurrentUser(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return nil, false
	}
	authoredIDs := make(map[string]struct{}, len(authored))
	for _, target := range authored {
		authoredIDs[target.ID] = struct{}{}
	}
	// List only the catalog's already read-authorized dashboard metadata. Do
	// not use navigationCatalog here: its appearance enrichment reads the
	// complete project definition, which is unnecessary for a fork link and
	// would inspect source documents before each dashboard's edit decision.
	page, err := listCatalogAll(r.Context(), h.Catalog, principal.ID, principal.DevBypass, []projectgraph.Kind{projectgraph.KindDashboard})
	if err != nil {
		return nil, false
	}
	result := make([]projectsignals.DataExplorerDashboardForkTargetSignal, 0)
	for _, item := range page.Items {
		if item.Ref.Kind != projectgraph.KindDashboard {
			continue
		}
		id := strings.TrimSpace(item.Ref.ID.String())
		if id == "" {
			continue
		}
		if _, exists := authoredIDs[id]; exists {
			continue
		}
		allowed, authErr := h.AuthorizeDashboard(r, id, access.CapabilityResourceEdit)
		if authErr != nil || !allowed {
			continue
		}
		if len(result) >= maxDashboardForkTargets {
			// Keep this projection bounded without exposing the number of
			// additional dashboards. The caller surfaces an actionable generic
			// message rather than silently truncating the authorized list.
			sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
			return result, true
		}
		result = append(result, projectsignals.DataExplorerDashboardForkTargetSignal{
			ID: id, Title: browserFirstNonEmpty(item.DisplayName, item.Name, id), ForkHref: "/dashboards/" + url.PathEscape(id) + "/fork",
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, false
}

func dashboardTargetSignal(target authoringapplication.ExplorationTarget) projectsignals.DataExplorerDashboardTargetSignal {
	pages := make([]projectsignals.DataExplorerDashboardPageSignal, 0, len(target.Pages))
	for _, page := range target.Pages {
		pages = append(pages, projectsignals.DataExplorerDashboardPageSignal{ID: page.ID, Title: page.Title, Placement: projectsignals.DashboardPagePlacement{Col: int64(page.Placement.Column), ColSpan: int64(page.Placement.ColumnSpan), Row: int64(page.Placement.Row), RowSpan: int64(page.Placement.RowSpan)}})
	}
	signal := projectsignals.DataExplorerDashboardTargetSignal{ID: target.ID, Title: target.Title, SemanticModelID: target.SemanticModel}
	if target.DraftID != "" {
		draftID := target.DraftID
		signal.DraftID = &draftID
	}
	if target.RevisionToken != "" {
		revisionToken := target.RevisionToken
		signal.RevisionToken = &revisionToken
	}
	if len(pages) > 0 {
		signal.Pages = &pages
	}
	return signal
}

// DataExplorerDashboardTargets refreshes only the authoring picker. It must
// not use the general data updates stream: that stream bootstraps and may
// execute the current exploration, while this action is intentionally a
// read-only dashboard projection that leaves the unsaved query untouched.
func (h *BrowserHandler) DataExplorerDashboardTargets(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if h == nil || h.DashboardAuthoring == nil || r == nil {
		stdhttp.Error(w, "dashboard authoring is unavailable", stdhttp.StatusServiceUnavailable)
		return
	}
	modelID := strings.TrimSpace(r.URL.Query().Get("model"))
	if modelID == "" {
		stdhttp.Error(w, "semantic model is required", stdhttp.StatusBadRequest)
		return
	}
	bootstrap := h.dashboardBootstrap(r, modelID)
	state := projectsignals.DataExplorerDashboardSignal{
		Enabled:     bootstrap.Enabled,
		Targets:     bootstrap.Targets,
		ForkTargets: optionalForkTargets(bootstrap.ForkTargets),
		State:       stringPtr("ready"),
		Message:     optionalString(bootstrap.Message),
	}
	if !bootstrap.Enabled {
		state.State = stringPtr("error")
		if state.Message == nil {
			state.Message = stringPtr("Dashboard targets are unavailable.")
		}
	}
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{"dataExplorerDashboard": state})
}

// DataExplorerDashboardTarget is a read-only, explicitly selected target
// projection. The initial explorer bootstrap contains only bounded metadata;
// this request is the sole path that reads the selected draft's page layout.
func (h *BrowserHandler) DataExplorerDashboardTarget(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if h == nil || h.DashboardAuthoring == nil {
		stdhttp.Error(w, "dashboard authoring is unavailable", stdhttp.StatusServiceUnavailable)
		return
	}
	principal, ok := h.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		stdhttp.Error(w, "dashboard authoring is forbidden", stdhttp.StatusForbidden)
		return
	}
	project, err := h.boundProject(r.Context())
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return
	}
	dashboardID := authoring.DashboardID(chi.URLParam(r, "dashboard"))
	target, err := h.DashboardAuthoring.ExplorationTarget(r.Context(), authoringapplication.ExplorationTargetRequest{ProjectID: project, ActorID: principal.ID, DashboardID: dashboardID})
	if err != nil {
		// Do not expose builder/compiler or model details in the browser body.
		// A signal patch lets the picker leave its pending state even when the
		// selected target disappeared or its serving generation changed.
		bootstrap := h.dashboardBootstrap(r, "")
		message := "Dashboard pages are temporarily unavailable. Refresh targets and try again."
		if errors.Is(err, access.ErrForbidden) {
			message = "This dashboard is no longer available for editing. Refresh targets and choose another dashboard."
		}
		// Keep the picker mounted even when refreshing the fallback list also
		// fails; otherwise the user cannot see the actionable error or retry.
		state := projectsignals.DataExplorerDashboardSignal{Enabled: true, Targets: bootstrap.Targets, ForkTargets: optionalForkTargets(bootstrap.ForkTargets), State: stringPtr("error"), Message: stringPtr(message)}
		_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{"dataExplorerDashboard": state})
		return
	}
	targets, err := h.DashboardAuthoring.ExplorationTargets(r.Context(), authoringapplication.ExplorationTargetsRequest{ProjectID: project, ActorID: principal.ID, SourceModelID: target.SemanticModel})
	if err != nil {
		// Keep internal lifecycle/compiler details out of the browser response
		// while still completing the pending selection with an actionable state.
		state := projectsignals.DataExplorerDashboardSignal{Enabled: true, State: stringPtr("error"), Message: stringPtr("Dashboard targets are temporarily unavailable. Refresh targets and try again.")}
		_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{"dataExplorerDashboard": state})
		return
	}
	selected := dashboardTargetSignal(target)
	projected := make([]projectsignals.DataExplorerDashboardTargetSignal, 0, len(targets))
	for _, candidate := range targets {
		value := dashboardTargetSignal(candidate)
		if value.ID == selected.ID {
			value = selected
		}
		projected = append(projected, value)
	}
	forkTargets, forkOverflow := h.dashboardForkTargets(r, targets)
	message := ""
	if forkOverflow {
		message = "There are more editable project dashboards than can be shown; use Refresh targets after choosing a narrower dashboard scope."
	}
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{"dataExplorerDashboard": projectsignals.DataExplorerDashboardSignal{Enabled: true, Targets: projected, ForkTargets: optionalForkTargets(forkTargets), Message: optionalString(message)}})
}

func optionalForkTargets(value []projectsignals.DataExplorerDashboardForkTargetSignal) *[]projectsignals.DataExplorerDashboardForkTargetSignal {
	if value == nil {
		return nil
	}
	return &value
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func (h *BrowserHandler) DataExplorerAddToDashboard(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	binding := h.dashboardAuthoringCommandBinding()
	if !binding.Valid() {
		stdhttp.Error(w, "dashboard authoring command is unavailable", stdhttp.StatusServiceUnavailable)
		return
	}
	if err := uicommand.VerifyClaim(uicommand.OperationClaims(r), binding.OperationID()); err != nil {
		stdhttp.Error(w, "invalid dashboard authoring command claim", stdhttp.StatusBadRequest)
		return
	}
	if h == nil || h.DashboardAuthoring == nil {
		stdhttp.Error(w, "dashboard authoring is unavailable", stdhttp.StatusServiceUnavailable)
		return
	}
	var signals addExplorationToDashboardSignal
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		stdhttp.Error(w, "add to dashboard payload is required", stdhttp.StatusBadRequest)
		return
	}
	principal, ok := h.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		stdhttp.Error(w, "dashboard authoring is forbidden", stdhttp.StatusForbidden)
		return
	}
	project, err := h.boundProject(r.Context())
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return
	}
	input := signals.AddExplorationToDashboard
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if requestID == "" || httpmiddleware.RequestIDWasGenerated(r) {
		requestID = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	}
	if requestID == "" {
		stdhttp.Error(w, "X-Request-ID is required", stdhttp.StatusBadRequest)
		return
	}
	if _, err := h.DashboardAuthoring.AppendExploration(r.Context(), authoringapplication.ExplorationAppendRequest{
		ProjectID: project, ActorID: principal.ID, DashboardID: authoring.DashboardID(input.DashboardID), PageID: input.PageID,
		RevisionToken: input.RevisionToken, RequestID: requestID, PlacementChoice: input.PlacementChoice, Spec: input.Spec,
	}); err != nil {
		status := stdhttp.StatusConflict
		message := "Add to dashboard could not be completed. Reload the latest dashboard state and try again."
		if errors.Is(err, access.ErrForbidden) {
			status = stdhttp.StatusForbidden
			message = "Adding this exploration to the dashboard is not permitted for your account."
		}
		// The application error may contain compiler, model, or repository
		// details. Transport responses are intentionally generic; the picker
		// uses the status code to render its stable, actionable failure state.
		stdhttp.Error(w, message, status)
		return
	}
	bootstrap := h.dashboardBootstrap(r, input.Spec.ModelID)
	message := "Added to dashboard."
	if bootstrap.Message != "" {
		message += " " + bootstrap.Message
	}
	state := projectsignals.DataExplorerDashboardSignal{Enabled: bootstrap.Enabled, Targets: bootstrap.Targets, ForkTargets: optionalForkTargets(bootstrap.ForkTargets), State: stringPtr("saved"), Message: stringPtr(message)}
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{"dataExplorerDashboard": state})
}

func stringPtr(value string) *string { return &value }
