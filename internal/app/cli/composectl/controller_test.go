package composectl

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform/compatibility"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	"github.com/stretchr/testify/require"
)

func TestControllerLockRejectsConcurrentOperationAndRecoversAfterRelease(t *testing.T) {
	root := t.TempDir()
	first, err := instancelock.AcquireNamed(root, controllerLockName)
	require.NoError(t, err)
	if _, err := instancelock.AcquireNamed(root, controllerLockName); err == nil || !strings.Contains(err.Error(), "another process") {
		t.Fatalf("concurrent lock error = %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := instancelock.AcquireNamed(root, controllerLockName)
	if err != nil {
		t.Fatalf("reacquire released lock: %v", err)
	}
	defer second.Release()
}

func TestRemoveInterruptedBackupArchivesPreservesCompletedBackups(t *testing.T) {
	directory := t.TempDir()
	stale := filepath.Join(directory, ".leapview-backup-interrupted.tmp")
	completed := filepath.Join(directory, "completed.tar.gz")
	if err := os.WriteFile(stale, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(completed, []byte("complete"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := removeInterruptedBackupArchives(directory); err != nil {
		t.Fatalf("removeInterruptedBackupArchives() error = %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("interrupted backup survived: %v", err)
	}
	if contents, err := os.ReadFile(completed); err != nil || string(contents) != "complete" {
		t.Fatalf("completed backup = %q, %v", contents, err)
	}
}

func TestUpgradeRejectsReleasedV010BeforeDockerOrStateMutation(t *testing.T) {
	root := t.TempDir()
	const releasedV010 = "ghcr.io/yacobolo/libredash@sha256:677caaf256cb3a0d61efd47b289debbd91984976a5a5c4b372196a5d79ce7153"
	next := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	deployment := "LEAPVIEW_IMAGE=" + releasedV010 + "\nCOMPOSE_HTTPS=0\n"
	if err := os.WriteFile(filepath.Join(root, deploymentEnvName), []byte(deployment), 0o600); err != nil {
		t.Fatal(err)
	}
	hostMarker := []byte("{\"image\":\"" + releasedV010 + "\"}\n")
	database := []byte("legacy-database-checksum-input")
	if err := os.WriteFile(filepath.Join(root, ".host-install.json"), hostMarker, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "libredash.db"), database, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("releases", "sha256-legacy"), filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	controller, err := New(Options{Root: root, DockerBin: "/bin/false", DockerPlatform: "linux/amd64"})
	require.NoError(t, err)

	err = controller.Upgrade(t.Context(), next)
	var decisionErr *compatibility.DecisionError
	if !errors.As(err, &decisionErr) || decisionErr.Decision.ReasonCode != compatibility.ReasonDeniedUnknownRelease {
		t.Fatalf("Upgrade() error = %v, want unknown endpoint denial", err)
	}
	if contents, err := os.ReadFile(filepath.Join(root, deploymentEnvName)); err != nil || string(contents) != deployment {
		t.Fatalf("deployment state changed before rejection: %q, %v", contents, err)
	}
	for _, path := range []string{rollbackEnvName, "backups"} {
		if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Fatalf("upgrade rejection created %s: %v", path, err)
		}
	}
	if contents, err := os.ReadFile(filepath.Join(root, ".host-install.json")); err != nil || !bytes.Equal(contents, hostMarker) {
		t.Fatalf("host installation marker changed: %q, %v", contents, err)
	}
	if contents, err := os.ReadFile(filepath.Join(root, "libredash.db")); err != nil || !bytes.Equal(contents, database) {
		t.Fatalf("legacy database changed: %q, %v", contents, err)
	}
	if active, err := os.Readlink(filepath.Join(root, "current")); err != nil || active != filepath.Join("releases", "sha256-legacy") {
		t.Fatalf("active generation changed: %q, %v", active, err)
	}
}

func TestRollbackRejectsReleasedV010BeforeDockerOrStateMutation(t *testing.T) {
	root := t.TempDir()
	current := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	checkpoint := filepath.Join(root, "pre-upgrade.tar.gz")
	deployment := []byte("LEAPVIEW_IMAGE=" + current + "\nCOMPOSE_HTTPS=0\n")
	marker := []byte("PREVIOUS_IMAGE=" + compatibility.ReleasedV010Image + "\nCHECKPOINT=" + checkpoint + "\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, deploymentEnvName), deployment, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, rollbackEnvName), marker, 0o600))
	require.NoError(t, os.WriteFile(checkpoint, []byte("checkpoint"), 0o600))
	controller, err := New(Options{Root: root, DockerBin: "/bin/false", DockerPlatform: "linux/amd64"})
	require.NoError(t, err)

	err = controller.Rollback(t.Context(), true)
	var decisionErr *compatibility.DecisionError
	require.ErrorAs(t, err, &decisionErr)
	require.Equal(t, compatibility.ReasonDeniedUnknownRelease, decisionErr.Decision.ReasonCode)
	contents, readErr := os.ReadFile(filepath.Join(root, deploymentEnvName))
	require.NoError(t, readErr)
	require.Equal(t, deployment, contents)
	contents, readErr = os.ReadFile(filepath.Join(root, rollbackEnvName))
	require.NoError(t, readErr)
	require.Equal(t, marker, contents)
	_, statErr := os.Stat(filepath.Join(root, "backups"))
	require.True(t, os.IsNotExist(statErr), "rollback denial created backup state: %v", statErr)
}

func TestUpgradeRollbackMarkerReadFailureRestoresRunningService(t *testing.T) {
	root := t.TempDir()
	current := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	next := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	if err := os.WriteFile(filepath.Join(root, deploymentEnvName), []byte("LEAPVIEW_IMAGE="+current+"\nCOMPOSE_HTTPS=0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, rollbackEnvName), 0o700); err != nil {
		t.Fatal(err)
	}
	c, err := New(Options{Root: root, DockerPlatform: "linux/amd64", TransitionPolicy: testTransitionPolicy(current, next, compatibility.OperationUpgrade)})
	require.NoError(t, err)
	c.isRunningOverride = func(context.Context) (bool, error) { return true, nil }
	c.stopOverride = func(context.Context, int) error { return nil }
	c.backupArchiveOverride = func(context.Context, string) error { return nil }
	starts := 0
	c.startOverride = func(context.Context) error {
		starts++
		return nil
	}
	err = c.Upgrade(context.Background(), next)
	if err == nil || !strings.Contains(err.Error(), "read rollback marker") {
		t.Fatalf("Upgrade() error = %v, want rollback marker read failure", err)
	}
	if starts != 1 {
		t.Fatalf("service restart calls = %d, want 1", starts)
	}
}

func TestUpgradeEnforcesRequirementsBeforeMutation(t *testing.T) {
	root := t.TempDir()
	current := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	next := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	require.NoError(t, os.WriteFile(filepath.Join(root, deploymentEnvName), []byte("LEAPVIEW_IMAGE="+current+"\nCOMPOSE_HTTPS=0\n"), 0o600))
	policy := testTransitionPolicy(current, next, compatibility.OperationUpgrade)
	policy.Transitions = nil
	policy.Releases[0].Defaults.Upgrade = compatibility.Rule{
		Allowed: true, ReasonCode: compatibility.ReasonAllowedExplicitTransition,
	}
	controller, err := New(Options{Root: root, DockerPlatform: "linux/amd64", TransitionPolicy: policy})
	require.NoError(t, err)
	mutated := false
	controller.isRunningOverride = func(context.Context) (bool, error) { mutated = true; return false, nil }
	controller.backupArchiveOverride = func(context.Context, string) error { mutated = true; return nil }
	controller.setImageOverride = func(string) error { mutated = true; return nil }

	err = controller.Upgrade(t.Context(), next)
	require.ErrorContains(t, err, compatibility.RequirementBackupBeforeMutation)
	require.False(t, mutated)
}

func TestUpgradeUsesDockerTargetPlatformForExactPreviousReleaseCandidate(t *testing.T) {
	t.Setenv("DOCKER_DEFAULT_PLATFORM", "linux/arm64")
	root := t.TempDir()
	current := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	next := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	require.NoError(t, os.WriteFile(filepath.Join(root, deploymentEnvName), []byte("LEAPVIEW_IMAGE="+current+"\nCOMPOSE_HTTPS=0\n"), 0o600))
	policy := testTransitionPolicy(current, next, compatibility.OperationUpgrade)
	for index := range policy.Releases {
		policy.Releases[index].Artifacts[0].Platform = "linux/arm64"
	}
	policy.Transitions[0].Platforms = []string{"linux/arm64"}
	controller, err := New(Options{Root: root, DockerBin: "/bin/false", TransitionPolicy: policy})
	require.NoError(t, err)
	controller.isRunningOverride = func(context.Context) (bool, error) { return false, nil }
	backupErr := errors.New("reached admitted transition backup")
	controller.backupArchiveOverride = func(context.Context, string) error { return backupErr }

	err = controller.Upgrade(t.Context(), next)
	require.ErrorIs(t, err, backupErr)
}

func TestDockerTargetPlatformComesFromServerEngine(t *testing.T) {
	t.Setenv("DOCKER_DEFAULT_PLATFORM", "")
	root := t.TempDir()
	docker := filepath.Join(root, "docker")
	require.NoError(t, os.WriteFile(docker, []byte("#!/bin/sh\n[ \"$1\" = version ] || exit 2\nprintf 'linux/arm64\\n'\n"), 0o700))
	controller, err := New(Options{Root: root, DockerBin: docker})
	require.NoError(t, err)
	platform, err := controller.targetDockerPlatform(t.Context())
	require.NoError(t, err)
	require.Equal(t, "linux/arm64", platform)
}

func TestRestorePreflightFailurePreservesRunningStateAndJoinsRestartError(t *testing.T) {
	c, err := New(Options{Root: t.TempDir()})
	require.NoError(t, err)
	restartErr := errors.New("restart failed")
	c.startOverride = func(context.Context) error { return restartErr }
	opErr := errors.New("marker write failed")
	err = c.restorePreflightFailure(context.Background(), true, opErr)
	if !errors.Is(err, opErr) || !errors.Is(err, restartErr) {
		t.Fatalf("joined error=%v", err)
	}
}

func TestRestorePreflightFailureLeavesStoppedServiceStopped(t *testing.T) {
	c, err := New(Options{Root: t.TempDir()})
	require.NoError(t, err)
	called := false
	c.startOverride = func(context.Context) error { called = true; return nil }
	if err := c.restorePreflightFailure(context.Background(), false, errors.New("restore failed")); err == nil {
		t.Fatal("expected operation error")
	}
	if called {
		t.Fatal("stopped service was restarted")
	}
}

func TestUpgradeOperationFaultsPreserveInitialServiceState(t *testing.T) {
	current := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	next := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	for _, test := range []struct {
		name        string
		wasRunning  bool
		markerErr   error
		imageErr    error
		backupErr   error
		pullErr     error
		restartErr  error
		priorMarker bool
		wantCalls   int
	}{
		{name: "running marker", wasRunning: true, markerErr: errors.New("marker"), wantCalls: 1},
		{name: "stopped marker", wasRunning: false, markerErr: errors.New("marker"), wantCalls: 0},
		{name: "running image", wasRunning: true, imageErr: errors.New("image"), wantCalls: 1},
		{name: "stopped image", wasRunning: false, imageErr: errors.New("image"), wantCalls: 0},
		{name: "running backup", wasRunning: true, backupErr: errors.New("backup"), wantCalls: 1},
		{name: "stopped backup", wasRunning: false, backupErr: errors.New("backup"), wantCalls: 0},
		{name: "running pull", wasRunning: true, pullErr: errors.New("pull"), wantCalls: 1},
		{name: "stopped pull", wasRunning: false, pullErr: errors.New("pull"), wantCalls: 0},
		{name: "running restart", wasRunning: true, restartErr: errors.New("restart"), wantCalls: 2},
		{name: "stopped upgrade remains stopped", wasRunning: false, wantCalls: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, deploymentEnvName), []byte("LEAPVIEW_IMAGE="+current+"\nCOMPOSE_HTTPS=0\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			c, err := New(Options{Root: root, DockerPlatform: "linux/amd64", TransitionPolicy: testTransitionPolicy(current, next, compatibility.OperationUpgrade)})
			require.NoError(t, err)
			starts := 0
			c.isRunningOverride = func(context.Context) (bool, error) { return test.wasRunning, nil }
			c.stopOverride = func(context.Context, int) error { return nil }
			c.backupArchiveOverride = func(context.Context, string) error { return test.backupErr }
			c.writePrivateOverride = func(string, []byte) error { return test.markerErr }
			c.setImageOverride = func(string) error { return test.imageErr }
			c.composeOverride = func(_ context.Context, _ io.Reader, _ io.Writer, _ io.Writer, args ...string) error {
				if len(args) >= 1 && args[0] == "pull" {
					return test.pullErr
				}
				return nil
			}
			c.startOverride = func(context.Context) error { starts++; return test.restartErr }
			gotErr := c.Upgrade(context.Background(), next)
			if gotErr == nil && (test.markerErr != nil || test.imageErr != nil || test.backupErr != nil || test.pullErr != nil || test.restartErr != nil) {
				t.Fatal("Upgrade unexpectedly succeeded")
			}
			if gotErr != nil && test.markerErr == nil && test.imageErr == nil && test.backupErr == nil && test.pullErr == nil && test.restartErr == nil {
				t.Fatalf("Upgrade unexpectedly failed: %v", gotErr)
			}
			if starts != test.wantCalls {
				t.Fatalf("start calls=%d want %d error=%v", starts, test.wantCalls, gotErr)
			}
		})
	}
}

func TestUpgradeAppliesAndRollsBackDeploymentPayload(t *testing.T) {
	current := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	next := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	for _, test := range []struct {
		name          string
		restartErr    error
		wantRollbacks int
	}{
		{name: "successful cutover"},
		{name: "failed health check", restartErr: errors.New("unhealthy"), wantRollbacks: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, "backups"), 0o700))
			require.NoError(t, os.WriteFile(
				filepath.Join(root, deploymentEnvName),
				[]byte("LEAPVIEW_IMAGE="+current+"\nCOMPOSE_HTTPS=0\n"),
				0o600,
			))
			update := &recordingDeploymentPayloadUpdate{}
			manager := &recordingDeploymentPayloadManager{update: update}
			controller, err := New(Options{
				Root: root, DeploymentPayloads: manager, DockerPlatform: "linux/amd64",
				TransitionPolicy: testTransitionPolicy(current, next, compatibility.OperationUpgrade),
			})
			require.NoError(t, err)
			controller.isRunningOverride = func(context.Context) (bool, error) { return true, nil }
			controller.stopOverride = func(context.Context, int) error { return nil }
			controller.backupArchiveOverride = func(_ context.Context, path string) error {
				return os.WriteFile(path, []byte("checkpoint"), 0o600)
			}
			controller.restoreArchiveOverride = func(context.Context, string) error { return nil }
			controller.composeOverride = func(context.Context, io.Reader, io.Writer, io.Writer, ...string) error { return nil }
			starts := 0
			controller.startOverride = func(context.Context) error {
				starts++
				if starts == 1 {
					return test.restartErr
				}
				return nil
			}

			err = controller.Upgrade(t.Context(), next)
			if test.restartErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, test.restartErr)
			}
			require.Equal(t, [][2]string{{current, next}}, manager.prepared)
			require.Equal(t, 1, update.applies)
			require.Equal(t, test.wantRollbacks, update.rollbacks)
			require.Equal(t, 1, update.closes)
		})
	}
}

