package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyTypeSpecProjectSkipsLinkedDependencies(t *testing.T) {
	src, dst, deps := t.TempDir(), filepath.Join(t.TempDir(), "project"), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "main.tsp"), []byte("namespace Test;"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(deps, filepath.Join(src, "node_modules")); err != nil {
		t.Fatal(err)
	}
	if err := copyTypeSpecProject(src, dst, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "node_modules")); !os.IsNotExist(err) {
		t.Fatalf("local dependency link was copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "main.tsp")); err != nil {
		t.Fatal(err)
	}
}

func TestCopyTypeSpecProjectSkipsLocalScratchArtifacts(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(filepath.Join(src, ".tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".tmp", "browser-state.json"), []byte("local session"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.tsp"), []byte("namespace Test;"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyTypeSpecProject(src, dst, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".tmp")); !os.IsNotExist(err) {
		t.Fatalf("scratch artifacts copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "main.tsp")); err != nil {
		t.Fatal(err)
	}
}
