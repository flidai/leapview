package securefs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadCanonicalRegularFileRejectsTraversalAndSymlink(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "artifact.json")
	if err := os.WriteFile(path, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadCanonicalRegularFile(path); err != nil || string(got) != `{"ok":true}` {
		t.Fatalf("ReadCanonicalRegularFile() = %q, %v", got, err)
	}
	traversal := root + string(os.PathSeparator) + "sub" + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "artifact.json"
	if _, err := ReadCanonicalRegularFile(traversal); err == nil || !strings.Contains(err.Error(), "not canonical") {
		t.Fatalf("lexically traversing path accepted: %v", err)
	}

	outside := t.TempDir()
	outsidePath := filepath.Join(outside, "artifact.json")
	if err := os.WriteFile(outsidePath, []byte(`{"outside":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.json")
	if err := os.Symlink(outsidePath, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCanonicalRegularFile(link); err == nil || !strings.Contains(err.Error(), "contains a symlink") {
		t.Fatalf("symlink path accepted: %v", err)
	}
}

func TestCanonicalPathWithinRootRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "fragment.yaml")
	if err := os.WriteFile(target, []byte("visuals: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "fragment.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CanonicalPathWithinRoot(root, link); err == nil || !strings.Contains(err.Error(), "outside filesystem root") {
		t.Fatalf("symlink escape accepted: %v", err)
	}
}
