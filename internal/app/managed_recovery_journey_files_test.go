package app

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/app/managedrecovery"
	appobjectstore "github.com/flidai/leapview/internal/app/objectstore"
	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/platform/objectstore"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/stretchr/testify/require"
)

type managedJourneyRestoredRoot struct {
	root                recoveryset.ObjectRoot
	source, destination string
	manifest            managedrecovery.FileManifest
}

func managedJourneyFileRestore(t *testing.T, f *sourceCredentialHTTPJourney, set recoveryset.RecoverySet) []managedJourneyRestoredRoot {
	program := os.Getenv("LEAPVIEW_TEST_MANAGED_RESTIC")
	if program == "" {
		t.Skip("explicit pinned Restic integration input required")
	}
	base := t.TempDir()
	password := filepath.Join(base, "password")
	require.NoError(t, os.WriteFile(password, []byte("disposable actual-publication backup password"), 0600))
	repository := filepath.Join(base, "repository")
	run := func(args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(t.Context(), program, append([]string{"--no-cache", "--repo", repository, "--password-file", password}, args...)...)
		command.Env = []string{"PATH=/nonexistent", "LANG=C", "TZ=UTC"}
		output, err := command.Output()
		require.NoError(t, err, "actual pinned Restic operation failed")
		return output
	}
	run("init")
	store, _, err := appobjectstore.New(t.Context(), f.config, f.instance, f.config.Environment)
	require.NoError(t, err)
	closure := managedJourneyNativeClosure(t, f, set.Serving)
	var restoredRoots []managedJourneyRestoredRoot
	for _, retained := range set.ObjectRoots {
		t.Run(retained.Kind, func(t *testing.T) {
			root := retained
			storage := ""
			var metadata *objectstore.ObjectMetadata
			var originalPayload []byte
			if root.Kind == recoveryset.ObjectRootServingArtifact {
				storage = filepath.Join(f.config.ArtifactDir(), "object-store")
				object, err := store.Open(t.Context(), root.URI)
				require.NoError(t, err)
				originalPayload, err = io.ReadAll(object.Body)
				require.NoError(t, err)
				require.NoError(t, object.Body.Close())
				i := object.Info
				metadata = &objectstore.ObjectMetadata{StorageSecurityDomain: i.StorageSecurityDomain, Digest: i.Digest, SizeBytes: i.SizeBytes, ContentType: i.ContentType, MetadataDigest: i.MetadataDigest}
			}
			source, err := providerrestore.ManagedLocalRootPath(root, storage)
			require.NoError(t, err)
			manifestRoot := source
			if metadata != nil {
				manifestRoot = filepath.Dir(source)
			}
			manifest, err := managedrecovery.CaptureFiles(manifestRoot)
			require.NoError(t, err)
			if metadata != nil {
				selected := []managedrecovery.FileContent{}
				for _, file := range manifest.Files {
					if file.Path == filepath.Base(source) {
						selected = append(selected, file)
					}
				}
				require.Len(t, selected, 1, "the actual serving artifact envelope must exist")
				manifest.Files = selected
			}
			manifestDigest, err := manifest.Digest()
			require.NoError(t, err)
			if metadata == nil {
				require.NoError(t, managedrecovery.VerifyManagedClosure(set.Serving, closure, manifest))
			}
			var snapshot string
			for _, line := range bytes.Split(run("backup", "--json", source), []byte("\n")) {
				var result struct {
					Type string `json:"message_type"`
					ID   string `json:"snapshot_id"`
				}
				if json.Unmarshal(line, &result) == nil && result.Type == "summary" {
					snapshot = result.ID
				}
			}
			require.Len(t, snapshot, 64)
			root.VersionID, root.ProviderRecoveryFrontier = snapshot, "restic:"+snapshot
			parent := filepath.Join(base, "replacement", root.Kind)
			require.NoError(t, os.MkdirAll(parent, 0700))
			destination := filepath.Join(parent, filepath.Base(source))
			restorer, err := managedrecovery.NewRestic(managedrecovery.ResticConfig{TargetID: f.instance, RecoverySetID: set.ID, Root: root, StorageRoot: storage, Restic: program, Repository: repository, PasswordFile: password, Destination: destination, Manifest: manifest, ManifestDigest: manifestDigest, ArtifactMetadata: metadata})
			require.NoError(t, err)
			request := providerrestore.ObjectRequest{TargetID: f.instance, RecoverySetID: set.ID, Root: root, IdempotencyKey: "actual-publication-" + root.Kind}
			for attempt := 0; attempt < 2; attempt++ {
				_, err := restorer.RestoreObject(t.Context(), request)
				require.NoError(t, err)
			}
			if metadata == nil {
				require.NoError(t, manifest.Verify(destination))
			} else {
				_, err := objectstore.VerifyFilesystemBackupFile(t.Context(), destination, root.URI, *metadata)
				require.NoError(t, err)
				require.NotEmpty(t, originalPayload)
				original, err := os.ReadFile(source)
				require.NoError(t, err)
				restored, err := os.ReadFile(destination)
				require.NoError(t, err)
				require.Equal(t, original, restored)
			}
			restoredRoots = append(restoredRoots, managedJourneyRestoredRoot{root: root, source: source, destination: destination, manifest: manifest})
		})
	}
	return restoredRoots
}
