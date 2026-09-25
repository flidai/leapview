//go:build linux

package demoupgrade

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/postgresbaseline"
	"github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// This test owns every named resource it creates. It uses no demo host, existing
// container, real credential, or network-visible port. Root is needed only to
// preserve the PostgreSQL volume's native UID/GID through the cold copy.
func TestColdPostgreSQLPairRecovery(t *testing.T) {
	if os.Getenv("LEAPVIEW_DEMO_UPGRADE_QUALIFICATION") != "1" {
		t.Skip("set LEAPVIEW_DEMO_UPGRADE_QUALIFICATION=1 for isolated physical restore qualification")
	}
	if os.Geteuid() != 0 {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command("sudo", "-n", "env", "LEAPVIEW_DEMO_UPGRADE_QUALIFICATION=1", executable, "-test.run=^TestColdPostgreSQLPairRecovery$", "-test.v", "-test.timeout=5m")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("root restore rehearsal: %v\n%s", err, output)
		} else {
			t.Log(string(output))
		}
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	docker := func(args ...string) string {
		t.Helper()
		output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("fixture Docker %s: %v\n%s", args[0], err, output)
		}
		return strings.TrimSpace(string(output))
	}
	const image = "docker.io/library/postgres:18-alpine@sha256:63bdc97d67b5133bf0e5ebd500bec6d046fa851dc81340d838f0347e616107e8"
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	original := "leapview-upgrade-test-" + suffix
	clone := original + "-restore"
	volume := original + "-pg"

	root := t.TempDir()
	t.Cleanup(func() {
		for _, name := range []string{clone, original} {
			_ = exec.Command("docker", "rm", "-f", name).Run()
		}
		_ = exec.Command("docker", "volume", "rm", volume).Run()
	})
	docker("volume", "create", volume)
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "managed-data"), []byte("coordinated CFO files"), 0600); err != nil {
		t.Fatal(err)
	}
	start := func(name, mount string) {
		docker("run", "-d", "--name", name, "--restart=no", "--network", "bridge", "-p", "127.0.0.1::5432", "-e", "POSTGRES_PASSWORD=isolated-test-only", "--mount", mount, image)
	}
	wait := func(name string) {
		for attempt := 0; attempt < 150; attempt++ {
			if exec.CommandContext(ctx, "docker", "exec", name, "pg_isready", "-h", "127.0.0.1", "-U", "postgres").Run() == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("fixture PostgreSQL readiness timed out")
	}
	connection := func(name, db string) string {
		var info []struct {
			NetworkSettings struct {
				Ports map[string][]struct{ HostPort string }
			}
		}
		if err := json.Unmarshal([]byte(docker("inspect", name)), &info); err != nil {
			t.Fatal(err)
		}
		port := info[0].NetworkSettings.Ports["5432/tcp"][0].HostPort
		return (&url.URL{Scheme: "postgres", User: url.UserPassword("postgres", "isolated-test-only"), Host: "127.0.0.1:" + port, Path: "/" + db, RawQuery: "sslmode=disable"}).String()
	}
	migrationConnection := func(name string) string {
		parsed, err := url.Parse(connection(name, "leapview_control"))
		if err != nil {
			t.Fatal(err)
		}
		parsed.User = url.UserPassword("leapview_control_migrator", "isolated-migrator-only")
		return parsed.String()
	}
	start(original, "type=volume,source="+volume+",target=/var/lib/postgresql")
	wait(original)
	admin, err := pgxpool.New(ctx, connection(original, "postgres"))
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"owner", "migrator", "runtime", "maintenance", "readonly", "backup"} {
		if _, err = admin.Exec(ctx, "CREATE ROLE leapview_control_"+role); err != nil {
			t.Fatal(err)
		}
	}
	for _, db := range []string{"leapview_control", "leapview_ducklake"} {
		if _, err = admin.Exec(ctx, "CREATE DATABASE "+db+" OWNER leapview_control_owner"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = admin.Exec(ctx, "ALTER ROLE leapview_control_migrator LOGIN NOINHERIT PASSWORD 'isolated-migrator-only'; GRANT leapview_control_owner TO leapview_control_migrator"); err != nil {
		t.Fatal(err)
	}
	admin.Close()
	provision, err := pgxpool.New(ctx, connection(original, "leapview_control"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provision.Exec(ctx, "GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator"); err != nil {
		t.Fatal(err)
	}
	defer provision.Close()
	control, err := pgxpool.New(ctx, migrationConnection(original))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := sql.Open("pgx", migrationConnection(original))
	if err != nil {
		t.Fatal(err)
	}
	if err = migrations.ApplyRiver(ctx, control); err != nil {
		t.Fatal(err)
	}
	provider, err := migrations.NewProvider(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.UpTo(ctx, 28); err != nil {
		t.Fatal(err)
	}
	if _, err = provision.Exec(ctx, `INSERT INTO platform.setting(key,value) VALUES('recovery-proof','before-upgrade')`); err != nil {
		t.Fatal(err)
	}
	control.Close()
	sqlDB.Close()
	provision.Close()
	docker("exec", original, "psql", "-U", "postgres", "-d", "leapview_ducklake", "-v", "ON_ERROR_STOP=1", "-c", "CREATE TABLE recovery_marker(value text); INSERT INTO recovery_marker VALUES ('paired-catalog-state');")
	docker("stop", "--time", "30", original)
	pgPath := docker("volume", "inspect", volume, "--format", "{{.Mountpoint}}")
	backup := filepath.Join(root, "backups", "point")
	digest, err := CaptureStoppedDirectories(ctx, backup, "fixture", map[string]string{"postgres": pgPath, "home": home})
	if err != nil {
		t.Fatal(err)
	}
	restoredPG := filepath.Join(root, "restored-pg")
	restoredHome := filepath.Join(root, "restored-home")
	restored := map[string]string{"postgres": restoredPG, "home": restoredHome}
	if err = RestoreStoppedDirectories(ctx, backup, "fixture", digest, restored); err != nil {
		t.Fatal(err)
	}
	start(clone, "type=bind,source="+restoredPG+",target=/var/lib/postgresql")
	wait(clone)
	verify := func(want int64) {
		pool, err := pgxpool.New(ctx, connection(clone, "leapview_control"))
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		var revision int64
		var marker string
		if err = pool.QueryRow(ctx, "SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1").Scan(&revision); err != nil || revision != want {
			t.Fatalf("restored schema %d want %d: %v", revision, want, err)
		}
		if err = pool.QueryRow(ctx, "SELECT value FROM platform.setting WHERE key='recovery-proof'").Scan(&marker); err != nil || marker != "before-upgrade" {
			t.Fatalf("control state lost: %q %v", marker, err)
		}
		got := docker("exec", clone, "psql", "-U", "postgres", "-d", "leapview_ducklake", "-Atc", "SELECT value FROM recovery_marker")
		if got != "paired-catalog-state" {
			t.Fatal("catalog state lost:", got)
		}
		data, err := os.ReadFile(filepath.Join(restoredHome, "managed-data"))
		if err != nil || string(data) != "coordinated CFO files" {
			t.Fatalf("managed files lost: %s %v", data, err)
		}
	}
	verify(28)
	upgraded, err := pgxpool.New(ctx, migrationConnection(clone))
	if err != nil {
		t.Fatal(err)
	}
	upgradeSQL, err := sql.Open("pgx", migrationConnection(clone))
	if err != nil {
		t.Fatal(err)
	}
	if err = postgresbaseline.ApplyDemoUpgrade(ctx, upgraded, upgradeSQL, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	upgraded.Close()
	upgradeSQL.Close()
	verify(30)
	docker("exec", clone, "psql", "-U", "postgres", "-d", "leapview_ducklake", "-c", "UPDATE recovery_marker SET value='failed-candidate';")
	os.WriteFile(filepath.Join(restoredHome, "managed-data"), []byte("failed-candidate"), 0600)
	docker("stop", "--time", "30", clone)
	if err = RestoreStoppedDirectories(ctx, backup, "fixture", digest, restored); err != nil {
		t.Fatal(err)
	}
	docker("start", clone)
	wait(clone)
	verify(28)
	t.Log(fmt.Sprintf("verified cold pair restore, real Goose 28 -> 30, then paired recovery to 28; snapshot %s", digest))
}
