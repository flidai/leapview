package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/app/tools/internal/bootstrap"
)

func TestTargetDirRequiresExplicitOutput(t *testing.T) {
	if _, err := bootstrap.TargetDir(""); err == nil || !strings.Contains(err.Error(), "out is required") {
		t.Fatalf("targetDir empty output error = %v, want required error", err)
	}
}

func TestTargetDirResolvesExplicitOutput(t *testing.T) {
	want := filepath.Join(t.TempDir(), "movielens")
	got, err := bootstrap.TargetDir(want)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("targetDir = %q, want %q", got, want)
	}
}

func TestMissingFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, expectedFiles[0].Name), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	missing := missingFiles(dir)
	if len(missing) != len(expectedFiles)-1 {
		t.Fatalf("missingFiles returned %d entries, want %d", len(missing), len(expectedFiles)-1)
	}
	for _, got := range missing {
		if got == expectedFiles[0].Name {
			t.Fatalf("missingFiles included existing file %s", got)
		}
	}
}

func TestTruthiness(t *testing.T) {
	truthyValues := []string{"1", "true", "TRUE", " yes "}
	for _, value := range truthyValues {
		if !bootstrap.Truthy(value) {
			t.Fatalf("truthy(%q) = false, want true", value)
		}
	}

	falseValues := []string{"", "0", "false", "no", "y"}
	for _, value := range falseValues {
		if bootstrap.Truthy(value) {
			t.Fatalf("truthy(%q) = true, want false", value)
		}
	}
}

func TestExtractExpectedFiles(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "ml-32m.zip")
	writeZip(t, archivePath, append(expectedFileNames(), "extra.csv"))

	target := t.TempDir()
	copied, err := extractExpectedFiles(archivePath, target)
	if err != nil {
		t.Fatal(err)
	}
	if copied != len(expectedFiles) {
		t.Fatalf("copied %d files, want %d", copied, len(expectedFiles))
	}

	for _, file := range expectedFiles {
		data, err := os.ReadFile(filepath.Join(target, file.Name))
		if err != nil {
			t.Fatalf("read extracted %s: %v", file.Name, err)
		}
		if got := strings.TrimSpace(string(data)); got != file.Name {
			t.Fatalf("extracted %s contains %q, want %q", file.Name, got, file.Name)
		}
	}
	if _, err := os.Stat(filepath.Join(target, "extra.csv")); !os.IsNotExist(err) {
		t.Fatalf("extra.csv should not be extracted, stat err: %v", err)
	}
}

func TestExtractExpectedFilesSupportsOfficialNestedEntries(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "ml-32m.zip")
	var nested []string
	for _, name := range expectedFileNames() {
		nested = append(nested, "ml-32m/"+name)
	}
	writeZip(t, archivePath, nested)

	target := t.TempDir()
	copied, err := extractExpectedFiles(archivePath, target)
	if err != nil {
		t.Fatal(err)
	}
	if copied != len(expectedFiles) {
		t.Fatalf("copied %d files, want %d", copied, len(expectedFiles))
	}
	for _, file := range expectedFiles {
		if _, err := os.Stat(filepath.Join(target, file.Name)); err != nil {
			t.Fatalf("expected nested %s to extract to target: %v", file.Name, err)
		}
	}
}

func TestExtractExpectedFilesReportsMissingEntries(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "ml-32m.zip")
	names := expectedFileNames()
	writeZip(t, archivePath, names[:len(names)-1])

	_, err := extractExpectedFiles(archivePath, t.TempDir())
	if err == nil {
		t.Fatal("extractExpectedFiles returned nil error for incomplete archive")
	}
	if !strings.Contains(err.Error(), names[len(names)-1]) {
		t.Fatalf("error %q does not name missing CSV %s", err, names[len(names)-1])
	}
}

func TestVerifyExpectedFileChecksumsReportsMismatch(t *testing.T) {
	dir := t.TempDir()
	for _, file := range expectedFiles {
		if err := os.WriteFile(filepath.Join(dir, file.Name), []byte("bad\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	err := verifyExpectedFileChecksums(dir)
	if err == nil {
		t.Fatal("verifyExpectedFileChecksums returned nil error for bad checksums")
	}
	if !strings.Contains(err.Error(), expectedFiles[0].Name) {
		t.Fatalf("checksum error %q does not name mismatched file %s", err, expectedFiles[0].Name)
	}
}

func expectedFileNames() []string {
	names := make([]string, 0, len(expectedFiles))
	for _, file := range expectedFiles {
		names = append(names, file.Name)
	}
	return names
}

func writeZip(t *testing.T, archivePath string, filenames []string) {
	t.Helper()

	out, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(out)
	for _, filename := range filenames {
		entry, err := writer.Create(filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(filepath.Base(filename) + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}
