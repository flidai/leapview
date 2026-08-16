package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/jobs"
	jobsqlite "github.com/flidai/leapview/internal/platform/jobs/sqlite"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectsqlite "github.com/flidai/leapview/internal/project/sqlite"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

type panicPhaseRepository struct {
	*Repository
	phase string
}

type activationResponseLossRepository struct {
	*Repository
	panicAfterActivation bool
}

func (r *activationResponseLossRepository) ActivateRunWorkflow(ctx context.Context, principalID, conversationID, runID string, workflow jobs.WorkflowIntent) (agent.Run, error) {
	run, err := r.Repository.ActivateRunWorkflow(ctx, principalID, conversationID, runID, workflow)
	if err == nil && r.panicAfterActivation {
		panic("simulated response loss after activation commit")
	}
	return run, err
}

func (r *panicPhaseRepository) CreateRun(ctx context.Context, input agent.RunInput) (agent.Run, error) {
	run, err := r.Repository.CreateRun(ctx, input)
	if r.phase == "run" {
		panic("simulated crash after run persistence")
	}
	return run, err
}

func (r *panicPhaseRepository) AppendMessage(ctx context.Context, input agent.MessageInput) (agent.Message, error) {
	message, err := r.Repository.AppendMessage(ctx, input)
	if r.phase == "message" {
		panic("simulated crash after message persistence")
	}
	return message, err
}

func (r *panicPhaseRepository) UpdateConversationTranscript(ctx context.Context, principalID, conversationID, transcriptJSON string) (agent.Conversation, error) {
	conversation, err := r.Repository.UpdateConversationTranscript(ctx, principalID, conversationID, transcriptJSON)
	if r.phase == "transcript" {
		panic("simulated crash after transcript persistence")
	}
	return conversation, err
}

func TestDurablePromptIdempotencyRepairsCrashPhases(t *testing.T) {
	for _, phase := range []string{"run", "message", "transcript"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			store, base := openAgentRepo(t, ctx)
			owner := createAgentPrincipal(t, ctx, store, "retry-"+phase+"@example.com")
			conversation, err := base.CreateConversation(ctx, agent.ConversationInput{PrincipalID: owner.ID})
			if err != nil {
				t.Fatal(err)
			}
			jobsRepo := jobsqlite.NewRepository(store.SQLDB())
			repo := &panicPhaseRepository{Repository: NewRepositoryWithWorkflow(store.SQLDB(), jobsRepo, jobsRepo), phase: phase}
			service := agent.NewService(repo, agent.Config{APIKey: "key", Model: "test"}, agent.WithModel(agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
				return agentcore.ModelResponse{}, fmt.Errorf("unused")
			})))
			service.SetPromptWorkflow(func(_ agent.PromptInput, runID string, _ agent.PromptDispatch) jobs.WorkflowIntent {
				payload := []byte(fmt.Sprintf(`{"run":"%s"}`, runID))
				return jobs.WorkflowIntent{Event: jobs.EventInput{Key: "agent_run.queued", ResourceKind: "agent_run", ResourceID: runID, EventType: "agent_run.queued", Data: []byte(`{}`)}, Job: jobs.EnqueueInput{ID: "agent:" + runID + ":run", Kind: "agent.run", WorkloadClass: jobs.WorkloadClassBackground, PrincipalID: owner.ID, GroupIDs: []string{}, EstimatedMemoryBytes: 1, ResourceKind: "agent_run", ResourceID: runID, Payload: payload}}
			})
			input := agent.PromptInput{Scope: agent.Scope{ProjectID: "test", PrincipalID: owner.ID}, ConversationID: conversation.ID, Input: "same prompt", RequestID: "retry-key"}
			func() {
				defer func() { _ = recover() }()
				_, _ = service.StartDurablePrompt(ctx, input, agent.PromptDispatch{})
			}()
			repo.phase = ""
			started, err := service.StartDurablePrompt(ctx, input, agent.PromptDispatch{})
			if err != nil {
				t.Fatalf("retry phase %s: %v", phase, err)
			}
			if !started.DurablyQueued() {
				t.Fatal("retry did not activate durable run")
			}
			runs, _ := repo.ListRuns(ctx, owner.ID, conversation.ID)
			messages, _ := repo.ListMessages(ctx, owner.ID, conversation.ID)
			events, _ := jobsRepo.ListEvents(ctx, "agent_run", started.RunID, 0, 20)
			job, err := jobsRepo.Get(ctx, "agent:"+started.RunID+":run")
			if err != nil || len(runs) != 1 || len(messages) != 1 || len(events) != 1 || job.ID == "" {
				t.Fatalf("repaired durable state runs=%d messages=%d events=%d job=%#v err=%v", len(runs), len(messages), len(events), job, err)
			}
		})
	}
}

