package migrations

import (
	"database/sql"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func authoringTypedMigrationFixture() []byte {
	return []byte(`-- +goose Up
SET LOCAL ROLE leapview_control_owner;
CREATE SCHEMA access;
CREATE SCHEMA audit;
-- +goose StatementBegin
CREATE FUNCTION access.valid_permission_pairs(profile text, value jsonb)
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
    SELECT (profile IS NULL AND value IS NULL)
        OR (profile = 'leapview.permissions/v1' AND jsonb_typeof(value) = 'array')
$$;
CREATE FUNCTION access.valid_capabilities(value jsonb)
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$ SELECT value IS NULL OR jsonb_typeof(value) = 'array' $$;
CREATE FUNCTION access.reject_device_authorization_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.id<>NEW.id OR OLD.capabilities IS DISTINCT FROM NEW.capabilities OR OLD.expires_at<>NEW.expires_at THEN
        RAISE EXCEPTION 'device authorization identity is immutable';
    END IF;
    RETURN NEW;
END; $$;
CREATE FUNCTION access.reject_authoring_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME='authoring_session' AND
       (OLD.id<>NEW.id OR OLD.capabilities IS DISTINCT FROM NEW.capabilities) THEN
        RAISE EXCEPTION 'authoring session identity is immutable';
    END IF;
    RETURN NEW;
END; $$;
CREATE FUNCTION access.reject_authoring_credential_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.active = FALSE AND NEW.active <> FALSE THEN
        RAISE EXCEPTION 'authoring credential activation is not reversible';
    END IF;
    IF OLD.replaced_at IS NOT NULL AND (NEW.replaced_at IS NULL OR NEW.replaced_at < OLD.replaced_at) THEN
        RAISE EXCEPTION 'credential replacement timestamp is monotonic';
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
CREATE TABLE audit.audit_event (
    audit_id uuid PRIMARY KEY,
    principal_id uuid,
    source text NOT NULL,
    operation text NOT NULL,
    action text NOT NULL,
    resource_kind text,
    resource_id text,
    capability text NOT NULL DEFAULT '',
    outcome text NOT NULL DEFAULT 'success',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE TABLE access.device_authorization (
    id text PRIMARY KEY,
    client_id text NOT NULL,
    device_code_hash text NOT NULL,
    user_code_hash text NOT NULL,
    target_id text NOT NULL,
    project_id text NOT NULL,
    capabilities jsonb NOT NULL CHECK (access.valid_capabilities(capabilities) AND jsonb_typeof(capabilities)='array' AND octet_length(capabilities::text)<=2048),
    status text NOT NULL CHECK (status IN ('pending','approved','denied','consumed')),
    principal_id uuid,
    expires_at timestamptz NOT NULL,
    poll_interval_seconds integer NOT NULL DEFAULT 5,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    approved_at timestamptz,
    denied_at timestamptz,
    consumed_at timestamptz,
    CHECK ((status='pending' AND principal_id IS NULL AND approved_at IS NULL AND denied_at IS NULL AND consumed_at IS NULL)
        OR (status='approved' AND principal_id IS NOT NULL AND approved_at IS NOT NULL AND denied_at IS NULL AND consumed_at IS NULL)
        OR (status='denied' AND principal_id IS NOT NULL AND denied_at IS NOT NULL AND consumed_at IS NULL)
        OR (status='consumed' AND principal_id IS NOT NULL AND approved_at IS NOT NULL AND consumed_at IS NOT NULL))
);
CREATE TABLE access.authoring_session (
    id text PRIMARY KEY,
    kind text NOT NULL,
    client_id text NOT NULL,
    principal_id uuid NOT NULL,
    target_id text NOT NULL,
    project_id text NOT NULL,
    capabilities jsonb NOT NULL CHECK (access.valid_capabilities(capabilities) AND jsonb_typeof(capabilities)='array' AND octet_length(capabilities::text)<=2048),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz
);
CREATE TABLE access.authoring_credential (
    id text PRIMARY KEY,
    session_id text NOT NULL REFERENCES access.authoring_session(id),
    access_token_hash text NOT NULL,
    refresh_token_hash text,
    access_expires_at timestamptz NOT NULL,
    refresh_expires_at timestamptz,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    replaced_at timestamptz
);
CREATE TRIGGER device_authorization_immutable BEFORE UPDATE ON access.device_authorization
    FOR EACH ROW EXECUTE FUNCTION access.reject_device_authorization_rewrite();
CREATE TRIGGER authoring_session_immutable BEFORE UPDATE ON access.authoring_session
    FOR EACH ROW EXECUTE FUNCTION access.reject_authoring_identity_rewrite();
CREATE TRIGGER authoring_credential_transition BEFORE UPDATE ON access.authoring_credential
    FOR EACH ROW EXECUTE FUNCTION access.reject_authoring_credential_transition();
RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
DROP SCHEMA audit CASCADE;
DROP SCHEMA access CASCADE;
RESET ROLE;
`)
}

func TestTypedAuthoringPermissionsMigrationRetiresLegacyScopes(t *testing.T) {
	harness := postgrestest.Start(t)
	owner := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Login: true, Password: "authoring-upgrade"})
	harness.GrantRole(t, owner, migrator)
	database := harness.NewDatabase(t, "authoring_permissions_upgrade")
	harness.GrantDatabase(t, database.Name, owner, "CONNECT", "CREATE")
	harness.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")
	admin, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(t.Context(), `
		ALTER DATABASE authoring_permissions_upgrade OWNER TO leapview_control_owner;
		REVOKE ALL ON SCHEMA public FROM PUBLIC;
		GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}
	migrationDB, err := sql.Open("pgx", database.URL(migrator))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrationDB.Close() })

	previous := fstest.MapFS{"001_authoring_fixture.sql": {Data: authoringTypedMigrationFixture()}}
	for revision := 2; revision <= 31; revision++ {
		name := fmt.Sprintf("%03d_noop.sql", revision)
		previous[name] = &fstest.MapFile{Data: []byte("-- +goose Up\n-- +goose Down\n")}
	}
	provider, err := newProvider(migrationDB, previous)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(t.Context(), 31); err != nil {
		t.Fatalf("apply authoring fixture revision 31: %v", err)
	}

	principalID := "10000000-0000-4000-8000-000000000001"
	oldCapabilities := ` ["PROJECT_ADMIN","RESOURCE_EDIT"] `
	if _, err := admin.Exec(t.Context(), `
		INSERT INTO access.device_authorization (
			id, client_id, device_code_hash, user_code_hash, target_id, project_id,
			capabilities, status, expires_at, created_at
		) VALUES ('device-old', 'leapview-cli', repeat('a', 64), repeat('b', 64), 'instance-old', 'project:old',
			$1::jsonb, 'pending', clock_timestamp() + interval '1 hour', clock_timestamp() - interval '1 hour')`, oldCapabilities); err != nil {
		t.Fatalf("seed legacy device authorization: %v", err)
	}
	if _, err := admin.Exec(t.Context(), `
		INSERT INTO access.authoring_session (
			id, kind, client_id, principal_id, target_id, project_id, capabilities,
			created_at, expires_at
		) VALUES ('session-old', 'human_cli', 'leapview-cli', $2::uuid, 'instance-old', 'project:old',
			$1::jsonb, clock_timestamp() - interval '1 hour', clock_timestamp() + interval '1 day')`, oldCapabilities, principalID); err != nil {
		t.Fatalf("seed legacy authoring session: %v", err)
	}
	if _, err := admin.Exec(t.Context(), `
		INSERT INTO access.authoring_credential (
			id, session_id, access_token_hash, access_expires_at, active
		) VALUES ('credential-old', 'session-old', repeat('c', 64), clock_timestamp() + interval '1 hour', TRUE)`); err != nil {
		t.Fatalf("seed legacy authoring credential: %v", err)
	}

	upgrade := make(fstest.MapFS, len(previous)+1)
	for name, file := range previous {
		upgrade[name] = file
	}
	contents, err := fs.ReadFile(MigrationFS(), "032_typed_authoring_permissions.sql")
	if err != nil {
		t.Fatal(err)
	}
	upgrade["032_typed_authoring_permissions.sql"] = &fstest.MapFile{Data: contents}
	provider, err = newProvider(migrationDB, upgrade)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(t.Context(), 32); err != nil {
		t.Fatalf("apply typed authoring upgrade: %v", err)
	}

	var sessionProfile, sessionPermissions string
	var revokedAt sql.NullTime
	if err := admin.QueryRow(t.Context(), `
		SELECT permission_profile, permissions::text, revoked_at
		FROM access.authoring_session WHERE id='session-old'`).Scan(&sessionProfile, &sessionPermissions, &revokedAt); err != nil {
		t.Fatal(err)
	}
	if sessionProfile != "leapview.permissions/v1" || strings.TrimSpace(sessionPermissions) != "[]" || !revokedAt.Valid {
		t.Fatalf("legacy authoring session after migration = %q/%q/%v, want typed empty scope and revoked", sessionProfile, sessionPermissions, revokedAt)
	}
	var active bool
	var replacedAt sql.NullTime
	if err := admin.QueryRow(t.Context(), `SELECT active, replaced_at FROM access.authoring_credential WHERE id='credential-old'`).Scan(&active, &replacedAt); err != nil {
		t.Fatal(err)
	}
	if active || !replacedAt.Valid {
		t.Fatalf("legacy authoring credential after migration = active:%t replaced:%v, want inactive and replaced", active, replacedAt)
	}
	var deviceProfile, devicePermissions string
	var expiresAt time.Time
	if err := admin.QueryRow(t.Context(), `
		SELECT permission_profile, permissions::text, expires_at
		FROM access.device_authorization WHERE id='device-old'`).Scan(&deviceProfile, &devicePermissions, &expiresAt); err != nil {
		t.Fatal(err)
	}
	if deviceProfile != "leapview.permissions/v1" || strings.TrimSpace(devicePermissions) != "[]" || !expiresAt.Before(time.Now().UTC()) {
		t.Fatalf("legacy device authorization after migration = %q/%q/%s, want typed empty scope and expired", deviceProfile, devicePermissions, expiresAt)
	}

	// Current typed SQL omits the historical capability columns. These inserts
	// prove the upgraded database accepts the same contract as the clean schema.
	if _, err := admin.Exec(t.Context(), `
		INSERT INTO access.device_authorization (
			id, client_id, device_code_hash, user_code_hash, target_id, project_id,
			permission_profile, permissions, status, expires_at, created_at
		) VALUES ('device-new', 'leapview-cli', repeat('d', 64), repeat('e', 64), 'instance-new', 'project:new',
			'leapview.permissions/v1', '[]'::jsonb, 'pending', clock_timestamp() + interval '1 hour', clock_timestamp())`); err != nil {
		t.Fatalf("insert typed device authorization without legacy capabilities: %v", err)
	}
	if _, err := admin.Exec(t.Context(), `
		INSERT INTO access.authoring_session (
			id, kind, client_id, principal_id, target_id, project_id, permission_profile, permissions,
			created_at, expires_at
		) VALUES ('session-new', 'human_cli', 'leapview-cli', $1::uuid, 'instance-new', 'project:new',
			'leapview.permissions/v1', '[]'::jsonb, clock_timestamp(), clock_timestamp() + interval '1 day')`, principalID); err != nil {
		t.Fatalf("insert typed authoring session without legacy capabilities: %v", err)
	}
	for _, table := range []string{"device_authorization", "authoring_session"} {
		var nullable bool
		if err := admin.QueryRow(t.Context(), `SELECT is_nullable='YES' FROM information_schema.columns WHERE table_schema='access' AND table_name=$1 AND column_name='capabilities'`, table).Scan(&nullable); err != nil {
			t.Fatal(err)
		}
		if !nullable {
			t.Fatalf("%s.capabilities remained NOT NULL after typed migration", table)
		}
	}
	var auditRows int
	if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE operation='retire_legacy_authoring_credentials'`).Scan(&auditRows); err != nil {
		t.Fatal(err)
	}
	if auditRows != 2 {
		t.Fatalf("legacy authoring retirement audit rows = %d, want 2", auditRows)
	}
}
