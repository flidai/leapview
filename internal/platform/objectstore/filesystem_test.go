package objectstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFilesystemStoreRequiresDedicatedRootAndDomain(t *testing.T) {
	if _, err := NewFilesystemStore(FilesystemStoreConfig{Root: t.TempDir()}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty security domain error=%v, want ErrInvalid", err)
	}
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	linkedRoot := filepath.Join(link, "child")
	if _, err := NewFilesystemStore(FilesystemStoreConfig{Root: linkedRoot, StorageSecurityDomain: "domain-a"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("symlink-component root error=%v, want ErrInvalid", err)
	}
	if _, err := os.Stat(filepath.Join(target, "child")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink-component root created outside target: stat err=%v", err)
	}
	missingParentRoot := filepath.Join(parent, "missing-parent", "root")
	if _, err := NewFilesystemStore(FilesystemStoreConfig{Root: missingParentRoot, StorageSecurityDomain: "domain-a"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing-parent root error=%v, want ErrInvalid", err)
	}
	if _, err := os.Stat(filepath.Dir(missingParentRoot)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing-parent root unexpectedly created parent: stat err=%v", err)
	}
}

func TestFilesystemStoreReplayRestartAndList(t *testing.T) {
	root := t.TempDir()
	created := time.Date(2026, 8, 30, 12, 0, 0, 123, time.UTC)
	store, err := NewFilesystemStore(FilesystemStoreConfig{Root: root, StorageSecurityDomain: "domain-a", Now: func() time.Time { return created }})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("filesystem payload")
	metadata := metadataFor(body)
	first, err := store.PutImmutable(context.Background(), "objects/a", bytes.NewReader(body), metadata)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.PutImmutable(context.Background(), "objects/a", bytes.NewReader(body), metadata)
	if err != nil || replay != first {
		t.Fatalf("replay info=%+v err=%v first=%+v", replay, err, first)
	}
	// A newly constructed store must recover the exact creation timestamp.
	restarted, err := NewFilesystemStore(FilesystemStoreConfig{Root: root, StorageSecurityDomain: "domain-a"})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := restarted.Open(context.Background(), "objects/a")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(opened.Body)
	_ = opened.Body.Close()
	if err != nil || !bytes.Equal(got, body) || opened.Info != first {
		t.Fatalf("opened body=%q info=%+v err=%v want=%+v", got, opened.Info, err, first)
	}
	if _, err := restarted.PutImmutable(context.Background(), "objects/a", bytes.NewReader([]byte("other")), metadataFor([]byte("other"))); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict error=%v", err)
	}
	if _, err := restarted.PutImmutable(context.Background(), "objects/b", bytes.NewReader([]byte("b")), metadataFor([]byte("b"))); err != nil {
		t.Fatal(err)
	}
	page, cursor, err := restarted.List(context.Background(), "objects", "", 1)
	if err != nil || len(page) != 1 || page[0].Key != "objects/a" || cursor != "objects/a" {
		t.Fatalf("page=%+v cursor=%q err=%v", page, cursor, err)
	}
	page, cursor, err = restarted.List(context.Background(), "objects", cursor, 1)
	if err != nil || len(page) != 1 || page[0].Key != "objects/b" || cursor != "" {
		t.Fatalf("second page=%+v cursor=%q err=%v", page, cursor, err)
	}
}

func TestFilesystemStoreTamperAndSymlinkDefense(t *testing.T) {
	root := t.TempDir()
	store, err := NewFilesystemStore(FilesystemStoreConfig{Root: root, StorageSecurityDomain: "domain-a"})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("payload")
	if _, err := store.PutImmutable(context.Background(), "objects/a", bytes.NewReader(body), metadataFor(body)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "objects", "a"+filesystemObjectSuffix)
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("x"), 20); err != nil {
		file.Close()
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err := store.Open(context.Background(), "objects/a"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("tampered open error=%v", err)
	}

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	other := []byte("outside")
	if _, err := store.PutImmutable(context.Background(), "escape/a", bytes.NewReader(other), metadataFor(other)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("symlink put error=%v", err)
	}
	// Listing fails closed rather than treating a symlink as an object.
	listRoot := t.TempDir()
	listStore, err := NewFilesystemStore(FilesystemStoreConfig{Root: listRoot, StorageSecurityDomain: "domain-a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "not-an-object"), filepath.Join(listRoot, "escape.lvobj")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := listStore.List(context.Background(), "", "", 10); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("symlink list error=%v", err)
	}

	// Replacing the configured root with a symlink is rejected on every
	// operation, preventing a stale store handle from escaping its root.
	backup := root + ".backup"
	if err := os.Rename(root, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []func() error{
		func() error {
			_, err := store.PutImmutable(context.Background(), "objects/new", bytes.NewReader(other), metadataFor(other))
			return err
		},
		func() error { _, err := store.Open(context.Background(), "objects/a"); return err },
		func() error { _, _, err := store.List(context.Background(), "", "", 10); return err },
		func() error { return store.Delete(context.Background(), "objects/a") },
	} {
		if err := operation(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("replaced-root operation error=%v", err)
		}
	}
}

func TestFilesystemStoreConcurrentCreateAndDelete(t *testing.T) {
	root := t.TempDir()
	store, err := NewFilesystemStore(FilesystemStoreConfig{Root: root, StorageSecurityDomain: "domain-a"})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(strings.Repeat("x", 4096))
	metadata := metadataFor(body)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.PutImmutable(context.Background(), "objects/race", bytes.NewReader(body), metadata)
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent exact create error=%v", err)
	}
	// Independent store instances still race safely through the filesystem's
	// atomic no-overwrite publication, and every exact replay succeeds.
	stores := make([]*FilesystemStore, 4)
	for i := range stores {
		stores[i], err = NewFilesystemStore(FilesystemStoreConfig{Root: root, StorageSecurityDomain: "domain-a"})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := range stores {
		if i == 0 {
			errs = make(chan error, len(stores))
		}
		wg.Add(1)
		go func(store *FilesystemStore) {
			defer wg.Done()
			if _, err := store.PutImmutable(context.Background(), "objects/cross-store", bytes.NewReader(body), metadata); err != nil {
				errs <- err
			}
		}(stores[i])
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("cross-store exact create error=%v", err)
	}
	if err := store.Delete(context.Background(), "objects/race"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), "objects/race"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete error=%v", err)
	}
}
