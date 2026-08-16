package module

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/platform/jobs"
)

type JobStore interface {
	jobs.Enqueuer
	jobs.EventAppender
	jobs.Canceller
}

func (m *Module) EnqueueRun(ctx context.Context, scope agent.Scope, started *agent.StartedPrompt) error {
	return m.enqueueRun(ctx, scope, started, "")
}

func (m *Module) EnqueueChatRun(ctx context.Context, scope agent.Scope, started *agent.StartedPrompt, clientID string) error {
	return m.enqueueRun(ctx, scope, started, clientID)
}

func (m *Module) enqueueRun(ctx context.Context, scope agent.Scope, started *agent.StartedPrompt, chatClientID string) error {
	if m == nil || started == nil {
		return errors.New("agent run queue is unavailable")
	}
	if started.DurablyQueued() {
		return nil
	}
	return errors.New("transactional agent run workflow is unavailable")
}

func (m *Module) runWorkflow(input agent.PromptInput, runID string, dispatch agent.PromptDispatch) jobs.WorkflowIntent {
	execution := m.runExecution
	scope := input.Scope
	payload, _ := json.Marshal(RunJob{
		Scope: scope, Conversation: input.ConversationID, Run: runID, CorrelationID: input.CorrelationID,
		ChatClientID: dispatch.ChatClientID,
	})
	event, _ := json.Marshal(map[string]any{
		"runId": runID, "conversationId": input.ConversationID, "status": execution.InitialState,
	})
	return jobs.WorkflowIntent{
		Event: jobs.EventInput{
			Key: execution.InitialEvent, ResourceKind: execution.ResourceKind, ResourceID: runID,
			EventType: execution.InitialEvent, Data: event,
		},
		Job: jobs.EnqueueInput{
			ID: "agent:" + runID + ":run", Kind: execution.JobKind,
			WorkloadClass: m.runWorkloadClass, PrincipalID: scope.PrincipalID, GroupIDs: append([]string(nil), scope.GroupIDs...),
			ResourceKind: execution.ResourceKind, ResourceID: runID, EstimatedMemoryBytes: 64 << 20, Payload: payload,
		},
	}
}

func (m *Module) CancelQueuedRun(ctx context.Context, scope agent.Scope, conversationID, runID string) (bool, error) {
	if m == nil {
		return false, errors.New("agent run queue is unavailable")
	}
	if m.service == nil {
		return false, errors.New("agent service is unavailable")
	}
	data, _ := json.Marshal(map[string]any{"runId": runID, "conversationId": conversationID})
	workflow := jobs.WorkflowIntent{Event: jobs.EventInput{
		Key: "agent_run.canceled:" + runID, ResourceKind: m.runExecution.ResourceKind, ResourceID: runID,
		EventType: "agent_run.canceled", Data: data,
	}}
	if m.service.SupportsCancellationWorkflow() {
		// An atomic adapter owns rollback. Never cancel the queue separately
		// after it reports an error, or a failed event write could leave the
		// domain and job out of sync.
		return m.service.CancelPersistedRunWithWorkflow(ctx, scope, conversationID, runID, workflow)
	}
	// Adapters without the capability retain the legacy two-step behavior.
	transactional, err := m.service.CancelPersistedRunWithWorkflow(ctx, scope, conversationID, runID, workflow)
	if err == nil && transactional {
		return true, nil
	}
	cancelled, cancelErr := jobs.CancelQueued(ctx, m.jobs, "agent:"+runID+":run")
	if cancelErr != nil {
		return false, cancelErr
	}
	if !cancelled {
		return err == nil, err
	}
	if err != nil {
		if _, fallbackErr := m.service.CancelPersistedRunWithWorkflow(ctx, scope, conversationID, runID, workflow); fallbackErr != nil {
			return false, fallbackErr
		}
	}
	_ = jobs.AppendJSONEvent(ctx, m.jobs, m.runExecution.ResourceKind, runID, "agent_run.canceled", map[string]any{"runId": runID, "conversationId": conversationID})
	return true, nil
}
