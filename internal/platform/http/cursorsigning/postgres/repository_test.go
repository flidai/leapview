package postgres

import (
	"context"
	"sync"
	"testing"

	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testRepository(t *testing.T) (*Repository, *pgxpool.Pool) {
	t.Helper()
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "cursor_signing_test")
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
	return NewRepository(p), p
}

func TestConfigureCreatesOneDurableKeyAndVerifiesCursor(t *testing.T) {
	r, p := testRepository(t)
	if err := r.Configure(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM platform.api_cursor_signing_keys WHERE active`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("active keys = %d, want 1", count)
	}
	token := cursorsigning.Sign("e1", []byte(`{"cursor":"one"}`))
	if payload, err := cursorsigning.Verify("e1", token); err != nil || string(payload) != `{"cursor":"one"}` {
		t.Fatalf("verify payload=%s err=%v", payload, err)
	}
}

func TestRotateRetainsVerificationKeyAndPublishesOneCurrentKey(t *testing.T) {
	r, p := testRepository(t)
	if err := r.Configure(t.Context()); err != nil {
		t.Fatal(err)
	}
	old := cursorsigning.Sign("e1", []byte(`{"cursor":"old"}`))
	rotated, err := r.Rotate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if rotated == "" {
		t.Fatal("rotation returned empty key id")
	}
	if _, err := cursorsigning.Verify("e1", old); err != nil {
		t.Fatalf("old cursor no longer verifies: %v", err)
	}
	newToken := cursorsigning.Sign("e1", []byte(`{"cursor":"new"}`))
	if _, err := cursorsigning.Verify("e1", newToken); err != nil {
		t.Fatalf("new cursor does not verify: %v", err)
	}
	var count int
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM platform.api_cursor_signing_keys WHERE active`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("active keys after rotation = %d, want 1", count)
	}
}

func TestRetiredKeyRemainsVerifiableWithinBoundedWindow(t *testing.T) {
	r, p := testRepository(t)
	if err := r.Configure(t.Context()); err != nil {
		t.Fatal(err)
	}
	old := cursorsigning.Sign("e1", []byte(`{"cursor":"old"}`))
	// Exercise the bounded active->retired transition. Retired keys remain
	// verifiable until their configured window expires.
	if _, err := p.Exec(t.Context(), `UPDATE platform.api_cursor_signing_keys SET active=false, verify_until=clock_timestamp()+interval '1 hour' WHERE active`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Rotate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.Configure(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := cursorsigning.Verify("e1", old); err != nil {
		t.Fatalf("retired cursor no longer verifies within window: %v", err)
	}
}

func TestConcurrentConfigureConvergesOnOneRing(t *testing.T) {
	r, _ := testRepository(t)
	const n = 8
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- r.Configure(context.Background())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestDirectSQLCursorKeyGuardsAndMetadataView(t *testing.T) {
	r, p := testRepository(t)
	if err := r.Configure(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `
		INSERT INTO platform.api_cursor_signing_keys (key_id, secret, active, created_at, verify_until)
		VALUES ('forged', decode(repeat('01',32),'hex'), false, clock_timestamp(), NULL)`); err == nil {
		t.Fatal("direct retired cursor key insert unexpectedly succeeded")
	}
	var activeID string
	if err := p.QueryRow(t.Context(), `SELECT key_id FROM platform.api_cursor_signing_keys WHERE active`).Scan(&activeID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE platform.api_cursor_signing_keys SET secret=decode(repeat('00',32),'hex') WHERE key_id=$1`, activeID); err == nil {
		t.Fatal("direct cursor secret mutation unexpectedly succeeded")
	}
	if _, err := p.Exec(t.Context(), `UPDATE platform.api_cursor_signing_keys SET active=false, verify_until=clock_timestamp()+interval '25 hours' WHERE key_id=$1`, activeID); err == nil {
		t.Fatal("unbounded cursor verification window unexpectedly succeeded")
	}
	if _, err := p.Exec(t.Context(), `UPDATE platform.api_cursor_signing_keys SET active=false, verify_until=clock_timestamp()+interval '1 hour' WHERE key_id=$1`, activeID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Rotate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE platform.api_cursor_signing_keys SET active=true WHERE key_id=$1`, activeID); err == nil {
		t.Fatal("retired cursor key was reactivated by direct SQL")
	}
	if err := p.QueryRow(t.Context(), `SELECT secret FROM platform.api_cursor_signing_key_metadata LIMIT 1`).Scan(new([]byte)); err == nil {
		t.Fatal("cursor metadata view exposed secret column")
	}
}
