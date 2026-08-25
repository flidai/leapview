package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestToolCatalogCompilesSchemasOnceAndPropagatesCancellation(t *testing.T) {
	catalog, err := NewToolCatalog([]ToolDefinition{{
		Name:         "wait",
		Description:  "Wait for cancellation.",
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`),
		Effect:       "read",
		Handler: ToolHandlerFunc(func(ctx context.Context, _ ToolCall) (ToolResult, error) {
			<-ctx.Done()
			return ToolResult{}, ctx.Err()
		}),
	}})
	if err != nil {
		t.Fatalf("new catalog: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = catalog.Execute(ctx, ToolCall{ID: "call-1", Name: "wait", Arguments: json.RawMessage(`{}`)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("execute error = %v, want context canceled", err)
	}
	definitions := catalog.Definitions()
	if len(definitions) != 1 || definitions[0].Effect != "read" || len(definitions[0].OutputSchema) == 0 {
		t.Fatalf("catalog metadata = %#v", definitions)
	}
}

func TestToolValidationFailuresBecomeToolResults(t *testing.T) {
	tests := []struct {
		name     string
		call     ToolCall
		wantCode string
	}{
		{
			name:     "unknown tool",
			call:     ToolCall{ID: "call_1", Name: "missing", Arguments: json.RawMessage(`{}`)},
			wantCode: "unknown_tool",
		},
		{
			name:     "malformed json",
			call:     ToolCall{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{`)},
			wantCode: "invalid_tool_arguments",
		},
		{
			name:     "schema mismatch",
			call:     ToolCall{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{"id":7}`)},
			wantCode: "invalid_tool_arguments",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model := &fakeModel{responses: []ModelResponse{
				{ToolCalls: []ToolCall{tc.call}, FinishReason: FinishReasonToolCalls},
				{Content: "repaired", FinishReason: FinishReasonStop},
			}}
			a := mustAgent(t, Definition{
				Name:         "test",
				SystemPrompt: "x",
				Model:        model,
				Tools: []ToolDefinition{{
					Name:        "lookup",
					Description: "lookup",
					InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`),
					Handler:     noopTool(),
				}},
			})

			_, err := a.Prompt(context.Background(), PromptRequest{Input: "go"})
			if err != nil {
				t.Fatalf("Prompt returned error: %v", err)
			}
			transcript := a.Transcript()
			var tool Message
			for _, message := range transcript {
				if message.Role == RoleTool {
					tool = message
					break
				}
			}
			if !tool.IsError {
				t.Fatalf("tool result IsError = false, want true: %#v", tool)
			}
			if !strings.Contains(tool.Content, tc.wantCode) {
				t.Fatalf("tool result = %s, want code %s", tool.Content, tc.wantCode)
			}
		})
	}
}

