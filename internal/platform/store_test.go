package platform

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"

	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/app/testing/extensionfixture"
	"github.com/flidai/leapview/internal/platform/locking"
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

func TestBackupInstanceArchivesDatabaseAndPersistentFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	dbPath := filepath.Join(home, "leapview.db")
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.UpsertSetting(ctx, "instance-backup-test", "db-value"); err != nil {
		t.Fatalf("seed setting: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	writeTestFile(t, filepath.Join(home, "artifacts", "dep_1.tar.gz"), "artifact")
	writeTestFile(t, filepath.Join(home, "data", "ducklake-file.parquet"), "ducklake-data")
	writeTestFile(t, filepath.Join(home, "runtime", "runtime-state"), "runtime")
	writeTestFile(t, filepath.Join(home, "managed-data", "objects", "blobs", "sha256", "ab", "blob"), "managed blob")
	writeTestFile(t, filepath.Join(home, "managed-data", "objects", "revisions", "revision", "data", "orders.csv"), "derived revision")
	writeTestFile(t, dbPath+"-wal", "stale wal sidecar")

	backupPath := filepath.Join(dir, "backups", "leapview-instance.tar.gz")
	if err := BackupInstance(ctx, InstanceBackupOptions{
		HomeDir: home, DBPath: dbPath, OutPath: backupPath,
		ExcludeRelativePaths: []string{"managed-data/objects/revisions"},
	}); err != nil {
		t.Fatalf("backup instance: %v", err)
	}

	entries := readTarGzEntries(t, backupPath)
	for _, want := range []string{
		instanceBackupManifestName,
		"leapview.db",
		"artifacts/dep_1.tar.gz",
		"data/ducklake-file.parquet",
		"runtime/runtime-state",
		"managed-data/objects/blobs/sha256/ab/blob",
	} {
		if _, ok := entries[want]; !ok {
			t.Fatalf("instance backup missing %q; entries=%v", want, sortedKeys(entries))
		}
	}
	if _, ok := entries["leapview.db-wal"]; ok {
		t.Fatalf("instance backup included sqlite WAL sidecar; entries=%v", sortedKeys(entries))
	}
	if _, ok := entries["managed-data/objects/revisions/revision/data/orders.csv"]; ok {
		t.Fatalf("instance backup included an excluded derived revision; entries=%v", sortedKeys(entries))
	}
	backupDBPath := filepath.Join(dir, "backup.db")
	if err := os.WriteFile(backupDBPath, entries["leapview.db"], 0o644); err != nil {
		t.Fatalf("write backup db: %v", err)
	}
	backupDB, err := sql.Open("sqlite", backupDBPath+"?_pragma=query_only(1)")
	if err != nil {
		t.Fatalf("open backup db: %v", err)
	}
	defer backupDB.Close()
	var value string
	if err := backupDB.QueryRowContext(ctx, `SELECT value FROM platform_settings WHERE key = 'instance-backup-test'`).Scan(&value); err != nil {
		t.Fatalf("read setting from backup db: %v", err)
	}
	if value != "db-value" {
		t.Fatalf("backup db setting = %q, want db-value", value)
	}
}

func TestOpenRejectsV010InstanceStateBeforeCreatingLeapViewDatabase(t *testing.T) {
	home := t.TempDir()
	legacyPath := filepath.Join(home, "libredash.db")
	legacyContents := []byte("released v0.1.0 state marker")
	if err := os.WriteFile(legacyPath, legacyContents, 0o600); err != nil {
		t.Fatal(err)
	}
	currentPath := filepath.Join(home, "leapview.db")

	store, err := Open(t.Context(), currentPath)
	if store != nil {
		_ = store.Close()
		t.Fatal("Open() returned a store for incompatible v0.1.0 state")
	}
	if err == nil || !strings.Contains(err.Error(), "v0.1.0") || !strings.Contains(err.Error(), "fresh LeapView instance") {
		t.Fatalf("Open() error = %v, want explicit fresh-install-only policy", err)
	}
	if _, err := os.Stat(currentPath); !os.IsNotExist(err) {
		t.Fatalf("incompatible state created current database: %v", err)
	}
	if got, err := os.ReadFile(legacyPath); err != nil || !bytes.Equal(got, legacyContents) {
		t.Fatalf("legacy state changed before rejection: %q, %v", got, err)
	}
}

func TestRecoverInterruptedInstanceOperationsRemovesDisposableWork(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "home")
	writeTestFile(t, filepath.Join(target, "current"), "current")
	writeTestFile(t, filepath.Join(parent, ".leapview-instance-backup-stale", "copy"), "backup")
	writeTestFile(t, filepath.Join(parent, ".leapview-instance-backup-stale.tar.gz"), "backup archive")
	writeTestFile(t, filepath.Join(parent, ".leapview-restore-stale", "copy"), "restore")
	writeTestFile(t, filepath.Join(parent, ".leapview-restore-old-stale", "copy"), "old")
	writeTestFile(t, filepath.Join(parent, ".leapview-current-backup-stale.tar.gz"), "checkpoint")
	writeTestFile(t, filepath.Join(parent, "leapview-current-backup-stale.tar.gz"), "legacy checkpoint")

	if err := recoverInterruptedInstanceOperations(target); err == nil || !strings.Contains(err.Error(), "ambiguous legacy") {
		t.Fatalf("recoverInterruptedInstanceOperations() error = %v, want ambiguous legacy error", err)
	}
	for _, stale := range []string{".leapview-instance-backup-stale", ".leapview-instance-backup-stale.tar.gz", ".leapview-restore-stale", ".leapview-restore-old-stale", ".leapview-current-backup-stale.tar.gz", "leapview-current-backup-stale.tar.gz"} {
		if _, err := os.Stat(filepath.Join(parent, stale)); err != nil {
			t.Fatalf("legacy artifact %q was mutated: %v", stale, err)
		}
	}
	if got := readTestFile(t, filepath.Join(target, "current")); got != "current" {
		t.Fatalf("current state = %q, want current", got)
	}
}

