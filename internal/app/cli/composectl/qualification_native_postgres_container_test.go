package composectl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	testcontainersnetwork "github.com/testcontainers/testcontainers-go/network"
)

// TestQualificationNativePostgresTopologyContainerBackedContract is the
// required real-runtime qualification for the native PostgreSQL sidecar. The
// fake runtime tests in qualification_native_postgres_test.go deliberately
// remain separate so argument/error matrices stay fast and deterministic.
func TestQualificationNativePostgresTopologyContainerBackedContract(t *testing.T) {
	if os.Getenv("LEAPVIEW_TEST_CONTAINERS") != "1" {
		t.Skip("set LEAPVIEW_TEST_CONTAINERS=1 to run the required container-backed qualification contract")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()

	network, err := testcontainersnetwork.New(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		_ = network.Remove(cleanupCtx)
	})

	runtime := newTestcontainersQualificationRuntime()
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	require.NoError(t, err)
	initScript := filepath.Join(repoRoot, "deploy", "postgres", "init.sh")
	require.FileExists(t, initScript)

	topology, err := newQualificationNativePostgresTopology(ctx, runtime, qualificationNativePostgresTopologyOptions{
		ComposeProject: "qualification-native-postgres",
		ComposeNetwork: network.Name,
		BundleRoot:     repoRoot,
		InitScript:     initScript,
		ContainerName:  "qualification-native-postgres-valid",
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		_ = topology.Remove(cleanupCtx)
	})

	// The canonical init hook creates the two isolated databases and all
	// reviewed login identities. Add only the minimal delivery/serving relation
	// surface needed by the qualification assertions; migrations remain owned
	// by the application and are intentionally outside this topology contract.
	qualificationExecSQL(t, ctx, topology.Container, qualificationNativePostgresContractSchema)
	qualificationExecSQL(t, ctx, topology.Container, qualificationNativePostgresRoleAndTLSChecks)

	// Bootstrap must remain open until the delivery lifecycle publishes its
	// first active pointer. The empty relation proves the initial state, and a
	// deliberately inserted pointer proves the closure check rejects an early
	// publication.
	require.NoError(t, topology.AssertBootstrapOpen(ctx, "real PostgreSQL initialization"))
	require.NoError(t, topology.AssertNativeDeliveryReads(ctx))
	qualificationExecSQL(t, ctx, topology.Container, qualificationNativePostgresPublishPointer)
	err = topology.AssertBootstrapOpen(ctx, "simulated candidate publication")
	require.Error(t, err)
	require.ErrorContains(t, err, "closed before candidate publication")

	// A runtime role must not cross the control/DuckLake database boundary even
	// when both databases are served by the same PostgreSQL instance.
	_, err = topology.Container.Exec(ctx, nil, "sh", "-ec", qualificationNativePostgresControlCrossDatabaseProbe)
	require.Error(t, err)

	// Revoke one canonical delivery read and require the qualification probe to
	// fail. This catches accidental grant drift in the real initialized image,
	// rather than only testing the expected error text against a fake runtime.
	qualificationExecSQL(t, ctx, topology.Container, qualificationNativePostgresRevokeDeliveryGrant)
	err = topology.AssertNativeDeliveryReads(ctx)
	require.Error(t, err)
	require.ErrorContains(t, err, "delivery_snapshot_seal")

	t.Run("broken init hook fails closed", func(t *testing.T) {
		brokenRoot := t.TempDir()
		brokenInit := filepath.Join(brokenRoot, "postgres-init.sh")
		canonical, readErr := os.ReadFile(initScript)
		require.NoError(t, readErr)
		// Keep the reviewed script body intact and make the hook fail only after
		// its normal work, proving that an init failure cannot be accepted as a
		// ready topology.
		require.NoError(t, os.WriteFile(brokenInit, append(canonical, []byte("\nexit 42\n")...), 0o755))

		brokenCtx, brokenCancel := context.WithTimeout(ctx, 12*time.Second)
		defer brokenCancel()
		brokenTopology, brokenErr := newQualificationNativePostgresTopology(brokenCtx, runtime, qualificationNativePostgresTopologyOptions{
			ComposeProject: "qualification-native-postgres",
			ComposeNetwork: network.Name,
			BundleRoot:     brokenRoot,
			InitScript:     brokenInit,
			ContainerName:  "qualification-native-postgres-broken-init",
		})
		if brokenTopology != nil {
			defer func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
				defer cleanupCancel()
				_ = brokenTopology.Remove(cleanupCtx)
			}()
		}
		require.Error(t, brokenErr)
	})
}