func TestToolOutputValidationAndHandlerFailures(t *testing.T) {
	tests := []struct {
		name         string
		handler      ToolHandler
		limit        int
		displayLimit int
		wantCode     string
	}{
		{
			name: "handler error",
			handler: ToolHandlerFunc(func(context.Context, ToolCall) (ToolResult, error) {
				return ToolResult{}, errors.New("service unavailable")
			}),
			wantCode: "tool_execution_failed",
		},
		{
			name: "panic",
			handler: ToolHandlerFunc(func(context.Context, ToolCall) (ToolResult, error) {
				panic("bad tool")
			}),
			wantCode: "tool_panic",
		},
		{
			name: "nil content",
			handler: ToolHandlerFunc(func(context.Context, ToolCall) (ToolResult, error) {
				return ToolResult{}, nil
			}),
			wantCode: "tool_result_invalid",
		},
		{
			name: "not serializable",
			handler: ToolHandlerFunc(func(context.Context, ToolCall) (ToolResult, error) {
				return ToolResult{Content: map[string]any{"bad": func() {}}}, nil
			}),
			wantCode: "tool_result_invalid",
		},
		{
			name: "too large",
			handler: ToolHandlerFunc(func(context.Context, ToolCall) (ToolResult, error) {
				return ToolResult{Content: map[string]any{"value": strings.Repeat("x", 100)}}, nil
			}),
			limit:    12,
			wantCode: "tool_output_contract_violation",
		},
		{
			name: "display too large",
			handler: ToolHandlerFunc(func(context.Context, ToolCall) (ToolResult, error) {
				return ToolResult{
					Content:        map[string]any{"ok": true},
					DisplayContent: map[string]any{"rows": strings.Repeat("row-data", 100)},
				}, nil
			}),
			displayLimit: 24,
			wantCode:     "tool_display_output_too_large",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			limits := Limits{}
			if tc.limit > 0 {
				limits.MaxToolResultBytes = tc.limit
			}
			if tc.displayLimit > 0 {
				limits.MaxToolDisplayBytes = tc.displayLimit
			}
			model := &fakeModel{responses: []ModelResponse{
				{ToolCalls: []ToolCall{{ID: "call_1", Name: "work", Arguments: json.RawMessage(`{}`)}}, FinishReason: FinishReasonToolCalls},
				{Content: "done", FinishReason: FinishReasonStop},
			}}
			a := mustAgent(t, Definition{
				Name:         "test",
				SystemPrompt: "x",
				Model:        model,
				Limits:       limits,
				Tools: []ToolDefinition{{
					Name:        "work",
					Description: "work",
					InputSchema: json.RawMessage(`{"type":"object"}`),
					Handler:     tc.handler,
				}},
			})

			_, err := a.Prompt(context.Background(), PromptRequest{Input: "go"})
			if err != nil {
				t.Fatalf("Prompt returned error: %v", err)
			}
			tool := onlyToolMessage(t, a.Transcript())
			if !tool.IsError || !strings.Contains(tool.Content, tc.wantCode) {
				t.Fatalf("tool content = %s, IsError=%v, want %s", tool.Content, tool.IsError, tc.wantCode)
			}
			if tc.name == "too large" {
				for _, want := range []string{"actual_bytes=", "max_bytes=12", "tool=work"} {
					if !strings.Contains(tool.Content, want) {
						t.Fatalf("tool contract error missing %q: %s", want, tool.Content)
					}
				}
			}
			if strings.Contains(tool.Content, "row-data") || tool.DisplayContent != nil {
				t.Fatalf("tool error leaked display payload: %#v", tool)
			}
		})
	}
}

func TestToolExecutionIsBoundedParallelAndOrdered(t *testing.T) {
	events := &recordingEvents{}
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	var running int32
	var maxRunning int32
	handler := ToolHandlerFunc(func(ctx context.Context, call ToolCall) (ToolResult, error) {
		current := atomic.AddInt32(&running, 1)
		for {
			old := atomic.LoadInt32(&maxRunning)
			if current <= old || atomic.CompareAndSwapInt32(&maxRunning, old, current) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-ctx.Done():
			atomic.AddInt32(&running, -1)
			return ToolResult{}, ctx.Err()
		case <-release:
			atomic.AddInt32(&running, -1)
			return ToolResult{Content: map[string]any{"id": call.ID}}, nil
		}
	})
	model := &fakeModel{responses: []ModelResponse{
		{ToolCalls: []ToolCall{
			{ID: "call_1", Name: "work", Arguments: json.RawMessage(`{}`)},
			{ID: "call_2", Name: "work", Arguments: json.RawMessage(`{}`)},
			{ID: "call_3", Name: "work", Arguments: json.RawMessage(`{}`)},
		}, FinishReason: FinishReasonToolCalls},
		{Content: "done", FinishReason: FinishReasonStop},
	}}
	a := mustAgent(t, Definition{
		Name:         "test",
		SystemPrompt: "x",
		Model:        model,
		Events:       events,
		Limits:       Limits{MaxConcurrentTools: 2, ToolTimeout: time.Second},
		Tools: []ToolDefinition{{
			Name:        "work",
			Description: "work",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Handler:     handler,
		}},
	})

	done := make(chan error, 1)
	go func() {
		_, err := a.Prompt(context.Background(), PromptRequest{Input: "go"})
		done <- err
	}()
	<-started
	<-started
	if atomic.LoadInt32(&maxRunning) != 2 {
		t.Fatalf("max running = %d, want 2", maxRunning)
	}
	close(release)
	<-started
	if err := <-done; err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}

	transcript := a.Transcript()
	gotIDs := make([]string, 0, 3)
	for _, message := range transcript {
		if message.Role == RoleTool {
			gotIDs = append(gotIDs, message.ToolCallID)
		}
	}
	if strings.Join(gotIDs, ",") != "call_1,call_2,call_3" {
		t.Fatalf("tool result order = %v", gotIDs)
	}
	declaredIDs := make([]string, 0, 3)
	for _, event := range events.events {
		if event.Type != EventTypeOutputPartAdded || event.OutputKind != OutputPartKindTool {
			continue
		}
		if event.OutputOrdinal != int64(len(declaredIDs)) || event.OutputPartID == "" {
			t.Fatalf("declared tool part %d = %#v", len(declaredIDs), event)
		}
		declaredIDs = append(declaredIDs, event.ToolCallID)
	}
	if strings.Join(declaredIDs, ",") != "call_1,call_2,call_3" {
		t.Fatalf("declared tool order = %v", declaredIDs)
	}
}