func TestRecoverInterruptedInstanceOperationsRollsBackMissingTarget(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "home")
	canonicalTarget, targetID, err := instanceTargetIdentity(target)
	if err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(parent, ".leapview-restore-old-"+targetID+"-20260727200000")
	writeTestFile(t, filepath.Join(old, "current"), "recover me")
	if err := writeInstanceOperationMarker(old, canonicalTarget, targetID); err != nil {
		t.Fatal(err)
	}
	staleRestore := filepath.Join(parent, ".leapview-restore-"+targetID+"-stale")
	writeTestFile(t, filepath.Join(staleRestore, "candidate"), "discard me")

	if err := recoverInterruptedInstanceOperations(target); err != nil {
		t.Fatalf("recoverInterruptedInstanceOperations() error = %v", err)
	}
	if got := readTestFile(t, filepath.Join(target, "current")); got != "recover me" {
		t.Fatalf("recovered state = %q, want recover me", got)
	}
	if _, err := os.Stat(staleRestore); !os.IsNotExist(err) {
		t.Fatalf("stale restore candidate survived: %v", err)
	}
}

func TestRecoverInterruptedInstanceOperationsScopesSiblingHomes(t *testing.T) {
	parent := t.TempDir()
	targetA := filepath.Join(parent, "home-a")
	targetB := filepath.Join(parent, "home-b")
	canonicalB, idB, _ := instanceTargetIdentity(targetB)
	writeTestFile(t, filepath.Join(targetA, "current"), "a")
	backupB := filepath.Join(parent, ".leapview-instance-backup-"+idB+"-stale")
	writeTestFile(t, filepath.Join(backupB, "payload"), "b-backup")
	restoreB := filepath.Join(parent, ".leapview-restore-"+idB+"-stale")
	writeTestFile(t, filepath.Join(restoreB, "payload"), "b-restore")
	oldB := filepath.Join(parent, ".leapview-restore-old-"+idB+"-20200101000000")
	writeTestFile(t, filepath.Join(oldB, "payload"), "b-old")
	if err := writeInstanceOperationMarker(oldB, canonicalB, idB); err != nil {
		t.Fatal(err)
	}
	if err := recoverInterruptedInstanceOperations(targetA); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{backupB, restoreB, oldB} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("sibling artifact %s changed: %v", p, err)
		}
	}
}

