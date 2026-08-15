package http

import (
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/access"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/builderview"
	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	"github.com/flidai/leapview/internal/dashboard/ui"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	uicommand "github.com/flidai/leapview/internal/platform/web/uicommand"
	"github.com/go-chi/chi/v5"
)

const dashboardBuilderOperationID = dashboardgen.GenOperationExecuteDashboardAuthoringCommand

var dashboardBuilderCommandBinding = dashboardgen.GenUIActionExecuteDashboardAuthoringCommand()

// DashboardBuilder serves the governed draft builder document shell. The
// application boundary authorizes before loading the draft revision.
func (h Handler) DashboardBuilder(w nethttp.ResponseWriter, r *nethttp.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "workspace"))
	dashboardID := strings.TrimSpace(chi.URLParam(r, "dashboard"))
	actorID := h.currentActor(r)
	if workspaceID == "" || dashboardID == "" || actorID == "" || h.Authoring == nil {
		writeBuilderError(w, r, access.ErrForbidden)
		return
	}
	builder, err := h.Authoring.Builder(r.Context(), builderview.Request{
		WorkspaceID: workspaceID, ActorID: actorID, DashboardID: authoring.DashboardID(dashboardID),
	})
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	if requestedDraft := strings.TrimSpace(r.URL.Query().Get("draft")); requestedDraft != "" && requestedDraft != builder.DraftID {
		writeBuilderError(w, r, authoring.ErrStaleRevision)
		return
	}
	envelope := dashboardBuilderEnvelope(builder)
	csrfToken := ""
	if h.CSRFToken != nil {
		csrfToken = h.CSRFToken(r)
	}
	var providers []webpage.Provider
	if h.Layout != nil {
		providers = []webpage.Provider{h.Layout(r)}
	}
	if err := ui.DashboardBuilderPage(envelope, csrfToken, ui.DashboardBuilderActionBindings{
		BackHref:       "/workspaces/" + url.PathEscape(workspaceID),
		PreviewHref:    dashboardBuilderPreviewPath(workspaceID, dashboardID, builder),
		ExportYAMLHref: dashboardBuilderBasePath(workspaceID, dashboardID) + "/export.yaml",
		CommandPath:    dashboardBuilderBasePath(workspaceID, dashboardID) + "/draft/command",
		CommandBinding: dashboardBuilderCommandBinding,
	}, providers...).Render(w); err != nil {
		nethttp.Error(w, "dashboard builder unavailable", nethttp.StatusInternalServerError)
	}
}

// DashboardBuilderUpdates emits the typed builder projection on the canonical
// Datastar page stream. It intentionally does not accept a client-selected
// revision; the application resolves the current authorized draft.
func (h Handler) DashboardBuilderUpdates(w nethttp.ResponseWriter, r *nethttp.Request) {
	workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace"))
	dashboardID := strings.TrimSpace(r.URL.Query().Get("dashboard"))
	actorID := h.currentActor(r)
	if workspaceID == "" || dashboardID == "" || actorID == "" || h.Authoring == nil {
		writeBuilderError(w, r, access.ErrForbidden)
		return
	}
	builder, err := h.Authoring.Builder(r.Context(), builderview.Request{
		WorkspaceID: workspaceID, ActorID: actorID, DashboardID: authoring.DashboardID(dashboardID),
		SelectedPageID: strings.TrimSpace(r.URL.Query().Get("page")), SelectedVisualID: strings.TrimSpace(r.URL.Query().Get("visual")),
	})
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	if requestedDraft := strings.TrimSpace(r.URL.Query().Get("draft")); requestedDraft != "" && requestedDraft != builder.DraftID {
		writeBuilderError(w, r, authoring.ErrStaleRevision)
		return
	}
	clientID := pagestream.EnsureClientID(w, r)
	streamID := "dashboard_builder:" + clientID + ":" + workspaceID + ":" + dashboardID
	updates := pagestream.NewSignalStream(w, r, pagestream.WithStreamTrace(h.traceStore(), streamID, "dashboard_builder.bootstrap"))
	if err := updates.Patch(ui.DashboardBuilderBootstrapSignals(dashboardBuilderEnvelope(builder))); err != nil {
		return
	}
	<-r.Context().Done()
}

