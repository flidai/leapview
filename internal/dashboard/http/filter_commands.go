package http

import (
	"context"
	"errors"
	"log/slog"
	nethttp "net/http"
	"sort"
	"strings"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/command"
	lddatastar "github.com/flidai/leapview/internal/dashboard/datastar"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	dashboardstream "github.com/flidai/leapview/internal/dashboard/stream"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
)

func (h Handler) FilterCommand(w nethttp.ResponseWriter, r *nethttp.Request) {
	metrics, ok := h.metricsForRequest(r)
	if !ok {
		nethttp.NotFound(w, r)
		return
	}
	signals, ok := h.readSignals(w, r)
	if !ok {
		return
	}
	dashboardID, validDashboard := commandDashboardID(r, signals)
	if !validDashboard {
		nethttp.NotFound(w, r)
		return
	}
	pageID := lddatastar.PageID(r, signals)
	resolved, err := resolveDashboard(metrics, dashboardID)
	if err != nil {
		nethttp.NotFound(w, r)
		return
	}
	definition := resolved.Definition
	request := command.Request{
		DashboardID: dashboardID, PageID: pageID,
		ModelID: metrics.ModelIDForDashboard(dashboardID),
	}
	if h.CommandGuard != nil {
		if err := h.CommandGuard(r, metrics, request, signals); err != nil {
			nethttp.NotFound(w, r)
			return
		}
	}
	if h.SessionStore == nil {
		nethttp.Error(w, "dashboard session store is unavailable", nethttp.StatusServiceUnavailable)
		return
	}
	clientID := pagestream.ClientIDFromRequest(r, signals.Runtime.ClientID)
	key, keyErr := h.dashboardSessionKey(r, definition, clientID, signals.Runtime.StreamInstanceID)
	if keyErr != nil {
		nethttp.NotFound(w, r)
		return
	}
	record, err := h.SessionStore.Load(r.Context(), key)
	if errors.Is(err, dashboardsession.ErrNotFound) {
		state := dashboardsession.NewState(pageID, dashboardfilter.NewMachine(
			definition.FilterApplication.WithDefaults().Mode, definition.FilterBindingSpecs(),
		).Snapshot())
		record, err = h.SessionStore.Create(r.Context(), key, state)
	}
	if err != nil {
		nethttp.Error(w, "dashboard session is unavailable", nethttp.StatusServiceUnavailable)
		return
	}
	before := record.State.Filters.State
	changedKeys := changedFilterBindings(signals.FilterCommand, before)
	sessionService := dashboardsession.Service{
		Store: h.SessionStore, ApplicationMode: definition.FilterApplication.WithDefaults().Mode,
		Bindings: definition.FilterBindingSpecs(),
	}
	result, err := sessionService.ExecuteFilterCommand(r.Context(), key, signals.FilterCommand)
	if err != nil {
		// Datastar applies JSON signal patches only for successful transport
		// responses. Command validation is part of the typed signal protocol,
		// so return a successful transport envelope with accepted=false.
		writeJSON(w, nethttp.StatusOK, map[string]any{
			"filterState": uisignals.DashboardFilterStateFromDomain(result.FilterState),
			"filterValidation": map[string]any{
				"accepted": false, "message": err.Error(), "currentRevision": result.FilterState.Revision,
				"clientMutationID": signals.FilterCommand.ClientMutationID,
			},
		})
		return
	}
	response, err := filterCommandResponse(
		definition, pageID, result.FilterState, signals.FilterCommand.ClientMutationID,
	)
	if err != nil {
		writeJSON(w, nethttp.StatusInternalServerError, map[string]any{
			"filterState": uisignals.DashboardFilterStateFromDomain(result.FilterState),
			"filterValidation": map[string]any{
				"accepted": false, "message": err.Error(), "currentRevision": result.FilterState.Revision,
				"clientMutationID": signals.FilterCommand.ClientMutationID,
			},
		})
		return
	}
	logger := h.Logger
	if logger == nil {
		logger = slog.Default()
	}
	affectedTargets := filterCommandTargets(definition, pageID, changedKeys)
	logger.Info("dashboard filter command",
		"event", "dashboard_filter_command",
		"dashboard", dashboardID,
		"page", pageID,
		"binding", signals.FilterCommand.BindingKey,
		"operation", filterCommandOperation(signals.FilterCommand),
		"applicationMode", definition.FilterApplication.WithDefaults().Mode,
		"baseRevision", signals.FilterCommand.BaseRevision,
		"filterRevision", result.FilterState.Revision,
		"affectedTargets", affectedTargets,
		"duplicate", result.Duplicate,
	)
	if result.Duplicate || result.FilterState.Revision == before.Revision {
		writeJSON(w, nethttp.StatusOK, response)
		return
	}

	streamID := h.scopedStreamID(lddatastar.ClientStreamID(r, signals, dashboardID, pageID))
	registry := h.Coordinators
	if registry == nil {
		registry = dashboardstream.NewRegistry()
	}
	broker := h.Broker
	if broker == nil {
		broker = pagestream.NewBroker()
	}
	coordinator := registry.Ensure(streamID, h.analyticalStreamContext(context.WithoutCancel(r.Context()), streamID), func(event dashboardstream.RefreshEvent) {
		broker.PublishEnvelope(streamID, lddatastar.RefreshEventEnvelope(event))
	})
	h.observeRefreshes(coordinator, dashboardID, pageID)
	_, refreshErr := coordinator.BeginPrepared(func(current dashboard.Filters) (dashboardstream.RefreshPreparation, error) {
		prepared, prepareErr := (command.Service{Metrics: metrics}).PrepareFilterState(
			request, current, result.FilterState, changedKeys,
		)
		return streamPreparation(prepared), prepareErr
	}, func(preparation dashboardstream.RefreshPreparation) dashboardstream.RefreshWork {
		plan, _ := preparation.Plan.(command.RefreshPlan)
		return dashboardstream.TargetWork(metrics, dashboardstream.WorkRequest{
			DashboardID: dashboardID, PageID: pageID, ModelID: request.ModelID,
			Filters: preparation.Filters, Plan: plan,
			EventObserved: h.RefreshEventObserved, CacheObserved: h.CacheObserved,
		})
	})
	if refreshErr != nil {
		response["filterValidation"] = map[string]any{
			"accepted": false, "message": refreshErr.Error(), "currentRevision": result.FilterState.Revision,
			"clientMutationID": signals.FilterCommand.ClientMutationID,
		}
		writeJSON(w, nethttp.StatusOK, response)
		return
	}
	writeJSON(w, nethttp.StatusOK, response)
}

