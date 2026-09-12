package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/agent"
	agentsqlite "github.com/flidai/leapview/internal/agent/sqlite"
	"github.com/flidai/leapview/internal/agent/ui"
	"github.com/flidai/leapview/internal/platform"
	jobsqlite "github.com/flidai/leapview/internal/platform/jobs/sqlite"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

func TestChatStopCancelsActiveRunAndPublishesSettledContinuationState(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "chat-stop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	owner, err := accesssqlite.NewRepository(store.SQLDB()).UpsertPrincipal(t.Context(), access.PrincipalInput{Email: "stop@example.com", DisplayName: "Stop"})
	if err != nil {
		t.Fatal(err)
	}
	startedModel := make(chan struct{})
	service := agent.NewService(agentsqlite.NewRepositoryWithEvents(store.SQLDB(), jobsqlite.NewRepository(store.SQLDB())), agent.Config{APIKey: "test", Model: "test"}, agent.WithModel(agentcore.ModelFunc(func(ctx context.Context, _ agentcore.ModelRequest, stream agentcore.ModelStream) (agentcore.ModelResponse, error) {
		if err := stream.Delta(ctx, "partial answer"); err != nil {
			return agentcore.ModelResponse{}, err
		}
		close(startedModel)
		<-ctx.Done()
		return agentcore.ModelResponse{}, ctx.Err()
	})))
	scope := agent.Scope{ProjectID: "project:stop-test", PrincipalID: owner.ID}
	conversation, err := service.CreateConversation(t.Context(), scope, "Stop")
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.StartPrompt(t.Context(), agent.PromptInput{Scope: scope, ConversationID: conversation.ID, Input: "Write an answer"})
	if err != nil {
		t.Fatal(err)
	}
	complete := make(chan error, 1)
	go func() {
		_, completeErr := started.Complete(context.Background(), nil)
		complete <- completeErr
	}()
	select {
	case <-startedModel:
	case completeErr := <-complete:
		t.Fatalf("prompt completed before stop: %v", completeErr)
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	case <-time.After(5 * time.Second):
		t.Fatal("prompt did not reach model")
	}

	handler := NewHandler(Options{
		Service:          service,
		ActiveProjectID:  scope.ProjectID,
		CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: owner.ID}, true },
		ChatSignalWith: func(_ context.Context, _ agent.Scope, activeID string, transcript []agent.ChatTranscriptItem, _ agent.ChatArtifactSignals, _ string, running bool) ui.ChatViewState {
			return ui.ChatViewState{Agent: ui.ChatSignal{ActiveConversationID: activeID, Transcript: ui.ChatTranscriptItems(transcript), Status: ui.ChatStatus{Enabled: true, Running: running}}}
		},
	})
	signals, err := json.Marshal(map[string]any{
		"agent": map[string]any{
			"activeConversationId": conversation.ID,
			"status":               map[string]any{"runId": started.RunID},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/chats/stop", bytes.NewReader(signals))
	request.Header.Set("X-LeapView-Operation-ID", cancelAgentRunOperation.APIGenOperationID())
	response := httptest.NewRecorder()
	handler.ChatStop(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("stop status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "canContinue") || !strings.Contains(response.Body.String(), "partial answer") {
		t.Fatalf("stop response did not publish settled transcript/status: %s", response.Body.String())
	}
	if err := <-complete; err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("complete error=%v, want cancellation", err)
	}
	run, err := service.GetRun(t.Context(), scope, conversation.ID, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != agent.RunStatusCanceled {
		t.Fatalf("run status=%q, want canceled", run.Status)
	}
	state, err := service.ConversationTranscriptState(t.Context(), scope, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Transcript) == 0 || !strings.Contains(state.Transcript[len(state.Transcript)-1].Text+state.Transcript[len(state.Transcript)-1].Markdown, "partial answer") {
		t.Fatalf("partial transcript=%#v, want persisted partial answer", state.Transcript)
	}
}

func TestChatStopRejectsStaleRunIdentity(t *testing.T) {
	fixture := newActiveChatFixture(t)
	conversation, err := fixture.service.CreateConversation(t.Context(), agent.Scope{PrincipalID: fixture.owner}, "Stop")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Options{
		Service: fixture.service, ActiveProjectID: "project:stop-test", CurrentPrincipal: fixture.ownerRequest,
	})
	signals, _ := json.Marshal(map[string]any{"agent": map[string]any{"activeConversationId": conversation.ID, "status": map[string]any{"runId": "stale-run"}}})
	request := httptest.NewRequest(http.MethodPost, "/chats/stop", bytes.NewReader(signals))
	request.Header.Set("X-LeapView-Operation-ID", cancelAgentRunOperation.APIGenOperationID())
	response := httptest.NewRecorder()
	handler.ChatStop(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("stale stop status=%d body=%s", response.Code, response.Body.String())
	}
}
