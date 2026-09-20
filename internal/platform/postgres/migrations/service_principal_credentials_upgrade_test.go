package migrations

import (
	"database/sql"
	"fmt"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestServicePrincipalCredentialsMigrationUpgradesRevisionTwentyThree proves that
// an existing service-secret table receives durable last-used evidence without
// changing existing credential rows, and that the forward-only migration is
// safe to replay.
func TestServicePrincipalCredentialsMigrationUpgradesRevisionTwentyThree(t *testing.T) {
	harness := postgrestest.Start(t)
	owner := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Login: true, Password: "service-credential-migration"})
	harness.GrantRole(t, owner, migrator)
	database := harness.NewDatabase(t, "service_principal_credentials_upgrade")
	harness.GrantDatabase(t, database.Name, owner, "CONNECT", "CREATE")
	harness.GrantDatabase(t, database.Name, migrator, "CONNECT")

	admin, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(t.Context(), `
		ALTER DATABASE service_principal_credentials_upgrade OWNER TO leapview_control_owner;
		REVOKE ALL ON SCHEMA public FROM PUBLIC;
		GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}

	migrationDB, err := sql.Open("pgx", database.URL(migrator))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrationDB.Close() })

	previous := fstest.MapFS{
		"001_access_fixture.sql": {Data: []byte(`-- +goose Up
SET LOCAL ROLE leapview_control_owner;
CREATE SCHEMA access;
CREATE TABLE access.principal (
    id uuid PRIMARY KEY,
    principal_type text NOT NULL,
    status text NOT NULL,
    revoked_at timestamptz,
    disabled_at timestamptz,
    blocked_at timestamptz
);
CREATE TABLE access.service_principal_secret (
    id uuid PRIMARY KEY,
    service_principal_id uuid NOT NULL REFERENCES access.principal(id),
    name text NOT NULL,
    secret_fingerprint bytea NOT NULL,
    verifier bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz
);
RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
DROP SCHEMA access CASCADE;
RESET ROLE;
`)},
	}
	for revision := 2; revision <= 23; revision++ {
		name := fmt.Sprintf("%03d_noop.sql", revision)
		previous[name] = &fstest.MapFile{Data: []byte("-- +goose Up\n-- +goose Down\n")}
	}
	provider, err := newProvider(migrationDB, previous)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("apply revision-023 fixture: %v", err)
	}
	current, _, err := provider.GetVersions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if current != 23 {
		t.Fatalf("fixture revision = %d, want 23", current)
	}

	principalID := "00000000-0000-7000-8000-000000000021"
	secretID := "00000000-0000-7000-8000-000000000022"
	if _, err := admin.Exec(t.Context(), `
		INSERT INTO access.principal(id, principal_type, status)
		VALUES ($1::uuid, 'service', 'active')`, principalID); err != nil {
		t.Fatalf("seed pre-upgrade service credential: %v", err)
	}
	if _, err := admin.Exec(t.Context(), `
		INSERT INTO access.service_principal_secret(id, service_principal_id, name, secret_fingerprint, verifier, expires_at)
		VALUES ($2::uuid, $1::uuid, 'pre-upgrade', decode(repeat('11', 32), 'hex'), decode(repeat('22', 32), 'hex'), clock_timestamp() + interval '1 hour')`, principalID, secretID); err != nil {
		t.Fatalf("seed pre-upgrade service secret: %v", err)
	}

	migration, err := fs.ReadFile(MigrationFS(), "024_service_principal_credentials.sql")
	if err != nil {
		t.Fatal(err)
	}
	upgrade := make(fstest.MapFS, len(previous)+1)
	for name, file := range previous {
		upgrade[name] = file
	}
	upgrade["024_service_principal_credentials.sql"] = &fstest.MapFile{Data: migration}
	provider, err = newProvider(migrationDB, upgrade)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("upgrade revision-023 fixture: %v", err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("replay service principal credentials migration: %v", err)
	}
	current, _, err = provider.GetVersions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if current != 24 {
		t.Fatalf("upgraded revision = %d, want 24", current)
	}

	var columnExists bool
	if err := admin.QueryRow(t.Context(), `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema='access' AND table_name='service_principal_secret' AND column_name='last_used_at')`).Scan(&columnExists); err != nil {
		t.Fatal(err)
	}
	if !columnExists {
		t.Fatal("service principal last_used_at column is missing after upgrade")
	}
	var name string
	var lastUsed any
	if err := admin.QueryRow(t.Context(), `SELECT name, last_used_at FROM access.service_principal_secret WHERE id=$1::uuid`, secretID).Scan(&name, &lastUsed); err != nil {
		t.Fatal(err)
	}
	if name != "pre-upgrade" || lastUsed != nil {
		t.Fatalf("pre-upgrade service credential = name %q, last_used_at %#v", name, lastUsed)
	}
	if _, err := admin.Exec(t.Context(), `UPDATE access.service_principal_secret SET last_used_at=clock_timestamp() WHERE id=$1::uuid`, secretID); err != nil {
		t.Fatalf("persist upgraded last-used evidence: %v", err)
	}
	if _, err := provider.DownTo(t.Context(), 23); err == nil {
		t.Fatal("service principal credentials migration Down unexpectedly succeeded")
	}
	var evidencePresent bool
	if err := admin.QueryRow(t.Context(), `SELECT last_used_at IS NOT NULL FROM access.service_principal_secret WHERE id=$1::uuid`, secretID).Scan(&evidencePresent); err != nil {
		t.Fatal(err)
	}
	if !evidencePresent {
		t.Fatal("failed migration Down removed last-used evidence")
	}
}
