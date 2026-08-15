package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/catalog"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
	"github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/project/testing/dashboardfixture"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

func toolNames(tools []agentcore.ToolDefinition) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestServiceUsesHostProvidedTools(t *testing.T) {
	service := NewService(nil, Config{APIKey: "key", Model: "model"})
	service.SetToolProviders(func(scope Scope) []agentcore.ToolDefinition {
		if scope.WorkspaceID != "test" || scope.PrincipalID != "principal" {
			t.Fatalf("scope = %#v", scope)
		}
		return []agentcore.ToolDefinition{{
			Name:        "list_workspace_assets",
			Description: "List workspace assets via APIGen.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
			Handler: agentcore.ToolHandlerFunc(func(context.Context, agentcore.ToolCall) (agentcore.ToolResult, error) {
				return agentcore.ToolResult{Content: map[string]any{"ok": true}}, nil
			}),
		}}
	})

	tools := service.toolDefinitions(Scope{WorkspaceID: "test", PrincipalID: "principal"})
	if runTool(t, tools, "list_workspace_assets", `{}`) != `{"ok":true}` {
		t.Fatalf("host-provided tool did not run")
	}
}

func TestServiceAppendsHostProvidedTools(t *testing.T) {
	service := NewService(nil, Config{APIKey: "key", Model: "model"})
	service.SetToolProviders(fakeToolProvider("one"))
	service.AppendToolProviders(fakeToolProvider("two"))
	tools := service.toolDefinitions(Scope{WorkspaceID: "test", PrincipalID: "principal"})
	if runTool(t, tools, "one", `{}`) != `{"name":"one"}` {
		t.Fatalf("first host-provided tool did not run")
	}
	if runTool(t, tools, "two", `{}`) != `{"name":"two"}` {
		t.Fatalf("appended host-provided tool did not run")
	}
}

func TestServiceRejectsGloballyDisabledAgent(t *testing.T) {
	service := NewService(nil, Config{})

	_, err := service.Prompt(context.Background(), PromptInput{
		Scope:          Scope{WorkspaceID: "test", PrincipalID: "principal"},
		ConversationID: "conv_test",
		Input:          "hello",
	})
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("Prompt() error = %v, want ErrDisabled", err)
	}
}

func TestServiceUsesConfiguredSystemPrompt(t *testing.T) {
	service := NewService(nil, Config{APIKey: "key", Model: "model"})
	service.SetSystemPromptProvider(func(context.Context) (string, error) {
		return "Stored platform system prompt.", nil
	})
	prompt, err := service.systemPrompt(context.Background())
	if err != nil {
		t.Fatalf("system prompt: %v", err)
	}
	if prompt != "Stored platform system prompt." {
		t.Fatalf("system prompt = %q, want stored prompt only", prompt)
	}
}

func TestServicePromptPersistsRunEventsMessagesAndTranscript(t *testing.T) {
	ctx := context.Background()
	store := openAgentAppStore(t, ctx)
	defer store.Close()
	principal := createAgentAppPrincipal(t, ctx, store, "viewer@example.com")

	model := newRecordingAgentModel(
		agentcore.ModelResponse{
			ToolCalls:    []agentcore.ToolCall{{ID: "call_dashboards", Name: "list_dashboards", Arguments: json.RawMessage(`{}`)}},
			FinishReason: agentcore.FinishReasonToolCalls,
			Usage:        agentcore.Usage{InputTokens: 20, OutputTokens: 5, TotalTokens: 25},
		},
		agentcore.ModelResponse{
			Content:      "You have Executive Sales available.",
			FinishReason: agentcore.FinishReasonStop,
			Usage:        agentcore.Usage{InputTokens: 30, OutputTokens: 9, TotalTokens: 39},
		},
	)
	service := NewService(store, Config{APIKey: "key", BaseURL: "http://model.test", Model: "fake-model"}, WithModel(model))
	service.SetToolProviders(fakeToolProvider("list_dashboards"))
	conversation, err := service.CreateConversation(ctx, Scope{WorkspaceID: "test", PrincipalID: principal.ID}, "Dashboards")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	result, err := service.Prompt(ctx, PromptInput{
		Scope:          Scope{WorkspaceID: "test", PrincipalID: principal.ID},
		ConversationID: conversation.ID,
		Input:          "What dashboards can I use?",
		CorrelationID:  "corr_1",
	})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if !strings.Contains(result.Content, "Executive Sales") || result.StopReason != agentcore.StopReasonCompleted {
		t.Fatalf("result = %#v", result)
	}
	messages, err := store.ListMessages(ctx, principal.ID, conversation.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("messages len = %d, want user/assistant/tool/assistant: %#v", len(messages), messages)
	}
	runs, err := store.ListRuns(ctx, principal.ID, conversation.ID)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != result.RunID {
		t.Fatalf("runs = %#v result=%#v", runs, result)
	}
	events, err := store.ListEvents(ctx, principal.ID, result.RunID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) == 0 || events[0].Seq != 1 {
		t.Fatalf("events = %#v", events)
	}
	updated, err := store.GetConversation(ctx, principal.ID, conversation.ID)
	if err != nil {
		t.Fatalf("get updated conversation: %v", err)
	}
	if !strings.Contains(updated.TranscriptJSON, "Executive Sales") {
		t.Fatalf("transcript was not updated: %s", updated.TranscriptJSON)
	}
	requests := model.Requests()
	if len(requests) != 2 || len(requests[1].Messages) == 0 || requests[1].Messages[len(requests[1].Messages)-1].Role != agentcore.RoleTool {
		t.Fatalf("second model request did not include tool result: %#v", requests)
	}
}

func TestServicePromptPersistsResolvedTurnContextWhileKeepingVisibleTextClean(t *testing.T) {
	ctx := context.Background()
	store := openAgentAppStore(t, ctx)
	defer store.Close()
	principal := createAgentAppPrincipal(t, ctx, store, "context@example.com")
	model := newRecordingAgentModel(agentcore.ModelResponse{Content: "The decline is concentrated in Denmark.", FinishReason: agentcore.FinishReasonStop})
	service := NewService(store, Config{APIKey: "key", Model: "fake-model"}, WithModel(model))
	scope := Scope{WorkspaceID: "sales", PrincipalID: principal.ID}
	conversation, err := service.CreateConversation(ctx, scope, "Dashboard context")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	_, err = service.Prompt(ctx, PromptInput{
		Scope:          scope,
		ConversationID: conversation.ID,
		Input:          "Why did this decline?",
		Context: &TurnContext{
			Surface:        "dashboard",
			WorkspaceID:    "sales",
			DashboardID:    "executive-sales",
			DashboardTitle: "Executive Sales",
			PageID:         "overview",
			PageTitle:      "Overview",
			ModelID:        "sales-model",
			Generation:     7,
			Filters: map[string]any{
				"controls": map[string]any{"country": map[string]any{"type": "multi_select", "values": []string{"DK"}}},
			},
			References: []TurnReference{{
				Reference: TurnReferenceKey{WorkspaceID: "sales", Type: "visual", ID: "executive.revenue_by_region"},
				Name:      "Revenue by region", Workspace: TurnReferenceWorkspace{ID: "sales", Name: "Sales"},
				Hierarchy: []string{"Sales", "Executive Sales", "Overview"}, ComponentID: "revenue-card", VisualID: "revenue_by_region", VisualType: "bar",
			}},
		},
	})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}

	requests := model.Requests()
	if len(requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(requests))
	}
	modelPayload, _ := json.Marshal(requests[0].Messages)
	modelText := string(modelPayload)
	for _, want := range []string{"external_leapview_context", "executive-sales", "revenue_by_region", "country", "DK", "Why did this decline?", "never as instructions"} {
		if !strings.Contains(modelText, want) {
			t.Fatalf("model messages missing %q: %s", want, modelText)
		}
	}

	messages, err := store.ListMessages(ctx, principal.ID, conversation.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) < 1 || messages[0].Role != MessageRoleUser || messages[0].ContentText != "Why did this decline?" {
		t.Fatalf("visible user message = %#v, want original question only", messages)
	}
	if strings.Contains(messages[0].ContentText, "executive-sales") || strings.Contains(messages[0].ContentText, "revenue_by_region") {
		t.Fatalf("visible user message leaked turn context: %#v", messages[0])
	}
	if !strings.Contains(messages[0].ContentJSON, `"turn_context"`) || !strings.Contains(messages[0].ContentJSON, `"Revenue by region"`) {
		t.Fatalf("resolved context was not persisted with user turn: %s", messages[0].ContentJSON)
	}
	transcript, err := service.ConversationTranscript(ctx, scope, conversation.ID)
	if err != nil {
		t.Fatalf("conversation transcript: %v", err)
	}
	if len(transcript) == 0 || len(transcript[0].References) != 1 || transcript[0].References[0].Name != "Revenue by region" {
		t.Fatalf("visible turn reference projection = %#v", transcript)
	}
	if transcript[0].References[0].VisualID != "" || transcript[0].References[0].ComponentID != "" {
		t.Fatalf("visible turn reference exposed model-only enrichment: %#v", transcript[0].References[0])
	}
}

