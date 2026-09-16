package http

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	nethttp "net/http"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/command"
	lddatastar "github.com/flidai/leapview/internal/dashboard/datastar"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	dashboardstream "github.com/flidai/leapview/internal/dashboard/stream"
	reportui "github.com/flidai/leapview/internal/dashboard/ui"
	"github.com/flidai/leapview/internal/dashboard/usage"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	webtransport "github.com/flidai/leapview/internal/platform/web/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/pagestream"
	"github.com/google/uuid"
)

var readStreamInstanceRandom = rand.Read

// Dashboard sessions expire after five minutes by default. Refreshing this
// lease well before expiry keeps an otherwise-idle SSE dashboard alive while
// still allowing a failed store to close the stream and trigger a reconnect.
const dashboardSessionKeepAliveInterval = time.Minute

func (h Handler) Updates(w nethttp.ResponseWriter, r *nethttp.Request) {
	projectID, projectErr := h.projectIDForRequest(r.Context())
	if projectErr != nil {
		nethttp.NotFound(w, r)
		return
	}
	metrics, ok := h.metricsForRequest(r)
	if !ok {
		nethttp.NotFound(w, r)
		return
	}
	dashboardID := strings.TrimSpace(r.URL.Query().Get("dashboard"))
	if dashboardID == "" {
		dashboardID = metrics.DefaultDashboardID()
	}
	pageID := strings.TrimSpace(r.URL.Query().Get("page"))
	resolved, err := resolveDashboard(metrics, dashboardID)
	if err != nil {
		nethttp.NotFound(w, r)
		return
	}
	reportDefinition, model := resolved.Definition, resolved.Model
	pages := metrics.Pages(dashboardID)
	activePage, ok := streamActivePage(pages, pageID)
	if !ok {
		nethttp.NotFound(w, r)
		return
	}
	definitions := make(map[string]visualizationdefinition.Definition)
	for _, component := range activePage.Visuals {
		id := component.Visual
		if id == "" {
			continue
		}
		definition, exists := resolved.Visualization(id)
		if !exists {
			nethttp.Error(w, "compiled visualization definition is missing", nethttp.StatusInternalServerError)
			return
		}
		definitions[id] = definition
	}
	initialFilters := reportDefinition.FiltersFromURLForPage(activePage.ID, r.URL.Query())
	clientID := webtransport.ClientIDFromRequest(r, strings.TrimSpace(r.URL.Query().Get("clientId")))
	streamInstanceID := strings.TrimSpace(r.URL.Query().Get("streamInstance"))
	if streamInstanceID == "" {
		streamInstanceID, err = fallbackStreamInstanceID()
		if err != nil {
			nethttp.Error(w, "dashboard stream identity is unavailable", nethttp.StatusServiceUnavailable)
			return
		}
	}
	filterState, err := reportDefinition.FilterStateFromURL(activePage.ID, r.URL.Query())
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	sessionKey, sessionKeyErr := h.dashboardSessionKey(r, reportDefinition, clientID, streamInstanceID)
	if sessionKeyErr != nil {
		nethttp.NotFound(w, r)
		return
	}
	newSession := false
	if h.SessionStore != nil {
		key := sessionKey
		state := dashboardsession.NewState(activePage.ID, dashboardfilter.MachineSnapshot{
			Version: dashboardfilter.MachineSnapshotVersion, State: filterState,
		})
		record, createErr := h.SessionStore.Create(r.Context(), key, state)
		newSession = createErr == nil
		if errors.Is(createErr, dashboardsession.ErrConflict) {
			record, createErr = h.SessionStore.Load(r.Context(), key)
		}
		if createErr != nil {
			nethttp.Error(w, "dashboard session is unavailable", nethttp.StatusServiceUnavailable)
			return
		}
		filterState = record.State.Filters.State
	}
	if newSession {
		h.recordDashboardView(r, projectID, dashboardID, activePage.ID)
	}
	initialFilters.CompiledState = &filterState
	initialFilters.ServingStateID = sessionKey.ServingStateID
	streamID := h.scopedStreamID(lddatastar.StreamID(clientID, dashboardID, activePage.ID, streamInstanceID))
	if h.SpatialTileStreamClosed != nil {
		defer h.SpatialTileStreamClosed(metrics, streamID)
	}
	request := command.Request{
		DashboardID: dashboardID,
		PageID:      activePage.ID,
		ModelID:     metrics.ModelIDForDashboard(dashboardID),
	}

	broker := h.Broker
	if broker == nil {
		broker = dashboardstream.NewDeliveryBroker()
	}
	mailbox, unsubscribe := broker.Subscribe(streamID)
	defer unsubscribe()

	registry := h.Coordinators
	if registry == nil {
		registry = dashboardstream.NewRegistry()
	}
	streamContext, cancelStream := context.WithCancel(r.Context())
	defer cancelStream()
	if h.SessionStore != nil {
		go keepDashboardSessionAlive(streamContext, h.SessionStore, sessionKey, dashboardSessionKeepAliveInterval, cancelStream)
	}
	coordinatorContext := h.analyticalStreamContext(streamContext, streamID)
	coordinator, closeCoordinator, openErr := registry.OpenWithError(streamID, coordinatorContext, func(event dashboardstream.RefreshEvent) {
		broker.PublishEnvelope(streamID, lddatastar.RefreshEventEnvelope(event))
	})
	if openErr != nil {
		nethttp.Error(w, "dashboard stream capacity is unavailable", nethttp.StatusServiceUnavailable)
		return
	}
	defer closeCoordinator()

	updates := pagestream.NewSignalStream(w, r)
	var providers []webpage.Provider
	if h.Layout != nil {
		providers = []webpage.Provider{h.Layout(r)}
	}
	catalog := h.catalogWithDashboardAppearance(r.Context(), metrics.Catalog(), dashboardID)
	bootstrap := reportui.BootstrapSignalsWithRouteScope(h.RouteScope, clientID, streamInstanceID, catalog, reportDefinition, model, definitions, pages, activePage, initialFilters, providers...)
	if presentation, ok := publicPresentationFromContext(r.Context()); ok {
		bootstrap = reportui.PublicBootstrapSignals(clientID, streamInstanceID, presentation.PublicID, presentation.Presentation, catalog, reportDefinition, model, definitions, pages, activePage, initialFilters)
	} else if hasClientAgentState(r) {
		delete(bootstrap, "agent")
		delete(bootstrap, "agentVisuals")
	} else if h.AgentBootstrap != nil {
		agentState := h.AgentBootstrap(r, projectID.String())
		bootstrap["agent"] = agentState.Agent
		bootstrap["agentVisuals"] = agentState.Visuals
	}
	status := lddatastar.LoadingPatch()["status"].(map[string]any)
	environment := ""
	if h.Environment != nil {
		environment = h.Environment(r)
	}
	if h.DataRefreshedAt != nil {
		status["lastUpdated"] = h.DataRefreshedAt(r.Context(), projectID.String(), environment, request.ModelID)
	}
	bootstrap["status"] = status

	h.observeRefreshes(coordinator, dashboardID, activePage.ID)
	service := command.Service{Metrics: metrics}
	publicationScope := ""
	if presentation, ok := publicPresentationFromContext(r.Context()); ok {
		publicationScope = presentation.PublicationID
	}
	registry.BindForPublication(streamID, projectID, environment, request.ModelID, publicationScope, func() {
		_, _ = coordinator.BeginPrepared(func(current dashboard.Filters) (dashboardstream.RefreshPreparation, error) {
			prepared, err := service.PrepareInitial(request, current)
			return streamPreparation(prepared), err
		}, func(preparation dashboardstream.RefreshPreparation) dashboardstream.RefreshWork {
			plan, _ := preparation.Plan.(command.RefreshPlan)
			return dashboardstream.TargetWork(metrics, dashboardstream.WorkRequest{
				DashboardID: dashboardID, PageID: activePage.ID, ModelID: request.ModelID,
				Filters: preparation.Filters, Plan: plan, EventObserved: h.RefreshEventObserved, CacheObserved: h.CacheObserved, CacheObservationObserved: h.CacheObservationObserved,
			})
		})
	})
	initialWorkGate := make(chan struct{})
	_, err = coordinator.BeginPrepared(func(dashboard.Filters) (dashboardstream.RefreshPreparation, error) {
		prepared, err := service.PrepareInitial(request, initialFilters)
		return streamPreparation(prepared), err
	}, func(preparation dashboardstream.RefreshPreparation) dashboardstream.RefreshWork {
		plan, _ := preparation.Plan.(command.RefreshPlan)
		work := dashboardstream.TargetWork(metrics, dashboardstream.WorkRequest{
			DashboardID:              dashboardID,
			PageID:                   activePage.ID,
			ModelID:                  request.ModelID,
			Filters:                  preparation.Filters,
			Plan:                     plan,
			EventObserved:            h.RefreshEventObserved,
			CacheObserved:            h.CacheObserved,
			CacheObservationObserved: h.CacheObservationObserved,
		})
		return func(ctx context.Context, publish dashboardstream.RefreshPublisher) {
			select {
			case <-initialWorkGate:
				work(ctx, publish)
			case <-ctx.Done():
			}
		}
	})
	// Establish the initial refresh before exposing bootstrap state, but hold
	// its query work until the bootstrap write completes. A table can emit its
	// first window request as soon as bootstrap mounts; publishing bootstrap
	// before establishing the plan lets that plan cancel the window request,
	// while running it during bootstrap can overflow the subscribed mailbox.
	if patchErr := updates.Patch(bootstrap); patchErr != nil {
		return
	}
	close(initialWorkGate)
	if err != nil {
		return
	}
	_ = updates.ForwardUpdates(streamContext, mailbox)
}

