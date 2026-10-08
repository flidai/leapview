package http

import (
	"context"
	"encoding/json"
	"errors"
	nethttp "net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/builderview"
	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	"github.com/flidai/leapview/internal/dashboard/ui"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/pagestream"
)

// DashboardBuilderUpdates emits the typed builder projection on the canonical
// Datastar page stream. It intentionally does not accept a client-selected
// revision; the application resolves the current authorized draft.
func (h Handler) DashboardBuilderUpdates(w nethttp.ResponseWriter, r *nethttp.Request) {
	project, err := h.projectIDForRequest(r.Context())
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	projectID := project.String()
	dashboardID := strings.TrimSpace(r.URL.Query().Get("dashboard"))
	var mounted struct {
		Builder *struct {
			DashboardID      string                                   `json:"dashboardId"`
			DraftID          string                                   `json:"draftId"`
			Revision         uisignals.DashboardBuilderRevisionSignal `json:"revision"`
			SelectedPageID   *string                                  `json:"selectedPageId"`
			SelectedVisualID *string                                  `json:"selectedVisualId"`
			Preview          *struct {
				Loading *bool `json:"loading"`
			} `json:"preview"`
		} `json:"builder"`
		Runtime     uisignals.RouteRuntimeSignal `json:"runtime"`
		FilterState json.RawMessage              `json:"builderFilterState"`
	}
	mountedOK := pagestream.ReadSignals(r, &mounted) == nil && mounted.Builder != nil && mounted.Builder.DashboardID == dashboardID

	actorID := h.currentActor(r)
	if projectID == "" || dashboardID == "" || actorID == "" || h.Authoring == nil {
		writeBuilderError(w, r, access.ErrForbidden)
		return
	}
	snapshot := r.URL.Query().Get("snapshot") == "1"
	var snapshotSignals struct {
		Refresh dashboardBuilderRefreshContext `json:"builderRefresh"`
		Runtime uisignals.RouteRuntimeSignal   `json:"runtime"`
	}
	if snapshot {
		// The completion refresh is a one-shot request from an already mounted
		// builder. Carry its selection and preview identity so the server can
		// refresh the selected page while retaining its scoped filter session.
		// Older callers may omit these fields; the authoritative builder read
		// below remains the source of all authored data.
		_ = pagestream.ReadSignals(r, &snapshotSignals)
	}
	selectedPageID := strings.TrimSpace(r.URL.Query().Get("page"))
	selectedVisualID := strings.TrimSpace(r.URL.Query().Get("visual"))
	if mountedOK {
		if mounted.Builder.SelectedPageID != nil {
			selectedPageID = strings.TrimSpace(*mounted.Builder.SelectedPageID)
		}
		if mounted.Builder.SelectedVisualID != nil {
			selectedVisualID = strings.TrimSpace(*mounted.Builder.SelectedVisualID)
		}
	}
	if snapshot && snapshotSignals.Refresh.DashboardID == dashboardID {
		if strings.TrimSpace(snapshotSignals.Refresh.PageID) != "" {
			selectedPageID = strings.TrimSpace(snapshotSignals.Refresh.PageID)
		}
		if strings.TrimSpace(snapshotSignals.Refresh.VisualID) != "" {
			selectedVisualID = strings.TrimSpace(snapshotSignals.Refresh.VisualID)
		}
	}
	builder, err := h.Authoring.Builder(r.Context(), builderview.Request{
		ProjectID: project, ActorID: actorID, DashboardID: authoring.DashboardID(dashboardID),
		SelectedPageID: selectedPageID, SelectedVisualID: selectedVisualID,
	})
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	if requestedDraft := strings.TrimSpace(r.URL.Query().Get("draft")); requestedDraft != "" && requestedDraft != builder.DraftID {
		writeBuilderError(w, r, authoring.ErrStaleRevision)
		return
	}
	clientID, ok := h.ClientIDs.Require(w, r)
	if !ok {
		return
	}
	streamInstanceID := strings.TrimSpace(r.URL.Query().Get("streamInstance"))
	if snapshot && snapshotSignals.Runtime.StreamInstanceID != nil && strings.TrimSpace(*snapshotSignals.Runtime.StreamInstanceID) != "" {
		streamInstanceID = strings.TrimSpace(*snapshotSignals.Runtime.StreamInstanceID)
	}
	if streamInstanceID == "" {
		streamInstanceID = clientID
	}
	// Builder commands update the mounted signals through their own responses.
	// Reopening this idle page stream must not replace pending browser edits,
	// selections, or filters with the initial URL's bootstrap. An interrupted
	// loading patch has the current revision but still needs its full preview.

	if r.URL.Query().Get("snapshot") != "1" && mountedOK &&
		mounted.Builder.DashboardID == dashboardID && mounted.Builder.DraftID == builder.DraftID &&
		mounted.Builder.Revision == builder.Revision &&
		mounted.Builder.Preview != nil && mounted.Builder.Preview.Loading != nil && !*mounted.Builder.Preview.Loading &&
		mounted.Runtime.ClientID != nil && *mounted.Runtime.ClientID == clientID &&
		mounted.Runtime.StreamInstanceID != nil && *mounted.Runtime.StreamInstanceID == streamInstanceID {
		resumed, err := h.resumeBuilderFilterSession(r, project, actorID, builder, mounted.Runtime, mounted.FilterState)
		if err != nil {
			writeBuilderError(w, r, err)
			return
		}
		if resumed {
			updates := pagestream.NewSignalStream(w, r)
			if err := updates.Patch(pagestream.SignalPatch{"pageStreamRecovery": false}); err != nil {
				return
			}
			updates.Wait(r.Context())
			return
		}
	}
	updates := pagestream.NewSignalStream(w, r)
	if !snapshot {
		if err := updates.Patch(builderLoadingPatch(builder)); err != nil {
			return
		}
	}
	var envelope uisignals.DashboardBuilderEnvelope
	if snapshot {
		envelope, err = h.dashboardBuilderSnapshotEnvelope(r, project, actorID, builder, snapshotSignals.Runtime)
	} else {
		envelope = h.dashboardBuilderEnvelopeWithPreviewForProject(r.Context(), project, actorID, builder)
	}
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	if snapshot {
		// Preview work can outlive a manual save. Fence the response before
		// publishing either half of the clear-and-replace signal update.
		latestBuilder, latestErr := h.Authoring.Builder(r.Context(), builderview.Request{
			ProjectID: project, ActorID: actorID, DashboardID: authoring.DashboardID(dashboardID),
			SelectedPageID: selectedPageID, SelectedVisualID: selectedVisualID,
		})
		if latestErr != nil || !sameDashboardBuilderRevision(builder, latestBuilder) {
			return
		}
		if snapshotSignals.Runtime.ClientID != nil && h.SessionStore != nil {
			servingStateID := optionalRuntimeValue(envelope.Runtime.ServingStateID)
			if !h.builderSnapshotFilterStateIsCurrent(r.Context(), r, builder, snapshotSignals.Runtime, envelope.BuilderFilterState, servingStateID) {
				return
			}
		}
	}
	envelope.Runtime.ClientID = uisignals.Optional(clientID)
	envelope.Runtime.StreamInstanceID = uisignals.Optional(streamInstanceID)
	envelope.Runtime.ProjectID = uisignals.Optional(project.String())
	envelope.Runtime.DashboardID = uisignals.Optional(dashboardID)
	envelope.Runtime.PageID = uisignals.Optional(firstBuilderPage(builder))
	bootstrap := ui.DashboardBuilderBootstrapSignals(envelope)
	if snapshot {
		// A one-shot Agent refresh replaces the complete visual graph. Datastar
		// merges nested objects, so clear old envelopes before publishing a new
		// chart type; otherwise stale union fields survive until a page reload.
		if err := updates.Patch(pagestream.SignalPatch{"builderVisuals": nil}); err != nil {
			return
		}
	}
	if snapshot || hasClientAgentState(r) {
		delete(bootstrap, "agent")
		delete(bootstrap, "agentVisuals")
	} else if h.AgentBootstrap != nil {
		agentState := h.AgentBootstrap(r, project.String())
		bootstrap["agent"] = agentState.Agent
		bootstrap["agentVisuals"] = agentState.Visuals
	}
	if err := updates.Patch(bootstrap); err != nil {
		return
	}
	if !snapshot {
		updates.Wait(r.Context())
	}
}

