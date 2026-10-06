//go:build linux

package composectl_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/cli/composectl"
	"github.com/flidai/leapview/internal/app/cli/hostinstall"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBundledPostgresColdSnapshotRestoreQualification(t *testing.T) {
	if os.Getenv("LEAPVIEW_TEST_BUNDLED_POSTGRES_DOCKER") != "1" {
		t.Skip("set LEAPVIEW_TEST_BUNDLED_POSTGRES_DOCKER=1 to run the isolated Docker restore qualification")
	}
	if runBundledPostgresRecoveryAsRoot(t) {
		return
	}

	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repository := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../../../.."))
	project := "lvpgrestore-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	var restoreParent string
	var existingRestoreDirectories map[string]struct{}
	t.Cleanup(func() {
		if restoreParent == "" {
			return
		}
		entries, readErr := os.ReadDir(restoreParent)
		if os.IsNotExist(readErr) {
			return
		}
		if readErr != nil {
			t.Errorf("read owned PostgreSQL volume directory after Docker cleanup: %v", readErr)
			return
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), ".restore-") {
				continue
			}
			if _, existedBefore := existingRestoreDirectories[entry.Name()]; existedBefore {
				continue
			}
			path := filepath.Join(restoreParent, entry.Name())
			info, statErr := os.Lstat(path)
			if statErr != nil {
				t.Errorf("inspect owned restore staging directory %s: %v", path, statErr)
				continue
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				t.Errorf("refuse to remove non-directory restore staging path %s", path)
				continue
			}
			if removeErr := os.RemoveAll(path); removeErr != nil {
				t.Errorf("remove owned restore staging directory %s: %v", path, removeErr)
			}
		}
	})
	root, secretRoot, controller := composectl.NewBundledPostgresRecoveryQualificationProject(t, repository, project)
	require.NoError(t, os.WriteFile(filepath.Join(root, "leapview.env"), []byte("LEAPVIEW_POSTGRES_REQUIRE_TLS=true\n"), 0o600))

	connections, err := controller.EnsureBundledPostgresRecoveryQualification(ctx)
	require.NoError(t, err)
	require.Contains(t, connections.ControlURL, "sslmode=verify-full")
	require.Contains(t, connections.DuckLakeURL, "sslmode=verify-full")
	secretFiles := readRecoverySecretFiles(t, secretRoot)
	readyPath := filepath.Join(root, ".postgres-credentials-ready")
	profilePath := filepath.Join(root, ".host-postgres-profile")
	readyMarker, err := os.ReadFile(readyPath)
	require.NoError(t, err)
	profileMarker, err := os.ReadFile(profilePath)
	require.NoError(t, err)

	for _, database := range []string{"leapview_control", "leapview_ducklake"} {
		_, err = composectl.QueryBundledPostgresRecoveryQualification(ctx, root, database,
			"CREATE TABLE recovery_snapshot_probe (value text PRIMARY KEY); INSERT INTO recovery_snapshot_probe VALUES ('captured');")
		require.NoError(t, err)
	}
	for _, grant := range []struct{ database, role, secret string }{
		{"leapview_control", "leapview_control_runtime", "control-runtime-password"},
		{"leapview_ducklake", "leapview_ducklake_runtime", "ducklake-runtime-password"},
	} {
		_, err = composectl.QueryBundledPostgresRecoveryQualification(ctx, root, grant.database,
			"GRANT USAGE ON SCHEMA public TO "+grant.role+"; GRANT SELECT ON recovery_snapshot_probe TO "+grant.role)
		require.NoError(t, err)
		got := bundledPostgresRecoveryTLSQuery(ctx, t, controller, grant.database, grant.role, grant.secret)
		require.Equal(t, "captured:true", got, "the pre-snapshot row must be readable through verify-full TLS")
	}

	require.NoError(t, controller.ComposeBundledPostgresRecoveryQualification(ctx, nil, "stop", "--timeout", "10", "postgres"))
	volumePath := inspectBundledPostgresVolume(t, ctx, project)
	restoreParentCandidate := filepath.Dir(volumePath)
	existingRestoreDirectories = recoveryRestoreDirectories(t, restoreParentCandidate)
	restoreParent = restoreParentCandidate
	snapshotRoot := filepath.Join(t.TempDir(), "cold-point")
	volumes := map[string]string{"compose": root, "postgres": volumePath}
	digest, err := hostinstall.CaptureStoppedDirectories(ctx, snapshotRoot, project, volumes)
	require.NoError(t, err, "the PostgreSQL writer must be stopped before the paired cold copy")

	// Advance both databases after capture so the restore must recover the exact
	// paired point rather than merely restart the same cluster unchanged.
	_, err = controller.EnsureBundledPostgresRecoveryQualification(ctx)
	require.NoError(t, err)
	for _, database := range []string{"leapview_control", "leapview_ducklake"} {
		_, err = composectl.QueryBundledPostgresRecoveryQualification(ctx, root, database,
			"UPDATE recovery_snapshot_probe SET value = 'after-snapshot'")
		require.NoError(t, err)
	}
	for _, grant := range []struct{ database, role, secret string }{
		{"leapview_control", "leapview_control_runtime", "control-runtime-password"},
		{"leapview_ducklake", "leapview_ducklake_runtime", "ducklake-runtime-password"},
	} {
		got := bundledPostgresRecoveryTLSQuery(ctx, t, controller, grant.database, grant.role, grant.secret)
		require.Equal(t, "after-snapshot:true", got)
	}
	require.NoError(t, controller.ComposeBundledPostgresRecoveryQualification(ctx, nil, "stop", "--timeout", "10", "postgres"))

	missingSecretPath := filepath.Join(secretRoot, "control-runtime-password")
	missingSecret, err := os.ReadFile(missingSecretPath)
	require.NoError(t, err)
	missingSecretInfo, err := os.Stat(missingSecretPath)
	require.NoError(t, err)
	require.NoError(t, os.Remove(missingSecretPath))
	_, err = controller.EnsureBundledPostgresRecoveryQualification(ctx)
	require.ErrorContains(t, err, "completed PostgreSQL credential set is missing or invalid",
		"the completed-set marker must prevent regeneration of a missing password")
	_, err = os.Lstat(missingSecretPath)
	require.True(t, os.IsNotExist(err), "startup must leave the missing password absent")

	require.Error(t, hostinstall.RestoreStoppedDirectories(ctx, snapshotRoot, project+"-mismatch", digest, volumes),
		"a snapshot bound to another target must be rejected")
	tamperedSecretPath := filepath.Join(snapshotRoot, "compose", ".postgres-secrets", "control-runtime-password")
	originalSnapshotSecret, err := os.ReadFile(tamperedSecretPath)
	require.NoError(t, err)
	secretInfo, err := os.Stat(tamperedSecretPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(tamperedSecretPath, append(append([]byte(nil), originalSnapshotSecret...), 'x'), secretInfo.Mode().Perm()))
	require.ErrorContains(t, hostinstall.RestoreStoppedDirectories(ctx, snapshotRoot, project, digest, volumes), "snapshot volume compose is corrupt",
		"tampered recovery credentials must be rejected before either destination changes")
	require.NoError(t, os.WriteFile(tamperedSecretPath, originalSnapshotSecret, secretInfo.Mode().Perm()))
	_, err = os.Lstat(missingSecretPath)
	require.True(t, os.IsNotExist(err), "rejected recovery must leave the live credential set untouched")
	require.NoError(t, os.WriteFile(missingSecretPath, missingSecret, missingSecretInfo.Mode().Perm()))

	// Reopening the current volume after a rejected recovery proves the failed
	// attempt did not partially replace its database files.
	_, err = controller.EnsureBundledPostgresRecoveryQualification(ctx)
	require.NoError(t, err)
	for _, grant := range []struct{ database, role, secret string }{
		{"leapview_control", "leapview_control_runtime", "control-runtime-password"},
		{"leapview_ducklake", "leapview_ducklake_runtime", "ducklake-runtime-password"},
	} {
		got := bundledPostgresRecoveryTLSQuery(ctx, t, controller, grant.database, grant.role, grant.secret)
		require.Equal(t, "after-snapshot:true", got, "tampered recovery must leave the live database intact")
	}
	require.NoError(t, controller.ComposeBundledPostgresRecoveryQualification(ctx, nil, "stop", "--timeout", "10", "postgres"))

	// Remove the current profile and credentials after the live-volume proof.
	// The matching Compose root in the cold point must restore them before the
	// canonical bundled startup can succeed again.
	require.NoError(t, os.RemoveAll(secretRoot))
	require.NoError(t, os.Remove(readyPath))
	require.NoError(t, os.Remove(profilePath))
	require.NoError(t, hostinstall.RestoreStoppedDirectories(ctx, snapshotRoot, project, digest, volumes))
	require.Equal(t, secretFiles, readRecoverySecretFiles(t, filepath.Join(root, ".postgres-secrets")))
	readyAfterRestore, err := os.ReadFile(readyPath)
	require.NoError(t, err)
	profileAfterRestore, err := os.ReadFile(profilePath)
	require.NoError(t, err)
	require.Equal(t, readyMarker, readyAfterRestore, "the credential-ready marker must match the recovered secret set")
	require.Equal(t, profileMarker, profileAfterRestore, "the selected PostgreSQL profile must survive recovery")

	connections, err = controller.EnsureBundledPostgresRecoveryQualification(ctx)
	require.NoError(t, err, "canonical startup must accept the restored markers and credential material")
	require.Contains(t, connections.ControlURL, "sslmode=verify-full")
	require.Contains(t, connections.DuckLakeURL, "sslmode=verify-full")
	for _, grant := range []struct{ database, role, secret string }{
		{"leapview_control", "leapview_control_runtime", "control-runtime-password"},
		{"leapview_ducklake", "leapview_ducklake_runtime", "ducklake-runtime-password"},
	} {
		got := bundledPostgresRecoveryTLSQuery(ctx, t, controller, grant.database, grant.role, grant.secret)
		require.Equal(t, "captured:true", got, "cold restore must recover database state and pass verify-full TLS")
	}
}

