package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	agentconfig "github.com/flidai/leapview/internal/agent/config"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	agentcore "github.com/flidai/leapview/pkg/agent"
	"github.com/flidai/leapview/pkg/jobs"
)

var (
	ErrDisabled          = apigenfailure.New("unavailable", "agent is not configured")
	ErrRunNotCancellable = apigenfailure.New("not_cancellable", "agent run is not cancellable")
)

const (
	maxToolArgumentsPreviewBytes = 2000
	maxToolResultPreviewBytes    = 4000
)

func IsBusy(err error) bool {
	return errors.Is(err, ErrBusy)
}

type Scope struct {
	ProjectID      string
	PrincipalID    string
	GroupIDs       []string
	ConversationID string
	Credential     CredentialScope
	DevAuthBypass  bool
}

type CredentialScope struct {
	ProjectID    string
	Capabilities []string
	Restricted   bool
}

type ToolProvider func(scope Scope) []agentcore.ToolDefinition

type SystemPromptProvider func(ctx context.Context) (string, error)

type Service struct {
	repo   Repository
	config Config
	model  agentcore.Model

	toolProviders        []ToolProvider
	systemPromptProvider SystemPromptProvider

	mu             sync.Mutex
	running        map[string]runningPrompt
	promptWorkflow func(PromptInput, string, PromptDispatch) jobs.WorkflowIntent
}

func (s *Service) SetPromptWorkflow(factory func(PromptInput, string, PromptDispatch) jobs.WorkflowIntent) {
	if s != nil {
		s.promptWorkflow = factory
	}
}

// ConfigureRunWorkflow connects an externally constructed service to the
// application workflow recorder before prompt execution begins.
func (s *Service) ConfigureRunWorkflow(recorder jobplatform.WorkflowRecorder) error {
	if s == nil || s.repo == nil || recorder == nil {
		return fmt.Errorf("agent run workflow recorder is required")
	}
	configurer, ok := s.repo.(interface {
		ConfigureRunWorkflow(jobplatform.WorkflowRecorder)
	})
	if ok {
		configurer.ConfigureRunWorkflow(recorder)
	} else if !s.runWorkflowAvailable() {
		return fmt.Errorf("agent repository does not support durable workflow configuration")
	}
	if !s.runWorkflowAvailable() {
		return fmt.Errorf("agent repository did not enable durable workflows")
	}
	return nil
}

// ConfigureAuditIntentRecorder connects an externally constructed service to
// the transaction-scoped Access recorder used by its repository. Production
// composition may receive a prebuilt service, but transactional command audit
// must still be configured before the HTTP handlers are exposed.
func (s *Service) ConfigureAuditIntentRecorder(recorder access.AuditIntentRecorder) error {
	if s == nil || s.repo == nil || recorder == nil {
		return fmt.Errorf("agent audit intent recorder is required")
	}
	configurer, ok := s.repo.(interface {
		ConfigureAuditIntentRecorder(access.AuditIntentRecorder)
	})
	if !ok {
		return fmt.Errorf("agent repository does not support durable audit configuration")
	}
	configurer.ConfigureAuditIntentRecorder(recorder)
	return nil
}

func (s *Service) runWorkflowAvailable() bool {
	if s == nil || s.repo == nil {
		return false
	}
	if availability, ok := s.repo.(interface{ RunWorkflowAvailable() bool }); ok {
		return availability.RunWorkflowAvailable()
	}
	return s.promptWorkflow != nil
}

type runningPrompt struct {
	runID  string
	cancel context.CancelFunc
}

type ServiceOption func(*Service)

func WithModel(model agentcore.Model) ServiceOption {
	return func(s *Service) {
		s.model = model
	}
}

func NewService(repo Repository, config Config, options ...ServiceOption) *Service {
	s := &Service{
		repo:    repo,
		config:  config,
		running: map[string]runningPrompt{},
	}
	for _, option := range options {
		option(s)
	}
	return s
}

