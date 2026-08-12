package ui

import (
	"net/url"
	"strings"

	"github.com/Yacobolo/toolbelt/pagestream"
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	uiactions "github.com/flidai/leapview/internal/platform/web/actions"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	g "maragu.dev/gomponents"
)

func ChatPage(workspaceID, csrfToken, view string, state ChatViewState, providers ...webpage.Provider) g.Node {
	layout := webpage.Resolve(firstProvider(providers), chatLayoutContext(workspaceID, view, state.Agent.ActiveConversationID))
	createRun := agentgen.GenUIActionCreateAgentRun()
	turnCommand := uiactions.CommandPost(createRun, "/chats/turns", "agent", "agentContext")
	if strings.TrimSpace(state.Agent.ActiveConversationID) == "" {
		turnCommand = uiactions.CommandPostSequence([]uicommand.Binding{
			agentgen.GenUIActionCreateAgentConversation(),
			createRun,
		}, "/chats/turns", "agent", "agentContext")
	}
	return webpage.Render(layout, webpage.Spec{
		Title: "Chat", CSRFToken: csrfToken, Scripts: []string{"/static/chat-page.js"},
		UpdatesURL: chatUpdatesURL(workspaceID, view, state.Agent.ActiveConversationID),
		ContentAttrs: []g.Node{
			g.Attr("data-on:lv-chat-reference-search__debounce.200ms", "$agentReferenceSearch.query = evt.detail.query; $agentReferenceSearch.requestId = evt.detail.requestId; "+uiactions.Get("/chats/references/search", "agentReferenceSearch", "agentContext")),
		},
		Content: g.El("lv-chat-page",
			g.Attr("slot", "page"),
			g.Attr("workspace-id", workspaceID),
			g.Attr("view", view),
			g.Attr("data-indicator", "agentTurnPending"),
			g.Attr("data-on:lv-chat-submit", "$agent.composer.value = evt.detail.input; $agentContext.references = evt.detail.references; "+turnCommand),
		),
	})
}

func ChatBootstrapSignals(workspaceID, view string, state ChatViewState, providers ...webpage.Provider) map[string]any {
	layout := webpage.Resolve(firstProvider(providers), chatLayoutContext(workspaceID, view, state.Agent.ActiveConversationID))
	return webpage.WithSignal(layout, chatInitialSignals(workspaceID, view, state))
}

func ChatSignalPatch(state ChatViewState) pagestream.SignalPatch {
	patch := ChatConversationsPatch(state.Agent.Conversations, state.Agent.ActiveConversationID)
	patch["agent"] = state.Agent
	patch["visuals"] = state.Visuals
	return patch
}

func ChatConversationsPatch(conversations []ChatConversationSummary, activeConversationID string) pagestream.SignalPatch {
	return pagestream.SignalPatch{
		"agent": map[string]any{"conversations": conversations},
		"chrome": map[string]any{"sidebar": map[string]any{"history": map[string]any{
			"items": chatHistoryItems(ChatSignal{ActiveConversationID: activeConversationID, Conversations: conversations}),
		}}},
	}
}

func chatUpdatesURL(workspaceID, view, conversationID string) string {
	values := url.Values{}
	values.Set("route", string(RouteChat))
	for key, value := range map[string]string{
		"workspace": workspaceID, "view": view, "conversation": conversationID,
	} {
		if strings.TrimSpace(value) != "" {
			values.Set(key, value)
		}
	}
	return "/updates?" + values.Encode()
}

func chatLayoutContext(workspaceID, view, activeConversationID string) webpage.Context {
	active := ""
	if strings.TrimSpace(view) == "list" {
		active = "chat"
	}
	return webpage.Context{
		Active: active, ScopeID: workspaceID, HistoryID: activeConversationID,
		SectionTitle: "Workspace", PageTitle: "Published assets",
	}
}

func firstProvider(providers []webpage.Provider) webpage.Provider {
	if len(providers) == 0 {
		return nil
	}
	return providers[0]
}
