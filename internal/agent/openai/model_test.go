package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentapp "github.com/flidai/leapview/internal/agent"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

func TestNewModelUsesBoundedDefaultHTTPClient(t *testing.T) {
	model := NewModel(agentapp.Config{APIKey: "test-key", BaseURL: "https://api.example.com", Model: "test-model"}, nil)
	if model.client == nil {
		t.Fatal("default HTTP client is nil")
	}
	if model.client == http.DefaultClient {
		t.Fatal("default HTTP client should not use the unbounded process-global client")
	}
	if model.client.Timeout != DefaultHTTPTimeout {
		t.Fatalf("default HTTP timeout = %s, want %s", model.client.Timeout, DefaultHTTPTimeout)
	}
}

func TestOpenAIModelConvertsChatCompletionPayloads(t *testing.T) {
	var got openAIChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %s, want /chat/completions", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeJSON(t, w, openAIChatResponse{
			ID: "chatcmpl_test",
			Choices: []openAIChoice{{
				Index: 0,
				Message: openAIMessage{
					Role:    "assistant",
					Content: "I will check.",
					ToolCalls: []openAIToolCall{{
						ID:   "call_1",
						Type: "function",
						Function: openAIFunctionCall{
							Name:      "list_dashboards",
							Arguments: `{}`,
						},
					}},
				},
				FinishReason: "tool_calls",
			}},
			Usage: openAIUsage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18},
		})
	}))
	defer server.Close()

	model := NewModel(agentapp.Config{APIKey: "test-key", BaseURL: server.URL, Model: "deepseek-v4-flash"}, server.Client())
	resp, err := model.Complete(context.Background(), agentcore.ModelRequest{
		Purpose: agentcore.ModelRequestPurposeTurn,
		Messages: []agentcore.Message{
			{Role: agentcore.RoleSystem, Content: "system"},
			{Role: agentcore.RoleUser, Content: "hello"},
			{Role: agentcore.RoleTool, ToolCallID: "call_previous", ToolName: "list_dashboards", Content: `{"dashboards":[]}`},
		},
		Tools: []agentcore.ToolSpec{{
			Name:        "list_dashboards",
			Description: "List dashboards.",
			InputSchema: []byte(`{"type":"object","additionalProperties":false}`),
		}},
		Limits: agentcore.Limits{ReserveOutputTokens: 123},
	}, nil)
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if got.Model != "deepseek-v4-flash" || got.MaxTokens != 123 {
		t.Fatalf("request model/max = %s/%d", got.Model, got.MaxTokens)
	}
	if got.Thinking == nil || got.Thinking.Type != "disabled" {
		t.Fatalf("deepseek v4 request should disable thinking: %#v", got.Thinking)
	}
	if len(got.Messages) != 3 || got.Messages[2].Role != "tool" || got.Messages[2].ToolCallID != "call_previous" {
		t.Fatalf("messages = %#v", got.Messages)
	}
	if len(got.Tools) != 1 || got.Tools[0].Function.Name != "list_dashboards" {
		t.Fatalf("tools = %#v", got.Tools)
	}
	if resp.Content != "I will check." || resp.FinishReason != agentcore.FinishReasonToolCalls {
		t.Fatalf("response = %#v", resp)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "list_dashboards" {
		t.Fatalf("tool calls = %#v", resp.ToolCalls)
	}
	if resp.Usage.TotalTokens != 18 {
		t.Fatalf("usage = %#v", resp.Usage)
	}
	if resp.ProviderMetadata["id"] != "chatcmpl_test" || resp.ProviderMetadata["model"] != "deepseek-v4-flash" {
		t.Fatalf("metadata = %#v", resp.ProviderMetadata)
	}
}

