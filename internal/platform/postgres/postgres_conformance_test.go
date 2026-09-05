package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgreSQL18PoolConformance(t *testing.T) {
	h := postgrestest.Start(t)
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

	t.Run("commit and constraint SQLSTATE", func(t *testing.T) {
		name := schema + ".postgres_conformance_commit"
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
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := p.QueryRow(ctx, "SELECT count(*) FROM "+name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("committed row count=%d err=%v, want 1", count, err)
		}
		_, err = p.Exec(ctx, "INSERT INTO "+name+" VALUES (1)")
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
			t.Fatalf("duplicate constraint error = %v, want unique_violation (23505)", err)
		}
	})

	t.Run("statement timeout", func(t *testing.T) {
		started := time.Now()
		_, err := p.Exec(ctx, "SELECT pg_sleep(10)")
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "57014" {
			t.Fatalf("statement timeout error = %v, want query_canceled (57014)", err)
		}
		if elapsed := time.Since(started); elapsed < time.Second || elapsed > 5*time.Second {
			t.Fatalf("statement timeout elapsed=%s, want configured 2s bound", elapsed)
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

	t.Run("serializable retry on 40001", func(t *testing.T) {
		name := schema + ".postgres_conformance_serialization"
		if _, err := p.Exec(ctx, "DROP TABLE IF EXISTS "+name); err != nil {
			t.Fatal(err)
		}
		defer p.Exec(context.Background(), "DROP TABLE IF EXISTS "+name)
		if _, err := p.Exec(ctx, "CREATE TABLE "+name+" (id integer primary key, value integer not null)"); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Exec(ctx, "INSERT INTO "+name+" VALUES (1, 0)"); err != nil {
			t.Fatal(err)
		}

		// Read the row in a serializable transaction before the retry attempt's
		// snapshot. The first retry attempt reads the same row, then this
		// transaction commits an update; the attempt's subsequent update must
		// fail with serialization_failure (40001).
		blocker, err := p.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Release()
		blockerTx, err := blocker.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			t.Fatal(err)
		}
		defer blockerTx.Rollback(context.Background())
		var blockerValue int
		if err := blockerTx.QueryRow(ctx, "SELECT value FROM "+name+" WHERE id = 1").Scan(&blockerValue); err != nil {
			t.Fatal(err)
		}

		const maxAttempts = 2
		var attempts int
		var retryObserved bool
		for attempts = 1; attempts <= maxAttempts; attempts++ {
			tx, err := p.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
			if err != nil {
				t.Fatalf("begin serializable attempt %d: %v", attempts, err)
			}
			var current int
			if err := tx.QueryRow(ctx, "SELECT value FROM "+name+" WHERE id = 1").Scan(&current); err != nil {
				_ = tx.Rollback(context.Background())
				t.Fatalf("read serializable attempt %d: %v", attempts, err)
			}
			if attempts == 1 {
				if _, err := blockerTx.Exec(ctx, "UPDATE "+name+" SET value = value + 1 WHERE id = 1"); err != nil {
					_ = tx.Rollback(context.Background())
					t.Fatalf("blocker update: %v", err)
				}
				if err := blockerTx.Commit(ctx); err != nil {
					_ = tx.Rollback(context.Background())
					t.Fatalf("blocker commit: %v", err)
				}
			}
			_, execErr := tx.Exec(ctx, "UPDATE "+name+" SET value = value + 1 WHERE id = 1")
			if execErr == nil {
				execErr = tx.Commit(ctx)
			} else {
				_ = tx.Rollback(context.Background())
			}
			if execErr == nil {
				break
			}
			if !IsTransactionRetryable(execErr) {
				var pgErr *pgconn.PgError
				if errors.As(execErr, &pgErr) {
					t.Fatalf("serializable attempt %d error = %v (SQLSTATE %s), want retryable 40001", attempts, execErr, pgErr.Code)
				}
				t.Fatalf("serializable attempt %d error = %v, want retryable 40001", attempts, execErr)
			}
			var pgErr *pgconn.PgError
			if !errors.As(execErr, &pgErr) || pgErr.Code != SQLStateSerializationFailure {
				t.Fatalf("serializable retry error = %v, want serialization_failure (40001)", execErr)
			}
			retryObserved = true
		}
		if !retryObserved || attempts != 2 {
			t.Fatalf("serializable attempts=%d retryObserved=%t, want one 40001 followed by a bounded retry", attempts, retryObserved)
		}
		var value int
		if err := p.QueryRow(ctx, "SELECT value FROM "+name+" WHERE id = 1").Scan(&value); err != nil {
			t.Fatal(err)
		}
		if value != 2 {
			t.Fatalf("serializable retry value = %d, want 2 committed increments", value)
		}
	})

	t.Run("idle transaction timeout replaces connection", func(t *testing.T) {
		// Keep this probe to one connection so a successful acquire after the
		// server terminates the idle session necessarily observes pool replacement.
		idleCfg := p.PoolConfig()
		idleCfg.MinConns = 1
		idleCfg.MaxConns = 1
		idleCfg.IdleTransactionTimeout = 500 * time.Millisecond
		idlePool, err := Open(ctx, idleCfg)
		if err != nil {
			t.Fatalf("open idle-timeout probe pool: %v", err)
		}
		defer idlePool.Close()
		conn, err := idlePool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		backendPID := conn.Conn().PgConn().PID()
		tx, err := conn.Begin(ctx)
		if err != nil {
			conn.Release()
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, "SELECT 1"); err != nil {
			_ = tx.Rollback(context.Background())
			conn.Release()
			t.Fatalf("initial idle transaction query: %v", err)
		}
		// No statements are issued while sleeping; this leaves the backend idle
		// in its open transaction long enough for PostgreSQL to terminate it.
		time.Sleep(2 * idleCfg.IdleTransactionTimeout)
		_, timeoutErr := tx.Exec(ctx, "SELECT 1")
		if timeoutErr == nil {
			conn.Release()
			t.Fatal("idle transaction remained usable after configured timeout")
		}
		t.Logf("idle transaction timeout error: %v", timeoutErr)
		if !isIdleTransactionTermination(timeoutErr) {
			conn.Release()
			t.Fatalf("idle transaction timeout error = %v, want SQLSTATE 25P03 or PostgreSQL connection termination", timeoutErr)
		}
		conn.Release()
		replacementCtx, replacementCancel := context.WithTimeout(ctx, 5*time.Second)
		defer replacementCancel()
		replacement, err := idlePool.Acquire(replacementCtx)
		if err != nil {
			t.Fatalf("acquire replacement after idle transaction timeout: %v", err)
		}
		defer replacement.Release()
		replacementPID := replacement.Conn().PgConn().PID()
		if replacementPID == backendPID {
			t.Fatalf("idle-timeout connection PID %d was reused, want pool replacement", backendPID)
		}
		var one int
		if err := replacement.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
			t.Fatalf("replacement connection query result=%d err=%v", one, err)
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
				if errors.As(got.err, &pgErr) && pgErr.Code == SQLStateDeadlockDetected {
					if !IsTransactionRetryable(got.err) {
						t.Fatalf("deadlock error = %v was not classified as transaction-retryable", got.err)
					}
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

// PostgreSQL may deliver idle_in_transaction_session_timeout as SQLSTATE
// 25P03, or close the socket before pgx receives the server's FATAL frame.
// Accept both documented forms while rejecting unrelated query failures.
func isIdleTransactionTermination(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "25P03"
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"idle-in-transaction",
		"idle in transaction",
		"terminating connection",
		"server closed the connection",
		"connection reset",
		"connection is closed",
		"closed network connection",
		"broken pipe",
		"unexpected eof",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
