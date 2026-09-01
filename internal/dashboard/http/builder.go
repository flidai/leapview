package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/flidai/leapview/internal/access"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/builderview"
	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/dashboard/ui"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	httpmiddleware "github.com/flidai/leapview/internal/platform/http/middleware"
	httptransport "github.com/flidai/leapview/internal/platform/http/transport"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	webtransport "github.com/flidai/leapview/internal/platform/web/transport"
	uicommand "github.com/flidai/leapview/internal/platform/web/uicommand"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/pagestream"
	"github.com/go-chi/chi/v5"
)

const dashboardBuilderOperationID = dashboardgen.GenOperationExecuteDashboardAuthoringCommand

var dashboardBuilderCommandBinding = dashboardgen.GenUIActionExecuteDashboardAuthoringCommand()

// DashboardBuilder serves the governed draft builder document shell. The
// application boundary authorizes before loading the draft revision.
func (h Handler) DashboardBuilder(w nethttp.ResponseWriter, r *nethttp.Request) {
	project, projectErr := h.projectIDForRequest(r.Context())
	if projectErr != nil {
		writeBuilderError(w, r, projectErr)
		return
	}
	projectID := project.String()
	dashboardID := strings.TrimSpace(chi.URLParam(r, "dashboard"))
	actorID := h.currentActor(r)
	if projectID == "" || dashboardID == "" || actorID == "" || h.Authoring == nil {
		writeBuilderError(w, r, access.ErrForbidden)
		return
	}
	builder, err := h.Authoring.Builder(r.Context(), builderview.Request{
		ProjectID: project, ActorID: actorID, DashboardID: authoring.DashboardID(dashboardID),
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
		BackHref:          "/",
		PreviewHref:       dashboardBuilderPreviewPath(dashboardID, builder),
		ExportYAMLHref:    dashboardBuilderDraftRoute(dashboardID, builder.DraftID, "/export.yaml"),
		PageBaseHref:      dashboardBuilderDraftRoute(dashboardID, builder.DraftID, "/edit"),
		CommandPath:       dashboardBuilderDraftRoute(dashboardID, builder.DraftID, "/draft/command"),
		CommandBinding:    dashboardBuilderCommandBinding,
		FilterCommandPath: dashboardBuilderDraftRoute(dashboardID, builder.DraftID, "/draft/filter"),
		FilterOptionPath:  dashboardBuilderDraftRoute(dashboardID, builder.DraftID, "/draft/filter-options"),
	}, providers...).Render(w); err != nil {
		nethttp.Error(w, "dashboard builder unavailable", nethttp.StatusInternalServerError)
	}
}

// DashboardDraftCreate keeps the deep-link entry point and executes the
// catalog modal's bounded form. The application still generates identities
// and the initial authored document transactionally.
func (h Handler) DashboardDraftCreate(w nethttp.ResponseWriter, r *nethttp.Request) {
	project, err := h.projectIDForRequest(r.Context())
	if err != nil || h.Authoring == nil || h.currentActor(r) == "" {
		writeBuilderError(w, r, access.ErrForbidden)
		return
	}
	if r.Method == nethttp.MethodGet {
		values := url.Values{"create": []string{"dashboard"}}
		if semanticModel := strings.TrimSpace(r.URL.Query().Get("semanticModel")); semanticModel != "" {
			values.Set("semanticModel", semanticModel)
		}
		nethttp.Redirect(w, r, "/?"+values.Encode(), nethttp.StatusSeeOther)
		return
	}
	creator, ok := h.Authoring.(browserDraftCreator)
	if !ok {
		writeBuilderError(w, r, errors.New("dashboard authoring create operation is unavailable"))
		return
	}
	if err := r.ParseForm(); err != nil {
		writeBuilderError(w, r, fmt.Errorf("read dashboard draft form: %w", err))
		return
	}
	idempotencyKey, err := browserFormRequestID(r)
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	semanticModel, err := projectgraph.NewResourceID(strings.TrimSpace(r.FormValue("semanticModel")))
	if err != nil {
		writeBuilderError(w, r, fmt.Errorf("%w: semantic model: %v", authoring.ErrInvalidAuthoring, err))
		return
	}
	if h.Metrics != nil && !dashboardSemanticModelAvailable(h.dashboardSemanticModelOptions(), semanticModel.String()) {
		writeBuilderError(w, r, fmt.Errorf("%w: select an available data model", authoring.ErrInvalidPayload))
		return
	}
	actor := h.currentActor(r)
	result, err := creator.Create(r.Context(), authoringservice.CreateRequest{
		ProjectID: project, ActorID: actor, Title: strings.TrimSpace(r.FormValue("title")), Slug: strings.TrimSpace(r.FormValue("slug")),
		SemanticModel: semanticModel, Visibility: authoring.VisibilityPrivate, Origin: authoring.OriginUI, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	if err := result.Lifecycle.Validate(); err != nil {
		writeBuilderError(w, r, err)
		return
	}
	draftID := ""
	if result.Lifecycle.Draft != nil {
		draftID = result.Lifecycle.Draft.ID.String()
	}
	nethttp.Redirect(w, r, dashboardBuilderDraftRoute(result.Lifecycle.ID.String(), draftID, "/edit"), nethttp.StatusSeeOther)
}

type dashboardSemanticModelOption struct {
	ID    string
	Title string
}

func (h Handler) dashboardSemanticModelOptions() []dashboardSemanticModelOption {
	if h.Metrics == nil {
		return nil
	}
	models := h.Metrics.Catalog().Models
	options := make([]dashboardSemanticModelOption, 0, len(models))
	for _, model := range models {
		id := strings.TrimSpace(model.ID.String())
		if id == "" {
			continue
		}
		title := strings.TrimSpace(model.Title)
		if title == "" {
			title = id
		}
		title = humanizeDashboardDataModelTitle(title)
		options = append(options, dashboardSemanticModelOption{ID: id, Title: title})
	}
	sort.Slice(options, func(i, j int) bool {
		if options[i].Title == options[j].Title {
			return options[i].ID < options[j].ID
		}
		return options[i].Title < options[j].Title
	})
	return options
}

func humanizeDashboardDataModelTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" || title != strings.ToLower(title) {
		return title
	}
	words := strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(title))
	for index, word := range words {
		runes := []rune(word)
		if len(runes) == 0 {
			continue
		}
		runes[0] = unicode.ToUpper(runes[0])
		words[index] = string(runes)
	}
	return strings.Join(words, " ")
}