func sameDashboardBuilderRevision(left, right uisignals.DashboardBuilderSignal) bool {
	return left.DraftID == right.DraftID &&
		left.Revision.ID == right.Revision.ID &&
		left.Revision.Number == right.Revision.Number &&
		left.Revision.ContentHash == right.Revision.ContentHash
}

func (h Handler) dashboardBuilderSnapshotEnvelope(r *nethttp.Request, project projectgraph.ResourceID, actorID string, builder uisignals.DashboardBuilderSignal, runtime uisignals.RouteRuntimeSignal) (uisignals.DashboardBuilderEnvelope, error) {
	filters := dashboard.Filters{}
	servingStateID := ""
	generation := ""
	if h.SessionStore != nil && runtime.ClientID != nil {
		request, err := h.builderFilterRequest(r, builderFilterSignals{Builder: builder, Runtime: runtime})
		if err != nil {
			return uisignals.DashboardBuilderEnvelope{}, err
		}
		compiled, err := h.Authoring.Compile(h.analyticalContext(r.Context()), preview.CompileRequest{
			ProjectID: project, ActorID: actorID,
			DashboardID: authoring.DashboardID(builder.DashboardID), DraftID: authoring.DraftID(builder.DraftID),
			ExpectedRevision: request.Revision,
		})
		if err != nil {
			return uisignals.DashboardBuilderEnvelope{}, err
		}
		generation = strings.TrimSpace(compiled.SemanticEvidence.Identity.GenerationID)
		servingStateID, err = builderActiveServingStateID(builder, compiled.SemanticEvidence.Identity.GenerationID, optionalRuntimeValue(runtime.ServingStateID))
		if err != nil {
			return uisignals.DashboardBuilderEnvelope{}, err
		}
		request.Key.ServingStateID = servingStateID
		record, loadErr := h.SessionStore.Load(r.Context(), request.Key)
		if errors.Is(loadErr, dashboardsession.ErrNotFound) {
			record, loadErr = h.ensureBuilderFilterSession(r.Context(), request.Key, request.PageID, compiled.Definition)
		}
		if loadErr != nil {
			return uisignals.DashboardBuilderEnvelope{}, loadErr
		}
		record, err = h.reconcileBuilderFilterSession(r.Context(), record, compiled.Definition)
		if err != nil {
			return uisignals.DashboardBuilderEnvelope{}, err
		}
		state := record.State.Filters.State
		filters = dashboard.Filters{CompiledState: &state, ActivePageID: firstBuilderPage(builder), ServingStateID: servingStateID}
	}

	envelope := h.dashboardBuilderEnvelopeWithTargetPreviewAndFiltersForProject(r.Context(), project, actorID, builder, "", filters)
	if generation != "" && servingStateID != "" {
		previewServingStateID := optionalRuntimeValue(envelope.Runtime.ServingStateID)
		if !strings.HasSuffix(previewServingStateID, ":generation:"+generation) {
			return uisignals.DashboardBuilderEnvelope{}, authoring.ErrStaleRevision
		}
	}
	if filters.CompiledState != nil {
		envelope.BuilderFilterState = uisignals.DashboardFilterStateFromDomain(*filters.CompiledState)
		envelope.BuilderFilterValidation = uisignals.DashboardFilterValidationResult{Accepted: true, CurrentRevision: int64(filters.CompiledState.Revision)}
	}
	if servingStateID != "" {
		// Preserve the session identity resolved above across authored revisions
		// within the same draft and semantic runtime generation.
		envelope.Runtime.ServingStateID = uisignals.Optional(servingStateID)
		for visualID, visual := range envelope.BuilderVisuals {
			visual.ServingStateID = servingStateID
			envelope.BuilderVisuals[visualID] = visual
		}
	}
	return envelope, nil
}

func (h Handler) builderSnapshotFilterStateIsCurrent(ctx context.Context, r *nethttp.Request, builder uisignals.DashboardBuilderSignal, runtime uisignals.RouteRuntimeSignal, expected uisignals.DashboardFilterState, servingStateID string) bool {
	request, err := h.builderFilterRequest(r, builderFilterSignals{Builder: builder, Runtime: runtime})
	if err != nil {
		return false
	}
	request.Key.ServingStateID = servingStateID
	record, err := h.SessionStore.Load(ctx, request.Key)
	if errors.Is(err, dashboardsession.ErrNotFound) {
		return expected.Revision == 0
	}
	return err == nil && int64(record.State.Filters.State.Revision) == expected.Revision
}
