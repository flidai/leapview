package deploymentaudit

import (
	"context"
	"errors"
	"testing"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAppendMutationAuditFailsClosed(t *testing.T) {
	var adapter *Adapter
	if _, err := adapter.AppendMutationAudit(context.Background(), nil, deploymentmodule.NativeDeliveryAuditInput{}); !errors.Is(err, deploymentpostgres.ErrInvalid) {
		t.Fatalf("nil adapter error = %v, want deployment.ErrInvalid", err)
	}
	if _, err := New().AppendMutationAudit(context.Background(), nil, deploymentmodule.NativeDeliveryAuditInput{}); !errors.Is(err, deploymentpostgres.ErrInvalid) {
		t.Fatalf("nil transaction error = %v, want deployment.ErrInvalid", err)
	}
}

func TestAppendMutationAuditUsesCallerTransactionAndReplay(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "deployment_audit_adapter")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Exec(t.Context(), accesspostgres.SchemaSQL()); err != nil {
		t.Fatal(err)
	}
	adapter := New()
	input := deploymentmodule.NativeDeliveryAuditInput{
		AuditID: "01900000-0000-7000-8000-000000000201", DomainEventID: "01900000-0000-7000-8000-000000000202",
		ScopeID: "target", ActorID: "operator", Action: "publication_created",
		ResourceKind: "publication", ResourceID: "publication-1", Outcome: "accepted",
		RequestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CorrelationID: "01900000-0000-7000-8000-000000000203", AggregateKey: "publication-1",
		AggregateSequence: 1, Metadata: []byte(`{"generation_id":"generation-1"}`),
	}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stored, err := adapter.AppendMutationAudit(t.Context(), tx, input)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if stored.AuditID != input.AuditID || stored.EventID != input.DomainEventID || stored.ScopeID != input.ScopeID || stored.ActorID != input.ActorID || stored.Action != input.Action || stored.ResourceKind != input.ResourceKind || stored.ResourceID != input.ResourceID || stored.Outcome != input.Outcome || stored.RequestDigest != input.RequestDigest {
		t.Fatalf("stored audit projection = %+v", stored)
	}
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
	if _, err := adapter.AppendMutationAudit(t.Context(), tx, input); err != nil {
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
	if _, err := adapter.AppendMutationAudit(t.Context(), tx, input); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("exact replay = %v", err)
	}
	read, err := adapter.GetMutationAudit(t.Context(), tx, input)
	if err != nil || read.AuditID != input.AuditID || read.RequestDigest != input.RequestDigest {
		_ = tx.Rollback(t.Context())
		t.Fatalf("durable audit read = %+v, %v", read, err)
	}
	changed := input
	changed.Action = "publication_cancelled"
	if _, err := adapter.GetMutationAudit(t.Context(), tx, changed); !errors.Is(err, deploymentpostgres.ErrConflict) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("conflicting read error = %v, want deployment.ErrConflict", err)
	}
	if _, err := adapter.AppendMutationAudit(t.Context(), tx, changed); !errors.Is(err, deploymentpostgres.ErrConflict) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("conflicting replay error = %v, want deployment.ErrConflict", err)
	}
	_ = tx.Rollback(t.Context())
}