func dashboardSemanticModelAvailable(options []dashboardSemanticModelOption, selected string) bool {
	selected = strings.TrimSpace(selected)
	for _, option := range options {
		if strings.TrimSpace(option.ID) == selected {
			return true
		}
	}
	return false
}

// DashboardDraftFork renders and executes the browser fork action. Forks use
// the active project source so provenance records exact serving evidence; the
// source remains unchanged and production approval/activation stay outside
// the browser workflow.
func (h Handler) DashboardDraftFork(w nethttp.ResponseWriter, r *nethttp.Request) {
	project, err := h.projectIDForRequest(r.Context())
	if err != nil || h.Authoring == nil || h.currentActor(r) == "" {
		writeBuilderError(w, r, access.ErrForbidden)
		return
	}
	dashboardID := strings.TrimSpace(chi.URLParam(r, "dashboard"))
	if r.Method == nethttp.MethodGet {
		csrfToken := ""
		if h.CSRFToken != nil {
			csrfToken = h.CSRFToken(r)
		}
		if err := ui.DashboardDraftForkPageWithKey(dashboardID, csrfToken, dashboardBuilderBasePath(dashboardID)+"/fork", httptransport.NewRequestID()).Render(w); err != nil {
			nethttp.Error(w, "dashboard fork unavailable", nethttp.StatusInternalServerError)
		}
		return
	}
	creator, ok := h.Authoring.(browserDraftCreator)
	if !ok {
		writeBuilderError(w, r, errors.New("dashboard authoring fork operation is unavailable"))
		return
	}
	if err := r.ParseForm(); err != nil {
		writeBuilderError(w, r, fmt.Errorf("read dashboard fork form: %w", err))
		return
	}
	idempotencyKey, err := browserFormRequestID(r)
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	result, err := creator.Fork(r.Context(), sourceadapter.ForkRequest{
		Source:          sourceadapter.SourceRef{Kind: sourceadapter.SourceProject, ProjectID: project, DashboardID: authoring.DashboardID(dashboardID)},
		TargetProjectID: project, ActorID: h.currentActor(r), Title: strings.TrimSpace(r.FormValue("title")), Slug: strings.TrimSpace(r.FormValue("slug")),
		Origin: authoring.OriginUI, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	if err := result.Lifecycle.Validate(); err != nil {
		writeBuilderError(w, r, err)
		return
	}
	draftID := ""
	if result.Lifecycle.Draft != nil {
		draftID = result.Lifecycle.Draft.ID.String()
	}
	nethttp.Redirect(w, r, dashboardBuilderDraftRoute(result.Lifecycle.ID.String(), draftID, "/edit"), nethttp.StatusSeeOther)
}

func browserFormRequestID(r *nethttp.Request) (string, error) {
	value := strings.TrimSpace(r.FormValue("idempotencyKey"))
	if value == "" {
		return "", fmt.Errorf("%w: idempotencyKey is required", authoring.ErrInvalidPayload)
	}
	return value, nil
}

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
	actorID := h.currentActor(r)
	if projectID == "" || dashboardID == "" || actorID == "" || h.Authoring == nil {
		writeBuilderError(w, r, access.ErrForbidden)
		return
	}
	builder, err := h.Authoring.Builder(r.Context(), builderview.Request{
		ProjectID: project, ActorID: actorID, DashboardID: authoring.DashboardID(dashboardID),
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
	clientID, ok := webtransport.RequireClientID(w, r)
	if !ok {
		return
	}
	streamInstanceID := strings.TrimSpace(r.URL.Query().Get("streamInstance"))
	if streamInstanceID == "" {
		streamInstanceID = clientID
	}
	envelope := h.dashboardBuilderEnvelopeWithPreviewForProject(r.Context(), project, actorID, builder)
	envelope.Runtime.ClientID = uisignals.Optional(clientID)
	envelope.Runtime.StreamInstanceID = uisignals.Optional(streamInstanceID)
	envelope.Runtime.ProjectID = uisignals.Optional(project.String())
	envelope.Runtime.DashboardID = uisignals.Optional(dashboardID)
	envelope.Runtime.PageID = uisignals.Optional(firstBuilderPage(builder))
	updates := pagestream.NewSignalStream(w, r)
	if err := updates.Patch(ui.DashboardBuilderBootstrapSignals(envelope)); err != nil {
		return
	}
	updates.Wait(r.Context())
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
		// Datastar command posts normally include only builderCommand, but
		// retain runtime when a caller sends the complete route envelope. The
		// response below also reconstructs this identity from the request and
		// server-bound route so command patches never clear stream metadata.
		Runtime uisignals.RouteRuntimeSignal `json:"runtime"`
	}
	if err := pagestream.ReadSignals(r, &signals); err != nil || signals.BuilderCommand == nil {
		nethttp.Error(w, "dashboard builder command payload is required", nethttp.StatusBadRequest)
		return
	}
	input := signals.BuilderCommand
	project, err := h.projectIDForRequest(r.Context())
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusForbidden)
		return
	}
	projectID := project.String()
	dashboardID := strings.TrimSpace(chi.URLParam(r, "dashboard"))
	if projectID == "" || dashboardID == "" || (input.DashboardID != "" && input.DashboardID != dashboardID) {
		nethttp.Error(w, "dashboard builder command scope is invalid", nethttp.StatusBadRequest)
		return
	}
	actorID := h.currentActor(r)
	if actorID == "" {
		writeBuilderError(w, r, access.ErrForbidden)
		return
	}
	command, err := input.authoringCommand(r, actorID, dashboardID)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	if command.IsBuilderIntent() {
		_, err = h.Authoring.ExecuteIntent(r.Context(), application.IntentRequest{ProjectID: project, ActorID: actorID, Command: command})
	} else {
		_, err = h.Authoring.Execute(r.Context(), project, command)
	}
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	if command.Archive != nil {
		_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{"builder": map[string]any{"redirectTo": "/"}})
		return
	}
	// Re-project after a successful mutation so the browser receives the
	// repository-authoritative revision and save state, including idempotent
	// replays.
	builder, err := h.Authoring.Builder(r.Context(), builderview.Request{
		ProjectID: project, ActorID: actorID, DashboardID: authoring.DashboardID(dashboardID),
		SelectedPageID: input.PageID, SelectedVisualID: input.VisualID,
	})
	if err != nil {
		// The mutation succeeded, but a read-side runtime failure must not be
		// mistaken for a command conflict.
		writeBuilderError(w, r, err)
		return
	}
	envelope := h.dashboardBuilderEnvelopeWithPreviewForProject(r.Context(), project, actorID, builder)
	envelope.Runtime = h.builderCommandRuntime(r, signals.Runtime, envelope.Runtime, project.String(), dashboardID, input.PageID, builder)
	// Datastar applies JSON merge-patch semantics. A complete visualization
	// envelope is a discriminated union, so merging a Table envelope into a Pie
	// envelope would retain stale mark/category/value keys and make the result
	// invalid until reload. Clear the preview map first, then publish the
	// authoritative replacement as a second patch in the same response. This
	// also removes previews for visuals deleted by the command.
	updates := pagestream.NewSignalStream(w, r)
	if err := updates.Patch(pagestream.SignalPatch{"builderVisuals": nil}); err != nil {
		return
	}
	_ = updates.Patch(pagestream.SignalPatch{
		"builder":                  envelope.Builder,
		"builderVisuals":           envelope.BuilderVisuals,
		"runtime":                  envelope.Runtime,
		"builderFilterContract":    envelope.BuilderFilterContract,
		"builderFilterState":       envelope.BuilderFilterState,
		"builderFilterOptionPages": envelope.BuilderFilterOptionPages,
		"builderFilterValidation":  envelope.BuilderFilterValidation,
		"status":                   uisignals.DashboardStatus{Loading: false},
	})
}

