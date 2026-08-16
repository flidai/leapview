# Embedded Agent Harness Specification

Status: draft

Target package: `github.com/flidai/leapview/pkg/agent`

## Recommended Decisions

- V1 should support OpenAI-compatible model providers only, such as OpenAI and DeepSeek. That gives us one clear chat, tool-call, tool-result, streaming, usage, and finish-reason contract.
- V1 should be configured from Go structs only. Do not support YAML/JSON profiles in the generic package.
- V1 should be an embedded application harness, not a complete agent platform. It should assume the host application owns auth, permissions, UI, durable workflows, service access, and all tools.
- V1 tools should be tailor-made service actions. The generic package should never expose raw filesystem, shell, browser, SQL, or network access.
- V1 should have one execution path: one harness, one model client, one system prompt, one tool list, bounded parallel tool execution, automatic compaction, in-memory transcript, and event streaming.

## Purpose

`pkg/agent` provides an embedded, framework-light agent runtime for Go applications. It owns the agent loop, turn lifecycle, OpenAI-compatible tool dispatch, automatic transcript compaction, event streaming, cancellation, and in-memory transcript state. It does not own product-specific tools, UI, memory stores, durable workflow engines, or ambient machine capabilities.

The package is designed for LeapView first, but it must be generic enough to lift into another Go project without bringing LeapView dependencies with it. It is not trying to become a complete standalone agent platform like `pi`; it is a reusable harness for applications that already have services, permissions, data models, and UI flows.

## Design Principles

- Embedded by default: the host application constructs the harness from Go structs and service dependencies.
- Go structs only: concrete model clients, service handles, prompts, limits, and tool handlers are wired in Go.
- No built-in tools: the package ships no filesystem, shell, DuckDB, HTTP, browser, BI, or MCP tools. Every callable capability is supplied by the host application.
- No ambient filesystem access: tools should call host services, repositories, and domain APIs rather than exposing raw process or filesystem capabilities.
- OpenAI-compatible provider contract: model requests and responses follow the OpenAI-compatible chat/tool API used by providers such as OpenAI and DeepSeek.
- Provider flexibility through compatibility: OpenAI, DeepSeek, OpenAI-compatible gateways, local compatible endpoints, and test fakes can sit behind the same client interface. Non-compatible providers must adapt to the OpenAI-compatible API outside the core.
- Tool schema convention: tool inputs use JSON Schema-compatible objects because that is the OpenAI tool contract.
- Tool validation is part of the harness: unknown tools, malformed JSON arguments, schema validation failures, handler failures, and outputs that violate the result byte contract are converted into clear model-visible tool results.
- Tool providers own semantic output bounds: pagination, windows, counts, and completeness are calculated before the result reaches the harness. The harness never silently slices successful structured output.
- Compaction is part of the harness: keep the last configured turns verbatim and summarize older turns into a model-visible summary message.
- Context-first cancellation: all model calls and tool calls receive `context.Context`.
- Deterministic state boundaries: one run uses the harness configuration supplied at construction time.
- Deterministic ordering: events and transcript messages are emitted in stable causal order, even when tool calls execute concurrently.
- Tool errors are model-visible: a failed tool call normally becomes a tool-result message instead of killing the whole run.
- Small core, explicit edges: the host app owns persistence, observability export, approval gates, and memory/context retrieval around the harness.
- Testable without network: the core loop must run against fake models, fake tools, and deterministic clocks/IDs.

## Non-Goals

- No general provider abstraction layer in the first package version.
- No CLI/TUI.
- No direct shell, filesystem, browser, network-fetch, or database tools.
- No opinions about auth, API keys, billing, or provider routing beyond the OpenAI-compatible client configured by the host.
- No hidden global tool registry.
- No YAML/JSON config loader.
- No durable session store or resume support in V1.
- No automatic durable workflow engine.
- No active-tool filtering in V1. The `Tools` slice is the active tool set.
- No general hook system in V1.
- No product-specific memory implementation. The host can compose extra context into the system prompt or user input before calling the harness.
- No parsing of LeapView dashboard/model YAML inside the generic package.

