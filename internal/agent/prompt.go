package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	agentcore "github.com/flidai/leapview/pkg/agent"
	"github.com/flidai/leapview/pkg/jobs"
)

type PromptInput struct {
	Scope          Scope
	ConversationID string
	Input          string
	Context        *TurnContext
	CorrelationID  string
	RequestID      string
	OnEvent        func(EventEnvelope)
}

// PromptDispatch describes delivery metadata persisted with a durable prompt.
type PromptDispatch struct {
	ChatClientID string
}

type PromptResult struct {
	ConversationID string               `json:"conversationId"`
	RunID          string               `json:"runId"`
	StopReason     agentcore.StopReason `json:"stopReason"`
	Content        string               `json:"content"`
}

type StartedPrompt struct {
	Scope          Scope
	ConversationID string
	RunID          string
	Input          string
	CorrelationID  string
	RequestID      string

	service       *Service
	systemPrompt  string
	initial       []agentcore.Message
	runContext    context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	closed        bool
	durablyQueued bool
	claimID       string
	claimFence    jobs.Fence
}

func promptDigest(input PromptInput) string {
	// The idempotency key identifies one complete request, not merely its
	// visible text. Context/correlation changes must therefore conflict with
	// an existing run rather than silently reusing it.
	payload, _ := json.Marshal(struct {
		Input         string       `json:"input"`
		Context       *TurnContext `json:"context,omitempty"`
		CorrelationID string       `json:"correlationId,omitempty"`
	}{input.Input, input.Context, input.CorrelationID})
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}

func (p *StartedPrompt) DurablyQueued() bool { return p != nil && p.durablyQueued }

func (p *StartedPrompt) SetDurableClaim(jobID string, fence jobs.Fence) {
	if p != nil {
		p.claimID, p.claimFence = jobID, fence
	}
}

func (s *Service) Prompt(ctx context.Context, input PromptInput) (PromptResult, error) {
	started, err := s.StartPrompt(ctx, input)
	if err != nil {
		return PromptResult{}, err
	}
	return started.Complete(ctx, input.OnEvent)
}

func (s *Service) StartPrompt(ctx context.Context, input PromptInput) (*StartedPrompt, error) {
	return s.startPrompt(ctx, input, nil)
}

// StartDurablePrompt persists the prompt and its required workflow as one
// transition when a workflow recorder is configured. Callers with an external
// service can inspect DurablyQueued and enqueue the returned prompt themselves.
func (s *Service) StartDurablePrompt(ctx context.Context, input PromptInput, dispatch PromptDispatch) (*StartedPrompt, error) {
	return s.startPrompt(ctx, input, &dispatch)
}

