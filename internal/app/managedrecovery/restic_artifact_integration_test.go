package managedrecovery

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/recoveryset"
)

func TestActualPinnedResticRestoresExactNativeArtifactFile(t *testing.T) {
	program := os.Getenv("LEAPVIEW_TEST_MANAGED_RESTIC")
	if program == "" {
		t.Skip("explicit pinned Restic integration inputs required")
	}
	base := t.TempDir()
	storage := filepath.Join(base, "objects")
	directory := filepath.Join(storage, "serving-artifacts")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("actual immutable native artifact bytes")
	digest := digestBytes(data)
	locator := "serving-artifacts/" + digest[7:] + ".tar.gz"
	source := filepath.Join(storage, locator)
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	manifest, err := CaptureFiles(directory)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest, err := manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	// A different artifact shares the directory and must not be selected.
	if err := os.WriteFile(filepath.Join(directory, "another.tar.gz"), []byte("other generation"), 0600); err != nil {
		t.Fatal(err)
	}
	password := filepath.Join(base, "password")
	if err := os.WriteFile(password, []byte("private integration password"), 0600); err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(base, "repository")
	run := func(args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(t.Context(), program, append([]string{"--no-cache", "--repo", repository, "--password-file", password}, args...)...)
		command.Env = []string{"PATH=/nonexistent", "LANG=C", "TZ=UTC"}
		output, err := command.Output()
		if err != nil {
			t.Fatal("actual pinned Restic operation failed")
		}
		return output
	}
	run("init")
	var snapshot string
	for _, line := range bytes.Split(run("backup", "--json", directory), []byte("\n")) {
		var result struct {
			Type string `json:"message_type"`
			ID   string `json:"snapshot_id"`
		}
		if json.Unmarshal(line, &result) == nil && result.Type == "summary" {
			snapshot = result.ID
		}
	}
	if len(snapshot) != 64 {
		t.Fatal("full actual snapshot missing")
	}
	destinationDirectory := filepath.Join(base, "replacement")
	if err := os.Mkdir(destinationDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	root := recoveryset.ObjectRoot{Kind: recoveryset.ObjectRootServingArtifact, URI: locator, Digest: digest, VersionID: snapshot, ProviderRecoveryFrontier: "restic:" + snapshot}
	config := ResticConfig{TargetID: "target", RecoverySetID: "set", Root: root, StorageRoot: storage, Restic: program, Repository: repository, PasswordFile: password, Destination: filepath.Join(destinationDirectory, filepath.Base(source)), Manifest: manifest, ManifestDigest: manifestDigest}
	restorer, err := NewRestic(config)
	if err != nil {
		t.Fatal(err)
	}
	request := providerrestore.ObjectRequest{TargetID: "target", RecoverySetID: "set", Root: root, IdempotencyKey: "exact-native-artifact-operation"}
	if _, err := restorer.RestoreObject(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(config.Destination)
	if err != nil || !bytes.Equal(restored, data) {
		t.Fatal("native artifact bytes differ")
	}
	if _, err := os.Lstat(filepath.Join(destinationDirectory, "another.tar.gz")); !os.IsNotExist(err) {
		t.Fatal("another generation exposed")
	}
	if _, err := restorer.RestoreObject(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}
