package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/agent"
	agentsqlite "github.com/flidai/leapview/internal/agent/sqlite"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	agentcore "github.com/flidai/leapview/pkg/agent"
	"github.com/go-chi/chi/v5"
)

func TestAgentAPICommandsRecordOneSuccessAudit(t *testing.T) {
	service, principalID := commandAuditService(t)
	var audits []CommandAuditInput
	handler := NewHandler(Options{
		Service: service,
		CurrentPrincipal: func(*http.Request) (Principal, bool) {
			return Principal{ID: principalID}, true
		},
		EnqueueRun: func(context.Context, agent.Scope, *agent.StartedPrompt) error { return nil },
		BuildAuditIntent: func(_ context.Context, input CommandAuditInput) (*access.AuditIntent, error) {
			audits = append(audits, input)
			return commandAuditTestIntent(input), nil
		},
	})
	router := chi.NewRouter()
	router.Post("/agent/conversations", handler.CreateConversation)
	router.Patch("/agent/conversations/{conversation}", handler.UpdateConversation)
	router.Delete("/agent/conversations/{conversation}", handler.ArchiveConversation)
	router.Post("/agent/conversations/{conversation}/runs", handler.CreateRun)
	router.Post("/agent/conversations/{conversation}/runs/{run}/cancel", handler.CancelRun)

	create := commandAuditRequest(http.MethodPost, "/agent/conversations", `{"title":"Audit me"}`)
	created := httptest.NewRecorder()
	router.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create conversation status = %d body=%s", created.Code, created.Body.String())
	}
	conversationID := decodeResponseID(t, created.Body.Bytes())

	update := commandAuditRequest(http.MethodPatch, "/agent/conversations/"+conversationID, `{"title":"Audited"}`)
	update.Header.Set("If-Match", "*")
	updated := httptest.NewRecorder()
	router.ServeHTTP(updated, update)
	if updated.Code != http.StatusOK {
		t.Fatalf("update conversation status = %d body=%s", updated.Code, updated.Body.String())
	}

	createRun := commandAuditRequest(http.MethodPost, "/agent/conversations/"+conversationID+"/runs", `{"input":"hello"}`)
	createRun.Header.Set("Idempotency-Key", "agent-run-audit")
	runCreated := httptest.NewRecorder()
	router.ServeHTTP(runCreated, createRun)
	if runCreated.Code != http.StatusAccepted {
		t.Fatalf("create run status = %d body=%s", runCreated.Code, runCreated.Body.String())
	}
	runID := decodeResponseID(t, runCreated.Body.Bytes())

	cancel := commandAuditRequest(http.MethodPost, "/agent/conversations/"+conversationID+"/runs/"+runID+"/cancel", ``)
	cancelled := httptest.NewRecorder()
	router.ServeHTTP(cancelled, cancel)
	if cancelled.Code != http.StatusAccepted {
		t.Fatalf("cancel run status = %d body=%s", cancelled.Code, cancelled.Body.String())
	}

	archive := commandAuditRequest(http.MethodDelete, "/agent/conversations/"+conversationID, ``)
	archived := httptest.NewRecorder()
	router.ServeHTTP(archived, archive)
	if archived.Code != http.StatusNoContent {
		t.Fatalf("archive conversation status = %d body=%s", archived.Code, archived.Body.String())
	}

	want := []string{
		createAgentConversationOperation.APIGenOperationID(),
		updateAgentConversationOperation.APIGenOperationID(),
		createAgentRunOperation.APIGenOperationID(),
		cancelAgentRunOperation.APIGenOperationID(),
		archiveAgentConversationOperation.APIGenOperationID(),
	}
	if len(audits) != len(want) {
		t.Fatalf("command audits = %#v", audits)
	}
	for index, operationID := range want {
		if audits[index].OperationID != operationID || audits[index].Scope.PrincipalID != principalID {
			t.Fatalf("command audit[%d] = %#v, want operation %s for %s", index, audits[index], operationID, conversationID)
		}
	}
}

