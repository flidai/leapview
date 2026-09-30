package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	agenttools "github.com/flidai/leapview/internal/agent/tools"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

func TestServiceConversationDashboardDraftPreservesSourceAndFollowsEditedBranch(t *testing.T) {
	ctx, store, service, scope, conversation := newConversationVisualArtifactFixture(t)
	original, err := store.AppendMessage(ctx, MessageInput{
		PrincipalID: scope.PrincipalID, ConversationID: conversation.ID,
		Role: MessageRoleUser, ContentText: "Build me a dashboard",
	})
	if err != nil {
		t.Fatalf("append original user message: %v", err)
	}
	input := conversationDashboardDraftInput(t)
	appendConversationDashboardDraft(t, ctx, store, scope, conversation.ID, "compose-finance", "part-finance", input)

	draft, err := service.ConversationDashboardDraft(ctx, scope, conversation.ID)
	if err != nil {
		t.Fatalf("ConversationDashboardDraft(): %v", err)
	}
	if draft.ToolCallID != "compose-finance" || draft.Title != "Finance" || draft.SemanticModelID != "semantic_model_finance" || len(draft.Visuals) != 1 {
		t.Fatalf("draft identity = %#v", draft)
	}
	if draft.Revision == "" || draft.Visuals[0].ID != "monthly_revenue" || draft.Visuals[0].ArtifactID != "monthly_revenue" {
		t.Fatalf("draft revision or visual identity = %#v", draft)
	}
	if len(draft.Visuals[0].Filters) != 1 || draft.Visuals[0].Filters[0].ID != "region" {
		t.Fatalf("draft filters = %#v, want the authored region filter", draft.Visuals[0].Filters)
	}

	other := createAgentAppPrincipal(t, ctx, store, "other-dashboard-draft-owner@example.com")
	if _, err := service.ConversationDashboardDraft(ctx, Scope{ProjectID: scope.ProjectID, PrincipalID: other.ID}, conversation.ID); err == nil {
		t.Fatal("ConversationDashboardDraft() succeeded for another owner")
	}

	edit, err := json.Marshal(map[string]string{"edit_message_id": original.ID})
	if err != nil {
		t.Fatalf("marshal edit metadata: %v", err)
	}
	if _, err := store.AppendMessage(ctx, MessageInput{
		PrincipalID: scope.PrincipalID, ConversationID: conversation.ID,
		Role: MessageRoleUser, ContentText: "Make a different dashboard", ContentJSON: string(edit),
	}); err != nil {
		t.Fatalf("append edited user message: %v", err)
	}
	if _, err := service.ConversationDashboardDraft(ctx, scope, conversation.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ConversationDashboardDraft() after rewind error = %v, want ErrNotFound", err)
	}
}

