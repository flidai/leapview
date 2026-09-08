package savedexplorationaudit

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAdapterRetainsCanonicalAuditAuthority(t *testing.T) {
	audit := accesspostgres.New()
	adapter := NewWithRepository(audit)
	if !adapter.Matches(audit) {
		t.Fatal("adapter does not retain the exact Access audit authority")
	}
	if adapter.Matches(accesspostgres.New()) {
		t.Fatal("adapter matched a sibling Access audit authority")
	}
}

func TestAdapterFailsClosedWithoutAuthorityOrTransaction(t *testing.T) {
	var nilAdapter *Adapter
	if err := nilAdapter.RecordAuditEvent(context.Background(), nil, access.AuditIntent{}); err == nil {
		t.Fatal("nil adapter unexpectedly accepted audit event")
	}
	adapter := NewWithRepository(accesspostgres.New())
	if err := adapter.RecordAuditEvent(context.Background(), nil, access.AuditIntent{}); err == nil {
		t.Fatal("nil transaction unexpectedly accepted audit event")
	}
}

func TestAdapterUsesCallerTransactionForPostgreSQLAudit(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "saved_exploration_audit_adapter_test")
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

	const eventID = "70000000-0000-7000-8000-000000000001"
	audit := accesspostgres.New()
	adapter := NewWithRepository(audit)
	intent := access.AuditIntent{
		EventID: eventID, Source: "analytics.exploration.saved", Operation: "createSavedExploration",
		PrincipalID: "10000000-0000-0000-0000-000000000001", Action: "saved_exploration.created",
		ResourceKind: "saved_exploration", ResourceID: "exploration-1", Capability: access.CapabilityResourceEdit,
		Outcome: "success", RequestID: "req_70000000000000000000000000000001", CorrelationID: "corr_70000000000000000000000000000001",
		AggregateKey: "saved_exploration:project:saved:exploration-1", AggregateSequence: 1, MetadataJSON: `{"schemaVersion":1}`,
	}

	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.RecordAuditEvent(t.Context(), tx, intent); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE audit_id=$1::uuid`, eventID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled-back audit rows = %d, want 0", count)
	}

	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.RecordAuditEvent(t.Context(), tx, intent); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE audit_id=$1::uuid`, eventID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("committed audit rows = %d, want 1", count)
	}
}
