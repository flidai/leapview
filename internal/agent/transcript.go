package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	agentcore "github.com/flidai/leapview/pkg/agent"
)

func transcriptFromMessages(conversationID string, messages []Message) []ChatTranscriptItem {
	return transcriptStateFromMessages(conversationID, messages).Transcript
}

func transcriptStateFromMessages(conversationID string, messages []Message) ChatTranscriptState {
	items := make([]ChatTranscriptItem, 0, len(messages))
	toolIndexByPartID := map[string]int{}
	toolIndexByCallID := map[string]int{}
	artifacts := emptyChatArtifactSignals()
	for _, message := range messages {
		switch message.Role {
		case MessageRoleUser:
			items = append(items, ChatTranscriptItem{
				ID:             message.ID,
				Kind:           "user",
				Text:           message.ContentText,
				ConversationID: conversationID,
				RunID:          message.RunID,
				CreatedAt:      message.CreatedAt,
				References:     turnReferencesFromContentJSON(message.ContentJSON),
			})
		case MessageRoleAssistant:
			output := outputPartFromContentJSON(message.ContentJSON)
			if strings.TrimSpace(message.ContentText) != "" {
				partID := output.ID
				if partID == "" {
					partID = message.ID
				}
				items = append(items, ChatTranscriptItem{
					ID:              partID,
					Kind:            "assistant",
					OutputOrdinal:   output.Ordinal,
					ParentMessageID: output.ParentMessageID,
					Markdown:        message.ContentText,
					Status:          "complete",
					ConversationID:  conversationID,
					RunID:           message.RunID,
					CreatedAt:       message.CreatedAt,
				})
			}
			for _, call := range toolCallsFromContentJSON(message.ContentJSON) {
				if call.ID == "" {
					continue
				}
				partID := call.outputPartID()
				toolIndexByPartID[partID] = len(items)
				toolIndexByCallID[call.ID] = len(items)
				items = append(items, ChatTranscriptItem{
					ID:              partID,
					Kind:            "tool",
					OutputOrdinal:   call.OutputOrdinal,
					ParentMessageID: call.ParentMessageID,
					ToolCallID:      call.ID,
					Name:            call.Name,
					Title:           toolTitle(call.Name),
					Status:          "running",
					InputJSON:       formatToolCallPreview(call),
					InputFormat:     "json",
					ArgumentsJSON:   formatJSONPreview(string(call.Arguments), maxToolArgumentsPreviewBytes),
					ConversationID:  conversationID,
					RunID:           message.RunID,
					CreatedAt:       message.CreatedAt,
				})
			}
		case MessageRoleTool:
			output := outputPartFromContentJSON(message.ContentJSON)
			artifact, artifactSignals := toolArtifact(message.ContentJSON)
			mergeChatArtifactSignals(&artifacts, artifactSignals)
			resultJSON := formatJSONPreview(message.ContentText, maxToolResultPreviewBytes)
			item := ChatTranscriptItem{
				ID:              output.ID,
				Kind:            "tool",
				OutputOrdinal:   output.Ordinal,
				ParentMessageID: output.ParentMessageID,
				ToolCallID:      message.ToolCallID,
				Name:            message.ToolName,
				Title:           toolTitle(message.ToolName),
				Status:          "complete",
				Summary:         toolSummary(message.ContentText),
				ResultSummary:   toolSummary(message.ContentText),
				ResultJSON:      resultJSON,
				ResultFormat:    toolPreviewFormat(message.ContentText),
				Artifact:        artifact,
				ConversationID:  conversationID,
				RunID:           message.RunID,
				CreatedAt:       message.CreatedAt,
			}
			if item.ID == "" {
				item.ID = message.ID
			}
			if message.IsError {
				item.Status = "error"
				item.Error = toolErrorSummary(message.ContentText)
				item.Summary = ""
				item.ResultSummary = ""
			}
			idx, ok := toolIndexByPartID[output.ID]
			if !ok && output.ID == "" {
				idx, ok = toolIndexByCallID[message.ToolCallID]
			}
			if ok {
				items[idx] = mergeToolTranscriptItem(items[idx], item)
				continue
			}
			items = append(items, item)
		}
	}
	return ChatTranscriptState{Transcript: items, Artifacts: artifacts}
}

func turnReferencesFromContentJSON(raw string) []TurnReference {
	var payload struct {
		TurnContext TurnContext `json:"turn_context"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil
	}
	normalized := payload.TurnContext.normalized()
	if len(normalized.References) == 0 {
		return nil
	}
	references := make([]TurnReference, 0, len(normalized.References))
	for _, reference := range normalized.References {
		references = append(references, TurnReference{
			Reference:   reference.Reference,
			Name:        reference.Name,
			Description: reference.Description,
			Resource:    reference.Resource,
			Hierarchy:   append([]string(nil), reference.Hierarchy...),
			Href:        reference.Href,
			Locations:   append([]TurnReferenceLocation(nil), reference.Locations...),
			Context:     append([]string(nil), reference.Context...),
		})
	}
	return references
}

type transcriptToolCall struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Arguments       json.RawMessage `json:"arguments"`
	OutputPartID    string          `json:"output_part_id"`
	OutputOrdinal   int64           `json:"output_ordinal"`
	ParentMessageID string          `json:"parent_message_id"`
}

func (c transcriptToolCall) outputPartID() string {
	if c.OutputPartID != "" {
		return c.OutputPartID
	}
	return "tool:" + c.ID
}

type transcriptOutputPart struct {
	ID              string `json:"output_part_id"`
	Ordinal         int64  `json:"output_ordinal"`
	ParentMessageID string `json:"parent_message_id"`
}

func outputPartFromContentJSON(raw string) transcriptOutputPart {
	var payload transcriptOutputPart
	_ = json.Unmarshal([]byte(raw), &payload)
	return payload
}

func toolCallsFromContentJSON(raw string) []transcriptToolCall {
	var payload struct {
		ToolCalls []transcriptToolCall `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil
	}
	return payload.ToolCalls
}

