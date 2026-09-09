package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyFileDigest(t *testing.T) {
	file := filepath.Join(t.TempDir(), fileName)
	data := []byte("pinned financial workbook")
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%x", sha256.Sum256(data))
	if err := verifyFileDigest(file, want); err != nil {
		t.Fatalf("verifyFileDigest valid file: %v", err)
	}
	if err := verifyFileDigest(file, strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("verifyFileDigest mismatch error = %v, want digest mismatch", err)
	}
}

func TestDownloadFile(t *testing.T) {
	payload := []byte("workbook")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "LeapView bootstrap" {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	destination := filepath.Join(t.TempDir(), fileName)
	if err := downloadFileFrom(server.Client(), destination, server.URL); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("downloaded payload = %q, want %q", got, payload)
	}
}

func TestXLSXColumnIndex(t *testing.T) {
	for reference, want := range map[string]int{"A1": 0, "P701": 15, "AA2": 26} {
		got, err := xlsxColumnIndex(reference)
		if err != nil {
			t.Fatalf("xlsxColumnIndex(%q): %v", reference, err)
		}
		if got != want {
			t.Fatalf("xlsxColumnIndex(%q) = %d, want %d", reference, got, want)
		}
	}
	if _, err := xlsxColumnIndex("42"); err == nil {
		t.Fatal("xlsxColumnIndex accepted a reference without a column")
	}
}

func TestTargetDirRequiresExplicitOutput(t *testing.T) {
	if _, err := targetDir(""); err == nil || !strings.Contains(err.Error(), "out is required") {
		t.Fatalf("targetDir empty output error = %v, want required error", err)
	}
}

func TestTruthiness(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", " yes "} {
		if !truthy(value) {
			t.Fatalf("truthy(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", "0", "false", "no"} {
		if truthy(value) {
			t.Fatalf("truthy(%q) = true, want false", value)
		}
	}
}