func TestAgentConversationUpdateUsesCanonicalRevisionAndProtocolFailure(t *testing.T) {
	service, principalID := commandAuditService(t)
	handler := NewHandler(Options{
		Service: service,
		CurrentPrincipal: func(*http.Request) (Principal, bool) {
			return Principal{ID: principalID}, true
		},
		BuildAuditIntent: func(_ context.Context, input CommandAuditInput) (*access.AuditIntent, error) {
			return commandAuditTestIntent(input), nil
		},
	})
	router := chi.NewRouter()
	router.Post("/agent/conversations", handler.CreateConversation)
	router.Get("/agent/conversations/{conversation}", handler.GetConversation)
	router.Patch("/agent/conversations/{conversation}", handler.UpdateConversation)

	created := httptest.NewRecorder()
	router.ServeHTTP(created, commandAuditRequest(http.MethodPost, "/agent/conversations", `{"title":"Revision"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", created.Code, created.Body.String())
	}
	conversationID := decodeResponseID(t, created.Body.Bytes())
	createETag := created.Header().Get("ETag")
	if createETag == "" {
		t.Fatal("create response omitted ETag")
	}

	got := httptest.NewRecorder()
	router.ServeHTTP(got, httptest.NewRequest(http.MethodGet, "/agent/conversations/"+conversationID, nil))
	if got.Code != http.StatusOK || got.Header().Get("ETag") != createETag {
		t.Fatalf("get status/etag = %d/%q, want 200/%q", got.Code, got.Header().Get("ETag"), createETag)
	}

	first := commandAuditRequest(http.MethodPatch, "/agent/conversations/"+conversationID, `{"title":"First"}`)
	first.Header.Set("If-Match", createETag)
	updated := httptest.NewRecorder()
	router.ServeHTTP(updated, first)
	if updated.Code != http.StatusOK {
		t.Fatalf("first update status = %d body=%s", updated.Code, updated.Body.String())
	}

	stale := commandAuditRequest(http.MethodPatch, "/agent/conversations/"+conversationID, `{"title":"Stale"}`)
	stale.Header.Set("If-Match", createETag)
	failed := httptest.NewRecorder()
	router.ServeHTTP(failed, stale)
	if failed.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale update status = %d body=%s", failed.Code, failed.Body.String())
	}
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(failed.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode stale problem: %v", err)
	}
	if problem.Code != "PRECONDITION_FAILED" {
		t.Fatalf("stale update code = %q", problem.Code)
	}
}

func TestAgentUICommandInvocationUsesStableGeneratedIdentity(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/chats/turns", nil)
	r.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "client-fixed"})
	identity := uiRequestIdentity(r, "hello")
	r.Header.Set(uicommand.HeaderOperationID, createAgentConversationOperation.APIGenOperationID())
	ctx, err := beginUICommandInvocation(r, agentUIBinding(createAgentConversationOperation), nil, "", "hello", identity)
	if err != nil {
		t.Fatalf("begin create UI invocation: %v", err)
	}
	if operationID, ok := apigencommand.OperationID(ctx); !ok || operationID != createAgentConversationOperation.APIGenOperationID() {
		t.Fatalf("operation ID = %q/%v", operationID, ok)
	}
	if got := r.Header.Get("X-Request-ID"); got != identity {
		t.Fatalf("request identity = %q, want %q", got, identity)
	}
	if got := r.Header.Get("X-LeapView-Invocation-Surface"); got != "ui" {
		t.Fatalf("surface = %q", got)
	}

	r.Header.Set(uicommand.HeaderOperationID, createAgentRunOperation.APIGenOperationID())
	runCtx, err := beginUICommandInvocation(r, agentUIBinding(createAgentRunOperation), nil, "conversation-1", "hello", identity)
	if err != nil {
		t.Fatalf("begin run UI invocation: %v", err)
	}
	if operationID, ok := apigencommand.OperationID(runCtx); !ok || operationID != createAgentRunOperation.APIGenOperationID() {
		// The second invocation should replace the context state with the run
		// operation while preserving the same request identity.
		t.Fatalf("run operation ID = %q/%v", operationID, ok)
	}
}

func TestAgentCommandRejectsMutationWhenDurableAuditIntentCannotBeBuilt(t *testing.T) {
	service, principalID := commandAuditService(t)
	var logs bytes.Buffer
	handler := NewHandler(Options{
		Service: service,
		CurrentPrincipal: func(*http.Request) (Principal, bool) {
			return Principal{ID: principalID}, true
		},
		BuildAuditIntent: func(context.Context, CommandAuditInput) (*access.AuditIntent, error) {
			return nil, errors.New("audit store unavailable")
		},
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	response := httptest.NewRecorder()
	handler.CreateConversation(response, commandAuditRequest(http.MethodPost, "/agent/conversations", `{"title":"Audit me"}`))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("create conversation status = %d body=%s", response.Code, response.Body.String())
	}
	if output := logs.String(); strings.Contains(output, principalID) {
		t.Fatalf("audit failure log leaked principal identity: %s", output)
	}
}

func TestAgentChatDraftAndActiveTurnsAuditCreatedCommandsOnce(t *testing.T) {
	service, principalID := commandAuditService(t)
	var audits []CommandAuditInput
	handler := NewHandler(Options{
		Service:        service,
		EnqueueChatRun: func(context.Context, agent.Scope, *agent.StartedPrompt, string) error { return nil },
		ExecuteStartedChatTurn: func(context.Context, *agent.Service, agent.Scope, *agent.StartedPrompt, ChatTurnExecution) (agent.PromptResult, error) {
			return agent.PromptResult{}, nil
		},
		BuildAuditIntent: func(_ context.Context, input CommandAuditInput) (*access.AuditIntent, error) {
			audits = append(audits, input)
			return commandAuditTestIntent(input), nil
		},
	})
	scope := agent.Scope{PrincipalID: principalID}
	draftRequest := httptest.NewRequest(http.MethodPost, "/chats/turns", nil)
	draftRequest.Header.Set(uicommand.HeaderOperationID, strings.Join([]string{createAgentConversationOperation.APIGenOperationID(), createAgentRunOperation.APIGenOperationID()}, ","))
	draftResponse := httptest.NewRecorder()
	handler.startDraftChatTurn(draftResponse, draftRequest, service, scope, "client-1", "hello", nil, false)
	if draftResponse.Code != http.StatusOK {
		t.Fatalf("draft chat status = %d body=%s", draftResponse.Code, draftResponse.Body.String())
	}
	if len(audits) != 2 || audits[0].OperationID != createAgentConversationOperation.APIGenOperationID() || audits[1].OperationID != createAgentRunOperation.APIGenOperationID() || audits[1].TargetID == "" {
		t.Fatalf("draft chat audits = %#v", audits)
	}

	conversation, err := service.CreateConversation(t.Context(), scope, "Existing")
	if err != nil {
		t.Fatal(err)
	}
	audits = nil
	activeRequest := httptest.NewRequest(http.MethodPost, "/chats/turns", nil)
	activeRequest.Header.Set(uicommand.HeaderOperationID, createAgentRunOperation.APIGenOperationID())
	activeResponse := httptest.NewRecorder()
	handler.runChatTurn(activeResponse, activeRequest, service, scope, "client-2", conversation.ID, "again", nil, true)
	if len(audits) != 1 || audits[0].OperationID != createAgentRunOperation.APIGenOperationID() || audits[0].TargetID != conversation.ID {
		t.Fatalf("active chat audits = %#v", audits)
	}
}

func commandAuditService(t *testing.T) (*agent.Service, string) {
	t.Helper()
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "agent-audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	principal, err := accesssqlite.NewRepository(store.SQLDB()).UpsertPrincipal(t.Context(), access.PrincipalInput{
		Email: "agent-audit@example.com", DisplayName: "Agent Audit",
	})
	if err != nil {
		t.Fatal(err)
	}
	model := agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
		return agentcore.ModelResponse{Content: "ok", FinishReason: agentcore.FinishReasonStop}, nil
	})
	return agent.NewService(
		agentsqlite.NewRepositoryWithAudit(store.SQLDB(), accesssqlite.NewRepository(store.SQLDB())),
		agent.Config{APIKey: "test", Model: "test"},
		agent.WithModel(model),
	), principal.ID
}

func commandAuditTestIntent(input CommandAuditInput) *access.AuditIntent {
	action := map[string]string{
		createAgentConversationOperation.APIGenOperationID():  "agent.conversation.created",
		updateAgentConversationOperation.APIGenOperationID():  "agent.conversation.updated",
		archiveAgentConversationOperation.APIGenOperationID(): "agent.conversation.archived",
		createAgentRunOperation.APIGenOperationID():           "agent.run.created",
		cancelAgentRunOperation.APIGenOperationID():           "agent.run.cancelled",
	}[input.OperationID]
	return &access.AuditIntent{
		EventID: "agent-command-pending", Source: "agent", Operation: input.OperationID,
		PrincipalID: input.Scope.PrincipalID, Action: action, ResourceKind: input.TargetType,
		ResourceID: input.TargetID, Capability: access.CapabilityResourceUse, Outcome: "success",
		RequestID: input.RequestID, CorrelationID: input.CorrelationID,
		AggregateKey: "agent-command:pending", MetadataJSON: `{}`,
	}
}

func commandAuditRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func decodeResponseID(t *testing.T, body []byte) string {
	t.Helper()
	var response struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.ID == "" {
		t.Fatalf("decode response id: %v body=%s", err, body)
	}
	return response.ID
}
