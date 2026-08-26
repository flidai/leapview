package agent

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type EventSink interface {
	// Emit receives best-effort lifecycle events. Returned errors are ignored by
	// the harness so observability sinks cannot break agent execution.
	Emit(ctx context.Context, event Event) error
}

type EventSinkFunc func(ctx context.Context, event Event) error

func (f EventSinkFunc) Emit(ctx context.Context, event Event) error {
	return f(ctx, event)
}

type noopEventSink struct{}

func (noopEventSink) Emit(context.Context, Event) error { return nil }

type EventType string

const (
	EventTypeAgentStart         EventType = "agent_start"
	EventTypeAgentEnd           EventType = "agent_end"
	EventTypeTurnStart          EventType = "turn_start"
	EventTypeTurnEnd            EventType = "turn_end"
	EventTypeModelRequest       EventType = "model_request"
	EventTypeModelResponse      EventType = "model_response"
	EventTypeModelRetry         EventType = "model_retry"
	EventTypeOutputPartAdded    EventType = "output_part_added"
	EventTypeOutputTextDelta    EventType = "output_text_delta"
	EventTypeOutputPartDone     EventType = "output_part_done"
	EventTypeToolExecutionStart EventType = "tool_execution_start"
	EventTypeToolExecutionEnd   EventType = "tool_execution_end"
	EventTypeCompactionStart    EventType = "compaction_start"
	EventTypeCompactionEnd      EventType = "compaction_end"
	EventTypeCompactionError    EventType = "compaction_error"
	EventTypeError              EventType = "error"
	EventTypeAbort              EventType = "abort"
)

type OutputPartKind string

const (
	OutputPartKindText OutputPartKind = "text"
	OutputPartKindTool OutputPartKind = "tool"
)

type Severity string

const (
	SeverityDebug Severity = "debug"
	SeverityInfo  Severity = "info"
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
)

type Event struct {
	Type            EventType
	Severity        Severity
	Time            time.Time
	Sequence        int64
	RunID           string
	TurnID          string
	MessageID       string
	OutputPartID    string
	OutputKind      OutputPartKind
	OutputOrdinal   int64
	ParentMessageID string
	ToolCallID      string
	ToolName        string
	ToolArguments   string
	ToolResult      string
	ToolDisplay     string
	CorrelationID   string
	Delta           string
	Content         string
	StopReason      StopReason
	FinishReason    FinishReason
	Error           *AgentError
	Usage           Usage
	Provider        string
	Model           string

	ProviderMetadata map[string]any
}

type runState struct {
	agent         *Agent
	runID         string
	correlationID string
	seq           atomic.Int64
	outputOrdinal atomic.Int64
}

func (r *runState) emit(ctx context.Context, event Event) error {
	if event.Severity == "" {
		event.Severity = SeverityInfo
	}
	event.RunID = r.runID
	event.CorrelationID = r.correlationID
	event.Sequence = r.seq.Add(1)
	event.Time = r.agent.def.Clock.Now()
	return r.agent.def.Events.Emit(ctx, event)
}

type outputPart struct {
	ID              string
	Kind            OutputPartKind
	Ordinal         int64
	ParentMessageID string
}

func (r *runState) addOutputPart(ctx context.Context, turnID string, kind OutputPartKind, parentMessageID, toolCallID, toolName, toolArguments string) (outputPart, error) {
	part := outputPart{
		ID:              r.agent.def.IDGenerator.NewID("part"),
		Kind:            kind,
		Ordinal:         r.outputOrdinal.Add(1) - 1,
		ParentMessageID: parentMessageID,
	}
	err := r.emit(ctx, Event{
		Type:            EventTypeOutputPartAdded,
		Severity:        SeverityInfo,
		TurnID:          turnID,
		MessageID:       parentMessageID,
		OutputPartID:    part.ID,
		OutputKind:      kind,
		OutputOrdinal:   part.Ordinal,
		ParentMessageID: parentMessageID,
		ToolCallID:      toolCallID,
		ToolName:        toolName,
		ToolArguments:   toolArguments,
	})
	return part, err
}

type eventModelStream struct {
	run       *runState
	turnID    string
	messageID string
	mu        sync.Mutex
	part      outputPart
	opened    bool
}

func (s *eventModelStream) ensureOpen(ctx context.Context) error {
	if s.opened {
		return nil
	}
	part, err := s.run.addOutputPart(ctx, s.turnID, OutputPartKindText, s.messageID, "", "", "")
	if err != nil {
		return err
	}
	s.part = part
	s.opened = true
	return nil
}

func (s *eventModelStream) Delta(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpen(ctx); err != nil {
		return err
	}
	return s.run.emit(ctx, Event{
		Type:            EventTypeOutputTextDelta,
		Severity:        SeverityInfo,
		TurnID:          s.turnID,
		MessageID:       s.messageID,
		OutputPartID:    s.part.ID,
		OutputKind:      s.part.Kind,
		OutputOrdinal:   s.part.Ordinal,
		ParentMessageID: s.part.ParentMessageID,
		Delta:           text,
	})
}

func (s *eventModelStream) finish(ctx context.Context, content string) outputPart {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.opened && strings.TrimSpace(content) == "" {
		return outputPart{}
	}
	if err := s.ensureOpen(ctx); err != nil {
		return outputPart{}
	}
	_ = s.run.emit(ctx, Event{
		Type:            EventTypeOutputPartDone,
		Severity:        SeverityInfo,
		TurnID:          s.turnID,
		MessageID:       s.messageID,
		OutputPartID:    s.part.ID,
		OutputKind:      s.part.Kind,
		OutputOrdinal:   s.part.Ordinal,
		ParentMessageID: s.part.ParentMessageID,
		Content:         content,
	})
	return s.part
}
