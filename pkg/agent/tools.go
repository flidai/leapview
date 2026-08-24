package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/sync/errgroup"
)

var ErrInvalidToolArguments = errors.New("invalid tool arguments")

type ToolDefinition struct {
	Name         string
	Description  string
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
	Effect       string
	Tags         []string
	Handler      ToolHandler
}

// ToolCatalog is the transport- and model-independent executable tool surface.
// Schemas are compiled when the catalog is constructed and reused for every
// invocation through that catalog.
type ToolCatalog struct {
	tools       map[string]*compiledTool
	definitions []ToolDefinition
	specs       []ToolSpec
}

func NewToolCatalog(definitions []ToolDefinition) (*ToolCatalog, error) {
	tools, specs, err := compileTools(definitions)
	if err != nil {
		return nil, err
	}
	cloned := make([]ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		definition.InputSchema = append(json.RawMessage(nil), definition.InputSchema...)
		definition.OutputSchema = append(json.RawMessage(nil), definition.OutputSchema...)
		definition.Tags = append([]string(nil), definition.Tags...)
		cloned = append(cloned, definition)
	}
	return &ToolCatalog{tools: tools, definitions: cloned, specs: specs}, nil
}

func (c *ToolCatalog) Definitions() []ToolDefinition {
	if c == nil {
		return nil
	}
	out := make([]ToolDefinition, 0, len(c.definitions))
	for _, definition := range c.definitions {
		definition.InputSchema = append(json.RawMessage(nil), definition.InputSchema...)
		definition.OutputSchema = append(json.RawMessage(nil), definition.OutputSchema...)
		definition.Tags = append([]string(nil), definition.Tags...)
		out = append(out, definition)
	}
	return out
}

func (c *ToolCatalog) Specs() []ToolSpec {
	if c == nil {
		return nil
	}
	return append([]ToolSpec(nil), c.specs...)
}

func (c *ToolCatalog) Execute(ctx context.Context, call ToolCall) (ToolResult, error) {
	if c == nil {
		return ToolResult{}, fmt.Errorf("tool catalog is not configured")
	}
	tool, ok := c.tools[call.Name]
	if !ok {
		return ToolResult{}, fmt.Errorf("unknown tool %q", call.Name)
	}
	if err := validateCompiledToolCall(tool, call); err != nil {
		return ToolResult{}, err
	}
	result, err := tool.def.Handler.Run(ctx, call)
	if err != nil || result.IsError || tool.outputSchema == nil || result.Content == nil {
		return result, err
	}
	encoded, err := json.Marshal(result.Content)
	if err != nil {
		return ToolResult{}, fmt.Errorf("tool output was not JSON serializable: %w", err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return ToolResult{}, fmt.Errorf("tool output was not valid JSON: %w", err)
	}
	if err := tool.outputSchema.Validate(instance); err != nil {
		return ToolResult{}, fmt.Errorf("tool output did not match the schema: %w", err)
	}
	return result, nil
}

func validateCompiledToolCall(tool *compiledTool, call ToolCall) error {
	if !json.Valid(call.Arguments) {
		return fmt.Errorf("%w: arguments must be valid JSON", ErrInvalidToolArguments)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(call.Arguments))
	if err != nil {
		return fmt.Errorf("%w: arguments must be valid JSON: %v", ErrInvalidToolArguments, err)
	}
	if err := tool.schema.Validate(instance); err != nil {
		return fmt.Errorf("%w: arguments did not match the schema: %v", ErrInvalidToolArguments, err)
	}
	return nil
}

type ToolHandler interface {
	Run(ctx context.Context, call ToolCall) (ToolResult, error)
}

type ToolHandlerFunc func(ctx context.Context, call ToolCall) (ToolResult, error)

func (f ToolHandlerFunc) Run(ctx context.Context, call ToolCall) (ToolResult, error) {
	return f(ctx, call)
}

type ToolCall struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Arguments       json.RawMessage `json:"arguments,omitempty"`
	OutputPartID    string          `json:"output_part_id,omitempty"`
	OutputOrdinal   int64           `json:"output_ordinal,omitempty"`
	ParentMessageID string          `json:"parent_message_id,omitempty"`
}

type ToolResult struct {
	Content        any // Canonical result validated against the tool output schema.
	ModelContent   any // Optional compact projection sent only to the model.
	DisplayContent any // Optional rich projection retained for the embedding UI.
	IsError        bool
	Fatal          bool
}