func TestRollbackAppliesAndRestoresDeploymentPayload(t *testing.T) {
	current := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	previous := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	for _, test := range []struct {
		name          string
		restartErr    error
		wantRollbacks int
	}{
		{name: "successful rollback"},
		{name: "failed rollback health check", restartErr: errors.New("unhealthy"), wantRollbacks: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, "backups"), 0o700))
			checkpoint := filepath.Join(root, "backups", "pre-upgrade.tar.gz")
			require.NoError(t, os.WriteFile(checkpoint, []byte("checkpoint"), 0o600))
			require.NoError(t, os.WriteFile(
				filepath.Join(root, deploymentEnvName),
				[]byte("LEAPVIEW_IMAGE="+current+"\nCOMPOSE_HTTPS=0\n"),
				0o600,
			))
			require.NoError(t, os.WriteFile(
				filepath.Join(root, rollbackEnvName),
				[]byte("PREVIOUS_IMAGE="+previous+"\nCHECKPOINT="+checkpoint+"\n"),
				0o600,
			))
			update := &recordingDeploymentPayloadUpdate{}
			manager := &recordingDeploymentPayloadManager{update: update}
			controller, err := New(Options{
				Root: root, DeploymentPayloads: manager, DockerPlatform: "linux/amd64",
				TransitionPolicy: testTransitionPolicy(current, previous, compatibility.OperationRollback),
			})
			require.NoError(t, err)
			controller.isRunningOverride = func(context.Context) (bool, error) { return true, nil }
			controller.stopOverride = func(context.Context, int) error { return nil }
			controller.backupArchiveOverride = func(_ context.Context, path string) error {
				return os.WriteFile(path, []byte("before"), 0o600)
			}
			controller.restoreArchiveOverride = func(context.Context, string) error { return nil }
			starts := 0
			controller.startOverride = func(context.Context) error {
				starts++
				if starts == 1 {
					return test.restartErr
				}
				return nil
			}

			err = controller.Rollback(t.Context(), true)
			if test.restartErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, test.restartErr)
			}
			require.Equal(t, [][2]string{{current, previous}}, manager.prepared)
			require.Equal(t, 1, update.applies)
			require.Equal(t, test.wantRollbacks, update.rollbacks)
			require.Equal(t, 1, update.closes)
		})
	}
}

