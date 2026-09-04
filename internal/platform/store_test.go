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

func TestStoreBackupCreatesReadableSQLiteCopy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, filepath.Join(dir, "leapview.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	backupPath := filepath.Join(dir, "backups", "leapview.backup.db")
	if err := store.Backup(ctx, backupPath); err != nil {
		t.Fatalf("backup store: %v", err)
	}
	if info, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup file missing: %v", err)
	} else if info.Size() == 0 {
		t.Fatal("backup file is empty")
	}
	backup, err := sql.Open("sqlite", backupPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer backup.Close()
	var tableCount int
	if err := backup.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'platform_settings'`).Scan(&tableCount); err != nil {
		t.Fatalf("query backup schema: %v", err)
	}
	if tableCount != 1 {
		t.Fatalf("backup platform settings tables = %d, want 1", tableCount)
	}
}

func TestStoreBackupCreatesPrivateDatabaseCopy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, filepath.Join(dir, "leapview.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	backupDir := filepath.Join(dir, "backups")
	if err := os.Mkdir(backupDir, 0o755); err != nil {
		t.Fatalf("seed backup directory: %v", err)
	}
	restoreUmask := setUmask(t, 0)
	backupPath := filepath.Join(backupDir, "leapview.backup.db")
	if err := store.Backup(ctx, backupPath); err != nil {
		restoreUmask()
		t.Fatalf("backup store: %v", err)
	}
	restoreUmask()
	assertFileMode(t, filepath.Dir(backupPath), 0o700)
	assertFileMode(t, backupPath, 0o600)
}

func TestStoreBackupRefusesOverwrite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, filepath.Join(dir, "leapview.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	backupPath := filepath.Join(dir, "leapview.backup.db")
	if err := os.WriteFile(backupPath, []byte("existing"), 0o644); err != nil {
		t.Fatalf("write existing backup: %v", err)
	}
	if err := store.Backup(ctx, backupPath); err == nil {
		t.Fatal("expected backup overwrite to fail")
	}
}

func TestRestoreReplacesPlatformDatabaseAndBacksUpCurrent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	currentPath := filepath.Join(dir, "current", "leapview.db")
	current, err := Open(ctx, currentPath)
	if err != nil {
		t.Fatalf("open current store: %v", err)
	}
	if err := current.UpsertSetting(ctx, "restore-test", "current"); err != nil {
		t.Fatalf("seed current setting: %v", err)
	}
	if err := current.Close(); err != nil {
		t.Fatalf("close current store: %v", err)
	}

	backupSource, err := Open(ctx, filepath.Join(dir, "source", "leapview.db"))
	if err != nil {
		t.Fatalf("open backup source: %v", err)
	}
	if err := backupSource.UpsertSetting(ctx, "restore-test", "restored"); err != nil {
		t.Fatalf("seed backup setting: %v", err)
	}
	backupPath := filepath.Join(dir, "backups", "leapview.restore.db")
	if err := backupSource.Backup(ctx, backupPath); err != nil {
		t.Fatalf("backup source: %v", err)
	}
	if err := backupSource.Close(); err != nil {
		t.Fatalf("close backup source: %v", err)
	}

	currentBackupPath := filepath.Join(dir, "backups", "leapview.before-restore.db")
	if err := Restore(ctx, currentPath, backupPath, currentBackupPath); err != nil {
		t.Fatalf("restore: %v", err)
	}

	restored, err := Open(ctx, currentPath)
	if err != nil {
		t.Fatalf("open restored store: %v", err)
	}
	value, err := restored.GetSetting(ctx, "restore-test")
	if err != nil {
		t.Fatalf("read restored setting: %v", err)
	}
	if value != "restored" {
		t.Fatalf("restored setting = %q, want restored", value)
	}
	if err := restored.Close(); err != nil {
		t.Fatalf("close restored store: %v", err)
	}

	before, err := Open(ctx, currentBackupPath)
	if err != nil {
		t.Fatalf("open before-restore backup: %v", err)
	}
	value, err = before.GetSetting(ctx, "restore-test")
	if err != nil {
		t.Fatalf("read before-restore setting: %v", err)
	}
	if value != "current" {
		t.Fatalf("before-restore setting = %q, want current", value)
	}
	if err := before.Close(); err != nil {
		t.Fatalf("close before-restore backup: %v", err)
	}
}

func TestRestoreRequiresCurrentBackupWhenTargetExists(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	currentPath := filepath.Join(dir, "leapview.db")
	store, err := Open(ctx, currentPath)
	if err != nil {
		t.Fatalf("open current store: %v", err)
	}
	backupPath := filepath.Join(dir, "backup.db")
	if err := store.Backup(ctx, backupPath); err != nil {
		t.Fatalf("backup store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close current store: %v", err)
	}
	err = Restore(ctx, currentPath, backupPath, "")
	if err == nil || !strings.Contains(err.Error(), "current backup path is required") {
		t.Fatalf("restore error = %v, want current backup path requirement", err)
	}
}

func TestRestoreRejectsInvalidBackupWithoutChangingCurrent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	currentPath := filepath.Join(dir, "leapview.db")
	store, err := Open(ctx, currentPath)
	if err != nil {
		t.Fatalf("open current store: %v", err)
	}
	if err := store.UpsertSetting(ctx, "restore-test", "current"); err != nil {
		t.Fatalf("seed current setting: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close current store: %v", err)
	}
	invalidBackup := filepath.Join(dir, "invalid.db")
	if err := os.WriteFile(invalidBackup, []byte("not sqlite"), 0o644); err != nil {
		t.Fatalf("write invalid backup: %v", err)
	}
	beforePath := filepath.Join(dir, "before.db")
	if err := Restore(ctx, currentPath, invalidBackup, beforePath); err == nil {
		t.Fatal("expected invalid backup restore to fail")
	}
	if _, err := os.Stat(beforePath); !os.IsNotExist(err) {
		t.Fatalf("before backup exists after invalid restore: %v", err)
	}
	current, err := Open(ctx, currentPath)
	if err != nil {
		t.Fatalf("reopen current store: %v", err)
	}
	value, err := current.GetSetting(ctx, "restore-test")
	if err != nil {
		t.Fatalf("read current setting: %v", err)
	}
	if value != "current" {
		t.Fatalf("current setting after invalid restore = %q, want current", value)
	}
	if err := current.Close(); err != nil {
		t.Fatalf("close current store: %v", err)
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
