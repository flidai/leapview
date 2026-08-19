package ducklake

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
)

var errIntentionalFailure = errors.New("intentional failure")

func TestLayoutUsesOneCatalogAndDataStore(t *testing.T) {
	layout := NewLayout(filepath.Join("tmp", "env"))

	if layout.CatalogPath != filepath.Join("tmp", "env", "catalog.duckdb") {
		t.Fatalf("CatalogPath = %q", layout.CatalogPath)
	}
	if layout.DataPath != filepath.Join("tmp", "env", "data") {
		t.Fatalf("DataPath = %q", layout.DataPath)
	}
	if _, ok := reflect.TypeOf(layout).FieldByName("LegacyDuckDBPath"); ok {
		t.Fatal("Layout still exposes LegacyDuckDBPath")
	}
}

func TestOpenMigratesSiblingLegacySQLiteCatalog(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "ducklake")
	legacyPath := filepath.Join(root, "catalog.sqlite")
	targetPath := filepath.Join(root, "catalog.duckdb")
	dataPath := filepath.Join(root, "data")
	createLegacySQLiteCatalog(t, ctx, legacyPath, dataPath)

	env, err := Open(ctx, admittedConfig(t, Config{RootDir: root, CatalogPath: targetPath, DataPath: dataPath}, "ducklake", "sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Close() })
	assertMigratedCatalogReadWrite(t, ctx, env)
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("legacy catalog backup missing: %v", err)
	}
}

