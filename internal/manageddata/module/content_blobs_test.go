package module

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
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
	got, _ := io.ReadAll(reader)
	_ = reader.Close()
	if !bytes.Equal(got, value) {
		t.Fatalf("stored bytes = %q", got)
	}
	if _, err := store.OpenContent(t.Context(), strings.Repeat("a", 64)); !errors.Is(err, ErrContentBlobNotFound) {
		t.Fatalf("missing blob error = %v", err)
	}
}