func TestOpenAIModelStreamsFirstTokenBeforeCompletion(t *testing.T) {
	firstEventWritten := make(chan struct{})
	releaseServer := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got openAIChatRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if !got.Stream {
			t.Error("streaming turn request did not set stream=true")
		}
		if got.StreamOptions == nil || !got.StreamOptions.IncludeUsage {
			t.Errorf("stream options = %#v, want include_usage", got.StreamOptions)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("test response does not support flushing")
		}
		io.WriteString(w, "data: {\"id\":\"chatcmpl_stream\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"first\"},\"finish_reason\":null}]}\n\n")
		flusher.Flush()
		close(firstEventWritten)
		<-releaseServer
		io.WriteString(w, "data: {\"id\":\"chatcmpl_stream\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" second\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()
	defer func() {
		select {
		case <-releaseServer:
		default:
			close(releaseServer)
		}
	}()

	model := NewModel(agentapp.Config{APIKey: "test-key", BaseURL: server.URL, Model: "test-model"}, server.Client())
	deltas := make(chan string, 1)
	result := make(chan struct {
		response agentcore.ModelResponse
		err      error
	}, 1)
	go func() {
		response, err := model.Complete(context.Background(), agentcore.ModelRequest{
			Purpose: agentcore.ModelRequestPurposeTurn,
		}, modelStreamFunc(func(_ context.Context, text string) error {
			deltas <- text
			return nil
		}))
		result <- struct {
			response agentcore.ModelResponse
			err      error
		}{response: response, err: err}
	}()

	select {
	case <-firstEventWritten:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not write first event")
	}
	select {
	case delta := <-deltas:
		if delta != "first" {
			t.Fatalf("first delta = %q, want %q", delta, "first")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first delta was not delivered before completion")
	}
	select {
	case <-result:
		t.Fatal("Complete returned before the server finished the stream")
	default:
	}
	close(releaseServer)

	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("Complete returned error: %v", got.err)
		}
		if got.response.Content != "first second" || got.response.FinishReason != agentcore.FinishReasonStop {
			t.Fatalf("response = %#v", got.response)
		}
		if got.response.Usage.TotalTokens != 5 {
			t.Fatalf("usage = %#v", got.response.Usage)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Complete did not finish")
	}
}

func TestOpenAIModelAccumulatesFragmentedToolCallDeltasAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got openAIChatRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if !got.Stream {
			t.Error("streaming turn request did not set stream=true")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeFragments := func(event string) {
			for _, fragment := range []string{event[:len(event)/3], event[len(event)/3 : 2*len(event)/3], event[2*len(event)/3:]} {
				_, _ = io.WriteString(w, fragment)
				flusher.Flush()
			}
		}
		writeFragments("data: {\"id\":\"chatcmpl_tool\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"city\\\":\\\"Ber\"}}]},\"finish_reason\":null}]}\n\n")
		writeFragments("data: {\"id\":\"chatcmpl_tool\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"lin\\\"\"}}]},\"finish_reason\":null}]}\n\n")
		writeFragments("data: {\"id\":\"chatcmpl_tool\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
		writeFragments("data: {\"id\":\"chatcmpl_tool\",\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":9,\"total_tokens\":21}}\n\n")
		writeFragments("data: [DONE]\n\n")
	}))
	defer server.Close()

	model := NewModel(agentapp.Config{APIKey: "test-key", BaseURL: server.URL, Model: "test-model"}, server.Client())
	response, err := model.Complete(context.Background(), agentcore.ModelRequest{
		Purpose: agentcore.ModelRequestPurposeTurn,
	}, modelStreamFunc(func(context.Context, string) error { return nil }))
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if response.ProviderMetadata["id"] != "chatcmpl_tool" {
		t.Fatalf("metadata = %#v", response.ProviderMetadata)
	}
	if response.FinishReason != agentcore.FinishReasonToolCalls {
		t.Fatalf("finish reason = %q", response.FinishReason)
	}
	if len(response.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v", response.ToolCalls)
	}
	call := response.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "lookup" || string(call.Arguments) != `{"city":"Berlin"}` {
		t.Fatalf("tool call = %#v", call)
	}
	if response.Usage.InputTokens != 12 || response.Usage.OutputTokens != 9 || response.Usage.TotalTokens != 21 {
		t.Fatalf("usage = %#v", response.Usage)
	}
}