// builderCommandRuntime preserves the route identity needed by the canonical
// page stream. Runtime project/dashboard/page values supplied by the browser
// are never trusted; project and dashboard come from the bound request while
// page falls back to the authoritative re-projected builder selection.
func (h Handler) builderCommandRuntime(r *nethttp.Request, supplied, runtime uisignals.RouteRuntimeSignal, projectID, dashboardID, requestedPage string, builder uisignals.DashboardBuilderSignal) uisignals.RouteRuntimeSignal {
	clientID := ""
	if supplied.ClientID != nil {
		clientID = webtransport.ClientIDFromRequest(r, *supplied.ClientID)
	} else {
		clientID = webtransport.ClientIDFromRequest(r, "")
	}
	streamID := ""
	if supplied.StreamInstanceID != nil {
		streamID = strings.TrimSpace(*supplied.StreamInstanceID)
	}
	if streamID == "" {
		streamID = strings.TrimSpace(r.URL.Query().Get("streamInstance"))
	}
	if streamID == "" {
		streamID = clientID
	}
	pageID := ""
	requestedPage = strings.TrimSpace(requestedPage)
	if requestedPage != "" && builderHasPage(builder, requestedPage) {
		pageID = requestedPage
	}
	if pageID == "" && builder.SelectedPageID != nil && builderHasPage(builder, strings.TrimSpace(*builder.SelectedPageID)) {
		pageID = strings.TrimSpace(*builder.SelectedPageID)
	}
	if pageID == "" && supplied.PageID != nil && builderHasPage(builder, strings.TrimSpace(*supplied.PageID)) {
		pageID = strings.TrimSpace(*supplied.PageID)
	}
	if pageID == "" {
		pageID = firstBuilderPage(builder)
	}
	runtime.Kind = uisignals.RouteKindDashboardBuilder
	runtime.ClientID = optionalRuntimeString(clientID)
	runtime.StreamInstanceID = optionalRuntimeString(streamID)
	runtime.ProjectID = optionalRuntimeString(projectID)
	runtime.DashboardID = optionalRuntimeString(dashboardID)
	runtime.PageID = optionalRuntimeString(pageID)
	return runtime
}

func builderHasPage(builder uisignals.DashboardBuilderSignal, pageID string) bool {
	pageID = strings.TrimSpace(pageID)
	if pageID == "" {
		return false
	}
	for _, page := range builder.Pages {
		if strings.TrimSpace(page.ID) == pageID {
			return true
		}
	}
	return false
}

func optionalRuntimeString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

// DashboardBuilderPreview renders one exact draft revision with the production
// dashboard component. No revision is defaulted: callers must identify the
// token they preview. The document shell bootstraps from a one-shot signal
// request to the same exact URL so preview data is never serialized into HTML.
func (h Handler) DashboardBuilderPreview(w nethttp.ResponseWriter, r *nethttp.Request) {
	request, dashboardID, err := h.dashboardBuilderPreviewRequest(r)
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	if r.URL.Query().Get("_signals") != "1" {
		compilation, compileErr := h.Authoring.Compile(h.analyticalContext(r.Context()), preview.CompileRequest{
			ProjectID: request.ProjectID, ActorID: request.ActorID, DashboardID: request.DashboardID,
			DraftID: request.DraftID, ExpectedRevision: request.ExpectedRevision,
		})
		if compileErr != nil {
			if errors.Is(compileErr, authoring.ErrStaleRevision) {
				h.writeDashboardBuilderPreviewRevisionChanged(w, r, dashboardID, request)
				return
			}
			writeBuilderError(w, r, compileErr)
			return
		}
		values := cloneURLValues(r.URL.Query())
		values.Set("_signals", "1")
		updatesURL := dashboardBuilderBasePath(dashboardID) + "/preview?" + values.Encode()
		backValues := url.Values{}
		backValues.Set("draft", string(request.DraftID))
		backValues.Set("page", request.PageID)
		backHref := dashboardBuilderBasePath(dashboardID) + "/edit?" + backValues.Encode()
		var providers []webpage.Provider
		if h.Layout != nil {
			providers = []webpage.Provider{h.Layout(r)}
		}
		revisionLabel := fmt.Sprintf("Revision %d", request.ExpectedRevision.Number)
		title := strings.TrimSpace(compilation.Definition.Title)
		if title == "" {
			title = "Draft dashboard"
		}
		if err := ui.DashboardDraftPreviewPage(title, dashboardID, request.PageID, revisionLabel, backHref, updatesURL, providers...).Render(w); err != nil {
			nethttp.Error(w, "dashboard draft preview unavailable", nethttp.StatusInternalServerError)
		}
		return
	}
	result, err := h.Authoring.Preview(h.analyticalContext(r.Context()), request)
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	pageHrefs := make(map[string]string, len(result.Definition.Pages))
	for _, page := range result.Definition.Pages {
		values := cloneURLValues(r.URL.Query())
		values.Del("_signals")
		values.Del("datastar")
		values.Set("page", page.ID)
		pageHrefs[page.ID] = dashboardBuilderBasePath(dashboardID) + "/preview?" + values.Encode()
	}
	signals, err := ui.DashboardDraftPreviewBootstrapSignals(result.Definition, result.PagePatch, request.PageID, result.SemanticEvidence.Identity.GenerationID, pageHrefs)
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	if err := pagestream.PatchResponse(w, r, pagestream.SignalPatch(signals)); err != nil {
		nethttp.Error(w, "dashboard draft preview unavailable", nethttp.StatusInternalServerError)
	}
}

