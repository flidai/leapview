package module

import (
	"encoding/json"
	"strings"

	agentcap "github.com/flidai/leapview/internal/agent"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

func appendServerUserTranscript(transcript []agentcap.ChatTranscriptItem, conversationID, input string) []agentcap.ChatTranscriptItem {
	if strings.TrimSpace(input) == "" {
		return transcript
	}
	next := append([]agentcap.ChatTranscriptItem{}, transcript...)
	next = append(next, agentcap.ChatTranscriptItem{
		ID:             "live:user",
		Kind:           "user",
		Text:           input,
		ConversationID: conversationID,
	})
	return next
}

func applyLiveTranscriptEvent(transcript []agentcap.ChatTranscriptItem, conversationID string, event agentcap.EventEnvelope) []agentcap.ChatTranscriptItem {
	next := append([]agentcap.ChatTranscriptItem{}, transcript...)
	switch event.Type {
	case string(agentcore.EventTypeOutputPartAdded):
		partID := stringPayload(event.Payload, "output_part_id")
		kind := stringPayload(event.Payload, "output_kind")
		if partID == "" || (kind != string(agentcore.OutputPartKindText) && kind != string(agentcore.OutputPartKindTool)) {
			return next
		}
		if transcriptPartIndex(next, partID) >= 0 {
			return next
		}
		item := agentcap.ChatTranscriptItem{
			ID:              partID,
			OutputOrdinal:   int64Payload(event.Payload, "output_ordinal"),
			ParentMessageID: stringPayload(event.Payload, "parent_message_id"),
			ConversationID:  conversationID,
			RunID:           event.RunID,
			CreatedAt:       event.CreatedAt,
		}
		if kind == string(agentcore.OutputPartKindText) {
			item.Kind = "assistant"
			item.Status = "streaming"
		} else {
			item.Kind = "tool"
			item.ToolCallID = stringPayload(event.Payload, "tool_call_id")
			item.Name = stringPayload(event.Payload, "tool_name")
			item.Title = liveToolTitle(item.Name)
			item.Status = "pending"
			applyLiveToolInput(&item, event.Payload)
		}
		return insertOutputPart(next, item)
	case string(agentcore.EventTypeOutputTextDelta):
		delta := stringPayload(event.Payload, "delta")
		idx := transcriptPartIndex(next, stringPayload(event.Payload, "output_part_id"))
		if delta == "" || idx < 0 || next[idx].Kind != "assistant" {
			return next
		}
		next[idx].Markdown += delta
		return next
	case string(agentcore.EventTypeOutputPartDone):
		idx := transcriptPartIndex(next, stringPayload(event.Payload, "output_part_id"))
		if idx < 0 {
			return next
		}
		if content, ok := payloadString(event.Payload, "content"); ok {
			next[idx].Markdown = content
		}
		next[idx].Status = "complete"
		return next
	case string(agentcore.EventTypeToolExecutionStart):
		idx := transcriptPartIndex(next, stringPayload(event.Payload, "output_part_id"))
		if idx < 0 || next[idx].Kind != "tool" {
			return next
		}
		next[idx].Status = "running"
		applyLiveToolInput(&next[idx], event.Payload)
		return next
	case string(agentcore.EventTypeToolExecutionEnd):
		idx := transcriptPartIndex(next, stringPayload(event.Payload, "output_part_id"))
		if idx < 0 || next[idx].Kind != "tool" {
			return next
		}
		applyLiveToolInput(&next[idx], event.Payload)
		result := stringPayload(event.Payload, "tool_result")
		preview := agentcap.PreviewToolResult(result)
		if result != "" {
			next[idx].ResultJSON = preview.ResultJSON
			next[idx].ResultFormat = preview.Format
		}
		if event.Severity == string(agentcore.SeverityError) || event.Severity == string(agentcore.SeverityWarn) {
			next[idx].Status = "error"
			next[idx].Error = preview.Error
			if next[idx].Error == "" {
				next[idx].Error = "Tool failed"
			}
			return next
		}
		next[idx].Status = "complete"
		next[idx].Summary = preview.Summary
		next[idx].ResultSummary = next[idx].Summary
		return next
	default:
		return next
	}
}

func applyLiveToolInput(item *agentcap.ChatTranscriptItem, payload map[string]any) {
	arguments := stringPayload(payload, "tool_arguments")
	if item == nil || arguments == "" {
		return
	}
	item.InputJSON, item.ArgumentsJSON = agentcap.PreviewToolInput(item.ToolCallID, item.Name, arguments)
	item.InputFormat = "json"
}

func transcriptPartIndex(transcript []agentcap.ChatTranscriptItem, partID string) int {
	if partID == "" {
		return -1
	}
	for i := range transcript {
		if transcript[i].ID == partID {
			return i
		}
	}
	return -1
}

func insertOutputPart(transcript []agentcap.ChatTranscriptItem, item agentcap.ChatTranscriptItem) []agentcap.ChatTranscriptItem {
	insertAt := len(transcript)
	for i := range transcript {
		if transcript[i].RunID == item.RunID && transcript[i].Kind != "user" && transcript[i].OutputOrdinal > item.OutputOrdinal {
			insertAt = i
			break
		}
	}
	transcript = append(transcript, agentcap.ChatTranscriptItem{})
	copy(transcript[insertAt+1:], transcript[insertAt:])
	transcript[insertAt] = item
	return transcript
}

func stringPayload(payload map[string]any, key string) string {
	value, _ := payloadString(payload, key)
	return value
}

func payloadString(payload map[string]any, key string) (string, bool) {
	if payload == nil {
		return "", false
	}
	value, ok := payload[key].(string)
	return value, ok
}

func int64Payload(payload map[string]any, key string) int64 {
	if payload == nil {
		return 0
	}
	switch value := payload[key].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	default:
		return 0
	}
}

func liveToolTitle(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "Tool"
	}
	parts := strings.Fields(strings.ReplaceAll(name, "_", " "))
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}
