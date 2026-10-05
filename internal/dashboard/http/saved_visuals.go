package http

import (
	"context"
	"encoding/json"
	"fmt"
	nethttp "net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/document"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	"github.com/go-chi/chi/v5"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

func (handler Handler) SavedVisualLibrary(w nethttp.ResponseWriter, r *nethttp.Request) {
	handler.savedVisualLibrary(w, r, "")
}

func (handler Handler) savedVisualLibrary(w nethttp.ResponseWriter, r *nethttp.Request, message string) {
	project, err := handler.projectIDForRequest(r.Context())
	actor := handler.currentActor(r)
	if err != nil || actor == "" || handler.SavedVisuals == nil {
		writeBuilderError(w, r, access.ErrForbidden)
		return
	}
	items, err := handler.SavedVisuals.SavedVisuals(r.Context(), project.String(), actor)
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	signal := uisignals.SavedVisualLibrarySignal{Visuals: []uisignals.SavedVisualSignal{}, ModelID: r.URL.Query().Get("model"), Error: message}
	for _, item := range items {
		signal.Visuals = append(signal.Visuals, uisignals.SavedVisualSignal{ID: item.ID, Title: item.Title, SemanticModelID: item.SemanticModelID, SourceKey: item.SourceKey})
		if item.ID == r.URL.Query().Get("savedId") {
			signal.SavedID = item.ID
			signal.SourceKey = item.SourceKey
		}
	}
	encoded, err := json.Marshal(map[string]any{"savedVisualLibrary": signal})
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	csrf := ""
	if handler.CSRFToken != nil {
		csrf = handler.CSRFToken(r)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = g.Group{
		g.Raw("<!doctype html>"),
		h.HTML(h.Lang("en"),
			h.Head(
				h.Meta(h.Charset("utf-8")),
				h.Meta(h.Name("viewport"), h.Content("width=device-width, initial-scale=1")),
				h.Meta(h.Name("csrf-token"), h.Content(csrf)),
				g.El("title", g.Text("Saved visuals")),
				h.Script(h.Src("/static/theme.js")),
				h.Link(h.Rel("stylesheet"), h.Href("/static/app.css")),
				h.Script(h.Type("module"), h.Src("/static/saved-visual-library.js")),
			),
			h.Body(
				g.El("style", g.Raw("html,body,main{min-width:0;width:100%;margin:0}html,body{height:100%;background:var(--lv-bg-panel);color:var(--lv-fg-default)}")),
				h.Main(g.Attr("data-signals", string(encoded)), g.El("lv-saved-visual-library")),
			),
		),
	}.Render(w)
}

func (handler Handler) SaveVisual(w nethttp.ResponseWriter, r *nethttp.Request) {
	project, err := handler.projectIDForRequest(r.Context())
	actor := handler.currentActor(r)
	if err != nil || actor == "" || handler.SavedVisuals == nil {
		writeBuilderError(w, r, access.ErrForbidden)
		return
	}
	r.Body = nethttp.MaxBytesReader(w, r.Body, 1<<20)
	if err = r.ParseForm(); err != nil {
		handler.savedVisualLibrary(w, r, "The visual is too large to save.")
		return
	}
	var input chatDraftVisual
	if err = json.Unmarshal([]byte(r.FormValue("definition")), &input); err != nil || input.SemanticModelID == "" || input.Visual.Type == "" {
		handler.savedVisualLibrary(w, r, "This visual has no editable definition.")
		return
	}
	key := strings.TrimSpace(r.FormValue("sourceKey"))
	title := strings.TrimSpace(r.FormValue("title"))
	if len(key) == 0 || len(key) > 512 || len(title) == 0 || len(title) > 512 {
		handler.savedVisualLibrary(w, r, "The visual needs a title and source.")
		return
	}
	input = input.forVisual("saved")
	doc := document.DashboardDocument{APIVersion: document.DashboardApiVersionLeapviewDevV1, Kind: document.DashboardResourceKindDashboard, Metadata: document.DashboardMetadata{ID: "dashboard:saved-visual", Name: "saved-visual"}, Spec: document.DashboardSpec{SemanticModel: input.SemanticModelID, Visuals: map[string]document.DashboardVisual{"saved": input.Visual}, Filters: input.Filters, Pages: []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{}}}}}
	if err = authoring.ValidateCanonicalDocument(doc); err != nil {
		handler.savedVisualLibrary(w, r, "This visual definition cannot be saved: "+err.Error())
		return
	}
	definition, err := json.Marshal(input)
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	saved, err := handler.SavedVisuals.SaveVisual(r.Context(), project.String(), actor, authoring.SavedVisual{Title: title, SourceKey: key, SemanticModelID: input.SemanticModelID, DefinitionJSON: string(definition)})
	if err != nil {
		handler.savedVisualLibrary(w, r, "The visual could not be saved. Please try again.")
		return
	}
	nethttp.Redirect(w, r, "/visuals/saved?savedId="+url.QueryEscape(saved.ID), nethttp.StatusSeeOther)
}

