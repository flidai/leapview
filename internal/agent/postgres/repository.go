// Package postgres implements the native PostgreSQL persistence boundary for
// the agent capability. Domain rows, agent events, workflow consequences and
// audit intents share one caller-owned pgx transaction where required.
package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	agentdb "github.com/flidai/leapview/internal/agent/postgres/internal/db"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// DBTX is the native pgx surface accepted by this capability.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Tx is intentionally an alias of the canonical jobs PostgreSQL transaction
// port. App adapters can therefore pass one native transaction to both
// capabilities without exposing a connection or transaction implementation.
type Tx = jobspostgres.Tx

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// WorkflowRecorder records canonical jobs events and optional follow-up work
// on the transaction supplied by the agent repository.
type WorkflowRecorder interface {
	RecordWorkflow(context.Context, jobspostgres.Tx, jobs.WorkflowIntent) error
}

// JobAuthority is the narrow caller-owned port needed for lease fencing and
// queued cancellation. It is implemented by platform/jobs/postgres.
type JobAuthority interface {
	Get(context.Context, string) (jobs.Job, error)
	GetTx(context.Context, jobspostgres.Tx, string) (jobs.Job, error)
	CancelTx(context.Context, jobspostgres.Tx, string) error
}

// AuditIntentRecorder is intentionally narrower than Access' database/sql
// compatibility port. The app composition layer adapts Access PostgreSQL's
// stateless audit appender to this transaction shape.
type AuditIntentRecorder interface {
	RecordAuditIntent(context.Context, Tx, access.AuditIntent) error
}

// DomainEventAppender is the strict capability-neutral port for canonical
// platform events. Implementations append to the caller-owned transaction and
// never commit it. The returned projection must preserve every immutable input
// field and the source EventID; callers use that projection to link audit rows
// without accepting a silently substituted identity.
type DomainEventAppender interface {
	AppendDomainEvent(context.Context, Tx, DomainEventInput) (DomainEvent, error)
}

type DomainEventInput struct {
	EventID, ScopeID, AggregateType, AggregateID, EventType string
	SchemaVersion                                           int64
	CorrelationID                                           string
	Payload                                                 []byte
}

type DomainEvent struct {
	EventID          string
	ScopeID          string
	AggregateType    string
	AggregateID      string
	AggregateVersion int64
	EventType        string
	SchemaVersion    int64
	CorrelationID    string
	Payload          []byte
}

type Options struct {
	Workflow WorkflowRecorder
	Jobs     JobAuthority
	Audit    AuditIntentRecorder
	Domain   DomainEventAppender
}

type Repository struct {
	db       DBTX
	workflow WorkflowRecorder
	jobs     JobAuthority
	audit    AuditIntentRecorder
	domain   DomainEventAppender
}

var _ agent.Repository = (*Repository)(nil)
var _ agent.RunWorkflowUnitOfWork = (*Repository)(nil)
var _ agent.RunWorkflowAuditUnitOfWork = (*Repository)(nil)
var _ agent.RunTerminalWorkflow = (*Repository)(nil)
var _ agent.RunCompletionWorkflow = (*Repository)(nil)
var _ agent.RunCancellationWorkflow = (*Repository)(nil)
var _ agent.RunLeaseVerifier = (*Repository)(nil)

func NewRepository(db DBTX) *Repository { return &Repository{db: db} }
func New(db DBTX) *Repository           { return NewRepository(db) }

func NewWithOptions(db DBTX, options Options) (*Repository, error) {
	if db == nil {
		return nil, errors.New("agent PostgreSQL database is required")
	}
	return &Repository{db: db, workflow: options.Workflow, jobs: options.Jobs, audit: options.Audit, domain: options.Domain}, nil
}

func NewProduction(db DBTX, options Options) (*Repository, error) {
	if options.Workflow == nil || options.Jobs == nil || options.Audit == nil || options.Domain == nil {
		return nil, errors.New("agent PostgreSQL workflow, jobs, audit, and domain-event authorities are required")
	}
	r, err := NewWithOptions(db, options)
	if err != nil {
		return nil, err
	}
	if !r.TransactionCapable() {
		return nil, errors.New("agent PostgreSQL database must support caller-owned transactions")
	}
	return r, nil
}

// DB exposes the configured native handle to app adapters without opening a
// second connection.
func (r *Repository) DB() DBTX {
	if r == nil {
		return nil
	}
	return r.db
}

// Capability markers are consumed by module composition to reject an
// unconfigured native repository before production handlers are exposed.
func (r *Repository) Configured() bool { return r != nil && r.db != nil }
func (r *Repository) TransactionCapable() bool {
	if r == nil || r.db == nil {
		return false
	}
	if _, ok := r.db.(Tx); ok {
		return true
	}
	_, ok := r.db.(beginner)
	return ok
}
func (r *Repository) WorkflowCapable() bool    { return r != nil && r.workflow != nil }
func (r *Repository) JobsCapable() bool        { return r != nil && r.jobs != nil }
func (r *Repository) AuditCapable() bool       { return r != nil && r.audit != nil }
func (r *Repository) DomainEventCapable() bool { return r != nil && r.domain != nil }

func (r *Repository) RunWorkflowAvailable() bool { return r != nil && r.workflow != nil }
func (r *Repository) NativePersistence()         {}

func (r *Repository) ConfigureRunWorkflow(recorder interface{}) {
	if r == nil || recorder == nil {
		return
	}
	if native, ok := recorder.(WorkflowRecorder); ok {
		r.workflow = native
	}
}

func (r *Repository) ConfigureAuditIntentRecorder(recorder interface{}) {
	if r == nil || recorder == nil {
		return
	}
	if native, ok := recorder.(AuditIntentRecorder); ok {
		r.audit = native
	}
}

// WithTx returns a repository bound to a caller-owned transaction. The
// returned repository never commits or rolls back that transaction.
func (r *Repository) WithTx(tx Tx) *Repository {
	if r == nil || tx == nil {
		return nil
	}
	return &Repository{db: tx, workflow: r.workflow, jobs: r.jobs, audit: r.audit, domain: r.domain}
}

func (r *Repository) withTx(ctx context.Context, fn func(Tx, *agentdb.Queries) error) error {
	if r == nil || r.db == nil {
		return errors.New("agent PostgreSQL database is required")
	}
	if tx, ok := r.db.(Tx); ok {
		return mapDBError(fn(tx, agentdb.New(tx)))
	}
	if b, ok := r.db.(beginner); ok {
		tx, err := b.Begin(ctx)
		if err != nil {
			return err
		}
		if err := fn(tx, agentdb.New(tx)); err != nil {
			_ = tx.Rollback(ctx)
			return mapDBError(err)
		}
		return tx.Commit(ctx)
	}
	return errors.New("agent PostgreSQL database must support caller-owned transactions")
}