type compiledTool struct {
	def          ToolDefinition
	schema       *jsonschema.Schema
	outputSchema *jsonschema.Schema
}

type toolExecutionResult struct {
	message Message
	fatal   error
}

func (a *Agent) executeToolCalls(ctx context.Context, run *runState, turnID string, calls []ToolCall) ([]Message, error) {
	results := make([]toolExecutionResult, len(calls))
	seen := map[string]struct{}{}
	valid := make([]int, 0, len(calls))
	for i, call := range calls {
		if call.ID == "" {
			results[i] = toolExecutionResult{message: a.toolErrorMessage(call, "invalid_tool_arguments", "Tool call ID is required.", nil, true)}
			run.emitToolExecutionEnd(ctx, turnID, call, results[i].message)
			continue
		}
		if _, ok := seen[call.ID]; ok {
			results[i] = toolExecutionResult{message: a.toolErrorMessage(call, "invalid_tool_arguments", "Tool call ID must be unique within an assistant message.", nil, true)}
			run.emitToolExecutionEnd(ctx, turnID, call, results[i].message)
			continue
		}
		seen[call.ID] = struct{}{}
		if errMsg, ok := a.validateToolCall(call); !ok {
			results[i] = toolExecutionResult{message: errMsg}
			run.emitToolExecutionEnd(ctx, turnID, call, results[i].message)
			continue
		}
		valid = append(valid, i)
	}

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(a.def.Limits.MaxConcurrentTools)
	var mu sync.Mutex
	for _, index := range valid {
		index := index
		group.Go(func() error {
			call := calls[index]
			_ = run.emit(groupCtx, Event{
				Type:            EventTypeToolExecutionStart,
				Severity:        SeverityInfo,
				TurnID:          turnID,
				MessageID:       call.ParentMessageID,
				OutputPartID:    call.OutputPartID,
				OutputKind:      OutputPartKindTool,
				OutputOrdinal:   call.OutputOrdinal,
				ParentMessageID: call.ParentMessageID,
				ToolCallID:      call.ID,
				ToolName:        call.Name,
			})
			result := a.runOneTool(groupCtx, call)
			run.emitToolExecutionEnd(groupCtx, turnID, call, result.message)
			mu.Lock()
			results[index] = result
			mu.Unlock()
			return nil
		})
	}
	_ = group.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	messages := make([]Message, len(results))
	for i, result := range results {
		messages[i] = result.message
		if result.fatal != nil {
			return messages, result.fatal
		}
	}
	return messages, nil
}

func (r *runState) emitToolExecutionEnd(ctx context.Context, turnID string, call ToolCall, message Message) {
	_ = r.emit(ctx, Event{
		Type:            EventTypeToolExecutionEnd,
		Severity:        eventSeverityForToolResult(message),
		TurnID:          turnID,
		MessageID:       call.ParentMessageID,
		OutputPartID:    call.OutputPartID,
		OutputKind:      OutputPartKindTool,
		OutputOrdinal:   call.OutputOrdinal,
		ParentMessageID: call.ParentMessageID,
		ToolCallID:      call.ID,
		ToolName:        call.Name,
	})
}

func eventSeverityForToolResult(message Message) Severity {
	if message.IsError {
		return SeverityWarn
	}
	return SeverityInfo
}

func (a *Agent) validateToolCall(call ToolCall) (Message, bool) {
	tool, ok := a.tools[call.Name]
	if !ok {
		return a.toolErrorMessage(call, "unknown_tool", fmt.Sprintf("Tool %q is not configured.", call.Name), nil, true), false
	}
	if err := validateCompiledToolCall(tool, call); err != nil {
		return a.toolErrorMessage(call, "invalid_tool_arguments", "Tool arguments did not match the schema.", []string{err.Error()}, true), false
	}
	return Message{}, true
}

