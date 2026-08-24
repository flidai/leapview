package agent

type ChatTranscriptItem struct {
	ID              string          `json:"id"`
	Kind            string          `json:"kind"`
	OutputOrdinal   int64           `json:"outputOrdinal,omitempty"`
	ParentMessageID string          `json:"parentMessageId,omitempty"`
	Text            string          `json:"text,omitempty"`
	Markdown        string          `json:"markdown,omitempty"`
	ToolCallID      string          `json:"toolCallId,omitempty"`
	Name            string          `json:"name,omitempty"`
	Title           string          `json:"title,omitempty"`
	Status          string          `json:"status,omitempty"`
	Summary         string          `json:"summary,omitempty"`
	ResultSummary   string          `json:"resultSummary,omitempty"`
	InputJSON       string          `json:"inputJson,omitempty"`
	InputFormat     string          `json:"inputFormat,omitempty"`
	ArgumentsJSON   string          `json:"argumentsJson,omitempty"`
	ResultJSON      string          `json:"resultJson,omitempty"`
	ResultFormat    string          `json:"resultFormat,omitempty"`
	Artifact        *ChatArtifact   `json:"artifact,omitempty"`
	Error           string          `json:"error,omitempty"`
	ConversationID  string          `json:"conversationId,omitempty"`
	RunID           string          `json:"runId,omitempty"`
	CreatedAt       string          `json:"createdAt,omitempty"`
	References      []TurnReference `json:"references,omitempty"`
}

type ChatArtifact struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Summary string `json:"summary,omitempty"`
}

type ChatArtifactSignals struct {
	Visuals map[string]any `json:"visuals"`
}

type ChatTranscriptState struct {
	Transcript []ChatTranscriptItem
	Artifacts  ChatArtifactSignals
}

type EventEnvelope struct {
	ID             string         `json:"id"`
	ConversationID string         `json:"conversationId,omitempty"`
	RunID          string         `json:"runId,omitempty"`
	Seq            int64          `json:"seq"`
	Type           string         `json:"type"`
	Severity       string         `json:"severity,omitempty"`
	CreatedAt      string         `json:"createdAt,omitempty"`
	Payload        map[string]any `json:"payload,omitempty"`
}
