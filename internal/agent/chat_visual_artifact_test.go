package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestServiceConversationVisualArtifactReturnsPersistedQuerySource(t *testing.T) {
	ctx, store, service, scope, conversation := newConversationVisualArtifactFixture(t)
	artifactID := "agent_visual_sales"
	arguments := conversationVisualArtifactArguments()
	appendConversationVisualArtifact(t, ctx, store, scope, conversation.ID, "call_visual_sales", artifactID, arguments, false)

	got, err := service.ConversationVisualArtifact(ctx, scope, conversation.ID, artifactID)
	if err != nil {
		t.Fatalf("ConversationVisualArtifact() error = %v", err)
	}
	var want struct {
		SemanticModelID string                     `json:"semanticModelId"`
		Visual          document.DashboardVisual   `json:"visual"`
		Filters         []document.DashboardFilter `json:"filters"`
	}
	if err := json.Unmarshal([]byte(arguments), &want); err != nil {
		t.Fatalf("decode expected source: %v", err)
	}
	if got.ArtifactID != artifactID || got.ToolCallID != "call_visual_sales" {
		t.Fatalf("artifact identity = (%q, %q), want (%q, %q)", got.ArtifactID, got.ToolCallID, artifactID, "call_visual_sales")
	}
	if got.SemanticModelID != want.SemanticModelID {
		t.Fatalf("semantic model = %q, want %q", got.SemanticModelID, want.SemanticModelID)
	}
	if !reflect.DeepEqual(got.Visual, want.Visual) {
		t.Fatalf("visual source = %#v, want %#v", got.Visual, want.Visual)
	}
	if !reflect.DeepEqual(got.Filters, want.Filters) {
		t.Fatalf("filters = %#v, want %#v", got.Filters, want.Filters)
	}
}

func TestServiceConversationVisualArtifactPairsRepeatedCallIDsWithTheirOutputs(t *testing.T) {
	ctx, store, service, scope, conversation := newConversationVisualArtifactFixture(t)
	callID := "call_visual_reused"
	oldArguments := strings.Replace(conversationVisualArtifactArguments(), `"semanticModelId":"semantic_model_finance"`, `"semanticModelId":"semantic_model_old"`, 1)
	newArguments := strings.Replace(conversationVisualArtifactArguments(), `"semanticModelId":"semantic_model_finance"`, `"semanticModelId":"semantic_model_new"`, 1)
	appendConversationVisualArtifactWithOutputPart(t, ctx, store, scope, conversation.ID, callID, "part-old", "agent_visual_old", oldArguments, false)
	appendConversationVisualArtifactWithOutputPart(t, ctx, store, scope, conversation.ID, callID, "part-new", "agent_visual_new", newArguments, false)

	for artifactID, wantModelID := range map[string]string{
		"agent_visual_old": "semantic_model_old",
		"agent_visual_new": "semantic_model_new",
	} {
		t.Run(artifactID, func(t *testing.T) {
			got, err := service.ConversationVisualArtifact(ctx, scope, conversation.ID, artifactID)
			if err != nil {
				t.Fatalf("ConversationVisualArtifact() error = %v", err)
			}
			if got.SemanticModelID != wantModelID {
				t.Fatalf("semantic model = %q, want %q", got.SemanticModelID, wantModelID)
			}
		})
	}
}

func TestServiceConversationVisualArtifactUsesLatestRepeatedArtifact(t *testing.T) {
	ctx, store, service, scope, conversation := newConversationVisualArtifactFixture(t)
	callID := "call_visual_same_artifact"
	artifactID := "agent_visual_same_artifact"
	oldArguments := strings.Replace(conversationVisualArtifactArguments(), `"semanticModelId":"semantic_model_finance"`, `"semanticModelId":"semantic_model_old"`, 1)
	newArguments := strings.Replace(conversationVisualArtifactArguments(), `"semanticModelId":"semantic_model_finance"`, `"semanticModelId":"semantic_model_new"`, 1)
	appendConversationVisualArtifactWithOutputPart(t, ctx, store, scope, conversation.ID, callID, "part-old", artifactID, oldArguments, false)
	appendConversationVisualArtifactWithOutputPart(t, ctx, store, scope, conversation.ID, callID, "part-new", artifactID, newArguments, false)

	got, err := service.ConversationVisualArtifact(ctx, scope, conversation.ID, artifactID)
	if err != nil {
		t.Fatalf("ConversationVisualArtifact() error = %v", err)
	}
	if got.SemanticModelID != "semantic_model_new" {
		t.Fatalf("semantic model = %q, want latest output model %q", got.SemanticModelID, "semantic_model_new")
	}
}

