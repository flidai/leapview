package http

// This file is the dashboard-builder filter transport boundary. Builder
// filters deliberately do not use the published dashboard session routes or
// state: every request is bound to an authoring draft revision and browser
// identity, then compiled through the read-only preview service.

import (
	"context"
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
	webtransport "github.com/flidai/leapview/internal/platform/web/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/pagestream"
	"github.com/go-chi/chi/v5"
)

type builderFilterSignals struct {
	Builder                    uisignals.DashboardBuilderSignal `json:"builder"`
	Runtime                    uisignals.RouteRuntimeSignal     `json:"runtime"`
	BuilderFilterCommand       dashboardfilter.Command          `json:"builderFilterCommand"`
	BuilderFilterOptionRequest dashboardfilter.OptionRequest    `json:"builderFilterOptionRequest"`
}

type builderFilterRequest struct {
	ProjectID   projectgraph.ResourceID
	DashboardID string
	ActorID     string
	ClientID    string
	PageID      string
	Key         dashboardsession.Key
	Builder     uisignals.DashboardBuilderSignal
	Revision    authoring.RevisionToken
}

// DashboardBuilderFilterCommand mutates only ephemeral filter state for one
// exact draft revision. The authored document and the published dashboard
// session are unreachable from this endpoint.
func (h Handler) DashboardBuilderFilterCommand(w nethttp.ResponseWriter, r *nethttp.Request) {
	var signals builderFilterSignals
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, "dashboard builder filter command payload is invalid", nethttp.StatusBadRequest)
		return
	}
	if strings.TrimSpace(string(signals.BuilderFilterCommand.Kind)) == "" {
		nethttp.Error(w, "dashboard builder filter command is required", nethttp.StatusBadRequest)
		return
	}
	request, err := h.builderFilterRequest(r, signals)
	if err != nil {
		writeBuilderFilterError(w, err)
		return
	}
	if h.SessionStore == nil {
		nethttp.Error(w, "dashboard builder filter session is unavailable", nethttp.StatusServiceUnavailable)
		return
	}

	// Compile the exact revision before creating state. This prevents a client
	// from manufacturing a filter contract or binding set for another draft.
	compiled, err := h.Authoring.Preview(h.analyticalContext(r.Context()), preview.PreviewRequest{
		ProjectID: request.ProjectID, ActorID: request.ActorID,
		DashboardID: authoring.DashboardID(request.DashboardID), DraftID: authoring.DraftID(request.Builder.DraftID),
		ExpectedRevision: request.Revision, PageID: request.PageID,
	})
	if err != nil {
		writeBuilderFilterError(w, err)
		return
	}
	request.Key.ServingStateID = builderServingStateIDForGeneration(request.Builder, compiled.SemanticEvidence.Identity.GenerationID)
	if request.Key.ServingStateID == "" {
		writeBuilderFilterError(w, fmt.Errorf("complete builder draft revision is required"))
		return
	}
	if supplied := strings.TrimSpace(optionalRuntimeValue(signals.Runtime.ServingStateID)); supplied != "" && supplied != request.Key.ServingStateID {
		writeBuilderFilterError(w, authoring.ErrStaleRevision)
		return
	}
	record, err := h.ensureBuilderFilterSession(r.Context(), request.Key, request.PageID, compiled.Definition)
	if err != nil {
		writeBuilderFilterError(w, err)
		return
	}
	service := dashboardsession.Service{
		Store: h.SessionStore, ApplicationMode: compiled.Definition.FilterApplication.WithDefaults().Mode,
		Bindings: compiled.Definition.FilterBindingSpecs(),
	}
	result, commandErr := service.ExecuteFilterCommand(r.Context(), request.Key, signals.BuilderFilterCommand)
	if commandErr != nil {
		if result.FilterState.Revision == 0 {
			result.FilterState = record.State.Filters.State
		}
		writeJSON(w, nethttp.StatusOK, builderFilterValidationResponse(compiled.Definition, result.FilterState, false, commandErr.Error(), signals.BuilderFilterCommand.ClientMutationID))
		return
	}

	state := result.FilterState
	filters := dashboard.Filters{CompiledState: &state, ActivePageID: request.PageID, ServingStateID: request.Key.ServingStateID}
	previewResult, previewErr := h.Authoring.Preview(h.analyticalContext(r.Context()), preview.PreviewRequest{
		ProjectID: request.ProjectID, ActorID: request.ActorID,
		DashboardID: authoring.DashboardID(request.DashboardID), DraftID: authoring.DraftID(request.Builder.DraftID),
		ExpectedRevision: request.Revision, PageID: request.PageID, Filters: filters,
	})
	if previewErr != nil {
		writeJSON(w, nethttp.StatusOK, builderFilterValidationResponse(compiled.Definition, state, false, previewErr.Error(), signals.BuilderFilterCommand.ClientMutationID))
		return
	}
	if generation := strings.TrimSpace(compiled.SemanticEvidence.Identity.GenerationID); generation != "" {
		if nextGeneration := strings.TrimSpace(previewResult.SemanticEvidence.Identity.GenerationID); nextGeneration != "" && nextGeneration != generation {
			message := "active serving generation changed during builder filter preview"
			writeJSON(w, nethttp.StatusOK, builderFilterValidationResponse(compiled.Definition, state, false, message, signals.BuilderFilterCommand.ClientMutationID))
			return
		}
	}
	response := builderFilterValidationResponse(previewResult.Definition, state, true, "", signals.BuilderFilterCommand.ClientMutationID)
	response["builderVisuals"] = dashboardBuilderPreviewVisuals(request.Builder, previewResult)
	writeJSON(w, nethttp.StatusOK, response)
}

