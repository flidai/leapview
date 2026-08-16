package sqlite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/agent"
	platformdb "github.com/flidai/leapview/internal/agent/internal/db"
	"github.com/flidai/leapview/internal/platform/jobs"
)

type Repository struct {
	db       *sql.DB
	q        *platformdb.Queries
	events   jobs.Repository
	workflow jobs.WorkflowRecorder
}

func NewRepository(sqlDB *sql.DB) *Repository {
	return NewRepositoryWithEvents(sqlDB, nil)
}

func NewRepositoryWithEvents(sqlDB *sql.DB, events jobs.Repository) *Repository {
	return &Repository{db: sqlDB, q: platformdb.New(sqlDB), events: events}
}

func NewRepositoryWithWorkflow(sqlDB *sql.DB, events jobs.Repository, workflow jobs.WorkflowRecorder) *Repository {
	return &Repository{db: sqlDB, q: platformdb.New(sqlDB), events: events, workflow: workflow}
}

func (r *Repository) RunWorkflowAvailable() bool {
	return r != nil && r.workflow != nil
}

// ConfigureRunWorkflow wires the shared transaction-capable workflow recorder
// into repositories used to construct an agent service outside the module.
func (r *Repository) ConfigureRunWorkflow(workflow jobs.WorkflowRecorder) {
	if r != nil {
		r.workflow = workflow
		if events, ok := workflow.(jobs.Repository); ok {
			r.events = events
		}
	}
}

func validAgentJobClaim(ctx context.Context, q *platformdb.Queries, jobID, runID string, fence jobs.Fence) bool {
	if q == nil {
		return false
	}
	claim, err := q.GetAgentRunJobClaim(ctx, platformdb.GetAgentRunJobClaimParams{JobID: jobID, RunID: runID})
	return err == nil && claim.Status == string(jobs.StatusRunning) && claim.LeaseOwner == fence.Owner &&
		claim.LeaseGeneration == fence.Generation && claim.LeaseExpiresAt.Valid && claim.LeaseValid == 1
}

func (r *Repository) CreateConversation(ctx context.Context, input agent.ConversationInput) (agent.Conversation, error) {
	metadata, err := normalizedJSONObject(input.MetadataJSON)
	if err != nil {
		return agent.Conversation{}, err
	}
	principalID, err := agentPrincipalID(input.PrincipalID)
	if err != nil {
		return agent.Conversation{}, err
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = agent.ConversationDefaultTitle
	}
	row, err := r.q.CreateAgentConversation(ctx, platformdb.CreateAgentConversationParams{
		ID:             newID("agentconv"),
		PrincipalID:    principalID,
		Title:          title,
		Status:         agent.ConversationStatusActive,
		MetadataJson:   metadata,
		TranscriptJson: "[]",
	})
	if err != nil {
		return agent.Conversation{}, err
	}
	return mapConversation(row), nil
}

func (r *Repository) ListConversations(ctx context.Context, principalID string) ([]agent.Conversation, error) {
	return r.ListConversationsPage(ctx, principalID, agent.Page{})
}

func (r *Repository) ListConversationsPage(ctx context.Context, principalID string, page agent.Page) ([]agent.Conversation, error) {
	principalID, err := agentPrincipalID(principalID)
	if err != nil {
		return nil, err
	}
	rows, err := r.q.ListAgentConversations(ctx, principalID)
	if err != nil {
		return nil, err
	}
	out := make([]agent.Conversation, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapConversation(row))
	}
	return pageByID(out, page, func(row agent.Conversation) string { return row.ID }), nil
}

func (r *Repository) GetConversation(ctx context.Context, principalID, conversationID string) (agent.Conversation, error) {
	principalID, err := agentPrincipalID(principalID)
	if err != nil {
		return agent.Conversation{}, err
	}
	if strings.TrimSpace(conversationID) == "" {
		return agent.Conversation{}, fmt.Errorf("conversation id is required")
	}
	row, err := r.q.GetAgentConversation(ctx, platformdb.GetAgentConversationParams{
		ID:          conversationID,
		PrincipalID: principalID,
	})
	if err != nil {
		return agent.Conversation{}, err
	}
	return mapConversation(row), nil
}