type recordingDeploymentPayloadManager struct {
	prepared [][2]string
	update   DeploymentPayloadUpdate
}

func (m *recordingDeploymentPayloadManager) Prepare(_ context.Context, current, next, _ string, _ []byte) (DeploymentPayloadUpdate, error) {
	m.prepared = append(m.prepared, [2]string{current, next})
	return m.update, nil
}

type recordingDeploymentPayloadUpdate struct {
	applies   int
	rollbacks int
	closes    int
}

func (u *recordingDeploymentPayloadUpdate) Apply() error    { u.applies++; return nil }
func (u *recordingDeploymentPayloadUpdate) Rollback() error { u.rollbacks++; return nil }
func (u *recordingDeploymentPayloadUpdate) Close() error    { u.closes++; return nil }

func TestRestoreOperationFaultsPreserveInitialServiceState(t *testing.T) {
	for _, test := range []struct {
		name       string
		wasRunning bool
		backupErr  error
		archiveErr error
		startErr   error
		wantStarts int
	}{
		{name: "running backup", wasRunning: true, backupErr: errors.New("backup"), wantStarts: 1},
		{name: "stopped backup", wasRunning: false, backupErr: errors.New("backup")},
		{name: "running archive", wasRunning: true, archiveErr: errors.New("archive"), wantStarts: 1},
		{name: "stopped archive", wasRunning: false, archiveErr: errors.New("archive")},
		{name: "running restart", wasRunning: true, startErr: errors.New("restart"), wantStarts: 2},
		{name: "stopped restore succeeds without start", wasRunning: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, err := New(Options{Root: t.TempDir()})
			require.NoError(t, err)
			starts := 0
			c.isRunningOverride = func(context.Context) (bool, error) { return test.wasRunning, nil }
			c.stopOverride = func(context.Context, int) error { return nil }
			c.backupArchiveOverride = func(context.Context, string) error { return test.backupErr }
			c.restoreArchiveOverride = func(context.Context, string) error { return test.archiveErr }
			c.startOverride = func(context.Context) error { starts++; return test.startErr }
			archive := filepath.Join(t.TempDir(), "restore.tar.gz")
			if err := os.WriteFile(archive, []byte("archive"), 0o600); err != nil {
				t.Fatal(err)
			}
			require.NoError(t, writeBackupChecksum(archive))
			gotErr := c.Restore(context.Background(), archive)
			if gotErr == nil && (test.backupErr != nil || test.archiveErr != nil || test.startErr != nil) {
				if test.wasRunning || test.archiveErr != nil || test.backupErr != nil {
					t.Fatal("Restore unexpectedly succeeded")
				}
			}
			if gotErr != nil && test.backupErr == nil && test.archiveErr == nil && test.startErr == nil {
				t.Fatal("Restore unexpectedly succeeded")
			}
			if starts != test.wantStarts {
				t.Fatalf("start calls=%d want %d error=%v", starts, test.wantStarts, gotErr)
			}
		})
	}
}

