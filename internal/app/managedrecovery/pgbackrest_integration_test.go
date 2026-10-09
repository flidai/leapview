package managedrecovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/jackc/pgx/v5"
)

// This is a physical adapter regression against real PostgreSQL WAL and a real
// pgBackRest repository. Application RecoverySet/role/TLS qualification is a
// separate journey; these test tables are not application acceptance evidence.
func TestActualPinnedPGBackRestRestoresExplicitWALFrontier(t *testing.T) {
	program, pgbin := os.Getenv("LEAPVIEW_TEST_MANAGED_PGBACKREST"), os.Getenv("LEAPVIEW_TEST_MANAGED_POSTGRES_BIN")
	if program == "" || pgbin == "" {
		t.Skip("explicit pinned PostgreSQL/pgBackRest integration inputs required")
	}
	if os.Geteuid() == 0 {
		t.Fatal("PostgreSQL qualification requires an unprivileged process")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	base, err := os.MkdirTemp("", "lv-pg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	source, repo, socket := filepath.Join(base, "source"), filepath.Join(base, "repository"), filepath.Join(base, "socket")
	if err := os.Mkdir(socket, 0700); err != nil {
		t.Fatal(err)
	}
	owner, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	configFile := filepath.Join(base, "pgbackrest.conf")
	contents := []byte(fmt.Sprintf("[global]\nrepo1-path=%s\nrepo1-retention-full=2\nstart-fast=y\nprocess-max=2\narchive-timeout=60\nlog-level-console=off\nlog-level-file=off\n[managed]\npg1-path=%s\npg1-port=%d\npg1-socket-path=%s\npg1-user=%s\n", repo, source, port, socket, owner.Username))
	if err := os.WriteFile(configFile, contents, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(ctx context.Context, program string, args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(ctx, program, args...)
		command.Env = append(os.Environ(), "LANG=C", "TZ=UTC")
		output, err := command.Output()
		if err != nil {
			t.Fatalf("actual pinned %s failed: %v", filepath.Base(program), err)
		}
		return output
	}
	pgctl := filepath.Join(pgbin, "pg_ctl")
	run(ctx, filepath.Join(pgbin, "initdb"), "-D", source, "--no-locale", "--encoding=UTF8", "--auth-local=trust", "--auth-host=reject")
	postgresConfig := fmt.Sprintf("\nlisten_addresses=''\nport=%d\nunix_socket_directories='%s'\narchive_mode=on\narchive_command='%s --config=%s --stanza=managed archive-push %%p'\n", port, socket, program, configFile)
	file, err := os.OpenFile(filepath.Join(source, "postgresql.conf"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString(postgresConfig)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatal("write PostgreSQL configuration")
	}
	run(ctx, pgctl, "-D", source, "-l", filepath.Join(base, "primary.log"), "-w", "start")
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		command := exec.CommandContext(cleanup, pgctl, "-D", source, "-m", "immediate", "-w", "stop")
		_ = command.Run()
	})
	connect := func(ctx context.Context, database string) (*pgx.Conn, error) {
		return pgx.Connect(ctx, fmt.Sprintf("host=%s port=%d user=%s dbname=%s sslmode=disable", socket, port, owner.Username, database))
	}
	admin, err := connect(ctx, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	if _, err := admin.Exec(ctx, "CREATE DATABASE control"); err != nil {
		t.Fatal(err)
	}
	control, err := connect(ctx, "control")
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close(context.Background())
	if _, err := control.Exec(ctx, "CREATE TABLE physical_adapter_regression(id integer PRIMARY KEY,value text NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	run(ctx, program, "--config="+configFile, "--stanza=managed", "stanza-create")
	run(ctx, program, "--config="+configFile, "--stanza=managed", "--type=full", "backup")
	var info []struct {
		Backup []struct {
			Label string `json:"label"`
		} `json:"backup"`
	}
	if err := json.Unmarshal(run(ctx, program, "--config="+configFile, "--stanza=managed", "--output=json", "info"), &info); err != nil || len(info) != 1 || len(info[0].Backup) != 1 {
		t.Fatal("actual full backup identity missing")
	}
	if _, err := control.Exec(ctx, "INSERT INTO physical_adapter_regression VALUES(1,'acknowledged-before-frontier')"); err != nil {
		t.Fatal(err)
	}
	var lsn, systemID string
	if err := admin.QueryRow(ctx, "SELECT pg_current_wal_insert_lsn()::text,(SELECT system_identifier::text FROM pg_control_system())").Scan(&lsn, &systemID); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Exec(ctx, "INSERT INTO physical_adapter_regression VALUES(2,'after-selected-frontier')"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "SELECT pg_switch_wal()"); err != nil {
		t.Fatal(err)
	}
	run(ctx, program, "--config="+configFile, "--stanza=managed", "check")
	frontier := PGFrontier{Stanza: "managed", BackupSet: info[0].Backup[0].Label, TargetLSN: lsn, SystemID: systemID}
	identity, err := frontier.RecoveryIdentity()
	if err != nil {
		t.Fatal(err)
	}
	point := recoveryset.ClusterRecoveryPoint{DatabaseRole: recoveryset.DatabaseControl, ClusterIdentity: "postgres-system-id:" + systemID, DatabaseIdentity: "control", RecoveryIdentity: identity}
	// Stop the isolated original before starting its replacement on the same
	// private socket. This test owns no application traffic or production host.
	control.Close(ctx)
	admin.Close(ctx)
	run(ctx, pgctl, "-D", source, "-m", "fast", "-w", "stop")
	readbacks := 0
	readback := func(ctx context.Context, path string, actual PGFrontier, request providerrestore.DatabaseRequest) ([]providerrestore.DatabaseResult, error) {
		readbacks++
		run(ctx, pgctl, "-D", path, "-l", filepath.Join(base, "replacement-"+strconv.Itoa(readbacks)+".log"), "-w", "start")
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(cleanup, pgctl, "-D", path, "-m", "fast", "-w", "stop")
			_ = command.Run()
		}()
		connection, err := connect(ctx, "control")
		if err != nil {
			return nil, err
		}
		defer connection.Close(context.Background())
		var paused bool
		for !paused {
			if err := connection.QueryRow(ctx, "SELECT pg_is_wal_replay_paused()").Scan(&paused); err != nil {
				return nil, err
			}
			if !paused {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(50 * time.Millisecond):
				}
			}
		}
		var observedSystem string
		var count int
		var value string
		if err := connection.QueryRow(ctx, "SELECT system_identifier::text FROM pg_control_system()").Scan(&observedSystem); err != nil {
			return nil, err
		}
		if err := connection.QueryRow(ctx, "SELECT count(*),min(value) FROM physical_adapter_regression").Scan(&count, &value); err != nil {
			return nil, err
		}
		if observedSystem != actual.SystemID || count != 1 || value != "acknowledged-before-frontier" {
			return nil, fmt.Errorf("explicit PostgreSQL frontier not recovered")
		}
		return []providerrestore.DatabaseResult{{DatabaseRole: point.DatabaseRole, ClusterIdentity: point.ClusterIdentity, DatabaseIdentity: point.DatabaseIdentity, RecoveryIdentity: point.RecoveryIdentity, StateDigest: digestBytes([]byte(value))}}, nil
	}
	restorer, err := NewPGBackRest(PGBackRestConfig{TargetID: "isolated-target", RecoverySetID: "isolated-set", Points: []recoveryset.ClusterRecoveryPoint{point}, Frontier: frontier, PGBackRest: program, ConfigFile: configFile, ConfigDigest: digestBytes(contents), Destination: filepath.Join(base, "replacement"), Readback: readback})
	if err != nil {
		t.Fatal(err)
	}
	request := providerrestore.DatabaseRequest{TargetID: "isolated-target", RecoverySetID: "isolated-set", Points: []recoveryset.ClusterRecoveryPoint{point}, IdempotencyKey: "physical-pgbackrest-regression"}
	if _, err := restorer.RestoreCluster(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := restorer.RestoreCluster(ctx, request); err != nil {
		t.Fatal(err)
	}
}