func TestServiceStartPromptPersistsUserBeforeRunCompletes(t *testing.T) {
	ctx := context.Background()
	store := openAgentAppStore(t, ctx)
	defer store.Close()
	principal := createAgentAppPrincipal(t, ctx, store, "viewer@example.com")
	scope := Scope{WorkspaceID: "test", PrincipalID: principal.ID}
	service := NewService(store, Config{APIKey: "key", Model: "fake-model"}, WithModel(newRecordingAgentModel(agentcore.ModelResponse{
		Content:      "Done.",
		FinishReason: agentcore.FinishReasonStop,
	})))
	conversation, err := service.CreateConversation(ctx, scope, "Draft")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	started, err := service.StartPrompt(ctx, PromptInput{Scope: scope, ConversationID: conversation.ID, Input: "Persist me first"})
	if err != nil {
		t.Fatalf("start prompt: %v", err)
	}
	if !service.ConversationRunning(conversation.ID) {
		t.Fatal("conversation should be marked running after start")
	}
	messages, err := store.ListMessages(ctx, principal.ID, conversation.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != MessageRoleUser || messages[0].ContentText != "Persist me first" {
		t.Fatalf("messages after start = %#v, want one persisted user prompt", messages)
	}

	result, err := service.CompletePrompt(ctx, started, nil)
	if err != nil {
		t.Fatalf("complete prompt: %v", err)
	}
	if result.RunID != started.RunID {
		t.Fatalf("result run = %q, want started run %q", result.RunID, started.RunID)
	}
	if service.ConversationRunning(conversation.ID) {
		t.Fatal("conversation should not be running after completion")
	}
	messages, err = store.ListMessages(ctx, principal.ID, conversation.ID)
	if err != nil {
		t.Fatalf("list completed messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Role != MessageRoleUser || messages[1].Role != MessageRoleAssistant {
		t.Fatalf("messages after completion = %#v, want user and assistant only", messages)
	}
}

func TestServiceRejectsArchivedConversationBeforeCreatingRun(t *testing.T) {
	ctx := context.Background()
	store := newTestAgentStore()
	principal := createAgentAppPrincipal(t, ctx, store, "archived@example.com")
	scope := Scope{WorkspaceID: "test", PrincipalID: principal.ID}
	service := NewService(store, Config{APIKey: "key", Model: "fake-model"}, WithModel(newRecordingAgentModel()))
	conversation, err := service.CreateConversation(ctx, scope, "Archived")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ArchiveConversation(ctx, scope, conversation.ID); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := service.StartPrompt(ctx, PromptInput{Scope: scope, ConversationID: conversation.ID, Input: "must reject"}); !errors.Is(err, ErrConversationArchived) {
			t.Fatalf("attempt %d error = %v, want ErrConversationArchived", attempt, err)
		}
	}
	runs, err := store.ListRuns(ctx, principal.ID, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := store.ListMessages(ctx, principal.ID, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 || len(messages) != 0 {
		t.Fatalf("archived rejection created runs/messages: %d/%d", len(runs), len(messages))
	}
}

func TestServiceDispatchesOnlyExplicitDurablePrompts(t *testing.T) {
	ctx := context.Background()
	store := &workflowAgentStore{testAgentStore: newTestAgentStore()}
	principal := createAgentAppPrincipal(t, ctx, store.testAgentStore, "dispatch@example.com")
	scope := Scope{WorkspaceID: "test", PrincipalID: principal.ID}
	service := NewService(store, Config{APIKey: "key", Model: "fake-model"}, WithModel(newRecordingAgentModel()))
	service.SetPromptWorkflow(func(_ PromptInput, runID string, dispatch PromptDispatch) jobs.WorkflowIntent {
		payload, _ := json.Marshal(dispatch)
		return jobs.WorkflowIntent{Job: jobs.EnqueueInput{ID: runID, Payload: payload}}
	})

	inlineConversation, err := service.CreateConversation(ctx, scope, "Inline")
	if err != nil {
		t.Fatal(err)
	}
	inline, err := service.StartPrompt(ctx, PromptInput{
		Scope: scope, ConversationID: inlineConversation.ID, Input: "Run inline",
	})
	if err != nil {
		t.Fatal(err)
	}
	if inline.DurablyQueued() || len(store.workflows) != 0 {
		t.Fatalf("inline prompt unexpectedly dispatched: %#v", store.workflows)
	}
	_ = inline.Abort(ctx, errors.New("test cleanup"))

	durableConversation, err := service.CreateConversation(ctx, scope, "Durable")
	if err != nil {
		t.Fatal(err)
	}
	durable, err := service.StartDurablePrompt(ctx, PromptInput{
		Scope: scope, ConversationID: durableConversation.ID, Input: "Run later",
	}, PromptDispatch{ChatClientID: "browser-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !durable.DurablyQueued() || len(store.workflows) != 1 {
		t.Fatalf("durable prompt workflows = %#v", store.workflows)
	}
	var dispatch PromptDispatch
	if err := json.Unmarshal(store.workflows[0].Job.Payload, &dispatch); err != nil {
		t.Fatal(err)
	}
	if dispatch.ChatClientID != "browser-1" {
		t.Fatalf("persisted dispatch = %#v", dispatch)
	}
	_ = durable.Abort(ctx, errors.New("test cleanup"))
}

func TestServiceResumesPersistedPromptAfterProcessRestart(t *testing.T) {
	ctx := context.Background()
	store := openAgentAppStore(t, ctx)
	defer store.Close()
	principal := createAgentAppPrincipal(t, ctx, store, "restart@example.com")
	scope := Scope{WorkspaceID: "test", PrincipalID: principal.ID}
	first := NewService(store, Config{APIKey: "key", Model: "fake-model"}, WithModel(newRecordingAgentModel()))
	conversation, err := first.CreateConversation(ctx, scope, "Restart")
	if err != nil {
		t.Fatal(err)
	}
	started, err := first.StartPrompt(ctx, PromptInput{Scope: scope, ConversationID: conversation.ID, Input: "Resume this", CorrelationID: "correlation-1"})
	if err != nil {
		t.Fatal(err)
	}

	restarted := NewService(store, Config{APIKey: "key", Model: "fake-model"}, WithModel(newRecordingAgentModel(agentcore.ModelResponse{Content: "Resumed.", FinishReason: agentcore.FinishReasonStop})))
	resumed, err := restarted.ResumePrompt(ctx, scope, conversation.ID, started.RunID, "correlation-1")
	if err != nil {
		t.Fatalf("ResumePrompt() error = %v", err)
	}
	result, err := resumed.Complete(ctx, nil)
	if err != nil || result.RunID != started.RunID || result.Content != "Resumed." {
		t.Fatalf("resumed result = %#v, err=%v", result, err)
	}
	run, err := restarted.GetRun(ctx, scope, conversation.ID, started.RunID)
	if err != nil || run.Status != RunStatusCompleted {
		t.Fatalf("run = %#v, err=%v", run, err)
	}
}

func TestServiceResumedPromptPreservesExecutionContext(t *testing.T) {
	ctx := context.Background()
	store := openAgentAppStore(t, ctx)
	defer store.Close()
	principal := createAgentAppPrincipal(t, ctx, store, "resume-context@example.com")
	scope := Scope{WorkspaceID: "test", PrincipalID: principal.ID}
	first := NewService(store, Config{APIKey: "key", Model: "fake-model"}, WithModel(newRecordingAgentModel()))
	conversation, err := first.CreateConversation(ctx, scope, "Resume context")
	if err != nil {
		t.Fatal(err)
	}
	started, err := first.StartPrompt(ctx, PromptInput{Scope: scope, ConversationID: conversation.ID, Input: "Use the tool"})
	if err != nil {
		t.Fatal(err)
	}

	type executionContextKey struct{}
	const contextValue = "background-workload-lease"
	observed := make(chan any, 1)
	restarted := NewService(store, Config{APIKey: "key", Model: "fake-model"}, WithModel(newRecordingAgentModel(
		agentcore.ModelResponse{
			ToolCalls:    []agentcore.ToolCall{{ID: "call_context", Name: "observe_context", Arguments: json.RawMessage(`{}`)}},
			FinishReason: agentcore.FinishReasonToolCalls,
		},
		agentcore.ModelResponse{Content: "Done.", FinishReason: agentcore.FinishReasonStop},
	)))
	restarted.SetToolProviders(func(Scope) []agentcore.ToolDefinition {
		return []agentcore.ToolDefinition{{
			Name:        "observe_context",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
			Handler: agentcore.ToolHandlerFunc(func(toolCtx context.Context, _ agentcore.ToolCall) (agentcore.ToolResult, error) {
				observed <- toolCtx.Value(executionContextKey{})
				return agentcore.ToolResult{Content: map[string]any{"ok": true}}, nil
			}),
		}}
	})
	resumeCtx := context.WithValue(ctx, executionContextKey{}, contextValue)
	resumed, err := restarted.ResumePrompt(resumeCtx, scope, conversation.ID, started.RunID, "")
	if err != nil {
		t.Fatalf("ResumePrompt() error = %v", err)
	}
	if _, err := resumed.Complete(resumeCtx, nil); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if got := <-observed; got != contextValue {
		t.Fatalf("tool execution context value = %#v, want %q", got, contextValue)
	}
}

func TestServiceCompletePromptFailureLeavesSubmittedUserMessage(t *testing.T) {
	ctx := context.Background()
	store := openAgentAppStore(t, ctx)
	defer store.Close()
	principal := createAgentAppPrincipal(t, ctx, store, "viewer@example.com")
	scope := Scope{WorkspaceID: "test", PrincipalID: principal.ID}
	service := NewService(store, Config{APIKey: "key", Model: "fake-model"}, WithModel(newFailingAgentModel(errors.New("model down"))))
	conversation, err := service.CreateConversation(ctx, scope, "Draft")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	started, err := service.StartPrompt(ctx, PromptInput{Scope: scope, ConversationID: conversation.ID, Input: "Keep failed input"})
	if err != nil {
		t.Fatalf("start prompt: %v", err)
	}
	if _, err := service.CompletePrompt(ctx, started, nil); err == nil {
		t.Fatal("complete prompt succeeded against failing model")
	}
	messages, err := store.ListMessages(ctx, principal.ID, conversation.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != MessageRoleUser || messages[0].ContentText != "Keep failed input" {
		t.Fatalf("messages after failure = %#v, want submitted user prompt", messages)
	}
	runs, err := store.ListRuns(ctx, principal.ID, conversation.ID)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != RunStatusFailed || runs[0].Error == "" {
		t.Fatalf("runs after failure = %#v, want failed run with error", runs)
	}
}

func TestServicePersistsDistinctProviderAndDeadlineCauses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		cause RunTerminationCause
	}{
		{name: "provider cancellation", err: context.Canceled, cause: RunCauseProviderCanceled},
		{name: "deadline", err: context.DeadlineExceeded, cause: RunCauseDeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := openAgentAppStore(t, ctx)
			defer store.Close()
			principal := createAgentAppPrincipal(t, ctx, store, tc.name+"@example.com")
			scope := Scope{WorkspaceID: "test", PrincipalID: principal.ID}
			service := NewService(store, Config{APIKey: "key", Model: "fake-model"}, WithModel(newFailingAgentModel(tc.err)))
			conversation, err := service.CreateConversation(ctx, scope, tc.name)
			if err != nil {
				t.Fatal(err)
			}
			started, err := service.StartPrompt(ctx, PromptInput{Scope: scope, ConversationID: conversation.ID, Input: "cause"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := started.Complete(ctx, nil); !errors.Is(err, tc.err) {
				t.Fatalf("Complete error = %v, want %v", err, tc.err)
			}
			runs, err := store.ListRuns(ctx, principal.ID, conversation.ID)
			if err != nil || len(runs) != 1 {
				t.Fatalf("runs = %#v err=%v", runs, err)
			}
			var metadata map[string]any
			if err := json.Unmarshal([]byte(runs[0].MetadataJSON), &metadata); err != nil {
				t.Fatal(err)
			}
			if metadata["terminationCause"] != string(tc.cause) {
				t.Fatalf("termination cause = %#v, want %q", metadata["terminationCause"], tc.cause)
			}
		})
	}
}

func TestServiceDurableCompletionUsesAtomicBoundaryWithoutFallback(t *testing.T) {
	ctx := context.Background()
	newService := func(t *testing.T, model agentcore.Model, atomicErr error) (*Service, *completionWorkflowAgentStore, Scope, Conversation) {
		t.Helper()
		store := &completionWorkflowAgentStore{
			workflowAgentStore: &workflowAgentStore{testAgentStore: newTestAgentStore()},
			atomicErr:          atomicErr,
		}
		principal := createAgentAppPrincipal(t, ctx, store.testAgentStore, "atomic@example.com")
		scope := Scope{WorkspaceID: "test", PrincipalID: principal.ID}
		service := NewService(store, Config{APIKey: "key", Model: "fake-model"}, WithModel(model))
		service.SetPromptWorkflow(func(_ PromptInput, runID string, _ PromptDispatch) jobs.WorkflowIntent {
			return jobs.WorkflowIntent{Job: jobs.EnqueueInput{ID: "agent:" + runID, ResourceID: runID}}
		})
		conversation, err := service.CreateConversation(ctx, scope, "Atomic")
		if err != nil {
			t.Fatal(err)
		}
		return service, store, scope, conversation
	}

	t.Run("success publishes callbacks only after atomic commit", func(t *testing.T) {
		service, store, scope, conversation := newService(t, newRecordingAgentModel(agentcore.ModelResponse{Content: "done", FinishReason: agentcore.FinishReasonStop}), nil)
		started, err := service.StartDurablePrompt(ctx, PromptInput{Scope: scope, ConversationID: conversation.ID, Input: "go"}, PromptDispatch{})
		if err != nil {
			t.Fatal(err)
		}
		started.SetDurableClaim("job-1", jobs.Fence{Owner: "worker", Generation: 1})
		callbacks := 0
		if _, err := started.Complete(ctx, func(event EventEnvelope) {
			if event.Type == "message_appended" {
				callbacks++
			}
		}); err != nil {
			t.Fatal(err)
		}
		if store.completionCalls != 1 || store.fallbackFinishCalls != 0 {
			t.Fatalf("completion calls=%d fallback finish calls=%d", store.completionCalls, store.fallbackFinishCalls)
		}
		if callbacks != store.committedMessages {
			t.Fatalf("callbacks=%d committed messages=%d", callbacks, store.committedMessages)
		}
	})

	t.Run("provider and atomic failures are joined without fallback or callback", func(t *testing.T) {
		providerErr := errors.New("provider failed")
		atomicErr := errors.New("atomic commit failed")
		service, store, scope, conversation := newService(t, newFailingAgentModel(providerErr), atomicErr)
		started, err := service.StartDurablePrompt(ctx, PromptInput{Scope: scope, ConversationID: conversation.ID, Input: "go"}, PromptDispatch{})
		if err != nil {
			t.Fatal(err)
		}
		started.SetDurableClaim("job-1", jobs.Fence{Owner: "worker", Generation: 1})
		callbacks := 0
		_, err = started.Complete(ctx, func(event EventEnvelope) {
			if event.Type == "message_appended" {
				callbacks++
			}
		})
		if !errors.Is(err, providerErr) || !errors.Is(err, atomicErr) {
			t.Fatalf("completion error = %v, want joined provider and atomic failures", err)
		}
		if store.completionCalls != 1 || store.fallbackFinishCalls != 0 || store.committedMessages != 0 || callbacks != 0 {
			t.Fatalf("calls=%d fallback=%d committed=%d callbacks=%d", store.completionCalls, store.fallbackFinishCalls, store.committedMessages, callbacks)
		}
		run, getErr := store.GetRun(ctx, scope.PrincipalID, conversation.ID, started.RunID)
		if getErr != nil || run.Status != RunStatusRunning {
			t.Fatalf("run after atomic rollback = %#v, %v", run, getErr)
		}
	})
}

func TestServiceStartedPromptAbortReleasesRunningAndFailsRun(t *testing.T) {
	ctx := context.Background()
	store := openAgentAppStore(t, ctx)
	defer store.Close()
	principal := createAgentAppPrincipal(t, ctx, store, "viewer@example.com")

	scope := Scope{WorkspaceID: "test", PrincipalID: principal.ID}
	service := NewService(store, Config{APIKey: "key", BaseURL: "http://127.0.0.1", Model: "fake-model"}, WithModel(newRecordingAgentModel()))
	conversation, err := service.CreateConversation(ctx, scope, "Draft")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	started, err := service.StartPrompt(ctx, PromptInput{Scope: scope, ConversationID: conversation.ID, Input: "Abort me"})
	if err != nil {
		t.Fatalf("start prompt: %v", err)
	}
	if !service.ConversationRunning(conversation.ID) {
		t.Fatal("conversation should be running after start")
	}
	if err := started.Abort(ctx, errors.New("background startup failed")); err != nil {
		t.Fatalf("abort prompt: %v", err)
	}
	if err := started.Abort(ctx, errors.New("second abort")); err != nil {
		t.Fatalf("second abort should be harmless: %v", err)
	}
	if service.ConversationRunning(conversation.ID) {
		t.Fatal("conversation should not be running after abort")
	}
	runs, err := store.ListRuns(ctx, principal.ID, conversation.ID)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != RunStatusFailed || !strings.Contains(runs[0].Error, "background startup failed") {
		t.Fatalf("runs after abort = %#v, want failed run with first abort error", runs)
	}
	messages, err := store.ListMessages(ctx, principal.ID, conversation.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != MessageRoleUser || messages[0].ContentText != "Abort me" {
		t.Fatalf("messages after abort = %#v, want submitted user prompt", messages)
	}
}

func TestServiceCancelsAnActiveRun(t *testing.T) {
	ctx := context.Background()
	store := openAgentAppStore(t, ctx)
	defer store.Close()
	principal := createAgentAppPrincipal(t, ctx, store, "viewer@example.com")
	scope := Scope{WorkspaceID: "test", PrincipalID: principal.ID}
	model := newRecordingAgentModel()
	model.delay = time.Minute
	service := NewService(store, Config{APIKey: "key", Model: "fake-model"}, WithModel(model))
	conversation, err := service.CreateConversation(ctx, scope, "Draft")
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.StartPrompt(ctx, PromptInput{Scope: scope, ConversationID: conversation.ID, Input: "Cancel me"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, completeErr := service.CompletePrompt(ctx, started, nil)
		done <- completeErr
	}()
	if err := service.CancelRun(ctx, scope, conversation.ID, started.RunID); err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("completion error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("agent completion did not stop after cancellation")
	}
	run, err := service.GetRun(ctx, scope, conversation.ID, started.RunID)
	if err != nil || run.Status != RunStatusCanceled {
		t.Fatalf("run = %#v, error = %v", run, err)
	}
}

func TestServicePromptPersistsDisplayContentButSendsCompactToolResult(t *testing.T) {
	ctx := context.Background()
	store := openAgentAppStore(t, ctx)
	defer store.Close()
	principal := createAgentAppPrincipal(t, ctx, store, "viewer@example.com")
	scope := Scope{WorkspaceID: "test", PrincipalID: principal.ID}
	model := newRecordingAgentModel(
		agentcore.ModelResponse{
			ToolCalls:    []agentcore.ToolCall{{ID: "call_visual", Name: "query_visual", Arguments: json.RawMessage(`{}`)}},
			FinishReason: agentcore.FinishReasonToolCalls,
		},
		agentcore.ModelResponse{Content: "Created the artifact.", FinishReason: agentcore.FinishReasonStop},
	)
	service := NewService(store, Config{APIKey: "key", Model: "fake-model"}, WithModel(model))
	service.SetToolProviders(func(Scope) []agentcore.ToolDefinition {
		return []agentcore.ToolDefinition{{
			Name:        "query_visual",
			Description: "visual",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Handler: agentcore.ToolHandlerFunc(func(context.Context, agentcore.ToolCall) (agentcore.ToolResult, error) {
				return agentcore.ToolResult{
					Content: map[string]any{"ok": true, "type": "table", "id": "agent_visual_1", "summary": "Created table.", "signal": "visuals.agent_visual_1"},
					DisplayContent: map[string]any{
						"kind":    "table",
						"id":      "agent_visual_1",
						"type":    "table",
						"patch":   map[string]any{"visuals": map[string]any{"agent_visual_1": map[string]any{"type": "table", "blocks": map[string]any{"a": map[string]any{"rows": []any{map[string]any{"status": "delivered"}}}}}}},
						"summary": "Created table.",
					},
				}, nil
			}),
		}}
	})
	conversation, err := service.CreateConversation(ctx, scope, "Display")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if _, err := service.Prompt(ctx, PromptInput{Scope: scope, ConversationID: conversation.ID, Input: "make a table"}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	requests := model.Requests()
	if len(requests) < 2 || len(requests[1].Messages) == 0 {
		t.Fatal("missing second request messages")
	}
	toolMessage := requests[1].Messages[len(requests[1].Messages)-1]
	if toolMessage.Role != agentcore.RoleTool || strings.Contains(toolMessage.Content, "delivered") || !strings.Contains(toolMessage.Content, "visuals.agent_visual_1") {
		t.Fatalf("model-visible tool message = %#v", toolMessage)
	}
	messages, err := store.ListMessages(ctx, principal.ID, conversation.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	var storedTool Message
	for _, message := range messages {
		if message.Role == MessageRoleTool {
			storedTool = message
			break
		}
	}
	if storedTool.ID == "" {
		t.Fatal("stored tool message missing")
	}
	if strings.Contains(storedTool.ContentText, "delivered") {
		t.Fatalf("stored compact content leaked display row: %s", storedTool.ContentText)
	}
	if !strings.Contains(storedTool.ContentJSON, "display_content") || !strings.Contains(storedTool.ContentJSON, "delivered") {
		t.Fatalf("stored content_json missing display artifact: %s", storedTool.ContentJSON)
	}
	updated, err := store.GetConversation(ctx, principal.ID, conversation.ID)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if strings.Contains(updated.TranscriptJSON, "delivered") || strings.Contains(updated.TranscriptJSON, "display_content") {
		t.Fatalf("conversation transcript snapshot should stay compact: %s", updated.TranscriptJSON)
	}
}

func TestServiceGenerateConversationTitleUsesNoToolsAndSavesCleanTitle(t *testing.T) {
	ctx := context.Background()
	store := openAgentAppStore(t, ctx)
	defer store.Close()
	principal := createAgentAppPrincipal(t, ctx, store, "viewer@example.com")
	scope := Scope{WorkspaceID: "test", PrincipalID: principal.ID}
	model := newRecordingAgentModel(agentcore.ModelResponse{
		Content:      "<think>private</think>\n\"Available dashboards.\"",
		FinishReason: agentcore.FinishReasonStop,
		Usage:        agentcore.Usage{InputTokens: 12, OutputTokens: 3, TotalTokens: 15},
	})
	service := NewService(store, Config{APIKey: "key", Model: "fake-model"}, WithModel(model))
	conversation, err := service.CreateConversation(ctx, scope, "")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if _, err := store.AppendMessage(ctx, MessageInput{
		PrincipalID:    scope.PrincipalID,
		ConversationID: conversation.ID,
		Role:           MessageRoleUser,
		ContentText:    "What dashboards can I use?",
	}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	updated, err := service.GenerateConversationTitle(ctx, scope, conversation.ID)
	if err != nil {
		t.Fatalf("generate title: %v", err)
	}
	if updated.Title != "Available dashboards" {
		t.Fatalf("title = %q", updated.Title)
	}
	requests := model.Requests()
	if len(requests) != 1 {
		t.Fatalf("title requests = %#v", requests)
	}
	got := requests[0]
	if got.Purpose != modelRequestPurposeTitle || got.Limits.ReserveOutputTokens != titleReserveOutputTokens {
		t.Fatalf("title purpose/limits = %#v", got)
	}
	if len(got.Tools) != 0 {
		t.Fatalf("title request should not include tools: %#v", got.Tools)
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != agentcore.RoleSystem || got.Messages[1].Role != agentcore.RoleUser {
		t.Fatalf("title messages = %#v", got.Messages)
	}
	if !strings.Contains(got.Messages[1].Content, "What dashboards can I use?") {
		t.Fatalf("title prompt did not include first user prompt: %#v", got.Messages)
	}
}

func TestServiceGenerateConversationTitleFallsBackWhenModelReturnsEmptyContent(t *testing.T) {
	ctx := context.Background()
	store := openAgentAppStore(t, ctx)
	defer store.Close()
	principal := createAgentAppPrincipal(t, ctx, store, "viewer@example.com")
	scope := Scope{WorkspaceID: "test", PrincipalID: principal.ID}
	service := NewService(store, Config{APIKey: "key", Model: "fake-model"}, WithModel(newRecordingAgentModel(agentcore.ModelResponse{
		Content:      "",
		FinishReason: agentcore.FinishReasonTruncated,
		Usage:        agentcore.Usage{InputTokens: 12, OutputTokens: 64, TotalTokens: 76},
	})))
	conversation, err := service.CreateConversation(ctx, scope, "")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if _, err := store.AppendMessage(ctx, MessageInput{
		PrincipalID:    scope.PrincipalID,
		ConversationID: conversation.ID,
		Role:           MessageRoleUser,
		ContentText:    "how are you?",
	}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	updated, err := service.GenerateConversationTitle(ctx, scope, conversation.ID)
	if err != nil {
		t.Fatalf("generate title: %v", err)
	}
	if updated.Title != "How are you" {
		t.Fatalf("title = %q", updated.Title)
	}
}

func TestServiceGenerateConversationTitleIsBestEffortAndSkipsUnsafeCases(t *testing.T) {
	ctx := context.Background()
	store := openAgentAppStore(t, ctx)
	defer store.Close()
	principal := createAgentAppPrincipal(t, ctx, store, "viewer@example.com")
	scope := Scope{WorkspaceID: "test", PrincipalID: principal.ID}
	model := newFailingAgentModel(errors.New("provider down"))
	service := NewService(store, Config{APIKey: "key", Model: "fake-model"}, WithModel(model))

	titled, err := service.CreateConversation(ctx, scope, "Manual title")
	if err != nil {
		t.Fatalf("create titled conversation: %v", err)
	}
	if updated, err := service.GenerateConversationTitle(ctx, scope, titled.ID); err != nil || updated.Title != "Manual title" {
		t.Fatalf("manual title changed or errored: updated=%#v err=%v", updated, err)
	}
	if len(model.Requests()) != 0 {
		t.Fatalf("model called for manual title")
	}

	multi, err := service.CreateConversation(ctx, scope, "")
	if err != nil {
		t.Fatalf("create multi conversation: %v", err)
	}
	for _, text := range []string{"hello", "again"} {
		if _, err := store.AppendMessage(ctx, MessageInput{
			PrincipalID:    scope.PrincipalID,
			ConversationID: multi.ID,
			Role:           MessageRoleUser,
			ContentText:    text,
		}); err != nil {
			t.Fatalf("append user message: %v", err)
		}
	}
	if updated, err := service.GenerateConversationTitle(ctx, scope, multi.ID); err != nil || updated.Title != ConversationDefaultTitle {
		t.Fatalf("multi-user title changed or errored: updated=%#v err=%v", updated, err)
	}
	if len(model.Requests()) != 0 {
		t.Fatalf("model called for multi-user conversation")
	}

	failing, err := service.CreateConversation(ctx, scope, "")
	if err != nil {
		t.Fatalf("create failing conversation: %v", err)
	}
	if _, err := store.AppendMessage(ctx, MessageInput{
		PrincipalID:    scope.PrincipalID,
		ConversationID: failing.ID,
		Role:           MessageRoleUser,
		ContentText:    "list semantic models",
	}); err != nil {
		t.Fatalf("append failing user message: %v", err)
	}
	if updated, err := service.GenerateConversationTitle(ctx, scope, failing.ID); err != nil || updated.Title != "List semantic models" {
		t.Fatalf("failing provider title changed or errored: updated=%#v err=%v", updated, err)
	}
	if len(model.Requests()) != 1 {
		t.Fatalf("model calls = %d, want 1 for failing provider", len(model.Requests()))
	}
}

func TestServiceConversationTranscriptDerivesDisplayItems(t *testing.T) {
	ctx := context.Background()
	store := openAgentAppStore(t, ctx)
	defer store.Close()
	principal := createAgentAppPrincipal(t, ctx, store, "viewer@example.com")
	service := NewService(store, Config{APIKey: "key", Model: "fake-model"})
	scope := Scope{WorkspaceID: "test", PrincipalID: principal.ID}
	conversation, err := service.CreateConversation(ctx, scope, "Transcript")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if _, err := store.AppendMessage(ctx, MessageInput{
		PrincipalID:    principal.ID,
		ConversationID: conversation.ID,
		Role:           MessageRoleUser,
		ContentText:    "hello",
	}); err != nil {
		t.Fatalf("append user: %v", err)
	}
	if _, err := store.AppendMessage(ctx, MessageInput{
		PrincipalID:    principal.ID,
		ConversationID: conversation.ID,
		Role:           MessageRoleSummary,
		ContentText:    "internal summary",
	}); err != nil {
		t.Fatalf("append summary: %v", err)
	}
	if _, err := store.AppendMessage(ctx, MessageInput{
		PrincipalID:    principal.ID,
		ConversationID: conversation.ID,
		Role:           MessageRoleAssistant,
		ContentText:    "Let me look that up.",
		ContentJSON:    `{"tool_calls":[{"id":"call_1","name":"list_dashboards","arguments":{"limit":2}}]}`,
	}); err != nil {
		t.Fatalf("append assistant tool call: %v", err)
	}
	if _, err := store.AppendMessage(ctx, MessageInput{
		PrincipalID:    principal.ID,
		ConversationID: conversation.ID,
		Role:           MessageRoleTool,
		ContentText:    `{"summary":"Found 2 dashboards"}`,
		ToolCallID:     "call_1",
		ToolName:       "list_dashboards",
	}); err != nil {
		t.Fatalf("append tool: %v", err)
	}
	if _, err := store.AppendMessage(ctx, MessageInput{
		PrincipalID:    principal.ID,
		ConversationID: conversation.ID,
		Role:           MessageRoleAssistant,
		ContentText:    "I found **two** dashboards.",
	}); err != nil {
		t.Fatalf("append assistant answer: %v", err)
	}

	transcript, err := service.ConversationTranscript(ctx, scope, conversation.ID)
	if err != nil {
		t.Fatalf("conversation transcript: %v", err)
	}
	if len(transcript) != 4 {
		t.Fatalf("transcript len = %d, want user/assistant/tool/assistant: %#v", len(transcript), transcript)
	}
	if transcript[0].Kind != "user" || transcript[0].Text != "hello" {
		t.Fatalf("user item = %#v", transcript[0])
	}
	if transcript[1].Kind != "assistant" || transcript[1].Markdown != "Let me look that up." {
		t.Fatalf("assistant preamble item = %#v", transcript[1])
	}
	if transcript[2].Kind != "tool" || transcript[2].ToolCallID != "call_1" || transcript[2].Status != "complete" || transcript[2].Summary != "Found 2 dashboards" || transcript[2].ResultSummary != "Found 2 dashboards" {
		t.Fatalf("tool item = %#v", transcript[2])
	}
	if strings.Contains(transcript[2].InputJSON, `"id"`) || strings.Contains(transcript[2].InputJSON, `"type"`) || !strings.Contains(transcript[2].InputJSON, `"name": "list_dashboards"`) || !strings.Contains(transcript[2].InputJSON, `"arguments": "{\"limit\":2}"`) {
		t.Fatalf("tool input preview = %q", transcript[2].InputJSON)
	}
	if !strings.Contains(transcript[2].ResultJSON, `"summary": "Found 2 dashboards"`) {
		t.Fatalf("tool result preview = %q", transcript[2].ResultJSON)
	}
	if transcript[2].Artifact != nil {
		t.Fatalf("non-visual tool should not produce artifact: %#v", transcript[2].Artifact)
	}
	if transcript[3].Kind != "assistant" || !strings.Contains(transcript[3].Markdown, "two") {
		t.Fatalf("assistant item = %#v", transcript[3])
	}
	for _, item := range transcript {
		if strings.Contains(item.Text+item.Markdown+item.Summary, "internal summary") {
			t.Fatalf("summary leaked into transcript: %#v", transcript)
		}
	}
}

func TestServiceConversationTranscriptExtractsVisualArtifact(t *testing.T) {
	ctx := context.Background()
	store := openAgentAppStore(t, ctx)
	defer store.Close()
	principal := createAgentAppPrincipal(t, ctx, store, "viewer@example.com")
	service := NewService(store, Config{APIKey: "key", Model: "fake-model"})
	scope := Scope{WorkspaceID: "test", PrincipalID: principal.ID}
	conversation, err := service.CreateConversation(ctx, scope, "Artifact")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if _, err := store.AppendMessage(ctx, MessageInput{
		PrincipalID:    principal.ID,
		ConversationID: conversation.ID,
		Role:           MessageRoleTool,
		ContentText:    `{"ok":true,"type":"bar","id":"agent_visual_123","summary":"Created chart.","signal":"visuals.agent_visual_123"}`,
		ContentJSON:    `{"display_content":{"type":"bar","id":"agent_visual_123","patch":{"visuals":{"agent_visual_123":{"type":"bar","title":"Orders","data":[{"label":"delivered","value":42}]}}},"summary":"Created chart."}}`,
		ToolCallID:     "call_1",
		ToolName:       "query_visual",
	}); err != nil {
		t.Fatalf("append tool: %v", err)
	}
	state, err := service.ConversationTranscriptState(ctx, scope, conversation.ID)
	if err != nil {
		t.Fatalf("conversation transcript: %v", err)
	}
	transcript := state.Transcript
	if len(transcript) != 1 || transcript[0].Artifact == nil {
		t.Fatalf("transcript artifact missing: %#v", transcript)
	}
	if transcript[0].Artifact.Type != "bar" || transcript[0].Artifact.ID != "agent_visual_123" {
		t.Fatalf("artifact = %#v", transcript[0].Artifact)
	}
	if strings.Contains(transcript[0].ResultJSON, "delivered") {
		t.Fatalf("compact result preview leaked chart data: %s", transcript[0].ResultJSON)
	}
	if _, ok := state.Artifacts.Visuals["agent_visual_123"]; !ok {
		t.Fatalf("artifact signal missing visual: %#v", state.Artifacts)
	}
}

func TestServiceConversationTranscriptRejectsVerboseArtifactPayload(t *testing.T) {
	ctx := context.Background()
	store := openAgentAppStore(t, ctx)
	defer store.Close()
	principal := createAgentAppPrincipal(t, ctx, store, "viewer@example.com")
	service := NewService(store, Config{APIKey: "key", Model: "fake-model"})
	scope := Scope{WorkspaceID: "test", PrincipalID: principal.ID}
	conversation, err := service.CreateConversation(ctx, scope, "Invalid artifact")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if _, err := store.AppendMessage(ctx, MessageInput{
		PrincipalID:    principal.ID,
		ConversationID: conversation.ID,
		Role:           MessageRoleTool,
		ContentText:    `{"type":"table","id":"agent_visual_123","patch":{"visuals":{"agent_visual_123":{"type":"table","title":"Orders","blocks":{"a":{"rows":[{"status":"delivered"}]}}}}},"summary":"Created table."}`,
		ToolCallID:     "call_1",
		ToolName:       "query_visual",
	}); err != nil {
		t.Fatalf("append tool: %v", err)
	}
	state, err := service.ConversationTranscriptState(ctx, scope, conversation.ID)
	if err != nil {
		t.Fatalf("conversation transcript: %v", err)
	}
	if len(state.Transcript) != 1 || state.Transcript[0].Artifact != nil {
		t.Fatalf("verbose artifact payload should be rejected: %#v", state.Transcript)
	}
	if !strings.Contains(state.Transcript[0].ResultJSON, "delivered") || !strings.Contains(state.Transcript[0].ResultJSON, `"patch"`) {
		t.Fatalf("invalid result should remain inspectable: %s", state.Transcript[0].ResultJSON)
	}
	if len(state.Artifacts.Visuals) != 0 {
		t.Fatalf("invalid artifact published visual signals: %#v", state.Artifacts)
	}
}

func TestServiceRejectsConcurrentConversationTurns(t *testing.T) {
	ctx := context.Background()
	store := openAgentAppStore(t, ctx)
	defer store.Close()
	principal := createAgentAppPrincipal(t, ctx, store, "viewer@example.com")
	model := newRecordingAgentModel(agentcore.ModelResponse{Content: "ok", FinishReason: agentcore.FinishReasonStop})
	model.delay = 100 * time.Millisecond
	service := NewService(store, Config{APIKey: "key", Model: "fake-model"}, WithModel(model))
	conversation, err := service.CreateConversation(ctx, Scope{WorkspaceID: "test", PrincipalID: principal.ID}, "Dashboards")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := service.Prompt(ctx, PromptInput{
				Scope:          Scope{WorkspaceID: "test", PrincipalID: principal.ID},
				ConversationID: conversation.ID,
				Input:          "hello",
			})
			errs <- err
		}()
	}
	first := <-errs
	second := <-errs
	if !IsBusy(first) && !IsBusy(second) {
		t.Fatalf("errors = %v / %v, want one busy error", first, second)
	}
}

type recordingAgentModel struct {
	mu        sync.Mutex
	requests  []agentcore.ModelRequest
	responses []agentcore.ModelResponse
	err       error
	delay     time.Duration
}

func newRecordingAgentModel(responses ...agentcore.ModelResponse) *recordingAgentModel {
	return &recordingAgentModel{responses: responses}
}

func newFailingAgentModel(err error) *recordingAgentModel {
	return &recordingAgentModel{err: err}
}

func (m *recordingAgentModel) Complete(ctx context.Context, req agentcore.ModelRequest, stream agentcore.ModelStream) (agentcore.ModelResponse, error) {
	if m.delay > 0 {
		timer := time.NewTimer(m.delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return agentcore.ModelResponse{}, ctx.Err()
		case <-timer.C:
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, req)
	if m.err != nil {
		return agentcore.ModelResponse{}, m.err
	}
	if len(m.responses) == 0 {
		return agentcore.ModelResponse{Content: "ok", FinishReason: agentcore.FinishReasonStop}, nil
	}
	response := m.responses[0]
	m.responses = m.responses[1:]
	if response.FinishReason == "" {
		response.FinishReason = agentcore.FinishReasonStop
	}
	if req.Purpose == agentcore.ModelRequestPurposeTurn && response.Content != "" && stream != nil {
		_ = stream.Delta(ctx, response.Content)
	}
	return response, nil
}

func (m *recordingAgentModel) Requests() []agentcore.ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agentcore.ModelRequest(nil), m.requests...)
}

type fakeAgentMetrics struct{}

func (fakeAgentMetrics) Catalog() catalog.Catalog {
	return catalog.Catalog{
		Workspace: catalog.Workspace{ID: "test", Title: "Test Workspace"},
		Models: []catalog.Model{
			{ID: "test", Title: "Test Model", Description: "Fixture model"},
		},
		Dashboards: []catalog.Dashboard{
			{ID: "executive-sales", Title: "Executive Sales", Description: "Sales dashboard", SemanticModel: "test", PageCount: 1},
		},
	}
}

func (fakeAgentMetrics) dashboardDefinition(id string) (dashboarddefinition.Definition, *semanticmodel.Model, bool) {
	if id != "executive-sales" {
		return dashboarddefinition.Definition{}, nil, false
	}
	report := fakeAgentAuthoringReport()
	model := fakeSemanticModel()
	return dashboardfixture.Compile(report, model), model, true
}

func fakeAgentAuthoringReport() dashboardauthoring.Dashboard {
	return dashboardauthoring.Dashboard{
		ID:            "executive-sales",
		Title:         "Executive Sales",
		Description:   "Sales dashboard",
		SemanticModel: "test",
		Visuals: dashboardauthoring.MergeVisualizations(dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{
			"orders": {Title: "Orders", Type: "bar", Query: dashboardauthoring.VisualQuery{Measures: []dashboardauthoring.FieldRef{{Field: "order_count"}}}},
		}), dashboardauthoring.TabularVisualizations("table", map[string]dashboardauthoring.TableVisual{
			"orders_table": {Title: "Orders", Query: dashboardauthoring.TableQuery{Table: "orders", Fields: []string{"orders.order_id"}}},
		})),
		Pages: []dashboard.Page{{ID: "overview", Title: "Overview", Visuals: []dashboard.PageVisual{{ID: "orders", Kind: "visual", Visual: "orders"}, {ID: "orders-table", Kind: "visual", Visual: "orders_table"}}}},
	}
}

func fakeSemanticModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name:  "test",
		Title: "Test Model",
		Sources: map[string]semanticmodel.Source{
			"orders": {Path: "orders.csv"},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Source:     "orders",
				PrimaryKey: "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id": {Expr: "order_id"},
				},
			},
		},
		Measures: map[string]semanticmodel.MetricMeasure{
			"order_count": {Fact: "orders", Aggregation: "count_distinct", Input: semanticmodel.MeasureInput{Field: "orders.order_id"}, Empty: "zero"},
		},
	}
}

func (fakeAgentMetrics) Pages(id string) []dashboard.Page {
	report, _, ok := fakeAgentMetrics{}.dashboardDefinition(id)
	if !ok {
		return nil
	}
	return report.Pages
}

func (fakeAgentMetrics) DefaultFilters(string) dashboard.Filters {
	return dashboard.Filters{}.WithDefaults()
}

func (fakeAgentMetrics) NormalizeVisualizationWindow(_ string, request dashboard.TableRequest) dashboard.TableRequest {
	return request.WithDefaults()
}

func (fakeAgentMetrics) QueryDashboardPage(_ context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	report, _, _ := fakeAgentMetrics{}.dashboardDefinition(dashboardID)
	definition := report.Visualizations["orders"]
	envelope, err := visualizationruntime.EnvelopeFromFrame(definition, visualizationruntime.Frame{Columns: []string{"label", "value"}, Rows: [][]any{{"delivered", 10}}}, nil, 0, 0)
	if err != nil {
		return dashboard.Patch{}, err
	}
	return dashboard.Patch{
		Filters: filters.WithDefaults(),
		Visuals: map[string]visualizationir.VisualizationEnvelope{"orders": envelope},
	}, nil
}

const largeDashboardPayloadMarker = "payload that must not appear in compact dashboard manifest"

type largeDashboardMetrics struct {
	fakeAgentMetrics
}

func (largeDashboardMetrics) dashboardDefinition(id string) (dashboarddefinition.Definition, *semanticmodel.Model, bool) {
	if id != "executive-sales" {
		return dashboarddefinition.Definition{}, nil, false
	}
	report, model := fakeAgentAuthoringReport(), fakeSemanticModel()
	report.Pages = make([]dashboard.Page, 0, 24)
	report.Visuals = map[string]dashboardauthoring.AuthoringVisualization{}
	for pageIndex := 1; pageIndex <= 24; pageIndex++ {
		chartID := fmt.Sprintf("chart_%02d", pageIndex)
		kpiID := fmt.Sprintf("kpi_%02d", pageIndex)
		tableID := fmt.Sprintf("table_%02d", pageIndex)
		report.Visuals[chartID] = dashboardauthoring.ChartVisualization(dashboardauthoring.Visual{
			Title:       fmt.Sprintf("Chart %02d", pageIndex),
			Description: largeDashboardPayloadMarker + strings.Repeat("x", 4096),
			Type:        "bar",
			Query:       dashboardauthoring.VisualQuery{Measures: []dashboardauthoring.FieldRef{{Field: "order_count"}}},
		})
		report.Visuals[kpiID] = dashboardauthoring.ChartVisualization(dashboardauthoring.Visual{
			Title:       fmt.Sprintf("KPI %02d", pageIndex),
			Description: largeDashboardPayloadMarker + strings.Repeat("y", 4096),
			Type:        "kpi",
			Query:       dashboardauthoring.VisualQuery{Measures: []dashboardauthoring.FieldRef{{Field: "order_count"}}},
		})
		report.Visuals[tableID] = dashboardauthoring.TabularVisualization("table", dashboardauthoring.TableVisual{
			Title: fmt.Sprintf("Table %02d", pageIndex),
			Query: dashboardauthoring.TableQuery{Table: "orders", Fields: []string{"orders.order_id"}},
			Columns: []dashboard.TableColumn{{
				Key:   largeDashboardPayloadMarker + strings.Repeat("z", 4096),
				Label: "Large Column",
			}},
		})
		report.Pages = append(report.Pages, dashboard.Page{
			ID:    fmt.Sprintf("page_%02d", pageIndex),
			Title: fmt.Sprintf("Page %02d", pageIndex),
			Visuals: []dashboard.PageVisual{
				{ID: chartID, Kind: "visual", Visual: chartID},
				{ID: kpiID, Kind: "visual", Visual: kpiID},
				{ID: tableID, Kind: "visual", Visual: tableID},
			},
		})
	}
	return dashboardfixture.Compile(report, model), model, true
}

func (largeDashboardMetrics) Pages(id string) []dashboard.Page {
	report, _, ok := largeDashboardMetrics{}.dashboardDefinition(id)
	if !ok {
		return nil
	}
	return report.Pages
}

func runTool(t *testing.T, tools []agentcore.ToolDefinition, name, args string) string {
	t.Helper()
	for _, tool := range tools {
		if tool.Name != name {
			continue
		}
		result, err := tool.Handler.Run(context.Background(), agentcore.ToolCall{ID: "call_1", Name: name, Arguments: []byte(args)})
		if err != nil {
			t.Fatalf("%s returned error: %v", name, err)
		}
		bytes, err := json.Marshal(result.Content)
		if err != nil {
			t.Fatalf("marshal %s result: %v", name, err)
		}
		return string(bytes)
	}
	t.Fatalf("tool %q not found", name)
	return ""
}

func fakeToolProvider(name string) ToolProvider {
	return func(Scope) []agentcore.ToolDefinition {
		return []agentcore.ToolDefinition{{
			Name:        name,
			Description: "Fake tool.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
			Handler: agentcore.ToolHandlerFunc(func(context.Context, agentcore.ToolCall) (agentcore.ToolResult, error) {
				return agentcore.ToolResult{Content: map[string]any{"name": name}}, nil
			}),
		}}
	}
}

func openAgentAppStore(t *testing.T, _ context.Context) *testAgentStore {
	t.Helper()
	return newTestAgentStore()
}

func createAgentAppPrincipal(t *testing.T, _ context.Context, store *testAgentStore, email string) testPrincipal {
	t.Helper()
	id := "principal_" + strings.NewReplacer("@", "_", ".", "_").Replace(email)
	store.upsertPrincipal(id)
	return testPrincipal{ID: id}
}

type testPrincipal struct {
	ID string
}

type testAgentStore struct {
	mu              sync.Mutex
	nextID          int
	principals      map[string]struct{}
	conversations   map[string]Conversation
	messages        map[string][]Message
	runs            map[string]Run
	runConversation map[string]string
	events          map[string][]Event
}

type workflowAgentStore struct {
	*testAgentStore
	workflows []jobs.WorkflowIntent
}

type completionWorkflowAgentStore struct {
	*workflowAgentStore
	atomicErr           error
	completionCalls     int
	fallbackFinishCalls int
	committedMessages   int
}

func (s *completionWorkflowAgentStore) CompleteRunWorkflow(ctx context.Context, finish RunFinish, messages []MessageInput, transcript string, _ jobs.WorkflowIntent) ([]Message, bool, error) {
	s.completionCalls++
	if s.atomicErr != nil {
		return nil, false, s.atomicErr
	}
	rows := make([]Message, 0, len(messages))
	for _, input := range messages {
		row, err := s.testAgentStore.AppendMessage(ctx, input)
		if err != nil {
			return nil, false, err
		}
		rows = append(rows, row)
	}
	if _, err := s.testAgentStore.UpdateConversationTranscript(ctx, finish.PrincipalID, finish.ConversationID, transcript); err != nil {
		return nil, false, err
	}
	if _, err := s.testAgentStore.FinishRun(ctx, finish); err != nil {
		return nil, false, err
	}
	s.committedMessages += len(rows)
	return rows, true, nil
}

func (s *completionWorkflowAgentStore) FinishRun(ctx context.Context, input RunFinish) (Run, error) {
	s.fallbackFinishCalls++
	return s.testAgentStore.FinishRun(ctx, input)
}

func (s *workflowAgentStore) CreateRun(ctx context.Context, input RunInput) (Run, error) {
	run, err := s.testAgentStore.CreateRun(ctx, input)
	if err != nil {
		return Run{}, err
	}
	s.mu.Lock()
	run.Status = input.Status
	s.runs[run.ID] = run
	s.mu.Unlock()
	return run, nil
}

func (s *workflowAgentStore) ActivateRunWorkflow(_ context.Context, principalID, conversationID, runID string, workflow jobs.WorkflowIntent) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.conversationLocked(principalID, conversationID); err != nil {
		return Run{}, err
	}
	run, ok := s.runs[runID]
	if !ok || s.runConversation[runID] != conversationID {
		return Run{}, sql.ErrNoRows
	}
	run.Status = RunStatusRunning
	s.runs[run.ID] = run
	s.workflows = append(s.workflows, workflow)
	return run, nil
}

func newTestAgentStore() *testAgentStore {
	return &testAgentStore{
		principals:      map[string]struct{}{},
		conversations:   map[string]Conversation{},
		messages:        map[string][]Message{},
		runs:            map[string]Run{},
		runConversation: map[string]string{},
		events:          map[string][]Event{},
	}
}

func (s *testAgentStore) Close() error { return nil }

func (s *testAgentStore) upsertPrincipal(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.principals[id] = struct{}{}
}

func (s *testAgentStore) CreateConversation(_ context.Context, input ConversationInput) (Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = ConversationDefaultTitle
	}
	conversation := Conversation{
		ID:             s.id("agentconv"),
		PrincipalID:    input.PrincipalID,
		Title:          title,
		Status:         ConversationStatusActive,
		MetadataJSON:   firstNonEmpty(input.MetadataJSON, "{}"),
		TranscriptJSON: "[]",
		CreatedAt:      testNow(),
		UpdatedAt:      testNow(),
	}
	s.conversations[conversation.ID] = conversation
	return conversation, nil
}

func (s *testAgentStore) ListConversations(_ context.Context, principalID string) ([]Conversation, error) {
	return s.ListConversationsPage(context.Background(), principalID, Page{})
}

func (s *testAgentStore) ListConversationsPage(_ context.Context, principalID string, page Page) ([]Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Conversation
	for _, conversation := range s.conversations {
		if conversation.PrincipalID == principalID && conversation.Status == ConversationStatusActive {
			out = append(out, conversation)
		}
	}
	return pageTestRows(out, page, func(row Conversation) string { return row.ID }), nil
}

func (s *testAgentStore) GetConversation(_ context.Context, principalID, conversationID string) (Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conversationLocked(principalID, conversationID)
}

func (s *testAgentStore) UpdateConversation(_ context.Context, input ConversationUpdate) (Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, err := s.conversationLocked(input.PrincipalID, input.ConversationID)
	if err != nil {
		return Conversation{}, err
	}
	if conversation.Status != ConversationStatusActive {
		return Conversation{}, sql.ErrNoRows
	}
	conversation.Title = strings.TrimSpace(input.Title)
	conversation.UpdatedAt = testNow()
	s.conversations[conversation.ID] = conversation
	return conversation, nil
}

func (s *testAgentStore) UpdateConversationAtomic(_ context.Context, input ConversationUpdate, check func(Conversation) error) (Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, err := s.conversationLocked(input.PrincipalID, input.ConversationID)
	if err != nil {
		return Conversation{}, err
	}
	if check != nil {
		if err := check(conversation); err != nil {
			return Conversation{}, err
		}
	}
	if conversation.Status != ConversationStatusActive {
		return Conversation{}, sql.ErrNoRows
	}
	conversation.Title = strings.TrimSpace(input.Title)
	conversation.UpdatedAt = testNow()
	s.conversations[conversation.ID] = conversation
	return conversation, nil
}

func (s *testAgentStore) ArchiveConversation(_ context.Context, principalID, conversationID string) (Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, err := s.conversationLocked(principalID, conversationID)
	if err != nil {
		return Conversation{}, err
	}
	conversation.Status = ConversationStatusArchived
	conversation.ArchivedAt = testNow()
	conversation.UpdatedAt = testNow()
	s.conversations[conversation.ID] = conversation
	return conversation, nil
}

func (s *testAgentStore) UpdateDefaultConversationTitle(_ context.Context, principalID, conversationID, title string) (Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, err := s.conversationLocked(principalID, conversationID)
	if err != nil {
		return Conversation{}, err
	}
	if conversation.Title != ConversationDefaultTitle || conversation.Status != ConversationStatusActive {
		return Conversation{}, sql.ErrNoRows
	}
	conversation.Title = title
	conversation.UpdatedAt = testNow()
	s.conversations[conversation.ID] = conversation
	return conversation, nil
}

func (s *testAgentStore) UpdateConversationTranscript(_ context.Context, principalID, conversationID, transcriptJSON string) (Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversation, err := s.conversationLocked(principalID, conversationID)
	if err != nil {
		return Conversation{}, err
	}
	conversation.TranscriptJSON = transcriptJSON
	conversation.UpdatedAt = testNow()
	s.conversations[conversation.ID] = conversation
	return conversation, nil
}

func (s *testAgentStore) AppendMessage(_ context.Context, input MessageInput) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.conversationLocked(input.PrincipalID, input.ConversationID); err != nil {
		return Message{}, err
	}
	message := Message{
		ID:             s.id("agentmsg"),
		ConversationID: input.ConversationID,
		RunID:          input.RunID,
		Seq:            int64(len(s.messages[input.ConversationID]) + 1),
		Role:           input.Role,
		ContentText:    input.ContentText,
		ContentJSON:    firstNonEmpty(input.ContentJSON, "{}"),
		ToolCallID:     input.ToolCallID,
		ToolName:       input.ToolName,
		IsError:        input.IsError,
		CreatedAt:      testNow(),
	}
	s.messages[input.ConversationID] = append(s.messages[input.ConversationID], message)
	return message, nil
}

