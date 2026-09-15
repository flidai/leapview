package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

func TestActiveChatEditReplacesSelectedTurnInsteadOfAppending(t *testing.T) {
	fixture := newActiveChatFixture(t)
	scope := agent.Scope{PrincipalID: fixture.owner}
	conversation, err := fixture.service.CreateConversation(t.Context(), scope, "Edit test")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Original question", "Later question"} {
		if _, err = fixture.service.Prompt(t.Context(), agent.PromptInput{Scope: scope, ConversationID: conversation.ID, Input: text}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := fixture.service.ConversationTranscriptState(t.Context(), scope, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	var selectedID string
	for _, item := range before.Transcript {
		if item.Kind == "user" && item.Text == "Original question" {
			selectedID = item.ID
			break
		}
	}
	if selectedID == "" {
		t.Fatal("missing selected user message")
	}
	handler := NewHandler(Options{Service: fixture.service, ActiveProjectID: "project_test", CurrentPrincipal: fixture.ownerRequest, ExecuteStartedChatTurn: func(ctx context.Context, _ *agent.Service, _ agent.Scope, started *agent.StartedPrompt, _ ChatTurnExecution) (agent.PromptResult, error) {
		return started.Complete(ctx, nil)
	}})
	body, _ := json.Marshal(map[string]any{"agent": map[string]any{"activeConversationId": conversation.ID, "composer": map[string]any{"value": "Revised question", "editMessageId": selectedID}}, "agentContext": map[string]any{"surface": "chat"}})
	request := httptest.NewRequest(http.MethodPost, "/chats/turns", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "edit-client"})
	request.Header.Set(uicommand.HeaderOperationID, createAgentRunOperation.APIGenOperationID())
	response := httptest.NewRecorder()
	handler.ChatTurn(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	after, err := fixture.service.ConversationTranscriptState(t.Context(), scope, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	var questions []string
	for _, item := range after.Transcript {
		if item.Kind == "user" {
			questions = append(questions, item.Text)
			if !item.Edited {
				t.Fatal("revised user message must be marked edited after reload")
			}
		}
	}
	if len(questions) != 1 || questions[0] != "Revised question" {
		t.Fatalf("active questions=%v; expected only revised question", questions)
	}
}
