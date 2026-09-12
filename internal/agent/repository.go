package agent

import (
	"context"
	"encoding/json"
	"strings"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/pkg/jobs"
)

var ErrNotFound = apigenfailure.New("not_found", "agent record not found")
var ErrConversationArchived = apigenfailure.New("not_found", "agent conversation is archived")
var ErrConversationBusy = apigenfailure.New("conflict", "agent conversation has a running turn")

// ErrBusy is retained as the service-facing name used by prompt and HTTP
// callers. Keep it identical to ErrConversationBusy so repository lifecycle
// mutations and prompt admission share one conflict identity.
var ErrBusy = ErrConversationBusy
var ErrRequestConflict = apigenfailure.New("conflict", "agent request id conflicts with existing run")
var ErrTranscriptConflict = apigenfailure.New("conflict", "agent conversation transcript is stale")

const (
	ConversationDefaultTitle   = "New conversation"
	ConversationStatusActive   = "active"
	ConversationStatusArchived = "archived"
	// Deleted conversations remain physically stored so their transcript and
	// audit/run history are retained. The native stores represent this state as
	// an internal metadata marker alongside the archived status for schema
	// compatibility; repositories map it to this public status.
	ConversationStatusDeleted = "deleted"

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
	// TranscriptRevision increments on every successful transcript CAS write.
	// It is persisted separately from the transport ETag so prompt workers can
	// fence transcript state without relying on presentation fields.
	TranscriptRevision int64
	CreatedAt          string
	UpdatedAt          string
	ArchivedAt         string
	DeletedAt          string
	Pinned             bool
}

// ConversationMetadataKey is reserved inside metadata_json for durable chat
// management state. Keeping this small state in the existing metadata object
// lets SQLite installations adopt chat management without an unsafe live
// table rewrite, while the repository still exposes typed fields to callers.
const ConversationMetadataKey = "_leapview_chat"

type ConversationMetadata struct {
	Pinned    bool
	DeletedAt string
}

// ParseConversationMetadata reads the repository-owned chat metadata. Older
// rows and arbitrary metadata remain valid when the reserved object is absent.
func ParseConversationMetadata(raw string) (ConversationMetadata, error) {
	var document map[string]any
	if strings.TrimSpace(raw) == "" {
		return ConversationMetadata{}, nil
	}
	if err := json.Unmarshal([]byte(raw), &document); err != nil {
		return ConversationMetadata{}, err
	}
	state := ConversationMetadata{}
	reserved, ok := document[ConversationMetadataKey].(map[string]any)
	if !ok {
		return state, nil
	}
	if pinned, ok := reserved["pinned"].(bool); ok {
		state.Pinned = pinned
	}
	if deletedAt, ok := reserved["deletedAt"].(string); ok {
		state.DeletedAt = strings.TrimSpace(deletedAt)
	}
	return state, nil
}

// UpdateConversationMetadata changes only repository-owned chat state and
// preserves all caller metadata. A zero DeletedAt removes the delete marker.
func UpdateConversationMetadata(raw string, state ConversationMetadata) (string, error) {
	var document map[string]any
	if strings.TrimSpace(raw) == "" {
		document = map[string]any{}
	} else if err := json.Unmarshal([]byte(raw), &document); err != nil {
		return "", err
	} else if document == nil {
		document = map[string]any{}
	}
	reserved, _ := document[ConversationMetadataKey].(map[string]any)
	if reserved == nil {
		reserved = map[string]any{}
	}
	reserved["pinned"] = state.Pinned
	if strings.TrimSpace(state.DeletedAt) == "" {
		delete(reserved, "deletedAt")
	} else {
		reserved["deletedAt"] = strings.TrimSpace(state.DeletedAt)
	}
	if !state.Pinned && strings.TrimSpace(state.DeletedAt) == "" && len(reserved) == 1 {
		delete(document, ConversationMetadataKey)
	} else {
		document[ConversationMetadataKey] = reserved
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
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

// RunWorkflowAuditUnitOfWork is the durable command boundary used when a
// transport supplied an Access audit intent. Implementations commit the
// intent with the queued run transition and workflow enqueue.
type RunWorkflowAuditUnitOfWork interface {
	ActivateRunWorkflowWithAudit(context.Context, string, string, string, jobs.WorkflowIntent, *access.AuditIntent) (Run, error)
}

// RunTerminalWorkflow atomically persists a terminal run transition and its
// durable event. Implementations must make the event key idempotent.
type RunTerminalWorkflow interface {
	FinishRunWorkflow(context.Context, RunFinish, jobs.WorkflowIntent) (Run, bool, error)
}

// RunCompletionWorkflow atomically persists newly produced messages and
// transcript state with a fenced terminal transition and keyed event.
type RunCompletionWorkflow interface {
	CompleteRunWorkflow(context.Context, RunFinish, []MessageInput, string, int64, jobs.WorkflowIntent) ([]Message, bool, error)
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
	// UpdateConversationTranscript applies one compare-and-swap transcript
	// mutation and returns the incremented persisted revision.
	UpdateConversationTranscript(ctx context.Context, principalID, conversationID, transcriptJSON string, expectedRevision int64) (Conversation, error)
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

// ConversationManagementRepository is the optional lifecycle surface used by
// chat management transports. It intentionally remains separate from the
// historical Repository contract so small in-memory stores and callers that
// only execute prompts do not need to implement destructive operations.
// Implementations must scope every operation by principalID and keep child
// transcript/run/event rows when a conversation is deleted.
type ConversationManagementRepository interface {
	ListArchivedConversations(ctx context.Context, principalID string) ([]Conversation, error)
	ListArchivedConversationsPage(ctx context.Context, principalID string, page Page) ([]Conversation, error)
	RestoreConversation(ctx context.Context, principalID, conversationID string) (Conversation, error)
	SetConversationPinned(ctx context.Context, principalID, conversationID string, pinned bool) (Conversation, error)
	DeleteConversation(ctx context.Context, principalID, conversationID string) (Conversation, error)
	BulkArchiveConversations(ctx context.Context, principalID string, conversationIDs []string) ([]Conversation, error)
	BulkDeleteConversations(ctx context.Context, principalID string, conversationIDs []string) ([]Conversation, error)
}
