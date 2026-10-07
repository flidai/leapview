package http

import (
	"encoding/json"
	"errors"
	nethttp "net/http"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func (h Handler) resumeBuilderFilterSession(r *nethttp.Request, project projectgraph.ResourceID, actor string, builder uisignals.DashboardBuilderSignal, runtime uisignals.RouteRuntimeSignal, raw json.RawMessage) error {
	if h.SessionStore == nil || len(raw) == 0 {
		return nil
	}
	var input dashboardfilter.State
	if err := json.Unmarshal(raw, &input); err != nil {
		return authoring.ErrInvalidPayload
	}
	if input.Revision == 0 {
		return nil
	}
	compiled, err := h.Authoring.Compile(h.analyticalContext(r.Context()), preview.CompileRequest{
		ProjectID: project, ActorID: actor,
		DashboardID: authoring.DashboardID(builder.DashboardID), DraftID: authoring.DraftID(builder.DraftID),
		ExpectedRevision: authoring.RevisionToken{RevisionID: authoring.RevisionID(builder.Revision.ID), Number: uint64(builder.Revision.Number), ContentHash: builder.Revision.ContentHash},
	})
	if err != nil {
		return err
	}
	serving, err := builderActiveServingStateID(builder, compiled.SemanticEvidence.Identity.GenerationID, optionalRuntimeValue(runtime.ServingStateID))
	if err != nil {
		return err
	}
	dashboardID, err := projectgraph.NewResourceID(builder.DashboardID)
	if err != nil {
		return err
	}
	key := dashboardsession.Key{ProjectID: project, PrincipalOrClient: actor + ":" + optionalRuntimeValue(runtime.ClientID), DashboardID: dashboardID, ServingStateID: serving, StreamInstanceID: optionalRuntimeValue(runtime.StreamInstanceID)}
	if _, err := h.SessionStore.Load(r.Context(), key); err == nil {
		return nil
	} else if !errors.Is(err, dashboardsession.ErrNotFound) {
		return err
	}
	snapshot, err := resumeDashboardFilters(compiled.Definition, input)
	if err != nil {
		return err
	}
	// This is a new ephemeral record for the same mounted filter session. Keep
	// its revision so the next browser command need not discard pending edits.
	snapshot.State.Revision = input.Revision
	_, err = h.SessionStore.Create(r.Context(), key, dashboardsession.NewState(firstBuilderPage(builder), snapshot))
	if errors.Is(err, dashboardsession.ErrConflict) {
		return nil
	}
	return err
}
