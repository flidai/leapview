package migrations

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io/fs"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
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
	if got, want := hex.EncodeToString(sum[:]), "dab523f64b743472e0209ee7be9800e2fe6803f86fe444f556e037cd2544ecd2"; got != want {
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