func (r *Repository) UpdateConversation(ctx context.Context, input agent.ConversationUpdate) (agent.Conversation, error) {
	principalID, err := agentPrincipalID(input.PrincipalID)
	if err != nil {
		return agent.Conversation{}, err
	}
	if strings.TrimSpace(input.ConversationID) == "" {
		return agent.Conversation{}, fmt.Errorf("conversation id is required")
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return agent.Conversation{}, fmt.Errorf("conversation title is required")
	}
	row, err := r.q.UpdateAgentConversationTitle(ctx, platformdb.UpdateAgentConversationTitleParams{
		Title: title, ConversationID: input.ConversationID, PrincipalID: principalID,
	})
	if err != nil {
		return agent.Conversation{}, err
	}
	return mapConversation(row), nil
}

// UpdateConversationAtomic serializes the revision check and title mutation
// in one SQLite transaction. The no-op UPDATE acquires SQLite's writer lock
// before reading the current row, so another writer cannot advance the
// conversation between the check and the mutation.
func (r *Repository) UpdateConversationAtomic(ctx context.Context, input agent.ConversationUpdate, check func(agent.Conversation) error) (agent.Conversation, error) {
	if r == nil || r.db == nil {
		return agent.Conversation{}, fmt.Errorf("agent repository database is required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return agent.Conversation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	txQueries := r.q.WithTx(tx)
	if err := txQueries.AcquireAgentConversationMutationLock(ctx, platformdb.AcquireAgentConversationMutationLockParams{
		ConversationID: input.ConversationID,
		PrincipalID:    input.PrincipalID,
	}); err != nil {
		return agent.Conversation{}, err
	}
	// The transaction-bound sqlc handle owns all reads/writes below; retain
	// the root DB only for the repository's non-query capability field.
	txRepo := &Repository{db: r.db, q: txQueries, events: r.events, workflow: r.workflow}
	current, err := txRepo.GetConversation(ctx, input.PrincipalID, input.ConversationID)
	if err != nil {
		return agent.Conversation{}, err
	}
	if check != nil {
		if err := check(current); err != nil {
			return agent.Conversation{}, err
		}
	}
	updated, err := txRepo.UpdateConversation(ctx, input)
	if err != nil {
		return agent.Conversation{}, err
	}
	if err := tx.Commit(); err != nil {
		return agent.Conversation{}, err
	}
	return updated, nil
}

func (r *Repository) ArchiveConversation(ctx context.Context, principalID, conversationID string) (agent.Conversation, error) {
	principalID, err := agentPrincipalID(principalID)
	if err != nil {
		return agent.Conversation{}, err
	}
	if strings.TrimSpace(conversationID) == "" {
		return agent.Conversation{}, fmt.Errorf("conversation id is required")
	}
	row, err := r.q.ArchiveAgentConversation(ctx, platformdb.ArchiveAgentConversationParams{
		ID:          conversationID,
		PrincipalID: principalID,
	})
	if err != nil {
		return agent.Conversation{}, err
	}
	return mapConversation(row), nil
}

func (r *Repository) UpdateDefaultConversationTitle(ctx context.Context, principalID, conversationID, title string) (agent.Conversation, error) {
	principalID, err := agentPrincipalID(principalID)
	if err != nil {
		return agent.Conversation{}, err
	}
	if strings.TrimSpace(conversationID) == "" {
		return agent.Conversation{}, fmt.Errorf("conversation id is required")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return agent.Conversation{}, fmt.Errorf("conversation title is required")
	}
	row, err := r.q.UpdateDefaultAgentConversationTitle(ctx, platformdb.UpdateDefaultAgentConversationTitleParams{
		Title:       title,
		ID:          conversationID,
		PrincipalID: principalID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return agent.Conversation{}, agent.ErrNotFound
		}
		return agent.Conversation{}, err
	}
	return mapConversation(row), nil
}

func (r *Repository) UpdateConversationTranscript(ctx context.Context, principalID, conversationID, transcriptJSON string) (agent.Conversation, error) {
	transcript, err := normalizedJSONArray(transcriptJSON)
	if err != nil {
		return agent.Conversation{}, err
	}
	principalID, err = agentPrincipalID(principalID)
	if err != nil {
		return agent.Conversation{}, err
	}
	if strings.TrimSpace(conversationID) == "" {
		return agent.Conversation{}, fmt.Errorf("conversation id is required")
	}
	row, err := r.q.UpdateAgentConversationTranscript(ctx, platformdb.UpdateAgentConversationTranscriptParams{
		TranscriptJson: transcript,
		ID:             conversationID,
		PrincipalID:    principalID,
	})
	if err != nil {
		return agent.Conversation{}, err
	}
	return mapConversation(row), nil
}

func (r *Repository) AppendMessage(ctx context.Context, input agent.MessageInput) (agent.Message, error) {
	content, err := normalizedJSONObject(input.ContentJSON)
	if err != nil {
		return agent.Message{}, err
	}
	if !validMessageRole(input.Role) {
		return agent.Message{}, fmt.Errorf("invalid agent message role %q", input.Role)
	}
	principalID, err := agentPrincipalID(input.PrincipalID)
	if err != nil {
		return agent.Message{}, err
	}
	row, err := r.q.AppendAgentMessage(ctx, platformdb.AppendAgentMessageParams{
		ID:             newID("agentmsg"),
		RunID:          input.RunID,
		Role:           input.Role,
		ContentText:    input.ContentText,
		ContentJson:    content,
		ToolCallID:     input.ToolCallID,
		ToolName:       input.ToolName,
		IsError:        input.IsError,
		ConversationID: input.ConversationID,
		PrincipalID:    principalID,
	})
	if err != nil {
		return agent.Message{}, err
	}
	return mapMessage(row), nil
}

func (r *Repository) ListMessages(ctx context.Context, principalID, conversationID string) ([]agent.Message, error) {
	return r.ListMessagesPage(ctx, principalID, conversationID, agent.Page{})
}

func (r *Repository) ListMessagesPage(ctx context.Context, principalID, conversationID string, page agent.Page) ([]agent.Message, error) {
	principalID, err := agentPrincipalID(principalID)
	if err != nil {
		return nil, err
	}
	if _, err := r.GetConversation(ctx, principalID, conversationID); err != nil {
		return nil, err
	}
	rows, err := r.q.ListAgentMessages(ctx, platformdb.ListAgentMessagesParams{
		ConversationID: conversationID,
		PrincipalID:    principalID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]agent.Message, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapMessage(row))
	}
	return pageByID(out, page, func(row agent.Message) string { return row.ID }), nil
}

func (r *Repository) CreateRun(ctx context.Context, input agent.RunInput) (agent.Run, error) {
	metadata, err := normalizedJSONObject(input.MetadataJSON)
	if err != nil {
		return agent.Run{}, err
	}
	principalID, err := agentPrincipalID(input.PrincipalID)
	if err != nil {
		return agent.Run{}, err
	}
	if _, err := r.GetConversation(ctx, principalID, input.ConversationID); err != nil {
		return agent.Run{}, err
	}
	runID := strings.TrimSpace(input.RunID)
	if runID == "" {
		runID = newID("agentrun")
	}
	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = agent.RunStatusRunning
	}
	if status != agent.RunStatusRunning && status != agent.RunStatusPreparing {
		return agent.Run{}, fmt.Errorf("invalid initial agent run status %q", status)
	}
	row, err := r.q.CreateAgentRun(ctx, platformdb.CreateAgentRunParams{
		ID:             runID,
		Status:         status,
		Model:          input.Model,
		MetadataJson:   metadata,
		ConversationID: input.ConversationID,
		PrincipalID:    principalID,
	})
	if err != nil {
		return agent.Run{}, err
	}
	return mapRun(row), nil
}