func TestStartPromptRestoresActiveDashboardSourceAfterTranscriptCompaction(t *testing.T) {
	ctx := context.Background()
	store := openAgentAppStore(t, ctx)
	t.Cleanup(func() { _ = store.Close() })
	owner := createAgentAppPrincipal(t, ctx, store, "compacted-draft-owner@example.com")
	model := newRecordingAgentModel(agentcore.ModelResponse{Content: "I will update the region filter.", FinishReason: agentcore.FinishReasonStop})
	service := NewService(store, Config{APIKey: "key", Model: "fake-model"}, WithModel(model))
	scope := Scope{ProjectID: "sales", PrincipalID: owner.ID}
	conversation, err := service.CreateConversation(ctx, scope, "Compacted draft")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	original, err := store.AppendMessage(ctx, MessageInput{
		PrincipalID: scope.PrincipalID, ConversationID: conversation.ID,
		Role: MessageRoleUser, ContentText: "Build the old dashboard",
	})
	if err != nil {
		t.Fatalf("append original user message: %v", err)
	}
	oldInput := conversationDashboardDraftInput(t)
	oldInput.Title = "Old Finance"
	oldVisualTitle := "Old sales by country"
	oldInput.Visuals[0].Visual.Title = &oldVisualTitle
	oldFilters := append([]document.DashboardFilter(nil), (*oldInput.Visuals[0].Filters)...)
	oldFilters[0].Label = "Old Region"
	oldInput.Visuals[0].Filters = &oldFilters
	appendConversationDashboardDraft(t, ctx, store, scope, conversation.ID, "compose-old", "part-old", oldInput)

	editMetadata, err := json.Marshal(map[string]string{"edit_message_id": original.ID})
	if err != nil {
		t.Fatalf("marshal edit metadata: %v", err)
	}
	if _, err := store.AppendMessage(ctx, MessageInput{
		PrincipalID: scope.PrincipalID, ConversationID: conversation.ID,
		Role: MessageRoleUser, ContentText: "Rebuild the dashboard with current values", ContentJSON: string(editMetadata),
	}); err != nil {
		t.Fatalf("append edited user message: %v", err)
	}
	currentInput := conversationDashboardDraftInput(t)
	currentInput.Title = "Current Finance"
	currentVisualTitle := "Current sales by country"
	currentInput.Visuals[0].Visual.Title = &currentVisualTitle
	currentFilters := append([]document.DashboardFilter(nil), (*currentInput.Visuals[0].Filters)...)
	currentFilters[0].Label = "Current Region"
	currentInput.Visuals[0].Filters = &currentFilters
	appendConversationDashboardDraft(t, ctx, store, scope, conversation.ID, "compose-current", "part-current", currentInput)

	// Compaction retains a short summary and recent prompt but omits the tool
	// invocation whose immutable message rows still own the complete source.
	compacted, err := json.Marshal([]agentcore.Message{
		{Role: agentcore.RoleSummary, Content: "The user is preparing a finance dashboard."},
		{Role: agentcore.RoleUser, Kind: agentcore.MessageKindExternalContext, Content: "<external_leapview_dashboard_draft>\n{\"title\":\"Old Finance\"}\n</external_leapview_dashboard_draft>"},
		{Role: agentcore.RoleUser, Kind: agentcore.MessageKindExternalContext, Content: "<external_leapview_dashboard_visual_00>\n{\"id\":\"old\",\"visual\":{\"title\":\"Old Region\"}}\n</external_leapview_dashboard_visual_00>"},
		{Role: agentcore.RoleUser, Kind: agentcore.MessageKindExternalContext, Content: "<external_leapview_context>\n{\"surface\":\"chat\"}\n</external_leapview_context>"},
		{Role: agentcore.RoleUser, Content: "Use the most recent layout."},
		{Role: agentcore.RoleAssistant, Content: "The current layout is ready for another update."},
	})
	if err != nil {
		t.Fatalf("marshal compacted transcript: %v", err)
	}
	currentConversation, err := store.GetConversation(ctx, scope.PrincipalID, conversation.ID)
	if err != nil {
		t.Fatalf("load conversation before compaction: %v", err)
	}
	if _, err := store.UpdateConversationTranscript(ctx, scope.PrincipalID, conversation.ID, string(compacted), currentConversation.TranscriptRevision); err != nil {
		t.Fatalf("persist compacted transcript: %v", err)
	}

	other := createAgentAppPrincipal(t, ctx, store, "other-compacted-draft-owner@example.com")
	if _, err := service.StartPrompt(ctx, PromptInput{Scope: Scope{ProjectID: scope.ProjectID, PrincipalID: other.ID}, ConversationID: conversation.ID, Input: "Change it"}); err == nil {
		t.Fatal("StartPrompt() exposed another owner's draft context")
	}
	started, err := service.StartPrompt(ctx, PromptInput{Scope: scope, ConversationID: conversation.ID, Input: "Change the region filter"})
	if err != nil {
		t.Fatalf("StartPrompt() after compaction: %v", err)
	}
	defer func() { _ = started.Abort(ctx, errors.New("test cleanup")) }()
	if _, err := service.CompletePrompt(ctx, started, nil); err != nil {
		t.Fatalf("CompletePrompt() with restored source: %v", err)
	}

	normalizedVisual, err := agenttools.NormalizeChatVisualDefinition(currentInput.Visuals[0].Visual)
	if err != nil {
		t.Fatalf("normalize expected current visual: %v", err)
	}
	wantSource, err := json.Marshal(struct {
		ID      string                     `json:"id"`
		Visual  document.DashboardVisual   `json:"visual"`
		Filters []document.DashboardFilter `json:"filters,omitempty"`
	}{
		ID: "monthly_revenue", Visual: normalizedVisual,
		Filters: *currentInput.Visuals[0].Filters,
	})
	if err != nil {
		t.Fatalf("marshal expected context source: %v", err)
	}
	wantContext := "<external_leapview_dashboard_visual_00>\n" + string(wantSource) + "\n</external_leapview_dashboard_visual_00>"
	var foundSource, foundManifest, foundNormalContext bool
	var sourceContextCount int
	requests := model.Requests()
	if len(requests) != 1 || requests[0].Purpose != agentcore.ModelRequestPurposeTurn {
		t.Fatalf("model requests = %#v, want one turn request", requests)
	}
	for _, message := range requests[0].Messages {
		if message.Kind != agentcore.MessageKindExternalContext {
			continue
		}
		if strings.Contains(message.Content, wantContext) {
			foundSource = true
		}
		if strings.Contains(message.Content, "<external_leapview_dashboard_visual_00>") {
			sourceContextCount++
		}
		if strings.Contains(message.Content, "<external_leapview_context>\n{\"surface\":\"chat\"}") {
			foundNormalContext = true
		}
		if strings.Contains(message.Content, `"title":"Current Finance"`) && strings.Contains(message.Content, `"visualIds":["monthly_revenue"]`) {
			foundManifest = true
		}
		if strings.Contains(message.Content, "Old Finance") || strings.Contains(message.Content, "Old Region") {
			t.Fatalf("compacted prompt context restored a superseded branch: %s", message.Content)
		}
	}
	if !foundSource || !foundManifest || !foundNormalContext || sourceContextCount != 1 {
		t.Fatalf("model prompt draft context = (source=%t, manifest=%t, normal=%t, source entries=%d), messages=%#v", foundSource, foundManifest, foundNormalContext, sourceContextCount, requests[0].Messages)
	}
}

