package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareInstallsOnceIntoExactRuntimeIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "extensions")
	installCalls := 0
	install := func(_ context.Context, receivedRoot, platform string) error {
		installCalls++
		path := filepath.Join(receivedRoot, "v1.5.4", platform, "ducklake.duckdb_extension")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("signed-fixture"), 0o600)
	}
	first, err := prepare(context.Background(), root, "v1.5.4", "linux_amd64_gcc4", install)
	if err != nil {
		t.Fatalf("prepare fixture: %v", err)
	}
	second, err := prepare(context.Background(), root, "v1.5.4", "linux_amd64_gcc4", install)
	if err != nil {
		t.Fatalf("reuse fixture: %v", err)
	}
	if installCalls != 1 || first != second {
		t.Fatalf("install calls = %d, paths = %q and %q", installCalls, first, second)
	}
}

func TestLocateArtifactRejectsSymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "extensions")
	path := filepath.Join(root, "v1.5.4", "linux_amd64_gcc4", "ducklake.duckdb_extension")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "ducklake.duckdb_extension")
	if err := os.WriteFile(target, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := locateArtifact(root, "v1.5.4", "linux_amd64_gcc4"); err == nil {
		t.Fatal("expected symlink artifact rejection")
	}
}
