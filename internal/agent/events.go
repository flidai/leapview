package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	agentcore "github.com/flidai/leapview/pkg/agent"
)

type storeEventSink struct {
	repo           Repository
	scope          Scope
	conversationID string
	runID          string
	onEvent        func(EventEnvelope)
	usage          agentcore.Usage
	mu             sync.Mutex
}

func (s *storeEventSink) Emit(ctx context.Context, event agentcore.Event) error {
	if event.Type == agentcore.EventTypeModelResponse || event.Type == agentcore.EventTypeCompactionEnd {
		s.mu.Lock()
		s.usage.InputTokens += event.Usage.InputTokens
		s.usage.OutputTokens += event.Usage.OutputTokens
		s.usage.TotalTokens += event.Usage.TotalTokens
		s.mu.Unlock()
	}
	row, err := s.repo.AppendEvent(ctx, EventInput{
		PrincipalID: s.scope.PrincipalID,
		RunID:       s.runID,
		Sequence:    event.Sequence,
		EventType:   string(event.Type),
		Severity:    string(event.Severity),
		PayloadJSON: eventPayloadJSON(event),
	})
	if err == nil && s.onEvent != nil {
		s.onEvent(eventEnvelope(s.conversationID, row))
	}
	return err
}

func metadataJSON(value map[string]any) string {
	bytes, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(bytes)
}

func eventPayloadJSON(event agentcore.Event) string {
	payload := map[string]any{
		"type":           event.Type,
		"turn_id":        event.TurnID,
		"message_id":     event.MessageID,
		"tool_call_id":   event.ToolCallID,
		"tool_name":      event.ToolName,
		"stop_reason":    event.StopReason,
		"finish_reason":  event.FinishReason,
		"usage":          event.Usage,
		"provider":       event.Provider,
		"model":          event.Model,
		"provider_meta":  event.ProviderMetadata,
		"correlation_id": event.CorrelationID,
	}
	if event.OutputPartID != "" {
		payload["output_part_id"] = event.OutputPartID
		payload["output_kind"] = event.OutputKind
		payload["output_ordinal"] = event.OutputOrdinal
		payload["parent_message_id"] = event.ParentMessageID
	}
	if event.Error != nil {
		payload["error"] = event.Error.Error()
	}
	if event.Delta != "" {
		payload["delta"] = event.Delta
	}
	if event.Content != "" || event.Type == agentcore.EventTypeOutputPartDone {
		payload["content"] = event.Content
	}
	if event.ToolArguments != "" {
		payload["tool_arguments"] = event.ToolArguments
	}
	if event.ToolResult != "" {
		payload["tool_result"] = event.ToolResult
	}
	if event.ToolDisplay != "" {
		payload["tool_display"] = event.ToolDisplay
	}
	return metadataJSON(payload)
}

func eventPayload(raw string) map[string]any {
	payload := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return payload
	}
	_ = json.Unmarshal([]byte(raw), &payload)
	return payload
}

func eventEnvelope(conversationID string, row Event) EventEnvelope {
	return EventEnvelope{
		ID:             row.ID,
		ConversationID: conversationID,
		RunID:          row.RunID,
		Seq:            row.Seq,
		Type:           row.EventType,
		Severity:       row.Severity,
		CreatedAt:      row.CreatedAt,
		Payload:        eventPayload(row.PayloadJSON),
	}
}

func messageEnvelope(conversationID string, row Message) EventEnvelope {
	return EventEnvelope{
		ID:             "message:" + row.ID,
		ConversationID: conversationID,
		RunID:          row.RunID,
		Seq:            row.Seq,
		Type:           "message_appended",
		Severity:       "info",
		CreatedAt:      row.CreatedAt,
		Payload: map[string]any{
			"message": map[string]any{
				"id":           row.ID,
				"role":         row.Role,
				"content":      row.ContentText,
				"content_json": eventPayload(row.ContentJSON),
				"tool_call_id": row.ToolCallID,
				"tool_name":    row.ToolName,
				"is_error":     row.IsError,
			},
		},
	}
}

func messageContentJSON(message agentcore.Message, context *TurnContext) string {
	payload := map[string]any{
		"role":            message.Role,
		"content":         message.Content,
		"display_content": message.DisplayContent,
		"tool_calls":      message.ToolCalls,
		"tool_call_id":    message.ToolCallID,
		"tool_name":       message.ToolName,
		"is_error":        message.IsError,
		"finish_reason":   message.FinishReason,
		"usage":           message.Usage,
	}
	if message.OutputPartID != "" {
		payload["output_part_id"] = message.OutputPartID
		payload["output_ordinal"] = message.OutputOrdinal
		payload["parent_message_id"] = message.ParentMessageID
	}
	if message.Role == agentcore.RoleUser && context != nil {
		normalized := context.normalized()
		payload["turn_context"] = normalized
	}
	return metadataJSON(payload)
}