func (s *Service) startPrompt(ctx context.Context, input PromptInput, dispatch *PromptDispatch) (*StartedPrompt, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	if s.repo == nil {
		return nil, fmt.Errorf("agent store is required")
	}
	if strings.TrimSpace(input.Input) == "" {
		return nil, fmt.Errorf("prompt input is required")
	}
	toolScope := input.Scope
	toolScope.ConversationID = input.ConversationID
	if err := s.acquire(input.ConversationID); err != nil {
		return nil, err
	}
	release := true
	defer func() {
		if release {
			s.release(input.ConversationID)
		}
	}()

	conversation, err := s.repo.GetConversation(ctx, input.Scope.PrincipalID, input.ConversationID)
	if err != nil {
		return nil, err
	}
	if conversation.Status == ConversationStatusArchived {
		return nil, ErrConversationArchived
	}
	initial, err := decodeTranscript(conversation.TranscriptJSON)
	if err != nil {
		return nil, err
	}
	systemPrompt, err := s.systemPrompt(ctx)
	if err != nil {
		return nil, err
	}
	durable := dispatch != nil && s.promptWorkflow != nil
	runID := newID("run")
	if durable && strings.TrimSpace(input.RequestID) != "" {
		hash := sha256.Sum256([]byte(input.Scope.PrincipalID + "\x00" + input.ConversationID + "\x00" + input.RequestID))
		runID = "run_" + hex.EncodeToString(hash[:12])
	}
	runStatus := RunStatusRunning
	if durable {
		runStatus = RunStatusPreparing
	}
	run, err := s.repo.CreateRun(ctx, RunInput{
		PrincipalID:    input.Scope.PrincipalID,
		ConversationID: input.ConversationID,
		RunID:          runID,
		Model:          s.config.Model,
		MetadataJSON:   metadataJSON(map[string]any{"base_url": s.config.NormalizedBaseURL(), "model": s.config.Model, "request_id": input.RequestID, "request_digest": promptDigest(input)}),
		Status:         runStatus,
	})
	if err != nil {
		if durable && strings.TrimSpace(input.RequestID) != "" {
			if existing, getErr := s.repo.GetRun(ctx, input.Scope.PrincipalID, input.ConversationID, runID); getErr == nil {
				var metadata map[string]any
				if json.Unmarshal([]byte(existing.MetadataJSON), &metadata) == nil {
					if digest, _ := metadata["request_digest"].(string); digest != "" && digest != promptDigest(input) {
						return nil, ErrRequestConflict
					}
				}
				if existing.Status != RunStatusPreparing && existing.Status != RunStatusRunning {
					return nil, ErrRequestConflict
				}
				// Repair any missing prepare steps idempotently before activating.
				stored, _ := s.repo.ListMessages(ctx, input.Scope.PrincipalID, input.ConversationID)
				messagePersisted := false
				for _, message := range stored {
					if message.RunID == runID && message.Role == MessageRoleUser && message.ContentText == input.Input {
						messagePersisted = true
						break
					}
				}
				transcript, transcriptErr := decodeTranscript(conversation.TranscriptJSON)
				if transcriptErr != nil {
					return nil, transcriptErr
				}
				systemPrompt, promptErr := s.systemPrompt(ctx)
				if promptErr != nil {
					return nil, promptErr
				}
				prepared, prepErr := agentcore.New(agentcore.Definition{Name: "leapview-governed", SystemPrompt: systemPrompt, Model: s.model, Tools: s.toolDefinitions(toolScope), InitialTranscript: transcript, IDGenerator: fixedRunIDGenerator{runID: runID}})
				if prepErr != nil {
					return nil, prepErr
				}
				lastUser, hasPrompt := lastVisibleUserMessage(transcript)
				// A previous run may have submitted identical text. Only a
				// message already bound to this run proves that prompt
				// preparation committed; otherwise prepare a fresh message.
				if !messagePersisted || !hasPrompt || lastUser.Content != input.Input {
					if prepErr = prepared.PreparePrompt(agentcore.PromptRequest{Input: input.Input, Context: turnContextItems(input.Context)}); prepErr != nil {
						return nil, prepErr
					}
				}
				transcript = prepared.Transcript()
				if !messagePersisted {
					if userMessage, ok := lastVisibleUserMessage(transcript); ok {
						if appendErr := s.appendMessage(ctx, input, runID, userMessage); appendErr != nil {
							return nil, appendErr
						}
					}
				}
				if persistErr := s.persistTranscript(ctx, input, transcript); persistErr != nil {
					return nil, persistErr
				}
				if existing.Status == RunStatusPreparing {
					unit, ok := s.repo.(RunWorkflowUnitOfWork)
					if !ok {
						return nil, ErrRequestConflict
					}
					workflow := s.promptWorkflow(input, runID, *dispatch)
					var activateErr error
					if intent, present := AuditIntentFromContext(ctx); present {
						if audited, ok := s.repo.(RunWorkflowAuditUnitOfWork); ok {
							_, activateErr = audited.ActivateRunWorkflowWithAudit(ctx, input.Scope.PrincipalID, input.ConversationID, runID, workflow, &intent)
						} else {
							_, activateErr = unit.ActivateRunWorkflow(ctx, input.Scope.PrincipalID, input.ConversationID, runID, workflow)
						}
					} else {
						_, activateErr = unit.ActivateRunWorkflow(ctx, input.Scope.PrincipalID, input.ConversationID, runID, workflow)
					}
					if activateErr != nil {
						return nil, activateErr
					}
				}
				runContext, cancel := context.WithCancel(context.Background())
				s.attachRun(input.ConversationID, runID, cancel)
				release = false
				return &StartedPrompt{Scope: input.Scope, ConversationID: input.ConversationID, RunID: runID, Input: input.Input, CorrelationID: input.CorrelationID, RequestID: input.RequestID, service: s, systemPrompt: systemPrompt, initial: transcript, runContext: runContext, cancel: cancel, durablyQueued: true}, nil
			}
		}
		return nil, err
	}
	prepared, err := agentcore.New(agentcore.Definition{
		Name:              "leapview-governed",
		SystemPrompt:      systemPrompt,
		Model:             s.model,
		Tools:             s.toolDefinitions(toolScope),
		InitialTranscript: initial,
		IDGenerator:       fixedRunIDGenerator{runID: run.ID},
	})
	if err != nil {
		return s.startFailure(ctx, input, run.ID, err)
	}
	if err := prepared.PreparePrompt(agentcore.PromptRequest{
		Input:   input.Input,
		Context: turnContextItems(input.Context),
	}); err != nil {
		return s.startFailure(ctx, input, run.ID, err)
	}
	initial = prepared.Transcript()
	userMessage, ok := lastVisibleUserMessage(initial)
	if !ok {
		err := fmt.Errorf("prepared transcript has no user prompt")
		return s.startFailure(ctx, input, run.ID, err)
	}
	if err := s.appendMessage(ctx, PromptInput{
		Scope:          input.Scope,
		ConversationID: input.ConversationID,
		Context:        input.Context,
	}, run.ID, userMessage); err != nil {
		return s.startFailure(ctx, input, run.ID, err)
	}
	if err := s.persistTranscript(ctx, input, initial); err != nil {
		return s.startFailure(ctx, input, run.ID, err)
	}
	durablyQueued := false
	if durable {
		unit, ok := s.repo.(RunWorkflowUnitOfWork)
		if !ok {
			err := fmt.Errorf("agent run workflow unit of work is unavailable")
			return s.startFailure(ctx, input, run.ID, err)
		}
		workflow := s.promptWorkflow(input, run.ID, *dispatch)
		if intent, present := AuditIntentFromContext(ctx); present {
			if audited, ok := s.repo.(RunWorkflowAuditUnitOfWork); ok {
				run, err = audited.ActivateRunWorkflowWithAudit(ctx, input.Scope.PrincipalID, input.ConversationID, run.ID, workflow, &intent)
			} else {
				run, err = unit.ActivateRunWorkflow(ctx, input.Scope.PrincipalID, input.ConversationID, run.ID, workflow)
			}
		} else {
			run, err = unit.ActivateRunWorkflow(ctx, input.Scope.PrincipalID, input.ConversationID, run.ID, workflow)
		}
		if err != nil {
			return s.startFailure(ctx, input, run.ID, err)
		}
		durablyQueued = true
	}
	runContext, cancel := context.WithCancel(context.Background())
	s.attachRun(input.ConversationID, run.ID, cancel)
	release = false
	return &StartedPrompt{
		Scope:          input.Scope,
		ConversationID: input.ConversationID,
		RunID:          run.ID,
		Input:          input.Input,
		CorrelationID:  input.CorrelationID,
		RequestID:      input.RequestID,
		service:        s,
		systemPrompt:   systemPrompt,
		initial:        initial,
		runContext:     runContext,
		cancel:         cancel,
		durablyQueued:  durablyQueued,
	}, nil
}

