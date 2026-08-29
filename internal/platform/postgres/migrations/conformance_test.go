package migrations

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/log"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const postgres18BaselineImage = "docker.io/library/postgres:18-alpine@sha256:63bdc97d67b5133bf0e5ebd500bec6d046fa851dc81340d838f0347e616107e8"

// TestBaselinePostgreSQL18 applies the clean baseline to a real PostgreSQL 18
// server.  CI can make container availability mandatory with
// LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1; local runs skip when Docker is not
// available, matching the existing platform PostgreSQL conformance tests.
func TestBaselinePostgreSQL18(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	if !postgresBaselineConformanceRequired() {
		testcontainers.SkipIfProviderIsNotHealthy(t)
	}
	container, err := tcpostgres.Run(ctx, postgres18BaselineImage,
		tcpostgres.WithDatabase("leapview_control"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("leapview-baseline-secret"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90*time.Second)),
		testcontainers.WithLogger(log.TestLogger(t)),
	)
	if err != nil {
		if postgresBaselineConformanceRequired() {
			t.Fatalf("required PostgreSQL 18 baseline container: %v", err)
		}
		t.Skipf("PostgreSQL 18 baseline container unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, container)

	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, role := range []string{"leapview_control_owner", "leapview_control_migrator", "leapview_control_runtime", "leapview_control_readonly"} {
		if _, err := db.Exec(ctx, `CREATE ROLE `+role+` NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT`); err != nil {
			t.Fatalf("provision %s: %v", role, err)
		}
	}
	if _, err := db.Exec(ctx, `GRANT leapview_control_owner TO leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `GRANT CREATE ON DATABASE leapview_control TO leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}

	conn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SET ROLE leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("apply baseline: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Re-running a clean baseline is safe for retries; the recorded checksum is
	// verified rather than replaced.
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("reapply baseline: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var schemaCount int
	if err := db.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.schemata
		WHERE schema_name = ANY($1::text[])`, []string{"access", "delivery", "refresh", "event", "audit", "lineage", "cache", "agent"}).Scan(&schemaCount); err != nil {
		t.Fatal(err)
	}
	if schemaCount != 8 {
		t.Fatalf("capability schema count = %d, want 8", schemaCount)
	}

	var revision int64
	if err := db.QueryRow(ctx, `SELECT revision FROM platform.schema_revision WHERE migration_id = $1`, BaselineMigrationID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != BaselineRevision {
		t.Fatalf("schema revision = %d, want %d", revision, BaselineRevision)
	}
	var canUpdateAudit, canUpdateRevision bool
	if err := db.QueryRow(ctx, `
		SELECT has_table_privilege('leapview_control_runtime', 'audit.audit_event', 'UPDATE'),
		       has_table_privilege('leapview_control_runtime', 'platform.schema_revision', 'UPDATE')`).
		Scan(&canUpdateAudit, &canUpdateRevision); err != nil {
		t.Fatal(err)
	}
	if canUpdateAudit || canUpdateRevision {
		t.Fatalf("runtime mutation grants leaked: audit update=%t revision update=%t", canUpdateAudit, canUpdateRevision)
	}
	if _, err := db.Exec(ctx, `UPDATE platform.schema_revision SET migration_id = 'tampered' WHERE revision = $1`, BaselineRevision); err == nil {
		t.Fatal("schema revision append-only trigger did not reject an update")
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO audit.audit_event (audit_id, action, outcome)
		VALUES ('00000000-0000-0000-0000-000000000002', 'schema.test', 'success')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE audit.audit_event SET action = 'tampered' WHERE audit_id = '00000000-0000-0000-0000-000000000002'`); err == nil {
		t.Fatal("audit append-only trigger did not reject an update")
	}
	// Exercise the append path as the non-superuser runtime role.  Trigger
	// execution must remain available even though the trigger function itself
	// is not callable by PUBLIC.
	runtimeConn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeConn.Release()
	if _, err := runtimeConn.Exec(ctx, `SET ROLE leapview_control_runtime`); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeConn.Exec(ctx, `
		INSERT INTO audit.audit_event (audit_id, action, outcome)
		VALUES ('00000000-0000-0000-0000-000000000003', 'runtime.test', 'success')`); err != nil {
		t.Fatalf("runtime audit append: %v", err)
	}

	// PostgreSQL enforces the JSON document boundary at the persistence edge.
	_, err = db.Exec(ctx, `
		INSERT INTO access.principal (id, principal_type, status, attributes)
		VALUES ('00000000-0000-0000-0000-000000000001', 'user', 'active', $1::jsonb)`, `{"oversized":"`+strings.Repeat("x", 20000)+`"}`)
	if err == nil {
		t.Fatal("oversized principal attributes unexpectedly accepted")
	}
}

func postgresBaselineConformanceRequired() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED"))) {
	case "1", "true", "t", "yes", "on":
		return true
	default:
		return false
	}
}