func (r *Repository) ActivateRunWorkflow(ctx context.Context, principalID, conversationID, runID string, workflow jobs.WorkflowIntent) (agent.Run, error) {
	if r.workflow == nil {
		return agent.Run{}, fmt.Errorf("agent workflow recorder is required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return agent.Run{}, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	changed, err := q.ActivateAgentRun(ctx, platformdb.ActivateAgentRunParams{
		RunID: runID, ConversationID: conversationID, PrincipalID: principalID,
	})
	if err != nil {
		return agent.Run{}, err
	}
	if changed != 1 {
		row, getErr := q.GetAgentRunInConversation(ctx, platformdb.GetAgentRunInConversationParams{RunID: runID, ConversationID: conversationID, PrincipalID: principalID})
		if getErr != nil || row.Status != agent.RunStatusRunning {
			return agent.Run{}, fmt.Errorf("agent run changed while queueing")
		}
	}
	if err := r.workflow.RecordWorkflow(ctx, tx, workflow); err != nil {
		return agent.Run{}, err
	}
	row, err := q.GetAgentRunInConversation(ctx, platformdb.GetAgentRunInConversationParams{RunID: runID, ConversationID: conversationID, PrincipalID: principalID})
	if err != nil {
		return agent.Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return agent.Run{}, err
	}
	return mapRun(row), nil
}