func TestRestoreRejectsMissingOrMismatchedChecksumBeforeStopping(t *testing.T) {
	for _, test := range []struct {
		name     string
		checksum string
		want     string
	}{
		{name: "missing", want: "checksum"},
		{name: "mismatched", checksum: strings.Repeat("0", 64) + "\n", want: "checksum mismatch"},
		{name: "malformed", checksum: "not-a-checksum\n", want: "invalid backup checksum"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			archive := filepath.Join(root, "restore.tar.gz")
			require.NoError(t, os.WriteFile(archive, []byte("archive"), 0o600))
			if test.checksum != "" {
				require.NoError(t, os.WriteFile(archive+".sha256", []byte(test.checksum), 0o600))
			}
			controller, err := New(Options{Root: root})
			require.NoError(t, err)
			stopped := false
			controller.isRunningOverride = func(context.Context) (bool, error) { return true, nil }
			controller.stopOverride = func(context.Context, int) error { stopped = true; return nil }

			err = controller.Restore(t.Context(), archive)
			require.ErrorContains(t, err, test.want)
			require.False(t, stopped)
		})
	}
}

func TestUpgradeFaultMatrixRestoresPersistentState(t *testing.T) {
	current := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	next := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	for _, test := range []struct {
		name        string
		wasRunning  bool
		markerErr   error
		imageErr    error
		backupErr   error
		pullErr     error
		restartErr  error
		priorMarker bool
	}{
		{name: "running marker", wasRunning: true, markerErr: errors.New("marker")},
		{name: "running marker preserves prior", wasRunning: true, markerErr: errors.New("marker"), priorMarker: true},
		{name: "stopped marker", wasRunning: false, markerErr: errors.New("marker")},
		{name: "running image", wasRunning: true, imageErr: errors.New("image")},
		{name: "stopped image", wasRunning: false, imageErr: errors.New("image")},
		{name: "running backup", wasRunning: true, backupErr: errors.New("backup")},
		{name: "stopped backup", wasRunning: false, backupErr: errors.New("backup")},
		{name: "running pull", wasRunning: true, pullErr: errors.New("pull")},
		{name: "running pull preserves marker", wasRunning: true, pullErr: errors.New("pull"), priorMarker: true},
		{name: "stopped pull", wasRunning: false, pullErr: errors.New("pull")},
		{name: "running health", wasRunning: true, restartErr: errors.New("restart")},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			originalDeployment := []byte("LEAPVIEW_IMAGE=" + current + "\nCOMPOSE_HTTPS=0\nUNRELATED=value\n")
			if err := os.WriteFile(filepath.Join(root, deploymentEnvName), originalDeployment, 0o600); err != nil {
				t.Fatal(err)
			}
			c, err := New(Options{Root: root, DockerPlatform: "linux/amd64", TransitionPolicy: testTransitionPolicy(current, next, compatibility.OperationUpgrade)})
			require.NoError(t, err)
			running := test.wasRunning
			dataPath := filepath.Join(root, "data.state")
			if err := os.MkdirAll(filepath.Join(root, "backups"), 0o700); err != nil {
				t.Fatal(err)
			}
			priorMarker := []byte("PREVIOUS_IMAGE=ghcr.io/flidai/leapview@sha256:" + strings.Repeat("c", 64) + "\nCHECKPOINT=/tmp/previous.tar.gz\n")
			if test.priorMarker {
				if err := os.WriteFile(filepath.Join(root, rollbackEnvName), priorMarker, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(dataPath, []byte("old-data"), 0o600); err != nil {
				t.Fatal(err)
			}
			c.isRunningOverride = func(context.Context) (bool, error) { return running, nil }
			c.stopOverride = func(context.Context, int) error { running = false; return nil }
			c.backupArchiveOverride = func(_ context.Context, path string) error {
				if test.backupErr != nil {
					return test.backupErr
				}
				return os.WriteFile(path, []byte("old-data"), 0o600)
			}
			c.writePrivateOverride = func(path string, content []byte) error {
				if test.markerErr != nil {
					return test.markerErr
				}
				return os.WriteFile(path, content, 0o600)
			}
			setCalls := 0
			c.setImageOverride = func(image string) error {
				setCalls++
				if test.imageErr != nil && setCalls == 1 {
					return test.imageErr
				}
				return updateEnvFile(filepath.Join(root, deploymentEnvName), map[string]string{"LEAPVIEW_IMAGE": image})
			}
			c.composeOverride = func(_ context.Context, _ io.Reader, _ io.Writer, _ io.Writer, args ...string) error {
				if len(args) > 0 && args[0] == "pull" {
					return test.pullErr
				}
				return nil
			}
			starts := 0
			c.startOverride = func(context.Context) error {
				starts++
				if test.restartErr != nil && starts == 1 {
					running = false
					return test.restartErr
				}
				running = true
				return nil
			}
			c.restoreArchiveOverride = func(_ context.Context, path string) error {
				return os.WriteFile(dataPath, []byte("old-data"), 0o600)
			}

			gotErr := c.Upgrade(context.Background(), next)
			if gotErr == nil && (test.markerErr != nil || test.imageErr != nil || test.backupErr != nil || test.pullErr != nil || test.restartErr != nil) {
				t.Fatal("Upgrade unexpectedly succeeded")
			}
			if gotErr != nil && test.markerErr == nil && test.imageErr == nil && test.backupErr == nil && test.pullErr == nil && test.restartErr == nil {
				t.Fatalf("Upgrade unexpectedly failed: %v", gotErr)
			}
			deployment, err := os.ReadFile(filepath.Join(root, deploymentEnvName))
			if err != nil || string(deployment) != string(originalDeployment) {
				t.Fatalf("deployment.env=%q err=%v, want exact original", deployment, err)
			}
			marker, markerErr := os.ReadFile(filepath.Join(root, rollbackEnvName))
			if test.priorMarker {
				if markerErr != nil || string(marker) != string(priorMarker) {
					t.Fatalf("rollback marker=%q err=%v, want exact prior bytes", marker, markerErr)
				}
			} else if !os.IsNotExist(markerErr) {
				t.Fatalf("rollback marker remains: %v", markerErr)
			}
			contents, err := os.ReadFile(dataPath)
			if err != nil || string(contents) != "old-data" {
				t.Fatalf("data=%q err=%v", contents, err)
			}
			if running != test.wasRunning {
				t.Fatalf("running=%v want %v (starts=%d)", running, test.wasRunning, starts)
			}
		})
	}
}

