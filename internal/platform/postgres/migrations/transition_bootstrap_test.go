package migrations

import (
	"database/sql"
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestTransitionBootstrapUpgradeAndCurrentPaths(t *testing.T) {
	harness := postgrestest.Start(t)
	owner := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Password: "bootstrap-test", Login: true})
	for _, name := range []string{"leapview_control_runtime", "leapview_control_maintenance", "leapview_control_readonly", "leapview_control_backup"} {
		harness.EnsureRole(t, postgrestest.Role{Name: name})
	}
	harness.GrantRole(t, owner, migrator)

	openDatabase := func(name string) (*pgxpool.Pool, *sql.DB) {
		database := harness.NewDatabase(t, name)
		harness.GrantDatabase(t, database.Name, owner, "CREATE")
		harness.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")
		pool, err := pgxpool.New(t.Context(), database.AdminURL())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		if _, err := pool.Exec(t.Context(), `ALTER DATABASE `+database.Name+` OWNER TO leapview_control_owner; REVOKE ALL ON SCHEMA public FROM PUBLIC; GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator`); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("pgx", database.URL(migrator))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return pool, db
	}
	readRevision := func(db *sql.DB) int64 {
		var revision int64
		if err := db.QueryRowContext(t.Context(), `SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1`).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		return revision
	}

	previous := fstest.MapFS{}
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		revision, parseErr := strconv.Atoi(prefix)
		if !ok || parseErr != nil {
			t.Fatalf("invalid migration name %s", entry.Name())
		}
		if revision > 19 {
			continue
		}
		contents, err := fs.ReadFile(MigrationFS(), entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		previous[entry.Name()] = &fstest.MapFile{Data: contents}
	}
	if len(previous) != 19 {
		t.Fatalf("previous migrations = %d, want 19", len(previous))
	}

	upgradePool, upgradeDB := openDatabase("bootstrap_upgrade")
	provider, err := newProvider(upgradeDB, previous)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := readRevision(upgradeDB); got != 19 {
		t.Fatalf("previous revision = %d, want 19", got)
	}
	if err := BootstrapTransitionOperation(t.Context(), upgradePool, upgradeDB); err != nil {
		t.Fatal(err)
	}
	if got := readRevision(upgradeDB); got != 19 {
		t.Fatalf("bootstrap revision = %d, want 19", got)
	}
	assertTransitionTruncateTriggers(t, upgradeDB, "_bootstrap", true)
	if err := BootstrapTransitionOperation(t.Context(), upgradePool, upgradeDB); err != nil {
		t.Fatalf("repeat bootstrap at revision 019: %v", err)
	}
	assertTransitionTruncateTriggers(t, upgradeDB, "_bootstrap", true)
	if err := ApplyGoose(t.Context(), upgradeDB); err != nil {
		t.Fatalf("apply migration 020 after bootstrap: %v", err)
	}
	if got := readRevision(upgradeDB); got != CurrentRevision {
		t.Fatalf("upgraded revision = %d, want %d", got, CurrentRevision)
	}
	assertTransitionTruncateTriggers(t, upgradeDB, "", true)
	assertTransitionTruncateTriggers(t, upgradeDB, "_bootstrap", true)
	if err := BootstrapTransitionOperation(t.Context(), upgradePool, upgradeDB); err != nil {
		t.Fatalf("already-current bootstrap: %v", err)
	}

	freshPool, freshDB := openDatabase("bootstrap_fresh")
	if err := ApplyGoose(t.Context(), freshDB); err != nil {
		t.Fatalf("fresh migrations: %v", err)
	}
	if got := readRevision(freshDB); got != CurrentRevision {
		t.Fatalf("fresh revision = %d, want %d", got, CurrentRevision)
	}
	assertTransitionTruncateTriggers(t, freshDB, "", true)
	assertTransitionTruncateTriggers(t, freshDB, "_bootstrap", false)
	if err := BootstrapTransitionOperation(t.Context(), freshPool, freshDB); err != nil {
		t.Fatalf("fresh current bootstrap: %v", err)
	}
}

func assertTransitionTruncateTriggers(t *testing.T, db *sql.DB, suffix string, want bool) {
	t.Helper()
	for _, name := range []string{
		"release_transition_operation_no_truncate",
		"release_transition_phase_no_truncate",
		"release_transition_fence_no_truncate",
	} {
		var exists bool
		if err := db.QueryRowContext(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = $1 AND NOT tgisinternal)`, name+suffix).Scan(&exists); err != nil || exists != want {
			t.Fatalf("truncate trigger %s present = %t, error = %v; want %t", name+suffix, exists, err, want)
		}
	}
}