## Reference Takeaways

The local `pi` reference and the Go agent article point to the same shape:

- Keep a low-level loop small: call the configured OpenAI-compatible model provider, append assistant message, execute requested tools, append tool results, repeat.
- Put product/runtime orchestration above the loop in a harness: configuration, transcript state, compaction, tools, and events.
- Treat provider streams as transport details and emit stable runtime events to the application.
- Do not persist runtime closures or concrete tool implementations.
- Use turn boundaries after assistant/tool-result completion as the safe place to stop.

## Package Layers

The package should be organized as two layers.

### 1. Loop

The loop is the minimal stateless engine. It receives a turn state and runs until the model stops asking for tools, a limit stops it, or the context is canceled.

Responsibilities:

- Convert current conversation into OpenAI-compatible model input through the configured client.
- Pass declared tool specs to the model.
- Parse model output into assistant text/content and OpenAI-compatible tool calls.
- Execute all tool calls requested in one assistant message with bounded parallelism.
- Append tool-result messages.
- Emit lifecycle events.
- Stop on completion, cancellation, max turns, or max tool calls.

### 2. Harness

The harness is the application-facing runtime. It owns configuration and in-memory transcript state.

Responsibilities:

- Accept one prompt/run request at a time.
- Frame application context as separate, untrusted, turn-scoped model input.
- Use the system prompt supplied at construction time.
- Use the tools supplied at construction time.
- Hold in-memory transcript state for the harness lifetime.
- Compact old transcript turns when context grows.
- Expose runtime events to UI/SSE/Datastar consumers.
- Reject concurrent runs while busy.

`PromptRequest.Context` accepts keyed JSON-serializable values. The harness
validates keys and limits, emits `<external_{key}>` user-role messages, and
automatically appends generic untrusted-data guidance to the model system
prompt. The embedding application does not construct context messages or
maintain separate prompt instructions.

Durable hosts may call `PreparePrompt`, persist `Transcript`, and later call
`RunPreparedPrompt` on an agent reconstructed with `InitialTranscript`. This is
the same lifecycle used by `Prompt`, split before model execution.

## Core Concepts

### Agent Definition

An agent definition is declarative configuration plus runtime adapters.

Illustrative shape:

```go
type Definition struct {
	Name        string
	Description string

	SystemPrompt string
	Model        Model

	Tools      []ToolDefinition
	Limits     Limits
	ToolOutput ToolOutputConfig
	Compaction CompactionConfig

	Events EventSink
}
```

This shape is illustrative, not final API. The important point is that V1 has one construction path and no secondary config loader.

Illustrative limits:

```go
type Limits struct {
	MaxTurns           int
	MaxToolCalls       int
	MaxConcurrentTools int
	ToolTimeout       time.Duration
	MaxToolResultBytes int

	ContextWindowTokens int
	ReserveOutputTokens int
	HardInputLimitTokens int
}
```

Default limits:

- `MaxTurns`: 16
- `MaxToolCalls`: 64 per run
- `MaxConcurrentTools`: 4 per assistant turn
- `ToolTimeout`: 30 seconds
- `MaxToolResultBytes`: 64 KiB after tool-output formatting
- `ReserveOutputTokens`: 4096
- `HardInputLimitTokens`: `ContextWindowTokens - ReserveOutputTokens`

Context trigger estimates include `ReserveOutputTokens`. Hard input-limit checks compare only estimated input tokens against `HardInputLimitTokens` so the reserve is not counted twice.

### Stop Reasons and Finish Reasons

The harness should normalize provider-specific finish reasons and expose stable stop reasons to callers and events.

Model finish reasons:

- `stop`: the model completed normally.
- `tool_calls`: the model requested one or more tool calls.
- `truncated`: the provider stopped because the output limit was reached. OpenAI-compatible adapters should normalize provider values such as `length` to this value.
- `content_filter`: the provider stopped for safety/filtering.
- `unknown`: the provider returned no recognized finish reason.

