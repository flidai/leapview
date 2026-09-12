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
	"time"

	agentapp "github.com/flidai/leapview/internal/agent"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

const DefaultHTTPTimeout = 5 * time.Minute

type OpenAIModel struct {
	config agentapp.Config
	client *http.Client
}

func NewModel(config agentapp.Config, client *http.Client) *OpenAIModel {
	if client == nil {
		client = &http.Client{Timeout: DefaultHTTPTimeout}
	}
	return &OpenAIModel{config: config, client: client}
}

func (m *OpenAIModel) Complete(ctx context.Context, req agentcore.ModelRequest, stream agentcore.ModelStream) (agentcore.ModelResponse, error) {
	if !m.config.Enabled() {
		return agentcore.ModelResponse{}, agentapp.ErrDisabled
	}
	streaming := req.Purpose == agentcore.ModelRequestPurposeTurn && stream != nil
	body := openAIChatRequest{
		Model:     m.config.Model,
		Messages:  openAIMessages(req.Messages),
		Tools:     openAITools(req.Tools),
		MaxTokens: req.Limits.ReserveOutputTokens,
		Stream:    streaming,
	}
	if streaming {
		body.StreamOptions = &openAIStreamOptions{IncludeUsage: true}
	}
	if disableThinkingForRequest(m.config) {
		body.Thinking = &openAIThinking{Type: "disabled"}
	}
	if len(body.Tools) > 0 {
		body.ToolChoice = "auto"
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return agentcore.ModelResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.config.NormalizedBaseURL()+"/chat/completions", bytes.NewReader(payload))
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
		bytes, _ := io.ReadAll(resp.Body)
		if isContextLimitResponse(resp.StatusCode, string(bytes)) {
			return agentcore.ModelResponse{}, agentcore.ErrContextLength
		}
		return agentcore.ModelResponse{}, fmt.Errorf("chat completion failed: status=%d", resp.StatusCode)
	}
	if streaming {
		if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
			bytes, err := io.ReadAll(resp.Body)
			if err != nil {
				return agentcore.ModelResponse{}, err
			}
			var decoded openAIChatResponse
			if err := json.Unmarshal(bytes, &decoded); err != nil {
				return agentcore.ModelResponse{}, err
			}
			if len(decoded.Choices) == 0 {
				return agentcore.ModelResponse{}, errors.New("chat completion returned no choices")
			}
			out := modelResponseFromChatResponse(decoded, m.config.Model)
			if out.FinishReason == agentcore.FinishReasonUnknown && len(out.ToolCalls) > 0 {
				out.FinishReason = agentcore.FinishReasonToolCalls
			}
			if out.Content != "" {
				if err := stream.Delta(ctx, out.Content); err != nil {
					return agentcore.ModelResponse{}, err
				}
			}
			return out, nil
		}
		return m.completeStream(ctx, resp.Body, stream)
	}
	bytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return agentcore.ModelResponse{}, err
	}
	var decoded openAIChatResponse
	if err := json.Unmarshal(bytes, &decoded); err != nil {
		return agentcore.ModelResponse{}, err
	}
	if len(decoded.Choices) == 0 {
		return agentcore.ModelResponse{}, errors.New("chat completion returned no choices")
	}
	out := modelResponseFromChatResponse(decoded, m.config.Model)
	if out.FinishReason == agentcore.FinishReasonUnknown && len(out.ToolCalls) > 0 {
		out.FinishReason = agentcore.FinishReasonToolCalls
	}
	if req.Purpose == agentcore.ModelRequestPurposeTurn && out.Content != "" && stream != nil {
		_ = stream.Delta(ctx, out.Content)
	}
	return out, nil
}

func (m *OpenAIModel) completeStream(ctx context.Context, body io.Reader, stream agentcore.ModelStream) (agentcore.ModelResponse, error) {
	reader := bufio.NewReader(body)
	content := strings.Builder{}
	toolCalls := make(map[int]*openAIStreamToolCall)
	selectedChoice := -1
	seenChoice := false
	seenDone := false
	var id string
	var finishReason string
	var usage openAIUsage

	for {
		data, err := readSSEData(reader)
		if errors.Is(err, io.EOF) {
			if !seenDone {
				return agentcore.ModelResponse{}, fmt.Errorf("chat completion stream ended unexpectedly: %w", io.ErrUnexpectedEOF)
			}
			break
		}
		if err != nil {
			return agentcore.ModelResponse{}, err
		}
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			seenDone = true
			break
		}

		var chunk openAIChatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return agentcore.ModelResponse{}, err
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return agentcore.ModelResponse{}, errors.New("chat completion stream returned provider error")
		}
		if chunk.ID != "" {
			id = chunk.ID
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != nil && strings.EqualFold(*choice.FinishReason, "error") {
				return agentcore.ModelResponse{}, errors.New("chat completion stream returned provider error")
			}
			if !seenChoice {
				selectedChoice = choice.Index
				seenChoice = true
			}
			if choice.Index != selectedChoice {
				continue
			}
			if choice.Delta.Content != "" {
				content.WriteString(choice.Delta.Content)
				if err := stream.Delta(ctx, choice.Delta.Content); err != nil {
					return agentcore.ModelResponse{}, err
				}
			}
			for _, delta := range choice.Delta.ToolCalls {
				call := toolCalls[delta.Index]
				if call == nil {
					call = &openAIStreamToolCall{}
					toolCalls[delta.Index] = call
				}
				if delta.ID != "" {
					call.ID = delta.ID
				}
				if delta.Type != "" {
					call.Type = delta.Type
				}
				if delta.Function.Name != "" {
					call.Name = delta.Function.Name
				}
				call.Arguments.WriteString(delta.Function.Arguments)
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finishReason = *choice.FinishReason
			}
		}
	}

	if !seenChoice {
		return agentcore.ModelResponse{}, errors.New("chat completion returned no choices")
	}
	out := agentcore.ModelResponse{
		Content:      content.String(),
		FinishReason: agentcore.NormalizeFinishReason(agentcore.FinishReason(finishReason)),
		Usage: agentcore.Usage{
			InputTokens:  usage.PromptTokens,
			OutputTokens: usage.CompletionTokens,
			TotalTokens:  usage.TotalTokens,
		},
		ProviderMetadata: map[string]any{
			"id":    id,
			"model": m.config.Model,
		},
	}
	indices := make([]int, 0, len(toolCalls))
	for index := range toolCalls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		call := toolCalls[index]
		if call.Type != "" && call.Type != "function" {
			continue
		}
		out.ToolCalls = append(out.ToolCalls, agentcore.ToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: json.RawMessage(call.Arguments.String()),
		})
	}
	if out.FinishReason == agentcore.FinishReasonUnknown && len(out.ToolCalls) > 0 {
		out.FinishReason = agentcore.FinishReasonToolCalls
	}
	return out, nil
}

