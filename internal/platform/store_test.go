package platform

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/app/testing/extensionfixture"
)

func TestStoreOpenMakesDatabasePrivate(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "leapview.db")
	if err := os.WriteFile(dbPath, nil, 0o644); err != nil {
		t.Fatalf("seed world-readable db file: %v", err)
	}
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	assertFileMode(t, dbPath, 0o600)
	assertExistingSQLiteSidecarsPrivate(t, dbPath)
}

func TestChmodDatabaseFileMakesSQLiteSidecarsPrivate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "leapview.db")
	paths := []string{dbPath, dbPath + "-wal", dbPath + "-shm"}
	restoreUmask := setUmask(t, 0)
	for _, path := range paths {
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			restoreUmask()
			t.Fatalf("seed sqlite file %s: %v", path, err)
		}
	}
	restoreUmask()

	if err := chmodDatabaseFile(dbPath); err != nil {
		t.Fatalf("chmod sqlite files: %v", err)
	}
	for _, path := range paths {
		assertFileMode(t, path, 0o600)
	}
}

func TestStoreUsesPerOperationConnectionsAndDrainsOnClose(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Ping(t.Context()); err != nil {
		t.Fatalf("ping store: %v", err)
	}
	stats := store.SQLDB().Stats()
	if stats.OpenConnections != 0 || stats.Idle != 0 {
		t.Fatalf("connections after operation = %d idle = %d, want per-operation connections", stats.OpenConnections, stats.Idle)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store twice: %v", err)
	}
	stats = store.SQLDB().Stats()
	if stats.OpenConnections != 0 {
		t.Fatalf("connections after close = %d, want 0", stats.OpenConnections)
	}
}

func TestStorePingReportsOpenState(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("ping open store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if err := store.Ping(ctx); err == nil {
		t.Fatal("expected closed store ping to fail")
	}
}

func TestStoreAndDuckLakeUseSeparateCatalogs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "leapview.db")
	catalogPath := filepath.Join(dir, "ducklake", "catalog.duckdb")
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	const settingKey = "test.catalog-separation"
	const settingValue = "preserved"
	if err := store.UpsertSetting(ctx, settingKey, settingValue); err != nil {
		t.Fatalf("seed platform setting: %v", err)
	}

	admission := extensionfixture.New(t, "ducklake")
	env, err := analyticsducklake.Open(ctx, analyticsducklake.Config{
		RootDir:            filepath.Dir(catalogPath),
		CatalogPath:        catalogPath,
		DataPath:           filepath.Join(dir, "ducklake", "data"),
		ExtensionAdmission: admission.Admission,
	})
	if err != nil {
		t.Fatalf("open DuckLake on platform catalog: %v", err)
	}
	defer env.Close()
	if _, err := env.Commit(ctx, "dep_1", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE TABLE model_orders AS SELECT 1 AS id")
		return err
	}); err != nil {
		t.Fatalf("commit DuckLake snapshot: %v", err)
	}
	value, err := store.GetSetting(ctx, settingKey)
	if err != nil {
		t.Fatalf("read platform setting after DuckLake commit: %v", err)
	}
	if value != settingValue {
		t.Fatalf("platform setting after DuckLake commit = %q, want %q", value, settingValue)
	}
	if _, err := os.Stat(catalogPath); err != nil {
		t.Fatalf("separate DuckLake catalog was not created: %v", err)
	}
}

func TestStoreUsesServingStateSchemaTerminology(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	tables := map[string]bool{}
	rows, err := store.SQLDB().QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		tables[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table rows: %v", err)
	}
	for _, name := range []string{"serving_states", "project_active_serving_states", "serving_state_artifacts"} {
		if !tables[name] {
			t.Fatalf("missing canonical serving-state table %q; tables=%v", name, tables)
		}
	}
	for _, name := range []string{"deployments", "workspace_active_deployments", "deployment_artifacts"} {
		if tables[name] {
			t.Fatalf("legacy deployment table %q should not be created", name)
		}
	}

	var cleanupAfterCount int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('serving_states') WHERE name = 'cleanup_after'`).Scan(&cleanupAfterCount); err != nil {
		t.Fatalf("inspect serving_states columns: %v", err)
	}
	if cleanupAfterCount != 0 {
		t.Fatal("serving_states.cleanup_after should not be part of the canonical schema")
	}
}

func duckLakeExtensionUnavailable(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "extension") &&
		(strings.Contains(text, "not found") ||
			strings.Contains(text, "failed to download") ||
			strings.Contains(text, "failed to install") ||
			strings.Contains(text, "not be loaded"))
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %s = %#o, want %#o", path, got, want)
	}
}

func assertExistingSQLiteSidecarsPrivate(t *testing.T, dbPath string) {
	t.Helper()
	for _, suffix := range []string{"-wal", "-shm"} {
		path := dbPath + suffix
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		assertFileMode(t, path, 0o600)
	}
}

func setUmask(t *testing.T, mask int) func() {
	t.Helper()
	old := syscall.Umask(mask)
	return func() {
		syscall.Umask(old)
	}
}
