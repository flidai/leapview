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

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/agent"
	agentsqlite "github.com/flidai/leapview/internal/agent/sqlite"
	"github.com/flidai/leapview/internal/agent/ui"
	"github.com/flidai/leapview/internal/platform"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/platform/transaction"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	agentcore "github.com/flidai/leapview/pkg/agent"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/flidai/leapview/pkg/pagestream"
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

func TestChatConversationDefersTranscriptAndConversationBootstrapToUpdatesStream(t *testing.T) {
	fixture := newActiveChatFixture(t)
	conversation, err := fixture.service.CreateConversation(t.Context(), agent.Scope{PrincipalID: fixture.owner}, "Owned")
	if err != nil {
		t.Fatal(err)
	}
	chatSignalCalls := 0
	chatSignalWithCalls := 0
	handler := NewHandler(Options{
		Service: fixture.service, CurrentPrincipal: fixture.ownerRequest,
		ChatSignal: func(context.Context, agent.Scope, string, string, bool) ui.ChatViewState {
			chatSignalCalls++
			return ui.ChatViewState{}
		},
		ChatSignalWith: func(context.Context, agent.Scope, string, []agent.ChatTranscriptItem, agent.ChatArtifactSignals, string, bool) ui.ChatViewState {
			chatSignalWithCalls++
			return ui.ChatViewState{}
		},
	})
	router := chi.NewRouter()
	router.Get("/chats/{conversation}", handler.ChatConversation)

	request := httptest.NewRequest(http.MethodGet, "/chats/"+conversation.ID, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("conversation status=%d body=%s", response.Code, response.Body.String())
	}
	if chatSignalCalls != 0 || chatSignalWithCalls != 0 {
		t.Fatalf("page request bootstrapped chat state: ChatSignal=%d ChatSignalWith=%d", chatSignalCalls, chatSignalWithCalls)
	}
	if !strings.Contains(response.Body.String(), "conversation="+conversation.ID) {
		t.Fatalf("page stream URL does not retain active conversation: %s", response.Body.String())
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
	for !strings.Contains(response.body(), "Live title") && time.Now().Before(deadline) {
		broker.Publish(streamID, pagestream.SignalPatch{"agent": map[string]any{"conversations": []map[string]any{{"id": liveConversation.ID, "title": liveConversation.Title}}}})
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if !strings.Contains(response.body(), "event: datastar-patch-signals") || !strings.Contains(response.body(), "Live title") {
		t.Fatalf("chat updates body=%s", response.body())
	}
}

func TestActiveChatTurnQueuesAndReturnsBeforeProviderExecution(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "chat-queued.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	owner, err := accesssqlite.NewRepository(store.SQLDB()).UpsertPrincipal(t.Context(), access.PrincipalInput{Email: "queued-chat@example.com", DisplayName: "Queued Chat"})
	if err != nil {
		t.Fatal(err)
	}
	repo := agentsqlite.NewRepositoryWithWorkflow(store.SQLDB(), nil, jobplatform.WorkflowRecorderFunc(func(context.Context, transaction.Transaction, jobs.WorkflowIntent) error {
		return nil
	}))
	service := agent.NewService(repo, agent.Config{APIKey: "test", Model: "test"}, agent.WithModel(agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
		return agentcore.ModelResponse{Content: "provider must not run in the command", FinishReason: agentcore.FinishReasonStop}, nil
	})))
	service.SetPromptWorkflow(func(input agent.PromptInput, runID string, _ agent.PromptDispatch) jobs.WorkflowIntent {
		return jobs.WorkflowIntent{Job: jobs.EnqueueInput{ID: "agent:" + runID + ":run", Kind: "agent.run", WorkloadClass: jobplatform.WorkloadClassBackground, PrincipalID: input.Scope.PrincipalID, ResourceKind: "agent_run", ResourceID: runID, EstimatedMemoryBytes: 1, Payload: []byte(`{}`)}}
	})
	conversation, err := service.CreateConversation(t.Context(), agent.Scope{PrincipalID: owner.ID}, "Queued")
	if err != nil {
		t.Fatal(err)
	}
	executed := false
	var queued *agent.StartedPrompt
	var observed []struct {
		transcript []agent.ChatTranscriptItem
		running    bool
		statusErr  string
	}
	handler := NewHandler(Options{
		Service: service,
		EnqueueChatRun: func(_ context.Context, _ agent.Scope, started *agent.StartedPrompt, _ string) error {
			queued = started
			return nil
		},
		ExecuteStartedChatTurn: func(context.Context, *agent.Service, agent.Scope, *agent.StartedPrompt, ChatTurnExecution) (agent.PromptResult, error) {
			executed = true
			return agent.PromptResult{}, nil
		},
		ChatSignalWith: func(_ context.Context, _ agent.Scope, activeID string, transcript []agent.ChatTranscriptItem, _ agent.ChatArtifactSignals, statusErr string, running bool) ui.ChatViewState {
			observed = append(observed, struct {
				transcript []agent.ChatTranscriptItem
				running    bool
				statusErr  string
			}{transcript: append([]agent.ChatTranscriptItem(nil), transcript...), running: running, statusErr: statusErr})
			return ui.ChatViewState{Agent: ui.ChatSignal{ActiveConversationID: activeID, Transcript: ui.ChatTranscriptItems(transcript), Status: ui.ChatStatus{Enabled: true, Running: running, Error: ui.Optional(statusErr)}}}
		},
	})
	scope := agent.Scope{PrincipalID: owner.ID}
	request := httptest.NewRequest(http.MethodPost, "/chats/turns", nil)
	request.Header.Set(uicommand.HeaderOperationID, createAgentRunOperation.APIGenOperationID())
	response := httptest.NewRecorder()
	startedAt := time.Now()
	handler.runChatTurn(response, request, service, scope, "queued-client", conversation.ID, "hello", nil, false)
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("queued chat command took %s", elapsed)
	}
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "datastar-patch-signals") {
		t.Fatalf("queued chat response status=%d body=%s", response.Code, response.Body.String())
	}
	if queued == nil || !queued.DurablyQueued() {
		t.Fatal("active chat turn was not durably queued")
	}
	if executed {
		t.Fatal("active chat turn executed provider work in the command request")
	}
	if len(observed) != 1 || !observed[0].running || observed[0].statusErr != "" {
		t.Fatalf("accepted queued signal = %#v, want one running signal without an error", observed)
	}
	foundInput := false
	for _, item := range observed[0].transcript {
		if item.Kind == "user" && item.Text == "hello" {
			foundInput = true
			break
		}
	}
	if !foundInput {
		t.Fatalf("accepted queued signal did not include persisted user turn: %#v", observed[0].transcript)
	}
}