// writeDashboardBuilderPreviewRevisionChanged keeps the exact-revision
// conflict status while replacing the otherwise opaque plain-text response
// with a branded browser recovery page. The link deliberately carries the
// requested draft and page so the builder can restore the user's context;
// callers still have to choose a fresh exact revision there.
func (h Handler) writeDashboardBuilderPreviewRevisionChanged(w nethttp.ResponseWriter, r *nethttp.Request, dashboardID string, request preview.PreviewRequest) {
	backValues := url.Values{}
	backValues.Set("draft", string(request.DraftID))
	backValues.Set("page", request.PageID)
	backHref := dashboardBuilderBasePath(dashboardID) + "/edit?" + backValues.Encode()
	var providers []webpage.Provider
	if h.Layout != nil {
		providers = []webpage.Provider{h.Layout(r)}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(nethttp.StatusConflict)
	if err := ui.DashboardDraftPreviewRevisionChangedPage(backHref, providers...).Render(w); err != nil {
		// The response status/body have already been started. There is no safe
		// way to replace them with net/http.Error at this point.
		return
	}
}

func (h Handler) dashboardBuilderPreviewRequest(r *nethttp.Request) (preview.PreviewRequest, string, error) {
	project, projectErr := h.projectIDForRequest(r.Context())
	projectID := project.String()
	dashboardID := strings.TrimSpace(chi.URLParam(r, "dashboard"))
	actorID := h.currentActor(r)
	draftID := strings.TrimSpace(r.URL.Query().Get("draft"))
	pageID := strings.TrimSpace(r.URL.Query().Get("page"))
	revision, err := revisionFromQuery(r.URL.Query())
	if projectErr != nil || err != nil || projectID == "" || dashboardID == "" || actorID == "" || h.Authoring == nil {
		if projectErr != nil {
			err = projectErr
		}
		if err == nil {
			err = access.ErrForbidden
		}
		return preview.PreviewRequest{}, dashboardID, err
	}
	if err := authoring.DraftID(draftID).Validate(); err != nil {
		return preview.PreviewRequest{}, dashboardID, err
	}
	if pageID == "" {
		return preview.PreviewRequest{}, dashboardID, fmt.Errorf("%w: preview page id is required", authoring.ErrInvalidPayload)
	}
	return preview.PreviewRequest{
		ProjectID: project, ActorID: actorID, DashboardID: authoring.DashboardID(dashboardID),
		DraftID:          authoring.DraftID(draftID),
		ExpectedRevision: revision, PageID: pageID, BestEffortVisuals: true,
	}, dashboardID, nil
}

func cloneURLValues(values url.Values) url.Values {
	clone := make(url.Values, len(values))
	for key, items := range values {
		clone[key] = append([]string(nil), items...)
	}
	return clone
}

// DashboardBuilderExportYAML downloads canonical authored YAML through the
// source adapter. Export authorization remains inside Application.ExportYAML.
func (h Handler) DashboardBuilderExportYAML(w nethttp.ResponseWriter, r *nethttp.Request) {
	project, projectErr := h.projectIDForRequest(r.Context())
	if projectErr != nil {
		writeBuilderError(w, r, projectErr)
		return
	}
	projectID := project.String()
	dashboardID := strings.TrimSpace(chi.URLParam(r, "dashboard"))
	actorID := h.currentActor(r)
	if projectID == "" || dashboardID == "" || actorID == "" || h.Authoring == nil {
		writeBuilderError(w, r, access.ErrForbidden)
		return
	}
	request := sourceadapter.ExportRequest{
		Source:  sourceadapter.SourceRef{Kind: sourceadapter.SourceInstance, ProjectID: project, DashboardID: authoring.DashboardID(dashboardID)},
		ActorID: actorID,
	}
	yaml, err := h.Authoring.ExportDraftYAML(r.Context(), request)
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
	DashboardID               string                            `json:"dashboardId"`
	DraftID                   string                            `json:"draftId"`
	RevisionID                string                            `json:"revisionId"`
	RevisionNumber            json.RawMessage                   `json:"revisionNumber"`
	RevisionContentHash       string                            `json:"revisionContentHash"`
	TargetRevisionID          string                            `json:"targetRevisionId"`
	TargetRevisionNumber      json.RawMessage                   `json:"targetRevisionNumber"`
	TargetRevisionContentHash string                            `json:"targetRevisionContentHash"`
	PageID                    string                            `json:"pageId"`
	NewPageID                 string                            `json:"newPageId"`
	VisualID                  string                            `json:"visualId"`
	TargetVisualID            string                            `json:"targetVisualId"`
	ComponentID               string                            `json:"componentId"`
	FieldID                   string                            `json:"fieldId"`
	FilterID                  string                            `json:"filterId"`
	Scope                     string                            `json:"scope"`
	Targets                   []string                          `json:"targets"`
	Dataset                   string                            `json:"dataset"`
	ControlType               string                            `json:"controlType"`
	Description               string                            `json:"description"`
	Required                  bool                              `json:"required"`
	ReaderEditable            bool                              `json:"readerEditable"`
	URLParameter              string                            `json:"urlParameter"`
	Role                      string                            `json:"role"`
	TargetRole                string                            `json:"targetRole"`
	Direction                 string                            `json:"direction"`
	Index                     *int                              `json:"index,omitempty"`
	Type                      string                            `json:"type"`
	Title                     string                            `json:"title"`
	NewVisualID               string                            `json:"newVisualId"`
	NewComponentID            string                            `json:"newComponentId"`
	TitleVisible              *bool                             `json:"titleVisible,omitempty"`
	LegendVisible             *bool                             `json:"legendVisible,omitempty"`
	AxisVisible               *bool                             `json:"axisVisible,omitempty"`
	DataLabelsVisible         *bool                             `json:"dataLabelsVisible,omitempty"`
	FormatKey                 string                            `json:"formatKey"`
	FormatValue               *string                           `json:"formatValue,omitempty"`
	Visibility                string                            `json:"visibility"`
	Icon                      string                            `json:"icon"`
	Color                     string                            `json:"color"`
	Placement                 *document.DashboardPlacement      `json:"placement,omitempty"`
	Placements                []dashboardBuilderPlacementSignal `json:"placements,omitempty"`
	Column                    int32                             `json:"column,omitempty"`
	Row                       int32                             `json:"row,omitempty"`
	ColumnSpan                int32                             `json:"columnSpan,omitempty"`
	RowSpan                   int32                             `json:"rowSpan,omitempty"`
	Col                       int32                             `json:"col,omitempty"`
	ColSpan                   int32                             `json:"colSpan,omitempty"`
	Columns                   int32                             `json:"columns,omitempty"`
	RowHeight                 int32                             `json:"rowHeight,omitempty"`
	Gap                       int32                             `json:"gap,omitempty"`
	Padding                   int32                             `json:"padding,omitempty"`
	Action                    string                            `json:"action"`
	Effect                    string                            `json:"effect"`
}

type dashboardBuilderPlacementSignal struct {
	ComponentID string                       `json:"componentId,omitempty"`
	VisualID    string                       `json:"visualId,omitempty"`
	Placement   *document.DashboardPlacement `json:"placement,omitempty"`
	Column      int32                        `json:"column,omitempty"`
	Row         int32                        `json:"row,omitempty"`
	ColumnSpan  int32                        `json:"columnSpan,omitempty"`
	RowSpan     int32                        `json:"rowSpan,omitempty"`
	Col         int32                        `json:"col,omitempty"`
	ColSpan     int32                        `json:"colSpan,omitempty"`
}

func (s dashboardBuilderPlacementSignal) placementUpdate() authoring.PlacementUpdate {
	componentID := strings.TrimSpace(s.ComponentID)
	if componentID == "" {
		componentID = strings.TrimSpace(s.VisualID)
	}
	column, columnSpan := s.Column, s.ColumnSpan
	if column == 0 {
		column = s.Col
	}
	if columnSpan == 0 {
		columnSpan = s.ColSpan
	}
	placement := document.DashboardPlacement{Column: column, Row: s.Row, ColumnSpan: columnSpan, RowSpan: s.RowSpan}
	if s.Placement != nil {
		placement = *s.Placement
	}
	return authoring.PlacementUpdate{ComponentID: componentID, Placement: placement}
}

func (s dashboardBuilderCommandSignal) authoringCommand(r *nethttp.Request, actorID, dashboardID string) (authoring.Command, error) {
	action := strings.TrimSpace(s.Action)
	if strings.TrimSpace(s.DraftID) == "" || strings.TrimSpace(s.RevisionID) == "" || strings.TrimSpace(s.RevisionContentHash) == "" {
		return authoring.Command{}, fmt.Errorf("draft id and complete expected revision are required")
	}
	number, err := parseRevisionNumber(s.RevisionNumber)
	if err != nil {
		return authoring.Command{}, err
	}
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if requestID == "" || httpmiddleware.RequestIDWasGenerated(r) {
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
	case "archive":
		command.Archive = &authoring.ArchivePayload{}
	case "set_visibility":
		visibility := authoring.Visibility(strings.TrimSpace(s.Visibility))
		if err := visibility.Validate(); err != nil {
			return authoring.Command{}, err
		}
		command.SetVisibility = &authoring.SetVisibilityPayload{Visibility: visibility}
	case "update_appearance":
		icon, color := strings.TrimSpace(s.Icon), strings.TrimSpace(s.Color)
		if err := dashboardappearance.ValidatePatch(dashboardappearance.Patch{Icon: &icon, Color: &color}); err != nil {
			return authoring.Command{}, err
		}
		documentColor := document.DashboardAppearanceColor(color)
		command.Metadata = &authoring.MetadataPatch{Appearance: &document.DashboardAppearance{Icon: &icon, Color: &documentColor}}
	case "add_page":
		command.AddPage = &authoring.AddPagePayload{PageID: strings.TrimSpace(s.PageID), Title: strings.TrimSpace(s.Title)}
	case "rename_page":
		command.RenamePage = &authoring.RenamePagePayload{PageID: strings.TrimSpace(s.PageID), Title: strings.TrimSpace(s.Title)}
	case "duplicate_page":
		command.DuplicatePage = &authoring.DuplicatePagePayload{PageID: strings.TrimSpace(s.PageID), NewPageID: strings.TrimSpace(s.NewPageID), Title: strings.TrimSpace(s.Title)}
	case "move_page":
		if s.Index == nil {
			return authoring.Command{}, fmt.Errorf("move page requires index")
		}
		command.MovePage = &authoring.MovePagePayload{PageID: strings.TrimSpace(s.PageID), Index: *s.Index}
	case "update_page_layout":
		command.UpdatePageLayout = &authoring.UpdatePageLayoutPayload{PageID: strings.TrimSpace(s.PageID), Columns: s.Columns, RowHeight: s.RowHeight, Gap: s.Gap, Padding: s.Padding}
	case "remove_page":
		command.RemovePage = &authoring.RemovePagePayload{PageID: strings.TrimSpace(s.PageID)}
	case "add_visual":
		command.AddVisual = &authoring.AddVisualPayload{PageID: strings.TrimSpace(s.PageID), VisualID: strings.TrimSpace(s.VisualID), ComponentID: strings.TrimSpace(s.ComponentID), Type: strings.TrimSpace(s.Type), Title: strings.TrimSpace(s.Title), FieldID: strings.TrimSpace(s.FieldID), Role: authoring.FieldRole(strings.TrimSpace(s.Role))}
	case "set_placements":
		placements := make([]authoring.PlacementUpdate, 0, len(s.Placements))
		for _, placement := range s.Placements {
			placements = append(placements, placement.placementUpdate())
		}
		if len(placements) == 0 {
			placements = append(placements, dashboardBuilderPlacementSignal{
				ComponentID: s.ComponentID, VisualID: s.VisualID, Placement: s.Placement,
				Column: s.Column, Row: s.Row, ColumnSpan: s.ColumnSpan, RowSpan: s.RowSpan, Col: s.Col, ColSpan: s.ColSpan,
			}.placementUpdate())
		}
		command.SetPlacements = &authoring.SetPlacementsPayload{PageID: strings.TrimSpace(s.PageID), Placements: placements}
	case "add_filter":
		command.AddFilter = &authoring.AddFilterPayload{FilterID: strings.TrimSpace(s.FilterID), Label: strings.TrimSpace(s.Title), Dimension: strings.TrimSpace(s.FieldID), Dataset: strings.TrimSpace(s.Dataset), ControlType: strings.TrimSpace(s.ControlType)}
	case "update_filter":
		command.UpdateFilter = &authoring.UpdateFilterPayload{FilterID: strings.TrimSpace(s.FilterID), Label: strings.TrimSpace(s.Title), Description: strings.TrimSpace(s.Description), Dataset: strings.TrimSpace(s.Dataset), ControlType: strings.TrimSpace(s.ControlType), Required: s.Required, ReaderEditable: s.ReaderEditable, URLParameter: strings.TrimSpace(s.URLParameter)}
	case "set_filter_targets":
		// A nil Targets slice (JSON field omitted) is the canonical all-pages
		// form; a non-nil list narrows the filter to visual definition IDs.
		command.SetFilterTargets = &authoring.SetFilterTargetsPayload{FilterID: strings.TrimSpace(s.FilterID), Targets: s.Targets}
	case "set_filter_scope":
		command.SetFilterScope = &authoring.SetFilterScopePayload{FilterID: strings.TrimSpace(s.FilterID), Scope: strings.TrimSpace(s.Scope), PageID: strings.TrimSpace(s.PageID), Targets: s.Targets}
	case "remove_filter":
		command.RemoveFilter = &authoring.RemoveFilterPayload{FilterID: strings.TrimSpace(s.FilterID)}
	case "add_filter_component":
		command.AddFilterComponent = &authoring.AddFilterComponentPayload{PageID: strings.TrimSpace(s.PageID), FilterID: strings.TrimSpace(s.FilterID), ComponentID: strings.TrimSpace(s.ComponentID)}
	case "remove_filter_component":
		command.RemoveFilterComponent = &authoring.RemoveFilterComponentPayload{PageID: strings.TrimSpace(s.PageID), ComponentID: strings.TrimSpace(s.ComponentID)}
	case "assign_field":
		command.AssignField = &authoring.AssignFieldPayload{PageID: strings.TrimSpace(s.PageID), VisualID: strings.TrimSpace(s.VisualID), FieldID: strings.TrimSpace(s.FieldID), Role: authoring.FieldRole(strings.TrimSpace(s.Role))}
	case "set_visual_type":
		command.SetVisualType = &authoring.SetVisualTypePayload{PageID: strings.TrimSpace(s.PageID), VisualID: strings.TrimSpace(s.VisualID), Type: document.DashboardVisualType(strings.TrimSpace(s.Type))}
	case "rename_visual":
		command.RenameVisual = &authoring.RenameVisualPayload{PageID: strings.TrimSpace(s.PageID), VisualID: strings.TrimSpace(s.VisualID), Title: strings.TrimSpace(s.Title)}
	case "duplicate_visual":
		command.DuplicateVisual = &authoring.DuplicateVisualPayload{PageID: strings.TrimSpace(s.PageID), VisualID: strings.TrimSpace(s.VisualID), NewVisualID: strings.TrimSpace(s.NewVisualID), NewComponentID: strings.TrimSpace(s.NewComponentID), Title: strings.TrimSpace(s.Title)}
	case "restore_revision":
		targetNumber, targetErr := parseRevisionNumber(s.TargetRevisionNumber)
		if targetErr != nil {
			return authoring.Command{}, fmt.Errorf("restore target: %w", targetErr)
		}
		command.RestoreRevision = &authoring.RestoreRevisionPayload{TargetRevision: authoring.RevisionToken{RevisionID: authoring.RevisionID(strings.TrimSpace(s.TargetRevisionID)), Number: targetNumber, ContentHash: strings.TrimSpace(s.TargetRevisionContentHash)}}
	case "remove_visual":
		command.RemoveVisual = &authoring.RemoveVisualPayload{PageID: strings.TrimSpace(s.PageID), VisualID: strings.TrimSpace(s.VisualID)}
	case "update_visual_format":
		command.UpdateVisualFormat = &authoring.UpdateVisualFormatPayload{PageID: strings.TrimSpace(s.PageID), VisualID: strings.TrimSpace(s.VisualID), Title: optionalTrimmedString(s.Title), TitleVisible: s.TitleVisible, LegendVisible: s.LegendVisible, AxisVisible: s.AxisVisible, DataLabelsVisible: s.DataLabelsVisible, FormatKey: strings.TrimSpace(s.FormatKey), FormatValue: s.FormatValue}
	case "remove_field":
		command.RemoveField = &authoring.RemoveFieldPayload{PageID: strings.TrimSpace(s.PageID), VisualID: strings.TrimSpace(s.VisualID), FieldID: strings.TrimSpace(s.FieldID), Role: authoring.FieldRole(strings.TrimSpace(s.Role))}
	case "move_field":
		command.MoveField = &authoring.MoveFieldPayload{PageID: strings.TrimSpace(s.PageID), VisualID: strings.TrimSpace(s.VisualID), FieldID: strings.TrimSpace(s.FieldID), Role: authoring.FieldRole(strings.TrimSpace(s.Role)), TargetRole: authoring.FieldRole(strings.TrimSpace(s.TargetRole)), Direction: strings.TrimSpace(s.Direction), Index: s.Index}
	case "set_interaction_target":
		command.SetInteractionTarget = &authoring.SetInteractionTargetPayload{PageID: strings.TrimSpace(s.PageID), VisualID: strings.TrimSpace(s.VisualID), TargetVisualID: strings.TrimSpace(s.TargetVisualID), Effect: strings.TrimSpace(s.Effect)}
	default:
		return authoring.Command{}, fmt.Errorf("unsupported dashboard builder action %q", s.Action)
	}
	return command, nil
}

func optionalTrimmedString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
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
		return authoring.RevisionToken{}, fmt.Errorf("%w: %v", authoring.ErrInvalidPayload, err)
	}
	id, hash := strings.TrimSpace(values.Get("revisionId")), strings.TrimSpace(values.Get("revisionContentHash"))
	if id == "" || hash == "" {
		return authoring.RevisionToken{}, fmt.Errorf("%w: complete preview revision is required", authoring.ErrInvalidPayload)
	}
	token := authoring.RevisionToken{RevisionID: authoring.RevisionID(id), Number: number, ContentHash: hash}
	if err := token.ValidateComplete(); err != nil {
		return authoring.RevisionToken{}, fmt.Errorf("%w: %v", authoring.ErrInvalidPayload, err)
	}
	return token, nil
}

