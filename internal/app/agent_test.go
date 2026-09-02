package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/agent"
	agentconfig "github.com/flidai/leapview/internal/agent/config"
)

func TestAgentAPIReportsDisabledWhenProviderMissing(t *testing.T) {
	store := testStore(t)
	auth := testAuth(store, accessmodule.AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: agent.NewService(testAgentRepository(store), agent.Config{})}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/conversations", nil)
	req.Header.Set("Authorization", "Bearer dev")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 body=%s", rec.Code, rec.Body.String())
	}
}

func TestGlobalAgentAPIListsPrincipalConversations(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	principal := testPrincipal(t, ctx, store, "viewer@example.com", "Viewer")
	token := testAPIToken(t, ctx, store, principal.ID, "agent-global")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	agentService := agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", Model: "fake-model"})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: agentService}))

	createReq := authedJSONRequest(http.MethodPost, "/api/v1/agent/conversations", token, `{"title":"Global ask"}`)
	createRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", createRec.Code, createRec.Body.String())
	}

	listReq := authedJSONRequest(http.MethodGet, "/api/v1/agent/conversations", token, "")
	listRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), "Global ask") {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	if strings.Contains(listRec.Body.String(), "workspaceId") {
		t.Fatalf("global conversation response retains workspaceId: %s", listRec.Body.String())
	}

	scopedToken, _, err := testAccessRepository(store).CreateAPITokenWithMetadata(ctx, access.APITokenInput{
		PrincipalID:  principal.ID,
		Name:         "agent-workspace-bound",
		Capabilities: []access.Capability{access.CapabilityResourceUse, access.CapabilityResourceRead},
	})
	if err != nil {
		t.Fatalf("create workspace-bound token: %v", err)
	}
	scopedReq := authedJSONRequest(http.MethodGet, "/api/v1/agent/conversations", scopedToken, "")
	scopedRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(scopedRec, scopedReq)
	if scopedRec.Code != http.StatusOK || !strings.Contains(scopedRec.Body.String(), "Global ask") {
		t.Fatalf("workspace-bound token did not retain principal conversation ownership: status=%d body=%s", scopedRec.Code, scopedRec.Body.String())
	}

	other := testPrincipal(t, ctx, store, "other@example.com", "Other")
	otherToken := testAPIToken(t, ctx, store, other.ID, "agent-other")
	otherReq := authedJSONRequest(http.MethodGet, "/api/v1/agent/conversations", otherToken, "")
	otherRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(otherRec, otherReq)
	if otherRec.Code != http.StatusOK || strings.Contains(otherRec.Body.String(), "Global ask") {
		t.Fatalf("principal isolation failed: status=%d body=%s", otherRec.Code, otherRec.Body.String())
	}

	legacyReq := authedJSONRequest(http.MethodGet, "/api/v1/workspaces/test/agent/conversations", token, "")
	legacyRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(legacyRec, legacyReq)
	if legacyRec.Code != http.StatusNotFound {
		t.Fatalf("legacy workspace agent route status=%d, want 404 body=%s", legacyRec.Code, legacyRec.Body.String())
	}
}

func TestAgentAPIConversationTurnPersistsMessagesAndEvents(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	principal := testPrincipal(t, ctx, store, "viewer@example.com", "Viewer")
	token := testAPIToken(t, ctx, store, principal.ID, "agent-test")
	if err := store.UpsertSetting(ctx, agentconfig.SystemPromptSettingKey, "Stored admin system prompt."); err != nil {
		t.Fatalf("seed system prompt: %v", err)
	}
	var calls atomic.Int64
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		if len(req.Messages) == 0 || req.Messages[0].Role != "system" || req.Messages[0].Content != "Stored admin system prompt." {
			t.Fatalf("model request system prompt = %#v", req.Messages)
		}
		if calls.Add(1) == 1 {
			writeRawJSON(t, w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"catalog_list","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
			return
		}
		writeRawJSON(t, w, `{"choices":[{"message":{"role":"assistant","content":"Executive Sales is available."},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`)
	}))
	defer modelServer.Close()
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	agentService := agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", BaseURL: modelServer.URL, Model: "fake-model"})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: agentService}))
	backgroundCtx, cancelBackground := context.WithCancel(context.Background())
	server.StartBackgroundJobs(backgroundCtx)
	t.Cleanup(func() {
		cancelBackground()
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.StopBackgroundJobs(stopCtx)
	})

	createReq := authedJSONRequest(http.MethodPost, "/api/v1/agent/conversations", token, `{"title":"Ask"}`)
	createRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", createRec.Code, createRec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	conversationID := created["id"].(string)

	turnReq := authedJSONRequest(http.MethodPost, "/api/v1/agent/conversations/"+conversationID+"/runs", token, `{"input":"What dashboards can I use?","correlationId":"corr_1"}`)
	turnReq.Header.Set("Idempotency-Key", "agent-run-1")
	turnRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(turnRec, turnReq)
	if turnRec.Code != http.StatusAccepted {
		t.Fatalf("turn status = %d body=%s", turnRec.Code, turnRec.Body.String())
	}
	var turn map[string]any
	if err := json.Unmarshal(turnRec.Body.Bytes(), &turn); err != nil {
		t.Fatalf("decode turn: %v", err)
	}
	runID := turn["id"].(string)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runReq := authedJSONRequest(http.MethodGet, "/api/v1/agent/conversations/"+conversationID+"/runs/"+runID, token, "")
		runRec := httptest.NewRecorder()
		server.Routes().ServeHTTP(runRec, runReq)
		if strings.Contains(runRec.Body.String(), `"status":"completed"`) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	messagesReq := authedJSONRequest(http.MethodGet, "/api/v1/agent/conversations/"+conversationID+"/messages", token, "")
	messagesRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(messagesRec, messagesReq)
	if messagesRec.Code != http.StatusOK || !strings.Contains(messagesRec.Body.String(), "Executive Sales") {
		t.Fatalf("messages status=%d body=%s", messagesRec.Code, messagesRec.Body.String())
	}
	eventsReq := authedJSONRequest(http.MethodGet, "/api/v1/agent/conversations/"+conversationID+"/runs/"+runID+"/events", token, "")
	eventsRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(eventsRec, eventsReq)
	if eventsRec.Code != http.StatusOK || !strings.Contains(eventsRec.Body.String(), "model_response") {
		t.Fatalf("events status=%d body=%s", eventsRec.Code, eventsRec.Body.String())
	}
}