func (s *testAgentStore) ListMessages(_ context.Context, principalID, conversationID string) ([]Message, error) {
	return s.ListMessagesPage(context.Background(), principalID, conversationID, Page{})
}

func (s *testAgentStore) ListMessagesPage(_ context.Context, principalID, conversationID string, page Page) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.conversationLocked(principalID, conversationID); err != nil {
		return nil, err
	}
	return pageTestRows(s.messages[conversationID], page, func(row Message) string { return row.ID }), nil
}

func (s *testAgentStore) CreateRun(_ context.Context, input RunInput) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.conversationLocked(input.PrincipalID, input.ConversationID); err != nil {
		return Run{}, err
	}
	runID := strings.TrimSpace(input.RunID)
	if runID == "" {
		runID = s.id("agentrun")
	}
	run := Run{ID: runID, ConversationID: input.ConversationID, Status: RunStatusRunning, Model: input.Model, StartedAt: testNow(), CreatedAt: testNow()}
	s.runs[run.ID] = run
	s.runConversation[run.ID] = input.ConversationID
	return run, nil
}

func (s *testAgentStore) FinishRun(_ context.Context, input RunFinish) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.conversationLocked(input.PrincipalID, input.ConversationID); err != nil {
		return Run{}, err
	}
	run, ok := s.runs[input.RunID]
	if !ok {
		return Run{}, sql.ErrNoRows
	}
	run.Status = input.Status
	run.StopReason = input.StopReason
	run.InputTokens = input.InputTokens
	run.OutputTokens = input.OutputTokens
	run.TotalTokens = input.TotalTokens
	run.Error = input.Error
	run.MetadataJSON = input.MetadataJSON
	run.FinishedAt = testNow()
	s.runs[run.ID] = run
	return run, nil
}