// ResumePrompt reconstructs an already-persisted running prompt for a durable
// worker after process restart. StartPrompt persists the run, user message, and
// transcript before it returns, so no request body or in-memory closure is
// required to continue execution.
func (s *Service) ResumePrompt(ctx context.Context, scope Scope, conversationID, runID, correlationID string) (*StartedPrompt, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	if s.repo == nil {
		return nil, fmt.Errorf("agent store is required")
	}
	conversationID, runID = strings.TrimSpace(conversationID), strings.TrimSpace(runID)
	if conversationID == "" || runID == "" {
		return nil, fmt.Errorf("conversation and run are required")
	}
	if err := s.acquireForResume(conversationID, runID); err != nil {
		return nil, err
	}
	release := true
	defer func() {
		if release {
			s.release(conversationID)
		}
	}()
	run, err := s.repo.GetRun(ctx, scope.PrincipalID, conversationID, runID)
	if err != nil {
		return nil, err
	}
	if run.Status != RunStatusRunning {
		return nil, fmt.Errorf("run %q is not resumable from status %q", runID, run.Status)
	}
	conversation, err := s.repo.GetConversation(ctx, scope.PrincipalID, conversationID)
	if err != nil {
		return nil, err
	}
	initial, err := decodeTranscript(conversation.TranscriptJSON)
	if err != nil {
		return nil, err
	}
	input := ""
	for index := len(initial) - 1; index >= 0; index-- {
		if initial[index].Role == agentcore.RoleUser && initial[index].Kind != agentcore.MessageKindExternalContext {
			input = strings.TrimSpace(initial[index].Content)
			break
		}
	}
	if input == "" {
		return nil, fmt.Errorf("persisted run has no user prompt")
	}
	systemPrompt, err := s.systemPrompt(ctx)
	if err != nil {
		return nil, err
	}
	runContext, cancel := context.WithCancel(ctx)
	s.attachRun(conversationID, runID, cancel)
	release = false
	return &StartedPrompt{Scope: scope, ConversationID: conversationID, RunID: runID, Input: input, CorrelationID: correlationID, service: s, systemPrompt: systemPrompt, initial: initial, runContext: runContext, cancel: cancel, durablyQueued: true}, nil
}

