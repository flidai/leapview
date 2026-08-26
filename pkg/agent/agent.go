package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
)

var errContextLimitStop = errors.New("agent context limit reached")

type Agent struct {
	def       Definition
	catalog   *ToolCatalog
	tools     map[string]*compiledTool
	toolSpecs []ToolSpec

	mu         sync.Mutex
	transcript []Message
	running    bool
	cancel     context.CancelFunc
}

type PromptRequest struct {
	Input         string
	Context       []ContextItem
	CorrelationID string
}

// PreparedPromptRequest resumes a prompt already appended with PreparePrompt.
// It is intended for hosts that durably persist the transcript before model
// execution. Most callers should use Prompt.
type PreparedPromptRequest struct {
	CorrelationID string
}

type RunResult struct {
	RunID        string
	StopReason   StopReason
	FinalMessage Message
	Turns        int
	ToolCalls    int
}

type StopReason string

const (
	StopReasonCompleted      StopReason = "completed"
	StopReasonMaxTurns       StopReason = "max_turns"
	StopReasonMaxToolCalls   StopReason = "max_tool_calls"
	StopReasonContextLimit   StopReason = "context_limit"
	StopReasonTruncated      StopReason = "truncated"
	StopReasonCanceled       StopReason = "canceled"
	StopReasonModelError     StopReason = "model_error"
	StopReasonFatalToolError StopReason = "fatal_tool_error"
)

func New(def Definition) (*Agent, error) {
	def = def.withDefaults()
	if strings.TrimSpace(def.SystemPrompt) == "" {
		return nil, NewError(ErrorCodeInvalidArgument, "system prompt is required", nil)
	}
	if def.Model == nil {
		return nil, NewError(ErrorCodeInvalidArgument, "model is required", nil)
	}
	if err := validateLimits(def.Limits); err != nil {
		return nil, err
	}
	if err := validateToolOutputConfig(def.ToolOutput); err != nil {
		return nil, err
	}
	catalog, err := NewToolCatalog(def.Tools)
	if err != nil {
		return nil, err
	}
	return &Agent{def: def, catalog: catalog, tools: catalog.tools, toolSpecs: catalog.Specs(), transcript: cloneMessages(def.InitialTranscript)}, nil
}

func (a *Agent) Prompt(ctx context.Context, req PromptRequest) (RunResult, error) {
	if err := a.PreparePrompt(req); err != nil {
		return RunResult{}, err
	}
	return a.RunPreparedPrompt(ctx, PreparedPromptRequest{CorrelationID: req.CorrelationID})
}

// PreparePrompt validates and appends a prompt and its context without calling
// the model. Context is framed as hidden, untrusted model input immediately
// before the visible user message.
func (a *Agent) PreparePrompt(req PromptRequest) error {
	if strings.TrimSpace(req.Input) == "" {
		return NewError(ErrorCodeInvalidArgument, "prompt input is required", nil)
	}
	contextMessages, err := externalContextMessages(req.Context, a.def.IDGenerator)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running || hasPreparedPrompt(a.transcript) {
		return NewError(ErrorCodeBusy, "agent already has a prepared or running prompt", nil)
	}
	a.transcript = append(a.transcript, contextMessages...)
	a.transcript = append(a.transcript, Message{
		ID:      a.def.IDGenerator.NewID("msg"),
		Role:    RoleUser,
		Content: req.Input,
	})
	return nil
}

// RunPreparedPrompt executes the last prompt in the transcript. A fresh Agent
// may be constructed from a transcript persisted after PreparePrompt, allowing
// durable hosts to resume without reconstructing context themselves.
func (a *Agent) RunPreparedPrompt(ctx context.Context, req PreparedPromptRequest) (RunResult, error) {
	runCtx, cancel := context.WithCancel(ctx)
	runID := a.def.IDGenerator.NewID("run")
	run := &runState{agent: a, runID: runID, correlationID: req.CorrelationID}

	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		cancel()
		return RunResult{}, NewError(ErrorCodeBusy, "agent is already running", nil)
	}
	if !hasPreparedPrompt(a.transcript) {
		a.mu.Unlock()
		cancel()
		return RunResult{}, NewError(ErrorCodeInvalidArgument, "agent transcript has no prepared prompt", nil)
	}
	a.running = true
	a.cancel = cancel
	a.mu.Unlock()

	defer func() {
		cancel()
		a.mu.Lock()
		a.running = false
		a.cancel = nil
		a.mu.Unlock()
	}()

	_ = run.emit(runCtx, Event{Type: EventTypeAgentStart, Severity: SeverityInfo})
	result, err := a.runLoop(runCtx, run)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			_ = run.emit(context.Background(), Event{Type: EventTypeAbort, Severity: SeverityWarn, StopReason: StopReasonCanceled})
			_ = run.emit(context.Background(), Event{Type: EventTypeAgentEnd, Severity: SeverityWarn, StopReason: StopReasonCanceled})
			return RunResult{}, NewError(ErrorCodeCanceled, "agent run canceled", err)
		}
		_ = run.emit(context.Background(), Event{Type: EventTypeError, Severity: SeverityError, Error: agentErrorPtr(ErrorCodeModel, "agent run failed", err)})
		_ = run.emit(context.Background(), Event{Type: EventTypeAgentEnd, Severity: SeverityError, StopReason: StopReasonModelError})
		return RunResult{}, err
	}
	_ = run.emit(runCtx, Event{Type: EventTypeAgentEnd, Severity: severityForStop(result.StopReason), StopReason: result.StopReason})
	return result, nil
}

