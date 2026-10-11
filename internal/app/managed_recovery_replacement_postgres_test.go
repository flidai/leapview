package app

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// The installed module's root-owned promotion helper is separately qualified.
// Here an unprivileged, loopback-only native process lets the app consume the
// real restored cluster. No fake helper receipt is produced or accepted.
func (restored managedJourneyRestoredCluster) startReplacement(t *testing.T, original config.Config) (config.Config, *pgx.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	socket := managedJourneySocketDirectory(t)
	configuration := fmt.Sprintf("listen_addresses='127.0.0.1'\nport=%d\nunix_socket_directories='%s'\nssl=on\nssl_min_protocol_version='TLSv1.2'\nssl_cert_file='%s'\nssl_key_file='%s'\narchive_mode=off\nshared_preload_libraries=''\nrecovery_target_lsn='%s'\nrecovery_target_timeline='%d'\nrecovery_target_action='promote'\nrestore_command='%s --config=%s --stanza=%s --pg1-path=%s archive-get %%f %%p'\n", port, socket, restored.certFile, restored.keyFile, restored.frontier.TargetLSN, restored.frontier.Timeline, restored.pgbackrest, restored.providerFile, restored.frontier.Stanza, restored.directory)
	require.NoError(t, os.WriteFile(filepath.Join(restored.directory, "postgresql.conf"), []byte(configuration), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(restored.directory, "postgresql.auto.conf"), nil, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(restored.directory, "pg_hba.conf"), []byte("local all postgres trust\nhostssl all all 127.0.0.1/32 scram-sha-256\n"), 0600))
	pgctl := filepath.Join(restored.pgbin, "pg_ctl")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, pgctl, "-D", restored.directory, "-l", filepath.Join(filepath.Dir(restored.directory), "replacement.log"), "-w", "start")
	// Provider output can contain connection details; keep failures bounded.
	require.NoError(t, command.Run(), "start actual restored PostgreSQL")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := exec.CommandContext(ctx, pgctl, "-D", restored.directory, "-m", "fast", "-w", "stop").Run(); err != nil {
			t.Error("restored PostgreSQL cleanup failed")
		}
	})
	controlURL, err := url.Parse(original.PostgresControlURL)
	require.NoError(t, err)
	admin, err := pgx.Connect(ctx, fmt.Sprintf("host=%s port=%d user=postgres dbname=%s sslmode=disable", socket, port, controlURL.Path[1:]))
	require.NoError(t, err)
	var promoted bool
	var systemID, target, targetTimeline, replayed string
	var timeline uint32
	// pg_ctl readiness includes hot standby. Wait for the configured target
	// promotion, then prove its exact source timeline and retained replay point.
	require.Eventually(t, func() bool {
		err := admin.QueryRow(ctx, `SELECT NOT pg_is_in_recovery() AND COALESCE(pg_last_wal_replay_lsn()>=$1::pg_lsn,false),
		 system_identifier::text,current_setting('recovery_target_lsn'),current_setting('recovery_target_timeline'),
		 COALESCE(pg_last_wal_replay_lsn()::text,''),(pg_control_checkpoint()).timeline_id FROM pg_control_system()`, restored.frontier.TargetLSN).
			Scan(&promoted, &systemID, &target, &targetTimeline, &replayed, &timeline)
		return err == nil && promoted && timeline == restored.frontier.Timeline+1
	}, 30*time.Second, 100*time.Millisecond, "configured exact-target promotion must finish")
	require.Equal(t, restored.frontier.SystemID, systemID)
	require.Equal(t, restored.frontier.TargetLSN, target)
	require.Equal(t, strconv.FormatUint(uint64(restored.frontier.Timeline), 10), targetTimeline)
	history, err := os.ReadFile(filepath.Join(restored.directory, "pg_wal", fmt.Sprintf("%08X.history", timeline)))
	require.NoError(t, err)
	var ancestor []string
	for _, line := range strings.Split(string(history), "\n") {
		if value := strings.TrimSpace(line); value != "" && !strings.HasPrefix(value, "#") {
			ancestor = strings.Fields(value)
		}
	}
	require.GreaterOrEqual(t, len(ancestor), 2)
	require.Equal(t, []string{targetTimeline, replayed}, ancestor[:2], "promotion must fork from the exact replayed source ancestry")
	relayEndpoint, privateIP := postgrestest.StartPrivateTLSRelay(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	require.True(t, net.ParseIP(controlURL.Hostname()).Equal(privateIP), "retain the exact TLS-admitted fixture interface")
	for _, endpoint := range []*string{&original.PostgresControlURL, &original.PostgresControlMigratorURL, &original.PostgresControlMaintenanceURL, &original.PostgresControlReadonlyURL, &original.PostgresControlUpgradeCoordinatorURL, &original.PostgresDuckLakeURL, &original.PostgresDuckLakeMigratorURL, &original.PostgresDuckLakeMaintenanceURL} {
		parsed, err := url.Parse(*endpoint)
		require.NoError(t, err)
		parsed.Host = relayEndpoint
		query := parsed.Query()
		query.Set("sslmode", "verify-full")
		query.Set("sslrootcert", restored.caFile)
		parsed.RawQuery = query.Encode()
		*endpoint = parsed.String()
	}
	return original, admin
}
