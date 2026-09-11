package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTargetDirResolvesAndRequiresOutput(t *testing.T) {
	t.Parallel()

	if _, err := TargetDir("   "); err == nil {
		t.Fatal("TargetDir should reject an empty output path")
	}

	got, err := TargetDir("./datasets")
	if err != nil {
		t.Fatalf("TargetDir returned error: %v", err)
	}
	want, err := filepath.Abs("./datasets")
	if err != nil {
		t.Fatalf("resolve expected path: %v", err)
	}
	if got != want {
		t.Fatalf("TargetDir = %q, want %q", got, want)
	}
}

func TestCacheDirHonorsConfiguredRoot(t *testing.T) {
	t.Setenv("LEAPVIEW_BOOTSTRAP_CACHE_DIR", filepath.Join("tmp", "leapview-cache"))

	got, err := CacheDir("ignored")
	if err != nil {
		t.Fatalf("CacheDir returned error: %v", err)
	}
	want, err := filepath.Abs(filepath.Join("tmp", "leapview-cache"))
	if err != nil {
		t.Fatalf("resolve expected cache path: %v", err)
	}
	if got != want {
		t.Fatalf("CacheDir = %q, want %q", got, want)
	}
}

func TestFileExistsRejectsDirectories(t *testing.T) {
	directory := t.TempDir()
	file := filepath.Join(directory, "asset.csv")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if !FileExists(file) {
		t.Fatal("FileExists should recognize a regular file")
	}
	if FileExists(directory) {
		t.Fatal("FileExists should reject a directory")
	}
}

func TestTruthyAcceptsDocumentedValues(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"1", "true", "TRUE", " yes "} {
		if !Truthy(value) {
			t.Errorf("Truthy(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", "0", "false", "no", "on"} {
		if Truthy(value) {
			t.Errorf("Truthy(%q) = true, want false", value)
		}
	}
}