func (s *Service) ConfigureDefaultModel(factory func(Config) agentcore.Model) {
	if s == nil || s.model != nil || factory == nil || !s.config.Enabled() {
		return
	}
	s.model = factory(s.config)
}

func (s *Service) SetToolProviders(providers ...ToolProvider) {
	s.toolProviders = append([]ToolProvider(nil), providers...)
}

func (s *Service) AppendToolProviders(providers ...ToolProvider) {
	s.toolProviders = append(s.toolProviders, providers...)
}

func (s *Service) SetSystemPromptProvider(provider SystemPromptProvider) {
	s.systemPromptProvider = provider
}

func (s *Service) Enabled() bool {
	return s != nil && s.config.Enabled()
}

func (s *Service) ConversationRunning(conversationID string) bool {
	if s == nil || strings.TrimSpace(conversationID) == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.running[conversationID]
	return ok
}

func (s *Service) Model() string {
	if s == nil {
		return ""
	}
	return s.config.Model
}

func (s *Service) CreateConversation(ctx context.Context, scope Scope, title string) (Conversation, error) {
	if s.repo == nil {
		return Conversation{}, fmt.Errorf("agent store is required")
	}
	return s.repo.CreateConversation(ctx, ConversationInput{
		PrincipalID:  scope.PrincipalID,
		Title:        title,
		MetadataJSON: `{}`,
	})
}

func (s *Service) ListConversations(ctx context.Context, scope Scope) ([]Conversation, error) {
	return s.repo.ListConversations(ctx, scope.PrincipalID)
}

func (s *Service) ListConversationsPage(ctx context.Context, scope Scope, page Page) ([]Conversation, error) {
	return s.repo.ListConversationsPage(ctx, scope.PrincipalID, normalizePage(page))
}

func (s *Service) GetConversation(ctx context.Context, scope Scope, conversationID string) (Conversation, error) {
	return s.repo.GetConversation(ctx, scope.PrincipalID, conversationID)
}

func (s *Service) UpdateConversation(ctx context.Context, scope Scope, conversationID, title string) (Conversation, error) {
	return s.repo.UpdateConversation(ctx, ConversationUpdate{
		PrincipalID:    scope.PrincipalID,
		ConversationID: conversationID,
		Title:          title,
	})
}

// UpdateConversationWithRevision evaluates check against the current
// conversation inside the repository's required atomic mutation boundary.
func (s *Service) UpdateConversationWithRevision(ctx context.Context, scope Scope, conversationID, title string, check func(Conversation) error) (Conversation, error) {
	input := ConversationUpdate{PrincipalID: scope.PrincipalID, ConversationID: conversationID, Title: title}
	return s.repo.UpdateConversationAtomic(ctx, input, check)
}

func (s *Service) ArchiveConversation(ctx context.Context, scope Scope, conversationID string) (Conversation, error) {
	return s.repo.ArchiveConversation(ctx, scope.PrincipalID, conversationID)
}

func (s *Service) conversationManagementRepository() (ConversationManagementRepository, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("agent store is required")
	}
	management, ok := s.repo.(ConversationManagementRepository)
	if !ok {
		return nil, fmt.Errorf("agent conversation management is unavailable")
	}
	return management, nil
}

func (s *Service) rejectRunningConversation(conversationID string) error {
	if s != nil && s.ConversationRunning(conversationID) {
		return ErrConversationBusy
	}
	return nil
}

func (s *Service) ListArchivedConversations(ctx context.Context, scope Scope) ([]Conversation, error) {
	management, err := s.conversationManagementRepository()
	if err != nil {
		return nil, err
	}
	return management.ListArchivedConversations(ctx, scope.PrincipalID)
}

func (s *Service) ListArchivedConversationsPage(ctx context.Context, scope Scope, page Page) ([]Conversation, error) {
	management, err := s.conversationManagementRepository()
	if err != nil {
		return nil, err
	}
	return management.ListArchivedConversationsPage(ctx, scope.PrincipalID, normalizePage(page))
}

