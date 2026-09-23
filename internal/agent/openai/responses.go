package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	agentapp "github.com/flidai/leapview/internal/agent"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

func usesGPT6LunaResponses(config agentapp.Config) bool {
	model := strings.ToLower(strings.TrimSpace(config.Model))
	if slash := strings.LastIndexByte(model, '/'); slash >= 0 {
		model = model[slash+1:]
	}
	return model == "gpt-6-luna" || strings.HasPrefix(model, "gpt-6-luna-")
}

func (m *OpenAIModel) completeResponse(ctx context.Context, req agentcore.ModelRequest, stream agentcore.ModelStream) (agentcore.ModelResponse, error) {
	streaming := req.Purpose == agentcore.ModelRequestPurposeTurn && stream != nil
	body := openAIResponsesRequest{
		Model:           m.config.Model,
		Input:           openAIResponseInput(req.Messages),
		Tools:           openAIResponseTools(req.Tools),
		Include:         []string{"reasoning.encrypted_content"},
		MaxOutputTokens: req.Limits.ReserveOutputTokens,
		Reasoning:       openAIResponseReasoning{Effort: m.config.NormalizedReasoningEffort()},
		Stream:          streaming,
		Store:           false,
	}
	if len(body.Tools) > 0 {
		body.ToolChoice = "auto"
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return agentcore.ModelResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.config.NormalizedBaseURL()+"/responses", bytes.NewReader(payload))
	if err != nil {
		return agentcore.ModelResponse{}, err
	}
	if streaming {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+m.config.APIKey)

	resp, err := m.client.Do(httpReq)
	if err != nil {
		return agentcore.ModelResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		if isContextLimitResponse(resp.StatusCode, string(body)) {
			return agentcore.ModelResponse{}, agentcore.ErrContextLength
		}
		return agentcore.ModelResponse{}, fmt.Errorf("response creation failed: status=%d", resp.StatusCode)
	}
	if streaming && strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return m.completeResponseStream(ctx, resp.Body, stream)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return agentcore.ModelResponse{}, err
	}
	var decoded openAIResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return agentcore.ModelResponse{}, err
	}
	out, err := modelResponseFromResponse(decoded, m.config.Model)
	if err != nil {
		return agentcore.ModelResponse{}, err
	}
	if streaming && out.Content != "" {
		if err := stream.Delta(ctx, out.Content); err != nil {
			return agentcore.ModelResponse{}, err
		}
	}
	return out, nil
}

func (m *OpenAIModel) completeResponseStream(ctx context.Context, body io.Reader, stream agentcore.ModelStream) (agentcore.ModelResponse, error) {
	reader := bufio.NewReader(body)
	content := strings.Builder{}
	toolCalls := make(map[int]*openAIResponseFunctionCall)
	var completed openAIResponse
	seenTerminal := false

streamLoop:
	for {
		data, err := readSSEData(reader)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return agentcore.ModelResponse{}, err
		}
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var event openAIResponseStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return agentcore.ModelResponse{}, err
		}
		switch event.Type {
		case "response.output_text.delta":
			content.WriteString(event.Delta)
			if err := stream.Delta(ctx, event.Delta); err != nil {
				return agentcore.ModelResponse{}, err
			}
		case "response.output_item.added":
			if event.Item.Type == "function_call" {
				call := event.Item
				toolCalls[event.OutputIndex] = &call
			}
		case "response.function_call_arguments.delta":
			call := toolCalls[event.OutputIndex]
			if call == nil {
				call = &openAIResponseFunctionCall{}
				toolCalls[event.OutputIndex] = call
			}
			call.Arguments += event.Delta
		case "response.output_item.done":
			if event.Item.Type == "function_call" {
				call := event.Item
				toolCalls[event.OutputIndex] = &call
			}
		case "response.completed", "response.incomplete":
			completed = event.Response
			seenTerminal = true
			break streamLoop
		case "response.failed", "error":
			return agentcore.ModelResponse{}, errors.New("responses stream returned provider error")
		}
	}
	if !seenTerminal {
		return agentcore.ModelResponse{}, fmt.Errorf("responses stream ended unexpectedly: %w", io.ErrUnexpectedEOF)
	}
	if completed.Status == "failed" || completed.Error.Code != "" {
		return agentcore.ModelResponse{}, errors.New("responses request failed")
	}
	if content.Len() == 0 {
		fallback := responseOutputText(completed.Output)
		if fallback != "" {
			content.WriteString(fallback)
			if err := stream.Delta(ctx, fallback); err != nil {
				return agentcore.ModelResponse{}, err
			}
		}
	}
	mergeResponseToolCalls(toolCalls, completed.Output)
	out := agentcore.ModelResponse{
		Content:       content.String(),
		FinishReason:  responseFinishReason(completed.Status, len(toolCalls)),
		ProviderState: responseProviderState(completed.Output),
		Usage: agentcore.Usage{
			InputTokens:  completed.Usage.InputTokens,
			OutputTokens: completed.Usage.OutputTokens,
			TotalTokens:  completed.Usage.TotalTokens,
		},
		ProviderMetadata: map[string]any{"id": completed.ID, "model": m.config.Model},
	}
	indices := make([]int, 0, len(toolCalls))
	for index := range toolCalls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		call := toolCalls[index]
		out.ToolCalls = append(out.ToolCalls, agentcore.ToolCall{ID: call.CallID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)})
	}
	return out, nil
}