func (r *Repository) recordAudit(ctx context.Context, tx Tx, intent *access.AuditIntent, resourceID, aggregateID string, domain *DomainEvent) error {
	if intent == nil {
		return nil
	}
	if r.audit == nil || tx == nil {
		return errors.New("agent audit intent recorder is required")
	}
	copy := *intent
	// Access' PostgreSQL audit table stores audit_id (and the optional request
	// and correlation identities) as UUIDs. Generate only an omitted source
	// event identity; a non-canonical caller value is rejected so retries never
	// silently lose their correlation key.
	var identityErr error
	copy.EventID, identityErr = requiredOrUUIDv7(copy.EventID, "audit event id")
	if identityErr != nil {
		return identityErr
	}
	if copy.RequestID != "" && !isCanonicalUUID(copy.RequestID) {
		return fmt.Errorf("audit request id must be a UUID")
	}
	if copy.CorrelationID != "" && !isCanonicalUUID(copy.CorrelationID) {
		return fmt.Errorf("audit correlation id must be a UUID")
	}
	if domain != nil {
		if !isCanonicalUUID(domain.EventID) || domain.AggregateVersion <= 0 {
			return errors.New("agent domain event appender returned invalid identity")
		}
		copy.DomainEventID = domain.EventID
		copy.AggregateSequence = domain.AggregateVersion
	}
	resourceID, aggregateID = strings.TrimSpace(resourceID), strings.TrimSpace(aggregateID)
	op := strings.ToLower(copy.Operation)
	isRun := strings.Contains(op, "agentrun")
	generated := strings.TrimSpace(copy.ResourceID) == ""
	if isRun || generated {
		copy.ResourceID = resourceID
	}
	if isRun {
		copy.ResourceKind = "agent_run"
	}
	if (isRun || generated) && strings.TrimSpace(copy.MetadataJSON) != "" {
		replacements := map[string]any{"resourceId": resourceID}
		if isRun {
			replacements["resourceKind"] = "agent_run"
		}
		metadata, err := access.RewriteGeneratedAuditEnvelopePayload(copy.MetadataJSON, replacements)
		if err != nil {
			return fmt.Errorf("agent audit metadata: %w", err)
		}
		copy.MetadataJSON = metadata
	}
	if aggregateID == "" {
		aggregateID = resourceID
	}
	if isRun {
		copy.AggregateKey = "agent_run:" + aggregateID
	} else {
		copy.AggregateKey = "agent_conversation:" + aggregateID
	}
	// The canonical domain event sequence is authoritative whenever a domain
	// event was appended in this transaction.  The legacy fallback below is
	// retained only for callers that intentionally omit the domain appender
	// (for example the SQLite compatibility path).
	if domain == nil {
		if isRun {
			if strings.Contains(op, "create") {
				copy.AggregateSequence = 1
			} else if strings.Contains(op, "cancel") {
				copy.AggregateSequence = 2
			}
		} else {
			switch {
			case strings.Contains(op, "create"):
				copy.AggregateSequence = 1
			case strings.Contains(op, "update"):
				copy.AggregateSequence = 2
			case strings.Contains(op, "archive"):
				copy.AggregateSequence = 3
			default:
				copy.AggregateSequence = 0
			}
		}
	}
	return r.audit.RecordAuditIntent(ctx, tx, copy)
}

func (r *Repository) recordDomain(ctx context.Context, tx Tx, scope, aggregateType, aggregateID, eventType string, payload []byte) (*DomainEvent, error) {
	if r.domain == nil {
		return nil, nil
	}
	if tx == nil {
		return nil, errors.New("agent domain event requires a transaction")
	}
	if len(payload) == 0 {
		payload = []byte(`{}`)
	}
	if !json.Valid(payload) {
		return nil, errors.New("invalid agent domain event payload")
	}
	eventID, err := newUUIDv7()
	if err != nil {
		return nil, err
	}
	correlationID, err := newUUIDv7()
	if err != nil {
		return nil, err
	}
	input := DomainEventInput{EventID: eventID, ScopeID: scope, AggregateType: aggregateType, AggregateID: aggregateID, EventType: eventType, SchemaVersion: 1, CorrelationID: correlationID, Payload: append([]byte(nil), payload...)}
	event, err := r.domain.AppendDomainEvent(ctx, tx, input)
	if err != nil {
		return nil, err
	}
	if !isUUIDv7(event.EventID) || event.EventID != input.EventID || event.ScopeID != input.ScopeID || event.AggregateType != input.AggregateType || event.AggregateID != input.AggregateID || event.EventType != input.EventType || event.SchemaVersion != input.SchemaVersion || !isUUIDv7(event.CorrelationID) || event.CorrelationID != input.CorrelationID || !jsonEquivalent(string(event.Payload), string(input.Payload)) || event.AggregateVersion <= 0 {
		return nil, errors.New("agent domain event appender returned incomplete event")
	}
	return &event, nil
}

func (r *Repository) CreateConversation(ctx context.Context, input agent.ConversationInput) (agent.Conversation, error) {
	metadata, err := normalizedJSONObject(input.MetadataJSON)
	if err != nil {
		return agent.Conversation{}, err
	}
	principal, err := principalID(input.PrincipalID)
	if err != nil {
		return agent.Conversation{}, err
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = agent.ConversationDefaultTitle
	}
	id := newID("agentconv")
	intent, hasIntent := agent.AuditIntentFromContext(ctx)
	var out agent.Conversation
	err = r.withTx(ctx, func(tx Tx, q *agentdb.Queries) error {
		row, err := q.CreateAgentConversation(ctx, agentdb.CreateAgentConversationParams{ID: id, PrincipalID: principal, Title: title, Status: agent.ConversationStatusActive, MetadataJson: []byte(metadata), TranscriptJson: []byte("[]")})
		if err != nil {
			return err
		}
		out = mapConversation(row)
		domain, err := r.recordDomain(ctx, tx, principal, "agent_conversation", id, "agent.conversation.created", []byte(`{"status":"active"}`))
		if err != nil {
			return err
		}
		if hasIntent {
			return r.recordAudit(ctx, tx, &intent, id, id, domain)
		}
		return nil
	})
	return out, err
}

func (r *Repository) ListConversations(ctx context.Context, principal string) ([]agent.Conversation, error) {
	return r.ListConversationsPage(ctx, principal, agent.Page{})
}
func (r *Repository) ListConversationsPage(ctx context.Context, principal string, page agent.Page) ([]agent.Conversation, error) {
	principal, err := principalID(principal)
	if err != nil {
		return nil, err
	}
	rows, err := agentdb.New(r.db).ListAgentConversations(ctx, principal)
	if err != nil {
		return nil, mapDBError(err)
	}
	out := make([]agent.Conversation, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapConversation(row))
	}
	return pageByID(out, page, func(v agent.Conversation) string { return v.ID }), nil
}

func (r *Repository) GetConversation(ctx context.Context, principal, id string) (agent.Conversation, error) {
	principal, err := principalID(principal)
	if err != nil {
		return agent.Conversation{}, mapDBError(err)
	}
	if strings.TrimSpace(id) == "" {
		return agent.Conversation{}, errors.New("conversation id is required")
	}
	row, err := agentdb.New(r.db).GetAgentConversation(ctx, agentdb.GetAgentConversationParams{ID: id, PrincipalID: principal})
	if err != nil {
		return agent.Conversation{}, mapDBError(err)
	}
	return mapConversation(row), nil
}