func dashboardBuilderEnvelope(builder uisignals.DashboardBuilderSignal) uisignals.DashboardBuilderEnvelope {
	builder = dashboardBuilderWithPreviewHref(builder)
	return uisignals.DashboardBuilderEnvelope{
		Builder:                  builder,
		BuilderVisuals:           map[string]uisignals.DashboardVisualizationSignal{},
		Runtime:                  uisignals.RouteRuntimeSignal{Kind: uisignals.RouteKindDashboardBuilder, DashboardID: uisignals.Optional(builder.DashboardID)},
		Status:                   uisignals.DashboardStatus{Loading: false},
		BuilderFilterOptionPages: map[string]uisignals.DashboardFilterOptionPage{},
		BuilderFilterValidation:  uisignals.DashboardFilterValidationResult{Accepted: true},
		BuilderFilterCommand: uisignals.DashboardFilterCommand{Value: &uisignals.DashboardFilterMutateCommand{
			DashboardFilterCommandBase: uisignals.DashboardFilterCommandBase{Kind: "mutate", BaseRevision: 1},
			Kind:                       "mutate", Operation: "clear",
		}},
	}
}

// dashboardBuilderEnvelopeWithPreview keeps the builder projection authoritative
// while adding the governed, exact-revision visual preview to the same stream
// patch. Preview is deliberately fail-soft: authoring/query errors are exposed
// on builder.preview without hiding the builder or failing the bootstrap.
func (h Handler) dashboardBuilderEnvelopeWithPreview(ctx context.Context, actorID string, builder uisignals.DashboardBuilderSignal) uisignals.DashboardBuilderEnvelope {
	projectID, err := h.projectIDForRequest(ctx)
	if err != nil {
		// Inline projections already carry the server-authoritative project ID;
		// use it when no request route context is available (for example tests or
		// a caller rendering a previously resolved builder signal).
		projectID, err = projectgraph.NewResourceID(builder.ProjectID)
		if err != nil {
			envelope := dashboardBuilderEnvelope(builder)
			envelope.Builder.Preview.Loading = false
			envelope.Builder.Preview.Active = false
			message := err.Error()
			envelope.Builder.Preview.Error = &message
			return envelope
		}
	}
	return h.dashboardBuilderEnvelopeWithPreviewForProject(ctx, projectID, actorID, builder)
}

