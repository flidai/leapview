package localruntime

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestInspectRuntimePackageVerifiesInstalledAssetChecksumsWithoutWrites(t *testing.T) {
	root, checksumPath := writeInspectionRuntimePackage(t)
	parent := filepath.Dir(root)
	before := snapshotFiles(t, parent)

	info, err := InspectRuntimePackage(root, testBuildIdentity())
	require.NoError(t, err)
	require.Equal(t, "1.2.3", info.Version)
	require.True(t, strings.HasPrefix(info.Digest, "sha256:"))
	require.Equal(t, before, snapshotFiles(t, parent))

	composePath := filepath.Join(root, composeFileName)
	compose, err := os.ReadFile(composePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(composePath, append(compose, []byte("# changed after packaging\n")...), 0o600))
	changed := snapshotFiles(t, parent)
	_, err = InspectRuntimePackage(root, testBuildIdentity())
	require.ErrorContains(t, err, "checksum mismatch")
	require.Equal(t, changed, snapshotFiles(t, parent))
	_, err = os.Stat(checksumPath)
	require.NoError(t, err)
}

func TestInspectRuntimePackageRejectsIncompleteOrMalformedChecksumsWithoutWrites(t *testing.T) {
	for _, test := range []struct {
		name       string
		edit       func([]string) []string
		removeFile bool
	}{
		{name: "missing checksum file", removeFile: true},
		{name: "missing runtime asset entry", edit: func(lines []string) []string { return lines[1:] }},
		{name: "duplicate entry", edit: func(lines []string) []string { return append(lines, lines[0]) }},
		{name: "malformed digest", edit: func(lines []string) []string {
			return append([]string{strings.Repeat("z", 64) + lines[0][64:]}, lines[1:]...)
		}},
		{name: "traversal path", edit: func(lines []string) []string {
			return append(lines, fmt.Sprintf("%x  ./../outside", sha256.Sum256([]byte("outside"))))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, checksumPath := writeInspectionRuntimePackage(t)
			if test.removeFile {
				require.NoError(t, os.Remove(checksumPath))
			} else {
				contents, err := os.ReadFile(checksumPath)
				require.NoError(t, err)
				lines := strings.Split(strings.TrimSuffix(string(contents), "\n"), "\n")
				require.NoError(t, os.WriteFile(checksumPath, []byte(strings.Join(test.edit(lines), "\n")+"\n"), 0o600))
			}
			before := snapshotFiles(t, filepath.Dir(root))
			_, err := InspectRuntimePackage(root, testBuildIdentity())
			require.Error(t, err)
			require.Equal(t, before, snapshotFiles(t, filepath.Dir(root)))
		})
	}
}

func writeInspectionRuntimePackage(t *testing.T) (string, string) {
	t.Helper()
	parent := t.TempDir()
	root := filepath.Join(parent, "local-runtime")
	require.NoError(t, os.Rename(testRuntimePackage(t), root))
	var checksums strings.Builder
	for _, name := range []string{manifestFileName, manifestSchemaName, composeFileName, postgresInitName} {
		contents, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)
		fmt.Fprintf(&checksums, "%x  ./local-runtime/%s\n", sha256.Sum256(contents), name)
	}
	checksumPath := filepath.Join(parent, "SHA256SUMS")
	require.NoError(t, os.WriteFile(checksumPath, []byte(checksums.String()), 0o600))
	return root, checksumPath
}

func TestInspectMissingRuntimeDoesNotCreateStateDirectory(t *testing.T) {
	checkout, packageRoot, stateRoot := t.TempDir(), testRuntimePackage(t), filepath.Join(t.TempDir(), "missing-state")
	endpoint := &fakeEndpoint{host: "unix:///var/run/docker.sock", server: "engine", fingerprint: "sha256:endpoint"}
	status, err := Inspect(context.Background(), InspectionOptions{
		CheckoutRoot: checkout, RuntimePackage: packageRoot, StateRoot: stateRoot,
		Endpoint: endpoint, BuildIdentity: testBuildIdentity(), Runner: &fakeRunner{},
	})
	require.NoError(t, err)
	require.False(t, status.Exists)
	_, err = os.Stat(stateRoot)
	require.True(t, errors.Is(err, os.ErrNotExist), "inspector created the missing state directory")
}

