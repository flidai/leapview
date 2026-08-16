package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/agent"
	agentsqlite "github.com/flidai/leapview/internal/agent/sqlite"
	"github.com/flidai/leapview/internal/agent/ui"
	"github.com/flidai/leapview/internal/platform"
	agentcore "github.com/flidai/leapview/pkg/agent"
	"github.com/go-chi/chi/v5"
)

type activeChatFixture struct {
	service      *agent.Service
	owner, other string
	store        *platform.Store
	ownerRequest func(*http.Request) (Principal, bool)
	otherRequest func(*http.Request) (Principal, bool)
}

type activeChatRecorder struct {
	mu  sync.Mutex
	rec *httptest.ResponseRecorder
}

func newActiveChatRecorder() *activeChatRecorder {
	return &activeChatRecorder{rec: httptest.NewRecorder()}
}

func (r *activeChatRecorder) Header() http.Header { return r.rec.Header() }

func (r *activeChatRecorder) WriteHeader(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rec.WriteHeader(status)
}

func (r *activeChatRecorder) Write(bytes []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec.Write(bytes)
}

func (r *activeChatRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rec.Flush()
}

func (r *activeChatRecorder) body() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec.Body.String()
}

func newActiveChatFixture(t *testing.T) activeChatFixture {
	t.Helper()
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "chat-active.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := accesssqlite.NewRepository(store.SQLDB())
	owner, err := repo.UpsertPrincipal(t.Context(), access.PrincipalInput{Email: "chat-owner@example.com", DisplayName: "Chat Owner"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := repo.UpsertPrincipal(t.Context(), access.PrincipalInput{Email: "chat-other@example.com", DisplayName: "Chat Other"})
	if err != nil {
		t.Fatal(err)
	}
	model := agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
		return agentcore.ModelResponse{Content: "Generated title", FinishReason: agentcore.FinishReasonStop}, nil
	})
	service := agent.NewService(agentsqlite.NewRepository(store.SQLDB()), agent.Config{APIKey: "test", Model: "test"}, agent.WithModel(model))
	return activeChatFixture{
		service: service, owner: owner.ID, other: other.ID, store: store,
		ownerRequest: func(*http.Request) (Principal, bool) { return Principal{ID: owner.ID}, true },
		otherRequest: func(*http.Request) (Principal, bool) { return Principal{ID: other.ID}, true },
	}
}

func TestChatConversationRouteEnforcesPrincipalOwnership(t *testing.T) {
	fixture := newActiveChatFixture(t)
	ctx := t.Context()
	owned, err := fixture.service.CreateConversation(ctx, agent.Scope{PrincipalID: fixture.owner}, "Owned")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Options{Service: fixture.service, CurrentPrincipal: fixture.ownerRequest})
	router := chi.NewRouter()
	router.Get("/chats/{conversation}", handler.ChatConversation)

	request := httptest.NewRequest(http.MethodGet, "/chats/"+owned.ID, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `<lv-chat-page`) {
		t.Fatalf("owned conversation status=%d body=%s", response.Code, response.Body.String())
	}

	hidden, err := fixture.service.CreateConversation(ctx, agent.Scope{PrincipalID: fixture.other}, "Hidden")
	if err != nil {
		t.Fatal(err)
	}
	hiddenHandler := NewHandler(Options{Service: fixture.service, CurrentPrincipal: fixture.ownerRequest})
	hiddenRouter := chi.NewRouter()
	hiddenRouter.Get("/chats/{conversation}", hiddenHandler.ChatConversation)
	hiddenRequest := httptest.NewRequest(http.MethodGet, "/chats/"+hidden.ID, nil)
	hiddenResponse := httptest.NewRecorder()
	hiddenRouter.ServeHTTP(hiddenResponse, hiddenRequest)
	if hiddenResponse.Code != http.StatusNotFound {
		t.Fatalf("hidden conversation status=%d body=%s", hiddenResponse.Code, hiddenResponse.Body.String())
	}
}

func TestChatConversationQueuesTitleRepairOnlyAfterOwnedRestore(t *testing.T) {
	fixture := newActiveChatFixture(t)
	conversation, err := fixture.service.CreateConversation(t.Context(), agent.Scope{PrincipalID: fixture.owner}, "")
	if err != nil {
		t.Fatal(err)
	}
	var queuedID, queuedClient string
	handler := NewHandler(Options{
		Service: fixture.service, CurrentPrincipal: fixture.ownerRequest,
		QueueMissingTitle: func(_ context.Context, _ agent.Scope, id, client string) { queuedID, queuedClient = id, client },
	})
	router := chi.NewRouter()
	router.Get("/chats/{conversation}", handler.ChatConversation)
	request := httptest.NewRequest(http.MethodGet, "/chats/"+conversation.ID, nil)
	request.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "title-client"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if queuedID != conversation.ID || queuedClient != "title-client" {
		t.Fatalf("queued title repair = %q/%q, want %q/title-client", queuedID, queuedClient, conversation.ID)
	}
}