func openAIResponseInput(messages []agentcore.Message) []openAIResponseInputItem {
	items := make([]openAIResponseInputItem, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case agentcore.RoleTool:
			items = append(items, openAIResponseInputItem{Type: "function_call_output", CallID: message.ToolCallID, Output: message.Content})
		case agentcore.RoleAssistant:
			if replay := openAIResponseReplayInput(message.ProviderState); len(replay) > 0 {
				items = append(items, replay...)
				continue
			}
			if message.Content != "" {
				items = append(items, openAIResponseInputItem{Type: "message", Role: "assistant", Content: message.Content})
			}
			for _, call := range message.ToolCalls {
				items = append(items, openAIResponseInputItem{Type: "function_call", CallID: call.ID, Name: call.Name, Arguments: string(call.Arguments)})
			}
		default:
			role := string(message.Role)
			if message.Role == agentcore.RoleSummary {
				role = "system"
			}
			items = append(items, openAIResponseInputItem{Type: "message", Role: role, Content: message.Content})
		}
	}
	return items
}

func openAIResponseTools(tools []agentcore.ToolSpec) []openAIResponseTool {
	out := make([]openAIResponseTool, 0, len(tools))
	for _, tool := range tools {
		parameters := json.RawMessage(`{"type":"object"}`)
		if len(tool.InputSchema) > 0 {
			parameters = compactJSONSchema(tool.InputSchema)
		}
		out = append(out, openAIResponseTool{Type: "function", Name: tool.Name, Description: tool.Description, Parameters: parameters, Strict: false})
	}
	return out
}

func modelResponseFromResponse(decoded openAIResponse, model string) (agentcore.ModelResponse, error) {
	if decoded.Status == "failed" || decoded.Error.Code != "" {
		return agentcore.ModelResponse{}, errors.New("responses request failed")
	}
	out := agentcore.ModelResponse{
		Content:       responseOutputText(decoded.Output),
		FinishReason:  responseFinishReason(decoded.Status, countResponseToolCalls(decoded.Output)),
		ProviderState: responseProviderState(decoded.Output),
		Usage: agentcore.Usage{
			InputTokens:  decoded.Usage.InputTokens,
			OutputTokens: decoded.Usage.OutputTokens,
			TotalTokens:  decoded.Usage.TotalTokens,
		},
		ProviderMetadata: map[string]any{"id": decoded.ID, "model": model},
	}
	for _, item := range decoded.Output {
		if item.Type != "function_call" {
			continue
		}
		out.ToolCalls = append(out.ToolCalls, agentcore.ToolCall{ID: item.CallID, Name: item.Name, Arguments: json.RawMessage(item.Arguments)})
	}
	return out, nil
}

func responseProviderState(output []openAIResponseOutputItem) json.RawMessage {
	if len(output) == 0 {
		return nil
	}
	state, err := json.Marshal(output)
	if err != nil {
		return nil
	}
	return state
}

func openAIResponseReplayInput(state json.RawMessage) []openAIResponseInputItem {
	if len(state) == 0 {
		return nil
	}
	var rawItems []json.RawMessage
	if err := json.Unmarshal(state, &rawItems); err != nil {
		return nil
	}
	items := make([]openAIResponseInputItem, 0, len(rawItems))
	for _, raw := range rawItems {
		var item openAIResponseInputItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil
		}
		item.raw = append(json.RawMessage(nil), raw...)
		items = append(items, item)
	}
	return items
}

func responseOutputText(output []openAIResponseOutputItem) string {
	var content strings.Builder
	for _, item := range output {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			if part.Type == "output_text" {
				content.WriteString(part.Text)
			}
		}
	}
	return content.String()
}