func TestUpgradeHealthFailureJoinsCleanupFailures(t *testing.T) {
	root := t.TempDir()
	current := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	next := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	if err := os.WriteFile(filepath.Join(root, deploymentEnvName), []byte("LEAPVIEW_IMAGE="+current+"\nCOMPOSE_HTTPS=0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "backups"), 0o700); err != nil {
		t.Fatal(err)
	}
	c, err := New(Options{Root: root, DockerPlatform: "linux/amd64", TransitionPolicy: testTransitionPolicy(current, next, compatibility.OperationUpgrade)})
	require.NoError(t, err)
	primary := errors.New("restart failed")
	stopErr := errors.New("stop cleanup failed")
	imageErr := errors.New("image cleanup failed")
	reinstateErr := errors.New("reinstate failed")
	running := true
	c.isRunningOverride = func(context.Context) (bool, error) { return running, nil }
	stopCalls := 0
	c.stopOverride = func(context.Context, int) error {
		stopCalls++
		running = false
		if stopCalls > 1 {
			return stopErr
		}
		return nil
	}
	c.backupArchiveOverride = func(_ context.Context, path string) error { return os.WriteFile(path, []byte("old-data"), 0o600) }
	c.writePrivateOverride = func(path string, content []byte) error { return os.WriteFile(path, content, 0o600) }
	setCalls := 0
	c.setImageOverride = func(image string) error {
		setCalls++
		if setCalls > 1 {
			return imageErr
		}
		return updateEnvFile(filepath.Join(root, deploymentEnvName), map[string]string{"LEAPVIEW_IMAGE": image})
	}
	c.composeOverride = func(context.Context, io.Reader, io.Writer, io.Writer, ...string) error { return nil }
	c.startOverride = func(context.Context) error { return primary }
	c.restoreArchiveOverride = func(context.Context, string) error { return reinstateErr }
	gotErr := c.Upgrade(context.Background(), next)
	for _, want := range []error{primary, stopErr, imageErr, reinstateErr} {
		if !errors.Is(gotErr, want) {
			t.Fatalf("Upgrade error=%v missing %v", gotErr, want)
		}
	}
}

func TestFirstLoginRetainsCredentialsUntilOutputSucceeds(t *testing.T) {
	root := t.TempDir()
	credentialsPath := filepath.Join(root, credentialsName)
	credentials := []byte("{\"temporaryPassword\":\"temporary\"}\n")
	if err := os.WriteFile(credentialsPath, credentials, 0o600); err != nil {
		t.Fatal(err)
	}
	controller, err := New(Options{Root: root, Stdout: failingWriter{}})
	require.NoError(t, err)
	if err := controller.FirstLogin(); err == nil {
		t.Fatal("first-login output failure = nil")
	}
	if contents, err := os.ReadFile(credentialsPath); err != nil || !bytes.Equal(contents, credentials) {
		t.Fatalf("credentials after output failure = %q, %v", contents, err)
	}

	var output bytes.Buffer
	controller, err = New(Options{Root: root, Stdout: &output})
	require.NoError(t, err)
	if err := controller.FirstLogin(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), credentials) {
		t.Fatalf("first-login output = %q", output.Bytes())
	}
	if _, err := os.Stat(credentialsPath); !os.IsNotExist(err) {
		t.Fatalf("credentials remain after successful output: %v", err)
	}
}

