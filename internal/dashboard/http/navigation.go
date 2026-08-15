package http

import (
	"context"
	"errors"
	nethttp "net/http"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/command"
	lddatastar "github.com/flidai/leapview/internal/dashboard/datastar"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	dashboardstream "github.com/flidai/leapview/internal/dashboard/stream"
	reportui "github.com/flidai/leapview/internal/dashboard/ui"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
)

func (h Handler) Navigate(w nethttp.ResponseWriter, r *nethttp.Request) {
	metrics, ok := h.metricsForRequest(r)
	if !ok {
		nethttp.NotFound(w, r)
		return
	}
	signals, ok := h.readSignals(w, r)
	if !ok {
		return
	}
	dashboardID := lddatastar.DashboardID(r, signals, metrics.DefaultDashboardID())
	sourcePageID := lddatastar.PageID(r, signals)
	resolved, err := resolveDashboard(metrics, dashboardID)
	if err != nil {
		nethttp.NotFound(w, r)
		return
	}
	definition, model := resolved.Definition, resolved.Model
	targetPage, ok := definition.PageOrDefault(signals.NavigationCommand.PageID)
	if !ok || targetPage.ID != signals.NavigationCommand.PageID {
		nethttp.Error(w, "unknown dashboard page", nethttp.StatusBadRequest)
		return
	}
	request := command.Request{
		DashboardID: dashboardID, PageID: sourcePageID,
		ModelID: metrics.ModelIDForDashboard(dashboardID),
	}
	if h.CommandGuard != nil {
		if err := h.CommandGuard(r, metrics, request, signals); err != nil {
			nethttp.NotFound(w, r)
			return
		}
	}
	request.PageID = targetPage.ID
	if h.SessionStore == nil {
		nethttp.Error(w, "dashboard session store is unavailable", nethttp.StatusServiceUnavailable)
		return
	}
	clientID := pagestream.ClientIDFromRequest(r, signals.Runtime.ClientID)
	streamInstanceID := signals.Runtime.StreamInstanceID
	key := h.dashboardSessionKey(r, definition, clientID, streamInstanceID)
	result, err := (dashboardsession.Service{Store: h.SessionStore}).Navigate(r.Context(), key, dashboardsession.NavigationCommand{
		PageID: targetPage.ID, BaseFilterRevision: signals.NavigationCommand.BaseFilterRevision,
		ClientMutationID: signals.NavigationCommand.ClientMutationID,
	})
	if err != nil {
		status := nethttp.StatusBadRequest
		if errors.Is(err, dashboardfilter.ErrStaleRevision) || errors.Is(err, dashboardsession.ErrConflict) {
			status = nethttp.StatusConflict
		}
		writeJSON(w, status, map[string]any{"error": err.Error(), "activePage": result.ActivePage})
		return
	}
	record, err := h.SessionStore.Load(r.Context(), key)
	if err != nil {
		nethttp.Error(w, "dashboard session is unavailable", nethttp.StatusServiceUnavailable)
		return
	}
	filterState := dashboardfilter.CloneState(record.State.Filters.State)
	initialFilters := definition.NormalizeFiltersForPage(targetPage.ID, dashboard.Filters{
		CompiledState: &filterState, ServingStateID: key.ServingStateID,
	})
	if err := decodeDashboardSelectionState(record.State.InteractionSelections, &initialFilters.Selections); err != nil {
		nethttp.Error(w, "dashboard interaction state is invalid", nethttp.StatusInternalServerError)
		return
	}
	if err := decodeDashboardSelectionState(record.State.SpatialSelections, &initialFilters.SpatialSelections); err != nil {
		nethttp.Error(w, "dashboard spatial state is invalid", nethttp.StatusInternalServerError)
		return
	}
	definitions := make(map[string]visualizationdefinition.Definition)
	for _, component := range targetPage.Visuals {
		if component.Visual == "" {
			continue
		}
		if visual, exists := resolved.Visualization(component.Visual); exists {
			definitions[component.Visual] = visual
		}
	}
	bootstrap := reportui.BootstrapSignalsWithRouteScope(
		h.RouteScope, clientID, streamInstanceID, metrics.Catalog(), definition, model, definitions,
		definition.Pages, targetPage, initialFilters,
	)
	if presentation, public := publicPresentationFromContext(r.Context()); public {
		bootstrap = reportui.PublicBootstrapSignals(
			clientID, streamInstanceID, presentation.PublicID, presentation.Presentation,
			metrics.Catalog(), definition, model, definitions, definition.Pages, targetPage, initialFilters,
		)
	}
	patch := pagestream.SignalPatch{}
	for _, root := range []string{
		"agentContext", "chrome", "page", "runtime", "filterState", "filterOptionPages",
		"urlParams", "visuals", "status",
	} {
		if value, exists := bootstrap[root]; exists {
			patch[root] = value
		}
	}
	if status, ok := patch["status"].(uisignals.DashboardStatus); ok {
		status.Generation = int64(result.StreamGeneration)
		patch["status"] = status
	}
	if context, ok := patch["agentContext"].(uisignals.AgentContextSignal); ok {
		context.Generation = int64(result.StreamGeneration)
		patch["agentContext"] = context
	}
	if visuals, ok := patch["visuals"].(map[string]uisignals.DashboardVisualizationSignal); ok {
		for id, visual := range visuals {
			visual.ServingStateID = key.ServingStateID
			visual.StreamGeneration = int64(result.StreamGeneration)
			visual.FilterRevision = int64(filterState.Revision)
			visual.ConsumerIdentity = targetPage.ID + "/" + id
			visuals[id] = visual
		}
		patch["visuals"] = visuals
	}
	sourceStreamID := h.scopedStreamID(lddatastar.StreamID(clientID, dashboardID, sourcePageID, streamInstanceID))
	broker := h.Broker
	if broker == nil {
		broker = pagestream.NewBroker()
	}
	broker.PublishEnvelope(sourceStreamID, pagestream.Envelope{
		Signals: patch,
		Delivery: pagestream.DeliveryMetadata{
			Generation: result.StreamGeneration, Boundary: true,
		},
		Trace: pagestream.TraceMetadata{Origin: "dashboard.navigation", CorrelationID: signals.NavigationCommand.ClientMutationID},
	})
	if result.Duplicate {
		writeJSON(w, nethttp.StatusOK, map[string]any{"activePage": targetPage.ID, "duplicate": true})
		return
	}
	registry := h.Coordinators
	if registry == nil {
		registry = dashboardstream.NewRegistry()
	}
	coordinator := registry.Ensure(sourceStreamID, h.analyticalStreamContext(context.WithoutCancel(r.Context()), sourceStreamID), func(event dashboardstream.RefreshEvent) {
		broker.PublishEnvelope(sourceStreamID, lddatastar.RefreshEventEnvelope(event))
	})
	h.observeRefreshes(coordinator, dashboardID, targetPage.ID)
	_, err = coordinator.BeginPrepared(func(dashboard.Filters) (dashboardstream.RefreshPreparation, error) {
		prepared, prepareErr := (command.Service{Metrics: metrics}).PrepareInitial(request, initialFilters)
		return streamPreparation(prepared), prepareErr
	}, func(preparation dashboardstream.RefreshPreparation) dashboardstream.RefreshWork {
		plan, _ := preparation.Plan.(command.RefreshPlan)
		return dashboardstream.TargetWork(metrics, dashboardstream.WorkRequest{
			DashboardID: dashboardID, PageID: targetPage.ID, ModelID: request.ModelID,
			Filters: preparation.Filters, Plan: plan,
			EventObserved: h.RefreshEventObserved, CacheObserved: h.CacheObserved,
		})
	})
	if err != nil && !errors.Is(err, dashboardstream.ErrStalePreparation) {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	writeJSON(w, nethttp.StatusOK, map[string]any{"activePage": targetPage.ID})
}