func hasPreparedPrompt(messages []Message) bool {
	lastUser, lastAssistant := -1, -1
	for i, message := range messages {
		switch {
		case message.Role == RoleUser && message.Kind != MessageKindExternalContext:
			lastUser = i
		case message.Role == RoleAssistant:
			lastAssistant = i
		}
	}
	return lastUser > lastAssistant
}

func (a *Agent) Abort() {
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *Agent) Transcript() []Message {
	return a.snapshotTranscript()
}

func (a *Agent) runLoop(ctx context.Context, run *runState) (RunResult, error) {
	result := RunResult{RunID: run.runID}
	for result.Turns < a.def.Limits.MaxTurns {
		result.Turns++
		turnID := a.def.IDGenerator.NewID("turn")
		_ = run.emit(ctx, Event{Type: EventTypeTurnStart, Severity: SeverityInfo, TurnID: turnID})

		if err := a.maybeCompact(ctx, run, false); err != nil {
			return result, err
		}
		if a.estimateModelInputTokens(a.snapshotTranscript()) > a.def.Limits.HardInputLimitTokens {
			result.StopReason = StopReasonContextLimit
			_ = run.emit(ctx, Event{Type: EventTypeTurnEnd, Severity: SeverityWarn, TurnID: turnID, StopReason: result.StopReason})
			return result, nil
		}

		messageID := a.def.IDGenerator.NewID("msg")
		stream := &eventModelStream{run: run, turnID: turnID, messageID: messageID}
		resp, err := a.completeTurn(ctx, run, turnID, stream, false)
		if err != nil {
			if errors.Is(err, errContextLimitStop) {
				result.StopReason = StopReasonContextLimit
				_ = run.emit(ctx, Event{Type: EventTypeTurnEnd, Severity: SeverityWarn, TurnID: turnID, StopReason: result.StopReason})
				return result, nil
			}
			return result, err
		}
		finish := NormalizeFinishReason(resp.FinishReason)
		textPart := stream.finish(ctx, resp.Content)
		toolCalls := a.declareToolOutputParts(ctx, run, turnID, messageID, resp.ToolCalls)
		assistant := Message{
			ID:              messageID,
			OutputPartID:    textPart.ID,
			OutputOrdinal:   textPart.Ordinal,
			ParentMessageID: messageID,
			Role:            RoleAssistant,
			Content:         resp.Content,
			ToolCalls:       toolCalls,
			FinishReason:    finish,
			Usage:           resp.Usage,
		}
		a.appendTranscript(assistant)
		result.FinalMessage = assistant

		if finish == FinishReasonTruncated {
			result.StopReason = StopReasonTruncated
			_ = run.emit(ctx, Event{Type: EventTypeTurnEnd, Severity: SeverityWarn, TurnID: turnID, StopReason: result.StopReason})
			return result, nil
		}
		if len(resp.ToolCalls) == 0 {
			result.StopReason = StopReasonCompleted
			_ = run.emit(ctx, Event{Type: EventTypeTurnEnd, Severity: SeverityInfo, TurnID: turnID, StopReason: result.StopReason})
			_ = a.maybeCompact(ctx, run, false)
			return result, nil
		}
		if result.ToolCalls+len(resp.ToolCalls) > a.def.Limits.MaxToolCalls {
			result.StopReason = StopReasonMaxToolCalls
			_ = run.emit(ctx, Event{Type: EventTypeTurnEnd, Severity: SeverityWarn, TurnID: turnID, StopReason: result.StopReason})
			return result, nil
		}

		toolMessages, err := a.executeToolCalls(ctx, run, turnID, toolCalls)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return result, err
			}
			for _, message := range toolMessages {
				a.appendTranscript(message)
			}
			result.StopReason = StopReasonFatalToolError
			_ = run.emit(ctx, Event{Type: EventTypeTurnEnd, Severity: SeverityError, TurnID: turnID, StopReason: result.StopReason})
			return result, nil
		}
		for _, message := range toolMessages {
			a.appendTranscript(message)
		}
		result.ToolCalls += len(resp.ToolCalls)
		_ = run.emit(ctx, Event{Type: EventTypeTurnEnd, Severity: SeverityInfo, TurnID: turnID})
		_ = a.maybeCompact(ctx, run, false)
	}
	result.StopReason = StopReasonMaxTurns
	return result, nil
}

