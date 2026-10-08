package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestTruncatedTurnRecoversWithoutReplayingCompletedTools(t *testing.T) {
	calls := 0
	model := &fakeModel{responses: []ModelResponse{
		{ToolCalls: []ToolCall{{ID: "saved", Name: "save", Arguments: json.RawMessage(`{}`)}}, FinishReason: FinishReasonToolCalls},
		{ToolCalls: []ToolCall{{ID: "partial", Name: "save", Arguments: json.RawMessage(`{"value":`)}}, FinishReason: FinishReasonTruncated},
		{Content: "Finished", FinishReason: FinishReasonStop},
	}}
	a, err := New(Definition{Name: "recovery", SystemPrompt: "Help", Model: model, Limits: Limits{MaxTruncationRetries: 1}, Tools: []ToolDefinition{{Name: "save", InputSchema: json.RawMessage(`{"type":"object"}`), Handler: ToolHandlerFunc(func(context.Context, ToolCall) (ToolResult, error) {
		calls++
		return ToolResult{Content: map[string]any{"saved": true}}, nil
	})}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.Prompt(context.Background(), PromptRequest{Input: "Create dashboard"})
	if err != nil || result.StopReason != StopReasonCompleted || calls != 1 {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
	}
	if len(model.requests) != 3 || !strings.Contains(model.requests[2].SystemPrompt, "smaller") {
		t.Fatal("missing recovery instruction")
	}
	for _, m := range model.requests[2].Messages {
		if m.Role == RoleAssistant && m.Content == "" && len(m.ToolCalls) == 0 {
			t.Fatal("empty truncated response entered recovery request")
		}
		for _, call := range m.ToolCalls {
			if call.ID == "partial" {
				t.Fatal("unfinished call replayed")
			}
		}
	}
	for _, m := range a.Transcript() {
		for _, call := range m.ToolCalls {
			if call.ID == "partial" {
				t.Fatal("unfinished call persisted")
			}
		}
	}
}

func TestResumeOmitsLegacyTruncatedCalls(t *testing.T) {
	model := &fakeModel{}
	a, err := New(Definition{Name: "recovery", SystemPrompt: "Help", Model: model, InitialTranscript: []Message{
		{Role: RoleUser, Content: "Build a dashboard"},
		{Role: RoleAssistant, FinishReason: FinishReasonTruncated, ToolCalls: []ToolCall{{ID: "partial", Name: "save", Arguments: json.RawMessage(`{"value":`)}}, ProviderState: json.RawMessage(`{"unfinished":true}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Prompt(context.Background(), PromptRequest{Input: "Continue"}); err != nil {
		t.Fatal(err)
	}
	for _, message := range model.requests[0].Messages {
		if message.Role == RoleAssistant {
			t.Fatal("unfinished historical call sent to provider")
		}
	}
}

func TestTruncationRecoveryIsBoundedAndOptIn(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		retries, turns, wantCalls int
		stop                      StopReason
	}{
		{"disabled", 0, 16, 1, StopReasonTruncated},
		{"one retry", 1, 16, 2, StopReasonTruncated},
		{"turn limit", 1, 1, 1, StopReasonTruncated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &fakeModel{responses: []ModelResponse{{FinishReason: FinishReasonTruncated}, {FinishReason: FinishReasonTruncated}}}
			a, err := New(Definition{Name: "recovery", SystemPrompt: "Help", Model: model, Limits: Limits{MaxTruncationRetries: tc.retries, MaxTurns: tc.turns}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := a.Prompt(context.Background(), PromptRequest{Input: "Create dashboard"})
			if err != nil || result.StopReason != tc.stop || len(model.requests) != tc.wantCalls {
				t.Fatalf("result=%+v requests=%d err=%v", result, len(model.requests), err)
			}
		})
	}
}