func recoveryRestoreDirectories(t *testing.T, parent string) map[string]struct{} {
	t.Helper()
	entries, err := os.ReadDir(parent)
	require.NoError(t, err)
	existing := make(map[string]struct{})
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".restore-") {
			existing[entry.Name()] = struct{}{}
		}
	}
	return existing
}

func runBundledPostgresRecoveryAsRoot(t *testing.T) bool {
	t.Helper()
	if os.Geteuid() == 0 {
		return false
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	args := []string{"-n", "env", "LEAPVIEW_TEST_BUNDLED_POSTGRES_DOCKER=1"}
	if tempDir := os.Getenv("TMPDIR"); tempDir != "" {
		args = append(args, "TMPDIR="+tempDir)
	}
	args = append(args, executable, "-test.run=^TestBundledPostgresColdSnapshotRestoreQualification$", "-test.v", "-test.timeout=8m")
	ctx, cancel := context.WithTimeout(t.Context(), 9*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, "sudo", args...).CombinedOutput()
	require.NoError(t, err, "root cold-restore self-test failed:\n%s", output)
	t.Log(string(output))
	return true
}

func inspectBundledPostgresVolume(t *testing.T, ctx context.Context, project string) string {
	t.Helper()
	volume := project + "_leapview-postgres-data"
	output, err := exec.CommandContext(ctx, "docker", "volume", "inspect", volume, "--format", "{{.Mountpoint}}").CombinedOutput()
	require.NoError(t, err, "inspect the isolated PostgreSQL volume: %s", output)
	path := strings.TrimSpace(string(output))
	require.True(t, filepath.IsAbs(path), "Docker volume mountpoint must be absolute: %q", path)
	return path
}

func readRecoverySecretFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	files := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		require.True(t, entry.Type().IsRegular(), "recovery credential entry %s must be a regular file", entry.Name())
		contents, readErr := os.ReadFile(filepath.Join(root, entry.Name()))
		require.NoError(t, readErr)
		files[entry.Name()] = contents
	}
	return files
}

func bundledPostgresRecoveryTLSQuery(
	ctx context.Context,
	t *testing.T,
	controller *composectl.Controller,
	database, role, secret string,
) string {
	t.Helper()
	script := fmt.Sprintf(`set -eu
password=$(cat /run/leapview-postgres-secrets/%s)
PGPASSWORD="$password" PGSSLMODE=verify-full PGSSLROOTCERT=/run/leapview-postgres-secrets/ca.crt \
  psql --no-psqlrc --tuples-only --no-align --host=postgres --port=5432 \
  --username=%s --dbname=%s \
  --command "SELECT (SELECT value FROM recovery_snapshot_probe LIMIT 1) || ':' || (SELECT ssl::text FROM pg_stat_ssl WHERE pid = pg_backend_pid())"`, secret, role, database)
	var output bytes.Buffer
	err := controller.ComposeBundledPostgresRecoveryQualification(ctx, &output, "exec", "-T", "postgres", "/bin/sh", "-ec", script)
	require.NoError(t, err, "verify-full query failed for %s.%s: %s", database, role, output.String())
	return strings.TrimSpace(output.String())
}
