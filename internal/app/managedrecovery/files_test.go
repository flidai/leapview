package managedrecovery

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRetainedContentManifestRejectsMutationAndSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ack.csv"), []byte("id,value\n1,42\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, err := CaptureFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := manifest.Digest()
	if err != nil || digest == "" {
		t.Fatalf("digest: %v", err)
	}
	if err := manifest.Verify(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ack.csv"), []byte("corrupted\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Verify(root); err == nil {
		t.Fatal("changed acknowledged bytes accepted")
	}
	if err := os.Remove(filepath.Join(root, "ack.csv")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "ack.csv")); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureFiles(root); err == nil {
		t.Fatal("symlink content captured")
	}
}

func TestRetainedContentManifestRejectsUnaccountedFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "one"), []byte("selected"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, err := CaptureFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "two"), []byte("after-frontier"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Verify(root); err == nil {
		t.Fatal("unaccounted after-frontier file accepted")
	}
	manifest.Files[0].Path = "../escape"
	if _, err := manifest.Digest(); err == nil {
		t.Fatal("escaping manifest path accepted")
	}
}
