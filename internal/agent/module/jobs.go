package module

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/agent"
	agenthttp "github.com/flidai/leapview/internal/agent/http"
	"github.com/flidai/leapview/internal/agent/ui"
	"github.com/flidai/leapview/internal/platform/jobs"
)

type RunJob struct {
	Scope                            agent.Scope
	Conversation, Run, CorrelationID string
	ChatClientID                     string
}

func boundedResumeError(err error) error {
	if err == nil {
		return fmt.Errorf("durable prompt resume failed")
	}
	// Keep provider/store internals (and prompt material) out of persisted
	// failure metadata. The original error remains available to local logs.
	return fmt.Errorf("durable prompt resume failed")
}

func (m *Module) JobHandlers(events jobs.EventAppender) []jobs.Handler {
	execution := m.runExecution
	return []jobs.Handler{jobs.HandlerFunc{JobKind: execution.JobKind, Run: func(ctx context.Context, job jobs.Job) error {
		var payload RunJob
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return err
		}
		if job.Kind != execution.JobKind || job.ResourceKind != execution.ResourceKind ||
			strings.TrimSpace(job.ResourceID) == "" || job.ResourceID != payload.Run ||
			strings.TrimSpace(payload.Conversation) == "" || strings.TrimSpace(payload.Run) == "" {
			return fmt.Errorf("agent run job payload does not match its resource binding")
		}
		if m.service == nil {
			return fmt.Errorf("agent service is unavailable")
		}
		// A worker may crash after the domain terminal transition commits but
		// before the generic runner marks the queue job complete. Re-delivery
		// must converge to that durable outcome instead of attempting ResumePrompt
		// against a terminal run.
		if existing, getErr := m.service.GetRun(ctx, payload.Scope, payload.Conversation, payload.Run); getErr == nil {
			switch existing.Status {
			case agent.RunStatusCompleted:
				return nil
			case agent.RunStatusFailed:
				return fmt.Errorf("agent run already failed")
			case agent.RunStatusCanceled:
				// A worker can die after the capability atomically terminalizes
				// the run/event but before the generic runner updates the queue
				// row. Preserve the domain cancellation on redelivery by
				// converging the exact claimed job to cancelled; returning nil
				// would otherwise make the runner try Complete (and silently
				// leave a running row when that fence is stale).
				if store, ok := events.(interface {
					CancelClaimed(context.Context, string, jobs.Fence) error
				}); ok {
					if err := store.CancelClaimed(context.WithoutCancel(ctx), job.ID, job.Fence()); err == nil || errors.Is(err, jobs.ErrConflict) {
						return nil
					}
				}
				return fmt.Errorf("agent run already canceled")
			}
		}
		started, err := m.service.ResumePrompt(ctx, payload.Scope, payload.Conversation, payload.Run, payload.CorrelationID)
		if err != nil {
			// A canceled worker context is infrastructure cancellation (lease
			// loss/shutdown), not a domain resume failure. Leave both records
			// recoverable for the next claim.
			if ctx.Err() != nil {
				return err
			}
			if store, ok := events.(jobs.Repository); ok {
				claimed, claimErr := store.Get(ctx, job.ID)
				if claimErr != nil || claimed.Fence() != job.Fence() || claimed.Status != jobs.StatusRunning {
					return fmt.Errorf("stale durable job claim")
				}
			}
			data, _ := json.Marshal(map[string]any{"runId": payload.Run, "conversationId": payload.Conversation})
			workflow := jobs.WorkflowIntent{Event: jobs.EventInput{Key: "agent_run.failed:" + payload.Run, ResourceKind: execution.ResourceKind, ResourceID: payload.Run, EventType: "agent_run.failed", Data: data}}
			transitioned, recoveryErr := m.service.FinalizePersistedRunFailureWithClaim(context.WithoutCancel(ctx), payload.Scope, payload.Conversation, payload.Run, boundedResumeError(err), workflow, job.ID, job.Fence())
			_ = transitioned // publication is part of FinishRunWorkflow's transaction
			if recoveryErr != nil {
				_ = recoveryErr // retain only local diagnostics; never persist storage details
				return boundedResumeError(err)
			}
			return err
		}
		started.SetDurableClaim(job.ID, job.Fence())
		if payload.ChatClientID == "" {
			_, err = started.Complete(ctx, nil)
		} else {
			_, err = m.executeStartedChatTurn(ctx, m.service, payload.Scope, started, agenthttp.ChatTurnExecution{
				EmitInitialRunning: true,
				GenerateTitle:      true,
				ClientID:           payload.ChatClientID,
				Emit: func(signal ui.ChatViewState) error {
					if m.broker != nil {
						m.broker.Publish(ChatStreamID(payload.Scope, payload.ChatClientID), chatSignalPatch(signal))
					}
					return nil
				},
			})
		}
		return err
	}}}
}