// DashboardBuilderCommand accepts the bounded builder intents and routes them
// through the application intent service. The HTTP handler only translates
// explicit wire fields into the closed authoring command union; it never
// rewrites a dashboard document.
func (h Handler) DashboardBuilderCommand(w nethttp.ResponseWriter, r *nethttp.Request) {
	if err := uicommand.VerifyClaim(uicommand.OperationClaims(r), dashboardBuilderOperationID); err != nil {
		nethttp.Error(w, "invalid dashboard builder command claim", nethttp.StatusBadRequest)
		return
	}
	if h.Authoring == nil {
		writeBuilderError(w, r, errors.New("dashboard authoring application is unavailable"))
		return
	}
	var signals struct {
		BuilderCommand *dashboardBuilderCommandSignal `json:"builderCommand"`
	}
	if err := pagestream.ReadSignals(r, &signals); err != nil || signals.BuilderCommand == nil {
		nethttp.Error(w, "dashboard builder command payload is required", nethttp.StatusBadRequest)
		return
	}
	input := signals.BuilderCommand
	workspaceID := strings.TrimSpace(chi.URLParam(r, "workspace"))
	dashboardID := strings.TrimSpace(chi.URLParam(r, "dashboard"))
	if workspaceID == "" || dashboardID == "" || (input.WorkspaceID != "" && input.WorkspaceID != workspaceID) || (input.DashboardID != "" && input.DashboardID != dashboardID) {
		nethttp.Error(w, "dashboard builder command scope is invalid", nethttp.StatusBadRequest)
		return
	}
	actorID := h.currentActor(r)
	if actorID == "" {
		writeBuilderError(w, r, access.ErrForbidden)
		return
	}
	command, err := input.authoringCommand(r, actorID, workspaceID, dashboardID)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	if command.IsBuilderIntent() {
		_, err = h.Authoring.ExecuteIntent(r.Context(), application.IntentRequest{WorkspaceID: workspaceID, ActorID: actorID, Command: command})
	} else {
		_, err = h.Authoring.Execute(r.Context(), workspaceID, command)
	}
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	// Re-project after a successful mutation so the browser receives the
	// repository-authoritative revision and save state, including idempotent
	// replays.
	builder, err := h.Authoring.Builder(r.Context(), builderview.Request{
		WorkspaceID: workspaceID, ActorID: actorID, DashboardID: authoring.DashboardID(dashboardID),
		SelectedPageID: input.PageID, SelectedVisualID: input.VisualID,
	})
	if err != nil {
		// The mutation succeeded, but a read-side runtime failure must not be
		// mistaken for a command conflict.
		writeBuilderError(w, r, err)
		return
	}
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{"builder": builder, "status": uisignals.DashboardStatus{Loading: false}})
}

// DashboardBuilderPreview renders one exact draft revision as JSON. No
// revision is defaulted: callers must identify the token they preview.
func (h Handler) DashboardBuilderPreview(w nethttp.ResponseWriter, r *nethttp.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "workspace"))
	dashboardID := strings.TrimSpace(chi.URLParam(r, "dashboard"))
	actorID := h.currentActor(r)
	revision, err := revisionFromQuery(r.URL.Query())
	if err != nil || workspaceID == "" || dashboardID == "" || actorID == "" || h.Authoring == nil {
		if err == nil {
			err = access.ErrForbidden
		}
		writeBuilderError(w, r, err)
		return
	}
	result, err := h.Authoring.Preview(r.Context(), preview.PreviewRequest{
		WorkspaceID: workspaceID, ActorID: actorID, DashboardID: authoring.DashboardID(dashboardID),
		DraftID:          authoring.DraftID(strings.TrimSpace(r.URL.Query().Get("draft"))),
		ExpectedRevision: revision, PageID: strings.TrimSpace(r.URL.Query().Get("page")),
	})
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	writeJSON(w, nethttp.StatusOK, result)
}

