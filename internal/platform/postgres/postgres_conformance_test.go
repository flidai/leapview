package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/log"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const postgres18ConformanceImage = "docker.io/library/postgres:18-alpine@sha256:63bdc97d67b5133bf0e5ebd500bec6d046fa851dc81340d838f0347e616107e8"

func TestPostgreSQL18PoolConformance(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()

	if !postgresConformanceRequired() {
		testcontainers.SkipIfProviderIsNotHealthy(t)
	}
	container, err := tcpostgres.Run(ctx, postgres18ConformanceImage,
		tcpostgres.WithDatabase("leapview_control"),
		tcpostgres.WithUsername("leapview_runtime"),
		tcpostgres.WithPassword("leapview-conformance-secret"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90*time.Second)),
		testcontainers.WithLogger(log.TestLogger(t)),
	)
	if err != nil {
		if postgresConformanceRequired() {
			t.Fatalf("required PostgreSQL conformance container: %v", err)
		}
		t.Skipf("PostgreSQL conformance container unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, container)

	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
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

	t.Run("rollback", func(t *testing.T) {
		name := "postgres_conformance_rollback"
		if _, err := p.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", name)); err != nil {
			t.Fatal(err)
		}
		defer p.Exec(context.Background(), fmt.Sprintf("DROP TABLE IF EXISTS %s", name))
		if _, err := p.Exec(ctx, fmt.Sprintf("CREATE TABLE %s (id integer primary key)", name)); err != nil {
			t.Fatal(err)
		}
		if err := p.AcquireFunc(ctx, func(conn *pgxpool.Conn) error {
			tx, err := conn.Begin(ctx)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, fmt.Sprintf("INSERT INTO %s VALUES (1)", name)); err != nil {
				return err
			}
			if err := tx.Rollback(ctx); err != nil {
				return err
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := p.AcquireFunc(ctx, func(conn *pgxpool.Conn) error {
			return conn.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", name)).Scan(&count)
		}); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("rollback left %d rows, want 0", count)
		}
	})

	t.Run("lock timeout", func(t *testing.T) {
		name := "postgres_conformance_lock"
		if _, err := p.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", name)); err != nil {
			t.Fatal(err)
		}
		defer p.Exec(context.Background(), fmt.Sprintf("DROP TABLE IF EXISTS %s", name))
		if _, err := p.Exec(ctx, fmt.Sprintf("CREATE TABLE %s (id integer primary key, value text)", name)); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Exec(ctx, fmt.Sprintf("INSERT INTO %s VALUES (1, 'held')", name)); err != nil {
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
		if _, err := holderTx.Exec(ctx, fmt.Sprintf("SELECT id FROM %s WHERE id = 1 FOR UPDATE", name)); err != nil {
			t.Fatal(err)
		}
		_, err = waiter.Exec(ctx, fmt.Sprintf("UPDATE %s SET value = 'blocked' WHERE id = 1", name))
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
			t.Fatalf("lock wait error = %v, want PostgreSQL lock_not_available (55P03)", err)
		}
	})
}

func postgresConformanceRequired() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED"))) {
	case "1", "true", "t", "yes", "on":
		return true
	default:
		return false
	}
}
