package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/app/config"
)

func TestProfileImageBlobStoreUsesDedicatedLocalNamespace(t *testing.T) {
	home := t.TempDir()
	store, err := profileImageBlobStore(t.Context(), config.Config{HomeDir: home, ManagedDataBackend: "local"})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("avatar")
	digest := sha256.Sum256(body)
	blob := accessmodule.AvatarBlob{SHA256: hex.EncodeToString(digest[:]), Size: int64(len(body))}
	if _, err := store.Put(t.Context(), blob, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	reader, err := store.Open(t.Context(), blob.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	stored, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(stored, body) {
		t.Fatalf("stored avatar = %q, %v", stored, err)
	}
	if _, err := os.Stat(filepath.Join(home, "profile-images", "blobs", "sha256", blob.SHA256[:2], blob.SHA256)); err != nil {
		t.Fatalf("dedicated profile-image object: %v", err)
	}
}