func (h Handler) dashboardBuilderEnvelopeWithPreviewForProject(ctx context.Context, projectID projectgraph.ResourceID, actorID string, builder uisignals.DashboardBuilderSignal) uisignals.DashboardBuilderEnvelope {
	envelope := dashboardBuilderEnvelope(builder)
	if servingStateID := builderServingStateID(builder); servingStateID != "" {
		envelope.Runtime.ServingStateID = uisignals.Optional(servingStateID)
	}
	result, err := h.Authoring.Preview(h.analyticalContext(ctx), preview.PreviewRequest{
		ProjectID: projectID, ActorID: strings.TrimSpace(actorID),
		DashboardID: authoring.DashboardID(strings.TrimSpace(builder.DashboardID)),
		DraftID:     authoring.DraftID(strings.TrimSpace(builder.DraftID)),
		ExpectedRevision: authoring.RevisionToken{
			RevisionID: authoring.RevisionID(strings.TrimSpace(builder.Revision.ID)), Number: uint64(maxInt64(builder.Revision.Number)), ContentHash: strings.TrimSpace(builder.Revision.ContentHash),
		},
		PageID: firstBuilderPage(builder), BestEffortVisuals: true,
	})
	contractDefinition := result.Definition
	contractEvidence := result.SemanticEvidence
	filterState := result.PagePatch.Filters.CompiledState
	if contractDefinition.ID == "" {
		compiled, compileErr := h.Authoring.Compile(h.analyticalContext(ctx), preview.CompileRequest{
			ProjectID: projectID, ActorID: strings.TrimSpace(actorID),
			DashboardID: authoring.DashboardID(strings.TrimSpace(builder.DashboardID)),
			DraftID:     authoring.DraftID(strings.TrimSpace(builder.DraftID)),
			ExpectedRevision: authoring.RevisionToken{
				RevisionID: authoring.RevisionID(strings.TrimSpace(builder.Revision.ID)), Number: uint64(maxInt64(builder.Revision.Number)), ContentHash: strings.TrimSpace(builder.Revision.ContentHash),
			},
		})
		if compileErr == nil {
			contractDefinition = compiled.Definition
			contractEvidence = compiled.SemanticEvidence
		}
	}
	if contractDefinition.ID != "" {
		// The preview is compiled and queried under one active semantic serving
		// lease. Include that generation in the synthetic draft identity so a
		// runtime cutover cannot reuse ephemeral state from the prior runtime.
		if generation := strings.TrimSpace(contractEvidence.Identity.GenerationID); generation != "" {
			if servingStateID := builderServingStateIDForGeneration(builder, generation); servingStateID != "" {
				envelope.Runtime.ServingStateID = uisignals.Optional(servingStateID)
			}
		}
		envelope.BuilderFilterContract = uisignals.DashboardFilterContractFromDefinition(contractDefinition)
		if filterState == nil {
			state := contractDefinition.DefaultFilterState()
			filterState = &state
		}
		envelope.BuilderFilterState = uisignals.DashboardFilterStateFromDomain(*filterState)
		envelope.BuilderFilterOptionPages = map[string]uisignals.DashboardFilterOptionPage{}
		envelope.BuilderFilterValidation = uisignals.DashboardFilterValidationResult{Accepted: true, CurrentRevision: int64(filterState.Revision)}
	}
	envelope.BuilderVisuals = dashboardBuilderPreviewVisuals(builder, result)
	for _, message := range result.VisualErrors {
		envelope.Builder = dashboardBuilderWithVisualPreviewError(envelope.Builder, message)
	}
	envelope.Builder.Preview.Loading = false
	previewErr := err
	if previewErr == nil && strings.TrimSpace(result.PagePatch.Status.Error) != "" {
		previewErr = errors.New(strings.TrimSpace(result.PagePatch.Status.Error))
	}
	if previewErr != nil {
		envelope.Builder.Preview.Active = false
		message := strings.TrimSpace(previewErr.Error())
		if message != "" {
			envelope.Builder.Preview.Error = &message
			envelope.Builder = dashboardBuilderWithVisualPreviewError(envelope.Builder, message)
		} else {
			envelope.Builder.Preview.Error = nil
		}
		return envelope
	}
	envelope.Builder.Preview.Active = true
	if len(result.VisualErrors) > 0 {
		noun := "visual"
		if len(result.VisualErrors) != 1 {
			noun = "visuals"
		}
		message := fmt.Sprintf("%d %s unavailable", len(result.VisualErrors), noun)
		envelope.Builder.Preview.Error = &message
		return envelope
	}
	// Command responses are merged into the existing Datastar signal tree.
	// Omitting an optional field leaves the previous value in place, so a
	// successful preview must explicitly clear an error from an earlier,
	// incomplete draft revision.
	envelope.Builder.Preview.Error = uisignals.Pointer("")
	return envelope
}