func keepDashboardSessionAlive(
	ctx context.Context,
	store dashboardsession.Store,
	key dashboardsession.Key,
	interval time.Duration,
	onFailure func(),
) {
	if store == nil {
		return
	}
	if interval <= 0 {
		interval = dashboardSessionKeepAliveInterval
	}
	touch := func() bool {
		if err := store.Touch(ctx, key); err != nil {
			if ctx.Err() == nil && onFailure != nil {
				onFailure()
			}
			return false
		}
		return true
	}
	// Loading a session does not extend its lease. Renew immediately so an SSE
	// reconnect cannot inherit a record that expires before the first tick.
	if !touch() {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !touch() {
				return
			}
		}
	}
}

func (h Handler) recordDashboardView(r *nethttp.Request, projectID projectgraph.ResourceID, dashboardID, pageID string) {
	if h.RecordDashboardView == nil || h.CurrentUsagePrincipal == nil {
		return
	}
	principalID, human := h.CurrentUsagePrincipal(r)
	if !human || strings.TrimSpace(principalID) == "" {
		return
	}
	dashboardResourceID, err := projectgraph.NewResourceID(strings.TrimSpace(dashboardID))
	if err != nil {
		return
	}
	view := usage.View{
		ProjectID: projectID, DashboardID: dashboardResourceID, PageID: pageID,
		PrincipalID: principalID, ViewedAt: time.Now().UTC(),
	}
	if err := h.RecordDashboardView(r.Context(), view); err != nil {
		logger := h.Logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.ErrorContext(r.Context(), "dashboard usage recording failed",
			"project", projectID, "dashboard", dashboardID, "page", pageID, "error", err)
	}
}