Run stop reasons:

- `completed`: the run ended with a final assistant response.
- `max_turns`: `MaxTurns` stopped the loop.
- `max_tool_calls`: `MaxToolCalls` stopped the loop.
- `context_limit`: the harness could not build a model request within the configured context budget.
- `truncated`: the final model response was truncated.
- `canceled`: the context or `Abort()` canceled the run.
- `model_error`: the model request failed.
- `fatal_tool_error`: a tool error marked fatal stopped the run.

### Messages

The core message model should map directly to the OpenAI-compatible chat/tool API and remain serializable. This is intentionally narrower than a fully provider-neutral abstraction because the package is an embedded harness, not a cross-provider framework.

Required roles:

- `system`: resolved instructions, normally not persisted as transcript unless audit mode requests it.
- `user`: user/application input.
- `assistant`: model output, including text, structured content, usage, finish reason, and requested tool calls.
- `tool`: result of one tool call, linked to the model's tool call ID.
- `summary`: model-visible compaction summary replacing older turns.

`summary` is an internal harness role, not a native OpenAI-compatible API role. Before model calls, the harness converts the current summary into a system/developer-style context message.

Messages should support text first. Image or multimodal content can be added later without changing the loop shape.

### Model Client

The model client is supplied by the host application. The core package should define the OpenAI-compatible request/response contract it needs, but the host decides whether that is backed by `openai-go`, DeepSeek, another OpenAI-compatible endpoint, a gateway, or a fake.

Required behavior:

- Accept OpenAI-compatible chat messages and tool definitions.
- Return assistant content, tool calls, finish reason, provider metadata, and usage when available.
- Support streaming through the configured event sink.
- Respect `context.Context`.

Illustrative shape:

```go
type Model interface {
	Complete(ctx context.Context, req ModelRequest, events EventSink) (ModelResponse, error)
}
```

`ModelRequest` includes a purpose so event handling can distinguish normal assistant turns from housekeeping calls:

```go
type ModelRequestPurpose string

const (
	ModelRequestPurposeTurn       ModelRequestPurpose = "turn"
	ModelRequestPurposeCompaction ModelRequestPurpose = "compaction"
)
```

The first implementation should use an OpenAI-compatible request/response contract instead of a generalized provider interface. Provider adapters live outside the core for V1.

For `Purpose=turn`, the model client may stream assistant deltas while building the final `ModelResponse`. For `Purpose=compaction`, the model client must not emit normal assistant `message_delta` events; compaction progress is reported through compaction events only. The harness appends only final turn assistant messages to transcript state.

Implementation note: keep the model streaming callback narrower than the full agent event sink. A model adapter should be able to emit model deltas, not arbitrary agent lifecycle events.

### Tool Definition

Tools are declarative specs plus executable handlers supplied by the host. In LeapView, handlers should be thin wrappers around application services such as catalog loading, semantic model inspection, query planning, dashboard draft creation, or deployment APIs.

Required fields:

- Stable name.
- Human/model-visible description.
- Input schema as JSON Schema-compatible data.
- Handler function.
- A bounded output schema with explicit pagination or completeness metadata where the result can be partial.

Illustrative shape:

```go
type ToolDefinition struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Handler     ToolHandler
}
```

The package must not infer or register any tools by name.

V1 schema validation uses JSON Schema for the OpenAI-compatible object schemas used by tool arguments. Prefer a small dependency over hand-rolling a partial validator.

### Tool Handler

Tool handlers receive raw JSON input and return structured results.

Required behavior:

- Validate input or return a tool-visible error.
- Respect `context.Context`.
- Be safe to run concurrently with other tool handlers.
- Avoid writing directly to global state when possible.
- Return a result that can be encoded into an OpenAI-compatible tool message.

Illustrative shape:

