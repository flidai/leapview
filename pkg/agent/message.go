package agent

type Role string

type MessageKind string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
	RoleSummary   Role = "summary"
)

const (
	MessageKindExternalContext        MessageKind = "external_context"
	messageKindExternalContextSummary MessageKind = "external_context_summary"
)

type Message struct {
	ID              string       `json:"id,omitempty"`
	OutputPartID    string       `json:"output_part_id,omitempty"`
	OutputOrdinal   int64        `json:"output_ordinal,omitempty"`
	ParentMessageID string       `json:"parent_message_id,omitempty"`
	Role            Role         `json:"role"`
	Kind            MessageKind  `json:"kind,omitempty"`
	Content         string       `json:"content,omitempty"`
	DisplayContent  any          `json:"display_content,omitempty"`
	ToolCalls       []ToolCall   `json:"tool_calls,omitempty"`
	ToolCallID      string       `json:"tool_call_id,omitempty"`
	ToolName        string       `json:"tool_name,omitempty"`
	IsError         bool         `json:"is_error,omitempty"`
	FinishReason    FinishReason `json:"finish_reason,omitempty"`
	Usage           Usage        `json:"usage,omitempty"`
}

func cloneMessages(messages []Message) []Message {
	out := make([]Message, len(messages))
	copy(out, messages)
	for i := range out {
		out[i].ToolCalls = append([]ToolCall(nil), out[i].ToolCalls...)
	}
	return out
}