func hasClientAgentState(r *nethttp.Request) bool {
	var signals struct {
		Agent *json.RawMessage `json:"agent"`
	}
	return pagestream.ReadSignals(r, &signals) == nil && signals.Agent != nil
}

func streamActivePage(pages []dashboard.Page, pageID string) (dashboard.Page, bool) {
	if pageID == "" && len(pages) > 0 {
		return pages[0], true
	}
	for _, page := range pages {
		if page.ID == pageID {
			return page, true
		}
	}
	return dashboard.Page{}, false
}

func fallbackStreamInstanceID() (string, error) {
	reader := randomFuncReader(readStreamInstanceRandom)
	id, err := uuid.NewV7FromReader(reader)
	if err != nil {
		return "", fmt.Errorf("generate dashboard stream identity: %w", err)
	}
	return id.String(), nil
}

// randomFuncReader preserves the test seam for the secure UUIDv7 entropy
// source while adapting the historical function-shaped reader to io.Reader.
type randomFuncReader func([]byte) (int, error)

func (r randomFuncReader) Read(p []byte) (int, error) { return r(p) }

func (h Handler) refreshObserver(dashboardID, pageID string) dashboardstream.SummaryObserver {
	logger := h.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return func(summary dashboardstream.RefreshSummary) {
		logger.Info("dashboard refresh",
			"event", "dashboard_refresh",
			"refreshId", summary.RefreshID,
			"generation", summary.Generation,
			"dashboard", dashboardID,
			"page", pageID,
			"command", summary.Command,
			"servingStateId", summary.ServingStateID,
			"filterRevision", summary.FilterRevision,
			"affectedTargets", summary.AffectedTargets,
			"plannedTargets", summary.PlannedTargets,
			"visualCount", summary.VisualCount,
			"optionCount", summary.OptionCount,
			"currentCount", summary.CurrentCount,
			"staleCount", summary.StaleCount,
			"targetSuccesses", summary.TargetSuccesses,
			"targetErrors", summary.TargetErrors,
			"queryCount", summary.QueryCount,
			"cancellationCount", summary.CancellationCount,
			"cancellationReason", summary.CancellationReason,
			"cacheOutcomes", summary.CacheOutcomes,
			"stageTimingsMs", summary.StageTimingsMs,
			"outcome", summary.Outcome,
		)
	}
}

func (h Handler) observeRefreshes(coordinator *dashboardstream.Coordinator, dashboardID, pageID string) {
	coordinator.SetStartObserver(h.RefreshStarted)
	logFinished := h.refreshObserver(dashboardID, pageID)
	coordinator.SetObserver(func(summary dashboardstream.RefreshSummary) {
		logFinished(summary)
		if h.RefreshFinished != nil {
			h.RefreshFinished(summary)
		}
	})
}
