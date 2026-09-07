package agentevents

import (
	"context"
	"errors"
	"testing"

	agentpostgres "github.com/flidai/leapview/internal/agent/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAppendDomainEventFailsClosedWithoutAdapterOrIdentity(t *testing.T) {
	input := agentpostgres.DomainEventInput{
		EventID: "not-a-uuid", ScopeID: "scope", AggregateType: "agent_conversation",
		AggregateID: "conversation-1", EventType: "agent.conversation.created",
		SchemaVersion: 1, CorrelationID: "01900000-0000-7000-8000-000000000002", Payload: []byte(`{}`),
	}
	if _, err := NewWithRepository(eventspostgres.New()).AppendDomainEvent(context.Background(), nil, input); err == nil {
		t.Fatal("invalid source event identity unexpectedly accepted")
	}
	var adapter *Adapter
	input.EventID = "01900000-0000-7000-8000-000000000001"
	if _, err := adapter.AppendDomainEvent(context.Background(), nil, input); err == nil {
		t.Fatal("nil adapter unexpectedly accepted")
	}
}

func TestAppendDomainEventUsesCallerTransactionAndValidatesReplay(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "agent_event_adapter")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Exec(t.Context(), eventspostgres.SchemaSQL()); err != nil {
		t.Fatal(err)
	}
	adapter := NewWithRepository(eventspostgres.New())
	input := agentpostgres.DomainEventInput{
		EventID: "01900000-0000-7000-8000-000000000011", ScopeID: "scope",
		AggregateType: "agent_conversation", AggregateID: "conversation-1",
		EventType: "agent.conversation.created", SchemaVersion: 1,
		CorrelationID: "01900000-0000-7000-8000-000000000012", Payload: []byte(`{"status":"active"}`),
	}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stored, err := adapter.AppendDomainEvent(t.Context(), tx, input)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if stored.EventID != input.EventID || stored.AggregateVersion != 1 {
		t.Fatalf("stored event = %+v", stored)
	}
	// The adapter must leave transaction ownership with the caller. A second
	// statement on tx succeeds, and rollback removes both the source marker and
	// event row.
	if _, err := tx.Exec(t.Context(), `CREATE TEMP TABLE source_marker (id integer)`); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count, maxVersion int64
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM event.event_log`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled-back event rows = %d, want 0", count)
	}

	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.AppendDomainEvent(t.Context(), tx, input); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	changed := input
	changed.Payload = []byte(`{"status":"changed"}`)
	if _, err := adapter.AppendDomainEvent(t.Context(), tx, changed); !errors.Is(err, jobs.ErrConflict) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("conflicting replay error = %v, want jobs.ErrConflict", err)
	}
	_ = tx.Rollback(t.Context())
	if err := db.QueryRow(t.Context(), `SELECT count(*), max(aggregate_version) FROM event.event_log`).Scan(&count, &maxVersion); err != nil {
		t.Fatal(err)
	}
	if count != 1 || maxVersion != 1 {
		t.Fatalf("event rows/version after replay conflict = %d/%d, want 1/1", count, maxVersion)
	}
}