func TestChatRestoreStreamsOwnedStateAndClearsUnauthorizedState(t *testing.T) {
	fixture := newActiveChatFixture(t)
	owned, err := fixture.service.CreateConversation(t.Context(), agent.Scope{PrincipalID: fixture.owner}, "Owned")
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := fixture.service.CreateConversation(t.Context(), agent.Scope{PrincipalID: fixture.other}, "Hidden")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Options{
		Service: fixture.service, CurrentPrincipal: fixture.ownerRequest,
		ChatSignal: func(_ context.Context, _ agent.Scope, activeID, _ string, _ bool) ui.ChatViewState {
			return ui.ChatViewState{Agent: ui.ChatSignal{ActiveConversationID: activeID}}
		},
		ChatSignalWith: func(_ context.Context, _ agent.Scope, activeID string, transcript []agent.ChatTranscriptItem, _ agent.ChatArtifactSignals, _ string, _ bool) ui.ChatViewState {
			return ui.ChatViewState{Agent: ui.ChatSignal{ActiveConversationID: activeID, Transcript: ui.ChatTranscriptItems(transcript)}}
		},
	})

	restore := func(id string) *httptest.ResponseRecorder {
		signals, _ := json.Marshal(map[string]any{"agent": map[string]any{"activeConversationId": id}})
		request := httptest.NewRequest(http.MethodGet, "/chats/restore?datastar="+url.QueryEscape(string(signals)), nil)
		response := httptest.NewRecorder()
		handler.ChatRestore(response, request)
		return response
	}

	ownedResponse := restore(owned.ID)
	if ownedResponse.Code != http.StatusOK || !strings.Contains(ownedResponse.Body.String(), "event: datastar-patch-signals") || !strings.Contains(ownedResponse.Body.String(), owned.ID) {
		t.Fatalf("owned restore status=%d body=%s", ownedResponse.Code, ownedResponse.Body.String())
	}
	hiddenResponse := restore(hidden.ID)
	if hiddenResponse.Code != http.StatusOK || strings.Contains(hiddenResponse.Body.String(), hidden.ID) || !strings.Contains(hiddenResponse.Body.String(), `"activeConversationId":""`) {
		t.Fatalf("unauthorized restore status=%d body=%s", hiddenResponse.Code, hiddenResponse.Body.String())
	}
}

func TestChatUpdatesForwardsDatastarConversationPatches(t *testing.T) {
	fixture := newActiveChatFixture(t)
	liveConversation, err := fixture.service.CreateConversation(t.Context(), agent.Scope{PrincipalID: fixture.owner}, "Live title")
	if err != nil {
		t.Fatal(err)
	}
	broker := pagestream.NewBroker()
	handler := NewHandler(Options{
		Service: fixture.service, Broker: broker, CurrentPrincipal: fixture.ownerRequest,
		ChatSignal: func(context.Context, agent.Scope, string, string, bool) ui.ChatViewState { return ui.ChatViewState{} },
	})
	requestCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request := httptest.NewRequestWithContext(requestCtx, http.MethodGet, "/updates?route=chat", nil)
	request.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "chat-client"})
	response := newActiveChatRecorder()
	done := make(chan struct{})
	go func() { defer close(done); handler.ChatUpdates(response, request) }()
	streamID := chatStreamID(agent.Scope{PrincipalID: fixture.owner}, "chat-client")
	deadline := time.Now().Add(time.Second)
	for broker.SubscriberCount(streamID) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if broker.SubscriberCount(streamID) == 0 {
		t.Fatal("chat updates stream did not subscribe")
	}
	broker.Publish(streamID, pagestream.SignalPatch{"agent": map[string]any{"conversations": []map[string]any{{"id": liveConversation.ID, "title": liveConversation.Title}}}})
	for !strings.Contains(response.body(), "Live title") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if !strings.Contains(response.body(), "event: datastar-patch-signals") || !strings.Contains(response.body(), "Live title") {
		t.Fatalf("chat updates body=%s", response.body())
	}
}