func (r *Repository) UpdateConversation(ctx context.Context, input agent.ConversationUpdate) (agent.Conversation, error) {
	principal, err := principalID(input.PrincipalID)
	if err != nil {
		return agent.Conversation{}, mapDBError(err)
	}
	title := strings.TrimSpace(input.Title)
	if strings.TrimSpace(input.ConversationID) == "" || title == "" {
		return agent.Conversation{}, errors.New("conversation id and title are required")
	}
	intent, hasIntent := agent.AuditIntentFromContext(ctx)
	var out agent.Conversation
	err = r.withTx(ctx, func(tx Tx, q *agentdb.Queries) error {
		row, err := q.UpdateAgentConversationTitle(ctx, agentdb.UpdateAgentConversationTitleParams{Title: title, ConversationID: input.ConversationID, PrincipalID: principal})
		if err != nil {
			return err
		}
		out = mapConversation(row)
		domain, err := r.recordDomain(ctx, tx, principal, "agent_conversation", input.ConversationID, "agent.conversation.updated", []byte(`{"status":"active"}`))
		if err != nil {
			return err
		}
		if hasIntent {
			return r.recordAudit(ctx, tx, &intent, input.ConversationID, input.ConversationID, domain)
		}
		return nil
	})
	return out, err
}

func (r *Repository) UpdateConversationAtomic(ctx context.Context, input agent.ConversationUpdate, check func(agent.Conversation) error) (agent.Conversation, error) {
	principal, err := principalID(input.PrincipalID)
	if err != nil {
		return agent.Conversation{}, mapDBError(err)
	}
	if strings.TrimSpace(input.ConversationID) == "" || strings.TrimSpace(input.Title) == "" {
		return agent.Conversation{}, errors.New("conversation id and title are required")
	}
	var out agent.Conversation
	err = r.withTx(ctx, func(tx Tx, q *agentdb.Queries) error {
		if err := q.AcquireAgentConversationMutationLock(ctx, agentdb.AcquireAgentConversationMutationLockParams{ConversationID: input.ConversationID, PrincipalID: principal}); err != nil {
			return err
		}
		row, err := q.GetAgentConversation(ctx, agentdb.GetAgentConversationParams{ID: input.ConversationID, PrincipalID: principal})
		if err != nil {
			return err
		}
		current := mapConversation(row)
		if check != nil {
			if err := check(current); err != nil {
				return err
			}
		}
		updated, err := q.UpdateAgentConversationTitle(ctx, agentdb.UpdateAgentConversationTitleParams{Title: strings.TrimSpace(input.Title), ConversationID: input.ConversationID, PrincipalID: principal})
		if err != nil {
			return err
		}
		out = mapConversation(updated)
		domain, err := r.recordDomain(ctx, tx, principal, "agent_conversation", input.ConversationID, "agent.conversation.updated", []byte(`{"status":"active"}`))
		if err != nil {
			return err
		}
		if intent, ok := agent.AuditIntentFromContext(ctx); ok {
			return r.recordAudit(ctx, tx, &intent, input.ConversationID, input.ConversationID, domain)
		}
		return nil
	})
	return out, err
}

func (r *Repository) ArchiveConversation(ctx context.Context, principal, id string) (agent.Conversation, error) {
	principal, err := principalID(principal)
	if err != nil {
		return agent.Conversation{}, err
	}
	if strings.TrimSpace(id) == "" {
		return agent.Conversation{}, errors.New("conversation id is required")
	}
	intent, hasIntent := agent.AuditIntentFromContext(ctx)
	var out agent.Conversation
	err = r.withTx(ctx, func(tx Tx, q *agentdb.Queries) error {
		row, err := q.ArchiveAgentConversation(ctx, agentdb.ArchiveAgentConversationParams{ID: id, PrincipalID: principal})
		if err != nil {
			return err
		}
		out = mapConversation(row)
		domain, err := r.recordDomain(ctx, tx, principal, "agent_conversation", id, "agent.conversation.archived", []byte(`{"status":"archived"}`))
		if err != nil {
			return err
		}
		if hasIntent {
			return r.recordAudit(ctx, tx, &intent, id, id, domain)
		}
		return nil
	})
	return out, err
}

func (r *Repository) UpdateDefaultConversationTitle(ctx context.Context, principal, id, title string) (agent.Conversation, error) {
	principal, err := principalID(principal)
	if err != nil {
		return agent.Conversation{}, err
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(title) == "" {
		return agent.Conversation{}, errors.New("conversation id and title are required")
	}
	var out agent.Conversation
	err = r.withTx(ctx, func(tx Tx, q *agentdb.Queries) error {
		row, err := q.UpdateDefaultAgentConversationTitle(ctx, agentdb.UpdateDefaultAgentConversationTitleParams{Title: strings.TrimSpace(title), ID: id, PrincipalID: principal})
		if err != nil {
			return err
		}
		domain, err := r.recordDomain(ctx, tx, principal, "agent_conversation", id, "agent.conversation.updated", []byte(`{"status":"active"}`))
		if err != nil {
			return err
		}
		if intent, ok := agent.AuditIntentFromContext(ctx); ok {
			if err := r.recordAudit(ctx, tx, &intent, id, id, domain); err != nil {
				return err
			}
		}
		out = mapConversation(row)
		return nil
	})
	return out, err
}

func (r *Repository) UpdateConversationTranscript(ctx context.Context, principal, id, transcript string) (agent.Conversation, error) {
	normalized, err := normalizedJSONArray(transcript)
	if err != nil {
		return agent.Conversation{}, err
	}
	principal, err = principalID(principal)
	if err != nil {
		return agent.Conversation{}, err
	}
	var out agent.Conversation
	err = r.withTx(ctx, func(tx Tx, q *agentdb.Queries) error {
		row, err := q.UpdateAgentConversationTranscript(ctx, agentdb.UpdateAgentConversationTranscriptParams{TranscriptJson: []byte(normalized), ID: id, PrincipalID: principal})
		if err != nil {
			return err
		}
		domain, err := r.recordDomain(ctx, tx, principal, "agent_conversation", id, "agent.conversation.updated", []byte(`{"status":"active"}`))
		if err != nil {
			return err
		}
		if intent, ok := agent.AuditIntentFromContext(ctx); ok {
			if err := r.recordAudit(ctx, tx, &intent, id, id, domain); err != nil {
				return err
			}
		}
		out = mapConversation(row)
		return nil
	})
	return out, err
}

func (r *Repository) AppendMessage(ctx context.Context, input agent.MessageInput) (agent.Message, error) {
	content, err := normalizedJSONObject(input.ContentJSON)
	if err != nil {
		return agent.Message{}, err
	}
	if !validMessageRole(input.Role) {
		return agent.Message{}, fmt.Errorf("invalid agent message role %q", input.Role)
	}
	principal, err := principalID(input.PrincipalID)
	if err != nil {
		return agent.Message{}, err
	}
	var out agent.Message
	err = r.withTx(ctx, func(tx Tx, q *agentdb.Queries) error {
		if err := q.AcquireAgentConversationMutationLock(ctx, agentdb.AcquireAgentConversationMutationLockParams{ConversationID: input.ConversationID, PrincipalID: principal}); err != nil {
			return err
		}
		row, err := q.AppendAgentMessage(ctx, agentdb.AppendAgentMessageParams{ID: newID("agentmsg"), RunID: input.RunID, Role: input.Role, ContentText: input.ContentText, ContentJson: []byte(content), ToolCallID: input.ToolCallID, ToolName: input.ToolName, IsError: input.IsError, ConversationID: input.ConversationID, PrincipalID: principal})
		if err != nil {
			return err
		}
		out = mapMessage(row)
		return nil
	})
	return out, err
}

func (r *Repository) ListMessages(ctx context.Context, principal, conversation string) ([]agent.Message, error) {
	return r.ListMessagesPage(ctx, principal, conversation, agent.Page{})
}
func (r *Repository) ListMessagesPage(ctx context.Context, principal, conversation string, page agent.Page) ([]agent.Message, error) {
	principal, err := principalID(principal)
	if err != nil {
		return nil, err
	}
	rows, err := agentdb.New(r.db).ListAgentMessages(ctx, agentdb.ListAgentMessagesParams{ConversationID: conversation, PrincipalID: principal})
	if err != nil {
		return nil, mapDBError(err)
	}
	out := make([]agent.Message, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapMessageList(row))
	}
	return pageByID(out, page, func(v agent.Message) string { return v.ID }), nil
}