```go
type ToolHandler interface {
	Run(ctx context.Context, call ToolCall) (ToolResult, error)
}
```

Errors should be classified as:

- Tool-visible error: append a failed tool result and let the model react.
- Runtime fatal error: append the tool result or tool error, then abort the run because the harness cannot continue safely.

A handler can mark a successful `ToolResult` as fatal when the returned result should be visible in transcript but the loop must stop. A handler can also return a fatal tool error when execution failed in a way that should stop the run.

### Tool Validation and Error Results

The harness validates tool calls before handlers run.

Validation rules:

- Tool names must be unique at harness construction.
- Tool schemas must parse at harness construction.
- Tool call IDs from the model must be present and unique within an assistant message.
- A model-requested tool name must exist in the configured tool list.
- Tool arguments must be valid JSON.
- Tool arguments must satisfy the tool input schema.
- Tool output must be JSON-serializable.
- Tool output is normalized and serialized as TOON by default.
- The harness does not slice strings, arrays, or nested objects. Providers must calculate semantic pages and continuation state from exactly the content they return.
- Tool output must fit the configured result-size limit after formatting. An overlarge result becomes a `tool_output_contract_violation`; no partial success is exposed.

If validation fails, the harness does not call the handler. It appends a tool-result message with `is_error=true` and a concise, model-readable payload.

Tool error payloads should be structured and consistent:

```json
{
  "error": {
    "code": "invalid_tool_arguments",
    "message": "Tool arguments did not match the schema.",
    "details": [
      "field 'dashboard_id' is required"
    ],
    "retryable": true
  }
}
```

Recommended tool error codes:

- `unknown_tool`
- `invalid_tool_arguments`
- `tool_execution_failed`
- `tool_panic`
- `tool_timeout`
- `tool_output_contract_violation`
- `tool_result_invalid`

The goal is to help the model repair its next call rather than strand the run on a formatting mistake.

## Configuration Boundary

The only V1 configuration boundary is Go structs. This is the only place concrete functions, service handles, model clients, and event sinks can be wired safely.

Do not add YAML/JSON profile support to `pkg/agent` in V1. If LeapView later wants an app-level YAML profile, that loader should live outside the generic harness and should produce the same Go struct as hand-written code.

The host application maps services and tools to concrete Go runtime adapters. For LeapView, dashboard-as-code YAML can define product behavior, but agent runtime wiring remains in Go.

## Transcript and Model Context

The harness keeps an in-memory transcript for model execution. Compaction mutates this model context by replacing older turns with a summary message.

The harness does not own full UI or audit history in V1. Applications that need complete chat history should consume events and persist or render them outside `pkg/agent`. Compaction must not cause the UI to lose already emitted messages.

## Runtime Lifecycle

### Prompt

1. Caller invokes `Prompt(ctx, input)`.
2. Harness rejects if a structural operation is already running.
3. Harness appends the user message to its in-memory transcript.
4. Harness creates a turn snapshot:
   - messages
   - resolved system prompt
   - model
   - tool definitions
   - limits
5. Harness estimates model context size, including system prompt, summary, recent messages, tool definitions, and output reserve.
6. Harness runs proactive compaction before the model call if the request would exceed the compaction trigger.
7. Loop calls the configured OpenAI-compatible model provider.
8. Loop streams assistant deltas and appends the assistant message.
9. If tool calls exist, loop validates them.
10. Loop executes valid tool calls with bounded parallelism.
11. Loop appends tool-result messages in assistant source order, including validation failures.
12. Harness emits turn end.
13. Harness compacts old transcript turns if the post-turn compaction trigger is reached.
14. Loop ends when no tool calls remain or a limit stops the run.

### Abort

`Abort()` cancels the active run context.

Expected behavior:

- In-flight model request receives cancellation.
- In-flight tool calls receive cancellation.
- The run emits an abort event.
- In-memory transcript messages already appended are not rolled back.

## Events

The package emits structured events and does not depend on a specific UI or tracing library.

Initial event types:

