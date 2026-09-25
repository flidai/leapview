package ui

import (
	"testing"

	"github.com/flidai/leapview/internal/agent"
	appshell "github.com/flidai/leapview/internal/app/shell"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
)

func TestChatTranscriptItemsPreserveAgentOwnedWireState(t *testing.T) {
	items := ChatTranscriptItems([]agent.ChatTranscriptItem{{
		ID:              "part-1",
		Kind:            "assistant",
		OutputOrdinal:   0,
		ParentMessageID: "message-1",
		Text:            "Hello",
		Artifact: &agent.ChatArtifact{
			Type:    "visualization",
			ID:      "visual-1",
			Summary: "A chart",
		},
		References: []agent.TurnReference{{
			Reference: agent.TurnReferenceKey{Kind: "metric", ID: "revenue"},
			Name:      "Revenue",
			Resource:  agent.TurnReferenceResource{ID: "project_demo", Name: "Demo"},
		}},
	}})

	if len(items) != 1 || items[0].Text == nil || *items[0].Text != "Hello" {
		t.Fatalf("transcript items = %#v", items)
	}
	if items[0].OutputOrdinal == nil || *items[0].OutputOrdinal != 0 || items[0].ParentMessageID == nil || *items[0].ParentMessageID != "message-1" {
		t.Fatalf("output identity = %#v", items[0])
	}
	if items[0].Artifact == nil || items[0].Artifact.ID != "visual-1" {
		t.Fatalf("artifact = %#v", items[0].Artifact)
	}
	if items[0].References == nil || len(*items[0].References) != 1 {
		t.Fatalf("references = %#v", items[0].References)
	}
}

func TestChatBootstrapSignalsAreOwnedByAgent(t *testing.T) {
	state := ChatViewState{Agent: ChatSignal{
		Conversations: []ChatConversationSummary{{ID: "conversation-1", Title: "Revenue"}},
		Transcript:    []ChatTranscriptItemSignal{},
		Status:        ChatStatus{Enabled: true},
	}}

	provider := appshell.Provider(appshell.Config{
		Presentation: webpage.Presentation{ProductName: "LeapView"}, RoleLabel: "viewer",
		Conversations: []appshell.Conversation{{ID: "conversation-1", Title: "Revenue"}},
	})
	signals := ChatBootstrapSignals("", "list", state, provider)

	runtime, ok := signals["runtime"].(RouteRuntimeSignal)
	if !ok || runtime.Kind != RouteChat {
		t.Fatalf("runtime = %#v", signals["runtime"])
	}
	chrome, ok := signals["chrome"].(appshell.Chrome)
	if !ok || chrome.Sidebar.History == nil || len(chrome.Sidebar.History.Items) != 1 {
		t.Fatalf("chrome = %#v", signals["chrome"])
	}
	if chrome.Sidebar.History.Items[0].Href != "/chats/conversation-1" {
		t.Fatalf("history item = %#v", chrome.Sidebar.History.Items[0])
	}
}

func TestChatConversationsPatchPreservesOneOrderForListAndSidebar(t *testing.T) {
	conversations := []ChatConversationSummary{
		{ID: "recent", Title: "Recent"},
		{ID: "restored", Title: "Restored"},
		{ID: "older", Title: "Older"},
	}
	patch := ChatConversationsPatch(conversations, "restored")

	agentPatch, ok := patch["agent"].(map[string]any)
	if !ok {
		t.Fatalf("agent patch = %#v", patch["agent"])
	}
	list, ok := agentPatch["conversations"].([]ChatConversationSummary)
	if !ok || len(list) != len(conversations) {
		t.Fatalf("chat list conversations = %#v", agentPatch["conversations"])
	}
	chrome := patch["chrome"].(map[string]any)
	sidebar := chrome["sidebar"].(map[string]any)
	history := sidebar["history"].(map[string]any)
	items, ok := history["items"].([]SidebarHistoryItemSignal)
	if !ok || len(items) != len(conversations) {
		t.Fatalf("sidebar history = %#v", history["items"])
	}
	for index, conversation := range conversations {
		if list[index].ID != conversation.ID || items[index].ID != conversation.ID {
			t.Fatalf("order differs at %d: list=%#v sidebar=%#v want=%q", index, list, items, conversation.ID)
		}
	}
	if !items[1].Active {
		t.Fatalf("active sidebar item = %#v", items)
	}
}