func (r *Repository) CreateRun(ctx context.Context, input agent.RunInput) (agent.Run, error) {
	metadata, err := normalizedJSONObject(input.MetadataJSON)
	if err != nil {
		return agent.Run{}, err
	}
	principal, err := principalID(input.PrincipalID)
	if err != nil {
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
	intent, hasIntent := agent.AuditIntentFromContext(ctx)
	var out agent.Run
	err = r.withTx(ctx, func(tx Tx, q *agentdb.Queries) error {
		row, err := q.CreateAgentRun(ctx, agentdb.CreateAgentRunParams{ID: runID, Status: status, Model: input.Model, MetadataJson: []byte(metadata), ConversationID: input.ConversationID, PrincipalID: principal})
		if err != nil {
			return err
		}
		out = mapRun(row)
		domain, err := r.recordDomain(ctx, tx, principal, "agent_run", runID, "agent.run.created", []byte(fmt.Sprintf(`{"status":%q}`, status)))
		if err != nil {
			return err
		}
		if hasIntent {
			return r.recordAudit(ctx, tx, &intent, runID, runID, domain)
		}
		return nil
	})
	return out, err
}

func (r *Repository) ActivateRunWorkflow(ctx context.Context, principal, conversation, runID string, intent jobs.WorkflowIntent) (agent.Run, error) {
	return r.activateRunWorkflow(ctx, principal, conversation, runID, intent, nil)
}
func (r *Repository) ActivateRunWorkflowWithAudit(ctx context.Context, principal, conversation, runID string, workflow jobs.WorkflowIntent, audit *access.AuditIntent) (agent.Run, error) {
	return r.activateRunWorkflow(ctx, principal, conversation, runID, workflow, audit)
}
func (r *Repository) activateRunWorkflow(ctx context.Context, principal, conversation, runID string, workflow jobs.WorkflowIntent, explicit *access.AuditIntent) (agent.Run, error) {
	if r.workflow == nil {
		return agent.Run{}, errors.New("agent workflow recorder is required")
	}
	principal, err := principalID(principal)
	if err != nil {
		return agent.Run{}, err
	}
	var out agent.Run
	err = r.withTx(ctx, func(tx Tx, q *agentdb.Queries) error {
		changed, err := q.ActivateAgentRun(ctx, agentdb.ActivateAgentRunParams{RunID: runID, ConversationID: conversation, PrincipalID: principal})
		if err != nil {
			return err
		}
		row, getErr := q.GetAgentRunInConversation(ctx, agentdb.GetAgentRunInConversationParams{RunID: runID, ConversationID: conversation, PrincipalID: principal})
		if getErr != nil {
			return getErr
		}
		if changed != 1 && row.Status != agent.RunStatusRunning {
			return errors.New("agent run changed while queueing")
		}
		if err := r.recordWorkflowEvent(ctx, tx, runID, workflow); err != nil {
			return err
		}
		var domain *DomainEvent
		if changed == 1 {
			domain, err = r.recordDomain(ctx, tx, principal, "agent_run", runID, "agent.run.activated", []byte(`{"status":"running"}`))
			if err != nil {
				return err
			}
		}
		if err := r.workflow.RecordWorkflow(ctx, tx, workflow); err != nil {
			return err
		}
		intent := explicit
		if intent == nil {
			if fromContext, ok := agent.AuditIntentFromContext(ctx); ok {
				intent = &fromContext
			}
		}
		if err := r.recordAudit(ctx, tx, intent, runID, runID, domain); err != nil {
			return err
		}
		out = mapRun(row)
		return nil
	})
	return out, err
}

func (r *Repository) FinishRunWorkflow(ctx context.Context, input agent.RunFinish, workflow jobs.WorkflowIntent) (agent.Run, bool, error) {
	if r.workflow == nil {
		return agent.Run{}, false, errors.New("agent workflow recorder is required")
	}
	metadata, err := normalizedJSONObject(input.MetadataJSON)
	if err != nil {
		return agent.Run{}, false, err
	}
	principal, err := principalID(input.PrincipalID)
	if err != nil {
		return agent.Run{}, false, err
	}
	var out agent.Run
	transitioned := false
	err = r.withTx(ctx, func(tx Tx, q *agentdb.Queries) error {
		if input.JobID != "" {
			if err := r.verifyJobTx(ctx, tx, input.JobID, input.RunID, input.JobFence); err != nil {
				return err
			}
		}
		prior, err := q.GetAgentRunStatus(ctx, agentdb.GetAgentRunStatusParams{RunID: input.RunID, ConversationID: input.ConversationID})
		if err != nil {
			return err
		}
		if prior != agent.RunStatusRunning && prior != agent.RunStatusPreparing {
			row, getErr := q.GetAgentRunInConversation(ctx, agentdb.GetAgentRunInConversationParams{RunID: input.RunID, ConversationID: input.ConversationID, PrincipalID: principal})
			if getErr != nil {
				return getErr
			}
			if row.Status != agent.RunStatusCompleted && row.Status != agent.RunStatusFailed && row.Status != agent.RunStatusCanceled {
				return fmt.Errorf("agent run is not terminalizable from status %q", prior)
			}
			if err := r.recordWorkflowEvent(ctx, tx, input.RunID, workflow); err != nil {
				return err
			}
			out, transitioned = mapRun(row), false
			return nil
		}
		row, err := q.FinishAgentRun(ctx, agentdb.FinishAgentRunParams{Status: input.Status, StopReason: input.StopReason, InputTokens: input.InputTokens, OutputTokens: input.OutputTokens, TotalTokens: input.TotalTokens, Error: input.Error, MetadataJson: []byte(metadata), ID: input.RunID, ConversationID: input.ConversationID, PrincipalID: principal})
		if err != nil {
			return err
		}
		if err := r.recordWorkflowEvent(ctx, tx, input.RunID, workflow); err != nil {
			return err
		}
		domain, err := r.recordDomain(ctx, tx, principal, "agent_run", input.RunID, "agent.run."+input.Status, []byte(fmt.Sprintf(`{"status":%q}`, input.Status)))
		if err != nil {
			return err
		}
		if err := r.workflow.RecordWorkflow(ctx, tx, workflow); err != nil {
			return err
		}
		if intent, ok := agent.AuditIntentFromContext(ctx); ok {
			if err := r.recordAudit(ctx, tx, &intent, input.RunID, input.RunID, domain); err != nil {
				return err
			}
		}
		out, transitioned = mapRun(row), true
		return nil
	})
	return out, transitioned, err
}

func (r *Repository) CompleteRunWorkflow(ctx context.Context, input agent.RunFinish, messages []agent.MessageInput, transcript string, workflow jobs.WorkflowIntent) ([]agent.Message, bool, error) {
	if r.workflow == nil {
		return nil, false, errors.New("agent workflow recorder is required")
	}
	metadata, err := normalizedJSONObject(input.MetadataJSON)
	if err != nil {
		return nil, false, err
	}
	transcript, err = normalizedJSONArray(transcript)
	if err != nil {
		return nil, false, err
	}
	principal, err := principalID(input.PrincipalID)
	if err != nil {
		return nil, false, err
	}
	var out []agent.Message
	transitioned := false
	err = r.withTx(ctx, func(tx Tx, q *agentdb.Queries) error {
		if input.JobID != "" {
			if err := r.verifyJobTx(ctx, tx, input.JobID, input.RunID, input.JobFence); err != nil {
				return err
			}
		}
		prior, err := q.GetAgentRunStatus(ctx, agentdb.GetAgentRunStatusParams{RunID: input.RunID, ConversationID: input.ConversationID})
		if err != nil {
			return err
		}
		if prior != agent.RunStatusRunning && prior != agent.RunStatusPreparing {
			if err := r.recordWorkflowEvent(ctx, tx, input.RunID, workflow); err != nil {
				return err
			}
			return nil
		}
		if err := q.AcquireAgentConversationMutationLock(ctx, agentdb.AcquireAgentConversationMutationLockParams{ConversationID: input.ConversationID, PrincipalID: principal}); err != nil {
			return err
		}
		out = make([]agent.Message, 0, len(messages))
		for _, message := range messages {
			if message.PrincipalID != input.PrincipalID || message.ConversationID != input.ConversationID || message.RunID != input.RunID {
				return errors.New("message binding mismatch")
			}
			content, err := normalizedJSONObject(message.ContentJSON)
			if err != nil {
				return err
			}
			row, err := q.AppendAgentMessage(ctx, agentdb.AppendAgentMessageParams{ID: newID("agentmsg"), RunID: message.RunID, Role: message.Role, ContentText: message.ContentText, ContentJson: []byte(content), ToolCallID: message.ToolCallID, ToolName: message.ToolName, IsError: message.IsError, ConversationID: message.ConversationID, PrincipalID: principal})
			if err != nil {
				return err
			}
			out = append(out, mapMessage(row))
		}
		if _, err := q.UpdateAgentConversationTranscript(ctx, agentdb.UpdateAgentConversationTranscriptParams{TranscriptJson: []byte(transcript), ID: input.ConversationID, PrincipalID: principal}); err != nil {
			return err
		}
		if _, err := q.FinishAgentRun(ctx, agentdb.FinishAgentRunParams{Status: input.Status, StopReason: input.StopReason, InputTokens: input.InputTokens, OutputTokens: input.OutputTokens, TotalTokens: input.TotalTokens, Error: input.Error, MetadataJson: []byte(metadata), ID: input.RunID, ConversationID: input.ConversationID, PrincipalID: principal}); err != nil {
			return err
		}
		if err := r.recordWorkflowEvent(ctx, tx, input.RunID, workflow); err != nil {
			return err
		}
		domain, err := r.recordDomain(ctx, tx, principal, "agent_run", input.RunID, "agent.run."+input.Status, []byte(fmt.Sprintf(`{"status":%q}`, input.Status)))
		if err != nil {
			return err
		}
		if err := r.workflow.RecordWorkflow(ctx, tx, workflow); err != nil {
			return err
		}
		if intent, ok := agent.AuditIntentFromContext(ctx); ok {
			if err := r.recordAudit(ctx, tx, &intent, input.RunID, input.RunID, domain); err != nil {
				return err
			}
		}
		transitioned = true
		return nil
	})
	return out, transitioned, err
}

func (r *Repository) CancelRunWorkflow(ctx context.Context, input agent.RunFinish, jobID string, workflow jobs.WorkflowIntent) (bool, error) {
	if r.workflow == nil || r.jobs == nil {
		return false, errors.New("agent workflow and jobs authorities are required")
	}
	metadata, err := normalizedJSONObject(input.MetadataJSON)
	if err != nil {
		return false, err
	}
	principal, err := principalID(input.PrincipalID)
	if err != nil {
		return false, err
	}
	transitioned := false
	err = r.withTx(ctx, func(tx Tx, q *agentdb.Queries) error {
		if tx == nil {
			return errors.New("agent cancellation requires a transaction")
		}
		job, err := r.jobs.GetTx(ctx, tx, jobID)
		if err != nil {
			return err
		}
		if job.ResourceID != input.RunID || (job.Status != jobs.StatusQueued && job.Status != jobs.StatusCancelled) {
			return errors.New("agent job is not cancellable")
		}
		if job.Status == jobs.StatusQueued {
			if err := r.jobs.CancelTx(ctx, tx, jobID); err != nil {
				return err
			}
		}
		prior, err := q.GetAgentRunStatus(ctx, agentdb.GetAgentRunStatusParams{RunID: input.RunID, ConversationID: input.ConversationID})
		if err != nil {
			return err
		}
		if prior == agent.RunStatusCanceled {
			transitioned = false
		} else if prior == agent.RunStatusRunning || prior == agent.RunStatusPreparing {
			if _, err := q.FinishAgentRun(ctx, agentdb.FinishAgentRunParams{Status: agent.RunStatusCanceled, StopReason: input.StopReason, Error: input.Error, MetadataJson: []byte(metadata), ID: input.RunID, ConversationID: input.ConversationID, PrincipalID: principal}); err != nil {
				return err
			}
			transitioned = true
		} else {
			return fmt.Errorf("agent run is not cancellable from status %q", prior)
		}
		if err := r.recordWorkflowEvent(ctx, tx, input.RunID, workflow); err != nil {
			return err
		}
		var domain *DomainEvent
		if transitioned {
			domain, err = r.recordDomain(ctx, tx, principal, "agent_run", input.RunID, "agent.run.canceled", []byte(`{"status":"canceled"}`))
			if err != nil {
				return err
			}
		}
		if err := r.workflow.RecordWorkflow(ctx, tx, workflow); err != nil {
			return err
		}
		if intent, ok := agent.AuditIntentFromContext(ctx); ok {
			return r.recordAudit(ctx, tx, &intent, input.RunID, input.RunID, domain)
		}
		return nil
	})
	return transitioned, err
}

func (r *Repository) VerifyRunLease(ctx context.Context, runID, jobID string, fence jobs.Fence) error {
	if r.jobs == nil {
		return nil
	}
	job, err := r.jobs.Get(ctx, jobID)
	if err != nil {
		return err
	}
	if job.Kind != "agent.run" || job.ResourceKind != "agent_run" || job.ResourceID != runID || job.Status != jobs.StatusRunning || job.Fence() != fence || !leaseUnexpired(job.LeaseExpiresAt) {
		return errors.New("stale durable job claim")
	}
	return nil
}

func (r *Repository) FinishRun(ctx context.Context, input agent.RunFinish) (agent.Run, error) {
	metadata, err := normalizedJSONObject(input.MetadataJSON)
	if err != nil {
		return agent.Run{}, err
	}
	if !validRunStatus(input.Status) || input.Status == agent.RunStatusRunning {
		return agent.Run{}, fmt.Errorf("invalid final agent run status %q", input.Status)
	}
	principal, err := principalID(input.PrincipalID)
	if err != nil {
		return agent.Run{}, err
	}
	if input.JobID != "" {
		if err := r.VerifyRunLease(ctx, input.RunID, input.JobID, input.JobFence); err != nil {
			return agent.Run{}, err
		}
	}
	var out agent.Run
	err = r.withTx(ctx, func(tx Tx, q *agentdb.Queries) error {
		row, err := q.FinishAgentRun(ctx, agentdb.FinishAgentRunParams{Status: input.Status, StopReason: input.StopReason, InputTokens: input.InputTokens, OutputTokens: input.OutputTokens, TotalTokens: input.TotalTokens, Error: input.Error, MetadataJson: []byte(metadata), ID: input.RunID, ConversationID: input.ConversationID, PrincipalID: principal})
		if err != nil {
			return err
		}
		domain, err := r.recordDomain(ctx, tx, principal, "agent_run", input.RunID, "agent.run."+input.Status, []byte(fmt.Sprintf(`{"status":%q}`, input.Status)))
		if err != nil {
			return err
		}
		if intent, ok := agent.AuditIntentFromContext(ctx); ok {
			if err := r.recordAudit(ctx, tx, &intent, input.RunID, input.RunID, domain); err != nil {
				return err
			}
		}
		out = mapRun(row)
		return nil
	})
	return out, err
}

func (r *Repository) ListRuns(ctx context.Context, principal, conversation string) ([]agent.Run, error) {
	return r.ListRunsPage(ctx, principal, conversation, agent.Page{})
}
func (r *Repository) ListRunsPage(ctx context.Context, principal, conversation string, page agent.Page) ([]agent.Run, error) {
	principal, err := principalID(principal)
	if err != nil {
		return nil, err
	}
	rows, err := agentdb.New(r.db).ListAgentRuns(ctx, agentdb.ListAgentRunsParams{ConversationID: conversation, PrincipalID: principal})
	if err != nil {
		return nil, mapDBError(err)
	}
	out := make([]agent.Run, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapRun(row))
	}
	return pageByID(out, page, func(v agent.Run) string { return v.ID }), nil
}
func (r *Repository) GetRun(ctx context.Context, principal, conversation, runID string) (agent.Run, error) {
	principal, err := principalID(principal)
	if err != nil {
		return agent.Run{}, err
	}
	row, err := agentdb.New(r.db).GetAgentRunInConversation(ctx, agentdb.GetAgentRunInConversationParams{RunID: runID, ConversationID: conversation, PrincipalID: principal})
	if err != nil {
		return agent.Run{}, mapDBError(err)
	}
	return mapRun(row), nil
}
func (r *Repository) GetRunByID(ctx context.Context, principal, runID string) (agent.Run, error) {
	principal, err := principalID(principal)
	if err != nil {
		return agent.Run{}, err
	}
	row, err := agentdb.New(r.db).GetAgentRunForPrincipal(ctx, agentdb.GetAgentRunForPrincipalParams{RunID: runID, PrincipalID: principal})
	if err != nil {
		return agent.Run{}, mapDBError(err)
	}
	return mapRun(row), nil
}

