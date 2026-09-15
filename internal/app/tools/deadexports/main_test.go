package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReviewedEntryMatchesDeclaration(t *testing.T) {
	entry := reviewedEntry{File: "internal/example/example.go", Kind: "method", Name: "Serve", Receiver: "Handler"}
	declaration := declaration{File: entry.File, Kind: entry.Kind, Name: entry.Name, Receiver: entry.Receiver}
	if _, ok := matchingEntry([]reviewedEntry{entry}, declaration); !ok {
		t.Fatal("reviewed entry did not match its declaration")
	}
}

func TestDeclarationKeyIncludesReceiver(t *testing.T) {
	first := declaration{File: "example.go", Kind: "method", Name: "Run", Receiver: "First"}
	second := first
	second.Receiver = "Second"
	if declarationKey(first) == declarationKey(second) {
		t.Fatal("methods on different receivers must not share an allowlist key")
	}
}

func TestMissingGeneratedInputsFailsClosed(t *testing.T) {
	root := t.TempDir()
	missing := missingGeneratedInputs(root)
	if len(missing) == 0 {
		t.Fatal("guard must reject a checkout without generated reference inputs")
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(missing[0]))); !os.IsNotExist(err) {
		t.Fatalf("test fixture unexpectedly contains generated input %q", missing[0])
	}
	for _, path := range generatedInputSentinels {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("generated"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if complete := missingGeneratedInputs(root); len(complete) != 0 {
		t.Fatalf("complete generated fixture reported missing inputs: %v", complete)
	}
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(generatedInputSentinels[0]))); err != nil {
		t.Fatal(err)
	}
	if complete := missingGeneratedInputs(root); len(complete) == 0 {
		t.Fatal("removing a generated input must fail closed")
	}
}

func TestScannerMetadataExcludesGuardSources(t *testing.T) {
	for _, path := range []string{".quality/dead-exports.json", "internal/app/tools/deadexports/main.go"} {
		if !scannerMetadata(path) {
			t.Fatalf("scanner metadata path %q was included as a source reference", path)
		}
	}
	if scannerMetadata("docs/README.md") {
		t.Fatal("ordinary documentation must remain in the reference corpus")
	}
}
