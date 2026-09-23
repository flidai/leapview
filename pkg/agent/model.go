package agent

import (
	"context"
	"encoding/json"
)

type Model interface {
	Complete(ctx context.Context, req ModelRequest, stream ModelStream) (ModelResponse, error)
}

type ModelFunc func(ctx context.Context, req ModelRequest, stream ModelStream) (ModelResponse, error)

func (f ModelFunc) Complete(ctx context.Context, req ModelRequest, stream ModelStream) (ModelResponse, error) {
	return f(ctx, req, stream)
}

type ModelStream interface {
	Delta(ctx context.Context, text string) error
}

type ModelRequestPurpose string

const (
	ModelRequestPurposeTurn       ModelRequestPurpose = "turn"
	ModelRequestPurposeCompaction ModelRequestPurpose = "compaction"
)

type ModelRequest struct {
	Purpose       ModelRequestPurpose
	RunID         string
	TurnID        string
	CorrelationID string
	SystemPrompt  string
	Messages      []Message
	Tools         []ToolSpec
	Limits        Limits
}

type ModelResponse struct {
	Content          string
	ToolCalls        []ToolCall
	FinishReason     FinishReason
	Usage            Usage
	ProviderMetadata map[string]any
	ProviderState    json.RawMessage
}

type FinishReason string

const (
	FinishReasonStop          FinishReason = "stop"
	FinishReasonToolCalls     FinishReason = "tool_calls"
	FinishReasonTruncated     FinishReason = "truncated"
	FinishReasonContentFilter FinishReason = "content_filter"
	FinishReasonUnknown       FinishReason = "unknown"
)

func NormalizeFinishReason(reason FinishReason) FinishReason {
	switch reason {
	case FinishReasonStop, FinishReasonToolCalls, FinishReasonTruncated, FinishReasonContentFilter:
		return reason
	case "length":
		return FinishReasonTruncated
	default:
		return FinishReasonUnknown
	}
}

type Usage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
	TotalTokens  int `json:"total_tokens,omitempty"`
}

type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type noopModelStream struct{}

func (noopModelStream) Delta(context.Context, string) error { return nil }

func cloneMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for k, v := range metadata {
		out[k] = v
	}
	return out
}
