package adminoffline

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	admincli "github.com/flidai/leapview/internal/admin/cli"
	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/compatibility"
	locking "github.com/flidai/leapview/internal/platform/locking"
)

func TestAdminBackupWritesInstanceArchive(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	setAdminStorageEnv(t, home)
	if err := createAdminDatabase(t, ctx, home).Close(); err != nil {
		t.Fatalf("close platform store: %v", err)
	}
	writeAdminFile(t, filepath.Join(home, "artifacts", "dep_1.tar.gz"), "artifact")

	backupPath := filepath.Join(t.TempDir(), "leapview.backup.tar.gz")
	cmd := admincli.Command(ctx, Operations{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"backup", "--out", backupPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("admin backup: %v", err)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if !strings.Contains(out.String(), "instance backup written: "+backupPath) {
		t.Fatalf("backup output = %q", out.String())
	}
}

func TestAdminBackupStreamsRestorableInstanceArchive(t *testing.T) {
	ctx := context.Background()
	sourceHome := t.TempDir()
	setAdminStorageEnv(t, sourceHome)
	store := createAdminDatabase(t, ctx, sourceHome)
	if err := store.UpsertSetting(ctx, "stream-test", "preserved"); err != nil {
		t.Fatalf("seed source platform store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close source platform store: %v", err)
	}

	var archive bytes.Buffer
	if err := (Operations{}).Backup(ctx, adminoffline.BackupRequest{Out: "-"}, &archive); err != nil {
		t.Fatalf("stream backup: %v", err)
	}
	if archive.Len() < 2 || archive.Bytes()[0] != 0x1f || archive.Bytes()[1] != 0x8b {
		t.Fatalf("stream backup is not a gzip archive: %x", archive.Bytes())
	}

	targetHome := filepath.Join(t.TempDir(), "volume", "home")
	setAdminStorageEnv(t, targetHome)
	current := createAdminDatabase(t, ctx, targetHome)
	if err := current.UpsertSetting(ctx, "stream-test", "current"); err != nil {
		t.Fatalf("seed current platform store: %v", err)
	}
	if err := current.Close(); err != nil {
		t.Fatalf("close current platform store: %v", err)
	}

	var restoreOutput bytes.Buffer
	if err := (Operations{}).Restore(ctx, adminoffline.RestoreRequest{
		From: "-", CurrentBackup: "-", Confirm: true,
	}, bytes.NewReader(archive.Bytes()), &restoreOutput); err != nil {
		t.Fatalf("restore streamed backup: %v", err)
	}
	if !strings.Contains(restoreOutput.String(), "instance restored from: stdin") {
		t.Fatalf("restore output = %q", restoreOutput.String())
	}
	checkpoints, err := filepath.Glob(filepath.Join(filepath.Dir(targetHome), platform.InstanceRestoreCheckpointPattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != 0 {
		t.Fatalf("discarded restore checkpoints remain: %v", checkpoints)
	}
	restored, err := platform.Open(ctx, filepath.Join(targetHome, "leapview.db"))
	if err != nil {
		t.Fatalf("open restored platform store: %v", err)
	}
	defer restored.Close()
	if value, err := restored.GetSetting(ctx, "stream-test"); err != nil || value != "preserved" {
		t.Fatalf("streamed setting = %q, %v", value, err)
	}
}

func TestAdminBackupRejectsExternalDuckLakeCatalog(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	setAdminStorageEnv(t, home)
	t.Setenv("LEAPVIEW_DUCKLAKE_CATALOG_PATH", filepath.Join(t.TempDir(), "catalog.duckdb"))
	createAdminDatabase(t, ctx, home).Close()

	err := (Operations{}).Backup(ctx, adminoffline.BackupRequest{
		Out: filepath.Join(t.TempDir(), "leapview.backup.tar.gz"),
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "DuckLake catalog path inside LEAPVIEW_HOME") {
		t.Fatalf("admin backup error = %v, want external DuckLake catalog rejection", err)
	}
}

func TestAdminRestoreRequiresConfirmation(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	setAdminStorageEnv(t, home)
	store := createAdminDatabase(t, ctx, home)
	backupPath := filepath.Join(t.TempDir(), "backup.db")
	if err := store.Backup(ctx, backupPath); err != nil {
		t.Fatalf("backup platform store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close platform store: %v", err)
	}

	err := (Operations{}).Restore(ctx, adminoffline.RestoreRequest{
		From: backupPath, CurrentBackup: filepath.Join(home, "before.db"),
	}, nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "admin restore requires --confirm") {
		t.Fatalf("admin restore error = %v, want confirmation requirement", err)
	}
}

func TestAdminRestorePreflightIsReadOnlyAndMachineReadable(t *testing.T) {
	ctx := context.Background()
	sourceHome := filepath.Join(t.TempDir(), "source")
	setAdminStorageEnv(t, sourceHome)
	source := createAdminDatabase(t, ctx, sourceHome)
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := (Operations{}).Backup(ctx, adminoffline.BackupRequest{Out: archivePath}, io.Discard); err != nil {
		t.Fatal(err)
	}

	targetHome := filepath.Join(t.TempDir(), "target")
	setAdminStorageEnv(t, targetHome)
	target := createAdminDatabase(t, ctx, targetHome)
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(targetHome, "marker")
	writeAdminFile(t, marker, "unchanged")
	var out bytes.Buffer
	if err := (Operations{}).Restore(ctx, adminoffline.RestoreRequest{
		From: archivePath, CurrentBackup: filepath.Join(t.TempDir(), "before.tar.gz"), PreflightOnly: true,
	}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"reasonCode": "restore.preflight.allowed"`) || !strings.Contains(out.String(), `"archiveSha256"`) {
		t.Fatalf("preflight output = %s", out.String())
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "unchanged" {
		t.Fatalf("target changed during preflight: %q %v", got, err)
	}
}

func TestAdminRestoreRejectsExternalDuckLakeCatalog(t *testing.T) {
	ctx := context.Background()
	sourceHome := t.TempDir()
	sourceStore := createAdminDatabase(t, ctx, sourceHome)
	if err := sourceStore.Close(); err != nil {
		t.Fatalf("close backup source: %v", err)
	}
	backupPath := filepath.Join(t.TempDir(), "restore.tar.gz")
	if err := platform.BackupInstance(ctx, platform.InstanceBackupOptions{
		HomeDir:         sourceHome,
		DBPath:          filepath.Join(sourceHome, "leapview.db"),
		OutPath:         backupPath,
		Environment:     "dev",
		ReleaseIdentity: adminTestReleaseIdentity(),
	}); err != nil {
		t.Fatalf("backup source: %v", err)
	}

	targetHome := t.TempDir()
	setAdminStorageEnv(t, targetHome)
	t.Setenv("LEAPVIEW_DUCKLAKE_CATALOG_PATH", filepath.Join(t.TempDir(), "catalog.duckdb"))
	err := (Operations{}).Restore(ctx, adminoffline.RestoreRequest{
		From: backupPath, CurrentBackup: filepath.Join(t.TempDir(), "before.tar.gz"), Confirm: true,
	}, nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "DuckLake catalog path inside LEAPVIEW_HOME") {
		t.Fatalf("admin restore error = %v, want external DuckLake catalog rejection", err)
	}
}

func TestAdminRestoreReplacesPlatformDatabase(t *testing.T) {
	ctx := context.Background()
	targetHome := t.TempDir()
	setAdminStorageEnv(t, targetHome)
	current := createAdminDatabase(t, ctx, targetHome)
	if err := current.UpsertSetting(ctx, "restore-test", "current"); err != nil {
		t.Fatalf("seed current platform store: %v", err)
	}
	if err := current.Close(); err != nil {
		t.Fatalf("close current platform store: %v", err)
	}
	writeAdminFile(t, filepath.Join(targetHome, "artifacts", "old.tar.gz"), "old artifact")

	sourceHome := filepath.Join(t.TempDir(), "backup-source")
	source := createAdminDatabase(t, ctx, sourceHome)
	if err := source.UpsertSetting(ctx, "restore-test", "restored"); err != nil {
		t.Fatalf("seed backup source: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close backup source: %v", err)
	}
	writeAdminFile(t, filepath.Join(sourceHome, "artifacts", "new.tar.gz"), "new artifact")

	backupPath := filepath.Join(t.TempDir(), "restore.tar.gz")
	if err := platform.BackupInstance(ctx, platform.InstanceBackupOptions{
		HomeDir:         sourceHome,
		DBPath:          filepath.Join(sourceHome, "leapview.db"),
		OutPath:         backupPath,
		Environment:     "dev",
		ReleaseIdentity: adminTestReleaseIdentity(),
	}); err != nil {
		t.Fatalf("backup source: %v", err)
	}

	beforePath := filepath.Join(t.TempDir(), "before-restore.tar.gz")
	var out bytes.Buffer
	if err := (Operations{}).Restore(ctx, adminoffline.RestoreRequest{
		From: backupPath, CurrentBackup: beforePath, Confirm: true,
	}, nil, &out); err != nil {
		t.Fatalf("admin restore: %v", err)
	}
	for _, want := range []string{
		"instance restored from: " + backupPath,
		"previous instance backup: " + beforePath,
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("restore output missing %q:\n%s", want, out.String())
		}
	}

	restored, err := platform.Open(ctx, filepath.Join(targetHome, "leapview.db"))
	if err != nil {
		t.Fatalf("open restored platform store: %v", err)
	}
	value, err := restored.GetSetting(ctx, "restore-test")
	closeErr := restored.Close()
	if err != nil {
		t.Fatalf("read restored setting: %v", err)
	}
	if closeErr != nil {
		t.Fatalf("close restored platform store: %v", closeErr)
	}
	if value != "restored" {
		t.Fatalf("restored setting = %q, want restored", value)
	}
	if got, err := os.ReadFile(filepath.Join(targetHome, "artifacts", "new.tar.gz")); err != nil || string(got) != "new artifact" {
		t.Fatalf("restored artifact = %q, %v; want new artifact", string(got), err)
	}
	if _, err := os.Stat(filepath.Join(targetHome, "artifacts", "old.tar.gz")); !os.IsNotExist(err) {
		t.Fatalf("old artifact survived restore: %v", err)
	}
	if _, err := os.Stat(beforePath); err != nil {
		t.Fatalf("before-restore backup missing: %v", err)
	}
}

func TestAdminDatabaseRestoreRejectsAnotherInstanceEnvironment(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	setAdminStorageEnv(t, home)
	current := createAdminDatabase(t, ctx, home)
	if err := current.BindInstanceEnvironment(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}

	sourcePath := filepath.Join(t.TempDir(), "prod.db")
	source, err := platform.Open(ctx, sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.BindInstanceEnvironment(ctx, "prod"); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "prod-backup.db")
	if err := source.Backup(ctx, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	err = (Operations{}).Restore(ctx, adminoffline.RestoreRequest{
		From: backupPath, CurrentBackup: filepath.Join(t.TempDir(), "before.db"), Confirm: true, DatabaseOnly: true,
	}, nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `bound to environment "prod"`) {
		t.Fatalf("database restore error = %v", err)
	}
	after, err := platform.Open(ctx, filepath.Join(home, "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()
	if environment, err := after.InstanceEnvironment(ctx); err != nil || environment != "dev" {
		t.Fatalf("current environment changed to %q: %v", environment, err)
	}
}

func TestAdminArchiveOperationsRequireExclusiveInstanceLock(t *testing.T) {
	home := t.TempDir()
	setAdminStorageEnv(t, home)
	held, err := locking.Acquire(home)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	ctx := context.Background()
	err = (Operations{}).Backup(ctx, adminoffline.BackupRequest{
		Out: filepath.Join(t.TempDir(), "backup.tar.gz"),
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "already using instance home") {
		t.Fatalf("backup lock error = %v", err)
	}
	err = (Operations{}).Restore(ctx, adminoffline.RestoreRequest{
		From: filepath.Join(t.TempDir(), "backup.tar.gz"), Confirm: true,
	}, nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "already using instance home") {
		t.Fatalf("restore lock error = %v", err)
	}
}

func createAdminDatabase(t *testing.T, ctx context.Context, home string) *platform.Store {
	t.Helper()
	store, err := platform.Open(ctx, filepath.Join(home, "leapview.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	return store
}

func writeAdminFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func setAdminStorageEnv(t *testing.T, home string) {
	t.Helper()
	t.Setenv("LEAPVIEW_HOME", home)
	t.Setenv("LEAPVIEW_DUCKDB_DIR", filepath.Join(home, "duckdb"))
	previous := loadArchiveReleaseIdentity
	loadArchiveReleaseIdentity = func() (compatibility.ReleaseIdentity, error) { return adminTestReleaseIdentity(), nil }
	t.Cleanup(func() { loadArchiveReleaseIdentity = previous })
}

func adminTestReleaseIdentity() compatibility.ReleaseIdentity {
	return compatibility.ReleaseIdentity{
		ReleaseID: "v1.2.3", Version: "1.2.3", SourceRevision: strings.Repeat("a", 40),
		Image:        "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64),
		Distribution: "public", Platform: "linux/amd64",
	}
}