func dashboardBuilderWithVisualPreviewError(builder uisignals.DashboardBuilderSignal, message string) uisignals.DashboardBuilderSignal {
	visualID := dashboardBuilderPreviewErrorVisualID(message)
	if visualID == "" {
		return builder
	}
	for pageIndex := range builder.Pages {
		for visualIndex := range builder.Pages[pageIndex].Visuals {
			visual := &builder.Pages[pageIndex].Visuals[visualIndex]
			if strings.TrimSpace(visual.VisualID) == visualID || strings.TrimSpace(visual.ID) == visualID {
				visual.PreviewError = uisignals.Pointer(message)
			}
		}
	}
	return builder
}

func dashboardBuilderPreviewErrorVisualID(message string) string {
	const marker = `visual "`
	start := strings.Index(message, marker)
	if start < 0 {
		return ""
	}
	remaining := message[start+len(marker):]
	end := strings.IndexByte(remaining, '"')
	if end <= 0 {
		return ""
	}
	return strings.TrimSpace(remaining[:end])
}

func builderServingStateID(builder uisignals.DashboardBuilderSignal) string {
	if strings.TrimSpace(builder.DraftID) == "" || strings.TrimSpace(builder.Revision.ID) == "" || builder.Revision.Number <= 0 || strings.TrimSpace(builder.Revision.ContentHash) == "" {
		return ""
	}
	return "builder:" + strings.TrimSpace(builder.DraftID) + ":" + strings.TrimSpace(builder.Revision.ID) + ":" + strings.TrimSpace(builder.Revision.ContentHash)
}