func (s *Service) acquireForResume(conversationID, runID string) error {
	s.mu.Lock()
	if active, ok := s.running[conversationID]; ok {
		if active.runID != runID {
			s.mu.Unlock()
			return ErrBusy
		}
		if active.cancel != nil {
			active.cancel()
		}
		delete(s.running, conversationID)
	}
	s.mu.Unlock()
	return s.acquire(conversationID)
}

func (s *Service) CompletePrompt(ctx context.Context, started *StartedPrompt, onEvent func(EventEnvelope)) (PromptResult, error) {
	if started == nil {
		return PromptResult{}, fmt.Errorf("started prompt is required")
	}
	return started.Complete(ctx, onEvent)
}

func (p *StartedPrompt) Complete(ctx context.Context, onEvent func(EventEnvelope)) (PromptResult, error) {
	if err := p.claim(); err != nil {
		return PromptResult{}, err
	}
	defer p.release()
	executionContext := p.runContext
	if executionContext == nil {
		executionContext = ctx
	}
	if p.cancel != nil {
		stop := context.AfterFunc(ctx, p.cancel)
		defer stop()
	}
	if p.durablyQueued && ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return PromptResult{}, ctx.Err()
	}
	s := p.service
	input := PromptInput{
		Scope:          p.Scope,
		ConversationID: p.ConversationID,
		Input:          p.Input,
		CorrelationID:  p.CorrelationID,
		OnEvent:        onEvent,
	}
	toolScope := input.Scope
	toolScope.ConversationID = input.ConversationID

	sink := &storeEventSink{repo: s.repo, scope: input.Scope, conversationID: input.ConversationID, runID: p.RunID, onEvent: input.OnEvent}
	def := agentcore.Definition{
		Name:              "leapview-governed",
		SystemPrompt:      p.systemPrompt,
		Model:             s.model,
		Tools:             s.toolDefinitions(toolScope),
		InitialTranscript: p.initial,
		Events:            sink,
		IDGenerator:       fixedRunIDGenerator{runID: p.RunID},
	}
	harness, err := agentcore.New(def)
	if err != nil {
		finishCtx := context.WithoutCancel(executionContext)
		var finishErr error
		if p.claimID != "" {
			finishErr = s.finishRunWithClaim(finishCtx, input, p.RunID, RunStatusFailed, "", sink.usage, err, p.claimID, p.claimFence)
		} else {
			finishErr = s.finishRun(finishCtx, input, p.RunID, RunStatusFailed, "", sink.usage, err)
		}
		if finishErr != nil {
			return PromptResult{}, errors.Join(err, finishErr)
		}
		return PromptResult{}, err
	}
	result, promptErr := harness.RunPreparedPrompt(executionContext, agentcore.PreparedPromptRequest{CorrelationID: input.CorrelationID})
	// A durable worker losing its lease or being shut down must leave the
	// domain run recoverable. The queue runner intentionally retains the job;
	// a later worker will resume it. Explicit user cancellation cancels only
	// runContext while the handler context remains live and therefore still
	// terminalizes below.
	if p.durablyQueued && ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return PromptResult{}, ctx.Err()
	}
	if p.claimID != "" && s.runWorkflowAvailable() {
		if verifier, ok := s.repo.(RunLeaseVerifier); ok {
			verifyCtx := ctx
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				verifyCtx = context.WithoutCancel(ctx)
			}
			if err := verifier.VerifyRunLease(verifyCtx, p.RunID, p.claimID, p.claimFence); err != nil {
				return PromptResult{}, err
			}
		}
	}
	transcript := harness.Transcript()
	status := RunStatusCompleted
	if promptErr != nil {
		status = RunStatusFailed
		// A cancellation of the prompt's own execution context is an explicit
		// user abort. Provider-side cancellation and deadline errors leave the
		// execution context live and therefore remain failed with their cause.
		if errors.Is(promptErr, context.Canceled) && executionContext.Err() != nil {
			status = RunStatusCanceled
		}
	}
	cause := RunTerminationCause("")
	if errors.Is(promptErr, context.Canceled) {
		if executionContext.Err() != nil {
			cause = RunCauseUserCanceled
		} else {
			cause = RunCauseProviderCanceled
		}
	} else if errors.Is(promptErr, context.DeadlineExceeded) {
		cause = RunCauseDeadlineExceeded
	}
	atomicCompletion := false
	if p.claimID != "" && s.runWorkflowAvailable() {
		if completion, ok := s.repo.(RunCompletionWorkflow); ok {
			atomicCompletion = true
			messages := newMessageInputs(input, p.RunID, p.initial, transcript)
			raw, _ := json.Marshal(compactTranscriptForStorage(transcript))
			meta := map[string]any{"model": s.config.Model}
			eventData := map[string]any{"runId": p.RunID, "conversationId": input.ConversationID}
			if cause != "" {
				meta["terminationCause"] = cause
				eventData["terminationCause"] = cause
			}
			encodedEventData, _ := json.Marshal(eventData)
			finishInput := RunFinish{PrincipalID: input.Scope.PrincipalID, ConversationID: input.ConversationID, RunID: p.RunID, Status: status, StopReason: string(result.StopReason), InputTokens: int64(sink.usage.InputTokens), OutputTokens: int64(sink.usage.OutputTokens), TotalTokens: int64(sink.usage.TotalTokens), MetadataJSON: metadataJSON(meta), Error: errorText(promptErr), JobID: p.claimID, JobFence: p.claimFence, Cause: cause}
			rows, changed, err := completion.CompleteRunWorkflow(context.WithoutCancel(executionContext), finishInput, messages, string(raw), jobs.WorkflowIntent{Event: jobs.EventInput{Key: "agent_run." + status + ":" + p.RunID, ResourceKind: "agent_run", ResourceID: p.RunID, EventType: "agent_run." + status, Data: encodedEventData}})
			if err != nil {
				if promptErr == nil {
					promptErr = err
				} else {
					promptErr = errors.Join(promptErr, err)
				}
			} else if changed && input.OnEvent != nil {
				for _, row := range rows {
					input.OnEvent(messageEnvelope(input.ConversationID, row))
				}
			}
		} else {
			if err := s.persistNewMessages(ctx, input, p.RunID, p.initial, transcript); err != nil && promptErr == nil {
				promptErr = err
			}
			if err := s.persistTranscript(ctx, input, transcript); err != nil && promptErr == nil {
				promptErr = err
			}
		}
	} else {
		if err := s.persistNewMessages(ctx, input, p.RunID, p.initial, transcript); err != nil && promptErr == nil {
			promptErr = err
		}
		if err := s.persistTranscript(ctx, input, transcript); err != nil && promptErr == nil {
			promptErr = err
		}
	}
	if !atomicCompletion {
		finish := func() error {
			return s.finishRunWithCause(context.WithoutCancel(executionContext), input, p.RunID, status, result.StopReason, sink.usage, promptErr, cause)
		}
		if p.claimID != "" {
			finish = func() error {
				return s.finishRunWithClaimCause(context.WithoutCancel(executionContext), input, p.RunID, status, result.StopReason, sink.usage, promptErr, p.claimID, p.claimFence, cause)
			}
		}
		if finishErr := finish(); finishErr != nil {
			if promptErr == nil {
				promptErr = finishErr
			} else {
				promptErr = errors.Join(promptErr, finishErr)
			}
		}
	}
	if promptErr != nil {
		return PromptResult{}, promptErr
	}
	return PromptResult{
		ConversationID: input.ConversationID,
		RunID:          result.RunID,
		StopReason:     result.StopReason,
		Content:        result.FinalMessage.Content,
	}, nil
}