- `agent_start`
- `agent_end`
- `turn_start`
- `turn_end`
- `model_request`
- `model_response`
- `model_retry`
- `message_delta`
- `message_end`
- `tool_start`
- `tool_end`
- `compaction_start`
- `compaction_end`
- `compaction_error`
- `error`
- `abort`

Event requirements:

- Every event has run ID, timestamp, and sequence number.
- Turn-scoped events have turn ID.
- Every event has severity: `debug`, `info`, `warn`, or `error`.
- Events may include request/correlation IDs supplied by the host application.
- Tool events include tool call ID and tool name.
- Provider events include provider/model metadata when the model adapter supplies it.
- Usage events include token/cost data when available.
- Events are emitted in causal order: run start, turn start, model deltas/end, tool starts/ends, turn end, optional compaction, run end.
- Tool completion events may reflect actual completion order, but tool-result messages are appended in assistant tool-call order.
- Event emission is best-effort. The harness calls `Emit(ctx, event)` synchronously at harness boundaries, but sink errors must not break agent execution. Provider streaming should still use a narrow delta path so subscribers do not block provider reads indefinitely.

LeapView can bridge these events to Datastar SSE patches.

## Tool Execution Policy

V1 supports multiple tool calls in one assistant turn. The model can ask for several tools, the harness runs them with bounded parallelism, and then appends all tool results before calling the model again.

Execution is one-way:

- Validate every requested tool call.
- Run valid tool calls concurrently up to `MaxConcurrentTools`.
- Convert validation failures and handler failures into tool-result messages.
- Append all tool-result messages in the assistant's original tool-call order.
- Continue the loop with the next model call.

This keeps service reads fast while preserving deterministic transcript order. Tool handlers must be concurrency-safe. Tools that mutate important state should return proposed changes and let the LeapView application apply them through a separate approved workflow.

Normal tool failures must not cancel sibling tool calls. Only context cancellation or fatal harness errors should stop the batch. Every requested tool call should produce exactly one tool-result message.

The harness should defensively recover panics from tool handlers and convert them into `tool_panic` model-visible tool errors. A nil or otherwise invalid handler result is converted into `tool_result_invalid`. These safeguards protect the embedding process; tools should still be written as ordinary error-returning Go code.

Policy controls:

- Max concurrent tools per turn.
- Max total tool calls per run.
- Per-tool timeout.
- Absolute model-result and display-result byte ceilings.

Approval is not part of V1.

## Compaction

V1 includes automatic Pi-style transcript compaction. The rule is intentionally simple:

- Keep the last `KeepLastTurns` complete turns verbatim.
- Summarize all older turns into one `summary` message.
- Build future model requests from system prompt, summary message, and recent turns.

A complete turn means a user message, the following assistant message, and any tool-result messages produced by that assistant message. Compaction must not split a tool call from its result.

Illustrative shape:

```go
type CompactionConfig struct {
	// Number of recent complete turns kept verbatim.
	KeepLastTurns int

	// Trigger when estimated model input reaches this ratio of the context window.
	TriggerRatio float64

	// Optional. Defaults to the package summary prompt.
	SystemPrompt string
}
```

Default behavior:

- Always enabled in V1.
- Keep the last 8 complete turns.
- Trigger when estimated input reaches 70% of the configured context window.
- Use the main model for summarization.
- Use the same `Model` with `Purpose=compaction`.
- Set tools to empty in compaction model requests.
- Require a text response from the compaction request.
- If compaction fails, emit an error event and continue without compacting until a hard limit is reached.

Compaction triggers:

- Proactive: before every model call, estimate `system + summary + recent messages + tool definitions + ReserveOutputTokens`. If it reaches `TriggerRatio * ContextWindowTokens`, compact before calling the model.
- Post-turn: after a completed turn, compact if the same estimated context reaches the trigger.
- Context-overflow retry: if the provider rejects a turn model request for context length, run compaction once, rebuild the request, and retry once. Do not apply this retry to compaction requests, malformed requests, authentication errors, rate limits, or arbitrary provider failures.

