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

// A false result requests a full bootstrap because the mounted preview no
// longer belongs to the active serving generation. Compile even without a
// filter session: the retained visual envelopes also depend on that generation.
func (h Handler) resumeBuilderFilterSession(r *nethttp.Request, project projectgraph.ResourceID, actor string, builder uisignals.DashboardBuilderSignal, runtime uisignals.RouteRuntimeSignal, raw json.RawMessage) (bool, error) {
	compiled, err := h.Authoring.Compile(h.analyticalContext(r.Context()), preview.CompileRequest{
		ProjectID: project, ActorID: actor,
		DashboardID: authoring.DashboardID(builder.DashboardID), DraftID: authoring.DraftID(builder.DraftID),
		ExpectedRevision: authoring.RevisionToken{RevisionID: authoring.RevisionID(builder.Revision.ID), Number: uint64(builder.Revision.Number), ContentHash: builder.Revision.ContentHash},
	})
	if err != nil {
		return false, err
	}
	supplied := optionalRuntimeValue(runtime.ServingStateID)
	if supplied == "" {
		return false, nil
	}
	serving, err := builderActiveServingStateID(builder, compiled.SemanticEvidence.Identity.GenerationID, supplied)
	if errors.Is(err, authoring.ErrStaleRevision) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if h.SessionStore == nil || len(raw) == 0 {
		return true, nil
	}
	var input dashboardfilter.State
	if err := json.Unmarshal(raw, &input); err != nil {
		return false, authoring.ErrInvalidPayload
	}
	if input.Revision == 0 {
		return true, nil
	}
	dashboardID, err := projectgraph.NewResourceID(builder.DashboardID)
	if err != nil {
		return false, err
	}
	key := dashboardsession.Key{ProjectID: project, PrincipalOrClient: actor + ":" + optionalRuntimeValue(runtime.ClientID), DashboardID: dashboardID, ServingStateID: serving, StreamInstanceID: optionalRuntimeValue(runtime.StreamInstanceID)}
	if _, err := h.SessionStore.Load(r.Context(), key); err == nil {
		return true, nil
	} else if !errors.Is(err, dashboardsession.ErrNotFound) {
		return false, err
	}
	snapshot, err := resumeDashboardFilters(compiled.Definition, input)
	if err != nil {
		return false, err
	}
	// This is a new ephemeral record for the same mounted filter session. Keep
	// its revision so the next browser command need not discard pending edits.
	snapshot.State.Revision = input.Revision
	_, err = h.SessionStore.Create(r.Context(), key, dashboardsession.NewState(firstBuilderPage(builder), snapshot))
	if errors.Is(err, dashboardsession.ErrConflict) {
		return true, nil
	}
	return err == nil, err
}