func TestServiceConversationVisualArtifactRejectsFailedAndUnknownArtifacts(t *testing.T) {
	ctx, store, service, scope, conversation := newConversationVisualArtifactFixture(t)
	arguments := conversationVisualArtifactArguments()
	appendConversationVisualArtifact(t, ctx, store, scope, conversation.ID, "call_success", "agent_visual_success", arguments, false)
	appendConversationVisualArtifact(t, ctx, store, scope, conversation.ID, "call_failed", "agent_visual_failed", arguments, true)

	for _, artifactID := range []string{"agent_visual_failed", "agent_visual_unknown"} {
		t.Run(artifactID, func(t *testing.T) {
			if _, err := service.ConversationVisualArtifact(ctx, scope, conversation.ID, artifactID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("ConversationVisualArtifact() error = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestServiceConversationVisualArtifactRequiresConversationOwner(t *testing.T) {
	ctx, store, service, scope, conversation := newConversationVisualArtifactFixture(t)
	artifactID := "agent_visual_private"
	appendConversationVisualArtifact(t, ctx, store, scope, conversation.ID, "call_private", artifactID, conversationVisualArtifactArguments(), false)
	other := createAgentAppPrincipal(t, ctx, store, "other-visual-owner@example.com")
	otherScope := Scope{ProjectID: scope.ProjectID, PrincipalID: other.ID}

	if _, err := service.ConversationVisualArtifact(ctx, otherScope, conversation.ID, artifactID); err == nil {
		t.Fatal("ConversationVisualArtifact() succeeded for another principal")
	}
}

func TestServiceConversationVisualArtifactExcludesSupersededTranscript(t *testing.T) {
	ctx, store, service, scope, conversation := newConversationVisualArtifactFixture(t)
	original, err := store.AppendMessage(ctx, MessageInput{
		PrincipalID: scope.PrincipalID, ConversationID: conversation.ID,
		Role: MessageRoleUser, ContentText: "Show net sales by country",
	})
	if err != nil {
		t.Fatalf("append original user message: %v", err)
	}
	artifactID := "agent_visual_superseded"
	appendConversationVisualArtifact(t, ctx, store, scope, conversation.ID, "call_superseded", artifactID, conversationVisualArtifactArguments(), false)
	editMetadata, err := json.Marshal(map[string]string{"edit_message_id": original.ID})
	if err != nil {
		t.Fatalf("marshal edit metadata: %v", err)
	}
	if _, err := store.AppendMessage(ctx, MessageInput{
		PrincipalID: scope.PrincipalID, ConversationID: conversation.ID,
		Role: MessageRoleUser, ContentText: "Show a different visual",
		ContentJSON: string(editMetadata),
	}); err != nil {
		t.Fatalf("append edited user message: %v", err)
	}

	if _, err := service.ConversationVisualArtifact(ctx, scope, conversation.ID, artifactID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ConversationVisualArtifact() error = %v, want superseded artifact to be not found", err)
	}
}

func newConversationVisualArtifactFixture(t *testing.T) (context.Context, *testAgentStore, *Service, Scope, Conversation) {
	t.Helper()
	ctx := context.Background()
	store := openAgentAppStore(t, ctx)
	t.Cleanup(func() { _ = store.Close() })
	principal := createAgentAppPrincipal(t, ctx, store, "visual-artifact-"+t.Name()+"@example.com")
	scope := Scope{ProjectID: "sales", PrincipalID: principal.ID}
	service := NewService(store, Config{APIKey: "key", Model: "fake-model"})
	conversation, err := service.CreateConversation(ctx, scope, "Visual artifact")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	return ctx, store, service, scope, conversation
}

func conversationVisualArtifactArguments() string {
	return `{"semanticModelId":"semantic_model_finance","visual":{"type":"donut","title":"Net sales by country","query":{"type":"aggregate","dimensions":["country"],"metrics":["net_sales"]},"presentation":{"type":"proportional","legend":"bottom"}},"filters":[{"id":"region","label":"Region","dimension":"region","control":{"type":"text"}}]}`
}

func appendConversationVisualArtifact(t *testing.T, ctx context.Context, store *testAgentStore, scope Scope, conversationID, callID, artifactID, arguments string, failed bool) {
	t.Helper()
	appendConversationVisualArtifactWithOutputPart(t, ctx, store, scope, conversationID, callID, "", artifactID, arguments, failed)
}

func appendConversationVisualArtifactWithOutputPart(t *testing.T, ctx context.Context, store *testAgentStore, scope Scope, conversationID, callID, outputPartID, artifactID, arguments string, failed bool) {
	t.Helper()
	assistantContent, err := json.Marshal(map[string]any{
		"tool_calls": []any{map[string]any{
			"id": callID, "name": "query_visual", "arguments": json.RawMessage(arguments), "output_part_id": outputPartID,
		}},
	})
	if err != nil {
		t.Fatalf("marshal assistant tool call: %v", err)
	}
	if _, err := store.AppendMessage(ctx, MessageInput{
		PrincipalID: scope.PrincipalID, ConversationID: conversationID,
		Role: MessageRoleAssistant, ContentJSON: string(assistantContent),
	}); err != nil {
		t.Fatalf("append assistant tool call: %v", err)
	}
	toolContent, err := json.Marshal(map[string]any{
		"output_part_id": outputPartID,
		"display_content": map[string]any{
			"type": "donut", "id": artifactID, "summary": "Created chart.",
			"patch": map[string]any{"visuals": map[string]any{artifactID: map[string]any{"type": "donut"}}},
		},
	})
	if err != nil {
		t.Fatalf("marshal tool artifact: %v", err)
	}
	if _, err := store.AppendMessage(ctx, MessageInput{
		PrincipalID: scope.PrincipalID, ConversationID: conversationID,
		Role: MessageRoleTool, ToolCallID: callID, ToolName: "query_visual",
		ContentJSON: string(toolContent), IsError: failed,
	}); err != nil {
		t.Fatalf("append query_visual result: %v", err)
	}
}
