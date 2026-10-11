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
	for _, layout := range []string{"default", "backup_tablespace", "wal_tablespace"} {
		t.Run(layout, func(t *testing.T) { actualPinnedPGBackRestFrontier(t, layout) })
	}
}

func actualPinnedPGBackRestFrontier(t *testing.T, layout string) {
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
	// The retained base becomes read-only during confined restore; locks must
	// be private to this fixture and creatable in the sandbox's private /tmp.
	lockRoot, err := os.MkdirTemp("/tmp", "lv-pg-lock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(lockRoot) })
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
	contents := []byte(fmt.Sprintf("[global]\nrepo1-path=%s\nlock-path=%s\nrepo1-retention-full=2\nstart-fast=y\nprocess-max=2\narchive-timeout=60\nlog-level-console=off\nlog-level-file=off\n[managed]\npg1-path=%s\npg1-port=%d\npg1-socket-path=%s\npg1-user=%s\n", repo, lockRoot, source, port, socket, owner.Username))
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
	originalTablespace := filepath.Join(base, "original-tablespace")
	createTablespace := func() {
		t.Helper()
		if err := os.MkdirAll(originalTablespace, 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(ctx, "CREATE TABLESPACE managed_custom LOCATION '"+originalTablespace+"'"); err != nil {
			t.Fatal(err)
		}
		if _, err := control.Exec(ctx, "CREATE TABLE external_adapter_regression(id integer) TABLESPACE managed_custom"); err != nil {
			t.Fatal(err)
		}
	}
	if layout == "backup_tablespace" {
		createTablespace()
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
	if layout == "wal_tablespace" {
		createTablespace()
	}
	if _, err := control.Exec(ctx, "INSERT INTO physical_adapter_regression VALUES(1,'acknowledged-before-frontier')"); err != nil {
		t.Fatal(err)
	}
	var lsn, systemID string
	var timeline uint32
	if err := admin.QueryRow(ctx, "SELECT pg_current_wal_insert_lsn()::text,(SELECT system_identifier::text FROM pg_control_system()),(SELECT timeline_id FROM pg_control_checkpoint())").Scan(&lsn, &systemID, &timeline); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Exec(ctx, "INSERT INTO physical_adapter_regression VALUES(2,'after-selected-frontier')"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "SELECT pg_switch_wal()"); err != nil {
		t.Fatal(err)
	}
	run(ctx, program, "--config="+configFile, "--stanza=managed", "check")
	frontier := PGFrontier{Stanza: "managed", BackupSet: info[0].Backup[0].Label, TargetLSN: lsn, SystemID: systemID, Timeline: timeline}
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
	if layout != "default" {
		if err := os.RemoveAll(originalTablespace); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(originalTablespace, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(originalTablespace, "outside-canary"), []byte("original filesystem must stay untouched"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	originalDigest, err := originalPGTreeDigest(source)
	if err != nil {
		t.Fatal(err)
	}
	var tablespaceManifest FileManifest
	if layout != "default" {
		tablespaceManifest, err = CaptureFiles(originalTablespace)
		if err != nil {
			t.Fatal(err)
		}
	}
	readbacks := 0
	readback := func(ctx context.Context, cluster *PGStagingCluster, actual PGFrontier, request providerrestore.DatabaseRequest) ([]providerrestore.DatabaseResult, error) {
		readbacks++
		path := cluster.Directory()
		command, err := cluster.PostgresCommand(ctx, filepath.Join(pgbin, "postgres"), "-c", "unix_socket_directories="+path)
		if err != nil {
			return nil, err
		}
		log, err := os.OpenFile(filepath.Join(path, "managed-regression-"+strconv.Itoa(readbacks)+".log"), os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		defer log.Close()
		command.Stdout, command.Stderr = log, log
		if err := command.Start(); err != nil {
			return nil, err
		}
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		exited := false
		defer func() {
			if !exited {
				<-done
			}
		}()
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(cleanup, pgctl, "-D", path, "-m", "fast", "-w", "stop")
			_ = command.Run()
		}()
		var connection *pgx.Conn
		for {
			connection, err = pgx.Connect(ctx, fmt.Sprintf("host=%s port=%d user=%s dbname=control sslmode=disable", path, port, owner.Username))
			if err == nil {
				break
			}
			select {
			case <-done:
				exited = true
				return nil, fmt.Errorf("confined PostgreSQL terminated before readback")
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
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
	restorer, err := NewPGBackRest(PGBackRestConfig{TargetID: "isolated-target", RecoverySetID: "isolated-set", Points: []recoveryset.ClusterRecoveryPoint{point}, Frontier: frontier, PGBackRest: program, Bubblewrap: os.Getenv("LEAPVIEW_TEST_MANAGED_BWRAP"), ConfigFile: configFile, ConfigDigest: digestBytes(contents), Destination: filepath.Join(base, "replacement"), Readback: readback})
	if err != nil {
		t.Fatal(err)
	}
	request := providerrestore.DatabaseRequest{TargetID: "isolated-target", RecoverySetID: "isolated-set", Points: []recoveryset.ClusterRecoveryPoint{point}, IdempotencyKey: "physical-pgbackrest-regression"}
	_, restoreErr := restorer.RestoreCluster(ctx, request)
	if layout != "default" {
		if err := tablespaceManifest.Verify(originalTablespace); err != nil {
			t.Fatal("restore or WAL replay wrote outside private staging")
		}
		if restoreErr == nil {
			t.Fatal("unsupported tablespace layout was accepted")
		}
		if _, err := os.Lstat(restorer.config.Destination); !os.IsNotExist(err) {
			t.Fatal("unsupported tablespace exposed replacement")
		}
		if layout == "backup_tablespace" && readbacks != 0 {
			t.Fatal("unsupported backup tablespace reached PostgreSQL launcher")
		}
	} else {
		if restoreErr != nil {
			t.Fatal(restoreErr)
		}
		if _, err := restorer.RestoreCluster(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	if after, err := originalPGTreeDigest(source); err != nil || after != originalDigest {
		t.Fatal("restoration changed the retained original PGDATA")
	}
}

// Unlike the supported managed content profile, this negative fixture contains
// deliberate tablespace symlinks. Hash their exact link text without following
// them, and all regular original files, to detect any provider-side mutation.
func originalPGTreeDigest(root string) (string, error) {
	projection := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			projection[name] = "link:" + link
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unexpected original PostgreSQL file")
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		projection[name] = digestBytes(contents)
		return nil
	})
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		return "", err
	}
	return digestBytes(encoded), nil
}