func testTransitionPolicy(current, next string, operation compatibility.Operation) *compatibility.Policy {
	platform := "linux/amd64"
	candidateRelease := "v1.1.0"
	if operation == compatibility.OperationRollback {
		candidateRelease = "v1.0.0"
	}
	denied := compatibility.Rule{
		ReasonCode:  compatibility.ReasonDeniedNoExplicitRule,
		Remediation: "use an explicitly supported transition",
	}
	return &compatibility.Policy{
		SchemaVersion:    compatibility.CurrentSchemaVersion,
		PolicyVersion:    "test/v1",
		CandidateRelease: candidateRelease,
		Releases: []compatibility.Release{
			{
				ID: "v1.0.0", Version: "1.0.0", SourceRevision: strings.Repeat("a", 40), Distribution: "test",
				LegacyBackupVersions: []int{},
				Artifacts:            []compatibility.Artifact{{Platform: platform, Image: current}},
				Defaults: compatibility.ReleaseDefaults{
					FreshInstall: compatibility.Rule{Allowed: true, ReasonCode: compatibility.ReasonAllowedFreshInstall},
					Upgrade:      denied, Rollback: denied,
				},
			},
			{
				ID: "v1.1.0", Version: "1.1.0", SourceRevision: strings.Repeat("b", 40), Distribution: "test",
				LegacyBackupVersions: []int{},
				Artifacts:            []compatibility.Artifact{{Platform: platform, Image: next}},
				Defaults: compatibility.ReleaseDefaults{
					FreshInstall: compatibility.Rule{Allowed: true, ReasonCode: compatibility.ReasonAllowedFreshInstall},
					Upgrade:      denied, Rollback: denied,
				},
			},
		},
		Transitions: []compatibility.Transition{{
			Operation: operation, From: "v1.0.0", To: "v1.1.0", Platforms: []string{platform},
			Decision: compatibility.Rule{Allowed: true, ReasonCode: compatibility.ReasonAllowedExplicitTransition, Requirements: []string{
				compatibility.RequirementBackupBeforeMutation, compatibility.RequirementStoppedInstance,
			}},
		}},
	}
}