func mergeToolTranscriptItem(started, finished ChatTranscriptItem) ChatTranscriptItem {
	if started.ID == "" {
		started.ID = finished.ID
	}
	if started.ParentMessageID == "" {
		started.ParentMessageID = finished.ParentMessageID
	}
	started.Status = finished.Status
	started.Summary = finished.Summary
	started.ResultSummary = finished.ResultSummary
	started.ResultJSON = finished.ResultJSON
	started.ResultFormat = finished.ResultFormat
	started.Artifact = finished.Artifact
	started.Error = finished.Error
	started.RunID = finished.RunID
	if started.InputJSON == "" {
		started.InputJSON = finished.InputJSON
	}
	if started.InputFormat == "" {
		started.InputFormat = finished.InputFormat
	}
	if started.ArgumentsJSON == "" {
		started.ArgumentsJSON = finished.ArgumentsJSON
	}
	if started.Name == "" {
		started.Name = finished.Name
	}
	if started.Title == "" {
		started.Title = finished.Title
	}
	return started
}

func toolArtifact(rawJSON string) (*ChatArtifact, ChatArtifactSignals) {
	raw := displayContentJSON(rawJSON)
	if raw == "" {
		return nil, emptyChatArtifactSignals()
	}
	var payload struct {
		Type    string         `json:"type"`
		ID      string         `json:"id"`
		Patch   map[string]any `json:"patch"`
		Summary string         `json:"summary"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, emptyChatArtifactSignals()
	}
	if payload.ID == "" || payload.Patch == nil {
		return nil, emptyChatArtifactSignals()
	}
	signals := emptyChatArtifactSignals()
	visuals, ok := payload.Patch["visuals"].(map[string]any)
	if payload.Type == "" || !ok {
		return nil, emptyChatArtifactSignals()
	}
	mergeMap(signals.Visuals, visuals)
	return &ChatArtifact{Type: payload.Type, ID: payload.ID, Summary: payload.Summary}, signals
}

func displayContentJSON(raw string) string {
	var payload struct {
		DisplayContent json.RawMessage `json:"display_content"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil || len(payload.DisplayContent) == 0 || string(payload.DisplayContent) == "null" {
		return ""
	}
	return string(payload.DisplayContent)
}

func emptyChatArtifactSignals() ChatArtifactSignals {
	return ChatArtifactSignals{Visuals: map[string]any{}}
}

func mergeChatArtifactSignals(target *ChatArtifactSignals, source ChatArtifactSignals) {
	if target.Visuals == nil {
		target.Visuals = map[string]any{}
	}
	mergeMap(target.Visuals, source.Visuals)
}

func mergeMap(target, source map[string]any) {
	for key, value := range source {
		target[key] = value
	}
}

func toolTitle(name string) string {
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

func formatToolCallPreview(call transcriptToolCall) string {
	payload := struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{
		Name:      call.Name,
		Arguments: "{}",
	}
	if len(call.Arguments) > 0 && json.Valid(call.Arguments) {
		payload.Arguments = string(call.Arguments)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return formatJSONPreview(string(raw), maxToolArgumentsPreviewBytes)
}

func formatJSONPreview(raw string, limit int) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || limit <= 0 {
		return ""
	}
	var indented bytes.Buffer
	if json.Valid([]byte(raw)) {
		if err := json.Indent(&indented, []byte(raw), "", "  "); err == nil {
			raw = indented.String()
		}
	}
	return truncateDisplayText(raw, limit)
}

func toolPreviewFormat(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw != "" && json.Valid([]byte(raw)) {
		return "json"
	}
	return "toon"
}

func toolSummary(raw string) string {
	payload := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return truncateDisplayText(raw, 160)
	}
	for _, key := range []string{"summary", "title", "name", "message"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return truncateDisplayText(value, 160)
		}
	}
	if total, ok := payload["total"].(float64); ok {
		return fmt.Sprintf("Returned %.0f records", total)
	}
	if count, ok := payload["count"].(float64); ok {
		return fmt.Sprintf("Returned %.0f records", count)
	}
	return ""
}

func toolErrorSummary(raw string) string {
	payload := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return truncateDisplayText(raw, 200)
	}
	if errPayload, ok := payload["error"].(map[string]any); ok {
		message, _ := errPayload["message"].(string)
		code, _ := errPayload["code"].(string)
		switch {
		case message != "" && code != "":
			return truncateDisplayText(code+": "+message, 200)
		case message != "":
			return truncateDisplayText(message, 200)
		case code != "":
			return truncateDisplayText(code, 200)
		}
	}
	return ""
}

func truncateDisplayText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	if limit <= 1 {
		return value[:limit]
	}
	return strings.TrimSpace(value[:limit-1]) + "..."
}

func platformRole(role agentcore.Role) string {
	switch role {
	case agentcore.RoleUser:
		return MessageRoleUser
	case agentcore.RoleAssistant:
		return MessageRoleAssistant
	case agentcore.RoleTool:
		return MessageRoleTool
	case agentcore.RoleSummary:
		return MessageRoleSummary
	default:
		return string(role)
	}
}