func (r *Repository) FinishRunWorkflow(ctx context.Context, input agent.RunFinish, workflow jobs.WorkflowIntent) (agent.Run, bool, error) {
	if r.workflow == nil {
		return agent.Run{}, false, fmt.Errorf("agent workflow recorder is required")
	}
	principalID, err := agentPrincipalID(input.PrincipalID)
	if err != nil {
		return agent.Run{}, false, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return agent.Run{}, false, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	if input.JobID != "" {
		if !validAgentJobClaim(ctx, q, input.JobID, input.RunID, input.JobFence) {
			return agent.Run{}, false, fmt.Errorf("stale durable job claim")
		}
	}
	priorStatus, err := q.GetAgentRunStatus(ctx, platformdb.GetAgentRunStatusParams{RunID: input.RunID, ConversationID: input.ConversationID})
	if err != nil {
		return agent.Run{}, false, err
	}
	if priorStatus != agent.RunStatusRunning && priorStatus != agent.RunStatusPreparing {
		current, getErr := q.GetAgentRunInConversation(ctx, platformdb.GetAgentRunInConversationParams{RunID: input.RunID, ConversationID: input.ConversationID, PrincipalID: principalID})
		if getErr != nil {
			return agent.Run{}, false, getErr
		}
		if current.Status == agent.RunStatusCompleted || current.Status == agent.RunStatusFailed || current.Status == agent.RunStatusCanceled {
			return mapRun(current), false, nil
		}
		return agent.Run{}, false, fmt.Errorf("agent run is not terminalizable from status %q", priorStatus)
	}
	row, err := q.FinishAgentRun(ctx, platformdb.FinishAgentRunParams{Status: input.Status, StopReason: input.StopReason, InputTokens: input.InputTokens, OutputTokens: input.OutputTokens, TotalTokens: input.TotalTokens, Error: input.Error, MetadataJson: input.MetadataJSON, ID: input.RunID, ConversationID: input.ConversationID, PrincipalID: principalID})
	if err != nil {
		current, getErr := q.GetAgentRunInConversation(ctx, platformdb.GetAgentRunInConversationParams{RunID: input.RunID, ConversationID: input.ConversationID, PrincipalID: principalID})
		if getErr != nil {
			return agent.Run{}, false, err
		}
		if current.Status != agent.RunStatusCompleted && current.Status != agent.RunStatusFailed && current.Status != agent.RunStatusCanceled {
			return agent.Run{}, false, err
		}
		return mapRun(current), false, nil
	}
	if err := r.workflow.RecordWorkflow(ctx, tx, workflow); err != nil {
		return agent.Run{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return agent.Run{}, false, err
	}
	return mapRun(row), true, nil
}

// CompleteRunWorkflow is the fenced durable completion boundary. All output
// mutations and the terminal event commit together, so a reclaimed worker
// cannot write messages or transcript state after losing its lease.
func (r *Repository) CompleteRunWorkflow(ctx context.Context, input agent.RunFinish, messages []agent.MessageInput, transcriptJSON string, workflow jobs.WorkflowIntent) ([]agent.Message, bool, error) {
	if r.workflow == nil {
		return nil, false, fmt.Errorf("agent workflow recorder is required")
	}
	principalID, err := agentPrincipalID(input.PrincipalID)
	if err != nil {
		return nil, false, err
	}
	transcript, err := normalizedJSONArray(transcriptJSON)
	if err != nil {
		return nil, false, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	if input.JobID != "" {
		if !validAgentJobClaim(ctx, q, input.JobID, input.RunID, input.JobFence) {
			return nil, false, fmt.Errorf("stale durable job claim")
		}
	}
	priorStatus, err := q.GetAgentRunStatus(ctx, platformdb.GetAgentRunStatusParams{RunID: input.RunID, ConversationID: input.ConversationID})
	if err != nil {
		return nil, false, err
	}
	if priorStatus != agent.RunStatusRunning && priorStatus != agent.RunStatusPreparing {
		return nil, false, nil
	}
	rows := make([]agent.Message, 0, len(messages))
	for _, message := range messages {
		if message.PrincipalID != input.PrincipalID || message.ConversationID != input.ConversationID || message.RunID != input.RunID {
			return nil, false, fmt.Errorf("message binding mismatch")
		}
		content, err := normalizedJSONObject(message.ContentJSON)
		if err != nil {
			return nil, false, err
		}
		row, err := q.AppendAgentMessage(ctx, platformdb.AppendAgentMessageParams{ID: newID("agentmsg"), RunID: input.RunID, Role: message.Role, ContentText: message.ContentText, ContentJson: content, ToolCallID: message.ToolCallID, ToolName: message.ToolName, IsError: message.IsError, ConversationID: message.ConversationID, PrincipalID: principalID})
		if err != nil {
			return nil, false, err
		}
		rows = append(rows, mapMessage(row))
	}
	if _, err := q.UpdateAgentConversationTranscript(ctx, platformdb.UpdateAgentConversationTranscriptParams{TranscriptJson: transcript, ID: input.ConversationID, PrincipalID: principalID}); err != nil {
		return nil, false, err
	}
	_, err = q.FinishAgentRun(ctx, platformdb.FinishAgentRunParams{Status: input.Status, StopReason: input.StopReason, InputTokens: input.InputTokens, OutputTokens: input.OutputTokens, TotalTokens: input.TotalTokens, Error: input.Error, MetadataJson: input.MetadataJSON, ID: input.RunID, ConversationID: input.ConversationID, PrincipalID: principalID})
	if err != nil {
		return nil, false, err
	}
	if err := r.workflow.RecordWorkflow(ctx, tx, workflow); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return rows, true, nil
}

// CancelRunWorkflow atomically cancels a queued agent job, terminalizes its
// matching domain run, and records the keyed cancellation event. Replays are
// idempotent when either side is already terminal from an older worker.
func (r *Repository) CancelRunWorkflow(ctx context.Context, input agent.RunFinish, jobID string, workflow jobs.WorkflowIntent) (bool, error) {
	if r.workflow == nil {
		return false, fmt.Errorf("agent workflow recorder is required")
	}
	principalID, err := agentPrincipalID(input.PrincipalID)
	if err != nil {
		return false, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	jobStatus, err := q.GetAgentRunJobStatus(ctx, platformdb.GetAgentRunJobStatusParams{JobID: jobID, RunID: input.RunID})
	if err != nil {
		return false, err
	}
	if jobStatus != string(jobs.StatusQueued) && jobStatus != string(jobs.StatusCancelled) {
		return false, fmt.Errorf("agent job is not cancellable")
	}
	if jobStatus == string(jobs.StatusQueued) {
		if changed, err := q.CancelQueuedAgentRunJob(ctx, platformdb.CancelQueuedAgentRunJobParams{JobID: jobID, RunID: input.RunID}); err != nil {
			return false, err
		} else if changed != 1 {
			return false, fmt.Errorf("agent job is not cancellable")
		}
	}
	transitioned := true
	priorStatus, err := q.GetAgentRunStatus(ctx, platformdb.GetAgentRunStatusParams{RunID: input.RunID, ConversationID: input.ConversationID})
	if err != nil {
		return false, err
	}
	var row platformdb.AgentRun
	var finishErr error
	if priorStatus == agent.RunStatusCanceled {
		transitioned = false
		row, finishErr = q.GetAgentRunInConversation(ctx, platformdb.GetAgentRunInConversationParams{RunID: input.RunID, ConversationID: input.ConversationID, PrincipalID: principalID})
	} else if priorStatus == agent.RunStatusRunning || priorStatus == agent.RunStatusPreparing {
		row, finishErr = q.FinishAgentRun(ctx, platformdb.FinishAgentRunParams{Status: agent.RunStatusCanceled, Error: input.Error, MetadataJson: input.MetadataJSON, ID: input.RunID, ConversationID: input.ConversationID, PrincipalID: principalID})
	} else {
		return false, fmt.Errorf("agent run is not cancellable from status %q", priorStatus)
	}
	if finishErr != nil {
		current, getErr := q.GetAgentRunInConversation(ctx, platformdb.GetAgentRunInConversationParams{RunID: input.RunID, ConversationID: input.ConversationID, PrincipalID: principalID})
		if getErr != nil || current.Status != agent.RunStatusCanceled {
			return false, finishErr
		}
		// A prior attempt may have committed the run/job but lost the event
		// publication. Re-record the keyed event on replay; the unique key
		// makes this a no-op when it already exists.
		transitioned = false
		row = current
	}
	if err := r.workflow.RecordWorkflow(ctx, tx, workflow); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	_ = row
	return transitioned, nil
}

func (r *Repository) VerifyRunLease(ctx context.Context, runID, jobID string, fence jobs.Fence) error {
	if r.events == nil {
		return nil
	}
	job, err := r.events.Get(ctx, jobID)
	if err != nil {
		return err
	}
	if job.Kind != "agent.run" || job.ResourceKind != "agent_run" || job.ResourceID != runID ||
		job.Status != jobs.StatusRunning || job.Fence() != fence || !leaseUnexpired(job.LeaseExpiresAt) {
		return fmt.Errorf("stale durable job claim")
	}
	return nil
}

func leaseUnexpired(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.After(time.Now())
		}
	}
	return false
}

func (r *Repository) FinishRun(ctx context.Context, input agent.RunFinish) (agent.Run, error) {
	metadata, err := normalizedJSONObject(input.MetadataJSON)
	if err != nil {
		return agent.Run{}, err
	}
	if !validRunStatus(input.Status) || input.Status == agent.RunStatusRunning {
		return agent.Run{}, fmt.Errorf("invalid final agent run status %q", input.Status)
	}
	principalID, err := agentPrincipalID(input.PrincipalID)
	if err != nil {
		return agent.Run{}, err
	}
	if input.JobID != "" {
		if !validAgentJobClaim(ctx, r.q, input.JobID, input.RunID, input.JobFence) {
			return agent.Run{}, fmt.Errorf("stale durable job claim")
		}
	}
	row, err := r.q.FinishAgentRun(ctx, platformdb.FinishAgentRunParams{
		Status:         input.Status,
		StopReason:     input.StopReason,
		InputTokens:    input.InputTokens,
		OutputTokens:   input.OutputTokens,
		TotalTokens:    input.TotalTokens,
		Error:          input.Error,
		MetadataJson:   metadata,
		ID:             input.RunID,
		ConversationID: input.ConversationID,
		PrincipalID:    principalID,
	})
	if err != nil {
		return agent.Run{}, err
	}
	return mapRun(row), nil
}

func (r *Repository) ListRuns(ctx context.Context, principalID, conversationID string) ([]agent.Run, error) {
	return r.ListRunsPage(ctx, principalID, conversationID, agent.Page{})
}

func (r *Repository) ListRunsPage(ctx context.Context, principalID, conversationID string, page agent.Page) ([]agent.Run, error) {
	principalID, err := agentPrincipalID(principalID)
	if err != nil {
		return nil, err
	}
	if _, err := r.GetConversation(ctx, principalID, conversationID); err != nil {
		return nil, err
	}
	rows, err := r.q.ListAgentRuns(ctx, platformdb.ListAgentRunsParams{
		ConversationID: conversationID,
		PrincipalID:    principalID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]agent.Run, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapRun(row))
	}
	return pageByID(out, page, func(row agent.Run) string { return row.ID }), nil
}

func (r *Repository) GetRun(ctx context.Context, principalID, conversationID, runID string) (agent.Run, error) {
	principalID, err := agentPrincipalID(principalID)
	if err != nil {
		return agent.Run{}, err
	}
	if strings.TrimSpace(conversationID) == "" {
		return agent.Run{}, fmt.Errorf("conversation id is required")
	}
	if strings.TrimSpace(runID) == "" {
		return agent.Run{}, fmt.Errorf("run id is required")
	}
	row, err := r.q.GetAgentRunInConversation(ctx, platformdb.GetAgentRunInConversationParams{
		RunID: runID, ConversationID: conversationID, PrincipalID: principalID,
	})
	if err != nil {
		return agent.Run{}, err
	}
	return mapRun(row), nil
}

func (r *Repository) GetRunByID(ctx context.Context, principalID, runID string) (agent.Run, error) {
	principalID, err := agentPrincipalID(principalID)
	if err != nil {
		return agent.Run{}, err
	}
	if strings.TrimSpace(runID) == "" {
		return agent.Run{}, fmt.Errorf("run id is required")
	}
	row, err := r.q.GetAgentRunForPrincipal(ctx, platformdb.GetAgentRunForPrincipalParams{
		RunID: runID, PrincipalID: principalID,
	})
	if err != nil {
		return agent.Run{}, err
	}
	return mapRun(row), nil
}

func (r *Repository) AppendEvent(ctx context.Context, input agent.EventInput) (agent.Event, error) {
	if r.events == nil {
		return agent.Event{}, fmt.Errorf("agent event repository is not configured")
	}
	payload, err := normalizedJSONObject(input.PayloadJSON)
	if err != nil {
		return agent.Event{}, err
	}
	principalID, err := agentPrincipalID(input.PrincipalID)
	if err != nil {
		return agent.Event{}, err
	}
	eventType := strings.TrimSpace(input.EventType)
	if eventType == "" {
		return agent.Event{}, fmt.Errorf("event type is required")
	}
	severity := strings.TrimSpace(input.Severity)
	if severity == "" {
		severity = "info"
	}
	if input.Sequence <= 0 {
		return agent.Event{}, fmt.Errorf("event sequence is required")
	}
	exists, err := r.agentRunExists(ctx, principalID, input.RunID)
	if err != nil {
		return agent.Event{}, err
	}
	if !exists {
		return agent.Event{}, sql.ErrNoRows
	}
	data, err := json.Marshal(map[string]any{"sequence": input.Sequence, "severity": severity, "payload": json.RawMessage(payload)})
	if err != nil {
		return agent.Event{}, err
	}
	stored, err := r.events.AppendEvent(ctx, "agent_run", input.RunID, eventType, data)
	if err != nil {
		return agent.Event{}, err
	}
	return agent.Event{ID: fmt.Sprintf("%020d", stored.ID), RunID: input.RunID, Seq: input.Sequence, EventType: eventType, Severity: severity, PayloadJSON: payload, CreatedAt: stored.CreatedAt}, nil
}

func (r *Repository) ListEvents(ctx context.Context, principalID, runID string) ([]agent.Event, error) {
	return r.ListEventsPage(ctx, principalID, runID, agent.Page{})
}

func (r *Repository) ListEventsPage(ctx context.Context, principalID, runID string, page agent.Page) ([]agent.Event, error) {
	if r.events == nil {
		return nil, fmt.Errorf("agent event repository is not configured")
	}
	principalID, err := agentPrincipalID(principalID)
	if err != nil {
		return nil, err
	}
	if exists, err := r.agentRunExists(ctx, principalID, runID); err != nil {
		return nil, err
	} else if !exists {
		return nil, sql.ErrNoRows
	}
	limit := page.Limit
	if limit <= 0 {
		limit = 10000
	}
	if limit > 10000 {
		limit = 10000
	}
	after := int64(0)
	if strings.TrimSpace(page.After) != "" {
		parsed, parseErr := strconv.ParseInt(strings.TrimSpace(page.After), 10, 64)
		if parseErr != nil || parsed < 1 {
			return nil, fmt.Errorf("invalid event cursor")
		}
		after = parsed
	}
	out := []agent.Event{}
	for len(out) < limit {
		batchSize := min(200, limit-len(out))
		rows, err := r.events.ListEvents(ctx, "agent_run", runID, after, batchSize)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			var data struct {
				Sequence int64           `json:"sequence"`
				Severity string          `json:"severity"`
				Payload  json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(row.Data, &data); err != nil {
				return nil, err
			}
			out = append(out, agent.Event{ID: fmt.Sprintf("%020d", row.ID), RunID: runID, Seq: data.Sequence, EventType: row.EventType, Severity: data.Severity, PayloadJSON: string(data.Payload), CreatedAt: row.CreatedAt})
			after = row.ID
		}
		if len(rows) < batchSize {
			break
		}
	}
	return out, nil
}

func (r *Repository) agentRunExists(ctx context.Context, principalID, runID string) (bool, error) {
	exists, err := r.q.AgentRunExistsForPrincipal(ctx, platformdb.AgentRunExistsForPrincipalParams{
		RunID: runID, PrincipalID: principalID,
	})
	return exists != 0, err
}

func mapConversation(row platformdb.AgentConversation) agent.Conversation {
	out := agent.Conversation{
		ID:             row.ID,
		PrincipalID:    row.PrincipalID,
		Title:          row.Title,
		Status:         row.Status,
		MetadataJSON:   row.MetadataJson,
		TranscriptJSON: row.TranscriptJson,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
	if row.ArchivedAt.Valid {
		out.ArchivedAt = row.ArchivedAt.String
	}
	return out
}

func mapMessage(row platformdb.AgentMessage) agent.Message {
	out := agent.Message{
		ID:             row.ID,
		ConversationID: row.ConversationID,
		Seq:            row.Seq,
		Role:           row.Role,
		ContentText:    row.ContentText,
		ContentJSON:    row.ContentJson,
		ToolCallID:     row.ToolCallID,
		ToolName:       row.ToolName,
		IsError:        row.IsError,
		CreatedAt:      row.CreatedAt,
	}
	if row.RunID.Valid {
		out.RunID = row.RunID.String
	}
	return out
}

func mapRun(row platformdb.AgentRun) agent.Run {
	out := agent.Run{
		ID:             row.ID,
		ConversationID: row.ConversationID,
		Status:         row.Status,
		Model:          row.Model,
		StopReason:     row.StopReason,
		InputTokens:    row.InputTokens,
		OutputTokens:   row.OutputTokens,
		TotalTokens:    row.TotalTokens,
		Error:          row.Error,
		StartedAt:      row.StartedAt,
		MetadataJSON:   row.MetadataJson,
		CreatedAt:      row.StartedAt,
	}
	if row.FinishedAt.Valid {
		out.FinishedAt = row.FinishedAt.String
	}
	return out
}

func agentPrincipalID(principalID string) (string, error) {
	principalID = strings.TrimSpace(principalID)
	if principalID == "" {
		return "", fmt.Errorf("principal id is required")
	}
	return principalID, nil
}

func normalizedJSONObject(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}", nil
	}
	if !json.Valid([]byte(raw)) {
		return "", fmt.Errorf("invalid JSON object")
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return "", err
	}
	if _, ok := value.(map[string]any); !ok {
		return "", fmt.Errorf("JSON value must be an object")
	}
	return raw, nil
}

