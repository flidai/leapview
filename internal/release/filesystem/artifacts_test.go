package filesystem

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func TestArtifactStoreSaveUploadWritesAtomically(t *testing.T) {
	store := NewArtifactStore(t.TempDir())

	size, err := store.SaveUpload(context.Background(), servingstate.ID("state_1"), &errAfterReader{data: []byte("bundle")})

	if err != nil {
		t.Fatalf("SaveUpload() error = %v", err)
	}
	if size != int64(len("bundle")) {
		t.Fatalf("size = %d, want %d", size, len("bundle"))
	}
	bytes, err := os.ReadFile(store.UploadPath("state_1"))
	if err != nil {
		t.Fatalf("read upload: %v", err)
	}
	if string(bytes) != "bundle" {
		t.Fatalf("upload = %q, want bundle", bytes)
	}
}

func TestArtifactStoreCreatesPrivateArtifactState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "artifacts")
	restoreUmask := setArtifactTestUmask(t, 0)
	store := NewArtifactStore(dir)
	if _, err := store.SaveUpload(context.Background(), servingstate.ID("state_1"), &errAfterReader{data: []byte("bundle")}); err != nil {
		restoreUmask()
		t.Fatalf("SaveUpload() error = %v", err)
	}
	artifact, err := store.PromoteUploaded(context.Background(), servingstate.ID("state_1"), "abc123", "{}")
	restoreUmask()
	if err != nil {
		t.Fatalf("PromoteUploaded() error = %v", err)
	}

	assertArtifactMode(t, dir, 0o700)
	assertArtifactMode(t, artifact.Path, 0o600)
}

func TestArtifactPromotionKeepsRecoverableUploadUntilValidationIsDurable(t *testing.T) {
	store := NewArtifactStore(t.TempDir())
	if _, err := store.SaveUpload(t.Context(), "state_1", &errAfterReader{data: []byte("bundle")}); err != nil {
		t.Fatal(err)
	}

	artifact, err := store.PromoteUploaded(t.Context(), "state_1", "abc123", "{}")
	if err != nil {
		t.Fatal(err)
	}
	if bytes, err := os.ReadFile(store.UploadPath("state_1")); err != nil || string(bytes) != "bundle" {
		t.Fatalf("recoverable upload after promotion = %q, %v", bytes, err)
	}
	if bytes, err := os.ReadFile(artifact.Path); err != nil || string(bytes) != "bundle" {
		t.Fatalf("promoted artifact = %q, %v", bytes, err)
	}
	if err := store.DiscardUploaded(t.Context(), "state_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.UploadPath("state_1")); !os.IsNotExist(err) {
		t.Fatalf("upload after durable validation stat error = %v, want not exist", err)
	}
}

func TestArtifactStoreSaveUploadCleansFailedUpload(t *testing.T) {
	dir := t.TempDir()
	store := NewArtifactStore(dir)
	wantErr := errors.New("read failed")

	size, err := store.SaveUpload(context.Background(), servingstate.ID("state_1"), &errAfterReader{data: []byte("partial"), err: wantErr})

	if !errors.Is(err, wantErr) {
		t.Fatalf("SaveUpload() error = %v, want %v", err, wantErr)
	}
	if size != int64(len("partial")) {
		t.Fatalf("size = %d, want copied partial size", size)
	}
	if _, statErr := os.Stat(store.UploadPath("state_1")); !os.IsNotExist(statErr) {
		t.Fatalf("upload path stat err = %v, want not exist", statErr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read artifact dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("artifact dir entries after failed upload = %#v", entryNames(entries))
	}
}

func TestArtifactStoreSaveUploadReplacesSymlinkWithoutFollowing(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	store := NewArtifactStore(dir)
	uploadPath := store.UploadPath("state_1")
	if err := os.Symlink(filepath.Join(outside, "outside.tar.gz"), uploadPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if _, err := store.SaveUpload(context.Background(), servingstate.ID("state_1"), &errAfterReader{data: []byte("bundle")}); err != nil {
		t.Fatalf("SaveUpload() error = %v", err)
	}

	info, err := os.Lstat(uploadPath)
	if err != nil {
		t.Fatalf("lstat upload: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("upload path is still a symlink")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "outside.tar.gz")); !os.IsNotExist(statErr) {
		t.Fatalf("outside file stat err = %v, want not exist", statErr)
	}
}

func TestArtifactStoreRejectsUnsafeServingStateID(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "..", "outside.upload.tar.gz")
	store := NewArtifactStore(dir)

	if _, err := store.SaveUpload(context.Background(), servingstate.ID("../outside"), &errAfterReader{data: []byte("bundle")}); err == nil {
		t.Fatal("SaveUpload() error = nil, want unsafe path component")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("outside upload stat err = %v, want not exist", err)
	}
}

func TestArtifactStoreRejectsWhitespacePathAliases(t *testing.T) {
	store := NewArtifactStore(t.TempDir())
	if _, err := store.SaveUpload(t.Context(), servingstate.ID(" state_1"), &errAfterReader{data: []byte("bundle")}); err == nil {
		t.Fatal("SaveUpload() error = nil, want non-canonical serving state id")
	}
	if _, err := store.SaveUpload(t.Context(), servingstate.ID("state_1 "), &errAfterReader{data: []byte("bundle")}); err == nil {
		t.Fatal("SaveUpload() error = nil, want non-canonical serving state id")
	}
}

func TestArtifactStoreRejectsUnsafePromoteDigest(t *testing.T) {
	dir := t.TempDir()
	store := NewArtifactStore(dir)
	if _, err := store.SaveUpload(context.Background(), servingstate.ID("state_1"), &errAfterReader{data: []byte("bundle")}); err != nil {
		t.Fatalf("SaveUpload() error = %v", err)
	}

	if _, err := store.PromoteUploaded(context.Background(), servingstate.ID("state_1"), "../escape", "{}"); err == nil {
		t.Fatal("PromoteUploaded() error = nil, want unsafe digest")
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "escape.tar.gz")); !os.IsNotExist(err) {
		t.Fatalf("escaped artifact stat err = %v, want not exist", err)
	}
}

func TestArtifactStoreRejectsWhitespaceDigestAlias(t *testing.T) {
	store := NewArtifactStore(t.TempDir())
	if _, err := store.SaveUpload(t.Context(), servingstate.ID("state_1"), &errAfterReader{data: []byte("bundle")}); err != nil {
		t.Fatalf("SaveUpload() error = %v", err)
	}

	if _, err := store.PromoteUploaded(t.Context(), servingstate.ID("state_1"), " abc123", "{}"); err == nil {
		t.Fatal("PromoteUploaded() error = nil, want non-canonical digest")
	}
}

type errAfterReader struct {
	data []byte
	err  error
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	if r.err != nil {
		return 0, r.err
	}
	return 0, io.EOF
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func assertArtifactMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %s = %#o, want %#o", path, got, want)
	}
}

func setArtifactTestUmask(t *testing.T, mask int) func() {
	t.Helper()
	old := syscall.Umask(mask)
	return func() {
		syscall.Umask(old)
	}
}