func countResponseToolCalls(output []openAIResponseOutputItem) int {
	count := 0
	for _, item := range output {
		if item.Type == "function_call" {
			count++
		}
	}
	return count
}

func mergeResponseToolCalls(calls map[int]*openAIResponseFunctionCall, output []openAIResponseOutputItem) {
	for index, item := range output {
		if item.Type != "function_call" {
			continue
		}
		call := &openAIResponseFunctionCall{Type: item.Type, CallID: item.CallID, Name: item.Name, Arguments: item.Arguments}
		calls[index] = call
	}
}

func responseFinishReason(status string, toolCalls int) agentcore.FinishReason {
	if toolCalls > 0 {
		return agentcore.FinishReasonToolCalls
	}
	if status == "incomplete" {
		return agentcore.FinishReasonTruncated
	}
	if status == "completed" {
		return agentcore.FinishReasonStop
	}
	return agentcore.FinishReasonUnknown
}

type openAIResponsesRequest struct {
	Model           string                    `json:"model"`
	Input           []openAIResponseInputItem `json:"input"`
	Tools           []openAIResponseTool      `json:"tools,omitempty"`
	Include         []string                  `json:"include,omitempty"`
	ToolChoice      string                    `json:"tool_choice,omitempty"`
	MaxOutputTokens int                       `json:"max_output_tokens,omitempty"`
	Reasoning       openAIResponseReasoning   `json:"reasoning"`
	Stream          bool                      `json:"stream,omitempty"`
	Store           bool                      `json:"store"`
}

type openAIResponseReasoning struct {
	Effort string `json:"effort"`
}

type openAIResponseInputItem struct {
	Type             string `json:"type"`
	ID               string `json:"id,omitempty"`
	Role             string `json:"role,omitempty"`
	Content          string `json:"content,omitempty"`
	CallID           string `json:"call_id,omitempty"`
	Name             string `json:"name,omitempty"`
	Arguments        string `json:"arguments,omitempty"`
	Output           string `json:"output,omitempty"`
	EncryptedContent string `json:"encrypted_content,omitempty"`
	raw              json.RawMessage
}

func (i openAIResponseInputItem) MarshalJSON() ([]byte, error) {
	if len(i.raw) > 0 {
		return i.raw, nil
	}
	type itemAlias openAIResponseInputItem
	return json.Marshal(itemAlias(i))
}

func (i *openAIResponseInputItem) UnmarshalJSON(data []byte) error {
	type itemAlias openAIResponseInputItem
	var decoded itemAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*i = openAIResponseInputItem(decoded)
	i.raw = append(json.RawMessage(nil), data...)
	return nil
}

type openAIResponseTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

type openAIResponse struct {
	ID     string                     `json:"id"`
	Status string                     `json:"status"`
	Output []openAIResponseOutputItem `json:"output"`
	Usage  openAIResponseUsage        `json:"usage"`
	Error  openAIResponseError        `json:"error"`
}

type openAIResponseOutputItem struct {
	Type             string                     `json:"type"`
	ID               string                     `json:"id,omitempty"`
	CallID           string                     `json:"call_id"`
	Name             string                     `json:"name"`
	Arguments        string                     `json:"arguments"`
	Content          []openAIResponseOutputPart `json:"content"`
	EncryptedContent string                     `json:"encrypted_content,omitempty"`
	Summary          json.RawMessage            `json:"summary,omitempty"`
	raw              json.RawMessage
}

func (i openAIResponseOutputItem) MarshalJSON() ([]byte, error) {
	if len(i.raw) > 0 {
		return i.raw, nil
	}
	type itemAlias openAIResponseOutputItem
	return json.Marshal(itemAlias(i))
}

func (i *openAIResponseOutputItem) UnmarshalJSON(data []byte) error {
	type itemAlias openAIResponseOutputItem
	var decoded itemAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*i = openAIResponseOutputItem(decoded)
	i.raw = append(json.RawMessage(nil), data...)
	return nil
}

type openAIResponseOutputPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type openAIResponseUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type openAIResponseError struct {
	Code string `json:"code"`
}

type openAIResponseFunctionCall struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIResponseStreamEvent struct {
	Type        string                     `json:"type"`
	Delta       string                     `json:"delta"`
	OutputIndex int                        `json:"output_index"`
	Item        openAIResponseFunctionCall `json:"item"`
	Response    openAIResponse             `json:"response"`
}