// DashboardBuilderExportYAML downloads canonical authored YAML through the
// source adapter. Export authorization remains inside Application.ExportYAML.
func (h Handler) DashboardBuilderExportYAML(w nethttp.ResponseWriter, r *nethttp.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "workspace"))
	dashboardID := strings.TrimSpace(chi.URLParam(r, "dashboard"))
	actorID := h.currentActor(r)
	if workspaceID == "" || dashboardID == "" || actorID == "" || h.Authoring == nil {
		writeBuilderError(w, r, access.ErrForbidden)
		return
	}
	yaml, err := h.Authoring.ExportYAML(r.Context(), sourceadapter.ExportRequest{
		Source:  sourceadapter.SourceRef{Kind: sourceadapter.SourceWorkspace, WorkspaceID: workspaceID, DashboardID: authoring.DashboardID(dashboardID)},
		ActorID: actorID,
	})
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+url.PathEscape(dashboardID)+`.yaml"`)
	w.WriteHeader(nethttp.StatusOK)
	_, _ = w.Write(yaml)
}

type dashboardBuilderCommandSignal struct {
	WorkspaceID         string          `json:"workspaceId"`
	DashboardID         string          `json:"dashboardId"`
	DraftID             string          `json:"draftId"`
	RevisionID          string          `json:"revisionId"`
	RevisionNumber      json.RawMessage `json:"revisionNumber"`
	RevisionContentHash string          `json:"revisionContentHash"`
	PageID              string          `json:"pageId"`
	VisualID            string          `json:"visualId"`
	ComponentID         string          `json:"componentId"`
	FieldID             string          `json:"fieldId"`
	Role                string          `json:"role"`
	Type                string          `json:"type"`
	Title               string          `json:"title"`
	Visibility          string          `json:"visibility"`
	Action              string          `json:"action"`
}

func (s dashboardBuilderCommandSignal) authoringCommand(r *nethttp.Request, actorID, workspaceID, dashboardID string) (authoring.Command, error) {
	action := strings.TrimSpace(s.Action)
	if strings.TrimSpace(s.DraftID) == "" || strings.TrimSpace(s.RevisionID) == "" || strings.TrimSpace(s.RevisionContentHash) == "" {
		return authoring.Command{}, fmt.Errorf("draft id and complete expected revision are required")
	}
	number, err := parseRevisionNumber(s.RevisionNumber)
	if err != nil {
		return authoring.Command{}, err
	}
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if requestID == "" {
		requestID = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	}
	if requestID == "" {
		return authoring.Command{}, fmt.Errorf("X-Request-ID is required")
	}
	command := authoring.Command{
		ID: authoring.CommandID(requestID), DashboardID: authoring.DashboardID(dashboardID), DraftID: authoring.DraftID(s.DraftID),
		ExpectedRevision: authoring.RevisionToken{RevisionID: authoring.RevisionID(s.RevisionID), Number: number, ContentHash: s.RevisionContentHash},
		Provenance:       authoring.Provenance{Origin: authoring.OriginUI, ActorID: actorID},
	}
	switch action {
	case "publish":
		command.Publish = &authoring.PublishPayload{}
	case "set_visibility":
		visibility := authoring.Visibility(strings.TrimSpace(s.Visibility))
		if err := visibility.Validate(); err != nil {
			return authoring.Command{}, err
		}
		command.SetVisibility = &authoring.SetVisibilityPayload{Visibility: visibility}
	case "add_page":
		command.AddPage = &authoring.AddPagePayload{PageID: strings.TrimSpace(s.PageID), Title: strings.TrimSpace(s.Title)}
	case "add_visual":
		command.AddVisual = &authoring.AddVisualPayload{PageID: strings.TrimSpace(s.PageID), VisualID: strings.TrimSpace(s.VisualID), ComponentID: strings.TrimSpace(s.ComponentID), Type: strings.TrimSpace(s.Type), Title: strings.TrimSpace(s.Title)}
	case "assign_field":
		command.AssignField = &authoring.AssignFieldPayload{PageID: strings.TrimSpace(s.PageID), VisualID: strings.TrimSpace(s.VisualID), FieldID: strings.TrimSpace(s.FieldID), Role: authoring.FieldRole(strings.TrimSpace(s.Role))}
	default:
		return authoring.Command{}, fmt.Errorf("unsupported dashboard builder action %q", s.Action)
	}
	return command, nil
}

func parseRevisionNumber(raw json.RawMessage) (uint64, error) {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return 0, fmt.Errorf("revision number is required")
	}
	value = strings.Trim(value, `"`)
	number, err := strconv.ParseUint(value, 10, 64)
	if err != nil || number == 0 {
		return 0, fmt.Errorf("revision number must be a positive integer")
	}
	return number, nil
}