func TestUpdateEnvFileIsPrivateAndRejectsMissingContractKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deployment.env")
	if err := os.WriteFile(path, []byte("LEAPVIEW_IMAGE=old\nCOMPOSE_HTTPS=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := updateEnvFile(path, map[string]string{"LEAPVIEW_IMAGE": "new"}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "LEAPVIEW_IMAGE=new\nCOMPOSE_HTTPS=1\n" {
		t.Fatalf("updated environment = %q, %v", contents, err)
	}
	info, err := os.Stat(path)
	require.NoError(t, err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("environment permissions = %v", info.Mode().Perm())
	}
	if err := updateEnvFile(path, map[string]string{"CADDY_DOMAIN": "dash.example.com"}); err == nil {
		t.Fatal("missing environment key update succeeded")
	}
}

func TestEnvironmentLineValuesRejectConfigurationInjection(t *testing.T) {
	for _, value := range []string{"prod\nLEAPVIEW_CSRF_KEY=forged", "dash.example.com\rCOMPOSE_HTTPS=0", "admin@example.com\x00suffix"} {
		if err := validateEnvLineValue("test value", value); err == nil {
			t.Fatalf("configuration injection value %q was accepted", value)
		}
	}
	if err := validateEnvLineValue("domain", "dash.example.com"); err != nil {
		t.Fatalf("ordinary value rejected: %v", err)
	}
}