func TestToolResultContentDefaultsToTOON(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{
		{ToolCalls: []ToolCall{{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{}`)}}, FinishReason: FinishReasonToolCalls},
		{Content: "done", FinishReason: FinishReasonStop},
	}}
	a := mustAgent(t, Definition{
		Name:         "test",
		SystemPrompt: "x",
		Model:        model,
		Tools: []ToolDefinition{{
			Name:        "lookup",
			Description: "lookup",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Handler: ToolHandlerFunc(func(context.Context, ToolCall) (ToolResult, error) {
				return ToolResult{Content: map[string]any{"ok": true, "id": "agent_table_1"}}, nil
			}),
		}},
	})

	if _, err := a.Prompt(context.Background(), PromptRequest{Input: "go"}); err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	tool := onlyToolMessage(t, a.Transcript())
	if strings.Contains(tool.Content, `"ok":true`) || !strings.Contains(tool.Content, "ok: true") || !strings.Contains(tool.Content, "id: agent_table_1") {
		t.Fatalf("tool result should be TOON by default: %s", tool.Content)
	}
}

func TestToolResultContentCanUseJSON(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{
		{ToolCalls: []ToolCall{{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{}`)}}, FinishReason: FinishReasonToolCalls},
		{Content: "done", FinishReason: FinishReasonStop},
	}}
	a := mustAgent(t, Definition{
		Name:         "test",
		SystemPrompt: "x",
		Model:        model,
		ToolOutput:   ToolOutputConfig{Format: ToolOutputJSON},
		Tools: []ToolDefinition{{
			Name:        "lookup",
			Description: "lookup",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Handler: ToolHandlerFunc(func(context.Context, ToolCall) (ToolResult, error) {
				return ToolResult{Content: map[string]any{"ok": true, "id": "agent_table_1"}}, nil
			}),
		}},
	})

	if _, err := a.Prompt(context.Background(), PromptRequest{Input: "go"}); err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	tool := onlyToolMessage(t, a.Transcript())
	if !strings.Contains(tool.Content, `"ok":true`) || !strings.Contains(tool.Content, `"id":"agent_table_1"`) {
		t.Fatalf("tool result should honor JSON output config: %s", tool.Content)
	}
}

func TestToolResultDoesNotMutateArraysOrStrings(t *testing.T) {
	rows := make([]map[string]any, 0, 5)
	for i := range 5 {
		rows = append(rows, map[string]any{"id": i + 1, "name": fmt.Sprintf("row-%d", i+1)})
	}
	model := &fakeModel{responses: []ModelResponse{
		{ToolCalls: []ToolCall{{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{}`)}}, FinishReason: FinishReasonToolCalls},
		{Content: "done", FinishReason: FinishReasonStop},
	}}
	a := mustAgent(t, Definition{
		Name:         "test",
		SystemPrompt: "x",
		Model:        model,
		Tools: []ToolDefinition{{
			Name:        "lookup",
			Description: "lookup",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Handler: ToolHandlerFunc(func(context.Context, ToolCall) (ToolResult, error) {
				return ToolResult{Content: map[string]any{"body": "abcdefghij", "items": rows}}, nil
			}),
		}},
	})

	if _, err := a.Prompt(context.Background(), PromptRequest{Input: "go"}); err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	tool := onlyToolMessage(t, a.Transcript())
	for _, want := range []string{"body: abcdefghij", "items[5]{id,name}:", "1,row-1", "5,row-5"} {
		if !strings.Contains(tool.Content, want) {
			t.Fatalf("tool result missing %q:\n%s", want, tool.Content)
		}
	}
	if strings.Contains(tool.Content, "_meta") || strings.Contains(tool.Content, "truncated") {
		t.Fatalf("harness added semantic truncation metadata:\n%s", tool.Content)
	}
}

func TestToolResultTOONQuotesAmbiguousScalarsAndKeys(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{
		{ToolCalls: []ToolCall{{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{}`)}}, FinishReason: FinishReasonToolCalls},
		{Content: "done", FinishReason: FinishReasonStop},
	}}
	a := mustAgent(t, Definition{
		Name:         "test",
		SystemPrompt: "x",
		Model:        model,
		Tools: []ToolDefinition{{
			Name:        "lookup",
			Description: "lookup",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Handler: ToolHandlerFunc(func(context.Context, ToolCall) (ToolResult, error) {
				return ToolResult{Content: map[string]any{
					"quoted:key": `He said "hi" \ ok`,
					"items": []any{
						map[string]any{"name": "true", "value": "123", "bad,field": "a,b"},
					},
				}}, nil
			}),
		}},
	})

	if _, err := a.Prompt(context.Background(), PromptRequest{Input: "go"}); err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	tool := onlyToolMessage(t, a.Transcript())
	for _, want := range []string{
		`"quoted:key": "He said \"hi\" \\ ok"`,
		`items[1]{"bad,field",name,value}:`,
		`"a,b","true","123"`,
	} {
		if !strings.Contains(tool.Content, want) {
			t.Fatalf("tool result missing quoted fragment %q:\n%s", want, tool.Content)
		}
	}
}