// A narrow import loads the definition under the current account, then uses
// the existing audited authoring transaction to add an independent copy.
func (handler Handler) AddSavedVisual(w nethttp.ResponseWriter, r *nethttp.Request) {
	project, err := handler.projectIDForRequest(r.Context())
	actor := handler.currentActor(r)
	reader, ok := handler.Authoring.(dashboardAuthoringDraftReader)
	if err != nil || actor == "" || !ok || handler.SavedVisuals == nil {
		writeBuilderError(w, r, access.ErrForbidden)
		return
	}
	r.Body = nethttp.MaxBytesReader(w, r.Body, 64<<10)
	if err = r.ParseForm(); err != nil {
		writeBuilderError(w, r, err)
		return
	}
	requestID, err := browserFormRequestID(r)
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	dashboardID := authoring.DashboardID(chi.URLParam(r, "dashboard"))
	draft, err := reader.Draft(r.Context(), application.DraftRequest{ProjectID: project, ActorID: actor, DashboardID: dashboardID})
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	if draft.Lifecycle.Draft == nil {
		writeBuilderError(w, r, authoring.ErrNotFound)
		return
	}
	saved, err := handler.SavedVisuals.SavedVisual(r.Context(), project.String(), actor, r.FormValue("savedVisualId"))
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	if saved.SemanticModelID != draft.Lifecycle.SemanticModel.String() {
		writeBuilderError(w, r, fmt.Errorf("%w: choose a visual from this dashboard's semantic model", authoring.ErrInvalidPayload))
		return
	}
	var input chatDraftVisual
	if err = json.Unmarshal([]byte(saved.DefinitionJSON), &input); err != nil {
		writeBuilderError(w, r, err)
		return
	}
	doc, err := draft.Revision.Document.Clone()
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	id := "saved_" + strings.ReplaceAll(requestID, "-", "")
	href := dashboardBuilderDraftRoute(dashboardID.String(), draft.Lifecycle.Draft.ID.String(), "/edit") + "&page=" + url.QueryEscape(r.FormValue("pageId"))
	if r.FormValue("embed") == "chat" {
		href += "&embed=chat"
	}
	if _, exists := doc.Spec.Visuals[id]; exists {
		if handler.chatDashboardReceipt(w, r, dashboardID.String()) {
			return
		}
		nethttp.Redirect(w, r, href, nethttp.StatusSeeOther)
		return
	}
	if r.FormValue("revisionId") != string(draft.Revision.Token().RevisionID) {
		writeBuilderError(w, r, authoring.ErrStaleRevision)
		return
	}
	pageIndex := -1
	for i, page := range doc.Spec.Pages {
		if page.ID == r.FormValue("pageId") {
			pageIndex = i
			break
		}
	}
	if pageIndex < 0 {
		writeBuilderError(w, r, authoring.ErrNotFound)
		return
	}
	page := &doc.Spec.Pages[pageIndex]
	row := int32(1)
	requestedRow, _ := strconv.Atoi(r.FormValue("row"))
	if requestedRow > 0 && requestedRow < 100000 {
		row = int32(requestedRow)
	}
	placement := chatVisualPlacement(string(input.Visual.Type), doc.Spec, *page, row)
	input = input.forVisual(id)
	doc.Spec.Visuals[id] = input.Visual
	page.Components = append(page.Components, document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: id, Type: "visual", Placement: placement}, Type: "visual", Visual: id}})
	doc.Spec.Filters = append(doc.Spec.Filters, input.Filters...)

	command := authoring.Command{ID: authoring.CommandID(requestID), DashboardID: dashboardID, DraftID: draft.Lifecycle.Draft.ID, ExpectedRevision: draft.Revision.Token(), Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: actor}, ReplaceDocument: &authoring.ReplaceDocumentPayload{Document: doc}}
	err = executeAuthoringUIMutation(r, "executeDashboardAuthoringCommand", project.String(), requestID, actor, dashboardID.String(), command.DraftID.String(), authoring.OriginUI, access.CapabilityResourceEdit, nil, func(ctx context.Context) error {
		_, err := handler.Authoring.Execute(ctx, project, command)
		return err
	})
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	if handler.chatDashboardReceipt(w, r, dashboardID.String()) {
		return
	}
	nethttp.Redirect(w, r, href, nethttp.StatusSeeOther)
}

// Unsave affects only the account library; dashboard copies remain independent.
func (handler Handler) UnsaveVisual(w nethttp.ResponseWriter, r *nethttp.Request) {
	project, err := handler.projectIDForRequest(r.Context())
	actor := handler.currentActor(r)
	if err != nil || actor == "" || handler.SavedVisuals == nil {
		writeBuilderError(w, r, access.ErrForbidden)
		return
	}
	r.Body = nethttp.MaxBytesReader(w, r.Body, 64<<10)
	if err = r.ParseForm(); err != nil {
		handler.savedVisualLibrary(w, r, "The visual could not be unsaved. Please try again.")
		return
	}
	if err = handler.SavedVisuals.UnsaveVisual(r.Context(), project.String(), actor, r.FormValue("savedVisualId")); err != nil {
		handler.savedVisualLibrary(w, r, "The visual could not be unsaved. Please try again.")
		return
	}
	nethttp.Redirect(w, r, "/visuals/saved", nethttp.StatusSeeOther)
}
