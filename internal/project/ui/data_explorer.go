package ui

import (
	"bytes"
	"encoding/json"
	"net/url"

	"github.com/flidai/leapview/internal/dashboard"
	uiactions "github.com/flidai/leapview/internal/platform/web/actions"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	catalog "github.com/flidai/leapview/internal/project/navigation"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
	g "maragu.dev/gomponents"
)

// DataExplorerAgentBootstrap is agent-owned state projected into the data
// explorer without making the project module depend on agent signal types.
type DataExplorerAgentBootstrap struct {
	Agent   any
	Visuals any
}

type DataExplorerAgentCommandBindings struct {
	CreateConversation uicommand.Binding
	CreateRun          uicommand.Binding
}

func (commands DataExplorerAgentCommandBindings) Workflow() []uicommand.Binding {
	return []uicommand.Binding{commands.CreateConversation, commands.CreateRun}
}

func DataExplorerPage(catalog catalog.Catalog, page uisignals.DataExplorerPageSignal, explorer uisignals.DataExplorerSignal, csrfToken string, providers ...webpage.Provider) g.Node {
	return DataExplorerPageWithAgent(catalog, page, explorer, DataExplorerAgentBootstrap{}, DataExplorerAgentCommandBindings{}, csrfToken, providers...)
}

func DataExplorerPageWithAgent(_ catalog.Catalog, page uisignals.DataExplorerPageSignal, explorer uisignals.DataExplorerSignal, agent DataExplorerAgentBootstrap, commands DataExplorerAgentCommandBindings, csrfToken string, providers ...webpage.Provider) g.Node {
	layout := webpage.Resolve(firstProvider(providers), webpage.Context{Active: "data-explorer", PageTitle: page.Title})
	explorerUpdatesURL := dataExplorerUpdatesURL(explorer.Command)
	agentTurn := "$agent.composer.value = evt.detail.input; $agentContext.references = evt.detail.references; " + uiactions.CommandPostConditional("$agent.activeConversationId", []uicommand.Binding{commands.CreateRun}, commands.Workflow(), "/chats/turns", "agent", "agentContext")
	agentRestore := "$agent.activeConversationId = evt.detail.conversationId; " + uiactions.Get("/chats/restore", "agent")
	return webpage.Render(layout, webpage.Spec{
		Title: page.Title, CSRFToken: csrfToken, Scripts: []string{"/static/data-explorer.js"},
		UpdatesURL: explorerUpdatesURL,
		Content: g.El("lv-data-explorer",
			g.Attr("slot", "page"),
			g.Attr("data-indicator", "agentTurnPending"),
			g.Attr("data-on:lv-data-explorer-command", "$dataExplorerCommand = evt.detail; "+uiactions.EventPost("/explore/command")),
			g.Attr("data-on:lv-chat-submit", agentTurn),
			g.Attr("data-on:lv-chat-restore", agentRestore),
			g.Attr("data-on:lv-chat-new", "$agent.activeConversationId = ''; $agent.transcript = []; $agent.composer.value = ''; $agentVisuals = {}"),
		),
		ContentAttrs: []g.Node{
			g.Attr("data-on:lv-chat-reference-search__debounce.200ms", "$agentReferenceSearch.query = evt.detail.query; $agentReferenceSearch.requestId = evt.detail.requestId; "+uiactions.Get("/chats/references/search", "agentReferenceSearch", "agentContext")),
		},
	})
}

func dataExplorerUpdatesURL(command uisignals.DataExplorerCommand) string {
	values := url.Values{"route": {string(uisignals.RouteKindData)}, "surface": {"explore"}}
	if uisignals.ValueOrZero(command.Mode) != "explore" || command.Explore == nil {
		if object := uisignals.ValueOrZero(command.ObjectKey); object != "" {
			values.Set("object", object)
		}
		return "/updates?" + values.Encode()
	}
	explore := command.Explore
	values.Set("mode", "explore")
	values.Set("v", "2")
	encoded, _ := canonicalExplorationJSON(explore.Spec)
	values.Set("state", string(encoded))
	return "/updates?" + values.Encode()
}

// canonicalExplorationJSON mirrors the browser URL codec: object keys are
// sorted lexicographically at every level while array order is preserved.
// Re-decoding through UseNumber avoids changing decimal/int lexical values.
func canonicalExplorationJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

const dataExplorerDefaultLimit = int64(100)

func DataExplorerBootstrapSignals(catalog catalog.Catalog, page uisignals.DataExplorerPageSignal, explorer uisignals.DataExplorerSignal, providers ...webpage.Provider) map[string]any {
	return DataExplorerBootstrapSignalsWithAgent(catalog, page, explorer, DataExplorerAgentBootstrap{}, providers...)
}

func DataExplorerBootstrapSignalsWithAgent(_ catalog.Catalog, page uisignals.DataExplorerPageSignal, explorer uisignals.DataExplorerSignal, agent DataExplorerAgentBootstrap, providers ...webpage.Provider) map[string]any {
	layout := webpage.Resolve(firstProvider(providers), webpage.Context{Active: "data-explorer", PageTitle: page.Title})
	context := DataExplorerAgentContext(page, explorer)
	if agent.Agent == nil {
		agent.Agent = uisignals.ChatSignal{
			Conversations: []uisignals.ChatConversationSummary{}, Transcript: []uisignals.ChatTranscriptItemSignal{},
			Status: uisignals.ChatStatus{}, Composer: uisignals.ComposerSignal{Disabled: true, Placeholder: "Agent is not configured."},
		}
	}
	if agent.Visuals == nil {
		agent.Visuals = map[string]any{}
	}
	return webpage.WithSignal(layout, map[string]any{
		"page":                page,
		"dataExplorer":        explorer,
		"dataExplorerCommand": explorer.Command,
		"status":              dashboard.Status{},
		"agent":               agent.Agent,
		"agentContext":        context,
		"agentReferenceSearch": uisignals.AgentReferenceSearchSignal{
			Results: []uisignals.AgentReferenceSignal{},
		},
		"agentVisuals": agent.Visuals,
	})
}

func DataExplorerAgentContext(page uisignals.DataExplorerPageSignal, explorer uisignals.DataExplorerSignal) uisignals.AgentContextSignal {
	command := explorer.Explore.Command
	spec := command.Spec
	modelID := spec.ModelID
	datasetID := uisignals.ValueOrZero(spec.DatasetID)
	return uisignals.AgentContextSignal{
		Surface: "data", ModelID: modelID, DatasetID: &datasetID,
		DashboardID: "", DashboardTitle: "", PageID: "", PageTitle: "", Exploration: &spec,
		Filters: uisignals.DashboardFilterState{
			AppliedControls: map[string]uisignals.DashboardAppliedFilterState{},
			DraftControls:   map[string]uisignals.DashboardFilterExpression{}, DirtyBindings: []string{},
		},
		ReferenceLimit: 12, References: []uisignals.AgentReferenceSignal{},
	}
}
