package app

import (
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/agent"
	agentpostgres "github.com/flidai/leapview/internal/agent/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAgentPostgresPersistenceComposesOneTransactionalBoundary(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "agent_composition")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := accesspostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	for name, schema := range map[string]string{
		"events": eventspostgres.SchemaSQL(), "jobs": jobspostgres.SchemaSQL(), "agent": agentpostgres.SchemaSQL(),
	} {
		if _, err := tx.Exec(t.Context(), schema); err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatalf("apply %s schema: %v", name, err)
		}
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	jobsAuthority := jobspostgres.New(db)
	persistence, err := NewAgentPostgresPersistence(db, AgentPostgresAuthorities{
		Access: accesspostgres.New(), Events: eventspostgres.New(), Jobs: jobsAuthority,
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := persistence.Repository
	conversation, err := repo.CreateConversation(t.Context(), agent.ConversationInput{PrincipalID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := repo.CreateRun(t.Context(), agent.RunInput{PrincipalID: "owner", ConversationID: conversation.ID, RunID: "run-composed", Status: agent.RunStatusPreparing})
	if err != nil {
		t.Fatal(err)
	}
	intent := access.AuditIntent{
		EventID: "01900000-0000-7000-8000-000000000031", Source: "agent",
		Operation: "agent.run.activate", Action: "agent.run.activated", Outcome: "success",
		MetadataJSON: `{"schemaVersion":1,"retention":"security","payloadSchema":"AgentRunAuditPayload","payload":{"resourceKind":"agent_run","resourceId":"run-composed"}}`,
	}
	workflow := jobs.WorkflowIntent{
		Event: jobs.EventInput{Key: "agent_run.queued:" + run.ID, ResourceKind: "agent_run", ResourceID: run.ID, EventType: "agent_run.queued", Data: []byte(`{"status":"queued"}`)},
		Job:   jobs.EnqueueInput{ID: "job-composed", Kind: "agent.run", WorkloadClass: "background", PrincipalID: "owner", PartitionKey: "agent:owner", ResourceKind: "agent_run", ResourceID: run.ID, EstimatedMemoryBytes: 1, Payload: []byte(`{"runId":"run-composed"}`)},
	}
	acquiresBefore := db.Stat().AcquireCount()
	if _, err := repo.(agent.RunWorkflowAuditUnitOfWork).ActivateRunWorkflowWithAudit(agent.WithAuditIntent(t.Context(), intent), "owner", conversation.ID, run.ID, workflow, &intent); err != nil {
		t.Fatal(err)
	}
	if got := db.Stat().AcquireCount() - acquiresBefore; got != 1 {
		t.Fatalf("control pool acquisitions for one workflow = %d, want 1", got)
	}
	var agentEvents, domainEvents, audits, jobEvents, jobsCount int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM agent.events WHERE run_id = $1`, run.ID).Scan(&agentEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM event.event_log WHERE aggregate_id = $1`, run.ID).Scan(&domainEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE audit_id = $1::uuid`, intent.EventID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM jobs.event WHERE resource_id = $1`, run.ID).Scan(&jobEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM jobs.job WHERE id = 'job-composed'`).Scan(&jobsCount); err != nil {
		t.Fatal(err)
	}
	if agentEvents != 1 || domainEvents != 2 || audits != 1 || jobEvents != 1 || jobsCount != 1 {
		t.Fatalf("atomic workflow rows agent=%d domain=%d audit=%d job_events=%d jobs=%d", agentEvents, domainEvents, audits, jobEvents, jobsCount)
	}
	var auditDomainID, eventID string
	if err := db.QueryRow(t.Context(), `SELECT event_id::text FROM audit.audit_event WHERE audit_id = $1::uuid`, intent.EventID).Scan(&auditDomainID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT event_id::text FROM event.event_log WHERE aggregate_id = $1 AND event_type = 'agent.run.activated'`, run.ID).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	if auditDomainID != eventID {
		t.Fatalf("audit domain event = %q, want source event %q", auditDomainID, eventID)
	}
	for label, value := range map[string]string{"domain event": eventID, "audit event": auditDomainID} {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed.Version() != 7 {
			t.Fatalf("%s identity = %q, want UUIDv7: %v", label, value, err)
		}
	}
	var versions []int64
	rows, err := db.Query(t.Context(), `SELECT aggregate_version FROM event.event_log WHERE aggregate_id = $1 ORDER BY aggregate_version`, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		versions = append(versions, version)
	}
	rows.Close()
	if len(versions) != 2 || versions[0] != 1 || versions[1] != 2 {
		t.Fatalf("aggregate versions = %v, want [1 2]", versions)
	}

	// A changed replay identity reaches the audit adapter only after the agent
	// mutation, domain event, and jobs consequence have been written. The
	// conflict must roll back every one of those writes.
	run2, err := repo.CreateRun(t.Context(), agent.RunInput{PrincipalID: "owner", ConversationID: conversation.ID, RunID: "run-rollback", Status: agent.RunStatusPreparing})
	if err != nil {
		t.Fatal(err)
	}
	conflicting := intent
	conflicting.Action = "agent.run.replayed-with-different-action"
	workflow2 := workflow
	workflow2.Event.Key = "agent_run.queued:" + run2.ID
	workflow2.Event.ResourceID = run2.ID
	workflow2.Job.ID = "job-rollback"
	workflow2.Job.ResourceID = run2.ID
	if _, err := repo.(agent.RunWorkflowAuditUnitOfWork).ActivateRunWorkflowWithAudit(agent.WithAuditIntent(t.Context(), conflicting), "owner", conversation.ID, run2.ID, workflow2, &conflicting); !errors.Is(err, access.ErrAuditIntentConflict) {
		t.Fatalf("conflicting workflow error = %v, want audit conflict", err)
	}
	var status string
	if err := db.QueryRow(t.Context(), `SELECT status FROM agent.runs WHERE id = $1`, run2.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != agent.RunStatusPreparing {
		t.Fatalf("rolled-back run status = %q, want preparing", status)
	}
	rollbackChecks := []struct {
		query string
		args  []any
		want  int
	}{
		{`SELECT count(*) FROM agent.events WHERE run_id = $1`, []any{run2.ID}, 0},
		{`SELECT count(*) FROM event.event_log WHERE aggregate_id = $1`, []any{run2.ID}, 1},
		{`SELECT count(*) FROM jobs.event WHERE resource_id = $1`, []any{run2.ID}, 0},
		{`SELECT count(*) FROM jobs.job WHERE id = 'job-rollback'`, nil, 0},
		{`SELECT count(*) FROM audit.audit_event WHERE audit_id = $1::uuid`, []any{intent.EventID}, 1},
	}
	for _, check := range rollbackChecks {
		var count int
		if err := db.QueryRow(t.Context(), check.query, check.args...).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != check.want {
			t.Fatalf("rollback query %q count = %d, want %d", check.query, count, check.want)
		}
	}
}
