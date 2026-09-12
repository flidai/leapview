package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/app/tools/internal/bootstrap"
)

func TestVerifyArchiveDigest(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "archive.zip")
	if err := os.WriteFile(archivePath, []byte("pinned dataset"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := verifyArchiveDigest(archivePath, "efb61cc996a59d8620addb6690351af1e3b9ae3bb138cd248f67220708c81825"); err != nil {
		t.Fatalf("verifyArchiveDigest valid archive: %v", err)
	}
	if err := verifyArchiveDigest(archivePath, strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("verifyArchiveDigest mismatch error = %v, want digest mismatch", err)
	}
}

func TestTargetDirRequiresExplicitOutput(t *testing.T) {
	if _, err := bootstrap.TargetDir(""); err == nil || !strings.Contains(err.Error(), "out is required") {
		t.Fatalf("targetDir empty output error = %v, want required error", err)
	}
}

func TestTargetDirResolvesExplicitOutput(t *testing.T) {
	want := filepath.Join(t.TempDir(), "olist")
	got, err := bootstrap.TargetDir(want)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("targetDir = %q, want %q", got, want)
	}
}

func TestMissingCSVs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, expectedCSVs[0]), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	missing := missingCSVs(dir)
	if len(missing) != len(expectedCSVs)-1 {
		t.Fatalf("missingCSVs returned %d entries, want %d", len(missing), len(expectedCSVs)-1)
	}
	for _, got := range missing {
		if got == expectedCSVs[0] {
			t.Fatalf("missingCSVs included existing file %s", got)
		}
	}
}

func TestExpectedCSVsIncludeRealGeographicInputs(t *testing.T) {
	want := map[string]bool{
		"olist_geolocation_dataset.csv": false,
		"olist_sellers_dataset.csv":     false,
	}
	for _, filename := range expectedCSVs {
		if _, ok := want[filename]; ok {
			want[filename] = true
		}
	}
	for filename, present := range want {
		if !present {
			t.Errorf("expectedCSVs missing %s", filename)
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

func TestExtractExpectedCSVs(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "olist.zip")
	writeZip(t, archivePath, append(expectedCSVs, "extra.csv"))

	target := t.TempDir()
	copied, err := extractExpectedCSVs(archivePath, target)
	if err != nil {
		t.Fatal(err)
	}
	if copied != len(expectedCSVs) {
		t.Fatalf("copied %d files, want %d", copied, len(expectedCSVs))
	}

	for _, filename := range expectedCSVs {
		data, err := os.ReadFile(filepath.Join(target, filename))
		if err != nil {
			t.Fatalf("read extracted %s: %v", filename, err)
		}
		if got := strings.TrimSpace(string(data)); got != filename {
			t.Fatalf("extracted %s contains %q, want %q", filename, got, filename)
		}
	}
	if _, err := os.Stat(filepath.Join(target, "extra.csv")); !os.IsNotExist(err) {
		t.Fatalf("extra.csv should not be extracted, stat err: %v", err)
	}
}

func TestExtractExpectedCSVsIgnoresNestedEntries(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "olist.zip")
	writeZip(t, archivePath, []string{"nested/" + expectedCSVs[0]})

	_, err := extractExpectedCSVs(archivePath, t.TempDir())
	if err == nil {
		t.Fatal("extractExpectedCSVs returned nil error for archive without top-level CSVs")
	}
	if !strings.Contains(err.Error(), expectedCSVs[0]) {
		t.Fatalf("error %q does not name missing top-level CSV %s", err, expectedCSVs[0])
	}
}

func TestExtractExpectedCSVsReportsMissingEntries(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "olist.zip")
	writeZip(t, archivePath, expectedCSVs[:len(expectedCSVs)-1])

	_, err := extractExpectedCSVs(archivePath, t.TempDir())
	if err == nil {
		t.Fatal("extractExpectedCSVs returned nil error for incomplete archive")
	}
	if !strings.Contains(err.Error(), expectedCSVs[len(expectedCSVs)-1]) {
		t.Fatalf("error %q does not name missing CSV %s", err, expectedCSVs[len(expectedCSVs)-1])
	}
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
		if _, err := entry.Write([]byte(filename + "\n")); err != nil {
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