func TestRecoverInterruptedInstanceOperationsSelectsNewestMatchingGeneration(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "home")
	canonical, id, _ := instanceTargetIdentity(target)
	old1 := filepath.Join(parent, ".leapview-restore-old-"+id+"-20200101000000")
	old2 := filepath.Join(parent, ".leapview-restore-old-"+id+"-20200201000000")
	writeTestFile(t, filepath.Join(old1, "value"), "old")
	writeTestFile(t, filepath.Join(old2, "value"), "new")
	if err := writeInstanceOperationMarker(old1, canonical, id); err != nil {
		t.Fatal(err)
	}
	if err := writeInstanceOperationMarker(old2, canonical, id); err != nil {
		t.Fatal(err)
	}
	if err := recoverInterruptedInstanceOperations(target); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, filepath.Join(target, "value")); got != "new" {
		t.Fatalf("recovered generation = %q", got)
	}
	if _, err := os.Stat(old1); !os.IsNotExist(err) {
		t.Fatalf("older generation survived: %v", err)
	}
}

func TestRecoverInterruptedInstanceOperationsRejectsMismatchedMarker(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "home")
	_, id, _ := instanceTargetIdentity(target)
	old := filepath.Join(parent, ".leapview-restore-old-"+id+"-20200101000000")
	writeTestFile(t, filepath.Join(old, "value"), "unsafe")
	if err := writeInstanceOperationMarker(old, filepath.Join(parent, "other"), id); err != nil {
		t.Fatal(err)
	}
	if err := recoverInterruptedInstanceOperations(target); err == nil {
		t.Fatal("expected marker mismatch error")
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("mismatched rollback mutated: %v", err)
	}
}

func TestRecoverInterruptedInstanceOperationsRejectsCorruptMarker(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "home")
	_, id, _ := instanceTargetIdentity(target)
	old := filepath.Join(parent, ".leapview-restore-old-"+id+"-20200101000000")
	writeTestFile(t, filepath.Join(old, "value"), "unsafe")
	writeTestFile(t, filepath.Join(old, instanceOperationMarkerName), "not-json")
	if err := recoverInterruptedInstanceOperations(target); err == nil || !strings.Contains(err.Error(), "unverified") {
		t.Fatalf("expected corrupt marker error, got %v", err)
	}
	if got := readTestFile(t, filepath.Join(old, "value")); got != "unsafe" {
		t.Fatalf("corrupt rollback mutated: %q", got)
	}
}

func TestRecoverInterruptedInstanceOperationsCleansRenameBoundaryArtifacts(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "home")
	canonical, id, _ := instanceTargetIdentity(target)
	writeTestFile(t, filepath.Join(target, "current"), "current")
	if err := writeInstanceOperationMarker(target, canonical, id); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(parent, ".leapview-restore-"+id+"-candidate")
	writeTestFile(t, filepath.Join(tmp, "candidate"), "discard")
	if err := recoverInterruptedInstanceOperations(target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, instanceOperationMarkerName)); !os.IsNotExist(err) {
		t.Fatalf("target marker survived: %v", err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("restore candidate survived: %v", err)
	}
}

func TestRecoverInterruptedInstanceOperationsIgnoresUnrelatedLegacyUserFile(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "home")
	userFile := filepath.Join(parent, ".leapview-instance-backup-notes.txt")
	writeTestFile(t, userFile, "keep")
	if err := recoverInterruptedInstanceOperations(target); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, userFile); got != "keep" {
		t.Fatalf("user file changed: %q", got)
	}
}

func TestRecoverInterruptedInstanceOperationsRemovesCheckpointAndMarker(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "home")
	canonical, id, _ := instanceTargetIdentity(target)
	checkpoint := filepath.Join(parent, ".leapview-current-backup-"+id+"-123.tar.gz")
	writeTestFile(t, checkpoint, "checkpoint")
	if err := writeCheckpointMarker(checkpoint, canonical, id); err != nil {
		t.Fatal(err)
	}
	if err := recoverInterruptedInstanceOperations(target); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{checkpoint, checkpointMarkerPath(checkpoint)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("checkpoint artifact %s survived: %v", p, err)
		}
	}
}