func TestOpenMigratesConfiguredSQLiteCatalogInPlace(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "ducklake")
	catalogPath := filepath.Join(root, "metadata.sqlite")
	dataPath := filepath.Join(root, "data")
	createLegacySQLiteCatalog(t, ctx, catalogPath, dataPath)

	env, err := Open(ctx, admittedConfig(t, Config{RootDir: root, CatalogPath: catalogPath, DataPath: dataPath}, "ducklake", "sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Close() })
	assertMigratedCatalogReadWrite(t, ctx, env)
	if _, err := os.Stat(catalogPath + ".legacy.sqlite"); err != nil {
		t.Fatalf("in-place legacy catalog backup missing: %v", err)
	}
	if sqlite, exists, err := sqliteCatalogFile(catalogPath); err != nil || !exists || sqlite {
		t.Fatalf("migrated configured catalog: sqlite=%t exists=%t error=%v", sqlite, exists, err)
	}
}

func createLegacySQLiteCatalog(t *testing.T, ctx context.Context, catalogPath, dataPath string) {
	t.Helper()
	if err := os.MkdirAll(dataPath, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	legacyAttach := fmt.Sprintf("ATTACH 'ducklake:sqlite:%s' AS legacy (DATA_PATH '%s')", sqlLiteral(catalogPath), sqlLiteral(dataPath))
	for _, statement := range []string{
		"LOAD ducklake",
		"LOAD sqlite",
		legacyAttach,
		"CREATE SCHEMA legacy.model",
		"CREATE TABLE legacy.model.orders(id BIGINT, amount DOUBLE)",
		"INSERT INTO legacy.model.orders VALUES (1, 10.5), (2, 20.25)",
	} {
		if _, err := legacy.ExecContext(ctx, statement); err != nil {
			_ = legacy.Close()
			t.Fatalf("legacy statement %q: %v", statement, err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertMigratedCatalogReadWrite(t *testing.T, ctx context.Context, env *Environment) {
	t.Helper()
	var count int
	if err := env.db.QueryRowContext(ctx, "SELECT count(*) FROM lake.model.orders").Scan(&count); err != nil {
		t.Fatalf("query migrated DuckLake table: %v", err)
	}
	if count != 2 {
		t.Fatalf("migrated row count = %d, want 2", count)
	}
	if _, err := env.Commit(ctx, "post_migration", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO model.orders VALUES (3, 30.75)")
		return err
	}); err != nil {
		t.Fatalf("commit after catalog migration: %v", err)
	}
	if err := env.db.QueryRowContext(ctx, "SELECT count(*) FROM lake.model.orders").Scan(&count); err != nil {
		t.Fatalf("query after post-migration commit: %v", err)
	}
	if count != 3 {
		t.Fatalf("post-migration row count = %d, want 3", count)
	}
}

func TestOpenCreatesPrivateCatalogAndDataDirectories(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	root := filepath.Join(parent, "ducklake")
	restoreUmask := setUmask(t, 0)
	env, err := Open(ctx, admittedConfig(t, Config{RootDir: root}))
	restoreUmask()
	if extensionUnavailable(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer env.Close()

	assertFileMode(t, root, 0o700)
	assertFileMode(t, filepath.Join(root, "data"), 0o700)
}

func TestEnvironmentCommitsAndReadsStableSnapshots(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	env, err := Open(ctx, admittedConfig(t, Config{RootDir: dir}))
	if extensionUnavailable(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer env.Close()

	snapshot1, err := env.Commit(ctx, "dep_1", map[string]string{"workspace": "test"}, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS model"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "CREATE OR REPLACE TABLE model.orders AS SELECT 1 AS id, 'first' AS label")
		return err
	})
	if err != nil {
		t.Fatalf("commit first snapshot: %v", err)
	}
	if snapshot1 <= 0 {
		t.Fatalf("snapshot1 = %d, want positive committed snapshot", snapshot1)
	}

	snapshot2, err := env.Commit(ctx, "dep_2", map[string]string{"workspace": "test"}, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE OR REPLACE TABLE model.orders AS SELECT 2 AS id, 'second' AS label")
		return err
	})
	if err != nil {
		t.Fatalf("commit second snapshot: %v", err)
	}
	if snapshot2 <= snapshot1 {
		t.Fatalf("snapshot2 = %d, want > snapshot1 %d", snapshot2, snapshot1)
	}

	assertOrder := func(t *testing.T, snapshotID int64, wantID int, wantLabel string) {
		t.Helper()
		var gotID int
		var gotLabel string
		query := "SELECT id, label FROM " + SnapshotRelation(snapshotID, "orders")
		if err := env.sqlDB().QueryRowContext(ctx, query).Scan(&gotID, &gotLabel); err != nil {
			t.Fatalf("query order: %v", err)
		}
		if gotID != wantID || gotLabel != wantLabel {
			t.Fatalf("order = (%d, %q), want (%d, %q)", gotID, gotLabel, wantID, wantLabel)
		}
	}
	assertOrder(t, snapshot1, 1, "first")
	assertOrder(t, snapshot2, 2, "second")

	if _, err := os.Stat(filepath.Join(dir, "catalog.duckdb")); err != nil {
		t.Fatalf("catalog.duckdb missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "data")); err != nil {
		t.Fatalf("data dir missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "leapview-workspace.duckdb")); !os.IsNotExist(err) {
		t.Fatalf("legacy DuckDB workspace file exists or stat failed: %v", err)
	}
}

func TestValidateSnapshotRejectsMissingSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	env, err := Open(ctx, admittedConfig(t, Config{RootDir: dir}))
	if extensionUnavailable(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer env.Close()

	if _, err := env.Commit(ctx, "dep_1", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE TABLE model_check AS SELECT 1 AS id")
		return err
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if err := env.ValidateSnapshot(ctx, 999); err == nil {
		t.Fatal("ValidateSnapshot missing snapshot error = nil")
	}
}

func TestFailedCommitDoesNotAdvanceVisibleSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	env, err := Open(ctx, admittedConfig(t, Config{RootDir: dir}))
	if extensionUnavailable(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer env.Close()

	snapshot1, err := env.Commit(ctx, "dep_1", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE TABLE model_orders AS SELECT 1 AS id")
		return err
	})
	if err != nil {
		t.Fatalf("commit first snapshot: %v", err)
	}
	if _, err := env.Commit(ctx, "dep_fail", nil, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "CREATE OR REPLACE TABLE model_orders AS SELECT 2 AS id"); err != nil {
			return err
		}
		return errIntentionalFailure
	}); !errors.Is(err, errIntentionalFailure) {
		t.Fatalf("failed commit error = %v, want intentional failure", err)
	}
	snapshots, err := env.Snapshots(ctx)
	if err != nil {
		t.Fatalf("snapshots: %v", err)
	}
	for _, snapshot := range snapshots {
		if snapshot.ID > snapshot1 {
			t.Fatalf("snapshots = %#v, want no committed snapshot after %d", snapshots, snapshot1)
		}
	}
	var id int
	if err := env.sqlDB().QueryRowContext(ctx, "SELECT id FROM model_orders").Scan(&id); err != nil {
		t.Fatalf("query visible table: %v", err)
	}
	if id != 1 {
		t.Fatalf("visible id = %d, want first committed value", id)
	}
}

func TestRetentionCandidatesPreserveProtectedSnapshots(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	env, err := Open(ctx, admittedConfig(t, Config{RootDir: dir}))
	if extensionUnavailable(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer env.Close()

	snapshot1, err := env.Commit(ctx, "dep_1", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE TABLE model_orders AS SELECT 1 AS id")
		return err
	})
	if err != nil {
		t.Fatalf("commit first: %v", err)
	}
	snapshot2, err := env.Commit(ctx, "dep_2", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE OR REPLACE TABLE model_orders AS SELECT 2 AS id")
		return err
	})
	if err != nil {
		t.Fatalf("commit second: %v", err)
	}

	candidates, err := env.RetentionCandidates(ctx, map[int64]struct{}{snapshot2: {}})
	if err != nil {
		t.Fatalf("retention candidates: %v", err)
	}
	if len(candidates) != 1 || candidates[0] != snapshot1 {
		t.Fatalf("candidates = %#v, want only %d", candidates, snapshot1)
	}
}

func TestMaintenanceDryRunsUseDuckLakeMetadata(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	env, err := Open(ctx, admittedConfig(t, Config{RootDir: dir}))
	if extensionUnavailable(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer env.Close()

	snapshot1, err := env.Commit(ctx, "dep_1", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE TABLE model_orders AS SELECT 1 AS id")
		return err
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := env.ExpireSnapshots(ctx, []int64{snapshot1}, true); err != nil {
		t.Fatalf("expire dry run: %v", err)
	}
	if err := env.CleanupOldFiles(ctx, true); err != nil {
		t.Fatalf("cleanup dry run: %v", err)
	}
	if err := env.DeleteOrphanedFiles(ctx, true); err != nil {
		t.Fatalf("orphan dry run: %v", err)
	}
	snapshots, err := env.Snapshots(ctx)
	if err != nil {
		t.Fatalf("snapshots after dry run: %v", err)
	}
	if len(snapshots) == 0 {
		t.Fatal("dry-run maintenance removed snapshots")
	}
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

func setUmask(t *testing.T, mask int) func() {
	t.Helper()
	old := syscall.Umask(mask)
	return func() {
		syscall.Umask(old)
	}
}
