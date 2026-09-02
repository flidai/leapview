package filesystem_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/storage"
	"github.com/flidai/leapview/internal/manageddata/storage/filesystem"
	"github.com/flidai/leapview/internal/manageddata/storage/storagetest"
)

func TestBlobStoreConformance(t *testing.T) {
	storagetest.BlobStoreConformance(t, func(t *testing.T) storage.BlobStore {
		store, err := filesystem.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return store
	})
}

func TestStoreUsesPrivatePermissionsAndContentAddressedPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "managed")
	store, err := filesystem.New(root)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("private")
	blob := testBlob(body)
	if _, err := store.Put(t.Context(), blob, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}

	path := store.BlobPath(blob.SHA256)
	if filepath.Dir(path) != filepath.Join(root, "blobs", "sha256", blob.SHA256[:2]) {
		t.Fatalf("BlobPath() = %q", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o400 {
		t.Fatalf("blob permissions = %o, want 400", got)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := rootInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("root permissions = %o, want 700", got)
	}
}

func TestBlobInventoryEnumeratesMetadataAndDeletesIdempotently(t *testing.T) {
	store, err := filesystem.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("inventory")
	blob := testBlob(body)
	if _, err := store.Put(t.Context(), blob, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	modified := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(store.BlobPath(blob.SHA256), modified, modified); err != nil {
		t.Fatal(err)
	}
	var metadata []storage.BlobMetadata
	if err := store.WalkBlobs(t.Context(), func(item storage.BlobMetadata) error {
		metadata = append(metadata, item)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(metadata) != 1 || metadata[0].SHA256 != blob.SHA256 || metadata[0].Size != blob.Size || !metadata[0].LastModified.Equal(modified) {
		t.Fatalf("WalkBlobs() = %#v", metadata)
	}
	if err := store.DeleteBlobs(t.Context(), []string{blob.SHA256}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteBlobs(t.Context(), []string{blob.SHA256}); err != nil {
		t.Fatalf("idempotent DeleteBlobs() = %v", err)
	}
}

func TestBlobInventoryRejectsNoncanonicalEntriesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	store, err := filesystem.New(root)
	if err != nil {
		t.Fatal(err)
	}
	inventoryRoot := filepath.Join(root, "blobs", "sha256")
	if err := os.WriteFile(filepath.Join(inventoryRoot, "unexpected"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.WalkBlobs(t.Context(), func(storage.BlobMetadata) error { return nil }); !errors.Is(err, storage.ErrIntegrity) {
		t.Fatalf("noncanonical WalkBlobs() error = %v", err)
	}
	if err := os.Remove(filepath.Join(inventoryRoot, "unexpected")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires optional Windows privileges")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(inventoryRoot, "aa")); err != nil {
		t.Fatal(err)
	}
	if err := store.WalkBlobs(t.Context(), func(storage.BlobMetadata) error { return nil }); !errors.Is(err, storage.ErrIntegrity) {
		t.Fatalf("symlink WalkBlobs() error = %v", err)
	}
}

func TestOpenRejectsBlobShardSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires optional Windows privileges")
	}
	root := t.TempDir()
	store, err := filesystem.New(root)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("trusted")
	blob := testBlob(body)
	if _, err := store.Put(t.Context(), blob, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}

	shard := filepath.Dir(store.BlobPath(blob.SHA256))
	if err := os.Chmod(shard, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.BlobPath(blob.SHA256)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(shard); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, blob.SHA256), []byte("attacker"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, shard); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Open(t.Context(), blob.SHA256); err == nil {
		t.Fatal("Open followed a blob shard symlink outside the store")
	}
}

func TestMaterializeRevisionCreatesImmutableHardLinkedView(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
	store, err := filesystem.New(root)
	if err != nil {
		t.Fatal(err)
	}
	one := testBlob([]byte("one"))
	two := testBlob([]byte("two"))
	for _, item := range []struct {
		blob storage.Blob
		body []byte
	}{{one, []byte("one")}, {two, []byte("two")}} {
		if _, err := store.Put(t.Context(), item.blob, bytes.NewReader(item.body)); err != nil {
			t.Fatal(err)
		}
	}

	manifest := manageddata.Manifest{Files: []manageddata.File{
		{Path: "orders/one.csv", SHA256: one.SHA256, Size: one.Size},
		{Path: "two.csv", SHA256: two.SHA256, Size: two.Size},
	}}
	first, err := store.MaterializeRevision(t.Context(), manifest.RevisionID(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.MaterializeRevision(t.Context(), manifest.RevisionID(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	if first.Root() != second.Root() {
		t.Fatalf("idempotent MaterializeRevision() roots = %q, %q", first.Root(), second.Root())
	}

	viewInfo, err := os.Stat(first.Root())
	if err != nil {
		t.Fatal(err)
	}
	if got := viewInfo.Mode().Perm(); got != 0o500 {
		t.Fatalf("revision permissions = %o, want 500", got)
	}
	sourceInfo, _ := os.Stat(store.BlobPath(one.SHA256))
	viewFileInfo, _ := os.Stat(filepath.Join(first.Root(), "orders", "one.csv"))
	if !os.SameFile(sourceInfo, viewFileInfo) {
		t.Fatal("revision file is not a hard link to its blob")
	}
	if got := viewFileInfo.Mode().Perm(); got != 0o400 {
		t.Fatalf("revision file permissions = %o, want 400", got)
	}

	if err := store.DeleteBlobs(t.Context(), []string{one.SHA256}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(first.Root(), "orders", "one.csv"))
	if err != nil || string(got) != "one" {
		t.Fatalf("hard-linked view after blob deletion = %q, %v", got, err)
	}
	third, err := store.MaterializeRevision(t.Context(), manifest.RevisionID(), manifest)
	if err != nil || third.Root() != first.Root() {
		t.Fatalf("MaterializeRevision() after blob deletion = %#v, %v", third, err)
	}
}

func TestMaterializeRevisionRejectsUnsafePathsAndMissingBlobs(t *testing.T) {
	store, err := filesystem.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	digest := testBlob([]byte("missing")).SHA256
	tests := []struct {
		name       string
		revisionID string
		path       string
		want       error
	}{
		{name: "revision traversal", revisionID: "../revision", path: "file.csv", want: storage.ErrInvalid},
		{name: "logical traversal", revisionID: "sha256:" + digest, path: "../file.csv", want: storage.ErrInvalid},
		{name: "missing blob", revisionID: "sha256:" + digest, path: "file.csv", want: storage.ErrNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := manageddata.Manifest{Files: []manageddata.File{{Path: test.path, SHA256: digest, Size: 7}}}
			revisionID := test.revisionID
			if revisionID == "sha256:"+digest {
				revisionID = manifest.RevisionID()
			}
			_, err := store.MaterializeRevision(t.Context(), revisionID, manifest)
			if !errors.Is(err, test.want) {
				t.Fatalf("MaterializeRevision() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestMaterializeRevisionRequiresCanonicalManagedRevisionID(t *testing.T) {
	store, err := filesystem.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	digest := testBlob([]byte("revision")).SHA256
	manifest := manageddata.Manifest{}
	for _, revisionID := range []string{
		digest,
		"sha256:" + strings.ToUpper(digest),
		"sha256:short",
		"md5:" + digest,
	} {
		t.Run(revisionID, func(t *testing.T) {
			_, err := store.MaterializeRevision(t.Context(), revisionID, manifest)
			if !errors.Is(err, storage.ErrInvalid) {
				t.Fatalf("MaterializeRevision(%q) error = %v", revisionID, err)
			}
		})
	}
}

func TestMaterializeRevisionRejectsCorruptExistingView(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
	store, err := filesystem.New(root)
	if err != nil {
		t.Fatal(err)
	}
	blob := testBlob([]byte("verified"))
	if _, err := store.Put(t.Context(), blob, bytes.NewReader([]byte("verified"))); err != nil {
		t.Fatal(err)
	}
	manifest := manageddata.Manifest{Files: []manageddata.File{{Path: "data.csv", SHA256: blob.SHA256, Size: blob.Size}}}
	lease, err := store.MaterializeRevision(t.Context(), manifest.RevisionID(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lease.Root(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(lease.Root(), "unexpected"), 0o500); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MaterializeRevision(t.Context(), manifest.RevisionID(), manifest); !errors.Is(err, storage.ErrIntegrity) {
		t.Fatalf("corrupt existing view error = %v, want %v", err, storage.ErrIntegrity)
	}
}

func testBlob(body []byte) storage.Blob {
	sum := sha256.Sum256(body)
	return storage.Blob{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
}