func TestAdminAgentConfigurationIsNotPublicAPI(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{}))
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		req := httptest.NewRequest(method, "/api/v1/admin/agent/config", nil)
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d want 404 body=%s", method, rec.Code, rec.Body.String())
		}
	}
}

func TestAgentConfigurationCommandUsesGeneratedPublicContract(t *testing.T) {
	ctx := t.Context()
	store := testStore(t)
	owner := testPlatformPrincipal(t, ctx, store, "owner@example.com", "Owner")
	token := testAPIToken(t, ctx, store, owner.ID, "agent-config")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))

	getReq := authedJSONRequest(http.MethodGet, "/api/v1/agent/config", token, "")
	getRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK || getRec.Header().Get("ETag") == "" {
		t.Fatalf("get status=%d etag=%q body=%s", getRec.Code, getRec.Header().Get("ETag"), getRec.Body.String())
	}
	req := authedJSONRequest(http.MethodPatch, "/api/v1/agent/config", token, `{"systemPrompt":"Use verified sources."}`)
	req.Header.Set("If-Match", getRec.Header().Get("ETag"))
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Use verified sources.") {
		t.Fatalf("update status=%d body=%s", rec.Code, rec.Body.String())
	}
	prompt, err := store.GetSetting(ctx, agentconfig.SystemPromptSettingKey)
	if err != nil || prompt != "Use verified sources." {
		t.Fatalf("stored prompt=%q err=%v", prompt, err)
	}
	staleReq := authedJSONRequest(http.MethodPatch, "/api/v1/agent/config", token, `{"systemPrompt":"Overwrite stale state."}`)
	staleReq.Header.Set("If-Match", getRec.Header().Get("ETag"))
	staleRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(staleRec, staleReq)
	if staleRec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale update status=%d body=%s", staleRec.Code, staleRec.Body.String())
	}
	prompt, err = store.GetSetting(ctx, agentconfig.SystemPromptSettingKey)
	if err != nil || prompt != "Use verified sources." {
		t.Fatalf("prompt after stale update=%q err=%v", prompt, err)
	}
	events, err := testAccessRepository(store).ListAuditEvents(ctx, access.AuditEventFilter{
		PrincipalID: owner.ID, Action: "agent.config.updated",
	})
	if err != nil || len(events) != 1 {
		t.Fatalf("agent config audits=%d err=%v", len(events), err)
	}
	if events[0].ResourceID != agentconfig.SystemPromptSettingKey {
		t.Fatalf("agent config audit resource=%q, want stable setting target %q", events[0].ResourceID, agentconfig.SystemPromptSettingKey)
	}
}

