package productaudit

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	product "github.com/flidai/leapview/internal/admin/product"
	productpostgres "github.com/flidai/leapview/internal/admin/product/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

type testBlobs struct{}

func TestNewWithRepositoryPreservesAccessAuditIdentity(t *testing.T) {
	audit := accesspostgres.New()
	adapter := NewWithRepository(audit)
	if !adapter.Matches(audit) {
		t.Fatal("adapter did not retain the supplied Access audit repository")
	}
	if adapter.Matches(accesspostgres.New()) {
		t.Fatal("adapter accepted a distinct Access audit repository")
	}
	var nilAdapter *Adapter
	if nilAdapter.Matches(audit) {
		t.Fatal("nil adapter matched an Access audit repository")
	}
}

func (testBlobs) Put(_ context.Context, blob product.Blob, _ io.Reader) (product.Blob, error) {
	return blob, nil
}
func (testBlobs) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, product.ErrNotFound
}

func TestAdapterBacksNativeProductServiceWithCanonicalAccessAudit(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "")
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
	if err := productpostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	principalID := "10000000-0000-0000-0000-000000000001"
	if _, err := db.Exec(t.Context(), `INSERT INTO access.principal(id, principal_type, status) VALUES ($1::uuid, 'user', 'active')`, principalID); err != nil {
		t.Fatal(err)
	}
	repo, err := productpostgres.NewWithOptions(db, productpostgres.Options{Audit: NewWithRepository(accesspostgres.New())})
	if err != nil {
		t.Fatal(err)
	}
	service, err := product.NewWithStorage(repo, testBlobs{})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := service.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetDisplayName(t.Context(), initial.Revision, "Audited PG", product.Mutation{PrincipalID: principalID, RequestID: "20000000-0000-0000-0000-000000000001", CorrelationID: "30000000-0000-0000-0000-000000000001"}); err != nil {
		t.Fatal(err)
	}
	var action string
	if err := db.QueryRow(t.Context(), `SELECT action FROM audit.audit_event WHERE resource_kind = 'product' AND resource_id = 'instance'`).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if action != "product.identity.updated" {
		t.Fatalf("audit action = %q", action)
	}
	if _, err := access.ParseCapability("RESOURCE_MANAGE"); err != nil {
		t.Fatal(err)
	}
}

func TestAdapterCanonicalAuditReplayAndConflict(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "")
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
	principalID := "10000000-0000-0000-0000-000000000011"
	if _, err := db.Exec(t.Context(), `INSERT INTO access.principal(id, principal_type, status) VALUES ($1::uuid, 'user', 'active')`, principalID); err != nil {
		t.Fatal(err)
	}
	adapter := NewWithRepository(accesspostgres.New())
	input := productpostgres.AuditInput{EventID: "20000000-0000-0000-0000-000000000011", PrincipalID: principalID, Source: "admin.product", Operation: "product.identity.updated", Action: "product.identity.updated", ResourceKind: "product", ResourceID: "instance", Capability: "RESOURCE_MANAGE", Outcome: "success", RequestID: "30000000-0000-0000-0000-000000000011", CorrelationID: "40000000-0000-0000-0000-000000000011", AggregateKey: "product:instance", AggregateSequence: 1, MetadataJSON: `{"fields":["displayName"]}`}
	for i := 0; i < 2; i++ {
		tx, err := db.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err := adapter.RecordAuditEvent(t.Context(), tx, input); err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatalf("replay %d: %v", i, err)
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	changed := input
	changed.Action = "product.identity.reset"
	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.RecordAuditEvent(t.Context(), tx, changed); err == nil {
		_ = tx.Rollback(t.Context())
		t.Fatal("changed replay unexpectedly succeeded")
	} else if !errors.Is(err, access.ErrAuditIntentConflict) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("changed replay error = %v, want conflict", err)
	}
	_ = tx.Rollback(t.Context())
}
