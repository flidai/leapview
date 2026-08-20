package agent

import (
	"context"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/pkg/jobs"
)

var ErrNotFound = apigenfailure.New("not_found", "agent record not found")
var ErrConversationArchived = apigenfailure.New("not_found", "agent conversation is archived")
var ErrRequestConflict = apigenfailure.New("conflict", "agent request id conflicts with existing run")

const (
	ConversationDefaultTitle   = "New conversation"
	ConversationStatusActive   = "active"
	ConversationStatusArchived = "archived"

	RunStatusRunning   = "running"
	RunStatusPreparing = "preparing"
	RunStatusCompleted = "completed"
	RunStatusFailed    = "failed"
	RunStatusCanceled  = "canceled"

	MessageRoleUser      = "user"
	MessageRoleAssistant = "assistant"
	MessageRoleTool      = "tool"
	MessageRoleSummary   = "summary"
)

// RunTerminationCause classifies why a terminal run stopped. Infrastructure
// interruptions (lease loss/shutdown) intentionally never reach a terminal
// transition; the remaining causes are persisted in run metadata so API and
// event consumers can distinguish user, provider, and deadline cancellation.
type RunTerminationCause string

const (
	RunCauseUserCanceled     RunTerminationCause = "user_canceled"
	RunCauseProviderCanceled RunTerminationCause = "provider_canceled"
	RunCauseDeadlineExceeded RunTerminationCause = "deadline_exceeded"
	RunCauseResumeFailure    RunTerminationCause = "resume_failure"
)

type Conversation struct {
	ID             string
	PrincipalID    string
	Title          string
	Status         string
	MetadataJSON   string
	TranscriptJSON string
	CreatedAt      string
	UpdatedAt      string
	ArchivedAt     string
}

type Page struct {
	Limit int
	After string
}

type Message struct {
	ID             string
	ConversationID string
	RunID          string
	Seq            int64
	Role           string
	ContentText    string
	ContentJSON    string
	ToolCallID     string
	ToolName       string
	IsError        bool
	CreatedAt      string
}

type Run struct {
	ID             string
	ConversationID string
	Status         string
	Model          string
	StopReason     string
	InputTokens    int64
	OutputTokens   int64
	TotalTokens    int64
	Error          string
	StartedAt      string
	FinishedAt     string
	MetadataJSON   string
	CreatedAt      string
}

type Event struct {
	ID          string
	RunID       string
	Seq         int64
	EventType   string
	Severity    string
	PayloadJSON string
	CreatedAt   string
}

type ConversationInput struct {
	PrincipalID  string
	Title        string
	MetadataJSON string
}

type ConversationUpdate struct {
	PrincipalID    string
	ConversationID string
	Title          string
}

type MessageInput struct {
	PrincipalID    string
	ConversationID string
	RunID          string
	Role           string
	ContentText    string
	ContentJSON    string
	ToolCallID     string
	ToolName       string
	IsError        bool
}

type RunInput struct {
	PrincipalID    string
	ConversationID string
	RunID          string
	Model          string
	MetadataJSON   string
	Status         string
}

type RunWorkflowUnitOfWork interface {
	ActivateRunWorkflow(context.Context, string, string, string, jobs.WorkflowIntent) (Run, error)
}

// RunTerminalWorkflow atomically persists a terminal run transition and its
// durable event. Implementations must make the event key idempotent.
type RunTerminalWorkflow interface {
	FinishRunWorkflow(context.Context, RunFinish, jobs.WorkflowIntent) (Run, bool, error)
}

// RunCompletionWorkflow atomically persists newly produced messages and
// transcript state with a fenced terminal transition and keyed event.
type RunCompletionWorkflow interface {
	CompleteRunWorkflow(context.Context, RunFinish, []MessageInput, string, jobs.WorkflowIntent) ([]Message, bool, error)
}

type RunCancellationWorkflow interface {
	CancelRunWorkflow(context.Context, RunFinish, string, jobs.WorkflowIntent) (bool, error)
}

type RunLeaseVerifier interface {
	VerifyRunLease(context.Context, string, string, jobs.Fence) error
}

type RunFinish struct {
	PrincipalID    string
	ConversationID string
	RunID          string
	Status         string
	StopReason     string
	InputTokens    int64
	OutputTokens   int64
	TotalTokens    int64
	Error          string
	MetadataJSON   string
	Cause          RunTerminationCause
	JobID          string
	JobFence       jobs.Fence
}

type EventInput struct {
	PrincipalID string
	RunID       string
	Sequence    int64
	EventType   string
	Severity    string
	PayloadJSON string
}

type Repository interface {
	CreateConversation(ctx context.Context, input ConversationInput) (Conversation, error)
	ListConversations(ctx context.Context, principalID string) ([]Conversation, error)
	ListConversationsPage(ctx context.Context, principalID string, page Page) ([]Conversation, error)
	GetConversation(ctx context.Context, principalID, conversationID string) (Conversation, error)
	UpdateConversation(ctx context.Context, input ConversationUpdate) (Conversation, error)
	// UpdateConversationAtomic evaluates the revision check after acquiring
	// the mutation lock and commits the check and update as one lifecycle.
	UpdateConversationAtomic(ctx context.Context, input ConversationUpdate, check func(Conversation) error) (Conversation, error)
	ArchiveConversation(ctx context.Context, principalID, conversationID string) (Conversation, error)
	UpdateDefaultConversationTitle(ctx context.Context, principalID, conversationID, title string) (Conversation, error)
	UpdateConversationTranscript(ctx context.Context, principalID, conversationID, transcriptJSON string) (Conversation, error)
	AppendMessage(ctx context.Context, input MessageInput) (Message, error)
	ListMessages(ctx context.Context, principalID, conversationID string) ([]Message, error)
	ListMessagesPage(ctx context.Context, principalID, conversationID string, page Page) ([]Message, error)
	CreateRun(ctx context.Context, input RunInput) (Run, error)
	FinishRun(ctx context.Context, input RunFinish) (Run, error)
	ListRuns(ctx context.Context, principalID, conversationID string) ([]Run, error)
	ListRunsPage(ctx context.Context, principalID, conversationID string, page Page) ([]Run, error)
	GetRun(ctx context.Context, principalID, conversationID, runID string) (Run, error)
	GetRunByID(ctx context.Context, principalID, runID string) (Run, error)
	AppendEvent(ctx context.Context, input EventInput) (Event, error)
	ListEvents(ctx context.Context, principalID, runID string) ([]Event, error)
	ListEventsPage(ctx context.Context, principalID, runID string, page Page) ([]Event, error)
}