const qualificationNativePostgresContractSchema = `
set -eu
export PGSSLMODE=require PGPASSWORD="$POSTGRES_PASSWORD"
psql --host 127.0.0.1 --username leapview_bootstrap --dbname leapview_control --no-psqlrc --set ON_ERROR_STOP=1 <<'SQL'
CREATE SCHEMA IF NOT EXISTS ducklake AUTHORIZATION leapview_control_owner;
CREATE SCHEMA IF NOT EXISTS delivery AUTHORIZATION leapview_control_owner;
CREATE SCHEMA IF NOT EXISTS serving_state AUTHORIZATION leapview_control_owner;
CREATE TABLE IF NOT EXISTS ducklake.catalog_identity (physical_pool_id text);
CREATE TABLE IF NOT EXISTS delivery.delivery_build_attempt (attempt_id text);
CREATE TABLE IF NOT EXISTS delivery.delivery_snapshot_seal (seal_id text);
CREATE TABLE IF NOT EXISTS delivery.delivery_active_pointer (pointer_id integer);
CREATE TABLE IF NOT EXISTS serving_state.bundle (generation_id text);
GRANT USAGE ON SCHEMA ducklake, delivery, serving_state TO leapview_control_runtime, leapview_control_readonly;
GRANT SELECT ON ducklake.catalog_identity, delivery.delivery_build_attempt, delivery.delivery_snapshot_seal, delivery.delivery_active_pointer, serving_state.bundle TO leapview_control_runtime, leapview_control_readonly;
SQL
`