func (a *Agent) completeTurn(ctx context.Context, run *runState, turnID string, stream *eventModelStream, retried bool) (ModelResponse, error) {
	req := a.buildModelRequest(run, turnID)
	_ = run.emit(ctx, Event{Type: EventTypeModelRequest, Severity: SeverityDebug, TurnID: turnID})
	resp, err := a.def.Model.Complete(ctx, req, stream)
	if err != nil {
		if !retried && isContextLengthError(err) {
			_ = run.emit(ctx, Event{Type: EventTypeModelRetry, Severity: SeverityWarn, TurnID: turnID, Error: agentErrorPtr(ErrorCodeLimit, "model context limit reached", err)})
			_ = a.maybeCompact(ctx, run, true)
			if a.estimateModelInputTokens(a.snapshotTranscript()) > a.def.Limits.HardInputLimitTokens {
				return ModelResponse{}, errContextLimitStop
			}
			return a.completeTurn(ctx, run, turnID, stream, true)
		}
		return ModelResponse{}, NewError(ErrorCodeModel, "model request failed", err)
	}
	resp.FinishReason = NormalizeFinishReason(resp.FinishReason)
	_ = run.emit(ctx, Event{
		Type:             EventTypeModelResponse,
		Severity:         SeverityDebug,
		TurnID:           turnID,
		FinishReason:     resp.FinishReason,
		Usage:            resp.Usage,
		ProviderMetadata: cloneMetadata(resp.ProviderMetadata),
	})
	return resp, nil
}

func (a *Agent) declareToolOutputParts(ctx context.Context, run *runState, turnID, parentMessageID string, calls []ToolCall) []ToolCall {
	declared := append([]ToolCall(nil), calls...)
	for i := range declared {
		part, _ := run.addOutputPart(ctx, turnID, OutputPartKindTool, parentMessageID, declared[i].ID, declared[i].Name, string(declared[i].Arguments))
		declared[i].OutputPartID = part.ID
		declared[i].OutputOrdinal = part.Ordinal
		declared[i].ParentMessageID = parentMessageID
	}
	return declared
}

func (a *Agent) buildModelRequest(run *runState, turnID string) ModelRequest {
	transcript := a.snapshotTranscript()
	systemPrompt := promptWithExternalContextGuidance(a.def.SystemPrompt, transcript)
	return ModelRequest{
		Purpose:       ModelRequestPurposeTurn,
		RunID:         run.runID,
		TurnID:        turnID,
		CorrelationID: run.correlationID,
		SystemPrompt:  systemPrompt,
		Messages:      a.modelMessagesFrom(transcript),
		Tools:         append([]ToolSpec(nil), a.toolSpecs...),
		Limits:        a.def.Limits,
	}
}

func (a *Agent) modelMessagesFrom(transcript []Message) []Message {
	messages := []Message{{Role: RoleSystem, Content: promptWithExternalContextGuidance(a.def.SystemPrompt, transcript)}}
	for _, message := range transcript {
		if message.Role == RoleSummary {
			if message.Kind == messageKindExternalContextSummary {
				messages = append(messages, Message{Role: RoleUser, Kind: MessageKindExternalContext, Content: "<external_context_summary>\n" + message.Content + "\n</external_context_summary>"})
				continue
			}
			messages = append(messages, Message{Role: RoleSystem, Content: "Conversation summary:\n" + message.Content})
			continue
		}
		if message.Role == RoleSystem {
			continue
		}
		message.DisplayContent = nil
		messages = append(messages, message)
	}
	return cloneMessages(messages)
}

func (a *Agent) estimateModelInputTokens(transcript []Message) int {
	total := estimateTokens(promptWithExternalContextGuidance(a.def.SystemPrompt, transcript))
	for _, message := range transcript {
		total += estimateTokens(message.Content)
		for _, call := range message.ToolCalls {
			total += estimateTokens(call.Name) + len(call.Arguments)/4 + 1
		}
	}
	for _, tool := range a.toolSpecs {
		total += estimateTokens(tool.Name) + estimateTokens(tool.Description) + len(tool.InputSchema)/4 + 1
	}
	return total
}

func (a *Agent) estimateModelRequestTokens(transcript []Message) int {
	return a.estimateModelInputTokens(transcript) + a.def.Limits.ReserveOutputTokens
}

func (a *Agent) appendTranscript(messages ...Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.transcript = append(a.transcript, messages...)
}

func (a *Agent) snapshotTranscript() []Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneMessages(a.transcript)
}

func severityForStop(reason StopReason) Severity {
	switch reason {
	case StopReasonCompleted:
		return SeverityInfo
	case StopReasonContextLimit, StopReasonMaxTurns, StopReasonMaxToolCalls, StopReasonTruncated, StopReasonCanceled:
		return SeverityWarn
	default:
		return SeverityError
	}
}
