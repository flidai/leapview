package objectstore

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemBackupVerifiesActualEnvelopeAndRetainedMetadata(t *testing.T) {
	root := t.TempDir()
	data := []byte("authentic immutable serving payload")
	metadata := ObjectMetadata{StorageSecurityDomain: "managed-backup-domain", Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(data)), SizeBytes: int64(len(data)), ContentType: "application/gzip", MetadataDigest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("retained metadata")))}
	key := "serving-artifacts/" + metadata.Digest[7:] + ".tar.gz"
	store, err := NewFilesystemStore(FilesystemStoreConfig{Root: root, StorageSecurityDomain: metadata.StorageSecurityDomain})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutImmutable(t.Context(), key, bytes.NewReader(data), metadata); err != nil {
		t.Fatal(err)
	}
	relative, err := FilesystemBackupRelativePath(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, relative)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := VerifyFilesystemBackupFile(t.Context(), path, key, metadata); err != nil || info.Key != key || info.Digest != metadata.Digest {
		t.Fatalf("actual envelope verification: %v", err)
	}
	for name, change := range map[string]func(*ObjectMetadata){
		"domain":       func(m *ObjectMetadata) { m.StorageSecurityDomain = "foreign-domain" },
		"payload":      func(m *ObjectMetadata) { m.Digest = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("foreign"))) },
		"size":         func(m *ObjectMetadata) { m.SizeBytes++ },
		"content type": func(m *ObjectMetadata) { m.ContentType = "text/plain" },
		"metadata":     func(m *ObjectMetadata) { m.MetadataDigest = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("foreign"))) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := metadata
			change(&changed)
			if _, err := VerifyFilesystemBackupFile(t.Context(), path, key, changed); err == nil {
				t.Fatal("foreign retained metadata accepted")
			}
		})
	}
	if _, err := VerifyFilesystemBackupFile(t.Context(), path, "serving-artifacts/foreign.tar.gz", metadata); err == nil {
		t.Fatal("foreign logical key accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("verification changed original envelope")
	}
	link := filepath.Join(filepath.Dir(path), "linked.lvobj")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyFilesystemBackupFile(t.Context(), link, key, metadata); err == nil {
		t.Fatal("symlink envelope accepted")
	}
	for _, bad := range []string{"../escape", "/absolute", "serving-artifacts/../escape", ""} {
		if _, err := FilesystemBackupRelativePath(bad); err == nil {
			t.Fatalf("unsafe key accepted: %q", bad)
		}
	}
	before[len(before)-1] ^= 1
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyFilesystemBackupFile(t.Context(), path, key, metadata); err == nil {
		t.Fatal("corrupt payload envelope accepted")
	}
}