const qualificationNativePostgresRoleAndTLSChecks = `
set -eu
check_tls() {
  role="$1"
  database="$2"
  password="$3"
  actual="$(PGSSLMODE=require PGPASSWORD="$password" psql --host 127.0.0.1 --username "$role" --dbname "$database" --no-psqlrc --tuples-only --no-align --set ON_ERROR_STOP=1 --command "SELECT current_user::text || '|' || current_database()::text || '|' || (SELECT ssl::text FROM pg_stat_ssl WHERE pid = pg_backend_pid())")"
  if [ "$actual" != "$role|$database|true" ]; then
    printf 'TLS identity mismatch: %s expected %s|%s|true\n' "$actual" "$role" "$database"
    exit 1
  fi
}
export PGSSLMODE=require PGPASSWORD="$POSTGRES_PASSWORD"
databases="$(psql --host 127.0.0.1 --username leapview_bootstrap --dbname postgres --no-psqlrc --tuples-only --no-align --set ON_ERROR_STOP=1 --command "SELECT string_agg(datname, ',' ORDER BY datname) FROM pg_database WHERE datname IN ('leapview_control', 'leapview_ducklake')")"
if [ "$databases" != "leapview_control,leapview_ducklake" ]; then
  printf 'database set mismatch: %s\n' "$databases"
  exit 1
fi
owners="$(psql --host 127.0.0.1 --username leapview_bootstrap --dbname postgres --no-psqlrc --tuples-only --no-align --set ON_ERROR_STOP=1 --command "SELECT string_agg(datname || '=' || pg_get_userbyid(datdba), ',' ORDER BY datname) FROM pg_database WHERE datname IN ('leapview_control', 'leapview_ducklake')")"
if [ "$owners" != "leapview_control=leapview_control_owner,leapview_ducklake=leapview_ducklake_owner" ]; then
  printf 'database owner mismatch: %s\n' "$owners"
  exit 1
fi
check_role() {
  role="$1"
  expected="$2"
  actual="$(psql --host 127.0.0.1 --username leapview_bootstrap --dbname postgres --no-psqlrc --tuples-only --no-align --set ON_ERROR_STOP=1 --command "SELECT rolcanlogin::text || '|' || rolsuper::text || '|' || rolcreatedb::text || '|' || rolcreaterole::text || '|' || rolinherit::text FROM pg_roles WHERE rolname = '$role'")"
  if [ "$actual" != "$expected" ]; then
    printf 'role attributes mismatch for %s: %s expected %s\n' "$role" "$actual" "$expected"
    exit 1
  fi
}
check_role leapview_control_owner 'false|false|false|false|false'
check_role leapview_ducklake_owner 'false|false|false|false|false'
check_role leapview_control_backup 'false|false|false|false|false'
check_role leapview_control_runtime 'true|false|false|false|false'
check_role leapview_control_readonly 'true|false|false|false|false'
check_role leapview_control_migrator 'true|false|false|false|false'
check_role leapview_control_upgrade_coordinator 'true|false|false|false|false'
check_role leapview_control_maintenance 'true|false|false|false|false'
check_role leapview_ducklake_runtime 'true|false|false|false|false'
check_role leapview_ducklake_migrator 'true|false|false|false|false'
check_role leapview_ducklake_maintenance 'true|false|false|false|false'
check_tls leapview_control_runtime leapview_control "$LEAPVIEW_POSTGRES_CONTROL_RUNTIME_PASSWORD"
check_tls leapview_control_readonly leapview_control "$LEAPVIEW_POSTGRES_CONTROL_READONLY_PASSWORD"
check_tls leapview_control_migrator leapview_control "$LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_PASSWORD"
check_tls leapview_control_upgrade_coordinator leapview_control "$LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_PASSWORD"
check_tls leapview_control_maintenance leapview_control "$LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_PASSWORD"
check_tls leapview_ducklake_runtime leapview_ducklake "$LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_PASSWORD"
check_tls leapview_ducklake_migrator leapview_ducklake "$LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_PASSWORD"
check_tls leapview_ducklake_maintenance leapview_ducklake "$LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_PASSWORD"
`

const qualificationNativePostgresPublishPointer = `
set -eu
export PGSSLMODE=require PGPASSWORD="$POSTGRES_PASSWORD"
psql --host 127.0.0.1 --username leapview_bootstrap --dbname leapview_control --no-psqlrc --set ON_ERROR_STOP=1 --command "INSERT INTO delivery.delivery_active_pointer(pointer_id) VALUES (1)"
`

const qualificationNativePostgresControlCrossDatabaseProbe = `
set -eu
export PGSSLMODE=require PGPASSWORD="$LEAPVIEW_POSTGRES_CONTROL_RUNTIME_PASSWORD"
psql --host 127.0.0.1 --username leapview_control_runtime --dbname leapview_ducklake --no-psqlrc --set ON_ERROR_STOP=1 --command 'SELECT 1'
`

const qualificationNativePostgresRevokeDeliveryGrant = `
set -eu
export PGSSLMODE=require PGPASSWORD="$POSTGRES_PASSWORD"
psql --host 127.0.0.1 --username leapview_bootstrap --dbname leapview_control --no-psqlrc --set ON_ERROR_STOP=1 --command "REVOKE SELECT ON delivery.delivery_snapshot_seal FROM leapview_control_runtime"
`

func qualificationExecSQL(t *testing.T, ctx context.Context, container qualificationContainer, script string) {
	t.Helper()
	output, err := container.Exec(ctx, nil, "sh", "-ec", script)
	if err != nil {
		logs, logsErr := container.Logs(ctx, 100)
		if logsErr == nil {
			t.Fatalf("native PostgreSQL qualification command failed: %v (%s) logs=%s", err, strings.TrimSpace(string(redactQualificationBytes(output))), strings.TrimSpace(string(redactQualificationLog(logs, 100))))
		}
		t.Fatalf("native PostgreSQL qualification command failed: %v (%s)", err, strings.TrimSpace(string(redactQualificationBytes(output))))
	}
}