func (p *StartedPrompt) Abort(ctx context.Context, runErr error) error {
	if p == nil {
		return nil
	}
	if err := p.claim(); err != nil {
		return nil
	}
	defer p.release()
	if runErr == nil {
		runErr = fmt.Errorf("prompt aborted")
	}
	input := PromptInput{
		Scope:          p.Scope,
		ConversationID: p.ConversationID,
		Input:          p.Input,
		CorrelationID:  p.CorrelationID,
	}
	if p.claimID != "" {
		return p.service.finishRunWithClaim(ctx, input, p.RunID, RunStatusFailed, "", agentcore.Usage{}, runErr, p.claimID, p.claimFence)
	}
	return p.service.finishRun(ctx, input, p.RunID, RunStatusFailed, "", agentcore.Usage{}, runErr)
}

func (p *StartedPrompt) claim() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return fmt.Errorf("started prompt is already closed")
	}
	p.closed = true
	return nil
}

func (p *StartedPrompt) release() {
	if p.service != nil {
		p.service.release(p.ConversationID)
	}
}

func (s *Service) acquire(conversationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.running[conversationID]; ok {
		return ErrBusy
	}
	s.running[conversationID] = runningPrompt{}
	return nil
}

func (s *Service) attachRun(conversationID, runID string, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.running[conversationID]; ok {
		s.running[conversationID] = runningPrompt{runID: runID, cancel: cancel}
	}
}

