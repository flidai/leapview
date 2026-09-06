package migrations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	apptesting "github.com/flidai/leapview/internal/app/testing"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type observedMigrationTx struct {
	pgx.Tx
	applied int
	fail    bool
}

func (tx *observedMigrationTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	for _, m := range ordered() {
		if sql == m.sql {
			tx.applied++
		}
	}
	if tx.fail && sql == contractPublicationSQL {
		// A real server-side SQL failure, after DDL and revision writes for 001/002.
		return tx.Tx.Exec(ctx, `SELECT 1 / 0`)
	}
	return tx.Tx.Exec(ctx, sql, args...)
}

func TestMigrationSupersessionPostgreSQL18(t *testing.T) {
	h := postgrestest.Start(t, apptesting.PostgresConformanceRequired())
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator"})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime"})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly"})
	h.GrantRole(t, owner, migrator)
	databaseNumber := 0
	newDB := func(t *testing.T) *pgxpool.Pool {
		t.Helper()
		databaseNumber++
		database := h.NewDatabase(t, fmt.Sprintf("supersession_%d", databaseNumber))
		h.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")
		cfg, err := pgxpool.ParseConfig(database.AdminURL())
		if err != nil {
			t.Fatal(err)
		}
		cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
			_, err := c.Exec(ctx, `SET ROLE leapview_control_migrator`)
			return err
		}
		db, err := pgxpool.NewWithConfig(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(db.Close)
		return db
	}
	begin := func(t *testing.T, db *pgxpool.Pool) *observedMigrationTx {
		t.Helper()
		tx, err := db.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
		return &observedMigrationTx{Tx: tx}
	}
	installPrefix := func(t *testing.T, db *pgxpool.Pool, n int, original bool) {
		t.Helper()
		tx := begin(t, db)
		for _, m := range ordered()[:n] {
			if _, err := tx.Exec(t.Context(), m.sql); err != nil {
				t.Fatal(err)
			}
			if original && m.revision == 2 {
				// SYNTHETIC compatibility fixture: corrected schema with a claimed
				// historical tuple. This is not evidence the invalid SQL ever ran.
				m.id = IdentityLedgerMigrationID
				m.checksum = IdentityLedgerChecksum()
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO platform.schema_revision(revision,migration_id,checksum) VALUES($1,$2,$3)`, m.revision, m.id, m.checksum); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	for _, lineage := range []string{"fresh", "baseline", "replacement", "original"} {
		t.Run(lineage, func(t *testing.T) {
			db := newDB(t)
			prefix := 0
			switch lineage {
			case "baseline":
				prefix = 1
			case "replacement", "original":
				prefix = 2
			}
			if prefix > 0 {
				installPrefix(t, db, prefix, lineage == "original")
			}
			tx := begin(t, db)
			if err := Apply(t.Context(), tx); err != nil {
				t.Fatal(err)
			}
			if tx.applied != len(ordered())-prefix {
				t.Fatalf("applied %d", tx.applied)
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := Verify(t.Context(), db); err != nil {
				t.Fatal(err)
			}
			runtimeTx := begin(t, db)
			if _, err := runtimeTx.Exec(t.Context(), `SET LOCAL ROLE leapview_control_runtime`); err != nil {
				t.Fatal(err)
			}
			if err := Verify(t.Context(), runtimeTx); err != nil {
				t.Fatalf("runtime verification: %v", err)
			}
			_ = runtimeTx.Rollback(t.Context())
			var before, after string
			query := `SELECT jsonb_agg(to_jsonb(r) ORDER BY revision)::text FROM platform.schema_revision r`
			if err := db.QueryRow(t.Context(), query).Scan(&before); err != nil {
				t.Fatal(err)
			}
			tx = begin(t, db)
			if err := Apply(t.Context(), tx); err != nil {
				t.Fatal(err)
			}
			if tx.applied != 0 {
				t.Fatal("replay executed SQL")
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(t.Context(), query).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatal("replay mutated revision evidence")
			}
			if lineage == "original" && !strings.Contains(after, IdentityLedgerChecksum()) {
				t.Fatal("original evidence lost")
			}
			for _, sql := range []string{`UPDATE platform.schema_revision SET checksum=checksum WHERE revision=2`, `DELETE FROM platform.schema_revision WHERE revision=2`, `TRUNCATE platform.schema_revision`} {
				if _, err := db.Exec(t.Context(), sql); err == nil {
					t.Fatalf("ledger mutation accepted: %s", sql)
				}
			}
		})
	}
	t.Run("invalid-history", func(t *testing.T) {
		for _, kind := range []string{"checksum", "id", "gap", "future"} {
			t.Run(kind, func(t *testing.T) {
				db := newDB(t)
				installPrefix(t, db, 1, false)
				revision := int64(2)
				id := IdentityLedgerMigrationID
				checksum := IdentityLedgerChecksum()
				switch kind {
				case "checksum":
					checksum = strings.Repeat("0", 64)
				case "id":
					id = "unknown"
				case "gap":
					revision = 3
				case "future":
					revision = 99
				}
				if _, err := db.Exec(t.Context(), `INSERT INTO platform.schema_revision(revision,migration_id,checksum) VALUES($1,$2,$3)`, revision, id, checksum); err != nil {
					t.Fatal(err)
				}
				tx := begin(t, db)
				if Apply(t.Context(), tx) == nil {
					t.Fatal("invalid history accepted")
				}
				if tx.applied != 0 {
					t.Fatal("DDL executed before validation")
				}
				_ = tx.Rollback(t.Context())
				if Verify(t.Context(), db) == nil {
					t.Fatal("invalid history verified")
				}
			})
		}
	})
	t.Run("failure-rollback-retry", func(t *testing.T) {
		db := newDB(t)
		tx := begin(t, db)
		tx.fail = true
		if Apply(t.Context(), tx) == nil {
			t.Fatal("failure not propagated")
		}
		_ = tx.Rollback(t.Context())
		var absent bool
		if err := db.QueryRow(t.Context(), `SELECT to_regclass('platform.schema_revision') IS NULL AND to_regclass('project.resource_identity') IS NULL`).Scan(&absent); err != nil || !absent {
			t.Fatalf("DDL or revision records survived: %v", err)
		}
		tx = begin(t, db)
		if err := Apply(t.Context(), tx); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("schema-precondition", func(t *testing.T) {
		db := newDB(t)
		installPrefix(t, db, 12, true)
		if _, err := db.Exec(t.Context(), `ALTER TABLE project.resource_identity DISABLE TRIGGER resource_identity_no_delete`); err != nil {
			t.Fatal(err)
		}
		tx := begin(t, db)
		if err := Apply(t.Context(), tx); err == nil || !strings.Contains(err.Error(), "guard missing") {
			t.Fatalf("drift accepted: %v", err)
		}
		_ = tx.Rollback(t.Context())
		if _, err := db.Exec(t.Context(), `ALTER TABLE project.resource_identity ENABLE TRIGGER resource_identity_no_delete`); err != nil {
			t.Fatal(err)
		}
		tx = begin(t, db)
		if err := Apply(t.Context(), tx); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("untracked-database", func(t *testing.T) {
		for _, fixture := range []string{"schema", "table", "empty-ledger"} {
			t.Run(fixture, func(t *testing.T) {
				db := newDB(t)
				var sql string
				switch fixture {
				case "schema":
					sql = `CREATE SCHEMA project`
				case "table":
					sql = `RESET ROLE; CREATE TABLE public.untracked(id bigint); SET ROLE leapview_control_migrator`
				case "empty-ledger":
					sql = BaselineSQL()
				}
				if _, err := db.Exec(t.Context(), sql); err != nil {
					t.Fatal(err)
				}
				tx := begin(t, db)
				if Apply(t.Context(), tx) == nil {
					t.Fatal("untracked database adopted")
				}
				if tx.applied != 0 {
					t.Fatal("migration SQL executed")
				}
			})
		}
	})
	t.Run("unsupported-isolation", func(t *testing.T) {
		db := newDB(t)
		tx, err := db.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(t.Context())
		if err := Apply(t.Context(), tx); err == nil || !strings.Contains(err.Error(), "READ COMMITTED") {
			t.Fatalf("unsupported isolation: %v", err)
		}
	})
	t.Run("excess-effective-privilege", func(t *testing.T) {
		db := newDB(t)
		installPrefix(t, db, 12, false)
		if _, err := db.Exec(t.Context(), `GRANT TRUNCATE ON project.resource_identity TO leapview_control_runtime`); err != nil {
			t.Fatal(err)
		}
		tx := begin(t, db)
		if err := Apply(t.Context(), tx); err == nil || !strings.Contains(err.Error(), "unexpected effective runtime privilege") {
			t.Fatalf("unsafe privilege accepted: %v", err)
		}
		_ = tx.Rollback(t.Context())
		if _, err := db.Exec(t.Context(), `REVOKE TRUNCATE ON project.resource_identity FROM leapview_control_runtime`); err != nil {
			t.Fatal(err)
		}
		tx = begin(t, db)
		if err := Apply(t.Context(), tx); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("concurrent-migrators", func(t *testing.T) {
		db := newDB(t)
		first := begin(t, db)
		if err := Apply(t.Context(), first); err != nil {
			t.Fatal(err)
		}
		// Same lock key in another database must not block independent migration.
		otherDB := newDB(t)
		other := begin(t, otherDB)
		otherCtx, otherCancel := context.WithTimeout(t.Context(), 5*time.Second)
		if err := Apply(otherCtx, other); err != nil {
			otherCancel()
			t.Fatalf("database-scoped lock: %v", err)
		}
		otherCancel()
		if err := other.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		second := begin(t, db)
		// A bounded acquisition proves the first transaction retains its lock.
		if _, err := second.Exec(t.Context(), `SET LOCAL lock_timeout='100ms'`); err != nil {
			t.Fatal(err)
		}
		var pgErr *pgconn.PgError
		if err := Apply(t.Context(), second); !errors.As(err, &pgErr) || pgErr.Code != "55P03" || !strings.Contains(err.Error(), "migration lock") {
			t.Fatalf("lock failure not explicit: %v", err)
		}
		_ = second.Rollback(t.Context())
		second = begin(t, db)
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		result := make(chan error, 1)
		go func() { result <- Apply(ctx, second) }()
		// Observe PostgreSQL lock waiting, not scheduling/timing assumptions.
		var pid int
		if err := first.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		deadline := time.After(5 * time.Second)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		waiting := false
		for !waiting {
			select {
			case err := <-result:
				t.Fatalf("second migrator did not wait: %v", err)
			case <-deadline:
				t.Fatal("no waiting migrator observed")
			case <-ticker.C:
				if err := db.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND pid<>$1)`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := first.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		if first.applied != len(ordered()) || second.applied != 0 {
			t.Fatalf("applications %d/%d", first.applied, second.applied)
		}
		if err := second.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := Verify(t.Context(), db); err != nil {
			t.Fatal(err)
		}
	})
}