// DashboardBuilderFilterOptions returns static or distinct options from the
// exact compiled draft definition. Distinct values are queried through the
// active runtime's definition-scoped capability, never the published report.
func (h Handler) DashboardBuilderFilterOptions(w nethttp.ResponseWriter, r *nethttp.Request) {
	var signals builderFilterSignals
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, "dashboard builder filter option payload is invalid", nethttp.StatusBadRequest)
		return
	}
	request, err := h.builderFilterRequest(r, signals)
	if err != nil {
		writeBuilderFilterError(w, err)
		return
	}
	if h.SessionStore == nil {
		nethttp.Error(w, "dashboard builder filter session is unavailable", nethttp.StatusServiceUnavailable)
		return
	}
	// Compile first, then ensure the exact-revision state exists. The builder
	// may request options immediately after bootstrap, before any mutation has
	// created its ephemeral session record.
	compiled, err := h.Authoring.Preview(h.analyticalContext(r.Context()), preview.PreviewRequest{
		ProjectID: request.ProjectID, ActorID: request.ActorID,
		DashboardID: authoring.DashboardID(request.DashboardID), DraftID: authoring.DraftID(request.Builder.DraftID),
		ExpectedRevision: request.Revision, PageID: request.PageID,
	})
	if err != nil {
		writeBuilderFilterError(w, err)
		return
	}
	request.Key.ServingStateID = builderServingStateIDForGeneration(request.Builder, compiled.SemanticEvidence.Identity.GenerationID)
	if request.Key.ServingStateID == "" {
		writeBuilderFilterError(w, fmt.Errorf("complete builder draft revision is required"))
		return
	}
	if supplied := strings.TrimSpace(optionalRuntimeValue(signals.Runtime.ServingStateID)); supplied != "" && supplied != request.Key.ServingStateID {
		// A runtime identity from a prior semantic generation must not create
		// or read options from the newly active draft session.
		writeJSON(w, nethttp.StatusOK, map[string]any{"builderFilterOptionPages": map[string]any{}})
		return
	}
	record, err := h.ensureBuilderFilterSession(r.Context(), request.Key, request.PageID, compiled.Definition)
	if err != nil {
		writeBuilderFilterError(w, err)
		return
	}
	state := record.State.Filters.State
	bindings := compiled.Definition.CompiledFilterBindings()
	binding, ok := bindings[signals.BuilderFilterOptionRequest.BindingKey]
	if !ok || (binding.Scope == dashboardfilter.ScopePage && binding.PageID != request.PageID) {
		nethttp.Error(w, "unknown builder filter option binding", nethttp.StatusBadRequest)
		return
	}
	filterDefinition, ok := compiled.Definition.FilterDefinitions[binding.Filter]
	if !ok {
		nethttp.Error(w, "unknown compiled builder filter definition", nethttp.StatusInternalServerError)
		return
	}
	queryMetrics, supportsDynamic := h.Metrics.(interface {
		QueryCompiledFilterOptionsForDefinition(context.Context, dashboarddefinition.Definition, dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error)
	})
	engine := dashboardfilter.NewOptionEngineWithCache(h.OptionCursorSecret, h.OptionCache, func(ctx context.Context, query dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error) {
		if !supportsDynamic {
			return dashboardfilter.OptionResult{}, fmt.Errorf("compiled filter options are not supported by this runtime")
		}
		return queryMetrics.QueryCompiledFilterOptionsForDefinition(ctx, compiled.Definition, query)
	})
	keysByRef := make(map[dashboardfilter.BindingRef]string, len(bindings))
	for key, candidate := range bindings {
		if candidate.Scope == dashboardfilter.ScopePage && candidate.PageID != request.PageID {
			continue
		}
		keysByRef[dashboardfilter.BindingRef{Scope: candidate.Scope, ID: candidate.ID}] = key
	}
	page, err := engine.Page(r.Context(), dashboardfilter.OptionContext{
		ServingStateID: request.Key.ServingStateID, PolicyIdentity: request.ActorID + ":" + request.ClientID,
		State: state, Binding: binding, Definition: filterDefinition, BindingKeysByRef: keysByRef,
	}, signals.BuilderFilterOptionRequest)
	if err != nil {
		if errors.Is(err, dashboardfilter.ErrStaleOptionRequest) {
			writeJSON(w, nethttp.StatusOK, map[string]any{"builderFilterOptionPages": map[string]any{}})
			return
		}
		writeBuilderFilterError(w, err)
		return
	}
	page.StreamGeneration = record.State.StreamGeneration
	writeJSON(w, nethttp.StatusOK, map[string]any{"builderFilterOptionPages": map[string]any{binding.Key: page}})
}

func optionalRuntimeValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (h Handler) builderFilterRequest(r *nethttp.Request, signals builderFilterSignals) (builderFilterRequest, error) {
	projectID, err := h.projectIDForRequest(r.Context())
	if err != nil {
		return builderFilterRequest{}, err
	}
	dashboardID := strings.TrimSpace(chi.URLParam(r, "dashboard"))
	actorID := h.currentActor(r)
	if dashboardID == "" || actorID == "" || h.Authoring == nil {
		return builderFilterRequest{}, access.ErrForbidden
	}
	if signals.Builder.DashboardID != dashboardID || strings.TrimSpace(signals.Builder.DraftID) == "" {
		return builderFilterRequest{}, fmt.Errorf("dashboard builder filter scope is invalid")
	}
	if queryDraft := strings.TrimSpace(r.URL.Query().Get("draft")); queryDraft != "" && queryDraft != signals.Builder.DraftID {
		return builderFilterRequest{}, authoring.ErrStaleRevision
	}
	if signals.Builder.Revision.Number <= 0 || strings.TrimSpace(signals.Builder.Revision.ID) == "" || strings.TrimSpace(signals.Builder.Revision.ContentHash) == "" {
		return builderFilterRequest{}, fmt.Errorf("complete builder draft revision is required")
	}
	revision := authoring.RevisionToken{RevisionID: authoring.RevisionID(signals.Builder.Revision.ID), Number: uint64(signals.Builder.Revision.Number), ContentHash: signals.Builder.Revision.ContentHash}
	if err := revision.ValidateComplete(); err != nil {
		return builderFilterRequest{}, err
	}
	suppliedClient := ""
	if signals.Runtime.ClientID != nil {
		suppliedClient = *signals.Runtime.ClientID
	}
	clientID := webtransport.ClientIDFromRequest(r, suppliedClient)
	if clientID == "" {
		return builderFilterRequest{}, fmt.Errorf("builder filter client identity is required")
	}
	streamID := strings.TrimSpace(r.URL.Query().Get("streamInstance"))
	if signals.Runtime.StreamInstanceID != nil && strings.TrimSpace(*signals.Runtime.StreamInstanceID) != "" {
		streamID = strings.TrimSpace(*signals.Runtime.StreamInstanceID)
	}
	if streamID == "" {
		streamID = clientID
	}
	dashboardResource, err := projectgraph.NewResourceID(dashboardID)
	if err != nil {
		return builderFilterRequest{}, err
	}
	// ServingStateID is an opaque exact-revision identity. Including the
	// content hash prevents a reused draft ID from inheriting old state.
	servingStateID := builderServingStateID(signals.Builder)
	if servingStateID == "" {
		return builderFilterRequest{}, fmt.Errorf("complete builder draft revision is required")
	}
	key := dashboardsession.Key{ProjectID: projectID, PrincipalOrClient: actorID + ":" + clientID, DashboardID: dashboardResource, ServingStateID: servingStateID, StreamInstanceID: streamID}
	return builderFilterRequest{ProjectID: projectID, DashboardID: dashboardID, ActorID: actorID, ClientID: clientID, PageID: firstBuilderPage(signals.Builder), Key: key, Builder: signals.Builder, Revision: revision}, nil
}

