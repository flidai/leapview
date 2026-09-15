package module

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/manageddata/storage"
)

func TestContentBlobStoreUsesIsolatedLocalDirectory(t *testing.T) {
	store, err := NewContentBlobStore(t.Context(), ProductConfig{Backend: "local"}, t.TempDir(), "unused")
	if err != nil {
		t.Fatal(err)
	}
	value := []byte("product-logo")
	digest := sha256.Sum256(value)
	expected := ContentBlob{SHA256: hex.EncodeToString(digest[:]), Size: int64(len(value))}
	stored, err := store.PutContent(t.Context(), expected, bytes.NewReader(value))
	if err != nil || stored != expected {
		t.Fatalf("PutContent() = %#v, %v", stored, err)
	}
	reader, err := store.OpenContent(t.Context(), expected.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, value) {
		t.Fatalf("stored bytes = %q", got)
	}
	if _, err := store.OpenContent(t.Context(), strings.Repeat("a", 64)); !errors.Is(err, ErrContentBlobNotFound) {
		t.Fatalf("missing blob error = %v", err)
	}
}

func TestContentBlobStoreRejectsCorruptLocalContentBeforeReturningReader(t *testing.T) {
	root := t.TempDir()
	store, err := NewContentBlobStore(t.Context(), ProductConfig{Backend: "local"}, root, "unused")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("product-logo")
	digest := sha256.Sum256(body)
	expected := ContentBlob{SHA256: hex.EncodeToString(digest[:]), Size: int64(len(body))}
	if _, err := store.PutContent(t.Context(), expected, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	blobPath := filepath.Join(root, "blobs", "sha256", expected.SHA256[:2], expected.SHA256)
	if err := os.Chmod(blobPath, 0o600); err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), body...)
	corrupt[0] ^= 1
	if err := os.WriteFile(blobPath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blobPath, 0o400); err != nil {
		t.Fatal(err)
	}

	reader, err := store.OpenContent(t.Context(), expected.SHA256)
	if reader != nil {
		_ = reader.Close()
		t.Fatal("OpenContent returned a reader for corrupt content")
	}
	if !errors.Is(err, storage.ErrIntegrity) {
		t.Fatalf("OpenContent(corrupt) error = %v, want integrity error", err)
	}
}

func TestContentBlobStoreRejectsCorruptContentBeforeReturningReader(t *testing.T) {
	body := []byte("trusted logo")
	digest := sha256.Sum256(body)
	expected := ContentBlob{SHA256: hex.EncodeToString(digest[:]), Size: int64(len(body))}
	corrupt := append([]byte(nil), body...)
	corrupt[0] ^= 1
	store := contentBlobStore{blobs: &contentBlobTestStore{body: corrupt}}
	reader, err := store.OpenContent(t.Context(), expected.SHA256)
	if reader != nil {
		_ = reader.Close()
		t.Fatal("OpenContent returned a reader for corrupt content")
	}
	if !errors.Is(err, storage.ErrIntegrity) {
		t.Fatalf("OpenContent(corrupt) error = %v, want integrity error", err)
	}
}

func TestContentBlobStoreRejectsOversizedPut(t *testing.T) {
	body := bytes.Repeat([]byte{'x'}, int(maxContentBlobBytes+1))
	digest := sha256.Sum256(body)
	backend := &contentBlobTestStore{}
	store := contentBlobStore{blobs: backend}
	_, err := store.PutContent(t.Context(), ContentBlob{SHA256: hex.EncodeToString(digest[:]), Size: int64(len(body))}, bytes.NewReader(body))
	if !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("PutContent(oversized) error = %v, want invalid error", err)
	}
	if backend.puts != 0 {
		t.Fatalf("PutContent(oversized) called backend %d times", backend.puts)
	}
}

func TestContentBlobStoreRejectsOversizedRead(t *testing.T) {
	body := bytes.Repeat([]byte{'x'}, int(maxContentBlobBytes+1))
	digest := sha256.Sum256(body)
	store := contentBlobStore{blobs: &contentBlobTestStore{body: body}}
	reader, err := store.OpenContent(t.Context(), hex.EncodeToString(digest[:]))
	if reader != nil {
		_ = reader.Close()
		t.Fatal("OpenContent returned a reader for oversized content")
	}
	if !errors.Is(err, storage.ErrIntegrity) {
		t.Fatalf("OpenContent(oversized) error = %v, want integrity error", err)
	}
}

type contentBlobTestStore struct {
	body []byte
	puts int
}

func (s *contentBlobTestStore) Put(_ context.Context, expected storage.Blob, _ io.Reader) (storage.Blob, error) {
	s.puts++
	return expected, nil
}

func (s *contentBlobTestStore) Stat(_ context.Context, digest string) (storage.Blob, error) {
	return storage.Blob{SHA256: digest, Size: int64(len(s.body))}, nil
}

func (s *contentBlobTestStore) Open(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.body)), nil
}