func (a *Agent) runOneTool(ctx context.Context, call ToolCall) (result toolExecutionResult) {
	toolCtx, cancel := context.WithTimeout(ctx, a.def.Limits.ToolTimeout)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			result.message = a.toolErrorMessage(call, "tool_panic", "Tool handler panicked.", []string{fmt.Sprint(recovered), string(debug.Stack())}, false)
		}
		result.message.OutputPartID = call.OutputPartID
		result.message.OutputOrdinal = call.OutputOrdinal
		result.message.ParentMessageID = call.ParentMessageID
	}()

	toolResult, err := a.catalog.Execute(toolCtx, call)
	if err != nil {
		var fatal fatalToolError
		if ctxErr := toolCtx.Err(); ctxErr != nil {
			result.message = a.toolErrorMessage(call, "tool_timeout", "Tool execution timed out or was canceled.", []string{ctxErr.Error()}, true)
			return result
		}
		result.message = a.toolErrorMessage(call, "tool_execution_failed", "Tool execution failed.", []string{err.Error()}, true)
		if isFatalToolError(err, &fatal) {
			result.fatal = NewError(ErrorCodeTool, "fatal tool error", fatal.err)
		}
		return result
	}
	if toolCtx.Err() != nil {
		result.message = a.toolErrorMessage(call, "tool_timeout", "Tool execution timed out or was canceled.", []string{toolCtx.Err().Error()}, true)
		return result
	}
	if toolResult.Content == nil {
		result.message = a.toolErrorMessage(call, "tool_result_invalid", "Tool returned no JSON-serializable result.", nil, false)
		return result
	}
	modelContent := toolResult.Content
	if toolResult.ModelContent != nil {
		modelContent = toolResult.ModelContent
	}
	body, err := formatToolOutput(modelContent, a.def.ToolOutput)
	if err != nil {
		result.message = a.toolErrorMessage(call, "tool_result_invalid", "Tool output was not JSON-serializable.", []string{err.Error()}, false)
		return result
	}
	if toolResult.DisplayContent != nil {
		displayBody, err := json.Marshal(toolResult.DisplayContent)
		if err != nil {
			result.message = a.toolErrorMessage(call, "tool_result_invalid", "Tool display output was not JSON-serializable.", []string{err.Error()}, false)
			return result
		}
		if len(displayBody) > a.def.Limits.MaxToolDisplayBytes {
			result.message = a.toolErrorMessage(call, "tool_display_output_too_large", "Tool display output exceeded the configured size limit.", nil, false)
			return result
		}
	}
	if len(body) > a.def.Limits.MaxToolResultBytes {
		result.message = a.toolErrorMessage(
			call,
			"tool_output_contract_violation",
			"Tool returned a model result larger than its output contract permits; no partial result was exposed.",
			[]string{
				fmt.Sprintf("tool=%s", call.Name),
				fmt.Sprintf("actual_bytes=%d", len(body)),
				fmt.Sprintf("max_bytes=%d", a.def.Limits.MaxToolResultBytes),
			},
			false,
		)
		return result
	}
	result.message = Message{
		ID:              a.def.IDGenerator.NewID("msg"),
		OutputPartID:    call.OutputPartID,
		OutputOrdinal:   call.OutputOrdinal,
		ParentMessageID: call.ParentMessageID,
		Role:            RoleTool,
		Content:         body,
		DisplayContent:  toolResult.DisplayContent,
		ToolCallID:      call.ID,
		ToolName:        call.Name,
		IsError:         toolResult.IsError,
	}
	if toolResult.Fatal {
		result.fatal = NewError(ErrorCodeTool, "fatal tool result", nil)
	}
	return result
}

func isFatalToolError(err error, target *fatalToolError) bool {
	if err == nil {
		return false
	}
	if v, ok := err.(fatalToolError); ok {
		*target = v
		return true
	}
	type unwrapper interface{ Unwrap() error }
	if wrapped, ok := err.(unwrapper); ok {
		return isFatalToolError(wrapped.Unwrap(), target)
	}
	return false
}

func (a *Agent) toolErrorMessage(call ToolCall, code, message string, details []string, retryable bool) Message {
	payload := map[string]any{
		"error": map[string]any{
			"code":      code,
			"message":   message,
			"retryable": retryable,
		},
	}
	if len(details) > 0 {
		payload["error"].(map[string]any)["details"] = details
	}
	body, err := formatToolOutput(payload, a.def.ToolOutput)
	if err != nil {
		fallback, _ := json.Marshal(payload)
		body = string(fallback)
	}
	return Message{
		OutputPartID:    call.OutputPartID,
		OutputOrdinal:   call.OutputOrdinal,
		ParentMessageID: call.ParentMessageID,
		Role:            RoleTool,
		Content:         body,
		ToolCallID:      call.ID,
		ToolName:        call.Name,
		IsError:         true,
	}
}