func TestInitializeRejectsInvalidPublicDomainBeforeStateMutation(t *testing.T) {
	root := t.TempDir()
	example := "LEAPVIEW_IMAGE=example.com/leapview@sha256:" + strings.Repeat("a", 64) +
		"\nCADDY_IMAGE=example.com/caddy@sha256:" + strings.Repeat("b", 64) +
		"\nCADDY_DOMAIN=dash.example.com\nCOMPOSE_HTTPS=1\n"
	if err := os.WriteFile(filepath.Join(root, "deployment.env.example"), []byte(example), 0o600); err != nil {
		t.Fatal(err)
	}
	controller, err := New(Options{Root: root, DockerBin: "/bin/false"})
	require.NoError(t, err)

	err = controller.Initialize(context.Background(), InitOptions{
		AdminEmail: "admin@example.com",
		Domain:     "https://dash.example.com",
	})
	if err == nil || !strings.Contains(err.Error(), "--domain must be a hostname") {
		t.Fatalf("invalid public domain error = %v", err)
	}
	for _, name := range []string{deploymentEnvName, appEnvName, credentialsName, controllerLockName} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Errorf("invalid public domain mutated %s: %v", name, err)
		}
	}
}

func TestCanonicalPublicDomain(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "dash.example.com", want: "dash.example.com"},
		{input: " Dash.Example.COM. ", want: "dash.example.com"},
		{input: "localhost", want: "localhost"},
	} {
		t.Run(test.input, func(t *testing.T) {
			got, err := canonicalPublicDomain(test.input)
			if err != nil || got != test.want {
				t.Fatalf("canonicalPublicDomain(%q) = %q, %v; want %q", test.input, got, err, test.want)
			}
		})
	}
	for _, input := range []string{
		"https://dash.example.com",
		"dash.example.com/path",
		"dash.example.com:8443",
		"user@dash.example.com",
		"*.example.com",
		"-dash.example.com",
		"dash..example.com",
		"dash_example.com",
	} {
		t.Run("reject "+input, func(t *testing.T) {
			if got, err := canonicalPublicDomain(input); err == nil {
				t.Fatalf("canonicalPublicDomain(%q) = %q, nil", input, got)
			}
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("output failed")
}