func TestToolResultTopLevelEmptyArrayIsExplicit(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{
		{ToolCalls: []ToolCall{{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{}`)}}, FinishReason: FinishReasonToolCalls},
		{Content: "done", FinishReason: FinishReasonStop},
	}}
	a := mustAgent(t, Definition{
		Name:         "test",
		SystemPrompt: "x",
		Model:        model,
		Tools: []ToolDefinition{{
			Name:        "lookup",
			Description: "lookup",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Handler: ToolHandlerFunc(func(context.Context, ToolCall) (ToolResult, error) {
				return ToolResult{Content: []any{}}, nil
			}),
		}},
	})

	if _, err := a.Prompt(context.Background(), PromptRequest{Input: "go"}); err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	tool := onlyToolMessage(t, a.Transcript())
	if !strings.Contains(tool.Content, "count: 0") || !strings.Contains(tool.Content, "items[0]:") {
		t.Fatalf("empty array should be explicit:\n%s", tool.Content)
	}
}

func TestToolResultOrderingAndDepthAreDeterministicAndLossless(t *testing.T) {
	content := map[string]any{
		"z": "last",
		"a": map[string]any{"b": map[string]any{"c": map[string]any{"d": "too deep"}}},
		"m": "middle",
	}
	model := &fakeModel{responses: []ModelResponse{
		{ToolCalls: []ToolCall{{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{}`)}}, FinishReason: FinishReasonToolCalls},
		{Content: "done", FinishReason: FinishReasonStop},
	}}
	a := mustAgent(t, Definition{
		Name:         "test",
		SystemPrompt: "x",
		Model:        model,
		Tools: []ToolDefinition{{
			Name:        "lookup",
			Description: "lookup",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Handler: ToolHandlerFunc(func(context.Context, ToolCall) (ToolResult, error) {
				return ToolResult{Content: content}, nil
			}),
		}},
	})

	if _, err := a.Prompt(context.Background(), PromptRequest{Input: "go"}); err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	tool := onlyToolMessage(t, a.Transcript())
	aIndex := strings.Index(tool.Content, "a:")
	mIndex := strings.Index(tool.Content, "m: middle")
	zIndex := strings.Index(tool.Content, "z: last")
	if aIndex < 0 || mIndex < 0 || zIndex < 0 || !(aIndex < mIndex && mIndex < zIndex) {
		t.Fatalf("object keys should be sorted deterministically:\n%s", tool.Content)
	}
	if strings.Contains(tool.Content, "max_depth") || !strings.Contains(tool.Content, "d: too deep") {
		t.Fatalf("deep object should be preserved without truncation metadata:\n%s", tool.Content)
	}
}

func TestToolDisplayContentIsNotSentBackToModel(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{
		{ToolCalls: []ToolCall{{ID: "call_1", Name: "visual", Arguments: json.RawMessage(`{}`)}}, FinishReason: FinishReasonToolCalls},
		{Content: "done", FinishReason: FinishReasonStop},
	}}
	display := map[string]any{
		"kind": "table",
		"patch": map[string]any{
			"tables": map[string]any{
				"agent_table_1": map[string]any{"rows": strings.Repeat("row-data", 1000)},
			},
		},
	}
	a := mustAgent(t, Definition{
		Name:         "test",
		SystemPrompt: "x",
		Model:        model,
		Tools: []ToolDefinition{{
			Name:        "visual",
			Description: "visual",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Handler: ToolHandlerFunc(func(context.Context, ToolCall) (ToolResult, error) {
				return ToolResult{
					Content:        display,
					ModelContent:   map[string]any{"ok": true, "id": "agent_table_1"},
					DisplayContent: display,
				}, nil
			}),
		}},
	})

	if _, err := a.Prompt(context.Background(), PromptRequest{Input: "go"}); err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	tool := onlyToolMessage(t, a.Transcript())
	if tool.DisplayContent == nil {
		t.Fatalf("tool transcript missing display content: %#v", tool)
	}
	if !strings.Contains(tool.Content, `ok: true`) || strings.Contains(tool.Content, "row-data") {
		t.Fatalf("tool model content should be compact: %s", tool.Content)
	}
	if len(model.requests) != 2 {
		t.Fatalf("model requests = %d, want 2", len(model.requests))
	}
	last := model.requests[1].Messages[len(model.requests[1].Messages)-1]
	if last.Role != RoleTool || last.DisplayContent != nil {
		t.Fatalf("second model request leaked display content: %#v", last)
	}
	if strings.Contains(last.Content, "row-data") {
		t.Fatalf("second model request leaked display rows: %s", last.Content)
	}
}