func (s *Service) release(conversationID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if active, ok := s.running[conversationID]; ok && active.cancel != nil {
		active.cancel()
	}
	delete(s.running, conversationID)
}

func (s *Service) persistNewMessages(ctx context.Context, input PromptInput, runID string, initial, transcript []agentcore.Message) error {
	seen := map[string]struct{}{}
	for _, message := range initial {
		if message.ID != "" {
			seen[message.ID] = struct{}{}
		}
	}
	for _, message := range transcript {
		if message.ID != "" {
			if _, ok := seen[message.ID]; ok {
				continue
			}
			seen[message.ID] = struct{}{}
		}
		if err := s.appendMessage(ctx, input, runID, message); err != nil {
			return err
		}
	}
	return nil
}

func newMessageInputs(input PromptInput, runID string, initial, transcript []agentcore.Message) []MessageInput {
	seen := map[string]struct{}{}
	for _, m := range initial {
		if m.ID != "" {
			seen[m.ID] = struct{}{}
		}
	}
	out := make([]MessageInput, 0)
	for _, m := range transcript {
		if m.ID != "" {
			if _, ok := seen[m.ID]; ok {
				continue
			}
			seen[m.ID] = struct{}{}
		}
		if m.Role == agentcore.RoleSystem || m.Kind == agentcore.MessageKindExternalContext {
			continue
		}
		text := m.Content
		if m.Role == agentcore.RoleUser {
			if visible, ok := m.DisplayContent.(string); ok && strings.TrimSpace(visible) != "" {
				text = visible
			}
		}
		content := messageContentJSON(m, input.Context)
		out = append(out, MessageInput{PrincipalID: input.Scope.PrincipalID, ConversationID: input.ConversationID, RunID: runID, Role: platformRole(m.Role), ContentText: text, ContentJSON: content, ToolCallID: m.ToolCallID, ToolName: m.ToolName, IsError: m.IsError})
	}
	return out
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 512 {
		return s[:512]
	}
	return s
}

