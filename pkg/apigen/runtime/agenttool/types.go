// Package agenttool provides SDK-neutral runtime support for endpoint-derived agent tools.
package agenttool

import (
	"encoding/json"
	"fmt"
)

const (
	ErrorCodeInvalidArguments = "invalid_arguments"
	ErrorCodeMissingContext   = "missing_context"
	ErrorCodeInvalidResponse  = "invalid_response"
	ErrorCodeProjectionFailed = "projection_failed"
)

// Error is a stable runtime error suitable for mapping to an agent SDK.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func runtimeError(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Effect describes the side effects of a tool.
type Effect string

const (
	EffectRead            Effect = "read"
	EffectIdempotentWrite Effect = "idempotent-write"
	EffectWrite           Effect = "write"
	EffectDestructive     Effect = "destructive"
)

// Confirmation is the minimum confirmation requirement for a tool.
type Confirmation string

const (
	ConfirmationNever  Confirmation = "never"
	ConfirmationPolicy Confirmation = "policy"
	ConfirmationAlways Confirmation = "always"
)

// Context supplies trusted values that are not visible to the model.
type Context map[string]any

// Contract is a generated, SDK-neutral tool descriptor.
type Contract struct {
	Name         string          `json:"name"`
	OperationID  string          `json:"operation_id"`
	Method       string          `json:"method"`
	Path         string          `json:"path"`
	Description  string          `json:"description,omitempty"`
	Effect       Effect          `json:"effect"`
	Confirmation Confirmation    `json:"confirmation"`
	Tags         []string        `json:"tags,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
	// ResponseContentType is the JSON representation selected for tool output.
	// BuildRequest uses it as the default Accept header.
	ResponseContentType string         `json:"response_content_type,omitempty"`
	Bindings            []Binding      `json:"bindings,omitempty"`
	Output              Output         `json:"output"`
	Metadata            map[string]any `json:"metadata,omitempty"`
}

// Binding maps one model argument, context value, or default to HTTP transport.
type Binding struct {
	Argument    string      `json:"argument,omitempty"`
	Source      string      `json:"source"`
	WireName    string      `json:"wire_name"`
	Mode        string      `json:"mode"`
	ContextKey  string      `json:"context_key,omitempty"`
	Description string      `json:"description,omitempty"`
	Required    bool        `json:"required,omitempty"`
	Default     any         `json:"default,omitempty"`
	Explode     bool        `json:"explode,omitempty"`
	Schema      ValueSchema `json:"schema"`
}

// ValueSchema describes one HTTP transport value used by a binding or projection.
type ValueSchema struct {
	Type                 string       `json:"type,omitempty"`
	Format               string       `json:"format,omitempty"`
	Const                *float64     `json:"const,omitempty"`
	Enum                 []string     `json:"enum,omitempty"`
	Minimum              *float64     `json:"minimum,omitempty"`
	Maximum              *float64     `json:"maximum,omitempty"`
	MinLength            *int         `json:"min_length,omitempty"`
	MaxLength            *int         `json:"max_length,omitempty"`
	MinItems             *int         `json:"min_items,omitempty"`
	MaxItems             *int         `json:"max_items,omitempty"`
	Items                *ValueSchema `json:"items,omitempty"`
	AdditionalProperties bool         `json:"additional_properties,omitempty"`
}

// Output describes successful response handling.
type Output struct {
	Mode   string       `json:"mode"`
	Select []Projection `json:"select,omitempty"`
	Cursor *Cursor      `json:"cursor,omitempty"`
}

// Projection is a schema-resolved recursive response selector.
type Projection struct {
	Source   string       `json:"source"`
	Target   string       `json:"target"`
	Kind     string       `json:"kind"`
	Schema   ValueSchema  `json:"schema,omitempty"`
	Optional bool         `json:"optional,omitempty"`
	Select   []Projection `json:"select,omitempty"`
	CountAs  string       `json:"count_as,omitempty"`
}

// Cursor exposes pagination state in a stable result shape.
type Cursor struct {
	Source        string `json:"source"`
	Target        string `json:"target"`
	HasMoreTarget string `json:"has_more_target"`
	Optional      bool   `json:"optional,omitempty"`
}

// Result is the SDK-neutral outcome of projecting an HTTP response.
type Result struct {
	StatusCode int
	Content    any
	IsError    bool
}

// DecodeContracts decodes generated contract JSON and returns independent values.
func DecodeContracts(data []byte) (map[string]Contract, error) {
	var contracts map[string]Contract
	if err := json.Unmarshal(data, &contracts); err != nil {
		return nil, fmt.Errorf("decode agent tool contracts: %w", err)
	}
	return contracts, nil
}

// CloneContract returns a defensive deep copy.
func CloneContract(contract Contract) Contract {
	data, _ := json.Marshal(contract)
	var cloned Contract
	_ = json.Unmarshal(data, &cloned)
	return cloned
}
