package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func agentPostgresTestRepo(t *testing.T, suffix string) (*pgxpool.Pool, *Repository) {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "agent_"+suffix)
	pool, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(t.Context(), SchemaSQL()); err != nil {
		t.Fatalf("apply agent schema: %v", err)
	}
	return pool, NewRepository(pool)
}

func TestPostgreSQL18AgentCRUDScopingAndMessageSequence(t *testing.T) {
	_, repo := agentPostgresTestRepo(t, "crud")
	ctx := t.Context()
	conversation, err := repo.CreateConversation(ctx, agent.ConversationInput{PrincipalID: "owner", MetadataJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	conversations, err := repo.ListConversations(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(conversations) != 1 || conversations[0] != conversation {
		t.Fatalf("listed conversations = %#v, want %#v", conversations, []agent.Conversation{conversation})
	}
	if conversations, err := repo.ListConversations(ctx, "other"); err != nil {
		t.Fatal(err)
	} else if len(conversations) != 0 {
		t.Fatalf("cross-principal conversation list = %#v, want empty", conversations)
	}
	if _, err := repo.GetConversation(ctx, "other", conversation.ID); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("cross-principal conversation lookup = %v", err)
	}
	run, err := repo.CreateRun(ctx, agent.RunInput{PrincipalID: "owner", ConversationID: conversation.ID, RunID: "run-crud", Status: agent.RunStatusPreparing, MetadataJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != agent.RunStatusPreparing {
		t.Fatalf("run status = %q", run.Status)
	}
	const writers = 16
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := repo.AppendMessage(ctx, agent.MessageInput{PrincipalID: "owner", ConversationID: conversation.ID, RunID: run.ID, Role: agent.MessageRoleUser, ContentText: fmt.Sprintf("message-%d", i), ContentJSON: `{}`})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	messages, err := repo.ListMessages(ctx, "owner", conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != writers {
		t.Fatalf("message count = %d, want %d", len(messages), writers)
	}
	for i, message := range messages {
		if message.Seq != int64(i+1) {
			t.Fatalf("message %d sequence = %d", i, message.Seq)
		}
	}
}

type nonTransactionalDB struct{}

func (nonTransactionalDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (nonTransactionalDB) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (nonTransactionalDB) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }

func TestAgentWriteRejectsNonTransactionalHandle(t *testing.T) {
	repo := NewRepository(nonTransactionalDB{})
	if _, err := repo.CreateConversation(t.Context(), agent.ConversationInput{PrincipalID: "owner"}); err == nil {
		t.Fatal("non-transactional write unexpectedly succeeded")
	}
}

type recordingWorkflow struct {
	mu    sync.Mutex
	count int
	err   error
}

type recordingDomain struct {
	mu     sync.Mutex
	inputs []DomainEventInput
	err    error
}

type mismatchingDomain struct{}

func (mismatchingDomain) AppendDomainEvent(_ context.Context, _ Tx, input DomainEventInput) (DomainEvent, error) {
	return DomainEvent{EventID: input.EventID, ScopeID: input.ScopeID, AggregateType: input.AggregateType, AggregateID: input.AggregateID, EventType: input.EventType, SchemaVersion: input.SchemaVersion, CorrelationID: input.CorrelationID, Payload: []byte(`{"tampered":true}`), AggregateVersion: 1}, nil
}

func TestAgentDomainProjectionValidationRollsBackSourceMutation(t *testing.T) {
	pool, _ := agentPostgresTestRepo(t, "domain_projection")
	repo, err := NewWithOptions(pool, Options{Domain: mismatchingDomain{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateConversation(t.Context(), agent.ConversationInput{PrincipalID: "owner", MetadataJSON: `{}`}); err == nil {
		t.Fatal("mismatching domain projection unexpectedly accepted")
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM agent.conversations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("source mutation count after domain projection rollback = %d", count)
	}
}

func (d *recordingDomain) AppendDomainEvent(_ context.Context, _ Tx, input DomainEventInput) (DomainEvent, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return DomainEvent{}, d.err
	}
	d.inputs = append(d.inputs, input)
	return DomainEvent{EventID: input.EventID, ScopeID: input.ScopeID, AggregateType: input.AggregateType, AggregateID: input.AggregateID, EventType: input.EventType, SchemaVersion: input.SchemaVersion, CorrelationID: input.CorrelationID, Payload: append([]byte(nil), input.Payload...), AggregateVersion: int64(len(d.inputs))}, nil
}

func (w *recordingWorkflow) RecordWorkflow(_ context.Context, _ jobspostgres.Tx, _ jobs.WorkflowIntent) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.count++
	return w.err
}

func TestPostgreSQL18AgentWorkflowReplayConflictAndRollback(t *testing.T) {
	pool, base := agentPostgresTestRepo(t, "workflow")
	ctx := t.Context()
	conversation, err := base.CreateConversation(ctx, agent.ConversationInput{PrincipalID: "owner", MetadataJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	workflow := &recordingWorkflow{}
	domain := &recordingDomain{}
	repo, err := NewWithOptions(pool, Options{Workflow: workflow, Domain: domain})
	if err != nil {
		t.Fatal(err)
	}
	run, err := repo.CreateRun(ctx, agent.RunInput{PrincipalID: "owner", ConversationID: conversation.ID, RunID: "run-workflow", Status: agent.RunStatusPreparing, MetadataJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	intent := jobs.WorkflowIntent{Event: jobs.EventInput{Key: "agent_run.queued:run-workflow", ResourceKind: "agent_run", ResourceID: run.ID, EventType: "agent_run.queued", Data: []byte(`{"ok":true}`)}}
	if _, err := repo.ActivateRunWorkflow(ctx, "owner", conversation.ID, run.ID, intent); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ActivateRunWorkflow(ctx, "owner", conversation.ID, run.ID, intent); err != nil {
		t.Fatalf("workflow replay: %v", err)
	}
	workflow.mu.Lock()
	if workflow.count != 2 {
		t.Fatalf("workflow call count = %d", workflow.count)
	}
	workflow.mu.Unlock()
	for i := 1; i <= 3; i++ {
		if _, err := repo.AppendEvent(ctx, agent.EventInput{PrincipalID: "owner", RunID: run.ID, Sequence: int64(i), EventType: "agent_run.output", PayloadJSON: fmt.Sprintf(`{"step":%d}`, i)}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := repo.ListEvents(ctx, "owner", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 || events[0].Seq != 0 || events[1].Seq != 1 || events[2].Seq != 2 || events[3].Seq != 3 {
		t.Fatalf("events = %#v", events)
	}
	if _, _, err := repo.FinishRunWorkflow(ctx, agent.RunFinish{PrincipalID: "owner", ConversationID: conversation.ID, RunID: run.ID, Status: agent.RunStatusFailed, MetadataJSON: `{}`}, jobs.WorkflowIntent{Event: jobs.EventInput{Key: "agent_run.failed:run-workflow", ResourceKind: "agent_run", ResourceID: run.ID, EventType: "agent_run.failed", Data: []byte(`{"ok":false}`)}}); err != nil {
		t.Fatal(err)
	}
	domain.mu.Lock()
	if len(domain.inputs) != 3 || domain.inputs[0].EventType != "agent.run.created" || domain.inputs[1].EventType != "agent.run.activated" || domain.inputs[2].EventType != "agent.run.failed" {
		t.Fatalf("canonical domain lifecycle = %#v", domain.inputs)
	}
	for _, input := range domain.inputs {
		if !isUUIDv7(input.EventID) || !isUUIDv7(input.CorrelationID) {
			t.Fatalf("domain identity = %#v", input)
		}
	}
	domain.mu.Unlock()
	if _, _, err := repo.FinishRunWorkflow(ctx, agent.RunFinish{PrincipalID: "owner", ConversationID: conversation.ID, RunID: run.ID, Status: agent.RunStatusFailed, MetadataJSON: `{}`}, jobs.WorkflowIntent{Event: jobs.EventInput{Key: "agent_run.failed:run-workflow", ResourceKind: "agent_run", ResourceID: run.ID, EventType: "agent_run.failed", Data: []byte(`{"ok":"changed"}`)}}); !errors.Is(err, jobs.ErrConflict) {
		t.Fatalf("terminal replay conflict = %v", err)
	}
	workflow.err = errors.New("workflow failed")
	run2, err := repo.CreateRun(ctx, agent.RunInput{PrincipalID: "owner", ConversationID: conversation.ID, RunID: "run-rollback", Status: agent.RunStatusPreparing, MetadataJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ActivateRunWorkflow(ctx, "owner", conversation.ID, run2.ID, intentForRun(run2.ID)); err == nil {
		t.Fatal("workflow failure unexpectedly committed")
	}
	stored, err := repo.GetRun(ctx, "owner", conversation.ID, run2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != agent.RunStatusPreparing {
		t.Fatalf("rolled-back run status = %q", stored.Status)
	}
}

func TestAgentUUIDv7Generation(t *testing.T) {
	for i := 0; i < 8; i++ {
		value, err := newUUIDv7()
		if err != nil {
			t.Fatal(err)
		}
		if !isUUIDv7(value) {
			t.Fatalf("generated identity %q is not UUIDv7", value)
		}
	}
}

func TestPostgreSQL18ConcurrentAgentEventAllocation(t *testing.T) {
	pool, repo := agentPostgresTestRepo(t, "events")
	ctx := t.Context()
	conversation, err := repo.CreateConversation(ctx, agent.ConversationInput{PrincipalID: "owner", MetadataJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	run, err := repo.CreateRun(ctx, agent.RunInput{PrincipalID: "owner", ConversationID: conversation.ID, RunID: "run-events", Status: agent.RunStatusRunning, MetadataJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	const writers = 16
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tx, err := pool.Begin(ctx)
			if err != nil {
				errs <- err
				return
			}
			intent := jobs.WorkflowIntent{Event: jobs.EventInput{Key: fmt.Sprintf("agent_run.progress:%d", i), ResourceKind: "agent_run", ResourceID: run.ID, EventType: "agent_run.progress", Data: []byte(fmt.Sprintf(`{"step":%d}`, i))}}
			if err := repo.recordWorkflowEvent(ctx, tx, run.ID, intent); err != nil {
				_ = tx.Rollback(ctx)
				errs <- err
				return
			}
			if err := tx.Commit(ctx); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	events, err := repo.ListEvents(ctx, "owner", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != writers {
		t.Fatalf("event count = %d, want %d", len(events), writers)
	}
	for _, event := range events {
		if event.Seq != 0 {
			t.Fatalf("workflow event stream sequence = %d, want 0", event.Seq)
		}
	}
	rows, err := pool.Query(ctx, `SELECT aggregate_version FROM agent.events WHERE run_id='run-events' ORDER BY aggregate_version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for i := 1; rows.Next(); i++ {
		var version int64
		if err := rows.Scan(&version); err != nil {
			t.Fatal(err)
		}
		if version != int64(i) {
			t.Fatalf("aggregate version = %d, want %d", version, i)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func intentForRun(runID string) jobs.WorkflowIntent {
	return jobs.WorkflowIntent{Event: jobs.EventInput{Key: "agent_run.queued:" + runID, ResourceKind: "agent_run", ResourceID: runID, EventType: "agent_run.queued", Data: []byte(`{}`)}}
}

func TestPostgreSQL18AgentCallerOwnedRollbackAndLeastPrivilege(t *testing.T) {
	pool, repo := agentPostgresTestRepo(t, "rollback")
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	txRepo := repo.WithTx(tx)
	conversation, err := txRepo.CreateConversation(ctx, agent.ConversationInput{PrincipalID: "rollback", MetadataJSON: `{}`})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetConversation(ctx, "rollback", conversation.ID); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("rolled-back conversation lookup = %v", err)
	}
	var hasDelete bool
	if err := pool.QueryRow(ctx, `SELECT has_table_privilege(current_user, 'agent.conversations', 'DELETE')`).Scan(&hasDelete); err != nil {
		t.Fatal(err)
	}
	// The administrator owns the table; this assertion documents the runtime
	// role contract in the schema test below rather than granting broad rights
	// to callers of the repository.
	if !hasDelete {
		t.Fatal("database administrator unexpectedly lacks table ownership")
	}
}

func TestPostgreSQL18AgentRuntimeLeastPrivilege(t *testing.T) {
	h := postgrestest.Start(t)
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Login: true, Password: "runtime-secret"})
	readonly := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly", Login: true, Password: "readonly-secret"})
	database := h.NewDatabase(t, "agent_roles")
	h.GrantDatabase(t, database.Name, runtime, "CONNECT")
	h.GrantDatabase(t, database.Name, readonly, "CONNECT")
	admin, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(t.Context(), SchemaSQL()); err != nil {
		t.Fatal(err)
	}
	runtimeDB, err := pgxpool.New(t.Context(), database.URL(runtime))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	var canDelete bool
	if err := runtimeDB.QueryRow(t.Context(), `SELECT has_table_privilege(current_user, 'agent.conversations', 'DELETE')`).Scan(&canDelete); err != nil {
		t.Fatal(err)
	}
	if canDelete {
		t.Fatal("runtime role has agent DELETE privilege")
	}
	if _, err := runtimeDB.Exec(t.Context(), `DELETE FROM agent.conversations`); err == nil {
		t.Fatal("runtime DELETE unexpectedly succeeded")
	}
	if _, err := runtimeDB.Exec(t.Context(), `UPDATE agent.messages SET content_text='forbidden'`); err == nil {
		t.Fatal("runtime message UPDATE unexpectedly succeeded")
	}
	if _, err := runtimeDB.Exec(t.Context(), `UPDATE agent.events SET severity='forbidden'`); err == nil {
		t.Fatal("runtime event UPDATE unexpectedly succeeded")
	}
	var canUseEventSequence, canReadEventSequence bool
	if err := runtimeDB.QueryRow(t.Context(), `
		SELECT has_sequence_privilege(current_user, 'agent.events_event_id_seq', 'USAGE'),
		       has_sequence_privilege(current_user, 'agent.events_event_id_seq', 'SELECT')`).
		Scan(&canUseEventSequence, &canReadEventSequence); err != nil {
		t.Fatal(err)
	}
	if !canUseEventSequence || canReadEventSequence {
		t.Fatalf("runtime event sequence privileges usage/select = %t/%t", canUseEventSequence, canReadEventSequence)
	}
	runtimeRepo := NewRepository(runtimeDB)
	conversation, err := runtimeRepo.CreateConversation(t.Context(), agent.ConversationInput{PrincipalID: "runtime", MetadataJSON: `{}`})
	if err != nil {
		t.Fatalf("runtime conversation insert: %v", err)
	}
	if _, err := runtimeRepo.AppendMessage(t.Context(), agent.MessageInput{PrincipalID: "runtime", ConversationID: conversation.ID, Role: agent.MessageRoleUser, ContentJSON: `{}`}); err != nil {
		t.Fatalf("runtime message insert: %v", err)
	}
	run, err := runtimeRepo.CreateRun(t.Context(), agent.RunInput{PrincipalID: "runtime", ConversationID: conversation.ID, RunID: "run-runtime-role", Status: agent.RunStatusRunning, MetadataJSON: `{}`})
	if err != nil {
		t.Fatalf("runtime run insert: %v", err)
	}
	if _, err := runtimeRepo.AppendEvent(t.Context(), agent.EventInput{PrincipalID: "runtime", RunID: run.ID, Sequence: 1, EventType: "agent.run.output", PayloadJSON: `{}`}); err != nil {
		t.Fatalf("runtime event insert with sequence usage: %v", err)
	}
	readonlyDB, err := pgxpool.New(t.Context(), database.URL(readonly))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(readonlyDB.Close)
	var rows int
	if err := readonlyDB.QueryRow(t.Context(), `SELECT count(*) FROM agent.conversations`).Scan(&rows); err != nil {
		t.Fatalf("readonly select: %v", err)
	}
	if _, err := readonlyDB.Exec(t.Context(), `UPDATE agent.messages SET content_text='forbidden'`); err == nil {
		t.Fatal("readonly UPDATE unexpectedly succeeded")
	}
	if _, err := readonlyDB.Exec(t.Context(), `DELETE FROM agent.conversations`); err == nil {
		t.Fatal("readonly DELETE unexpectedly succeeded")
	}
}

func TestPostgreSQL18AgentRetentionMaintenanceBoundary(t *testing.T) {
	h := postgrestest.Start(t)
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Login: true, Password: "runtime-secret"})
	maintenanceRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Login: true, Password: "maintenance-secret"})
	database := h.NewDatabase(t, "agent_retention_roles")
	h.GrantDatabase(t, database.Name, runtimeRole, "CONNECT")
	h.GrantDatabase(t, database.Name, maintenanceRole, "CONNECT")
	admin, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(t.Context(), SchemaSQL()); err != nil {
		t.Fatal(err)
	}

	var runtimeDelete, runtimeExecute, maintenanceDelete, maintenanceFloor, maintenanceExecute bool
	if err := admin.QueryRow(t.Context(), `SELECT
		has_table_privilege($1, 'agent.events', 'DELETE'),
		has_function_privilege($1, 'agent.prune_archived_agent_history(timestamptz,integer)', 'EXECUTE'),
		has_table_privilege($2, 'agent.events', 'DELETE'),
		has_table_privilege($2, 'agent.retention_floor', 'SELECT'),
		has_function_privilege($2, 'agent.prune_archived_agent_history(timestamptz,integer)', 'EXECUTE')`, runtimeRole.Name, maintenanceRole.Name).
		Scan(&runtimeDelete, &runtimeExecute, &maintenanceDelete, &maintenanceFloor, &maintenanceExecute); err != nil {
		t.Fatal(err)
	}
	if runtimeDelete || runtimeExecute || maintenanceDelete || maintenanceFloor || !maintenanceExecute {
		t.Fatalf("agent retention grants runtime_delete=%v runtime_execute=%v maintenance_delete=%v maintenance_floor=%v maintenance_execute=%v", runtimeDelete, runtimeExecute, maintenanceDelete, maintenanceFloor, maintenanceExecute)
	}

	runtimeDB, err := pgxpool.New(t.Context(), database.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	if _, err := runtimeDB.Exec(t.Context(), `SELECT agent.prune_archived_agent_history(clock_timestamp(), 1)`); err == nil {
		t.Fatal("runtime retention function unexpectedly executable")
	}
	if _, err := runtimeDB.Exec(t.Context(), `DELETE FROM agent.events`); err == nil {
		t.Fatal("runtime event DELETE unexpectedly succeeded")
	}
	if _, err := runtimeDB.Exec(t.Context(), `SELECT set_config('agent.retention', 'on', false)`); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeDB.Exec(t.Context(), `DELETE FROM agent.events`); err == nil {
		t.Fatal("runtime forged retention marker bypassed event trigger")
	}

	base := NewRepository(admin)
	seedArchived := func(id string, events int, running bool) {
		t.Helper()
		conversation, seedErr := base.CreateConversation(t.Context(), agent.ConversationInput{PrincipalID: "retention", MetadataJSON: `{}`})
		if seedErr != nil {
			t.Fatal(seedErr)
		}
		run, seedErr := base.CreateRun(t.Context(), agent.RunInput{PrincipalID: "retention", ConversationID: conversation.ID, RunID: id, Status: agent.RunStatusRunning, MetadataJSON: `{}`})
		if seedErr != nil {
			t.Fatal(seedErr)
		}
		for i := 0; i < events; i++ {
			if _, seedErr = base.AppendEvent(t.Context(), agent.EventInput{PrincipalID: "retention", RunID: run.ID, Sequence: int64(i + 1), EventType: "agent.run.output", PayloadJSON: `{}`}); seedErr != nil {
				t.Fatal(seedErr)
			}
		}
		for i := 0; i < 2; i++ {
			if _, seedErr = base.AppendMessage(t.Context(), agent.MessageInput{PrincipalID: "retention", ConversationID: conversation.ID, RunID: run.ID, Role: agent.MessageRoleUser, ContentJSON: `{}`}); seedErr != nil {
				t.Fatal(seedErr)
			}
		}
		if !running {
			if _, seedErr = base.FinishRun(t.Context(), agent.RunFinish{PrincipalID: "retention", ConversationID: conversation.ID, RunID: run.ID, Status: agent.RunStatusCompleted, MetadataJSON: `{}`}); seedErr != nil {
				t.Fatal(seedErr)
			}
		}
		if _, seedErr = base.ArchiveConversation(t.Context(), "retention", conversation.ID); seedErr != nil {
			t.Fatal(seedErr)
		}
	}
	seedArchived("run-retention-a", 2, false)
	seedArchived("run-retention-b", 1, false)
	seedArchived("run-retention-active", 1, true)
	if _, err := admin.Exec(t.Context(), `SELECT set_config('agent.retention', 'on', false); DELETE FROM agent.events`); err == nil {
		t.Fatal("forged retention marker bypassed owner trigger")
	}

	maintenanceDB, err := pgxpool.New(t.Context(), database.URL(maintenanceRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(maintenanceDB.Close)
	if _, err := maintenanceDB.Exec(t.Context(), `DELETE FROM agent.conversations`); err == nil {
		t.Fatal("maintenance direct conversation DELETE unexpectedly succeeded")
	}
	if _, err := maintenanceDB.Exec(t.Context(), `SELECT * FROM agent.retention_floor`); err == nil {
		t.Fatal("maintenance retention-floor read unexpectedly succeeded")
	}
	if _, err := maintenanceDB.Exec(t.Context(), `SELECT agent.prune_archived_agent_history(NULL::timestamptz, 1)`); err == nil {
		t.Fatal("NULL cutoff unexpectedly accepted")
	}
	if _, err := maintenanceDB.Exec(t.Context(), `SELECT agent.prune_archived_agent_history(clock_timestamp(), 1001)`); err == nil {
		t.Fatal("oversized retention batch unexpectedly accepted")
	}

	maintenance := NewMaintenance(maintenanceDB)
	physicalRows := func() int {
		t.Helper()
		var count int
		if err := admin.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM agent.conversations) + (SELECT count(*) FROM agent.runs) + (SELECT count(*) FROM agent.messages) + (SELECT count(*) FROM agent.events)`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	rollbackBefore := physicalRows()
	rollbackTx, err := maintenanceDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.PruneTx(t.Context(), rollbackTx, time.Now().UTC().Add(time.Hour), 1); err != nil {
		_ = rollbackTx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := rollbackTx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if rollbackAfter := physicalRows(); rollbackAfter != rollbackBefore {
		t.Fatalf("caller-owned retention rollback changed physical rows from %d to %d", rollbackBefore, rollbackAfter)
	}
	beforePhysical := physicalRows()
	first, err := maintenance.Prune(t.Context(), time.Now().UTC().Add(time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	firstRemoved := first.ConversationsDeleted + first.MessagesDeleted + first.RunsDeleted + first.RunEventsDeleted
	if firstRemoved != 1 {
		t.Fatalf("global retention batch removed %d rows, want 1: %#v", firstRemoved, first)
	}
	if !first.ConversationsFloorAt.Equal(time.Unix(0, 0).UTC()) || !first.RunEventsFloorAt.Equal(time.Unix(0, 0).UTC()) {
		t.Fatalf("retention floor advanced across a limited backlog: %#v", first)
	}
	if removed := beforePhysical - physicalRows(); removed > 1 {
		t.Fatalf("first physical retention batch removed %d rows, want <=1", removed)
	}
	beforePhysical = physicalRows()
	second, err := maintenance.Prune(t.Context(), time.Now().UTC().Add(time.Hour), 3)
	if err != nil {
		t.Fatal(err)
	}
	if second.RunEventsDeleted == 0 || second.MessagesDeleted == 0 {
		t.Fatalf("maintenance did not drain terminal evidence: %#v", second)
	}
	if removed := beforePhysical - physicalRows(); removed > 3 || second.ConversationsDeleted+second.MessagesDeleted+second.RunsDeleted+second.RunEventsDeleted > 3 {
		t.Fatalf("global retention batch exceeded limit: physical=%d result=%#v", removed, second)
	}
	if _, err := maintenance.Prune(t.Context(), time.Now().UTC().Add(time.Hour), MaxRetentionBatch); err != nil {
		t.Fatal(err)
	}
	var activeCount int
	if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM agent.conversations c JOIN agent.runs r ON r.conversation_id=c.id WHERE r.id='run-retention-active'`).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if activeCount != 1 {
		t.Fatal("archived conversation with nonterminal run was pruned")
	}
	var activeMessages int
	if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM agent.messages m JOIN agent.runs r ON r.id=m.run_id WHERE r.id='run-retention-active'`).Scan(&activeMessages); err != nil {
		t.Fatal(err)
	}
	if activeMessages != 2 {
		t.Fatalf("messages for archived conversation with nonterminal run were pruned: %d", activeMessages)
	}
}

type recordingAudit struct {
	mu    sync.Mutex
	count int
	err   error
}

func (a *recordingAudit) RecordAuditIntent(_ context.Context, _ Tx, _ access.AuditIntent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.count++
	return a.err
}

func TestPostgreSQL18AgentAuditAtomicity(t *testing.T) {
	pool, base := agentPostgresTestRepo(t, "audit")
	audit := &recordingAudit{}
	workflow := &recordingWorkflow{}
	repo, err := NewWithOptions(pool, Options{Workflow: workflow, Audit: audit})
	if err != nil {
		t.Fatal(err)
	}
	ctx := agent.WithAuditIntent(t.Context(), access.AuditIntent{Operation: "agent.conversation.create", PrincipalID: "owner", RequestID: uuid.NewString()})
	conversation, err := repo.CreateConversation(ctx, agent.ConversationInput{PrincipalID: "owner", MetadataJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if conversation.ID == "" {
		t.Fatal("conversation ID is empty")
	}
	audit.err = errors.New("audit failed")
	_, err = repo.CreateConversation(ctx, agent.ConversationInput{PrincipalID: "owner", MetadataJSON: `{}`})
	if err == nil {
		t.Fatal("audit failure unexpectedly committed")
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM agent.conversations WHERE principal_id='owner'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("conversation count after audit rollback = %d", count)
	}
	run, err := repo.CreateRun(t.Context(), agent.RunInput{PrincipalID: "owner", ConversationID: conversation.ID, RunID: "run-audit-workflow", Status: agent.RunStatusPreparing, MetadataJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	workflow.err = nil
	if _, err := repo.ActivateRunWorkflow(ctx, "owner", conversation.ID, run.ID, intentForRun(run.ID)); err == nil {
		t.Fatal("workflow+audit failure unexpectedly committed")
	}
	stored, err := base.GetRun(t.Context(), "owner", conversation.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != agent.RunStatusPreparing {
		t.Fatalf("workflow+audit rollback status = %q", stored.Status)
	}
	events, err := base.ListEvents(t.Context(), "owner", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("workflow+audit rollback events = %#v", events)
	}
}

func TestPostgreSQL18AgentLeaseFencing(t *testing.T) {
	pool, base := agentPostgresTestRepo(t, "fence")
	ctx := t.Context()
	conversation, err := base.CreateConversation(ctx, agent.ConversationInput{PrincipalID: "owner", MetadataJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	workflow := &recordingWorkflow{}
	jobsAuthority := &fakeJobsAuthority{job: jobs.Job{ID: "job-1", Kind: "agent.run", ResourceKind: "agent_run", ResourceID: "run-fence", Status: jobs.StatusRunning, LeaseOwner: "worker", LeaseGeneration: 2, LeaseExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339Nano)}}
	repo, err := NewWithOptions(pool, Options{Workflow: workflow, Jobs: jobsAuthority})
	if err != nil {
		t.Fatal(err)
	}
	run, err := repo.CreateRun(ctx, agent.RunInput{PrincipalID: "owner", ConversationID: conversation.ID, RunID: "run-fence", Status: agent.RunStatusRunning, MetadataJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.VerifyRunLease(ctx, run.ID, "job-1", jobs.Fence{Owner: "worker", Generation: 1}); err == nil {
		t.Fatal("stale fence unexpectedly accepted")
	}
	if _, _, err := repo.FinishRunWorkflow(ctx, agent.RunFinish{PrincipalID: "owner", ConversationID: conversation.ID, RunID: run.ID, Status: agent.RunStatusCompleted, MetadataJSON: `{}`, JobID: "job-1", JobFence: jobs.Fence{Owner: "worker", Generation: 1}}, jobs.WorkflowIntent{}); err == nil {
		t.Fatal("stale fenced completion unexpectedly accepted")
	}
}

type fakeJobsAuthority struct{ job jobs.Job }

func (f *fakeJobsAuthority) Get(_ context.Context, _ string) (jobs.Job, error) { return f.job, nil }
func (f *fakeJobsAuthority) GetTx(_ context.Context, _ jobspostgres.Tx, _ string) (jobs.Job, error) {
	return f.job, nil
}
func (f *fakeJobsAuthority) CancelTx(_ context.Context, _ jobspostgres.Tx, _ string) error {
	return nil
}
