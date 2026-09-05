package migrations

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io/fs"
	"strings"
	"testing"

	jobpostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	recoverypostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestEmbeddedGooseBaselineIsTheOnlyImmutableMigration(t *testing.T) {
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	var sqlFiles []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sql") {
			sqlFiles = append(sqlFiles, entry.Name())
		}
	}
	if len(sqlFiles) != 1 || sqlFiles[0] != "001_control_plane.sql" {
		t.Fatalf("embedded Goose migrations = %v", sqlFiles)
	}
	contents, err := fs.ReadFile(MigrationFS(), sqlFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	if got, want := hex.EncodeToString(sum[:]), "87f0e8e6f10964d2f71f5b5378eaef10861dd49ddd4e63ec314451f0e7fd29ff"; got != want {
		t.Fatalf("immutable Goose baseline digest = %s, want %s", got, want)
	}
	text := string(contents)
	for _, required := range []string{"-- +goose Up", "SET LOCAL ROLE leapview_control_owner", "CREATE TABLE IF NOT EXISTS event.event_log"} {
		if !strings.Contains(text, required) {
			t.Errorf("Goose baseline missing %q", required)
		}
	}
	for _, forbidden := range []string{"platform.schema_revision", "watermill", "cache.cache_l3", "CREATE SCHEMA IF NOT EXISTS cache"} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
			t.Errorf("Goose baseline retains removed contract %q", forbidden)
		}
	}
}

func TestEmbeddedGooseBaselineMirrorsCanonicalJobsRiverFence(t *testing.T) {
	contents, err := fs.ReadFile(MigrationFS(), "001_control_plane.sql")
	if err != nil {
		t.Fatal(err)
	}
	baseline := strings.ReplaceAll(strings.ReplaceAll(string(contents), "-- +goose StatementBegin\n", ""), "-- +goose StatementEnd\n", "")
	canonical := strings.ReplaceAll(strings.ReplaceAll(jobpostgres.SchemaSQL(), "-- +goose StatementBegin\n", ""), "-- +goose StatementEnd\n", "")
	const start = "CREATE OR REPLACE FUNCTION jobs.guard_river_result_fence()"
	const end = "CREATE TABLE IF NOT EXISTS jobs.job_history ("
	if got, want := recoverySQLBlock(t, baseline, start, end), recoverySQLBlock(t, canonical, start, end); got != want {
		t.Error("Goose baseline River result fence differs from canonical jobs schema")
	}
}

func TestEmbeddedGooseBaselineMirrorsCanonicalRecoveryGuards(t *testing.T) {
	contents, err := fs.ReadFile(MigrationFS(), "001_control_plane.sql")
	if err != nil {
		t.Fatal(err)
	}
	baseline := strings.ReplaceAll(strings.ReplaceAll(string(contents), "-- +goose StatementBegin\n", ""), "-- +goose StatementEnd\n", "")
	canonical := recoverypostgres.SchemaSQL()
	for _, block := range []struct {
		name, start, end string
	}{
		{name: "object root constraints", start: "CREATE TABLE IF NOT EXISTS recovery.recovery_object_root (", end: "CREATE TABLE IF NOT EXISTS recovery.validation_attempt ("},
		{name: "publication guard", start: "CREATE OR REPLACE FUNCTION recovery.reject_frontier_mutation()", end: "CREATE OR REPLACE FUNCTION recovery.reject_frontier_insert()"},
		{name: "validation guard", start: "CREATE OR REPLACE FUNCTION recovery.guard_validation_result_insert()", end: "DROP TRIGGER IF EXISTS recovery_validation_result_guard"},
	} {
		want := recoverySQLBlock(t, canonical, block.start, block.end)
		got := recoverySQLBlock(t, baseline, block.start, block.end)
		if got != want {
			t.Errorf("Goose baseline %s differs from canonical recovery schema", block.name)
		}
	}
}

func recoverySQLBlock(t *testing.T, source, start, end string) string {
	t.Helper()
	startAt := strings.Index(source, start)
	if startAt < 0 {
		t.Fatalf("SQL block start %q is missing", start)
	}
	endAt := strings.Index(source[startAt:], end)
	if endAt < 0 {
		t.Fatalf("SQL block end %q is missing", end)
	}
	return source[startAt : startAt+endAt]
}

func TestGooseEntryPointsRejectNilDatabase(t *testing.T) {
	if _, err := NewProvider(nil); err == nil {
		t.Fatal("NewProvider(nil) unexpectedly succeeded")
	}
	if err := ApplyGoose(t.Context(), nil); err == nil {
		t.Fatal("ApplyGoose(nil) unexpectedly succeeded")
	}
	if err := VerifyGoose(t.Context(), nil); err == nil {
		t.Fatal("VerifyGoose(nil) unexpectedly succeeded")
	}
	if err := ReconcileRolePolicy(t.Context(), nil, "SELECT 1"); err == nil {
		t.Fatal("ReconcileRolePolicy(nil) unexpectedly succeeded")
	}
}

func TestVerifyGooseFailsClosedOnFreshDatabase(t *testing.T) {
	harness := postgrestest.Start(t)
	database := harness.NewDatabase(t, "goose_verify_fresh")
	db, err := sql.Open("pgx", database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := VerifyGoose(t.Context(), db); err == nil {
		t.Fatal("VerifyGoose unexpectedly succeeded on a fresh database")
	}

	var eventSchemaExists, versionTableExists bool
	if err := db.QueryRowContext(t.Context(), `
		SELECT EXISTS (
		           SELECT 1 FROM information_schema.schemata
		           WHERE schema_name = 'event'
		       ),
		       EXISTS (
		           SELECT 1 FROM information_schema.tables
		           WHERE table_schema = 'public' AND table_name = 'goose_db_version'
		       )`).Scan(&eventSchemaExists, &versionTableExists); err != nil {
		t.Fatal(err)
	}
	if eventSchemaExists {
		t.Fatal("VerifyGoose applied the event schema while checking a fresh database")
	}
	if !versionTableExists {
		t.Fatal("VerifyGoose did not initialize its authoritative version table")
	}
	var version int64
	if err := db.QueryRowContext(t.Context(), `
		SELECT version_id
		FROM public.goose_db_version
		ORDER BY id DESC LIMIT 1`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 0 {
		t.Fatalf("fresh Goose version row = %d, want 0", version)
	}
}
