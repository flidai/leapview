package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type permissionPairContractFixture struct {
	Name    string          `json:"name"`
	Profile *string         `json:"profile"`
	Pairs   json.RawMessage `json:"pairs"`
	Valid   bool            `json:"valid"`
}

func readPermissionPairContractFixtures(t *testing.T) []permissionPairContractFixture {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate permission pair contract fixture")
	}
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(source), "../../../access/testdata/permission_pair_contract.json"))
	if err != nil {
		t.Fatalf("read permission pair contract fixture: %v", err)
	}
	var fixtures []permissionPairContractFixture
	if err := json.Unmarshal(encoded, &fixtures); err != nil {
		t.Fatalf("decode permission pair contract fixture: %v", err)
	}
	return fixtures
}

func permissionValidationFixtureBaseline() []byte {
	return []byte(`-- +goose Up
SET LOCAL ROLE leapview_control_owner;
CREATE SCHEMA access;
CREATE SCHEMA audit;
-- +goose StatementBegin
CREATE FUNCTION access.valid_capabilities(value jsonb)
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$ SELECT TRUE $$;
-- +goose StatementEnd
CREATE TABLE audit.audit_event (
    audit_id uuid PRIMARY KEY,
    principal_id uuid NOT NULL,
    source text NOT NULL,
    operation text NOT NULL,
    action text NOT NULL,
    resource_kind text NOT NULL,
    resource_id text NOT NULL,
    capability text NOT NULL,
    outcome text NOT NULL,
    metadata jsonb NOT NULL
);
CREATE TABLE access.api_token (
    id uuid PRIMARY KEY,
    principal_id uuid NOT NULL,
    name text NOT NULL,
    token_fingerprint bytea NOT NULL,
    verifier bytea NOT NULL,
    capabilities jsonb,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_used_at timestamptz,
    revoked_at timestamptz
);
RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
DROP SCHEMA audit CASCADE;
DROP SCHEMA access CASCADE;
RESET ROLE;
`)
}

func TestTypedPermissionValidationMigrationUpgradesRevisionTwentyTwoWithContractCorpus(t *testing.T) {
	harness := postgrestest.Start(t)
	owner := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Login: true, Password: "permission-upgrade"})
	runtimeRole := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Login: true, Password: "permission-runtime"})
	harness.GrantRole(t, owner, migrator)
	database := harness.NewDatabase(t, "permission_validation_upgrade")
	harness.GrantDatabase(t, database.Name, owner, "CONNECT", "CREATE")
	harness.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")
	harness.GrantDatabase(t, database.Name, runtimeRole, "CONNECT")

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(ctx, `
		ALTER DATABASE permission_validation_upgrade OWNER TO leapview_control_owner;
		REVOKE ALL ON SCHEMA public FROM PUBLIC;
		GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}

	migrationDB, err := sql.Open("pgx", database.URL(migrator))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrationDB.Close() })
	typedMigration, err := fs.ReadFile(MigrationFS(), "024_typed_api_token_permissions.sql")
	if err != nil {
		t.Fatal(err)
	}
	hardeningMigration, err := fs.ReadFile(MigrationFS(), "026_typed_permission_validation_hardening.sql")
	if err != nil {
		t.Fatal(err)
	}
	previous := fstest.MapFS{
		"001_permission_fixture.sql":          {Data: permissionValidationFixtureBaseline()},
		"024_typed_api_token_permissions.sql": {Data: typedMigration},
	}
	provider, err := newProvider(migrationDB, previous)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 22); err != nil {
		t.Fatalf("apply revision 22 fixture: %v", err)
	}
	if current, _, err := provider.GetVersions(ctx); err != nil {
		t.Fatal(err)
	} else if current != 22 {
		t.Fatalf("pre-hardening revision = %d, want 22", current)
	}

	fixtures := readPermissionPairContractFixtures(t)
	var validFixture permissionPairContractFixture
	for _, fixture := range fixtures {
		if fixture.Name == "exact semantic consume" {
			validFixture = fixture
			break
		}
	}
	if validFixture.Name == "" {
		t.Fatal("permission contract fixture lacks exact semantic consume case")
	}
	if _, err := admin.Exec(ctx, `
		INSERT INTO access.api_token (
			id, principal_id, name, token_fingerprint, verifier,
			permission_profile, permissions, expires_at
		) VALUES (
			'10000000-0000-0000-0000-000000000001'::uuid,
			'20000000-0000-0000-0000-000000000001'::uuid,
			'existing-valid-pair', decode(repeat('ab', 32), 'hex'),
			decode(repeat('cd', 32), 'hex'), 'leapview.permissions/v1',
			$1::jsonb, clock_timestamp() + interval '1 day'
		)`, validFixture.Pairs); err != nil {
		t.Fatalf("insert valid revision-22 typed token: %v", err)
	}

	upgrade := fstest.MapFS{
		"001_permission_fixture.sql":                    {Data: permissionValidationFixtureBaseline()},
		"024_typed_api_token_permissions.sql":           {Data: typedMigration},
		"023_noop.sql":                                  {Data: []byte("-- +goose Up\n-- +goose Down\n")},
		"026_typed_permission_validation_hardening.sql": {Data: hardeningMigration},
	}
	provider, err = newProvider(migrationDB, upgrade)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 24); err != nil {
		t.Fatalf("upgrade revision 22 fixture through revision 24: %v", err)
	}
	if current, _, err := provider.GetVersions(ctx); err != nil {
		t.Fatal(err)
	} else if current != 24 {
		t.Fatalf("post-hardening revision = %d, want 24", current)
	}

	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			var profile any
			if fixture.Profile != nil {
				profile = *fixture.Profile
			}
			var got bool
			if err := admin.QueryRow(ctx, `
				SELECT access.valid_permission_pairs($1::text, $2::jsonb)`, profile, fixture.Pairs).Scan(&got); err != nil {
				t.Fatalf("validate permission pairs: %v", err)
			}
			if got != fixture.Valid {
				t.Fatalf("PostgreSQL permission contract validity = %t, want %t", got, fixture.Valid)
			}
		})
	}

	var persisted int
	if err := admin.QueryRow(ctx, `
		SELECT count(*) FROM access.api_token
		WHERE id = '10000000-0000-0000-0000-000000000001'::uuid
		  AND permissions = $1::jsonb`, validFixture.Pairs).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != 1 {
		t.Fatalf("valid existing typed token rows = %d, want 1", persisted)
	}
}