// builderServingStateIDForGeneration scopes the exact authored revision to
// the active semantic runtime generation. The draft/revision/hash portion is
// retained as the stable inner identity while the generation suffix ensures a
// runtime cutover starts a fresh ephemeral filter session.
func builderServingStateIDForGeneration(builder uisignals.DashboardBuilderSignal, generation string) string {
	base := builderServingStateID(builder)
	generation = strings.TrimSpace(generation)
	if base == "" || generation == "" {
		return base
	}
	return base + ":generation:" + generation
}

func maxInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func dashboardBuilderPreviewVisuals(builder uisignals.DashboardBuilderSignal, result preview.Preview) map[string]uisignals.DashboardVisualizationSignal {
	visuals := make(map[string]uisignals.DashboardVisualizationSignal, len(result.PagePatch.Visuals))
	pageID := firstBuilderPage(builder)
	generation := result.PagePatch.Status.Generation
	if generation <= 0 {
		generation = 1
	}
	servingStateID := strings.TrimSpace(result.SemanticEvidence.Identity.GenerationID)
	if servingStateID == "" {
		servingStateID = strings.TrimSpace(result.PagePatch.Filters.ServingStateID)
	}
	filterRevision := int64(0)
	if result.PagePatch.Filters.CompiledState != nil {
		filterRevision = int64(result.PagePatch.Filters.CompiledState.Revision)
	}
	for authoredVisualID, envelope := range result.PagePatch.Visuals {
		authoredVisualID = strings.TrimSpace(authoredVisualID)
		if authoredVisualID == "" {
			authoredVisualID = strings.TrimSpace(envelope.VisualID)
		}
		if authoredVisualID == "" {
			continue
		}
		signal := uisignals.DashboardVisualizationSignalFromIR(envelope)
		signal.VisualID = authoredVisualID
		signal.ServingStateID = servingStateID
		signal.StreamGeneration = generation
		signal.FilterRevision = filterRevision
		signal.InteractionRevision = int64(result.PagePatch.Filters.InteractionRevision)
		signal.ConsumerIdentity = pageID + "/" + authoredVisualID
		visuals[authoredVisualID] = signal
	}
	return visuals
}

// dashboardBuilderWithPreviewHref derives the exact-revision preview URL from
// the authoritative builder projection. The shell carries an initial fallback
// URL, but every streamed projection must replace it after a mutation so a
// preview cannot target an older revision.
func dashboardBuilderWithPreviewHref(builder uisignals.DashboardBuilderSignal) uisignals.DashboardBuilderSignal {
	if strings.TrimSpace(builder.DashboardID) == "" ||
		strings.TrimSpace(builder.DraftID) == "" || strings.TrimSpace(builder.Revision.ID) == "" ||
		builder.Revision.Number <= 0 || strings.TrimSpace(builder.Revision.ContentHash) == "" {
		builder.Preview.Href = nil
		return builder
	}
	href := dashboardBuilderPreviewPath(builder.DashboardID, builder)
	builder.Preview.Href = &href
	return builder
}

func dashboardBuilderBasePath(dashboardID string) string {
	return "/dashboards/" + url.PathEscape(dashboardID)
}

func dashboardBuilderDraftRoute(dashboardID, draftID, suffix string) string {
	base := dashboardBuilderBasePath(dashboardID) + suffix
	if strings.TrimSpace(draftID) == "" {
		return base
	}
	values := url.Values{}
	values.Set("draft", draftID)
	return base + "?" + values.Encode()
}

func dashboardBuilderPreviewPath(dashboardID string, builder uisignals.DashboardBuilderSignal) string {
	values := url.Values{}
	values.Set("page", firstBuilderPage(builder))
	values.Set("draft", builder.DraftID)
	values.Set("revisionId", builder.Revision.ID)
	values.Set("revisionNumber", strconv.FormatInt(builder.Revision.Number, 10))
	values.Set("revisionContentHash", builder.Revision.ContentHash)
	return dashboardBuilderBasePath(dashboardID) + "/preview?" + values.Encode()
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

func writeBuilderError(w nethttp.ResponseWriter, r *nethttp.Request, err error) {
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
	case err != nil && strings.Contains(err.Error(), "strictly compile dashboard:"):
		status = nethttp.StatusUnprocessableEntity
	case errors.Is(err, authoring.ErrInvalidAuthoring), errors.Is(err, authoring.ErrInvalidIdentifier), errors.Is(err, authoring.ErrInvalidPayload):
		status = nethttp.StatusBadRequest
	}
	if status == nethttp.StatusForbidden {
		webtransport.WriteBrowserAuthorizationError(w, r, status)
		return
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
	case nethttp.StatusUnprocessableEntity:
		if err != nil {
			message = err.Error()
		}
	}
	nethttp.Error(w, message, status)
}
