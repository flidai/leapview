package api

import "github.com/flidai/leapview/internal/agent"

type AgentConversationCreateRequest struct {
	Title string `json:"title"`
}

type AgentConversationUpdateRequest struct {
	Title string `json:"title"`
}

type AgentConversationResponse struct {
	ID              string `json:"id"`
	PrincipalID     string `json:"principalId"`
	Title           string `json:"title"`
	Status          string `json:"status"`
	Pinned          bool   `json:"pinned,omitempty"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
	ArchivedAt      string `json:"archivedAt,omitempty"`
	MessageCount    int    `json:"messageCount,omitempty"`
	LastMessageText string `json:"lastMessageText,omitempty"`
	TitlePending    bool   `json:"titlePending,omitempty"`
}

type AgentRunResponse struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversationId"`
	PrincipalID    string `json:"principalId"`
	Status         string `json:"status"`
	Model          string `json:"model,omitempty"`
	StopReason     string `json:"stopReason,omitempty"`
	InputTokens    int64  `json:"inputTokens,omitempty"`
	OutputTokens   int64  `json:"outputTokens,omitempty"`
	TotalTokens    int64  `json:"totalTokens,omitempty"`
	Error          string `json:"error,omitempty"`
	StartedAt      string `json:"startedAt"`
	CompletedAt    string `json:"completedAt,omitempty"`
	CreatedAt      string `json:"createdAt"`
}

type AgentMessageResponse struct {
	ID          string         `json:"id"`
	RunID       string         `json:"runId,omitempty"`
	Seq         int64          `json:"seq"`
	Role        string         `json:"role"`
	ContentText string         `json:"contentText,omitempty"`
	Content     map[string]any `json:"content,omitempty"`
	ToolCallID  string         `json:"toolCallId,omitempty"`
	ToolName    string         `json:"toolName,omitempty"`
	IsError     bool           `json:"isError,omitempty"`
	CreatedAt   string         `json:"createdAt"`
}

type AgentTurnRequest struct {
	Input         string `json:"input"`
	CorrelationID string `json:"correlationId,omitempty"`
}

type AgentEventResponse struct {
	ID           string         `json:"id"`
	Event        string         `json:"event"`
	ResourceType string         `json:"resourceType"`
	ResourceID   string         `json:"resourceId"`
	Data         map[string]any `json:"data"`
	CreatedAt    string         `json:"createdAt"`
}

type AdminAgentToolResponse struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Effect       string         `json:"effect"`
	Defaults     map[string]any `json:"defaults"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema"`
	Tags         []string       `json:"tags"`
}

type AdminAgentResponse struct {
	BaseURL                string `json:"baseUrl,omitempty"`
	APIMode                string `json:"apiMode,omitempty"`
	ConfigurationRevision  int64  `json:"configurationRevision"`
	AdminManaged           bool   `json:"adminManaged"`
	CredentialConfigured   bool   `json:"credentialConfigured"`
	ConfigurationAvailable bool   `json:"configurationAvailable"`
	TestToken              string `json:"testToken,omitempty"`
	TestMessage            string `json:"testMessage,omitempty"`

	Configured      bool                     `json:"configured"`
	Enabled         bool                     `json:"enabled"`
	Status          string                   `json:"status"`
	StatusDetail    string                   `json:"statusDetail,omitempty"`
	Model           string                   `json:"model,omitempty"`
	ReasoningEffort string                   `json:"reasoningEffort,omitempty"`
	SystemPrompt    string                   `json:"systemPrompt"`
	Tools           []AdminAgentToolResponse `json:"tools"`
}

type AdminAgentConfigPatchRequest struct {
	RestoreRevision  int64                     `json:"restoreRevision,omitempty"`
	Provider         *agent.ConfigurationInput `json:"provider,omitempty"`
	Action           string                    `json:"action,omitempty"`
	ExpectedRevision int64                     `json:"expectedRevision,omitempty"`
	TestToken        string                    `json:"testToken,omitempty"`

	SystemPrompt *string `json:"systemPrompt,omitempty"`
}