func (s *Service) RestoreConversation(ctx context.Context, scope Scope, conversationID string) (Conversation, error) {
	if err := s.rejectRunningConversation(conversationID); err != nil {
		return Conversation{}, err
	}
	management, err := s.conversationManagementRepository()
	if err != nil {
		return Conversation{}, err
	}
	return management.RestoreConversation(ctx, scope.PrincipalID, conversationID)
}

func (s *Service) SetConversationPinned(ctx context.Context, scope Scope, conversationID string, pinned bool) (Conversation, error) {
	management, err := s.conversationManagementRepository()
	if err != nil {
		return Conversation{}, err
	}
	return management.SetConversationPinned(ctx, scope.PrincipalID, conversationID, pinned)
}

func (s *Service) PinConversation(ctx context.Context, scope Scope, conversationID string) (Conversation, error) {
	return s.SetConversationPinned(ctx, scope, conversationID, true)
}

func (s *Service) UnpinConversation(ctx context.Context, scope Scope, conversationID string) (Conversation, error) {
	return s.SetConversationPinned(ctx, scope, conversationID, false)
}

func (s *Service) DeleteConversation(ctx context.Context, scope Scope, conversationID string) (Conversation, error) {
	if err := s.rejectRunningConversation(conversationID); err != nil {
		return Conversation{}, err
	}
	management, err := s.conversationManagementRepository()
	if err != nil {
		return Conversation{}, err
	}
	return management.DeleteConversation(ctx, scope.PrincipalID, conversationID)
}

func (s *Service) BulkArchiveConversations(ctx context.Context, scope Scope, conversationIDs []string) ([]Conversation, error) {
	management, err := s.conversationManagementRepository()
	if err != nil {
		return nil, err
	}
	return management.BulkArchiveConversations(ctx, scope.PrincipalID, conversationIDs)
}

func (s *Service) BulkDeleteConversations(ctx context.Context, scope Scope, conversationIDs []string) ([]Conversation, error) {
	for _, conversationID := range conversationIDs {
		if err := s.rejectRunningConversation(conversationID); err != nil {
			return nil, err
		}
	}
	management, err := s.conversationManagementRepository()
	if err != nil {
		return nil, err
	}
	return management.BulkDeleteConversations(ctx, scope.PrincipalID, conversationIDs)
}

// ManageConversation is the compact action surface used by HTTP and Datastar
// adapters. Bulk actions intentionally derive their IDs from the principal's
// own active/archived read models so a caller can never supply another user's
// conversation as an implicit bulk target.
func (s *Service) ManageConversation(ctx context.Context, scope Scope, action, conversationID string) error {
	action = strings.TrimSpace(action)
	conversationID = strings.TrimSpace(conversationID)
	switch action {
	case "pin":
		_, err := s.PinConversation(ctx, scope, conversationID)
		return err
	case "unpin":
		_, err := s.UnpinConversation(ctx, scope, conversationID)
		return err
	case "archive":
		_, err := s.ArchiveConversation(ctx, scope, conversationID)
		return err
	case "restore":
		_, err := s.RestoreConversation(ctx, scope, conversationID)
		return err
	case "delete":
		_, err := s.DeleteConversation(ctx, scope, conversationID)
		return err
	case "archive_all", "delete_all":
		active, err := s.listAllConversations(ctx, scope, false)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(active))
		for _, conversation := range active {
			ids = append(ids, conversation.ID)
		}
		if action == "archive_all" {
			_, err = s.BulkArchiveConversations(ctx, scope, ids)
			return err
		}
		archived, err := s.listAllConversations(ctx, scope, true)
		if err != nil {
			return err
		}
		for _, conversation := range archived {
			ids = append(ids, conversation.ID)
		}
		_, err = s.BulkDeleteConversations(ctx, scope, ids)
		return err
	default:
		return fmt.Errorf("unsupported conversation management action %q", action)
	}
}

