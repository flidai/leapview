package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	agentconfig "github.com/flidai/leapview/internal/agent/config"
	"github.com/flidai/leapview/internal/platform/jobs"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

var (
	ErrDisabled          = apigenfailure.New("unavailable", "agent is not configured")
	ErrBusy              = apigenfailure.New("conflict", "agent conversation already has a running turn")
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
	WorkspaceID   string
	PrincipalID   string
	Credential    CredentialScope
	DevAuthBypass bool
}

type CredentialScope struct {
	WorkspaceID string
	Privileges  []string
	Restricted  bool
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
func (s *Service) ConfigureRunWorkflow(recorder jobs.WorkflowRecorder) error {
	if s == nil || s.repo == nil || recorder == nil {
		return fmt.Errorf("agent run workflow recorder is required")
	}
	configurer, ok := s.repo.(interface{ ConfigureRunWorkflow(jobs.WorkflowRecorder) })
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

func (s *Service) SetModel(model agentcore.Model) {
	s.model = model
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

func (s *Service) CancelPersistedRun(ctx context.Context, scope Scope, conversationID, runID string) error {
	run, err := s.GetRun(ctx, scope, conversationID, runID)
	if err != nil {
		return err
	}
	if run.Status != RunStatusRunning {
		return ErrRunNotCancellable
	}
	s.release(conversationID)
	return s.finishRun(ctx, PromptInput{Scope: scope, ConversationID: conversationID}, runID, RunStatusCanceled, "", agentcore.Usage{}, context.Canceled)
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
	return false, s.finishRun(ctx, PromptInput{Scope: scope, ConversationID: conversationID}, runID, RunStatusCanceled, "", agentcore.Usage{}, context.Canceled)
}

// SupportsCancellationWorkflow reports whether queued cancellation can be
// committed atomically with its domain run and event. Callers must not apply
// the legacy two-step fallback after an atomic adapter reports an error: that
// would break its rollback guarantee.
func (s *Service) SupportsCancellationWorkflow() bool {
	if s == nil || s.repo == nil {
		return false
	}
	_, ok := s.repo.(RunCancellationWorkflow)
	return ok && s.runWorkflowAvailable()
}

// FailPersistedRun is the capability-owned recovery path for durable jobs
// that cannot reconstruct a StartedPrompt. It is deliberately idempotent:
// terminal runs are left untouched, while running/preparing runs transition
// exactly once using a bounded context independent of the worker lease.
func (s *Service) FailPersistedRun(ctx context.Context, scope Scope, conversationID, runID string, runErr error) error {
	_, err := s.FinalizePersistedRunFailure(ctx, scope, conversationID, runID, runErr)
	return err
}

// FinalizePersistedRunFailure reports whether this call performed the
// terminal transition. The boolean lets durable event publishers suppress
// duplicate notifications on redelivery.
func (s *Service) FinalizePersistedRunFailure(ctx context.Context, scope Scope, conversationID, runID string, runErr error) (bool, error) {
	return s.FinalizePersistedRunFailureWithWorkflow(ctx, scope, conversationID, runID, runErr, jobs.WorkflowIntent{})
}

func (s *Service) FinalizePersistedRunFailureWithWorkflow(ctx context.Context, scope Scope, conversationID, runID string, runErr error, workflow jobs.WorkflowIntent) (bool, error) {
	return s.finalizePersistedRunFailure(ctx, scope, conversationID, runID, runErr, workflow, "", jobs.Fence{})
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
	finishErr := s.finishRun(cleanupCtx, PromptInput{Scope: scope, ConversationID: conversationID}, runID, RunStatusFailed, "", agentcore.Usage{}, runErr)
	if finishErr == nil {
		return true, nil
	}
	// Legacy repositories lack the transactional terminalizer. Treat an
	// already-terminal row as an idempotent replay after the attempted update.
	if current, getErr := s.GetRun(cleanupCtx, scope, conversationID, runID); getErr == nil && current.Status != RunStatusRunning && current.Status != RunStatusPreparing {
		return false, nil
	}
	return false, finishErr
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

func (s *Service) ConversationEvents(ctx context.Context, scope Scope, conversationID string) ([]EventEnvelope, error) {
	if _, err := s.repo.GetConversation(ctx, scope.PrincipalID, conversationID); err != nil {
		return nil, err
	}
	messages, err := s.repo.ListMessages(ctx, scope.PrincipalID, conversationID)
	if err != nil {
		return nil, err
	}
	events := make([]EventEnvelope, 0, len(messages))
	for _, message := range messages {
		events = append(events, messageEnvelope(conversationID, message))
	}
	runs, err := s.repo.ListRuns(ctx, scope.PrincipalID, conversationID)
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		runEvents, err := s.repo.ListEvents(ctx, scope.PrincipalID, run.ID)
		if err != nil {
			return nil, err
		}
		for _, event := range runEvents {
			events = append(events, eventEnvelope(conversationID, event))
		}
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].CreatedAt == events[j].CreatedAt {
			return events[i].ID < events[j].ID
		}
		return events[i].CreatedAt < events[j].CreatedAt
	})
	return events, nil
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
	if _, err := s.repo.GetConversation(ctx, scope.PrincipalID, conversationID); err != nil {
		return ChatTranscriptState{}, err
	}
	messages, err := s.repo.ListMessages(ctx, scope.PrincipalID, conversationID)
	if err != nil {
		return ChatTranscriptState{}, err
	}
	return transcriptStateFromMessages(conversationID, messages), nil
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
