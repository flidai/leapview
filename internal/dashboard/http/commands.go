package http

import (
	"context"
	"encoding/json"
	"errors"
	nethttp "net/http"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/command"
	lddatastar "github.com/flidai/leapview/internal/dashboard/datastar"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	dashboardstream "github.com/flidai/leapview/internal/dashboard/stream"
)

type commandPrepare func(command.Service, command.Request, dashboard.Filters) (command.PreparedRefresh, error)

func (h Handler) VisualWindow(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.handleCommand(w, r, func(service command.Service, request command.Request, current dashboard.Filters) (command.PreparedRefresh, error) {
		return service.PrepareVisualWindow(request, current)
	})
}

func (h Handler) Select(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.handleCommand(w, r, func(service command.Service, request command.Request, current dashboard.Filters) (command.PreparedRefresh, error) {
		return service.PrepareSelect(request, current)
	})
}

func (h Handler) SpatialSelect(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.handleCommand(w, r, func(service command.Service, request command.Request, current dashboard.Filters) (command.PreparedRefresh, error) {
		return service.PrepareSpatialSelect(request, current)
	})
}

func (h Handler) ClearSelection(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.handleCommand(w, r, func(service command.Service, request command.Request, current dashboard.Filters) (command.PreparedRefresh, error) {
		return service.PrepareClearSelection(request, current)
	})
}

func (h Handler) handleCommand(w nethttp.ResponseWriter, r *nethttp.Request, prepare commandPrepare) {
	h.handleCommandWithBefore(w, r, prepare, nil)
}

func (h Handler) handleCommandWithBefore(w nethttp.ResponseWriter, r *nethttp.Request, prepare commandPrepare, before func(Metrics, command.Request) func(context.Context) error) {
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
	pageID := lddatastar.PageID(r, signals)
	modelID := lddatastar.ModelID(r, signals, dashboardID, metrics.ModelIDForDashboard)
	streamID := h.scopedStreamID(lddatastar.ClientStreamID(r, signals, dashboardID, pageID))
	request := command.Request{
		DashboardID:               dashboardID,
		PageID:                    pageID,
		ModelID:                   modelID,
		VisualWindowCommand:       signals.VisualWindowCommand,
		InteractionCommand:        signals.InteractionCommand,
		SpatialInteractionCommand: signals.SpatialInteractionCommand,
	}
	if h.CommandGuard != nil {
		if err := h.CommandGuard(r, metrics, request, signals); err != nil {
			nethttp.NotFound(w, r)
			return
		}
	}

	registry := h.Coordinators
	if registry == nil {
		registry = dashboardstream.NewRegistry()
	}
	broker := h.Broker
	if broker == nil {
		broker = pagestream.NewBroker()
	}
	coordinatorContext := h.analyticalStreamContext(context.WithoutCancel(r.Context()), streamID)
	coordinator := registry.Ensure(streamID, coordinatorContext, func(event dashboardstream.RefreshEvent) {
		broker.PublishEnvelope(streamID, lddatastar.RefreshEventEnvelope(event))
	})
	h.observeRefreshes(coordinator, dashboardID, pageID)
	_, err := coordinator.BeginPrepared(func(current dashboard.Filters) (dashboardstream.RefreshPreparation, error) {
		if h.SharedCommandPrepare != nil {
			prepared, generation, err := h.SharedCommandPrepare(r, request, signals, func(shared dashboard.Filters) (command.PreparedRefresh, error) {
				shared.DataRevisions = current.DataRevisions
				return prepare(command.Service{Metrics: metrics}, request, shared)
			})
			if err == nil {
				err = h.persistPreparedSelections(r, metrics, request, signals, prepared)
			}
			preparation := streamPreparation(prepared)
			preparation.Generation = generation
			return preparation, err
		}
		prepared, err := prepare(command.Service{Metrics: metrics}, request, current)
		if err == nil {
			err = h.persistPreparedSelections(r, metrics, request, signals, prepared)
		}
		return streamPreparation(prepared), err
	}, func(preparation dashboardstream.RefreshPreparation) dashboardstream.RefreshWork {
		plan, _ := preparation.Plan.(command.RefreshPlan)
		workRequest := dashboardstream.WorkRequest{
			DashboardID:   dashboardID,
			PageID:        pageID,
			ModelID:       modelID,
			Filters:       preparation.Filters,
			Plan:          plan,
			EventObserved: h.RefreshEventObserved,
			CacheObserved: h.CacheObserved,
		}
		if before != nil {
			workRequest.Before = before(metrics, request)
		}
		return dashboardstream.TargetWork(metrics, workRequest)
	})
	if errors.Is(err, dashboardstream.ErrStalePreparation) {
		writeJSON(w, nethttp.StatusOK, map[string]any{})
		return
	}
	if err != nil {
		// Invalid commands still form a generation so the canonical filters and
		// scoped failure are delivered through the page stream.
		_, _ = coordinator.BeginPrepared(func(current dashboard.Filters) (dashboardstream.RefreshPreparation, error) {
			return dashboardstream.RefreshPreparation{Filters: current, Command: "invalid_command"}, nil
		}, func(dashboardstream.RefreshPreparation) dashboardstream.RefreshWork {
			return func(ctx context.Context, publish dashboardstream.RefreshPublisher) {
				if ctx.Err() == nil {
					publish(dashboardstream.RefreshEvent{Type: dashboardstream.RefreshEventTargetError, Target: "refresh", Err: err})
				}
			}
		})
	}
	// Datastar treats JSON responses as signal patches and consumes the body
	// before completing its request. A 204 response is valid HTTP, but Datastar
	// closes that branch by aborting its fetch controller, which browsers expose
	// as a failed request. The empty patch acknowledges command acceptance while
	// progressive results continue exclusively on the page /updates stream.
	writeJSON(w, nethttp.StatusOK, map[string]any{})
}