func TestActiveChatTurnDoesNotOverwriteSettledWorkerTranscript(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "chat-queued-settled.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	owner, err := accesssqlite.NewRepository(store.SQLDB()).UpsertPrincipal(t.Context(), access.PrincipalInput{Email: "queued-settled@example.com", DisplayName: "Queued Settled"})
	if err != nil {
		t.Fatal(err)
	}
	repo := agentsqlite.NewRepositoryWithWorkflow(store.SQLDB(), nil, jobplatform.WorkflowRecorderFunc(func(context.Context, transaction.Transaction, jobs.WorkflowIntent) error {
		return nil
	}))
	service := agent.NewService(repo, agent.Config{APIKey: "test", Model: "test"}, agent.WithModel(agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
		return agentcore.ModelResponse{Content: "worker terminal output", FinishReason: agentcore.FinishReasonStop}, nil
	})))
	service.SetPromptWorkflow(func(input agent.PromptInput, runID string, _ agent.PromptDispatch) jobs.WorkflowIntent {
		return jobs.WorkflowIntent{Job: jobs.EnqueueInput{ID: "agent:" + runID + ":run", Kind: "agent.run", WorkloadClass: jobplatform.WorkloadClassBackground, PrincipalID: input.Scope.PrincipalID, ResourceKind: "agent_run", ResourceID: runID, EstimatedMemoryBytes: 1, Payload: []byte(`{}`)}}
	})
	scope := agent.Scope{PrincipalID: owner.ID}
	conversation, err := service.CreateConversation(t.Context(), scope, "Queued settled")
	if err != nil {
		t.Fatal(err)
	}
	var started *agent.StartedPrompt
	workerSettled := false
	handler := NewHandler(Options{
		Service: service,
		EnqueueChatRun: func(_ context.Context, _ agent.Scope, queued *agent.StartedPrompt, _ string) error {
			started = queued
			return nil
		},
		ChatSignalWith: func(ctx context.Context, _ agent.Scope, activeID string, transcript []agent.ChatTranscriptItem, _ agent.ChatArtifactSignals, statusErr string, running bool) ui.ChatViewState {
			if !workerSettled {
				workerSettled = true
				worker, resumeErr := service.ResumePrompt(ctx, scope, conversation.ID, started.RunID, "")
				if resumeErr != nil {
					t.Errorf("resume queued worker: %v", resumeErr)
				} else if _, completeErr := worker.Complete(context.Background(), nil); completeErr != nil {
					t.Errorf("complete queued worker: %v", completeErr)
				}
			}
			return ui.ChatViewState{Agent: ui.ChatSignal{ActiveConversationID: activeID, Transcript: ui.ChatTranscriptItems(transcript), Status: ui.ChatStatus{Enabled: true, Running: running, Error: ui.Optional(statusErr)}}}
		},
	})
	request := httptest.NewRequest(http.MethodPost, "/chats/turns", nil)
	request.Header.Set(uicommand.HeaderOperationID, createAgentRunOperation.APIGenOperationID())
	response := httptest.NewRecorder()
	handler.runChatTurn(response, request, service, scope, "queued-client", conversation.ID, "hello", nil, false)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "worker terminal output") {
		t.Fatalf("queued acknowledgement status=%d body=%s, want settled worker transcript", response.Code, response.Body.String())
	}
	run, err := service.GetRun(t.Context(), scope, conversation.ID, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != agent.RunStatusCompleted {
		t.Fatalf("run status=%q, want completed", run.Status)
	}
}