func (s *Service) listAllConversations(ctx context.Context, scope Scope, archived bool) ([]Conversation, error) {
	all := make([]Conversation, 0)
	after := ""
	for {
		page := Page{Limit: 100, After: after}
		var rows []Conversation
		var err error
		if archived {
			rows, err = s.ListArchivedConversationsPage(ctx, scope, page)
		} else {
			rows, err = s.ListConversationsPage(ctx, scope, page)
		}
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
		if len(rows) < page.Limit {
			return all, nil
		}
		after = rows[len(rows)-1].ID
	}
}

func (s *Service) ListMessages(ctx context.Context, scope Scope, conversationID string) ([]Message, error) {
	return s.repo.ListMessages(ctx, scope.PrincipalID, conversationID)
}

func (s *Service) ListMessagesPage(ctx context.Context, scope Scope, conversationID string, page Page) ([]Message, error) {
	return s.repo.ListMessagesPage(ctx, scope.PrincipalID, conversationID, normalizePage(page))
}

func (s *Service) ListRunsPage(ctx context.Context, scope Scope, conversationID string, page Page) ([]Run, error) {
	return s.repo.ListRunsPage(ctx, scope.PrincipalID, conversationID, normalizePage(page))
}

func (s *Service) GetRun(ctx context.Context, scope Scope, conversationID, runID string) (Run, error) {
	return s.repo.GetRun(ctx, scope.PrincipalID, conversationID, runID)
}

func (s *Service) CancelRun(ctx context.Context, scope Scope, conversationID, runID string) error {
	run, err := s.GetRun(ctx, scope, conversationID, runID)
	if err != nil {
		return err
	}
	if run.Status != RunStatusRunning {
		return ErrRunNotCancellable
	}
	s.mu.Lock()
	active, ok := s.running[conversationID]
	if !ok || active.runID != runID || active.cancel == nil {
		s.mu.Unlock()
		return ErrRunNotCancellable
	}
	cancel := active.cancel
	s.mu.Unlock()
	cancel()
	return nil
}

// CancelPersistedRunWithWorkflow atomically records an explicit cancellation
// and its terminal event when the repository supports transactional workflow
// intents. Queued jobs have no worker lease to fence, but cancellation still
// needs an idempotent status/event transition.
func (s *Service) CancelPersistedRunWithWorkflow(ctx context.Context, scope Scope, conversationID, runID string, workflow jobs.WorkflowIntent) (bool, error) {
	run, err := s.GetRun(ctx, scope, conversationID, runID)
	if err != nil {
		return false, err
	}
	if run.Status != RunStatusRunning && run.Status != RunStatusPreparing {
		return false, ErrRunNotCancellable
	}
	finish := RunFinish{PrincipalID: scope.PrincipalID, ConversationID: conversationID, RunID: runID, Status: RunStatusCanceled, Error: context.Canceled.Error(), MetadataJSON: metadataJSON(map[string]any{"model": s.config.Model, "terminationCause": RunCauseUserCanceled}), Cause: RunCauseUserCanceled}
	if cancellation, ok := s.repo.(RunCancellationWorkflow); ok && s.runWorkflowAvailable() {
		return cancellation.CancelRunWorkflow(context.WithoutCancel(ctx), finish, "agent:"+runID+":run", workflow)
	}
	if terminalizer, ok := s.repo.(RunTerminalWorkflow); ok && s.runWorkflowAvailable() && workflow.Event.Key != "" {
		_, _, err := terminalizer.FinishRunWorkflow(context.WithoutCancel(ctx), finish, workflow)
		return true, err
	}
	return false, fmt.Errorf("transactional run cancellation workflow is unavailable")
}