func TestDurablePromptRetryAfterActivationResponseLossConverges(t *testing.T) {
	ctx := context.Background()
	store, base := openAgentRepo(t, ctx)
	owner := createAgentPrincipal(t, ctx, store, "activation-retry@example.com")
	conversation, err := base.CreateConversation(ctx, agent.ConversationInput{PrincipalID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	queue := jobsqlite.NewRepository(store.SQLDB())
	repo := &activationResponseLossRepository{Repository: NewRepositoryWithWorkflow(store.SQLDB(), queue, queue), panicAfterActivation: true}
	service := agent.NewService(repo, agent.Config{APIKey: "key", Model: "fake"}, agent.WithModel(agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
		return agentcore.ModelResponse{}, errors.New("unused")
	})))
	service.SetPromptWorkflow(func(_ agent.PromptInput, runID string, _ agent.PromptDispatch) jobs.WorkflowIntent {
		return jobs.WorkflowIntent{Event: jobs.EventInput{Key: "agent_run.queued:" + runID, ResourceKind: "agent_run", ResourceID: runID, EventType: "agent_run.queued", Data: []byte(`{}`)}, Job: jobs.EnqueueInput{ID: "agent:" + runID + ":run", Kind: "agent.run", WorkloadClass: jobs.WorkloadClassBackground, PrincipalID: owner.ID, GroupIDs: []string{}, EstimatedMemoryBytes: 1, ResourceKind: "agent_run", ResourceID: runID, Payload: []byte(`{}`)}}
	})
	input := agent.PromptInput{Scope: agent.Scope{ProjectID: "test", PrincipalID: owner.ID}, ConversationID: conversation.ID, Input: "same request", RequestID: "request-1"}
	func() {
		defer func() { _ = recover() }()
		_, _ = service.StartDurablePrompt(ctx, input, agent.PromptDispatch{})
	}()
	repo.panicAfterActivation = false
	started, err := service.StartDurablePrompt(ctx, input, agent.PromptDispatch{})
	if err != nil {
		t.Fatalf("retry after activation response loss: %v", err)
	}
	if !started.DurablyQueued() {
		t.Fatal("retry did not return durably queued prompt")
	}
	runs, err := repo.ListRuns(ctx, owner.ID, conversation.ID)
	if err != nil || len(runs) != 1 || runs[0].ID != started.RunID || runs[0].Status != agent.RunStatusRunning {
		t.Fatalf("runs after retry = %#v err=%v", runs, err)
	}
	messages, err := repo.ListMessages(ctx, owner.ID, conversation.ID)
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages after retry = %#v err=%v", messages, err)
	}
	events, err := queue.ListEvents(ctx, "agent_run", started.RunID, 0, 20)
	if err != nil || len(events) != 1 {
		t.Fatalf("events after retry = %#v err=%v", events, err)
	}
	if _, err := queue.Get(ctx, "agent:"+started.RunID+":run"); err != nil {
		t.Fatalf("durable job after retry: %v", err)
	}
}

func TestDurablePromptRetryMismatchedDigestConflicts(t *testing.T) {
	ctx := context.Background()
	store, base := openAgentRepo(t, ctx)
	owner := createAgentPrincipal(t, ctx, store, "digest-retry@example.com")
	conversation, err := base.CreateConversation(ctx, agent.ConversationInput{PrincipalID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	queue := jobsqlite.NewRepository(store.SQLDB())
	repo := NewRepositoryWithWorkflow(store.SQLDB(), queue, queue)
	service := agent.NewService(repo, agent.Config{APIKey: "key", Model: "fake"}, agent.WithModel(agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
		return agentcore.ModelResponse{}, errors.New("unused")
	})))
	service.SetPromptWorkflow(func(_ agent.PromptInput, runID string, _ agent.PromptDispatch) jobs.WorkflowIntent {
		return jobs.WorkflowIntent{Event: jobs.EventInput{Key: "agent_run.queued:" + runID, ResourceKind: "agent_run", ResourceID: runID, EventType: "agent_run.queued", Data: []byte(`{}`)}, Job: jobs.EnqueueInput{ID: "agent:" + runID + ":run", Kind: "agent.run", WorkloadClass: jobs.WorkloadClassBackground, PrincipalID: owner.ID, GroupIDs: []string{}, EstimatedMemoryBytes: 1, ResourceKind: "agent_run", ResourceID: runID, Payload: []byte(`{}`)}}
	})
	input := agent.PromptInput{Scope: agent.Scope{ProjectID: "test", PrincipalID: owner.ID}, ConversationID: conversation.ID, Input: "one", RequestID: "request-2"}
	if _, err := service.StartDurablePrompt(ctx, input, agent.PromptDispatch{}); err != nil {
		t.Fatal(err)
	}
	service = agent.NewService(repo, agent.Config{APIKey: "key", Model: "fake"}, agent.WithModel(agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
		return agentcore.ModelResponse{}, errors.New("unused")
	})))
	service.SetPromptWorkflow(func(_ agent.PromptInput, runID string, _ agent.PromptDispatch) jobs.WorkflowIntent {
		return jobs.WorkflowIntent{Event: jobs.EventInput{Key: "agent_run.queued:" + runID, ResourceKind: "agent_run", ResourceID: runID, EventType: "agent_run.queued", Data: []byte(`{}`)}, Job: jobs.EnqueueInput{ID: "agent:" + runID + ":run", Kind: "agent.run", WorkloadClass: jobs.WorkloadClassBackground, PrincipalID: owner.ID, GroupIDs: []string{}, EstimatedMemoryBytes: 1, ResourceKind: "agent_run", ResourceID: runID, Payload: []byte(`{}`)}}
	})
	input.Input = "different"
	if _, err := service.StartDurablePrompt(ctx, input, agent.PromptDispatch{}); !errors.Is(err, agent.ErrRequestConflict) {
		t.Fatalf("mismatched retry error = %v, want ErrRequestConflict", err)
	}
}