func (r *Repository) AppendEvent(ctx context.Context, input agent.EventInput) (agent.Event, error) {
	payload, err := normalizedJSONObject(input.PayloadJSON)
	if err != nil {
		return agent.Event{}, err
	}
	principal, err := principalID(input.PrincipalID)
	if err != nil {
		return agent.Event{}, err
	}
	if input.Sequence <= 0 || strings.TrimSpace(input.EventType) == "" {
		return agent.Event{}, errors.New("event sequence and type are required")
	}
	var out agent.Event
	err = r.withTx(ctx, func(tx Tx, q *agentdb.Queries) error {
		exists, err := q.AgentRunExistsForPrincipal(ctx, agentdb.AgentRunExistsForPrincipalParams{RunID: input.RunID, PrincipalID: principal})
		if err != nil {
			return err
		}
		if !exists {
			return agent.ErrNotFound
		}
		aggregate, err := q.AllocateAgentEventSequence(ctx, input.RunID)
		if err != nil {
			return err
		}
		row, err := r.insertEvent(ctx, tx, input.RunID, int64(aggregate), input.Sequence, strings.TrimSpace(input.EventType), defaultSeverity(input.Severity), payload, "")
		if err != nil {
			return err
		}
		out = mapEvent(row)
		return nil
	})
	return out, err
}