func (s *testAgentStore) ListRuns(_ context.Context, principalID, conversationID string) ([]Run, error) {
	return s.ListRunsPage(context.Background(), principalID, conversationID, Page{})
}

func (s *testAgentStore) ListRunsPage(_ context.Context, principalID, conversationID string, page Page) ([]Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.conversationLocked(principalID, conversationID); err != nil {
		return nil, err
	}
	var out []Run
	for runID, candidate := range s.runs {
		if s.runConversation[runID] == conversationID {
			out = append(out, candidate)
		}
	}
	return pageTestRows(out, page, func(row Run) string { return row.ID }), nil
}

func (s *testAgentStore) GetRun(_ context.Context, principalID, conversationID, runID string) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.conversationLocked(principalID, conversationID); err != nil {
		return Run{}, err
	}
	run, ok := s.runs[runID]
	if !ok || s.runConversation[runID] != conversationID {
		return Run{}, sql.ErrNoRows
	}
	return run, nil
}

func (s *testAgentStore) GetRunByID(_ context.Context, principalID, runID string) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversationID, ok := s.runConversation[runID]
	if !ok {
		return Run{}, sql.ErrNoRows
	}
	if _, err := s.conversationLocked(principalID, conversationID); err != nil {
		return Run{}, err
	}
	return s.runs[runID], nil
}