func TestRepositoryPersistsConversationRunMessagesAndEvents(t *testing.T) {
	ctx := context.Background()
	store, repo := openAgentRepo(t, ctx)
	owner := createAgentPrincipal(t, ctx, store, "owner@example.com")
	other := createAgentPrincipal(t, ctx, store, "other@example.com")

	conversation, err := repo.CreateConversation(ctx, agent.ConversationInput{
		PrincipalID:  owner.ID,
		Title:        "Ask about dashboards",
		MetadataJSON: `{"source":"test"}`,
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if conversation.PrincipalID != owner.ID {
		t.Fatalf("conversation owner = %s, want %s", conversation.PrincipalID, owner.ID)
	}
	if conversation.Status != agent.ConversationStatusActive || conversation.TranscriptJSON != "[]" {
		t.Fatalf("conversation = %#v", conversation)
	}
	conversation, err = repo.UpdateConversationTranscript(ctx, owner.ID, conversation.ID, `[{"role":"user","content":"seed"}]`)
	if err != nil {
		t.Fatalf("update transcript: %v", err)
	}
	if conversation.TranscriptJSON != `[{"role":"user","content":"seed"}]` {
		t.Fatalf("updated transcript = %q", conversation.TranscriptJSON)
	}

	hidden, err := repo.CreateConversation(ctx, agent.ConversationInput{
		PrincipalID: other.ID,
		Title:       "Other user chat",
	})
	if err != nil {
		t.Fatalf("create hidden conversation: %v", err)
	}
	conversations, err := repo.ListConversations(ctx, owner.ID)
	if err != nil {
		t.Fatalf("list conversations: %v", err)
	}
	if len(conversations) != 1 || conversations[0].ID != conversation.ID {
		t.Fatalf("visible conversations = %#v, want only %s", conversations, conversation.ID)
	}
	if _, err := repo.GetConversation(ctx, owner.ID, hidden.ID); err != sql.ErrNoRows {
		t.Fatalf("get other principal conversation error = %v, want sql.ErrNoRows", err)
	}
	conversation, err = repo.UpdateConversation(ctx, agent.ConversationUpdate{
		PrincipalID:    owner.ID,
		ConversationID: conversation.ID,
		Title:          "Updated title",
	})
	if err != nil {
		t.Fatalf("update conversation: %v", err)
	}
	if conversation.Title != "Updated title" {
		t.Fatalf("updated title = %q", conversation.Title)
	}

	userMessage, err := repo.AppendMessage(ctx, agent.MessageInput{
		PrincipalID:    owner.ID,
		ConversationID: conversation.ID,
		Role:           agent.MessageRoleUser,
		ContentText:    "What dashboards can I use?",
		ContentJSON:    `{"text":"What dashboards can I use?"}`,
	})
	if err != nil {
		t.Fatalf("append user message: %v", err)
	}
	assistantMessage, err := repo.AppendMessage(ctx, agent.MessageInput{
		PrincipalID:    owner.ID,
		ConversationID: conversation.ID,
		Role:           agent.MessageRoleAssistant,
		ContentText:    "You can use Executive Sales.",
	})
	if err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	if userMessage.Seq != 1 || assistantMessage.Seq != 2 {
		t.Fatalf("message seqs = %d,%d, want 1,2", userMessage.Seq, assistantMessage.Seq)
	}
	messages, err := repo.ListMessages(ctx, owner.ID, conversation.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Role != agent.MessageRoleUser || messages[1].Role != agent.MessageRoleAssistant {
		t.Fatalf("messages = %#v", messages)
	}
	if _, err := repo.ListMessages(ctx, other.ID, conversation.ID); err != sql.ErrNoRows {
		t.Fatalf("list other principal messages error = %v, want sql.ErrNoRows", err)
	}

	run, err := repo.CreateRun(ctx, agent.RunInput{
		PrincipalID:    owner.ID,
		ConversationID: conversation.ID,
		RunID:          "run_external",
		Model:          "gpt-test",
		MetadataJSON:   `{"provider":"fake"}`,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if run.Status != agent.RunStatusRunning || run.ID != "run_external" {
		t.Fatalf("run = %#v", run)
	}
	eventOne, err := repo.AppendEvent(ctx, agent.EventInput{
		PrincipalID: owner.ID,
		RunID:       run.ID,
		Sequence:    7,
		EventType:   "model_request",
		Severity:    "debug",
		PayloadJSON: `{"purpose":"turn"}`,
	})
	if err != nil {
		t.Fatalf("append event one: %v", err)
	}
	eventTwo, err := repo.AppendEvent(ctx, agent.EventInput{
		PrincipalID: owner.ID,
		RunID:       run.ID,
		Sequence:    8,
		EventType:   "model_response",
		Severity:    "debug",
		PayloadJSON: `{"finish":"stop"}`,
	})
	if err != nil {
		t.Fatalf("append event two: %v", err)
	}
	if eventOne.Seq != 7 || eventTwo.Seq != 8 {
		t.Fatalf("event seqs = %d,%d, want 7,8", eventOne.Seq, eventTwo.Seq)
	}
	run, err = repo.FinishRun(ctx, agent.RunFinish{
		PrincipalID:    owner.ID,
		ConversationID: conversation.ID,
		RunID:          run.ID,
		Status:         agent.RunStatusCompleted,
		StopReason:     "completed",
		InputTokens:    10,
		OutputTokens:   20,
		TotalTokens:    30,
		MetadataJSON:   `{"provider":"fake","model":"gpt-test"}`,
	})
	if err != nil {
		t.Fatalf("finish run: %v", err)
	}
	if run.Status != agent.RunStatusCompleted {
		t.Fatalf("finished run = %#v", run)
	}
	gotRun, err := repo.GetRun(ctx, owner.ID, conversation.ID, run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if gotRun.ID != run.ID || gotRun.ConversationID != conversation.ID || gotRun.TotalTokens != 30 || gotRun.FinishedAt == "" {
		t.Fatalf("got run = %#v", gotRun)
	}
	runs, err := repo.ListRunsPage(ctx, owner.ID, conversation.ID, agent.Page{Limit: 1})
	if err != nil {
		t.Fatalf("list runs page: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("runs page = %#v", runs)
	}
	events, err := repo.ListEvents(ctx, owner.ID, run.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 2 || events[0].Seq != 7 || events[1].Seq != 8 {
		t.Fatalf("events = %#v", events)
	}
	events, err = repo.ListEventsPage(ctx, owner.ID, run.ID, agent.Page{Limit: 1, After: eventOne.ID})
	if err != nil {
		t.Fatalf("list events page: %v", err)
	}
	if len(events) != 1 || events[0].ID != eventTwo.ID {
		t.Fatalf("events page = %#v", events)
	}
	archived, err := repo.ArchiveConversation(ctx, owner.ID, conversation.ID)
	if err != nil {
		t.Fatalf("archive conversation: %v", err)
	}
	if archived.Status != agent.ConversationStatusArchived || archived.ArchivedAt == "" {
		t.Fatalf("archived conversation = %#v", archived)
	}
	conversations, err = repo.ListConversations(ctx, owner.ID)
	if err != nil {
		t.Fatalf("list after archive: %v", err)
	}
	if len(conversations) != 0 {
		t.Fatalf("archived conversation should be hidden from active list: %#v", conversations)
	}
}

func TestRepositoryScopesConversationsToPrincipal(t *testing.T) {
	ctx := context.Background()
	store, repo := openAgentRepo(t, ctx)
	owner := createAgentPrincipal(t, ctx, store, "global-owner@example.com")

	conversation, err := repo.CreateConversation(ctx, agent.ConversationInput{
		PrincipalID: owner.ID,
		Title:       "Global conversation",
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	conversations, err := repo.ListConversations(ctx, owner.ID)
	if err != nil {
		t.Fatalf("list conversations: %v", err)
	}
	if len(conversations) != 1 || conversations[0].ID != conversation.ID {
		t.Fatalf("conversations = %#v, want principal-owned conversation %s", conversations, conversation.ID)
	}
	if _, err := repo.GetConversation(ctx, owner.ID, conversation.ID); err != nil {
		t.Fatalf("get principal conversation: %v", err)
	}
}

func TestRepositoryCreateRunRejectsArchivedConversation(t *testing.T) {
	ctx := context.Background()
	store, repo := openAgentRepo(t, ctx)
	owner := createAgentPrincipal(t, ctx, store, "archive-run@example.com")
	conversation, err := repo.CreateConversation(ctx, agent.ConversationInput{PrincipalID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ArchiveConversation(ctx, owner.ID, conversation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRun(ctx, agent.RunInput{PrincipalID: owner.ID, ConversationID: conversation.ID, RunID: "archived-run", Status: agent.RunStatusRunning}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("CreateRun error = %v, want sql.ErrNoRows", err)
	}
}

func TestDurableStartArchivedConversationHasNoSideEffects(t *testing.T) {
	ctx := context.Background()
	store, base := openAgentRepo(t, ctx)
	owner := createAgentPrincipal(t, ctx, store, "archive-durable@example.com")
	conversation, err := base.CreateConversation(ctx, agent.ConversationInput{PrincipalID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.ArchiveConversation(ctx, owner.ID, conversation.ID); err != nil {
		t.Fatal(err)
	}
	queue := jobsqlite.NewRepository(store.SQLDB())
	repo := NewRepositoryWithWorkflow(store.SQLDB(), queue, queue)
	service := agent.NewService(repo, agent.Config{APIKey: "key", Model: "fake"}, agent.WithModel(agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
		return agentcore.ModelResponse{}, errors.New("unused")
	})))
	service.SetPromptWorkflow(func(_ agent.PromptInput, runID string, _ agent.PromptDispatch) jobs.WorkflowIntent {
		return jobs.WorkflowIntent{Event: jobs.EventInput{Key: "agent_run.queued:" + runID, ResourceKind: "agent_run", ResourceID: runID, EventType: "agent_run.queued", Data: []byte(`{}`)}, Job: jobs.EnqueueInput{ID: "agent:" + runID + ":run", Kind: "agent.run", WorkloadClass: jobs.WorkloadClassBackground, PrincipalID: owner.ID, GroupIDs: []string{}, EstimatedMemoryBytes: 1, ResourceKind: "agent_run", ResourceID: runID, Payload: []byte(`{}`)}}
	})
	_, err = service.StartDurablePrompt(ctx, agent.PromptInput{Scope: agent.Scope{ProjectID: "test", PrincipalID: owner.ID}, ConversationID: conversation.ID, Input: "must not start"}, agent.PromptDispatch{})
	if !errors.Is(err, agent.ErrConversationArchived) {
		t.Fatalf("StartDurablePrompt error = %v, want archived", err)
	}
	runs, err := repo.ListRuns(ctx, owner.ID, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := repo.ListMessages(ctx, owner.ID, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 || len(messages) != 0 {
		t.Fatalf("archived durable start created runs/messages: %d/%d", len(runs), len(messages))
	}
	candidates, err := queue.Candidates(ctx, jobs.WorkloadClassBackground, 20)
	if err != nil {
		t.Fatal(err)
	}
	events, err := queue.ListEvents(ctx, "agent_run", "missing", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 || len(events) != 0 {
		t.Fatalf("archived durable start created queue side effects: jobs=%d events=%d", len(candidates), len(events))
	}
}

func TestActivateRunWorkflowRollsBackRunningTransitionOnFailure(t *testing.T) {
	ctx := context.Background()
	store, base := openAgentRepo(t, ctx)
	owner := createAgentPrincipal(t, ctx, store, "atomic-owner@example.com")
	conversation, err := base.CreateConversation(ctx, agent.ConversationInput{PrincipalID: owner.ID, Title: "Atomic run"})
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected workflow failure")
	repo := NewRepositoryWithWorkflow(
		store.SQLDB(),
		jobsqlite.NewRepository(store.SQLDB()),
		jobs.WorkflowRecorderFunc(func(context.Context, transaction.Transaction, jobs.WorkflowIntent) error {
			return injected
		}),
	)
	run, err := repo.CreateRun(ctx, agent.RunInput{
		PrincipalID: owner.ID, ConversationID: conversation.ID, RunID: "run_atomic",
		Model: "gpt-test", MetadataJSON: `{}`, Status: agent.RunStatusPreparing,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.ActivateRunWorkflow(ctx, owner.ID, conversation.ID, run.ID, jobs.WorkflowIntent{
		Job: jobs.EnqueueInput{ID: "agent:" + run.ID + ":run"},
	})
	if !errors.Is(err, injected) {
		t.Fatalf("ActivateRunWorkflow() error = %v, want injected failure", err)
	}
	current, err := repo.GetRun(ctx, owner.ID, conversation.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != agent.RunStatusPreparing {
		t.Fatalf("status after workflow failure = %q, want preparing", current.Status)
	}
}

func TestFinishRunWorkflowRollsBackTerminalTransitionOnEventFailure(t *testing.T) {
	ctx := context.Background()
	store, base := openAgentRepo(t, ctx)
	owner := createAgentPrincipal(t, ctx, store, "terminal-atomic@example.com")
	conversation, err := base.CreateConversation(ctx, agent.ConversationInput{PrincipalID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	repo := NewRepositoryWithWorkflow(store.SQLDB(), jobsqlite.NewRepository(store.SQLDB()), jobs.WorkflowRecorderFunc(func(context.Context, transaction.Transaction, jobs.WorkflowIntent) error {
		return errors.New("event failure")
	}))
	run, err := repo.CreateRun(ctx, agent.RunInput{PrincipalID: owner.ID, ConversationID: conversation.ID, RunID: "run_terminal_atomic", Status: agent.RunStatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = repo.FinishRunWorkflow(ctx, agent.RunFinish{PrincipalID: owner.ID, ConversationID: conversation.ID, RunID: run.ID, Status: agent.RunStatusFailed, MetadataJSON: `{}`}, jobs.WorkflowIntent{Event: jobs.EventInput{Key: "agent_run.failed:" + run.ID, ResourceKind: "agent_run", ResourceID: run.ID, EventType: "agent_run.failed", Data: []byte(`{}`)}})
	if err == nil {
		t.Fatal("FinishRunWorkflow() unexpectedly succeeded")
	}
	current, err := repo.GetRun(ctx, owner.ID, conversation.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != agent.RunStatusRunning {
		t.Fatalf("status after event failure = %q, want running", current.Status)
	}
}

func TestFinishRunWorkflowRejectsReclaimedLease(t *testing.T) {
	ctx := context.Background()
	store, base := openAgentRepo(t, ctx)
	owner := createAgentPrincipal(t, ctx, store, "lease-fence@example.com")
	conversation, err := base.CreateConversation(ctx, agent.ConversationInput{PrincipalID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	jobsRepo := jobsqlite.NewRepository(store.SQLDB())
	job, err := jobsRepo.Enqueue(ctx, jobs.EnqueueInput{ID: "agent:lease-fence", Kind: "agent.run", WorkloadClass: jobs.WorkloadClassBackground, PrincipalID: owner.ID, GroupIDs: []string{}, EstimatedMemoryBytes: 1, ResourceKind: "agent_run", ResourceID: "run_lease_fence", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	job, ok, err := jobsRepo.ClaimByID(ctx, job.ID, jobs.WorkloadClassBackground, "worker-a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim = %#v, %v", job, err)
	}
	if err := jobsRepo.Complete(ctx, job.ID, job.Fence()); err != nil {
		t.Fatal(err)
	}
	repo := NewRepositoryWithWorkflow(store.SQLDB(), jobsRepo, jobs.WorkflowRecorderFunc(func(context.Context, transaction.Transaction, jobs.WorkflowIntent) error { return nil }))
	run, err := repo.CreateRun(ctx, agent.RunInput{PrincipalID: owner.ID, ConversationID: conversation.ID, RunID: "run_lease_fence", Status: agent.RunStatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = repo.FinishRunWorkflow(ctx, agent.RunFinish{PrincipalID: owner.ID, ConversationID: conversation.ID, RunID: run.ID, Status: agent.RunStatusFailed, MetadataJSON: `{}`, JobID: job.ID, JobFence: job.Fence()}, jobs.WorkflowIntent{Event: jobs.EventInput{Key: "agent_run.failed:" + run.ID, ResourceKind: "agent_run", ResourceID: run.ID, EventType: "agent_run.failed", Data: []byte(`{}`)}})
	if err == nil {
		t.Fatal("stale lease unexpectedly finalized run")
	}
	current, err := repo.GetRun(ctx, owner.ID, conversation.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != agent.RunStatusRunning {
		t.Fatalf("status after stale lease = %q, want running", current.Status)
	}
}

func TestFinishRunWorkflowRejectsStaleClaimAfterReclaim(t *testing.T) {
	ctx := context.Background()
	store, base := openAgentRepo(t, ctx)
	owner := createAgentPrincipal(t, ctx, store, "lease-reclaim@example.com")
	conversation, err := base.CreateConversation(ctx, agent.ConversationInput{PrincipalID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	queue := jobsqlite.NewRepository(store.SQLDB())
	job, err := queue.Enqueue(ctx, jobs.EnqueueInput{ID: "agent:lease-reclaim", Kind: "agent.run", WorkloadClass: jobs.WorkloadClassBackground, PrincipalID: owner.ID, GroupIDs: []string{}, EstimatedMemoryBytes: 1, ResourceKind: "agent_run", ResourceID: "run_lease_reclaim", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	first, ok, err := queue.ClaimByID(ctx, job.ID, jobs.WorkloadClassBackground, "worker-a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("first claim = %#v ok=%v err=%v", first, ok, err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE api_async_jobs SET lease_expires_at = datetime('now', '-1 second') WHERE id = ?`, job.ID); err != nil {
		t.Fatal(err)
	}
	second, ok, err := queue.ClaimByID(ctx, job.ID, jobs.WorkloadClassBackground, "worker-b", time.Minute)
	if err != nil || !ok || second.LeaseGeneration == first.LeaseGeneration {
		t.Fatalf("reclaim = %#v ok=%v err=%v", second, ok, err)
	}
	repo := NewRepositoryWithWorkflow(store.SQLDB(), queue, queue)
	if _, err := repo.CreateRun(ctx, agent.RunInput{PrincipalID: owner.ID, ConversationID: conversation.ID, RunID: "run_lease_reclaim", Status: agent.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	workflow := jobs.WorkflowIntent{Event: jobs.EventInput{Key: "agent_run.failed:run_lease_reclaim", ResourceKind: "agent_run", ResourceID: "run_lease_reclaim", EventType: "agent_run.failed", Data: []byte(`{}`)}}
	finish := agent.RunFinish{PrincipalID: owner.ID, ConversationID: conversation.ID, RunID: "run_lease_reclaim", Status: agent.RunStatusFailed, MetadataJSON: `{}`}
	finish.JobID, finish.JobFence = first.ID, first.Fence()
	if _, _, err := repo.FinishRunWorkflow(ctx, finish, workflow); err == nil {
		t.Fatal("stale claim terminalized run")
	}
	current, err := repo.GetRun(ctx, owner.ID, conversation.ID, "run_lease_reclaim")
	if err != nil || current.Status != agent.RunStatusRunning {
		t.Fatalf("run after stale claim = %#v err=%v", current, err)
	}
}

func TestCancelRunWorkflowIsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	store, base := openAgentRepo(t, ctx)
	owner := createAgentPrincipal(t, ctx, store, "cancel-workflow@example.com")
	conversation, err := base.CreateConversation(ctx, agent.ConversationInput{PrincipalID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	jobsRepo := jobsqlite.NewRepository(store.SQLDB())
	repo := NewRepositoryWithWorkflow(store.SQLDB(), jobsRepo, jobsRepo)
	run, err := repo.CreateRun(ctx, agent.RunInput{PrincipalID: owner.ID, ConversationID: conversation.ID, RunID: "run_cancel_workflow", Status: agent.RunStatusPreparing})
	if err != nil {
		t.Fatal(err)
	}
	jobID := "agent:" + run.ID + ":run"
	if _, err := jobsRepo.Enqueue(ctx, jobs.EnqueueInput{ID: jobID, Kind: "agent.run", WorkloadClass: jobs.WorkloadClassBackground, PrincipalID: owner.ID, GroupIDs: []string{}, EstimatedMemoryBytes: 1, ResourceKind: "agent_run", ResourceID: run.ID, Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	workflow := jobs.WorkflowIntent{Event: jobs.EventInput{Key: "agent_run.canceled:" + run.ID, ResourceKind: "agent_run", ResourceID: run.ID, EventType: "agent_run.canceled", Data: []byte(`{"runId":"run_cancel_workflow"}`)}}
	finish := agent.RunFinish{PrincipalID: owner.ID, ConversationID: conversation.ID, RunID: run.ID, Status: agent.RunStatusCanceled, Error: context.Canceled.Error(), MetadataJSON: `{}`}
	if changed, err := repo.CancelRunWorkflow(ctx, finish, jobID, workflow); err != nil || !changed {
		t.Fatalf("first cancellation changed=%v err=%v", changed, err)
	}
	if changed, err := repo.CancelRunWorkflow(ctx, finish, jobID, workflow); err != nil || changed {
		t.Fatalf("replay cancellation changed=%v err=%v", changed, err)
	}
	current, err := repo.GetRun(ctx, owner.ID, conversation.ID, run.ID)
	if err != nil || current.Status != agent.RunStatusCanceled {
		t.Fatalf("run after cancellation = %#v err=%v", current, err)
	}
	job, err := jobsRepo.Get(ctx, jobID)
	if err != nil || job.Status != jobs.StatusCancelled {
		t.Fatalf("job after cancellation = %#v err=%v", job, err)
	}
	events, err := jobsRepo.ListEvents(ctx, "agent_run", run.ID, 0, 20)
	if err != nil || len(events) != 1 || events[0].EventType != "agent_run.canceled" {
		t.Fatalf("events after cancellation = %#v err=%v", events, err)
	}
}

func TestCancelRunWorkflowConcurrentReplayPublishesOneEvent(t *testing.T) {
	ctx := context.Background()
	store, base := openAgentRepo(t, ctx)
	owner := createAgentPrincipal(t, ctx, store, "cancel-concurrent@example.com")
	conversation, err := base.CreateConversation(ctx, agent.ConversationInput{PrincipalID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	queue := jobsqlite.NewRepository(store.SQLDB())
	repo := NewRepositoryWithWorkflow(store.SQLDB(), queue, queue)
	run, err := repo.CreateRun(ctx, agent.RunInput{PrincipalID: owner.ID, ConversationID: conversation.ID, RunID: "run_cancel_concurrent", Status: agent.RunStatusPreparing})
	if err != nil {
		t.Fatal(err)
	}
	jobID := "agent:" + run.ID + ":run"
	if _, err := queue.Enqueue(ctx, jobs.EnqueueInput{ID: jobID, Kind: "agent.run", WorkloadClass: jobs.WorkloadClassBackground, PrincipalID: owner.ID, GroupIDs: []string{}, EstimatedMemoryBytes: 1, ResourceKind: "agent_run", ResourceID: run.ID, Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	workflow := jobs.WorkflowIntent{Event: jobs.EventInput{Key: "agent_run.canceled:" + run.ID, ResourceKind: "agent_run", ResourceID: run.ID, EventType: "agent_run.canceled", Data: []byte(`{"runId":"run_cancel_concurrent"}`)}}
	finish := agent.RunFinish{PrincipalID: owner.ID, ConversationID: conversation.ID, RunID: run.ID, Status: agent.RunStatusCanceled, Error: context.Canceled.Error(), MetadataJSON: `{}`}
	results := make(chan bool, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			changed, callErr := repo.CancelRunWorkflow(ctx, finish, jobID, workflow)
			results <- changed
			errs <- callErr
		}()
	}
	changedCount := 0
	for i := 0; i < 8; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent cancellation error: %v", err)
		}
		if <-results {
			changedCount++
		}
	}
	if changedCount != 1 {
		t.Fatalf("changed count = %d, want one", changedCount)
	}
	events, err := queue.ListEvents(ctx, "agent_run", run.ID, 0, 20)
	if err != nil || len(events) != 1 {
		t.Fatalf("events = %#v err=%v, want one", events, err)
	}
}

func TestCancelRunWorkflowEventFailureRollsBackRunAndJob(t *testing.T) {
	ctx := context.Background()
	store, base := openAgentRepo(t, ctx)
	owner := createAgentPrincipal(t, ctx, store, "cancel-rollback@example.com")
	conversation, err := base.CreateConversation(ctx, agent.ConversationInput{PrincipalID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	queue := jobsqlite.NewRepository(store.SQLDB())
	repo := NewRepositoryWithWorkflow(store.SQLDB(), queue, jobs.WorkflowRecorderFunc(func(context.Context, transaction.Transaction, jobs.WorkflowIntent) error {
		return errors.New("event recorder unavailable")
	}))
	run, err := repo.CreateRun(ctx, agent.RunInput{PrincipalID: owner.ID, ConversationID: conversation.ID, RunID: "run_cancel_rollback", Status: agent.RunStatusPreparing})
	if err != nil {
		t.Fatal(err)
	}
	jobID := "agent:" + run.ID + ":run"
	if _, err := queue.Enqueue(ctx, jobs.EnqueueInput{ID: jobID, Kind: "agent.run", WorkloadClass: jobs.WorkloadClassBackground, PrincipalID: owner.ID, GroupIDs: []string{}, EstimatedMemoryBytes: 1, ResourceKind: "agent_run", ResourceID: run.ID, Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	workflow := jobs.WorkflowIntent{Event: jobs.EventInput{Key: "agent_run.canceled:" + run.ID, ResourceKind: "agent_run", ResourceID: run.ID, EventType: "agent_run.canceled", Data: []byte(`{}`)}}
	finish := agent.RunFinish{PrincipalID: owner.ID, ConversationID: conversation.ID, RunID: run.ID, Status: agent.RunStatusCanceled, Error: context.Canceled.Error(), MetadataJSON: `{}`}
	if changed, err := repo.CancelRunWorkflow(ctx, finish, jobID, workflow); err == nil || changed {
		t.Fatalf("cancellation with event failure changed=%v err=%v", changed, err)
	}
	current, err := repo.GetRun(ctx, owner.ID, conversation.ID, run.ID)
	if err != nil || current.Status != agent.RunStatusPreparing {
		t.Fatalf("run after rollback = %#v err=%v", current, err)
	}
	job, err := queue.Get(ctx, jobID)
	if err != nil || job.Status != jobs.StatusQueued {
		t.Fatalf("job after rollback = %#v err=%v", job, err)
	}
}

func TestCompleteRunWorkflowAtomicSuccessPersistsAllState(t *testing.T) {
	ctx := context.Background()
	store, base := openAgentRepo(t, ctx)
	owner := createAgentPrincipal(t, ctx, store, "complete-success@example.com")
	conv, err := base.CreateConversation(ctx, agent.ConversationInput{PrincipalID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	queue := jobsqlite.NewRepository(store.SQLDB())
	repo := NewRepositoryWithWorkflow(store.SQLDB(), queue, queue)
	run, err := repo.CreateRun(ctx, agent.RunInput{PrincipalID: owner.ID, ConversationID: conv.ID, RunID: "run_complete_success", Status: agent.RunStatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	job, err := queue.Enqueue(ctx, jobs.EnqueueInput{ID: "agent:" + run.ID + ":run", Kind: "agent.run", WorkloadClass: jobs.WorkloadClassBackground, PrincipalID: owner.ID, GroupIDs: []string{}, EstimatedMemoryBytes: 1, ResourceKind: "agent_run", ResourceID: run.ID, Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	job, ok, err := queue.ClaimByID(ctx, job.ID, jobs.WorkloadClassBackground, "worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim: %v", err)
	}
	msg := agent.MessageInput{PrincipalID: owner.ID, ConversationID: conv.ID, RunID: run.ID, Role: agent.MessageRoleAssistant, ContentText: "done", ContentJSON: `{"content":"done"}`}
	msg2 := msg
	msg2.Role = agent.MessageRoleTool
	msg2.ToolCallID = "call-2"
	msg2.ToolName = "lookup"
	workflow := jobs.WorkflowIntent{Event: jobs.EventInput{Key: "agent_run.completed:" + run.ID, ResourceKind: "agent_run", ResourceID: run.ID, EventType: "agent_run.completed", Data: []byte(`{"runId":"run_complete_success"}`)}}
	rows, changed, err := repo.CompleteRunWorkflow(ctx, agent.RunFinish{PrincipalID: owner.ID, ConversationID: conv.ID, RunID: run.ID, Status: agent.RunStatusCompleted, JobID: job.ID, JobFence: job.Fence(), MetadataJSON: `{}`}, []agent.MessageInput{msg, msg2}, `[{"role":"assistant","content":"done"}]`, workflow)
	if err != nil || !changed || len(rows) != 2 {
		t.Fatalf("complete rows=%d changed=%v err=%v", len(rows), changed, err)
	}
	got, _ := repo.GetRun(ctx, owner.ID, conv.ID, run.ID)
	if got.Status != agent.RunStatusCompleted {
		t.Fatalf("run status=%q", got.Status)
	}
	messages, _ := repo.ListMessages(ctx, owner.ID, conv.ID)
	if len(messages) != 2 {
		t.Fatalf("messages=%d", len(messages))
	}
	events, _ := queue.ListEvents(ctx, "agent_run", run.ID, 0, 10)
	if len(events) != 1 {
		t.Fatalf("events=%d", len(events))
	}
	if _, changed, err := repo.CompleteRunWorkflow(ctx, agent.RunFinish{PrincipalID: owner.ID, ConversationID: conv.ID, RunID: run.ID, Status: agent.RunStatusCompleted, JobID: job.ID, JobFence: job.Fence(), MetadataJSON: `{}`}, []agent.MessageInput{msg}, `[]`, workflow); err != nil || changed {
		t.Fatalf("terminal replay changed=%v err=%v", changed, err)
	}
}

func TestCompleteRunWorkflowFailureRollsBackAndRejectsStaleBinding(t *testing.T) {
	ctx := context.Background()
	store, base := openAgentRepo(t, ctx)
	owner := createAgentPrincipal(t, ctx, store, "complete-rollback@example.com")
	conv, err := base.CreateConversation(ctx, agent.ConversationInput{PrincipalID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	queue := jobsqlite.NewRepository(store.SQLDB())
	failing := jobs.WorkflowRecorderFunc(func(context.Context, transaction.Transaction, jobs.WorkflowIntent) error {
		return errors.New("event failed")
	})
	repo := NewRepositoryWithWorkflow(store.SQLDB(), queue, failing)
	run, err := repo.CreateRun(ctx, agent.RunInput{PrincipalID: owner.ID, ConversationID: conv.ID, RunID: "run_complete_rollback", Status: agent.RunStatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	job, err := queue.Enqueue(ctx, jobs.EnqueueInput{ID: "agent:" + run.ID + ":run", Kind: "agent.run", WorkloadClass: jobs.WorkloadClassBackground, PrincipalID: owner.ID, GroupIDs: []string{}, EstimatedMemoryBytes: 1, ResourceKind: "agent_run", ResourceID: run.ID, Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	job, _, _ = queue.ClaimByID(ctx, job.ID, jobs.WorkloadClassBackground, "worker", time.Minute)
	msg := agent.MessageInput{PrincipalID: owner.ID, ConversationID: conv.ID, RunID: run.ID, Role: agent.MessageRoleAssistant, ContentText: "done", ContentJSON: `{"content":"done"}`}
	workflow := jobs.WorkflowIntent{Event: jobs.EventInput{Key: "agent_run.completed:" + run.ID, ResourceKind: "agent_run", ResourceID: run.ID, EventType: "agent_run.completed", Data: []byte(`{}`)}}
	if _, _, err := repo.CompleteRunWorkflow(ctx, agent.RunFinish{PrincipalID: owner.ID, ConversationID: conv.ID, RunID: run.ID, Status: agent.RunStatusCompleted, JobID: job.ID, JobFence: job.Fence(), MetadataJSON: `{}`}, []agent.MessageInput{msg}, `[{"role":"assistant"}]`, workflow); err == nil {
		t.Fatal("expected workflow failure")
	}
	got, _ := repo.GetRun(ctx, owner.ID, conv.ID, run.ID)
	if got.Status != agent.RunStatusRunning {
		t.Fatalf("run status=%q", got.Status)
	}
	messages, _ := repo.ListMessages(ctx, owner.ID, conv.ID)
	if len(messages) != 0 {
		t.Fatalf("messages=%d", len(messages))
	}
	// Binding mismatch is rejected before any write.
	bad := msg
	bad.RunID = "other-run"
	if _, _, err := repo.CompleteRunWorkflow(ctx, agent.RunFinish{PrincipalID: owner.ID, ConversationID: conv.ID, RunID: run.ID, Status: agent.RunStatusCompleted, JobID: job.ID, JobFence: job.Fence(), MetadataJSON: `{}`}, []agent.MessageInput{bad}, `[]`, workflow); err == nil {
		t.Fatal("expected binding mismatch")
	}
}

func TestRepositoryRejectsInvalidJSON(t *testing.T) {
	ctx := context.Background()
	store, repo := openAgentRepo(t, ctx)
	principal := createAgentPrincipal(t, ctx, store, "owner@example.com")

	if _, err := repo.CreateConversation(ctx, agent.ConversationInput{
		PrincipalID:  principal.ID,
		Title:        "Bad metadata",
		MetadataJSON: `{`,
	}); err == nil {
		t.Fatal("CreateConversation accepted invalid metadata JSON")
	}
	conversation, err := repo.CreateConversation(ctx, agent.ConversationInput{
		PrincipalID: principal.ID,
		Title:       "Good chat",
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if _, err := repo.AppendMessage(ctx, agent.MessageInput{
		PrincipalID:    principal.ID,
		ConversationID: conversation.ID,
		Role:           agent.MessageRoleUser,
		ContentJSON:    `{`,
	}); err == nil {
		t.Fatal("AppendMessage accepted invalid content JSON")
	}
	if _, err := repo.UpdateConversationTranscript(ctx, principal.ID, conversation.ID, `{}`); err == nil {
		t.Fatal("UpdateConversationTranscript accepted non-array transcript JSON")
	}
}

func openAgentRepo(t *testing.T, ctx context.Context) (*platform.Store, *Repository) {
	t.Helper()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := projectsqlite.NewRepository(store.SQLDB()).Ensure(ctx, projectsqlite.EnsureInput{ID: "test", Title: "Test"}); err != nil {
		t.Fatalf("ensure project: %v", err)
	}
	return store, NewRepositoryWithEvents(store.SQLDB(), jobsqlite.NewRepository(store.SQLDB()))
}

func createAgentPrincipal(t *testing.T, ctx context.Context, store *platform.Store, email string) access.Principal {
	t.Helper()
	repo := accesssqlite.NewRepository(store.SQLDB())
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Email: email, DisplayName: email})
	if err != nil {
		t.Fatalf("upsert principal %s: %v", email, err)
	}
	return principal
}
