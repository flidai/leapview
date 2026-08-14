package module

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/platform/jobs"
)

type runJobStore struct {
	input jobs.EnqueueInput
	event jobs.Event
}

func (s *runJobStore) Enqueue(_ context.Context, input jobs.EnqueueInput) (jobs.Job, error) {
	s.input = input
	return jobs.Job{ID: input.ID}, nil
}

func (s *runJobStore) AppendEvent(_ context.Context, kind, id, eventType string, data []byte) (jobs.Event, error) {
	s.event = jobs.Event{ResourceKind: kind, ResourceID: id, EventType: eventType, Data: data}
	return s.event, nil
}

func (s *runJobStore) Cancel(context.Context, string) error { return nil }

func TestEnqueueRunRejectsNonTransactionalFallback(t *testing.T) {
	store := &runJobStore{}
	module, err := Build(t.Context(), Config{Jobs: store})
	if err != nil {
		t.Fatal(err)
	}
	started := &agent.StartedPrompt{ConversationID: "conversation-1", RunID: "run-1", CorrelationID: "correlation-1"}
	scope := agent.Scope{Credential: agent.CredentialScope{WorkspaceID: "credential-workspace"}}
	if err := module.EnqueueRun(t.Context(), scope, started); err == nil {
		t.Fatal("non-transactional enqueue fallback was accepted")
	}
	if store.input.ID != "" || store.event.ID != 0 {
		t.Fatalf("non-transactional workflow wrote job %#v or event %#v", store.input, store.event)
	}
}

func TestEnqueueChatRunRejectsNonTransactionalFallback(t *testing.T) {
	store := &runJobStore{}
	module, err := Build(t.Context(), Config{Jobs: store})
	if err != nil {
		t.Fatal(err)
	}
	started := &agent.StartedPrompt{ConversationID: "conversation-1", RunID: "run-1"}
	if err := module.EnqueueChatRun(t.Context(), agent.Scope{}, started, "browser-1"); err == nil {
		t.Fatal("non-transactional chat enqueue fallback was accepted")
	}
}

func TestRunWorkflowPersistsBrowserDeliveryAtomically(t *testing.T) {
	module, err := Build(t.Context(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	intent := module.runWorkflow(
		agent.PromptInput{ConversationID: "conversation-1"},
		"run-1",
		agent.PromptDispatch{ChatClientID: "browser-1"},
	)
	var payload RunJob
	if err := json.Unmarshal(intent.Job.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ChatClientID != "browser-1" || payload.Conversation != "conversation-1" || payload.Run != "run-1" {
		t.Fatalf("workflow payload = %#v", payload)
	}
	if intent.Job.Kind != module.runExecution.JobKind || intent.Job.ResourceKind != module.runExecution.ResourceKind || intent.Event.EventType != module.runExecution.InitialEvent {
		t.Fatalf("workflow contract fields = job %#v event %#v", intent.Job, intent.Event)
	}
}

func TestBuildConstructsOwnedHTTPHandler(t *testing.T) {
	module, err := Build(t.Context(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if module.HTTP() == nil {
		t.Fatal("expected agent module to construct its HTTP handler")
	}
}

func TestRunWorkspaceUsesExplicitScopeThenCredentialThenGlobal(t *testing.T) {
	if got := runWorkspaceID(agent.Scope{WorkspaceID: "scope", Credential: agent.CredentialScope{WorkspaceID: "credential"}}, "_global"); got != "scope" {
		t.Fatalf("scope workspace = %q", got)
	}
	if got := runWorkspaceID(agent.Scope{Credential: agent.CredentialScope{WorkspaceID: "credential"}}, "_global"); got != "credential" {
		t.Fatalf("credential workspace = %q", got)
	}
	if got := runWorkspaceID(agent.Scope{}, "_global"); got != "_global" {
		t.Fatalf("global workspace = %q", got)
	}
}
