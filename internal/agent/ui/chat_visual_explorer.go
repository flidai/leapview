package ui

import (
	"encoding/json"
	"net/url"

	"github.com/flidai/leapview/internal/agent"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	g "maragu.dev/gomponents"
)

func ChatVisualExplorerPage(conversationID, modelID, datasetID, csrfToken string, item agent.ChatTranscriptItem, envelope visualizationir.VisualizationEnvelope, provider webpage.Provider) (g.Node, error) {
	// JSON is carried in escaped attributes, never executable inline script.
	visualJSON, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	itemJSON, err := json.Marshal(chatTranscriptItem(item))
	if err != nil {
		return nil, err
	}
	modelHref := ""
	if modelID != "" {
		values := url.Values{"mode": {"explore"}, "semanticModel": {modelID}}
		if datasetID != "" {
			values.Set("dataset", datasetID)
		}
		modelHref = "/explore?" + values.Encode()
	}
	layout := webpage.Resolve(provider, webpage.Context{Active: "explore", HistoryID: conversationID, PageTitle: "Visual Explorer"})
	return webpage.Render(layout, webpage.Spec{
		Title: "Visual Explorer", CSRFToken: csrfToken,
		UpdatesURL: chatUpdatesURL("", "conversation", conversationID),
		Scripts:    []string{"/static/chat-visual-explorer.js"},
		Content: g.El("lv-chat-visual-explorer",
			g.Attr("slot", "page"),
			g.Attr("data-visual", string(visualJSON)),
			g.Attr("data-transcript", string(itemJSON)),
			g.Attr("chat-href", "/chats/"+url.PathEscape(conversationID)),
			g.Attr("model-href", modelHref),
		),
	}), nil
}