func conversationDashboardDraftInput(t *testing.T) agenttools.ComposeChatDashboardInput {
	t.Helper()
	var source struct {
		Visual  document.DashboardVisual   `json:"visual"`
		Filters []document.DashboardFilter `json:"filters"`
	}
	if err := json.Unmarshal([]byte(conversationVisualArtifactArguments()), &source); err != nil {
		t.Fatalf("decode visual fixture: %v", err)
	}
	return agenttools.ComposeChatDashboardInput{
		Title: "Finance", SemanticModelID: "semantic_model_finance",
		Visuals: []agenttools.ChatDashboardVisualInput{{ID: "monthly_revenue", Visual: source.Visual, Filters: &source.Filters}},
	}
}

func appendConversationDashboardDraft(t *testing.T, ctx context.Context, store *testAgentStore, scope Scope, conversationID, callID, outputPartID string, input agenttools.ComposeChatDashboardInput) {
	t.Helper()
	arguments, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal dashboard draft source: %v", err)
	}
	normalizedInput := input
	normalizedInput.Visuals = append([]agenttools.ChatDashboardVisualInput(nil), input.Visuals...)
	for index := range normalizedInput.Visuals {
		normalizedInput.Visuals[index].Visual, err = agenttools.NormalizeChatVisualDefinition(normalizedInput.Visuals[index].Visual)
		if err != nil {
			t.Fatalf("normalize dashboard visual %d: %v", index, err)
		}
	}
	revision, err := agenttools.ChatDashboardDraftRevision(callID, normalizedInput)
	if err != nil {
		t.Fatalf("fingerprint dashboard draft source: %v", err)
	}
	envelopeBytes, err := os.ReadFile("../../api/visualization/conformance/cartesian-inline.json")
	if err != nil {
		t.Fatalf("read visualization conformance fixture: %v", err)
	}
	var envelope visualizationir.VisualizationEnvelope
	if err := json.Unmarshal(envelopeBytes, &envelope); err != nil {
		t.Fatalf("decode visualization conformance fixture: %v", err)
	}
	artifactID := input.Visuals[0].ID
	envelope.VisualID = artifactID
	if err := visualizationir.ValidateEnvelope(envelope); err != nil {
		t.Fatalf("validate visualization fixture: %v", err)
	}
	display := map[string]any{
		"type": "dashboard_draft", "id": callID, "revision": revision,
		"title": input.Title, "semanticModelId": input.SemanticModelID,
		"visuals": []any{map[string]any{"id": artifactID, "artifactId": artifactID, "title": "Net sales by country"}},
		"summary": "Composed Finance with 1 visuals.",
		"patch":   map[string]any{"visuals": map[string]any{artifactID: envelope}},
	}
	assistant, err := json.Marshal(map[string]any{"tool_calls": []any{map[string]any{
		"id": callID, "name": agenttools.ComposeChatDashboardToolName, "arguments": json.RawMessage(arguments), "output_part_id": outputPartID,
	}}})
	if err != nil {
		t.Fatalf("marshal dashboard draft call: %v", err)
	}
	if _, err := store.AppendMessage(ctx, MessageInput{
		PrincipalID: scope.PrincipalID, ConversationID: conversationID,
		Role: MessageRoleAssistant, ContentJSON: string(assistant),
	}); err != nil {
		t.Fatalf("append dashboard draft call: %v", err)
	}
	toolContent, err := json.Marshal(map[string]any{"output_part_id": outputPartID, "display_content": display})
	if err != nil {
		t.Fatalf("marshal dashboard draft output: %v", err)
	}
	if _, err := store.AppendMessage(ctx, MessageInput{
		PrincipalID: scope.PrincipalID, ConversationID: conversationID,
		Role: MessageRoleTool, ToolCallID: callID, ToolName: agenttools.ComposeChatDashboardToolName,
		ContentJSON: string(toolContent),
	}); err != nil {
		t.Fatalf("append dashboard draft output: %v", err)
	}
}
