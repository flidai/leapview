package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteGeneratedFileRetainsOpenReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config_gen.go")
	prefix := "package config\n\nvar "
	previous := []byte(prefix + "Previous = 1\n")
	if err := os.WriteFile(path, previous, 0o644); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	observed := make([]byte, len(prefix))
	if _, err := io.ReadFull(reader, observed); err != nil {
		t.Fatal(err)
	}
	replacement := []byte("package config\n")
	if err := writeGeneratedFile(path, replacement); err != nil {
		t.Fatal(err)
	}
	remainder, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	observed = append(observed, remainder...)
	if _, err := parser.ParseFile(token.NewFileSet(), path, observed, 0); err != nil {
		t.Fatalf("regeneration corrupted an open source reader: %v", err)
	}
	if !bytes.Equal(observed, previous) {
		t.Fatalf("open reader saw %q, want original complete source %q", observed, previous)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, replacement) {
		t.Fatalf("new reader saw %q, want replacement %q", current, replacement)
	}
}

func TestWriteGeneratedFileCreatesCompleteArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generated", "config_gen.go")
	content := []byte("package config\n")
	if err := writeGeneratedFile(path, content); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, content) {
		t.Fatalf("generated artifact = %q, want %q", actual, content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("generated permissions = %o, want 644", info.Mode().Perm())
	}
	assertOnlyArtifact(t, filepath.Dir(path), filepath.Base(path))
}

func TestWriteGeneratedFilePreservesUnchangedArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config_gen.go")
	content := []byte("package config\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	previousTime := time.Unix(1, 0)
	if err := os.Chtimes(path, previousTime, previousTime); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeGeneratedFile(path, content); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("unchanged artifact was rewritten")
	}
	assertOnlyArtifact(t, filepath.Dir(path), filepath.Base(path))
}

func TestWriteGeneratedFileCleansUpFailedPublication(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config_gen.go")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	// Renaming a staged file onto this existing directory must fail without
	// disturbing it or leaving the unpublished temporary artifact behind.
	sentinel := filepath.Join(path, "retained")
	if err := os.WriteFile(sentinel, []byte("previous content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeGeneratedFile(path, []byte("package config\n")); err == nil {
		t.Fatal("publication over an existing directory succeeded")
	}
	actual, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != "previous content" {
		t.Fatalf("failed publication changed existing content: %q", actual)
	}
	assertOnlyArtifact(t, directory, filepath.Base(path))
}

func assertOnlyArtifact(t *testing.T, directory, name string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != name {
		t.Fatalf("expected only %s after publication, found %v", name, entries)
	}
}
