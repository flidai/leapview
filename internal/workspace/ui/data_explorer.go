package ui

import (
	"github.com/flidai/leapview/internal/dashboard"
	uiactions "github.com/flidai/leapview/internal/platform/web/actions"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	catalog "github.com/flidai/leapview/internal/workspace/navigation"
	uisignals "github.com/flidai/leapview/internal/workspace/ui/signals"
	g "maragu.dev/gomponents"
)

// DataExplorerAgentBootstrap is agent-owned state projected into the data
// explorer without making the workspace module depend on agent signal types.
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
	explorerUpdatesURL := updatesURL(uisignals.RouteData, "object", uisignals.ValueOrZero(explorer.Command.ObjectKey))
	agentTurn := "$agent.composer.value = evt.detail.input; $agentContext.references = evt.detail.references; " + uiactions.CommandPostConditional("$agent.activeConversationId", []uicommand.Binding{commands.CreateRun}, commands.Workflow(), "/chats/turns", "agent", "agentContext")
	agentRestore := "$agent.activeConversationId = evt.detail.conversationId; " + uiactions.Get("/chats/restore", "agent")
	return webpage.Render(layout, webpage.Spec{
		Title: page.Title, CSRFToken: csrfToken, Scripts: []string{"/static/data-explorer.js"},
		UpdatesURL: explorerUpdatesURL,
		Content: g.El("lv-data-explorer",
			g.Attr("slot", "page"),
			g.Attr("data-indicator", "agentTurnPending"),
			g.Attr("data-on:lv-data-explorer-command", "$dataExplorerCommand = evt.detail; "+uiactions.EventPost("/data/command")),
			g.Attr("data-on:lv-chat-submit", agentTurn),
			g.Attr("data-on:lv-chat-restore", agentRestore),
			g.Attr("data-on:lv-chat-new", "$agent.activeConversationId = ''; $agent.transcript = []; $agent.composer.value = ''; $agentVisuals = {}"),
		),
		ContentAttrs: []g.Node{
			g.Attr("data-on:lv-chat-reference-search__debounce.200ms", "$agentReferenceSearch.query = evt.detail.query; $agentReferenceSearch.requestId = evt.detail.requestId; "+uiactions.Get("/chats/references/search", "agentReferenceSearch", "agentContext")),
		},
	})
}

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
	modelID := uisignals.ValueOrZero(command.ModelID)
	datasetID := uisignals.ValueOrZero(command.DatasetID)
	return uisignals.AgentContextSignal{
		Surface: "data", ModelID: modelID, DatasetID: &datasetID,
		DashboardID: "", DashboardTitle: "", PageID: "", PageTitle: "",
		Exploration: &uisignals.DataExploreAgentContextSignal{
			Dimensions: append([]string(nil), command.Dimensions...), Measures: append([]string(nil), command.Measures...),
			Filters: append([]uisignals.DataExploreFilterSignal(nil), command.Filters...),
			Sort:    append([]uisignals.DataExploreSortSignal(nil), command.Sort...), Time: command.Time, Limit: command.Limit,
		},
		Filters: uisignals.DashboardFilterState{
			AppliedControls: map[string]uisignals.DashboardAppliedFilterState{},
			DraftControls:   map[string]uisignals.DashboardFilterExpression{}, DirtyBindings: []string{},
		},
		ReferenceLimit: 12, References: []uisignals.AgentReferenceSignal{},
	}
}
