package agent

import (
	"encoding/json"
	"strings"
	"testing"

	agentcore "github.com/flidai/leapview/pkg/agent"
)

func TestDashboardPreviewContextUsesOwnedPersistedSource(t *testing.T) {
	ctx, store, service, scope, conversation := newConversationVisualArtifactFixture(t)
	appendConversationVisualArtifact(t, ctx, store, scope, conversation.ID, "preview-call", "preview-artifact", conversationVisualArtifactArguments(), false)
	messages, err := store.ListMessages(ctx, scope.PrincipalID, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := PromptInput{Scope: scope, ConversationID: conversation.ID, Context: &TurnContext{Surface: "chat", PreviewArtifactID: "preview-artifact"}}
	items, err := service.promptDashboardPreviewContextItems(ctx, input, messages)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Key != "leapview_dashboard_preview" || !strings.Contains(string(raw), "semantic_model_finance") || !strings.Contains(string(raw), "filters") {
		t.Fatalf("missing canonical source: %s", raw)
	}
	input.Context.PreviewArtifactID = "missing"
	if _, err := service.promptDashboardPreviewContextItems(ctx, input, messages); err == nil {
		t.Fatal("unknown artifact accepted")
	}
	input.Context.PreviewArtifactID = "preview-artifact"
	input.Scope.PrincipalID = "other-owner"
	if _, err := service.promptDashboardPreviewContextItems(ctx, input, messages); err == nil {
		t.Fatal("other owner's source accepted")
	}
}

func TestDashboardPreviewContextPreservesComposedDraftAndBoundsSource(t *testing.T) {
	ctx, store, service, scope, conversation := newConversationVisualArtifactFixture(t)
	appendConversationDashboardDraft(t, ctx, store, scope, conversation.ID, "compose", "compose-output", conversationDashboardDraftInput(t))
	appendConversationVisualArtifact(t, ctx, store, scope, conversation.ID, "preview-call", "preview-artifact", conversationVisualArtifactArguments(), false)
	messages, err := store.ListMessages(ctx, scope.PrincipalID, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := PromptInput{Scope: scope, ConversationID: conversation.ID, Context: &TurnContext{Surface: "chat", PreviewArtifactID: "preview-artifact"}}
	items, err := service.promptDashboardPreviewContextItems(ctx, input, messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) < 3 || items[0].Key != "leapview_dashboard_draft" || items[len(items)-1].Key != "leapview_dashboard_preview" {
		t.Fatalf("preview replaced draft: %#v", items)
	}
	var source map[string]any
	if err := json.Unmarshal([]byte(conversationVisualArtifactArguments()), &source); err != nil {
		t.Fatal(err)
	}
	source["visual"].(map[string]any)["title"] = strings.Repeat("x", 16<<10)
	raw, _ := json.Marshal(source)
	appendConversationVisualArtifact(t, ctx, store, scope, conversation.ID, "oversized-call", "oversized", string(raw), false)
	messages, _ = store.ListMessages(ctx, scope.PrincipalID, conversation.ID)
	input.Context.PreviewArtifactID = "oversized"
	if _, err := service.promptDashboardPreviewContextItems(ctx, input, messages); err == nil {
		t.Fatal("oversized source accepted")
	}
}

func TestDashboardPreviewContextRemovesStaleSelectionAfterCompaction(t *testing.T) {
	stale := agentcore.Message{Role: agentcore.RoleUser, Kind: agentcore.MessageKindExternalContext, Content: "<external_leapview_dashboard_preview>\n{\"artifactId\":\"old\"}\n</external_leapview_dashboard_preview>"}
	summary := agentcore.Message{Role: agentcore.RoleSummary, Content: "A visual was previewed"}
	got := withoutPreviousDashboardDraftContext([]agentcore.Message{summary, stale})
	if len(got) != 1 || got[0].Content != summary.Content {
		t.Fatalf("stale preview retained: %#v", got)
	}
}

func TestStartPromptRestoresSelectedPreviewAfterCompaction(t *testing.T) {
	ctx, store, _, scope, conversation := newConversationVisualArtifactFixture(t)
	appendConversationVisualArtifact(t, ctx, store, scope, conversation.ID, "preview-call", "preview-artifact", conversationVisualArtifactArguments(), false)
	compacted, _ := json.Marshal([]agentcore.Message{{Role: agentcore.RoleSummary, Content: "A chart was created; its query is no longer in this summary."}})
	current, err := store.GetConversation(ctx, scope.PrincipalID, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateConversationTranscript(ctx, scope.PrincipalID, conversation.ID, string(compacted), current.TranscriptRevision); err != nil {
		t.Fatal(err)
	}
	model := newRecordingAgentModel(agentcore.ModelResponse{Content: "Ready", FinishReason: agentcore.FinishReasonStop})
	service := NewService(store, Config{APIKey: "key", Model: "fake-model"}, WithModel(model))
	started, err := service.StartPrompt(ctx, PromptInput{Scope: scope, ConversationID: conversation.ID, Input: "Change the preview", Context: &TurnContext{Surface: "chat", PreviewArtifactID: "preview-artifact"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompletePrompt(ctx, started, nil); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range started.initial {
		if strings.Contains(message.Content, "<external_leapview_dashboard_preview>") && strings.Contains(message.Content, "net_sales") && strings.Contains(message.Content, "region") {
			found = true
		}
	}
	if !found {
		t.Fatal("compacted turn lost selected preview source")
	}
}

func TestDashboardPreviewCannotAttachFutureArtifactToEditedTurn(t *testing.T) {
	ctx, store, service, scope, conversation := newConversationVisualArtifactFixture(t)
	original, err := store.AppendMessage(ctx, MessageInput{PrincipalID: scope.PrincipalID, ConversationID: conversation.ID, Role: MessageRoleUser, ContentText: "Create a chart"})
	if err != nil {
		t.Fatal(err)
	}
	appendConversationVisualArtifact(t, ctx, store, scope, conversation.ID, "preview-call", "preview-artifact", conversationVisualArtifactArguments(), false)
	messages, err := store.ListMessages(ctx, scope.PrincipalID, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.promptDashboardPreviewContextItems(ctx, PromptInput{Scope: scope, ConversationID: conversation.ID, EditMessageID: original.ID, Context: &TurnContext{Surface: "chat", PreviewArtifactID: "preview-artifact"}}, messages)
	if err == nil {
		t.Fatal("edited turn received future artifact source")
	}
}