func TestToolDisplayContentDoesNotAffectTokenEstimate(t *testing.T) {
	a := mustAgent(t, Definition{
		Name:         "test",
		SystemPrompt: "x",
		Model:        &fakeModel{responses: []ModelResponse{{Content: "ok", FinishReason: FinishReasonStop}}},
	})
	base := []Message{{Role: RoleTool, ToolCallID: "call_1", ToolName: "visual", Content: `{"ok":true}`}}
	withDisplay := []Message{{Role: RoleTool, ToolCallID: "call_1", ToolName: "visual", Content: `{"ok":true}`, DisplayContent: map[string]any{"rows": strings.Repeat("row-data", 1000)}}}
	if got, want := a.estimateModelInputTokens(withDisplay), a.estimateModelInputTokens(base); got != want {
		t.Fatalf("token estimate with display = %d, want %d", got, want)
	}
}

func TestToolFatalResultStopsRunAfterAppendingResult(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{
		{ToolCalls: []ToolCall{{ID: "call_1", Name: "fatal", Arguments: json.RawMessage(`{}`)}}, FinishReason: FinishReasonToolCalls},
		{Content: "should not call", FinishReason: FinishReasonStop},
	}}
	a := mustAgent(t, Definition{
		Name:         "test",
		SystemPrompt: "x",
		Model:        model,
		Tools: []ToolDefinition{{
			Name:        "fatal",
			Description: "fatal",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Handler: ToolHandlerFunc(func(context.Context, ToolCall) (ToolResult, error) {
				return ToolResult{Content: map[string]any{"error": "stop"}, Fatal: true}, nil
			}),
		}},
	})

	result, err := a.Prompt(context.Background(), PromptRequest{Input: "go"})
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if result.StopReason != StopReasonFatalToolError {
		t.Fatalf("StopReason = %s, want fatal_tool_error", result.StopReason)
	}
	if len(model.requests) != 1 {
		t.Fatalf("model calls = %d, want 1", len(model.requests))
	}
	tool := onlyToolMessage(t, a.Transcript())
	if tool.ToolCallID != "call_1" || !strings.Contains(tool.Content, "stop") {
		t.Fatalf("tool result = %#v, want appended fatal result", tool)
	}
}

func TestFatalToolErrorStopsRunAfterAppendingErrorResult(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{
		{ToolCalls: []ToolCall{{ID: "call_1", Name: "fatal", Arguments: json.RawMessage(`{}`)}}, FinishReason: FinishReasonToolCalls},
		{Content: "should not call", FinishReason: FinishReasonStop},
	}}
	a := mustAgent(t, Definition{
		Name:         "test",
		SystemPrompt: "x",
		Model:        model,
		Tools: []ToolDefinition{{
			Name:        "fatal",
			Description: "fatal",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Handler: ToolHandlerFunc(func(context.Context, ToolCall) (ToolResult, error) {
				return ToolResult{}, FatalToolError(errors.New("stop now"))
			}),
		}},
	})

	result, err := a.Prompt(context.Background(), PromptRequest{Input: "go"})
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if result.StopReason != StopReasonFatalToolError {
		t.Fatalf("StopReason = %s, want fatal_tool_error", result.StopReason)
	}
	tool := onlyToolMessage(t, a.Transcript())
	if !tool.IsError || !strings.Contains(tool.Content, "tool_execution_failed") {
		t.Fatalf("tool result = %#v, want execution error", tool)
	}
}

func onlyToolMessage(t *testing.T, messages []Message) Message {
	t.Helper()
	for _, message := range messages {
		if message.Role == RoleTool {
			return message
		}
	}
	t.Fatal("no tool message found")
	return Message{}
}