func TestRecoverInterruptedInstanceOperationsConcurrentSiblings(t *testing.T) {
	parent := t.TempDir()
	targets := []string{filepath.Join(parent, "home-a"), filepath.Join(parent, "home-b")}
	for _, target := range targets {
		_, id, _ := instanceTargetIdentity(target)
		writeTestFile(t, filepath.Join(target, "current"), "current")
		writeTestFile(t, filepath.Join(parent, ".leapview-restore-"+id+"-candidate", "candidate"), "candidate")
	}
	errs := make(chan error, len(targets))
	for _, target := range targets {
		go func(target string) { errs <- recoverInterruptedInstanceOperations(target) }(target)
	}
	for range targets {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestConcurrentSiblingBackupAndRestoreOperationsRemainIsolated(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	homeA, homeB := filepath.Join(parent, "home-a"), filepath.Join(parent, "home-b")
	for _, home := range []string{homeA, homeB} {
		store, err := Open(ctx, filepath.Join(home, instanceBackupDBName))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	archiveB := filepath.Join(parent, "source-b.tar.gz")
	if err := BackupInstance(ctx, InstanceBackupOptions{HomeDir: homeB, DBPath: filepath.Join(homeB, instanceBackupDBName), OutPath: archiveB}); err != nil {
		t.Fatal(err)
	}
	archiveA := filepath.Join(parent, "backup-a.tar.gz")
	currentB := filepath.Join(parent, "custom-current-b.tar.gz")
	errs := make(chan error, 2)
	go func() {
		errs <- BackupInstance(ctx, InstanceBackupOptions{HomeDir: homeA, DBPath: filepath.Join(homeA, instanceBackupDBName), OutPath: archiveA})
	}()
	go func() {
		errs <- RestoreInstance(ctx, InstanceRestoreOptions{TargetHomeDir: homeB, BackupPath: archiveB, CurrentBackupOut: currentB})
	}()
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(archiveA); err != nil {
		t.Fatalf("sibling backup missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(homeB, instanceBackupDBName)); err != nil {
		t.Fatalf("sibling restore state missing: %v", err)
	}
}

func TestBackupInstanceCreatesPrivateArchive(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	dbPath := filepath.Join(home, "leapview.db")
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	restoreUmask := setUmask(t, 0)
	backupDir := filepath.Join(dir, "backups")
	if err := os.Mkdir(backupDir, 0o755); err != nil {
		restoreUmask()
		t.Fatalf("seed permissive backup directory: %v", err)
	}
	backupPath := filepath.Join(backupDir, "leapview-instance.tar.gz")
	if err := BackupInstance(ctx, InstanceBackupOptions{HomeDir: home, DBPath: dbPath, OutPath: backupPath}); err != nil {
		restoreUmask()
		t.Fatalf("backup instance: %v", err)
	}
	restoreUmask()
	assertFileMode(t, backupDir, 0o700)
	assertFileMode(t, backupPath, 0o600)
}

func TestBackupInstanceRejectsUnsafeSymlink(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	dbPath := filepath.Join(home, "leapview.db")
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, "artifacts"), 0o755); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "outside"), filepath.Join(home, "artifacts", "outside")); err != nil {
		t.Fatalf("create unsafe symlink: %v", err)
	}

	backupPath := filepath.Join(dir, "backups", "leapview-instance.tar.gz")
	err = BackupInstance(ctx, InstanceBackupOptions{HomeDir: home, DBPath: dbPath, OutPath: backupPath})
	if err == nil || !strings.Contains(err.Error(), "unsafe symlink") {
		t.Fatalf("backup error = %v, want unsafe symlink", err)
	}
	if _, statErr := os.Stat(backupPath); !os.IsNotExist(statErr) {
		t.Fatalf("backup path exists after unsafe symlink error: %v", statErr)
	}
}