func TestOpenAIModelFallsBackToJSONWhenStreamingIsIgnored(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got openAIChatRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if !got.Stream {
			t.Error("turn request did not ask for streaming")
		}
		writeJSON(t, w, openAIChatResponse{
			ID: "chatcmpl_json_fallback",
			Choices: []openAIChoice{{
				Message:      openAIMessage{Role: "assistant", Content: "fallback"},
				FinishReason: "stop",
			}},
			Usage: openAIUsage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3},
		})
	}))
	defer server.Close()

	model := NewModel(agentapp.Config{APIKey: "test-key", BaseURL: server.URL, Model: "test-model"}, server.Client())
	deltas := make(chan string, 1)
	response, err := model.Complete(context.Background(), agentcore.ModelRequest{Purpose: agentcore.ModelRequestPurposeTurn}, modelStreamFunc(func(_ context.Context, text string) error {
		deltas <- text
		return nil
	}))
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if response.Content != "fallback" || response.Usage.TotalTokens != 3 {
		t.Fatalf("response = %#v", response)
	}
	if delta := <-deltas; delta != "fallback" {
		t.Fatalf("delta = %q, want fallback", delta)
	}
}

func TestOpenAIModelRejectsMidstreamProviderErrorWithoutLeakingBody(t *testing.T) {
	const marker = "provider-secret-stream-error-marker"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_error\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"error\"}],\"error\":{\"message\":\""+marker+"\"}}\n\n")
	}))
	defer server.Close()

	model := NewModel(agentapp.Config{APIKey: "test-key", BaseURL: server.URL, Model: "test-model"}, server.Client())
	_, err := model.Complete(context.Background(), agentcore.ModelRequest{Purpose: agentcore.ModelRequestPurposeTurn}, modelStreamFunc(func(context.Context, string) error { return nil }))
	if err == nil {
		t.Fatal("Complete returned nil error")
	}
	if !strings.Contains(err.Error(), "provider error") {
		t.Fatalf("error = %q, want generic provider error", err)
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatalf("error exposed provider response body: %q", err)
	}
}

func TestOpenAIModelRejectsUnexpectedStreamEOF(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_partial\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	defer server.Close()

	model := NewModel(agentapp.Config{APIKey: "test-key", BaseURL: server.URL, Model: "test-model"}, server.Client())
	_, err := model.Complete(context.Background(), agentcore.ModelRequest{Purpose: agentcore.ModelRequestPurposeTurn}, modelStreamFunc(func(context.Context, string) error { return nil }))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v, want unexpected EOF", err)
	}
}

func TestOpenAIModelDoesNotExposeProviderErrorBody(t *testing.T) {
	const marker = "provider-secret-error-marker"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, marker, http.StatusBadGateway)
	}))
	defer server.Close()

	model := NewModel(agentapp.Config{APIKey: "test-key", BaseURL: server.URL, Model: "test-model"}, server.Client())
	_, err := model.Complete(context.Background(), agentcore.ModelRequest{}, nil)
	if err == nil {
		t.Fatal("Complete returned nil error")
	}
	if !strings.Contains(err.Error(), "status=502") {
		t.Fatalf("error = %q, want provider status", err)
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatalf("error exposed provider response body: %q", err)
	}
}

func TestOpenAIModelPreservesContextLimitDetection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "maximum context length exceeded", http.StatusBadRequest)
	}))
	defer server.Close()

	model := NewModel(agentapp.Config{APIKey: "test-key", BaseURL: server.URL, Model: "test-model"}, server.Client())
	_, err := model.Complete(context.Background(), agentcore.ModelRequest{}, nil)
	if !errors.Is(err, agentcore.ErrContextLength) {
		t.Fatalf("error = %v, want context length error", err)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

type modelStreamFunc func(context.Context, string) error

func (f modelStreamFunc) Delta(ctx context.Context, text string) error {
	return f(ctx, text)
}