func (s *testAgentStore) AppendEvent(_ context.Context, input EventInput) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversationID, ok := s.runConversation[input.RunID]
	if !ok {
		return Event{}, sql.ErrNoRows
	}
	if _, err := s.conversationLocked(input.PrincipalID, conversationID); err != nil {
		return Event{}, err
	}
	event := Event{
		ID:          s.id("agentevt"),
		RunID:       input.RunID,
		Seq:         input.Sequence,
		EventType:   input.EventType,
		Severity:    firstNonEmpty(input.Severity, "info"),
		PayloadJSON: firstNonEmpty(input.PayloadJSON, "{}"),
		CreatedAt:   testNow(),
	}
	s.events[input.RunID] = append(s.events[input.RunID], event)
	return event, nil
}

func (s *testAgentStore) ListEvents(_ context.Context, principalID, runID string) ([]Event, error) {
	return s.ListEventsPage(context.Background(), principalID, runID, Page{})
}

func (s *testAgentStore) ListEventsPage(_ context.Context, principalID, runID string, page Page) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conversationID, ok := s.runConversation[runID]
	if !ok {
		return nil, sql.ErrNoRows
	}
	if _, err := s.conversationLocked(principalID, conversationID); err != nil {
		return nil, err
	}
	return pageTestRows(s.events[runID], page, func(row Event) string { return row.ID }), nil
}

func (s *testAgentStore) conversationLocked(principalID, conversationID string) (Conversation, error) {
	conversation, ok := s.conversations[conversationID]
	if !ok || conversation.PrincipalID != principalID {
		return Conversation{}, sql.ErrNoRows
	}
	return conversation, nil
}

func (s *testAgentStore) id(prefix string) string {
	s.nextID++
	return fmt.Sprintf("%s_%d", prefix, s.nextID)
}

func testNow() string {
	return "2026-01-01 00:00:00"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func pageTestRows[T any](rows []T, page Page, id func(T) string) []T {
	limit := page.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	start := 0
	if page.After != "" {
		start = len(rows)
		for i, row := range rows {
			if id(row) == page.After {
				start = i + 1
				break
			}
		}
	}
	if start >= len(rows) {
		return []T{}
	}
	end := start + limit
	if end > len(rows) {
		end = len(rows)
	}
	return append([]T(nil), rows[start:end]...)
}
