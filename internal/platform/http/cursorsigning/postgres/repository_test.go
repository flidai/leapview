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

func TestPostgreSQL18CursorRetentionRoleBoundary(t *testing.T) {
	h := postgrestest.Start(t)
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Login: true, Password: "runtime-secret"})
	maintenance := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Login: true, Password: "maintenance-secret"})
	database := h.NewDatabase(t, "cursor_signing_retention_roles")
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
	var runtimeExecute bool
	if err := runtimeDB.QueryRow(t.Context(), `SELECT has_function_privilege(current_user, 'platform.prune_expired_cursor_signing_keys(integer)', 'EXECUTE')`).Scan(&runtimeExecute); err != nil {
		t.Fatal(err)
	}
	if runtimeExecute {
		t.Fatal("runtime role has cursor retention EXECUTE privilege")
	}
	if _, err := runtimeDB.Exec(t.Context(), `SELECT platform.prune_expired_cursor_signing_keys(1)`); err == nil {
		t.Fatal("runtime cursor retention unexpectedly succeeded")
	}
	if err := NewRepository(runtimeDB).Configure(t.Context()); err != nil {
		t.Fatalf("runtime cursor configuration: %v", err)
	}

	maintenanceDB, err := pgxpool.New(t.Context(), database.URL(maintenance))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(maintenanceDB.Close)
	var maintenanceExecute bool
	if err := maintenanceDB.QueryRow(t.Context(), `SELECT has_function_privilege(current_user, 'platform.prune_expired_cursor_signing_keys(integer)', 'EXECUTE')`).Scan(&maintenanceExecute); err != nil {
		t.Fatal(err)
	}
	if !maintenanceExecute {
		t.Fatal("maintenance role is missing cursor retention EXECUTE privilege")
	}
	if removed, err := NewMaintenance(maintenanceDB).PruneExpired(t.Context(), 1000); err != nil || removed != 0 {
		t.Fatalf("empty maintenance cursor retention removed=%d err=%v", removed, err)
	}
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

func TestMaintenancePruneExpiredPreservesCurrentAndVerifiableKeys(t *testing.T) {
	r, p := testRepository(t)
	if err := r.Configure(t.Context()); err != nil {
		t.Fatal(err)
	}
	var currentID string
	if err := p.QueryRow(t.Context(), `SELECT key_id FROM platform.api_cursor_signing_keys WHERE active`).Scan(&currentID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Rotate(t.Context()); err != nil {
		t.Fatal(err)
	}
	const expiredCount = 3
	// Seed expired and still-verifiable retired rows under the test admin while
	// leaving the production insert/update guards enabled for normal paths.
	if _, err := p.Exec(t.Context(), `ALTER TABLE platform.api_cursor_signing_keys DISABLE TRIGGER ALL`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < expiredCount; i++ {
		if _, err := p.Exec(t.Context(), `
			INSERT INTO platform.api_cursor_signing_keys (key_id, secret, active, created_at, verify_until)
			VALUES ($1, decode(repeat('01',32),'hex'), false, clock_timestamp(), clock_timestamp()-interval '1 minute')`,
			"expired-"+string(rune('a'+i))); err != nil {
			_ = enableCursorKeyTriggers(p, t)
			t.Fatal(err)
		}
	}
	if _, err := p.Exec(t.Context(), `
		INSERT INTO platform.api_cursor_signing_keys (key_id, secret, active, created_at, verify_until)
		VALUES ('verifiable-retired', decode(repeat('02',32),'hex'), false, clock_timestamp(), clock_timestamp()+interval '1 hour')`); err != nil {
		_ = enableCursorKeyTriggers(p, t)
		t.Fatal(err)
	}
	if err := enableCursorKeyTriggers(p, t); err != nil {
		t.Fatal(err)
	}

	// Runtime configuration only reads verifiable rows; it must not perform
	// destructive retention as a side effect.
	if err := r.Configure(t.Context()); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM platform.api_cursor_signing_keys WHERE key_id LIKE 'expired-%'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != expiredCount {
		t.Fatalf("runtime configuration removed %d expired keys, want %d", expiredCount-before, 0)
	}

	maintenance := NewMaintenance(p)
	removed, err := maintenance.PruneExpired(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("bounded cursor prune removed %d rows, want 1", removed)
	}
	removed, err = maintenance.PruneExpired(t.Context(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if removed != expiredCount-1 {
		t.Fatalf("cursor prune retry removed %d rows, want %d", removed, expiredCount-1)
	}
	if removed, err = maintenance.PruneExpired(t.Context(), 1000); err != nil || removed != 0 {
		t.Fatalf("idempotent cursor prune retry removed=%d err=%v", removed, err)
	}
	var current, expired, verifiable int
	if err := p.QueryRow(t.Context(), `
		SELECT count(*) FILTER (WHERE key_id=$1),
		       count(*) FILTER (WHERE key_id LIKE 'expired-%'),
		       count(*) FILTER (WHERE key_id='verifiable-retired')
		FROM platform.api_cursor_signing_keys`, currentID).Scan(&current, &expired, &verifiable); err != nil {
		t.Fatal(err)
	}
	if current != 1 {
		t.Fatalf("pre-rotation key %q disappeared before verification expiry", currentID)
	}
	var active int
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM platform.api_cursor_signing_keys WHERE active`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 || expired != 0 || verifiable != 1 {
		t.Fatalf("cursor retention state active=%d expired=%d verifiable=%d", active, expired, verifiable)
	}
}

func enableCursorKeyTriggers(p *pgxpool.Pool, t *testing.T) error {
	t.Helper()
	_, err := p.Exec(t.Context(), `ALTER TABLE platform.api_cursor_signing_keys ENABLE TRIGGER ALL`)
	return err
}