func (r *Repository) ListEvents(ctx context.Context, principal, runID string) ([]agent.Event, error) {
	return r.ListEventsPage(ctx, principal, runID, agent.Page{})
}
func (r *Repository) ListEventsPage(ctx context.Context, principal, runID string, page agent.Page) ([]agent.Event, error) {
	principal, err := principalID(principal)
	if err != nil {
		return nil, err
	}
	exists, err := agentdb.New(r.db).AgentRunExistsForPrincipal(ctx, agentdb.AgentRunExistsForPrincipalParams{RunID: runID, PrincipalID: principal})
	if err != nil {
		return nil, mapDBError(err)
	}
	if !exists {
		return nil, agent.ErrNotFound
	}
	limit := page.Limit
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	after := int64(0)
	if strings.TrimSpace(page.After) != "" {
		after, err = strconv.ParseInt(strings.TrimSpace(page.After), 10, 64)
		if err != nil || after < 1 {
			return nil, errors.New("invalid event cursor")
		}
	}
	rows, err := agentdb.New(r.db).ListAgentEvents(ctx, agentdb.ListAgentEventsParams{RunID: runID, AfterID: after, PageLimit: int32(limit)})
	if err != nil {
		return nil, mapDBError(err)
	}
	out := make([]agent.Event, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapEvent(row))
	}
	return out, nil
}

