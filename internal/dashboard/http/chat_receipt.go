package http

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/url"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/builderview"
	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/dashboard/ui"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	"github.com/go-chi/chi/v5"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// A chat mutation acknowledges the committed draft without booting a second
// application, opening streams, or querying every chart in the builder.
func (handler Handler) chatDashboardReceipt(w nethttp.ResponseWriter, r *nethttp.Request, dashboardID string) bool {
	if r.FormValue("builderReceipt") == "1" {
		return handler.savedVisualImportReceipt(w, r, dashboardID)
	}
	if r.FormValue("chatReceipt") != "1" {
		return false
	}
	project, err := handler.projectIDForRequest(r.Context())
	reader, ok := handler.Authoring.(dashboardAuthoringDraftReader)
	if err != nil || !ok {
		writeBuilderError(w, r, access.ErrForbidden)
		return true
	}
	draft, err := reader.Draft(r.Context(), application.DraftRequest{ProjectID: project, ActorID: handler.currentActor(r), DashboardID: authoring.DashboardID(dashboardID)})
	if err != nil {
		writeBuilderError(w, r, err)
		return true
	}
	if draft.Lifecycle.Draft == nil {
		writeBuilderError(w, r, authoring.ErrNotFound)
		return true
	}
	components := make([]map[string]string, 0)
	pageID := r.FormValue("pageId")
	for _, page := range draft.Revision.Document.Spec.Pages {
		if pageID == "" {
			pageID = page.ID
		}
		for _, component := range page.Components {
			if visual, ok := component.Value.(*document.VisualDashboardPageComponent); ok {
				components = append(components, map[string]string{"id": visual.ID, "pageId": page.ID})
			}
		}
	}
	href := dashboardBuilderDraftRoute(dashboardID, draft.Lifecycle.Draft.ID.String(), "/edit") + "&embed=chat&page=" + url.QueryEscape(pageID)
	payload, err := json.Marshal(map[string]any{
		"type": "lv-dashboard-mutation", "href": href, "revisionId": string(draft.Revision.Token().RevisionID), "pageId": pageID, "components": components,
		"reference": map[string]any{"reference": map[string]string{"kind": "dashboard", "id": dashboardID}, "name": draft.Lifecycle.Title, "hierarchy": []string{}, "href": href, "locations": []string{}, "context": []string{"Editable dashboard draft"}},
	})
	if err != nil {
		writeBuilderError(w, r, err)
		return true
	}
	renderDashboardReceipt(w, payload)
	return true
}

// Importing through a native form updates the builder's typed signals in place,
// keeping the active agent, draft, page, and scroll state alive.
func (handler Handler) savedVisualImportReceipt(w nethttp.ResponseWriter, r *nethttp.Request, dashboardID string) bool {
	project, err := handler.projectIDForRequest(r.Context())
	actor := handler.currentActor(r)
	if err != nil || actor == "" {
		writeBuilderError(w, r, access.ErrForbidden)
		return true
	}
	builder, err := handler.Authoring.Builder(r.Context(), builderview.Request{
		ProjectID: project, ActorID: actor, DashboardID: authoring.DashboardID(dashboardID),
		SelectedPageID: r.FormValue("pageId"),
	})
	if err != nil {
		writeBuilderError(w, r, err)
		return true
	}
	envelope := handler.dashboardBuilderEnvelopeWithPreviewForProject(r.Context(), project, actor, builder)
	handler.renderBuilderSignalReceipt(w, r, envelope)
	return true
}

func (handler Handler) renderBuilderSignalReceipt(w nethttp.ResponseWriter, r *nethttp.Request, envelope uisignals.DashboardBuilderEnvelope) {
	payload, err := json.Marshal(map[string]any{
		"type": "lv-builder-imported", "envelope": envelope, "agentContext": ui.DashboardBuilderAgentContext(envelope),
	})
	if err != nil {
		writeBuilderError(w, r, err)
		return
	}
	renderDashboardReceipt(w, payload)
}

func renderDashboardReceipt(w nethttp.ResponseWriter, payload []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = g.Group{g.Raw("<!doctype html>"), h.HTML(h.Head(h.Meta(h.Charset("utf-8")), h.Script(h.Type("module"), h.Src("/static/chat-dashboard-receipt.js"))), h.Body(h.Div(h.ID("chat-dashboard-receipt"), g.Attr("hidden", ""), g.Attr("data-receipt", string(payload)))))}.Render(w)
}

func (handler Handler) RemoveChatDashboardVisual(w nethttp.ResponseWriter, r *nethttp.Request) {
	project, err := handler.projectIDForRequest(r.Context())
	actor := handler.currentActor(r)
	reader, ok := handler.Authoring.(dashboardAuthoringDraftReader)
	if err != nil || actor == "" || !ok {
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
	if r.FormValue("revisionId") != string(draft.Revision.Token().RevisionID) {
		writeBuilderError(w, r, authoring.ErrStaleRevision)
		return
	}
	command := authoring.Command{ID: authoring.CommandID(requestID), DashboardID: dashboardID, DraftID: draft.Lifecycle.Draft.ID, ExpectedRevision: draft.Revision.Token(), Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: actor}, RemoveVisual: &authoring.RemoveVisualPayload{PageID: r.FormValue("pageId"), VisualID: r.FormValue("componentId")}}
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
	nethttp.Redirect(w, r, dashboardBuilderDraftRoute(dashboardID.String(), draft.Lifecycle.Draft.ID.String(), "/edit"), nethttp.StatusSeeOther)
}