func (h Handler) ensureBuilderFilterSession(ctx context.Context, key dashboardsession.Key, pageID string, definition dashboarddefinition.Definition) (dashboardsession.Record, error) {
	record, err := h.SessionStore.Load(ctx, key)
	if err == nil {
		return record, nil
	}
	if !errors.Is(err, dashboardsession.ErrNotFound) {
		return dashboardsession.Record{}, err
	}
	machine := dashboardfilter.NewMachine(definition.FilterApplication.WithDefaults().Mode, definition.FilterBindingSpecs())
	record, createErr := h.SessionStore.Create(ctx, key, dashboardsession.NewState(pageID, machine.Snapshot()))
	if errors.Is(createErr, dashboardsession.ErrConflict) {
		return h.SessionStore.Load(ctx, key)
	}
	return record, createErr
}

func builderFilterValidationResponse(definition dashboarddefinition.Definition, state dashboardfilter.State, accepted bool, message, mutationID string) map[string]any {
	return map[string]any{
		"builderFilterContract":    uisignals.DashboardFilterContractFromDefinition(definition),
		"builderFilterState":       uisignals.DashboardFilterStateFromDomain(state),
		"builderFilterOptionPages": map[string]any{},
		"builderFilterValidation":  uisignals.DashboardFilterValidationResult{Accepted: accepted, Message: message, CurrentRevision: int64(state.Revision), ClientMutationID: mutationID},
	}
}

func writeBuilderFilterError(w nethttp.ResponseWriter, err error) {
	status := nethttp.StatusConflict
	switch {
	case errors.Is(err, access.ErrForbidden):
		status = nethttp.StatusForbidden
	case errors.Is(err, authoring.ErrNotFound):
		status = nethttp.StatusNotFound
	case errors.Is(err, authoring.ErrInvalidPayload), errors.Is(err, authoring.ErrInvalidIdentifier):
		status = nethttp.StatusBadRequest
	case errors.Is(err, dashboardsession.ErrNotFound):
		status = nethttp.StatusConflict
	}
	nethttp.Error(w, err.Error(), status)
}