func (r *Repository) insertEvent(ctx context.Context, db DBTX, runID string, aggregateVersion, streamSequence int64, eventType, severity, payload, key string) (agentdb.InsertAgentEventRow, error) {
	q := agentdb.New(db)
	if key != "" {
		if old, err := q.GetAgentEventByKey(ctx, agentdb.GetAgentEventByKeyParams{RunID: runID, EventKey: key}); err == nil {
			if old.StreamSequence != streamSequence || old.EventType != eventType || old.Severity != severity || !jsonEquivalent(old.PayloadJson, payload) {
				return agentdb.InsertAgentEventRow{}, jobs.ErrConflict
			}
			return agentdb.InsertAgentEventRow{EventID: old.EventID, RunID: old.RunID, AggregateVersion: old.AggregateVersion, StreamSequence: old.StreamSequence, EventType: old.EventType, Severity: old.Severity, PayloadJson: old.PayloadJson, EventKey: old.EventKey, CreatedAt: old.CreatedAt}, nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return agentdb.InsertAgentEventRow{}, err
		}
	}
	row, err := q.InsertAgentEvent(ctx, agentdb.InsertAgentEventParams{RunID: runID, AggregateVersion: aggregateVersion, StreamSequence: streamSequence, EventType: eventType, Severity: severity, PayloadJson: []byte(payload), EventKey: key})
	if err == nil {
		return row, nil
	}
	// ON CONFLICT DO NOTHING produces pgx.ErrNoRows. Resolve the winner and
	// compare every immutable field so retries cannot silently change history.
	if errors.Is(err, pgx.ErrNoRows) {
		old, getErr := q.GetAgentEventBySequence(ctx, agentdb.GetAgentEventBySequenceParams{RunID: runID, AggregateVersion: aggregateVersion})
		if getErr != nil {
			return agentdb.InsertAgentEventRow{}, getErr
		}
		if old.StreamSequence != streamSequence || old.EventType != eventType || old.Severity != severity || !jsonEquivalent(old.PayloadJson, payload) || (key != "" && old.EventKey != key) {
			return agentdb.InsertAgentEventRow{}, jobs.ErrConflict
		}
		return agentdb.InsertAgentEventRow{EventID: old.EventID, RunID: old.RunID, AggregateVersion: old.AggregateVersion, StreamSequence: old.StreamSequence, EventType: old.EventType, Severity: old.Severity, PayloadJson: old.PayloadJson, EventKey: old.EventKey, CreatedAt: old.CreatedAt}, nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return agentdb.InsertAgentEventRow{}, jobs.ErrConflict
	}
	return agentdb.InsertAgentEventRow{}, err
}

func (r *Repository) recordWorkflowEvent(ctx context.Context, tx Tx, runID string, workflow jobs.WorkflowIntent) error {
	if strings.TrimSpace(workflow.Event.Key) == "" {
		return nil
	}
	if tx == nil {
		return errors.New("agent workflow event requires a transaction")
	}
	q := agentdb.New(tx)
	if err := q.AcquireAgentRunByIDMutationLock(ctx, runID); err != nil {
		return err
	}
	payload := strings.TrimSpace(string(workflow.Event.Data))
	if payload == "" {
		payload = "{}"
	}
	if !json.Valid([]byte(payload)) {
		return errors.New("invalid workflow event payload")
	}
	if strings.TrimSpace(workflow.Event.EventType) == "" {
		return errors.New("workflow event type is required")
	}
	if old, err := q.GetAgentEventByKey(ctx, agentdb.GetAgentEventByKeyParams{RunID: runID, EventKey: workflow.Event.Key}); err == nil {
		if old.EventType != workflow.Event.EventType || old.Severity != "info" || !jsonEquivalent(old.PayloadJson, payload) {
			return jobs.ErrConflict
		}
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	seq, err := q.AllocateAgentEventSequence(ctx, runID)
	if err != nil {
		return err
	}
	_, err = r.insertEvent(ctx, tx, runID, int64(seq), 0, workflow.Event.EventType, "info", payload, workflow.Event.Key)
	return err
}

func (r *Repository) verifyJobTx(ctx context.Context, tx Tx, jobID, runID string, fence jobs.Fence) error {
	if r.jobs == nil || tx == nil {
		return errors.New("agent jobs authority is required")
	}
	job, err := r.jobs.GetTx(ctx, tx, jobID)
	if err != nil {
		return err
	}
	if job.Kind != "agent.run" || job.ResourceKind != "agent_run" || job.ResourceID != runID || job.Status != jobs.StatusRunning || job.Fence() != fence || !leaseUnexpired(job.LeaseExpiresAt) {
		return errors.New("stale durable job claim")
	}
	return nil
}

// The generated query rows intentionally remain query-specific. This adapter
// keeps the domain model independent from sqlc naming decisions.
func mapConversation(row any) agent.Conversation {
	switch v := row.(type) {
	case agentdb.CreateAgentConversationRow:
		return agent.Conversation{ID: v.ID, PrincipalID: v.PrincipalID, Title: v.Title, Status: v.Status, MetadataJSON: v.MetadataJson, TranscriptJSON: v.TranscriptJson, CreatedAt: timestampString(v.CreatedAt), UpdatedAt: timestampString(v.UpdatedAt), ArchivedAt: timestampString(v.ArchivedAt)}
	case agentdb.GetAgentConversationRow:
		return agent.Conversation{ID: v.ID, PrincipalID: v.PrincipalID, Title: v.Title, Status: v.Status, MetadataJSON: v.MetadataJson, TranscriptJSON: v.TranscriptJson, CreatedAt: timestampString(v.CreatedAt), UpdatedAt: timestampString(v.UpdatedAt), ArchivedAt: timestampString(v.ArchivedAt)}
	case agentdb.ArchiveAgentConversationRow:
		return agent.Conversation{ID: v.ID, PrincipalID: v.PrincipalID, Title: v.Title, Status: v.Status, MetadataJSON: v.MetadataJson, TranscriptJSON: v.TranscriptJson, CreatedAt: timestampString(v.CreatedAt), UpdatedAt: timestampString(v.UpdatedAt), ArchivedAt: timestampString(v.ArchivedAt)}
	case agentdb.UpdateAgentConversationTitleRow:
		return agent.Conversation{ID: v.ID, PrincipalID: v.PrincipalID, Title: v.Title, Status: v.Status, MetadataJSON: v.MetadataJson, TranscriptJSON: v.TranscriptJson, CreatedAt: timestampString(v.CreatedAt), UpdatedAt: timestampString(v.UpdatedAt), ArchivedAt: timestampString(v.ArchivedAt)}
	case agentdb.UpdateAgentConversationTranscriptRow:
		return agent.Conversation{ID: v.ID, PrincipalID: v.PrincipalID, Title: v.Title, Status: v.Status, MetadataJSON: v.MetadataJson, TranscriptJSON: v.TranscriptJson, CreatedAt: timestampString(v.CreatedAt), UpdatedAt: timestampString(v.UpdatedAt), ArchivedAt: timestampString(v.ArchivedAt)}
	case agentdb.UpdateDefaultAgentConversationTitleRow:
		return agent.Conversation{ID: v.ID, PrincipalID: v.PrincipalID, Title: v.Title, Status: v.Status, MetadataJSON: v.MetadataJson, TranscriptJSON: v.TranscriptJson, CreatedAt: timestampString(v.CreatedAt), UpdatedAt: timestampString(v.UpdatedAt), ArchivedAt: timestampString(v.ArchivedAt)}
	default:
		return agent.Conversation{}
	}
}

func mapMessage(row agentdb.AppendAgentMessageRow) agent.Message {
	return agent.Message{ID: row.ID, ConversationID: row.ConversationID, RunID: row.RunID.String, Seq: row.Sequence, Role: row.Role, ContentText: row.ContentText, ContentJSON: row.ContentJson, ToolCallID: row.ToolCallID, ToolName: row.ToolName, IsError: row.IsError, CreatedAt: timestampString(row.CreatedAt)}
}
func mapMessageList(row agentdb.ListAgentMessagesRow) agent.Message {
	return agent.Message{ID: row.ID, ConversationID: row.ConversationID, RunID: row.RunID.String, Seq: row.Sequence, Role: row.Role, ContentText: row.ContentText, ContentJSON: row.MContentJson, ToolCallID: row.ToolCallID, ToolName: row.ToolName, IsError: row.IsError, CreatedAt: timestampString(row.CreatedAt)}
}

func mapRun(row any) agent.Run {
	switch v := row.(type) {
	case agentdb.CreateAgentRunRow:
		return agent.Run{ID: v.ID, ConversationID: v.ConversationID, Status: v.Status, Model: v.Model, StopReason: v.StopReason, InputTokens: v.InputTokens, OutputTokens: v.OutputTokens, TotalTokens: v.TotalTokens, Error: v.Error, StartedAt: timestampString(v.StartedAt), FinishedAt: timestampString(v.FinishedAt), MetadataJSON: v.MetadataJson, CreatedAt: timestampString(v.StartedAt)}
	case agentdb.FinishAgentRunRow:
		return agent.Run{ID: v.ID, ConversationID: v.ConversationID, Status: v.Status, Model: v.Model, StopReason: v.StopReason, InputTokens: v.InputTokens, OutputTokens: v.OutputTokens, TotalTokens: v.TotalTokens, Error: v.Error, StartedAt: timestampString(v.StartedAt), FinishedAt: timestampString(v.FinishedAt), MetadataJSON: v.MetadataJson, CreatedAt: timestampString(v.StartedAt)}
	case agentdb.GetAgentRunInConversationRow:
		return agent.Run{ID: v.ID, ConversationID: v.ConversationID, Status: v.Status, Model: v.Model, StopReason: v.StopReason, InputTokens: v.InputTokens, OutputTokens: v.OutputTokens, TotalTokens: v.TotalTokens, Error: v.Error, StartedAt: timestampString(v.StartedAt), FinishedAt: timestampString(v.FinishedAt), MetadataJSON: v.RMetadataJson, CreatedAt: timestampString(v.StartedAt)}
	case agentdb.GetAgentRunForPrincipalRow:
		return agent.Run{ID: v.ID, ConversationID: v.ConversationID, Status: v.Status, Model: v.Model, StopReason: v.StopReason, InputTokens: v.InputTokens, OutputTokens: v.OutputTokens, TotalTokens: v.TotalTokens, Error: v.Error, StartedAt: timestampString(v.StartedAt), FinishedAt: timestampString(v.FinishedAt), MetadataJSON: v.RMetadataJson, CreatedAt: timestampString(v.StartedAt)}
	case agentdb.ListAgentRunsRow:
		return agent.Run{ID: v.ID, ConversationID: v.ConversationID, Status: v.Status, Model: v.Model, StopReason: v.StopReason, InputTokens: v.InputTokens, OutputTokens: v.OutputTokens, TotalTokens: v.TotalTokens, Error: v.Error, StartedAt: timestampString(v.StartedAt), FinishedAt: timestampString(v.FinishedAt), MetadataJSON: v.RMetadataJson, CreatedAt: timestampString(v.StartedAt)}
	default:
		return agent.Run{}
	}
}

func mapEvent(row any) agent.Event {
	switch v := row.(type) {
	case agentdb.InsertAgentEventRow:
		return agent.Event{ID: fmt.Sprintf("%020d", v.EventID), RunID: v.RunID, Seq: v.StreamSequence, EventType: v.EventType, Severity: v.Severity, PayloadJSON: v.PayloadJson, CreatedAt: timestampString(v.CreatedAt)}
	case agentdb.ListAgentEventsRow:
		return agent.Event{ID: fmt.Sprintf("%020d", v.EventID), RunID: v.RunID, Seq: v.StreamSequence, EventType: v.EventType, Severity: v.Severity, PayloadJSON: v.PayloadJson, CreatedAt: timestampString(v.CreatedAt)}
	default:
		return agent.Event{}
	}
}

func timestampString(v pgtype.Timestamptz) string {
	if !v.Valid {
		return ""
	}
	return v.Time.Format(time.RFC3339Nano)
}

func principalID(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", errors.New("principal id is required")
	}
	return v, nil
}