func readSSEData(reader *bufio.Reader) (string, error) {
	var data []string
	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(data) > 0 {
				return strings.Join(data, "\n"), nil
			}
		} else if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
			data = append(data, value)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(data) > 0 {
					return strings.Join(data, "\n"), nil
				}
				return "", io.EOF
			}
			return "", err
		}
	}
}

func modelResponseFromChatResponse(decoded openAIChatResponse, model string) agentcore.ModelResponse {
	choice := decoded.Choices[0]
	out := agentcore.ModelResponse{
		Content:      choice.Message.Content,
		FinishReason: agentcore.NormalizeFinishReason(agentcore.FinishReason(choice.FinishReason)),
		Usage: agentcore.Usage{
			InputTokens:  decoded.Usage.PromptTokens,
			OutputTokens: decoded.Usage.CompletionTokens,
			TotalTokens:  decoded.Usage.TotalTokens,
		},
		ProviderMetadata: map[string]any{
			"id":    decoded.ID,
			"model": model,
		},
	}
	for _, call := range choice.Message.ToolCalls {
		if call.Type != "" && call.Type != "function" {
			continue
		}
		out.ToolCalls = append(out.ToolCalls, agentcore.ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: json.RawMessage(call.Function.Arguments),
		})
	}
	return out
}

func openAIMessages(messages []agentcore.Message) []openAIMessage {
	out := make([]openAIMessage, 0, len(messages))
	for _, message := range messages {
		msg := openAIMessage{
			Role:       string(message.Role),
			Content:    message.Content,
			ToolCallID: message.ToolCallID,
		}
		for _, call := range message.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, openAIToolCall{
				ID:   call.ID,
				Type: "function",
				Function: openAIFunctionCall{
					Name:      call.Name,
					Arguments: string(call.Arguments),
				},
			})
		}
		out = append(out, msg)
	}
	return out
}

func openAITools(tools []agentcore.ToolSpec) []openAITool {
	out := make([]openAITool, 0, len(tools))
	for _, tool := range tools {
		params := json.RawMessage(`{"type":"object"}`)
		if len(tool.InputSchema) > 0 {
			params = compactJSONSchema(tool.InputSchema)
		}
		out = append(out, openAITool{
			Type: "function",
			Function: openAIToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  params,
			},
		})
	}
	return out
}

func isContextLimitResponse(status int, body string) bool {
	if status != http.StatusBadRequest && status != http.StatusRequestEntityTooLarge {
		return false
	}
	body = strings.ToLower(body)
	return strings.Contains(body, "context") || strings.Contains(body, "maximum context") || strings.Contains(body, "token")
}

// DeepSeek V4 enables reasoning by default; short metadata calls need normal content.
func disableThinkingForRequest(config agentapp.Config) bool {
	model := strings.ToLower(strings.TrimSpace(config.Model))
	return strings.HasPrefix(model, "deepseek-v4")
}

type openAIChatRequest struct {
	Model         string               `json:"model"`
	Messages      []openAIMessage      `json:"messages"`
	Tools         []openAITool         `json:"tools,omitempty"`
	ToolChoice    string               `json:"tool_choice,omitempty"`
	MaxTokens     int                  `json:"max_tokens,omitempty"`
	Thinking      *openAIThinking      `json:"thinking,omitempty"`
	Stream        bool                 `json:"stream,omitempty"`
	StreamOptions *openAIStreamOptions `json:"stream_options,omitempty"`
}

type openAIThinking struct {
	Type string `json:"type"`
}

type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAITool struct {
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIChatResponse struct {
	ID      string         `json:"id"`
	Choices []openAIChoice `json:"choices"`
	Usage   openAIUsage    `json:"usage"`
}

type openAIChatStreamChunk struct {
	ID      string               `json:"id"`
	Choices []openAIStreamChoice `json:"choices"`
	Usage   *openAIUsage         `json:"usage"`
	Error   json.RawMessage      `json:"error"`
}

type openAIStreamChoice struct {
	Index        int                `json:"index"`
	Delta        openAIMessageDelta `json:"delta"`
	FinishReason *string            `json:"finish_reason"`
}

type openAIMessageDelta struct {
	Content   string                `json:"content"`
	ToolCalls []openAIToolCallDelta `json:"tool_calls"`
}

type openAIToolCallDelta struct {
	Index    int                `json:"index"`
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

type openAIStreamToolCall struct {
	ID        string
	Type      string
	Name      string
	Arguments strings.Builder
}

type openAIChoice struct {
	Index        int           `json:"index"`
	Message      openAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