func filterCommandOperation(command dashboardfilter.Command) string {
	if command.Kind == dashboardfilter.CommandMutate {
		return string(command.Operation)
	}
	if command.Kind == dashboardfilter.CommandReset {
		return string(command.ResetScope)
	}
	return string(command.Kind)
}

func filterCommandTargets(definition dashboarddefinition.Definition, pageID string, keys []string) []string {
	bindings := definition.CompiledFilterBindings()
	seen := map[string]struct{}{}
	for _, key := range keys {
		for _, target := range bindings[key].Targets {
			if strings.HasPrefix(target, pageID+"/") {
				seen[target] = struct{}{}
			}
		}
	}
	targets := make([]string, 0, len(seen))
	for target := range seen {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	return targets
}

func changedFilterBindings(command dashboardfilter.Command, before dashboardfilter.State) []string {
	switch command.Kind {
	case dashboardfilter.CommandMutate:
		return []string{command.BindingKey}
	case dashboardfilter.CommandApply:
		return append([]string(nil), before.DirtyBindings...)
	case dashboardfilter.CommandReset:
		return append([]string(nil), command.BindingKeys...)
	default:
		return nil
	}
}

func filterCommandResponse(
	definition dashboarddefinition.Definition,
	pageID string,
	state dashboardfilter.State,
	clientMutationID string,
) (map[string]any, error) {
	params, err := definition.URLParamsFromFilterState(pageID, state)
	if err != nil {
		return nil, err
	}
	values := map[string]any{}
	for _, binding := range definition.CompiledFilterBindings() {
		if binding.URL.Param == "" {
			continue
		}
		if binding.Scope == dashboardfilter.ScopePage && binding.PageID != pageID {
			continue
		}
		// Datastar command responses merge signal objects. Null is therefore
		// the explicit tombstone for a canonical filter parameter that is no
		// longer present (unfiltered or reset to its default).
		values[binding.URL.Param] = nil
	}
	for key, entries := range params {
		if len(entries) == 1 {
			values[key] = entries[0]
		} else if len(entries) > 1 {
			values[key] = append([]string(nil), entries...)
		}
	}
	return map[string]any{
		"filterState": uisignals.DashboardFilterStateFromDomain(state),
		"urlParams":   values,
		"filterValidation": map[string]any{
			"accepted": true, "message": "", "currentRevision": state.Revision,
			"clientMutationID": clientMutationID,
		},
	}, nil
}