func (h Handler) persistPreparedSelections(
	r *nethttp.Request,
	metrics Metrics,
	request command.Request,
	signals dashboard.Signals,
	prepared command.PreparedRefresh,
) error {
	plan := prepared.Plan
	if plan.Command != "select" && plan.Command != "spatial_select" && plan.Command != "clear_selection" {
		return nil
	}
	if h.SessionStore == nil {
		return nil
	}
	resolved, err := resolveDashboard(metrics, request.DashboardID)
	if err != nil {
		return nil
	}
	definition := resolved.Definition
	clientID := pagestream.ClientIDFromRequest(r, signals.Runtime.ClientID)
	key := h.dashboardSessionKey(r, definition, clientID, signals.Runtime.StreamInstanceID)
	interaction, err := selectionMaps(prepared.Filters.Selections)
	if err != nil {
		return err
	}
	spatial, err := selectionMaps(prepared.Filters.SpatialSelections)
	if err != nil {
		return err
	}
	_, err = (dashboardsession.Service{Store: h.SessionStore}).UpdateSelections(r.Context(), key, interaction, spatial)
	if errors.Is(err, dashboardsession.ErrNotFound) {
		return nil
	}
	return err
}

func selectionMaps[T any](values []T) ([]map[string]any, error) {
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	var result []map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	if result == nil {
		result = []map[string]any{}
	}
	return result, nil
}

func streamPreparation(prepared command.PreparedRefresh) dashboardstream.RefreshPreparation {
	targets := make([]string, 0, len(prepared.Plan.Targets))
	for _, target := range prepared.Plan.Targets {
		targets = append(targets, target.Key())
	}
	preparation := dashboardstream.RefreshPreparation{
		Filters: prepared.Filters,
		Command: prepared.Plan.Command,
		Targets: targets,
		Plan:    prepared.Plan,
	}
	if prepared.Plan.Command == "visual_window" && len(prepared.Plan.Targets) == 1 {
		request := prepared.Plan.Targets[0].WindowRequest
		preparation.SequenceKey = "window:" + request.Table
		preparation.Sequence = int64(request.RequestSeq)
		preparation.SequenceEpoch = int64(request.ResetVersion)
	}
	return preparation
}

func (h Handler) readSignals(w nethttp.ResponseWriter, r *nethttp.Request) (dashboard.Signals, bool) {
	signals := dashboard.Signals{}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return dashboard.Signals{}, false
	}
	return signals, true
}