func (s *Service) FinalizePersistedRunFailureWithClaim(ctx context.Context, scope Scope, conversationID, runID string, runErr error, workflow jobs.WorkflowIntent, jobID string, fence jobs.Fence) (bool, error) {
	return s.finalizePersistedRunFailure(ctx, scope, conversationID, runID, runErr, workflow, jobID, fence)
}

func (s *Service) finalizePersistedRunFailure(ctx context.Context, scope Scope, conversationID, runID string, runErr error, workflow jobs.WorkflowIntent, jobID string, fence jobs.Fence) (bool, error) {
	if runErr == nil {
		runErr = fmt.Errorf("durable prompt resume failed")
	}
	// Persist only a bounded, non-sensitive diagnostic. Callers may retain the
	// original error for local logs, but provider/store internals must not leak
	// into durable API state.
	errText := runErr.Error()
	if len(errText) > 512 {
		errText = errText[:512]
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	finish := RunFinish{PrincipalID: scope.PrincipalID, ConversationID: conversationID, RunID: runID, Status: RunStatusFailed, Error: errText, MetadataJSON: metadataJSON(map[string]any{"model": s.config.Model, "terminationCause": RunCauseResumeFailure}), Cause: RunCauseResumeFailure}
	finish.JobID, finish.JobFence = jobID, fence
	if terminalizer, ok := s.repo.(RunTerminalWorkflow); ok && s.runWorkflowAvailable() && workflow.Event.Key != "" {
		_, transitioned, err := terminalizer.FinishRunWorkflow(cleanupCtx, finish, workflow)
		return transitioned, err
	}
	return false, fmt.Errorf("transactional run failure workflow is unavailable")
}

func (s *Service) GetRunByID(ctx context.Context, scope Scope, runID string) (Run, error) {
	return s.repo.GetRunByID(ctx, scope.PrincipalID, runID)
}

func (s *Service) ListEvents(ctx context.Context, scope Scope, runID string) ([]Event, error) {
	return s.repo.ListEvents(ctx, scope.PrincipalID, runID)
}

func (s *Service) ListRunEventsPage(ctx context.Context, scope Scope, conversationID, runID string, page Page) ([]Event, error) {
	if _, err := s.repo.GetRun(ctx, scope.PrincipalID, conversationID, runID); err != nil {
		return nil, err
	}
	return s.repo.ListEventsPage(ctx, scope.PrincipalID, runID, normalizePage(page))
}

func normalizePage(page Page) Page {
	if page.Limit <= 0 || page.Limit > 100 {
		page.Limit = 100
	}
	return page
}

func (s *Service) ConversationTranscript(ctx context.Context, scope Scope, conversationID string) ([]ChatTranscriptItem, error) {
	state, err := s.ConversationTranscriptState(ctx, scope, conversationID)
	if err != nil {
		return nil, err
	}
	return state.Transcript, nil
}

func (s *Service) ConversationTranscriptState(ctx context.Context, scope Scope, conversationID string) (ChatTranscriptState, error) {
	_, err := s.repo.GetConversation(ctx, scope.PrincipalID, conversationID)
	if err != nil {
		return ChatTranscriptState{}, err
	}
	messages, err := s.repo.ListMessages(ctx, scope.PrincipalID, conversationID)
	if err != nil {
		return ChatTranscriptState{}, err
	}
	// TranscriptJSON drives model context and edit validation. Message rows are
	// retained as immutable history; successful edit user rows carry a target
	// marker so the active UI projection can retain complete multipart/tool
	// output rows while excluding the replaced branch.
	return transcriptStateFromMessages(conversationID, activeMessageProjection(messages)), nil
}

func (s *Service) systemPrompt(ctx context.Context) (string, error) {
	if s != nil && s.systemPromptProvider != nil {
		prompt, err := s.systemPromptProvider(ctx)
		if err != nil {
			return "", err
		}
		return agentconfig.NormalizeSystemPrompt(prompt)
	}
	return agentconfig.DefaultSystemPrompt, nil
}
