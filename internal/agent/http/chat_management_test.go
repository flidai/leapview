package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/agent/ui"
	"github.com/go-chi/chi/v5"
)

func TestChatManagementStateKeepsChatSignalStateUntouched(t *testing.T) {
	handler := NewHandler(Options{
		ChatSignal: func(_ context.Context, _ agent.Scope, _, _ string, _ bool) ui.ChatViewState {
			return ui.ChatViewState{Agent: ui.ChatSignal{
				ActiveConversationID: "conversation-1",
				Conversations:        []ui.ChatConversationSummary{{ID: "conversation-1", Title: "Revenue"}},
				Transcript:           []ui.ChatTranscriptItemSignal{{ID: "message-1", Kind: "user"}},
			}}
		},
	})
	request := httptest.NewRequest(http.MethodPost, "/chats/manage", nil)
	request.Header.Set("Referer", "https://example.test/chats/conversation-1")
	response := httptest.NewRecorder()

	handler.writeChatManagementState(response, request, agent.Scope{}, chatManagementSignal{
		Action:                conversationManagementActionPin,
		ConversationID:        "conversation-1",
		ArchivedConversations: []ui.ChatConversationSummary{},
	})

	body := response.Body.String()
	if !strings.Contains(body, `"chatManagement"`) || !strings.Contains(body, `"conversations"`) {
		t.Fatalf("management patch = %s, missing management/conversation state", body)
	}
	for _, field := range []string{`"transcript"`, `"composer"`, `"agent":{"status"`, `"activeConversationId"`} {
		if strings.Contains(body, field) {
			t.Fatalf("management patch replaced chat field %s: %s", field, body)
		}
	}
}

func TestChatManagementActiveConversationIDUsesOnlyChatReferrerPath(t *testing.T) {
	tests := []struct {
		name     string
		referrer string
		want     string
	}{
		{name: "conversation", referrer: "https://example.test/chats/conversation-1", want: "conversation-1"},
		{name: "escaped", referrer: "https://example.test/chats/conversation%201", want: "conversation 1"},
		{name: "chat list", referrer: "https://example.test/chats", want: ""},
		{name: "new chat", referrer: "https://example.test/chats/new", want: ""},
		{name: "other page", referrer: "https://example.test/dashboards/sales", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/chats/manage", nil)
			request.Header.Set("Referer", tt.referrer)
			if got := chatManagementActiveConversationID(request); got != tt.want {
				t.Fatalf("active conversation = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestChatManagementDoesNotRequireConfiguredModel(t *testing.T) {
	service := &agent.Service{}
	handler := NewHandler(Options{Service: service, CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: "owner"}, true }})
	response := httptest.NewRecorder()
	got, scope, ok := handler.conversationManagementRequest(response, httptest.NewRequest(http.MethodPost, "/chats/manage", nil))
	if !ok || got != service || scope.PrincipalID != "owner" {
		t.Fatalf("management unavailable without model: ok=%v scope=%+v", ok, scope)
	}
	handler = NewHandler(Options{Service: service})
	response = httptest.NewRecorder()
	if _, _, ok := handler.conversationManagementRequest(response, httptest.NewRequest(http.MethodPost, "/chats/manage", nil)); ok || response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated management status=%d ok=%v", response.Code, ok)
	}
}

func TestChatManagementDoesNotAddChatHistoryToSettingsNavigation(t *testing.T) {
	handler := NewHandler(Options{})
	request := httptest.NewRequest(http.MethodGet, "/chats/management", nil)
	request.Header.Set("Referer", "http://localhost/admin/profile")
	response := httptest.NewRecorder()
	handler.writeChatManagementState(response, request, agent.Scope{}, chatManagementSignal{})
	if strings.Contains(response.Body.String(), `"chrome"`) {
		t.Fatalf("settings navigation replaced: %s", response.Body.String())
	}
}

func TestDeletedChatReturnsNotFoundFromConversationAPI(t *testing.T) {
	service, principalID := commandAuditService(t)
	scope := agent.Scope{PrincipalID: principalID}
	conversation, err := service.CreateConversation(context.Background(), scope, "Disposable deletion test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteConversation(context.Background(), scope, conversation.ID); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Options{Service: service, CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: principalID}, true }})
	router := chi.NewRouter()
	router.Get("/agent/conversations/{conversation}", handler.GetConversation)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/agent/conversations/"+conversation.ID, nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("deleted conversation status=%d body=%s", response.Code, response.Body.String())
	}
}

type missingConversationRepository struct{ agent.Repository }

func (missingConversationRepository) GetConversation(context.Context, string, string) (agent.Conversation, error) {
	return agent.Conversation{}, agent.ErrNotFound
}
func TestConversationAPIReturnsNotFoundForNativeRepositoryError(t *testing.T) {
	service := agent.NewService(missingConversationRepository{}, agent.Config{APIKey: "test", Model: "test"})
	handler := NewHandler(Options{Service: service, CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: "owner"}, true }})
	router := chi.NewRouter()
	router.Get("/agent/conversations/{conversation}", handler.GetConversation)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/agent/conversations/deleted", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing conversation status=%d body=%s", response.Code, response.Body.String())
	}
}