func revisionFromQuery(values url.Values) (authoring.RevisionToken, error) {
	number, err := parseRevisionNumber(json.RawMessage(strconv.Quote(values.Get("revisionNumber"))))
	if err != nil {
		return authoring.RevisionToken{}, err
	}
	id, hash := strings.TrimSpace(values.Get("revisionId")), strings.TrimSpace(values.Get("revisionContentHash"))
	if id == "" || hash == "" {
		return authoring.RevisionToken{}, fmt.Errorf("complete preview revision is required")
	}
	token := authoring.RevisionToken{RevisionID: authoring.RevisionID(id), Number: number, ContentHash: hash}
	if err := token.ValidateComplete(); err != nil {
		return authoring.RevisionToken{}, err
	}
	return token, nil
}

func dashboardBuilderEnvelope(builder uisignals.DashboardBuilderSignal) uisignals.DashboardBuilderEnvelope {
	return uisignals.DashboardBuilderEnvelope{
		Builder: builder,
		Runtime: uisignals.RouteRuntimeSignal{Kind: uisignals.RouteKindDashboardBuilder, WorkspaceID: uisignals.Optional(builder.WorkspaceID), DashboardID: uisignals.Optional(builder.DashboardID)},
		Status:  uisignals.DashboardStatus{Loading: false},
	}
}

func dashboardBuilderBasePath(workspaceID, dashboardID string) string {
	return "/workspaces/" + url.PathEscape(workspaceID) + "/dashboards/" + url.PathEscape(dashboardID)
}

func dashboardBuilderPreviewPath(workspaceID, dashboardID string, builder uisignals.DashboardBuilderSignal) string {
	values := url.Values{}
	values.Set("page", firstBuilderPage(builder))
	values.Set("draft", builder.DraftID)
	values.Set("revisionId", builder.Revision.ID)
	values.Set("revisionNumber", strconv.FormatInt(builder.Revision.Number, 10))
	values.Set("revisionContentHash", builder.Revision.ContentHash)
	return dashboardBuilderBasePath(workspaceID, dashboardID) + "/preview?" + values.Encode()
}

func firstBuilderPage(builder uisignals.DashboardBuilderSignal) string {
	if builder.SelectedPageID != nil && strings.TrimSpace(*builder.SelectedPageID) != "" {
		return *builder.SelectedPageID
	}
	if len(builder.Pages) > 0 {
		return builder.Pages[0].ID
	}
	return ""
}

func (h Handler) currentActor(r *nethttp.Request) string {
	if h.CurrentPrincipalID == nil {
		return ""
	}
	return strings.TrimSpace(h.CurrentPrincipalID(r))
}

func (h Handler) traceStore() *pagestream.TraceStore {
	if h.Broker == nil {
		return nil
	}
	return h.Broker.TraceStore()
}

func writeBuilderError(w nethttp.ResponseWriter, _ *nethttp.Request, err error) {
	status := nethttp.StatusInternalServerError
	switch {
	case errors.Is(err, access.ErrForbidden):
		status = nethttp.StatusForbidden
	case errors.Is(err, authoring.ErrNotFound):
		status = nethttp.StatusNotFound
	case errors.Is(err, authoring.ErrStaleRevision):
		status = nethttp.StatusConflict
	case errors.Is(err, authoring.ErrConflict):
		status = nethttp.StatusConflict
	case errors.Is(err, authoring.ErrInvalidAuthoring), errors.Is(err, authoring.ErrInvalidIdentifier), errors.Is(err, authoring.ErrInvalidPayload):
		status = nethttp.StatusBadRequest
	}
	message := "dashboard builder unavailable"
	switch status {
	case nethttp.StatusForbidden:
		message = "forbidden"
	case nethttp.StatusNotFound:
		message = "dashboard builder not found"
	case nethttp.StatusConflict:
		message = "dashboard builder revision is stale"
		if !errors.Is(err, authoring.ErrStaleRevision) {
			message = "dashboard builder conflict"
		}
	case nethttp.StatusBadRequest:
		if err != nil {
			message = err.Error()
		}
	}
	nethttp.Error(w, message, status)
}