func TestInspectExistingRuntimeLeavesStateAndAttachmentsUnchanged(t *testing.T) {
	checkoutRoot, packageRoot, stateRoot := t.TempDir(), testRuntimePackage(t), t.TempDir()
	endpoint := &fakeEndpoint{host: "unix:///var/run/docker.sock", server: "engine", fingerprint: "sha256:endpoint"}
	canonicalCheckout, checkoutID, err := checkoutIdentity(checkoutRoot)
	require.NoError(t, err)
	manifest, manifestDigest, err := loadManifest(packageRoot, testBuildIdentity())
	require.NoError(t, err)
	runtimeRoot := stateDirectory(stateRoot, checkoutID)
	require.NoError(t, os.MkdirAll(runtimeRoot, 0o700))
	state := State{
		SchemaVersion: stateSchemaVersion, AttachmentRegistryVersion: attachmentSchemaVersion,
		Status: statusApplied, Phase: phaseReady, OperationID: uuid.NewString(),
		Checkout:  checkout{CanonicalRoot: canonicalCheckout, ID: checkoutID},
		Runtime:   runtimeID{OwnerID: "owner", ComposeProject: "project", ManifestDigest: manifestDigest, Version: testBuildIdentity().Version, Revision: testBuildIdentity().Revision},
		Endpoint:  endpointID{Host: endpoint.Host(), ServerID: endpoint.ServerID(), Fingerprint: endpoint.Fingerprint()},
		Network:   networkID{AppPort: 54321, URL: "http://127.0.0.1:54321"},
		Authority: authorityID{Environment: "dev", IssuerID: "lvissuer_test", ProjectUID: "lvproject_test", PoolID: "pool", CompatibilityDigest: "sha256:compatibility"},
	}
	state.Session.TargetName = sessionTargetName(state)
	statePath := filepath.Join(runtimeRoot, stateFileName)
	require.NoError(t, saveState(statePath, state))
	credentialPath := filepath.Join(runtimeRoot, developmentCredentialsFileName)
	environment := map[string]string{
		"LEAPVIEW_IMAGE":                                       manifest.LeapView.Image,
		"LEAPVIEW_LOCAL_APP_PORT":                              "54321",
		"LEAPVIEW_LOCAL_CHECKOUT_ID":                           checkoutID,
		"LEAPVIEW_LOCAL_OWNER_ID":                              "owner",
		"LEAPVIEW_DEVELOPMENT_CREDENTIAL_ENV_FILE":             credentialPath,
		"LEAPVIEW_DEVELOPMENT_CREDENTIAL_VARIABLES":            "",
		"LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID":                   "pool",
		"LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST": "sha256:compatibility",
	}
	for _, key := range []string{
		"LEAPVIEW_POSTGRES_BOOTSTRAP_PASSWORD", "LEAPVIEW_POSTGRES_CONTROL_RUNTIME_PASSWORD",
		"LEAPVIEW_POSTGRES_CONTROL_READONLY_PASSWORD", "LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_PASSWORD",
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_PASSWORD", "LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_PASSWORD",
		"LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_PASSWORD", "LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_PASSWORD",
		"LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_PASSWORD", "LEAPVIEW_CSRF_KEY",
	} {
		environment[key] = "retained-secret"
	}
	require.NoError(t, writeEnvironment(filepath.Join(runtimeRoot, runtimeEnvFileName), environment))
	now := time.Now().UTC()
	registry := attachmentRegistry{
		SchemaVersion: attachmentSchemaVersion, Binding: attachmentBindingFor(state),
		Attachments: []attachmentRecord{{
			ID: uuid.NewString(), TokenDigest: attachmentTokenDigest("opaque-test-token"), PID: os.Getpid(),
			AttachedAt: now, HeartbeatAt: now,
		}},
	}
	require.NoError(t, saveAttachmentRegistry(filepath.Join(runtimeRoot, attachmentsFileName), registry))
	before := snapshotFiles(t, runtimeRoot)
	for _, lock := range []string{controllerLock, attachmentsLockName} {
		_, statErr := os.Stat(filepath.Join(runtimeRoot, lock))
		require.True(t, errors.Is(statErr, os.ErrNotExist), "test precondition: lock %s already exists", lock)
	}

	runner := &fakeRunner{}
	status, err := Inspect(context.Background(), InspectionOptions{
		CheckoutRoot: checkoutRoot, RuntimePackage: packageRoot, StateRoot: stateRoot,
		Endpoint: endpoint, BuildIdentity: testBuildIdentity(), Runner: runner,
	})
	require.NoError(t, err)
	require.True(t, status.Exists)
	require.Len(t, status.Attachments, 1)
	require.Equal(t, before, snapshotFiles(t, runtimeRoot))
	for _, command := range runner.commands {
		joined := strings.Join(command, " ")
		for _, forbidden := range []string{" up ", " down ", " stop ", " rm ", " pull ", " start ", " run "} {
			require.NotContains(t, " "+joined+" ", forbidden, "inspection ran a mutating command: %s", joined)
		}
	}
	for _, lock := range []string{controllerLock, attachmentsLockName} {
		_, statErr := os.Stat(filepath.Join(runtimeRoot, lock))
		require.True(t, errors.Is(statErr, os.ErrNotExist), "inspector created lock %s", lock)
	}
}

func TestInspectCorruptStateDoesNotRepairOrLockIt(t *testing.T) {
	checkout, packageRoot, stateRoot := t.TempDir(), testRuntimePackage(t), t.TempDir()
	endpoint := &fakeEndpoint{host: "unix:///var/run/docker.sock", server: "engine", fingerprint: "sha256:endpoint"}
	_, checkoutID, err := checkoutIdentity(checkout)
	require.NoError(t, err)
	runtimeRoot := stateDirectory(stateRoot, checkoutID)
	require.NoError(t, os.MkdirAll(runtimeRoot, 0o700))
	statePath := filepath.Join(runtimeRoot, stateFileName)
	corrupt := []byte("{broken\n")
	require.NoError(t, os.WriteFile(statePath, corrupt, 0o600))
	before := snapshotFiles(t, runtimeRoot)
	_, err = Inspect(context.Background(), InspectionOptions{
		CheckoutRoot: checkout, RuntimePackage: packageRoot, StateRoot: stateRoot,
		Endpoint: endpoint, BuildIdentity: testBuildIdentity(), Runner: &fakeRunner{},
	})
	require.Error(t, err)
	require.Equal(t, before, snapshotFiles(t, runtimeRoot))
	for _, lock := range []string{controllerLock, attachmentsLockName} {
		_, statErr := os.Stat(filepath.Join(runtimeRoot, lock))
		require.True(t, errors.Is(statErr, os.ErrNotExist), "inspector created lock %s", lock)
	}
}

func snapshotFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[relative] = string(contents)
		return nil
	})
	require.NoError(t, err)
	return result
}