func mapDBError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return agent.ErrNotFound
	}
	return err
}
func defaultSeverity(v string) string {
	if strings.TrimSpace(v) == "" {
		return "info"
	}
	return strings.TrimSpace(v)
}
func validMessageRole(v string) bool {
	return v == agent.MessageRoleUser || v == agent.MessageRoleAssistant || v == agent.MessageRoleTool || v == agent.MessageRoleSummary
}
func validRunStatus(v string) bool {
	return v == agent.RunStatusRunning || v == agent.RunStatusPreparing || v == agent.RunStatusCompleted || v == agent.RunStatusFailed || v == agent.RunStatusCanceled
}
func normalizedJSONObject(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}", nil
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return "", fmt.Errorf("invalid JSON object: %w", err)
	}
	if _, ok := v.(map[string]any); !ok {
		return "", errors.New("JSON value must be an object")
	}
	return raw, nil
}
func normalizedJSONArray(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "[]", nil
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return "", fmt.Errorf("invalid JSON array: %w", err)
	}
	if _, ok := v.([]any); !ok {
		return "", errors.New("JSON value must be an array")
	}
	return raw, nil
}

func jsonEquivalent(left, right string) bool {
	var a, b any
	if json.Unmarshal([]byte(left), &a) != nil || json.Unmarshal([]byte(right), &b) != nil {
		return left == right
	}
	return reflect.DeepEqual(a, b)
}
func pageByID[T any](rows []T, page agent.Page, id func(T) string) []T {
	limit := page.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	start := 0
	if after := strings.TrimSpace(page.After); after != "" {
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
func leaseUnexpired(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, v); err == nil {
			return parsed.After(time.Now())
		}
	}
	return false
}
func newID(prefix string) string { return prefix + "_" + newSecret()[:24] }
func newUUIDv7() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func isCanonicalUUID(value string) bool {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	return err == nil && parsed.String() == strings.ToLower(strings.TrimSpace(value))
}

func isUUIDv7(value string) bool {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	return err == nil && parsed.Version() == 7 && parsed.String() == strings.ToLower(strings.TrimSpace(value))
}

func requiredOrUUIDv7(value, label string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return newUUIDv7()
	}
	if isCanonicalUUID(value) {
		return strings.ToLower(value), nil
	}
	return "", fmt.Errorf("%s must be a UUID", label)
}

func newSecret() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		sum := sha256.Sum256([]byte(time.Now().Format(time.RFC3339Nano)))
		return hex.EncodeToString(sum[:])
	}
	return hex.EncodeToString(b[:])
}

var _ = mapMessageList