If proactive compaction cannot make estimated input tokens fit under `HardInputLimitTokens`, the harness should stop with `context_limit` instead of dropping the active user message or splitting an assistant tool-call block from its tool results.

The summary prompt should ask the model to preserve:

- user goals and constraints
- decisions already made
- tool results that are still relevant
- pending tasks or unresolved questions
- IDs/names/paths/entities needed by future turns
- important failures and corrections

The summary prompt should exclude:

- irrelevant small talk
- duplicate tool output
- raw large result payloads when a concise description is enough

Compaction happens after a turn completes, before the next model call. The harness replaces the compacted prefix with the newest summary so repeated compactions refine the same summary plus any newly old turns.

Compaction affects only the harness model context. It does not delete events that have already been emitted to the application.

Events:

- `compaction_start`
- `compaction_end`
- `compaction_error`

## Errors

Errors should be classified with stable codes:

- `canceled`
- `busy`
- `invalid_argument`
- `invalid_state`
- `model`
- `tool`
- `compaction`
- `limit`
- `unknown`

Expected failures should be ordinary returned errors, not panics.

Tool handler errors default to model-visible tool errors unless marked fatal.

## Observability

The package emits events suitable for logs, metrics, tracing, and UI. It should not import OpenTelemetry in core.

The observer adapter can translate lifecycle events into spans:

- `agent.run`
- `agent.turn`
- `agent.model_call`
- `agent.tool_call`
- `agent.compaction`

Each span/event should carry safe metadata:

- run ID
- turn ID
- provider
- model
- tool name
- stop reason
- token usage
- duration
- error code

## Recommended Dependencies

Keep `pkg/agent` small, but use focused packages where they remove real implementation risk.

Recommended for V1:

- `golang.org/x/sync/errgroup`: bounded parallel tool execution with context cancellation. Use `SetLimit(MaxConcurrentTools)`. Do not return ordinary tool failures from goroutines; collect them into tool-result messages so sibling tools still complete.
- `github.com/santhosh-tekuri/jsonschema/v6`: JSON Schema validation for tool arguments. This avoids maintaining a partial schema validator while still keeping schemas OpenAI-compatible.

Recommended outside `pkg/agent` for LeapView wiring:

- `github.com/openai/openai-go`: concrete OpenAI-compatible provider adapter. Use it in LeapView application code or a future adapter package, not in the core harness.
- `github.com/openai/openai-go/option.WithBaseURL`: configure OpenAI-compatible providers such as DeepSeek, gateways, or local endpoints.

Possible later:

- `github.com/invopop/jsonschema`: generate JSON Schemas from Go structs if we want typed tool input structs to produce schemas automatically. This is generation, not validation, so it is not required for V1.

Avoid in core V1:

- OpenTelemetry packages.
- Retry frameworks.
- Durable workflow packages.
- Tokenizer packages unless token estimates prove too rough.
- Generic event bus packages.

## LeapView Integration Direction

LeapView should define the first concrete agent as a BI project assistant.

Likely host-owned tools:

- `list_dashboards`
- `get_dashboard`
- `get_page_state`
- `list_semantic_models`
- `describe_model`
- `run_metric_query`
- `explain_visual`
- `suggest_filter`
- `create_dashboard_draft`

These tools belong in LeapView application packages, not in `pkg/agent`. They should expose curated actions over LeapView services rather than raw filesystem, SQL, or network access.

Likely UI flow:

- A report page opens an agent panel.
- User messages go to a LeapView route.
- Route calls a configured `pkg/agent` harness.
- Harness events stream into Datastar signals.
- Tool handlers use existing semantic/dashboard/data services.
- Agent output can propose YAML changes first; applying edits remains a separate approved command.

## Package File Structure

All generic harness code lives under `pkg/agent`. LeapView-specific tools, prompts, routes, Datastar bindings, and service wiring live outside this package.

