package postgres

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	product "github.com/flidai/leapview/internal/admin/product"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type noopAudit struct{}

func (noopAudit) RecordAuditEvent(context.Context, pgx.Tx, AuditInput) error { return nil }

type noopBlobs struct{}

func (noopBlobs) Put(_ context.Context, blob product.Blob, _ io.Reader) (product.Blob, error) {
	return blob, nil
}
func (noopBlobs) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, product.ErrNotFound
}

func productTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "")
	p, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProductIdentityCASReplayAndConcurrentWriters(t *testing.T) {
	db := productTestDB(t)
	r, err := NewWithOptions(db, Options{Audit: noopAudit{}})
	if err != nil {
		t.Fatal(err)
	}
	service, err := product.NewWithStorage(r, noopBlobs{})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := service.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if initial.DisplayName != product.DefaultDisplayName || initial.Revision != 1 {
		t.Fatalf("initial identity = %#v", initial)
	}
	updated, err := service.SetDisplayName(t.Context(), initial.Revision, "Acme Analytics", product.Mutation{})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.DisplayName != "Acme Analytics" {
		t.Fatalf("updated identity = %#v", updated)
	}
	if _, err := service.SetDisplayName(t.Context(), initial.Revision, "Stale", product.Mutation{}); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("stale CAS = %v, want ErrPrecondition", err)
	}
	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.SetDisplayName(t.Context(), updated.Revision, "Concurrent", product.Mutation{})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	var successes int
	for err := range errs {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrPrecondition) {
			t.Fatalf("concurrent CAS = %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent CAS successes = %d, want 1", successes)
	}
}

func TestProductIdentityTamperAndRollback(t *testing.T) {
	db := productTestDB(t)
	r, err := NewWithOptions(db, Options{Audit: noopAudit{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE admin.product_identity SET display_name = 'tampered'`); err == nil {
		t.Fatal("tampered product update unexpectedly succeeded")
	}
	initial, err := r.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	failingRepo, err := NewWithOptions(db, Options{Audit: failingAudit{}})
	if err != nil {
		t.Fatal(err)
	}
	service, err := product.NewWithStorage(failingRepo, noopBlobs{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetDisplayName(t.Context(), initial.Revision, "Rolled back", product.Mutation{}); err == nil {
		t.Fatal("mutation succeeded despite audit failure")
	}
	got, err := r.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != initial.DisplayName || got.Revision != initial.Revision {
		t.Fatalf("rollback leaked identity = %#v", got)
	}
}

type failingAudit struct{}

func (failingAudit) RecordAuditEvent(context.Context, pgx.Tx, AuditInput) error {
	return errors.New("audit failure")
}
