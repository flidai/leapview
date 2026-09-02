package postgres

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgreSQL18PoolConformance(t *testing.T) {
	h := postgrestest.Start(t, postgrestest.Required(os.Getenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED")))
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_runtime", Password: "leapview-conformance-secret", Login: true})
	db := h.NewDatabase(t, "leapview_control")
	schema := db.CreateSchema(t, "conformance", runtime)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	url := db.URL(runtime)
	p, err := Open(ctx, Config{
		URL:                    url,
		ExpectedMajor:          18,
		RuntimeRole:            "leapview_runtime",
		Intent:                 IntentReadWrite,
		RequireTLS:             false, // the testcontainer is intentionally local and plaintext
		MinConns:               1,
		MaxConns:               4,
		AcquireTimeout:         2 * time.Second,
		StatementTimeout:       2 * time.Second,
		LockTimeout:            150 * time.Millisecond,
		IdleTransactionTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("open PostgreSQL 18 pool: %v", err)
	}
	defer p.Close()

	t.Run("concurrent connections", func(t *testing.T) {
		const count = 3
		connections := make([]*pgxpool.Conn, 0, count)
		for range count {
			conn, err := p.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			connections = append(connections, conn)
		}
		var wg sync.WaitGroup
		errs := make(chan error, len(connections))
		for _, conn := range connections {
			wg.Add(1)
			go func(conn *pgxpool.Conn) {
				defer wg.Done()
				var one int
				errs <- conn.QueryRow(ctx, "SELECT 1").Scan(&one)
			}(conn)
		}
		wg.Wait()
		for _, conn := range connections {
			conn.Release()
		}
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("repository surface releases leases", func(t *testing.T) {
		var one int
		if err := p.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
			t.Fatalf("QueryRow result=%d err=%v", one, err)
		}
		rows, err := p.Query(ctx, "SELECT value FROM generate_series(1, 3) AS value ORDER BY value")
		if err != nil {
			t.Fatal(err)
		}
		if !rows.Next() {
			t.Fatalf("Query returned no first row: %v", rows.Err())
		}
		if err := rows.Scan(&one); err != nil || one != 1 {
			t.Fatalf("Query first result=%d err=%v", one, err)
		}
		rows.Close()
		if acquired := p.Stats().AcquiredConns(); acquired != 0 {
			t.Fatalf("repository surface retained %d pool leases", acquired)
		}
	})

	t.Run("rollback", func(t *testing.T) {
		name := schema + ".postgres_conformance_rollback"
		if _, err := p.Exec(ctx, "DROP TABLE IF EXISTS "+name); err != nil {
			t.Fatal(err)
		}
		defer p.Exec(context.Background(), "DROP TABLE IF EXISTS "+name)
		if _, err := p.Exec(ctx, "CREATE TABLE "+name+" (id integer primary key)"); err != nil {
			t.Fatal(err)
		}
		tx, err := p.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO "+name+" VALUES (1)"); err != nil {
			t.Fatal(err)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := p.QueryRow(ctx, "SELECT count(*) FROM "+name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("rollback left %d rows, want 0", count)
		}
	})

	t.Run("lock timeout", func(t *testing.T) {
		name := schema + ".postgres_conformance_lock"
		if _, err := p.Exec(ctx, "DROP TABLE IF EXISTS "+name); err != nil {
			t.Fatal(err)
		}
		defer p.Exec(context.Background(), "DROP TABLE IF EXISTS "+name)
		if _, err := p.Exec(ctx, "CREATE TABLE "+name+" (id integer primary key, value text)"); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Exec(ctx, "INSERT INTO "+name+" VALUES (1, 'held')"); err != nil {
			t.Fatal(err)
		}
		holder, err := p.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer holder.Release()
		waiter, err := p.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer waiter.Release()
		holderTx, err := holder.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer holderTx.Rollback(context.Background())
		if _, err := holderTx.Exec(ctx, "SELECT id FROM "+name+" WHERE id = 1 FOR UPDATE"); err != nil {
			t.Fatal(err)
		}
		_, err = waiter.Exec(ctx, "UPDATE "+name+" SET value = 'blocked' WHERE id = 1")
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
			t.Fatalf("lock wait error = %v, want PostgreSQL lock_not_available (55P03)", err)
		}
	})

	t.Run("connection cancellation", func(t *testing.T) {
		conn, err := p.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Release()
		queryCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		_, err = conn.Exec(queryCtx, "SELECT pg_sleep(5)")
		if err == nil {
			t.Fatal("cancellable query unexpectedly completed")
		}
		var pgErr *pgconn.PgError
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) && (!errors.As(err, &pgErr) || pgErr.Code != "57014") {
			t.Fatalf("cancellable query error = %v, want context cancellation", err)
		}
		replacement, err := p.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire replacement connection after cancellation: %v", err)
		}
		defer replacement.Release()
		var one int
		if err := replacement.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
			t.Fatalf("connection unusable after cancellation: %v", err)
		}
	})

	t.Run("deadlock SQLSTATE", func(t *testing.T) {
		name := schema + ".postgres_conformance_deadlock"
		if _, err := p.Exec(ctx, "DROP TABLE IF EXISTS "+name); err != nil {
			t.Fatal(err)
		}
		defer p.Exec(context.Background(), "DROP TABLE IF EXISTS "+name)
		if _, err := p.Exec(ctx, "CREATE TABLE "+name+" (id integer primary key)"); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Exec(ctx, "INSERT INTO "+name+" VALUES (1), (2)"); err != nil {
			t.Fatal(err)
		}
		first, err := p.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer first.Release()
		second, err := p.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer second.Release()
		firstTx, err := first.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer firstTx.Rollback(context.Background())
		secondTx, err := second.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer secondTx.Rollback(context.Background())
		for _, tx := range []interface {
			Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
		}{firstTx, secondTx} {
			if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = 0"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := firstTx.Exec(ctx, "SELECT id FROM "+name+" WHERE id = 1 FOR UPDATE"); err != nil {
			t.Fatal(err)
		}
		if _, err := secondTx.Exec(ctx, "SELECT id FROM "+name+" WHERE id = 2 FOR UPDATE"); err != nil {
			t.Fatal(err)
		}
		type result struct{ err error }
		results := make(chan result, 2)
		go func() {
			_, err := firstTx.Exec(ctx, "SELECT id FROM "+name+" WHERE id = 2 FOR UPDATE")
			results <- result{err: err}
		}()
		go func() {
			_, err := secondTx.Exec(ctx, "SELECT id FROM "+name+" WHERE id = 1 FOR UPDATE")
			results <- result{err: err}
		}()
		var deadlock bool
		for range 2 {
			select {
			case got := <-results:
				var pgErr *pgconn.PgError
				if errors.As(got.err, &pgErr) && pgErr.Code == "40P01" {
					deadlock = true
				}
			case <-time.After(10 * time.Second):
				t.Fatal("deadlock resolution exceeded timeout")
			}
		}
		if !deadlock {
			t.Fatal("two-transaction cycle did not produce PostgreSQL deadlock_detected (40P01)")
		}
	})
}