func normalizedJSONArray(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "[]", nil
	}
	if !json.Valid([]byte(raw)) {
		return "", fmt.Errorf("invalid JSON array")
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return "", err
	}
	if _, ok := value.([]any); !ok {
		return "", fmt.Errorf("JSON value must be an array")
	}
	return raw, nil
}

func validMessageRole(role string) bool {
	switch role {
	case agent.MessageRoleUser, agent.MessageRoleAssistant, agent.MessageRoleTool, agent.MessageRoleSummary:
		return true
	default:
		return false
	}
}

func validRunStatus(status string) bool {
	switch status {
	case agent.RunStatusRunning, agent.RunStatusCompleted, agent.RunStatusFailed, agent.RunStatusCanceled:
		return true
	default:
		return false
	}
}

func pageByID[T any](rows []T, page agent.Page, id func(T) string) []T {
	limit := page.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	start := 0
	after := strings.TrimSpace(page.After)
	if after != "" {
		start = len(rows)
		for i, row := range rows {
			if id(row) == after {
				start = i + 1
				break
			}
		}
	}
	if start >= len(rows) {
		return []T{}
	}
	end := start + limit
	if end > len(rows) {
		end = len(rows)
	}
	return append([]T(nil), rows[start:end]...)
}

func newID(prefix string) string {
	return prefix + "_" + newSecret()[:24]
}

func newSecret() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		sum := sha256.Sum256([]byte(time.Now().Format(time.RFC3339Nano)))
		return hex.EncodeToString(sum[:])
	}
	return hex.EncodeToString(b[:])
}