func TestAgentAPISupportsConversationAndRunReads(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	principal := testPrincipal(t, ctx, store, "viewer@example.com", "Viewer")
	token := testAPIToken(t, ctx, store, principal.ID, "agent-test")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	agentService := agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", Model: "fake-model"})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: agentService}))
	scope := agent.Scope{PrincipalID: principal.ID}
	conversation, err := agentService.CreateConversation(ctx, scope, "Original")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	run, err := testAgentRepository(store).CreateRun(ctx, agent.RunInput{
		PrincipalID:    principal.ID,
		ConversationID: conversation.ID,
		RunID:          "run_test",
		Model:          "fake-model",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := testAgentRepository(store).AppendEvent(ctx, agent.EventInput{
		PrincipalID: principal.ID,
		RunID:       run.ID,
		Sequence:    1,
		EventType:   "model_request",
		PayloadJSON: `{"ok":true}`,
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	updateReq := authedJSONRequest(http.MethodPatch, "/api/v1/agent/conversations/"+conversation.ID, token, `{"title":"Updated"}`)
	updateReq.Header.Set("If-Match", "*")
	updateRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(updateRec, updateReq)
	if updateRec.Code != http.StatusOK || !strings.Contains(updateRec.Body.String(), `"title":"Updated"`) {
		t.Fatalf("update status=%d body=%s", updateRec.Code, updateRec.Body.String())
	}

	runsReq := authedJSONRequest(http.MethodGet, "/api/v1/agent/conversations/"+conversation.ID+"/runs?limit=1", token, "")
	runsRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(runsRec, runsReq)
	if runsRec.Code != http.StatusOK || !strings.Contains(runsRec.Body.String(), `"id":"run_test"`) {
		t.Fatalf("runs status=%d body=%s", runsRec.Code, runsRec.Body.String())
	}

	runReq := authedJSONRequest(http.MethodGet, "/api/v1/agent/conversations/"+conversation.ID+"/runs/"+run.ID, token, "")
	runRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(runRec, runReq)
	if runRec.Code != http.StatusOK || !strings.Contains(runRec.Body.String(), `"conversationId":"`+conversation.ID+`"`) {
		t.Fatalf("run status=%d body=%s", runRec.Code, runRec.Body.String())
	}

	eventsReq := authedJSONRequest(http.MethodGet, "/api/v1/agent/conversations/"+conversation.ID+"/runs/"+run.ID+"/events?limit=1", token, "")
	eventsRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(eventsRec, eventsReq)
	if eventsRec.Code != http.StatusOK || !strings.Contains(eventsRec.Body.String(), `"event":"model_request"`) {
		t.Fatalf("nested events status=%d body=%s", eventsRec.Code, eventsRec.Body.String())
	}
	if _, err := testAgentRepository(store).FinishRun(ctx, agent.RunFinish{
		PrincipalID: principal.ID, ConversationID: conversation.ID, RunID: run.ID, Status: agent.RunStatusCompleted,
	}); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	sseReq := authedJSONRequest(http.MethodGet, "/api/v1/agent/conversations/"+conversation.ID+"/runs/"+run.ID+"/events", token, "")
	sseReq.Header.Set("Accept", "text/event-stream")
	sseRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(sseRec, sseReq)
	if sseRec.Code != http.StatusOK || !strings.HasPrefix(sseRec.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(sseRec.Body.String(), "event: model_request") {
		t.Fatalf("SSE events status=%d headers=%v body=%s", sseRec.Code, sseRec.Header(), sseRec.Body.String())
	}

	archiveReq := authedJSONRequest(http.MethodDelete, "/api/v1/agent/conversations/"+conversation.ID, token, "")
	archiveRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(archiveRec, archiveReq)
	if archiveRec.Code != http.StatusNoContent || archiveRec.Body.Len() != 0 {
		t.Fatalf("archive status=%d body=%s", archiveRec.Code, archiveRec.Body.String())
	}
	listReq := authedJSONRequest(http.MethodGet, "/api/v1/agent/conversations", token, "")
	listRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK || strings.Contains(listRec.Body.String(), conversation.ID) {
		t.Fatalf("archived conversation listed status=%d body=%s", listRec.Code, listRec.Body.String())
	}
}

func TestAgentAPIRejectsConcurrentTurnsForConversation(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	principal := testPrincipal(t, ctx, store, "viewer@example.com", "Viewer")
	token := testAPIToken(t, ctx, store, principal.ID, "agent-test")
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		writeRawJSON(t, w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer modelServer.Close()
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	agentService := agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", BaseURL: modelServer.URL, Model: "fake-model"})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: agentService}))
	conversation, err := agentService.CreateConversation(ctx, agent.Scope{PrincipalID: principal.ID}, "Ask")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			req := authedJSONRequest(http.MethodPost, "/api/v1/agent/conversations/"+conversation.ID+"/runs", token, `{"input":"hello"}`)
			req.Header.Set("Idempotency-Key", fmt.Sprintf("concurrent-%d", index))
			rec := httptest.NewRecorder()
			server.Routes().ServeHTTP(rec, req)
			statuses <- rec.Code
		}(i)
	}
	wg.Wait()
	close(statuses)
	sawConflict := false
	for status := range statuses {
		if status == http.StatusConflict {
			sawConflict = true
		}
	}
	if !sawConflict {
		t.Fatal("concurrent turns did not return a 409 conflict")
	}
}

func TestRefreshRunAPIRejectsExternallySuppliedTarget(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	principal := testPrincipal(t, ctx, store, "editor@example.com", "Editor")
	token := testAPIToken(t, ctx, store, principal.ID, "refresh-contract-test")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))
	req := authedJSONRequest(http.MethodPost, "/api/v1/projects/project:test/refresh-runs", token, `{"modelId":"model.orders","targetType":"model"}`)
	req.Header.Set("Idempotency-Key", "legacy-refresh-target")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("legacy refresh create status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func authedJSONRequest(method, path, token, body string) *http.Request {
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Accept", "application/json")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if method == http.MethodPost {
		req.Header.Set("Idempotency-Key", "test-"+strings.ReplaceAll(path, "/", "-"))
	}
	if method == http.MethodPatch {
		req.Header.Set("If-Match", "*")
	}
	return req
}

func writeRawJSON(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatalf("write response: %v", err)
	}
}