func TestActiveChatTurnUsesOnlyScopedStreamWhenBrokerConfigured(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "chat-queued-ordered.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	owner, err := accesssqlite.NewRepository(store.SQLDB()).UpsertPrincipal(t.Context(), access.PrincipalInput{Email: "queued-ordered@example.com", DisplayName: "Queued Ordered"})
	if err != nil {
		t.Fatal(err)
	}
	repo := agentsqlite.NewRepositoryWithWorkflow(store.SQLDB(), nil, jobplatform.WorkflowRecorderFunc(func(context.Context, transaction.Transaction, jobs.WorkflowIntent) error {
		return nil
	}))
	service := agent.NewService(repo, agent.Config{APIKey: "test", Model: "test"}, agent.WithModel(agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
		return agentcore.ModelResponse{Content: "unused", FinishReason: agentcore.FinishReasonStop}, nil
	})))
	service.SetPromptWorkflow(func(input agent.PromptInput, runID string, _ agent.PromptDispatch) jobs.WorkflowIntent {
		return jobs.WorkflowIntent{Job: jobs.EnqueueInput{ID: "agent:" + runID + ":run", Kind: "agent.run", WorkloadClass: jobplatform.WorkloadClassBackground, PrincipalID: input.Scope.PrincipalID, ResourceKind: "agent_run", ResourceID: runID, EstimatedMemoryBytes: 1, Payload: []byte(`{}`)}}
	})
	scope := agent.Scope{PrincipalID: owner.ID}
	conversation, err := service.CreateConversation(t.Context(), scope, "Ordered")
	if err != nil {
		t.Fatal(err)
	}
	signalCalls := 0
	handler := NewHandler(Options{
		Service: service,
		Broker:  pagestream.NewBroker(),
		EnqueueChatRun: func(context.Context, agent.Scope, *agent.StartedPrompt, string) error {
			return nil
		},
		ChatSignalWith: func(context.Context, agent.Scope, string, []agent.ChatTranscriptItem, agent.ChatArtifactSignals, string, bool) ui.ChatViewState {
			signalCalls++
			return ui.ChatViewState{}
		},
	})
	request := httptest.NewRequest(http.MethodPost, "/chats/turns", nil)
	request.Header.Set(uicommand.HeaderOperationID, createAgentRunOperation.APIGenOperationID())
	response := httptest.NewRecorder()
	handler.runChatTurn(response, request, service, scope, "ordered-client", conversation.ID, "hello", nil, false)
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("queued command status=%d body=%s, want empty success response", response.Code, response.Body.String())
	}
	if signalCalls != 0 {
		t.Fatalf("queued command built %d response signals, want scoped worker stream only", signalCalls)
	}
}

func TestChatConversationUpdatesIgnoreGlobalWorkerPatches(t *testing.T) {
	fixture := newActiveChatFixture(t)
	conversation, err := fixture.service.CreateConversation(t.Context(), agent.Scope{PrincipalID: fixture.owner}, "Scoped")
	if err != nil {
		t.Fatal(err)
	}
	broker := pagestream.NewBroker()
	handler := NewHandler(Options{Service: fixture.service, Broker: broker, CurrentPrincipal: fixture.ownerRequest})
	requestCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request := httptest.NewRequestWithContext(requestCtx, http.MethodGet, "/updates?route=chat&conversation="+conversation.ID, nil)
	request.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "scoped-client"})
	response := newActiveChatRecorder()
	done := make(chan struct{})
	go func() { defer close(done); handler.ChatUpdates(response, request) }()
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(response.body(), "datastar-patch-signals") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	globalID := chatStreamID(agent.Scope{PrincipalID: fixture.owner}, "scoped-client")
	conversationID := chatConversationStreamID(agent.Scope{PrincipalID: fixture.owner}, "scoped-client", conversation.ID)
	broker.Publish(globalID, pagestream.SignalPatch{"agent": map[string]any{"activeConversationId": "old-conversation"}})
	time.Sleep(20 * time.Millisecond)
	if strings.Contains(response.body(), "old-conversation") {
		cancel()
		<-done
		t.Fatal("conversation stream forwarded a global worker patch")
	}
	broker.Publish(conversationID, pagestream.SignalPatch{"agent": map[string]any{"status": map[string]any{"error": "scoped-worker-ready"}}})
	patchDeadline := time.Now().Add(time.Second)
	for !strings.Contains(response.body(), "scoped-worker-ready") && time.Now().Before(patchDeadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if !strings.Contains(response.body(), "scoped-worker-ready") {
		t.Fatalf("conversation stream did not forward scoped patch: %s", response.body())
	}
}
