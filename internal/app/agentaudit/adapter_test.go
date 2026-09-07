package agentaudit

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRecordAuditIntentFailsClosedWithoutTransaction(t *testing.T) {
	if err := NewWithRepository(accesspostgres.New()).RecordAuditIntent(context.Background(), nil, access.AuditIntent{}); err == nil {
		t.Fatal("audit adapter accepted nil transaction")
	}
}

func TestNilAuditAdapterFailsClosed(t *testing.T) {
	var adapter *Adapter
	if err := adapter.RecordAuditIntent(context.Background(), nil, access.AuditIntent{}); err == nil {
		t.Fatal("nil audit adapter unexpectedly accepted")
	}
}

func TestRecordAuditIntentUsesCallerTransactionAndValidatesReplay(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "agent_audit_adapter")
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
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	adapter := NewWithRepository(accesspostgres.New())
	intent := access.AuditIntent{
		EventID: "01900000-0000-7000-8000-000000000021", ScopeID: "scope",
		Source: "agent", Operation: "agent.conversation.create", Action: "agent.conversation.created",
		ResourceKind: "agent_conversation", ResourceID: "conversation-1", Outcome: "success",
		AggregateKey: "agent_conversation:conversation-1", AggregateSequence: 1, MetadataJSON: `{}`,
	}
	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.RecordAuditIntent(t.Context(), tx, intent); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	// A caller-owned transaction remains usable after the adapter returns.
	if _, err := tx.Exec(t.Context(), `CREATE TEMP TABLE source_marker (id integer)`); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled-back audit rows = %d, want 0", count)
	}
	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.RecordAuditIntent(t.Context(), tx, intent); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Access's authority is idempotent for the exact immutable identity.
	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.RecordAuditIntent(t.Context(), tx, intent); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("exact replay: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	changed := intent
	changed.Action = "agent.conversation.archived"
	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.RecordAuditIntent(t.Context(), tx, changed); !errors.Is(err, access.ErrAuditIntentConflict) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("conflicting replay error = %v, want access.ErrAuditIntentConflict", err)
	}
	_ = tx.Rollback(t.Context())
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("audit rows after replay conflict = %d, want 1", count)
	}
}
