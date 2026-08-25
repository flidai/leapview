//go:build !windows

package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestRestorePreflightRejectsUnsupportedCheckpointFilesystemEntry(t *testing.T) {
	ctx := context.Background()
	archivePath, identity := createManifestV2TestArchive(t, ctx, "prod")
	target := filepath.Join(t.TempDir(), "target")
	createCurrentInstanceState(t, ctx, target, "prod")
	unsupported := filepath.Join(target, "unsupported.fifo")
	if err := syscall.Mkfifo(unsupported, 0o600); err != nil {
		t.Skipf("named pipes unavailable: %v", err)
	}
	checkpoint := filepath.Join(t.TempDir(), "current.tar.gz")
	plan, preflightErr := PreflightInstanceRestore(ctx, InstanceRestorePreflightOptions{
		ArchivePath: archivePath, TargetHomeDir: target, CurrentBackupOut: checkpoint,
		ExpectedEnvironment: "prod", TargetReleaseIdentity: identity,
	})
	if preflightErr == nil || plan.ReasonCode != RestorePreflightCheckpointInvalid || !strings.Contains(preflightErr.Error(), "unsupported entry") {
		t.Fatalf("preflight plan=%#v err=%v", plan, preflightErr)
	}
	restoreErr := RestoreInstance(ctx, InstanceRestoreOptions{
		TargetHomeDir: target, BackupPath: archivePath, CurrentBackupOut: checkpoint,
		ExpectedEnvironment: "prod", TargetReleaseIdentity: identity,
	})
	if restoreErr == nil || restoreErr.Error() != preflightErr.Error() {
		t.Fatalf("restore error = %v, want exact preflight denial %v", restoreErr, preflightErr)
	}
	if info, err := os.Lstat(unsupported); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("unsupported entry changed after denial: info=%#v err=%v", info, err)
	}
	if _, err := os.Stat(checkpoint); !os.IsNotExist(err) {
		t.Fatalf("checkpoint-readiness denial created checkpoint: %v", err)
	}
}
