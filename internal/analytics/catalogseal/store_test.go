package catalogseal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestLocalObjectStoreCreateOnlyAndMetadata(t *testing.T) {
	store, err := NewLocalObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("catalog-bytes")
	identity := "sha256:" + strings.Repeat("a", 64)
	if err := store.Create(context.Background(), "catalogs/sha256/a.ducklake", bytes.NewReader(body), ObjectMetadata{MetadataDigest: identity, MetadataSize: "13"}); !errors.Is(err, ErrObjectCorrupt) {
		t.Fatalf("mismatched metadata error = %v", err)
	}
	// Use a reader-independent check by deriving the digest from Open after a
	// temporary valid create in a fresh store.
	store, err = NewLocalObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The exact digest is intentionally obtained by hashing bytes through a
	// tiny helper rather than hard-coding an algorithm-specific fixture.
	h := sha256.Sum256(body)
	got := "sha256:" + hex.EncodeToString(h[:])
	if err := store.Create(context.Background(), "catalogs/sha256/a.ducklake", bytes.NewReader(body), ObjectMetadata{MetadataDigest: got, MetadataSize: "13"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), "catalogs/sha256/a.ducklake", bytes.NewReader([]byte("other")), ObjectMetadata{MetadataDigest: got, MetadataSize: "13"}); !errors.Is(err, ErrObjectCorrupt) {
		t.Fatalf("overwrite error = %v", err)
	}
	object, err := store.Open(context.Background(), "catalogs/sha256/a.ducklake")
	if err != nil {
		t.Fatal(err)
	}
	read, err := io.ReadAll(object.Body)
	_ = object.Body.Close()
	if err != nil || !bytes.Equal(read, body) || object.Metadata[MetadataDigest] != got || object.Size != int64(len(body)) {
		t.Fatalf("readback body=%q metadata=%v size=%d err=%v", read, object.Metadata, object.Size, err)
	}
}

func TestLocalObjectStoreRejectsTraversal(t *testing.T) {
	store, err := NewLocalObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), "../escape", strings.NewReader("x"), ObjectMetadata{}); !errors.Is(err, ErrObjectUpload) {
		t.Fatalf("traversal error = %v", err)
	}
}