func (s *Service) appendMessage(ctx context.Context, input PromptInput, runID string, message agentcore.Message) error {
	if message.Role == agentcore.RoleSystem || message.Kind == agentcore.MessageKindExternalContext {
		return nil
	}
	contentText := message.Content
	if message.Role == agentcore.RoleUser {
		if visible, ok := message.DisplayContent.(string); ok && strings.TrimSpace(visible) != "" {
			contentText = visible
		}
	}
	row, err := s.repo.AppendMessage(ctx, MessageInput{
		PrincipalID:    input.Scope.PrincipalID,
		ConversationID: input.ConversationID,
		RunID:          runID,
		Role:           platformRole(message.Role),
		ContentText:    contentText,
		ContentJSON:    messageContentJSON(message, input.Context),
		ToolCallID:     message.ToolCallID,
		ToolName:       message.ToolName,
		IsError:        message.IsError,
	})
	if err == nil && input.OnEvent != nil {
		input.OnEvent(messageEnvelope(input.ConversationID, row))
	}
	return err
}

func lastVisibleUserMessage(messages []agentcore.Message) (agentcore.Message, bool) {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == agentcore.RoleUser && messages[index].Kind != agentcore.MessageKindExternalContext {
			return messages[index], true
		}
	}
	return agentcore.Message{}, false
}

func (s *Service) persistTranscript(ctx context.Context, input PromptInput, transcript []agentcore.Message) error {
	bytes, err := json.Marshal(compactTranscriptForStorage(transcript))
	if err != nil {
		return err
	}
	_, err = s.repo.UpdateConversationTranscript(ctx, input.Scope.PrincipalID, input.ConversationID, string(bytes))
	return err
}

func compactTranscriptForStorage(transcript []agentcore.Message) []agentcore.Message {
	out := make([]agentcore.Message, len(transcript))
	for i, message := range transcript {
		message.DisplayContent = nil
		out[i] = message
	}
	return out
}

func (s *Service) finishRun(ctx context.Context, input PromptInput, runID, status string, stop agentcore.StopReason, usage agentcore.Usage, runErr error) error {
	return s.finishRunWithCause(ctx, input, runID, status, stop, usage, runErr, "")
}

func (s *Service) finishRunWithCause(ctx context.Context, input PromptInput, runID, status string, stop agentcore.StopReason, usage agentcore.Usage, runErr error, cause RunTerminationCause) error {
	return s.finishRunWithClaimCause(ctx, input, runID, status, stop, usage, runErr, "", jobs.Fence{}, cause)
}

func (s *Service) finishRunWithClaim(ctx context.Context, input PromptInput, runID, status string, stop agentcore.StopReason, usage agentcore.Usage, runErr error, jobID string, fence jobs.Fence) error {
	return s.finishRunWithClaimCause(ctx, input, runID, status, stop, usage, runErr, jobID, fence, "")
}