func TestBackupInstanceRejectsSymlinkState(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	dbPath := filepath.Join(home, "leapview.db")
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, "artifacts"), 0o755); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "artifacts", "target.tar.gz"), []byte("artifact"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink("target.tar.gz", filepath.Join(home, "artifacts", "latest.tar.gz")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	backupPath := filepath.Join(dir, "backups", "leapview-instance.tar.gz")
	err = BackupInstance(ctx, InstanceBackupOptions{HomeDir: home, DBPath: dbPath, OutPath: backupPath})
	if err == nil || !strings.Contains(err.Error(), "symlink entries are not supported") {
		t.Fatalf("backup error = %v, want symlink rejection", err)
	}
	if _, statErr := os.Stat(backupPath); !os.IsNotExist(statErr) {
		t.Fatalf("backup path exists after symlink error: %v", statErr)
	}
}

func TestBackupInstanceRejectsOutputInsideHomeDir(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dbPath := filepath.Join(home, "leapview.db")
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	backupPath := filepath.Join(home, "backups", "leapview-instance.tar.gz")
	err = BackupInstance(ctx, InstanceBackupOptions{HomeDir: home, DBPath: dbPath, OutPath: backupPath})
	if err == nil || !strings.Contains(err.Error(), "backup output path must not be inside home dir") {
		t.Fatalf("backup error = %v, want in-home output rejection", err)
	}
}

func TestRestoreInstanceReplacesHomeAndBacksUpCurrent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	currentHome := filepath.Join(dir, "current")
	currentDBPath := filepath.Join(currentHome, "leapview.db")
	current, err := Open(ctx, currentDBPath)
	if err != nil {
		t.Fatalf("open current store: %v", err)
	}
	if err := current.UpsertSetting(ctx, "instance-restore-test", "current"); err != nil {
		t.Fatalf("seed current setting: %v", err)
	}
	if err := current.Close(); err != nil {
		t.Fatalf("close current store: %v", err)
	}
	writeTestFile(t, filepath.Join(currentHome, "artifacts", "old.tar.gz"), "old artifact")
	readOnlyRevisionData := filepath.Join(currentHome, "managed-data", "objects", "revisions", "immutable", "data")
	writeTestFile(t, filepath.Join(readOnlyRevisionData, "orders.csv"), "immutable managed data")
	if err := os.Chmod(readOnlyRevisionData, 0o500); err != nil {
		t.Fatalf("make managed data revision read-only: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(readOnlyRevisionData, 0o700)
		oldTargets, _ := filepath.Glob(filepath.Join(dir, ".leapview-restore-old-*"))
		for _, oldTarget := range oldTargets {
			_ = os.Chmod(filepath.Join(oldTarget, "managed-data", "objects", "revisions", "immutable", "data"), 0o700)
		}
	})

	sourceHome := filepath.Join(dir, "source")
	sourceDBPath := filepath.Join(sourceHome, "leapview.db")
	source, err := Open(ctx, sourceDBPath)
	if err != nil {
		t.Fatalf("open source store: %v", err)
	}
	if err := source.UpsertSetting(ctx, "instance-restore-test", "restored"); err != nil {
		t.Fatalf("seed source setting: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close source store: %v", err)
	}
	writeTestFile(t, filepath.Join(sourceHome, "artifacts", "new.tar.gz"), "new artifact")
	writeTestFile(t, filepath.Join(sourceHome, "data", "ducklake-file.parquet"), "ducklake-data")
	writeTestFile(t, filepath.Join(sourceHome, "managed-data", "objects", "revisions", "stale", "data", "orders.csv"), "stale derived revision")
	backupPath := filepath.Join(dir, "backups", "restore.tar.gz")
	if err := BackupInstance(ctx, InstanceBackupOptions{HomeDir: sourceHome, DBPath: sourceDBPath, OutPath: backupPath}); err != nil {
		t.Fatalf("backup source instance: %v", err)
	}

	beforePath := filepath.Join(dir, "backups", "before-restore.tar.gz")
	if err := RestoreInstance(ctx, InstanceRestoreOptions{
		TargetHomeDir: currentHome, BackupPath: backupPath, CurrentBackupOut: beforePath,
		ResetRelativePaths: []string{"managed-data/objects/revisions"},
	}); err != nil {
		t.Fatalf("restore instance: %v", err)
	}

	restored, err := Open(ctx, currentDBPath)
	if err != nil {
		t.Fatalf("open restored store: %v", err)
	}
	value, err := restored.GetSetting(ctx, "instance-restore-test")
	if err != nil {
		t.Fatalf("read restored setting: %v", err)
	}
	if value != "restored" {
		t.Fatalf("restored setting = %q, want restored", value)
	}
	if err := restored.Close(); err != nil {
		t.Fatalf("close restored store: %v", err)
	}
	if got := readTestFile(t, filepath.Join(currentHome, "artifacts", "new.tar.gz")); got != "new artifact" {
		t.Fatalf("restored artifact = %q, want new artifact", got)
	}
	if _, err := os.Stat(filepath.Join(currentHome, "artifacts", "old.tar.gz")); !os.IsNotExist(err) {
		t.Fatalf("old artifact survived restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(currentHome, "managed-data", "objects", "revisions")); !os.IsNotExist(err) {
		t.Fatalf("derived revision cache survived restore: %v", err)
	}
	beforeEntries := readTarGzEntries(t, beforePath)
	if got := string(beforeEntries["artifacts/old.tar.gz"]); got != "old artifact" {
		t.Fatalf("before-restore artifact = %q, want old artifact", got)
	}

	discardedBeforePath := filepath.Join(dir, "custom-disposable-current.tar.gz")
	if err := RestoreInstance(ctx, InstanceRestoreOptions{
		TargetHomeDir:        currentHome,
		BackupPath:           backupPath,
		CurrentBackupOut:     discardedBeforePath,
		DiscardCurrentBackup: true,
	}); err != nil {
		t.Fatalf("restore instance with disposable current backup: %v", err)
	}
	if _, err := os.Stat(discardedBeforePath); !os.IsNotExist(err) {
		t.Fatalf("disposable current backup survived successful restore: %v", err)
	}
}

func TestRestoreInstanceRejectsBackupFromAnotherEnvironmentBeforeReplacement(t *testing.T) {
	ctx := context.Background()
	sourceHome := t.TempDir()
	sourceDB := filepath.Join(sourceHome, instanceBackupDBName)
	source, err := Open(ctx, sourceDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.BindInstanceEnvironment(ctx, "prod"); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "prod.tar.gz")
	if err := BackupInstance(ctx, InstanceBackupOptions{HomeDir: sourceHome, DBPath: sourceDB, OutPath: archive}); err != nil {
		t.Fatal(err)
	}
	targetHome := t.TempDir()
	marker := filepath.Join(targetHome, "current-state")
	if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = RestoreInstance(ctx, InstanceRestoreOptions{TargetHomeDir: targetHome, BackupPath: archive, ExpectedEnvironment: "staging"})
	if err == nil || !strings.Contains(err.Error(), RestorePreflightWrongEnvironment) || !strings.Contains(err.Error(), `archive environment "prod"`) {
		t.Fatalf("restore environment error = %v", err)
	}
	if contents, readErr := os.ReadFile(marker); readErr != nil || string(contents) != "preserve" {
		t.Fatalf("current state changed: %q, %v", contents, readErr)
	}
}

func TestRestoreInstancePreservesLifetimeLockAcrossHomeSwap(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sourceHome := filepath.Join(dir, "source")
	sourceDB := filepath.Join(sourceHome, instanceBackupDBName)
	source, err := Open(ctx, sourceDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "source.tar.gz")
	if err := BackupInstance(ctx, InstanceBackupOptions{HomeDir: sourceHome, DBPath: sourceDB, OutPath: archive}); err != nil {
		t.Fatal(err)
	}

	targetHome := filepath.Join(dir, "target")
	lock, err := instancelock.Acquire(targetHome)
	if err != nil {
		t.Fatal(err)
	}
	if err := RestoreInstance(ctx, InstanceRestoreOptions{
		TargetHomeDir:        targetHome,
		BackupPath:           archive,
		PreserveRelativeFile: instancelock.FileName,
	}); err != nil {
		t.Fatal(err)
	}
	if competing, err := instancelock.Acquire(targetHome); err == nil {
		_ = competing.Release()
		t.Fatal("competing process acquired the instance lock after restore")
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	reacquired, err := instancelock.Acquire(targetHome)
	if err != nil {
		t.Fatalf("acquire restored instance after release: %v", err)
	}
	if err := reacquired.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreInstanceRequiresCurrentBackupWhenTargetHasState(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sourceHome := filepath.Join(dir, "source")
	sourceDBPath := filepath.Join(sourceHome, "leapview.db")
	source, err := Open(ctx, sourceDBPath)
	if err != nil {
		t.Fatalf("open source store: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close source store: %v", err)
	}
	backupPath := filepath.Join(dir, "backup.tar.gz")
	if err := BackupInstance(ctx, InstanceBackupOptions{HomeDir: sourceHome, DBPath: sourceDBPath, OutPath: backupPath}); err != nil {
		t.Fatalf("backup source: %v", err)
	}
	targetHome := filepath.Join(dir, "target")
	target, err := Open(ctx, filepath.Join(targetHome, "leapview.db"))
	if err != nil {
		t.Fatalf("open target store: %v", err)
	}
	if err := target.Close(); err != nil {
		t.Fatalf("close target store: %v", err)
	}
	err = RestoreInstance(ctx, InstanceRestoreOptions{TargetHomeDir: targetHome, BackupPath: backupPath})
	if err == nil || !strings.Contains(err.Error(), "current instance backup path is required") {
		t.Fatalf("restore error = %v, want current backup path requirement", err)
	}
}

func TestRestoreInstanceSanitizesArchivePermissions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sourceDBPath := filepath.Join(dir, "source", instanceBackupDBName)
	source, err := Open(ctx, sourceDBPath)
	if err != nil {
		t.Fatalf("open source store: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close source store: %v", err)
	}
	backupPath := filepath.Join(dir, "backup.tar.gz")
	writeInstanceBackupArchive(t, backupPath, []testTarEntry{
		{name: instanceBackupManifestName, mode: 0o777, body: []byte(`{"version":1,"kind":"leapview-instance","dbPath":"leapview.db"}` + "\n")},
		{name: instanceBackupDBName, mode: 0o777, body: readTestBytes(t, sourceDBPath)},
		{name: "artifacts", mode: 0o777, dir: true},
		{name: "artifacts/publish.tar.gz", mode: 0o777, body: []byte("artifact")},
	})

	targetHome := filepath.Join(dir, "target")
	if err := RestoreInstance(ctx, InstanceRestoreOptions{TargetHomeDir: targetHome, BackupPath: backupPath, AllowLegacyV1: true}); err != nil {
		t.Fatalf("restore instance: %v", err)
	}

	fileModeWants := map[string]os.FileMode{
		filepath.Join(targetHome, instanceBackupManifestName):    0o600,
		filepath.Join(targetHome, instanceBackupDBName):          0o600,
		filepath.Join(targetHome, "artifacts", "publish.tar.gz"): 0o600,
	}
	for path, want := range fileModeWants {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat restored file %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("restored file mode for %s = %#o, want %#o", path, got, want)
		}
	}
	info, err := os.Stat(filepath.Join(targetHome, "artifacts"))
	if err != nil {
		t.Fatalf("stat restored artifacts dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("restored dir mode = %#o, want 0700", got)
	}
}

func TestRestoreInstanceRejectsSymlinkEntries(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sourceDBPath := filepath.Join(dir, "source", instanceBackupDBName)
	source, err := Open(ctx, sourceDBPath)
	if err != nil {
		t.Fatalf("open source store: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close source store: %v", err)
	}
	backupPath := filepath.Join(dir, "backup.tar.gz")
	writeInstanceBackupArchive(t, backupPath, []testTarEntry{
		{name: instanceBackupManifestName, mode: 0o644, body: []byte(`{"version":1,"kind":"leapview-instance","dbPath":"leapview.db"}` + "\n")},
		{name: instanceBackupDBName, mode: 0o600, body: readTestBytes(t, sourceDBPath)},
		{name: "artifacts/latest.tar.gz", mode: 0o777, symlink: true, linkname: "target.tar.gz"},
	})

	targetHome := filepath.Join(dir, "target")
	err = RestoreInstance(ctx, InstanceRestoreOptions{TargetHomeDir: targetHome, BackupPath: backupPath, AllowLegacyV1: true})
	if err == nil || !strings.Contains(err.Error(), RestorePreflightUnsupportedEntry) {
		t.Fatalf("restore error = %v, want symlink rejection", err)
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

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
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

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(bytes)
}

func readTestBytes(t *testing.T, path string) []byte {
	t.Helper()
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return bytes
}

type testTarEntry struct {
	name     string
	mode     int64
	body     []byte
	dir      bool
	symlink  bool
	linkname string
	typeflag byte
}

func writeInstanceBackupArchive(t *testing.T, archivePath string, entries []testTarEntry) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		t.Fatalf("mkdir archive dir: %v", err)
	}
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gzw := gzip.NewWriter(file)
	tw := tar.NewWriter(gzw)
	for _, entry := range entries {
		header := &tar.Header{
			Name: entry.name,
			Mode: entry.mode,
		}
		if entry.typeflag != 0 {
			header.Typeflag = entry.typeflag
		} else if entry.dir {
			header.Typeflag = tar.TypeDir
		} else if entry.symlink {
			header.Typeflag = tar.TypeSymlink
			header.Linkname = entry.linkname
		} else {
			header.Typeflag = tar.TypeReg
			header.Size = int64(len(entry.body))
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("write tar header %s: %v", entry.name, err)
		}
		if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA {
			if _, err := tw.Write(entry.body); err != nil {
				t.Fatalf("write tar body %s: %v", entry.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
}

func readTarGzEntries(t *testing.T, path string) map[string][]byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer file.Close()
	gzr, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("open gzip: %v", err)
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)
	entries := map[string][]byte{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			continue
		}
		bytes, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read tar entry %s: %v", header.Name, err)
		}
		entries[header.Name] = bytes
	}
	return entries
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
