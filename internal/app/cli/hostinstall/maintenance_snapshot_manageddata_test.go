//go:build linux

package hostinstall

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/storage"
	"github.com/flidai/leapview/internal/manageddata/storage/filesystem"
)

func TestColdSnapshotPreservesManagedDataRevisionLinks(t *testing.T) {
	root, volumes := snapshotFixture(t)
	// Managed revisions intentionally contain read-only directories.
	t.Cleanup(func() {
		_ = filepath.WalkDir(filepath.Dir(filepath.Dir(root)), func(path string, entry os.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				_ = os.Chmod(path, 0700)
			}
			return nil
		})
	})
	store, err := filesystem.New(filepath.Join(volumes["home"], "managed-data"))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("revenue\n42\n")
	blob := storage.Blob{SHA256: fmt.Sprintf("%x", sha256.Sum256(body)), Size: int64(len(body))}
	if _, err = store.Put(t.Context(), blob, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	manifest := manageddata.Manifest{Files: []manageddata.File{{Path: "financial.csv", SHA256: blob.SHA256, Size: blob.Size}}}
	lease, err := store.MaterializeRevision(t.Context(), manifest.RevisionID(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	digest, err := CaptureStoppedDirectories(t.Context(), root, "installation", volumes)
	if err != nil {
		t.Fatal(err)
	}
	for _, home := range []string{filepath.Join(root, "home"), filepath.Join(filepath.Dir(volumes["home"]), "restored")} {
		if home != filepath.Join(root, "home") {
			if err = RestoreStoppedDirectories(t.Context(), root, "installation", digest, map[string]string{
				"home": home, "postgres": filepath.Join(filepath.Dir(home), "restored-pg"),
			}); err != nil {
				t.Fatal(err)
			}
		}
		restored, err := filesystem.New(filepath.Join(home, "managed-data"))
		if err != nil {
			t.Fatal(err)
		}
		view, err := restored.MaterializeRevision(t.Context(), manifest.RevisionID(), manifest)
		if err != nil {
			t.Fatalf("managed-data revision cannot open after copy to %s: %v", home, err)
		}
		view.Release()
		originalInfo, err := os.Stat(store.BlobPath(blob.SHA256))
		if err != nil {
			t.Fatal(err)
		}
		copyInfo, err := os.Stat(restored.BlobPath(blob.SHA256))
		if err != nil {
			t.Fatal(err)
		}
		if os.SameFile(originalInfo, copyInfo) {
			t.Fatal("snapshot/restore shares a writable inode with live data")
		}
		if home != filepath.Join(root, "home") {
			relative, err := filepath.Rel(home, restored.BlobPath(blob.SHA256))
			if err != nil {
				t.Fatal(err)
			}
			backupInfo, err := os.Stat(filepath.Join(root, "home", relative))
			if err != nil {
				t.Fatal(err)
			}
			if os.SameFile(backupInfo, copyInfo) {
				t.Fatal("restored files share a writable inode with the immutable backup")
			}
		}
	}
}

func TestColdSnapshotRejectsBrokenHardLinkWithUnchangedBytes(t *testing.T) {
	root, volumes := snapshotFixture(t)
	if err := os.Link(filepath.Join(volumes["home"], "state"), filepath.Join(volumes["home"], "linked")); err != nil {
		t.Fatal(err)
	}
	digest, err := CaptureStoppedDirectories(t.Context(), root, "installation", volumes)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "home", "linked")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	if err = RestoreStoppedDirectories(t.Context(), root, "installation", digest, volumes); err == nil {
		t.Fatal("snapshot with broken hard-link identity was accepted")
	}
	live, _ := os.Stat(filepath.Join(volumes["home"], "state"))
	linked, _ := os.Stat(filepath.Join(volumes["home"], "linked"))
	if !os.SameFile(live, linked) {
		t.Fatal("failed verification changed the live files")
	}
}