Proposed V1 structure:

```text
pkg/agent/
  spec.md
  doc.go
  agent.go
  definition.go
  loop.go
  model.go
  message.go
  tools.go
  validation.go
  compaction.go
  events.go
  errors.go
  limits.go
  ids.go
  clock.go

  agent_test.go
  loop_test.go
  tools_test.go
  validation_test.go
  compaction_test.go
  events_test.go
```

File responsibilities:

- `doc.go`: package documentation and high-level usage example.
- `agent.go`: public `Agent`/harness type, construction, `Prompt`, `Abort`, transcript accessors.
- `definition.go`: `Definition`, construction validation, defaulting, and option structs.
- `loop.go`: low-level model/tool loop and turn lifecycle.
- `model.go`: OpenAI-compatible `Model`, `ModelRequest`, `ModelResponse`, usage, finish reasons, and streaming delta types.
- `message.go`: message roles, content parts, transcript and model-context helpers.
- `tools.go`: `ToolDefinition`, `ToolCall`, `ToolResult`, tool registry, tool execution.
- `validation.go`: harness-level validation for definitions, tool calls, tool outputs, and limits.
- `compaction.go`: keep-last-N-turn compaction, proactive budget checks, context-overflow retry compaction, summary request creation, summary replacement.
- `events.go`: event types, event sink, sequencing, no-op sink.
- `errors.go`: stable error codes and helpers for model-visible tool error payloads.
- `limits.go`: limits, defaults, and token-estimation interfaces.
- `ids.go`: run/turn/message/tool-call ID helpers for deterministic tests.
- `clock.go`: clock abstraction for deterministic timestamps in tests.

Test focus:

- `agent_test.go`: public harness construction, prompt lifecycle, busy/abort behavior.
- `loop_test.go`: fake model loop paths, multi-turn tool cycles, stop conditions.
- `tools_test.go`: bounded parallel execution, deterministic result ordering, timeout/cancel handling, panic recovery, nil result handling.
- `validation_test.go`: unknown tool, malformed args, schema failures, oversized output, duplicate IDs.
- `compaction_test.go`: keep-last-turn boundaries, no split tool results, proactive compaction, context-overflow retry, summary replacement, no-tools summary request.
- `events_test.go`: event order, event payload IDs, severity, and correlation metadata.

V1 should keep one package namespace, `agent`, and avoid subpackages. Optional future adapters such as OpenAI SDK wiring should live outside the core first, likely in LeapView `internal/`, until the generic boundary proves stable.

## First Implementation Milestones

1. Create OpenAI-compatible types: messages, tool calls, tool results, model request/response, events, errors.
2. Implement in-memory loop with fake OpenAI-compatible model tests.
3. Add declarative tool registry with no built-in tools.
4. Add event subscription and streaming deltas.
5. Add tool-call validation and model-visible tool error results.
6. Add bounded parallel tool execution with deterministic result ordering.
7. Add automatic keep-last-N-turns compaction, proactive budget checks, and one context-overflow compaction retry.
8. Add limits for max turns, max tool calls, max concurrent tools, per-tool timeout, result size, and context window.
9. Wire a LeapView-specific agent package outside the generic core.

## Post-V1 Candidates

Add only when the embedded LeapView agent proves it needs them:

- durable session store and resume
- approval gates inside the harness
- app-level YAML profile loader outside `pkg/agent`
- multimodal message parts
- richer observability adapters

## Resolved V1 Decisions

- Package name: use `pkg/agent`.
- OpenAI SDK adapter: keep it outside `pkg/agent` for V1. LeapView can own the first adapter in `internal/`, using `openai-go` and `option.WithBaseURL` for OpenAI-compatible endpoints.
- Tool input schemas: expose schemas as `json.RawMessage` in public definitions and compile them internally with `github.com/santhosh-tekuri/jsonschema/v6`.
- First BI tool set: define it outside the generic package and start with the smallest useful read/action surface for the embedded LeapView assistant.