func (s *Service) finishRunWithClaimCause(ctx context.Context, input PromptInput, runID, status string, stop agentcore.StopReason, usage agentcore.Usage, runErr error, jobID string, fence jobs.Fence, cause RunTerminationCause) error {
	errText := ""
	if runErr != nil {
		errText = runErr.Error()
		if len(errText) > 512 {
			errText = errText[:512]
		}
	}
	metadata := map[string]any{"model": s.config.Model}
	if cause != "" {
		metadata["terminationCause"] = cause
	}
	finish := RunFinish{
		PrincipalID:    input.Scope.PrincipalID,
		ConversationID: input.ConversationID,
		RunID:          runID,
		Status:         status,
		StopReason:     string(stop),
		InputTokens:    int64(usage.InputTokens),
		OutputTokens:   int64(usage.OutputTokens),
		TotalTokens:    int64(usage.TotalTokens),
		Error:          errText,
		MetadataJSON:   metadataJSON(metadata),
		JobID:          jobID,
		JobFence:       fence,
		Cause:          cause,
	}
	if jobID != "" && s.runWorkflowAvailable() {
		if terminalizer, ok := s.repo.(RunTerminalWorkflow); ok {
			eventType := "agent_run." + status
			data, _ := json.Marshal(map[string]any{"runId": runID, "conversationId": input.ConversationID})
			_, _, err := terminalizer.FinishRunWorkflow(ctx, finish, jobs.WorkflowIntent{Event: jobs.EventInput{Key: eventType + ":" + runID, ResourceKind: "agent_run", ResourceID: runID, EventType: eventType, Data: data}})
			return err
		}
	}
	_, err := s.repo.FinishRun(ctx, finish)
	return err
}

func (s *Service) cleanupFailedRun(ctx context.Context, input PromptInput, runID string, runErr error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if s.promptWorkflow != nil {
		if terminalizer, ok := s.repo.(RunTerminalWorkflow); ok {
			errText := "durable prompt failed before activation"
			if runErr != nil && strings.TrimSpace(runErr.Error()) != "" {
				errText = runErr.Error()
			}
			if len(errText) > 512 {
				errText = errText[:512]
			}
			data, _ := json.Marshal(map[string]any{"runId": runID, "conversationId": input.ConversationID})
			finish := RunFinish{PrincipalID: input.Scope.PrincipalID, ConversationID: input.ConversationID, RunID: runID, Status: RunStatusFailed, Error: errText, MetadataJSON: metadataJSON(map[string]any{"model": s.config.Model, "terminationCause": RunCauseResumeFailure}), Cause: RunCauseResumeFailure}
			_, _, err := terminalizer.FinishRunWorkflow(cleanupCtx, finish, jobs.WorkflowIntent{Event: jobs.EventInput{Key: "agent_run.failed:" + runID, ResourceKind: "agent_run", ResourceID: runID, EventType: "agent_run.failed", Data: data}})
			return err
		}
	}
	return s.finishRun(cleanupCtx, input, runID, RunStatusFailed, "", agentcore.Usage{}, runErr)
}

func (s *Service) startFailure(ctx context.Context, input PromptInput, runID string, runErr error) (*StartedPrompt, error) {
	if cleanupErr := s.cleanupFailedRun(ctx, input, runID, runErr); cleanupErr != nil {
		return nil, errors.Join(runErr, fmt.Errorf("failed to terminalize agent run: %w", cleanupErr))
	}
	return nil, runErr
}

type fixedRunIDGenerator struct {
	runID string
}

func (g fixedRunIDGenerator) NewID(prefix string) string {
	if prefix == "run" {
		return g.runID
	}
	return newID(prefix)
}

func decodeTranscript(raw string) ([]agentcore.Message, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var messages []agentcore.Message
	if err := json.Unmarshal([]byte(raw), &messages); err != nil {
		return nil, err
	}
	return messages, nil
}
